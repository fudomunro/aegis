package connectors

import "testing"

func TestTruncateBytes(t *testing.T) {
	if got := truncateBytes([]byte("  hello  "), 10); got != "hello" {
		t.Fatalf("short = %q", got)
	}
	if got := truncateBytes([]byte("abcdefghij"), 4); got != "abcd…" {
		t.Fatalf("truncated = %q", got)
	}
	if got := truncateBytes([]byte("abcd"), 4); got != "abcd" {
		t.Fatalf("exact = %q", got)
	}
}
