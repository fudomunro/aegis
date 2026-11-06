package core

import (
	"net/http"

	"github.com/fudomunro/aegis/internal/connectors"
)

// HandleAdminSignal serves /v1/admin/signal/* (issue #168). The store
// declares its admin copy via connectors.ImportAdmin (issue #254).
func (s *Server) HandleAdminSignal(w http.ResponseWriter, r *http.Request) {
	// A typed-nil behind ImportStore is not nil and would panic on call.
	var store connectors.ImportStore
	if s.signal != nil {
		store = s.signal
	}
	s.HandleAdminImportStore(w, r, importStoreAdmin{
		prefix:    "/v1/admin/signal",
		serviceID: connectors.SignalServiceID,
		store:     store,
		admin:     s.signal.Admin(),
	})
}
