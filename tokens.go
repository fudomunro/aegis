package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
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
	mu     sync.RWMutex
	path   string
	salt   []byte
	key    []byte
	Tokens map[string]*OAuthToken `json:"tokens"`
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
	Version int               `json:"version"`
	Salt    string            `json:"salt"`
	Tokens  map[string]string `json:"tokens"` // service_id -> base64(encrypted(json))
}

const (
	tokenVersion   = 1
	pbkdf2Iter     = 100_000
	pbkdf2KeyLen   = 32 // AES-256
	tokenFilePerms = 0600
)

// NewTokenStore loads or creates a token store encrypted with the given passphrase.
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

	for svcID, encB64 := range tf.Tokens {
		decrypted, err := ts.decrypt(encB64)
		if err != nil {
			slog.Warn("failed to decrypt token, skipping", "service", svcID, "error", err)
			continue
		}
		var tok OAuthToken
		if err := json.Unmarshal(decrypted, &tok); err != nil {
			slog.Warn("failed to parse token, skipping", "service", svcID, "error", err)
			continue
		}
		ts.Tokens[svcID] = &tok
	}

	slog.Info("loaded token store", "path", path, "services", len(ts.Tokens))
	return ts, nil
}

// Get returns the token for a service, or nil if not stored.
func (ts *TokenStore) Get(serviceID string) *OAuthToken {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.Tokens[serviceID]
}

// Save stores a token for a service, encrypting and writing to disk.
func (ts *TokenStore) Save(serviceID string, tok *OAuthToken) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	ts.Tokens[serviceID] = tok
	return ts.persist()
}

// Delete removes a token for a service.
func (ts *TokenStore) Delete(serviceID string) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	delete(ts.Tokens, serviceID)
	return ts.persist()
}

// IsExpired returns true if the token is expired or will expire within the grace period.
func (t *OAuthToken) IsExpired(grace time.Duration) bool {
	return time.Now().Add(grace).After(t.Expiry)
}

// Status returns a summary of all stored tokens (without revealing secrets).
// Keys are provider connection IDs after issue #26 (legacy files may still
// use service IDs until FoldIntoConnections runs).
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

// FoldIntoConnections rewrites legacy per-service token keys into shared
// provider connection IDs (issue #26). Best-effort: keeps the strongest
// token per connection (refresh preferred, then more scopes, then later
// expiry) and unions recorded scopes. Call after config is available.
func (ts *TokenStore) FoldIntoConnections(cfg *Config) {
	if ts == nil || cfg == nil {
		return
	}

	type group struct {
		keys   []string
		best   string
		tok    *OAuthToken
		scopes map[string]bool
	}
	groups := make(map[string]*group)

	ts.mu.Lock()
	defer ts.mu.Unlock()

	for key, tok := range ts.Tokens {
		connID := cfg.connectionIDForService(key)
		g := groups[connID]
		if g == nil {
			g = &group{scopes: make(map[string]bool)}
			groups[connID] = g
		}
		g.keys = append(g.keys, key)
		for _, sc := range tok.Scopes {
			g.scopes[sc] = true
		}
		if g.tok == nil || betterFoldToken(tok, g.tok) {
			g.best = key
			g.tok = tok
		}
	}

	changed := false
	for connID, g := range groups {
		if len(g.keys) == 1 && g.keys[0] == connID {
			continue
		}
		picked := *g.tok
		if len(g.scopes) > 0 {
			union := make([]string, 0, len(g.scopes))
			for sc := range g.scopes {
				union = append(union, sc)
			}
			// Prefer recorded scopes on the winning token when present.
			if len(picked.Scopes) > 0 {
				picked.Scopes = unionScopes(picked.Scopes, union)
			} else {
				picked.Scopes = union
			}
		}
		for _, key := range g.keys {
			delete(ts.Tokens, key)
		}
		ts.Tokens[connID] = &picked
		changed = true
		slog.Info("folded OAuth tokens into provider connection",
			"connection", connID,
			"sources", g.keys,
			"scopes", picked.Scopes,
		)
	}

	if changed && ts.path != "" {
		if err := ts.persist(); err != nil {
			slog.Error("failed to persist folded token store", "error", err)
		}
	}
}

// betterFoldToken reports whether a should win over b when folding tokens
// into one provider connection.
func betterFoldToken(a, b *OAuthToken) bool {
	if a == nil {
		return false
	}
	if b == nil {
		return true
	}
	aRefresh, bRefresh := a.RefreshToken != "", b.RefreshToken != ""
	if aRefresh != bRefresh {
		return aRefresh
	}
	if len(a.Scopes) != len(b.Scopes) {
		return len(a.Scopes) > len(b.Scopes)
	}
	return a.Expiry.After(b.Expiry)
}

type TokenStatus struct {
	HasToken   bool     `json:"has_token"`
	Expired    bool     `json:"expired"`
	ExpiresIn  string   `json:"expires_in"`
	HasRefresh bool     `json:"has_refresh"`
	Scopes     []string `json:"scopes"`
}

// persist writes the encrypted token store to disk. Must be called with ts.mu held.
// Skips disk writes if no path is configured (in-memory store).
func (ts *TokenStore) persist() error {
	if ts.path == "" {
		return nil
	}
	tf := tokenFile{
		Version: tokenVersion,
		Salt:    base64.StdEncoding.EncodeToString(ts.salt),
		Tokens:  make(map[string]string),
	}

	for svcID, tok := range ts.Tokens {
		plain, err := json.Marshal(tok)
		if err != nil {
			return fmt.Errorf("marshaling token for %s: %w", svcID, err)
		}
		encrypted, err := ts.encrypt(plain)
		if err != nil {
			return fmt.Errorf("encrypting token for %s: %w", svcID, err)
		}
		tf.Tokens[svcID] = encrypted
	}

	data, err := json.MarshalIndent(tf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling token file: %w", err)
	}
	return os.WriteFile(ts.path, data, tokenFilePerms)
}

func (ts *TokenStore) encrypt(plaintext []byte) (string, error) {
	block, err := aes.NewCipher(ts.key)
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
