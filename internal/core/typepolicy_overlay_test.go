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
		"tracks": {
			Default:   policy.AccessFull,
			Providers: map[string]string{"spotify": policy.AccessNone},
		},
		"places": {
			Default:   policy.AccessNone,
			Providers: map[string]string{"immich": policy.AccessCity},
		},
	}); err != nil {
		t.Fatalf("SetDataGrants: %v", err)
	}

	if got := cfg.EffectiveAccess("spotify", "tracks"); got != policy.AccessNone {
		t.Fatalf("spotify tracks = %q, want none", got)
	}
	if got := cfg.EffectiveAccess("immich", "tracks"); got != policy.AccessFull {
		t.Fatalf("immich tracks = %q, want full", got)
	}
	if got := cfg.EffectiveAccess("immich", "places"); got != policy.AccessCity {
		t.Fatalf("immich places = %q, want city", got)
	}
	if got := cfg.EffectiveAccess("spotify", "places"); got != policy.AccessNone {
		t.Fatalf("spotify places = %q, want none", got)
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
	if file.DataGrants["tracks"].Default != policy.AccessFull {
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
	if got := fresh.EffectiveAccess("spotify", "tracks"); got != policy.AccessNone {
		t.Fatalf("reloaded spotify tracks = %q", got)
	}
}

func TestDataGrantsOverlay_ServiceLimitsOnlyTighten(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	permsPath := filepath.Join(dir, "data_grants.json")
	mustWriteFile(t, cfgPath, `{
		"services": [
			{"id": "spotify", "name": "Spotify", "action": "inject", "endpoint": "https://api.spotify.com/v1"}
		]
	}`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if err := cfg.InitDataGrantsStore(permsPath); err != nil {
		t.Fatalf("InitDataGrantsStore: %v", err)
	}
	if err := cfg.SetDataGrants(policy.DataGrants{
		"tracks": {Default: policy.AccessRead},
	}); err != nil {
		t.Fatalf("SetDataGrants: %v", err)
	}

	// Service map cannot grant above the type default.
	if err := cfg.SetServicePermissions("spotify", policy.Permissions{"tracks": policy.AccessFull}); err != nil {
		t.Fatalf("SetServicePermissions: %v", err)
	}
	if got := cfg.EffectiveAccess("spotify", "tracks"); got != policy.AccessRead {
		t.Fatalf("legacy cannot loosen: %q", got)
	}

	// Service map can tighten.
	if err := cfg.SetServicePermissions("spotify", policy.Permissions{"tracks": policy.AccessNone}); err != nil {
		t.Fatalf("SetServicePermissions: %v", err)
	}
	if got := cfg.EffectiveAccess("spotify", "tracks"); got != policy.AccessNone {
		t.Fatalf("legacy tightens: %q", got)
	}
}

func TestLoadConfig_DataGrantsBootstrap(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	mustWriteFile(t, cfgPath, `{
		"data_grants": {
			"tracks": {"default": "read", "providers": {"spotify": "none"}},
			"places": {"default": "none", "providers": {"google-timeline": "city"}}
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
	if got := cfg.EffectiveAccess("spotify", "tracks"); got != policy.AccessNone {
		t.Fatalf("bootstrap provider tighten = %q", got)
	}
	if got := cfg.EffectiveAccess("google-timeline", "places"); got != policy.AccessCity {
		t.Fatalf("bootstrap places opt-in = %q", got)
	}

	// Overlay types replace bootstrap when present.
	permsPath := filepath.Join(dir, "data_grants.json")
	mustWriteFile(t, permsPath, `{"version":3,"data_grants":{"tracks":{"default":"read-write"}},"service_limits":{}}`)
	if err := cfg.InitDataGrantsStore(permsPath); err != nil {
		t.Fatalf("InitDataGrantsStore: %v", err)
	}
	if got := cfg.EffectiveAccess("spotify", "tracks"); got != policy.AccessFull {
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

	body := `{"data_grants":{"tracks":{"default":"read","providers":{"spotify":"none"}}}}`
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
	if resp.DataGrants["tracks"].Default != policy.AccessRead {
		t.Fatalf("data_grants.tracks = %+v", resp.DataGrants["tracks"])
	}
	if got := srv.config.EffectiveAccess("spotify", "tracks"); got != policy.AccessNone {
		t.Fatalf("effective = %q", got)
	}
}

func TestDataGrants_BootstrapKeyAndOverlayFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	mustWriteFile(t, cfgPath, `{
		"data_grants": {
			"tracks": {"default": "read", "providers": {"spotify": "none"}},
			"places": {"default": "none", "providers": {"google-timeline": "city"}}
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
	if got := cfg.EffectiveAccess("spotify", "tracks"); got != policy.AccessNone {
		t.Fatalf("bootstrap data_grants provider tighten = %q", got)
	}
	if got := cfg.EffectiveAccess("google-timeline", "places"); got != policy.AccessCity {
		t.Fatalf("bootstrap grant_limits places = %q", got)
	}

	// Overlay file is data_grants.json with v3 keys.
	ovPath := filepath.Join(dir, "data_grants.json")
	mustWriteFile(t, ovPath, `{"version":3,"data_grants":{"tracks":{"default":"read-write"}},"service_limits":{}}`)
	if err := cfg.InitDataGrantsStore(ovPath); err != nil {
		t.Fatalf("InitDataGrantsStore: %v", err)
	}
	if got := cfg.EffectiveAccess("spotify", "tracks"); got != policy.AccessFull {
		t.Fatalf("overlay data_grants win = %q", got)
	}

	// Saves write v3 keys only.
	if err := cfg.SetDataGrants(policy.DataGrants{"tracks": {Default: policy.AccessRead}}); err != nil {
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
	if file.DataGrants["tracks"].Default != policy.AccessRead {
		t.Fatalf("saved data_grants = %+v", file.DataGrants)
	}
	if file.Version != dataGrantsFileVersion {
		t.Fatalf("version = %d", file.Version)
	}
}
