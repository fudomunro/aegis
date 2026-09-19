package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := writeFile(path, content); err != nil {
		t.Fatal(err)
	}
}

func TestPermissionsOverlay_PersistsWithoutTouchingGuardianJSON(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "guardian.json")
	permsPath := filepath.Join(dir, "permissions.json")
	initial := `{
		"connections": {
			"spotify": {
				"client_id": "cid",
				"client_secret": "super-secret",
				"auth_url": "https://example.com/auth",
				"token_url": "https://example.com/token"
			}
		},
		"services": [
			{
				"id": "spotify",
				"name": "Spotify",
				"action": "inject",
				"endpoint": "https://api.spotify.com/v1",
				"connection": "spotify",
				"cache_ttl": "10m",
				"permissions": {"default": "read"}
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
	if err := cfg.InitPermissionsStore(permsPath); err != nil {
		t.Fatalf("InitPermissionsStore: %v", err)
	}
	// Bootstrap from guardian.json still applies when overlay is empty.
	if got := cfg.GetService("spotify").Permissions.For("tracks"); got != AccessRead {
		t.Fatalf("bootstrap tracks = %q, want read", got)
	}

	if err := cfg.SetServicePermissions("spotify", Permissions{
		"default": AccessRead,
		"player":  AccessNone,
	}); err != nil {
		t.Fatalf("SetServicePermissions: %v", err)
	}

	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("guardian.json was rewritten:\nbefore=%s\nafter=%s", before, after)
	}
	if !strings.Contains(string(after), "super-secret") {
		t.Fatal("guardian.json lost OAuth secret")
	}

	ovRaw, err := os.ReadFile(permsPath)
	if err != nil {
		t.Fatalf("read overlay: %v", err)
	}
	var ov permissionsOverlayFile
	if err := json.Unmarshal(ovRaw, &ov); err != nil {
		t.Fatalf("parse overlay: %v", err)
	}
	if ov.Version != permissionsFileVersion {
		t.Fatalf("overlay version = %d", ov.Version)
	}
	if ov.Services["spotify"] == nil || (*ov.Services["spotify"])["player"] != AccessNone {
		t.Fatalf("overlay spotify = %+v", ov.Services["spotify"])
	}

	// Fresh load: bootstrap + overlay.
	fresh, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.InitPermissionsStore(permsPath); err != nil {
		t.Fatal(err)
	}
	svc := fresh.GetService("spotify")
	if svc == nil {
		t.Fatal("spotify missing after reload")
	}
	if svc.Permissions.For("player") != AccessNone || svc.Permissions.For("tracks") != AccessRead {
		t.Fatalf("reloaded permissions = %+v", svc.Permissions)
	}

	// Clear via overlay — bootstrap default must not resurface.
	if err := cfg.SetServicePermissions("spotify", nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if cfg.GetService("spotify").Permissions != nil {
		t.Fatal("expected nil in-memory permissions after clear")
	}
	fresh, err = LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.InitPermissionsStore(permsPath); err != nil {
		t.Fatal(err)
	}
	if fresh.GetService("spotify").Permissions != nil {
		t.Fatalf("clear must override bootstrap, got %+v", fresh.GetService("spotify").Permissions)
	}

	// guardian.json still untouched after clear.
	after2, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after2) {
		t.Fatal("guardian.json was rewritten after clear")
	}
}

func TestPermissionsOverlay_BootstrapUnchangedWhenAbsentFromOverlay(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "guardian.json")
	permsPath := filepath.Join(dir, "permissions.json")
	mustWriteFile(t, cfgPath, `{
		"services": [
			{"id": "spotify", "name": "Spotify", "action": "inject", "permissions": {"player": "none"}},
			{"id": "immich", "name": "Immich", "action": "inject", "permissions": {"photos": "read"}}
		]
	}`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.InitPermissionsStore(permsPath); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SetServicePermissions("immich", Permissions{"photos": AccessRead, "default": AccessNone}); err != nil {
		t.Fatal(err)
	}

	fresh, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.InitPermissionsStore(permsPath); err != nil {
		t.Fatal(err)
	}
	// spotify not in overlay → bootstrap.
	if got := fresh.GetService("spotify").Permissions.For("player"); got != AccessNone {
		t.Fatalf("spotify player = %q, want none from bootstrap", got)
	}
	// immich in overlay → overlay wins.
	if got := fresh.GetService("immich").Permissions.For("contacts"); got != AccessNone {
		t.Fatalf("immich contacts = %q, want none from overlay default", got)
	}
	if got := fresh.GetService("immich").Permissions.For("photos"); got != AccessRead {
		t.Fatalf("immich photos = %q, want read", got)
	}
}

func TestPermissionsOverlay_MissingFileIsEmpty(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "guardian.json")
	permsPath := filepath.Join(dir, "does-not-exist.json")
	mustWriteFile(t, cfgPath, `{"services":[{"id":"spotify","name":"Spotify","action":"inject","permissions":{"player":"none"}}]}`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.InitPermissionsStore(permsPath); err != nil {
		t.Fatalf("missing overlay should not fail: %v", err)
	}
	if got := cfg.GetService("spotify").Permissions.For("player"); got != AccessNone {
		t.Fatalf("player = %q, want none from bootstrap", got)
	}
}

func TestPermissionsOverlay_InvalidLevelRejectedOnLoad(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "guardian.json")
	permsPath := filepath.Join(dir, "permissions.json")
	mustWriteFile(t, cfgPath, `{"services":[{"id":"spotify","name":"Spotify","action":"inject"}]}`)
	mustWriteFile(t, permsPath, `{"version":1,"services":{"spotify":{"tracks":"maybe"}}}`)
	if _, err := LoadConfig(cfgPath); err == nil {
		t.Fatal("expected invalid overlay level to fail LoadConfig")
	}
}

func TestPermissionsOverlay_ReloadReappliesOverlay(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "guardian.json")
	permsPath := filepath.Join(dir, "permissions.json")
	mustWriteFile(t, cfgPath, `{"services":[{"id":"spotify","name":"Spotify","action":"inject","permissions":{"default":"read"}}]}`)
	mustWriteFile(t, permsPath, `{"version":1,"services":{"spotify":{"default":"none"}}}`)

	// LoadConfig applies the sibling overlay automatically.
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.GetService("spotify").Permissions.For("tracks"); got != AccessNone {
		t.Fatalf("overlay tracks = %q, want none", got)
	}

	// Simulate external guardian.json edit (bootstrap change), then reload.
	mustWriteFile(t, cfgPath, `{"services":[{"id":"spotify","name":"Spotify","action":"inject","permissions":{"default":"read","player":"read"}}]}`)
	if err := cfg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	// Overlay still wins for default.
	if got := cfg.GetService("spotify").Permissions.For("tracks"); got != AccessNone {
		t.Fatalf("after reload tracks = %q, want none from overlay", got)
	}
}

func TestInitPermissionsStore_EmptyPathRejected(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "guardian.json")
	mustWriteFile(t, cfgPath, `{"services":[{"id":"spotify","name":"Spotify","action":"inject"}]}`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.InitPermissionsStore(""); err == nil {
		t.Fatal("empty permissions overlay path must be rejected")
	}
}
