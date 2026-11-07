package scopes

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
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

func TestServiceVisible(t *testing.T) {
	owner := (*Scope)(nil)
	shire := &Scope{ID: "shire", Dataset: "shire"}
	gondor := &Scope{ID: "gondor", Dataset: "gondor"}

	if !ServiceVisible("", owner) || !ServiceVisible("", shire) {
		t.Fatal("shared service must be visible to every scope")
	}
	if !ServiceVisible("shire", owner) {
		t.Fatal("owner scope sees every service")
	}
	if !ServiceVisible("shire", shire) {
		t.Fatal("matching dataset must be visible")
	}
	if ServiceVisible("shire", gondor) {
		t.Fatal("other dataset must be invisible")
	}
	if !ServiceVisible("", shire) {
		t.Fatal("empty dataset is shared")
	}
}

func TestCacheSuffix(t *testing.T) {
	if CacheSuffix(nil) != "" {
		t.Fatal("default principal must not change cache keys")
	}
	// Every scoped principal is namespaced — grants change response content
	// even on the owner dataset (issue #253).
	plain := CacheSuffix(&Scope{ID: "x"})
	if !strings.Contains(plain, "|sg:") {
		t.Fatalf("scope without dataset = %q, want grants fingerprint", plain)
	}
	shire := CacheSuffix(&Scope{Dataset: "shire"})
	if !strings.HasPrefix(shire, "|ds:shire|sg:") {
		t.Fatalf("suffix = %q", shire)
	}
	read := CacheSuffix(&Scope{Dataset: "shire", Grants: readAllGrants()})
	if read == shire {
		t.Fatal("different grants must not share a cache suffix")
	}
	if again := CacheSuffix(&Scope{Dataset: "shire", Grants: readAllGrants()}); again != read {
		t.Fatalf("identical grants must fingerprint identically: %q vs %q", again, read)
	}
	if got := CacheSuffix(&Scope{Dataset: "gondor", Grants: readAllGrants()}); got == read {
		t.Fatal("different datasets must not share a cache suffix")
	}
}

func readAllGrants() types.DataGrants {
	return types.DataGrants{types.PermissionDefaultKey: {Default: types.AccessRead}}
}
