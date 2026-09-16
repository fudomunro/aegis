package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func googleSharedConfig() *Config {
	return &Config{
		Connections: map[string]Connection{
			"google": {
				Name:         "Google",
				ClientID:     "cid",
				ClientSecret: "secret",
				AuthURL:      "https://accounts.google.com/o/oauth2/v2/auth",
				TokenURL:     "https://oauth2.googleapis.com/token",
				Scopes: []string{
					"https://www.googleapis.com/auth/youtube.readonly",
					"https://www.googleapis.com/auth/photospicker.mediaitems.readonly",
					"https://www.googleapis.com/auth/contacts.readonly",
				},
			},
		},
		Services: []Service{
			{
				ID:          "youtube-music",
				Name:        "YouTube Music",
				Action:      "inject",
				Connection:  "google",
				OAuthScopes: []string{"https://www.googleapis.com/auth/youtube.readonly"},
			},
			{
				ID:          "google-photos",
				Name:        "Google Photos",
				Action:      "inject",
				Connection:  "google",
				OAuthScopes: []string{"https://www.googleapis.com/auth/photospicker.mediaitems.readonly"},
			},
			{
				ID:          "google",
				Name:        "Google Contacts",
				Action:      "inject",
				Connection:  "google",
				OAuthScopes: []string{"https://www.googleapis.com/auth/contacts.readonly"},
			},
			{
				ID:     "spotify",
				Name:   "Spotify",
				Action: "inject",
				OAuth: &OAuthConfig{
					ClientID:     "spot",
					ClientSecret: "spot-secret",
					AuthURL:      "https://accounts.spotify.com/authorize",
					TokenURL:     "https://accounts.spotify.com/api/token",
					Scopes:       []string{"user-library-read"},
				},
			},
		},
	}
}

func TestConnectionIDForService(t *testing.T) {
	cfg := googleSharedConfig()
	for _, id := range []string{"youtube-music", "google-photos", "google"} {
		if got := cfg.connectionIDForService(id); got != "google" {
			t.Fatalf("connectionIDForService(%q) = %q, want google", id, got)
		}
	}
	if got := cfg.connectionIDForService("spotify"); got != "spotify" {
		t.Fatalf("connectionIDForService(spotify) = %q, want spotify", got)
	}
	if got := cfg.connectionIDForService("unknown"); got != "unknown" {
		t.Fatalf("connectionIDForService(unknown) = %q, want unknown", got)
	}
}

func TestConnectionForService_SharedAndLegacy(t *testing.T) {
	cfg := googleSharedConfig()

	id, conn := cfg.ConnectionForService("youtube-music")
	if id != "google" || conn == nil || conn.ClientID != "cid" {
		t.Fatalf("youtube-music → (%q, %+v)", id, conn)
	}

	id, conn = cfg.ConnectionForService("spotify")
	if id != "spotify" || conn == nil || conn.ClientID != "spot" {
		t.Fatalf("spotify → (%q, %+v)", id, conn)
	}
}

func TestOAuthForConnection(t *testing.T) {
	cfg := googleSharedConfig()
	oauth := cfg.OAuthForConnection("google")
	if oauth == nil || oauth.TokenURL != "https://oauth2.googleapis.com/token" {
		t.Fatalf("OAuthForConnection(google) = %+v", oauth)
	}
	if cfg.OAuthForConnection("missing") != nil {
		t.Fatal("expected nil for unknown connection")
	}
}

func TestRequiredAndLoginScopes(t *testing.T) {
	cfg := googleSharedConfig()

	req := cfg.requiredScopes("google-photos")
	if len(req) != 1 || req[0] != "https://www.googleapis.com/auth/photospicker.mediaitems.readonly" {
		t.Fatalf("requiredScopes = %v", req)
	}

	login := cfg.loginScopes("google", "youtube-music")
	if len(login) != 3 {
		t.Fatalf("loginScopes = %v, want connection union of 3", login)
	}

	// Legacy 1:1 service login scopes come from inline oauth.
	legacy := cfg.loginScopes("spotify", "spotify")
	if len(legacy) != 1 || legacy[0] != "user-library-read" {
		t.Fatalf("legacy loginScopes = %v", legacy)
	}
}

func TestScopesSatisfy(t *testing.T) {
	if !scopesSatisfy([]string{"a", "b", "c"}, []string{"b"}) {
		t.Fatal("expected satisfied")
	}
	if scopesSatisfy([]string{"a"}, []string{"a", "b"}) {
		t.Fatal("expected missing b")
	}
	if !scopesSatisfy([]string{"a"}, nil) {
		t.Fatal("empty required should always satisfy")
	}
}

func TestValidateConfig_UnknownConnection(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "youtube-music", Name: "YT", Action: "inject", Connection: "google"},
		},
	}
	err := validateConfig(cfg)
	if err == nil {
		t.Fatal("expected error for unknown connection")
	}
}

func TestValidateConfig_SharedConnectionOK(t *testing.T) {
	if err := validateConfig(googleSharedConfig()); err != nil {
		t.Fatalf("validateConfig: %v", err)
	}
}

func TestLoadConfig_SharedConnections(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "guardian.json")
	body := `{
		"connections": {
			"google": {
				"client_id": "cid",
				"client_secret": "sec",
				"auth_url": "https://accounts.google.com/o/oauth2/v2/auth",
				"token_url": "https://oauth2.googleapis.com/token",
				"scopes": ["https://www.googleapis.com/auth/youtube.readonly"]
			}
		},
		"services": [
			{
				"id": "youtube-music",
				"name": "YouTube Music",
				"action": "inject",
				"connection": "google",
				"oauth_scopes": ["https://www.googleapis.com/auth/youtube.readonly"]
			}
		]
	}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Connections["google"].ClientID != "cid" {
		t.Fatalf("connections not loaded: %+v", cfg.Connections)
	}
	if cfg.Services[0].Connection != "google" {
		t.Fatalf("service connection = %q", cfg.Services[0].Connection)
	}
}

func TestTokenStore_FoldIntoConnections(t *testing.T) {
	cfg := googleSharedConfig()
	ts := &TokenStore{Tokens: make(map[string]*OAuthToken)}

	// Legacy per-service tokens with overlapping scopes.
	ts.Tokens["youtube-music"] = &OAuthToken{
		AccessToken:  "yt-access",
		RefreshToken: "yt-refresh",
		Expiry:       time.Now().Add(time.Hour),
		Scopes:       []string{"https://www.googleapis.com/auth/youtube.readonly"},
	}
	ts.Tokens["google-photos"] = &OAuthToken{
		AccessToken:  "photos-access",
		RefreshToken: "photos-refresh",
		Expiry:       time.Now().Add(2 * time.Hour),
		Scopes:       []string{"https://www.googleapis.com/auth/photospicker.mediaitems.readonly"},
	}
	ts.Tokens["google"] = &OAuthToken{
		AccessToken:  "contacts-access",
		RefreshToken: "contacts-refresh",
		Expiry:       time.Now().Add(30 * time.Minute),
		Scopes:       []string{"https://www.googleapis.com/auth/contacts.readonly"},
	}
	ts.Tokens["spotify"] = &OAuthToken{
		AccessToken:  "spot",
		RefreshToken: "spot-refresh",
		Expiry:       time.Now().Add(time.Hour),
		Scopes:       []string{"user-library-read"},
	}

	ts.FoldIntoConnections(cfg)

	if len(ts.Tokens) != 2 {
		t.Fatalf("token keys after fold = %v", keysOf(ts.Tokens))
	}
	g := ts.Tokens["google"]
	if g == nil {
		t.Fatal("expected google connection token")
	}
	// Best token: photos has refresh + most recent expiry among google group...
	// photos has later expiry than youtube; contacts has fewer scopes.
	if g.RefreshToken != "photos-refresh" {
		t.Fatalf("picked refresh = %q, want photos-refresh (latest expiry among refresh holders)", g.RefreshToken)
	}
	if len(g.Scopes) != 3 {
		t.Fatalf("union scopes = %v, want 3", g.Scopes)
	}
	if ts.Tokens["spotify"] == nil || ts.Tokens["spotify"].AccessToken != "spot" {
		t.Fatalf("spotify token should be untouched: %+v", ts.Tokens["spotify"])
	}
	if _, ok := ts.Tokens["youtube-music"]; ok {
		t.Fatal("youtube-music key should have been folded away")
	}
}

func TestGetValidToken_SharedConnectionScopeGate(t *testing.T) {
	cfg := googleSharedConfig()
	srv := testServerWithBase(cfg, "https://guardian.example.com")

	// Token only has YouTube scope — Photos surface must not use it.
	srv.tokens.Save("google", &OAuthToken{
		AccessToken:  "yt-only",
		RefreshToken: "r",
		Expiry:       time.Now().Add(time.Hour),
		Scopes:       []string{"https://www.googleapis.com/auth/youtube.readonly"},
	})

	if tok := srv.getValidToken("youtube-music", slog.Default()); tok != "yt-only" {
		t.Fatalf("youtube token = %q, want yt-only", tok)
	}
	if tok := srv.getValidToken("google-photos", slog.Default()); tok != "" {
		t.Fatalf("photos token = %q, want empty (missing scope)", tok)
	}
	if tok := srv.getValidToken("google", slog.Default()); tok != "" {
		t.Fatalf("contacts token = %q, want empty (missing scope)", tok)
	}

	// Full union grants every Google surface.
	srv.tokens.Save("google", &OAuthToken{
		AccessToken:  "full",
		RefreshToken: "r",
		Expiry:       time.Now().Add(time.Hour),
		Scopes: []string{
			"https://www.googleapis.com/auth/youtube.readonly",
			"https://www.googleapis.com/auth/photospicker.mediaitems.readonly",
			"https://www.googleapis.com/auth/contacts.readonly",
		},
	})
	for _, id := range []string{"youtube-music", "google-photos", "google"} {
		if tok := srv.getValidToken(id, slog.Default()); tok != "full" {
			t.Fatalf("%s token = %q, want full", id, tok)
		}
	}
}

func TestGetValidToken_LegacyOneToOne(t *testing.T) {
	svc := Service{
		ID:     "spotify",
		Name:   "Spotify",
		Action: "inject",
		OAuth: &OAuthConfig{
			ClientID:     "cid",
			ClientSecret: "sec",
			AuthURL:      "https://accounts.spotify.com/authorize",
			TokenURL:     "https://accounts.spotify.com/api/token",
			Scopes:       []string{"user-library-read"},
		},
	}
	srv := testServerWithBase(&Config{Services: []Service{svc}}, "https://guardian.example.com")
	srv.tokens.Save("spotify", &OAuthToken{
		AccessToken: "spot-tok",
		Expiry:      time.Now().Add(time.Hour),
		Scopes:      []string{"user-library-read"},
	})
	if tok := srv.getValidToken("spotify", slog.Default()); tok != "spot-tok" {
		t.Fatalf("token = %q", tok)
	}
}

func keysOf(m map[string]*OAuthToken) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
