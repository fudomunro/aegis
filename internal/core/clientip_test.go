package core

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCompileTrustedProxies(t *testing.T) {
	nets, err := compileTrustedProxies([]string{"127.0.0.1", "10.0.0.0/8", " 192.168.1.2 "})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(nets) != 3 {
		t.Fatalf("nets = %d, want 3", len(nets))
	}
	if _, err := compileTrustedProxies([]string{"not-an-ip"}); err == nil {
		t.Fatal("invalid IP must fail config load")
	}
	if _, err := compileTrustedProxies([]string{"10.0.0.0/99"}); err == nil {
		t.Fatal("invalid CIDR must fail config load")
	}
	empty, err := compileTrustedProxies(nil)
	if err != nil || empty != nil {
		t.Fatalf("empty = %v err=%v", empty, err)
	}
}

func TestClientIP_NoTrustedProxiesIgnoresForwarded(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/v1/library/tracks", nil)
	r.RemoteAddr = "203.0.113.9:41000"
	r.Header.Set("X-Real-IP", "6.6.6.6")
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 7.7.7.7")
	if got := clientIP(r, nil); got != "203.0.113.9" {
		t.Fatalf("clientIP = %q, want RemoteAddr host", got)
	}
}

func TestClientIP_UntrustedPeerCannotForgeHeaders(t *testing.T) {
	trusted, err := compileTrustedProxies([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.9:41000"
	r.Header.Set("X-Real-IP", "6.6.6.6")
	r.Header.Set("X-Forwarded-For", "6.6.6.6")
	if got := clientIP(r, trusted); got != "203.0.113.9" {
		t.Fatalf("forged header from untrusted peer must be ignored: %q", got)
	}
}

func TestClientIP_TrustedProxyHeaders(t *testing.T) {
	trusted, err := compileTrustedProxies([]string{"127.0.0.1/32", "10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}

	// X-Real-IP from the immediate proxy (demo nginx shape).
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "127.0.0.1:52000"
	r.Header.Set("X-Real-IP", "203.0.113.9")
	if got := clientIP(r, trusted); got != "203.0.113.9" {
		t.Fatalf("x-real-ip = %q", got)
	}

	// XFF: walk right to left past trusted hops, stop at the first untrusted.
	r = httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.2:52000"
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.9, 10.0.0.3")
	if got := clientIP(r, trusted); got != "203.0.113.9" {
		t.Fatalf("xff rightmost-untrusted = %q", got)
	}

	// Attacker-controlled padding left of the real client is ignored.
	r = httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "127.0.0.1:52000"
	r.Header.Set("X-Forwarded-For", "8.8.8.8, 203.0.113.9")
	if got := clientIP(r, trusted); got != "203.0.113.9" {
		t.Fatalf("xff padding ignored = %q", got)
	}

	// Every hop trusted: the leftmost is the client.
	r = httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.2:52000"
	r.Header.Set("X-Forwarded-For", "10.0.0.9, 10.0.0.3")
	if got := clientIP(r, trusted); got != "10.0.0.9" {
		t.Fatalf("all-trusted xff = %q", got)
	}

	// No usable header: fall back to the peer.
	r = httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "127.0.0.1:52000"
	r.Header.Set("X-Forwarded-For", "garbage")
	if got := clientIP(r, trusted); got != "127.0.0.1" {
		t.Fatalf("garbage xff fallback = %q", got)
	}
}
