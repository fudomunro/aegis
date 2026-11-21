package tokens

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestTokenStore opens a store with a tiny PBKDF2 cost so the suite stays
// fast. Production uses NewTokenStore / pbkdf2IterDefault.
func newTestTokenStore(path, passphrase string) (*TokenStore, error) {
	return NewStoreWithIterations(path, passphrase, 1_000)
}

func TestTokenStore_SaveAndGet(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewStoreWithIterations(filepath.Join(dir, "tokens.json"), "test-passphrase", 1_000)
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

func TestTokenStore_MissingKeyCheckRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")

	// Build a store, then strip key_check to simulate a foreign/hand-edited
	// file. Without the check a wrong passphrase is indistinguishable from an
	// empty store, so the file must be refused outright rather than loaded.
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
	stripped, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, stripped, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := newTestTokenStore(path, "correct"); err == nil {
		t.Fatal("a file without a key check must be rejected")
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

	ts, err := NewStoreWithIterations(path, "pass", 2_000)
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

	ts, err := NewStoreWithIterations(path, "pass", 1_000)
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

func TestTokenStore_LegacyTokenFileRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")

	// Version 1: string ciphertexts under the master key, implicit PBKDF2 cost.
	writeJSONFile(t, path, map[string]any{
		"version": 1,
		"salt":    base64.StdEncoding.EncodeToString(make([]byte, 16)),
		"tokens":  map[string]string{"spotify": "AAAA"},
	})
	_, err := NewStoreWithIterations(path, "correct", 1_000)
	if err == nil || !strings.Contains(err.Error(), "unsupported token file version: 1") {
		t.Fatalf("v1 file must be rejected as unsupported, got %v", err)
	}

	// Version 2: object ciphertexts with entry salts, but no secrets map.
	writeJSONFile(t, path, map[string]any{
		"version":    2,
		"kdf":        kdfPBKDF2SHA256,
		"iterations": 1_000,
		"salt":       base64.StdEncoding.EncodeToString(make([]byte, 16)),
		"key_check":  "AAAA",
		"tokens":     map[string]map[string]string{"spotify": {"salt": "c2FsdA==", "data": "AAAA"}},
	})
	_, err = NewStoreWithIterations(path, "correct", 1_000)
	if err == nil || !strings.Contains(err.Error(), "unsupported token file version: 2") {
		t.Fatalf("v2 file must be rejected as unsupported, got %v", err)
	}

	// A version 1 file is refused on its version, not on the passphrase:
	// the same answer either way, and nothing is rewritten.
	if _, err := NewStoreWithIterations(path, "wrong", 1_000); err == nil {
		t.Fatal("expected error for a legacy file with a wrong passphrase")
	}
}

func TestTokenStore_ChangePassphrase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")

	ts, err := NewStoreWithIterations(path, "old-pass", 1_000)
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

	if _, err := NewStoreWithIterations(path, "old-pass", 1_000); err == nil {
		t.Fatal("old passphrase must stop working after rekey")
	}
	reloaded, err := NewStoreWithIterations(path, "new-pass", 1_000)
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

	ts, err := NewStoreWithIterations(path, "pass", 50_000)
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

	ts, err := NewStoreWithIterations(path, "pass", 500)
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
	ts, err := NewStoreWithIterations("", "pass", 1_000)
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
		"tokens":     map[string]any{},
		"key_check":  "AAAA",
	})
	if _, err := NewStoreWithIterations(path, "pass", 1_000); err == nil {
		t.Fatal("unsupported version must fail")
	}

	writeJSONFile(t, path, map[string]any{
		"version":    tokenVersion,
		"kdf":        "scrypt",
		"iterations": 1_000,
		"salt":       base64.StdEncoding.EncodeToString(make([]byte, 16)),
		"tokens":     map[string]any{},
		"key_check":  "AAAA",
	})
	if _, err := NewStoreWithIterations(path, "pass", 1_000); err == nil {
		t.Fatal("unsupported kdf must fail")
	}

	writeJSONFile(t, path, map[string]any{
		"version":   tokenVersion,
		"kdf":       kdfPBKDF2SHA256,
		"salt":      base64.StdEncoding.EncodeToString(make([]byte, 16)),
		"tokens":    map[string]any{},
		"key_check": "AAAA",
	})
	if _, err := NewStoreWithIterations(path, "pass", 1_000); err == nil {
		t.Fatal("missing iterations must fail")
	}
}

func TestTokenStore_LoadsRecordedIterations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")

	ts, err := NewStoreWithIterations(path, "pass", 1_500)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.Save("spotify", &OAuthToken{
		AccessToken: "x",
		Expiry:      time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	loaded, err := NewStoreWithIterations(path, "pass", 10_000) // preferred ignored on load
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

func TestTokenStoreEncryptDecryptRoundtrip(t *testing.T) {
	ts, err := newTestTokenStore(t.TempDir()+"/tokens.json", "passphrase")
	if err != nil {
		t.Fatal(err)
	}
	enc, err := encryptWith(ts.key, []byte("secret-token-payload"))
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
