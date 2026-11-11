package core

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/sampledata"
	"github.com/fudomunro/aegis/internal/shared"
)

// Sync relationships (issue #365) record which sync-participating kinds a
// connected source belongs to, and which member of each kind's group is the
// designated canonical home. Rows live on the source's service-connections
// entry (the `sync` block) — one per (source, Kind).
//
// Hub designation: the row's Canonical flag is the relationship's hub field,
// and canonical_for is kept in step with it — pointing canonical_for at a
// source reassigns the hub across the group (the bulk knob), and reading it
// shows the hub. For every row-managed kind the two agree exactly
// (flag(S,K) ⇔ S claims K); kinds without rows (e.g. photos) keep the plain
// claim behavior they have today. Write paths update both.
//
// Establishment is idempotent and runs at connect events (OAuth, static
// credential saves) and at startup (migration). Local file imports join as
// read-only members. Sample fixtures are not user data sources and stay out
// of groups.

// syncRelEntry is one stored relationship row (issue #365): membership in a
// kind's sync group plus that group's hub flag.
type syncRelEntry struct {
	Canonical bool `json:"canonical,omitempty"`
}

// EnsureSyncRelationships upserts sync rows for a source (issue #365): one
// row per listed kind, seeded from the source's canonical_for claims. The
// first write-capable source to join a group with no designated member
// claims the hub (the default hub — admin-editable). Rows already present
// keep their flags.
func (c *Config) EnsureSyncRelationships(id string, kindIDs []string, writeCapable func(kind string) bool) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("service id is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	svc := c.serviceByID(id)
	if svc == nil {
		return fmt.Errorf("service %q not configured", id)
	}
	if c.svcConnOverlay == nil {
		c.svcConnOverlay = map[string]serviceConnectionEntry{}
	}
	entry := c.svcConnOverlay[id]
	if entry.Sync == nil {
		entry.Sync = map[string]syncRelEntry{}
	}
	changed := false
	for _, kindID := range kindIDs {
		kind := strings.ToLower(strings.TrimSpace(kindID))
		if kind == "" {
			continue
		}
		if _, ok := entry.Sync[kind]; ok {
			continue
		}
		row := syncRelEntry{}
		if svc.IsCanonicalFor(kindID) {
			row.Canonical = true
		} else if writeCapable != nil && writeCapable(kindID) &&
			!c.anyClaimLocked(kind) && !c.anyCanonicalRowLocked(kind) {
			// Default hub: first write-capable source into a group with no
			// designated member claims it. The claim is the designation
			// language, so it persists alongside the flag.
			row.Canonical = true
			claims := normalizeCanonicalFor(append(append([]string(nil), svc.CanonicalFor...), kindID))
			svc.CanonicalFor = claims
			entry.CanonicalFor = claims
			entry.ClearCanonicalFor = false
		}
		entry.Sync[kind] = row
		changed = true
	}
	if !changed {
		return nil
	}
	c.svcConnOverlay[id] = entry
	c.syncFlagsFromClaimsLocked()
	return c.saveOverlayLocked()
}

// SetSyncRelationship designates (or unsets) a source as the canonical home
// for one kind — the admin edit of a relationship row (issue #365). The
// designation is written to canonical_for (the reassignment language, so it
// fans across the group and survives hand-edits there) and mirrored onto the
// row's hub flag.
func (c *Config) SetSyncRelationship(id string, rel shared.SyncRelationship) error {
	id = strings.TrimSpace(id)
	kind := strings.ToLower(strings.TrimSpace(rel.Kind))
	if id == "" || kind == "" {
		return fmt.Errorf("service id and kind are required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	svc := c.serviceByID(id)
	if svc == nil {
		return fmt.Errorf("service %q not configured", id)
	}
	if c.svcConnOverlay == nil {
		c.svcConnOverlay = map[string]serviceConnectionEntry{}
	}
	entry := c.svcConnOverlay[id]
	if entry.Sync == nil {
		entry.Sync = map[string]syncRelEntry{}
	}
	if _, ok := entry.Sync[kind]; !ok {
		entry.Sync[kind] = syncRelEntry{}
	}

	var claims []string
	for _, k := range svc.CanonicalFor {
		if strings.EqualFold(strings.TrimSpace(k), rel.Kind) {
			continue
		}
		claims = append(claims, k)
	}
	if rel.Canonical {
		claims = append(claims, rel.Kind)
	}
	claims = normalizeCanonicalFor(claims)
	svc.CanonicalFor = claims
	entry.CanonicalFor = claims
	entry.ClearCanonicalFor = len(claims) == 0

	// Single-owner across the group: drop the claim (and flag) everywhere
	// else. In-memory claims cover bootstrap aegis.json holders; overlay
	// entries are rewritten so the file cannot resurrect a rival claim.
	if rel.Canonical {
		for i := range c.Services {
			if c.Services[i].ID == id {
				continue
			}
			kept := make([]string, 0, len(c.Services[i].CanonicalFor))
			for _, k := range c.Services[i].CanonicalFor {
				if strings.EqualFold(strings.TrimSpace(k), rel.Kind) {
					continue
				}
				kept = append(kept, k)
			}
			c.Services[i].CanonicalFor = normalizeCanonicalFor(kept)
		}
		for otherID, other := range c.svcConnOverlay {
			if otherID == id {
				continue
			}
			kept := make([]string, 0, len(other.CanonicalFor))
			for _, k := range other.CanonicalFor {
				if strings.EqualFold(strings.TrimSpace(k), rel.Kind) {
					continue
				}
				kept = append(kept, k)
			}
			if len(kept) == len(other.CanonicalFor) {
				continue
			}
			other.CanonicalFor = normalizeCanonicalFor(kept)
			c.svcConnOverlay[otherID] = other
		}
	}
	c.svcConnOverlay[id] = entry
	c.syncFlagsFromClaimsLocked()
	if err := validateCanonicalFor(c.Services); err != nil {
		return err
	}
	return c.saveOverlayLocked()
}

// syncFlagsFromClaimsLocked mirrors canonical_for claims onto sync row hub
// flags (issue #365). For every row-managed kind the flag equals membership
// in the claim list: claimed → hub, unclaimed → not. Kinds without rows
// (non-participating kinds) are untouched.
// Caller must hold c.mu.
func (c *Config) syncFlagsFromClaimsLocked() {
	claimed := map[string]string{} // kind -> claiming service id
	for i := range c.Services {
		for _, k := range c.Services[i].CanonicalFor {
			kind := strings.ToLower(strings.TrimSpace(k))
			if kind == "" {
				continue
			}
			if _, seen := claimed[kind]; !seen {
				claimed[kind] = c.Services[i].ID
			}
		}
	}
	for id, entry := range c.svcConnOverlay {
		if len(entry.Sync) == 0 {
			continue
		}
		svc := c.serviceByID(id)
		changed := false
		for kind, row := range entry.Sync {
			want := claimed[kind] == id
			if row.Canonical != want {
				row.Canonical = want
				entry.Sync[kind] = row
				changed = true
			}
		}
		if !changed {
			continue
		}
		c.svcConnOverlay[id] = entry
		if svc != nil {
			svc.SyncRelationships = syncFlagMap(entry.Sync)
		}
	}
	// Sources without overlay rows still need their in-memory view current.
	for i := range c.Services {
		id := c.Services[i].ID
		entry := c.svcConnOverlay[id]
		c.Services[i].SyncRelationships = syncFlagMap(entry.Sync)
	}
}

// syncFlagMap projects stored rows onto the in-memory kind → hub-flag view.
func syncFlagMap(rows map[string]syncRelEntry) map[string]bool {
	if len(rows) == 0 {
		return nil
	}
	out := make(map[string]bool, len(rows))
	for kind, row := range rows {
		out[kind] = row.Canonical
	}
	return out
}

// SyncRelationshipsFor returns the sync row view for one source (kind →
// hub flag), as recorded on its connection record.
func (c *Config) SyncRelationshipsFor(id string) map[string]bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry := c.svcConnOverlay[id]
	return syncFlagMap(entry.Sync)
}

// anyClaimLocked reports whether any service claims kind in canonical_for.
// Caller must hold c.mu.
func (c *Config) anyClaimLocked(kind string) bool {
	for i := range c.Services {
		if c.Services[i].IsCanonicalFor(kind) {
			return true
		}
	}
	return false
}

// anyCanonicalRowLocked reports whether some row for kind already carries the
// hub flag. Caller must hold c.mu.
func (c *Config) anyCanonicalRowLocked(kind string) bool {
	for _, entry := range c.svcConnOverlay {
		if row, ok := entry.Sync[kind]; ok && row.Canonical {
			return true
		}
	}
	return false
}

// saveOverlayLocked persists the service-connections overlay. Caller must
// hold c.mu.
func (c *Config) saveOverlayLocked() error {
	if c.svcConnOverlay == nil {
		c.svcConnOverlay = map[string]serviceConnectionEntry{}
	}
	return saveServiceConnections(c.svcConnPath, &serviceConnectionsFile{
		Version:   serviceConnectionsFileVersion,
		Services:  c.svcConnOverlay,
		Tailscale: c.tailscaleAdmin,
	})
}

// EstablishSyncRelationships sweeps every connected source and upserts sync
// rows for the sync-participating kinds it provides (issue #365). Idempotent:
// run at startup (migration over existing connections + canonical_for) and
// after each connect event.
func (s *Server) EstablishSyncRelationships() {
	for _, svc := range s.config.ServiceSnapshots() {
		if !s.sourceConnected(svc) {
			continue
		}
		conn, ok := s.connectors[svc.ID]
		if !ok {
			continue
		}
		kindIDs := s.providedSyncKinds(conn)
		if len(kindIDs) == 0 {
			continue
		}
		writeCapable := func(kindID string) bool {
			return s.syncKindWriteCapable(kindID, conn)
		}
		if err := s.config.EnsureSyncRelationships(svc.ID, kindIDs, writeCapable); err != nil {
			slog.Warn("sync relationship establishment failed",
				"service", svc.ID, "error", err)
		}
	}
}

// sourceConnected reports whether a source's data is actually connected:
// a local file import, static credentials, an OAuth token for its
// connection, or an endpoint that needs no credentials at all. Sample
// fixtures are demo data, not user data sources.
func (s *Server) sourceConnected(svc Service) bool {
	if sampledata.IsSampleLibraryService(svc.ID, svc.Endpoint) {
		return false
	}
	if connectors.IsSampleCalendarService(&connectors.ServiceInfo{ID: svc.ID, Endpoint: svc.Endpoint}) {
		return false
	}
	if connectors.LocalImportStoreIDs()[svc.ID] {
		return true
	}
	if svc.HasStaticAPIKey() {
		return true
	}
	// OAuth-backed sources count as connected once their connection has a
	// token; a service that names a connection but has none registered is
	// misconfigured, not connected. Sources with no OAuth connection at all
	// need no authorization.
	if svc.Connection != "" {
		connID, _ := s.config.ConnectionForService(svc.ID)
		return connID != "" && s.tokens.Get(connID) != nil
	}
	return svc.Endpoint != ""
}

// providedSyncKinds returns the sync-participating kinds the connector feeds.
// Registry-driven so the hub never names a kind (issue #312).
func (s *Server) providedSyncKinds(conn connectors.ServiceConnector) []string {
	var out []string
	for _, k := range s.library().KindRegistry() {
		def := k.Def()
		if !def.SyncParticipant || !k.Provides(conn) {
			continue
		}
		out = append(out, def.ID)
	}
	return out
}

// syncKindWriteCapable reports whether the connector can receive writes for
// kind — the gate for default-hub designation and for read-only membership.
func (s *Server) syncKindWriteCapable(kindID string, conn connectors.ServiceConnector) bool {
	k, ok := s.library().RegistryKind(kindID)
	if !ok {
		return false
	}
	return k.WriteCapable(conn)
}
