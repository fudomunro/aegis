package core

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/core/apps"
	"github.com/fudomunro/aegis/internal/shared"
)

// HandleAdminApps manages third-party application credentials (issue #131).
//
//	GET    /v1/admin/apps
//	POST   /v1/admin/apps              {"id","name","scopes"} → {app, token}
//	GET    /v1/admin/apps/{id}
//	PATCH  /v1/admin/apps/{id}         {"name","scopes","disabled"}
//	POST   /v1/admin/apps/{id}/rotate  → {app, token}
//	DELETE /v1/admin/apps/{id}
//
// Plaintext tokens are returned only from create and rotate.
func (s *Server) HandleAdminApps(w http.ResponseWriter, r *http.Request) {
	if s.apps == nil {
		shared.WriteJSONError(w, http.StatusServiceUnavailable, "app registry disabled")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/admin/apps")
	path = strings.Trim(path, "/")

	if path == "" {
		switch r.Method {
		case http.MethodGet:
			shared.WriteJSON(w, map[string]any{"apps": s.apps.List()})
		case http.MethodPost:
			body, ok := shared.ReadBodyLimited(w, r, maxAdminBodyBytes)
			if !ok {
				return
			}
			var req apps.AppCreateRequest
			if err := json.Unmarshal(body, &req); err != nil {
				shared.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
			view, token, err := s.apps.Create(req)
			if err != nil {
				withPeerLog(slog.With("handler", "admin_apps"), r).
					Warn("failed to create app", "app_id", req.ID, "error", err)
				shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
			s.invalidateCaches()
			withPeerLog(slog.With("handler", "admin_apps"), r).
				Info("third-party app created", "app_id", view.ID, "scopes", view.Scopes)
			shared.WriteJSON(w, map[string]any{
				"app":   view,
				"token": token,
				"note":  "Store this token now — it is not shown again.",
			})
		default:
			shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	// path is "{id}" or "{id}/rotate"
	id := path
	rotate := false
	if strings.HasSuffix(path, "/rotate") {
		rotate = true
		id = strings.TrimSuffix(path, "/rotate")
		id = strings.Trim(id, "/")
	}
	if id == "" || strings.Contains(id, "/") {
		shared.WriteJSONError(w, http.StatusNotFound, "unknown admin endpoint")
		return
	}

	switch {
	case rotate:
		if r.Method != http.MethodPost {
			shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		view, token, err := s.apps.Rotate(id)
		if err != nil {
			shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.invalidateCaches()
		withPeerLog(slog.With("handler", "admin_apps"), r).
			Info("third-party app token rotated", "app_id", id)
		shared.WriteJSON(w, map[string]any{
			"app":   view,
			"token": token,
			"note":  "Store this token now — it is not shown again. The previous token is invalid.",
		})
	case r.Method == http.MethodGet:
		view, ok := s.apps.Get(id)
		if !ok {
			shared.WriteJSONError(w, http.StatusNotFound, fmt.Sprintf("app %q not found", id))
			return
		}
		shared.WriteJSON(w, map[string]any{"app": view})
	case r.Method == http.MethodPatch || r.Method == http.MethodPut:
		body, ok := shared.ReadBodyLimited(w, r, maxAdminBodyBytes)
		if !ok {
			return
		}
		var req apps.AppUpdateRequest
		if err := json.Unmarshal(body, &req); err != nil {
			shared.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		view, err := s.apps.Update(id, req)
		if err != nil {
			shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.invalidateCaches()
		withPeerLog(slog.With("handler", "admin_apps"), r).
			Info("third-party app updated", "app_id", id, "disabled", view.Disabled)
		shared.WriteJSON(w, map[string]any{"app": view, "status": "saved"})
	case r.Method == http.MethodDelete:
		if err := s.apps.Delete(id); err != nil {
			shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.invalidateCaches()
		withPeerLog(slog.With("handler", "admin_apps"), r).
			Info("third-party app deleted", "app_id", id)
		shared.WriteJSON(w, map[string]string{"status": "deleted", "id": id})
	default:
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
