package shared

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadBodyLimited_OversizedReturns413(t *testing.T) {
	const capBytes = 1024
	payload := bytes.Repeat([]byte("a"), capBytes+1024)
	req := httptest.NewRequest("PUT", "/v1/admin/example", bytes.NewReader(payload))
	req.ContentLength = int64(len(payload))
	w := httptest.NewRecorder()

	body, ok := ReadBodyLimited(w, req, capBytes)
	if ok {
		t.Fatalf("expected overflow, got %d bytes", len(body))
	}
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "exceeds") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestReadBodyLimited_ReadsUnderCap(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/admin/example", bytes.NewReader([]byte("hello")))
	w := httptest.NewRecorder()
	body, ok := ReadBodyLimited(w, req, 1024)
	if !ok {
		t.Fatalf("expected read to succeed, body=%s", w.Body.String())
	}
	if string(body) != "hello" {
		t.Fatalf("body = %q", body)
	}
}
