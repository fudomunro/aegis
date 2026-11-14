package connectors

import (
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/kinds"
)

// sampleRFC822WithAttachments is a Takeout-style message: nested alternative
// body, a base64 PDF attachment, a named inline image, and a text file.
const sampleRFC822WithAttachments = "From: sender@example.com\r\n" +
	"To: me@example.com\r\n" +
	"Subject: Certificate\r\n" +
	"Date: Sat, 01 Jun 2024 09:00:00 +0000\r\n" +
	"Message-ID: <cert-1@example.com>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"OUTER\"\r\n" +
	"\r\n" +
	"--OUTER\r\n" +
	"Content-Type: multipart/alternative; boundary=\"INNER\"\r\n" +
	"\r\n" +
	"--INNER\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"Please find the certificate attached.\r\n" +
	"--INNER\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"\r\n" +
	"<p>Please find the certificate attached.</p>\r\n" +
	"--INNER--\r\n" +
	"--OUTER\r\n" +
	"Content-Type: application/pdf; name=\"cert.pdf\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"Content-Disposition: attachment; filename=\"cert.pdf\"\r\n" +
	"\r\n" +
	"JVBERi0xLjQ=\r\n" +
	"--OUTER\r\n" +
	"Content-Type: image/png\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"Content-Disposition: inline; filename=\"scan.png\"\r\n" +
	"\r\n" +
	"aGVsbG8=\r\n" +
	"--OUTER\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"Content-Disposition: attachment; filename=\"notes.txt\"\r\n" +
	"\r\n" +
	"aGVsbG8gd29ybGQ=\r\n" +
	"--OUTER--\r\n"

func TestRFC822Attachments_WalksParts(t *testing.T) {
	atts := RFC822Attachments([]byte(sampleRFC822WithAttachments))
	if len(atts) != 3 {
		t.Fatalf("attachments = %+v, want 3", atts)
	}
	byID := map[string]kinds.AttachmentRef{}
	for _, a := range atts {
		byID[a.ID] = a
	}
	pdf := byID["2"]
	if pdf.Filename != "cert.pdf" || pdf.MimeType != "application/pdf" || pdf.Size != len("%PDF-1.4") {
		t.Fatalf("pdf = %+v", pdf)
	}
	scan := byID["3"]
	if scan.Filename != "scan.png" || scan.MimeType != "image/png" || scan.Size != len("hello") {
		t.Fatalf("scan = %+v", scan)
	}
	notes := byID["4"]
	if notes.Filename != "notes.txt" || notes.Size != len("hello world") {
		t.Fatalf("notes = %+v", notes)
	}
	// Body alternatives are not attachments.
	for _, a := range atts {
		if strings.HasPrefix(a.ID, "1.") {
			t.Fatalf("body part listed as attachment: %+v", a)
		}
	}
}

func TestRFC822Attachments_FailOpenOnBadPart(t *testing.T) {
	raw := strings.Replace(sampleRFC822WithAttachments,
		"JVBERi0xLjQ=", "!!!not-base64!!!", 1)
	atts := RFC822Attachments([]byte(raw))
	if len(atts) != 2 {
		t.Fatalf("attachments = %+v, want 2 (bad part skipped)", atts)
	}
	for _, a := range atts {
		if a.Filename == "cert.pdf" {
			t.Fatalf("undecodable part must be skipped: %+v", a)
		}
	}
}

func TestRFC822Attachments_NonMultipart(t *testing.T) {
	raw := "From: a@b.c\r\nContent-Type: application/pdf\r\n\r\nbody"
	if atts := RFC822Attachments([]byte(raw)); len(atts) != 0 {
		t.Fatalf("non-multipart body is not an attachment: %+v", atts)
	}
	if atts := RFC822Attachments([]byte("garbage")); atts != nil {
		t.Fatalf("unparseable input = %+v, want nil", atts)
	}
}

func TestRFC822AttachmentPartAndText(t *testing.T) {
	raw := []byte(sampleRFC822WithAttachments)
	data, mimeType, filename, ok := RFC822AttachmentPart(raw, "2")
	if !ok || string(data) != "%PDF-1.4" || mimeType != "application/pdf" || filename != "cert.pdf" {
		t.Fatalf("part 2 = %q %q %q %v", data, mimeType, filename, ok)
	}
	if _, _, _, ok := RFC822AttachmentPart(raw, "99"); ok {
		t.Fatal("missing part must not resolve")
	}
	if _, _, _, ok := RFC822AttachmentPart(raw, ""); ok {
		t.Fatal("empty part id must not resolve")
	}

	text, found := RFC822AttachmentText(raw, "4")
	if !found || text != "hello world" {
		t.Fatalf("text part = %q %v", text, found)
	}
	text, found = RFC822AttachmentText(raw, "2")
	if !found || text != "" {
		t.Fatalf("binary text = %q %v, want empty found", text, found)
	}
	if _, found := RFC822AttachmentText(raw, "99"); found {
		t.Fatal("missing part must not be found")
	}
}

func TestParseTakeoutMessage_CarriesAttachmentSummaries(t *testing.T) {
	msg, err := parseTakeoutMessage([]byte(sampleRFC822WithAttachments), "takeout")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	atts, ok := msg.Sources[0].Extra["attachments"].([]kinds.AttachmentRef)
	if !ok {
		t.Fatalf("extra attachments = %#v", msg.Sources[0].Extra["attachments"])
	}
	if len(atts) != 3 {
		t.Fatalf("attachments = %+v, want 3", atts)
	}
	if atts[0].Filename != "cert.pdf" || atts[0].ID != "2" {
		t.Fatalf("first = %+v", atts[0])
	}
}

func FuzzRFC822Attachments(f *testing.F) {
	f.Add([]byte(sampleRFC822WithAttachments))
	f.Add([]byte("garbage"))
	f.Add([]byte("From: a@b.c\r\n\r\nbody"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		atts := RFC822Attachments(raw)
		for _, a := range atts {
			// Summaries must never be empty-handed: each entry names a part
			// that the part lookup can resolve.
			if a.ID == "" {
				t.Fatalf("empty part id in %+v", atts)
			}
			data, _, _, ok := RFC822AttachmentPart(raw, a.ID)
			if !ok {
				t.Fatalf("summary id %q does not resolve", a.ID)
			}
			if len(data) != a.Size {
				t.Fatalf("size mismatch for %q: %d != %d", a.ID, len(data), a.Size)
			}
		}
	})
}

func FuzzRFC822AttachmentPart(f *testing.F) {
	f.Add([]byte(sampleRFC822WithAttachments), "2")
	f.Add([]byte(sampleRFC822WithAttachments), "1.1")
	f.Add([]byte("garbage"), "1")
	f.Fuzz(func(t *testing.T, raw []byte, partID string) {
		RFC822AttachmentPart(raw, partID)
		RFC822AttachmentText(raw, partID)
	})
}
