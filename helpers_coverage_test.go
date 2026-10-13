package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUnionScopes(t *testing.T) {
	got := unionScopes([]string{"a", "b", "", "a"}, []string{"b", "c", ""})
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("unionScopes = %v", got)
	}
	if unionScopes(nil, nil) != nil && len(unionScopes(nil, nil)) != 0 {
		t.Fatalf("empty union = %v", unionScopes(nil, nil))
	}
}

func TestLoginScopes(t *testing.T) {
	cfg := &Config{
		Connections: map[string]Connection{
			"google": {Name: "Google", Scopes: []string{"email", "photos"}},
		},
		Services: []Service{
			{ID: "youtube-music", Connection: "google", OAuthScopes: []string{"yt.readonly"}},
			{ID: "google-photos", Connection: "google", OAuthScopes: []string{"photos.picker"}},
			{ID: "spotify", OAuthScopes: []string{"user-library-read", "playlist-read"}},
		},
	}

	if got := cfg.loginScopes("google"); len(got) != 2 || got[0] != "email" || got[1] != "photos" {
		t.Fatalf("declared connection scopes = %v", got)
	}

	// Surface union when connection has no declared scopes.
	cfg.Connections["google2"] = Connection{Name: "G2"}
	cfg.Services = append(cfg.Services,
		Service{ID: "g-photos", Connection: "google2", OAuthScopes: []string{"photos"}},
		Service{ID: "g-contacts", Connection: "google2", OAuthScopes: []string{"contacts", "photos"}},
	)
	got := cfg.loginScopes("google2")
	if len(got) != 2 || got[0] != "photos" || got[1] != "contacts" {
		t.Fatalf("union scopes = %v", got)
	}

	// Service without connection uses service ID as connection key.
	if got := cfg.loginScopes("spotify"); len(got) != 2 {
		t.Fatalf("spotify scopes = %v", got)
	}
}

func TestGetConnection(t *testing.T) {
	cfg := &Config{}
	if cfg.GetConnection("x") != nil {
		t.Fatal("nil connections map should return nil")
	}
	cfg.Connections = map[string]Connection{
		"google": {Name: "Google", ClientID: "cid"},
	}
	conn := cfg.GetConnection("google")
	if conn == nil || conn.ClientID != "cid" || conn.Name != "Google" {
		t.Fatalf("conn = %+v", conn)
	}
	if cfg.GetConnection("missing") != nil {
		t.Fatal("missing connection should be nil")
	}
}

func TestLibraryRouteHandlers_Registry(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	routes := srv.libraryRouteHandlers()
	want := []string{
		"/v1/library/playlists",
		"/v1/library/tracks",
		"/v1/library/albums",
		"/v1/library/artists",
		"/v1/library/contacts",
		"/v1/library/photos",
		"/v1/library/events",
		"/v1/library/freebusy",
		"/v1/library/messages",
		"/v1/library/transactions",
		"/v1/library/places",
		"/v1/library/copies",
		"/v1/library/review",
		"/v1/library/links",
		"/v1/library/links/",
	}
	if len(routes) != len(want) {
		t.Fatalf("routes = %v", routes)
	}
	for _, path := range want {
		if routes[path] == nil {
			t.Fatalf("missing handler for %s", path)
		}
	}
}

func TestTokenDisplayName(t *testing.T) {
	srv := testServerWithBase(&Config{
		Connections: map[string]Connection{
			"google": {Name: "Google Workspace"},
		},
		Services: []Service{
			{ID: "spotify", Name: "Spotify"},
		},
	}, "https://aegis.example.com")

	if got := srv.tokenDisplayName("google"); got != "Google Workspace" {
		t.Fatalf("connection name = %q", got)
	}
	if got := srv.tokenDisplayName("spotify"); got != "Spotify" {
		t.Fatalf("service name = %q", got)
	}
	if got := srv.tokenDisplayName("youtube-music"); got != "Youtube Music" {
		// Title-case each dash-separated token: "youtube" → "Youtube", "music" → "Music"
		t.Fatalf("fallback name = %q", got)
	}
	if got := srv.tokenDisplayName(""); got != "connection" {
		t.Fatalf("empty name = %q", got)
	}
}

func TestGetValidToken_ScopeAndExpiry(t *testing.T) {
	cfg := &Config{Services: []Service{{
		ID:          "spotify",
		OAuthScopes: []string{"user-library-read", "playlist-modify"},
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	logger := slog.Default()

	if srv.getValidToken("spotify", logger) != "" {
		t.Fatal("no token should be empty")
	}

	// Missing required scope.
	srv.tokens.Save("spotify", &OAuthToken{
		AccessToken: "scoped",
		Expiry:      time.Now().Add(time.Hour),
		Scopes:      []string{"user-library-read"},
	})
	if srv.getValidToken("spotify", logger) != "" {
		t.Fatal("missing scope should be empty")
	}

	// Valid token with required scopes.
	srv.tokens.Save("spotify", &OAuthToken{
		AccessToken: "good",
		Expiry:      time.Now().Add(time.Hour),
		Scopes:      []string{"user-library-read", "playlist-modify"},
	})
	if got := srv.getValidToken("spotify", logger); got != "good" {
		t.Fatalf("token = %q", got)
	}

	// Expired without refresh.
	srv.tokens.Save("spotify", &OAuthToken{
		AccessToken: "stale",
		Expiry:      time.Now().Add(-time.Hour),
		Scopes:      []string{"user-library-read", "playlist-modify"},
	})
	if srv.getValidToken("spotify", logger) != "" {
		t.Fatal("expired without refresh should be empty")
	}

	// Nil token store.
	srv.tokens = nil
	if srv.getValidToken("spotify", logger) != "" {
		t.Fatal("nil store should be empty")
	}
}

func TestTokenStoreEncryptDecryptRoundtrip(t *testing.T) {
	ts, err := newTestTokenStore(t.TempDir()+"/tokens.json", "passphrase")
	if err != nil {
		t.Fatal(err)
	}
	enc, err := ts.encrypt([]byte("secret-token-payload"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if enc == "" || strings.Contains(enc, "secret-token-payload") {
		t.Fatalf("ciphertext = %q", enc)
	}
	dec, err := ts.decrypt(enc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(dec) != "secret-token-payload" {
		t.Fatalf("decrypted = %q", dec)
	}

	// Tampered ciphertext fails.
	if _, err := ts.decrypt(enc[:4] + "AAAA" + enc[8:]); err == nil {
		t.Fatal("expected decrypt failure on tamper")
	}
}

func TestScopesSatisfy_CoverageHelpers(t *testing.T) {
	if !scopesSatisfy([]string{"a", "b"}, nil) {
		t.Fatal("empty required should satisfy")
	}
	if !scopesSatisfy([]string{"a", "b", "c"}, []string{"a", "c"}) {
		t.Fatal("subset should satisfy")
	}
	if scopesSatisfy([]string{"a"}, []string{"a", "z"}) {
		t.Fatal("missing scope should not satisfy")
	}
}

func TestHandleAdminRoutesStillRegistered(t *testing.T) {
	// Smoke: library route handlers are wired and method-checked.
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	handler := srv.libraryRouteHandlers()["/v1/library/review"]
	req := httptest.NewRequest(http.MethodHead, "/v1/library/review", nil)
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", w.Code)
	}
}
