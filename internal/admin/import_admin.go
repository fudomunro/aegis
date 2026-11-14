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
// status and POST import (issue #266). The route prefix and every
// provider-facing string are declared by the store's spec and Admin()
// copy (issue #254, issue #393) — core serves them generically and
// admin.html renders the panel from the same declarations.
type ImportStoreAdmin struct {
	Spec  connectors.LocalImportStoreSpec
	Store connectors.ImportStore
}

// Admin returns the store's declared admin copy. When the store is
// disabled the typed-nil entry still describes itself.
func (s ImportStoreAdmin) Admin() connectors.ImportAdmin {
	if s.Store == nil {
		st, _ := s.Spec.Open("")
		return st.Admin()
	}
	return s.Store.Admin()
}

// HandleAdminImportStore serves GET <prefix>/status and POST <prefix>/import.
func (h *Handlers) HandleAdminImportStore(w http.ResponseWriter, r *http.Request, spec ImportStoreAdmin) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, spec.Spec.AdminPrefix()), "/")
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

// ImportStoreHandler returns the admin handler for one declared store
// (issue #393): core registers it under the spec's route prefix and the
// handler never names a store.
func (h *Handlers) ImportStoreHandler(spec connectors.LocalImportStoreSpec) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.HandleAdminImportStore(w, r, ImportStoreAdmin{
			Spec:  spec,
			Store: h.LocalStores[spec.Stem],
		})
	}
}

// ImportStoreView is the admin.html view of one local import store: the
// declared panel copy plus live status, so the page renders every import
// panel from declarations instead of restating them (issue #265 pattern,
// issue #393).
type ImportStoreView struct {
	Stem      string                   `json:"stem"`
	ServiceID string                   `json:"service_id"`
	Panel     connectors.ImportPanel   `json:"panel"`
	FlagName  string                   `json:"flag_name"`
	Status    connectors.ImportStoreStatus `json:"status"`
}

// HandleAdminImportStores serves GET /v1/admin/import-stores: every
// declared local import store with its panel copy and live status.
func (h *Handlers) HandleAdminImportStores(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	views := []ImportStoreView{}
	for _, spec := range connectors.LocalImportStoreSpecs() {
		admin := ImportStoreAdmin{Spec: spec, Store: h.LocalStores[spec.Stem]}
		a := admin.Admin()
		views = append(views, ImportStoreView{
			Stem:      spec.Stem,
			ServiceID: spec.Service.ID,
			Panel:     a.Panel,
			FlagName:  a.FlagName,
			Status:    admin.Status(h.BaseURL),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"stores": views})
}

// Status builds the admin status DTO from the store's declared copy and
// live counts. The counter fields are per-store; unused ones stay zero.
func (spec ImportStoreAdmin) Status(baseURL string) connectors.ImportStoreStatus {
	admin := spec.Admin()
	st := connectors.ImportStoreStatus{
		Enabled:    spec.Store != nil && spec.Store.Enabled(),
		SamplePath: admin.SamplePath,
	}
	counts := map[string]int{"count": 0, "contacts": 0, "messages": 0}
	if st.Enabled {
		st.Path = spec.Store.Path()
		if admin.Counts != nil {
			for k, v := range admin.Counts() {
				counts[k] = v
			}
		}
		st.Count, st.Contacts, st.Messages = counts["count"], counts["contacts"], counts["messages"]
		st.StoreEmpty = st.Count == 0 && st.Contacts == 0 && st.Messages == 0
	}
	st.LibraryURL = strings.TrimRight(baseURL, "/") + admin.LibraryPath
	if admin.Hints != nil {
		st.Hints = admin.Hints(connectors.ImportHintState{
			Enabled:    st.Enabled,
			Counts:     counts,
			LibraryURL: st.LibraryURL,
		})
	}
	return st
}

func (h *Handlers) runAdminImportStore(w http.ResponseWriter, r *http.Request, spec ImportStoreAdmin) {
	admin := spec.Admin()
	if spec.Store == nil || !spec.Store.Enabled() {
		shared.WriteJSONError(w, http.StatusBadRequest, "store disabled — set "+admin.FlagName)
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
		shared.WriteJSONError(w, http.StatusBadRequest, "path is required — "+admin.PathHint)
		return
	}
	res, err := admin.Import(req.Path)
	if err != nil {
		shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Library cache must not serve the old empty list.
	h.Host.InvalidateService(spec.Spec.Service.ID)
	attrs := []any{"path", req.Path}
	if res != nil {
		attrs = append(attrs, res.LogAttrs()...)
	}
	slog.Info(spec.Spec.Stem+" import from admin UI", attrs...)
	w.Header().Set("Content-Type", "application/json")
	if admin.BareResult {
		json.NewEncoder(w).Encode(res)
		return
	}
	out := map[string]any{
		"result": res,
		"status": spec.Status(h.BaseURL),
	}
	if res != nil {
		out["summary"] = res.Summary()
	}
	json.NewEncoder(w).Encode(out)
}
