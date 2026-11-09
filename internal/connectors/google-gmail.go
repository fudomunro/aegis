package connectors

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

// GoogleGmailConnector proxies the Gmail API and implements MessageProvider.
// It lists the user's recent messages as neutral Message values so agents can
// ask "what email have I received?" through /v1/library/messages. Message
// content stays on Gmail (non-Aegis canonical, issue #16). Read-only in
// this phase — agent write ops are not advertised (issue #61). SMS/WhatsApp
// are out of scope until a named OAuth-capable connector exists (issue #60).
// https://github.com/fudomunro/aegis/issues/60
type GoogleGmailConnector struct {
	host Host
}

// NewGoogleGmailConnector creates a connector wired to the given Host (issue #15 seam).
func NewGoogleGmailConnector(host Host) *GoogleGmailConnector {
	return &GoogleGmailConnector{host: host}
}

var (
	_ kinds.MessageProvider  = (*GoogleGmailConnector)(nil)
	_ kinds.DocumentProvider = (*GoogleGmailConnector)(nil)
)

// GoogleGmailServiceID is the connector/service ID used in config, tokens,
// sources[], and admin. Documented name is "Gmail".
const GoogleGmailServiceID = "google-gmail"

const GoogleGmailServiceName = "Gmail"

// GmailMailSource is the overview declaration: the live mailbox home
// (issue #16) — canonical rows, no overview parent.
var GmailMailSource = MailSource{
	ServiceID: GoogleGmailServiceID,
	Canonical: true,
}

// Default library window and caps. Gmail list endpoints return IDs only, so
// the library hydrates metadata per message up to a modest cap to keep
// "my inbox" answers useful without unbounded detail fetches.
const (
	googleGmailDefaultQuery   = "newer_than:30d"
	googleGmailListPageSize   = 25
	GoogleGmailLibraryCap     = 150
	googleGmailMetadataFormat = "metadata"
)

// GoogleGmailUpstreamBase returns the configured Gmail API base.
func (c *GoogleGmailConnector) GoogleGmailUpstreamBase() string {
	return c.host.UpstreamBaseFor(GoogleGmailServiceID, "https://gmail.googleapis.com/gmail/v1")
}

// Capabilities returns what Gmail kinds and operations are supported.
func (c *GoogleGmailConnector) Capabilities() shared.ServiceCapabilities {
	return shared.ServiceCapabilities{
		Kinds: []shared.KindCapability{
			{
				Name:        "messages",
				Description: "Messages — list mail messages/threads (metadata/snippet only by default; body stays on Gmail)",
				Operations: []shared.Operation{
					{
						Name:        "read_list",
						Description: "List the user's messages as neutral Message values (paginated). Permission key: messages.",
						Method:      "GET",
						Path:        "/me/messages",
						Parameters: []shared.Parameter{
							{Name: "q", In: "query", Type: "string", Description: "Gmail search query (default: newer_than:30d)"},
							{Name: "maxResults", In: "query", Type: "integer", Description: "List page size for IDs (1-500)", Default: googleGmailListPageSize},
							{Name: "pageSize", In: "query", Type: "integer", Description: "Alias for maxResults"},
							{Name: "pageToken", In: "query", Type: "string", Description: "Page token for pagination"},
						},
						Response: shared.ResponseSpec{
							Description: "Neutral messages: channel, subject, snippet, from/to, sent_at, direction, read, sources[]",
							Fields:      []string{"messages", "total"},
						},
					},
					{
						Name:        "get_by_id",
						Description: "Get a single message by service-local ID (metadata; format=full includes decoded body)",
						Method:      "GET",
						Path:        "/messages/{id}",
						Parameters: []shared.Parameter{
							{Name: "id", In: "path", Type: "string", Description: "Service-local message ID", Required: true},
							{Name: "format", In: "query", Type: "string", Description: "metadata or full", Default: googleGmailMetadataFormat},
						},
						Response: shared.ResponseSpec{
							Description: "Neutral message with service-local id and sources[]",
							Fields:      []string{"subject", "snippet", "from", "to", "sent_at", "sources"},
						},
					},
					{
						Name:        "list_attachments",
						Description: "List MIME attachments on one message (id, filename, mime_type, size)",
						Method:      "GET",
						Path:        "/messages/{id}/attachments",
						Parameters: []shared.Parameter{
							{Name: "id", In: "path", Type: "string", Description: "Service-local message ID", Required: true},
						},
						Response: shared.ResponseSpec{
							Description: "Attachment summaries for the message; fetch bytes at /messages/{id}/attachments/{attachmentId}",
							Fields:      []string{"message_id", "attachments", "total"},
						},
					},
					{
						Name:        "get_attachment",
						Description: "Fetch one attachment (Gmail attachment JSON: size + base64url data)",
						Method:      "GET",
						Path:        "/messages/{id}/attachments/{attachmentId}",
						Parameters: []shared.Parameter{
							{Name: "id", In: "path", Type: "string", Description: "Service-local message ID", Required: true},
							{Name: "attachmentId", In: "path", Type: "string", Description: "Attachment id from list_attachments or message extra.attachments", Required: true},
						},
						Response: shared.ResponseSpec{
							Description: "Attachment payload from Gmail",
							Fields:      []string{"size", "data", "attachmentId"},
						},
					},
					{
						Name:        "list_threads",
						Description: "List recent threads (id + snippet)",
						Method:      "GET",
						Path:        "/me/threads",
						Parameters: []shared.Parameter{
							{Name: "q", In: "query", Type: "string", Description: "Gmail search query"},
							{Name: "maxResults", In: "query", Type: "integer", Description: "Max threads per page", Default: googleGmailListPageSize},
							{Name: "pageToken", In: "query", Type: "string", Description: "Page token"},
						},
						Response: shared.ResponseSpec{
							Description: "Threads: id, snippet, message_count, sources[]",
							Fields:      []string{"threads", "total"},
						},
					},
					{
						Name:        "get_thread",
						Description: "Get one thread with its messages as neutral Message values",
						Method:      "GET",
						Path:        "/threads/{id}",
						Parameters: []shared.Parameter{
							{Name: "id", In: "path", Type: "string", Description: "Service-local thread ID", Required: true},
						},
						Response: shared.ResponseSpec{
							Description: "Thread messages in neutral Message shape",
							Fields:      []string{"id", "messages", "total"},
						},
					},
				},
			},
		},
	}
}

// SystemInstructions returns the system message injected when Gmail is referenced.
func (c *GoogleGmailConnector) SystemInstructions(baseURL string) string {
	root := strings.TrimRight(baseURL, "/")
	return fmt.Sprintf(`When the user's request involves Gmail, email, their inbox, or recent messages, use Aegis's unified data API. Do not call Gmail API directly.

GET %s/v1/library/messages — the user's mail across connected services (subject, snippet, from/to, sent_at, direction, sources[])
GET %s/v1/library/documents — attachments and life-admin files from mail and other sources (title, mime_type, correspondent, sources[])

sources[] records where each item lives (service + id) for sync. Fetch each message at /v1/services/{sources[].service}/messages/{sources[].id} — never send one service's id to another. Library lists carry snippets only.

Only when you need a full message body:
GET %s/v1/services/google-gmail/messages/{id} — one message; add ?format=full for the decoded body
GET %s/v1/services/google-gmail/threads/{id} — one thread with its messages

Attachments (certificates, PDFs, photos):
GET %s/v1/services/google-gmail/messages/{id}/attachments — list attachment ids, filenames, and mime types
GET %s/v1/services/google-gmail/messages/{id}/attachments/{attachmentId} — one attachment payload
Documents from mail use the same google-gmail source; sources[].blob_url points at the attachment bytes. Aegis does not store mailboxes or file bytes.

Message bodies stay on Gmail — Aegis does not store mailboxes. Cross-service merges prefer RFC Message-ID.`, root, root, root, root, root, root)
}

// HandleProxy processes a Gmail API request through the connector.
func (c *GoogleGmailConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	logger := slog.With("service", GoogleGmailServiceID, "method", r.Method, "path", remainingPath)

	token := c.host.GetValidToken(GoogleGmailServiceID, logger)
	if token == "" {
		shared.WriteJSONError(w, http.StatusUnauthorized,
			"no valid Gmail token — authorize at /v1/auth/"+GoogleGmailServiceID+"/login")
		return
	}

	if remainingPath == "/me/messages" || remainingPath == "/me/messages/" ||
		remainingPath == "/messages" || remainingPath == "/messages/" {
		if r.Method != http.MethodGet {
			shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		c.HandleListMessages(w, r)
		return
	}

	if remainingPath == "/me/threads" || remainingPath == "/me/threads/" ||
		remainingPath == "/threads" || remainingPath == "/threads/" {
		if r.Method != http.MethodGet {
			shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		c.HandleListThreads(w, r)
		return
	}

	if mid, aid, ok := matchGmailAttachmentRoute(remainingPath); ok {
		if r.Method != http.MethodGet {
			shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if aid == "" {
			c.HandleListAttachments(w, r, token, mid)
			return
		}
		c.HandleGetAttachment(w, r, token, mid, aid)
		return
	}

	if id, ok := GmailBareIDFromProxyPath(remainingPath, "messages"); ok {
		if r.Method != http.MethodGet {
			shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		c.HandleGetMessage(w, r, token, id)
		return
	}

	if id, ok := GmailBareIDFromProxyPath(remainingPath, "threads"); ok {
		if r.Method != http.MethodGet {
			shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		c.HandleGetThread(w, r, token, id)
		return
	}

	// Generic authenticated pass-through for other Gmail API paths under this base.
	StreamProxy(w, r, c.GoogleGmailUpstreamBase(), remainingPath, token, "Gmail", logger)
}

// GmailBareIDFromProxyPath extracts an id from /messages/{id}, /me/messages/{id},
// /threads/{id}, or /me/threads/{id}.
// Gmail id length bounds. Message/thread ids are short hex-like strings.
// Attachment ids are opaque and regularly exceed 400 characters (issue #207).
const (
	gmailMaxMessageIDLen    = 128
	gmailMaxAttachmentIDLen = 2048
)

// validGmailBareID reports whether id looks like a Gmail message/thread id.
// Identifiers from other services must never be forwarded to the Gmail API.
func validGmailBareID(id string) bool {
	return validGmailIDLen(id, gmailMaxMessageIDLen) && gmailIDCharset(id, false)
}

// validGmailAttachmentID accepts the longer opaque ids Gmail assigns to MIME
// parts (often 400+ chars, may include base64 '=' padding).
func validGmailAttachmentID(id string) bool {
	return validGmailIDLen(id, gmailMaxAttachmentIDLen) && gmailIDCharset(id, true)
}

func validGmailIDLen(id string, max int) bool {
	return id != "" && len(id) <= max
}

func gmailIDCharset(id string, attachment bool) bool {
	if strings.ContainsAny(id, "/\\ \t\r\n") {
		return false
	}
	if strings.Contains(id, ":") {
		return false
	}
	if id == "." || id == ".." || strings.Contains(id, "..") {
		return false
	}
	for _, r := range id {
		ok := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			r == '-' || r == '_' || r == '.'
		// Attachment ids are opaque base64/base64url and often exceed 400 chars.
		if attachment && (r == '=' || r == '+') {
			ok = true
		}
		if !ok {
			return false
		}
	}
	return true
}

func isGmailInvalidIDError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Invalid id value") ||
		strings.Contains(msg, "invalidArgument") ||
		strings.Contains(msg, "Not Found") ||
		strings.Contains(msg, "404")
}

const gmailQuotaMarker = "Quota exceeded"

// IsGmailQuotaError reports Google's per-user rate-quota failures. Core's
// archive ingest and sync runner classify provider errors through this,
// never through the quota body text (issue #286).
func IsGmailQuotaError(err error) bool {
	return err != nil && strings.Contains(err.Error(), gmailQuotaMarker)
}

// Archive-ingest knowledge for Gmail live sync into the local mail archive
// (issue #286; docs/mail-ingest.md). Core paces and defaults through these.

// GmailArchiveDefaultQuery pulls every mailbox the user can see, including
// spam/trash, so "all of my email" is actually complete.
const GmailArchiveDefaultQuery = "in:anywhere"

// GmailArchiveGetInterval paces full-message gets. Gmail's "Units per minute
// per user" budget is easy to exhaust when hydrating a whole page of IDs;
// without pacing, mid-page 403s used to skip those messages forever.
var GmailArchiveGetInterval = 1200 * time.Millisecond

// PaceGmailArchiveGet sleeps between full-message hydrates so a large page
// does not trip the per-user quota mid-page. A non-positive interval skips
// the wait (tests use this) and reports only the context state.
func PaceGmailArchiveGet(ctx context.Context) error {
	if GmailArchiveGetInterval <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(GmailArchiveGetInterval)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// GmailArchiveListPage is the provider ID page size during archive ingest.
const GmailArchiveListPage = 100

// GmailQuotaBackoff waits out a provider per-user rate quota window.
const GmailQuotaBackoff = 5 * time.Minute

// IngestMail implements MailIngest: one bounded list+hydrate run into the
// local mail archive (docs/mail-ingest.md). Each call lists up to
// MaxMessages IDs (continuing the stored page token) and stores full
// payloads. Callers repeat until Result.Done.
func (c *GoogleGmailConnector) IngestMail(ctx context.Context, sink MailSink, opts shared.MailIngestOptions) (shared.MailIngestResult, error) {
	res := shared.MailIngestResult{Service: GoogleGmailServiceID}
	maxMsgs := opts.MaxMessages
	if maxMsgs <= 0 {
		maxMsgs = 200
	}
	if maxMsgs > 1000 {
		maxMsgs = 1000
	}
	query := strings.TrimSpace(opts.Query)
	if query == "" {
		query = GmailArchiveDefaultQuery
	}
	res.Query = query

	logger := slog.With("service", GoogleGmailServiceID, "handler", "mail_archive_sync")
	token := c.host.GetValidToken(GoogleGmailServiceID, logger)
	if token == "" {
		res.Error = "no valid Gmail token"
		_ = sink.SaveCursor(GoogleGmailServiceID, shared.MailIngestCursor{Error: res.Error})
		return res, kinds.ErrUnauthorized
	}

	cursor, err := sink.LoadCursor(GoogleGmailServiceID)
	if err != nil {
		cursor = shared.MailIngestCursor{}
	}
	if opts.ResetCursor {
		cursor = shared.MailIngestCursor{}
	}
	pageToken := cursor.PageToken

	for res.Stored+res.Failed < maxMsgs {
		pageSize := GmailArchiveListPage
		if remaining := maxMsgs - res.Stored - res.Failed; remaining > 0 && remaining < pageSize {
			pageSize = remaining
		}
		values := url.Values{}
		values.Set("maxResults", fmt.Sprint(pageSize))
		if query != "" {
			values.Set("q", query)
		}
		if pageToken != "" {
			values.Set("pageToken", pageToken)
		}
		listURL := c.GoogleGmailUpstreamBase() + "/users/me/messages?" + values.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
		if err != nil {
			return c.finishMailSync(sink, res, cursor, err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := shared.HTTPClient.Do(req)
		if err != nil {
			return c.finishMailSync(sink, res, cursor, err)
		}
		data, err := readMailBody(resp)
		if err != nil {
			return c.finishMailSync(sink, res, cursor, err)
		}
		if resp.StatusCode != http.StatusOK {
			listErr := GmailListHTTPError("messages list", resp.StatusCode, data)
			// Keep the fetch page token so a quota blip retries this page.
			return c.finishMailSync(sink, res, cursor, listErr)
		}
		var raw GoogleGmailMessageList
		if err := json.Unmarshal(data, &raw); err != nil {
			return c.finishMailSync(sink, res, cursor, err)
		}
		// pageFetchToken is the token that produced this page. Only advance the
		// durable cursor after the whole page is stored (or permanently skipped).
		pageFetchToken := pageToken
		for _, item := range raw.Messages {
			if res.Stored+res.Failed >= maxMsgs {
				res.Truncated = true
				break
			}
			if item.ID == "" {
				continue
			}
			res.Listed++
			if err := c.storeGmailMessageArchive(ctx, sink, token, item.ID); err != nil {
				if IsGmailQuotaError(err) || ctx.Err() != nil {
					// Rate quota: stop now and leave the cursor on this page so
					// the remaining IDs are retried after the window resets.
					logger.Warn("mail archive sync paused", "id", item.ID, "error", err)
					res.Truncated = true
					retry := cursor
					retry.PageToken = pageFetchToken
					retry.Done = false
					return c.finishMailSync(sink, res, retry, err)
				}
				// Fail-open per message: one bad item must not drop the batch.
				logger.Warn("mail archive store failed", "id", item.ID, "error", err)
				res.Failed++
				continue
			}
			res.Stored++
			res.LastStored = item.ID
			if err := PaceGmailArchiveGet(ctx); err != nil {
				retry := cursor
				retry.PageToken = pageFetchToken
				retry.Done = false
				return c.finishMailSync(sink, res, retry, err)
			}
		}
		pageToken = raw.NextPageToken
		if pageToken == "" {
			res.Done = true
			break
		}
		if res.Stored+res.Failed >= maxMsgs {
			res.Truncated = true
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	cursor.PageToken = pageToken
	cursor.Done = res.Done
	cursor.LastSyncAt = now
	cursor.Error = res.Error
	if err := sink.SaveCursor(GoogleGmailServiceID, cursor); err != nil {
		logger.Warn("mail archive cursor save failed", "error", err)
	}
	return res, nil
}

func (c *GoogleGmailConnector) finishMailSync(sink MailSink, res shared.MailIngestResult, cursor shared.MailIngestCursor, err error) (shared.MailIngestResult, error) {
	res.Error = err.Error()
	if IsGmailQuotaError(err) {
		res.RetryAfter = GmailQuotaBackoff
	}
	cursor.LastSyncAt = time.Now().UTC().Format(time.RFC3339)
	cursor.Error = res.Error
	_ = sink.SaveCursor(GoogleGmailServiceID, cursor)
	return res, err
}

func (c *GoogleGmailConnector) storeGmailMessageArchive(ctx context.Context, sink MailSink, token, id string) error {
	raw, err := c.FetchGmailMessage(ctx, token, id, "full")
	if err != nil {
		return err
	}
	payload, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	msg := MapGoogleGmailMessage(raw, "full")
	return sink.Store(GoogleGmailServiceID, msg, payload)
}

func readMailBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// matchGmailAttachmentRoute reports whether remainingPath is the attachments
// subresource of a message. attachmentID is empty for the list form
// (messages/{mid}/attachments). IDs are raw path segments and may be foreign.
func matchGmailAttachmentRoute(remainingPath string) (messageID, attachmentID string, ok bool) {
	p := strings.Trim(remainingPath, "/")
	p = strings.TrimPrefix(p, "me/")
	if !strings.HasPrefix(p, "messages/") {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(p, "messages/"), "/")
	if len(parts) == 2 && parts[1] == "attachments" {
		return parts[0], "", true
	}
	if len(parts) == 3 && parts[1] == "attachments" {
		return parts[0], parts[2], true
	}
	return "", "", false
}

// GmailAttachmentDocumentID joins message and attachment ids into a path-safe
// library document id (the text route is a single path segment).
func GmailAttachmentDocumentID(messageID, attachmentID string) string {
	return messageID + "~" + attachmentID
}

// SplitGmailAttachmentDocumentID splits a composite document id.
func SplitGmailAttachmentDocumentID(id string) (messageID, attachmentID string, ok bool) {
	messageID, attachmentID, ok = strings.Cut(id, "~")
	if !ok || messageID == "" || attachmentID == "" {
		return "", "", false
	}
	return messageID, attachmentID, true
}

// HandleListAttachments returns attachment metadata for one message.
// Gmail has no attachments.list — summaries come from the full message payload.
func (c *GoogleGmailConnector) HandleListAttachments(w http.ResponseWriter, r *http.Request, token, messageID string) {
	if !validGmailBareID(messageID) {
		slog.Warn("gmail list attachments rejected non-gmail id", "id", messageID)
		shared.WriteJSONError(w, http.StatusNotFound,
			"not a Gmail message id — use sources[].service and sources[].id from the library list")
		return
	}
	raw, err := c.FetchGmailMessage(r.Context(), token, messageID, "full")
	if err != nil {
		if isGmailInvalidIDError(err) {
			shared.WriteJSONError(w, http.StatusNotFound,
				"message not found on Gmail — check sources[].service and sources[].id")
			return
		}
		if IsGmailQuotaError(err) {
			shared.WriteJSONError(w, http.StatusTooManyRequests,
				"Gmail quota exceeded — retry shortly or query the local archive at /v1/library/messages")
			return
		}
		slog.Warn("gmail list attachments failed", "id", messageID, "error", err)
		shared.WriteJSONError(w, http.StatusBadGateway, "failed to fetch message from Gmail")
		return
	}
	atts := GmailAttachmentSummaries(raw)
	if atts == nil {
		atts = []GmailAttachmentSummary{}
	}
	out, err := json.Marshal(map[string]any{
		"message_id":  raw.ID,
		"attachments": atts,
		"total":       len(atts),
	})
	if err != nil {
		shared.WriteJSONError(w, http.StatusInternalServerError, "failed to encode attachments")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}

// HandleGetAttachment fetches one MIME attachment via the Gmail attachments API.
func (c *GoogleGmailConnector) HandleGetAttachment(w http.ResponseWriter, r *http.Request, token, messageID, attachmentID string) {
	if !validGmailBareID(messageID) || !validGmailAttachmentID(attachmentID) {
		shared.WriteJSONError(w, http.StatusNotFound, "not a Gmail attachment id")
		return
	}
	upstreamURL := c.GoogleGmailUpstreamBase() + "/users/me/messages/" + url.PathEscape(messageID) +
		"/attachments/" + url.PathEscape(attachmentID)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstreamURL, nil)
	if err != nil {
		shared.WriteJSONError(w, http.StatusInternalServerError, "failed to build request")
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := shared.HTTPClient.Do(req)
	if err != nil {
		slog.Warn("gmail get attachment failed", "error", err)
		shared.WriteJSONError(w, http.StatusBadGateway, "failed to fetch attachment from Gmail")
		return
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		shared.WriteJSONError(w, http.StatusBadGateway, "failed to read attachment from Gmail")
		return
	}
	if resp.StatusCode == http.StatusNotFound {
		shared.WriteJSONError(w, http.StatusNotFound, "attachment not found on Gmail")
		return
	}
	if resp.StatusCode == http.StatusTooManyRequests || IsGmailQuotaError(fmt.Errorf("%s", data)) {
		shared.WriteJSONError(w, http.StatusTooManyRequests, "Gmail quota exceeded — retry shortly")
		return
	}
	if resp.StatusCode != http.StatusOK {
		slog.Warn("gmail get attachment bad status", "status", resp.StatusCode)
		shared.WriteJSONError(w, http.StatusBadGateway, "failed to fetch attachment from Gmail")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func GmailBareIDFromProxyPath(remainingPath, kind string) (string, bool) {
	p := remainingPath
	for _, prefix := range []string{"/me/" + kind + "/", "/" + kind + "/"} {
		if strings.HasPrefix(p, prefix) {
			rest := strings.Trim(strings.TrimPrefix(p, prefix), "/")
			// Only a single path segment is a bare id. Extra segments
			// (/attachments and similar) are not message GETs — do not
			// forward the joined path as a fake id.
			if rest == "" || strings.Contains(rest, "/") {
				return "", false
			}
			return rest, true
		}
	}
	return "", false
}

func (c *GoogleGmailConnector) HandleListMessages(w http.ResponseWriter, r *http.Request) {
	messages, truncated, err := c.ListMessagesWithQuery(r.Context(), r.URL.Query())
	if err != nil {
		if err == kinds.ErrUnauthorized {
			shared.WriteJSONError(w, http.StatusUnauthorized,
				"no valid Gmail token — authorize at /v1/auth/"+GoogleGmailServiceID+"/login")
			return
		}
		slog.Warn("gmail list messages failed", "error", err)
		shared.WriteJSONError(w, http.StatusBadGateway, "failed to fetch messages from Gmail")
		return
	}
	if messages == nil {
		messages = []kinds.Message{}
	}
	result := kinds.MessageList{
		Messages:  messages,
		Total:     len(messages),
		Synced:    true,
		Truncated: truncated,
		ByService: map[string]kinds.LibraryServiceStatus{
			GoogleGmailServiceID: {
				Status:    kinds.LibraryStatusOK,
				Count:     len(messages),
				Truncated: truncated,
			},
		},
	}
	data, err := json.Marshal(result)
	if err != nil {
		shared.WriteJSONError(w, http.StatusInternalServerError, "failed to build messages list")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func (c *GoogleGmailConnector) HandleListThreads(w http.ResponseWriter, r *http.Request) {
	token := c.host.GetValidToken(GoogleGmailServiceID, slog.Default())
	if token == "" {
		shared.WriteJSONError(w, http.StatusUnauthorized,
			"no valid Gmail token — authorize at /v1/auth/"+GoogleGmailServiceID+"/login")
		return
	}
	threads, truncated, err := c.ListThreads(r.Context(), token, r.URL.Query())
	if err != nil {
		slog.Warn("gmail list threads failed", "error", err)
		shared.WriteJSONError(w, http.StatusBadGateway, "failed to fetch threads from Gmail")
		return
	}
	if threads == nil {
		threads = []map[string]any{}
	}
	result := map[string]any{
		"threads":   threads,
		"total":     len(threads),
		"truncated": truncated,
		"by_service": map[string]kinds.LibraryServiceStatus{
			GoogleGmailServiceID: {Status: kinds.LibraryStatusOK, Count: len(threads), Truncated: truncated},
		},
	}
	data, err := json.Marshal(result)
	if err != nil {
		shared.WriteJSONError(w, http.StatusInternalServerError, "failed to build threads list")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

type googleGmailThreadListRef struct {
	ID        string `json:"id"`
	Snippet   string `json:"snippet"`
	HistoryID string `json:"historyId"`
}

type googleGmailThreadList struct {
	Threads            []googleGmailThreadListRef `json:"threads"`
	NextPageToken      string                     `json:"nextPageToken"`
	ResultSizeEstimate int                        `json:"resultSizeEstimate"`
}

func (c *GoogleGmailConnector) ListThreads(ctx context.Context, token string, q url.Values) ([]map[string]any, bool, error) {
	query := ""
	pageToken := ""
	pageSize := googleGmailListPageSize
	if q != nil {
		query = strings.TrimSpace(q.Get("q"))
		pageToken = q.Get("pageToken")
		if v := strings.TrimSpace(q.Get("maxResults")); v != "" {
			if n, err := ParseIntLoose(v); err == nil && n > 0 {
				pageSize = n
			}
		}
	}
	var out []map[string]any
	truncated := false
	for page := 0; page < shared.LibraryPageCap; page++ {
		values := url.Values{}
		values.Set("maxResults", fmt.Sprint(pageSize))
		if query != "" {
			values.Set("q", query)
		}
		if pageToken != "" {
			values.Set("pageToken", pageToken)
		}
		u := c.GoogleGmailUpstreamBase() + "/users/me/threads?" + values.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, false, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := shared.HTTPClient.Do(req)
		if err != nil {
			return nil, false, err
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, false, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, false, GmailListHTTPError("threads list", resp.StatusCode, data)
		}
		var raw googleGmailThreadList
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, false, err
		}
		for _, th := range raw.Threads {
			if th.ID == "" {
				continue
			}
			out = append(out, map[string]any{
				"id":        th.ID,
				"snippet":   th.Snippet,
				"historyId": th.HistoryID,
				"source": map[string]string{
					"service":      GoogleGmailServiceID,
					"service_name": GoogleGmailServiceName,
					"id":           th.ID,
					"url":          "https://mail.google.com/mail/u/0/#all/" + th.ID,
				},
			})
		}
		pageToken = raw.NextPageToken
		if pageToken == "" {
			break
		}
		if page == shared.LibraryPageCap-1 && pageToken != "" {
			truncated = true
		}
	}
	return out, truncated, nil
}

func (c *GoogleGmailConnector) HandleGetMessage(w http.ResponseWriter, r *http.Request, token, id string) {
	format := strings.TrimSpace(r.URL.Query().Get("format"))
	if format == "" {
		format = googleGmailMetadataFormat
	}
	if !validGmailBareID(id) {
		// Wrong-service id (or a concatenated path). Do not call Gmail.
		slog.Warn("gmail get message rejected non-gmail id", "id", id)
		shared.WriteJSONError(w, http.StatusNotFound,
			"not a Gmail message id — use sources[].service and sources[].id from the library list")
		return
	}
	raw, err := c.FetchGmailMessage(r.Context(), token, id, format)
	if err != nil {
		if isGmailInvalidIDError(err) {
			slog.Warn("gmail get message unknown id", "id", id)
			shared.WriteJSONError(w, http.StatusNotFound,
				"message not found on Gmail — check sources[].service and sources[].id")
			return
		}
		if IsGmailQuotaError(err) {
			shared.WriteJSONError(w, http.StatusTooManyRequests,
				"Gmail quota exceeded — retry shortly or query the local archive at /v1/library/messages")
			return
		}
		slog.Warn("gmail get message failed", "id", id, "error", err)
		shared.WriteJSONError(w, http.StatusBadGateway, "failed to fetch message from Gmail")
		return
	}
	msg := MapGoogleGmailMessage(raw, format)
	data, err := json.Marshal(msg)
	if err != nil {
		shared.WriteJSONError(w, http.StatusInternalServerError, "failed to encode message")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func (c *GoogleGmailConnector) HandleGetThread(w http.ResponseWriter, r *http.Request, token, id string) {
	upstreamURL := c.GoogleGmailUpstreamBase() + "/users/me/threads/" + url.PathEscape(id) +
		"?format=metadata&metadataHeaders=" + strings.Join(GmailMetadataHeaders(), "&metadataHeaders=")
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstreamURL, nil)
	if err != nil {
		shared.WriteJSONError(w, http.StatusInternalServerError, "failed to build request")
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := shared.HTTPClient.Do(req)
	if err != nil {
		shared.WriteJSONError(w, http.StatusBadGateway, "Gmail thread request failed")
		return
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		shared.WriteJSONError(w, http.StatusBadGateway, "failed to read Gmail thread response")
		return
	}
	if resp.StatusCode != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(data)
		return
	}
	var raw googleGmailThread
	if err := json.Unmarshal(data, &raw); err != nil {
		shared.WriteJSONError(w, http.StatusBadGateway, "failed to parse Gmail thread response")
		return
	}
	messages := make([]kinds.Message, 0, len(raw.Messages))
	for _, m := range raw.Messages {
		messages = append(messages, MapGoogleGmailMessage(m, googleGmailMetadataFormat))
	}
	out, err := json.Marshal(map[string]any{
		"id":       raw.ID,
		"messages": messages,
		"total":    len(messages),
	})
	if err != nil {
		shared.WriteJSONError(w, http.StatusInternalServerError, "failed to encode thread")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}

// ListMessages implements MessageProvider. Fetches recent Gmail messages and
// hydrates headers into neutral Message metadata.
func (c *GoogleGmailConnector) ListMessages(ctx context.Context) ([]kinds.Message, bool, error) {
	return c.ListMessagesWithQuery(ctx, nil)
}

func (c *GoogleGmailConnector) ListMessagesWithQuery(ctx context.Context, q url.Values) ([]kinds.Message, bool, error) {
	token := c.host.GetValidToken(GoogleGmailServiceID, slog.Default())
	if token == "" {
		return nil, false, kinds.ErrUnauthorized
	}
	query := googleGmailDefaultQuery
	if q != nil {
		if v := strings.TrimSpace(q.Get("q")); v != "" {
			query = v
		}
	}
	pageToken := ""
	if q != nil {
		pageToken = q.Get("pageToken")
	}
	pageSize := googleGmailListPageSize
	if q != nil {
		if v := strings.TrimSpace(q.Get("maxResults")); v != "" {
			if n, err := ParseIntLoose(v); err == nil && n > 0 {
				pageSize = n
			}
		} else if v := strings.TrimSpace(q.Get("pageSize")); v != "" {
			if n, err := ParseIntLoose(v); err == nil && n > 0 {
				pageSize = n
			}
		}
	}

	// Library vs proxy caps: proxy may request more pages; library stays modest.
	hydrateCap := GoogleGmailLibraryCap
	if q != nil && q.Get("maxHydrate") != "" {
		if n, err := ParseIntLoose(q.Get("maxHydrate")); err == nil && n > 0 {
			hydrateCap = n
		}
	}

	var all []kinds.Message
	truncated := false
	hydrated := 0
	for page := 0; page < shared.LibraryPageCap; page++ {
		values := url.Values{}
		values.Set("maxResults", fmt.Sprint(pageSize))
		if query != "" {
			values.Set("q", query)
		}
		if pageToken != "" {
			values.Set("pageToken", pageToken)
		}
		listURL := c.GoogleGmailUpstreamBase() + "/users/me/messages?" + values.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
		if err != nil {
			return nil, false, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := shared.HTTPClient.Do(req)
		if err != nil {
			return nil, false, err
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, false, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, false, GmailListHTTPError("messages list", resp.StatusCode, data)
		}
		var raw GoogleGmailMessageList
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, false, err
		}
		for _, item := range raw.Messages {
			if item.ID == "" {
				continue
			}
			if hydrated >= hydrateCap {
				truncated = true
				break
			}
			msg, err := c.FetchAndMapMessage(ctx, token, item.ID)
			if err != nil {
				// Fail-open per message: one unreadable item must not empty the library.
				slog.Warn("gmail message fetch failed", "id", item.ID, "error", err)
				continue
			}
			if msg.Sources[0].ID == "" && msg.Subject == "" && msg.EntityID == "" {
				continue
			}
			all = append(all, msg)
			hydrated++
		}
		if truncated {
			break
		}
		pageToken = raw.NextPageToken
		if pageToken == "" {
			break
		}
		if hydrated >= hydrateCap || (page == shared.LibraryPageCap-1 && pageToken != "") {
			truncated = true
		}
	}
	return all, truncated, nil
}

func (c *GoogleGmailConnector) FetchAndMapMessage(ctx context.Context, token, id string) (kinds.Message, error) {
	raw, err := c.FetchGmailMessage(ctx, token, id, googleGmailMetadataFormat)
	if err != nil {
		return kinds.Message{}, err
	}
	return MapGoogleGmailMessage(raw, googleGmailMetadataFormat), nil
}

func (c *GoogleGmailConnector) FetchGmailMessage(ctx context.Context, token, id, format string) (GoogleGmailMessage, error) {
	if format == "" {
		format = googleGmailMetadataFormat
	}
	u := c.GoogleGmailUpstreamBase() + "/users/me/messages/" + url.PathEscape(id) + "?format=" + url.QueryEscape(format)
	if format == googleGmailMetadataFormat || format == "raw" {
		for _, h := range GmailMetadataHeaders() {
			u += "&metadataHeaders=" + url.QueryEscape(h)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return GoogleGmailMessage{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := shared.HTTPClient.Do(req)
	if err != nil {
		return GoogleGmailMessage{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return GoogleGmailMessage{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return GoogleGmailMessage{}, GmailListHTTPError("message get", resp.StatusCode, data)
	}
	var raw GoogleGmailMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return GoogleGmailMessage{}, err
	}
	return raw, nil
}

type GoogleGmailMessageListRef struct {
	ID       string `json:"id"`
	ThreadID string `json:"threadId"`
}

type GoogleGmailMessageList struct {
	Messages           []GoogleGmailMessageListRef `json:"messages"`
	NextPageToken      string                      `json:"nextPageToken"`
	ResultSizeEstimate int                         `json:"resultSizeEstimate"`
}

type googleGmailThread struct {
	ID       string               `json:"id"`
	Snippet  string               `json:"snippet"`
	Messages []GoogleGmailMessage `json:"messages"`
}

type gmailMessageHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type gmailMessageBody struct {
	Data         string `json:"data"`
	AttachmentID string `json:"attachmentId"`
	Size         int    `json:"size"`
}

// gmailMessagePart is Gmail's MessagePart; payload is also a MessagePart so
// nested multiparts (multipart/mixed wrapping multipart/alternative) stay walkable.
type gmailMessagePart struct {
	MimeType string               `json:"mimeType"`
	Filename string               `json:"filename"`
	Body     gmailMessageBody     `json:"body"`
	Headers  []gmailMessageHeader `json:"headers"`
	Parts    []gmailMessagePart   `json:"parts"`
}

type GoogleGmailMessage struct {
	ID           string            `json:"id"`
	ThreadID     string            `json:"threadId"`
	LabelIDs     []string          `json:"labelIds"`
	Snippet      string            `json:"snippet"`
	InternalDate string            `json:"internalDate"`
	Payload      *gmailMessagePart `json:"payload"`
}

func GmailMetadataHeaders() []string {
	return []string{"From", "To", "Cc", "Subject", "Date", "Message-ID"}
}

func GmailHeaderValue(h GoogleGmailMessage, name string) string {
	if h.Payload == nil {
		return ""
	}
	for _, hdr := range h.Payload.Headers {
		if strings.EqualFold(hdr.Name, name) {
			return hdr.Value
		}
	}
	return ""
}

func GmailInternalDateRFC3339(ms string) string {
	ms = strings.TrimSpace(ms)
	if ms == "" {
		return ""
	}
	var n int64
	if _, err := fmt.Sscan(ms, &n); err != nil || n <= 0 {
		return ""
	}
	return time.UnixMilli(n).UTC().Format(time.RFC3339)
}

func GmailHeaderDateRFC3339(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	layouts := []string{
		time.RFC1123Z,
		time.RFC1123,
		"Mon, 2 Jan 2006 15:04:05 -0700",
		"Mon, 2 Jan 2006 15:04:05 MST",
		"2 Jan 2006 15:04:05 -0700",
		time.RFC3339,
		time.RFC3339Nano,
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	if t, err := mail.ParseDate(raw); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return ""
}

// MapGmailAddressHeader parses a single address or address list into Contacts.
func MapGmailAddressHeader(raw string) []kinds.Contact {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	addrs, err := mail.ParseAddressList(raw)
	if err != nil {
		// Fail-open: keep a crude name/email split rather than dropping the party.
		if strings.Contains(raw, "@") {
			c := kinds.NewContact(GoogleGmailServiceID, GoogleGmailServiceName, "", "", []string{raw}, nil)
			return []kinds.Contact{c}
		}
		c := kinds.NewContact(GoogleGmailServiceID, GoogleGmailServiceName, "", raw, nil, nil)
		return []kinds.Contact{c}
	}
	out := make([]kinds.Contact, 0, len(addrs))
	for _, a := range addrs {
		if a == nil {
			continue
		}
		var emails []string
		if a.Address != "" {
			emails = []string{a.Address}
		}
		out = append(out, kinds.NewContact(GoogleGmailServiceID, GoogleGmailServiceName, "", a.Name, emails, nil))
	}
	return out
}

func GmailDirectionFromLabels(labels []string) string {
	has := func(l string) bool {
		for _, x := range labels {
			if strings.EqualFold(x, l) {
				return true
			}
		}
		return false
	}
	switch {
	case has("SENT"):
		return "outbound"
	case has("INBOX"):
		return "inbound"
	default:
		return "unknown"
	}
}

func GmailMailboxFromLabels(labels []string) string {
	for _, l := range labels {
		switch strings.ToUpper(l) {
		case "INBOX", "SENT", "DRAFT", "SPAM", "TRASH", "STARRED", "IMPORTANT", "UNREAD":
			return strings.ToUpper(l)
		}
	}
	return ""
}

func GmailReadFromLabels(labels []string) bool {
	for _, l := range labels {
		if strings.EqualFold(l, "UNREAD") {
			return false
		}
	}
	return true
}

// GmailAttachmentSummary is one MIME attachment on a message.
type GmailAttachmentSummary struct {
	ID       string `json:"id"`
	Filename string `json:"filename,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	Size     int    `json:"size,omitempty"`
}

// GmailAttachmentSummaries walks the MIME tree for parts that need a separate
// attachment fetch (body.attachmentId). Nested multiparts are included so
// typical multipart/mixed + multipart/alternative mail is fully listed.
func GmailAttachmentSummaries(raw GoogleGmailMessage) []GmailAttachmentSummary {
	if raw.Payload == nil {
		return nil
	}
	var out []GmailAttachmentSummary
	var walk func(part gmailMessagePart)
	walk = func(part gmailMessagePart) {
		if id := strings.TrimSpace(part.Body.AttachmentID); id != "" {
			out = append(out, GmailAttachmentSummary{
				ID:       id,
				Filename: gmailPartFilename(part),
				MimeType: strings.TrimSpace(part.MimeType),
				Size:     part.Body.Size,
			})
		}
		for _, child := range part.Parts {
			walk(child)
		}
	}
	walk(*raw.Payload)
	return out
}

func gmailPartFilename(part gmailMessagePart) string {
	if fn := strings.TrimSpace(part.Filename); fn != "" {
		return fn
	}
	for _, h := range part.Headers {
		if !strings.EqualFold(h.Name, "Content-Disposition") && !strings.EqualFold(h.Name, "Content-Type") {
			continue
		}
		if _, params, err := mime.ParseMediaType(h.Value); err == nil {
			if fn := strings.TrimSpace(params["filename"]); fn != "" {
				return fn
			}
			if fn := strings.TrimSpace(params["name"]); fn != "" {
				return fn
			}
		}
	}
	return ""
}

// NeutralAttachmentDocument maps one attachment to the neutral Document shape
// used by /v1/library/documents. Source is google-gmail (same connector as the
// parent message); bytes stay on Gmail via sources[].blob_url.
func (c *GoogleGmailConnector) NeutralAttachmentDocument(raw GoogleGmailMessage, att GmailAttachmentSummary) kinds.Document {
	proxyRoot := strings.TrimRight(c.host.BaseURL(), "/") + "/v1/services/" + GoogleGmailServiceID
	docID := GmailAttachmentDocumentID(raw.ID, att.ID)
	src := kinds.DocumentSource{
		Service:     GoogleGmailServiceID,
		ServiceName: GoogleGmailServiceName,
		ID:          docID,
		URL:         GmailMessageURL(raw.ID),
		Canonical:   true,
		BlobURL:     proxyRoot + "/messages/" + url.PathEscape(raw.ID) + "/attachments/" + url.PathEscape(att.ID),
	}
	title := att.Filename
	if title == "" {
		title = strings.TrimSpace(GmailHeaderValue(raw, "Subject"))
	}
	if title == "" {
		title = att.ID
	}
	sentAt := GmailInternalDateRFC3339(raw.InternalDate)
	if sentAt == "" {
		sentAt = GmailHeaderDateRFC3339(GmailHeaderValue(raw, "Date"))
	}
	d := kinds.Document{
		Title:        title,
		MimeType:     att.MimeType,
		CreatedAt:    sentAt,
		DocumentType: "email-attachment",
		Excerpt:      strings.TrimSpace(raw.Snippet),
		EntityID:     docID,
		Sources:      []kinds.DocumentSource{src},
	}
	fromList := MapGmailAddressHeader(GmailHeaderValue(raw, "From"))
	if len(fromList) > 0 {
		f := fromList[0]
		if f.Name != "" {
			d.Correspondent = f.Name
		} else if len(f.Emails) > 0 {
			d.Correspondent = f.Emails[0]
		}
		d.People = []kinds.Contact{f}
	}
	return d
}

// MapGoogleGmailMessage converts a Gmail message into the neutral shape.
func MapGoogleGmailMessage(raw GoogleGmailMessage, format string) kinds.Message {
	fromList := MapGmailAddressHeader(GmailHeaderValue(raw, "From"))
	toList := MapGmailAddressHeader(GmailHeaderValue(raw, "To"))
	ccList := MapGmailAddressHeader(GmailHeaderValue(raw, "Cc"))
	subject := strings.TrimSpace(GmailHeaderValue(raw, "Subject"))
	if decoded, err := new(mime.WordDecoder).DecodeHeader(subject); err == nil {
		subject = strings.TrimSpace(decoded)
	}
	messageID := kinds.NormalizeMessageID(GmailHeaderValue(raw, "Message-ID"))
	sentAt := GmailInternalDateRFC3339(raw.InternalDate)
	if sentAt == "" {
		sentAt = GmailHeaderDateRFC3339(GmailHeaderValue(raw, "Date"))
	}

	var from *kinds.Contact
	if len(fromList) > 0 {
		f := fromList[0]
		from = &f
	}

	src := kinds.MessageSource{
		Service:     GoogleGmailServiceID,
		ServiceName: GoogleGmailServiceName,
		ID:          raw.ID,
		ThreadID:    raw.ThreadID,
		URL:         GmailMessageURL(raw.ID),
		Mailbox:     GmailMailboxFromLabels(raw.LabelIDs),
		Canonical:   true,
	}
	extra := map[string]any{}
	if messageID != "" {
		extra["message_id"] = messageID
	}
	if raw.Snippet != "" {
		extra["snippet"] = raw.Snippet
	}
	if atts := GmailAttachmentSummaries(raw); len(atts) > 0 {
		extra["attachments"] = atts
	}
	if len(extra) > 0 {
		src.Extra = extra
	}

	msg := kinds.Message{
		Channel:    kinds.MessageChannelEmail,
		Subject:    subject,
		Snippet:    raw.Snippet,
		From:       from,
		To:         toList,
		CC:         ccList,
		SentAt:     sentAt,
		ReceivedAt: sentAt,
		Direction:  GmailDirectionFromLabels(raw.LabelIDs),
		Read:       GmailReadFromLabels(raw.LabelIDs),
		ThreadID:   raw.ThreadID,
		EntityID:   messageID,
		Labels:     append([]string(nil), raw.LabelIDs...),
		Sources:    []kinds.MessageSource{src},
	}
	if format == "full" {
		msg.Body = GmailDecodeBody(raw)
	}
	return msg
}

func GmailMessageURL(id string) string {
	if id == "" {
		return ""
	}
	return "https://mail.google.com/mail/u/0/#all/" + id
}

// GmailDecodeBody walks the Gmail payload for text/plain (fallback text/html
// stripped of tags is intentionally not done — agents can fetch HTML via proxy).
func GmailDecodeBody(raw GoogleGmailMessage) string {
	if raw.Payload == nil {
		return ""
	}
	if data := strings.TrimSpace(raw.Payload.Body.Data); data != "" {
		return GmailBase64Decode(data)
	}
	for _, part := range raw.Payload.Parts {
		if strings.HasPrefix(strings.ToLower(part.MimeType), "text/plain") {
			if data := strings.TrimSpace(part.Body.Data); data != "" {
				return GmailBase64Decode(data)
			}
		}
	}
	for _, part := range raw.Payload.Parts {
		if strings.HasPrefix(strings.ToLower(part.MimeType), "text/") {
			if data := strings.TrimSpace(part.Body.Data); data != "" {
				return GmailBase64Decode(data)
			}
		}
	}
	return ""
}

func GmailBase64Decode(data string) string {
	// Gmail uses base64url without padding.
	data = strings.ReplaceAll(data, "-", "+")
	data = strings.ReplaceAll(data, "_", "/")
	if m := len(data) % 4; m != 0 {
		data += strings.Repeat("=", 4-m)
	}
	out, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return ""
	}
	return string(out)
}

// GmailListHTTPError keeps the operator-facing reason (API disabled, quota,
// insufficient scope) instead of a bare status code. Fail-open callers log this.
func GmailListHTTPError(op string, status int, body []byte) error {
	snippet := strings.TrimSpace(string(body))
	if len(snippet) > 400 {
		snippet = snippet[:400]
	}
	if strings.Contains(snippet, "accessNotConfigure") ||
		strings.Contains(snippet, "has not been used in project") ||
		strings.Contains(snippet, "it is disabled") {
		return fmt.Errorf("gmail %s returned %d: Gmail API is not enabled on this Google Cloud project — enable https://console.developers.google.com/apis/api/gmail.googleapis.com/overview then retry", op, status)
	}
	if snippet == "" {
		return fmt.Errorf("gmail %s returned %d", op, status)
	}
	return fmt.Errorf("gmail %s returned %d: %s", op, status, snippet)
}

// ParseIntLoose parses a positive-ish integer from a query param.
func ParseIntLoose(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// ListUserDocuments implements DocumentProvider. Each MIME attachment on
// recent mail becomes a neutral Document whose source is google-gmail — the
// same connector as the parent message — so case-file evidence stays linked.
// Transient upstream errors keep partial results (fail-open) so one bad page
// does not report "failed to fetch documents" and drop every PDF already found.
func (c *GoogleGmailConnector) ListUserDocuments(ctx context.Context) ([]kinds.Document, bool, error) {
	token := c.host.GetValidToken(GoogleGmailServiceID, slog.Default())
	if token == "" {
		return nil, false, kinds.ErrUnauthorized
	}
	var out []kinds.Document
	truncated := false
	pageToken := ""
	hydrated := 0
	for page := 0; page < shared.LibraryPageCap; page++ {
		values := url.Values{}
		values.Set("maxResults", fmt.Sprint(googleGmailListPageSize))
		values.Set("q", googleGmailDefaultQuery)
		if pageToken != "" {
			values.Set("pageToken", pageToken)
		}
		listURL := c.GoogleGmailUpstreamBase() + "/users/me/messages?" + values.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
		if err != nil {
			return gmailDocumentsResult(out, truncated, hydrated, err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := shared.HTTPClient.Do(req)
		if err != nil {
			return gmailDocumentsResult(out, truncated, hydrated, err)
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return gmailDocumentsResult(out, truncated, hydrated, err)
		}
		if resp.StatusCode != http.StatusOK {
			return gmailDocumentsResult(out, truncated, hydrated, GmailListHTTPError("messages list", resp.StatusCode, data))
		}
		var raw GoogleGmailMessageList
		if err := json.Unmarshal(data, &raw); err != nil {
			return gmailDocumentsResult(out, truncated, hydrated, err)
		}
		for _, item := range raw.Messages {
			if item.ID == "" {
				continue
			}
			if hydrated >= GoogleGmailLibraryCap {
				truncated = true
				break
			}
			// format=full matches the content-transport attachment list, which
			// is what surfaces part-level attachmentIds for real MIME mail.
			msg, err := c.FetchGmailMessage(ctx, token, item.ID, "full")
			if err != nil {
				// Fail-open per message: one unreadable item must not empty the list.
				slog.Warn("gmail message fetch for attachments failed", "id", item.ID, "error", err)
				hydrated++
				continue
			}
			for _, att := range GmailAttachmentSummaries(msg) {
				out = append(out, c.NeutralAttachmentDocument(msg, att))
			}
			hydrated++
		}
		if truncated {
			break
		}
		pageToken = raw.NextPageToken
		if pageToken == "" {
			break
		}
		if hydrated >= GoogleGmailLibraryCap || (page == shared.LibraryPageCap-1 && pageToken != "") {
			truncated = true
		}
	}
	return out, truncated, nil
}

// gmailDocumentsResult keeps attachments already collected when a later page
// fails. The library aggregate drops items whenever err != nil, so partial
// success must report a clean truncated list instead of FailMsg.
func gmailDocumentsResult(out []kinds.Document, truncated bool, hydrated int, err error) ([]kinds.Document, bool, error) {
	if err == nil {
		return out, truncated, nil
	}
	if len(out) > 0 || hydrated > 0 {
		slog.Warn("gmail documents list degraded; keeping partial results", "count", len(out), "hydrated", hydrated, "error", err)
		return out, true, nil
	}
	return out, truncated, err
}

// FetchDocumentText implements DocumentProvider. Binary attachments have no
// extractable text without OCR (which Aegis does not run) — those return an
// empty string. Inline or fetched text attachments are decoded when present.
func (c *GoogleGmailConnector) FetchDocumentText(ctx context.Context, id string) (string, bool, error) {
	messageID, attachmentID, ok := SplitGmailAttachmentDocumentID(id)
	if !ok || !validGmailBareID(messageID) || !validGmailAttachmentID(attachmentID) {
		return "", false, fmt.Errorf("not a Gmail attachment document id")
	}
	token := c.host.GetValidToken(GoogleGmailServiceID, slog.Default())
	if token == "" {
		return "", false, kinds.ErrUnauthorized
	}
	raw, err := c.FetchGmailMessage(ctx, token, messageID, "full")
	if err != nil {
		return "", false, err
	}
	if text, found := gmailAttachmentInlineText(raw, attachmentID); found {
		return text, false, nil
	}
	return "", false, nil
}

func gmailAttachmentInlineText(raw GoogleGmailMessage, attachmentID string) (string, bool) {
	if raw.Payload == nil {
		return "", false
	}
	var walk func(part gmailMessagePart) (string, bool)
	walk = func(part gmailMessagePart) (string, bool) {
		if strings.TrimSpace(part.Body.AttachmentID) == attachmentID {
			if data := strings.TrimSpace(part.Body.Data); data != "" {
				return GmailBase64Decode(data), true
			}
			return "", true
		}
		for _, child := range part.Parts {
			if text, found := walk(child); found {
				return text, true
			}
		}
		return "", false
	}
	return walk(*raw.Payload)
}

// Host returns the runtime host for mail-archive sync (package main).
func (c *GoogleGmailConnector) Host() Host { return c.host }
