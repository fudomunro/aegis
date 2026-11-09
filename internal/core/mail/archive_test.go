package mail

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/types"
)

func openTestArchive(t *testing.T) *Archive {
	t.Helper()
	a, err := Open(filepath.Join(t.TempDir(), "mail-archive.db"))
	if err != nil {
		t.Fatalf("open mail archive: %v", err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

func sampleMailRecord(id, subject, body string) Record {
	return Record{
		Service:     connectors.GoogleGmailServiceID,
		ServiceName: connectors.GoogleGmailServiceName,
		ID:          id,
		ThreadID:    "t-" + id,
		MessageID:   "<" + id + "@example.com>",
		Mailbox:     "INBOX",
		Labels:      []string{"INBOX", "UNREAD"},
		Subject:     subject,
		Snippet:     body[:minInt(20, len(body))],
		FromName:    "Ada Lovelace",
		FromEmail:   "ada@example.com",
		To:          []kinds.Contact{kinds.NewContact(connectors.GoogleGmailServiceID, connectors.GoogleGmailServiceName, "", "Me", []string{"me@example.com"}, nil)},
		SentAt:      "2024-06-01T09:00:00Z",
		ReceivedAt:  "2024-06-01T09:00:05Z",
		Direction:   "inbound",
		Read:        false,
		BodyText:    body,
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestArchiveDisabled(t *testing.T) {
	a, err := Open("")
	if err != nil || a != nil {
		t.Fatalf("empty path should disable archive, got %v %v", a, err)
	}
	if err := a.Upsert(Record{Service: "x", ID: "1"}); err == nil {
		t.Fatal("expected disabled upsert error")
	}
	if _, err := a.Get("x", "1"); err == nil {
		t.Fatal("expected disabled get error")
	}
	if _, err := a.Search(SearchQuery{}); err == nil {
		t.Fatal("expected disabled search error")
	}
}

func TestArchiveUpsertGetSearch(t *testing.T) {
	a := openTestArchive(t)
	payload := []byte(`{"id":"m1","subject":"Invoice due"}`)
	gz, err := GzipPayload(payload)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	rec := sampleMailRecord("m1", "Invoice due", "Please pay the June invoice for widgets.")
	rec.PayloadGzip = gz
	if err := a.Upsert(rec); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Second upsert replaces metadata but keeps payload when omitted.
	rec.Subject = "Invoice due (updated)"
	rec.PayloadGzip = nil
	if err := a.Upsert(rec); err != nil {
		t.Fatalf("upsert replace: %v", err)
	}
	got, err := a.Get(connectors.GoogleGmailServiceID, "m1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Subject != "Invoice due (updated)" {
		t.Fatalf("subject = %q", got.Subject)
	}
	if len(got.PayloadGzip) == 0 {
		t.Fatal("expected payload to be retained")
	}
	raw, err := got.Payload()
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	if !strings.Contains(string(raw), "Invoice due") {
		t.Fatalf("payload = %s", raw)
	}

	if err := a.Upsert(sampleMailRecord("m2", "Lunch plans", "Want to grab tacos tomorrow?")); err != nil {
		t.Fatalf("upsert m2: %v", err)
	}

	hits, err := a.Search(SearchQuery{Q: "invoice", WithBody: true})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if hits.Total != 1 || len(hits.Messages) != 1 {
		t.Fatalf("fts hits = %+v", hits)
	}
	if !strings.Contains(hits.Messages[0].Body, "widgets") {
		t.Fatalf("body missing: %+v", hits.Messages[0])
	}

	byFrom, err := a.Search(SearchQuery{From: "ada@example.com"})
	if err != nil {
		t.Fatalf("search from: %v", err)
	}
	if byFrom.Total != 2 {
		t.Fatalf("from total = %d", byFrom.Total)
	}

	byTo, err := a.Search(SearchQuery{To: "me@example.com", After: "2024-06-01", Before: "2024-06-02"})
	if err != nil {
		t.Fatalf("search to/date: %v", err)
	}
	if byTo.Total != 2 {
		t.Fatalf("to/date total = %d", byTo.Total)
	}

	byLabel, err := a.Search(SearchQuery{Labels: []string{"INBOX"}})
	if err != nil {
		t.Fatalf("search label: %v", err)
	}
	if byLabel.Total != 2 {
		t.Fatalf("label total = %d", byLabel.Total)
	}

	// FTS operator characters must not error the query.
	if _, err := a.Search(SearchQuery{Q: `NEAR(a b) OR "x*"`}); err != nil {
		t.Fatalf("hostile fts query: %v", err)
	}

	// Oldest-first is required for "first/earliest message" questions.
	oldest, err := a.Search(SearchQuery{Sort: "asc", Limit: 1})
	if err != nil {
		t.Fatalf("search asc: %v", err)
	}
	if len(oldest.Messages) != 1 {
		t.Fatalf("oldest hits = %+v", oldest)
	}
	if oldest.Messages[0].SentAt != "2024-06-01T09:00:00Z" {
		t.Fatalf("oldest sent_at = %q", oldest.Messages[0].SentAt)
	}
	newest, err := a.Search(SearchQuery{Sort: "desc", Limit: 1})
	if err != nil {
		t.Fatalf("search desc: %v", err)
	}
	if len(newest.Messages) != 1 || newest.Messages[0].Subject != "Lunch plans" {
		t.Fatalf("newest = %+v", newest.Messages)
	}

	st, err := a.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !st.Enabled || st.MessageCount != 2 || st.ByService[connectors.GoogleGmailServiceID] != 2 {
		t.Fatalf("status = %+v", st)
	}
}

func TestArchiveSyncCursor(t *testing.T) {
	a := openTestArchive(t)
	if err := a.SaveSyncCursor(connectors.GoogleGmailServiceID, types.MailIngestCursor{
		PageToken:  "tok1",
		LastSyncAt: "2024-06-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("save cursor: %v", err)
	}
	cur, err := a.LoadSyncCursor(connectors.GoogleGmailServiceID)
	if err != nil {
		t.Fatalf("load cursor: %v", err)
	}
	if cur.PageToken != "tok1" || cur.Done || cur.LastSyncAt != "2024-06-01T00:00:00Z" {
		t.Fatalf("cursor = %+v", cur)
	}
	if err := a.SaveSyncCursor(connectors.GoogleGmailServiceID, types.MailIngestCursor{PageToken: "tok1", Done: true}); err != nil {
		t.Fatalf("save done: %v", err)
	}
	cur, _ = a.LoadSyncCursor(connectors.GoogleGmailServiceID)
	if !cur.Done {
		t.Fatal("expected done cursor")
	}
}

func TestRecordFromMessage(t *testing.T) {
	msg := newMessage(connectors.GoogleGmailServiceID, connectors.GoogleGmailServiceName, "id9", kinds.MessageChannelEmail, "Hello", "hi", "2024-06-01T09:00:00Z")
	msg.From = &kinds.Contact{Name: "Ada", Emails: []string{"Ada@Example.com"}}
	msg.EntityID = "<id9@example.com>"
	msg.Body = "body text"
	rec := RecordFromMessage("", "", msg, []byte{1})
	if rec.Service != connectors.GoogleGmailServiceID || rec.ID != "id9" || rec.FromEmail != "Ada@Example.com" {
		t.Fatalf("rec = %+v", rec)
	}
	if rec.MessageID != "<id9@example.com>" || rec.BodyText != "body text" {
		t.Fatalf("rec meta = %+v", rec)
	}
}

func newMessage(service, serviceName, id, channel, subject, snippet, sentAt string) kinds.Message {
	src := kinds.MessageSource{
		Service:     service,
		ServiceName: serviceName,
		ID:          id,
		Canonical:   true,
	}
	return kinds.Message{
		Channel: channel,
		Subject: subject,
		Snippet: snippet,
		SentAt:  sentAt,
		Sources: []kinds.MessageSource{src},
	}
}
