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

// ProxyOpts is the connector-specific parts of the shared proxy path.
type ProxyOpts struct {
	serviceID   string
	displayName string
	defaultBase string
	defaultTTL  time.Duration
	// token resolves the upstream credential — static headers or an API key,
	// not only OAuth. Nil means Host.GetValidToken(serviceID).
	token func(logger *slog.Logger) string
	// missingToken is the client copy when no credential resolves. Empty
	// means the OAuth authorize hint.
	missingToken string
	// rejectToken is the client copy when upstream rejects the credential.
	// Empty means the OAuth reconnect hint.
	rejectToken string
	// rejectForbidden treats upstream 403 as credential rejection alongside
	// 401. OAuth providers answer 403 for insufficient scope and that must
	// stay visible, so this is opt-in for API-key services.
	rejectForbidden bool
	// mapUpstream optionally rewrites the client path/query to the provider API.
	// Nil means use remainingPath + original query as-is.
	mapUpstream func(path string, query url.Values) (upstreamPath string, upstreamQuery url.Values, err error)
	// expandPages optionally merges additional pages into the raw first response
	// before transform. Runs only for successful GETs.
	expandPages func(ctx context.Context, path string, query url.Values, raw map[string]any, token string, logger *slog.Logger) map[string]any
	// transform maps raw upstream bytes to the LLM-facing response shape.
	// Nil passes the upstream bytes through unchanged; a transform that
	// returns nil does the same, for payloads a JSON re-marshal would reject
	// (upstream error pages, exports).
	transform func(method, path string, data []byte, logger *slog.Logger) any
}

// RunProxy is the shared HandleProxy pipeline for connectors that speak a
// JSON provider API: token check → cache → upstream → credential/rate-limit
// handling → optional pagination → transform → cache-on-200.
//
// Credential source, failure copy, and response shape are per-connector
// (ProxyOpts); everything else lives here so the rules — cache GET + 200 only,
// map upstream 401 to "reconnect in admin", fail-open on transform errors —
// cannot drift between connectors.
func RunProxy(s Host, w http.ResponseWriter, r *http.Request, remainingPath string, opts ProxyOpts) {
	logger := slog.With("service", opts.serviceID, "method", r.Method, "path", remainingPath)

	cred := ""
	if opts.token != nil {
		cred = opts.token(logger)
	} else {
		cred = s.GetValidToken(opts.serviceID, logger)
	}
	if cred == "" {
		msg := opts.missingToken
		if msg == "" {
			msg = "no valid " + opts.displayName + " token — authorize at /v1/auth/" + opts.serviceID + "/login"
		}
		WriteJSONError(w, http.StatusUnauthorized, msg)
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
	data, status, err := DoUpstreamRequest(r.Context(), r.Method, upstreamURL, r.Body, cred)
	upstreamElapsed := time.Since(upstreamStart)
	s.ObserveUpstream(opts.serviceID, upstreamElapsed, StatusOrZero(status, err))
	if err != nil {
		logger.Error(opts.serviceID+" request failed", "error", err)
		WriteJSONError(w, http.StatusBadGateway, opts.displayName+" API request failed")
		return
	}
	logger.Debug(opts.serviceID+" response", "status", status, "bytes", len(data))

	if status == http.StatusUnauthorized || (opts.rejectForbidden && status == http.StatusForbidden) {
		logger.Warn(opts.serviceID + " rejected the credential")
		msg := opts.rejectToken
		if msg == "" {
			msg = opts.displayName + " token is invalid or expired — re-authorize at /v1/auth/" + opts.serviceID + "/login"
		}
		WriteJSONError(w, http.StatusUnauthorized, msg)
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
			raw = opts.expandPages(r.Context(), remainingPath, query, raw, cred, logger)
			if rem, err := json.Marshal(raw); err == nil {
				data = rem
			}
		}
	}

	result := data
	if opts.transform != nil {
		if transformed := opts.transform(r.Method, remainingPath, data, logger); transformed != nil {
			marshaled, err := json.Marshal(transformed)
			if err != nil {
				logger.Error("failed to marshal transformed response", "error", err)
				WriteJSONError(w, http.StatusInternalServerError, "response transformation failed")
				return
			}
			result = marshaled
		}
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

// StreamProxy is the streaming variant of RunProxy: forward the request with
// a bearer token and copy the upstream response back unchanged. Use it for
// HandleProxy tails that must move bytes through — full bodies, exports,
// generic provider paths — where a cache entry and a neutral model would be
// the wrong shape. Upstream statuses pass through unmapped; resolving the
// credential and naming it in copy stays with the connector, since the tail
// runs only after the connector's own routing and auth handling.
//
// The agent-facing surface is already gated by types.IsAgentContentPath, so
// this does not re-classify paths. It carries the request rules that were
// previously copied per connector: bearer token plus the caller's
// Content-Type, and nothing else.
func StreamProxy(w http.ResponseWriter, r *http.Request, upstreamBase, remainingPath, token, displayName string, logger *slog.Logger) {
	upstreamURL := upstreamBase + remainingPath
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, r.Body)
	if err != nil {
		logger.Error("failed to build upstream request", "error", err)
		WriteJSONError(w, http.StatusInternalServerError, "failed to build request")
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	resp, err := HTTPClient.Do(req)
	if err != nil {
		logger.Error("upstream request failed", "error", err)
		WriteJSONError(w, http.StatusBadGateway, displayName+" request failed")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func StatusOrZero(status int, err error) int {
	if err != nil {
		return 0
	}
	return status
}
