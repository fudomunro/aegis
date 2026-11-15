package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

// fakeMailSink is an in-memory MailSink for connector ingest tests.
type fakeMailSink struct {
	messages map[string]kinds.Message
	payloads map[string][]byte
	cursors  map[string]shared.MailIngestCursor
	meta     map[string]string
	storeErr error
}

func newFakeMailSink() *fakeMailSink {
	return &fakeMailSink{
		messages: map[string]kinds.Message{},
		payloads: map[string][]byte{},
		cursors:  map[string]shared.MailIngestCursor{},
		meta:     map[string]string{},
	}
}

func (s *fakeMailSink) HasMessage(service string, msg kinds.Message) (bool, error) {
	if msg.EntityID != "" {
		for _, m := range s.messages {
			if m.EntityID == msg.EntityID {
				return true, nil
			}
		}
		return false, nil
	}
	_, ok := s.messages[service+"/"+firstMessageSourceID(msg)]
	return ok, nil
}

func firstMessageSourceID(msg kinds.Message) string {
	if len(msg.Sources) > 0 {
		return msg.Sources[0].ID
	}
	return ""
}

func (s *fakeMailSink) Store(service string, msg kinds.Message, rawPayload []byte) error {
	if s.storeErr != nil {
		return s.storeErr
	}
	key := service + "/" + firstMessageSourceID(msg)
	s.messages[key] = msg
	s.payloads[key] = rawPayload
	return nil
}

func (s *fakeMailSink) LoadCursor(service string) (shared.MailIngestCursor, error) {
	return s.cursors[service], nil
}

func (s *fakeMailSink) SaveCursor(service string, cur shared.MailIngestCursor) error {
	s.cursors[service] = cur
	return nil
}

func (s *fakeMailSink) MetaGet(key string) (string, error) { return s.meta[key], nil }
func (s *fakeMailSink) MetaSet(key, value string) error {
	s.meta[key] = value
	return nil
}

func TestMicrosoftMail_ImplementsProviderIngestAndConnector(t *testing.T) {
	c := NewMicrosoftMailConnector(newTestHost(""))
	var _ kinds.MessageProvider = c
	var _ MailIngest = c
	var _ ServiceConnector = c
	_ = c
}

func TestMicrosoftMailSource_Declaration(t *testing.T) {
	found := false
	for _, s := range MailSources() {
		if s.ServiceID != MicrosoftMailServiceID {
			continue
		}
		found = true
		if !s.Canonical {
			t.Fatal("microsoft-mail must claim canonical (live mailbox home)")
		}
		if s.Parent != "" {
			t.Fatalf("parent = %q, want none", s.Parent)
		}
	}
	if !found {
		t.Fatal("MicrosoftMailSource missing from MailSources()")
	}
	if src := MailSourceFor(MicrosoftMailServiceID); !src.Canonical {
		t.Fatal("MailSourceFor(microsoft-mail) must return the declaration")
	}
}

func TestMicrosoftMail_MapMicrosoftMessage(t *testing.T) {
	raw := microsoftMailboxMessage{
		ID:                "m1",
		InternetMessageID: "<Msg-ID-1@Example.com>",
		ConversationID:    "conv-1",
		Subject: "  Quarterly report ",
		BodyPreview:       "please see the numbers",
		Body:              &microsoftMailBody{ContentType: "text", Content: "full body text"},
		From:              &microsoftMailRecipient{},
		SentDateTime:      "2024-03-01T12:00:00Z",
		ReceivedDateTime:  "2024-03-01T12:00:01Z",
		IsRead:            false,
		ParentFolderID:    "folder-inbox",
		WebLink:           "https://outlook.office.com/mail/id/m1",
		HasAttachments:    true,
	}
	raw.From.EmailAddress.Name = "Ada Lovelace"
	raw.From.EmailAddress.Address = "ada@example.com"
	raw.ToRecipients = []microsoftMailRecipient{{}}
	raw.ToRecipients[0].EmailAddress.Address = "me@example.com"
	folders := map[string]string{"folder-inbox": "Inbox"}

	msg := MapMicrosoftMessage(raw, folders, true)
	if msg.Channel != kinds.MessageChannelEmail {
		t.Fatalf("channel = %q", msg.Channel)
	}
	if msg.Subject != "Quarterly report" {
		t.Fatalf("subject = %q", msg.Subject)
	}
	if msg.Snippet != "please see the numbers" {
		t.Fatalf("snippet = %q", msg.Snippet)
	}
	if msg.Body != "full body text" {
		t.Fatalf("body = %q", msg.Body)
	}
	if msg.EntityID != "msg-id-1@example.com" {
		t.Fatalf("entity id = %q (want normalized Message-ID)", msg.EntityID)
	}
	if msg.SentAt != "2024-03-01T12:00:00Z" || msg.ReceivedAt != "2024-03-01T12:00:01Z" {
		t.Fatalf("times = %q / %q", msg.SentAt, msg.ReceivedAt)
	}
	if msg.From == nil || msg.From.Name != "Ada Lovelace" || msg.From.Emails[0] != "ada@example.com" {
		t.Fatalf("from = %+v", msg.From)
	}
	if len(msg.To) != 1 || msg.To[0].Emails[0] != "me@example.com" {
		t.Fatalf("to = %+v", msg.To)
	}
	if msg.Direction != "inbound" {
		t.Fatalf("direction = %q", msg.Direction)
	}
	if msg.Read {
		t.Fatal("want unread")
	}
	if msg.ThreadID != "conv-1" {
		t.Fatalf("thread = %q", msg.ThreadID)
	}
	src := msg.Sources[0]
	if src.Service != MicrosoftMailServiceID || src.ID != "m1" || !src.Canonical {
		t.Fatalf("source = %+v", src)
	}
	if src.Mailbox != "INBOX" {
		t.Fatalf("mailbox = %q", src.Mailbox)
	}
	if src.URL != "https://outlook.office.com/mail/id/m1" {
		t.Fatalf("url = %q", src.URL)
	}
	if src.Extra["message_id"] != "msg-id-1@example.com" {
		t.Fatalf("extra message_id = %v", src.Extra["message_id"])
	}
	if src.Extra["has_attachments"] != true {
		t.Fatalf("extra has_attachments = %v", src.Extra["has_attachments"])
	}
	labels := strings.Join(msg.Labels, ",")
	if labels != "INBOX,UNREAD" {
		t.Fatalf("labels = %v", msg.Labels)
	}

	// Library list mapping stays snippet-level.
	list := MapMicrosoftMessage(raw, folders, false)
	if list.Body != "" {
		t.Fatalf("list body = %q, want empty", list.Body)
	}

	// Unknown folder degrades to unknown direction without a mailbox.
	unknown := MapMicrosoftMessage(raw, map[string]string{}, false)
	if unknown.Direction != "unknown" || unknown.Sources[0].Mailbox != "" {
		t.Fatalf("unknown folder: direction=%q mailbox=%q", unknown.Direction, unknown.Sources[0].Mailbox)
	}
}

// microsoftMailPage is one mailbox page in the fixture: the message ids it
// serves and the next-page skiptoken ("" when it is the last page).
type microsoftMailPage struct {
	items []string
	next  string
}

// microsoftMailFixture serves mailFolders plus skiptoken-keyed message
// pages, so repeated runs (cursor resumes) see stable pages.
func microsoftMailFixture(t *testing.T, pages map[string]microsoftMailPage) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/me/mailFolders":
			_, _ = w.Write([]byte(`{"value":[
				{"id":"f-inbox","displayName":"Inbox"},
				{"id":"f-sent","displayName":"Sent Items"},
				{"id":"f-drafts","displayName":"Drafts"}
			]}`))
		case r.URL.Path == "/me/messages" && !strings.Contains(r.URL.Path, "/messages/"):
			page, ok := pages[r.URL.Query().Get("$skiptoken")]
			if !ok {
				_, _ = w.Write([]byte(`{"value":[]}`))
				return
			}
			var sb strings.Builder
			sb.WriteString(`{"value":[`)
			for i, id := range page.items {
				if i > 0 {
					sb.WriteString(",")
				}
				// One inbox message per id; sent for "s" ids, drafts for "d" ids.
				folder := "f-inbox"
				isRead := true
				if strings.HasPrefix(id, "s") {
					folder = "f-sent"
				}
				if strings.HasPrefix(id, "d") {
					folder = "f-drafts"
				}
				if strings.HasSuffix(id, "u") {
					isRead = false
				}
				sb.WriteString(`{"id":"` + id + `","internetMessageId":"<` + id + `@example.com>",` +
					`"conversationId":"conv-` + id + `","subject":"Subject ` + id + `",` +
					`"bodyPreview":"preview ` + id + `","body":{"contentType":"text","content":"body ` + id + `"},` +
					`"from":{"emailAddress":{"name":"Sender","address":"sender@example.com"}},` +
					`"toRecipients":[{"emailAddress":{"address":"me@example.com"}}],` +
					`"sentDateTime":"2024-03-01T12:00:00Z","receivedDateTime":"2024-03-01T12:00:01Z",` +
					`"isRead":` + boolJSON(isRead) + `,"parentFolderId":"` + folder + `",` +
					`"webLink":"https://outlook.office.com/mail/id/` + id + `"}`)
			}
			sb.WriteString(`]`)
			if page.next != "" {
				next := "http://" + r.Host + "/me/messages?$skiptoken=" + page.next
				sb.WriteString(`,"@odata.nextLink":"` + next + `"`)
			}
			sb.WriteString(`}`)
			_, _ = w.Write([]byte(sb.String()))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func boolJSON(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func TestMicrosoftMail_ListMessages_MapsAndPages(t *testing.T) {
	ts := microsoftMailFixture(t, map[string]microsoftMailPage{
		"":   {items: []string{"m1u", "s1"}, next: "p2"},
		"p2": {items: []string{"d1"}},
	})
	th := newTestHostWithService(MicrosoftMailServiceID, ts.URL, "https://aegis.example.com")
	th.setToken(MicrosoftMailServiceID, "tok")
	c := NewMicrosoftMailConnector(th)

	msgs, truncated, err := c.ListMessages(context.Background())
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if truncated {
		t.Fatal("expected truncated=false")
	}
	if len(msgs) != 3 {
		t.Fatalf("messages = %d, want 3", len(msgs))
	}
	if msgs[0].Sources[0].Mailbox != "INBOX" || msgs[0].Direction != "inbound" || msgs[0].Read {
		t.Fatalf("m1u = %+v", msgs[0])
	}
	if msgs[1].Sources[0].Mailbox != "SENT" || msgs[1].Direction != "outbound" {
		t.Fatalf("s1 = %+v", msgs[1])
	}
	if msgs[2].Sources[0].Mailbox != "DRAFT" || msgs[2].Direction != "unknown" {
		t.Fatalf("d1 = %+v", msgs[2])
	}
	if msgs[0].Body != "" {
		t.Fatalf("library list leaked body: %q", msgs[0].Body)
	}
}

func TestMicrosoftMail_ListMessages_Unauthorized(t *testing.T) {
	th := newTestHostWithService(MicrosoftMailServiceID, "https://graph.example.com", "https://aegis.example.com")
	c := NewMicrosoftMailConnector(th)
	if _, _, err := c.ListMessages(context.Background()); err != kinds.ErrUnauthorized {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestMicrosoftMail_IngestMail_StoresAndResumesCursor(t *testing.T) {
	ts := microsoftMailFixture(t, map[string]microsoftMailPage{
		"":   {items: []string{"m1", "m2", "m3"}, next: "p2"},
		"p2": {items: []string{"m4"}},
	})
	th := newTestHostWithService(MicrosoftMailServiceID, ts.URL, "https://aegis.example.com")
	th.setToken(MicrosoftMailServiceID, "tok")
	c := NewMicrosoftMailConnector(th)
	sink := newFakeMailSink()

	// First run caps at two messages: the mid-page overflow leaves the
	// cursor on the first page so nothing is skipped.
	res, err := c.IngestMail(context.Background(), sink, shared.MailIngestOptions{MaxMessages: 2})
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if res.Stored != 2 || !res.Truncated || res.Done {
		t.Fatalf("run 1 result = %+v", res)
	}
	cur := sink.cursors[MicrosoftMailServiceID]
	if cur.PageToken != "" {
		t.Fatalf("run 1 cursor = %q, want the first page replayed", cur.PageToken)
	}

	// Second run finishes the walk from the durable continuation.
	res2, err := c.IngestMail(context.Background(), sink, shared.MailIngestOptions{MaxMessages: 1000})
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if !res2.Done || res2.Stored != 4 {
		t.Fatalf("run 2 result = %+v", res2)
	}
	if len(sink.messages) != 4 {
		t.Fatalf("stored = %d, want 4", len(sink.messages))
	}
	msg := sink.messages[MicrosoftMailServiceID+"/m1"]
	if msg.Body != "body m1" || msg.EntityID != "m1@example.com" {
		t.Fatalf("stored m1 = %+v", msg)
	}
	if len(sink.payloads[MicrosoftMailServiceID+"/m1"]) == 0 {
		t.Fatal("raw payload missing")
	}
	cur = sink.cursors[MicrosoftMailServiceID]
	if !cur.Done || cur.LastSyncAt == "" {
		t.Fatalf("final cursor = %+v", cur)
	}

	// A fresh run after Done restarts the walk and upserts.
	res3, err := c.IngestMail(context.Background(), sink, shared.MailIngestOptions{})
	if err != nil {
		t.Fatalf("run 3: %v", err)
	}
	if !res3.Done || res3.Stored < 4 {
		t.Fatalf("run 3 result = %+v", res3)
	}
}

func TestMicrosoftMail_IngestMail_ContinuationQueryIsOpaque(t *testing.T) {
	ts := microsoftMailFixture(t, map[string]microsoftMailPage{
		"":   {items: []string{"m1"}, next: "p2"},
		"p2": {items: []string{"m2"}},
	})
	th := newTestHostWithService(MicrosoftMailServiceID, ts.URL, "https://aegis.example.com")
	th.setToken(MicrosoftMailServiceID, "tok")
	c := NewMicrosoftMailConnector(th)
	sink := newFakeMailSink()

	res, err := c.IngestMail(context.Background(), sink, shared.MailIngestOptions{MaxMessages: 1})
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if res.Stored != 1 || res.Done {
		t.Fatalf("run 1 result = %+v", res)
	}
	// The durable cursor records Graph's own continuation query so the walk
	// survives an endpoint move.
	if cur := sink.cursors[MicrosoftMailServiceID]; cur.PageToken != "$skiptoken=p2" {
		t.Fatalf("cursor = %q, want $skiptoken=p2", cur.PageToken)
	}

	res2, err := c.IngestMail(context.Background(), sink, shared.MailIngestOptions{MaxMessages: 100})
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if res2.Stored != 1 || !res2.Done {
		t.Fatalf("run 2 result = %+v", res2)
	}
	if len(sink.messages) != 2 {
		t.Fatalf("stored = %d, want 2: %+v", len(sink.messages), sink.messages)
	}
	if _, ok := sink.messages[MicrosoftMailServiceID+"/m2"]; !ok {
		t.Fatal("m2 must arrive from the continuation page")
	}
}

func TestMicrosoftMail_IngestMail_ThrottleKeepsCursorAndSendsRetryAfter(t *testing.T) {
	var calls int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/me/mailFolders" {
			_, _ = w.Write([]byte(`{"value":[]}`))
			return
		}
		calls++
		if calls == 1 {
			_, _ = w.Write([]byte(`{"value":[{"id":"m1","subject":"one"}],"@odata.nextLink":"http://` +
				r.Host + `/me/messages?$skiptoken=p2"}`))
			return
		}
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(ts.Close)

	th := newTestHostWithService(MicrosoftMailServiceID, ts.URL, "https://aegis.example.com")
	th.setToken(MicrosoftMailServiceID, "tok")
	c := NewMicrosoftMailConnector(th)
	sink := newFakeMailSink()

	res, err := c.IngestMail(context.Background(), sink, shared.MailIngestOptions{MaxMessages: 100})
	if err == nil {
		t.Fatal("want throttle error")
	}
	if res.Stored != 1 || res.RetryAfter != 7*time.Second {
		t.Fatalf("result = %+v (retry after %v)", res, res.RetryAfter)
	}
	// Cursor keeps the completed page's continuation so page two retries.
	if cur := sink.cursors[MicrosoftMailServiceID]; cur.PageToken != "$skiptoken=p2" {
		t.Fatalf("cursor = %q, want $skiptoken=p2", cur.PageToken)
	}
}

func TestMicrosoftMail_IngestMail_Unauthorized(t *testing.T) {
	th := newTestHostWithService(MicrosoftMailServiceID, "https://graph.example.com", "https://aegis.example.com")
	c := NewMicrosoftMailConnector(th)
	res, err := c.IngestMail(context.Background(), newFakeMailSink(), shared.MailIngestOptions{})
	if !errors.Is(err, kinds.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if res.Error == "" {
		t.Fatal("want recorded error")
	}
}

func TestMicrosoftMail_IngestMail_ResetCursorRestarts(t *testing.T) {
	ts := microsoftMailFixture(t, map[string]microsoftMailPage{
		"":   {items: []string{"m1"}, next: "p2"},
		"p2": {items: []string{"m2"}},
	})
	th := newTestHostWithService(MicrosoftMailServiceID, ts.URL, "https://aegis.example.com")
	th.setToken(MicrosoftMailServiceID, "tok")
	c := NewMicrosoftMailConnector(th)
	sink := newFakeMailSink()

	res, err := c.IngestMail(context.Background(), sink, shared.MailIngestOptions{MaxMessages: 1})
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if res.Stored != 1 {
		t.Fatalf("run 1 = %+v", res)
	}
	// Without reset, the run resumes mid-walk; with reset it starts over.
	res2, err := c.IngestMail(context.Background(), sink,
		shared.MailIngestOptions{MaxMessages: 1, ResetCursor: true})
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if res2.Stored != 1 || res2.LastStored != "m1" {
		t.Fatalf("run 2 = %+v, want a restarted walk", res2)
	}
}

func TestMicrosoftMail_IngestMail_StoreFailureFailsOpen(t *testing.T) {
	ts := microsoftMailFixture(t, map[string]microsoftMailPage{
		"": {items: []string{"m1", "m2"}},
	})
	th := newTestHostWithService(MicrosoftMailServiceID, ts.URL, "https://aegis.example.com")
	th.setToken(MicrosoftMailServiceID, "tok")
	c := NewMicrosoftMailConnector(th)
	sink := newFakeMailSink()
	sink.storeErr = errors.New("disk full")

	res, err := c.IngestMail(context.Background(), sink, shared.MailIngestOptions{})
	if err != nil {
		t.Fatalf("IngestMail: %v", err)
	}
	if res.Failed != 2 || res.Stored != 0 || !res.Done {
		t.Fatalf("result = %+v", res)
	}
}

func TestMicrosoftMail_HandleProxy_GetMessage(t *testing.T) {
	var gotPaths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/me/mailFolders" {
			_, _ = w.Write([]byte(`{"value":[{"id":"f-inbox","displayName":"Inbox"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"m1","internetMessageId":"<m1@example.com>","subject":"Hi",` +
			`"bodyPreview":"hi there","body":{"contentType":"text","content":"full text"},` +
			`"from":{"emailAddress":{"address":"ada@example.com"}},"sentDateTime":"2024-03-01T12:00:00Z",` +
			`"isRead":true,"parentFolderId":"f-inbox"}`))
	}))
	t.Cleanup(ts.Close)

	th := newTestHostWithService(MicrosoftMailServiceID, ts.URL, "https://aegis.example.com")
	th.setToken(MicrosoftMailServiceID, "tok")
	c := NewMicrosoftMailConnector(th)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/services/microsoft-mail/messages/m1", nil)
	c.HandleProxy(rec, req, "/messages/m1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(gotPaths) == 0 || !strings.HasSuffix(gotPaths[0], "/me/messages/m1") {
		t.Fatalf("upstream paths = %v", gotPaths)
	}
	var msg kinds.Message
	if err := json.Unmarshal(rec.Body.Bytes(), &msg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if msg.Body != "full text" || msg.Subject != "Hi" {
		t.Fatalf("message = %+v", msg)
	}

	// Collection lists and deeper paths are not content transport here.
	rec = httptest.NewRecorder()
	c.HandleProxy(rec, req, "/messages")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("list status = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	c.HandleProxy(rec, req, "/messages/m1/attachments")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("attachments status = %d", rec.Code)
	}
}

func TestMicrosoftMailIDFromProxyPath(t *testing.T) {
	if id, ok := MicrosoftMailIDFromProxyPath("/messages/m1"); !ok || id != "m1" {
		t.Fatalf("bare id = %q %v", id, ok)
	}
	if id, ok := MicrosoftMailIDFromProxyPath("/me/messages/abc-123_x"); !ok || id != "abc-123_x" {
		t.Fatalf("me-prefixed id = %q %v", id, ok)
	}
	for _, p := range []string{
		"/messages", "/messages/", "/messages/../etc", "/messages/a/b",
		"/threads/t1", "/me/messages/a b", "",
	} {
		if _, ok := MicrosoftMailIDFromProxyPath(p); ok {
			t.Fatalf("accepted %q, want reject", p)
		}
	}
}

func TestMicrosoftMail_ListMessages_FailOpenPartial(t *testing.T) {
	page := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/me/mailFolders" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"value":[]}`))
			return
		}
		page++
		if page == 1 {
			w.Header().Set("Content-Type", "application/json")
			next := "http://" + r.Host + "/me/messages?$skiptoken=2"
			_, _ = w.Write([]byte(`{"value":[
				{"id":"m1","subject":"Hello","bodyPreview":"hi",
				 "from":{"emailAddress":{"name":"Ada","address":"ada@example.com"}}}
			],"@odata.nextLink":"` + next + `"}`))
			return
		}
		http.Error(w, "graph exploded", http.StatusInternalServerError)
	}))
	t.Cleanup(ts.Close)

	th := newTestHostWithService(MicrosoftMailServiceID, ts.URL, "https://aegis.example.com")
	th.setToken(MicrosoftMailServiceID, "tok")
	c := NewMicrosoftMailConnector(th)

	msgs, truncated, err := c.ListMessages(context.Background())
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Subject != "Hello" {
		t.Fatalf("messages = %+v, want the gathered page", msgs)
	}
	if !truncated {
		t.Fatal("want truncated when the walk stopped on a failed page")
	}
}
