package connectors

import "testing"

func TestValidGmailBareID(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"1a2b3c4d5e6f7890", true},
		{"18f1aabbccdd1122", true},
		{"at:/did:plc:abc/app.bsky.feed.post/xyz", false},
		{"18f1aabbccdd1122/attachments", false},
		{"msg-1_2", true},
		{"", false},
		{"has space", false},
		{"has:colon", false},
		{string(make([]byte, 200)), false},
	}
	for _, tc := range cases {
		if got := validGmailBareID(tc.id); got != tc.want {
			t.Errorf("validGmailBareID(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

func TestGmailBareIDFromProxyPath_RejectsExtraSegments(t *testing.T) {
	if id, ok := GmailBareIDFromProxyPath("/messages/abc/attachments", "messages"); ok {
		t.Fatalf("extra segment must not parse as bare id, got %q", id)
	}
	if id, ok := GmailBareIDFromProxyPath("/messages/abc", "messages"); !ok || id != "abc" {
		t.Fatalf("bare id parse failed: %q %v", id, ok)
	}
	if id, ok := GmailBareIDFromProxyPath("/me/messages/abc", "messages"); !ok || id != "abc" {
		t.Fatalf("me prefix parse failed: %q %v", id, ok)
	}
}

func TestIsGmailInvalidIDError(t *testing.T) {
	if !isGmailInvalidIDError(errString(`gmail message get returned 400: "Invalid id value"`)) {
		t.Fatal("invalid id error not detected")
	}
	if isGmailInvalidIDError(errString("connection reset")) {
		t.Fatal("transient error must not look like invalid id")
	}
	if isGmailInvalidIDError(nil) {
		t.Fatal("nil error")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
