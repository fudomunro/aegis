package core

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/core/tokens"
)

type Config struct {
	mu          sync.RWMutex
	Connections map[string]Connection `json:"connections,omitempty"`
	Services    []Service             `json:"services"`
	// DataGrantsBootstrap is the optional aegis.json agent data-grant section
	// (issue #148 / #174). The data_grants.json overlay `data_grants` map
	// wins when present.
	DataGrantsBootstrap policy.DataGrants `json:"data_grants,omitempty"`
	// Hook configures pre-request matching skips (issue #205). See hook_skip.go.
	Hook HookConfig `json:"hook,omitempty"`
	// TrustedProxies lists proxy addresses (CIDR or single IP) whose
	// forwarded client-IP headers are believed (issue #253). Empty:
	// RemoteAddr only — forwarded headers are never trusted.
	TrustedProxies []string `json:"trusted_proxies,omitempty"`
	// ClientIPHeader selects which forwarded header carries the client
	// address from a trusted proxy: x-forwarded-for (default) or x-real-ip.
	// Exactly one header is read — never a fallback chain.
	ClientIPHeader string `json:"client_ip_header,omitempty"`
	// RateLimits tunes brute-force damping on auth/token routes (issue #253).
	RateLimits RateLimitConfig `json:"rate_limits,omitempty"`
	// hookSkip holds compiled Hook.SkipPatterns (not serialized).
	hookSkip hookSkip
	// trustedProxyNets holds compiled TrustedProxies (not serialized).
	trustedProxyNets []net.IPNet
	// clientIPHeaderKey holds the compiled ClientIPHeader (not serialized).
	clientIPHeaderKey string
	path              string
	// dataGrantsPath is the admin data-grant overlay file (data_grants.json).
	// Admin data-grant saves always write here — never aegis.json.
	dataGrantsPath string
	// typePolicies is the agent data-grant map (issue #148). It
	// materializes to the owner key's rule map via DataGrants.Levels;
	// request authorization is one lookup over that map.
	typePolicies policy.DataGrants
	// svcConnPath is the admin service-connections overlay
	// (service_connections.json). Immich/local-store setup writes here so
	// aegis.json is never rewritten from the admin UI.
	svcConnPath string
	// svcConnOverlay is the loaded overlay map (service id → connection).
	svcConnOverlay map[string]serviceConnectionEntry
	// tailscaleAdmin holds the optional Tailscale Admin API credential used
	// for Immich VIP Service setup checks/fixes (issue #124).
	tailscaleAdmin *tailscaleAdminEntry
	// secrets is the encrypted token store used for static credentials from
	// the service-connections overlay (issue #184). Attached at startup via
	// InitServiceConnectionsStore alongside -token-pass.
	secrets *tokens.TokenStore
}

// defaultDataGrantsPath returns the overlay path beside a aegis.json path.
func defaultDataGrantsPath(configPath string) string {
	dir := filepath.Dir(configPath)
	if dir == "" {
		dir = "."
	}
	return filepath.Join(dir, defaultDataGrantsName)
}

// Connection is a shared OAuth provider registration (issue #26). Multiple
// service surfaces can reference one connection so the user authorizes once
// and Aegis keeps a single token/refresh path per provider.
type Connection struct {
	Name         string `json:"name,omitempty"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	AuthURL      string `json:"auth_url"`
	TokenURL     string `json:"token_url"`
	// Scopes is the union requested at login for this connection.
	Scopes []string `json:"scopes,omitempty"`
}

// Service represents a named external service (e.g. Spotify, YouTube Music)
// that Aegis can intercept and enrich requests for.
type Service struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Aliases     []string          `json:"aliases,omitempty"`
	Action      string            `json:"action"` // "inject" or "block"
	Endpoint    string            `json:"endpoint,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	CacheTTL    Duration          `json:"cache_ttl"`
	Description string            `json:"description,omitempty"`
	// CanonicalFor lists library kinds where this service is the authoritative
	// non-Aegis location (issue #16). At most one non-blocked service may
	// claim each kind. Example: ["playlists"] means Spotify is home for playlists.
	CanonicalFor []string `json:"canonical_for,omitempty"`
	// Connection names the shared provider connection this surface uses
	// (issue #26). Required for OAuth-enabled surfaces.
	Connection string `json:"connection,omitempty"`
	// OAuthScopes lists scopes this surface requires from the connection.
	// When set, a shared token missing any of these degrades the surface
	// to unauthorized rather than failing the whole connection.
	OAuthScopes []string `json:"oauth_scopes,omitempty"`
	// Dataset binds this service's data to one data-scope partition
	// (docs/data-scopes.md). Empty means the service is shared (visible to
	// every principal). Non-empty is visible only to scopes with the same
	// dataset; the default/owner scope sees every service.
	Dataset string `json:"dataset,omitempty"`
	// SyncRelationships is the in-memory view of this source's sync rows
	// (issue #365): kind -> hub flag. Stored on the service-connections
	// overlay entry; not part of aegis.json.
	SyncRelationships map[string]bool `json:"-"`
}

// OAuthConfig holds the OAuth2 credentials and endpoints for a service.
type OAuthConfig struct {
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	AuthURL      string   `json:"auth_url"`
	TokenURL     string   `json:"token_url"`
	Scopes       []string `json:"scopes"`
}

// Duration wraps time.Duration for JSON marshalling.
type Duration struct{ time.Duration }

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = parsed
	return nil
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	resolveConfigSecrets(&cfg)
	if err := validateConfig(&cfg); err != nil {
		return nil, err
	}
	cfg.path = path
	cfg.dataGrantsPath = defaultDataGrantsPath(path)
	cfg.svcConnPath = defaultServiceConnectionsPath(path)
	cfg.typePolicies = cfg.DataGrantsBootstrap
	if err := cfg.applyDataGrantsOverlayLocked(); err != nil {
		return nil, err
	}
	if err := cfg.applyServiceConnectionsOverlayLocked(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) Reload() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	data, err := os.ReadFile(c.path)
	if err != nil {
		return err
	}
	var fresh Config
	if err := json.Unmarshal(data, &fresh); err != nil {
		return err
	}
	resolveConfigSecrets(&fresh)
	if err := validateConfig(&fresh); err != nil {
		return err
	}
	c.Connections = fresh.Connections
	c.Services = fresh.Services
	c.DataGrantsBootstrap = fresh.DataGrantsBootstrap
	c.Hook = fresh.Hook
	c.hookSkip = fresh.hookSkip
	c.TrustedProxies = fresh.TrustedProxies
	c.RateLimits = fresh.RateLimits
	c.trustedProxyNets = fresh.trustedProxyNets
	c.ClientIPHeader = fresh.ClientIPHeader
	c.clientIPHeaderKey = fresh.clientIPHeaderKey
	c.typePolicies = c.DataGrantsBootstrap
	if err := c.applyDataGrantsOverlayLocked(); err != nil {
		return err
	}
	if err := c.applyServiceConnectionsOverlayLocked(); err != nil {
		return err
	}
	slog.Info("config reloaded", "path", c.path, "services", len(fresh.Services), "connections", len(fresh.Connections))
	return nil
}

func validateConfig(cfg *Config) error {
	services := cfg.Services
	for i := range services {
		services[i].CanonicalFor = normalizeCanonicalFor(services[i].CanonicalFor)
	}
	if err := validateCanonicalFor(services); err != nil {
		return err
	}
	if err := policy.ValidateDataGrants(cfg.DataGrantsBootstrap); err != nil {
		return fmt.Errorf("data_grants: %w", err)
	}
	skip, err := compileHookSkip(cfg.Hook)
	if err != nil {
		return err
	}
	cfg.hookSkip = skip
	nets, err := compileTrustedProxies(cfg.TrustedProxies)
	if err != nil {
		return err
	}
	cfg.trustedProxyNets = nets
	headerKey, err := compileClientIPHeader(cfg.ClientIPHeader)
	if err != nil {
		return err
	}
	cfg.clientIPHeaderKey = headerKey
	for _, svc := range services {
		if svc.Connection == "" {
			continue
		}
		if _, ok := cfg.Connections[svc.Connection]; !ok {
			return fmt.Errorf("service %q references unknown connection %q", svc.ID, svc.Connection)
		}
	}
	return nil
}

// clone returns a copy of the service whose maps and slices are independent
// of the config's live state, so callers can read outside the config lock
// while Reload mutates the store (issue #95).
func (s Service) clone() Service {
	cp := s
	if s.Aliases != nil {
		cp.Aliases = append([]string(nil), s.Aliases...)
	}
	if s.Headers != nil {
		cp.Headers = make(map[string]string, len(s.Headers))
		for k, v := range s.Headers {
			cp.Headers[k] = v
		}
	}
	if s.CanonicalFor != nil {
		cp.CanonicalFor = append([]string(nil), s.CanonicalFor...)
	}
	if s.OAuthScopes != nil {
		cp.OAuthScopes = append([]string(nil), s.OAuthScopes...)
	}
	if s.SyncRelationships != nil {
		cp.SyncRelationships = make(map[string]bool, len(s.SyncRelationships))
		for k, v := range s.SyncRelationships {
			cp.SyncRelationships[k] = v
		}
	}
	return cp
}

func (c *Config) serviceByID(id string) *Service {
	for i := range c.Services {
		if c.Services[i].ID == id {
			return &c.Services[i]
		}
	}
	return nil
}

// FindMatchingServices returns all services whose name or aliases appear
// as substrings in the user message (case-insensitive).
func (c *Config) FindMatchingServices(userMessage string) []Service {
	c.mu.RLock()
	defer c.mu.RUnlock()

	lower := strings.ToLower(userMessage)
	var matched []Service
	for i := range c.Services {
		if matchesService(c.Services[i], lower) {
			matched = append(matched, c.Services[i].clone())
		}
	}
	return matched
}

func matchesService(svc Service, lowerMessage string) bool {
	if strings.Contains(lowerMessage, strings.ToLower(svc.Name)) {
		return true
	}
	for _, alias := range svc.Aliases {
		if strings.Contains(lowerMessage, strings.ToLower(alias)) {
			return true
		}
	}
	return false
}

// GetService returns a snapshot of the service with the given ID, or nil.
// The result is a copy: callers may read it without holding the config lock,
// and later config mutations will not race with those reads (issue #95).
func (c *Config) GetService(id string) *Service {
	c.mu.RLock()
	defer c.mu.RUnlock()
	svc := c.serviceByID(id)
	if svc == nil {
		return nil
	}
	cp := svc.clone()
	return &cp
}

// ServiceSnapshots returns copies of every configured service, safe to range
// without the config lock (issue #95).
func (c *Config) ServiceSnapshots() []Service {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Service, 0, len(c.Services))
	for i := range c.Services {
		out = append(out, c.Services[i].clone())
	}
	return out
}

// ServiceCount returns the number of configured services.
func (c *Config) ServiceCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.Services)
}

// EnsureService injects an in-memory service entry when the id is missing.
// Used for file-based sources (Quicken import) that are not operator-facing
// services but still need to pass library fan-out checks. Not persisted.
func (c *Config) EnsureService(svc Service) {
	id := strings.TrimSpace(svc.ID)
	if id == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.serviceByID(id) != nil {
		return
	}
	c.Services = append(c.Services, svc)
}

// GetConnection returns a snapshot of the named shared provider connection, or nil.
func (c *Config) GetConnection(id string) *Connection {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.Connections == nil {
		return nil
	}
	conn, ok := c.Connections[id]
	if !ok {
		return nil
	}
	cp := conn
	if conn.Scopes != nil {
		cp.Scopes = append([]string(nil), conn.Scopes...)
	}
	return &cp
}

// trustedNets returns the compiled trusted-proxy list (issue #253).
func (c *Config) trustedNets() []net.IPNet {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.trustedProxyNets
}

// clientIPHeaderKey returns the compiled forwarded-header name (issue #253).
func (c *Config) clientIPHeader() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.clientIPHeaderKey
}

// DataGrants returns a snapshot of the agent data-grant map.
func (c *Config) DataGrants() policy.DataGrants {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.typePolicies == nil {
		return nil
	}
	out := make(policy.DataGrants, len(c.typePolicies))
	for k, v := range c.typePolicies {
		out[k] = cloneDataGrant(v)
	}
	return out
}

// SetDataGrants replaces the agent data-grant map and persists the overlay.
// An empty map is stored explicitly so it clears bootstrap grants on reload.
func (c *Config) SetDataGrants(policies policy.DataGrants) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.setDataGrantsLocked(policies)
}

// SeedDataGrant installs a data-grant default when the key has no entry yet.
// Used for user-defined kinds whose schema declares default_access (issue #15).
// Never overwrites an existing grant and never persists — admin overlay wins.
func (c *Config) SeedDataGrant(kind string, grant policy.DataGrant) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if kind == "" {
		return
	}
	if c.typePolicies == nil {
		c.typePolicies = policy.DataGrants{}
	}
	if _, exists := c.typePolicies[kind]; exists {
		return
	}
	c.typePolicies[kind] = grant
}
