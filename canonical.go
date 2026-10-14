package main

import (
	"fmt"
	"strings"

	"github.com/fudomunro/aegis/internal/types"
)

// Known library kinds that may appear in a service's canonical_for list.
// Keep in sync with libraryKinds IDs.
var canonicalKindIDs = []string{
	"playlists", "tracks", "albums", "artists", "contacts", "photos", "events", "messages",
	"transactions", "places",
}

func isCanonicalKindID(id string) bool {
	for _, k := range canonicalKindIDs {
		if k == id {
			return true
		}
	}
	return false
}

// IsCanonicalFor reports whether this service is the configured authoritative
// location for the given library kind (issue #16). Blocked services never count.
func (s Service) IsCanonicalFor(kind string) bool {
	if strings.EqualFold(s.Action, "block") {
		return false
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return false
	}
	for _, k := range s.CanonicalFor {
		if strings.EqualFold(strings.TrimSpace(k), kind) {
			return true
		}
	}
	return false
}

// normalizeCanonicalFor trims entries and drops empties / duplicates.
func normalizeCanonicalFor(list []string) []string {
	if len(list) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(list))
	for _, raw := range list {
		k := strings.TrimSpace(raw)
		if k == "" || seen[strings.ToLower(k)] {
			continue
		}
		seen[strings.ToLower(k)] = true
		out = append(out, k)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// validateCanonicalFor checks kind IDs and that at most one non-blocked service
// claims each kind.
func validateCanonicalFor(services []Service) error {
	owner := map[string]string{} // kind -> service id
	for _, svc := range services {
		for _, raw := range svc.CanonicalFor {
			k := strings.TrimSpace(raw)
			if k == "" {
				continue
			}
			if !isCanonicalKindID(k) {
				return fmt.Errorf("service %q: unknown canonical_for kind %q", svc.ID, k)
			}
			if strings.EqualFold(svc.Action, "block") {
				continue
			}
			lower := strings.ToLower(k)
			if prev, ok := owner[lower]; ok && prev != svc.ID {
				return fmt.Errorf("canonical_for %q claimed by both %q and %q", k, prev, svc.ID)
			}
			owner[lower] = svc.ID
		}
	}
	return nil
}

// canonicalServiceIDs returns the set of non-blocked service IDs declared as
// the authoritative location for kind.
func (c *Config) canonicalServiceIDs(kind string) map[string]bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := map[string]bool{}
	for _, svc := range c.Services {
		if svc.IsCanonicalFor(kind) {
			out[svc.ID] = true
		}
	}
	return out
}

// canonicalServiceName returns the display name of the configured canonical
// service for kind, or "".
func (s *Server) canonicalServiceName(kind string) string {
	for _, svc := range s.config.ServiceSnapshots() {
		if svc.IsCanonicalFor(kind) {
			if svc.Name != "" {
				return svc.Name
			}
			return svc.ID
		}
	}
	return ""
}

// markPlaylistSourcesCanonical stamps sources from serviceID as canonical.
func markPlaylistSourcesCanonical(items []types.Playlist, serviceID string) {
	for i := range items {
		for j := range items[i].Sources {
			if items[i].Sources[j].Service == serviceID {
				items[i].Sources[j].Canonical = true
			}
		}
	}
}

// markTrackSourcesCanonical stamps sources from serviceID as canonical.
func markTrackSourcesCanonical(items []types.Track, serviceID string) {
	for i := range items {
		for j := range items[i].Sources {
			if items[i].Sources[j].Service == serviceID {
				items[i].Sources[j].Canonical = true
			}
		}
	}
}

// markContactSourcesCanonical stamps sources from serviceID as canonical.
func markContactSourcesCanonical(items []types.Contact, serviceID string) {
	for i := range items {
		for j := range items[i].Sources {
			if items[i].Sources[j].Service == serviceID {
				items[i].Sources[j].Canonical = true
			}
		}
	}
}

// markPhotoSourcesCanonical stamps sources from serviceID as canonical.
func markPhotoSourcesCanonical(items []types.Photo, serviceID string) {
	for i := range items {
		for j := range items[i].Sources {
			if items[i].Sources[j].Service == serviceID {
				items[i].Sources[j].Canonical = true
			}
		}
	}
}

// markCalendarEventSourcesCanonical stamps sources from serviceID as canonical.
func markCalendarEventSourcesCanonical(items []types.CalendarEvent, serviceID string) {
	for i := range items {
		for j := range items[i].Sources {
			if items[i].Sources[j].Service == serviceID {
				items[i].Sources[j].Canonical = true
			}
		}
	}
}

// markMessageSourcesCanonical stamps sources from serviceID as canonical.
func markMessageSourcesCanonical(items []types.Message, serviceID string) {
	for i := range items {
		for j := range items[i].Sources {
			if items[i].Sources[j].Service == serviceID {
				items[i].Sources[j].Canonical = true
			}
		}
	}
}

// stampPlaylistCanonical marks sources when serviceID is config-declared
// canonical for playlists.
func (s *Server) stampPlaylistCanonical(items []types.Playlist, serviceID string) {
	svc := s.config.GetService(serviceID)
	if svc != nil && svc.IsCanonicalFor("playlists") {
		markPlaylistSourcesCanonical(items, serviceID)
	}
}

// stampTrackCanonical marks sources when serviceID is config-declared
// canonical for tracks.
func (s *Server) stampTrackCanonical(items []types.Track, serviceID string) {
	svc := s.config.GetService(serviceID)
	if svc != nil && svc.IsCanonicalFor("tracks") {
		markTrackSourcesCanonical(items, serviceID)
	}
}

// stampContactCanonical marks sources when serviceID is config-declared
// canonical for contacts.
func (s *Server) stampContactCanonical(items []types.Contact, serviceID string) {
	svc := s.config.GetService(serviceID)
	if svc != nil && svc.IsCanonicalFor("contacts") {
		markContactSourcesCanonical(items, serviceID)
	}
}

// stampPhotoCanonical marks sources when serviceID is config-declared
// canonical for photos (connectors may already set Canonical).
func (s *Server) stampPhotoCanonical(items []types.Photo, serviceID string) {
	svc := s.config.GetService(serviceID)
	if svc != nil && svc.IsCanonicalFor("photos") {
		markPhotoSourcesCanonical(items, serviceID)
	}
}

// stampCalendarEventCanonical marks sources when serviceID is config-declared
// canonical for calendar events (connectors may already set Canonical).
func (s *Server) stampCalendarEventCanonical(items []types.CalendarEvent, serviceID string) {
	svc := s.config.GetService(serviceID)
	if svc != nil && svc.IsCanonicalFor("events") {
		markCalendarEventSourcesCanonical(items, serviceID)
	}
}

// stampMessageCanonical marks sources when serviceID is config-declared
// canonical for messages (connectors may already set Canonical).
func (s *Server) stampMessageCanonical(items []types.Message, serviceID string) {
	svc := s.config.GetService(serviceID)
	if svc != nil && svc.IsCanonicalFor("messages") {
		markMessageSourcesCanonical(items, serviceID)
	}
}

// multiSourceMutationRule is injected whenever library instructions are used
// so agents hit the home copy first and do not invent auto-mirroring.
func multiSourceMutationRule() string {
	return "Multi-source mutation: when an item has multiple sources[], write to the " +
		"canonical source first (sources[].canonical, or the configured home service). " +
		"Aegis does not auto-mirror writes to secondary services — ask the user before updating other copies. " +
		"To copy a playlist or liked tracks onto another service deliberately, use POST /v1/library/mirror " +
		"({\"kind\":\"playlists\"|\"tracks\", \"source_service\", \"source_id\", \"target_service\", \"dry_run\"})."
}

// writePolicyNotice returns system-instruction text for this service's write
// posture relative to configured canonical homes (issue #16 phase 2). Empty
// when the service cannot write library kinds or no home relationship exists.
func (s *Server) writePolicyNotice(svc Service) string {
	if strings.EqualFold(svc.Action, "block") {
		return ""
	}
	svcName := svc.Name
	if svcName == "" {
		svcName = svc.ID
	}
	var lines []string
	for _, kindID := range canonicalKindIDs {
		if !svc.Permissions.AllowsWrite(kindID) {
			continue
		}
		homeName := s.canonicalServiceName(kindID)
		label := kindID
		if k := LibraryKindByID(kindID); k != nil {
			label = k.Label
		}
		switch {
		case svc.IsCanonicalFor(kindID):
			lines = append(lines, fmt.Sprintf(
				"%s is the configured canonical home for %s — send mutations of %s here first.",
				svcName, label, label,
			))
		case homeName != "":
			lines = append(lines, fmt.Sprintf(
				"For %s mutations, prefer %s (canonical home) before %s. Secondary copies are not auto-synced.",
				label, homeName, svcName,
			))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "Write policy (issue #16):\n" + strings.Join(lines, "\n")
}

// writePolicyNotices builds write-policy text for every matched inject service.
// Duplicate lines are dropped so multi-service prompts stay short.
func (s *Server) writePolicyNotices(matched []Service) []string {
	seen := map[string]bool{}
	var out []string
	for _, svc := range matched {
		if svc.Action != "inject" {
			continue
		}
		notice := s.writePolicyNotice(svc)
		if notice == "" || seen[notice] {
			continue
		}
		seen[notice] = true
		out = append(out, notice)
	}
	return out
}

func markTransactionSourcesCanonical(items []types.Transaction, serviceID string) {
	for i := range items {
		for j := range items[i].Sources {
			if items[i].Sources[j].Service == serviceID {
				items[i].Sources[j].Canonical = true
			}
		}
	}
}

// stampTransactionCanonical marks sources when serviceID is config-declared
// canonical for transactions (issue #141).
func (s *Server) stampTransactionCanonical(items []types.Transaction, serviceID string) {
	svc := s.config.GetService(serviceID)
	if svc != nil && svc.IsCanonicalFor("transactions") {
		markTransactionSourcesCanonical(items, serviceID)
	}
}

func markPlaceVisitSourcesCanonical(items []types.PlaceVisit, serviceID string) {
	for i := range items {
		for j := range items[i].Sources {
			if items[i].Sources[j].Service == serviceID {
				items[i].Sources[j].Canonical = true
			}
		}
	}
}

// stampPlaceVisitCanonical marks sources when serviceID is config-declared
// canonical for places (issue #142).
func (s *Server) stampPlaceVisitCanonical(items []types.PlaceVisit, serviceID string) {
	svc := s.config.GetService(serviceID)
	if svc != nil && svc.IsCanonicalFor("places") {
		markPlaceVisitSourcesCanonical(items, serviceID)
	}
}
