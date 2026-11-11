package library

import (
	"net/http"
	"strings"
)

// mailPermType is the permission key for the local mail archive surface.
// It shares the messages kind so admin already has a control for it.
const mailPermType = "messages"

// mailReadable reports whether any non-blocked service allows message reads
// (owner tier). Request-scoped callers use mailReadableFor.
func (h *Handlers) MailReadable() bool {
	return h.LibraryReadableFor(mailPermType)
}

// mailReadableFor reports whether the request may use the mail archive:
// the request key's data grant allows message reads and the owner tier
// has readable message sources.
func (h *Handlers) MailReadableFor(r *http.Request) bool {
	if !h.requestGrants(r).AllowsKindMethod(mailPermType, http.MethodGet) {
		return false
	}
	return h.MailReadable()
}

// mailArchiveHints returns pre-request instruction text when the user wants
// local archive search rather than a live provider listing (issue #311: one
// retrieval surface, the library API).
func (h *Handlers) MailArchiveHints(r *http.Request, msg string) []string {
	if h.Deps.MailArchive == nil || !h.Deps.MailArchive.Available() || !h.MailReadableFor(r) {
		return nil
	}
	if !mentionsMailArchive(msg) {
		return nil
	}
	root := strings.TrimRight(h.BaseURL, "/")
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
