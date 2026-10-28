package core

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"

	"github.com/fudomunro/aegis/internal/types"
)

// Data scopes bind an authenticated principal to a dataset partition and
// optional tightened data grants. They are the multi-user isolation seam:
// identity → which data this caller may read. See docs/data-scopes.md.

// dataScopesFileVersion is the on-disk schema version for data_scopes.json.
const dataScopesFileVersion = 1

// DataScope is one principal's view of Aegis data.
type DataScope struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// Dataset partitions local stores and service visibility. Empty means
	// the owner/default partition (no dataset filter — today's behavior).
	Dataset string `json:"dataset,omitempty"`
	// APIKey is the agent credential presented by this scope. Stored on
	// disk (data_scopes.json, 0600); never returned by admin APIs.
	APIKey string `json:"api_key,omitempty"`
	// APIKeyHash is hex(sha256(APIKey)) after load. Not serialized back.
	APIKeyHash string `json:"-"`
	// APIKeyHint is a short non-secret prefix for admin UIs.
	APIKeyHint string `json:"api_key_hint,omitempty"`
	// ReadOnly rejects mutating agent methods for this principal (demo).
	ReadOnly bool `json:"read_only,omitempty"`
	// Grants optionally tighten type-level agent access for this principal.
	// They never grant more than the default policy allows.
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
	if file.Version != 0 && file.Version != dataScopesFileVersion {
		return fmt.Errorf("unsupported data_scopes.json version %d", file.Version)
	}
	byID := make(map[string]*DataScope, len(file.DataScopes))
	byHash := make(map[string]*DataScope, len(file.DataScopes))
	for i := range file.DataScopes {
		sc := file.DataScopes[i]
		if sc.ID == "" {
			return fmt.Errorf("data_scopes: entry %d missing id", i)
		}
		if sc.APIKey == "" {
			return fmt.Errorf("data_scopes: scope %q missing api_key", sc.ID)
		}
		sum := sha256.Sum256([]byte(sc.APIKey))
		sc.APIKeyHash = hex.EncodeToString(sum[:])
		if len(sc.APIKey) >= 8 {
			sc.APIKeyHint = sc.APIKey[:8]
		}
		sc.APIKey = ""
		stored := sc
		if _, ok := byID[stored.ID]; ok {
			return fmt.Errorf("data_scopes: duplicate scope id %q", stored.ID)
		}
		byID[stored.ID] = &stored
		byHash[stored.APIKeyHash] = &stored
	}
	s.mu.Lock()
	s.byID = byID
	s.byHash = byHash
	s.mu.Unlock()
	return nil
}

// FindByAPIKey resolves a presented agent credential to its scope.
// Comparison is constant-time on the digest. Returns nil when unknown,
// disabled, or the key is empty.
func (s *DataScopeStore) FindByAPIKey(key string) *DataScope {
	if s == nil || key == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(key))
	hash := hex.EncodeToString(sum[:])
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sc := range s.byHash {
		if subtle.ConstantTimeCompare([]byte(sc.APIKeyHash), []byte(hash)) == 1 && !sc.Disabled {
			return sc
		}
	}
	return nil
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

// All returns a snapshot of scopes (API keys omitted).
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
		out = append(out, cp)
	}
	return out
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

// scopeCacheSuffix namespaces library cache entries per dataset partition so
// one principal cannot read another's cached page.
func scopeCacheSuffix(sc *DataScope) string {
	if sc == nil || sc.Dataset == "" {
		return ""
	}
	return "|ds:" + sc.Dataset
}
