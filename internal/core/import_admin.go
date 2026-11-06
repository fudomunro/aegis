package core

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/shared"
)

// importStoreAdmin is one admin surface over a local import store: GET
// status and POST import (issue #266). The route prefix is core's (it is
// route layout); every provider-facing string and counter shape is declared
// by the store in connectors.ImportAdmin (issue #254, the #265 pattern).
type importStoreAdmin struct {
	prefix    string // URL prefix, e.g. "/v1/admin/quicken"
	serviceID string // service whose caches invalidate after import
	store     connectors.ImportStore
	admin     connectors.ImportAdmin
}

// HandleAdminImportStore serves GET <prefix>/status and POST <prefix>/import.
func (s *Server) HandleAdminImportStore(w http.ResponseWriter, r *http.Request, spec importStoreAdmin) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, spec.prefix), "/")
	switch {
	case path == "" || path == "status":
		if r.Method != http.MethodGet {
			shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(spec.status(s.baseURL))
	case path == "import":
		if r.Method != http.MethodPost {
			shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.runAdminImportStore(w, r, spec)
	default:
		shared.WriteJSONError(w, http.StatusNotFound, "unknown admin endpoint")
	}
}

// status builds the admin status DTO from the store's declared copy and
// live counts. The counter fields are per-store; unused ones stay zero.
func (spec importStoreAdmin) status(baseURL string) connectors.ImportStoreStatus {
	st := connectors.ImportStoreStatus{
		Enabled:    spec.store != nil && spec.store.Enabled(),
		SamplePath: spec.admin.SamplePath,
	}
	counts := map[string]int{"count": 0, "contacts": 0, "messages": 0}
	if st.Enabled {
		st.Path = spec.store.Path()
		if spec.admin.Counts != nil {
			for k, v := range spec.admin.Counts() {
				counts[k] = v
			}
		}
		st.Count, st.Contacts, st.Messages = counts["count"], counts["contacts"], counts["messages"]
		st.StoreEmpty = st.Count == 0 && st.Contacts == 0 && st.Messages == 0
	}
	st.LibraryURL = strings.TrimRight(baseURL, "/") + spec.admin.LibraryPath
	if spec.admin.Hints != nil {
		st.Hints = spec.admin.Hints(connectors.ImportHintState{
			Enabled:    st.Enabled,
			Counts:     counts,
			LibraryURL: st.LibraryURL,
		})
	}
	return st
}

func (s *Server) runAdminImportStore(w http.ResponseWriter, r *http.Request, spec importStoreAdmin) {
	if spec.store == nil || !spec.store.Enabled() {
		shared.WriteJSONError(w, http.StatusBadRequest, "store disabled — set "+spec.admin.FlagName)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		shared.WriteJSONError(w, http.StatusBadRequest, "invalid body")
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		shared.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Path = strings.TrimSpace(req.Path)
	if req.Path == "" {
		shared.WriteJSONError(w, http.StatusBadRequest, "path is required — "+spec.admin.PathHint)
		return
	}
	res, err := spec.admin.Import(req.Path)
	if err != nil {
		shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Library cache must not serve the old empty list.
	s.invalidateService(spec.serviceID)
	attrs := []any{"path", req.Path}
	if res != nil {
		attrs = append(attrs, res.LogAttrs()...)
	}
	slog.Info(strings.TrimPrefix(spec.prefix, "/v1/admin/")+" import from admin UI", attrs...)
	w.Header().Set("Content-Type", "application/json")
	if spec.admin.BareResult {
		json.NewEncoder(w).Encode(res)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{
		"result": res,
		"status": spec.status(s.baseURL),
	})
}
