package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCache_SnapshotStatsClear(t *testing.T) {
	c := NewCache()
	c.Set("library:playlists", `{"playlists":[]}`, time.Minute)
	c.Set("spotify:/me", "x", time.Minute)
	c.Set("expired", "old", -time.Second)

	live, total, bytes := c.Stats()
	if live != 2 || total != 3 {
		t.Fatalf("stats live=%d total=%d, want live=2 total=3", live, total)
	}
	if bytes == 0 {
		t.Fatal("expected non-zero live bytes")
	}

	items := c.Snapshot()
	if len(items) != 3 {
		t.Fatalf("snapshot len=%d, want 3", len(items))
	}
	// Sorted by key.
	if items[0].Key != "expired" || items[1].Key != "library:playlists" || items[2].Key != "spotify:/me" {
		t.Fatalf("unexpected key order: %s, %s, %s", items[0].Key, items[1].Key, items[2].Key)
	}
	if !items[0].Expired {
		t.Fatal("expected expired entry")
	}
	if items[1].Expired {
		t.Fatal("expected live library entry")
	}

	c.Clear()
	live, total, _ = c.Stats()
	if live != 0 || total != 0 {
		t.Fatalf("after clear live=%d total=%d, want 0/0", live, total)
	}
}

func TestHandleAdmin_HTMLShell(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	req := httptest.NewRequest("GET", "/v1/admin", nil)
	w := httptest.NewRecorder()

	srv.HandleAdmin(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Guardian — Admin") {
		t.Fatal("expected admin page title")
	}
	if !strings.Contains(body, "/v1/admin/summary") {
		t.Fatal("expected summary API reference in page")
	}
	if !strings.Contains(body, "/v1/admin/library/playlists") {
		t.Fatal("expected admin Library tab to use /v1/admin/library/* paths")
	}
	if !strings.Contains(body, `id="library-pager"`) || !strings.Contains(body, `id="cache-pager"`) {
		t.Fatal("expected library and cache pager containers")
	}
	if !strings.Contains(body, `id="tab-advanced"`) || !strings.Contains(body, `id="panel-advanced"`) {
		t.Fatal("expected Advanced tab consolidating cache/runtime/map details")
	}
	if strings.Contains(body, "Guardian Admin") || strings.Contains(body, "status-strip") {
		t.Fatal("expected top-bar stats/title removed in issue #69 cleanup")
	}
	if !strings.Contains(body, "PAGE_SIZE") || !strings.Contains(body, "paginate(") {
		t.Fatal("expected client-side pagination helpers in admin JS")
	}
}

func TestHandleAdminSummary_RedactsSecrets(t *testing.T) {
	cfg := &Config{
		Connections: map[string]Connection{
			"spotify": {
				Name:         "Spotify",
				ClientID:     "client-id-value",
				ClientSecret: "super-secret-value",
				AuthURL:      "https://accounts.spotify.com/authorize",
				TokenURL:     "https://accounts.spotify.com/api/token",
				Scopes:       []string{"user-library-read"},
			},
		},
		Services: []Service{
			{
				ID:          "spotify",
				Name:        "Spotify",
				Action:      "inject",
				Endpoint:    "https://api.spotify.com/v1",
				CacheTTL:    Duration{Duration: 10 * time.Minute},
				Connection:  "spotify",
				OAuthScopes: []string{"user-library-read"},
				Permissions: Permissions{
					"default": AccessRead,
					"player":  AccessNone,
				},
			},
			{ID: "tidal", Name: "Tidal", Action: "block"},
		},
	}
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.tokens.Tokens["spotify"] = &OAuthToken{
		AccessToken:  "access-token-secret",
		RefreshToken: "refresh-token-secret",
		Expiry:       time.Now().Add(time.Hour),
		Scopes:       []string{"user-library-read"},
	}
	srv.cache.Set("library:playlists", "payload", time.Minute)

	req := httptest.NewRequest("GET", "/v1/admin/summary", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminSummary)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}

	raw := w.Body.String()
	for _, secret := range []string{
		"super-secret-value",
		"access-token-secret",
		"refresh-token-secret",
		"client-id-value",
	} {
		if strings.Contains(raw, secret) {
			t.Fatalf("response leaked secret %q", secret)
		}
	}

	var summary AdminSummary
	if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil {
		t.Fatalf("parse summary: %v", err)
	}
	if len(summary.Services) != 2 {
		t.Fatalf("services = %d, want 2", len(summary.Services))
	}
	spotify := summary.Services[0]
	if !spotify.HasOAuth || !spotify.HasSecret || !spotify.HasClientID {
		t.Fatal("expected oauth flags set without exposing values")
	}
	if spotify.Permissions["player"] != AccessNone || spotify.Permissions["default"] != AccessRead {
		t.Fatalf("permissions = %+v", spotify.Permissions)
	}
	if spotify.Token == nil || !spotify.Token.HasToken {
		t.Fatal("expected token status attached")
	}
	if summary.Cache.Live != 1 {
		t.Fatalf("cache.live = %d, want 1", summary.Cache.Live)
	}
	if summary.Runtime.AuthMode != AuthModeAPIKey {
		t.Fatalf("auth mode = %q", summary.Runtime.AuthMode)
	}
}

func TestAdminRoutes_AuthRequired(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")

	// HTML shell is public.
	req := httptest.NewRequest("GET", "/v1/admin/", nil)
	w := httptest.NewRecorder()
	srv.handleAdminRoutes(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("html shell status = %d, want 200", w.Code)
	}

	// Summary requires auth.
	req = httptest.NewRequest("GET", "/v1/admin/summary", nil)
	w = httptest.NewRecorder()
	srv.handleAdminRoutes(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("summary status = %d, want 401", w.Code)
	}

	// Clear requires auth.
	req = httptest.NewRequest("POST", "/v1/admin/cache/clear", nil)
	w = httptest.NewRecorder()
	srv.handleAdminRoutes(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("clear status = %d, want 401", w.Code)
	}

	// Library preview requires auth.
	req = httptest.NewRequest("GET", "/v1/admin/library/playlists", nil)
	w = httptest.NewRecorder()
	srv.handleAdminRoutes(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("library status = %d, want 401", w.Code)
	}

	// Unknown path.
	req = httptest.NewRequest("GET", "/v1/admin/nope", nil)
	w = httptest.NewRecorder()
	srv.handleAdminRoutes(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown status = %d, want 404", w.Code)
	}
}

func TestHandleAdminCacheClear(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	srv.cache.Set("k", "v", time.Minute)

	req := httptest.NewRequest("POST", "/v1/admin/cache/clear", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminCacheClear)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	live, total, _ := srv.cache.Stats()
	if live != 0 || total != 0 {
		t.Fatalf("cache not cleared: live=%d total=%d", live, total)
	}
}

func TestConfig_SetServicePermissions_Persists(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/guardian.json"
	initial := `{
		"services": [
			{
				"id": "spotify",
				"name": "Spotify",
				"action": "inject",
				"endpoint": "https://api.spotify.com/v1",
				"cache_ttl": "10m"
			}
		]
	}`
	if err := writeFile(path, initial); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if err := cfg.SetServicePermissions("spotify", Permissions{
		"default": AccessRead,
		"player":  AccessNone,
	}); err != nil {
		t.Fatalf("SetServicePermissions: %v", err)
	}

	// In-memory updated.
	if cfg.GetService("spotify").Permissions.For("player") != AccessNone {
		t.Fatal("in-memory player permission not applied")
	}

	// Reloaded from disk.
	fresh, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	svc := fresh.GetService("spotify")
	if svc == nil || svc.Permissions.For("player") != AccessNone || svc.Permissions.For("tracks") != AccessRead {
		t.Fatalf("disk permissions = %+v", svc)
	}

	// Clear restores unrestricted.
	if err := cfg.SetServicePermissions("spotify", nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if cfg.GetService("spotify").Permissions != nil {
		t.Fatal("expected nil permissions after clear")
	}
	fresh, err = LoadConfig(path)
	if err != nil {
		t.Fatalf("reload after clear: %v", err)
	}
	if fresh.GetService("spotify").Permissions != nil {
		t.Fatal("expected nil permissions on disk after clear")
	}

	// Unknown service / invalid level.
	if err := cfg.SetServicePermissions("nope", Permissions{"default": AccessRead}); err == nil {
		t.Fatal("expected error for unknown service")
	}
	if err := cfg.SetServicePermissions("spotify", Permissions{"tracks": "maybe"}); err == nil {
		t.Fatal("expected error for invalid level")
	}
}

func TestHandleAdminServicePermissions_PutAndGet(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/guardian.json"
	initial := `{"services":[{"id":"spotify","name":"Spotify","action":"inject"}]}`
	if err := writeFile(path, initial); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.connectors["spotify"] = NewSpotifyConnector(srv)
	srv.cache.Set("stale", "x", time.Minute)

	body := `{"permissions":{"default":"read","player":"none","playlists":"read-write"}}`
	req := httptest.NewRequest("PUT", "/v1/admin/services/spotify/permissions", strings.NewReader(body))
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.handleAdminRoutes(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if cfg.GetService("spotify").Permissions.For("player") != AccessNone {
		t.Fatal("permissions not applied via admin route")
	}
	// Mirror adapters are not agent "Editable" — playlists write is clamped on save.
	if got := cfg.GetService("spotify").Permissions.For("playlists"); got != AccessRead {
		t.Fatalf("playlists permission = %q, want read (clamped; not agent-writable)", got)
	}
	// Search is read-only on the connector — write levels are clamped to read.
	body = `{"permissions":{"search":"read-write","player":"none"}}`
	req = httptest.NewRequest("PUT", "/v1/admin/services/spotify/permissions", strings.NewReader(body))
	req.Header.Set("X-Api-Key", "test-key")
	w = httptest.NewRecorder()
	srv.handleAdminRoutes(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("clamp put status = %d, body=%s", w.Code, w.Body.String())
	}
	if got := cfg.GetService("spotify").Permissions.For("search"); got != AccessRead {
		t.Fatalf("search permission = %q, want read (clamped; no agent write path)", got)
	}
	if live, _, _ := srv.cache.Stats(); live != 0 {
		t.Fatalf("cache should be cleared after permission save, live=%d", live)
	}

	// GET returns current map + available/writable types.
	req = httptest.NewRequest("GET", "/v1/admin/services/spotify/permissions", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w = httptest.NewRecorder()
	srv.handleAdminRoutes(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get status = %d", w.Code)
	}
	var got struct {
		ServiceID          string            `json:"service_id"`
		Permissions        map[string]string `json:"permissions"`
		AvailableDataTypes []string          `json:"available_data_types"`
		WritableDataTypes  []string          `json:"writable_data_types"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ServiceID != "spotify" || got.Permissions["player"] != "none" {
		t.Fatalf("get = %+v", got)
	}
	if !stringSliceContains(got.AvailableDataTypes, "playlists") {
		t.Fatalf("available types = %v", got.AvailableDataTypes)
	}
	// Spotify has no agent-meaningful write ops — form must not offer Editable.
	if len(got.WritableDataTypes) != 0 {
		t.Fatalf("spotify writable types should be empty (mirror is not Editable), got %v", got.WritableDataTypes)
	}

	// Invalid level rejected.
	req = httptest.NewRequest("PUT", "/v1/admin/services/spotify/permissions", strings.NewReader(`{"permissions":{"tracks":"maybe"}}`))
	req.Header.Set("X-Api-Key", "test-key")
	w = httptest.NewRecorder()
	srv.handleAdminRoutes(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid level status = %d, want 400", w.Code)
	}

	// Unknown service.
	req = httptest.NewRequest("PUT", "/v1/admin/services/nope/permissions", strings.NewReader(`{"permissions":{}}`))
	req.Header.Set("X-Api-Key", "test-key")
	w = httptest.NewRecorder()
	srv.handleAdminRoutes(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown service status = %d, want 404", w.Code)
	}
}

func TestAdminSummary_IncludesAvailableDataTypes(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.connectors["spotify"] = NewSpotifyConnector(srv)

	req := httptest.NewRequest("GET", "/v1/admin/summary", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminSummary)(w, req)

	var summary AdminSummary
	if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if len(summary.Services) != 1 {
		t.Fatalf("services = %d", len(summary.Services))
	}
	if !stringSliceContains(summary.Services[0].AvailableDataTypes, "player") {
		t.Fatalf("available types = %v", summary.Services[0].AvailableDataTypes)
	}
}

func TestAdminSummary_SupportsPhotoPicker(t *testing.T) {
	cfg := &Config{Services: []Service{
		{ID: "google-photos", Name: "Google Photos", Action: "inject"},
		{ID: "spotify", Name: "Spotify", Action: "inject"},
	}}
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.connectors["google-photos"] = NewGooglePhotosConnector(srv)
	srv.connectors["spotify"] = NewSpotifyConnector(srv)

	req := httptest.NewRequest("GET", "/v1/admin/summary", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminSummary)(w, req)

	var summary AdminSummary
	if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	byID := map[string]AdminService{}
	for _, svc := range summary.Services {
		byID[svc.ID] = svc
	}
	if !byID["google-photos"].SupportsPhotoPicker {
		t.Fatal("expected google-photos supports_photo_picker")
	}
	if byID["spotify"].SupportsPhotoPicker {
		t.Fatal("spotify should not advertise photo picker")
	}
}

func TestHandleAdmin_HTMLIncludesPhotoPick(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	req := httptest.NewRequest("GET", "/v1/admin", nil)
	w := httptest.NewRecorder()
	srv.HandleAdmin(w, req)
	body := w.Body.String()
	if !strings.Contains(body, "photo-pick") {
		t.Fatal("expected photo-pick control in admin HTML")
	}
	if !strings.Contains(body, "photo-pick-status-") {
		t.Fatal("expected per-card photo pick status element")
	}
	if !strings.Contains(body, "picker/sessions") {
		t.Fatal("expected picker session path in admin JS")
	}
	if !strings.Contains(body, "Enable Photos Picker API") {
		t.Fatal("expected enable-API link label in admin JS")
	}
}

func TestHandleAdminHTML_ServiceCardHierarchy(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	req := httptest.NewRequest("GET", "/v1/admin", nil)
	w := httptest.NewRecorder()
	srv.HandleAdmin(w, req)
	body := w.Body.String()
	// Primary: name + data flow + connection status. Details stay secondary.
	for _, want := range []string{
		"svc-primary",
		"svc-name",
		"svc-flow",
		"svc-status",
		"svc-details",
		"svc-actions-top",
		"serviceCardActions",
		"serviceFlowRows",
		"Agents → Guardian → service",
		"unified library",
		"has_event_provider",
		"has_message_provider",
		"!status.connected",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("admin HTML missing service card marker %q", want)
		}
	}
}

func TestHandleAdminSummary_ProviderJSONFlags(t *testing.T) {
	// Ensure library fan-in flags used by the Services flow block stay in the payload.
	srv := testServerWithBase(&Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject", Connection: "spotify"},
		},
		Connections: map[string]Connection{
			"spotify": {Name: "Spotify", ClientID: "cid", ClientSecret: "sec", Scopes: []string{"user-library-read"}},
		},
	}, "https://guardian.example.com")

	req := httptest.NewRequest("GET", "/v1/admin/summary", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminSummary)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"has_event_provider"`) ||
		!strings.Contains(w.Body.String(), `"has_message_provider"`) {
		t.Fatalf("summary missing library fan-in flags for services flow block")
	}
	var summary AdminSummary
	if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(summary.Services) != 1 || summary.Services[0].ID != "spotify" {
		t.Fatalf("services = %+v", summary.Services)
	}
	if !summary.Services[0].HasOAuth || summary.Services[0].Action != "inject" {
		t.Fatalf("spotify flags = %+v", summary.Services[0])
	}
}
