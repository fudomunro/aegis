package connectors

import (
	"errors"
	"net/mail"
	"strings"
	"testing"
)

func takeoutMsg(t *testing.T, headers string) *mail.Message {
	t.Helper()
	msg, err := mail.ReadMessage(strings.NewReader(headers + "\r\n\r\nbody\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

func TestTakeoutLabels(t *testing.T) {
	msg := takeoutMsg(t, "X-GM-LABELS: INBOX, \"Starred\" UNREAD")
	got := TakeoutLabels(msg, "/exports/All mail Including Spam and Trash.mbox")
	want := []string{"INBOX", "STARRED", "UNREAD", "TAKEOUT"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("labels = %v, want %v", got, want)
	}

	// Without X-GM-LABELS the source base name stands in, TAKEOUT is appended.
	plain := takeoutMsg(t, "Subject: hi")
	got = TakeoutLabels(plain, "/exports/All mail Including Spam and Trash.mbox")
	want = []string{"ALL_MAIL_INCLUDING_SPAM_AND_TRASH", "TAKEOUT"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("labels = %v, want %v", got, want)
	}
}

func TestTakeoutMailboxFromLabels(t *testing.T) {
	if got := TakeoutMailboxFromLabels([]string{"INBOX", "TAKEOUT"}); got != "INBOX" {
		t.Fatalf("mailbox = %q, want INBOX", got)
	}
	if got := TakeoutMailboxFromLabels([]string{"sent"}); got != "SENT" {
		t.Fatalf("mailbox = %q, want SENT", got)
	}
	if got := TakeoutMailboxFromLabels([]string{"TAKEOUT"}); got != "TAKEOUT" {
		t.Fatalf("mailbox = %q, want TAKEOUT fallback", got)
	}
}

func TestTakeoutDirection(t *testing.T) {
	if got := TakeoutDirection([]string{"SENT"}); got != "outbound" {
		t.Fatalf("direction = %q, want outbound", got)
	}
	if got := TakeoutDirection([]string{"INBOX"}); got != "inbound" {
		t.Fatalf("direction = %q, want inbound", got)
	}
	if got := TakeoutDirection([]string{"TAKEOUT"}); got != "unknown" {
		t.Fatalf("direction = %q, want unknown", got)
	}
}

func TestTakeoutRead(t *testing.T) {
	if TakeoutRead([]string{"INBOX", "UNREAD"}) {
		t.Fatal("UNREAD must map to Read=false")
	}
	if !TakeoutRead([]string{"INBOX"}) {
		t.Fatal("messages without UNREAD are read")
	}
}

func TestTakeoutMessageID(t *testing.T) {
	id := TakeoutMessageID("<abc@x>", []byte("raw"))
	if id != "msgid:<abc@x>" {
		t.Fatalf("id = %q, want msgid: form", id)
	}
	// No Message-ID → content-hash form, stable across calls.
	a := TakeoutMessageID("", []byte("raw"))
	b := TakeoutMessageID("", []byte("raw"))
	if !strings.HasPrefix(a, "blob:") || a != b {
		t.Fatalf("blob id = %q / %q", a, b)
	}
	if TakeoutMessageID("", []byte("other")) == a {
		t.Fatal("different content must hash differently")
	}
}

func FuzzTakeoutMessageID(f *testing.F) {
	f.Add("", []byte("raw"))
	f.Add("<abc@x>", []byte("raw"))
	f.Add("  <>  ", []byte{})
	f.Add("weird id", []byte("payload"))
	f.Fuzz(func(t *testing.T, messageID string, raw []byte) {
		id := TakeoutMessageID(messageID, raw)
		if id == "" {
			t.Fatal("empty takeout id")
		}
		if !strings.HasPrefix(id, "msgid:") && !strings.HasPrefix(id, "blob:") {
			t.Fatalf("unexpected id prefix: %q", id)
		}
		// Same inputs → same id (stable hashing).
		if again := TakeoutMessageID(messageID, raw); again != id {
			t.Fatalf("unstable id: %q vs %q", id, again)
		}
	})
}

func TestParseTakeoutMessageMultipart(t *testing.T) {
	raw := []byte(strings.Join([]string{
		"From: Ada <ada@example.com>",
		"To: me@example.com",
		"Subject: Multipart note",
		"Message-ID: <mp@example.com>",
		"Date: Fri, 01 Jan 2024 10:00:00 +0000",
		"MIME-Version: 1.0",
		`Content-Type: multipart/alternative; boundary="b"`,
		"",
		"--b",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"plain body here",
		"--b",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<p>html body</p>",
		"--b--",
		"",
	}, "\n"))
	msg, err := parseTakeoutMessage(raw, "mail.mbox")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if msg.Body != "plain body here" {
		t.Fatalf("body = %q", msg.Body)
	}
	if len(msg.Sources) != 1 || msg.Sources[0].ID != "msgid:mp@example.com" {
		t.Fatalf("id = %+v", msg.Sources)
	}
}

func TestMailSourceDeclarations(t *testing.T) {
	gm := MailSourceFor(GoogleGmailServiceID)
	if !gm.Canonical || gm.Parent != "" {
		t.Fatalf("gmail source = %+v, want canonical with no parent", gm)
	}
	to := MailSourceFor(TakeoutServiceID)
	if to.Canonical || to.Parent != GoogleGmailServiceID {
		t.Fatalf("takeout source = %+v, want non-canonical under %s", to, GoogleGmailServiceID)
	}
	// Canonical is claimed, never assumed (issue #16).
	if got := MailSourceFor("unknown-service"); got.Canonical || got.Parent != "" {
		t.Fatalf("unknown source = %+v, want the zero declaration", got)
	}
}

func TestLooksLikeMboxName(t *testing.T) {
	if !looksLikeMboxName("inbox.mbox") {
		t.Fatal("inbox.mbox should look like mbox")
	}
	if looksLikeMboxName("notes.txt") {
		t.Fatal("notes.txt is not mbox")
	}
	if !looksLikeMboxName("All Mail") {
		t.Fatal("All Mail should look like mbox")
	}
}

func TestIsGmailQuotaError(t *testing.T) {
	if IsGmailQuotaError(nil) {
		t.Fatal("nil is not a quota error")
	}
	quota := GmailListHTTPError("list", 403,
		[]byte(`{"error":{"code":403,"message":"Quota exceeded for quota metric 'Total Query Cost'"}}`))
	if !IsGmailQuotaError(quota) {
		t.Fatalf("err = %v, want quota classification", quota)
	}
	if IsGmailQuotaError(errors.New("connection reset")) {
		t.Fatal("unrelated errors must not classify as quota")
	}
}

func TestPaceGmailArchiveGetSkipsWhenDisabled(t *testing.T) {
	prev := GmailArchiveGetInterval
	GmailArchiveGetInterval = 0
	t.Cleanup(func() { GmailArchiveGetInterval = prev })
	if err := PaceGmailArchiveGet(t.Context()); err != nil {
		t.Fatalf("pace: %v", err)
	}
}
