package admin

import (
	"net/http"

	"github.com/fudomunro/aegis/internal/shared"
)

// HandleAdminSearchStatus reports search index health for the admin UI:
// GET /v1/admin/search/status. Mirrors the mail-archive status surface.
func (h *Handlers) HandleAdminSearchStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	st := SearchIndexStatus{Enabled: h.Search.Enabled()}
	if st.Enabled {
		st.Path = h.Search.Path()
	}
	counts := map[string]int{}
	if st.Enabled {
		var err error
		counts, err = h.Search.CountsByKind()
		if err != nil {
			shared.WriteJSONError(w, http.StatusInternalServerError, "failed to read search index status")
			return
		}
	}
	for _, kind := range h.KindCatalog.AllLibraryKinds() {
		n := counts[kind.ID]
		st.Kinds = append(st.Kinds, SearchKindStatus{
			ID:         kind.ID,
			Label:      kind.Label,
			Searchable: kind.Search != nil,
			Docs:       n,
		})
		st.Total += n
	}
	shared.WriteJSON(w, st)
}

type SearchKindStatus struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Searchable reports whether the kind declares a SearchSpec.
	Searchable bool `json:"searchable"`
	// Docs is the indexed document count (0 when never materialized).
	Docs int `json:"docs"`
}

// Status is the admin-facing search index summary.
type SearchIndexStatus struct {
	Enabled bool               `json:"enabled"`
	Path    string             `json:"path,omitempty"`
	Total   int                `json:"total"`
	Kinds   []SearchKindStatus `json:"kinds"`
}
