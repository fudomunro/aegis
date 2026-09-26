package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

	// Reload with wrong passphrase — must refuse, not look like an empty store.
	ts2, err := NewTokenStore(path, "wrong")
	if err == nil {
		t.Fatal("expected error with wrong passphrase")
	}
	if ts2 != nil {
		t.Fatal("expected nil store with wrong passphrase")
	}
	if !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("error should name the likely cause, got: %v", err)
	}

	// Original passphrase still decrypts the existing tokens.
	ts3, err := NewTokenStore(path, "correct")
	if err != nil {
		t.Fatalf("reload with correct passphrase: %v", err)
	}
	got := ts3.Get("spotify")
	if got == nil || got.AccessToken != "secret" {
		t.Fatal("original tokens must survive a wrong-passphrase open")
	}
}

func TestTokenStore_WrongPassphraseEmptyStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")

	ts1, err := NewTokenStore(path, "correct")
	if err != nil {
		t.Fatal(err)
	}
	// Persist an empty store so the file (and key check) exist on disk.
	if err := ts1.persist(); err != nil {
		t.Fatal(err)
	}

	if _, err := NewTokenStore(path, "wrong"); err == nil {
		t.Fatal("expected error for wrong passphrase on empty store")
	}
	if _, err := NewTokenStore(path, "correct"); err != nil {
		t.Fatalf("correct passphrase should load empty store: %v", err)
	}
}

func TestTokenStore_LegacyFileAllDecryptFail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")

	// Build a store, then strip key_check to simulate a pre-issue-100 file.
	ts1, _ := NewTokenStore(path, "correct")
	ts1.Save("spotify", &OAuthToken{AccessToken: "secret", Expiry: time.Now().Add(time.Hour)})

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	delete(raw, "key_check")
	legacy, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := NewTokenStore(path, "wrong"); err == nil {
		t.Fatal("expected error for wrong passphrase on legacy file")
	}
	ts3, err := NewTokenStore(path, "correct")
	if err != nil {
		t.Fatalf("correct passphrase should load legacy file: %v", err)
	}
	if got := ts3.Get("spotify"); got == nil || got.AccessToken != "secret" {
		t.Fatal("legacy token not loaded")
	}
}

func TestTokenStore_PartialDecryptBlocksPersist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")

	ts1, _ := NewTokenStore(path, "correct")
	ts1.Save("spotify", &OAuthToken{AccessToken: "keep", Expiry: time.Now().Add(time.Hour)})
	ts1.Save("youtube", &OAuthToken{AccessToken: "also", Expiry: time.Now().Add(time.Hour)})

	// Corrupt one ciphertext so a correct passphrase still has an unreadable entry.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var tf tokenFile
	if err := json.Unmarshal(data, &tf); err != nil {
		t.Fatal(err)
	}
	for id := range tf.Tokens {
		if id == "youtube" {
			tf.Tokens[id] = base64.StdEncoding.EncodeToString([]byte("not-a-valid-ciphertext"))
		}
	}
	corrupt, err := json.MarshalIndent(tf, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	before := string(corrupt)
	ts2, err := NewTokenStore(path, "correct")
	if err != nil {
		t.Fatalf("partial load should return a store: %v", err)
	}
	if ts2.Get("spotify") == nil || ts2.Get("spotify").AccessToken != "keep" {
		t.Fatal("intact token should load")
	}
	if ts2.Get("youtube") != nil {
		t.Fatal("corrupt token should not load")
	}

	// Save must refuse and leave the on-disk file untouched.
	err = ts2.Save("new-service", &OAuthToken{AccessToken: "x", Expiry: time.Now().Add(time.Hour)})
	if err == nil {
		t.Fatal("Save must refuse after a partial decrypt failure")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != before {
		t.Fatal("token file must not be rewritten after partial decrypt failure")
	}
}

func TestTokenStore_SaveWritesKeyCheck(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")

	ts, _ := NewTokenStore(path, "pass")
	if err := ts.Save("spotify", &OAuthToken{AccessToken: "x", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var tf tokenFile
	if err := json.Unmarshal(data, &tf); err != nil {
		t.Fatal(err)
	}
	if tf.KeyCheck == "" {
		t.Fatal("expected key_check in token file")
	}
	plain, err := (&TokenStore{key: ts.key}).decrypt(tf.KeyCheck)
	if err != nil || string(plain) != keyCheckPlaintext {
		t.Fatalf("key check did not seal the known plaintext: %v %q", err, plain)
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

func TestTokenStore_GetReturnsCopy(t *testing.T) {
	ts := &TokenStore{Tokens: make(map[string]*OAuthToken)}
	tok := &OAuthToken{
		AccessToken: "orig",
		Expiry:      time.Now().Add(time.Hour),
		Scopes:      []string{"a"},
	}
	if err := ts.Save("spotify", tok); err != nil {
		t.Fatal(err)
	}

	got := ts.Get("spotify")
	got.AccessToken = "mutated"
	got.Scopes[0] = "mutated-scope"

	again := ts.Get("spotify")
	if again.AccessToken != "orig" || again.Scopes[0] != "a" {
		t.Fatalf("store mutated via Get copy: %+v", again)
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
