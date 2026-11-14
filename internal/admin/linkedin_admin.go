package admin

import (
	"net/http"

	"github.com/fudomunro/aegis/internal/connectors"
)

// HandleAdminLinkedIn serves /v1/admin/linkedin/* (issue #218). The store
// declares its admin copy via connectors.ImportAdmin (issue #254).
func (h *Handlers) HandleAdminLinkedIn(w http.ResponseWriter, r *http.Request) {
	// A typed-nil behind ImportStore is not nil and would panic on call.
	var store connectors.ImportStore
	if h.LinkedIn != nil {
		store = h.LinkedIn
	}
	h.HandleAdminImportStore(w, r, ImportStoreAdmin{
		Prefix:    "/v1/admin/linkedin",
		ServiceID: connectors.LinkedInServiceID,
		Store:     store,
		Admin:     h.LinkedIn.Admin(),
	})
}
