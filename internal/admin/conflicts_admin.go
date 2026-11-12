package admin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/shared"
	"github.com/fudomunro/aegis/internal/syncjobs"
)

// Admin conflict surface (issue #365 phase 3). The library page lists the
// detected non-hub edits and offers accept (make the member's edit
// canonical, then fan out) or reject (re-converge that member). Admin-only
// like the rest of /v1/admin — agent credentials never reach these writes.
//
// No issue numbers in user-facing copy: strings below stay clean; refs
// live in comments only.

// HandleAdminSyncConflicts serves the conflict queue:
// GET  /v1/admin/sync/conflicts[?status=open][&kind=playlists]
// POST /v1/admin/sync/conflicts/{id}/accept | /reject
func (h *Handlers) HandleAdminSyncConflicts(w http.ResponseWriter, r *http.Request) {
	if h.SyncConflicts == nil || !h.SyncConflicts.Enabled() {
		shared.WriteJSONError(w, http.StatusServiceUnavailable, "sync conflicts disabled")
		return
	}
	switch r.Method {
	case http.MethodGet:
		status := strings.TrimSpace(r.URL.Query().Get("status"))
		if status == "" {
			status = syncjobs.ConflictStatusOpen
		}
		shared.WriteJSON(w, map[string]any{
			"conflicts": h.SyncConflicts.List(syncjobs.ConflictFilter{
				Status: status,
				Kind:   strings.TrimSpace(r.URL.Query().Get("kind")),
			}),
		})
	case http.MethodPost:
		path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/admin/sync/conflicts/"), "/")
		accept := false
		switch {
		case strings.HasSuffix(path, "/accept"):
			path = strings.TrimSuffix(path, "/accept")
			accept = true
		case strings.HasSuffix(path, "/reject"):
			path = strings.TrimSuffix(path, "/reject")
		default:
			shared.WriteJSONError(w, http.StatusNotFound, "unknown sync conflict action")
			return
		}
		id := strings.TrimSpace(path)
		if id == "" {
			shared.WriteJSONError(w, http.StatusBadRequest, "conflict id required")
			return
		}
		if h.Conflicts == nil {
			shared.WriteJSONError(w, http.StatusServiceUnavailable, "sync conflicts disabled")
			return
		}
		rec, err := h.Conflicts.ResolveConflict(r.Context(), id, accept)
		if err != nil {
			if errors.Is(err, syncjobs.ErrSyncTerminal) {
				shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
			shared.WriteJSONError(w, http.StatusBadGateway, err.Error())
			return
		}
		shared.WriteJSON(w, rec)
	default:
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
