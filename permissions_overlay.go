package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
)

// permissionsFileVersion is the on-disk schema version for permissions.json.
const permissionsFileVersion = 1

// permissionsOverlayFile is the admin-edited agent permission overlay.
// Bootstrap permissions stay in aegis.json; this file wins when a service
// id is present. A present key with null clears the block (unrestricted).
// Contains no secrets — safe to manage from the admin UI without rewriting
// OAuth client credentials in aegis.json.
type permissionsOverlayFile struct {
	Version int `json:"version"`
	// Services maps service id → permissions. Present+null means cleared.
	Services map[string]*Permissions `json:"services"`
}

// loadPermissionsOverlay reads the overlay file. A missing file yields an
// empty overlay (bootstrap permissions remain effective).
func loadPermissionsOverlay(path string) (*permissionsOverlayFile, error) {
	ov := &permissionsOverlayFile{
		Version:  permissionsFileVersion,
		Services: map[string]*Permissions{},
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ov, nil
		}
		return nil, fmt.Errorf("reading permissions overlay: %w", err)
	}
	if err := json.Unmarshal(data, ov); err != nil {
		return nil, fmt.Errorf("parsing permissions overlay: %w", err)
	}
	if ov.Services == nil {
		ov.Services = map[string]*Permissions{}
	}
	for id, p := range ov.Services {
		if p == nil {
			continue
		}
		if err := ValidatePermissions(*p); err != nil {
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
		Services: ov.Services,
	}
	if out.Services == nil {
		out.Services = map[string]*Permissions{}
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := atomicWriteFile(path, data, 0o644); err != nil {
		return err
	}
	slog.Info("permissions overlay saved", "path", path, "services", len(out.Services))
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
// in-memory service permissions. Bootstrap values remain for services absent
// from the overlay. Caller must hold c.mu.
func (c *Config) applyPermissionsOverlayLocked() error {
	ov, err := loadPermissionsOverlay(c.permsPath)
	if err != nil {
		return err
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
		c.Services[i].Permissions = Permissions(p.Clone())
	}
	return nil
}

// setPermissionsOverlayEntryLocked updates the in-memory overlay entry for a
// service and persists the overlay file. Empty/nil p is stored as explicit
// clear (JSON null) so bootstrap permissions do not resurface on reload.
// Caller must hold c.mu.
func (c *Config) setPermissionsOverlayEntryLocked(id string, p Permissions) error {
	if c.permsOverlay == nil {
		c.permsOverlay = map[string]*Permissions{}
	}
	if len(p) == 0 {
		c.permsOverlay[id] = nil
	} else {
		cloned := Permissions(p.Clone())
		c.permsOverlay[id] = &cloned
	}
	return savePermissionsOverlay(c.permsPath, &permissionsOverlayFile{
		Version:  permissionsFileVersion,
		Services: c.permsOverlay,
	})
}
