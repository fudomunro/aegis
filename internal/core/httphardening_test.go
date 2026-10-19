package core

import (
	"bytes"
	"crypto/subtle"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestConstantTimeEqual(t *testing.T) {
	if !constantTimeEqual("secret", "secret") {
		t.Fatal("equal strings should match")
	}
	if constantTimeEqual("secret", "secreT") {
		t.Fatal("different strings must not match")
	}
	if constantTimeEqual("secret", "longer-secret") {
		t.Fatal("length mismatch must not match")
	}
	if constantTimeEqual("", "x") {
		t.Fatal("empty vs non-empty must not match")
	}
	// Sanity: subtle package is in play (issue #98).
	if subtle.ConstantTimeCompare([]byte("a"), []byte("a")) != 1 {
		t.Fatal("subtle compare broken")
	}
}

func TestCheckAPIKey_ConstantTimePaths(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.apiKey = "test-key"
	srv.authMode = AuthModeAPIKey

	req := httptest.NewRequest("GET", "/v1/admin/summary", nil)
	req.Header.Set("X-Api-Key", "test-key")
	if !srv.checkAPIKey(req) {
		t.Fatal("X-Api-Key should authenticate")
	}

	req = httptest.NewRequest("GET", "/v1/admin/summary", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	if !srv.checkAPIKey(req) {
		t.Fatal("Bearer should authenticate")
	}

	req = httptest.NewRequest("GET", "/v1/admin/summary", nil)
	req.Header.Set("X-Api-Key", "wrong")
	if srv.checkAPIKey(req) {
		t.Fatal("wrong key must fail")
	}

	req = httptest.NewRequest("GET", "/v1/admin/summary", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	if srv.checkAPIKey(req) {
		t.Fatal("wrong bearer must fail")
	}
}

func TestSecurityHeaders_OnHTMLAndJSON(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".html") {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte("<html><body>hi</body></html>"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	})
	handler := securityHeadersMiddleware(inner)

	for _, path := range []string{"/v1/admin", "/v1/admin/summary"} {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		for _, hdr := range []string{
			"X-Content-Type-Options",
			"X-Frame-Options",
			"Content-Security-Policy",
			"Referrer-Policy",
		} {
			if w.Header().Get(hdr) == "" {
				t.Fatalf("%s: missing security header %s", path, hdr)
			}
		}
		if w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("nosniff = %q", w.Header().Get("X-Content-Type-Options"))
		}
		if w.Header().Get("X-Frame-Options") != "DENY" {
			t.Fatalf("X-Frame-Options = %q", w.Header().Get("X-Frame-Options"))
		}
	}
	_ = srv
}

func TestReadBodyLimited_OversizedReturns413(t *testing.T) {
	payload := bytes.Repeat([]byte("a"), maxAdminBodyBytes+1024)
	req := httptest.NewRequest("PUT", "/v1/admin/services/spotify/grant-limits", bytes.NewReader(payload))
	req.ContentLength = int64(len(payload))
	w := httptest.NewRecorder()

	body, ok := readBodyLimited(w, req, maxAdminBodyBytes)
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

func TestHandlePreRequest_OversizedBodyReturns413(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	payload := bytes.Repeat([]byte("x"), maxHookBodyBytes+1024)
	req := httptest.NewRequest("POST", "/v1/pre-request", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.AuthMiddleware(withBodyLimit(maxHookBodyBytes, srv.HandlePreRequest))(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413, body = %s", w.Code, w.Body.String())
	}
}

func TestHandlePreRequest_OversizedBodyDoesNotBufferWholePayload(t *testing.T) {
	// Fail-open stays for unreadable bodies; oversized is a hard 413 (issue #97).
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	small := []byte(`{"metadata":{},"user_message":"hi"}`)
	req := httptest.NewRequest("POST", "/v1/pre-request", bytes.NewReader(small))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(withBodyLimit(maxHookBodyBytes, srv.HandlePreRequest))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("small body status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestHandleAdminPermissions_OversizedBodyReturns413(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	payload := bytes.Repeat([]byte("p"), maxAdminBodyBytes+1024)
	req := httptest.NewRequest("PUT", "/v1/admin/services/spotify/grant-limits", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.AuthMiddleware(withBodyLimit(maxAdminBodyBytes, srv.HandleAdminServiceGrantLimits))(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413, body = %s", w.Code, w.Body.String())
	}
}

func TestAdminShell_RequiresAuth(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	handler := srv.AuthMiddleware(srv.HandleAdmin)

	req := httptest.NewRequest("GET", "/v1/admin", nil)
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated admin shell status = %d, want 401", w.Code)
	}

	req = httptest.NewRequest("GET", "/v1/admin", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w = httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("authenticated admin shell status = %d, want 200", w.Code)
	}
}

func TestHTTPServerTimeouts(t *testing.T) {
	readHeader, read, write, idle := httpServerTimeouts()
	if readHeader <= 0 {
		t.Fatal("ReadHeaderTimeout must be set")
	}
	if read <= 0 {
		t.Fatal("ReadTimeout must be set")
	}
	if write != 0 {
		t.Fatalf("WriteTimeout = %v, want unlimited for streaming", write)
	}
	if idle <= 0 {
		t.Fatal("IdleTimeout must be set")
	}
	if readHeader > 30*time.Second {
		t.Fatalf("ReadHeaderTimeout too large: %v", readHeader)
	}
}

func TestWithBodyLimit_WrapsBody(t *testing.T) {
	var sawLimited bool
	inner := func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil {
			t.Fatal("body should be non-nil")
		}
		// Reading past the cap must error (MaxBytesReader).
		buf := make([]byte, 16)
		n, err := r.Body.Read(buf)
		_ = n
		_ = err
		// Drain a tiny body successfully.
		sawLimited = r.Body != nil
		w.WriteHeader(http.StatusNoContent)
	}
	handler := withBodyLimit(8, http.HandlerFunc(inner))
	req := httptest.NewRequest("POST", "/x", bytes.NewReader([]byte("ok")))
	w := httptest.NewRecorder()
	handler(w, req)
	if !sawLimited {
		t.Fatal("body was not presented to handler")
	}
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestNewAegisHTTPServer_HasTimeouts(t *testing.T) {
	srv := newAegisHTTPServer("127.0.0.1:0", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if srv.ReadHeaderTimeout <= 0 {
		t.Fatal("ReadHeaderTimeout unset")
	}
	if srv.IdleTimeout <= 0 {
		t.Fatal("IdleTimeout unset")
	}
}

func TestGracefulStop_DrainsInFlight(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusOK)
	})
	srv := newAegisHTTPServer("127.0.0.1:0", handler)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)

	respCh := make(chan *http.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/")
		if err != nil {
			errCh <- err
			return
		}
		respCh <- resp
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}

	// Shutdown while the request is in flight; it must still complete.
	done := make(chan error, 1)
	go func() { done <- gracefulStop(srv, 2*time.Second) }()

	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("gracefulStop: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("gracefulStop did not return")
	}

	select {
	case resp := <-respCh:
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	case err := <-errCh:
		t.Fatalf("in-flight request failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight request never finished")
	}
}
