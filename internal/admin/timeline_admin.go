package admin

import (
	"net/http"

	"github.com/fudomunro/aegis/internal/connectors"
)

// HandleAdminTimeline serves /v1/admin/timeline/* (issue #142). The store
// declares its admin copy via connectors.ImportAdmin (issue #254).
func (h *Handlers) HandleAdminTimeline(w http.ResponseWriter, r *http.Request) {
	// A typed-nil behind ImportStore is not nil and would panic on call.
	var store connectors.ImportStore
	if h.Timeline != nil {
		store = h.Timeline
	}
	h.HandleAdminImportStore(w, r, ImportStoreAdmin{
		Prefix:    "/v1/admin/timeline",
		ServiceID: connectors.TimelineServiceID,
		Store:     store,
		Admin:     h.Timeline.Admin(),
	})
}
