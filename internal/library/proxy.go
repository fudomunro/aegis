package library

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

// HandleServiceProxy is the /v1/services/{id}/{path} transport. After issue
// #119 it is content/ingest only (photo bytes, full message bodies, playlist
// items, local import) — not a general provider-API mirror. Library is the
// data surface.
// Route pattern: /v1/services/{service_id} or /v1/services/{service_id}/{path...}
func (h *Handlers) HandleServiceProxy(w http.ResponseWriter, r *http.Request) {
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
	canonical, err := CanonicalProxyPath(remainingPath)
	if err != nil {
		shared.WriteJSONError(w, http.StatusBadRequest, "invalid request path")
		return
	}
	remainingPath = canonical

	logger := h.Host.Logger(r, "service_proxy").With(
		"service", serviceID, "method", r.Method, "path", remainingPath)

	svc, ok := h.Host.Service(serviceID)
	if !ok {
		logger.Warn("service not found")
		shared.WriteJSONError(w, http.StatusNotFound, fmt.Sprintf("service %q not configured", serviceID))
		return
	}
	// Data-scope isolation: foreign partitions look missing, never 403
	// (docs/data-scopes.md).
	if !shared.ServiceVisible(svc.Dataset, shared.DataScopeFrom(r)) {
		logger.Warn("service not found", "reason", "dataset partition")
		shared.WriteJSONError(w, http.StatusNotFound, fmt.Sprintf("service %q not configured", serviceID))
		return
	}

	// Issue #119: /v1/services is content/ingest transport only. Library is
	// the data surface; provider-shaped lists, playback, search, and generic
	// pass-through are not agent API.
	if !IsAgentContentPath(remainingPath) {
		logger.Warn("service path rejected: not content/ingest", "path", remainingPath)
		h.Host.RecordAccess(r, kinds.AccessEvent{
			Agent:    h.Host.Agent(r),
			Surface:  kinds.SurfaceService,
			Target:   serviceID,
			Kind:     KindForPath(remainingPath),
			Decision: kinds.DecisionDeny,
			Reason:   "not_content_path",
			Method:   r.Method,
			Status:   http.StatusNotFound,
		})
		shared.WriteJSONError(w, http.StatusNotFound,
			"service paths are content/ingest only (photo bytes, full message bodies, playlist items, local import); use /v1/library/{kind} for user data")
		return
	}

	kind := KindForPath(remainingPath)
	accessLevel := h.RequestLevel(r, kind)

	if svc.Action == "block" {
		logger.Warn("service blocked", "reason", svc.Description)
		h.Host.RecordAccess(r, kinds.AccessEvent{
			Agent:    h.Host.Agent(r),
			Surface:  kinds.SurfaceService,
			Target:   serviceID,
			Kind:     kind,
			Decision: kinds.DecisionDeny,
			Reason:   "service_blocked",
			Method:   r.Method,
			Status:   http.StatusForbidden,
		})
		shared.WriteJSONError(w, http.StatusForbidden, fmt.Sprintf("access to %s is not allowed: %s", svc.Name, svc.Description))
		return
	}

	// Enforce kind permissions before any upstream or connector work.
	if !h.AllowDataPermission(w, r, svc, remainingPath) {
		logger.Warn("data grant denied",
			"data_type", kind,
			"permission", accessLevel,
		)
		h.Host.RecordAccess(r, kinds.AccessEvent{
			Agent:    h.Host.Agent(r),
			Surface:  kinds.SurfaceService,
			Target:   serviceID,
			Kind:     kind,
			Decision: kinds.DecisionDeny,
			Reason:   "permission",
			Method:   r.Method,
			Status:   http.StatusForbidden,
		})
		return
	}

	// Content/ingest transport is connector-owned. There is no generic
	// provider pass-through (issue #119) — Aegis does not mirror service APIs.
	connector, ok := h.Connectors[serviceID]
	if !ok {
		logger.Warn("no connector for content path")
		shared.WriteJSONError(w, http.StatusNotFound,
			fmt.Sprintf("service %q has no connector for content/ingest", serviceID))
		return
	}
	connector.HandleProxy(w, r, remainingPath)
	h.recordProxyAccess(r, serviceID, kind, w.Header().Get("X-Aegis-Cache"))
}

// recordProxyAccess records a redacted service-proxy access event (issue #32).
func (h *Handlers) recordProxyAccess(r *http.Request, serviceID, kind, cache string) {
	switch cache {
	case "hit":
		cache = kinds.CacheHit
	case "miss":
		cache = kinds.CacheMiss
	default:
		cache = ""
	}
	h.Host.RecordAccess(r, kinds.AccessEvent{
		Agent:   h.Host.Agent(r),
		Surface: kinds.SurfaceService,
		Target:  serviceID,
		Kind:    kind,
		Cache:   cache,
		Method:  r.Method,
	})
}
