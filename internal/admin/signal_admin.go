package admin

import (
	"net/http"

	"github.com/fudomunro/aegis/internal/connectors"
)

// HandleAdminSignal serves /v1/admin/signal/* (issue #168). The store
// declares its admin copy via connectors.ImportAdmin (issue #254).
func (h *Handlers) HandleAdminSignal(w http.ResponseWriter, r *http.Request) {
	// A typed-nil behind ImportStore is not nil and would panic on call.
	var store connectors.ImportStore
	if h.Signal != nil {
		store = h.Signal
	}
	h.HandleAdminImportStore(w, r, ImportStoreAdmin{
		Prefix:    "/v1/admin/signal",
		ServiceID: connectors.SignalServiceID,
		Store:     store,
		Admin:     h.Signal.Admin(),
	})
}
