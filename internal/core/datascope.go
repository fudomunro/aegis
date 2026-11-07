package core

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

// dataScopesFileVersion is the on-disk schema version for data_scopes.json.
// Version 2 stores only key hashes (issue #253); version 1 files with
// plaintext api_key are migrated on load.
const dataScopesFileVersion = 2

// inviteTokenPrefix marks generated invite tokens (the scope credential
// under external access) so they cannot be confused with other keys.
const inviteTokenPrefix = "aeg_invite_"

// DataScope is one principal's view of Aegis data.
type DataScope struct {
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

// dataScopesFile is the on-disk format under the data directory.
type dataScopesFile struct {
	Version    int         `json:"version"`
	DataScopes []DataScope `json:"data_scopes"`
}

// DataScopeStore is a mutex-guarded registry of principal scopes.
type DataScopeStore struct {
	mu     sync.RWMutex
	path   string
	byID   map[string]*DataScope
	byHash map[string]*DataScope
}

// NewDataScopeStore loads data_scopes.json. A missing file yields an empty
// store (legacy single-user deployments). path is required in production.
func NewDataScopeStore(path string) (*DataScopeStore, error) {
	s := &DataScopeStore{
		path:   path,
		byID:   make(map[string]*DataScope),
		byHash: make(map[string]*DataScope),
	}
	if err := s.reload(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *DataScopeStore) reload() error {
	if s.path == "" {
		return fmt.Errorf("data scopes path is required")
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.mu.Lock()
			s.byID = make(map[string]*DataScope)
			s.byHash = make(map[string]*DataScope)
			s.mu.Unlock()
			return nil
		}
		return err
	}
	var file dataScopesFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return fmt.Errorf("parse data_scopes.json: %w", err)
	}
	if file.Version != 0 && file.Version != 1 && file.Version != dataScopesFileVersion {
		return fmt.Errorf("unsupported data_scopes.json version %d", file.Version)
	}
	byID := make(map[string]*DataScope, len(file.DataScopes))
	byHash := make(map[string]*DataScope, len(file.DataScopes))
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
				sc.APIKeyHint = scopeKeyHint(sc.APIKey)
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
func (s *DataScopeStore) persistLocked() error {
	if s.path == "" {
		return fmt.Errorf("data scopes path is empty; cannot save")
	}
	ids := make([]string, 0, len(s.byID))
	for id := range s.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := dataScopesFile{Version: dataScopesFileVersion, DataScopes: make([]DataScope, 0, len(ids))}
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
func (s *DataScopeStore) FindByAPIKey(key string) *DataScope {
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
func (s *DataScopeStore) Get(id string) *DataScope {
	if s == nil || id == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byID[id]
}

// All returns a snapshot of scopes (key material omitted).
func (s *DataScopeStore) All() []DataScope {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]DataScope, 0, len(s.byID))
	for _, sc := range s.byID {
		cp := *sc
		cp.APIKey = ""
		cp.APIKeyHash = ""
		out = append(out, cp)
	}
	return out
}

// DataScopeView is the secret-free admin projection of a scope. Key material
// never leaves the store; api_key_hint identifies a credential in UIs.
type DataScopeView struct {
	ID         string           `json:"id"`
	Name       string           `json:"name,omitempty"`
	Dataset    string           `json:"dataset,omitempty"`
	APIKeyHint string           `json:"api_key_hint,omitempty"`
	Grants     types.DataGrants `json:"grants,omitempty"`
	ReadOnly   bool             `json:"read_only,omitempty"`
	Disabled   bool             `json:"disabled,omitempty"`
}

// DataScopeCreateRequest mints a scope with a generated invite token.
type DataScopeCreateRequest struct {
	ID       string           `json:"id"`
	Name     string           `json:"name,omitempty"`
	Dataset  string           `json:"dataset,omitempty"`
	Grants   types.DataGrants `json:"grants,omitempty"`
	ReadOnly bool             `json:"read_only,omitempty"`
}

// DataScopeUpdateRequest patches scope fields. The key is rotated, not
// patched (issue #253).
type DataScopeUpdateRequest struct {
	Name     *string           `json:"name,omitempty"`
	Grants   *types.DataGrants `json:"grants,omitempty"`
	ReadOnly *bool             `json:"read_only,omitempty"`
	Disabled *bool             `json:"disabled,omitempty"`
}

func scopeView(sc *DataScope) DataScopeView {
	return DataScopeView{
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
func (s *DataScopeStore) List() []DataScopeView {
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
	out := make([]DataScopeView, 0, len(ids))
	for _, id := range ids {
		out = append(out, scopeView(s.byID[id]))
	}
	return out
}

// View returns the secret-free view of one scope.
func (s *DataScopeStore) View(id string) (DataScopeView, bool) {
	if s == nil {
		return DataScopeView{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	sc := s.byID[id]
	if sc == nil {
		return DataScopeView{}, false
	}
	return scopeView(sc), true
}

// Create mints a scope with a generated invite token. The plaintext token is
// returned once; only its hash is stored (issue #253).
func (s *DataScopeStore) Create(req DataScopeCreateRequest) (DataScopeView, string, error) {
	id := strings.TrimSpace(req.ID)
	if id == "" {
		return DataScopeView{}, "", fmt.Errorf("scope id is required")
	}
	if err := validateScopeID(id); err != nil {
		return DataScopeView{}, "", err
	}
	if req.Grants != nil {
		if err := types.ValidateDataGrants(req.Grants); err != nil {
			return DataScopeView{}, "", err
		}
	}
	token, hash, hint, err := generateInviteToken()
	if err != nil {
		return DataScopeView{}, "", fmt.Errorf("generating invite token: %w", err)
	}
	sc := &DataScope{
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
		return DataScopeView{}, "", fmt.Errorf("scope %q already exists", id)
	}
	for _, existing := range s.byID {
		if existing.APIKeyHash == hash {
			return DataScopeView{}, "", fmt.Errorf("token collision")
		}
	}
	s.byID[id] = sc
	s.byHash[hash] = sc
	if err := s.persistLocked(); err != nil {
		delete(s.byID, id)
		delete(s.byHash, hash)
		return DataScopeView{}, "", err
	}
	return scopeView(sc), token, nil
}

// Update patches name/grants/read_only/disabled. Key material is not
// patchable — use Rotate.
func (s *DataScopeStore) Update(id string, req DataScopeUpdateRequest) (DataScopeView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc := s.byID[id]
	if sc == nil {
		return DataScopeView{}, fmt.Errorf("scope %q not found", id)
	}
	if req.Name != nil {
		sc.Name = strings.TrimSpace(*req.Name)
	}
	if req.Grants != nil {
		if err := types.ValidateDataGrants(*req.Grants); err != nil {
			return DataScopeView{}, err
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
		return DataScopeView{}, err
	}
	return scopeView(sc), nil
}

// Rotate replaces the scope's invite token and returns the new plaintext
// once. The previous token stops authenticating immediately.
func (s *DataScopeStore) Rotate(id string) (DataScopeView, string, error) {
	token, hash, hint, err := generateInviteToken()
	if err != nil {
		return DataScopeView{}, "", fmt.Errorf("generating invite token: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sc := s.byID[id]
	if sc == nil {
		return DataScopeView{}, "", fmt.Errorf("scope %q not found", id)
	}
	if _, exists := s.byHash[hash]; exists {
		return DataScopeView{}, "", fmt.Errorf("token collision")
	}
	oldHash := sc.APIKeyHash
	sc.APIKeyHash = hash
	sc.APIKeyHint = hint
	if oldHash != "" {
		delete(s.byHash, oldHash)
	}
	s.byHash[hash] = sc
	if err := s.persistLocked(); err != nil {
		return DataScopeView{}, "", err
	}
	return scopeView(sc), token, nil
}

// generateInviteToken returns a new high-entropy invite token (plaintext)
// and its storage hash/hint.
func generateInviteToken() (token, hash, hint string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", "", err
	}
	token = inviteTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	hash = hex.EncodeToString(sum[:])
	hint = scopeKeyHint(token)
	return token, hash, hint, nil
}

// scopeKeyHint is the non-secret key prefix for admin UIs. Generated invite
// tokens are hinted past their well-known prefix so hints discriminate.
func scopeKeyHint(key string) string {
	payload := key
	if strings.HasPrefix(key, inviteTokenPrefix) {
		payload = strings.TrimPrefix(key, inviteTokenPrefix)
	}
	if len(payload) > 8 {
		payload = payload[:8]
	}
	if strings.HasPrefix(key, inviteTokenPrefix) {
		return inviteTokenPrefix + payload
	}
	return payload
}

func validateScopeID(id string) error {
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
func (s *DataScopeStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

type dataScopeKey struct{}

// withDataScope attaches the resolved principal scope to the request.
func withDataScope(r *http.Request, sc *DataScope) *http.Request {
	if sc == nil {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), dataScopeKey{}, sc))
}

// DataScopeFrom returns the principal scope for the request, or nil when the
// caller is the default/owner principal (legacy API key or tailnet identity).
func DataScopeFrom(r *http.Request) *DataScope {
	sc, _ := r.Context().Value(dataScopeKey{}).(*DataScope)
	return sc
}

// scopeDataset returns the partition id for the request ("" = no filter).
func scopeDataset(r *http.Request) string {
	sc := DataScopeFrom(r)
	if sc == nil {
		return ""
	}
	return sc.Dataset
}

// serviceVisibleToScope reports whether a service's connector may feed this
// principal. Services without a dataset are shared (every scope). A dataset
// bound service is visible only to scopes in that partition. The default
// scope (empty dataset) sees every service.
func serviceVisibleToScope(svc *Service, sc *DataScope) bool {
	if svc == nil {
		return false
	}
	if svc.Dataset == "" {
		return true
	}
	if sc == nil {
		// Default principal: no partition filter, sees shared and all
		// dataset-bound services (owner view).
		return true
	}
	return sc.Dataset == svc.Dataset
}

// scopeAllows reports whether the request's data scope grants the method on
// a data type. Deny-by-default: an unlisted type resolves to none. The owner
// principal (no scope) is unrestricted here — owner-tier policy is enforced
// separately so the two layers never drift into each other.
func scopeAllows(r *http.Request, dataType, method string) bool {
	sc := DataScopeFrom(r)
	if sc == nil {
		return true
	}
	return types.AllowsMethod(types.ScopeGrantsAccess(sc.Grants, dataType, ""), method)
}

// scopeCacheSuffix namespaces library cache entries per principal so one
// scope cannot read another's cached page: the dataset partition plus a
// fingerprint of the scope's grants (two principals with different grants
// see different responses even inside one dataset).
func scopeCacheSuffix(sc *DataScope) string {
	if sc == nil {
		return ""
	}
	suffix := ""
	if sc.Dataset != "" {
		suffix += "|ds:" + sc.Dataset
	}
	return suffix + "|sg:" + scopeGrantsFingerprint(sc.Grants)
}

// scopeGrantsFingerprint is a stable short digest of a grant map (json.Marshal
// sorts map keys). Empty grants share the reserved "none" fingerprint.
func scopeGrantsFingerprint(g types.DataGrants) string {
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
