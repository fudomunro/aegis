package core

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/connectors"
)

// --- Admin import status / run (issue #142) ---

// TimelineImportStatus is GET /v1/admin/timeline/status.
type TimelineImportStatus struct {
	Enabled    bool     `json:"enabled"`
	Path       string   `json:"path,omitempty"`
	Count      int      `json:"count"`
	StoreEmpty bool     `json:"store_empty"`
	LibraryURL string   `json:"library_url,omitempty"`
	SamplePath string   `json:"sample_hint,omitempty"`
	Hints      []string `json:"hints,omitempty"`
}

// TimelineImportRequest is POST /v1/admin/timeline/import.
type TimelineImportRequest struct {
	Path string `json:"path"`
}

// HandleAdminTimeline serves /v1/admin/timeline/*.
func (s *Server) HandleAdminTimeline(w http.ResponseWriter, r *http.Request) {
	s.HandleAdminImportStore(w, r, importStoreAdmin{
		prefix:     "/v1/admin/timeline",
		flagName:   "-timeline-store",
		pathHint:   "a Timeline JSON file, directory, or Takeout zip",
		serviceID:  connectors.TimelineServiceID,
		store:      s.timeline,
		status:     func() any { return s.timelineStatus() },
		importPath: func(path string) (any, error) { return connectors.ImportTimelinePath(s.timeline, path) },
		logAttrs: func(res any) []any {
			return res.(connectors.TimelineImportResult).LogAttrs()
		},
		bareResult: true,
	})
}

func (s *Server) timelineStatus() TimelineImportStatus {
	store := s.timeline
	st := TimelineImportStatus{
		Enabled:    store != nil && store.Enabled(),
		SamplePath: "~/Downloads/Takeout/Location History/Semantic Location History (JSON files or a Takeout zip)",
	}
	if st.Enabled {
		st.Path = store.Path()
		if n, err := store.Count(); err == nil {
			st.Count = n
		}
		st.StoreEmpty = st.Count == 0
	}
	root := strings.TrimRight(s.baseURL, "/")
	st.LibraryURL = root + "/v1/library/places"
	if !st.Enabled {
		st.Hints = append(st.Hints, "Timeline store is disabled — restart with -timeline-store <path> (default under the data dir).")
	} else if st.StoreEmpty {
		st.Hints = append(st.Hints,
			"Export Location History from Google Takeout (Timeline → Export), then import the Semantic Location History folder or zip below.",
			"Re-import is safe: rows already stored are skipped. places permission defaults to none — enable city, read, or precise to let agents query it.")
	} else {
		st.Hints = append(st.Hints,
			fmt.Sprintf("%d visit/trip window(s) imported. Agents can query them via %s when places permissions allow.", st.Count, st.LibraryURL))
	}
	return st
}
