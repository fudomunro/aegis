package admin

import (
	"net/http"

	"github.com/fudomunro/aegis/internal/connectors"
)

// HandleAdminQuicken serves /v1/admin/quicken/* (issue #141). The store
// declares its admin copy via connectors.ImportAdmin (issue #254).
func (h *Handlers) HandleAdminQuicken(w http.ResponseWriter, r *http.Request) {
	// A typed-nil behind ImportStore is not nil and would panic on call.
	var store connectors.ImportStore
	if h.Quicken != nil {
		store = h.Quicken
	}
	h.HandleAdminImportStore(w, r, ImportStoreAdmin{
		Prefix:    "/v1/admin/quicken",
		ServiceID: connectors.QuickenServiceID,
		Store:     store,
		Admin:     h.Quicken.Admin(),
	})
}
