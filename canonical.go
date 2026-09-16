package main

import (
	"fmt"
	"sort"
	"strings"
)

// Known library kinds that may appear in a service's canonical_for list.
// Keep in sync with libraryKinds IDs.
var canonicalKindIDs = []string{
	"playlists", "tracks", "albums", "artists", "contacts", "photos",
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
	for _, svc := range s.config.Services {
		if svc.IsCanonicalFor(kind) {
			if svc.Name != "" {
				return svc.Name
			}
			return svc.ID
		}
	}
	return ""
}

// CanonicalService returns the first source marked canonical, else the first
// source service ID, else "".
func (p Playlist) CanonicalService() string {
	return firstCanonicalOrPrimary(
		len(p.Sources),
		func(i int) bool { return p.Sources[i].Canonical },
		func(i int) string { return p.Sources[i].Service },
	)
}

// CanonicalService returns the first source marked canonical, else primary.
func (t Track) CanonicalService() string {
	return firstCanonicalOrPrimary(
		len(t.Sources),
		func(i int) bool { return t.Sources[i].Canonical },
		func(i int) string { return t.Sources[i].Service },
	)
}

// CanonicalService returns the first source marked canonical, else primary.
func (c Contact) CanonicalService() string {
	return firstCanonicalOrPrimary(
		len(c.Sources),
		func(i int) bool { return c.Sources[i].Canonical },
		func(i int) string { return c.Sources[i].Service },
	)
}

// CanonicalService returns the first source marked canonical, else primary.
func (p Photo) CanonicalService() string {
	return firstCanonicalOrPrimary(
		len(p.Sources),
		func(i int) bool { return p.Sources[i].Canonical },
		func(i int) string { return p.Sources[i].Service },
	)
}

func firstCanonicalOrPrimary(n int, isCanonical func(int) bool, serviceID func(int) string) string {
	if n == 0 {
		return ""
	}
	for i := 0; i < n; i++ {
		if isCanonical(i) {
			return serviceID(i)
		}
	}
	return serviceID(0)
}

// markPlaylistSourcesCanonical stamps sources from serviceID as canonical.
func markPlaylistSourcesCanonical(items []Playlist, serviceID string) {
	for i := range items {
		for j := range items[i].Sources {
			if items[i].Sources[j].Service == serviceID {
				items[i].Sources[j].Canonical = true
			}
		}
	}
}

// markTrackSourcesCanonical stamps sources from serviceID as canonical.
func markTrackSourcesCanonical(items []Track, serviceID string) {
	for i := range items {
		for j := range items[i].Sources {
			if items[i].Sources[j].Service == serviceID {
				items[i].Sources[j].Canonical = true
			}
		}
	}
}

// markContactSourcesCanonical stamps sources from serviceID as canonical.
func markContactSourcesCanonical(items []Contact, serviceID string) {
	for i := range items {
		for j := range items[i].Sources {
			if items[i].Sources[j].Service == serviceID {
				items[i].Sources[j].Canonical = true
			}
		}
	}
}

// markPhotoSourcesCanonical stamps sources from serviceID as canonical.
func markPhotoSourcesCanonical(items []Photo, serviceID string) {
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
func (s *Server) stampPlaylistCanonical(items []Playlist, serviceID string) {
	svc := s.config.GetService(serviceID)
	if svc != nil && svc.IsCanonicalFor("playlists") {
		markPlaylistSourcesCanonical(items, serviceID)
	}
}

// stampTrackCanonical marks sources when serviceID is config-declared
// canonical for tracks.
func (s *Server) stampTrackCanonical(items []Track, serviceID string) {
	svc := s.config.GetService(serviceID)
	if svc != nil && svc.IsCanonicalFor("tracks") {
		markTrackSourcesCanonical(items, serviceID)
	}
}

// stampContactCanonical marks sources when serviceID is config-declared
// canonical for contacts.
func (s *Server) stampContactCanonical(items []Contact, serviceID string) {
	svc := s.config.GetService(serviceID)
	if svc != nil && svc.IsCanonicalFor("contacts") {
		markContactSourcesCanonical(items, serviceID)
	}
}

// stampPhotoCanonical marks sources when serviceID is config-declared
// canonical for photos (connectors may already set Canonical).
func (s *Server) stampPhotoCanonical(items []Photo, serviceID string) {
	svc := s.config.GetService(serviceID)
	if svc != nil && svc.IsCanonicalFor("photos") {
		markPhotoSourcesCanonical(items, serviceID)
	}
}

// sortPlaylistSourcesCanonicalFirst orders canonical sources before others.
func sortPlaylistSourcesCanonicalFirst(src []PlaylistSource) {
	sort.SliceStable(src, func(i, j int) bool {
		if src[i].Canonical != src[j].Canonical {
			return src[i].Canonical
		}
		if src[i].ServiceName != src[j].ServiceName {
			return src[i].ServiceName < src[j].ServiceName
		}
		return src[i].Service < src[j].Service
	})
}

// sortTrackSourcesCanonicalFirst orders canonical sources before others.
func sortTrackSourcesCanonicalFirst(src []TrackSource) {
	sort.SliceStable(src, func(i, j int) bool {
		if src[i].Canonical != src[j].Canonical {
			return src[i].Canonical
		}
		if src[i].ServiceName != src[j].ServiceName {
			return src[i].ServiceName < src[j].ServiceName
		}
		return src[i].Service < src[j].Service
	})
}

// sortContactSourcesCanonicalFirst orders canonical sources before others.
func sortContactSourcesCanonicalFirst(src []ContactSource) {
	sort.SliceStable(src, func(i, j int) bool {
		if src[i].Canonical != src[j].Canonical {
			return src[i].Canonical
		}
		if src[i].ServiceName != src[j].ServiceName {
			return src[i].ServiceName < src[j].ServiceName
		}
		return src[i].Service < src[j].Service
	})
}
