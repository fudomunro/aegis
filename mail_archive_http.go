package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// mailPermType is the permission key for the local mail archive surface.
// It shares the messages data-type so admin already has a control for it.
const mailPermType = "messages"

// handleMailRoutes dispatches /v1/mail/* under AuthMiddleware (registered in main).
func (s *Server) handleMailRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/mail")
	if path == "" {
		path = "/"
	}
	switch {
	case path == "/" || path == "":
		writeJSONError(w, http.StatusNotFound, "unknown mail archive route")
	case path == "/search" || path == "/search/":
		s.HandleMailSearch(w, r)
	case path == "/status" || path == "/status/":
		s.HandleMailStatus(w, r)
	case path == "/sync" || path == "/sync/":
		s.HandleMailSync(w, r)
	case strings.HasPrefix(path, "/messages/"):
		s.HandleMailMessage(w, r, strings.TrimPrefix(path, "/messages/"))
	default:
		writeJSONError(w, http.StatusNotFound, "unknown mail archive route")
	}
}

func (s *Server) requireMailArchive(w http.ResponseWriter) *MailArchive {
	if s.mail == nil {
		writeJSONError(w, http.StatusNotFound,
			"mail archive disabled — start Guardian with -mail-archive mail-archive.db")
		return nil
	}
	return s.mail
}

func (s *Server) mailReadable() bool {
	// Local archive is readable when any non-blocked service allows message reads.
	return s.libraryReadableFor(mailPermType)
}

// HandleMailStatus reports archive health: GET /v1/mail/status
func (s *Server) HandleMailStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.mailReadable() {
		writeJSONError(w, http.StatusForbidden, "permission denied: agent access to messages is not allowed")
		return
	}
	a := s.requireMailArchive(w)
	if a == nil {
		return
	}
	st, err := a.Status()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read mail archive status")
		return
	}
	writeMailJSON(w, st)
}

// HandleMailSearch runs FTS + filters: GET /v1/mail/search
//
// Query params: q, from, to, after, before, label (repeatable), limit, offset,
// include_body (1 to fill Message.body from the archive).
func (s *Server) HandleMailSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.mailReadable() {
		writeJSONError(w, http.StatusForbidden, "permission denied: agent access to messages is not allowed")
		return
	}
	a := s.requireMailArchive(w)
	if a == nil {
		return
	}
	q := r.URL.Query()
	limit := 0
	offset := 0
	if v := strings.TrimSpace(q.Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	if v := strings.TrimSpace(q.Get("offset")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			offset = n
		}
	}
	withBody := q.Get("include_body") == "1" || strings.EqualFold(q.Get("include_body"), "true")
	search := MailSearchQuery{
		Q:        q.Get("q"),
		From:     q.Get("from"),
		To:       q.Get("to"),
		After:    q.Get("after"),
		Before:   q.Get("before"),
		Labels:   q["label"],
		Limit:    limit,
		Offset:   offset,
		WithBody: withBody,
	}
	resp, err := a.Search(search)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "mail archive search failed")
		return
	}
	writeMailJSON(w, resp)
}

// HandleMailMessage fetches one archived message:
//
//	GET /v1/mail/messages/{service}/{id}           — neutral Message (body included)
//	GET /v1/mail/messages/{service}/{id}/raw       — gzip-decoded provider payload JSON
func (s *Server) HandleMailMessage(w http.ResponseWriter, r *http.Request, rest string) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.mailReadable() {
		writeJSONError(w, http.StatusForbidden, "permission denied: agent access to messages is not allowed")
		return
	}
	a := s.requireMailArchive(w)
	if a == nil {
		return
	}
	rest = strings.Trim(rest, "/")
	parts := strings.Split(rest, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		writeJSONError(w, http.StatusNotFound, "expected /v1/mail/messages/{service}/{id}")
		return
	}
	service, id := parts[0], parts[1]
	wantRaw := len(parts) >= 3 && parts[2] == "raw"

	rec, err := a.Get(service, id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "message not found in mail archive")
		return
	}
	if wantRaw {
		payload, err := rec.Payload()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to decompress archived payload")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(payload)
		return
	}
	writeMailJSON(w, rec.Message())
}

// MailSyncRequest is the body for POST /v1/mail/sync.
type MailSyncRequest struct {
	// Service to ingest. Empty defaults to google-gmail when configured.
	Service string `json:"service,omitempty"`
	// MaxMessages caps this run (default 200, max 1000). Call again to continue.
	MaxMessages int `json:"max_messages,omitempty"`
	// Query overrides the provider list query (default in:anywhere for full archive).
	Query string `json:"query,omitempty"`
	// ResetCursor restarts from the first page.
	ResetCursor bool `json:"reset_cursor,omitempty"`
}

// HandleMailSync ingests a bounded batch into the archive: POST /v1/mail/sync
//
// Resumable: the page cursor is stored in the archive. Repeat until done=true.
func (s *Server) HandleMailSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.mailReadable() {
		writeJSONError(w, http.StatusForbidden, "permission denied: agent access to messages is not allowed")
		return
	}
	s.handleMailSyncRequest(w, r, true)
}

// HandleAdminMailStatus is the admin UI status panel: GET /v1/admin/mail/status
// Skips the agent messages permission gate — the operator owns the archive.
func (s *Server) HandleAdminMailStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	a := s.requireMailArchive(w)
	if a == nil {
		return
	}
	st, err := a.Status()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read mail archive status")
		return
	}
	writeMailJSON(w, st)
}

// HandleAdminMailSync is the admin UI ingest control: POST /v1/admin/mail/sync
// Same body and resumable semantics as /v1/mail/sync, without the agent gate.
func (s *Server) HandleAdminMailSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	s.handleMailSyncRequest(w, r, false)
}

func (s *Server) handleMailSyncRequest(w http.ResponseWriter, r *http.Request, requireAgentMessagesRead bool) {
	a := s.requireMailArchive(w)
	if a == nil {
		return
	}
	var req MailSyncRequest
	if r.Body != nil {
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil && err.Error() != "EOF" {
			// Empty body is fine; other decode errors are client faults.
			if r.ContentLength != 0 {
				writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
		}
	}
	if req.Service == "" {
		req.Service = googleGmailServiceID
	}
	if req.MaxMessages <= 0 {
		req.MaxMessages = 200
	}
	if req.MaxMessages > 1000 {
		req.MaxMessages = 1000
	}

	connector, ok := s.connectors[req.Service]
	if !ok {
		writeJSONError(w, http.StatusNotFound, "no connector for service "+req.Service)
		return
	}
	svc := s.config.GetService(req.Service)
	if svc == nil || svc.Action == "block" {
		writeJSONError(w, http.StatusForbidden, "service blocked")
		return
	}
	// Admin sync is operator-owned; only the agent surface honors messages=read.
	if requireAgentMessagesRead && !svc.Permissions.AllowsRead(mailPermType) {
		writeJSONError(w, http.StatusForbidden, "permission denied: agent access to messages is not allowed")
		return
	}

	syncer, ok := connector.(MailArchiveSyncer)
	if !ok {
		writeJSONError(w, http.StatusNotImplemented, "service does not support mail archive sync")
		return
	}

	s.mailSyncMu.Lock()
	defer s.mailSyncMu.Unlock()
	result, err := syncer.SyncMailArchive(r.Context(), a, MailSyncOptions{
		MaxMessages: req.MaxMessages,
		Query:       req.Query,
		ResetCursor: req.ResetCursor,
	})
	if err != nil && result.Error == "" {
		result.Error = err.Error()
	}
	// Fail-open: report what was stored even when the upstream call failed mid-run.
	writeMailJSON(w, result)
}

func writeMailJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

// mailArchiveHints returns pre-request instruction text when the user wants
// local archive search rather than a live provider listing.
func (s *Server) mailArchiveHints(msg string) []string {
	if s.mail == nil || !s.mailReadable() {
		return nil
	}
	if !mentionsMailArchive(msg) {
		return nil
	}
	root := strings.TrimRight(s.baseURL, "/")
	return []string{`The user wants the local mail archive (full history search). Prefer:
GET ` + root + `/v1/mail/search?q=…&from=…&to=…&after=YYYY-MM-DD&before=YYYY-MM-DD&limit=50
GET ` + root + `/v1/mail/messages/{service}/{id} — one archived message with body
GET ` + root + `/v1/mail/messages/{service}/{id}/raw — provider payload JSON
GET ` + root + `/v1/mail/status — archive size and sync cursor
POST ` + root + `/v1/mail/sync — ingest more mail (body: {"max_messages":200})
This searches the local compressed archive; live Gmail listing is only needed for brand-new mail that is not yet synced.`}
}

// mentionsMailArchive detects archive/history-oriented phrasing.
func mentionsMailArchive(msg string) bool {
	m := strings.ToLower(msg)
	for _, k := range []string{
		"mail archive", "email archive", "search my mail", "search my email",
		"search all email", "search all mail", "all of my email", "all of my mail",
		"full mail history", "full email history", "local mail", "local email",
	} {
		if strings.Contains(m, k) {
			return true
		}
	}
	return false
}
