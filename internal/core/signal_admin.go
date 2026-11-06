package core

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/connectors"
)

// --- Admin import status / run (issue #168) ---

// SignalImportStatus is GET /v1/admin/signal/status.
type SignalImportStatus struct {
	Enabled    bool     `json:"enabled"`
	Path       string   `json:"path,omitempty"`
	Contacts   int      `json:"contacts"`
	Messages   int      `json:"messages"`
	StoreEmpty bool     `json:"store_empty"`
	LibraryURL string   `json:"library_url,omitempty"`
	SamplePath string   `json:"sample_hint,omitempty"`
	Hints      []string `json:"hints,omitempty"`
}

// SignalImportRequest is POST /v1/admin/signal/import.
type SignalImportRequest struct {
	Path string `json:"path"`
}

// HandleAdminSignal serves /v1/admin/signal/*.
func (s *Server) HandleAdminSignal(w http.ResponseWriter, r *http.Request) {
	s.HandleAdminImportStore(w, r, importStoreAdmin{
		prefix:    "/v1/admin/signal",
		flagName:  "-signal-store",
		pathHint:  "a Signal export file or directory",
		serviceID: connectors.SignalServiceID,
		store:     s.signal,
		status:    func() any { return s.signalStatus() },
		importPath: func(path string) (any, error) {
			return connectors.ImportSignalPath(s.signal, path)
		},
		logAttrs: func(res any) []any {
			return res.(connectors.SignalImportResult).LogAttrs()
		},
	})
}

func (s *Server) signalStatus() SignalImportStatus {
	store := s.signal
	st := SignalImportStatus{
		Enabled:    store != nil && store.Enabled(),
		SamplePath: "~/Downloads/signal-export (JSONL, or plaintext .db from signalbackup-tools)",
	}
	if st.Enabled {
		st.Path = store.Path()
		if c, m, err := store.Counts(); err == nil {
			st.Contacts, st.Messages = c, m
		}
		st.StoreEmpty = st.Contacts == 0 && st.Messages == 0
	}
	root := strings.TrimRight(s.baseURL, "/")
	st.LibraryURL = root + "/v1/library/messages"
	if !st.Enabled {
		st.Hints = append(st.Hints, "Signal store is disabled — restart with -signal-store <path> (default under the data dir).")
	} else if st.StoreEmpty {
		st.Hints = append(st.Hints,
			"Signal has no public chat API. Export on the device, decrypt if needed, then import the files below.",
			"Android: Settings → Chats → Chat backups → Create backup, then decrypt with signalbackup-tools to a plaintext database.sqlite.",
			"Desktop: copy the Signal data directory's sql/db.sqlite after decrypting it (community exporters can dump JSONL).",
			"Aegis also accepts a JSONL export: one object per line with \"type\":\"contact\"|\"message\".",
			"Re-import is safe: rows already stored are skipped. Attachments are never stored.")
	} else {
		st.Hints = append(st.Hints,
			fmt.Sprintf("%d contact(s) and %d message(s) imported. Agents can query them via %s when contacts/messages permissions allow.",
				st.Contacts, st.Messages, st.LibraryURL))
	}
	return st
}
