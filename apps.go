package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// appsFileVersion is the on-disk schema version for apps.json.
const appsFileVersion = 1

// appTokenPrefix marks third-party app tokens so they cannot be confused
// with the shared API key in logs or config.
const appTokenPrefix = "aeg_app_"

// App is a registered third-party client (issue #131). Tokens are stored as
// SHA-256 hashes; the plaintext is returned once at create/rotate.
type App struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// TokenHash is hex(sha256(token)). Never the plaintext.
	TokenHash string `json:"token_hash"`
	// TokenHint is a short non-secret prefix for admin UIs (first 12 chars).
	TokenHint string `json:"token_hint,omitempty"`
	// Scopes restrict which library data types the app may read, using the
	// same access levels as service permissions (none / freebusy / read /
	// write / read-write). freebusy is events-only.
	Scopes Permissions `json:"scopes"`
	// CreatedAt / UpdatedAt are informational.
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Disabled blocks authentication without deleting the record.
	Disabled bool `json:"disabled,omitempty"`
}

// appsFile is the on-disk format under the data directory. Mode 0600: it
// holds token hashes (not secrets by themselves, but treat as sensitive).
type appsFile struct {
	Version int   `json:"version"`
	Apps    []App `json:"apps"`
}

// AppStore is a mutex-guarded registry of third-party app credentials.
type AppStore struct {
	mu     sync.RWMutex
	path   string
	byID   map[string]*App
	byHash map[string]*App // hex sha256 → app
}

// NewAppStore loads apps.json (empty when missing). path is required in
// production; tests may pass a temp path.
func NewAppStore(path string) (*AppStore, error) {
	s := &AppStore{
		path:   path,
		byID:   make(map[string]*App),
		byHash: make(map[string]*App),
	}
	if err := s.reload(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *AppStore) reload() error {
	if s.path == "" {
		return fmt.Errorf("apps path is required")
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.mu.Lock()
			s.byID = make(map[string]*App)
			s.byHash = make(map[string]*App)
			s.mu.Unlock()
			return nil
		}
		return fmt.Errorf("reading apps file: %w", err)
	}
	var f appsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("parsing apps file: %w", err)
	}
	byID := make(map[string]*App, len(f.Apps))
	byHash := make(map[string]*App, len(f.Apps))
	for i := range f.Apps {
		app := f.Apps[i]
		if app.ID == "" {
			return fmt.Errorf("apps file: entry %d missing id", i)
		}
		if err := ValidatePermissions(app.Scopes); err != nil {
			return fmt.Errorf("apps file app %q: %w", app.ID, err)
		}
		cp := app
		byID[cp.ID] = &cp
		if cp.TokenHash != "" {
			byHash[strings.ToLower(cp.TokenHash)] = &cp
		}
	}
	s.mu.Lock()
	s.byID = byID
	s.byHash = byHash
	s.mu.Unlock()
	return nil
}

func (s *AppStore) persistLocked() error {
	if s.path == "" {
		return fmt.Errorf("apps path is empty; cannot save")
	}
	ids := make([]string, 0, len(s.byID))
	for id := range s.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := appsFile{Version: appsFileVersion, Apps: make([]App, 0, len(ids))}
	for _, id := range ids {
		out.Apps = append(out.Apps, *s.byID[id])
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	// 0600: app token hashes live here.
	return atomicWriteFile(s.path, data, 0o600)
}

// generateAppToken returns a new high-entropy app token (plaintext) and its
// storage hash/hint.
func generateAppToken() (token, hash, hint string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", "", err
	}
	token = appTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	return token, hashAppToken(token), appTokenHint(token), nil
}

func hashAppToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func appTokenHint(token string) string {
	if len(token) <= 12 {
		return token
	}
	return token[:12]
}

// AppCreateRequest is the admin body for registering a third-party app.
type AppCreateRequest struct {
	ID     string     `json:"id"`
	Name   string     `json:"name"`
	Scopes Permissions `json:"scopes"`
}

// AppUpdateRequest patches name/scopes/disabled. Nil fields are left alone.
// Scopes, when present, are normalized and default to none for unlisted types.
type AppUpdateRequest struct {
	Name     *string      `json:"name,omitempty"`
	Scopes   *Permissions `json:"scopes,omitempty"`
	Disabled *bool        `json:"disabled,omitempty"`
}

// AppView is the admin/API projection (never includes token material).
type AppView struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	TokenHint string            `json:"token_hint,omitempty"`
	Scopes    map[string]string `json:"scopes"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	Disabled  bool              `json:"disabled,omitempty"`
}

func appView(a *App) AppView {
	scopes := a.Scopes.Clone()
	if scopes == nil {
		scopes = map[string]string{}
	}
	return AppView{
		ID:        a.ID,
		Name:      a.Name,
		TokenHint: a.TokenHint,
		Scopes:    scopes,
		CreatedAt: a.CreatedAt,
		UpdatedAt: a.UpdatedAt,
		Disabled:  a.Disabled,
	}
}

// List returns apps sorted by id (admin view).
func (s *AppStore) List() []AppView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.byID))
	for id := range s.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]AppView, 0, len(ids))
	for _, id := range ids {
		out = append(out, appView(s.byID[id]))
	}
	return out
}

// Get returns one app view.
func (s *AppStore) Get(id string) (AppView, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.byID[id]
	if !ok {
		return AppView{}, false
	}
	return appView(a), true
}

// Create registers an app and returns the one-time plaintext token.
func (s *AppStore) Create(req AppCreateRequest) (AppView, string, error) {
	id := strings.TrimSpace(req.ID)
	if id == "" {
		return AppView{}, "", fmt.Errorf("app id is required")
	}
	if err := validateAppID(id); err != nil {
		return AppView{}, "", err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = id
	}
	scopes := req.Scopes
	if scopes == nil {
		scopes = Permissions{PermissionDefaultKey: AccessNone}
	}
	if err := ValidatePermissions(scopes); err != nil {
		return AppView{}, "", err
	}
	// Fail closed: unspecified types deny unless explicitly allowed.
	normalized := Permissions{}
	for k, v := range scopes {
		normalized[k] = normalizeAccess(v)
	}
	if _, ok := normalized[PermissionDefaultKey]; !ok {
		normalized[PermissionDefaultKey] = AccessNone
	}

	token, hash, hint, err := generateAppToken()
	if err != nil {
		return AppView{}, "", fmt.Errorf("generating app token: %w", err)
	}
	now := time.Now().UTC()
	app := &App{
		ID:        id,
		Name:      name,
		TokenHash: hash,
		TokenHint: hint,
		Scopes:    normalized,
		CreatedAt: now,
		UpdatedAt: now,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.byID[id]; exists {
		return AppView{}, "", fmt.Errorf("app %q already exists", id)
	}
	for _, existing := range s.byID {
		if existing.TokenHash == hash {
			return AppView{}, "", fmt.Errorf("token collision")
		}
	}
	s.byID[id] = app
	s.byHash[strings.ToLower(hash)] = app
	if err := s.persistLocked(); err != nil {
		delete(s.byID, id)
		delete(s.byHash, strings.ToLower(hash))
		return AppView{}, "", err
	}
	return appView(app), token, nil
}

// Update patches name/scopes/disabled.
func (s *AppStore) Update(id string, req AppUpdateRequest) (AppView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	app, ok := s.byID[id]
	if !ok {
		return AppView{}, fmt.Errorf("app %q not found", id)
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			return AppView{}, fmt.Errorf("name cannot be empty")
		}
		app.Name = name
	}
	if req.Scopes != nil {
		normalized := Permissions{}
		for k, v := range *req.Scopes {
			normalized[k] = normalizeAccess(v)
		}
		if err := ValidatePermissions(normalized); err != nil {
			return AppView{}, err
		}
		if _, ok := normalized[PermissionDefaultKey]; !ok {
			normalized[PermissionDefaultKey] = AccessNone
		}
		app.Scopes = normalized
	}
	if req.Disabled != nil {
		app.Disabled = *req.Disabled
	}
	app.UpdatedAt = time.Now().UTC()
	if err := s.persistLocked(); err != nil {
		return AppView{}, err
	}
	return appView(app), nil
}

// Rotate replaces the app token and returns the new plaintext once.
func (s *AppStore) Rotate(id string) (AppView, string, error) {
	token, hash, hint, err := generateAppToken()
	if err != nil {
		return AppView{}, "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	app, ok := s.byID[id]
	if !ok {
		return AppView{}, "", fmt.Errorf("app %q not found", id)
	}
	oldHash := strings.ToLower(app.TokenHash)
	app.TokenHash = hash
	app.TokenHint = hint
	app.UpdatedAt = time.Now().UTC()
	if oldHash != "" && oldHash != strings.ToLower(hash) {
		delete(s.byHash, oldHash)
	}
	s.byHash[strings.ToLower(hash)] = app
	if err := s.persistLocked(); err != nil {
		return AppView{}, "", err
	}
	return appView(app), token, nil
}

// Delete removes an app (revoke).
func (s *AppStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	app, ok := s.byID[id]
	if !ok {
		return fmt.Errorf("app %q not found", id)
	}
	delete(s.byID, id)
	if app.TokenHash != "" {
		delete(s.byHash, strings.ToLower(app.TokenHash))
	}
	return s.persistLocked()
}

// RevokeByToken disables the app that owns token and clears the token so the
// credential cannot be reused. Used by POST /v1/apps/revoke (issue #131
// disconnect toggle). Returns the app id that was revoked.
func (s *AppStore) RevokeByToken(token string) (string, error) {
	if token == "" {
		return "", fmt.Errorf("token is required")
	}
	hash := strings.ToLower(hashAppToken(token))
	s.mu.Lock()
	defer s.mu.Unlock()
	app, ok := s.byHash[hash]
	if !ok {
		// Unknown or already revoked — idempotent success for the client.
		return "", nil
	}
	id := app.ID
	if app.TokenHash != "" {
		delete(s.byHash, strings.ToLower(app.TokenHash))
	}
	app.TokenHash = ""
	app.TokenHint = ""
	app.Disabled = true
	app.UpdatedAt = time.Now().UTC()
	if err := s.persistLocked(); err != nil {
		return "", err
	}
	return id, nil
}

// Enable re-activates a disabled app (authorize after disconnect).
func (s *AppStore) Enable(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	app, ok := s.byID[id]
	if !ok {
		return fmt.Errorf("app %q not found", id)
	}
	app.Disabled = false
	app.UpdatedAt = time.Now().UTC()
	return s.persistLocked()
}

// FindByToken resolves a plaintext app token to its app. Returns nil when
// the token does not match any enabled app. Comparison is constant-time on
// the hash.
func (s *AppStore) FindByToken(token string) *App {
	if token == "" {
		return nil
	}
	hash := hashAppToken(token)
	s.mu.RLock()
	defer s.mu.RUnlock()
	app, ok := s.byHash[strings.ToLower(hash)]
	if !ok || app.Disabled {
		return nil
	}
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(app.TokenHash)), []byte(strings.ToLower(hash))) != 1 {
		return nil
	}
	// Copy so callers cannot mutate the store.
	cp := *app
	if app.Scopes != nil {
		cp.Scopes = app.Scopes.Clone()
	}
	return &cp
}

// Path is the on-disk apps.json location.
func (s *AppStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func validateAppID(id string) error {
	if len(id) > 64 {
		return fmt.Errorf("app id must be at most 64 characters")
	}
	for _, r := range id {
		ok := r == '-' || r == '_' ||
			(r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9')
		if !ok {
			return fmt.Errorf("app id must be alphanumeric, dash, or underscore")
		}
	}
	return nil
}

// --- request context ---

type appIdentityKey struct{}

// AppIdentity is the authenticated third-party app on the request.
type AppIdentity struct {
	ID     string
	Name   string
	Scopes Permissions
}

func withAppIdentity(r *http.Request, app *App) *http.Request {
	id := &AppIdentity{ID: app.ID, Name: app.Name, Scopes: app.Scopes}
	return r.WithContext(context.WithValue(r.Context(), appIdentityKey{}, id))
}

// AppIdentityFrom returns the third-party app identity when the request was
// authenticated with an app token.
func AppIdentityFrom(r *http.Request) *AppIdentity {
	if r == nil {
		return nil
	}
	id, _ := r.Context().Value(appIdentityKey{}).(*AppIdentity)
	return id
}

// appLibraryCacheSuffix namespaces cache entries by app scope level so a
// freebusy-only app never reads a full-events cache entry (and vice versa).
func appLibraryCacheSuffix(app *AppIdentity, permType string) string {
	if app == nil {
		return ""
	}
	return "|app|" + normalizeAccess(app.Scopes.For(permType))
}

// appAllowsLibrary reports whether the app may call the given library
// endpoint for permType under its scopes.
func appAllowsLibrary(app *AppIdentity, permType, method string) bool {
	if app == nil {
		return true
	}
	return allowsMethod(normalizeAccess(app.Scopes.For(permType)), method)
}
