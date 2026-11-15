package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

// MicrosoftMailConnector proxies Microsoft Graph mail and implements
// MessageProvider plus the MailIngest seam (issue #399). It lists the
// user's Microsoft 365 / Outlook.com mail as neutral Message values for
// the unified library and feeds the local mail archive one bounded batch
// at a time — the third MailIngest implementation beside the Gmail live
// walk and the Google Takeout export (issue #286). Message content stays
// on Microsoft Graph (non-Aegis canonical). Read-only in this phase.
type MicrosoftMailConnector struct {
	host Host
}

// NewMicrosoftMailConnector creates a connector wired to the given Host.
func NewMicrosoftMailConnector(host Host) *MicrosoftMailConnector {
	return &MicrosoftMailConnector{host: host}
}

// MicrosoftMailServiceID is the connector/service ID used in config,
// tokens, sources[], and admin. Documented name is "Microsoft Mail".
const MicrosoftMailServiceID = "microsoft-mail"

const MicrosoftMailServiceName = "Microsoft Mail"

// MicrosoftMailSource is the overview declaration: the live mailbox home
// (issue #16) — canonical rows, no overview parent.
var MicrosoftMailSource = MailSource{
	ServiceID: MicrosoftMailServiceID,
	Canonical: true,
}

// microsoftMailSelect is the Graph projection needed for neutral messages.
const microsoftMailSelect = "id,internetMessageId,conversationId,subject,bodyPreview,body,from,toRecipients,ccRecipients,sentDateTime,receivedDateTime,isRead,isDraft,parentFolderId,webLink,hasAttachments"

// Default library window and caps. The Graph list response carries full
// metadata per message (no per-id hydrate), so the library cap bounds the
// listing directly.
const (
	MicrosoftMailLibraryCap      = 150
	MicrosoftMailArchiveListPage = 100
	MicrosoftMailArchiveDefault  = 200
	MicrosoftMailArchiveMax      = 1000
	// MicrosoftMailArchiveMaxPages bounds one ingest run's page walk so an
	// endless empty-page feed cannot spin the batch loop.
	MicrosoftMailArchiveMaxPages = 100
	// MicrosoftMailThrottleBackoff waits out a Graph 429 when no Retry-After
	// header is present.
	MicrosoftMailThrottleBackoff = 5 * time.Minute
)

// microsoftMailBodyLimit bounds one hydrated message (library detail and
// archive ingest). Message bodies are text/HTML; anything larger is rare
// and stays reachable on the source service.
const microsoftMailBodyLimit = 4 << 20

// microsoftMailFoldersCacheKey stores the folder-id → display-name map
// between runs (mailbox/direction mapping).
const microsoftMailFoldersCacheKey = "microsoft-mail:folders"

// MicrosoftMailUpstreamBase returns the configured Graph API base.
func (c *MicrosoftMailConnector) MicrosoftMailUpstreamBase() string {
	return c.host.UpstreamBaseFor(MicrosoftMailServiceID, MicrosoftGraphDefaultBase)
}

// Capabilities returns what Microsoft Mail kinds and operations are supported.
func (c *MicrosoftMailConnector) Capabilities() shared.ServiceCapabilities {
	return shared.ServiceCapabilities{
		Kinds: []shared.KindCapability{
			{
				Name:        "messages",
				Description: "Mail messages — list Outlook mail as neutral Message values (snippet only; body stays on Microsoft Graph)",
				Operations: []shared.Operation{
					{
						Name:        "read_list",
						Description: "List the user's recent mail as neutral Message values (paginated). Permission key: messages.",
						Method:      "GET",
						Path:        "/me/messages",
						Parameters: []shared.Parameter{
							{Name: "pageSize", In: "query", Type: "integer", Description: "Max items per page (1-1000)", Default: 50},
							{Name: "pageToken", In: "query", Type: "string", Description: "Page token for pagination"},
						},
						Response: shared.ResponseSpec{
							Description: "Neutral messages: subject, snippet, from/to/cc, sent_at, direction, read, thread_id, entity_id, sources[]",
							Fields:      []string{"messages", "total"},
						},
					},
					{
						Name:        "get_by_id",
						Description: "Get a single message with its body by service-local message id",
						Method:      "GET",
						Path:        "/messages/{id}",
						Parameters: []shared.Parameter{
							{Name: "id", In: "path", Type: "string", Description: "Service-local message id", Required: true},
						},
						Response: shared.ResponseSpec{
							Description: "Neutral message with body text and sources[]",
							Fields:      []string{"subject", "body", "from", "to", "cc", "sent_at", "sources"},
						},
					},
				},
			},
		},
	}
}

// SystemInstructions returns the system message injected when Microsoft
// mail is referenced.
func (c *MicrosoftMailConnector) SystemInstructions(baseURL string) string {
	root := strings.TrimRight(baseURL, "/")
	return fmt.Sprintf(`When the user's request involves Outlook, Microsoft 365, or their work email, use Aegis's unified data API. Do not call Microsoft Graph directly.

GET %s/v1/library/messages — the user's mail across connected services (subject, snippet, from/to, sent_at, direction, sources[])

sources[] records where each message lives (service + id) for sync. Fetch each message at /v1/services/{sources[].service}/messages/{sources[].id} — never send one service's id to another. Library lists carry snippets only.

Only when you need a full message body:
GET %s/v1/services/microsoft-mail/messages/{id} — one message with its body text

Message bodies stay on Microsoft Graph — Aegis does not store mailboxes. Cross-service merges prefer RFC Message-ID.`, root, root)
}

// HandleProxy processes a Microsoft Graph mail request through the
// connector. Issue #119 leaves only message detail (full bodies) as
// agent-facing content transport — lists live on /v1/library/messages.
func (c *MicrosoftMailConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	logger := slog.With("service", MicrosoftMailServiceID, "method", r.Method, "path", remainingPath)

	token := c.host.GetValidToken(MicrosoftMailServiceID, logger)
	if token == "" {
		shared.WriteJSONError(w, http.StatusUnauthorized,
			"no valid Microsoft token — authorize at /v1/auth/"+MicrosoftMailServiceID+"/login")
		return
	}

	if id, ok := MicrosoftMailIDFromProxyPath(remainingPath); ok {
		if r.Method != http.MethodGet {
			shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		c.HandleGetMessage(w, r, token, id)
		return
	}

	shared.WriteJSONError(w, http.StatusNotFound,
		"microsoft-mail serves message detail only; use /v1/library/messages for the list")
}

// MicrosoftMailIDFromProxyPath extracts a message id from /messages/{id} or
// /me/messages/{id}. Deeper paths (attachments and friends) are not content
// transport in this phase and report not-ok.
func MicrosoftMailIDFromProxyPath(remainingPath string) (string, bool) {
	p := strings.Trim(strings.TrimSpace(remainingPath), "/")
	for _, prefix := range []string{"messages/", "me/messages/"} {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		id := strings.TrimPrefix(p, prefix)
		if id == "" || strings.Contains(id, "/") {
			return "", false
		}
		if !validMicrosoftMailID(id) {
			return "", false
		}
		return id, true
	}
	return "", false
}

// validMicrosoftMailID bounds a service-local message id. Identifiers from
// other services must never be forwarded to the Graph API.
func validMicrosoftMailID(id string) bool {
	if id == "" || len(id) > 512 {
		return false
	}
	if strings.ContainsAny(id, "/\\ \t\r\n") {
		return false
	}
	if id == "." || id == ".." || strings.Contains(id, "..") {
		return false
	}
	return true
}

// HandleGetMessage returns one neutral message with its body text.
func (c *MicrosoftMailConnector) HandleGetMessage(w http.ResponseWriter, r *http.Request, token, id string) {
	raw, err := c.fetchMessage(r.Context(), token, id)
	if err != nil {
		slog.Error("microsoft mail message request failed", "service", MicrosoftMailServiceID, "error", err)
		shared.WriteJSONError(w, http.StatusBadGateway, "Microsoft Graph request failed")
		return
	}
	msg := MapMicrosoftMessage(raw, c.mailboxFolders(r.Context(), token), true)
	out, err := json.Marshal(msg)
	if err != nil {
		shared.WriteJSONError(w, http.StatusInternalServerError, "failed to encode message")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}

// microsoftMailRecipient is a Graph recipient (emailAddress name+address).
type microsoftMailRecipient struct {
	EmailAddress struct {
		Name    string `json:"name"`
		Address string `json:"address"`
	} `json:"emailAddress"`
}

// microsoftMailboxMessage is the subset of a Graph message record we use.
type microsoftMailboxMessage struct {
	ID                 string                  `json:"id"`
	InternetMessageID  string                  `json:"internetMessageId"`
	ConversationID     string                  `json:"conversationId"`
	Subject            string                  `json:"subject"`
	BodyPreview        string                  `json:"bodyPreview"`
	Body               *microsoftMailBody      `json:"body"`
	From               *microsoftMailRecipient `json:"from"`
	ToRecipients       []microsoftMailRecipient `json:"toRecipients"`
	CcRecipients       []microsoftMailRecipient `json:"ccRecipients"`
	SentDateTime       string                  `json:"sentDateTime"`
	ReceivedDateTime   string                  `json:"receivedDateTime"`
	IsRead             bool                    `json:"isRead"`
	IsDraft            bool                    `json:"isDraft"`
	ParentFolderID     string                  `json:"parentFolderId"`
	WebLink            string                  `json:"webLink"`
	HasAttachments     bool                    `json:"hasAttachments"`
}

// microsoftMailBody is a Graph message body (text or HTML content).
type microsoftMailBody struct {
	ContentType string `json:"contentType"`
	Content     string `json:"content"`
}

// fetchMessage hydrates one full Graph message (body included).
func (c *MicrosoftMailConnector) fetchMessage(ctx context.Context, token, id string) (microsoftMailboxMessage, error) {
	var raw microsoftMailboxMessage
	upstreamURL := microsoftGraphURL(c.MicrosoftMailUpstreamBase(),
		"/me/messages/"+url.PathEscape(id)+"?$select="+microsoftMailSelect)
	data, _, status, err := microsoftMailGet(ctx, token, upstreamURL)
	if err != nil {
		return raw, err
	}
	if status != http.StatusOK {
		return raw, fmt.Errorf("microsoft mail message returned %d: %s", status, truncateBytes(data, 200))
	}
	err = json.Unmarshal(data, &raw)
	return raw, err
}

// microsoftMailGet performs one authenticated Graph GET with the mail body
// limit (wider than the shared Graph request limit). Headers are returned so
// callers can honor throttle hints.
func microsoftMailGet(ctx context.Context, token, url string) ([]byte, http.Header, int, error) {
	return microsoftGraphDo(ctx, token, http.MethodGet, url, "", nil, microsoftMailBodyLimit)
}

// ListMessages implements MessageProvider: the user's recent Outlook mail
// as neutral Message values for the unified library (snippet-level).
func (c *MicrosoftMailConnector) ListMessages(ctx context.Context) ([]kinds.Message, bool, error) {
	token := c.host.GetValidToken(MicrosoftMailServiceID, slog.Default())
	if token == "" {
		return nil, false, kinds.ErrUnauthorized
	}
	folders := c.mailboxFolders(ctx, token)
	next := microsoftGraphURL(c.MicrosoftMailUpstreamBase(),
		"/me/messages?$top=50&$select="+microsoftMailSelect)
	var all []kinds.Message
	truncated := false
	for page := 0; page < shared.LibraryPageCap && next != ""; page++ {
		raw, err := graphPage[microsoftMailboxMessage](ctx, token, next)
		if err != nil {
			return stopGraphWalk(all, err)
		}
		for _, item := range raw.Value {
			if item.ID == "" {
				continue
			}
			if len(all) >= MicrosoftMailLibraryCap {
				truncated = true
				break
			}
			all = append(all, MapMicrosoftMessage(item, folders, false))
		}
		if truncated {
			break
		}
		next = raw.NextLink
		if next == "" {
			break
		}
		if page == shared.LibraryPageCap-1 {
			truncated = true
		}
	}
	return all, truncated, nil
}

// IngestMail implements MailIngest: one bounded list run into the local
// mail archive (docs/mail-ingest.md). Each call pages the mailbox from the
// durable continuation token (the page query Graph returned in
// @odata.nextLink) and stores full payloads. Callers repeat until
// Result.Done. Fail-open per message; one bad item must not drop the batch.
func (c *MicrosoftMailConnector) IngestMail(ctx context.Context, sink MailSink, opts shared.MailIngestOptions) (shared.MailIngestResult, error) {
	res := shared.MailIngestResult{Service: MicrosoftMailServiceID}
	maxMsgs := opts.MaxMessages
	if maxMsgs <= 0 {
		maxMsgs = MicrosoftMailArchiveDefault
	}
	if maxMsgs > MicrosoftMailArchiveMax {
		maxMsgs = MicrosoftMailArchiveMax
	}
	query := strings.TrimSpace(opts.Query)
	res.Query = query

	logger := slog.With("service", MicrosoftMailServiceID, "handler", "mail_archive_sync")
	token := c.host.GetValidToken(MicrosoftMailServiceID, logger)
	if token == "" {
		res.Error = "no valid Microsoft token"
		_ = sink.SaveCursor(MicrosoftMailServiceID, shared.MailIngestCursor{Error: res.Error})
		return res, kinds.ErrUnauthorized
	}

	cursor, err := sink.LoadCursor(MicrosoftMailServiceID)
	if err != nil {
		cursor = shared.MailIngestCursor{}
	}
	if opts.ResetCursor {
		cursor = shared.MailIngestCursor{}
	}
	pageToken := cursor.PageToken

	folders := c.mailboxFolders(ctx, token)

	pages := 0
	for res.Stored+res.Failed < maxMsgs {
		if pages >= MicrosoftMailArchiveMaxPages {
			res.Truncated = true
			break
		}
		pages++
		pageSize := MicrosoftMailArchiveListPage
		if remaining := maxMsgs - res.Stored - res.Failed; remaining < pageSize {
			pageSize = remaining
		}
		raw, headers, err := c.fetchArchivePage(ctx, token, pageToken, query, pageSize)
		if errors.Is(err, errMailThrottled) {
			// Throttled: stop and leave the cursor on this page so the
			// remaining messages are retried after the window resets.
			res.Truncated = true
			res.RetryAfter = microsoftMailThrottleRetryAfter(headers)
			retry := cursor
			retry.PageToken = pageToken
			retry.Done = false
			return c.finishMailSync(sink, res, retry, err)
		}
		if err != nil {
			return c.finishMailSync(sink, res, cursor, err)
		}
		// pageFetchToken is the token that produced this page. Only advance
		// the durable cursor after the whole page is stored (or skipped).
		pageFetchToken := pageToken
		if c.storeArchivePage(sink, logger, folders, raw.Value, &res, maxMsgs) {
			// Leave the cursor on this page: the next run re-listes it and
			// the upsert absorbs the overlap, so no message is skipped.
			break
		}
		nextPageToken := graphPageQuery(raw.NextLink)
		if raw.NextLink == "" {
			res.Done = true
			// Walk complete: the next run starts from the top, like the
			// Gmail ingest's cleared page token.
			pageToken = ""
			break
		}
		if nextPageToken == "" || nextPageToken == pageFetchToken {
			// A continuation link without a new token would replay the same
			// page forever — stop and surface the stuck page instead.
			return c.finishMailSync(sink, res, cursor,
				fmt.Errorf("microsoft mail list made no paging progress"))
		}
		pageToken = nextPageToken
		if res.Stored+res.Failed >= maxMsgs {
			res.Truncated = true
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	cursor.PageToken = pageToken
	cursor.Done = res.Done
	cursor.LastSyncAt = now
	cursor.Error = res.Error
	if err := sink.SaveCursor(MicrosoftMailServiceID, cursor); err != nil {
		logger.Warn("mail archive cursor save failed", "error", err)
	}
	return res, nil
}

// archiveListURL builds one mailbox page URL. A non-empty pageToken is
// Graph's own continuation query (from @odata.nextLink) and already carries
// the walk's projection and search.
func (c *MicrosoftMailConnector) archiveListURL(pageToken, query string, pageSize int) string {
	if pageToken != "" {
		return microsoftGraphURL(c.MicrosoftMailUpstreamBase(), "/me/messages?"+pageToken)
	}
	values := url.Values{}
	values.Set("$top", fmt.Sprint(pageSize))
	values.Set("$select", microsoftMailSelect)
	if query != "" {
		values.Set("$search", `"`+strings.ReplaceAll(query, `"`, "")+`"`)
	}
	return microsoftGraphURL(c.MicrosoftMailUpstreamBase(), "/me/messages?"+values.Encode())
}

// storeMailMessageArchive maps and stores one full message. The raw Graph
// record rides along as the archive payload.
func (c *MicrosoftMailConnector) storeMailMessageArchive(sink MailSink, raw microsoftMailboxMessage, folders map[string]string) error {
	payload, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	msg := MapMicrosoftMessage(raw, folders, true)
	return sink.Store(MicrosoftMailServiceID, msg, payload)
}

// errMailThrottled reports a 429 on the archive list; the walk leaves the
// cursor on the page and surfaces Retry-After instead of counting a failure.
var errMailThrottled = errors.New("microsoft graph throttled mail list")

// fetchArchivePage fetches and decodes one archive list page, classifying
// the statuses the walk treats specially: unauthorized and throttled wrap
// errMailThrottled/ErrUnauthorized, other non-200s carry the upstream text.
// The headers ride along so the throttle path can read Retry-After.
func (c *MicrosoftMailConnector) fetchArchivePage(ctx context.Context, token, pageToken, query string, pageSize int) (graphList[microsoftMailboxMessage], http.Header, error) {
	listURL := c.archiveListURL(pageToken, query, pageSize)
	data, headers, status, err := microsoftMailGet(ctx, token, listURL)
	if err != nil {
		return graphList[microsoftMailboxMessage]{}, nil, err
	}
	switch {
	case status == http.StatusUnauthorized:
		return graphList[microsoftMailboxMessage]{}, headers, kinds.ErrUnauthorized
	case status == http.StatusTooManyRequests:
		return graphList[microsoftMailboxMessage]{}, headers,
			fmt.Errorf("%w (%d)", errMailThrottled, status)
	case status != http.StatusOK:
		return graphList[microsoftMailboxMessage]{}, headers,
			fmt.Errorf("microsoft mail list returned %d: %s", status, truncateBytes(data, 200))
	}
	var raw graphList[microsoftMailboxMessage]
	if err := json.Unmarshal(data, &raw); err != nil {
		return graphList[microsoftMailboxMessage]{}, headers, err
	}
	return raw, nil, nil
}

// storeArchivePage stores each listed message fail-open — one bad item must
// not drop the batch. Reports true when the message cap cut the page short,
// meaning the cursor must not advance past it.
func (c *MicrosoftMailConnector) storeArchivePage(sink MailSink, logger *slog.Logger, folders map[string]string, items []microsoftMailboxMessage, res *shared.MailIngestResult, maxMsgs int) (truncatedMidPage bool) {
	for _, item := range items {
		if res.Stored+res.Failed >= maxMsgs {
			res.Truncated = true
			return true
		}
		if item.ID == "" {
			continue
		}
		res.Listed++
		if err := c.storeMailMessageArchive(sink, item, folders); err != nil {
			logger.Warn("mail archive store failed", "id", item.ID, "error", err)
			res.Failed++
			continue
		}
		res.Stored++
		res.LastStored = item.ID
	}
	return false
}

// finishMailSync records the outcome on the durable cursor and reports
// provider back-off asks (the runner contract — core never classifies
// provider errors itself).
func (c *MicrosoftMailConnector) finishMailSync(sink MailSink, res shared.MailIngestResult, cursor shared.MailIngestCursor, err error) (shared.MailIngestResult, error) {
	res.Error = err.Error()
	cursor.LastSyncAt = time.Now().UTC().Format(time.RFC3339)
	cursor.Error = res.Error
	_ = sink.SaveCursor(MicrosoftMailServiceID, cursor)
	return res, err
}

// graphPageQuery extracts the continuation query from a Graph nextLink so
// the durable cursor survives endpoint moves and mock bases.
func graphPageQuery(nextLink string) string {
	u, err := url.Parse(strings.TrimSpace(nextLink))
	if err != nil {
		return ""
	}
	return u.RawQuery
}

// microsoftMailThrottleRetryAfter honors a 429 Retry-After header, else
// the fixed back-off.
func microsoftMailThrottleRetryAfter(h http.Header) time.Duration {
	if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return MicrosoftMailThrottleBackoff
}

// mailboxFolders resolves folder-id → display name for mailbox/direction
// mapping, cached across runs. Fail-open: an empty map degrades mapping to
// "unknown" without failing the run.
func (c *MicrosoftMailConnector) mailboxFolders(ctx context.Context, token string) map[string]string {
	if raw, ok := c.host.CacheGet(microsoftMailFoldersCacheKey); ok {
		var cached map[string]string
		if json.Unmarshal([]byte(raw), &cached) == nil && len(cached) > 0 {
			return cached
		}
	}
	folders := map[string]string{}
	next := microsoftGraphURL(c.MicrosoftMailUpstreamBase(),
		"/me/mailFolders?$top=100&$select=id,displayName")
	for page := 0; page < shared.LibraryPageCap && next != ""; page++ {
		data, status, err := microsoftGraphRequest(ctx, token, http.MethodGet, next, nil)
		if err != nil || status != http.StatusOK {
			return folders
		}
		var raw graphList[struct {
			ID          string `json:"id"`
			DisplayName string `json:"displayName"`
		}]
		if err := json.Unmarshal(data, &raw); err != nil {
			return folders
		}
		for _, f := range raw.Value {
			folders[f.ID] = f.DisplayName
		}
		next = raw.NextLink
	}
	if len(folders) > 0 {
		if data, err := json.Marshal(folders); err == nil {
			c.host.CacheSet(microsoftMailFoldersCacheKey, string(data), time.Hour)
		}
	}
	return folders
}

// microsoftMailboxName normalizes a Graph folder display name onto the
// neutral mailbox tokens the other mail sources use.
func microsoftMailboxName(display string) string {
	switch strings.ToLower(strings.TrimSpace(display)) {
	case "inbox":
		return "INBOX"
	case "sent items", "sent":
		return "SENT"
	case "drafts":
		return "DRAFT"
	case "junk email", "junk":
		return "SPAM"
	case "deleted items", "deleted", "trash":
		return "TRASH"
	case "archive":
		return "ARCHIVE"
	case "":
		return ""
	default:
		return strings.TrimSpace(display)
	}
}

// microsoftDirectionFromMailbox maps the neutral mailbox token to the
// direction vocabulary (same shape as the Gmail label mapping).
func microsoftDirectionFromMailbox(mailbox string) string {
	switch strings.ToUpper(mailbox) {
	case "SENT":
		return "outbound"
	case "INBOX", "SPAM":
		return "inbound"
	default:
		return "unknown"
	}
}

// mapMicrosoftMailRecipients maps Graph recipients onto neutral contacts.
func mapMicrosoftMailRecipients(in []microsoftMailRecipient) []kinds.Contact {
	var out []kinds.Contact
	for _, r := range in {
		name := strings.TrimSpace(r.EmailAddress.Name)
		address := strings.TrimSpace(r.EmailAddress.Address)
		if name == "" && address == "" {
			continue
		}
		var emails []string
		if address != "" {
			emails = []string{address}
		}
		out = append(out, kinds.NewContact(MicrosoftMailServiceID, MicrosoftMailServiceName, "", name, emails, nil))
	}
	return out
}

// MapMicrosoftMessage converts a Graph message into the neutral shape.
// withBody includes the body text (archive ingest and detail fetch);
// library lists stay snippet-level.
func MapMicrosoftMessage(raw microsoftMailboxMessage, folders map[string]string, withBody bool) kinds.Message {
	from := mapMicrosoftMailRecipients(microsoftMailRecipientsOf(raw.From))
	to := mapMicrosoftMailRecipients(raw.ToRecipients)
	cc := mapMicrosoftMailRecipients(raw.CcRecipients)
	mailbox := microsoftMailboxName(folders[raw.ParentFolderID])
	sentAt := microsoftInstantToRFC3339(raw.SentDateTime)
	receivedAt := microsoftInstantToRFC3339(raw.ReceivedDateTime)

	var sender *kinds.Contact
	if len(from) > 0 {
		f := from[0]
		sender = &f
	}

	labels := []string{}
	if mailbox != "" {
		labels = append(labels, mailbox)
	}
	if !raw.IsRead {
		labels = append(labels, "UNREAD")
	}

	src := kinds.MessageSource{
		Service:     MicrosoftMailServiceID,
		ServiceName: MicrosoftMailServiceName,
		ID:          raw.ID,
		ThreadID:    raw.ConversationID,
		URL:         raw.WebLink,
		Mailbox:     mailbox,
		Canonical:   true,
	}
	extra := map[string]any{}
	if id := kinds.NormalizeMessageID(raw.InternetMessageID); id != "" {
		extra["message_id"] = id
	}
	if raw.BodyPreview != "" {
		extra["snippet"] = raw.BodyPreview
	}
	if raw.HasAttachments {
		extra["has_attachments"] = true
	}
	if len(extra) > 0 {
		src.Extra = extra
	}

	msg := kinds.Message{
		Channel:    kinds.MessageChannelEmail,
		Subject:    strings.TrimSpace(raw.Subject),
		Snippet:    raw.BodyPreview,
		From:       sender,
		To:         to,
		CC:         cc,
		SentAt:     sentAt,
		ReceivedAt: receivedAt,
		Direction:  microsoftDirectionFromMailbox(mailbox),
		Read:       raw.IsRead,
		ThreadID:   raw.ConversationID,
		EntityID:   kinds.NormalizeMessageID(raw.InternetMessageID),
		Labels:     labels,
		Sources:    []kinds.MessageSource{src},
	}
	if withBody && raw.Body != nil {
		msg.Body = raw.Body.Content
	}
	return msg
}

// microsoftMailRecipientsOf adapts the optional single sender to the
// recipient slice shape.
func microsoftMailRecipientsOf(from *microsoftMailRecipient) []microsoftMailRecipient {
	if from == nil {
		return nil
	}
	return []microsoftMailRecipient{*from}
}
