package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

func TestPermissionsOverlay_TypePoliciesPrimary(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	permsPath := filepath.Join(dir, "permissions.json")
	initial := `{
		"services": [
			{
				"id": "spotify",
				"name": "Spotify",
				"action": "inject",
				"endpoint": "https://api.spotify.com/v1",
				"permissions": {"tracks": "read", "player": "none"}
			},
			{
				"id": "immich",
				"name": "Immich",
				"action": "inject",
				"endpoint": "http://immich.local/api",
				"permissions": {"places": "city"}
			}
		]
	}`
	mustWriteFile(t, cfgPath, initial)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if err := cfg.InitPermissionsStore(permsPath); err != nil {
		t.Fatalf("InitPermissionsStore: %v", err)
	}

	// Type policy is primary: tracks default full, spotify tightens to none.
	if err := cfg.SetTypePolicies(types.TypePolicies{
		"tracks": {
			Default:   types.AccessFull,
			Providers: map[string]string{"spotify": types.AccessNone},
		},
		"places": {
			Default:   types.AccessNone,
			Providers: map[string]string{"immich": types.AccessCity},
		},
	}); err != nil {
		t.Fatalf("SetTypePolicies: %v", err)
	}

	if got := cfg.EffectiveAccess("spotify", "tracks"); got != types.AccessNone {
		t.Fatalf("spotify tracks = %q, want none", got)
	}
	if got := cfg.EffectiveAccess("immich", "tracks"); got != types.AccessFull {
		t.Fatalf("immich tracks = %q, want full", got)
	}
	if got := cfg.EffectiveAccess("immich", "places"); got != types.AccessCity {
		t.Fatalf("immich places = %q, want city", got)
	}
	if got := cfg.EffectiveAccess("spotify", "places"); got != types.AccessNone {
		t.Fatalf("spotify places = %q, want none", got)
	}

	// Persisted as v2 with types as primary.
	raw, err := os.ReadFile(permsPath)
	if err != nil {
		t.Fatal(err)
	}
	var file permissionsOverlayFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if file.Version != permissionsFileVersion {
		t.Fatalf("version = %d", file.Version)
	}
	if file.Types["tracks"].Default != types.AccessFull {
		t.Fatalf("types.tracks = %+v", file.Types["tracks"])
	}

	// Reload keeps type policy and resolves the same way.
	fresh, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig reload: %v", err)
	}
	if err := fresh.InitPermissionsStore(permsPath); err != nil {
		t.Fatalf("InitPermissionsStore reload: %v", err)
	}
	if got := fresh.EffectiveAccess("spotify", "tracks"); got != types.AccessNone {
		t.Fatalf("reloaded spotify tracks = %q", got)
	}
}

func TestPermissionsOverlay_LegacyServiceMapOnlyTightens(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	permsPath := filepath.Join(dir, "permissions.json")
	mustWriteFile(t, cfgPath, `{
		"services": [
			{"id": "spotify", "name": "Spotify", "action": "inject", "endpoint": "https://api.spotify.com/v1"}
		]
	}`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if err := cfg.InitPermissionsStore(permsPath); err != nil {
		t.Fatalf("InitPermissionsStore: %v", err)
	}
	if err := cfg.SetTypePolicies(types.TypePolicies{
		"tracks": {Default: types.AccessRead},
	}); err != nil {
		t.Fatalf("SetTypePolicies: %v", err)
	}

	// Service map cannot grant above the type default.
	if err := cfg.SetServicePermissions("spotify", types.Permissions{"tracks": types.AccessFull}); err != nil {
		t.Fatalf("SetServicePermissions: %v", err)
	}
	if got := cfg.EffectiveAccess("spotify", "tracks"); got != types.AccessRead {
		t.Fatalf("legacy cannot loosen: %q", got)
	}

	// Service map can tighten.
	if err := cfg.SetServicePermissions("spotify", types.Permissions{"tracks": types.AccessNone}); err != nil {
		t.Fatalf("SetServicePermissions: %v", err)
	}
	if got := cfg.EffectiveAccess("spotify", "tracks"); got != types.AccessNone {
		t.Fatalf("legacy tightens: %q", got)
	}
}

func TestPermissionsOverlay_V1ServicesRemainAuthorityWithoutTypePolicy(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	permsPath := filepath.Join(dir, "permissions.json")
	mustWriteFile(t, cfgPath, `{
		"services": [
			{"id": "google-timeline", "name": "Timeline", "action": "inject"}
		]
	}`)
	// v1 overlay: services only.
	mustWriteFile(t, permsPath, `{"version":1,"services":{"google-timeline":{"places":"city","tracks":"read"}}}`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if err := cfg.InitPermissionsStore(permsPath); err != nil {
		t.Fatalf("InitPermissionsStore: %v", err)
	}
	if got := cfg.EffectiveAccess("google-timeline", "places"); got != types.AccessCity {
		t.Fatalf("v1 places = %q", got)
	}
	if got := cfg.EffectiveAccess("google-timeline", "tracks"); got != types.AccessRead {
		t.Fatalf("v1 tracks = %q", got)
	}
}

func TestLoadConfig_TypePoliciesBootstrap(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	mustWriteFile(t, cfgPath, `{
		"type_policies": {
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
	if got := cfg.EffectiveAccess("spotify", "tracks"); got != types.AccessNone {
		t.Fatalf("bootstrap provider tighten = %q", got)
	}
	if got := cfg.EffectiveAccess("google-timeline", "places"); got != types.AccessCity {
		t.Fatalf("bootstrap places opt-in = %q", got)
	}

	// Overlay types replace bootstrap when present.
	permsPath := filepath.Join(dir, "permissions.json")
	mustWriteFile(t, permsPath, `{"version":2,"types":{"tracks":{"default":"read-write"}},"services":{}}`)
	if err := cfg.InitPermissionsStore(permsPath); err != nil {
		t.Fatalf("InitPermissionsStore: %v", err)
	}
	if got := cfg.EffectiveAccess("spotify", "tracks"); got != types.AccessFull {
		t.Fatalf("overlay types win = %q", got)
	}
}

func TestHandleAdminTypePolicies(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "aegis.json")
	mustWriteFile(t, cfgPath, `{"services":[{"id":"spotify","name":"Spotify","action":"inject"}]}`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	if err := srv.config.InitPermissionsStore(filepath.Join(dir, "permissions.json")); err != nil {
		t.Fatalf("InitPermissionsStore: %v", err)
	}

	body := `{"types":{"tracks":{"default":"read","providers":{"spotify":"none"}}}}`
	req := httptest.NewRequest("PUT", "/v1/admin/type-policies", strings.NewReader(body))
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.handleAdminRoutes(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest("GET", "/v1/admin/type-policies", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w = httptest.NewRecorder()
	srv.handleAdminRoutes(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Types map[string]types.TypePolicy `json:"types"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Types["tracks"].Default != types.AccessRead {
		t.Fatalf("types.tracks = %+v", resp.Types["tracks"])
	}
	if got := srv.config.EffectiveAccess("spotify", "tracks"); got != types.AccessNone {
		t.Fatalf("effective = %q", got)
	}
}
