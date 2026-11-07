package core

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/shared"
)

// HandleAdminDataScopes manages invite tokens — the data-scope credentials
// external principals hold (issue #253).
//
//	GET    /v1/admin/data-scopes
//	POST   /v1/admin/data-scopes             {"id","name","dataset","grants","read_only"} → {scope, invite_token}
//	GET    /v1/admin/data-scopes/{id}
//	PATCH  /v1/admin/data-scopes/{id}        {"name","grants","read_only","disabled"}
//	POST   /v1/admin/data-scopes/{id}/rotate → {scope, invite_token}
//
// Plaintext invite tokens are returned only from create and rotate; only the
// SHA-256 hash is stored.
func (s *Server) HandleAdminDataScopes(w http.ResponseWriter, r *http.Request) {
	if s.scopes == nil {
		shared.WriteJSONError(w, http.StatusServiceUnavailable, "data scopes disabled")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/admin/data-scopes")
	path = strings.Trim(path, "/")

	if path == "" {
		switch r.Method {
		case http.MethodGet:
			shared.WriteJSON(w, map[string]any{"data_scopes": s.scopes.List()})
		case http.MethodPost:
			body, ok := shared.ReadBodyLimited(w, r, maxAdminBodyBytes)
			if !ok {
				return
			}
			var req DataScopeCreateRequest
			if err := json.Unmarshal(body, &req); err != nil {
				shared.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
			view, token, err := s.scopes.Create(req)
			if err != nil {
				withPeerLog(slog.With("handler", "admin_data_scopes"), r).
					Warn("failed to mint invite token", "scope_id", req.ID, "error", err)
				shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
			s.invalidateCaches()
			withPeerLog(slog.With("handler", "admin_data_scopes"), r).
				Info("invite token minted", "scope_id", view.ID, "dataset", view.Dataset)
			shared.WriteJSON(w, map[string]any{
				"scope":        view,
				"invite_token": token,
				"note":         "Store this invite token now — it is not shown again.",
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
		view, token, err := s.scopes.Rotate(id)
		if err != nil {
			shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.invalidateCaches()
		withPeerLog(slog.With("handler", "admin_data_scopes"), r).
			Info("invite token rotated", "scope_id", id)
		shared.WriteJSON(w, map[string]any{
			"scope":        view,
			"invite_token": token,
			"note":         "Store this invite token now — it is not shown again. The previous token is invalid.",
		})
	case r.Method == http.MethodGet:
		view, ok := s.scopes.View(id)
		if !ok {
			shared.WriteJSONError(w, http.StatusNotFound, fmt.Sprintf("data scope %q not found", id))
			return
		}
		shared.WriteJSON(w, map[string]any{"scope": view})
	case r.Method == http.MethodPatch || r.Method == http.MethodPut:
		body, ok := shared.ReadBodyLimited(w, r, maxAdminBodyBytes)
		if !ok {
			return
		}
		var req DataScopeUpdateRequest
		if err := json.Unmarshal(body, &req); err != nil {
			shared.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		view, err := s.scopes.Update(id, req)
		if err != nil {
			shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.invalidateCaches()
		withPeerLog(slog.With("handler", "admin_data_scopes"), r).
			Info("data scope updated", "scope_id", id, "disabled", view.Disabled)
		shared.WriteJSON(w, map[string]any{"scope": view, "status": "saved"})
	default:
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
