package core

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

// HandleServiceProxy is the /v1/services/{id}/{path} transport. After issue
// #119 it is content/ingest only (photo bytes, full message bodies, playlist
// items, local import) — not a general provider-API mirror. Library is the
// data surface.
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

	// Canonicalize once so permission classification, connector routing, and
	// the upstream URL cannot diverge (issue #94). Non-canonical paths 400.
	canonical, err := types.CanonicalProxyPath(remainingPath)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request path")
		return
	}
	remainingPath = canonical

	logger := withPeerLog(slog.With("service", serviceID, "method", r.Method, "path", remainingPath), r)

	svc := s.config.GetService(serviceID)
	if svc == nil {
		logger.Warn("service not found")
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("service %q not configured", serviceID))
		return
	}

	// Issue #119: /v1/services is content/ingest transport only. Library is
	// the data surface; provider-shaped lists, playback, search, and generic
	// pass-through are not agent API.
	if !types.IsAgentContentPath(remainingPath) {
		logger.Warn("service path rejected: not content/ingest", "path", remainingPath)
		s.recordAccess(AccessEvent{
			Agent:    agentForRequest(r),
			Surface:  MetricSurfaceService,
			Target:   serviceID,
			DataType: types.DataTypeForPath(remainingPath),
			Decision: DecisionDeny,
			Reason:   "not_content_path",
			Method:   r.Method,
			Status:   http.StatusNotFound,
		})
		writeJSONError(w, http.StatusNotFound,
			"service paths are content/ingest only (photo bytes, full message bodies, playlist items, local import); use /v1/library/{kind} for user data")
		return
	}

	dataType := types.DataTypeForPath(remainingPath)
	accessLevel := s.effectivePermissions(svc).AccessForDataType(dataType)

	if svc.Action == "block" {
		logger.Warn("service blocked", "reason", svc.Description)
		s.recordAccess(AccessEvent{
			Agent:    agentForRequest(r),
			Surface:  MetricSurfaceService,
			Target:   serviceID,
			DataType: dataType,
			Decision: DecisionDeny,
			Reason:   "service_blocked",
			Method:   r.Method,
			Status:   http.StatusForbidden,
		})
		writeJSONError(w, http.StatusForbidden, fmt.Sprintf("access to %s is not allowed: %s", svc.Name, svc.Description))
		return
	}

	// Enforce data-type permissions before any upstream or connector work.
	if !s.AllowDataPermission(w, r, svc, remainingPath) {
		logger.Warn("data grant denied",
			"data_type", dataType,
			"permission", accessLevel,
		)
		s.recordAccess(AccessEvent{
			Agent:    agentForRequest(r),
			Surface:  MetricSurfaceService,
			Target:   serviceID,
			DataType: dataType,
			Decision: DecisionDeny,
			Reason:   "permission",
			Method:   r.Method,
			Status:   http.StatusForbidden,
		})
		return
	}

	// Content/ingest transport is connector-owned. There is no generic
	// provider pass-through (issue #119) — Aegis does not mirror service APIs.
	connector, ok := s.connectors[serviceID]
	if !ok {
		logger.Warn("no connector for content path")
		writeJSONError(w, http.StatusNotFound,
			fmt.Sprintf("service %q has no connector for content/ingest", serviceID))
		return
	}
	connector.HandleProxy(w, r, remainingPath)
	s.recordProxyAccess(r, serviceID, dataType, w.Header().Get("X-Aegis-Cache"))
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
		Method:   r.Method,
	})
}

// statusCodeOrZero returns the upstream status, or 0 when the request failed
// before a response arrived (issue #105 latency metrics).
func statusCodeOrZero(resp *http.Response, err error) int {
	if err != nil || resp == nil {
		return 0
	}
	return resp.StatusCode
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// GetValidToken returns a valid access token for the service's provider
// connection, refreshing if needed. Returns "" when there is no token or the
// connection lacks scopes required by this surface (issue #26).
func (s *Server) GetValidToken(serviceID string, logger *slog.Logger) string {
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
