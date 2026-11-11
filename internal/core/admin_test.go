package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/admin"
	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/mail"
	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/core/tokens"
	"github.com/fudomunro/aegis/internal/shared"
)

func TestCache_SnapshotStatsClear(t *testing.T) {
	c := shared.NewCache()
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
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest("GET", "/v1/admin", nil)
	w := httptest.NewRecorder()

	srv.admin().HandleAdmin(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Aegis — Admin") {
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
	if strings.Contains(body, "Aegis Admin") || strings.Contains(body, "status-strip") {
		t.Fatal("expected top-bar stats/title removed in issue #69 cleanup")
	}
	if !strings.Contains(body, "PAGE_SIZE") || !strings.Contains(body, "paginate(") {
		t.Fatal("expected client-side pagination helpers in admin JS")
	}
}

// Issue #150: friendly, durable SPA URLs. Refresh on any section path must
// still serve the admin shell so the client can restore that section.
func TestHandleAdmin_FriendlyShellPaths(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	for _, path := range []string{
		"/",
		"/home",
		"/library",
		"/services",
		"/advanced",
		"/library/",
		"/v1/admin",
		"/v1/admin/",
	} {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		srv.admin().HandleAdmin(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", path, w.Code)
		}
		if !strings.Contains(w.Body.String(), `id="tab-home"`) {
			t.Fatalf("GET %s missing Home tab shell", path)
		}
	}

	for _, path := range []string{"/nope", "/v1/admin/nope", "/v1/library"} {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		srv.admin().HandleAdmin(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want 404", path, w.Code)
		}
	}

	req := httptest.NewRequest("POST", "/library", nil)
	w := httptest.NewRecorder()
	srv.admin().HandleAdmin(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /library status = %d, want 405", w.Code)
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
			},
			{ID: "tidal", Name: "Tidal", Action: "block"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Tokens["spotify"] = &tokens.OAuthToken{
		AccessToken:  "access-token-secret",
		RefreshToken: "refresh-token-secret",
		Expiry:       time.Now().Add(time.Hour),
		Scopes:       []string{"user-library-read"},
	}
	srv.cache.Set("library:playlists", "payload", time.Minute)

	req := httptest.NewRequest("GET", "/v1/admin/summary", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.admin().HandleAdminSummary)(w, req)

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

	var summary admin.AdminSummary
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
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")

	// Every admin route requires admin credentials (issues #98, #181).
	for _, p := range []struct{ method, path string }{
		{"GET", "/v1/admin/"},
		{"GET", "/v1/admin/summary"},
		{"POST", "/v1/admin/cache/clear"},
		{"GET", "/v1/admin/library/playlists"},
	} {
		req := httptest.NewRequest(p.method, p.path, nil)
		w := httptest.NewRecorder()
		dispatchAdmin(srv, w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s without auth = %d, want 401", p.method, p.path, w.Code)
		}
	}

	// With admin credentials the HTML shell serves.
	req := httptest.NewRequest("GET", "/v1/admin/", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	dispatchAdmin(srv, w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("html shell with auth = %d, want 200", w.Code)
	}

	// Unknown path with auth.
	req = httptest.NewRequest("GET", "/v1/admin/nope", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w = httptest.NewRecorder()
	dispatchAdmin(srv, w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown status = %d, want 404", w.Code)
	}
}

func TestHandleAdminCacheClear(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.cache.Set("k", "v", time.Minute)

	req := httptest.NewRequest("POST", "/v1/admin/cache/clear", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.admin().HandleAdminCacheClear)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	live, total, _ := srv.cache.Stats()
	if live != 0 || total != 0 {
		t.Fatalf("cache not cleared: live=%d total=%d", live, total)
	}
}

func TestHandleAdminMailStatusAndSync(t *testing.T) {
	a := openTestMailArchive(t)
	if err := a.Upsert(sampleMailRecord("m1", "Board notes", "Please review the board notes.")); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	srv := mailTestServer(t, a)
	// A messages=none data grant must not block the operator admin panel.
	srv.config.typePolicies = policy.DataGrants{"messages": {Default: shared.AccessNone}}

	req := httptest.NewRequest("GET", "/v1/admin/mail/status", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.admin().HandleAdminMailStatus)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var st mail.Status
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("status json: %v", err)
	}
	if st.MessageCount != 1 {
		t.Fatalf("status = %+v", st)
	}

	// Route wiring used by the admin UI.
	req = httptest.NewRequest("GET", "/v1/admin/mail/status", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w = httptest.NewRecorder()
	dispatchAdmin(srv, w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("admin route status = %d body=%s", w.Code, w.Body.String())
	}

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/users/me/messages/") {
			w.Write([]byte(gmailPayloadJSON("m9")))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"messages": []map[string]string{{"id": "m9"}},
		})
	}))
	t.Cleanup(up.Close)
	srv.config.updateService(connectors.GoogleGmailServiceID, func(svc *Service) { svc.Endpoint = up.URL })
	srv.tokens.Save(connectors.GoogleGmailServiceID, &tokens.OAuthToken{AccessToken: "access-token", Expiry: time.Now().Add(time.Hour)})

	req = httptest.NewRequest("POST", "/v1/admin/mail/sync", strings.NewReader(`{"max_messages":5,"reset_cursor":true}`))
	req.Header.Set("X-Api-Key", "test-key")
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	dispatchAdmin(srv, w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("sync = %d body=%s", w.Code, w.Body.String())
	}
	var res shared.MailIngestResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("sync json: %v", err)
	}
	if res.Stored != 1 {
		t.Fatalf("res = %+v", res)
	}
}

func TestHandleAdminMailDisabled(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest("GET", "/v1/admin/mail/status", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.admin().HandleAdminMailStatus)(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("disabled status = %d body=%s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest("POST", "/v1/admin/mail/sync", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w = httptest.NewRecorder()
	dispatchAdmin(srv, w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("disabled sync = %d", w.Code)
	}
}

func TestAdminSummary_SupportsPhotoPicker(t *testing.T) {
	cfg := &Config{Services: []Service{
		{ID: "google-photos", Name: "Google Photos", Action: "inject"},
		{ID: "spotify", Name: "Spotify", Action: "inject"},
	}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors["google-photos"] = connectors.NewGooglePhotosConnector(srv)
	srv.connectors["spotify"] = connectors.NewSpotifyConnector(srv)

	req := httptest.NewRequest("GET", "/v1/admin/summary", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.admin().HandleAdminSummary)(w, req)

	var summary admin.AdminSummary
	if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	byID := map[string]admin.AdminService{}
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
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest("GET", "/v1/admin", nil)
	w := httptest.NewRecorder()
	srv.admin().HandleAdmin(w, req)
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
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest("GET", "/v1/admin", nil)
	w := httptest.NewRecorder()
	srv.admin().HandleAdmin(w, req)
	body := w.Body.String()
	// Primary: name + data flow + connection status. Details stay secondary.
	for _, want := range []string{
		"svc-title-row",
		"svc-name",
		"svc-flow",
		"svc-connection",
		"svc-details",
		"svc-advanced",
		"data-service-advanced",
		"openAdvanced",
		"<summary>Advanced</summary>",
		`id="agent-data-access-help"`,
		"Data grants by kind",
		`id="data-grant-panel"`,
		"typeOtherAccessorsHTML",
		"type-access-row",
		"/v1/admin/permissions",
		"Saves to",
		"serviceConnectionInline",
		"serviceFlowRows",
		"Pick photos",
		"supports_photo_picker",
		"Agents → Aegis → service",
		"unified library",
		"has_event_provider",
		"has_message_provider",
		"!status.connected",
		// Connection cluster stays on one line on the title row
		"flex-wrap: nowrap",
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
	}, "https://aegis.example.com")

	req := httptest.NewRequest("GET", "/v1/admin/summary", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.admin().HandleAdminSummary)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"has_event_provider"`) ||
		!strings.Contains(w.Body.String(), `"has_message_provider"`) {
		t.Fatalf("summary missing library fan-in flags for services flow block")
	}
	var summary admin.AdminSummary
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
