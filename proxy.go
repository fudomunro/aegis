package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// HandleServiceProxy proxies requests to a configured service's upstream endpoint.
// Route pattern: /v1/services/{service_id} or /v1/services/{service_id}/{path...}
func (s *Server) HandleServiceProxy(w http.ResponseWriter, r *http.Request) {
	// Extract service ID and remaining path.
	// Route is /v1/services/{id} or /v1/services/{id}/...
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/v1/services/"), "/", 2)
	serviceID := parts[0]
	remainingPath := ""
	if len(parts) > 1 {
		remainingPath = "/" + parts[1]
	}

	logger := withPeerLog(slog.With("service", serviceID, "method", r.Method, "path", remainingPath), r)

	svc := s.config.GetService(serviceID)
	if svc == nil {
		logger.Warn("service not found")
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("service %q not configured", serviceID))
		return
	}

	// Handle /capabilities requests — return connector metadata filtered by permissions.
	if remainingPath == "/capabilities" {
		if connector, ok := s.connectors[serviceID]; ok {
			caps := FilterCapabilities(connector.Capabilities(), svc.Permissions)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(caps)
		} else {
			writeJSONError(w, http.StatusNotFound, fmt.Sprintf("service %q has no connector", serviceID))
		}
		return
	}

	dataType := DataTypeForPath(remainingPath)

	if svc.Action == "block" {
		logger.Warn("service blocked", "reason", svc.Description)
		s.recordAccess(AccessEvent{
			Agent:    agentForRequest(r),
			Surface:  MetricSurfaceService,
			Target:   serviceID,
			DataType: dataType,
		})
		writeJSONError(w, http.StatusForbidden, fmt.Sprintf("access to %s is not allowed: %s", svc.Name, svc.Description))
		return
	}

	// Enforce data-type permissions before any upstream or connector work.
	if !s.AllowDataPermission(w, r, svc, remainingPath) {
		logger.Warn("permission denied",
			"data_type", dataType,
			"permission", svc.Permissions.For(dataType),
		)
		s.recordAccess(AccessEvent{
			Agent:    agentForRequest(r),
			Surface:  MetricSurfaceService,
			Target:   serviceID,
			DataType: dataType,
		})
		return
	}

	// If a connector is registered for this service, delegate to it.
	if connector, ok := s.connectors[serviceID]; ok {
		connector.HandleProxy(w, r, remainingPath)
		s.recordProxyAccess(r, serviceID, dataType, w.Header().Get("X-Guardian-Cache"))
		return
	}

	if svc.Endpoint == "" {
		logger.Warn("no upstream endpoint configured")
		writeJSONError(w, http.StatusBadGateway, fmt.Sprintf("service %q has no upstream endpoint", serviceID))
		return
	}

	// Build the upstream URL: service endpoint + remaining path + original query string.
	upstreamURL := strings.TrimRight(svc.Endpoint, "/") + remainingPath
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}

	// For GET requests, check cache first.
	cacheKey := serviceID + ":" + r.URL.RequestURI()
	if r.Method == http.MethodGet {
		if cached, ok := s.cache.Get(cacheKey); ok {
			logger.Debug("cache hit")
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Guardian-Cache", "hit")
			io.WriteString(w, cached)
			s.recordProxyAccess(r, serviceID, dataType, MetricCacheHit)
			return
		}
	}

	// Forward the request to the upstream service.
	upstreamReq, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, r.Body)
	if err != nil {
		logger.Error("failed to create upstream request", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to create upstream request")
		return
	}

	// Copy original headers.
	for k, vv := range r.Header {
		for _, v := range vv {
			upstreamReq.Header.Add(k, v)
		}
	}

	// Apply service-specific headers (may override originals).
	for k, v := range svc.Headers {
		upstreamReq.Header.Set(k, v)
	}

	// If we have a stored OAuth token for this service, use it.
	// This overrides any static Authorization header from config.
	if tok := s.getValidToken(serviceID, logger); tok != "" {
		upstreamReq.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := http.DefaultClient.Do(upstreamReq)
	if err != nil {
		logger.Error("upstream request failed", "error", err)
		writeJSONError(w, http.StatusBadGateway, fmt.Sprintf("upstream request to %s failed", svc.Name))
		return
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Error("failed to read upstream response", "error", err)
		writeJSONError(w, http.StatusBadGateway, "failed to read upstream response")
		return
	}

	logger.Debug("upstream response", "status", resp.StatusCode, "bytes", len(data))

	// Cache GET responses.
	if r.Method == http.MethodGet {
		ttl := svc.CacheTTL.Duration
		if ttl == 0 {
			ttl = 5 * time.Minute
		}
		s.cache.Set(cacheKey, string(data), ttl)
	}

	// Forward the response.
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	if ct := resp.Header.Get("Content-Type"); ct == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.Header().Set("X-Guardian-Cache", "miss")
	w.WriteHeader(resp.StatusCode)
	w.Write(data)
	s.recordProxyAccess(r, serviceID, dataType, MetricCacheMiss)
}

// recordProxyAccess records a redacted service-proxy access event (issue #32).
func (s *Server) recordProxyAccess(r *http.Request, serviceID, dataType, cache string) {
	switch cache {
	case "hit":
		cache = MetricCacheHit
	case "miss":
		cache = MetricCacheMiss
	default:
		cache = MetricCacheNone
	}
	s.recordAccess(AccessEvent{
		Agent:    agentForRequest(r),
		Surface:  MetricSurfaceService,
		Target:   serviceID,
		DataType: dataType,
		Cache:    cache,
	})
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// getValidToken returns a valid access token for the service's provider
// connection, refreshing if needed. Returns "" when there is no token or the
// connection lacks scopes required by this surface (issue #26).
func (s *Server) getValidToken(serviceID string, logger *slog.Logger) string {
	if s.tokens == nil {
		return ""
	}

	connectionID := s.config.connectionIDForService(serviceID)
	tok := s.tokens.Get(connectionID)
	if tok == nil {
		return ""
	}

	if !scopesSatisfy(tok.Scopes, s.config.requiredScopes(serviceID)) {
		logger.Warn("OAuth connection missing required scopes for surface",
			"service", serviceID,
			"connection", connectionID,
			"required", s.config.requiredScopes(serviceID),
			"granted", tok.Scopes,
		)
		return ""
	}

	if !tok.IsExpired(30 * time.Second) {
		return tok.AccessToken
	}

	// Token is expired or about to expire — try to refresh.
	if tok.RefreshToken == "" {
		logger.Warn("OAuth token expired, no refresh token", "service", serviceID, "connection", connectionID)
		return ""
	}

	logger.Info("refreshing expired OAuth token", "service", serviceID, "connection", connectionID)
	refreshed, err := s.RefreshToken(connectionID)
	if err != nil {
		logger.Error("token refresh failed", "service", serviceID, "connection", connectionID, "error", err)
		return tok.AccessToken // try with expired token as fallback
	}

	if !scopesSatisfy(refreshed.Scopes, s.config.requiredScopes(serviceID)) {
		return ""
	}

	return refreshed.AccessToken
}
