package admin

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/mail"
	"github.com/fudomunro/aegis/internal/shared"
)

func (h *Handlers) requireMailArchive(w http.ResponseWriter) *mail.Archive {
	if h.Mail == nil {
		shared.WriteJSONError(w, http.StatusNotFound,
			"mail archive disabled — start Aegis with a mail archive path (default: <data-dir>/mail-archive.db)")
		return nil
	}
	return h.Mail
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
func (h *Handlers) HandleAdminMailStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	a := h.requireMailArchive(w)
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
func (h *Handlers) HandleAdminMailSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	h.handleMailSyncRequest(w, r)
}

func (h *Handlers) handleMailSyncRequest(w http.ResponseWriter, r *http.Request) {
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
	h.handleMailSyncPayload(w, r, req)
}

// handleMailSyncPayload runs one already-decoded sync request so callers
// that own their decode (the admin import route) cannot drift from the
// shared route mechanics (issue #227).
func (h *Handlers) handleMailSyncPayload(w http.ResponseWriter, r *http.Request, req MailSyncRequest) {
	if h.requireMailArchive(w) == nil {
		return
	}
	// Pre-flight shape of this route is its own contract: a service that
	// cannot sync the archive is 501, an unknown one 404, not a fail-open
	// empty result. Ingest sources are connector-declared (issue #286).
	if _, live := h.Connectors[req.Service]; !live && h.MailIngests[req.Service] == nil {
		shared.WriteJSONError(w, http.StatusNotFound, "no connector for service "+req.Service)
		return
	}
	svc, svcOK := h.Host.ServiceRow(req.Service)
	if _, live := h.Connectors[req.Service]; live {
		if !svcOK || svc.Action == "block" {
			shared.WriteJSONError(w, http.StatusForbidden, "service blocked")
			return
		}
	}
	if h.MailIngests[req.Service] == nil {
		shared.WriteJSONError(w, http.StatusNotImplemented, "service does not support mail archive sync")
		return
	}

	// One ingest implementation, shared with the sync queue runner
	// (issue #227): the mechanics live in doMailIngest so the route and the
	// queue cannot drift. Path/query/reset are per-source knobs the ingest
	// interprets (issue #286).
	result, err := h.Host.RunMailIngest(r.Context(), req.Service, shared.MailIngestOptions{
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
func (h *Handlers) HandleAdminMailImport(w http.ResponseWriter, r *http.Request) {
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
	h.handleMailSyncPayload(w, r, req)
}

func writeMailJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}
