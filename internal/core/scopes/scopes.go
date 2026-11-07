package scopes

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/fudomunro/aegis/internal/shared"
	"github.com/fudomunro/aegis/internal/types"
)

// Data scopes bind an authenticated principal to a dataset partition and
// optional tightened data grants. They are the multi-user isolation seam:
// identity → which data this caller may read. See docs/data-scopes.md.

// fileVersion is the on-disk schema version for data_scopes.json.
// Version 2 stores only key hashes (issue #253); version 1 files with
// plaintext api_key are migrated on load.
const fileVersion = 2

// InviteTokenPrefix marks generated invite tokens (the scope credential
// under external access) so they cannot be confused with other keys.
const InviteTokenPrefix = "aeg_invite_"

// Scope is one principal's view of Aegis data.
type Scope struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// Dataset partitions local stores and service visibility. Empty means
	// the owner/default partition (no dataset filter — today's behavior).
	Dataset string `json:"dataset,omitempty"`
	// APIKey is the legacy plaintext credential. Accepted on load and
	// migrated to hash at rest; never written back and never returned by
	// admin APIs.
	APIKey string `json:"api_key,omitempty"`
	// APIKeyHash is hex(sha256(api_key)) — the stored credential. Mint and
	// rotate write only this; authentication hashes the presented key.
	APIKeyHash string `json:"api_key_hash,omitempty"`
	// APIKeyHint is a short non-secret prefix for admin UIs.
	APIKeyHint string `json:"api_key_hint,omitempty"`
	// ReadOnly rejects mutating agent methods for this principal (demo).
	ReadOnly bool `json:"read_only,omitempty"`
	// Grants are this principal's deny-by-default allow list (issue #253).
	// Scope grants only tighten owner-tier policy (AND of both layers) and an
	// unlisted type resolves to none. See docs/external-access.md.
	Grants types.DataGrants `json:"grants,omitempty"`
	// Disabled blocks authentication without deleting the record.
	Disabled bool `json:"disabled,omitempty"`
}

// file is the on-disk format under the data directory.
type file struct {
	Version    int     `json:"version"`
	DataScopes []Scope `json:"data_scopes"`
}

// Store is a mutex-guarded registry of principal scopes.
type Store struct {
	mu     sync.RWMutex
	path   string
	byID   map[string]*Scope
	byHash map[string]*Scope
}

// NewStore loads data_scopes.json. A missing file yields an empty
// store (legacy single-user deployments). path is required in production.
func NewStore(path string) (*Store, error) {
	s := &Store{
		path:   path,
		byID:   make(map[string]*Scope),
		byHash: make(map[string]*Scope),
	}
	if err := s.reload(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) reload() error {
	if s.path == "" {
		return fmt.Errorf("data scopes path is required")
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.mu.Lock()
			s.byID = make(map[string]*Scope)
			s.byHash = make(map[string]*Scope)
			s.mu.Unlock()
			return nil
		}
		return err
	}
	var file file
	if err := json.Unmarshal(raw, &file); err != nil {
		return fmt.Errorf("parse data_scopes.json: %w", err)
	}
	if file.Version != 0 && file.Version != 1 && file.Version != fileVersion {
		return fmt.Errorf("unsupported data_scopes.json version %d", file.Version)
	}
	byID := make(map[string]*Scope, len(file.DataScopes))
	byHash := make(map[string]*Scope, len(file.DataScopes))
	migrated := false
	for i := range file.DataScopes {
		sc := file.DataScopes[i]
		if sc.ID == "" {
			return fmt.Errorf("data_scopes: entry %d missing id", i)
		}
		if err := types.ValidateDataGrants(sc.Grants); err != nil {
			return fmt.Errorf("data_scopes: scope %q grants: %w", sc.ID, err)
		}
		if sc.APIKey != "" {
			// Legacy plaintext entry (issue #253): keep only the hash at
			// rest, exactly like a freshly minted invite token.
			sum := sha256.Sum256([]byte(sc.APIKey))
			sc.APIKeyHash = hex.EncodeToString(sum[:])
			if sc.APIKeyHint == "" {
				sc.APIKeyHint = keyHint(sc.APIKey)
			}
			sc.APIKey = ""
			migrated = true
		}
		if sc.APIKeyHash == "" {
			return fmt.Errorf("data_scopes: scope %q missing api_key or api_key_hash", sc.ID)
		}
		sc.APIKeyHash = strings.ToLower(sc.APIKeyHash)
		stored := sc
		if _, ok := byID[stored.ID]; ok {
			return fmt.Errorf("data_scopes: duplicate scope id %q", stored.ID)
		}
		if _, ok := byHash[stored.APIKeyHash]; ok {
			return fmt.Errorf("data_scopes: duplicate api_key for scope %q", stored.ID)
		}
		byID[stored.ID] = &stored
		byHash[stored.APIKeyHash] = &stored
	}
	s.mu.Lock()
	s.byID = byID
	s.byHash = byHash
	if migrated {
		if err := s.persistLocked(); err != nil {
			s.mu.Unlock()
			return fmt.Errorf("data_scopes: migrate plaintext api_key: %w", err)
		}
	}
	s.mu.Unlock()
	return nil
}

// persistLocked writes data_scopes.json (sorted, 0600). Callers hold s.mu.
func (s *Store) persistLocked() error {
	if s.path == "" {
		return fmt.Errorf("data scopes path is empty; cannot save")
	}
	ids := make([]string, 0, len(s.byID))
	for id := range s.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := file{Version: fileVersion, DataScopes: make([]Scope, 0, len(ids))}
	for _, id := range ids {
		out.DataScopes = append(out.DataScopes, *s.byID[id])
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return shared.AtomicWriteFile(s.path, data, 0o600)
}

// FindByAPIKey resolves a presented agent credential to its scope.
// The presented key is hashed then looked up directly. Returns nil when
// unknown, disabled, or the key is empty.
func (s *Store) FindByAPIKey(key string) *Scope {
	if s == nil || key == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(key))
	hash := hex.EncodeToString(sum[:])
	s.mu.RLock()
	defer s.mu.RUnlock()
	sc := s.byHash[hash]
	if sc == nil || sc.Disabled {
		return nil
	}
	return sc
}

// Get returns a scope by id, or nil.
func (s *Store) Get(id string) *Scope {
	if s == nil || id == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byID[id]
}

// All returns a snapshot of scopes (key material omitted).
func (s *Store) All() []Scope {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Scope, 0, len(s.byID))
	for _, sc := range s.byID {
		cp := *sc
		cp.APIKey = ""
		cp.APIKeyHash = ""
		out = append(out, cp)
	}
	return out
}

// View is the secret-free admin projection of a scope. Key material
// never leaves the store; api_key_hint identifies a credential in UIs.
type View struct {
	ID         string           `json:"id"`
	Name       string           `json:"name,omitempty"`
	Dataset    string           `json:"dataset,omitempty"`
	APIKeyHint string           `json:"api_key_hint,omitempty"`
	Grants     types.DataGrants `json:"grants,omitempty"`
	ReadOnly   bool             `json:"read_only,omitempty"`
	Disabled   bool             `json:"disabled,omitempty"`
}

// CreateRequest mints a scope with a generated invite token.
type CreateRequest struct {
	ID       string           `json:"id"`
	Name     string           `json:"name,omitempty"`
	Dataset  string           `json:"dataset,omitempty"`
	Grants   types.DataGrants `json:"grants,omitempty"`
	ReadOnly bool             `json:"read_only,omitempty"`
}

// UpdateRequest patches scope fields. The key is rotated, not
// patched (issue #253).
type UpdateRequest struct {
	Name     *string           `json:"name,omitempty"`
	Grants   *types.DataGrants `json:"grants,omitempty"`
	ReadOnly *bool             `json:"read_only,omitempty"`
	Disabled *bool             `json:"disabled,omitempty"`
}

func view(sc *Scope) View {
	return View{
		ID:         sc.ID,
		Name:       sc.Name,
		Dataset:    sc.Dataset,
		APIKeyHint: sc.APIKeyHint,
		Grants:     sc.Grants,
		ReadOnly:   sc.ReadOnly,
		Disabled:   sc.Disabled,
	}
}

// List returns secret-free views of every scope, ordered by id.
func (s *Store) List() []View {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.byID))
	for id := range s.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]View, 0, len(ids))
	for _, id := range ids {
		out = append(out, view(s.byID[id]))
	}
	return out
}

// View returns the secret-free view of one scope.
func (s *Store) View(id string) (View, bool) {
	if s == nil {
		return View{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	sc := s.byID[id]
	if sc == nil {
		return View{}, false
	}
	return view(sc), true
}

// Create mints a scope with a generated invite token. The plaintext token is
// returned once; only its hash is stored (issue #253).
func (s *Store) Create(req CreateRequest) (View, string, error) {
	id := strings.TrimSpace(req.ID)
	if id == "" {
		return View{}, "", fmt.Errorf("scope id is required")
	}
	if err := validateID(id); err != nil {
		return View{}, "", err
	}
	if req.Grants != nil {
		if err := types.ValidateDataGrants(req.Grants); err != nil {
			return View{}, "", err
		}
	}
	token, hash, hint, err := generateInviteToken()
	if err != nil {
		return View{}, "", fmt.Errorf("generating invite token: %w", err)
	}
	sc := &Scope{
		ID:         id,
		Name:       strings.TrimSpace(req.Name),
		Dataset:    strings.TrimSpace(req.Dataset),
		APIKeyHash: hash,
		APIKeyHint: hint,
		ReadOnly:   req.ReadOnly,
		Grants:     req.Grants,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.byID[id]; exists {
		return View{}, "", fmt.Errorf("scope %q already exists", id)
	}
	for _, existing := range s.byID {
		if existing.APIKeyHash == hash {
			return View{}, "", fmt.Errorf("token collision")
		}
	}
	s.byID[id] = sc
	s.byHash[hash] = sc
	if err := s.persistLocked(); err != nil {
		delete(s.byID, id)
		delete(s.byHash, hash)
		return View{}, "", err
	}
	return view(sc), token, nil
}

// Update patches name/grants/read_only/disabled. Key material is not
// patchable — use Rotate.
func (s *Store) Update(id string, req UpdateRequest) (View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc := s.byID[id]
	if sc == nil {
		return View{}, fmt.Errorf("scope %q not found", id)
	}
	if req.Name != nil {
		sc.Name = strings.TrimSpace(*req.Name)
	}
	if req.Grants != nil {
		if err := types.ValidateDataGrants(*req.Grants); err != nil {
			return View{}, err
		}
		sc.Grants = *req.Grants
	}
	if req.ReadOnly != nil {
		sc.ReadOnly = *req.ReadOnly
	}
	if req.Disabled != nil {
		sc.Disabled = *req.Disabled
	}
	if err := s.persistLocked(); err != nil {
		return View{}, err
	}
	return view(sc), nil
}

// Rotate replaces the scope's invite token and returns the new plaintext
// once. The previous token stops authenticating immediately.
func (s *Store) Rotate(id string) (View, string, error) {
	token, hash, hint, err := generateInviteToken()
	if err != nil {
		return View{}, "", fmt.Errorf("generating invite token: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sc := s.byID[id]
	if sc == nil {
		return View{}, "", fmt.Errorf("scope %q not found", id)
	}
	if _, exists := s.byHash[hash]; exists {
		return View{}, "", fmt.Errorf("token collision")
	}
	oldHash := sc.APIKeyHash
	sc.APIKeyHash = hash
	sc.APIKeyHint = hint
	if oldHash != "" {
		delete(s.byHash, oldHash)
	}
	s.byHash[hash] = sc
	if err := s.persistLocked(); err != nil {
		return View{}, "", err
	}
	return view(sc), token, nil
}

// generateInviteToken returns a new high-entropy invite token (plaintext)
// and its storage hash/hint.
func generateInviteToken() (token, hash, hint string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", "", err
	}
	token = InviteTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	hash = hex.EncodeToString(sum[:])
	hint = keyHint(token)
	return token, hash, hint, nil
}

// keyHint is the non-secret key prefix for admin UIs. Generated invite
// tokens are hinted past their well-known prefix so hints discriminate.
func keyHint(key string) string {
	payload := key
	if strings.HasPrefix(key, InviteTokenPrefix) {
		payload = strings.TrimPrefix(key, InviteTokenPrefix)
	}
	if len(payload) > 8 {
		payload = payload[:8]
	}
	if strings.HasPrefix(key, InviteTokenPrefix) {
		return InviteTokenPrefix + payload
	}
	return payload
}

func validateID(id string) error {
	if len(id) > 64 {
		return fmt.Errorf("scope id must be at most 64 characters")
	}
	for _, r := range id {
		ok := r == '-' || r == '_' ||
			(r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9')
		if !ok {
			return fmt.Errorf("scope id must be alphanumeric, dash, or underscore")
		}
	}
	return nil
}

// Path is the on-disk data_scopes.json location.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

type scopeKey struct{}

// WithDataScope attaches the resolved principal scope to the request.
func WithDataScope(r *http.Request, sc *Scope) *http.Request {
	if sc == nil {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), scopeKey{}, sc))
}

// From returns the principal scope for the request, or nil when the
// caller is the default/owner principal (legacy API key or tailnet identity).
func From(r *http.Request) *Scope {
	sc, _ := r.Context().Value(scopeKey{}).(*Scope)
	return sc
}

// Dataset returns the partition id for the request ("" = no filter).
func Dataset(r *http.Request) string {
	sc := From(r)
	if sc == nil {
		return ""
	}
	return sc.Dataset
}

// ServiceVisible reports whether a service's connector may feed this
// principal. Services without a dataset are shared (every scope). A dataset
// bound service is visible only to scopes in that partition. The default
// scope (empty dataset) sees every service.
func ServiceVisible(dataset string, sc *Scope) bool {
	if dataset == "" {
		return true
	}
	if sc == nil {
		// Default principal: no partition filter, sees shared and all
		// dataset-bound services (owner view).
		return true
	}
	return sc.Dataset == dataset
}

// Allows reports whether the request's data scope grants the method on
// a data type. Deny-by-default: an unlisted type resolves to none. The owner
// principal (no scope) is unrestricted here — owner-tier policy is enforced
// separately so the two layers never drift into each other.
func Allows(r *http.Request, dataType, method string) bool {
	sc := From(r)
	if sc == nil {
		return true
	}
	return types.AllowsMethod(types.ScopeGrantsAccess(sc.Grants, dataType, ""), method)
}

// CacheSuffix namespaces library cache entries per principal so one
// scope cannot read another's cached page: the dataset partition plus a
// fingerprint of the scope's grants (two principals with different grants
// see different responses even inside one dataset).
func CacheSuffix(sc *Scope) string {
	if sc == nil {
		return ""
	}
	suffix := ""
	if sc.Dataset != "" {
		suffix += "|ds:" + sc.Dataset
	}
	return suffix + "|sg:" + grantsFingerprint(sc.Grants)
}

// grantsFingerprint is a stable short digest of a grant map (json.Marshal
// sorts map keys). Empty grants share the reserved "none" fingerprint.
func grantsFingerprint(g types.DataGrants) string {
	if len(g) == 0 {
		return "none"
	}
	raw, err := json.Marshal(g)
	if err != nil {
		return "invalid"
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

// ForceReadOnlyAll marks every data scope read-only (demo lockdown).
func (s *Store) ForceReadOnlyAll() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sc := range s.byID {
		sc.ReadOnly = true
	}
}
