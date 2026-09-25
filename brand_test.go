package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleBrandAsset_PublicIcons(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	cases := []struct {
		path        string
		contentType string
		minBytes    int
	}{
		{"/favicon.ico", "image/x-icon", 100},
		{"/favicon.svg", "image/svg+xml", 50},
		{"/favicon-16.png", "image/png", 50},
		{"/favicon-32.png", "image/png", 50},
		{"/apple-touch-icon.png", "image/png", 100},
		{"/logo.svg", "image/svg+xml", 50},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		w := httptest.NewRecorder()
		srv.HandleBrandAsset(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", tc.path, w.Code)
		}
		if got := w.Header().Get("Content-Type"); got != tc.contentType {
			t.Fatalf("%s Content-Type = %q, want %q", tc.path, got, tc.contentType)
		}
		if w.Body.Len() < tc.minBytes {
			t.Fatalf("%s body too small: %d", tc.path, w.Body.Len())
		}
		// Brand assets are public — no auth middleware on these routes.
		if strings.Contains(w.Body.String(), "api-key") {
			t.Fatalf("%s unexpectedly looks like an auth challenge", tc.path)
		}
	}
}

func TestHandleBrandAsset_MethodsAndMisses(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")

	req := httptest.NewRequest(http.MethodGet, "/logo.svg", nil)
	w := httptest.NewRecorder()
	srv.HandleBrandAsset(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /logo.svg status = %d", w.Code)
	}
	if w.Body.Len() == 0 {
		t.Fatal("GET /logo.svg empty body")
	}

	req = httptest.NewRequest(http.MethodHead, "/favicon.ico", nil)
	w = httptest.NewRecorder()
	srv.HandleBrandAsset(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HEAD /favicon.ico status = %d", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("HEAD body len = %d, want 0", w.Body.Len())
	}
	if w.Header().Get("Content-Length") == "" {
		t.Fatal("HEAD missing Content-Length")
	}

	req = httptest.NewRequest(http.MethodPost, "/favicon.ico", nil)
	w = httptest.NewRecorder()
	srv.HandleBrandAsset(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/not-a-brand.svg", nil)
	w = httptest.NewRecorder()
	srv.HandleBrandAsset(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown path status = %d, want 404", w.Code)
	}
}

func TestRegisterBrandRoutes(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	mux := http.NewServeMux()
	registerBrandRoutes(mux, srv)

	for _, path := range []string{"/favicon.ico", "/favicon.svg", "/logo.svg", "/apple-touch-icon.png"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("mux %s status = %d", path, w.Code)
		}
	}
}

func TestBrandSVGs_AreSuspiciousJellyfish(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	for _, path := range []string{"/favicon.svg", "/logo.svg"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		srv.HandleBrandAsset(w, req)
		body := w.Body.String()
		if !strings.Contains(body, "<svg") {
			t.Fatalf("%s missing svg root", path)
		}
		// Issue #87: Medusa jellyfish mark — bell + eyes + tentacles.
		if !strings.Contains(body, "#e6edf3") && !strings.Contains(body, "#0d1117") && !strings.Contains(body, "currentColor") {
			t.Fatalf("%s missing brand colors", path)
		}
	}
}
