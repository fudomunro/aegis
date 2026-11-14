package admin

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/shared"
)

// HandleAdminServiceSync edits one sync relationship (issue #365):
// POST /v1/admin/services/{id}/sync — designate (or unset) this source as
// the canonical home of one kind's sync group. The write lands in
// canonical_for like every other hub designation, so the hub reassigns
// across the group and both views stay in step.
func (h *Handlers) HandleAdminServiceSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/admin/services/"), "/")
	id = strings.TrimSuffix(id, "/sync")
	if id == "" {
		shared.WriteJSONError(w, http.StatusBadRequest, "service id is required")
		return
	}
	body, ok := shared.ReadBodyLimited(w, r, shared.MaxAdminBodyBytes)
	if !ok {
		return
	}
	var req struct {
		Kind       string `json:"kind"`
		Canonical  bool   `json:"canonical"`
		Projection string `json:"projection"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			shared.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}
	if strings.TrimSpace(req.Kind) == "" {
		shared.WriteJSONError(w, http.StatusBadRequest, "kind is required")
		return
	}
	rel := shared.SyncRelationship{Kind: req.Kind, Canonical: req.Canonical, Projection: req.Projection}
	if err := h.Host.SetSyncRelationship(id, rel); err != nil {
		h.Host.Logger(r, "admin_service_sync").
			Warn("failed to update sync relationship", "service", id, "kind", req.Kind, "error", err)
		shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.Host.InvalidateCaches()
	h.Host.Logger(r, "admin_service_sync").
		Info("sync relationship updated", "service", id, "kind", req.Kind, "canonical", req.Canonical)
	svc, ok := h.Host.ServiceRow(id)
	if !ok {
		shared.WriteJSONError(w, http.StatusNotFound, "unknown service "+id)
		return
	}
	shared.WriteJSON(w, map[string]any{"status": "saved", "service": svc})
}
