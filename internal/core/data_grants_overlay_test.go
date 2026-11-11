package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/shared"
)

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := writeFile(path, content); err != nil {
		t.Fatal(err)
	}
}

func TestPermissionsOverlay_PersistsWithoutTouchingAegisJSON(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	permsPath := filepath.Join(dir, "data_grants.json")
	initial := `{
		"connections": {
			"spotify": {
				"client_id": "cid",
				"client_secret": "super-secret",
				"auth_url": "https://example.com/auth",
				"token_url": "https://example.com/token"
			}
		},
		"data_grants": {"tracks": {"default": "read"}},
		"services": [
			{
				"id": "spotify",
				"name": "Spotify",
				"action": "inject",
				"endpoint": "https://api.spotify.com/v1",
				"connection": "spotify",
				"cache_ttl": "10m"
			}
		]
	}`
	mustWriteFile(t, cfgPath, initial)
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if err := cfg.InitDataGrantsStore(permsPath); err != nil {
		t.Fatalf("InitDataGrantsStore: %v", err)
	}
	// Bootstrap from aegis.json still applies when the overlay is empty.
	if got := cfg.DataGrants().Levels().Level("tracks"); got != shared.AccessRead {
		t.Fatalf("bootstrap tracks = %q, want read", got)
	}

	if err := cfg.SetDataGrants(policy.DataGrants{
		"tracks": {Default: shared.AccessFull},
		"player": {Default: shared.AccessNone},
	}); err != nil {
		t.Fatalf("SetDataGrants: %v", err)
	}

	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("aegis.json was rewritten:\nbefore=%s\nafter=%s", before, after)
	}
	if !strings.Contains(string(after), "super-secret") {
		t.Fatal("aegis.json lost OAuth secret")
	}

	ovRaw, err := os.ReadFile(permsPath)
	if err != nil {
		t.Fatalf("read overlay: %v", err)
	}
	var ov dataGrantsOverlayFile
	if err := json.Unmarshal(ovRaw, &ov); err != nil {
		t.Fatalf("parse overlay: %v", err)
	}
	if ov.Version != dataGrantsFileVersion {
		t.Fatalf("overlay version = %d", ov.Version)
	}
	if ov.DataGrants["tracks"].Default != shared.AccessFull {
		t.Fatalf("overlay tracks = %+v", ov.DataGrants["tracks"])
	}

	// Fresh load: overlay replaces the bootstrap map.
	fresh, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.InitDataGrantsStore(permsPath); err != nil {
		t.Fatal(err)
	}
	rules := fresh.DataGrants().Levels()
	if rules.Level("tracks") != shared.AccessFull || rules.Level("player") != shared.AccessNone {
		t.Fatalf("reloaded grants = %+v", fresh.DataGrants())
	}

	// Clear via an explicit empty map — bootstrap must not resurface.
	if err := cfg.SetDataGrants(policy.DataGrants{}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if len(cfg.DataGrants()) != 0 {
		t.Fatalf("expected empty in-memory grants after clear, got %+v", cfg.DataGrants())
	}
	fresh, err = LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.InitDataGrantsStore(permsPath); err != nil {
		t.Fatal(err)
	}
	if len(fresh.DataGrants()) != 0 {
		t.Fatalf("clear must override bootstrap, got %+v", fresh.DataGrants())
	}

	// aegis.json still untouched after clear.
	after2, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after2) {
		t.Fatal("aegis.json was rewritten after clear")
	}
}

func TestPermissionsOverlay_BootstrapUnchangedWhenAbsentFromOverlay(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	permsPath := filepath.Join(dir, "data_grants.json")
	mustWriteFile(t, cfgPath, `{
		"data_grants": {"tracks": {"default": "read"}, "player": {"default": "none"}},
		"services": [{"id": "spotify", "name": "Spotify", "action": "inject"}]
	}`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.InitDataGrantsStore(permsPath); err != nil {
		t.Fatal(err)
	}
	// An overlay file without a data_grants map leaves bootstrap in place.
	if got := cfg.DataGrants().Levels().Level("player"); got != shared.AccessNone {
		t.Fatalf("player = %q, want none from bootstrap", got)
	}
	if got := cfg.DataGrants().Levels().Level("tracks"); got != shared.AccessRead {
		t.Fatalf("tracks = %q, want read from bootstrap", got)
	}
}

func TestPermissionsOverlay_MissingFileIsEmpty(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	permsPath := filepath.Join(dir, "does-not-exist.json")
	mustWriteFile(t, cfgPath, `{"data_grants": {"player": {"default": "none"}}, "services":[{"id":"spotify", "name": "Spotify", "action": "inject"}]}`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.InitDataGrantsStore(permsPath); err != nil {
		t.Fatalf("missing overlay should not fail: %v", err)
	}
	if got := cfg.DataGrants().Levels().Level("player"); got != shared.AccessNone {
		t.Fatalf("player = %q, want none from bootstrap", got)
	}
}

func TestPermissionsOverlay_InvalidLevelRejectedOnLoad(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	permsPath := filepath.Join(dir, "data_grants.json")
	mustWriteFile(t, cfgPath, `{"services":[{"id":"spotify", "name": "Spotify", "action": "inject"}]}`)
	mustWriteFile(t, permsPath, `{"version":3,"data_grants":{"tracks":{"default":"maybe"}}}`)
	if _, err := LoadConfig(cfgPath); err == nil {
		t.Fatal("expected invalid overlay level to fail LoadConfig")
	}
}

func TestPermissionsOverlay_ReloadReappliesOverlay(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	permsPath := filepath.Join(dir, "data_grants.json")
	mustWriteFile(t, cfgPath, `{"data_grants": {"tracks": {"default": "read"}}, "services":[{"id":"spotify", "name": "Spotify", "action": "inject"}]}`)
	mustWriteFile(t, permsPath, `{"version":3,"data_grants":{"tracks":{"default":"none"}}}`)

	// LoadConfig applies the sibling overlay automatically.
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.DataGrants().Levels().Level("tracks"); got != shared.AccessNone {
		t.Fatalf("overlay tracks = %q, want none", got)
	}

	// Simulate external aegis.json edit (bootstrap change), then reload.
	mustWriteFile(t, cfgPath, `{"data_grants": {"tracks": {"default": "read-write"}}, "services":[{"id":"spotify", "name": "Spotify", "action": "inject"}]}`)
	if err := cfg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	// Overlay still wins.
	if got := cfg.DataGrants().Levels().Level("tracks"); got != shared.AccessNone {
		t.Fatalf("after reload tracks = %q, want none from overlay", got)
	}
}

func TestInitDataGrantsStore_EmptyPathRejected(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	mustWriteFile(t, cfgPath, `{"services":[{"id":"spotify","name":"Spotify","action":"inject"}]}`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.InitDataGrantsStore(""); err == nil {
		t.Fatal("empty permissions overlay path must be rejected")
	}
}
