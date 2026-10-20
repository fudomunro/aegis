package connectors

import "testing"

func TestGmailAttachmentPath(t *testing.T) {
	mid, aid, ok := GmailAttachmentPath("/messages/msg123/attachments/att456")
	if !ok || mid != "msg123" || aid != "att456" {
		t.Fatalf("got %q %q %v", mid, aid, ok)
	}
	mid, aid, ok = GmailAttachmentPath("/me/messages/msg123/attachments/att456")
	if !ok || mid != "msg123" || aid != "att456" {
		t.Fatalf("me prefix: %q %q %v", mid, aid, ok)
	}
	for _, p := range []string{
		"/messages/msg123",
		"/messages/msg123/attachments",
		"/messages/at:/did:plc:x/attachments/att",
		"/messages/msg/attachments/at:foo",
		"/messages/msg/attachments/att/extra",
	} {
		if _, _, ok := GmailAttachmentPath(p); ok {
			t.Errorf("expected reject for %q", p)
		}
	}
}

func TestValidGmailBareID_RejectsForeignIDs(t *testing.T) {
	if validGmailBareID("at:/did:plc:abc/app.bsky.feed.post/xyz") {
		t.Fatal("AT-URI must not be a gmail id")
	}
	if validGmailBareID("abc/attachments") {
		t.Fatal("path-like id must be rejected")
	}
	if !validGmailBareID("18f1aabbccdd1122") {
		t.Fatal("hex id should be valid")
	}
}
