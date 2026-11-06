package core

import (
	"net/http"

	"github.com/fudomunro/aegis/internal/connectors"
)

// HandleAdminTimeline serves /v1/admin/timeline/* (issue #142). The store
// declares its admin copy via connectors.ImportAdmin (issue #254).
func (s *Server) HandleAdminTimeline(w http.ResponseWriter, r *http.Request) {
	// A typed-nil behind ImportStore is not nil and would panic on call.
	var store connectors.ImportStore
	if s.timeline != nil {
		store = s.timeline
	}
	s.HandleAdminImportStore(w, r, importStoreAdmin{
		prefix:    "/v1/admin/timeline",
		serviceID: connectors.TimelineServiceID,
		store:     store,
		admin:     s.timeline.Admin(),
	})
}
