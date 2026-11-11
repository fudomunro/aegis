package core

import (
	"sort"

	"github.com/fudomunro/aegis/internal/shared"
)

// invalidateLibrary drops cached library aggregates and search views.
// MusicBrainz MBID lookups live in s.mbCache and survive (issue #273).
func (s *Server) invalidateLibrary() {
	s.cache.ClearPrefix("library:")
}

// invalidateLibraryKind drops cached aggregates for one library kind (its
// list and search views). Cache keys for a kind all start with
// "library:{id}" followed by "|", so the prefix cannot bleed into a longer
// kind id; the freebusy view ("library:freebusy") is only dropped by
// invalidateLibrary.
func (s *Server) invalidateLibraryKind(kindID string) {
	s.cache.ClearPrefix("library:" + kindID)
}

// invalidateService drops library aggregates plus one service's content
// transport entries. Used when a connection, token, or ingest for that
// service changes what cached responses would say.
func (s *Server) invalidateService(serviceID string) {
	s.invalidateLibrary()
	if serviceID != "" {
		s.cache.ClearPrefix(serviceID + ":")
	}
}

// invalidateCaches drops every transport and library entry. Used where the
// change (permission edits, app/scope visibility) is not scoped to one kind
// or service. MBID lookups survive: they are resolution metadata, not data.
func (s *Server) invalidateCaches() {
	s.cache.Clear()
}

// invalidateAllCaches drops every cache including MBID lookups. The admin
// cache-clear endpoint is an explicit flush-all.
func (s *Server) invalidateAllCaches() {
	s.invalidateCaches()
	s.mbCache.Clear()
}

// cacheStats sums the response cache and the MBID cache for admin views.
func (s *Server) cacheStats() (live, total, bytes int) {
	l1, t1, b1 := s.cache.Stats()
	l2, t2, b2 := s.mbCache.Stats()
	return l1 + l2, t1 + t2, b1 + b2
}

// cacheSnapshot lists both caches' entries (MBID keys keep their "mbid:"
// prefix) so the admin cache view stays complete after the split (issue #273).
func (s *Server) cacheSnapshot() []shared.CacheItem {
	items := append(s.cache.Snapshot(), s.mbCache.Snapshot()...)
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return items
}
