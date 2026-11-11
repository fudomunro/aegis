package admin

import (
	"net/http"

	"github.com/fudomunro/aegis/internal/syncjobs"

	"github.com/fudomunro/aegis/internal/shared"
)

// HandleAdminMaintenanceSignals reports (GET) or captures (POST) the signal
// bundle. Admin-scoped like the rest of /v1/admin — the bundle names services
// and failure modes and must not leak to agent credentials (issue #181).
func (h *Handlers) HandleAdminMaintenanceSignals(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		shared.WriteJSON(w, h.Maintenance.CollectMaintenanceSignals())
	case http.MethodPost:
		if h.DataDir == "" {
			shared.WriteJSONError(w, http.StatusInternalServerError, "data directory not configured")
			return
		}
		sig := h.Maintenance.CollectMaintenanceSignals()
		path, err := h.Maintenance.WriteMaintenanceSignals(sig)
		if err != nil {
			shared.WriteJSONError(w, http.StatusInternalServerError, "capture failed: "+err.Error())
			return
		}
		shared.WriteJSON(w, syncjobs.MaintenanceCaptureResult{Path: path, Signals: sig})
	default:
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
