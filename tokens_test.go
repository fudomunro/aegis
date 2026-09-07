package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestTokenStore_SaveAndGet(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTokenStore(filepath.Join(dir, "tokens.json"), "test-passphrase")
	if err != nil {
		t.Fatal(err)
	}

	tok := &OAuthToken{
		AccessToken:  "access-123",
		RefreshToken: "refresh-456",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(1 * time.Hour),
		Scopes:       []string{"user-library-read"},
	}

	if err := ts.Save("spotify", tok); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got := ts.Get("spotify")
	if got == nil {
		t.Fatal("expected token after save")
	}
	if got.AccessToken != "access-123" {
		t.Fatalf("AccessToken = %q", got.AccessToken)
	}
	if got.RefreshToken != "refresh-456" {
		t.Fatalf("RefreshToken = %q", got.RefreshToken)
	}
	if len(got.Scopes) != 1 || got.Scopes[0] != "user-library-read" {
		t.Fatalf("Scopes = %v", got.Scopes)
	}
}

func TestTokenStore_Persistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")

	ts1, _ := NewTokenStore(path, "pass123")
	ts1.Save("spotify", &OAuthToken{
		AccessToken: "persisted",
		Expiry:      time.Now().Add(time.Hour),
	})

	// Reload from disk with same passphrase.
	ts2, err := NewTokenStore(path, "pass123")
	if err != nil {
		t.Fatal(err)
	}
	got := ts2.Get("spotify")
	if got == nil || got.AccessToken != "persisted" {
		t.Fatal("token not persisted across reload")
	}
}

func TestTokenStore_WrongPassphrase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")

	ts1, _ := NewTokenStore(path, "correct")
	ts1.Save("spotify", &OAuthToken{
		AccessToken: "secret",
		Expiry:      time.Now().Add(time.Hour),
	})

	// Reload with wrong passphrase — should load but fail to decrypt.
	ts2, err := NewTokenStore(path, "wrong")
	if err != nil {
		t.Fatal(err)
	}
	if ts2.Get("spotify") != nil {
		t.Fatal("expected nil token with wrong passphrase")
	}
}

func TestTokenStore_Delete(t *testing.T) {
	dir := t.TempDir()
	ts, _ := NewTokenStore(filepath.Join(dir, "tokens.json"), "pass")
	ts.Save("spotify", &OAuthToken{AccessToken: "x", Expiry: time.Now().Add(time.Hour)})

	if ts.Get("spotify") == nil {
		t.Fatal("expected token before delete")
	}

	ts.Delete("spotify")
	if ts.Get("spotify") != nil {
		t.Fatal("expected nil after delete")
	}

	// Reload from disk to verify delete persisted.
	path := filepath.Join(dir, "tokens.json")
	ts2, _ := NewTokenStore(path, "pass")
	if ts2.Get("spotify") != nil {
		t.Fatal("expected nil after reload")
	}
}

func TestTokenStore_MultipleServices(t *testing.T) {
	dir := t.TempDir()
	ts, _ := NewTokenStore(filepath.Join(dir, "tokens.json"), "pass")

	ts.Save("spotify", &OAuthToken{AccessToken: "spot", Expiry: time.Now().Add(time.Hour)})
	ts.Save("youtube-music", &OAuthToken{AccessToken: "yt", Expiry: time.Now().Add(time.Hour)})
	ts.Save("amazon-music", &OAuthToken{AccessToken: "amz", Expiry: time.Now().Add(time.Hour)})

	if ts.Get("spotify").AccessToken != "spot" {
		t.Fatal("spotify token wrong")
	}
	if ts.Get("youtube-music").AccessToken != "yt" {
		t.Fatal("youtube-music token wrong")
	}
	if ts.Get("amazon-music").AccessToken != "amz" {
		t.Fatal("amazon-music token wrong")
	}

	status := ts.Status()
	if len(status) != 3 {
		t.Fatalf("status has %d services, want 3", len(status))
	}
}

func TestOAuthToken_IsExpired(t *testing.T) {
	tok := &OAuthToken{Expiry: time.Now().Add(-time.Minute)}
	if !tok.IsExpired(0) {
		t.Fatal("expected expired")
	}

	tok.Expiry = time.Now().Add(30 * time.Second)
	if !tok.IsExpired(time.Minute) {
		t.Fatal("expected expired within 1m grace")
	}

	tok.Expiry = time.Now().Add(5 * time.Minute)
	if tok.IsExpired(time.Minute) {
		t.Fatal("expected not expired")
	}
}

func TestTokenStore_Status(t *testing.T) {
	dir := t.TempDir()
	ts, _ := NewTokenStore(filepath.Join(dir, "tokens.json"), "pass")

	ts.Save("spotify", &OAuthToken{
		AccessToken:  "x",
		RefreshToken: "r",
		Expiry:       time.Now().Add(time.Hour),
		Scopes:       []string{"read"},
	})

	status := ts.Status()
	s := status["spotify"]
	if !s.HasToken {
		t.Fatal("expected HasToken")
	}
	if s.Expired {
		t.Fatal("expected not expired")
	}
	if !s.HasRefresh {
		t.Fatal("expected HasRefresh")
	}
	if len(s.Scopes) != 1 {
		t.Fatal("expected 1 scope")
	}
}
