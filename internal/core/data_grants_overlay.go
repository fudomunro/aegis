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

// dataGrantsOverlayFile is the admin-edited data-grant overlay
// (data_grants.json). DataGrants maps kind keys to DataGrant — the product
// "data grant" for the LLM agent (issue #174). Bootstrap grants stay in
// aegis.json; this file wins when the map is present. Contains no secrets —
// safe to manage from the admin UI without rewriting OAuth client
// credentials in aegis.json. Legacy `service_limits` keys in old files are
// ignored.
type dataGrantsOverlayFile struct {
	Version int `json:"version"`
	// DataGrants is the agent data-grant map (typeID → DataGrant).
	// A present map (including empty) replaces aegis.json data_grants;
	// null/absent leaves bootstrap grants in place.
	DataGrants map[string]policy.DataGrant `json:"data_grants"`
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
	if ov.DataGrants != nil {
		if err := policy.ValidateDataGrants(ov.DataGrants); err != nil {
			return nil, fmt.Errorf("data grants overlay: %w", err)
		}
	}
	return ov, nil
}

// saveDataGrantsOverlay atomically writes the overlay file as v3
// (data_grants only — legacy keys are not written).
func saveDataGrantsOverlay(path string, ov *dataGrantsOverlayFile) error {
	if path == "" {
		return fmt.Errorf("data grants overlay has no path; cannot save")
	}
	out := dataGrantsOverlayFile{
		Version:    dataGrantsFileVersion,
		DataGrants: ov.DataGrants,
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
		"data_grants", len(out.DataGrants))
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
// the in-memory data grants. A present overlay `data_grants` map —
// including empty — replaces aegis.json `data_grants`; a missing map leaves
// bootstrap grants in place. Caller must hold c.mu.
func (c *Config) applyDataGrantsOverlayLocked() error {
	ov, err := loadDataGrantsOverlay(c.dataGrantsPath)
	if err != nil {
		return err
	}
	if ov.DataGrants != nil {
		c.typePolicies = ov.DataGrants
	}
	return nil
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
		Version:    dataGrantsFileVersion,
		DataGrants: c.typePolicies,
	})
}

func cloneDataGrant(p policy.DataGrant) policy.DataGrant {
	if p.Default == "" {
		return policy.DataGrant{}
	}
	return policy.DataGrant{Default: shared.NormalizeAccess(p.Default)}
}
