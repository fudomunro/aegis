package core

import (
	"net/http"

	"github.com/fudomunro/aegis/internal/core/search"
	"github.com/fudomunro/aegis/internal/shared"
)

// HandleAdminSearchStatus reports search index health for the admin UI:
// GET /v1/admin/search/status. Mirrors the mail-archive status surface.
func (s *Server) HandleAdminSearchStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	st := search.Status{Enabled: s.search.Enabled()}
	if st.Enabled {
		st.Path = s.search.Path()
	}
	counts := map[string]int{}
	if st.Enabled {
		var err error
		counts, err = s.search.CountsByKind()
		if err != nil {
			shared.WriteJSONError(w, http.StatusInternalServerError, "failed to read search index status")
			return
		}
	}
	for _, kind := range s.allLibraryKinds() {
		n := counts[kind.ID]
		st.Kinds = append(st.Kinds, search.KindStatus{
			ID:         kind.ID,
			Label:      kind.Label,
			Searchable: kind.Search != nil,
			Docs:       n,
		})
		st.Total += n
	}
	shared.WriteJSON(w, st)
}
