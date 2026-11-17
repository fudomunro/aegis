package mail

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
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
	if err := a.SaveSyncCursor(connectors.GoogleGmailServiceID, shared.MailIngestCursor{
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
	if err := a.SaveSyncCursor(connectors.GoogleGmailServiceID, shared.MailIngestCursor{PageToken: "tok1", Done: true}); err != nil {
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

func TestArchiveGetMergedSiblings(t *testing.T) {
	a := openTestArchive(t)
	gmail := sampleMailRecord("g1", "Shared message", "hello")
	gmail.MessageID = "<shared@example.com>"
	gmail.Dataset = "shire"
	if err := a.Upsert(gmail); err != nil {
		t.Fatalf("upsert gmail: %v", err)
	}
	takeout := sampleMailRecord("t1", "Shared message", "hello")
	takeout.MessageID = "<shared@example.com>"
	takeout.Service = connectors.TakeoutServiceID
	takeout.ServiceName = connectors.TakeoutServiceName
	takeout.Dataset = "shire"
	if err := a.Upsert(takeout); err != nil {
		t.Fatalf("upsert takeout: %v", err)
	}

	// Fetch by either source id returns the same merged identity entry
	// (issue #311).
	for _, src := range []struct{ service, id string }{
		{connectors.GoogleGmailServiceID, "g1"},
		{connectors.TakeoutServiceID, "t1"},
	} {
		msg, ok := a.GetMerged("shire", src.service, src.id)
		if !ok {
			t.Fatalf("GetMerged(%s, %s) missing", src.service, src.id)
		}
		if len(msg.Sources) != 2 || msg.Match != "message_id" {
			t.Fatalf("merged entry = %+v", msg)
		}
		if msg.Body == "" {
			t.Fatalf("fetch must carry body: %+v", msg)
		}
	}

	// Dataset isolation: the other partition cannot fetch the row even
	// with a direct id (docs/data-scopes.md).
	if _, ok := a.GetMerged("gondor", connectors.GoogleGmailServiceID, "g1"); ok {
		t.Fatal("cross-dataset fetch must miss")
	}
	if _, ok := a.GetMerged("shire", connectors.GoogleGmailServiceID, "missing"); ok {
		t.Fatal("missing row must miss")
	}
}

func TestArchiveSearchAll(t *testing.T) {
	a := openTestArchive(t)
	for i := 0; i < 5; i++ {
		rec := sampleMailRecord("m"+string(rune('a'+i)), "Subject "+string(rune('a'+i)), "body")
		rec.MessageID = "<" + rec.ID + "@example.com>"
		if err := a.Upsert(rec); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	resp, err := a.Search(SearchQuery{All: true})
	if err != nil {
		t.Fatalf("search all: %v", err)
	}
	if resp.Total != 5 || len(resp.Messages) != 5 {
		t.Fatalf("all = %d msgs=%d", resp.Total, len(resp.Messages))
	}
	if resp.Truncated {
		t.Fatal("window must not report truncated")
	}
	// Filters apply to All queries the same way.
	resp, err = a.Search(SearchQuery{All: true, Q: "subject-a", WithBody: true})
	if err != nil {
		t.Fatalf("search all q: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("filtered all = %+v", resp)
	}
	// WithBody fills body text on All queries (issue #311 fetch/search).
	if resp.Messages[0].Body == "" {
		t.Fatalf("body missing: %+v", resp.Messages[0])
	}
}

// searchTotal counts matches for the given free-text query.
func searchTotal(t *testing.T, a *Archive, q string) int {
	t.Helper()
	resp, err := a.Search(SearchQuery{Q: q, Limit: 50})
	if err != nil {
		t.Fatalf("search %q: %v", q, err)
	}
	return resp.Total
}

// TestArchiveStoreBuffersUntilFlush pins the batched ingest write path:
// Store buffers rows and they only become queryable at a flush point.
// Ingest calls Upsert through Sink.Store and pays one commit per batch
// instead of per message.
func TestArchiveStoreBuffersUntilFlush(t *testing.T) {
	a := openTestArchive(t)
	for i := 0; i < 3; i++ {
		if err := a.Store(sampleMailRecord(fmt.Sprintf("b%d", i), "buffered", "some body text")); err != nil {
			t.Fatalf("store %d: %v", i, err)
		}
	}
	if got := searchTotal(t, a, "buffered"); got != 0 {
		t.Fatalf("buffered rows are queryable before flush: %d", got)
	}
	if err := a.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if got := searchTotal(t, a, "buffered"); got != 3 {
		t.Fatalf("after flush got %d rows, want 3", got)
	}
	// Flush is idempotent on an empty buffer.
	if err := a.Flush(); err != nil {
		t.Fatalf("second flush: %v", err)
	}
}

// TestArchiveSetMetaCommitsPending pins the ordering invariant the
// ingests rely on: a durable position is written together with the rows
// it covers. The takeout walk cursor rides on SetMeta.
func TestArchiveSetMetaCommitsPending(t *testing.T) {
	a := openTestArchive(t)
	for i := 0; i < 2; i++ {
		if err := a.Store(sampleMailRecord(fmt.Sprintf("m%d", i), "meta", "some body text")); err != nil {
			t.Fatalf("store %d: %v", i, err)
		}
	}
	if err := a.SetMeta("takeout:cursor", `{"offset":42}`); err != nil {
		t.Fatalf("set meta: %v", err)
	}
	if got := searchTotal(t, a, "meta"); got != 2 {
		t.Fatalf("rows did not commit with the meta write: %d", got)
	}
	v, err := a.GetMeta("takeout:cursor")
	if err != nil || v != `{"offset":42}` {
		t.Fatalf("meta = %q, %v", v, err)
	}
}

// TestArchiveSaveSyncCursorCommitsPending pins the same invariant on the
// sync cursor path used by the live Gmail and Graph walks.
func TestArchiveSaveSyncCursorCommitsPending(t *testing.T) {
	a := openTestArchive(t)
	for i := 0; i < 2; i++ {
		if err := a.Store(sampleMailRecord(fmt.Sprintf("c%d", i), "cursor", "some body text")); err != nil {
			t.Fatalf("store %d: %v", i, err)
		}
	}
	want := shared.MailIngestCursor{PageToken: "page-1", LastSyncAt: "2024-06-01T09:00:00Z"}
	if err := a.SaveSyncCursor(connectors.GoogleGmailServiceID, want); err != nil {
		t.Fatalf("save cursor: %v", err)
	}
	if got := searchTotal(t, a, "cursor"); got != 2 {
		t.Fatalf("rows did not commit with the cursor write: %d", got)
	}
	got, err := a.LoadSyncCursor(connectors.GoogleGmailServiceID)
	if err != nil {
		t.Fatalf("load cursor: %v", err)
	}
	if got.PageToken != want.PageToken || got.LastSyncAt != want.LastSyncAt {
		t.Fatalf("cursor = %+v, want %+v", got, want)
	}
}

// TestArchiveStoreAutoFlushes bounds the buffer: a long walk commits on
// its own every ingestBatchSize rows so memory stays flat and a crash
// cannot strand a whole export in the buffer. The remainder stays
// buffered until the cursor write (or Flush) closes the batch.
func TestArchiveStoreAutoFlushes(t *testing.T) {
	a := openTestArchive(t)
	n := ingestBatchSize + 5
	for i := 0; i < n; i++ {
		if err := a.Store(sampleMailRecord(fmt.Sprintf("a%d", i), "autoflush", "some body text")); err != nil {
			t.Fatalf("store %d: %v", i, err)
		}
	}
	if got := searchTotal(t, a, "autoflush"); got != ingestBatchSize {
		t.Fatalf("auto-flush committed %d rows, want %d", got, ingestBatchSize)
	}
	if err := a.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if got := searchTotal(t, a, "autoflush"); got != n {
		t.Fatalf("after flush got %d rows, want %d", got, n)
	}
}

// TestArchiveCloseFlushes keeps rows from stranding in the buffer when a
// run ends without a cursor write (fail-open callers that drop the error).
func TestArchiveCloseFlushes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mail-archive.db")
	a, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := a.Store(sampleMailRecord("z1", "closed", "some body text")); err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	b, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer b.Close()
	if got := searchTotal(t, b, "closed"); got != 1 {
		t.Fatalf("row did not survive close: %d", got)
	}
}

// TestSinkStoreCommitsWithCursor guards the sink wiring itself: Sink.Store
// must buffer (not write immediately), so the ingest's cursor write is
// what commits the batch.
func TestSinkStoreCommitsWithCursor(t *testing.T) {
	a := openTestArchive(t)
	s := Sink{A: a}
	msg := newMessage(connectors.GoogleGmailServiceID, connectors.GoogleGmailServiceName,
		"sink1", kinds.MessageChannelEmail, "sink subject", "snip", "2024-06-01T09:00:00Z")
	msg.Body = "sink body text"
	if err := s.Store(connectors.GoogleGmailServiceID, msg, []byte(`{"raw":true}`)); err != nil {
		t.Fatalf("sink store: %v", err)
	}
	if got := searchTotal(t, a, "sink"); got != 0 {
		t.Fatalf("sink store wrote through immediately: %d", got)
	}
	if err := s.SaveCursor(connectors.GoogleGmailServiceID, shared.MailIngestCursor{PageToken: "p"}); err != nil {
		t.Fatalf("save cursor: %v", err)
	}
	if got := searchTotal(t, a, "sink"); got != 1 {
		t.Fatalf("rows did not commit with the sink cursor write: %d", got)
	}
}
