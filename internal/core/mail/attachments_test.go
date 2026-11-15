package mail

import (
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/kinds"
)

// sampleRFC822Attachment is a Takeout-style message with one base64 PDF
// attachment and one text attachment (issue #215 fixture).
const sampleRFC822Attachment = "From: sender@example.com\r\n" +
	"To: me@example.com\r\n" +
	"Subject: Certificate\r\n" +
	"Date: Sat, 01 Jun 2024 09:00:00 +0000\r\n" +
	"Message-ID: <cert-1@example.com>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"OUTER\"\r\n" +
	"\r\n" +
	"--OUTER\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"Please find the certificate attached.\r\n" +
	"--OUTER\r\n" +
	"Content-Type: application/pdf; name=\"cert.pdf\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"Content-Disposition: attachment; filename=\"cert.pdf\"\r\n" +
	"\r\n" +
	"JVBERi0xLjQ=\r\n" +
	"--OUTER\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"Content-Disposition: attachment; filename=\"notes.txt\"\r\n" +
	"\r\n" +
	"aGVsbG8gd29ybGQ=\r\n" +
	"--OUTER--\r\n"

// sampleTakeoutRecord stores one Takeout copy of the fixture message with
// its raw payload, like the ingest does.
func sampleTakeoutRecord(t *testing.T, id string, raw string) Record {
	t.Helper()
	gz, err := GzipPayload([]byte(raw))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	msg, err := parseSampleTakeoutMessage(raw)
	if err != nil {
		t.Fatalf("message: %v", err)
	}
	rec := RecordFromMessage(connectors.TakeoutServiceID, connectors.TakeoutServiceName, msg, gz)
	rec.ID = id
	return rec
}

// parseSampleTakeoutMessage builds the neutral message the takeout ingest
// would produce for the fixture (attachment summaries included).
func parseSampleTakeoutMessage(raw string) (kinds.Message, error) {
	atts := connectors.RFC822Attachments([]byte(raw))
	msg := kinds.Message{
		EntityID: kinds.NormalizeMessageID("cert-1@example.com"),
		Subject:  "Certificate",
		Snippet:  "Please find the certificate attached.",
		SentAt:   "2024-06-01T09:00:00Z",
		Sources: []kinds.MessageSource{{
			Service:     connectors.TakeoutServiceID,
			ServiceName: connectors.TakeoutServiceName,
			ID:          connectors.TakeoutMessageID("cert-1@example.com", []byte(raw)),
		}},
	}
	if len(atts) > 0 {
		msg.Sources[0].Extra = map[string]any{"attachments": atts}
	}
	return msg, nil
}

func TestAttachmentDocuments_FromStoredPayload(t *testing.T) {
	a := openTestArchive(t)
	if err := a.Upsert(sampleTakeoutRecord(t, "msgid:cert-1@example.com", sampleRFC822Attachment)); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	docs, truncated, err := a.AttachmentDocuments("https://aegis.example.com", AttachmentQuery{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if truncated {
		t.Fatal("small archive must not truncate")
	}
	if len(docs) != 2 {
		t.Fatalf("docs = %+v, want 2", docs)
	}
	byTitle := map[string]kinds.Document{}
	for _, d := range docs {
		byTitle[d.Title] = d
	}
	pdf, ok := byTitle["cert.pdf"]
	if !ok {
		t.Fatalf("missing cert.pdf in %+v", docs)
	}
	if pdf.DocumentType != kinds.EmailAttachmentDocumentType || pdf.MimeType != "application/pdf" {
		t.Fatalf("pdf doc = %+v", pdf)
	}
	if len(pdf.Sources) != 1 {
		t.Fatalf("sources = %+v", pdf.Sources)
	}
	src := pdf.Sources[0]
	if src.Service != connectors.TakeoutServiceID || src.Canonical {
		t.Fatalf("takeout copy must be non-canonical: %+v", src)
	}
	wantBlob := "https://aegis.example.com/v1/library/documents/" + connectors.TakeoutServiceID +
		"/msgid:cert-1@example.com~2/bytes"
	if src.BlobURL != wantBlob {
		t.Fatalf("blob_url = %q, want %q", src.BlobURL, wantBlob)
	}
	if pdf.Extra["message_id"] != "cert-1@example.com" {
		t.Fatalf("identity extra = %+v", pdf.Extra)
	}
}

func TestAttachmentDocuments_SkipsCanonicalSources(t *testing.T) {
	a := openTestArchive(t)
	takeout := sampleTakeoutRecord(t, "msgid:cert-1@example.com", sampleRFC822Attachment)
	if err := a.Upsert(takeout); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// A live-Gmail archive copy of the same payload: the live connector
	// owns that source's attachment surface (issue #215).
	gmail := takeout
	gmail.Service = connectors.GoogleGmailServiceID
	gmail.ServiceName = connectors.GoogleGmailServiceName
	gmail.ID = "m1"
	if err := a.Upsert(gmail); err != nil {
		t.Fatalf("upsert gmail: %v", err)
	}

	docs, _, err := a.AttachmentDocuments("https://aegis.example.com", AttachmentQuery{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, d := range docs {
		for _, src := range d.Sources {
			if src.Service == connectors.GoogleGmailServiceID {
				t.Fatalf("canonical source listed: %+v", d)
			}
		}
	}
	if len(docs) != 2 {
		t.Fatalf("docs = %+v, want takeout copies only", docs)
	}
}

func TestAttachmentDocuments_LegacyRowsBackfillOnce(t *testing.T) {
	a := openTestArchive(t)
	// Legacy shape: payload stored before attachment summaries existed —
	// RecordFromMessage without Extra leaves Attachments nil (SQL NULL).
	msg, err := parseSampleTakeoutMessage("")
	if err != nil {
		t.Fatalf("message: %v", err)
	}
	msg.Sources[0].Extra = nil
	gz, err := GzipPayload([]byte(sampleRFC822Attachment))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	rec := RecordFromMessage(connectors.TakeoutServiceID, connectors.TakeoutServiceName, msg, gz)
	rec.ID = "msgid:cert-1@example.com"
	if err := a.Upsert(rec); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if got, err := a.Get(rec.Service, rec.ID); err != nil || got.Attachments != nil {
		t.Fatalf("legacy row must start unexamined: %+v %v", got.Attachments, err)
	}

	docs, _, err := a.AttachmentDocuments("https://aegis.example.com", AttachmentQuery{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("docs = %+v, want 2 after backfill", docs)
	}
	got, err := a.Get(rec.Service, rec.ID)
	if err != nil || got.Attachments == nil || len(got.Attachments) != 2 {
		t.Fatalf("row must be stamped after backfill: %+v %v", got.Attachments, err)
	}

	// Undecodable payloads are stamped examined-none and never retried.
	bad := RecordFromMessage(connectors.TakeoutServiceID, connectors.TakeoutServiceName, msg, gz)
	bad.ID = "blob:deadbeef"
	bad.PayloadGzip = []byte("not-gzip")
	if err := a.Upsert(bad); err != nil {
		t.Fatalf("upsert bad: %v", err)
	}
	if _, _, err := a.AttachmentDocuments("https://aegis.example.com", AttachmentQuery{}); err != nil {
		t.Fatalf("list: %v", err)
	}
	gotBad, err := a.Get(bad.Service, bad.ID)
	if err != nil || gotBad.Attachments == nil || len(gotBad.Attachments) != 0 {
		t.Fatalf("bad payload must be stamped examined-none: %+v %v", gotBad.Attachments, err)
	}
}

func TestAttachmentDocuments_Filters(t *testing.T) {
	a := openTestArchive(t)
	rec := sampleTakeoutRecord(t, "msgid:cert-1@example.com", sampleRFC822Attachment)
	if err := a.Upsert(rec); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if docs, _, err := a.AttachmentDocuments("", AttachmentQuery{After: "2025-01-01"}); err != nil || len(docs) != 0 {
		t.Fatalf("after filter = %+v %v", docs, err)
	}
	if docs, _, err := a.AttachmentDocuments("", AttachmentQuery{Before: "2020-01-01"}); err != nil || len(docs) != 0 {
		t.Fatalf("before filter = %+v %v", docs, err)
	}
	if docs, _, err := a.AttachmentDocuments("", AttachmentQuery{Q: "cert.pdf"}); err != nil || len(docs) != 2 {
		t.Fatalf("q filter = %+v %v", docs, err)
	}
	if docs, _, err := a.AttachmentDocuments("", AttachmentQuery{Q: "nomatch"}); err != nil || len(docs) != 0 {
		t.Fatalf("q miss = %+v %v", docs, err)
	}
	if docs, _, err := a.AttachmentDocuments("", AttachmentQuery{Dataset: "other"}); err != nil || len(docs) != 0 {
		t.Fatalf("dataset filter = %+v %v", docs, err)
	}
}

func TestAttachmentTextAndBytes(t *testing.T) {
	a := openTestArchive(t)
	rec := sampleTakeoutRecord(t, "msgid:cert-1@example.com", sampleRFC822Attachment)
	if err := a.Upsert(rec); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	docID := ArchiveAttachmentDocID(rec.ID, "2")
	data, mimeType, filename, ok := a.AttachmentBytes("", rec.Service, docID)
	if !ok || string(data) != "%PDF-1.4" || mimeType != "application/pdf" || filename != "cert.pdf" {
		t.Fatalf("bytes = %q %q %q %v", data, mimeType, filename, ok)
	}
	text, found := a.AttachmentText("", rec.Service, ArchiveAttachmentDocID(rec.ID, "3"))
	if !found || text != "hello world" {
		t.Fatalf("text = %q %v", text, found)
	}
	// Binary attachments return empty text but still resolve.
	text, found = a.AttachmentText("", rec.Service, docID)
	if !found || text != "" {
		t.Fatalf("binary text = %q %v", text, found)
	}
	if _, _, _, ok := a.AttachmentBytes("", rec.Service, ArchiveAttachmentDocID(rec.ID, "99")); ok {
		t.Fatal("missing part must not resolve")
	}
	if _, _, _, ok := a.AttachmentBytes("", rec.Service, "no-tilde-id"); ok {
		t.Fatal("malformed id must not resolve")
	}
	// Data-scope isolation.
	if _, _, _, ok := a.AttachmentBytes("other", rec.Service, docID); ok {
		t.Fatal("foreign dataset must not resolve")
	}
}

func TestRecordAttachmentsRoundTrip(t *testing.T) {
	a := openTestArchive(t)
	rec := sampleTakeoutRecord(t, "msgid:cert-1@example.com", sampleRFC822Attachment)
	if rec.Attachments == nil || len(rec.Attachments) != 2 {
		t.Fatalf("record attachments = %+v", rec.Attachments)
	}
	if err := a.Upsert(rec); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := a.Get(rec.Service, rec.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Attachments) != 2 || got.Attachments[0].Filename != "cert.pdf" {
		t.Fatalf("scanned attachments = %+v", got.Attachments)
	}
	msg := got.Message()
	atts, ok := msg.Sources[0].Extra["attachments"].([]kinds.AttachmentRef)
	if !ok || len(atts) != 2 {
		t.Fatalf("message extra attachments = %#v", msg.Sources[0].Extra["attachments"])
	}
}

func TestRecordFromMessage_ToleratesForeignAttachmentShapes(t *testing.T) {
	// Ingests put their own summary structs in the source Extra (the live
	// Gmail shape); the JSON wire is what the row stores.
	msg := kinds.Message{
		EntityID: "cert-1@example.com",
		Sources: []kinds.MessageSource{{
			Service: connectors.GoogleGmailServiceID,
			ID:      "m1",
			Extra: map[string]any{
				"attachments": []map[string]any{
					{"id": "0.1", "filename": "cert.pdf", "mime_type": "application/pdf", "size": 8},
				},
			},
		}},
	}
	rec := RecordFromMessage(connectors.GoogleGmailServiceID, "", msg, nil)
	if len(rec.Attachments) != 1 || rec.Attachments[0].ID != "0.1" || rec.Attachments[0].Size != 8 {
		t.Fatalf("attachments = %+v", rec.Attachments)
	}
}

func TestSplitArchiveAttachmentDocID(t *testing.T) {
	row, part, ok := SplitArchiveAttachmentDocID("msgid:msg~1@example.com~2.1")
	if !ok || row != "msgid:msg~1@example.com" || part != "2.1" {
		t.Fatalf("split = %q %q %v", row, part, ok)
	}
	if _, _, ok := SplitArchiveAttachmentDocID("notilde"); ok {
		t.Fatal("id without separator must fail")
	}
	if _, _, ok := SplitArchiveAttachmentDocID("~1"); ok {
		t.Fatal("id without row must fail")
	}
	if ArchiveAttachmentDocID("a", "2") != "a~2" {
		t.Fatal("doc id shape")
	}
}
