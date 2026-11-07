package core

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/shared"
	"github.com/fudomunro/aegis/internal/types"
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
		shared.WriteJSONError(w, http.StatusNotFound, "unknown mail archive route")
	case path == "/search" || path == "/search/":
		s.HandleMailSearch(w, r)
	case path == "/status" || path == "/status/":
		s.HandleMailStatus(w, r)
	case path == "/sync" || path == "/sync/":
		s.HandleMailSync(w, r)
	case strings.HasPrefix(path, "/messages/"):
		s.HandleMailMessage(w, r, strings.TrimPrefix(path, "/messages/"))
	default:
		shared.WriteJSONError(w, http.StatusNotFound, "unknown mail archive route")
	}
}

func (s *Server) requireMailArchive(w http.ResponseWriter) *MailArchive {
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
	if !scopeAllows(r, mailPermType, http.MethodGet) {
		return false
	}
	return s.mailReadable()
}

// HandleMailStatus reports archive health: GET /v1/mail/status
func (s *Server) HandleMailStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.mailReadableFor(r) {
		shared.WriteJSONError(w, http.StatusForbidden, "denied: data grant for agent access to messages is not allowed")
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

// HandleMailSearch runs FTS + filters: GET /v1/mail/search
//
// Query params: q, from, to (addresses), after, before (time bounds),
// label (repeatable), limit, cursor (preferred) or offset (admin UI),
// sort (asc|desc, default desc), include_body (1 to fill Message.body).
// from/to are sender/recipient addresses only — not time filters (issue #185).
func (s *Server) HandleMailSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.mailReadableFor(r) {
		shared.WriteJSONError(w, http.StatusForbidden, "denied: data grant for agent access to messages is not allowed")
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
	// Prefer opaque cursor (library contract); keep offset for admin UI.
	if v := strings.TrimSpace(q.Get("cursor")); v != "" {
		n, err := types.DecodeCursor(v)
		if err != nil {
			shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		offset = n
	} else if v := strings.TrimSpace(q.Get("offset")); v != "" {
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
		Sort:     q.Get("sort"),
		Dataset:  scopeDataset(r),
	}
	resp, err := a.Search(search)
	if err != nil {
		shared.WriteJSONError(w, http.StatusInternalServerError, "mail archive search failed")
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
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.mailReadableFor(r) {
		shared.WriteJSONError(w, http.StatusForbidden, "denied: data grant for agent access to messages is not allowed")
		return
	}
	a := s.requireMailArchive(w)
	if a == nil {
		return
	}
	rest = strings.Trim(rest, "/")
	parts := strings.Split(rest, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		shared.WriteJSONError(w, http.StatusNotFound, "expected /v1/mail/messages/{service}/{id}")
		return
	}
	service, id := parts[0], parts[1]
	wantRaw := len(parts) >= 3 && parts[2] == "raw"

	rec, err := a.Get(service, id)
	if err != nil {
		shared.WriteJSONError(w, http.StatusNotFound, "message not found in mail archive")
		return
	}
	// Data-scope isolation: a partitioned principal cannot fetch rows from
	// another dataset even with a direct id (docs/data-scopes.md).
	if ds := scopeDataset(r); ds != "" && rec.Dataset != ds {
		shared.WriteJSONError(w, http.StatusNotFound, "message not found in mail archive")
		return
	}
	if wantRaw {
		payload, err := rec.Payload()
		if err != nil {
			shared.WriteJSONError(w, http.StatusInternalServerError, "failed to decompress archived payload")
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

// HandleMailSync ingests a bounded batch into the archive: POST /v1/mail/sync
//
// Resumable: the page cursor is stored in the archive. Repeat until done=true.
func (s *Server) HandleMailSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.mailReadableFor(r) {
		shared.WriteJSONError(w, http.StatusForbidden, "denied: data grant for agent access to messages is not allowed")
		return
	}
	s.handleMailSyncRequest(w, r, true)
}

// HandleAdminMailStatus is the admin UI status panel: GET /v1/admin/mail/status
// Skips the agent messages permission gate — the operator owns the archive.
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
// Same body and resumable semantics as /v1/mail/sync, without the agent gate.
func (s *Server) HandleAdminMailSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	s.handleMailSyncRequest(w, r, false)
}

func (s *Server) handleMailSyncRequest(w http.ResponseWriter, r *http.Request, requireAgentMessagesRead bool) {
	var req MailSyncRequest
	if r.Body != nil {
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil && err.Error() != "EOF" {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				writeBodyReadError(w, err)
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
	s.handleMailSyncPayload(w, r, req, requireAgentMessagesRead)
}

// handleMailSyncPayload runs one already-decoded sync request so callers
// that own their decode (the admin import route) cannot drift from the
// shared route mechanics (issue #227).
func (s *Server) handleMailSyncPayload(w http.ResponseWriter, r *http.Request, req MailSyncRequest, requireAgentMessagesRead bool) {
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
	// Admin sync is operator-owned; only the agent surface honors messages=read.
	// Ingest-only sync locations (the Takeout export) have no config entry —
	// agent reads then need messages read on some message-capable service.
	if requireAgentMessagesRead {
		if svc != nil {
			if !s.requestPermissions(r, svc).AllowsRead(mailPermType) {
				shared.WriteJSONError(w, http.StatusForbidden, "denied: data grant for agent access to messages is not allowed")
				return
			}
		} else if !s.mailReadableFor(r) {
			shared.WriteJSONError(w, http.StatusForbidden, "denied: data grant for agent access to messages is not allowed")
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
	if errors.Is(err, types.ErrMailConfig) {
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
				writeBodyReadError(w, err)
				return
			}
			shared.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}
	req.Service = connectors.TakeoutServiceID
	s.handleMailSyncPayload(w, r, req, false)
}

func writeMailJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

// mailArchiveHints returns pre-request instruction text when the user wants
// local archive search rather than a live provider listing.
func (s *Server) mailArchiveHints(r *http.Request, msg string) []string {
	if s.mail == nil || !s.mailReadableFor(r) {
		return nil
	}
	if !mentionsMailArchive(msg) {
		return nil
	}
	root := strings.TrimRight(s.baseURL, "/")
	return []string{`The user wants the local mail archive (full history search). Prefer:
GET ` + root + `/v1/mail/search?q=…&from=…&to=…&after=YYYY-MM-DD&before=YYYY-MM-DD&limit=50&sort=desc
GET ` + root + `/v1/mail/search?sort=asc&limit=1 — oldest archived message(s); use sort=asc for earliest/first/oldest questions
GET ` + root + `/v1/mail/messages/{service}/{id} — one archived message with body
GET ` + root + `/v1/mail/messages/{service}/{id}/raw — provider payload JSON
GET ` + root + `/v1/mail/status — archive size and sync cursor
POST ` + root + `/v1/mail/sync — ingest more mail (body: {"max_messages":200})
Default sort is newest-first (desc). For "oldest", "earliest", or "first" message questions use sort=asc (and limit=1 when only one answer is needed).
All ingested sources form one mail collection: search results merge on RFC Message-ID into multi-source entries. The same collection overview is at ` + root + `/v1/library/messages; use /v1/mail/search for full-history queries. Live provider listing is only needed for brand-new mail that is not yet synced.`}
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
