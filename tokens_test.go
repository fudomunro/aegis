package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestTokenStore opens a store with a tiny PBKDF2 cost so the suite stays
// fast. Production uses NewTokenStore / pbkdf2IterDefault.
func newTestTokenStore(path, passphrase string) (*TokenStore, error) {
	return newTokenStore(path, passphrase, 1_000)
}

func TestTokenStore_SaveAndGet(t *testing.T) {
	dir := t.TempDir()
	ts, err := newTokenStore(filepath.Join(dir, "tokens.json"), "test-passphrase", 1_000)
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

	ts1, _ := newTestTokenStore(path, "pass123")
	ts1.Save("spotify", &OAuthToken{
		AccessToken: "persisted",
		Expiry:      time.Now().Add(time.Hour),
	})

	// Reload from disk with same passphrase.
	ts2, err := newTestTokenStore(path, "pass123")
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

	ts1, _ := newTestTokenStore(path, "correct")
	ts1.Save("spotify", &OAuthToken{
		AccessToken: "secret",
		Expiry:      time.Now().Add(time.Hour),
	})

	// Reload with wrong passphrase — must refuse, not look like an empty store.
	ts2, err := newTestTokenStore(path, "wrong")
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
	ts3, err := newTestTokenStore(path, "correct")
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

	ts1, err := newTestTokenStore(path, "correct")
	if err != nil {
		t.Fatal(err)
	}
	// Persist an empty store so the file (and key check) exist on disk.
	if err := ts1.persist(); err != nil {
		t.Fatal(err)
	}

	if _, err := newTestTokenStore(path, "wrong"); err == nil {
		t.Fatal("expected error for wrong passphrase on empty store")
	}
	if _, err := newTestTokenStore(path, "correct"); err != nil {
		t.Fatalf("correct passphrase should load empty store: %v", err)
	}
}

func TestTokenStore_LegacyFileAllDecryptFail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")

	// Build a store, then strip key_check to simulate a pre-issue-100 file.
	ts1, _ := newTestTokenStore(path, "correct")
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

	if _, err := newTestTokenStore(path, "wrong"); err == nil {
		t.Fatal("expected error for wrong passphrase on legacy file")
	}
	ts3, err := newTestTokenStore(path, "correct")
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

	ts1, _ := newTestTokenStore(path, "correct")
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
			tf.Tokens[id] = tokenCiphertext{Data: base64.StdEncoding.EncodeToString([]byte("not-a-valid-ciphertext"))}
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
	ts2, err := newTestTokenStore(path, "correct")
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

	ts, _ := newTestTokenStore(path, "pass")
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
	ts, _ := newTestTokenStore(filepath.Join(dir, "tokens.json"), "pass")
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
	ts2, _ := newTestTokenStore(path, "pass")
	if ts2.Get("spotify") != nil {
		t.Fatal("expected nil after reload")
	}
}

func TestTokenStore_MultipleServices(t *testing.T) {
	dir := t.TempDir()
	ts, _ := newTestTokenStore(filepath.Join(dir, "tokens.json"), "pass")

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
	ts, _ := newTestTokenStore(filepath.Join(dir, "tokens.json"), "pass")

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

func TestTokenStore_PersistAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")

	ts, err := newTestTokenStore(path, "pass")
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.Save("spotify", &OAuthToken{
		AccessToken: "atomic",
		Expiry:      time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	// No leftover temp files from the atomic write path.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "tokens.json" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("directory entries = %v, want only tokens.json", names)
	}

	// tokenFilePerms (0600) must survive the temp+chmod+rename path.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != tokenFilePerms {
		t.Fatalf("mode = %v, want %v", info.Mode().Perm(), tokenFilePerms)
	}

	// File is a complete parseable token store after the write.
	reloaded, err := newTestTokenStore(path, "pass")
	if err != nil {
		t.Fatalf("reload after atomic write: %v", err)
	}
	got := reloaded.Get("spotify")
	if got == nil || got.AccessToken != "atomic" {
		t.Fatal("token missing after atomic write")
	}
}

func TestTokenStore_PersistSurvivesInterruptedWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")

	ts, err := newTestTokenStore(path, "pass")
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.Save("spotify", &OAuthToken{
		AccessToken: "keep-me",
		Expiry:      time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a crash mid-write: a truncated same-directory temp file left
	// behind next to a complete tokens.json (what atomic rename avoids for
	// the real file, and what a leftover must not corrupt).
	partial := filepath.Join(dir, ".tokens.json.tmpcrashed")
	if err := os.WriteFile(partial, []byte(`{"version":1,"salt":"`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Real file is still the complete previous snapshot and still loads.
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("tokens.json changed after a simulated interrupted write")
	}
	reloaded, err := newTestTokenStore(path, "pass")
	if err != nil {
		t.Fatalf("load after interrupted write: %v", err)
	}
	if got := reloaded.Get("spotify"); got == nil || got.AccessToken != "keep-me" {
		t.Fatal("tokens must remain loadable after interrupted write")
	}

	// A subsequent persist replaces the store cleanly and ignores the leftover.
	ts2, err := newTestTokenStore(path, "pass")
	if err != nil {
		t.Fatal(err)
	}
	if err := ts2.Save("spotify", &OAuthToken{
		AccessToken: "updated",
		Expiry:      time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	final, err := newTestTokenStore(path, "pass")
	if err != nil {
		t.Fatal(err)
	}
	if got := final.Get("spotify"); got == nil || got.AccessToken != "updated" {
		t.Fatal("persist after interrupted write should write a complete store")
	}
	// tokens.json must still be the only live store file (temp leftovers aside).
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != tokenFilePerms {
		t.Fatalf("mode after rewrite = %v, want %v", info.Mode().Perm(), tokenFilePerms)
	}
}

func TestTokenStore_NewFileRecordsKDFParams(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")

	ts, err := newTokenStore(path, "pass", 2_000)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.Save("spotify", &OAuthToken{
		AccessToken: "x",
		Expiry:      time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	tf := readTokenFile(t, path)
	if tf.Version != tokenVersion {
		t.Fatalf("version = %d, want %d", tf.Version, tokenVersion)
	}
	if tf.KDF != kdfPBKDF2SHA256 {
		t.Fatalf("kdf = %q, want %q", tf.KDF, kdfPBKDF2SHA256)
	}
	if tf.Iterations != 2_000 {
		t.Fatalf("iterations = %d, want 2000", tf.Iterations)
	}
	if tf.Salt == "" {
		t.Fatal("expected master salt")
	}
	entry, ok := tf.Tokens["spotify"]
	if !ok {
		t.Fatal("missing spotify entry")
	}
	if entry.Salt == "" {
		t.Fatal("expected per-entry salt")
	}
	if entry.Data == "" {
		t.Fatal("expected per-entry ciphertext")
	}
}

func TestTokenStore_DefaultKDFMeetsGuidance(t *testing.T) {
	if pbkdf2IterDefault < 600_000 {
		t.Fatalf("pbkdf2IterDefault = %d, want >= 600000 (OWASP PBKDF2-HMAC-SHA256)", pbkdf2IterDefault)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")
	ts, err := NewTokenStore(path, "pass")
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.Save("spotify", &OAuthToken{AccessToken: "x", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	tf := readTokenFile(t, path)
	if tf.Iterations != pbkdf2IterDefault {
		t.Fatalf("iterations = %d, want %d", tf.Iterations, pbkdf2IterDefault)
	}
}

func TestTokenStore_PerEntrySaltsAreUnique(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")

	ts, err := newTokenStore(path, "pass", 1_000)
	if err != nil {
		t.Fatal(err)
	}
	ts.Save("spotify", &OAuthToken{AccessToken: "a", Expiry: time.Now().Add(time.Hour)})
	ts.Save("youtube", &OAuthToken{AccessToken: "b", Expiry: time.Now().Add(time.Hour)})

	tf := readTokenFile(t, path)
	if tf.Tokens["spotify"].Salt == tf.Tokens["youtube"].Salt {
		t.Fatal("per-entry salts must be unique")
	}
	if tf.Tokens["spotify"].Salt == "" || tf.Tokens["youtube"].Salt == "" {
		t.Fatal("every entry needs a salt")
	}
}

func TestTokenStore_LegacyV1FileLoadsAndMigratesOnWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")
	writeLegacyV1TokenFile(t, path, "correct", map[string]string{
		"spotify": `{"access_token":"legacy-secret","refresh_token":"","token_type":"Bearer","expiry":"2030-01-01T00:00:00Z","scopes":null}`,
	})

	loaded, err := newTokenStore(path, "correct", 1_000)
	if err != nil {
		t.Fatalf("v1 file must load: %v", err)
	}
	got := loaded.Get("spotify")
	if got == nil || got.AccessToken != "legacy-secret" {
		t.Fatalf("v1 token = %+v", got)
	}
	if loaded.kdfIterations != pbkdf2IterLegacy {
		t.Fatalf("legacy iterations = %d, want %d", loaded.kdfIterations, pbkdf2IterLegacy)
	}

	// Next persist migrates the file to v2 with explicit KDF params.
	if err := loaded.Save("spotify", &OAuthToken{
		AccessToken: "migrated",
		Expiry:      time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	tf := readTokenFile(t, path)
	if tf.Version != tokenVersion {
		t.Fatalf("migrated version = %d, want %d", tf.Version, tokenVersion)
	}
	if tf.KDF != kdfPBKDF2SHA256 {
		t.Fatalf("migrated kdf = %q", tf.KDF)
	}
	if tf.Iterations != pbkdf2IterLegacy {
		t.Fatalf("migrated iterations = %d, want %d (same cost until rekey)", tf.Iterations, pbkdf2IterLegacy)
	}
	if tf.Tokens["spotify"].Salt == "" {
		t.Fatal("migrated entry must gain a per-entry salt")
	}

	reloaded, err := newTokenStore(path, "correct", 1_000)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Get("spotify"); got == nil || got.AccessToken != "migrated" {
		t.Fatalf("migrated token = %+v", got)
	}
}

func TestTokenStore_LegacyV1WrongPassphraseStillRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")
	writeLegacyV1TokenFile(t, path, "correct", map[string]string{
		"spotify": `{"access_token":"secret"}`,
	})

	if _, err := newTokenStore(path, "wrong", 1_000); err == nil {
		t.Fatal("expected error for wrong passphrase on v1 file")
	}
}

func TestTokenStore_ChangePassphrase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")

	ts, err := newTokenStore(path, "old-pass", 1_000)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.Save("spotify", &OAuthToken{
		AccessToken: "keep",
		Expiry:      time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	oldSalt := base64.StdEncoding.EncodeToString(ts.salt)

	if err := ts.ChangePassphrase("new-pass"); err != nil {
		t.Fatal(err)
	}
	if base64.StdEncoding.EncodeToString(ts.salt) == oldSalt {
		t.Fatal("rekey must rotate the master salt")
	}

	if _, err := newTokenStore(path, "old-pass", 1_000); err == nil {
		t.Fatal("old passphrase must stop working after rekey")
	}
	reloaded, err := newTokenStore(path, "new-pass", 1_000)
	if err != nil {
		t.Fatalf("new passphrase: %v", err)
	}
	if got := reloaded.Get("spotify"); got == nil || got.AccessToken != "keep" {
		t.Fatalf("token after rekey = %+v", got)
	}
}

func TestTokenStore_ChangePassphraseUpgradesBelowPreferredIterations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")

	ts, err := newTokenStore(path, "pass", 50_000)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.Save("spotify", &OAuthToken{
		AccessToken: "x",
		Expiry:      time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	// Simulate a store still below the preferred cost.
	ts.kdfIterations = 10_000
	if err := ts.ChangePassphrase("pass"); err != nil {
		t.Fatal(err)
	}
	tf := readTokenFile(t, path)
	if tf.Iterations != 50_000 {
		t.Fatalf("iterations after rekey = %d, want preferred 50000", tf.Iterations)
	}
}

func TestTokenStore_ChangePassphraseNeverDowngradesIterations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")

	ts, err := newTokenStore(path, "pass", 500)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.Save("spotify", &OAuthToken{
		AccessToken: "x",
		Expiry:      time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	// Pretend the file was already harder than preferred.
	ts.kdfIterations = 50_000
	if err := ts.ChangePassphrase("next"); err != nil {
		t.Fatal(err)
	}
	tf := readTokenFile(t, path)
	if tf.Iterations != 50_000 {
		t.Fatalf("iterations = %d, want 50000 (never downgrade)", tf.Iterations)
	}
}

func TestTokenStore_ChangePassphraseRejectsEmpty(t *testing.T) {
	ts, err := newTokenStore("", "pass", 1_000)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.ChangePassphrase(""); err == nil {
		t.Fatal("empty passphrase must be rejected")
	}
}

func TestTokenStore_UnsupportedVersionAndKDF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")

	writeJSONFile(t, path, map[string]any{
		"version":    99,
		"kdf":        kdfPBKDF2SHA256,
		"iterations": 1_000,
		"salt":       base64.StdEncoding.EncodeToString(make([]byte, 16)),
		"tokens":     map[string]string{},
	})
	if _, err := newTokenStore(path, "pass", 1_000); err == nil {
		t.Fatal("unsupported version must fail")
	}

	writeJSONFile(t, path, map[string]any{
		"version":    tokenVersion,
		"kdf":        "scrypt",
		"iterations": 1_000,
		"salt":       base64.StdEncoding.EncodeToString(make([]byte, 16)),
		"tokens":     map[string]string{},
	})
	if _, err := newTokenStore(path, "pass", 1_000); err == nil {
		t.Fatal("unsupported kdf must fail")
	}

	writeJSONFile(t, path, map[string]any{
		"version": tokenVersion,
		"kdf":     kdfPBKDF2SHA256,
		"salt":    base64.StdEncoding.EncodeToString(make([]byte, 16)),
		"tokens":  map[string]string{},
	})
	if _, err := newTokenStore(path, "pass", 1_000); err == nil {
		t.Fatal("missing iterations must fail on v2")
	}
}

func TestTokenStore_LoadsRecordedIterations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")

	ts, err := newTokenStore(path, "pass", 1_500)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.Save("spotify", &OAuthToken{
		AccessToken: "x",
		Expiry:      time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	loaded, err := newTokenStore(path, "pass", 10_000) // preferred ignored on load
	if err != nil {
		t.Fatal(err)
	}
	if loaded.kdfIterations != 1_500 {
		t.Fatalf("loaded iterations = %d, want 1500 from file", loaded.kdfIterations)
	}
}

func readTokenFile(t *testing.T, path string) tokenFile {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var tf tokenFile
	if err := json.Unmarshal(data, &tf); err != nil {
		t.Fatal(err)
	}
	return tf
}

func writeJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRunTokenRekeyCLI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")

	ts, err := newTokenStore(path, "old-pass", 1_000)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.Save("spotify", &OAuthToken{
		AccessToken: "cli-token",
		Expiry:      time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	// Wrong current passphrase must fail and leave the file alone.
	if code := runTokenRekeyCLI(path, "wrong", "new-pass"); code == 0 {
		t.Fatal("rekey with wrong current passphrase should fail")
	}
	if _, err := newTokenStore(path, "old-pass", 1_000); err != nil {
		t.Fatalf("token file must survive failed rekey: %v", err)
	}

	if code := runTokenRekeyCLI(path, "old-pass", "new-pass"); code != 0 {
		t.Fatal("rekey should succeed")
	}
	if _, err := newTokenStore(path, "old-pass", 1_000); err == nil {
		t.Fatal("old passphrase must stop working")
	}
	reloaded, err := newTokenStore(path, "new-pass", 1_000)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Get("spotify"); got == nil || got.AccessToken != "cli-token" {
		t.Fatalf("token after CLI rekey = %+v", got)
	}

	if code := runTokenRekeyCLI("", "old", "new"); code == 0 {
		t.Fatal("missing path should fail")
	}
	if code := runTokenRekeyCLI(path, "", "new"); code == 0 {
		t.Fatal("missing current passphrase should fail")
	}
	if code := runTokenRekeyCLI(path, "new-pass", "  "); code == 0 {
		t.Fatal("blank new passphrase should fail")
	}
}

// writeLegacyV1TokenFile builds a pre-issue-101 tokens.json: version 1, bare
// string ciphertexts, implicit 100k PBKDF2, no kdf fields.
func writeLegacyV1TokenFile(t *testing.T, path, passphrase string, tokens map[string]string) {
	t.Helper()
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		t.Fatal(err)
	}
	key := deriveKey(passphrase, salt, pbkdf2IterLegacy)
	keyCheck, err := encryptWith(key, []byte(keyCheckPlaintext))
	if err != nil {
		t.Fatal(err)
	}
	encTokens := make(map[string]string, len(tokens))
	for id, plain := range tokens {
		enc, err := encryptWith(key, []byte(plain))
		if err != nil {
			t.Fatal(err)
		}
		encTokens[id] = enc
	}
	writeJSONFile(t, path, map[string]any{
		"version":   tokenVersionV1,
		"salt":      base64.StdEncoding.EncodeToString(salt),
		"key_check": keyCheck,
		"tokens":    encTokens,
	})
}
