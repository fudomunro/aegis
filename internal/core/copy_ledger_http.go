package core

import (
	"net/http"
	"strings"
)

// CopyListResponse is GET /v1/library/copies.
type CopyListResponse struct {
	Copies    []CopyRecord   `json:"copies"`
	Total     int            `json:"total"`
	ByStatus  map[string]int `json:"by_status,omitempty"`
	SyncModes map[string]int `json:"sync_modes,omitempty"`
	ByService map[string]int `json:"by_service,omitempty"`
}

// HandleLibraryCopies lists durable copy-ledger rows (issue #76).
//
// GET /v1/library/copies?kind=&service=&status=&entity_key=
func (s *Server) HandleLibraryCopies(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	q := r.URL.Query()
	filter := CopyFilter{
		Kind:      strings.TrimSpace(q.Get("kind")),
		Service:   strings.TrimSpace(q.Get("service")),
		Status:    strings.TrimSpace(q.Get("status")),
		EntityKey: strings.TrimSpace(q.Get("entity_key")),
	}
	var rows []CopyRecord
	if s != nil && s.copies != nil {
		rows = s.copies.List(filter)
	}
	resp := CopyListResponse{
		Copies:    rows,
		Total:     len(rows),
		ByStatus:  map[string]int{},
		SyncModes: map[string]int{},
		ByService: map[string]int{},
	}
	for _, rec := range rows {
		if rec.Status != "" {
			resp.ByStatus[rec.Status]++
		}
		if rec.SyncMode != "" {
			resp.SyncModes[rec.SyncMode]++
		}
		if rec.Service != "" {
			resp.ByService[rec.Service]++
		}
	}
	writeJSON(w, resp)
}
