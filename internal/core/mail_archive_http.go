package core

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/mail"
	"github.com/fudomunro/aegis/internal/core/scopes"
	"github.com/fudomunro/aegis/internal/shared"
)

// mailPermType is the permission key for the local mail archive surface.
// It shares the messages kind so admin already has a control for it.
const mailPermType = "messages"

func (s *Server) requireMailArchive(w http.ResponseWriter) *mail.Archive {
	if s.mail == nil {
		shared.WriteJSONError(w, http.StatusNotFound,
			"mail archive disabled — start Aegis with a mail archive path (default: <data-dir>/mail-archive.db)")
		return nil
	}
	return s.mail
}

// mailReadable reports whether any non-blocked service allows message reads
// (owner tier). Request-scoped callers use mailReadableFor.
func (s *Server) mailReadable() bool {
	return s.libraryReadableFor(mailPermType)
}

// mailReadableFor reports whether the request may use the mail archive: the
// principal's scope grants messages (deny-by-default) and the owner tier
// allows message reads.
func (s *Server) mailReadableFor(r *http.Request) bool {
	if !scopes.Allows(r, mailPermType, http.MethodGet) {
		return false
	}
	return s.mailReadable()
}

// MailSyncRequest is the body for admin mail ingest (issue #311: agent
// retrieval lives on /v1/library/messages; ingest stays admin).
type MailSyncRequest struct {
	// Service to ingest. Empty defaults to google-gmail when configured.
	// "google-takeout" syncs the read-only Takeout export path.
	Service string `json:"service,omitempty"`
	// Path sets/overrides the Takeout export location for this run
	// (google-takeout only). Empty uses the stored path.
	Path string `json:"path,omitempty"`
	// MaxMessages caps this run (default 200 for Gmail, 5000 for Takeout).
	MaxMessages int `json:"max_messages,omitempty"`
	// Query overrides the provider list query (Gmail only; default in:anywhere).
	Query string `json:"query,omitempty"`
	// ResetCursor restarts from the first page (Gmail) or rescans (Takeout).
	ResetCursor bool `json:"reset_cursor,omitempty"`
}

// HandleAdminMailStatus is the admin UI status panel: GET /v1/admin/mail/status
// The operator owns the archive; agent-facing status retired with /v1/mail.
func (s *Server) HandleAdminMailStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	a := s.requireMailArchive(w)
	if a == nil {
		return
	}
	st, err := a.Status()
	if err != nil {
		shared.WriteJSONError(w, http.StatusInternalServerError, "failed to read mail archive status")
		return
	}
	writeMailJSON(w, st)
}

// HandleAdminMailSync is the admin UI ingest control: POST /v1/admin/mail/sync
// Resumable: the page cursor is stored in the archive. Repeat until done=true.
func (s *Server) HandleAdminMailSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	s.handleMailSyncRequest(w, r)
}

func (s *Server) handleMailSyncRequest(w http.ResponseWriter, r *http.Request) {
	var req MailSyncRequest
	if r.Body != nil {
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil && err.Error() != "EOF" {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				shared.WriteBodyReadError(w, err)
				return
			}
			// Empty body is fine; other decode errors are client faults.
			if r.ContentLength != 0 {
				shared.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
		}
	}
	if req.Service == "" {
		req.Service = connectors.GoogleGmailServiceID
	}
	s.handleMailSyncPayload(w, r, req)
}

// handleMailSyncPayload runs one already-decoded sync request so callers
// that own their decode (the admin import route) cannot drift from the
// shared route mechanics (issue #227).
func (s *Server) handleMailSyncPayload(w http.ResponseWriter, r *http.Request, req MailSyncRequest) {
	if s.requireMailArchive(w) == nil {
		return
	}
	// Pre-flight shape of this route is its own contract: a service that
	// cannot sync the archive is 501, an unknown one 404, not a fail-open
	// empty result. Ingest sources are connector-declared (issue #286).
	if _, live := s.connectors[req.Service]; !live && s.mailIngestFor(req.Service) == nil {
		shared.WriteJSONError(w, http.StatusNotFound, "no connector for service "+req.Service)
		return
	}
	svc := s.config.GetService(req.Service)
	if _, live := s.connectors[req.Service]; live {
		if svc == nil || svc.Action == "block" {
			shared.WriteJSONError(w, http.StatusForbidden, "service blocked")
			return
		}
	}
	if s.mailIngestFor(req.Service) == nil {
		shared.WriteJSONError(w, http.StatusNotImplemented, "service does not support mail archive sync")
		return
	}

	// One ingest implementation, shared with the sync queue runner
	// (issue #227): the mechanics live in doMailIngest so the route and the
	// queue cannot drift. Path/query/reset are per-source knobs the ingest
	// interprets (issue #286).
	result, err := s.doMailIngest(r.Context(), mailIngestPayload{
		Service:     req.Service,
		MaxMessages: req.MaxMessages,
		Query:       req.Query,
		Path:        req.Path,
		ResetCursor: req.ResetCursor,
	})
	if errors.Is(err, shared.ErrMailConfig) {
		// Missing or rejected ingest location is a client error, not a
		// fail-open result.
		shared.WriteJSONError(w, http.StatusBadRequest, result.Error)
		return
	}
	// Fail-open: report what was stored even when the upstream call failed
	// mid-run. doMailIngest has already filled result.Error from the error.
	writeMailJSON(w, result)
}

// HandleAdminMailImport syncs the read-only Takeout export:
// POST /v1/admin/mail/import {"path":"…","max_messages":N}
func (s *Server) HandleAdminMailImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// Force Takeout: this route is only for the export sync location.
	var req MailSyncRequest
	if r.Body != nil {
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil && r.ContentLength != 0 {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				shared.WriteBodyReadError(w, err)
				return
			}
			shared.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}
	req.Service = connectors.TakeoutServiceID
	s.handleMailSyncPayload(w, r, req)
}

func writeMailJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

// mailArchiveHints returns pre-request instruction text when the user wants
// local archive search rather than a live provider listing (issue #311: one
// retrieval surface, the library API).
func (s *Server) mailArchiveHints(r *http.Request, msg string) []string {
	if s.mail == nil || !s.mailReadableFor(r) {
		return nil
	}
	if !mentionsMailArchive(msg) {
		return nil
	}
	root := strings.TrimRight(s.baseURL, "/")
	return []string{`The user wants the local mail archive (full history search). Prefer:
GET ` + root + `/v1/library/messages?q=…&from=…&to=…&after=YYYY-MM-DD&before=YYYY-MM-DD&limit=50&sort=desc
GET ` + root + `/v1/library/messages?sort=asc&limit=1 — oldest archived message(s); use sort=asc for earliest/first/oldest questions
GET ` + root + `/v1/library/messages/{service}/{id} — one archived message with body
Default sort is newest-first (desc). For "oldest", "earliest", or "first" message questions use sort=asc (and limit=1 when only one answer is needed).
All ingested sources form one mail collection: results merge on RFC Message-ID into multi-source entries. Live provider rows and archive rows are the same collection — the local archive is a source of ` + root + `/v1/library/messages alongside live services, and full-history queries cover both.`}
}

// mentionsMailArchive detects archive/history-oriented phrasing.
func mentionsMailArchive(msg string) bool {
	m := strings.ToLower(msg)
	for _, k := range []string{
		"mail archive", "email archive", "search my mail", "search my email",
		"search all email", "search all mail", "all of my email", "all of my mail",
		"full mail history", "full email history", "local mail", "local email",
		"oldest message", "oldest email", "oldest mail", "earliest message",
		"earliest email", "first message", "first email", "first mail",
		"oldest available", "earliest available",
		// Finding a specific message is a search, not a list browse.
		"find my email", "find my mail", "find the email", "find the mail",
		"find an email", "find a mail", "find this email", "find this mail",
		"where is the email", "where is the mail", "where's the email",
		"search for email", "search for mail", "search email for", "search mail for",
		"look up email", "look up mail", "look up the email", "look up the mail",
		"email about", "mail about", "message about",
	} {
		if strings.Contains(m, k) {
			return true
		}
	}
	return false
}
