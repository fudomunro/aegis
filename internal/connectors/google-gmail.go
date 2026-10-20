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

	"github.com/fudomunro/aegis/internal/types"
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

// GoogleGmailServiceID is the connector/service ID used in config, tokens,
// sources[], and admin. Documented name is "Gmail".
const GoogleGmailServiceID = "google-gmail"

const GoogleGmailServiceName = "Gmail"

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

// Capabilities returns what Gmail data types and operations are supported.
func (c *GoogleGmailConnector) Capabilities() types.ServiceCapabilities {
	return types.ServiceCapabilities{
		DataTypes: []types.DataTypeCapability{
			{
				Name:        "messages",
				Description: "Messages — list mail messages/threads (metadata/snippet only by default; body stays on Gmail)",
				Operations: []types.Operation{
					{
						Name:        "read_list",
						Description: "List the user's messages as neutral Message values (paginated). Permission key: messages.",
						Method:      "GET",
						Path:        "/me/messages",
						Parameters: []types.Parameter{
							{Name: "q", In: "query", Type: "string", Description: "Gmail search query (default: newer_than:30d)"},
							{Name: "maxResults", In: "query", Type: "integer", Description: "List page size for IDs (1-500)", Default: googleGmailListPageSize},
							{Name: "pageSize", In: "query", Type: "integer", Description: "Alias for maxResults"},
							{Name: "pageToken", In: "query", Type: "string", Description: "Page token for pagination"},
						},
						Response: types.ResponseSpec{
							Description: "Neutral messages: channel, subject, snippet, from/to, sent_at, direction, read, sources[]",
							Fields:      []string{"messages", "total"},
						},
					},
					{
						Name:        "get_by_id",
						Description: "Get a single message by service-local ID (metadata; format=full includes decoded body)",
						Method:      "GET",
						Path:        "/messages/{id}",
						Parameters: []types.Parameter{
							{Name: "id", In: "path", Type: "string", Description: "Service-local message ID", Required: true},
							{Name: "format", In: "query", Type: "string", Description: "metadata or full", Default: googleGmailMetadataFormat},
						},
						Response: types.ResponseSpec{
							Description: "Neutral message with service-local id and sources[]",
							Fields:      []string{"subject", "snippet", "from", "to", "sent_at", "sources"},
						},
					},
					{
						Name:        "list_threads",
						Description: "List recent threads (id + snippet)",
						Method:      "GET",
						Path:        "/me/threads",
						Parameters: []types.Parameter{
							{Name: "q", In: "query", Type: "string", Description: "Gmail search query"},
							{Name: "maxResults", In: "query", Type: "integer", Description: "Max threads per page", Default: googleGmailListPageSize},
							{Name: "pageToken", In: "query", Type: "string", Description: "Page token"},
						},
						Response: types.ResponseSpec{
							Description: "Threads: id, snippet, message_count, sources[]",
							Fields:      []string{"threads", "total"},
						},
					},
					{
						Name:        "get_thread",
						Description: "Get one thread with its messages as neutral Message values",
						Method:      "GET",
						Path:        "/threads/{id}",
						Parameters: []types.Parameter{
							{Name: "id", In: "path", Type: "string", Description: "Service-local thread ID", Required: true},
						},
						Response: types.ResponseSpec{
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

sources[] records where each message lives (service + id) for sync. Fetch each message at /v1/services/{sources[].service}/messages/{sources[].id} — never send one service's id to another. Library lists carry snippets only.

Only when you need a full message body:
GET %s/v1/services/google-gmail/messages/{id} — one message; add ?format=full for the decoded body
GET %s/v1/services/google-gmail/threads/{id} — one thread with its messages

Message bodies stay on Gmail — Aegis does not store mailboxes. Cross-service merges prefer RFC Message-ID.`, root, root, root)
}

// HandleProxy processes a Gmail API request through the connector.
func (c *GoogleGmailConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	logger := slog.With("service", GoogleGmailServiceID, "method", r.Method, "path", remainingPath)

	token := c.host.GetValidToken(GoogleGmailServiceID, logger)
	if token == "" {
		WriteJSONError(w, http.StatusUnauthorized,
			"no valid Gmail token — authorize at /v1/auth/"+GoogleGmailServiceID+"/login")
		return
	}

	if remainingPath == "/me/messages" || remainingPath == "/me/messages/" ||
		remainingPath == "/messages" || remainingPath == "/messages/" {
		if r.Method != http.MethodGet {
			WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		c.HandleListMessages(w, r)
		return
	}

	if remainingPath == "/me/threads" || remainingPath == "/me/threads/" ||
		remainingPath == "/threads" || remainingPath == "/threads/" {
		if r.Method != http.MethodGet {
			WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		c.HandleListThreads(w, r)
		return
	}

	if mid, aid, ok := GmailAttachmentPath(remainingPath); ok {
		if r.Method != http.MethodGet {
			WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		c.HandleGetAttachment(w, r, token, mid, aid)
		return
	}

	if id, ok := GmailBareIDFromProxyPath(remainingPath, "messages"); ok {
		if r.Method != http.MethodGet {
			WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		c.HandleGetMessage(w, r, token, id)
		return
	}

	if id, ok := GmailBareIDFromProxyPath(remainingPath, "threads"); ok {
		if r.Method != http.MethodGet {
			WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		c.HandleGetThread(w, r, token, id)
		return
	}

	// Generic authenticated pass-through for other Gmail API paths under this base.
	upstreamURL := c.GoogleGmailUpstreamBase() + remainingPath
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, r.Body)
	if err != nil {
		logger.Error("failed to build upstream request", "error", err)
		WriteJSONError(w, http.StatusInternalServerError, "failed to build request")
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	resp, err := HTTPClient.Do(req)
	if err != nil {
		logger.Error("upstream request failed", "error", err)
		WriteJSONError(w, http.StatusBadGateway, "Gmail request failed")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// GmailBareIDFromProxyPath extracts an id from /messages/{id}, /me/messages/{id},
// /threads/{id}, or /me/threads/{id}.
// validGmailBareID reports whether id looks like a Gmail message/thread id.
// Identifiers from other services must never be forwarded to the Gmail API.
func validGmailBareID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	if strings.ContainsAny(id, "/\\ \t\r\n") {
		return false
	}
	if strings.Contains(id, ":") {
		return false
	}
	for _, r := range id {
		ok := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '-' || r == '_'
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

func isGmailQuotaError(err error) bool {
	return err != nil && strings.Contains(err.Error(), gmailQuotaMarker)
}

// GmailAttachmentPath parses messages/{mid}/attachments/{aid}.
func GmailAttachmentPath(remainingPath string) (messageID, attachmentID string, ok bool) {
	p := strings.Trim(remainingPath, "/")
	p = strings.TrimPrefix(p, "me/")
	if !strings.HasPrefix(p, "messages/") {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(p, "messages/"), "/")
	if len(parts) != 3 || parts[1] != "attachments" {
		return "", "", false
	}
	messageID, attachmentID = parts[0], parts[2]
	if !validGmailBareID(messageID) || !validGmailBareID(attachmentID) {
		return "", "", false
	}
	return messageID, attachmentID, true
}

// HandleGetAttachment fetches one MIME attachment via the Gmail attachments API.
func (c *GoogleGmailConnector) HandleGetAttachment(w http.ResponseWriter, r *http.Request, token, messageID, attachmentID string) {
	if !validGmailBareID(messageID) || !validGmailBareID(attachmentID) {
		WriteJSONError(w, http.StatusNotFound, "not a Gmail attachment id")
		return
	}
	upstreamURL := c.GoogleGmailUpstreamBase() + "/users/me/messages/" + url.PathEscape(messageID) +
		"/attachments/" + url.PathEscape(attachmentID)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstreamURL, nil)
	if err != nil {
		WriteJSONError(w, http.StatusInternalServerError, "failed to build request")
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := HTTPClient.Do(req)
	if err != nil {
		slog.Warn("gmail get attachment failed", "error", err)
		WriteJSONError(w, http.StatusBadGateway, "failed to fetch attachment from Gmail")
		return
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		WriteJSONError(w, http.StatusBadGateway, "failed to read attachment from Gmail")
		return
	}
	if resp.StatusCode == http.StatusNotFound {
		WriteJSONError(w, http.StatusNotFound, "attachment not found on Gmail")
		return
	}
	if resp.StatusCode == http.StatusTooManyRequests || isGmailQuotaError(fmt.Errorf("%s", data)) {
		WriteJSONError(w, http.StatusTooManyRequests, "Gmail quota exceeded — retry shortly")
		return
	}
	if resp.StatusCode != http.StatusOK {
		slog.Warn("gmail get attachment bad status", "status", resp.StatusCode)
		WriteJSONError(w, http.StatusBadGateway, "failed to fetch attachment from Gmail")
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
		if err == types.ErrUnauthorized {
			WriteJSONError(w, http.StatusUnauthorized,
				"no valid Gmail token — authorize at /v1/auth/"+GoogleGmailServiceID+"/login")
			return
		}
		slog.Warn("gmail list messages failed", "error", err)
		WriteJSONError(w, http.StatusBadGateway, "failed to fetch messages from Gmail")
		return
	}
	if messages == nil {
		messages = []types.Message{}
	}
	result := types.MessageList{
		Messages:  messages,
		Total:     len(messages),
		Synced:    true,
		Truncated: truncated,
		ByService: map[string]types.LibraryServiceStatus{
			GoogleGmailServiceID: {
				Status:    types.LibraryStatusOK,
				Count:     len(messages),
				Truncated: truncated,
			},
		},
	}
	data, err := json.Marshal(result)
	if err != nil {
		WriteJSONError(w, http.StatusInternalServerError, "failed to build messages list")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func (c *GoogleGmailConnector) HandleListThreads(w http.ResponseWriter, r *http.Request) {
	token := c.host.GetValidToken(GoogleGmailServiceID, slog.Default())
	if token == "" {
		WriteJSONError(w, http.StatusUnauthorized,
			"no valid Gmail token — authorize at /v1/auth/"+GoogleGmailServiceID+"/login")
		return
	}
	threads, truncated, err := c.ListThreads(r.Context(), token, r.URL.Query())
	if err != nil {
		slog.Warn("gmail list threads failed", "error", err)
		WriteJSONError(w, http.StatusBadGateway, "failed to fetch threads from Gmail")
		return
	}
	if threads == nil {
		threads = []map[string]any{}
	}
	result := map[string]any{
		"threads":   threads,
		"total":     len(threads),
		"truncated": truncated,
		"by_service": map[string]types.LibraryServiceStatus{
			GoogleGmailServiceID: {Status: types.LibraryStatusOK, Count: len(threads), Truncated: truncated},
		},
	}
	data, err := json.Marshal(result)
	if err != nil {
		WriteJSONError(w, http.StatusInternalServerError, "failed to build threads list")
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
	for page := 0; page < LibraryPageCap; page++ {
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
		resp, err := HTTPClient.Do(req)
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
		if page == LibraryPageCap-1 && pageToken != "" {
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
		WriteJSONError(w, http.StatusNotFound,
			"not a Gmail message id — use sources[].service and sources[].id from the library list")
		return
	}
	raw, err := c.FetchGmailMessage(r.Context(), token, id, format)
	if err != nil {
		if isGmailInvalidIDError(err) {
			slog.Warn("gmail get message unknown id", "id", id)
			WriteJSONError(w, http.StatusNotFound,
				"message not found on Gmail — check sources[].service and sources[].id")
			return
		}
		if isGmailQuotaError(err) {
			WriteJSONError(w, http.StatusTooManyRequests,
				"Gmail quota exceeded — retry shortly or use /v1/mail/search (local archive)")
			return
		}
		slog.Warn("gmail get message failed", "id", id, "error", err)
		WriteJSONError(w, http.StatusBadGateway, "failed to fetch message from Gmail")
		return
	}
	msg := MapGoogleGmailMessage(raw, format)
	data, err := json.Marshal(msg)
	if err != nil {
		WriteJSONError(w, http.StatusInternalServerError, "failed to encode message")
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
		WriteJSONError(w, http.StatusInternalServerError, "failed to build request")
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := HTTPClient.Do(req)
	if err != nil {
		WriteJSONError(w, http.StatusBadGateway, "Gmail thread request failed")
		return
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		WriteJSONError(w, http.StatusBadGateway, "failed to read Gmail thread response")
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
		WriteJSONError(w, http.StatusBadGateway, "failed to parse Gmail thread response")
		return
	}
	messages := make([]types.Message, 0, len(raw.Messages))
	for _, m := range raw.Messages {
		messages = append(messages, MapGoogleGmailMessage(m, googleGmailMetadataFormat))
	}
	out, err := json.Marshal(map[string]any{
		"id":       raw.ID,
		"messages": messages,
		"total":    len(messages),
	})
	if err != nil {
		WriteJSONError(w, http.StatusInternalServerError, "failed to encode thread")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}

// ListMessages implements MessageProvider. Fetches recent Gmail messages and
// hydrates headers into neutral Message metadata.
func (c *GoogleGmailConnector) ListMessages(ctx context.Context) ([]types.Message, bool, error) {
	return c.ListMessagesWithQuery(ctx, nil)
}

func (c *GoogleGmailConnector) ListMessagesWithQuery(ctx context.Context, q url.Values) ([]types.Message, bool, error) {
	token := c.host.GetValidToken(GoogleGmailServiceID, slog.Default())
	if token == "" {
		return nil, false, types.ErrUnauthorized
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

	var all []types.Message
	truncated := false
	hydrated := 0
	for page := 0; page < LibraryPageCap; page++ {
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
		resp, err := HTTPClient.Do(req)
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
		if hydrated >= hydrateCap || (page == LibraryPageCap-1 && pageToken != "") {
			truncated = true
		}
	}
	return all, truncated, nil
}

func (c *GoogleGmailConnector) FetchAndMapMessage(ctx context.Context, token, id string) (types.Message, error) {
	raw, err := c.FetchGmailMessage(ctx, token, id, googleGmailMetadataFormat)
	if err != nil {
		return types.Message{}, err
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
	resp, err := HTTPClient.Do(req)
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

type GoogleGmailMessage struct {
	ID           string   `json:"id"`
	ThreadID     string   `json:"threadId"`
	LabelIDs     []string `json:"labelIds"`
	Snippet      string   `json:"snippet"`
	InternalDate string   `json:"internalDate"`
	Payload      *struct {
		Headers []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"headers"`
		Body struct {
			Data         string `json:"data"`
			AttachmentID string `json:"attachmentId"`
		} `json:"body"`
		Parts []struct {
			MimeType string `json:"mimeType"`
			Body     struct {
				Data string `json:"data"`
			} `json:"body"`
			Headers []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"headers"`
		} `json:"parts"`
	} `json:"payload"`
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
func MapGmailAddressHeader(raw string) []types.Contact {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	addrs, err := mail.ParseAddressList(raw)
	if err != nil {
		// Fail-open: keep a crude name/email split rather than dropping the party.
		if strings.Contains(raw, "@") {
			c := types.NewContact(GoogleGmailServiceID, GoogleGmailServiceName, "", "", []string{raw}, nil)
			return []types.Contact{c}
		}
		c := types.NewContact(GoogleGmailServiceID, GoogleGmailServiceName, "", raw, nil, nil)
		return []types.Contact{c}
	}
	out := make([]types.Contact, 0, len(addrs))
	for _, a := range addrs {
		if a == nil {
			continue
		}
		var emails []string
		if a.Address != "" {
			emails = []string{a.Address}
		}
		out = append(out, types.NewContact(GoogleGmailServiceID, GoogleGmailServiceName, "", a.Name, emails, nil))
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

// MapGoogleGmailMessage converts a Gmail message into the neutral shape.
// GmailAttachmentSummaries lists MIME parts that have an attachmentId so
// agents can fetch binaries at /messages/{id}/attachments/{attachmentId}.
func GmailAttachmentSummaries(raw GoogleGmailMessage) []map[string]string {
	if raw.Payload == nil {
		return nil
	}
	var out []map[string]string
	if id := strings.TrimSpace(raw.Payload.Body.AttachmentID); id != "" {
		out = append(out, map[string]string{"id": id})
	}
	// Part attachment ids are not in the flattened struct; body.data-only
	// parts are inline and need no fetch.
	return out
}

func MapGoogleGmailMessage(raw GoogleGmailMessage, format string) types.Message {
	fromList := MapGmailAddressHeader(GmailHeaderValue(raw, "From"))
	toList := MapGmailAddressHeader(GmailHeaderValue(raw, "To"))
	ccList := MapGmailAddressHeader(GmailHeaderValue(raw, "Cc"))
	subject := strings.TrimSpace(GmailHeaderValue(raw, "Subject"))
	if decoded, err := new(mime.WordDecoder).DecodeHeader(subject); err == nil {
		subject = strings.TrimSpace(decoded)
	}
	messageID := types.NormalizeMessageID(GmailHeaderValue(raw, "Message-ID"))
	sentAt := GmailInternalDateRFC3339(raw.InternalDate)
	if sentAt == "" {
		sentAt = GmailHeaderDateRFC3339(GmailHeaderValue(raw, "Date"))
	}

	var from *types.Contact
	if len(fromList) > 0 {
		f := fromList[0]
		from = &f
	}

	src := types.MessageSource{
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

	msg := types.Message{
		Channel:    types.MessageChannelEmail,
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
		Sources:    []types.MessageSource{src},
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

// Host returns the runtime host for mail-archive sync (package main).
func (c *GoogleGmailConnector) Host() Host { return c.host }
