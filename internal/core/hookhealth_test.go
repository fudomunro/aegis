package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/core/hookhealth"
)

func TestHandleAdminHookHealth_GetAndCheck(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")

	req := httptest.NewRequest("GET", "/v1/admin/hook-health", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.admin().HandleAdminHookHealth)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%s", w.Code, w.Body.String())
	}
	var rep hookhealth.HookHealthReport
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rep.ExpectedHookName != "aegis" {
		t.Fatalf("expected_hook_name = %q", rep.ExpectedHookName)
	}

	body := strings.NewReader(`{"hooks":{"aegis":{"url":"https://aegis.example.com/v1/pre-request","apikey":"x"}}}`)
	req = httptest.NewRequest("POST", "/v1/admin/hook-health/check", body)
	req.Header.Set("Authorization", "Bearer test-key")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.admin().HandleAdminHookHealth)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST status = %d body=%s", w.Code, w.Body.String())
	}

	// Empty body is a 400.
	req = httptest.NewRequest("POST", "/v1/admin/hook-health/check", strings.NewReader("  "))
	req.Header.Set("Authorization", "Bearer test-key")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.admin().HandleAdminHookHealth)(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty body status = %d", w.Code)
	}

	// Method not allowed.
	req = httptest.NewRequest("DELETE", "/v1/admin/hook-health", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.admin().HandleAdminHookHealth)(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE status = %d", w.Code)
	}

	// Unauthenticated.
	req = httptest.NewRequest("GET", "/v1/admin/hook-health", nil)
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.admin().HandleAdminHookHealth)(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauth status = %d", w.Code)
	}
}

func TestHandlePreRequest_RecordsHookObservation(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	body, _ := json.Marshal(PreRequestPayload{
		UserMessage: "play my spotify playlists",
		RequestBody: json.RawMessage(`{"messages":[]}`),
		Metadata:    RequestMetadata{Model: "m", Provider: "openai", LoginName: "u"},
	})
	req := httptest.NewRequest("POST", "/v1/pre-request", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Host = "aegis.example.com"
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandlePreRequest)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	rep := srv.hookHealth.Snapshot(srv.baseURL)
	if len(rep.Recent) == 0 {
		t.Fatal("expected a recorded observation")
	}
	obs := rep.Recent[0]
	if !obs.HasUserMessage || !obs.HasRequestBody || obs.Action != ActionModify {
		t.Fatalf("obs = %+v", obs)
	}
}

func TestDeny_RecordsHookAuthFail(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest("POST", "/v1/pre-request", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandlePreRequest)(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", w.Code)
	}
	rep := srv.hookHealth.Snapshot(srv.baseURL)
	if rep.AuthFailures != 1 {
		t.Fatalf("authFailures = %d", rep.AuthFailures)
	}
}

func TestAdminSummary_IncludesApertureURL(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.apertureURL = "https://aperture.example.com"
	req := httptest.NewRequest("GET", "/v1/admin/summary", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.admin().HandleAdminSummary)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var rep struct {
		Runtime struct {
			BaseURL     string `json:"base_url"`
			ApertureURL string `json:"aperture_url"`
		} `json:"runtime"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rep.Runtime.ApertureURL != "https://aperture.example.com" {
		t.Fatalf("aperture_url = %q", rep.Runtime.ApertureURL)
	}
	if rep.Runtime.BaseURL != "https://aegis.example.com" {
		t.Fatalf("base_url = %q", rep.Runtime.BaseURL)
	}
}
