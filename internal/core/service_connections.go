package core

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// serviceConnectionsFileVersion is the on-disk schema for service_connections.json.
// Version 2 never writes secret values: static credentials live in the
// encrypted token store under -token-pass (issue #184).
const serviceConnectionsFileVersion = 2

// serviceConnectionEntry is an admin-saved connection surface for a service
// that authenticates with static headers (Immich and other local/foreign
// stores). Header values are held in memory from the encrypted token store
// (issue #184) and are never echoed back by admin APIs. Legacy plaintext
// files may still carry Headers; they are migrated on load.
type serviceConnectionEntry struct {
	Endpoint     string            `json:"endpoint,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	CanonicalFor []string          `json:"canonical_for,omitempty"`
	// ClearCanonicalFor forces an empty claim list (override set).
	ClearCanonicalFor bool     `json:"clear_canonical_for,omitempty"`
	Action            string   `json:"action,omitempty"`
	CacheTTL          string   `json:"cache_ttl,omitempty"`
	Description       string   `json:"description,omitempty"`
	Name              string   `json:"name,omitempty"`
	Aliases           []string `json:"aliases,omitempty"`
}

// serviceConnectionsFile is the admin-edited overlay for static-auth services.
// Bootstrap aegis.json values remain for fields this file does not set; a
// present service entry replaces endpoint/headers/canonical_for as a unit so
// the admin UI can own Immich setup without rewriting aegis.json.
type serviceConnectionsFile struct {
	Version  int                               `json:"version"`
	Services map[string]serviceConnectionEntry `json:"services"`
	// Tailscale holds operator credentials for the Admin API used to check
	// and fix Immich VIP Service setup (issue #124). Presence only in APIs.
	Tailscale *tailscaleAdminEntry `json:"tailscale,omitempty"`
}

// tailscaleAdminEntry is the operator-held Tailscale Admin API credential
// (issue #124). APIKey is held in memory from the encrypted token store
// (issue #184) and never echoed back. Legacy plaintext files may still
// carry api_key; it is migrated on load.
type tailscaleAdminEntry struct {
	// APIKey is a tskey-api-… access token (or OAuth access token).
	APIKey string `json:"api_key,omitempty"`
	// Tailnet is the API tailnet path ("-" for the default tailnet).
	Tailnet string `json:"tailnet,omitempty"`
	// HostDeviceID is the device that runs Immich (for tagging / approval).
	HostDeviceID string `json:"host_device_id,omitempty"`
	// HostTag is the service-host tag (default tag:immich-host).
	HostTag string `json:"host_tag,omitempty"`
}

func defaultServiceConnectionsPath(configPath string) string {
	dir := strings.TrimSpace(configPath)
	if dir == "" {
		return "service_connections.json"
	}
	// Sibling of aegis.json / data_grants.json.
	if i := strings.LastIndexAny(dir, `/\`); i >= 0 {
		return dir[:i+1] + "service_connections.json"
	}
	return "service_connections.json"
}

func loadServiceConnections(path string) (*serviceConnectionsFile, error) {
	ov := &serviceConnectionsFile{
		Version:  serviceConnectionsFileVersion,
		Services: map[string]serviceConnectionEntry{},
	}
	if strings.TrimSpace(path) == "" {
		return ov, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ov, nil
		}
		return nil, fmt.Errorf("reading service connections overlay: %w", err)
	}
	if err := json.Unmarshal(data, ov); err != nil {
		return nil, fmt.Errorf("parsing service connections overlay: %w", err)
	}
	if ov.Services == nil {
		ov.Services = map[string]serviceConnectionEntry{}
	}
	// Resolve $ENV: indirection for in-memory use. Migration encrypts the
	// resolved values into the token store and strips the plaintext file.
	resolveServiceConnectionSecrets(ov.Services)
	if ov.Tailscale != nil && ov.Tailscale.APIKey != "" {
		ov.Tailscale.APIKey = resolveSecret("service_connections.tailscale.api_key", ov.Tailscale.APIKey)
	}
	return ov, nil
}

// stripServiceConnectionSecrets removes secret values from an overlay
// snapshot so the on-disk file never writes them (issue #184).
func stripServiceConnectionSecrets(ov *serviceConnectionsFile) *serviceConnectionsFile {
	out := &serviceConnectionsFile{
		Version:  serviceConnectionsFileVersion,
		Services: map[string]serviceConnectionEntry{},
	}
	for id, entry := range ov.Services {
		entry.Headers = nil
		out.Services[id] = entry
	}
	if ov.Tailscale != nil {
		ts := *ov.Tailscale
		ts.APIKey = ""
		out.Tailscale = &ts
	}
	return out
}

func saveServiceConnections(path string, ov *serviceConnectionsFile) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("service connections overlay has no path; cannot save")
	}
	out := stripServiceConnectionSecrets(ov)
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	// 0600 — file is non-secret after #184, but stays tight as local state.
	if err := atomicWriteFile(path, data, 0o600); err != nil {
		return err
	}
	slog.Info("service connections overlay saved", "path", path, "services", len(out.Services))
	return nil
}

// InitServiceConnectionsStore sets the overlay path, attaches the encrypted
// secret store, migrates any legacy plaintext credentials into it, and
// applies existing entries onto bootstrap services. Production startup passes
// the data-dir default next to aegis.json and the TokenStore unlocked with
// -token-pass (issue #184).
func (c *Config) InitServiceConnectionsStore(path string, secrets *TokenStore) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.svcConnPath = path
	c.secrets = secrets
	if err := c.applyServiceConnectionsOverlayLocked(); err != nil {
		return err
	}
	return c.migratePlaintextServiceSecretsLocked()
}

// applyServiceConnectionsOverlayLocked reloads admin-saved connection settings.
// Overlay entries that claim canonical kinds take ownership — those kinds are
// stripped from bootstrap services so aegis.json + overlay stay single-owner.
// Static credentials come from the encrypted token store when attached;
// legacy plaintext values remain in memory only until migration.
// Caller must hold c.mu.
func (c *Config) applyServiceConnectionsOverlayLocked() error {
	ov, err := loadServiceConnections(c.svcConnPath)
	if err != nil {
		return err
	}
	c.hydrateServiceConnectionSecretsLocked(ov)
	c.svcConnOverlay = ov.Services
	c.tailscaleAdmin = ov.Tailscale
	for id, entry := range ov.Services {
		c.applyServiceConnectionEntryLocked(id, entry)
	}
	// Enforce single-owner: kinds claimed by overlay services are removed from
	// every other service (bootstrap aegis.json is not rewritten).
	claims := map[string]string{} // kind -> overlay service id
	for id, entry := range ov.Services {
		for _, k := range entry.CanonicalFor {
			claims[strings.ToLower(strings.TrimSpace(k))] = id
		}
	}
	if len(claims) > 0 {
		for i := range c.Services {
			id := c.Services[i].ID
			kept := make([]string, 0, len(c.Services[i].CanonicalFor))
			for _, k := range c.Services[i].CanonicalFor {
				lower := strings.ToLower(strings.TrimSpace(k))
				if owner, claimed := claims[lower]; claimed && owner != id {
					continue
				}
				kept = append(kept, k)
			}
			c.Services[i].CanonicalFor = normalizeCanonicalFor(kept)
		}
	}
	return validateCanonicalFor(c.Services)
}

// hydrateServiceConnectionSecretsLocked fills header maps and the Tailscale
// API key from the encrypted token store. Legacy plaintext values already on
// the entry are kept in memory so migration can move them.
// Caller must hold c.mu.
func (c *Config) hydrateServiceConnectionSecretsLocked(ov *serviceConnectionsFile) {
	if c.secrets == nil {
		return
	}
	for id, entry := range ov.Services {
		if len(entry.Headers) > 0 {
			continue
		}
		if h := c.loadServiceHeadersLocked(id); len(h) > 0 {
			entry.Headers = h
			ov.Services[id] = entry
		}
	}
	if ov.Tailscale != nil && ov.Tailscale.APIKey == "" {
		ov.Tailscale.APIKey = c.secrets.GetSecret(TailscaleAPIKeySecretKey())
	}
}

// loadServiceHeadersLocked reads a service's static headers from the
// encrypted token store. Caller must hold c.mu.
func (c *Config) loadServiceHeadersLocked(id string) map[string]string {
	if c.secrets == nil {
		return nil
	}
	raw := c.secrets.GetSecret(ServiceHeadersSecretKey(id))
	if raw == "" {
		return nil
	}
	var headers map[string]string
	if err := json.Unmarshal([]byte(raw), &headers); err != nil {
		slog.Warn("failed to parse stored service headers", "service", id, "error", err)
		return nil
	}
	return headers
}

// saveServiceHeadersLocked writes a service's static headers into the
// encrypted token store (never into service_connections.json).
// Caller must hold c.mu.
func (c *Config) saveServiceHeadersLocked(id string, headers map[string]string) error {
	key := ServiceHeadersSecretKey(id)
	if len(headers) == 0 {
		if c.secrets == nil {
			return nil
		}
		return c.secrets.DeleteSecret(key)
	}
	if c.secrets == nil {
		return fmt.Errorf("encrypted token store is required to save static credentials (issue #184)")
	}
	raw, err := json.Marshal(headers)
	if err != nil {
		return err
	}
	return c.secrets.SetSecret(key, string(raw))
}

// migratePlaintextServiceSecretsLocked moves legacy plaintext credentials
// from service_connections.json into the encrypted token store, then rewrites
// the overlay without secrets. Logged once per migration.
// Caller must hold c.mu.
func (c *Config) migratePlaintextServiceSecretsLocked() error {
	if c.secrets == nil {
		return nil
	}
	ov, err := loadServiceConnections(c.svcConnPath)
	if err != nil {
		return err
	}
	migrated := 0
	for id, entry := range ov.Services {
		if len(entry.Headers) == 0 {
			continue
		}
		headers := cloneHeaderMap(entry.Headers)
		for k, v := range headers {
			headers[k] = resolveSecret("service_connections."+id+".headers."+k, v)
		}
		if err := c.saveServiceHeadersLocked(id, headers); err != nil {
			return fmt.Errorf("migrating headers for %s: %w", id, err)
		}
		migrated++
		entry.Headers = nil
		ov.Services[id] = entry
	}
	if ov.Tailscale != nil && strings.TrimSpace(ov.Tailscale.APIKey) != "" {
		key := resolveSecret("service_connections.tailscale.api_key", ov.Tailscale.APIKey)
		if err := c.secrets.SetSecret(TailscaleAPIKeySecretKey(), key); err != nil {
			return fmt.Errorf("migrating tailscale api key: %w", err)
		}
		migrated++
		ov.Tailscale.APIKey = ""
	}
	if migrated == 0 {
		return nil
	}
	if err := saveServiceConnections(c.svcConnPath, ov); err != nil {
		return fmt.Errorf("rewriting service connections after secret migration: %w", err)
	}
	slog.Info("migrated plaintext service-connection credentials into encrypted token store",
		"path", c.svcConnPath,
		"credentials", migrated,
	)
	// Reload so in-memory overlay matches the stripped file + secret store.
	return c.applyServiceConnectionsOverlayLocked()
}

// applyServiceConnectionEntryLocked upserts one overlay entry onto Services.
// Caller must hold c.mu.
func (c *Config) applyServiceConnectionEntryLocked(id string, entry serviceConnectionEntry) {
	idx := -1
	for i := range c.Services {
		if c.Services[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		svc := Service{
			ID:       id,
			Name:     entry.Name,
			Action:   entry.Action,
			Endpoint: entry.Endpoint,
		}
		if svc.Name == "" {
			svc.Name = id
		}
		if svc.Action == "" {
			svc.Action = "inject"
		}
		if entry.CacheTTL != "" {
			var d Duration
			if err := d.UnmarshalJSON([]byte(`"` + entry.CacheTTL + `"`)); err == nil {
				svc.CacheTTL = d
			}
		}
		if entry.Description != "" {
			svc.Description = entry.Description
		}
		if len(entry.Aliases) > 0 {
			svc.Aliases = append([]string(nil), entry.Aliases...)
		}
		svc.Headers = cloneHeaderMap(entry.Headers)
		svc.CanonicalFor = normalizeCanonicalFor(entry.CanonicalFor)
		c.Services = append(c.Services, svc)
		return
	}
	svc := &c.Services[idx]
	if entry.Name != "" {
		svc.Name = entry.Name
	}
	if entry.Action != "" {
		svc.Action = entry.Action
	}
	if entry.Endpoint != "" {
		svc.Endpoint = entry.Endpoint
	}
	if entry.CacheTTL != "" {
		var d Duration
		if err := d.UnmarshalJSON([]byte(`"` + entry.CacheTTL + `"`)); err == nil {
			svc.CacheTTL = d
		}
	}
	if entry.Description != "" {
		svc.Description = entry.Description
	}
	if len(entry.Aliases) > 0 {
		svc.Aliases = append([]string(nil), entry.Aliases...)
	}
	if entry.Headers != nil {
		svc.Headers = cloneHeaderMap(entry.Headers)
	}
	if entry.ClearCanonicalFor {
		svc.CanonicalFor = nil
	} else if entry.CanonicalFor != nil {
		svc.CanonicalFor = normalizeCanonicalFor(entry.CanonicalFor)
	}
}

func cloneHeaderMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// SetServiceConnection upserts admin-owned connection fields for a service
// (endpoint, static headers, canonical_for) and persists the overlay. Used by
// the Immich setup flow so operators never have to hand-edit aegis.json.
// An empty API key leaves existing headers unchanged.
func (c *Config) SetServiceConnection(id string, entry serviceConnectionEntry) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("service id is required")
	}
	if entry.Endpoint != "" {
		entry.Endpoint = strings.TrimSpace(entry.Endpoint)
		if entry.Endpoint == "" {
			return fmt.Errorf("endpoint is required")
		}
	}
	entry.CanonicalFor = normalizeCanonicalFor(entry.CanonicalFor)
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.svcConnOverlay == nil {
		c.svcConnOverlay = map[string]serviceConnectionEntry{}
	}
	prev := c.svcConnOverlay[id]
	merged := prev
	if entry.Name != "" {
		merged.Name = entry.Name
	}
	if entry.Action != "" {
		merged.Action = entry.Action
	}
	if entry.Endpoint != "" {
		merged.Endpoint = entry.Endpoint
	}
	if entry.CacheTTL != "" {
		merged.CacheTTL = entry.CacheTTL
	}
	if entry.Description != "" {
		merged.Description = entry.Description
	}
	if len(entry.Aliases) > 0 {
		merged.Aliases = append([]string(nil), entry.Aliases...)
	}
	if entry.Headers != nil {
		if c.secrets == nil {
			return fmt.Errorf("encrypted token store is required to save static credentials (issue #184)")
		}
		if merged.Headers == nil {
			merged.Headers = map[string]string{}
		}
		for k, v := range entry.Headers {
			merged.Headers[k] = v
		}
	}
	if entry.ClearCanonicalFor {
		merged.ClearCanonicalFor = true
		merged.CanonicalFor = nil
	} else if entry.CanonicalFor != nil {
		merged.ClearCanonicalFor = false
		merged.CanonicalFor = append([]string(nil), entry.CanonicalFor...)
	}

	// Strip this kind from other services when this entry claims it, so the
	// single-owner rule holds without forcing the admin to edit aegis.json.
	if len(merged.CanonicalFor) > 0 {
		claims := map[string]bool{}
		for _, k := range merged.CanonicalFor {
			claims[strings.ToLower(strings.TrimSpace(k))] = true
		}
		for i := range c.Services {
			if c.Services[i].ID == id {
				continue
			}
			kept := make([]string, 0, len(c.Services[i].CanonicalFor))
			for _, k := range c.Services[i].CanonicalFor {
				if claims[strings.ToLower(strings.TrimSpace(k))] {
					continue
				}
				kept = append(kept, k)
			}
			c.Services[i].CanonicalFor = normalizeCanonicalFor(kept)
		}
	}

	c.svcConnOverlay[id] = merged
	c.applyServiceConnectionEntryLocked(id, merged)

	// Drop photo library cache so the next list reflects the new home copy.
	if err := validateCanonicalFor(c.Services); err != nil {
		return err
	}
	if err := c.saveServiceHeadersLocked(id, merged.Headers); err != nil {
		return err
	}
	if err := saveServiceConnections(c.svcConnPath, &serviceConnectionsFile{
		Version:   serviceConnectionsFileVersion,
		Services:  c.svcConnOverlay,
		Tailscale: c.tailscaleAdmin,
	}); err != nil {
		return err
	}
	return nil
}

// HasServiceAPIKey reports whether the service carries a static API key header
// (presence only — never the value).
func (s Service) HasStaticAPIKey() bool {
	for k := range s.Headers {
		lower := strings.ToLower(k)
		if lower == "x-api-key" || strings.Contains(lower, "api-key") || strings.Contains(lower, "apikey") {
			return strings.TrimSpace(s.Headers[k]) != ""
		}
	}
	return false
}

// tailscaleAdminSnapshot is a redacted view of the stored Tailscale Admin
// credential. The API key is never included — only presence and routing.
type tailscaleAdminSnapshot struct {
	HasAPIKey    bool   `json:"has_api_key"`
	Tailnet      string `json:"tailnet,omitempty"`
	HostDeviceID string `json:"host_device_id,omitempty"`
	HostTag      string `json:"host_tag,omitempty"`
}

// TailscaleAdminSnapshot returns non-secret Tailscale Admin settings.
func (c *Config) TailscaleAdminSnapshot() tailscaleAdminSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tailscaleAdminSnapshotLocked()
}

func (c *Config) tailscaleAdminSnapshotLocked() tailscaleAdminSnapshot {
	if c.tailscaleAdmin == nil {
		return tailscaleAdminSnapshot{}
	}
	return tailscaleAdminSnapshot{
		HasAPIKey:    strings.TrimSpace(c.tailscaleAdmin.APIKey) != "",
		Tailnet:      strings.TrimSpace(c.tailscaleAdmin.Tailnet),
		HostDeviceID: strings.TrimSpace(c.tailscaleAdmin.HostDeviceID),
		HostTag:      strings.TrimSpace(c.tailscaleAdmin.HostTag),
	}
}

// TailscaleAdminAPIKey returns the stored Admin API key for outbound calls
// (internal only — never serialize this into admin responses).
func (c *Config) TailscaleAdminAPIKey() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.tailscaleAdmin == nil {
		return ""
	}
	return strings.TrimSpace(c.tailscaleAdmin.APIKey)
}

// SetTailscaleAdmin upserts operator Tailscale Admin settings and persists
// service_connections.json. An empty API key leaves the stored key unchanged.
func (c *Config) SetTailscaleAdmin(entry tailscaleAdminEntry) error {
	entry.Tailnet = strings.TrimSpace(entry.Tailnet)
	entry.HostDeviceID = strings.TrimSpace(entry.HostDeviceID)
	entry.HostTag = strings.TrimSpace(entry.HostTag)
	entry.APIKey = strings.TrimSpace(entry.APIKey)

	c.mu.Lock()
	defer c.mu.Unlock()
	merged := tailscaleAdminEntry{}
	if c.tailscaleAdmin != nil {
		merged = *c.tailscaleAdmin
	}
	if entry.APIKey != "" {
		merged.APIKey = entry.APIKey
	}
	if entry.Tailnet != "" {
		merged.Tailnet = entry.Tailnet
	}
	if entry.HostDeviceID == "-" {
		merged.HostDeviceID = ""
	} else if entry.HostDeviceID != "" {
		merged.HostDeviceID = entry.HostDeviceID
	}
	if entry.HostTag != "" {
		merged.HostTag = entry.HostTag
	}
	c.tailscaleAdmin = &merged

	if c.secrets == nil && merged.APIKey != "" {
		return fmt.Errorf("encrypted token store is required to save static credentials (issue #184)")
	}
	if c.secrets != nil {
		if err := c.secrets.SetSecret(TailscaleAPIKeySecretKey(), merged.APIKey); err != nil {
			return err
		}
	}
	if err := saveServiceConnections(c.svcConnPath, &serviceConnectionsFile{
		Version:   serviceConnectionsFileVersion,
		Services:  c.svcConnOverlay,
		Tailscale: c.tailscaleAdmin,
	}); err != nil {
		return err
	}
	return nil
}
