package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHookHealth_RecordAndSnapshot(t *testing.T) {
	h := NewHookHealth(4)
	h.Record(HookObservation{
		At:             time.Now(),
		Host:           "guardian.example.ts.net",
		Action:         ActionModify,
		HasUserMessage: true,
		HasRequestBody: true,
		Model:          "m",
		Provider:       "p",
		Login:          "u@example.com",
	})
	h.Record(HookObservation{Action: "auth_fail", Host: "aegis.example.ts.net"})

	rep := h.Snapshot("https://aegis.example.ts.net")
	if rep.Status != "error" {
		t.Fatalf("status = %q, want error (stale host + auth failures)", rep.Status)
	}
	if rep.AuthFailures != 1 || rep.StaleHostHits != 1 {
		t.Fatalf("authFailures=%d stale=%d", rep.AuthFailures, rep.StaleHostHits)
	}
	if rep.LastHookAt == nil {
		t.Fatal("expected last_hook_at")
	}
	codes := map[string]bool{}
	for _, is := range rep.Issues {
		codes[is.Code] = true
	}
	for _, want := range []string{"stale_hook_host", "hook_auth_failures"} {
		if !codes[want] {
			t.Fatalf("missing issue %q in %+v", want, rep.Issues)
		}
	}
	if rep.ExpectedURL != "https://aegis.example.ts.net/v1/pre-request" {
		t.Fatalf("expected_url = %q", rep.ExpectedURL)
	}
	if len(rep.ExpectedHookConfig) == 0 {
		t.Fatal("expected hook config snippet")
	}
}

func TestHookHealth_NoTrafficWarns(t *testing.T) {
	rep := NewHookHealth(0).Snapshot("https://aegis.example.com")
	if rep.Status != "warn" {
		t.Fatalf("status = %q, want warn", rep.Status)
	}
	found := false
	for _, is := range rep.Issues {
		if is.Code == "no_hook_traffic" {
			found = true
		}
	}
	if !found {
		t.Fatalf("issues = %+v", rep.Issues)
	}
}

func TestHookHealth_MissingPayloadFields(t *testing.T) {
	h := NewHookHealth(8)
	h.Record(HookObservation{Action: ActionAllow, Host: "aegis.example.com"})
	rep := h.Snapshot("https://aegis.example.com")
	codes := map[string]bool{}
	for _, is := range rep.Issues {
		codes[is.Code] = true
	}
	if !codes["missing_request_body"] || !codes["missing_user_message"] {
		t.Fatalf("issues = %+v", rep.Issues)
	}
}

func TestCheckApertureConfig_LegacyGuardian(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.ts.net")
	raw := []byte(`{
	  "hooks": {
	    "guardian": {
	      "url": "https://guardian.example.ts.net/v1/pre-request",
	      "apikey": "k",
	      "timeout": "5s"
	    }
	  },
	  "grants": [{
	    "src": ["*"],
	    "app": {
	      "tailscale.com/cap/aperture": [{
	        "models": "**",
	        "send_hooks": [{
	          "name": "guardian",
	          "events": ["pre_request"],
	          "send": ["user_message"]
	        }]
	      }]
	    }
	  }]
	}`)
	rep := srv.CheckApertureConfig(raw)
	codes := map[string]bool{}
	for _, is := range rep.Issues {
		codes[is.Code] = true
	}
	for _, want := range []string{
		"legacy_hook_name",
		"legacy_hook_url",
		"legacy_send_hook_name",
		"send_missing_request_body",
	} {
		if !codes[want] {
			t.Fatalf("missing %q in %+v", want, rep.Issues)
		}
	}
	if rep.Status != "error" {
		t.Fatalf("status = %q, want error", rep.Status)
	}
}

func TestCheckApertureConfig_ExpectedSnippetPasses(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	rep := srv.hookHealth.Snapshot(srv.baseURL)
	raw := rep.ExpectedHookConfig
	// Replace placeholder apikey so the hook def is concrete.
	raw = bytes.ReplaceAll(raw, []byte("<AEGIS_API_KEY if auth-mode is auto/api-key>"), []byte("test-key"))
	checked := srv.CheckApertureConfig(raw)
	for _, is := range checked.Issues {
		if is.Severity == "error" {
			t.Fatalf("unexpected error on expected config: %+v", is)
		}
	}
}

func TestCheckApertureConfig_URLOutOfSync(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	raw := []byte(`{
	  "hooks": {
	    "aegis": {"url": "https://old.example.com/v1/pre-request", "apikey": "k"}
	  }
	}`)
	rep := srv.CheckApertureConfig(raw)
	found := false
	for _, is := range rep.Issues {
		if is.Code == "hook_url_mismatch" || is.Code == "legacy_hook_url" {
			found = true
		}
	}
	if !found {
		t.Fatalf("issues = %+v", rep.Issues)
	}
}

func TestCheckApertureConfig_NotJSON(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	rep := srv.CheckApertureConfig([]byte("not-json"))
	found := false
	for _, is := range rep.Issues {
		if is.Code == "config_not_json" {
			found = true
		}
	}
	if !found {
		t.Fatalf("issues = %+v", rep.Issues)
	}
}

func TestHandleAdminHookHealth_GetAndCheck(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")

	req := httptest.NewRequest("GET", "/v1/admin/hook-health", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminHookHealth)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%s", w.Code, w.Body.String())
	}
	var rep HookHealthReport
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
	srv.AuthMiddleware(srv.HandleAdminHookHealth)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST status = %d body=%s", w.Code, w.Body.String())
	}

	// Empty body is a 400.
	req = httptest.NewRequest("POST", "/v1/admin/hook-health/check", strings.NewReader("  "))
	req.Header.Set("Authorization", "Bearer test-key")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminHookHealth)(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty body status = %d", w.Code)
	}

	// Method not allowed.
	req = httptest.NewRequest("DELETE", "/v1/admin/hook-health", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminHookHealth)(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE status = %d", w.Code)
	}

	// Unauthenticated.
	req = httptest.NewRequest("GET", "/v1/admin/hook-health", nil)
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminHookHealth)(w, req)
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

func TestHostMatchesBase(t *testing.T) {
	if !hostMatchesBase("aegis.example.com", "https://aegis.example.com") {
		t.Fatal("exact host should match")
	}
	if !hostMatchesBase("aegis.example.com:443", "https://aegis.example.com") {
		t.Fatal("port should be ignored")
	}
	if hostMatchesBase("guardian.example.com", "https://aegis.example.com") {
		t.Fatal("stale host should not match")
	}
	if !hostMatchesBase("", "https://aegis.example.com") {
		t.Fatal("empty host is treated as unknown/pass")
	}
}

func TestIsStaleHookHost(t *testing.T) {
	if !isStaleHookHost("Guardian.example.ts.net") {
		t.Fatal("guardian host is stale")
	}
	if isStaleHookHost("aegis.example.ts.net") {
		t.Fatal("aegis host is not stale")
	}
}

func TestAdminSummary_IncludesApertureURL(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.apertureURL = "https://aperture.example.com"
	req := httptest.NewRequest("GET", "/v1/admin/summary", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminSummary)(w, req)
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
