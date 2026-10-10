package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Config struct {
	mu          sync.RWMutex
	Connections map[string]Connection `json:"connections,omitempty"`
	Services    []Service             `json:"services"`
	path        string
	// permsPath is the admin permissions overlay file (permissions.json).
	// Admin permission saves always write here — never aegis.json.
	permsPath string
	// permsOverlay is the loaded overlay map (service id → permissions;
	// present key with nil value means explicitly cleared).
	permsOverlay map[string]*Permissions
	// svcConnPath is the admin service-connections overlay
	// (service_connections.json). Immich/local-store setup writes here so
	// aegis.json is never rewritten from the admin UI.
	svcConnPath string
	// svcConnOverlay is the loaded overlay map (service id → connection).
	svcConnOverlay map[string]serviceConnectionEntry
}

// defaultPermissionsPath returns the overlay path beside a aegis.json path.
func defaultPermissionsPath(configPath string) string {
	dir := filepath.Dir(configPath)
	if dir == "" || dir == "." {
		return "permissions.json"
	}
	return filepath.Join(dir, "permissions.json")
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
	// Permissions restricts agent access by data type (playlists, tracks, …).
	// Nil means unrestricted. See permissions.go for levels and the "default" key.
	Permissions Permissions `json:"permissions,omitempty"`
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
	cfg.permsPath = defaultPermissionsPath(path)
	cfg.svcConnPath = defaultServiceConnectionsPath(path)
	if err := cfg.applyPermissionsOverlayLocked(); err != nil {
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
	if err := c.applyPermissionsOverlayLocked(); err != nil {
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
	for _, svc := range services {
		if err := ValidatePermissions(svc.Permissions); err != nil {
			return fmt.Errorf("service %q: %w", svc.ID, err)
		}
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
// while Reload / SetServicePermissions mutate the store (issue #95).
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
	if s.Permissions != nil {
		cp.Permissions = Permissions(s.Permissions.Clone())
	}
	if s.CanonicalFor != nil {
		cp.CanonicalFor = append([]string(nil), s.CanonicalFor...)
	}
	if s.OAuthScopes != nil {
		cp.OAuthScopes = append([]string(nil), s.OAuthScopes...)
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

// updateService applies mut to a service under the write lock. Intended for
// tests that install mock endpoints; production mutates via LoadConfig,
// Reload, and SetServicePermissions.
func (c *Config) updateService(id string, mut func(*Service)) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	svc := c.serviceByID(id)
	if svc == nil {
		return false
	}
	mut(svc)
	return true
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

// SetServicePermissions validates and persists agent permissions for a service.
// An empty/nil map clears the permissions block (unrestricted access).
// Always writes the permissions overlay (permissions.json) — never aegis.json.
func (c *Config) SetServicePermissions(id string, p Permissions) error {
	if err := ValidatePermissions(p); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	idx := -1
	for i := range c.Services {
		if c.Services[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("service %q not found", id)
	}

	if len(p) == 0 {
		c.Services[idx].Permissions = nil
	} else {
		c.Services[idx].Permissions = Permissions(p.Clone())
	}
	return c.setPermissionsOverlayEntryLocked(id, p)
}
