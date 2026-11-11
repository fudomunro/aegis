package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/shared"
)

func TestDataGrantsOverlay_Primary(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	permsPath := filepath.Join(dir, "data_grants.json")
	initial := `{
		"services": [
			{
				"id": "spotify",
				"name": "Spotify",
				"action": "inject",
				"endpoint": "https://api.spotify.com/v1",
				"grant_limits": {"tracks": "read", "player": "none"}
			},
			{
				"id": "immich",
				"name": "Immich",
				"action": "inject",
				"endpoint": "http://immich.local/api",
				"grant_limits": {"places": "city"}
			}
		]
	}`
	mustWriteFile(t, cfgPath, initial)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if err := cfg.InitDataGrantsStore(permsPath); err != nil {
		t.Fatalf("InitDataGrantsStore: %v", err)
	}

	// Data grant is primary: tracks default full, spotify tightens to none.
	if err := cfg.SetDataGrants(policy.DataGrants{
		"tracks": {Default: shared.AccessFull},
		"places": {Default: shared.AccessNone},
	}); err != nil {
		t.Fatalf("SetDataGrants: %v", err)
	}

	// The rule map takes each kind's default level.
	if got := cfg.DataGrants().Levels().Level("tracks"); got != shared.AccessFull {
		t.Fatalf("tracks = %q, want full", got)
	}
	if got := cfg.DataGrants().Levels().Level("places"); got != shared.AccessNone {
		t.Fatalf("places = %q, want none", got)
	}

	// Persisted as v3 data_grants.
	raw, err := os.ReadFile(permsPath)
	if err != nil {
		t.Fatal(err)
	}
	var file dataGrantsOverlayFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if file.Version != dataGrantsFileVersion {
		t.Fatalf("version = %d", file.Version)
	}
	if file.DataGrants["tracks"].Default != shared.AccessFull {
		t.Fatalf("data_grants.tracks = %+v", file.DataGrants["tracks"])
	}

	// Reload keeps data grant and resolves the same way.
	fresh, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig reload: %v", err)
	}
	if err := fresh.InitDataGrantsStore(permsPath); err != nil {
		t.Fatalf("InitDataGrantsStore reload: %v", err)
	}
	if got := fresh.DataGrants().Levels().Level("tracks"); got != shared.AccessFull {
		t.Fatalf("reloaded tracks = %q", got)
	}
}

func TestLoadConfig_DataGrantsBootstrap(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	mustWriteFile(t, cfgPath, `{
		"data_grants": {
			"tracks": {"default": "read"},
			"places": {"default": "none"}
		},
		"services": [
			{"id": "spotify", "name": "Spotify", "action": "inject"},
			{"id": "google-timeline", "name": "Timeline", "action": "inject"}
		]
	}`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.DataGrants().Levels().Level("tracks"); got != shared.AccessRead {
		t.Fatalf("bootstrap tracks = %q", got)
	}
	if got := cfg.DataGrants().Levels().Level("places"); got != shared.AccessNone {
		t.Fatalf("bootstrap places = %q", got)
	}

	// Overlay types replace bootstrap when present.
	permsPath := filepath.Join(dir, "data_grants.json")
	mustWriteFile(t, permsPath, `{"version":3,"data_grants":{"tracks":{"default":"read-write"}},"service_limits":{}}`)
	if err := cfg.InitDataGrantsStore(permsPath); err != nil {
		t.Fatalf("InitDataGrantsStore: %v", err)
	}
	if got := cfg.DataGrants().Levels().Level("tracks"); got != shared.AccessFull {
		t.Fatalf("overlay types win = %q", got)
	}
}

func TestHandleAdminDataGrants(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	mustWriteFile(t, cfgPath, `{"services":[{"id":"spotify","name":"Spotify","action":"inject"}]}`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	if err := srv.config.InitDataGrantsStore(filepath.Join(dir, "data_grants.json")); err != nil {
		t.Fatalf("InitDataGrantsStore: %v", err)
	}

	body := `{"data_grants":{"tracks":{"default":"read"}}}`
	req := httptest.NewRequest("PUT", "/v1/admin/data-grants", strings.NewReader(body))
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	dispatchAdmin(srv, w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest("GET", "/v1/admin/data-grants", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w = httptest.NewRecorder()
	dispatchAdmin(srv, w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		DataGrants map[string]policy.DataGrant `json:"data_grants"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.DataGrants["tracks"].Default != shared.AccessRead {
		t.Fatalf("data_grants.tracks = %+v", resp.DataGrants["tracks"])
	}
	if got := srv.config.DataGrants().Levels().Level("tracks"); got != shared.AccessRead {
		t.Fatalf("effective = %q", got)
	}
}

func TestDataGrants_BootstrapKeyAndOverlayFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	mustWriteFile(t, cfgPath, `{
		"data_grants": {
			"tracks": {"default": "read"},
			"places": {"default": "none"}
		},
		"services": [
			{"id": "spotify", "name": "Spotify", "action": "inject"},
			{"id": "google-timeline", "name": "Timeline", "action": "inject",
			 "grant_limits": {"places": "city"}}
		]
	}`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.DataGrants().Levels().Level("tracks"); got != shared.AccessRead {
		t.Fatalf("bootstrap tracks = %q", got)
	}
	if got := cfg.DataGrants().Levels().Level("places"); got != shared.AccessNone {
		t.Fatalf("bootstrap places = %q (grant_limits and provider opt-in are legacy)", got)
	}

	// Overlay file is data_grants.json with v3 keys.
	ovPath := filepath.Join(dir, "data_grants.json")
	mustWriteFile(t, ovPath, `{"version":3,"data_grants":{"tracks":{"default":"read-write"}},"service_limits":{}}`)
	if err := cfg.InitDataGrantsStore(ovPath); err != nil {
		t.Fatalf("InitDataGrantsStore: %v", err)
	}
	if got := cfg.DataGrants().Levels().Level("tracks"); got != shared.AccessFull {
		t.Fatalf("overlay data_grants win = %q", got)
	}

	// Saves write v3 keys only.
	if err := cfg.SetDataGrants(policy.DataGrants{"tracks": {Default: shared.AccessRead}}); err != nil {
		t.Fatalf("SetDataGrants: %v", err)
	}
	raw, err := os.ReadFile(ovPath)
	if err != nil {
		t.Fatal(err)
	}
	var file dataGrantsOverlayFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if file.DataGrants["tracks"].Default != shared.AccessRead {
		t.Fatalf("saved data_grants = %+v", file.DataGrants)
	}
	if file.Version != dataGrantsFileVersion {
		t.Fatalf("version = %d", file.Version)
	}
}
