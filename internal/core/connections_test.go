package core

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
			"spotify": {
				Name:         "Spotify",
				ClientID:     "spot",
				ClientSecret: "spot-secret",
				AuthURL:      "https://accounts.spotify.com/authorize",
				TokenURL:     "https://accounts.spotify.com/api/token",
				Scopes:       []string{"user-library-read"},
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
				ID:          "spotify",
				Name:        "Spotify",
				Action:      "inject",
				Connection:  "spotify",
				OAuthScopes: []string{"user-library-read"},
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

func TestConnectionForService_Shared(t *testing.T) {
	cfg := googleSharedConfig()

	id, conn := cfg.ConnectionForService("youtube-music")
	if id != "google" || conn == nil || conn.ClientID != "cid" {
		t.Fatalf("youtube-music → (%q, %+v)", id, conn)
	}

	id, conn = cfg.ConnectionForService("spotify")
	if id != "spotify" || conn == nil || conn.ClientID != "spot" {
		t.Fatalf("spotify → (%q, %+v)", id, conn)
	}

	id, conn = cfg.ConnectionForService("tidal")
	if id != "" || conn != nil {
		t.Fatalf("tidal → (%q, %+v), want empty", id, conn)
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

	login := cfg.loginScopes("google")
	if len(login) != 3 {
		t.Fatalf("loginScopes = %v, want connection union of 3", login)
	}

	login = cfg.loginScopes("spotify")
	if len(login) != 1 || login[0] != "user-library-read" {
		t.Fatalf("spotify loginScopes = %v", login)
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
	path := filepath.Join(dir, "aegis.json")
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

func TestGetValidToken_SharedConnectionScopeGate(t *testing.T) {
	cfg := googleSharedConfig()
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	// Token only has YouTube scope — Photos surface must not use it.
	srv.tokens.Save("google", &OAuthToken{
		AccessToken:  "yt-only",
		RefreshToken: "r",
		Expiry:       time.Now().Add(time.Hour),
		Scopes:       []string{"https://www.googleapis.com/auth/youtube.readonly"},
	})

	if tok := srv.GetValidToken("youtube-music", slog.Default()); tok != "yt-only" {
		t.Fatalf("youtube token = %q, want yt-only", tok)
	}
	if tok := srv.GetValidToken("google-photos", slog.Default()); tok != "" {
		t.Fatalf("photos token = %q, want empty (missing scope)", tok)
	}
	if tok := srv.GetValidToken("google", slog.Default()); tok != "" {
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
		if tok := srv.GetValidToken(id, slog.Default()); tok != "full" {
			t.Fatalf("%s token = %q, want full", id, tok)
		}
	}
}
