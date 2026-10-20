package core

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/pbkdf2"
)

// TokenStore manages encrypted OAuth tokens and static credentials at rest.
// All secrets are sealed under the same master key derived from -token-pass
// (issue #184), so -token-new-pass rekey covers both OAuth tokens and
// service-connection static credentials.
type TokenStore struct {
	// mu protects Tokens / Secrets / salt / key / path / loadUnsafe.
	// Do not hold it during disk I/O.
	mu sync.RWMutex
	// persistMu serializes whole-store encrypt+write so concurrent Save/Delete
	// cannot interleave stale snapshots onto disk.
	persistMu sync.Mutex
	path      string
	salt      []byte
	key       []byte
	// kdfIterations is the PBKDF2 cost that produced key from salt.
	kdfIterations int
	// preferredIterations is the PBKDF2 cost used for new key material
	// (new stores and ChangePassphrase). Never lower an existing file's cost.
	preferredIterations int
	// loadUnsafe is set when some on-disk entries could not be decrypted or
	// parsed. persist refuses to rewrite the file so we never destroy tokens
	// we failed to read (issue #100).
	loadUnsafe bool
	Tokens     map[string]*OAuthToken `json:"tokens"`
	// Secrets holds static credentials (Immich/YNAB/Tailscale Admin API keys
	// and similar) keyed by namespaced secret id. Encrypted like Tokens.
	Secrets map[string]string `json:"secrets,omitempty"`
}

// OAuthToken holds the credentials for a single service.
type OAuthToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	Expiry       time.Time `json:"expiry"`
	Scopes       []string  `json:"scopes"`
}

// tokenCiphertext is one encrypted token entry.
// A non-empty Salt means the ciphertext was sealed under an HKDF-derived
// entry key (format v2). Empty Salt means the store master key (format v1).
type tokenCiphertext struct {
	Salt string `json:"salt,omitempty"`
	Data string `json:"data"`
}

// tokensByService accepts both legacy string ciphertexts (v1) and structured
// entries (v2). Marshal always writes the v2 object form.
type tokensByService map[string]tokenCiphertext

func (t *tokensByService) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*t = tokensByService{}
		return nil
	}
	var v2 map[string]tokenCiphertext
	if err := json.Unmarshal(data, &v2); err == nil {
		*t = v2
		return nil
	}
	var v1 map[string]string
	if err := json.Unmarshal(data, &v1); err == nil {
		m := make(tokensByService, len(v1))
		for id, enc := range v1 {
			m[id] = tokenCiphertext{Data: enc}
		}
		*t = m
		return nil
	}
	return fmt.Errorf("tokens: expected map of ciphertext strings (v1) or objects (v2)")
}

func (t tokensByService) MarshalJSON() ([]byte, error) {
	if t == nil {
		t = tokensByService{}
	}
	return json.Marshal(map[string]tokenCiphertext(t))
}

// tokenFile is the on-disk format.
//
//	version 1 (legacy): file-level salt, implicit PBKDF2 iterations (100k),
//	                    tokens are map[string]string sealed under the master key.
//	version 2: explicit kdf + iterations, file-level salt, per-entry
//	           salts with HKDF-derived entry keys.
//	version 3 (current): same as v2 plus a secrets map for static credentials
//	                     from service_connections (issue #184).
//
// All load; every write is version 3 (issue #101 / #184).
type tokenFile struct {
	Version    int    `json:"version"`
	KDF        string `json:"kdf,omitempty"`
	Iterations int    `json:"iterations,omitempty"`
	Salt       string `json:"salt"`
	// KeyCheck seals a known plaintext with the store master key so a wrong
	// passphrase is distinguishable from an empty store (issue #100).
	// Empty on legacy files written before this field existed.
	KeyCheck string          `json:"key_check,omitempty"`
	Tokens   tokensByService `json:"tokens"`
	Secrets  tokensByService `json:"secrets,omitempty"`
}

const (
	tokenVersionV1 = 1
	tokenVersionV2 = 2
	tokenVersion   = 3
	// pbkdf2IterLegacy is the implicit cost of version 1 files.
	pbkdf2IterLegacy = 100_000
	// pbkdf2IterDefault is current OWASP guidance for PBKDF2-HMAC-SHA256.
	pbkdf2IterDefault = 600_000
	pbkdf2KeyLen      = 32 // AES-256
	tokenFilePerms    = 0600
	kdfPBKDF2SHA256   = "pbkdf2-sha256"
	entryHKDFInfo     = "aegis-token-entry-v2"
	// keyCheckPlaintext is sealed under the derived key on every write and
	// verified on load. It is not a secret — only a wrong-key detector.
	keyCheckPlaintext = "aegis-token-key-check-v1"

	// Static-credential secret keys (issue #184). Namespaced so they cannot
	// collide with OAuth service connection IDs.
	secretKeyServiceHeadersPrefix = "static:service:"
	secretKeyServiceHeadersSuffix = ":headers"
	secretKeyTailscaleAPIKey      = "static:tailscale:api_key"
)

// ServiceHeadersSecretKey is the TokenStore secret id for a service's static
// HTTP headers (JSON object of header name → value).
func ServiceHeadersSecretKey(serviceID string) string {
	return secretKeyServiceHeadersPrefix + serviceID + secretKeyServiceHeadersSuffix
}

// TailscaleAPIKeySecretKey is the TokenStore secret id for the Tailscale
// Admin API key.
func TailscaleAPIKeySecretKey() string {
	return secretKeyTailscaleAPIKey
}

// errPassphrase is returned when the token file exists but the supplied
// passphrase cannot unlock it. Callers must not persist over the file.
var errPassphrase = errors.New("token passphrase incorrect (or token file corrupt); refusing to load token store")

// NewTokenStore loads or creates a token store encrypted with the given passphrase.
// New files use the current default PBKDF2 cost; existing files keep the KDF
// parameters recorded on disk (or the legacy v1 cost) until rekeyed.
// If a token file exists but the passphrase cannot unlock it, returns an error
// and never hands back a store that would overwrite the file on the next Save.
func NewTokenStore(path, passphrase string) (*TokenStore, error) {
	return newTokenStore(path, passphrase, pbkdf2IterDefault)
}

// newTokenStore is NewTokenStore with an explicit preferred PBKDF2 cost for
// new key material. Used by tests to keep the suite fast; production passes
// pbkdf2IterDefault.
func newTokenStore(path, passphrase string, preferredIterations int) (*TokenStore, error) {
	if preferredIterations < 1 {
		return nil, fmt.Errorf("pbkdf2 iterations must be positive, got %d", preferredIterations)
	}
	ts := &TokenStore{
		path:                path,
		Tokens:              make(map[string]*OAuthToken),
		preferredIterations: preferredIterations,
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// New store — generate fresh salt at the preferred cost.
			ts.salt = make([]byte, 16)
			if _, err := io.ReadFull(rand.Reader, ts.salt); err != nil {
				return nil, fmt.Errorf("generating salt: %w", err)
			}
			ts.kdfIterations = preferredIterations
			ts.key = deriveKey(passphrase, ts.salt, ts.kdfIterations)
			slog.Info("created new token store",
				"path", path,
				"kdf", kdfPBKDF2SHA256,
				"iterations", ts.kdfIterations,
			)
			return ts, nil
		}
		return nil, fmt.Errorf("reading token file: %w", err)
	}

	var tf tokenFile
	if err := json.Unmarshal(data, &tf); err != nil {
		return nil, fmt.Errorf("parsing token file: %w", err)
	}
	iterations, err := fileKDFIterations(tf)
	if err != nil {
		return nil, err
	}
	salt, err := base64.StdEncoding.DecodeString(tf.Salt)
	if err != nil {
		return nil, fmt.Errorf("decoding salt: %w", err)
	}
	ts.salt = salt
	ts.kdfIterations = iterations
	ts.key = deriveKey(passphrase, ts.salt, ts.kdfIterations)

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
	for svcID, entry := range tf.Tokens {
		decrypted, err := ts.decryptEntry(entry)
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

	ts.Secrets = make(map[string]string, len(tf.Secrets))
	for key, entry := range tf.Secrets {
		decrypted, err := ts.decryptEntry(entry)
		if err != nil {
			decryptFailures++
			slog.Warn("failed to decrypt secret, skipping", "secret", key, "error", err)
			continue
		}
		ts.Secrets[key] = string(decrypted)
	}

	// Legacy file, no key check, and every entry failed to decrypt:
	// almost certainly the wrong passphrase. Refuse rather than look empty.
	totalOnDisk := len(tf.Tokens) + len(tf.Secrets)
	if tf.KeyCheck == "" && totalOnDisk > 0 && decryptFailures == totalOnDisk {
		slog.Error("all token-store entries failed to decrypt", "path", path, "entries", totalOnDisk)
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
			"loaded_tokens", len(ts.Tokens),
			"loaded_secrets", len(ts.Secrets),
			"on_disk", totalOnDisk)
	}

	slog.Info("loaded token store",
		"path", path,
		"services", len(ts.Tokens),
		"secrets", len(ts.Secrets),
		"version", tf.Version,
		"kdf", kdfPBKDF2SHA256,
		"iterations", ts.kdfIterations,
	)
	return ts, nil
}

// fileKDFIterations returns the PBKDF2 cost recorded in the file, or the
// legacy v1 default. Rejects unknown KDF names so a future algorithm cannot
// be silently treated as PBKDF2.
func fileKDFIterations(tf tokenFile) (int, error) {
	switch tf.Version {
	case tokenVersionV1:
		if tf.Iterations != 0 && tf.Iterations != pbkdf2IterLegacy {
			return 0, fmt.Errorf("legacy token file with unexpected iterations: %d", tf.Iterations)
		}
		if tf.KDF != "" && tf.KDF != kdfPBKDF2SHA256 {
			return 0, fmt.Errorf("unsupported token kdf %q", tf.KDF)
		}
		return pbkdf2IterLegacy, nil
	case tokenVersionV2, tokenVersion:
		if tf.KDF != "" && tf.KDF != kdfPBKDF2SHA256 {
			return 0, fmt.Errorf("unsupported token kdf %q", tf.KDF)
		}
		if tf.Iterations < 1 {
			return 0, fmt.Errorf("token file missing kdf iterations")
		}
		return tf.Iterations, nil
	default:
		return 0, fmt.Errorf("unsupported token file version: %d", tf.Version)
	}
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

// GetSecret returns a copy of a static credential, or "" when unset.
func (ts *TokenStore) GetSecret(key string) string {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	if ts.Secrets == nil {
		return ""
	}
	return ts.Secrets[key]
}

// HasSecret reports whether a static credential is stored (presence only).
func (ts *TokenStore) HasSecret(key string) bool {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	_, ok := ts.Secrets[key]
	return ok
}

// SetSecret stores a static credential under the encrypted token store
// (issue #184). An empty value deletes the key.
func (ts *TokenStore) SetSecret(key, value string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("secret key is required")
	}
	if err := ts.ensurePersistable(); err != nil {
		return err
	}
	ts.mu.Lock()
	if ts.Secrets == nil {
		ts.Secrets = make(map[string]string)
	}
	if value == "" {
		delete(ts.Secrets, key)
	} else {
		ts.Secrets[key] = value
	}
	ts.mu.Unlock()
	return ts.persist()
}

// DeleteSecret removes a static credential from the encrypted store.
func (ts *TokenStore) DeleteSecret(key string) error {
	if err := ts.ensurePersistable(); err != nil {
		return err
	}
	ts.mu.Lock()
	delete(ts.Secrets, key)
	ts.mu.Unlock()
	return ts.persist()
}

// SecretKeys returns secret ids currently stored (for migration logging).
func (ts *TokenStore) SecretKeys() []string {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	out := make([]string, 0, len(ts.Secrets))
	for k := range ts.Secrets {
		out = append(out, k)
	}
	return out
}

// ChangePassphrase re-encrypts every token under next with a fresh master salt
// and the store's preferred PBKDF2 cost (never lower than the cost already in
// use). Passing the current passphrase is allowed: it still rotates the salt
// and raises KDF parameters on legacy files.
func (ts *TokenStore) ChangePassphrase(next string) error {
	if next == "" {
		return fmt.Errorf("new token passphrase must not be empty")
	}
	if err := ts.ensurePersistable(); err != nil {
		return err
	}

	newSalt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, newSalt); err != nil {
		return fmt.Errorf("generating salt: %w", err)
	}
	newIter := ts.preferredIterations
	ts.mu.RLock()
	if ts.kdfIterations > newIter {
		newIter = ts.kdfIterations
	}
	ts.mu.RUnlock()
	newKey := deriveKey(next, newSalt, newIter)

	ts.mu.Lock()
	ts.salt = newSalt
	ts.key = newKey
	ts.kdfIterations = newIter
	ts.mu.Unlock()

	if err := ts.persist(); err != nil {
		return fmt.Errorf("persisting rekeyed token store: %w", err)
	}
	slog.Info("token store passphrase changed",
		"path", ts.path,
		"services", len(ts.Tokens),
		"iterations", newIter,
	)
	return nil
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

// persist writes the encrypted token store to disk as format v2. Snapshots
// under ts.mu, then encrypts/writes under persistMu so concurrent readers are
// not blocked by AES + filesystem work.
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
	secretPlains := make([]plainTok, 0, len(ts.Secrets))
	for key, val := range ts.Secrets {
		secretPlains = append(secretPlains, plainTok{id: key, data: []byte(val)})
	}
	key := ts.key
	iterations := ts.kdfIterations
	ts.mu.RUnlock()

	keyCheck, err := encryptWith(key, []byte(keyCheckPlaintext))
	if err != nil {
		return fmt.Errorf("encrypting key check: %w", err)
	}
	tf := tokenFile{
		Version:    tokenVersion,
		KDF:        kdfPBKDF2SHA256,
		Iterations: iterations,
		Salt:       saltB64,
		KeyCheck:   keyCheck,
		Tokens:     make(tokensByService, len(plains)),
	}
	for _, p := range plains {
		entry, err := encryptEntry(key, p.data)
		if err != nil {
			return fmt.Errorf("encrypting token for %s: %w", p.id, err)
		}
		tf.Tokens[p.id] = entry
	}
	if len(secretPlains) > 0 {
		tf.Secrets = make(tokensByService, len(secretPlains))
		for _, p := range secretPlains {
			entry, err := encryptEntry(key, p.data)
			if err != nil {
				return fmt.Errorf("encrypting secret %s: %w", p.id, err)
			}
			tf.Secrets[p.id] = entry
		}
	}

	data, err := json.MarshalIndent(tf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling token file: %w", err)
	}
	return atomicWriteFile(path, data, tokenFilePerms)
}

// encryptEntry seals plaintext under a fresh HKDF-derived entry key.
func encryptEntry(masterKey, plaintext []byte) (tokenCiphertext, error) {
	entrySalt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, entrySalt); err != nil {
		return tokenCiphertext{}, err
	}
	entryKey, err := deriveEntryKey(masterKey, entrySalt)
	if err != nil {
		return tokenCiphertext{}, err
	}
	enc, err := encryptWith(entryKey, plaintext)
	if err != nil {
		return tokenCiphertext{}, err
	}
	return tokenCiphertext{
		Salt: base64.StdEncoding.EncodeToString(entrySalt),
		Data: enc,
	}, nil
}

// decryptEntry opens a token ciphertext. Entries with a salt use an
// HKDF-derived entry key (v2); saltless entries use the master key (v1).
func (ts *TokenStore) decryptEntry(entry tokenCiphertext) ([]byte, error) {
	key := ts.key
	if entry.Salt != "" {
		salt, err := base64.StdEncoding.DecodeString(entry.Salt)
		if err != nil {
			return nil, fmt.Errorf("decoding entry salt: %w", err)
		}
		entryKey, err := deriveEntryKey(ts.key, salt)
		if err != nil {
			return nil, err
		}
		key = entryKey
	}
	return decryptWith(key, entry.Data)
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

func decryptWith(key []byte, encoded string) ([]byte, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
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

func (ts *TokenStore) encrypt(plaintext []byte) (string, error) {
	return encryptWith(ts.key, plaintext)
}

func (ts *TokenStore) decrypt(encoded string) ([]byte, error) {
	return decryptWith(ts.key, encoded)
}

func deriveKey(passphrase string, salt []byte, iterations int) []byte {
	return pbkdf2.Key([]byte(passphrase), salt, iterations, pbkdf2KeyLen, sha256.New)
}

// deriveEntryKey stretches the master key into a per-entry key. Cheap relative
// to PBKDF2, so each token can carry its own salt without re-running the KDF.
func deriveEntryKey(masterKey, entrySalt []byte) ([]byte, error) {
	return hkdf.Key(sha256.New, masterKey, entrySalt, entryHKDFInfo, pbkdf2KeyLen)
}
