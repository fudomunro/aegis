package core

import (
	"net/http"

	"github.com/fudomunro/aegis/internal/connectors"
)

// HandleAdminQuicken serves /v1/admin/quicken/* (issue #141). The store
// declares its admin copy via connectors.ImportAdmin (issue #254).
func (s *Server) HandleAdminQuicken(w http.ResponseWriter, r *http.Request) {
	// A typed-nil behind ImportStore is not nil and would panic on call.
	var store connectors.ImportStore
	if s.quicken != nil {
		store = s.quicken
	}
	s.HandleAdminImportStore(w, r, importStoreAdmin{
		prefix:    "/v1/admin/quicken",
		serviceID: connectors.QuickenServiceID,
		store:     store,
		admin:     s.quicken.Admin(),
	})
}
