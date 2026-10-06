package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
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
		writeJSONError(w, http.StatusServiceUnavailable, "app registry disabled")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/admin/apps")
	path = strings.Trim(path, "/")

	if path == "" {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, map[string]any{"apps": s.apps.List()})
		case http.MethodPost:
			body, ok := readBodyLimited(w, r, maxAdminBodyBytes)
			if !ok {
				return
			}
			var req AppCreateRequest
			if err := json.Unmarshal(body, &req); err != nil {
				writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
			view, token, err := s.apps.Create(req)
			if err != nil {
				withPeerLog(slog.With("handler", "admin_apps"), r).
					Warn("failed to create app", "app_id", req.ID, "error", err)
				writeJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
			s.cache.Clear()
			withPeerLog(slog.With("handler", "admin_apps"), r).
				Info("third-party app created", "app_id", view.ID, "scopes", view.Scopes)
			writeJSON(w, map[string]any{
				"app":   view,
				"token": token,
				"note":  "Store this token now — it is not shown again.",
			})
		default:
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
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
		writeJSONError(w, http.StatusNotFound, "unknown admin endpoint")
		return
	}

	switch {
	case rotate:
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		view, token, err := s.apps.Rotate(id)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.cache.Clear()
		withPeerLog(slog.With("handler", "admin_apps"), r).
			Info("third-party app token rotated", "app_id", id)
		writeJSON(w, map[string]any{
			"app":   view,
			"token": token,
			"note":  "Store this token now — it is not shown again. The previous token is invalid.",
		})
	case r.Method == http.MethodGet:
		view, ok := s.apps.Get(id)
		if !ok {
			writeJSONError(w, http.StatusNotFound, fmt.Sprintf("app %q not found", id))
			return
		}
		writeJSON(w, map[string]any{"app": view})
	case r.Method == http.MethodPatch || r.Method == http.MethodPut:
		body, ok := readBodyLimited(w, r, maxAdminBodyBytes)
		if !ok {
			return
		}
		var req AppUpdateRequest
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		view, err := s.apps.Update(id, req)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.cache.Clear()
		withPeerLog(slog.With("handler", "admin_apps"), r).
			Info("third-party app updated", "app_id", id, "disabled", view.Disabled)
		writeJSON(w, map[string]any{"app": view, "status": "saved"})
	case r.Method == http.MethodDelete:
		if err := s.apps.Delete(id); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.cache.Clear()
		withPeerLog(slog.With("handler", "admin_apps"), r).
			Info("third-party app deleted", "app_id", id)
		writeJSON(w, map[string]string{"status": "deleted", "id": id})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
