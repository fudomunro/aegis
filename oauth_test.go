package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestHandleAuthLogin_GoogleOfflineAccess(t *testing.T) {
	cfg := &Config{Services: []Service{{
		ID:   "youtube-music",
		Name: "YouTube Music",
		OAuth: &OAuthConfig{
			ClientID: "cid",
			AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
			TokenURL: "https://oauth2.googleapis.com/token",
			Scopes:   []string{"https://www.googleapis.com/auth/youtube.readonly"},
		},
	}}}
	srv := testServerWithBase(cfg, "https://guardian.example.com")

	req := httptest.NewRequest(http.MethodGet, "/v1/auth/youtube-music/login", nil)
	rec := httptest.NewRecorder()
	srv.HandleAuthLogin(rec, req)

	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want 307", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "https://accounts.google.com/o/oauth2/v2/auth?") {
		t.Fatalf("location = %v", loc)
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("access_type") != "offline" {
		t.Fatalf("access_type = %q, want offline", q.Get("access_type"))
	}
	if q.Get("prompt") != "consent" {
		t.Fatalf("prompt = %q, want consent", q.Get("prompt"))
	}
	if q.Get("redirect_uri") != "https://guardian.example.com/v1/auth/youtube-music/callback" {
		t.Fatalf("redirect_uri = %q", q.Get("redirect_uri"))
	}
}

func TestHandleAuthLogin_SpotifyOmitsGoogleParams(t *testing.T) {
	cfg := &Config{Services: []Service{{
		ID:   "spotify",
		Name: "Spotify",
		OAuth: &OAuthConfig{
			ClientID: "cid",
			AuthURL:  "https://accounts.spotify.com/authorize",
			TokenURL: "https://accounts.spotify.com/api/token",
			Scopes:   []string{"user-library-read"},
		},
	}}}
	srv := testServerWithBase(cfg, "https://guardian.example.com")

	req := httptest.NewRequest(http.MethodGet, "/v1/auth/spotify/login", nil)
	rec := httptest.NewRecorder()
	srv.HandleAuthLogin(rec, req)

	loc := rec.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("access_type") != "" {
		t.Fatalf("spotify should not send access_type, got %q", u.Query().Get("access_type"))
	}
}
