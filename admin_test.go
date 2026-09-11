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
	if !strings.Contains(body, "Guardian Admin") {
		t.Fatal("expected admin page title")
	}
	if !strings.Contains(body, "/v1/admin/summary") {
		t.Fatal("expected summary API reference in page")
	}
}

func TestHandleAdminSummary_RedactsSecrets(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{
				ID:       "spotify",
				Name:     "Spotify",
				Action:   "inject",
				Endpoint: "https://api.spotify.com/v1",
				CacheTTL: Duration{Duration: 10 * time.Minute},
				Permissions: Permissions{
					"default": AccessRead,
					"player":  AccessNone,
				},
				OAuth: &OAuthConfig{
					ClientID:     "client-id-value",
					ClientSecret: "super-secret-value",
					Scopes:       []string{"user-library-read"},
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
