package hookhealth

import (
	"bytes"
	"testing"
	"time"
)

func TestHookHealth_RecordAndSnapshot(t *testing.T) {
	h := NewHookHealth(4)
	h.Record(HookObservation{
		At:             time.Now(),
		Host:           "guardian.example.ts.net",
		Action:         "modify",
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
	h.Record(HookObservation{Action: "allow", Host: "aegis.example.com"})
	rep := h.Snapshot("https://aegis.example.com")
	codes := map[string]bool{}
	for _, is := range rep.Issues {
		codes[is.Code] = true
	}
	if !codes["missing_request_body"] || !codes["missing_user_message"] {
		t.Fatalf("issues = %+v", rep.Issues)
	}
}

func TestHookHealth_RecordsUnrecognizedBodyShape(t *testing.T) {
	h := NewHookHealth(8)
	h.Record(HookObservation{Action: "allow", BodyShape: "unknown"})
	rep := h.Snapshot("https://aegis.example.com")
	if rep.UnrecognizedShapes != 1 {
		t.Fatalf("unrecognized_shapes = %d, want 1", rep.UnrecognizedShapes)
	}
}

func TestCheckApertureConfig_LegacyGuardian(t *testing.T) {
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
	rep := NewHookHealth(0).CheckApertureConfig(raw, "https://aegis.example.ts.net")
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
	h := NewHookHealth(0)
	baseURL := "https://aegis.example.com"
	raw := h.Snapshot(baseURL).ExpectedHookConfig
	// Replace placeholder apikey so the hook def is concrete.
	raw = bytes.ReplaceAll(raw, []byte("<AEGIS_API_KEY if auth-mode is auto/api-key>"), []byte("test-key"))
	checked := h.CheckApertureConfig(raw, baseURL)
	for _, is := range checked.Issues {
		if is.Severity == "error" {
			t.Fatalf("unexpected error on expected config: %+v", is)
		}
	}
}

func TestCheckApertureConfig_URLOutOfSync(t *testing.T) {
	raw := []byte(`{
	  "hooks": {
	    "aegis": {"url": "https://old.example.com/v1/pre-request", "apikey": "k"}
	  }
	}`)
	rep := NewHookHealth(0).CheckApertureConfig(raw, "https://aegis.example.com")
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
	rep := NewHookHealth(0).CheckApertureConfig([]byte("not-json"), "https://aegis.example.com")
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

func TestSortIssues_StableBySeverityThenCode(t *testing.T) {
	issues := []HookIssue{
		{Severity: "info", Code: "b"},
		{Severity: "error", Code: "z"},
		{Severity: "warn", Code: "a"},
		{Severity: "error", Code: "a"},
		{Severity: "warn", Code: "b"},
	}
	SortIssues(issues)
	want := []string{"error:a", "error:z", "warn:a", "warn:b", "info:b"}
	for i, w := range want {
		got := issues[i].Severity + ":" + issues[i].Code
		if got != w {
			t.Fatalf("[%d] = %s, want %s (all=%+v)", i, got, w, issues)
		}
	}
}
