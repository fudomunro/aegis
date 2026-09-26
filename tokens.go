package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/pbkdf2"
)

// TokenStore manages encrypted OAuth tokens at rest.
type TokenStore struct {
	// mu protects Tokens / salt / key / path / loadUnsafe. Do not hold it during disk I/O.
	mu sync.RWMutex
	// persistMu serializes whole-store encrypt+write so concurrent Save/Delete
	// cannot interleave stale snapshots onto disk.
	persistMu sync.Mutex
	path      string
	salt      []byte
	key       []byte
	// loadUnsafe is set when some on-disk entries could not be decrypted or
	// parsed. persist refuses to rewrite the file so we never destroy tokens
	// we failed to read (issue #100).
	loadUnsafe bool
	Tokens     map[string]*OAuthToken `json:"tokens"`
}

// OAuthToken holds the credentials for a single service.
type OAuthToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	Expiry       time.Time `json:"expiry"`
	Scopes       []string  `json:"scopes"`
}

// tokenFile is the on-disk format.
type tokenFile struct {
	Version int    `json:"version"`
	Salt    string `json:"salt"`
	// KeyCheck seals a known plaintext with the store key so a wrong
	// passphrase is distinguishable from an empty store (issue #100).
	// Empty on legacy files written before this field existed.
	KeyCheck string            `json:"key_check,omitempty"`
	Tokens   map[string]string `json:"tokens"` // service_id -> base64(encrypted(json))
}

const (
	tokenVersion   = 1
	pbkdf2Iter     = 100_000
	pbkdf2KeyLen   = 32 // AES-256
	tokenFilePerms = 0600
	// keyCheckPlaintext is sealed under the derived key on every write and
	// verified on load. It is not a secret — only a wrong-key detector.
	keyCheckPlaintext = "aegis-token-key-check-v1"
)

// errPassphrase is returned when the token file exists but the supplied
// passphrase cannot unlock it. Callers must not persist over the file.
var errPassphrase = errors.New("token passphrase incorrect (or token file corrupt); refusing to load token store")

// NewTokenStore loads or creates a token store encrypted with the given passphrase.
// If a token file exists but the passphrase cannot unlock it, returns an error
// and never hands back a store that would overwrite the file on the next Save.
func NewTokenStore(path, passphrase string) (*TokenStore, error) {
	ts := &TokenStore{path: path, Tokens: make(map[string]*OAuthToken)}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// New store — generate fresh salt.
			ts.salt = make([]byte, 16)
			if _, err := io.ReadFull(rand.Reader, ts.salt); err != nil {
				return nil, fmt.Errorf("generating salt: %w", err)
			}
			ts.key = deriveKey(passphrase, ts.salt)
			slog.Info("created new token store", "path", path)
			return ts, nil
		}
		return nil, fmt.Errorf("reading token file: %w", err)
	}

	var tf tokenFile
	if err := json.Unmarshal(data, &tf); err != nil {
		return nil, fmt.Errorf("parsing token file: %w", err)
	}
	if tf.Version != tokenVersion {
		return nil, fmt.Errorf("unsupported token file version: %d", tf.Version)
	}

	salt, err := base64.StdEncoding.DecodeString(tf.Salt)
	if err != nil {
		return nil, fmt.Errorf("decoding salt: %w", err)
	}
	ts.salt = salt
	ts.key = deriveKey(passphrase, ts.salt)

	// Prefer the sealed key check when present: it detects a wrong passphrase
	// even for an empty store. Legacy files fall through to the all-fail rule.
	if tf.KeyCheck != "" {
		plain, err := ts.decrypt(tf.KeyCheck)
		if err != nil || string(plain) != keyCheckPlaintext {
			slog.Error("token key check failed", "path", path, "error", err)
			return nil, errPassphrase
		}
	}

	decryptFailures := 0
	parseFailures := 0
	for svcID, encB64 := range tf.Tokens {
		decrypted, err := ts.decrypt(encB64)
		if err != nil {
			decryptFailures++
			slog.Warn("failed to decrypt token, skipping", "service", svcID, "error", err)
			continue
		}
		var tok OAuthToken
		if err := json.Unmarshal(decrypted, &tok); err != nil {
			parseFailures++
			slog.Warn("failed to parse token, skipping", "service", svcID, "error", err)
			continue
		}
		ts.Tokens[svcID] = &tok
	}

	// Legacy file, no key check, and every entry failed to decrypt:
	// almost certainly the wrong passphrase. Refuse rather than look empty.
	if tf.KeyCheck == "" && len(tf.Tokens) > 0 && decryptFailures == len(tf.Tokens) {
		slog.Error("all tokens failed to decrypt", "path", path, "services", len(tf.Tokens))
		return nil, errPassphrase
	}

	// Some entries were unreadable. Keep what we can load in memory, but never
	// persist — a rewrite would drop the undecrypted ciphertext on disk.
	if decryptFailures > 0 || parseFailures > 0 {
		ts.loadUnsafe = true
		slog.Error("token store loaded incompletely; persist is disabled",
			"path", path,
			"decrypt_failures", decryptFailures,
			"parse_failures", parseFailures,
			"loaded", len(ts.Tokens),
			"on_disk", len(tf.Tokens))
	}

	slog.Info("loaded token store", "path", path, "services", len(ts.Tokens))
	return ts, nil
}

// Get returns a copy of the token for a service, or nil if not stored.
// The copy is safe for callers to read after the store lock is released.
func (ts *TokenStore) Get(serviceID string) *OAuthToken {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	tok := ts.Tokens[serviceID]
	if tok == nil {
		return nil
	}
	cp := *tok
	if tok.Scopes != nil {
		cp.Scopes = append([]string(nil), tok.Scopes...)
	}
	return &cp
}

// Save stores a token for a service. The token is copied; encrypt/persist
// runs without holding the read path lock.
func (ts *TokenStore) Save(serviceID string, tok *OAuthToken) error {
	if tok == nil {
		return fmt.Errorf("nil token for %s", serviceID)
	}
	if err := ts.ensurePersistable(); err != nil {
		return err
	}
	cp := *tok
	if tok.Scopes != nil {
		cp.Scopes = append([]string(nil), tok.Scopes...)
	}

	ts.mu.Lock()
	ts.Tokens[serviceID] = &cp
	ts.mu.Unlock()

	return ts.persist()
}

// Delete removes a token for a service.
func (ts *TokenStore) Delete(serviceID string) error {
	if err := ts.ensurePersistable(); err != nil {
		return err
	}
	ts.mu.Lock()
	delete(ts.Tokens, serviceID)
	ts.mu.Unlock()

	return ts.persist()
}

// ensurePersistable returns an error when the store was loaded with entries
// we could not decrypt or parse. Rewriting then would drop those ciphertexts.
func (ts *TokenStore) ensurePersistable() error {
	ts.mu.RLock()
	unsafe := ts.loadUnsafe
	ts.mu.RUnlock()
	if unsafe {
		return fmt.Errorf("refusing to modify token store: on-disk entries failed to load (wrong passphrase or corrupt file); fix %s before saving", ts.path)
	}
	return nil
}

// IsExpired returns true if the token is expired or will expire within the grace period.
func (t *OAuthToken) IsExpired(grace time.Duration) bool {
	return time.Now().Add(grace).After(t.Expiry)
}

// Status returns a summary of all stored tokens (without revealing secrets).
// Keys are provider connection IDs.
func (ts *TokenStore) Status() map[string]TokenStatus {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	status := make(map[string]TokenStatus)
	for svcID, tok := range ts.Tokens {
		status[svcID] = TokenStatus{
			HasToken:   true,
			Expired:    tok.IsExpired(0),
			ExpiresIn:  time.Until(tok.Expiry).Truncate(time.Second).String(),
			HasRefresh: tok.RefreshToken != "",
			Scopes:     tok.Scopes,
		}
	}
	return status
}

type TokenStatus struct {
	HasToken   bool     `json:"has_token"`
	Expired    bool     `json:"expired"`
	ExpiresIn  string   `json:"expires_in"`
	HasRefresh bool     `json:"has_refresh"`
	Scopes     []string `json:"scopes"`
}

// persist writes the encrypted token store to disk. Snapshots under ts.mu,
// then encrypts/writes under persistMu so concurrent readers are not blocked
// by AES + filesystem work.
// Skips disk writes if no path is configured (in-memory store).
// Refuses to write if the store was loaded with unreadable entries (issue #100).
// Uses atomic temp+rename so a crash cannot leave a truncated tokens.json (issue #99).
func (ts *TokenStore) persist() error {
	ts.persistMu.Lock()
	defer ts.persistMu.Unlock()

	ts.mu.RLock()
	path := ts.path
	if path == "" {
		ts.mu.RUnlock()
		return nil
	}
	if ts.loadUnsafe {
		ts.mu.RUnlock()
		return fmt.Errorf("refusing to persist token store: on-disk entries failed to load; fix %s before saving", path)
	}
	saltB64 := base64.StdEncoding.EncodeToString(ts.salt)
	type plainTok struct {
		id   string
		data []byte
	}
	plains := make([]plainTok, 0, len(ts.Tokens))
	for svcID, tok := range ts.Tokens {
		plain, err := json.Marshal(tok)
		if err != nil {
			ts.mu.RUnlock()
			return fmt.Errorf("marshaling token for %s: %w", svcID, err)
		}
		plains = append(plains, plainTok{id: svcID, data: plain})
	}
	key := ts.key
	ts.mu.RUnlock()

	keyCheck, err := encryptWith(key, []byte(keyCheckPlaintext))
	if err != nil {
		return fmt.Errorf("encrypting key check: %w", err)
	}
	tf := tokenFile{
		Version:  tokenVersion,
		Salt:     saltB64,
		KeyCheck: keyCheck,
		Tokens:   make(map[string]string, len(plains)),
	}
	for _, p := range plains {
		encrypted, err := encryptWith(key, p.data)
		if err != nil {
			return fmt.Errorf("encrypting token for %s: %w", p.id, err)
		}
		tf.Tokens[p.id] = encrypted
	}

	data, err := json.MarshalIndent(tf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling token file: %w", err)
	}
	return atomicWriteFile(path, data, tokenFilePerms)
}

func encryptWith(key, plaintext []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func (ts *TokenStore) encrypt(plaintext []byte) (string, error) {
	return encryptWith(ts.key, plaintext)
}

func (ts *TokenStore) decrypt(encoded string) ([]byte, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(ts.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	return gcm.Open(nil, nonce, ciphertext, nil)
}

func deriveKey(passphrase string, salt []byte) []byte {
	return pbkdf2.Key([]byte(passphrase), salt, pbkdf2Iter, pbkdf2KeyLen, sha256.New)
}
