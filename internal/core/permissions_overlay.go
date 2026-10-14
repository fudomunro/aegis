package core

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/fudomunro/aegis/internal/types"
)

// permissionsFileVersion is the on-disk schema version for permissions.json.
// Version 2 stores type-owned policies as primary (issue #148) and keeps
// service maps as migration overrides that can only tighten the type default.
const permissionsFileVersion = 2

// permissionsOverlayFile is the admin-edited agent permission overlay.
//
// Primary (v2): Types maps data-type / permission keys to TypePolicy —
// type default plus optional per-provider tighten-only overrides.
//
// Migration: Services maps service id → legacy Permissions. Those maps are
// treated as provider overrides that can only further restrict the type
// default (compat window; see AGENTS.md "service permissions dual-path").
// Bootstrap permissions stay in aegis.json; this file wins when a key is
// present. A present service key with null clears the block.
// Contains no secrets — safe to manage from the admin UI without rewriting
// OAuth client credentials in aegis.json.
type permissionsOverlayFile struct {
	Version int `json:"version"`
	// Types is the primary type-owned policy map (typeID → TypePolicy).
	// A present map (including empty) replaces aegis.json type_policies;
	// null/absent leaves bootstrap policies in place.
	Types map[string]types.TypePolicy `json:"types"`
	// Services maps service id → permissions. Present+null means cleared.
	Services map[string]*types.Permissions `json:"services"`
}

// loadPermissionsOverlay reads the overlay file. A missing file yields a
// zero overlay (bootstrap permissions and type policies remain effective).
// Version 1 files (services only) load unchanged; their service maps remain
// migration overrides until rewritten as type policies. A present `types`
// map — including empty — is authoritative for type policy.
func loadPermissionsOverlay(path string) (*permissionsOverlayFile, error) {
	ov := &permissionsOverlayFile{
		Version:  permissionsFileVersion,
		Services: map[string]*types.Permissions{},
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &permissionsOverlayFile{}, nil
		}
		return nil, fmt.Errorf("reading permissions overlay: %w", err)
	}
	if err := json.Unmarshal(data, ov); err != nil {
		return nil, fmt.Errorf("parsing permissions overlay: %w", err)
	}
	if ov.Services == nil {
		ov.Services = map[string]*types.Permissions{}
	}
	if ov.Types != nil {
		if err := types.ValidateTypePolicies(ov.Types); err != nil {
			return nil, fmt.Errorf("permissions overlay types: %w", err)
		}
	}
	for id, p := range ov.Services {
		if p == nil {
			continue
		}
		if err := types.ValidatePermissions(*p); err != nil {
			return nil, fmt.Errorf("permissions overlay service %q: %w", id, err)
		}
	}
	return ov, nil
}

// savePermissionsOverlay atomically writes the overlay file.
func savePermissionsOverlay(path string, ov *permissionsOverlayFile) error {
	if path == "" {
		return fmt.Errorf("permissions overlay has no path; cannot save")
	}
	out := permissionsOverlayFile{
		Version:  permissionsFileVersion,
		Types:    ov.Types,
		Services: ov.Services,
	}
	if out.Services == nil {
		out.Services = map[string]*types.Permissions{}
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := atomicWriteFile(path, data, 0o644); err != nil {
		return err
	}
	slog.Info("permissions overlay saved", "path", path,
		"types", len(out.Types), "services", len(out.Services))
	return nil
}

// InitPermissionsStore overrides the overlay path and applies any existing
// overlay onto bootstrap permissions from aegis.json. Production startup
// passes the data-dir default or an explicit -permissions-file value.
func (c *Config) InitPermissionsStore(path string) error {
	if path == "" {
		return fmt.Errorf("permissions overlay path is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.permsPath = path
	return c.applyPermissionsOverlayLocked()
}

// applyPermissionsOverlayLocked reloads the overlay from disk and rewrites
// in-memory type policies and service permission overrides. Bootstrap values
// remain for services absent from the overlay. A present overlay `types` map
// (including empty) replaces aegis.json `type_policies`; a v1/missing types
// section leaves bootstrap policies in place. Caller must hold c.mu.
func (c *Config) applyPermissionsOverlayLocked() error {
	ov, err := loadPermissionsOverlay(c.permsPath)
	if err != nil {
		return err
	}
	if ov.Types != nil {
		c.typePolicies = ov.Types
	}
	c.permsOverlay = ov.Services
	for i := range c.Services {
		id := c.Services[i].ID
		p, ok := ov.Services[id]
		if !ok {
			continue
		}
		if p == nil || len(*p) == 0 {
			c.Services[i].Permissions = nil
			continue
		}
		c.Services[i].Permissions = types.Permissions(p.Clone())
	}
	return nil
}

// setPermissionsOverlayEntryLocked updates the in-memory overlay entry for a
// service and persists the overlay file. Empty/nil p is stored as explicit
// clear (JSON null) so bootstrap permissions do not resurface on reload.
// Service maps are migration overrides — they only tighten type policy.
// Caller must hold c.mu.
func (c *Config) setPermissionsOverlayEntryLocked(id string, p types.Permissions) error {
	if c.permsOverlay == nil {
		c.permsOverlay = map[string]*types.Permissions{}
	}
	if len(p) == 0 {
		c.permsOverlay[id] = nil
	} else {
		cloned := types.Permissions(p.Clone())
		c.permsOverlay[id] = &cloned
	}
	return savePermissionsOverlay(c.permsPath, &permissionsOverlayFile{
		Version:  permissionsFileVersion,
		Types:    c.typePolicies,
		Services: c.permsOverlay,
	})
}

// setTypePoliciesLocked replaces the type-owned policy map and persists the
// overlay. An empty map is stored as an explicit empty `types` object so it
// clears bootstrap policies on reload (rather than falling back to them).
// Caller must hold c.mu.
func (c *Config) setTypePoliciesLocked(policies types.TypePolicies) error {
	if err := types.ValidateTypePolicies(policies); err != nil {
		return err
	}
	if policies == nil {
		c.typePolicies = map[string]types.TypePolicy{}
	} else {
		c.typePolicies = make(types.TypePolicies, len(policies))
		for k, v := range policies {
			c.typePolicies[k] = cloneTypePolicy(v)
		}
	}
	return savePermissionsOverlay(c.permsPath, &permissionsOverlayFile{
		Version:  permissionsFileVersion,
		Types:    c.typePolicies,
		Services: c.permsOverlay,
	})
}

func cloneTypePolicy(p types.TypePolicy) types.TypePolicy {
	out := types.TypePolicy{Default: types.NormalizeAccess(p.Default)}
	if p.Default == "" {
		out.Default = ""
	}
	if len(p.Providers) == 0 {
		return out
	}
	out.Providers = make(map[string]string, len(p.Providers))
	for k, v := range p.Providers {
		out.Providers[k] = types.NormalizeAccess(v)
	}
	return out
}
