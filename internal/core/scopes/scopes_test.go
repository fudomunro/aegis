package scopes

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeDataScopes(t *testing.T, scopes []Scope) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data_scopes.json")
	raw, err := json.Marshal(file{Version: fileVersion, DataScopes: scopes})
	if err != nil {
		t.Fatalf("marshal scopes: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write scopes: %v", err)
	}
	return path
}

func TestNewStore_MissingFile(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := s.FindByAPIKey("nope"); got != nil {
		t.Fatalf("expected nil scope, got %#v", got)
	}
}

func TestNewStore_LoadAndResolve(t *testing.T) {
	path := writeDataScopes(t, []Scope{
		{ID: "shire", Name: "Shire", Dataset: "shire", APIKey: "YOUR_SHIRE_KEY", ReadOnly: true},
		{ID: "gondor", Dataset: "gondor", APIKey: "YOUR_GONDOR_KEY"},
		{ID: "off", Dataset: "x", APIKey: "YOUR_OFF_KEY", Disabled: true},
	})
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	sc := s.FindByAPIKey("YOUR_SHIRE_KEY")
	if sc == nil || sc.ID != "shire" || sc.Dataset != "shire" || !sc.ReadOnly {
		t.Fatalf("shire scope = %#v", sc)
	}
	if sc.APIKey != "" {
		t.Fatalf("plaintext key retained in memory: %q", sc.APIKey)
	}
	if s.FindByAPIKey("YOUR_OFF_KEY") != nil {
		t.Fatal("disabled scope must not authenticate")
	}
	if s.FindByAPIKey("wrong") != nil {
		t.Fatal("unknown key must not authenticate")
	}
	if s.Get("gondor") == nil || s.Get("missing") != nil {
		t.Fatal("Get by id failed")
	}
	if len(s.All()) != 3 {
		t.Fatalf("All() = %d scopes", len(s.All()))
	}
}
