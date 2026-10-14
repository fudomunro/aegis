package connectors

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

// connectorProxyOpts is the connector-specific parts of the shared proxy path.
type ProxyOpts struct {
	serviceID   string
	displayName string
	defaultBase string
	defaultTTL  time.Duration
	// mapUpstream optionally rewrites the client path/query to the provider API.
	// Nil means use remainingPath + original query as-is.
	mapUpstream func(path string, query url.Values) (upstreamPath string, upstreamQuery url.Values, err error)
	// expandPages optionally merges additional pages into the raw first response
	// before transform. Runs only for successful GETs.
	expandPages func(ctx context.Context, path string, query url.Values, raw map[string]any, token string, logger *slog.Logger) map[string]any
	// transform maps raw upstream bytes to the LLM-facing response shape.
	transform func(path string, data []byte, logger *slog.Logger) any
}

// runConnectorProxy is the shared HandleProxy pipeline for OAuth connectors:
// token check → cache → upstream → 401/429 → optional pagination → transform → cache-on-200.
func RunProxy(s Host, w http.ResponseWriter, r *http.Request, remainingPath string, opts ProxyOpts) {
	logger := slog.With("service", opts.serviceID, "method", r.Method, "path", remainingPath)

	token := s.GetValidToken(opts.serviceID, logger)
	if token == "" {
		WriteJSONError(w, http.StatusUnauthorized, "no valid "+opts.displayName+" token — authorize at /v1/auth/"+opts.serviceID+"/login")
		return
	}

	upstreamPath := remainingPath
	query := r.URL.Query()
	if opts.mapUpstream != nil {
		mappedPath, mappedQuery, err := opts.mapUpstream(remainingPath, query.Clone())
		if err != nil {
			WriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		upstreamPath = mappedPath
		if mappedQuery != nil {
			query = mappedQuery
		}
	}

	upstreamBase := s.UpstreamBaseFor(opts.serviceID, opts.defaultBase)
	upstreamURL := upstreamBase + upstreamPath
	if encoded := query.Encode(); encoded != "" {
		upstreamURL += "?" + encoded
	}

	cacheKey := opts.serviceID + ":" + r.URL.RequestURI()
	if r.Method == http.MethodGet {
		if cached, ok := s.CacheGet(cacheKey); ok {
			logger.Debug("cache hit")
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Aegis-Cache", "hit")
			io.WriteString(w, cached)
			return
		}
	}

	upstreamStart := time.Now()
	data, status, err := DoUpstreamRequest(r.Context(), r.Method, upstreamURL, r.Body, token)
	upstreamElapsed := time.Since(upstreamStart)
	s.ObserveUpstream(opts.serviceID, upstreamElapsed, StatusOrZero(status, err))
	if err != nil {
		logger.Error(opts.serviceID+" request failed", "error", err)
		WriteJSONError(w, http.StatusBadGateway, opts.displayName+" API request failed")
		return
	}
	logger.Debug(opts.serviceID+" response", "status", status, "bytes", len(data))

	if status == http.StatusUnauthorized {
		logger.Warn(opts.serviceID + " returned 401, token may be revoked")
		WriteJSONError(w, http.StatusUnauthorized, opts.displayName+" token is invalid or expired — re-authorize at /v1/auth/"+opts.serviceID+"/login")
		return
	}
	if status == http.StatusTooManyRequests {
		logger.Warn(opts.serviceID + " rate limited")
		w.Header().Set("Retry-After", "30")
		WriteJSONError(w, http.StatusTooManyRequests, opts.displayName+" rate limit exceeded — retry later")
		return
	}

	if opts.expandPages != nil && r.Method == http.MethodGet {
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err == nil {
			raw = opts.expandPages(r.Context(), remainingPath, query, raw, token, logger)
			if rem, err := json.Marshal(raw); err == nil {
				data = rem
			}
		}
	}

	transformed := opts.transform(remainingPath, data, logger)
	result, err := json.Marshal(transformed)
	if err != nil {
		logger.Error("failed to marshal transformed response", "error", err)
		WriteJSONError(w, http.StatusInternalServerError, "response transformation failed")
		return
	}

	if r.Method == http.MethodGet && status == http.StatusOK {
		ttl := opts.defaultTTL
		if ttl == 0 {
			ttl = 60 * time.Second
		}
		if svc := s.ServiceConfig(opts.serviceID); svc != nil && svc.CacheTTL > 0 {
			ttl = svc.CacheTTL
		}
		s.CacheSet(cacheKey, string(result), ttl)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Aegis-Cache", "miss")
	w.WriteHeader(status)
	w.Write(result)
}

func StatusOrZero(status int, err error) int {
	if err != nil {
		return 0
	}
	return status
}
