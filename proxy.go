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

	logger := slog.With("service", serviceID, "method", r.Method, "path", remainingPath)

	svc := s.config.GetService(serviceID)
	if svc == nil {
		logger.Warn("service not found")
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("service %q not configured", serviceID))
		return
	}

	if svc.Action == "block" {
		logger.Warn("service blocked", "reason", svc.Description)
		writeJSONError(w, http.StatusForbidden, fmt.Sprintf("access to %s is not allowed: %s", svc.Name, svc.Description))
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
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
