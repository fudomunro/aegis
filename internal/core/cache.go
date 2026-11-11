package core

import ()

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
