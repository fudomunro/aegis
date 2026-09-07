package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "test.json")

	cfg := `{
		"sources": [
			{
				"id": "src1",
				"type": "api",
				"url": "https://example.com/data",
				"cache_ttl": "5m"
			}
		],
		"rules": [
			{
				"id": "rule1",
				"match": { "keywords": ["patient"], "models": ["*"] },
				"source_ids": ["src1"],
				"action": "inject"
			}
		]
	}`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(loaded.Sources) != 1 {
		t.Fatalf("got %d sources, want 1", len(loaded.Sources))
	}
	if loaded.Sources[0].ID != "src1" {
		t.Fatalf("source ID = %q, want %q", loaded.Sources[0].ID, "src1")
	}
	if loaded.Sources[0].CacheTTL.Duration != 5*time.Minute {
		t.Fatalf("cache_ttl = %v, want 5m", loaded.Sources[0].CacheTTL.Duration)
	}
	if len(loaded.Rules) != 1 {
		t.Fatalf("got %d rules, want 1", len(loaded.Rules))
	}
}

func TestLoadConfig_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "bad.json")
	os.WriteFile(cfgPath, []byte("{bad}"), 0644)

	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestLoadConfig_MissingFile(t *testing.T) {
	_, err := LoadConfig("/nonexistent/path.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestFindMatchingRules_KeywordMatch(t *testing.T) {
	cfg := &Config{
		Rules: []Rule{
			{ID: "r1", Match: Match{Keywords: []string{"patient"}}, Action: "inject"},
			{ID: "r2", Match: Match{Keywords: []string{"invoice"}}, Action: "block"},
		},
	}

	matched := cfg.FindMatchingRules("gpt-4", "alice", "Tell me about the patient record")
	if len(matched) != 1 || matched[0].ID != "r1" {
		t.Fatalf("expected r1, got %v", matched)
	}
}

func TestFindMatchingRules_NoMatch(t *testing.T) {
	cfg := &Config{
		Rules: []Rule{
			{ID: "r1", Match: Match{Keywords: []string{"patient"}}, Action: "inject"},
		},
	}

	matched := cfg.FindMatchingRules("gpt-4", "alice", "Hello world")
	if len(matched) != 0 {
		t.Fatalf("expected no matches, got %v", matched)
	}
}

func TestFindMatchingRules_ModelFilter(t *testing.T) {
	cfg := &Config{
		Rules: []Rule{
			{ID: "r1", Match: Match{Models: []string{"gpt-4"}}, Action: "inject"},
		},
	}

	// Matching model
	matched := cfg.FindMatchingRules("gpt-4", "alice", "hello")
	if len(matched) != 1 {
		t.Fatalf("expected 1 match for gpt-4, got %d", len(matched))
	}

	// Non-matching model
	matched = cfg.FindMatchingRules("claude-3", "alice", "hello")
	if len(matched) != 0 {
		t.Fatalf("expected 0 matches for claude-3, got %d", len(matched))
	}
}

func TestFindMatchingRules_WildcardModel(t *testing.T) {
	cfg := &Config{
		Rules: []Rule{
			{ID: "r1", Match: Match{Models: []string{"*"}}, Action: "inject"},
		},
	}

	matched := cfg.FindMatchingRules("any-model", "alice", "hello")
	if len(matched) != 1 {
		t.Fatalf("expected wildcard to match, got %d", len(matched))
	}
}

func TestFindMatchingRules_UserFilter(t *testing.T) {
	cfg := &Config{
		Rules: []Rule{
			{ID: "r1", Match: Match{Users: []string{"alice", "bob"}}, Action: "block"},
		},
	}

	matched := cfg.FindMatchingRules("gpt-4", "alice", "hello")
	if len(matched) != 1 {
		t.Fatalf("expected alice to match, got %d", len(matched))
	}

	matched = cfg.FindMatchingRules("gpt-4", "mallory", "hello")
	if len(matched) != 0 {
		t.Fatalf("expected mallory not to match, got %d", len(matched))
	}
}

func TestFindMatchingRules_CaseInsensitiveKeywords(t *testing.T) {
	cfg := &Config{
		Rules: []Rule{
			{ID: "r1", Match: Match{Keywords: []string{"Patient"}}, Action: "inject"},
		},
	}

	matched := cfg.FindMatchingRules("gpt-4", "alice", "the PATIENT record")
	if len(matched) != 1 {
		t.Fatalf("expected case-insensitive keyword match, got %d", len(matched))
	}
}

func TestFindMatchingRules_SubstringKeyword(t *testing.T) {
	cfg := &Config{
		Rules: []Rule{
			{ID: "r1", Match: Match{Keywords: []string{"medical"}}, Action: "inject"},
		},
	}

	matched := cfg.FindMatchingRules("gpt-4", "alice", "show me the medical_record table")
	if len(matched) != 1 {
		t.Fatalf("expected substring keyword match, got %d", len(matched))
	}
}

func TestFindMatchingRules_AllFiltersCombined(t *testing.T) {
	cfg := &Config{
		Rules: []Rule{
			{
				ID:     "r1",
				Match:  Match{Keywords: []string{"patient"}, Models: []string{"gpt-4"}, Users: []string{"alice"}},
				Action: "inject",
			},
		},
	}

	// All match
	matched := cfg.FindMatchingRules("gpt-4", "alice", "patient info")
	if len(matched) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matched))
	}

	// Wrong user
	matched = cfg.FindMatchingRules("gpt-4", "bob", "patient info")
	if len(matched) != 0 {
		t.Fatalf("expected 0 matches for wrong user, got %d", len(matched))
	}

	// Wrong model
	matched = cfg.FindMatchingRules("claude-3", "alice", "patient info")
	if len(matched) != 0 {
		t.Fatalf("expected 0 matches for wrong model, got %d", len(matched))
	}

	// Wrong keyword
	matched = cfg.FindMatchingRules("gpt-4", "alice", "hello")
	if len(matched) != 0 {
		t.Fatalf("expected 0 matches for wrong keyword, got %d", len(matched))
	}
}

func TestFindMatchingRules_NilMatchFields(t *testing.T) {
	// When match fields are nil, they should not filter (match everything).
	cfg := &Config{
		Rules: []Rule{
			{ID: "r1", Match: Match{}, Action: "inject"},
		},
	}

	matched := cfg.FindMatchingRules("any-model", "any-user", "any message")
	if len(matched) != 1 {
		t.Fatalf("expected nil fields to match all, got %d", len(matched))
	}
}

func TestGetSource(t *testing.T) {
	cfg := &Config{
		Sources: []Source{
			{ID: "src1", Type: "api", URL: "https://example.com"},
			{ID: "src2", Type: "api", URL: "https://other.com"},
		},
	}

	src := cfg.GetSource("src1")
	if src == nil {
		t.Fatal("expected to find src1")
	}
	if src.URL != "https://example.com" {
		t.Fatalf("URL = %q, want %q", src.URL, "https://example.com")
	}

	src = cfg.GetSource("missing")
	if src != nil {
		t.Fatal("expected nil for missing source")
	}
}

func TestConfig_Reload(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "reload.json")

	initial := `{"sources":[],"rules":[{"id":"r1","match":{"keywords":["old"]},"action":"inject"}]}`
	os.WriteFile(cfgPath, []byte(initial), 0644)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Rules) != 1 || cfg.Rules[0].ID != "r1" {
		t.Fatal("initial config wrong")
	}

	updated := `{"sources":[],"rules":[{"id":"r2","match":{"keywords":["new"]},"action":"block"}]}`
	os.WriteFile(cfgPath, []byte(updated), 0644)

	if err := cfg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if len(cfg.Rules) != 1 || cfg.Rules[0].ID != "r2" {
		t.Fatalf("after reload: rules = %v", cfg.Rules)
	}
}

func TestDuration_UnmarshalJSON(t *testing.T) {
	var d Duration
	if err := d.UnmarshalJSON([]byte(`"10m"`)); err != nil {
		t.Fatal(err)
	}
	if d.Duration != 10*time.Minute {
		t.Fatalf("got %v, want 10m", d.Duration)
	}

	if err := d.UnmarshalJSON([]byte(`"bad"`)); err == nil {
		t.Fatal("expected error for bad duration")
	}
}

func TestDuration_MarshalJSON(t *testing.T) {
	d := Duration{3 * time.Hour}
	b, err := d.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `"3h0m0s"` {
		t.Fatalf("got %s, want %q", string(b), "3h0m0s")
	}
}
