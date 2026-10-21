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
	mid, aid, ok = GmailAttachmentPath("/messages/msg123/attachments/0.1")
	if !ok || aid != "0.1" {
		t.Fatalf("dotted attachment id: %q %q %v", mid, aid, ok)
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

func TestGmailAttachmentListPath(t *testing.T) {
	mid, ok := GmailAttachmentListPath("/messages/msg123/attachments")
	if !ok || mid != "msg123" {
		t.Fatalf("got %q %v", mid, ok)
	}
	mid, ok = GmailAttachmentListPath("/me/messages/msg123/attachments/")
	if !ok || mid != "msg123" {
		t.Fatalf("me prefix: %q %v", mid, ok)
	}
	// Foreign ids still match the shape so callers can 404 with a hint.
	mid, ok = GmailAttachmentListPath("/messages/caal8h=x@mail.gmail.com/attachments")
	if !ok || mid != "caal8h=x@mail.gmail.com" {
		t.Fatalf("foreign id shape: %q %v", mid, ok)
	}
	for _, p := range []string{
		"/messages/msg123",
		"/messages/msg123/attachments/att1",
		"/messages/msg123/attachments/att1/extra",
	} {
		if _, ok := GmailAttachmentListPath(p); ok {
			t.Errorf("expected reject for %q", p)
		}
	}
}

func TestGmailAttachmentDocumentID(t *testing.T) {
	id := GmailAttachmentDocumentID("msg1", "0.1")
	if id != "msg1~0.1" {
		t.Fatalf("id = %q", id)
	}
	mid, aid, ok := SplitGmailAttachmentDocumentID(id)
	if !ok || mid != "msg1" || aid != "0.1" {
		t.Fatalf("split = %q %q %v", mid, aid, ok)
	}
	if _, _, ok := SplitGmailAttachmentDocumentID("noseparator"); ok {
		t.Fatal("expected reject")
	}
}

func TestValidGmailBareID_RejectsForeignIDs(t *testing.T) {
	if validGmailBareID("at:/did:plc:abc/app.bsky.feed.post/xyz") {
		t.Fatal("AT-URI must not be a gmail id")
	}
	if validGmailBareID("abc/attachments") {
		t.Fatal("path-like id must be rejected")
	}
	if validGmailBareID("..") || validGmailBareID("foo..bar") {
		t.Fatal("path traversal id must be rejected")
	}
	if !validGmailBareID("18f1aabbccdd1122") {
		t.Fatal("hex id should be valid")
	}
	if !validGmailBareID("0.1") {
		t.Fatal("gmail attachment ids may contain dots")
	}
}
