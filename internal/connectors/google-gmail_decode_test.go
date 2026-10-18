package connectors

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func b64url(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

func gmailMsg(t *testing.T, payload string) GoogleGmailMessage {
	t.Helper()
	var msg GoogleGmailMessage
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return msg
}

func TestGmailDecodeBody(t *testing.T) {
	// Top-level body wins over parts.
	msg := gmailMsg(t, `{
		"payload": {
			"body": {"data": "`+b64url("hello top")+`"},
			"parts": [{"mimeType": "text/plain", "body": {"data": "`+b64url("hello part")+`"}}]
		}
	}`)
	if got := GmailDecodeBody(msg); got != "hello top" {
		t.Fatalf("top-level = %q", got)
	}

	// text/plain preferred over html.
	msg = gmailMsg(t, `{
		"payload": {
			"parts": [
				{"mimeType": "text/html", "body": {"data": "`+b64url("<p>html</p>")+`"}},
				{"mimeType": "text/plain", "body": {"data": "`+b64url("plain body")+`"}}
			]
		}
	}`)
	if got := GmailDecodeBody(msg); got != "plain body" {
		t.Fatalf("plain = %q", got)
	}

	// Fall back to any text/* when no text/plain (case-insensitive mime).
	msg = gmailMsg(t, `{
		"payload": {
			"parts": [
				{"mimeType": "TEXT/HTML", "body": {"data": "`+b64url("html only")+`"}}
			]
		}
	}`)
	if got := GmailDecodeBody(msg); got != "html only" {
		t.Fatalf("html fallback = %q", got)
	}

	if got := GmailDecodeBody(GoogleGmailMessage{}); got != "" {
		t.Fatalf("empty = %q", got)
	}
}

func TestGmailBase64Decode_PaddingAndInvalid(t *testing.T) {
	raw := base64.RawURLEncoding.EncodeToString([]byte("hi?/"))
	if got := GmailBase64Decode(raw); got != "hi?/" {
		t.Fatalf("got %q", got)
	}
	if got := GmailBase64Decode("!!!not-base64!!!"); got != "" {
		t.Fatalf("invalid = %q", got)
	}
}

func TestParseIntLoose(t *testing.T) {
	if n, err := ParseIntLoose(" 42 "); err != nil || n != 42 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if _, err := ParseIntLoose(""); err == nil {
		t.Fatal("empty must error")
	}
	if _, err := ParseIntLoose("abc"); err == nil {
		t.Fatal("non-numeric must error")
	}
}
