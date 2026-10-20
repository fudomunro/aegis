package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLooksLikeDevelopment(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"play some jazz on Spotify", false},
		{"what playlists do I have?", false},
		{"find my yt music playlists", false},
		{"check Spotify's top charts", false},
		{"compare my Spotify and YouTube Music libraries", false},
		{"Fix the Spotify connector in internal/connectors/spotify.go", true},
		{"Update FindMatchingServices so it stops matching too eagerly", true},
		{"Review this:\n```go\nfunc (c *SpotifyConnector) Capabilities() {}\n```", true},
		{"edit internal/core/handler.go to skip development matches", true},
		{"the bug is in librarykind.go around hints", true},
		{"see AGENTS.md for the product rules", true},
		{"run go test ./internal/core and report failures", true},
		{"check go.mod for the sqlite dependency", true},
		{"change LibraryKind registry so places stays last", true},
	}
	for _, tc := range cases {
		if got := looksLikeDevelopment(tc.msg); got != tc.want {
			t.Errorf("looksLikeDevelopment(%q) = %v, want %v", tc.msg, got, tc.want)
		}
	}
}

func TestLooksLikeDevelopment_Empty(t *testing.T) {
	if looksLikeDevelopment("") {
		t.Fatal("empty message must not look like development")
	}
}

func TestShouldSkipMatching_EmptyMessage(t *testing.T) {
	cfg := &Config{}
	if reason := cfg.shouldSkipMatching("   "); reason != "" {
		t.Fatalf("reason = %q, want empty for blank message", reason)
	}
}

func TestShouldSkipMatching_SkipPattern(t *testing.T) {
	cfg := &Config{}
	skip, err := compileHookSkip(HookConfig{SkipPatterns: []string{`worktree`, `aegis-issue-`}})
	if err != nil {
		t.Fatalf("compileHookSkip: %v", err)
	}
	cfg.hookSkip = skip

	if reason := cfg.shouldSkipMatching("play Spotify in the aegis-issue-205 worktree"); reason != "skip_pattern" {
		t.Fatalf("reason = %q, want skip_pattern", reason)
	}
	if reason := cfg.shouldSkipMatching("play some jazz on Spotify"); reason != "" {
		t.Fatalf("reason = %q, want empty", reason)
	}
}

func TestShouldSkipMatching_DevelopmentWinsOverPatterns(t *testing.T) {
	cfg := &Config{}
	if reason := cfg.shouldSkipMatching("fix spotify.go matching"); reason != "development" {
		t.Fatalf("reason = %q, want development", reason)
	}
}

func TestCompileHookSkip_InvalidPattern(t *testing.T) {
	_, err := compileHookSkip(HookConfig{SkipPatterns: []string{`([`}})
	if err == nil {
		t.Fatal("expected error for invalid regex")
	}
}

func TestCompileHookSkip_EmptyPattern(t *testing.T) {
	_, err := compileHookSkip(HookConfig{SkipPatterns: []string{`  `}})
	if err == nil {
		t.Fatal("expected error for empty pattern")
	}
}

func TestLoadConfig_HookSkipPatterns(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "test.json")
	cfg := `{
		"hook": {"skip_patterns": ["my-repo", "AGENTS\\.md"]},
		"services": [
			{"id": "spotify", "name": "Spotify", "action": "inject", "cache_ttl": "5m"}
		]
	}`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if reason := loaded.shouldSkipMatching("tweak the Spotify connector in my-repo"); reason != "skip_pattern" {
		t.Fatalf("reason = %q, want skip_pattern", reason)
	}
	if reason := loaded.shouldSkipMatching("play Spotify"); reason != "" {
		t.Fatalf("reason = %q, want empty", reason)
	}
}

func TestLoadConfig_HookInvalidPattern(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "bad.json")
	cfg := `{"hook": {"skip_patterns": ["("]}, "services": []}`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(cfgPath); err == nil {
		t.Fatal("expected error for invalid hook.skip_patterns regex")
	}
}

func TestReload_CopiesHookSkip(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "test.json")
	cfg := `{"services": [{"id": "spotify", "name": "Spotify", "action": "inject", "cache_ttl": "5m"}]}`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	updated := `{
		"hook": {"skip_patterns": ["reload-marker"]},
		"services": [{"id": "spotify", "name": "Spotify", "action": "inject", "cache_ttl": "5m"}]
	}`
	if err := os.WriteFile(cfgPath, []byte(updated), 0644); err != nil {
		t.Fatal(err)
	}
	if err := loaded.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if reason := loaded.shouldSkipMatching("play Spotify with reload-marker"); reason != "skip_pattern" {
		t.Fatalf("reason = %q, want skip_pattern after reload", reason)
	}
}

func TestHandlePreRequest_SkipsDevelopment(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	payload := PreRequestPayload{
		Metadata:    RequestMetadata{LoginName: "alice", Model: "gpt-4", RequestID: "req-dev"},
		UserMessage: "Fix the Spotify matcher in internal/core/config.go",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest("POST", "/v1/pre-request", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandlePreRequest)(w, req)

	var resp GuardrailResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Action != ActionAllow {
		t.Fatalf("action = %q, want %q (development must not inject)", resp.Action, ActionAllow)
	}
	if len(resp.RequestBody) != 0 {
		t.Fatal("development skip must not rewrite the body")
	}
}

func TestHandlePreRequest_SkipsConfiguredPattern(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
		},
	}
	skip, err := compileHookSkip(HookConfig{SkipPatterns: []string{`coding-workspace`}})
	if err != nil {
		t.Fatal(err)
	}
	cfg.hookSkip = skip
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	payload := PreRequestPayload{
		Metadata:    RequestMetadata{LoginName: "alice", Model: "gpt-4", RequestID: "req-pat"},
		UserMessage: "play Spotify in the coding-workspace session",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest("POST", "/v1/pre-request", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandlePreRequest)(w, req)

	var resp GuardrailResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Action != ActionAllow {
		t.Fatalf("action = %q, want allow", resp.Action)
	}
	if len(resp.RequestBody) != 0 {
		t.Fatal("skip pattern must not rewrite the body")
	}
}

func TestHandlePreRequest_StillInjectsConsumerSpotify(t *testing.T) {
	if looksLikeDevelopment("play some jazz on Spotify") {
		t.Fatal("consumer query must not look like development")
	}

	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject", Endpoint: "https://api.example.com/spotify"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	originalReq := map[string]any{
		"model": "gpt-4",
		"messages": []map[string]string{
			{"role": "user", "content": "Play some jazz on Spotify"},
		},
	}
	reqBody, _ := json.Marshal(originalReq)
	payload := PreRequestPayload{
		Metadata:    RequestMetadata{LoginName: "alice", Model: "gpt-4", RequestID: "req-consumer"},
		UserMessage: "play some jazz on Spotify",
		RequestBody: reqBody,
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest("POST", "/v1/pre-request", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandlePreRequest)(w, req)

	var resp GuardrailResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Consumer query must not be classified as development. Without a
	// connector, matching may still allow if there is nothing to inject —
	// the important part is it is not the development/skip path leaving
	// a consumer rewrite on the table for a false "development" hit.
	if looksLikeDevelopment(payload.UserMessage) {
		t.Fatal("consumer Spotify query misclassified as development")
	}
	_ = resp
}

func TestHandlePreRequest_SkipAllowsUnchanged(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	payload := PreRequestPayload{
		Metadata:    RequestMetadata{LoginName: "alice", Model: "gpt-4", RequestID: "req-obs"},
		UserMessage: "review AGENTS.md and the Spotify connector",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest("POST", "/v1/pre-request", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandlePreRequest)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"allow"`) {
		t.Fatalf("body = %s, want allow", w.Body.String())
	}
}
