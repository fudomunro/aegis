package core

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/connectors"
)

// Quicken admin import status / run (issue #141).
// QuickenImportStatus is GET /v1/admin/quicken/status.
type QuickenImportStatus struct {
	Enabled    bool     `json:"enabled"`
	Path       string   `json:"path,omitempty"`
	Count      int      `json:"count"`
	StoreEmpty bool     `json:"store_empty"`
	LibraryURL string   `json:"library_url,omitempty"`
	SamplePath string   `json:"sample_hint,omitempty"`
	Hints      []string `json:"hints,omitempty"`
}

// QuickenImportRequest is POST /v1/admin/quicken/import.
type QuickenImportRequest struct {
	Path string `json:"path"`
}

// HandleAdminQuicken serves /v1/admin/quicken/*.
func (s *Server) HandleAdminQuicken(w http.ResponseWriter, r *http.Request) {
	s.HandleAdminImportStore(w, r, importStoreAdmin{
		prefix:     "/v1/admin/quicken",
		flagName:   "-quicken-store",
		pathHint:   "a QIF/QFX file or directory",
		serviceID:  connectors.QuickenServiceID,
		store:      s.quicken,
		status:     func() any { return s.quickenStatus() },
		importPath: func(path string) (any, error) { return connectors.ImportQuickenPath(s.quicken, path) },
		logAttrs: func(res any) []any {
			return res.(connectors.QuickenImportResult).LogAttrs()
		},
	})
}

func (s *Server) quickenStatus() QuickenImportStatus {
	store := s.quicken
	st := QuickenImportStatus{
		Enabled:    store != nil && store.Enabled(),
		SamplePath: "~/Downloads/Quicken (QIF / QFX / OFX file or directory)",
	}
	if st.Enabled {
		st.Path = store.Path()
		if n, err := store.Count(); err == nil {
			st.Count = n
		}
		st.StoreEmpty = st.Count == 0
	}
	root := strings.TrimRight(s.baseURL, "/")
	st.LibraryURL = root + "/v1/library/transactions"
	if !st.Enabled {
		st.Hints = append(st.Hints, "Quicken store is disabled — restart with -quicken-store <path> (default under the data dir).")
	} else if st.StoreEmpty {
		st.Hints = append(st.Hints,
			"Export QIF or QFX from Quicken (File → Export), then import the file or its folder below.",
			"Re-import is safe: rows already stored (by FITID) are skipped.")
	} else {
		st.Hints = append(st.Hints,
			fmt.Sprintf("%d line item(s) imported. Agents can query them via %s.", st.Count, st.LibraryURL))
	}
	return st
}
