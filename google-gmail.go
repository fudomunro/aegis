package main

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
)

// GoogleGmailConnector proxies the Gmail API and implements MessageProvider.
// It lists the user's recent messages as neutral Message values so agents can
// ask "what email have I received?" through /v1/library/messages. Message
// content stays on Gmail (non-Aegis canonical, issue #16). Read-only in
// this phase — agent write ops are not advertised (issue #61). SMS/WhatsApp
// are out of scope until a named OAuth-capable connector exists (issue #60).
// https://github.com/fudomunro/aegis/issues/60
type GoogleGmailConnector struct {
	server *Server
}

// NewGoogleGmailConnector creates a connector wired to the given server.
func NewGoogleGmailConnector(srv *Server) *GoogleGmailConnector {
	return &GoogleGmailConnector{server: srv}
}

// googleGmailServiceID is the connector/service ID used in config, tokens,
// sources[], and admin. Documented name is "Gmail".
const googleGmailServiceID = "google-gmail"

const googleGmailServiceName = "Gmail"

// Default library window and caps. Gmail list endpoints return IDs only, so
// the library hydrates metadata per message up to a modest cap to keep
// "my inbox" answers useful without unbounded detail fetches.
const (
	googleGmailDefaultQuery   = "newer_than:30d"
	googleGmailListPageSize   = 25
	googleGmailLibraryCap     = 50
	googleGmailMetadataFormat = "metadata"
)

// googleGmailUpstreamBase returns the configured Gmail API base.
func (c *GoogleGmailConnector) googleGmailUpstreamBase() string {
	const defaultBase = "https://gmail.googleapis.com/gmail/v1"
	if svc := c.server.config.GetService(googleGmailServiceID); svc != nil && svc.Endpoint != "" {
		return strings.TrimRight(svc.Endpoint, "/")
	}
	return defaultBase
}

// Capabilities returns what Gmail data types and operations are supported.
func (c *GoogleGmailConnector) Capabilities() ServiceCapabilities {
	return ServiceCapabilities{
		DataTypes: []DataTypeCapability{
			{
				Name:        "messages",
				Description: "Messages — list mail messages/threads (metadata/snippet only by default; body stays on Gmail)",
				Operations: []Operation{
					{
						Name:        "read_list",
						Description: "List the user's messages as neutral Message values (paginated). Permission key: messages.",
						Method:      "GET",
						Path:        "/me/messages",
						Parameters: []Parameter{
							{Name: "q", In: "query", Type: "string", Description: "Gmail search query (default: newer_than:30d)"},
							{Name: "maxResults", In: "query", Type: "integer", Description: "List page size for IDs (1-500)", Default: googleGmailListPageSize},
							{Name: "pageSize", In: "query", Type: "integer", Description: "Alias for maxResults"},
							{Name: "pageToken", In: "query", Type: "string", Description: "Page token for pagination"},
						},
						Response: ResponseSpec{
							Description: "Neutral messages: channel, subject, snippet, from/to, sent_at, direction, read, sources[]",
							Fields:      []string{"messages", "total"},
						},
					},
					{
						Name:        "get_by_id",
						Description: "Get a single message by service-local ID (metadata; format=full includes decoded body)",
						Method:      "GET",
						Path:        "/messages/{id}",
						Parameters: []Parameter{
							{Name: "id", In: "path", Type: "string", Description: "Service-local message ID", Required: true},
							{Name: "format", In: "query", Type: "string", Description: "metadata or full", Default: googleGmailMetadataFormat},
						},
						Response: ResponseSpec{
							Description: "Neutral message with service-local id and sources[]",
							Fields:      []string{"subject", "snippet", "from", "to", "sent_at", "sources"},
						},
					},
					{
						Name:        "list_threads",
						Description: "List recent threads (id + snippet)",
						Method:      "GET",
						Path:        "/me/threads",
						Parameters: []Parameter{
							{Name: "q", In: "query", Type: "string", Description: "Gmail search query"},
							{Name: "maxResults", In: "query", Type: "integer", Description: "Max threads per page", Default: googleGmailListPageSize},
							{Name: "pageToken", In: "query", Type: "string", Description: "Page token"},
						},
						Response: ResponseSpec{
							Description: "Threads: id, snippet, message_count, sources[]",
							Fields:      []string{"threads", "total"},
						},
					},
					{
						Name:        "get_thread",
						Description: "Get one thread with its messages as neutral Message values",
						Method:      "GET",
						Path:        "/threads/{id}",
						Parameters: []Parameter{
							{Name: "id", In: "path", Type: "string", Description: "Service-local thread ID", Required: true},
						},
						Response: ResponseSpec{
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
	base := root + "/v1/services/" + googleGmailServiceID
	return fmt.Sprintf(`When the user's request involves Gmail, email, their inbox, or recent messages, you MUST use the Aegis proxy at %s instead of calling Gmail API directly.

For "my email" across every connected mail/message service, prefer the unified library endpoint first:
GET %s/v1/library/messages — messages in one neutral list (subject, snippet, from/to as Contact identities, sent_at, direction, read, sources[])

Gmail-specific endpoints (call the full proxy URL; Aegis authenticates you):
GET /me/messages — list messages (neutral Message metadata; default query newer_than:30d)
GET /messages/{id} — one message (add ?format=full to include decoded body)
GET /me/threads — list recent threads
GET /threads/{id} — one thread with its messages

Messages use Aegis's neutral identity matching for from/to/cc (email → phone → handle → name), so senders can be matched to /v1/library/contacts when a contact service is connected.
Message bodies stay on Gmail — Aegis does not store mailboxes. Cross-service merges prefer RFC Message-ID.
Always include the full proxy base URL in your requests.`, base, root)
}

// HandleProxy processes a Gmail API request through the connector.
func (c *GoogleGmailConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	logger := slog.With("service", googleGmailServiceID, "method", r.Method, "path", remainingPath)

	token := c.server.getValidToken(googleGmailServiceID, logger)
	if token == "" {
		writeJSONError(w, http.StatusUnauthorized,
			"no valid Gmail token — authorize at /v1/auth/"+googleGmailServiceID+"/login")
		return
	}

	if remainingPath == "/me/messages" || remainingPath == "/me/messages/" ||
		remainingPath == "/messages" || remainingPath == "/messages/" {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		c.handleListMessages(w, r)
		return
	}

	if remainingPath == "/me/threads" || remainingPath == "/me/threads/" ||
		remainingPath == "/threads" || remainingPath == "/threads/" {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		c.handleListThreads(w, r)
		return
	}

	if id, ok := gmailBareIDFromProxyPath(remainingPath, "messages"); ok {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		c.handleGetMessage(w, r, token, id)
		return
	}

	if id, ok := gmailBareIDFromProxyPath(remainingPath, "threads"); ok {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		c.handleGetThread(w, r, token, id)
		return
	}

	// Generic authenticated pass-through for other Gmail API paths under this base.
	upstreamURL := c.googleGmailUpstreamBase() + remainingPath
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, r.Body)
	if err != nil {
		logger.Error("failed to build upstream request", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to build request")
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	resp, err := aegisHTTPClient.Do(req)
	if err != nil {
		logger.Error("upstream request failed", "error", err)
		writeJSONError(w, http.StatusBadGateway, "Gmail request failed")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// gmailBareIDFromProxyPath extracts an id from /messages/{id}, /me/messages/{id},
// /threads/{id}, or /me/threads/{id}.
func gmailBareIDFromProxyPath(remainingPath, kind string) (string, bool) {
	p := remainingPath
	for _, prefix := range []string{"/me/" + kind + "/", "/" + kind + "/"} {
		if strings.HasPrefix(p, prefix) {
			id := strings.Trim(strings.TrimPrefix(p, prefix), "/")
			return id, id != ""
		}
	}
	return "", false
}

func (c *GoogleGmailConnector) handleListMessages(w http.ResponseWriter, r *http.Request) {
	messages, truncated, err := c.listMessagesWithQuery(r.Context(), r.URL.Query())
	if err != nil {
		if err == ErrUnauthorized {
			writeJSONError(w, http.StatusUnauthorized,
				"no valid Gmail token — authorize at /v1/auth/"+googleGmailServiceID+"/login")
			return
		}
		slog.Warn("gmail list messages failed", "error", err)
		writeJSONError(w, http.StatusBadGateway, "failed to fetch messages from Gmail")
		return
	}
	if messages == nil {
		messages = []Message{}
	}
	result := MessageList{
		Messages:  messages,
		Total:     len(messages),
		Synced:    true,
		Truncated: truncated,
		ByService: map[string]LibraryServiceStatus{
			googleGmailServiceID: {
				Status:    LibraryStatusOK,
				Count:     len(messages),
				Truncated: truncated,
			},
		},
	}
	data, err := json.Marshal(result)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to build messages list")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func (c *GoogleGmailConnector) handleListThreads(w http.ResponseWriter, r *http.Request) {
	token := c.server.getValidToken(googleGmailServiceID, slog.Default())
	if token == "" {
		writeJSONError(w, http.StatusUnauthorized,
			"no valid Gmail token — authorize at /v1/auth/"+googleGmailServiceID+"/login")
		return
	}
	threads, truncated, err := c.listThreads(r.Context(), token, r.URL.Query())
	if err != nil {
		slog.Warn("gmail list threads failed", "error", err)
		writeJSONError(w, http.StatusBadGateway, "failed to fetch threads from Gmail")
		return
	}
	if threads == nil {
		threads = []map[string]any{}
	}
	result := map[string]any{
		"threads":   threads,
		"total":     len(threads),
		"truncated": truncated,
		"by_service": map[string]LibraryServiceStatus{
			googleGmailServiceID: {Status: LibraryStatusOK, Count: len(threads), Truncated: truncated},
		},
	}
	data, err := json.Marshal(result)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to build threads list")
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

func (c *GoogleGmailConnector) listThreads(ctx context.Context, token string, q url.Values) ([]map[string]any, bool, error) {
	query := ""
	pageToken := ""
	pageSize := googleGmailListPageSize
	if q != nil {
		query = strings.TrimSpace(q.Get("q"))
		pageToken = q.Get("pageToken")
		if v := strings.TrimSpace(q.Get("maxResults")); v != "" {
			if n, err := parseIntLoose(v); err == nil && n > 0 {
				pageSize = n
			}
		}
	}
	var out []map[string]any
	truncated := false
	for page := 0; page < libraryPageCap; page++ {
		values := url.Values{}
		values.Set("maxResults", fmt.Sprint(pageSize))
		if query != "" {
			values.Set("q", query)
		}
		if pageToken != "" {
			values.Set("pageToken", pageToken)
		}
		u := c.googleGmailUpstreamBase() + "/users/me/threads?" + values.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, false, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := aegisHTTPClient.Do(req)
		if err != nil {
			return nil, false, err
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, false, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, false, gmailListHTTPError("threads list", resp.StatusCode, data)
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
					"service":      googleGmailServiceID,
					"service_name": googleGmailServiceName,
					"id":           th.ID,
					"url":          "https://mail.google.com/mail/u/0/#all/" + th.ID,
				},
			})
		}
		pageToken = raw.NextPageToken
		if pageToken == "" {
			break
		}
		if page == libraryPageCap-1 && pageToken != "" {
			truncated = true
		}
	}
	return out, truncated, nil
}

func (c *GoogleGmailConnector) handleGetMessage(w http.ResponseWriter, r *http.Request, token, id string) {
	format := strings.TrimSpace(r.URL.Query().Get("format"))
	if format == "" {
		format = googleGmailMetadataFormat
	}
	raw, err := c.fetchGmailMessage(r.Context(), token, id, format)
	if err != nil {
		slog.Warn("gmail get message failed", "id", id, "error", err)
		writeJSONError(w, http.StatusBadGateway, "failed to fetch message from Gmail")
		return
	}
	msg := mapGoogleGmailMessage(raw, format)
	data, err := json.Marshal(msg)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to encode message")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func (c *GoogleGmailConnector) handleGetThread(w http.ResponseWriter, r *http.Request, token, id string) {
	upstreamURL := c.googleGmailUpstreamBase() + "/users/me/threads/" + url.PathEscape(id) +
		"?format=metadata&metadataHeaders=" + strings.Join(gmailMetadataHeaders(), "&metadataHeaders=")
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstreamURL, nil)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to build request")
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := aegisHTTPClient.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "Gmail thread request failed")
		return
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "failed to read Gmail thread response")
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
		writeJSONError(w, http.StatusBadGateway, "failed to parse Gmail thread response")
		return
	}
	messages := make([]Message, 0, len(raw.Messages))
	for _, m := range raw.Messages {
		messages = append(messages, mapGoogleGmailMessage(m, googleGmailMetadataFormat))
	}
	out, err := json.Marshal(map[string]any{
		"id":       raw.ID,
		"messages": messages,
		"total":    len(messages),
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to encode thread")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}

// ListMessages implements MessageProvider. Fetches recent Gmail messages and
// hydrates headers into neutral Message metadata.
func (c *GoogleGmailConnector) ListMessages(ctx context.Context) ([]Message, bool, error) {
	return c.listMessagesWithQuery(ctx, nil)
}

func (c *GoogleGmailConnector) listMessagesWithQuery(ctx context.Context, q url.Values) ([]Message, bool, error) {
	token := c.server.getValidToken(googleGmailServiceID, slog.Default())
	if token == "" {
		return nil, false, ErrUnauthorized
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
			if n, err := parseIntLoose(v); err == nil && n > 0 {
				pageSize = n
			}
		} else if v := strings.TrimSpace(q.Get("pageSize")); v != "" {
			if n, err := parseIntLoose(v); err == nil && n > 0 {
				pageSize = n
			}
		}
	}

	// Library vs proxy caps: proxy may request more pages; library stays modest.
	hydrateCap := googleGmailLibraryCap
	if q != nil && q.Get("maxHydrate") != "" {
		if n, err := parseIntLoose(q.Get("maxHydrate")); err == nil && n > 0 {
			hydrateCap = n
		}
	}

	var all []Message
	truncated := false
	hydrated := 0
	for page := 0; page < libraryPageCap; page++ {
		values := url.Values{}
		values.Set("maxResults", fmt.Sprint(pageSize))
		if query != "" {
			values.Set("q", query)
		}
		if pageToken != "" {
			values.Set("pageToken", pageToken)
		}
		listURL := c.googleGmailUpstreamBase() + "/users/me/messages?" + values.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
		if err != nil {
			return nil, false, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := aegisHTTPClient.Do(req)
		if err != nil {
			return nil, false, err
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, false, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, false, gmailListHTTPError("messages list", resp.StatusCode, data)
		}
		var raw googleGmailMessageList
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
			msg, err := c.fetchAndMapMessage(ctx, token, item.ID)
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
		if hydrated >= hydrateCap || (page == libraryPageCap-1 && pageToken != "") {
			truncated = true
		}
	}
	return all, truncated, nil
}

func (c *GoogleGmailConnector) fetchAndMapMessage(ctx context.Context, token, id string) (Message, error) {
	raw, err := c.fetchGmailMessage(ctx, token, id, googleGmailMetadataFormat)
	if err != nil {
		return Message{}, err
	}
	return mapGoogleGmailMessage(raw, googleGmailMetadataFormat), nil
}

func (c *GoogleGmailConnector) fetchGmailMessage(ctx context.Context, token, id, format string) (googleGmailMessage, error) {
	if format == "" {
		format = googleGmailMetadataFormat
	}
	u := c.googleGmailUpstreamBase() + "/users/me/messages/" + url.PathEscape(id) + "?format=" + url.QueryEscape(format)
	if format == googleGmailMetadataFormat || format == "raw" {
		for _, h := range gmailMetadataHeaders() {
			u += "&metadataHeaders=" + url.QueryEscape(h)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return googleGmailMessage{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := aegisHTTPClient.Do(req)
	if err != nil {
		return googleGmailMessage{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return googleGmailMessage{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return googleGmailMessage{}, gmailListHTTPError("message get", resp.StatusCode, data)
	}
	var raw googleGmailMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return googleGmailMessage{}, err
	}
	return raw, nil
}

type googleGmailMessageListRef struct {
	ID       string `json:"id"`
	ThreadID string `json:"threadId"`
}

type googleGmailMessageList struct {
	Messages           []googleGmailMessageListRef `json:"messages"`
	NextPageToken      string                      `json:"nextPageToken"`
	ResultSizeEstimate int                         `json:"resultSizeEstimate"`
}

type googleGmailThread struct {
	ID       string               `json:"id"`
	Snippet  string               `json:"snippet"`
	Messages []googleGmailMessage `json:"messages"`
}

type googleGmailMessage struct {
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

func gmailMetadataHeaders() []string {
	return []string{"From", "To", "Cc", "Subject", "Date", "Message-ID"}
}

func gmailHeaderValue(h googleGmailMessage, name string) string {
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

func gmailInternalDateRFC3339(ms string) string {
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

func gmailHeaderDateRFC3339(raw string) string {
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

// mapGmailAddressHeader parses a single address or address list into Contacts.
func mapGmailAddressHeader(raw string) []Contact {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	addrs, err := mail.ParseAddressList(raw)
	if err != nil {
		// Fail-open: keep a crude name/email split rather than dropping the party.
		if strings.Contains(raw, "@") {
			c := newContact(googleGmailServiceID, googleGmailServiceName, "", "", []string{raw}, nil)
			return []Contact{c}
		}
		c := newContact(googleGmailServiceID, googleGmailServiceName, "", raw, nil, nil)
		return []Contact{c}
	}
	out := make([]Contact, 0, len(addrs))
	for _, a := range addrs {
		if a == nil {
			continue
		}
		var emails []string
		if a.Address != "" {
			emails = []string{a.Address}
		}
		out = append(out, newContact(googleGmailServiceID, googleGmailServiceName, "", a.Name, emails, nil))
	}
	return out
}

func gmailDirectionFromLabels(labels []string) string {
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

func gmailMailboxFromLabels(labels []string) string {
	for _, l := range labels {
		switch strings.ToUpper(l) {
		case "INBOX", "SENT", "DRAFT", "SPAM", "TRASH", "STARRED", "IMPORTANT", "UNREAD":
			return strings.ToUpper(l)
		}
	}
	return ""
}

func gmailReadFromLabels(labels []string) bool {
	for _, l := range labels {
		if strings.EqualFold(l, "UNREAD") {
			return false
		}
	}
	return true
}

// mapGoogleGmailMessage converts a Gmail message into the neutral shape.
func mapGoogleGmailMessage(raw googleGmailMessage, format string) Message {
	fromList := mapGmailAddressHeader(gmailHeaderValue(raw, "From"))
	toList := mapGmailAddressHeader(gmailHeaderValue(raw, "To"))
	ccList := mapGmailAddressHeader(gmailHeaderValue(raw, "Cc"))
	subject := strings.TrimSpace(gmailHeaderValue(raw, "Subject"))
	if decoded, err := new(mime.WordDecoder).DecodeHeader(subject); err == nil {
		subject = strings.TrimSpace(decoded)
	}
	messageID := normalizeMessageID(gmailHeaderValue(raw, "Message-ID"))
	sentAt := gmailInternalDateRFC3339(raw.InternalDate)
	if sentAt == "" {
		sentAt = gmailHeaderDateRFC3339(gmailHeaderValue(raw, "Date"))
	}

	var from *Contact
	if len(fromList) > 0 {
		f := fromList[0]
		from = &f
	}

	src := MessageSource{
		Service:     googleGmailServiceID,
		ServiceName: googleGmailServiceName,
		ID:          raw.ID,
		ThreadID:    raw.ThreadID,
		URL:         gmailMessageURL(raw.ID),
		Mailbox:     gmailMailboxFromLabels(raw.LabelIDs),
		Canonical:   true,
	}
	extra := map[string]any{}
	if messageID != "" {
		extra["message_id"] = messageID
	}
	if raw.Snippet != "" {
		extra["snippet"] = raw.Snippet
	}
	if len(extra) > 0 {
		src.Extra = extra
	}

	msg := Message{
		Channel:    MessageChannelEmail,
		Subject:    subject,
		Snippet:    raw.Snippet,
		From:       from,
		To:         toList,
		CC:         ccList,
		SentAt:     sentAt,
		ReceivedAt: sentAt,
		Direction:  gmailDirectionFromLabels(raw.LabelIDs),
		Read:       gmailReadFromLabels(raw.LabelIDs),
		ThreadID:   raw.ThreadID,
		EntityID:   messageID,
		Labels:     append([]string(nil), raw.LabelIDs...),
		Sources:    []MessageSource{src},
	}
	if format == "full" {
		msg.Body = gmailDecodeBody(raw)
	}
	return msg
}

func gmailMessageURL(id string) string {
	if id == "" {
		return ""
	}
	return "https://mail.google.com/mail/u/0/#all/" + id
}

// gmailDecodeBody walks the Gmail payload for text/plain (fallback text/html
// stripped of tags is intentionally not done — agents can fetch HTML via proxy).
func gmailDecodeBody(raw googleGmailMessage) string {
	if raw.Payload == nil {
		return ""
	}
	if data := strings.TrimSpace(raw.Payload.Body.Data); data != "" {
		return gmailBase64Decode(data)
	}
	for _, part := range raw.Payload.Parts {
		if strings.HasPrefix(strings.ToLower(part.MimeType), "text/plain") {
			if data := strings.TrimSpace(part.Body.Data); data != "" {
				return gmailBase64Decode(data)
			}
		}
	}
	for _, part := range raw.Payload.Parts {
		if strings.HasPrefix(strings.ToLower(part.MimeType), "text/") {
			if data := strings.TrimSpace(part.Body.Data); data != "" {
				return gmailBase64Decode(data)
			}
		}
	}
	return ""
}

func gmailBase64Decode(data string) string {
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

// gmailListHTTPError keeps the operator-facing reason (API disabled, quota,
// insufficient scope) instead of a bare status code. Fail-open callers log this.
func gmailListHTTPError(op string, status int, body []byte) error {
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

// parseIntLoose parses a positive-ish integer from a query param.
func parseIntLoose(s string) (int, error) {
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
