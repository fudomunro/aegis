package admin

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/shared"
)

// ImportStoreAdmin is one admin surface over a local import store: GET
// status and POST import (issue #266). The route prefix is core's (it is
// route layout); every provider-facing string and counter shape is declared
// by the store in connectors.ImportAdmin (issue #254, the #265 pattern).
type ImportStoreAdmin struct {
	Prefix    string // URL prefix, e.g. "/v1/admin/quicken"
	ServiceID string // service whose caches invalidate after import
	Store     connectors.ImportStore
	Admin     connectors.ImportAdmin
}

// HandleAdminImportStore serves GET <prefix>/status and POST <prefix>/import.
func (h *Handlers) HandleAdminImportStore(w http.ResponseWriter, r *http.Request, spec ImportStoreAdmin) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, spec.Prefix), "/")
	switch {
	case path == "" || path == "status":
		if r.Method != http.MethodGet {
			shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(spec.Status(h.BaseURL))
	case path == "import":
		if r.Method != http.MethodPost {
			shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.runAdminImportStore(w, r, spec)
	default:
		shared.WriteJSONError(w, http.StatusNotFound, "unknown admin endpoint")
	}
}

// status builds the admin status DTO from the store's declared copy and
// live counts. The counter fields are per-store; unused ones stay zero.
func (spec ImportStoreAdmin) Status(baseURL string) connectors.ImportStoreStatus {
	st := connectors.ImportStoreStatus{
		Enabled:    spec.Store != nil && spec.Store.Enabled(),
		SamplePath: spec.Admin.SamplePath,
	}
	counts := map[string]int{"count": 0, "contacts": 0, "messages": 0}
	if st.Enabled {
		st.Path = spec.Store.Path()
		if spec.Admin.Counts != nil {
			for k, v := range spec.Admin.Counts() {
				counts[k] = v
			}
		}
		st.Count, st.Contacts, st.Messages = counts["count"], counts["contacts"], counts["messages"]
		st.StoreEmpty = st.Count == 0 && st.Contacts == 0 && st.Messages == 0
	}
	st.LibraryURL = strings.TrimRight(baseURL, "/") + spec.Admin.LibraryPath
	if spec.Admin.Hints != nil {
		st.Hints = spec.Admin.Hints(connectors.ImportHintState{
			Enabled:    st.Enabled,
			Counts:     counts,
			LibraryURL: st.LibraryURL,
		})
	}
	return st
}

func (h *Handlers) runAdminImportStore(w http.ResponseWriter, r *http.Request, spec ImportStoreAdmin) {
	if spec.Store == nil || !spec.Store.Enabled() {
		shared.WriteJSONError(w, http.StatusBadRequest, "store disabled — set "+spec.Admin.FlagName)
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
		shared.WriteJSONError(w, http.StatusBadRequest, "path is required — "+spec.Admin.PathHint)
		return
	}
	res, err := spec.Admin.Import(req.Path)
	if err != nil {
		shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Library cache must not serve the old empty list.
	h.Host.InvalidateService(spec.ServiceID)
	attrs := []any{"path", req.Path}
	if res != nil {
		attrs = append(attrs, res.LogAttrs()...)
	}
	slog.Info(strings.TrimPrefix(spec.Prefix, "/v1/admin/")+" import from admin UI", attrs...)
	w.Header().Set("Content-Type", "application/json")
	if spec.Admin.BareResult {
		json.NewEncoder(w).Encode(res)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{
		"result": res,
		"status": spec.Status(h.BaseURL),
	})
}
