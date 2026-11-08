package core

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/shared"
)

// dataGrantsFileVersion is the on-disk schema version for data_grants.json.
// Version 3 stores agent data grants as primary (issue #148 / #174) and keeps
// service limits as tighten-only overrides that can only restrict a data grant.
const dataGrantsFileVersion = 3

// dataGrantsOverlayFile is the admin-edited data-grant overlay (data_grants.json).
//
// Primary (v3): DataGrants maps data-type keys to DataGrant —
// default access level plus optional per-provider tighten-only overrides.
// This is the product "data grant" for the LLM agent (issue #174).
//
// Migration: ServiceLimits maps service id → legacy Permissions. Those maps
// are treated as provider overrides that can only further restrict the data
// grant (compat window; see AGENTS.md). Bootstrap grants stay in aegis.json;
// this file wins when a key is present. A present service key with null
// clears the block. Contains no secrets — safe to manage from the admin UI
// without rewriting OAuth client credentials in aegis.json.
type dataGrantsOverlayFile struct {
	Version int `json:"version"`
	// DataGrants is the primary agent data-grant map (typeID → DataGrant).
	// A present map (including empty) replaces aegis.json data_grants;
	// null/absent leaves bootstrap grants in place.
	DataGrants map[string]policy.DataGrant `json:"data_grants"`
	// ServiceLimits maps service id → tighten-only grant limits.
	// Present+null means cleared.
	ServiceLimits map[string]*policy.Permissions `json:"service_limits"`
}

// loadDataGrantsOverlay reads the overlay file. A missing file yields a
// zero overlay (bootstrap data grants remain effective). A present
// `data_grants` map — including empty — is authoritative for agent data grants.
func loadDataGrantsOverlay(path string) (*dataGrantsOverlayFile, error) {
	ov := &dataGrantsOverlayFile{Version: dataGrantsFileVersion}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &dataGrantsOverlayFile{}, nil
		}
		return nil, fmt.Errorf("reading data grants overlay: %w", err)
	}
	if err := json.Unmarshal(data, ov); err != nil {
		return nil, fmt.Errorf("parsing data grants overlay: %w", err)
	}
	if ov.ServiceLimits == nil {
		ov.ServiceLimits = map[string]*policy.Permissions{}
	}
	if ov.DataGrants != nil {
		if err := policy.ValidateDataGrants(ov.DataGrants); err != nil {
			return nil, fmt.Errorf("data grants overlay: %w", err)
		}
	}
	for id, p := range ov.ServiceLimits {
		if p == nil {
			continue
		}
		if err := policy.ValidatePermissions(*p); err != nil {
			return nil, fmt.Errorf("data grants overlay service %q: %w", id, err)
		}
	}
	return ov, nil
}

// saveDataGrantsOverlay atomically writes the overlay file as v3
// (data_grants + service_limits only — legacy keys are not written).
func saveDataGrantsOverlay(path string, ov *dataGrantsOverlayFile) error {
	if path == "" {
		return fmt.Errorf("data grants overlay has no path; cannot save")
	}
	out := dataGrantsOverlayFile{
		Version:       dataGrantsFileVersion,
		DataGrants:    ov.DataGrants,
		ServiceLimits: ov.ServiceLimits,
	}
	if out.ServiceLimits == nil {
		out.ServiceLimits = map[string]*policy.Permissions{}
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := shared.AtomicWriteFile(path, data, 0o644); err != nil {
		return err
	}
	slog.Info("data grants overlay saved", "path", path,
		"data_grants", len(out.DataGrants), "service_limits", len(out.ServiceLimits))
	return nil
}

// InitDataGrantsStore overrides the overlay path and applies any existing
// overlay onto bootstrap permissions from aegis.json. Production startup
// passes the data-dir default or an explicit -data-grants-file value.
func (c *Config) InitDataGrantsStore(path string) error {
	if path == "" {
		return fmt.Errorf("data grants overlay path is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dataGrantsPath = path
	return c.applyDataGrantsOverlayLocked()
}

// applyDataGrantsOverlayLocked reloads the overlay from disk and rewrites
// in-memory data grants and service permission overrides. Bootstrap values
// remain for services absent from the overlay. A present overlay
// `data_grants` (or v2 `types`) map — including empty — replaces aegis.json
// `data_grants`; a v1/missing map leaves bootstrap grants in place.
// Caller must hold c.mu.
func (c *Config) applyDataGrantsOverlayLocked() error {
	ov, err := loadDataGrantsOverlay(c.dataGrantsPath)
	if err != nil {
		return err
	}
	if ov.DataGrants != nil {
		c.typePolicies = ov.DataGrants
	}
	c.serviceLimits = ov.ServiceLimits
	for i := range c.Services {
		id := c.Services[i].ID
		p, ok := ov.ServiceLimits[id]
		if !ok {
			continue
		}
		if p == nil || len(*p) == 0 {
			c.Services[i].GrantLimits = nil
			continue
		}
		c.Services[i].GrantLimits = policy.Permissions(p.Clone())
	}
	return nil
}

// setServiceLimitsEntryLocked updates the in-memory overlay entry for a
// service and persists the overlay file. Empty/nil p is stored as explicit
// clear (JSON null) so bootstrap permissions do not resurface on reload.
// Service maps are migration overrides — they only tighten data grant.
// Caller must hold c.mu.
func (c *Config) setServiceLimitsEntryLocked(id string, p policy.Permissions) error {
	if c.serviceLimits == nil {
		c.serviceLimits = map[string]*policy.Permissions{}
	}
	if len(p) == 0 {
		c.serviceLimits[id] = nil
	} else {
		cloned := policy.Permissions(p.Clone())
		c.serviceLimits[id] = &cloned
	}
	return saveDataGrantsOverlay(c.dataGrantsPath, &dataGrantsOverlayFile{
		Version:       dataGrantsFileVersion,
		DataGrants:    c.typePolicies,
		ServiceLimits: c.serviceLimits,
	})
}

// setDataGrantsLocked replaces the agent data-grant map and persists the
// overlay. An empty map is stored as an explicit empty `data_grants` object
// so it clears bootstrap grants on reload (rather than falling back to them).
// Caller must hold c.mu.
func (c *Config) setDataGrantsLocked(policies policy.DataGrants) error {
	if err := policy.ValidateDataGrants(policies); err != nil {
		return err
	}
	if policies == nil {
		c.typePolicies = map[string]policy.DataGrant{}
	} else {
		c.typePolicies = make(policy.DataGrants, len(policies))
		for k, v := range policies {
			c.typePolicies[k] = cloneDataGrant(v)
		}
	}
	return saveDataGrantsOverlay(c.dataGrantsPath, &dataGrantsOverlayFile{
		Version:       dataGrantsFileVersion,
		DataGrants:    c.typePolicies,
		ServiceLimits: c.serviceLimits,
	})
}

func cloneDataGrant(p policy.DataGrant) policy.DataGrant {
	out := policy.DataGrant{Default: policy.NormalizeAccess(p.Default)}
	if p.Default == "" {
		out.Default = ""
	}
	if len(p.Providers) == 0 {
		return out
	}
	out.Providers = make(map[string]string, len(p.Providers))
	for k, v := range p.Providers {
		out.Providers[k] = policy.NormalizeAccess(v)
	}
	return out
}
