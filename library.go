package main

import (
	"context"
	"errors"
	"net/http"
)

// ErrUnauthorized is returned by library providers when a service has no valid token.
var ErrUnauthorized = errors.New("unauthorized")

// PlaylistProvider is implemented by connectors that can return the user's
// playlists in Guardian's neutral format. truncated is true when the page cap
// was hit with more items still upstream.
type PlaylistProvider interface {
	ListUserPlaylists(ctx context.Context) (playlists []Playlist, truncated bool, err error)
}

// LikedTrackProvider is implemented by connectors that can return the user's
// liked/saved tracks in Guardian's neutral format.
type LikedTrackProvider interface {
	ListUserLikedTracks(ctx context.Context) (tracks []Track, truncated bool, err error)
}

// AlbumProvider is implemented by connectors that can return the user's
// saved albums in Guardian's neutral format.
type AlbumProvider interface {
	ListUserSavedAlbums(ctx context.Context) (albums []Album, truncated bool, err error)
}

// ArtistProvider is implemented by connectors that can return the user's
// followed or top artists in Guardian's neutral format.
type ArtistProvider interface {
	ListUserFollowedArtists(ctx context.Context) (artists []Artist, truncated bool, err error)
}

func playlistServiceName(p Playlist) string {
	if len(p.Sources) > 0 {
		return p.Sources[0].ServiceName
	}
	return ""
}

func trackServiceName(t Track) string {
	if len(t.Sources) > 0 {
		return t.Sources[0].ServiceName
	}
	return ""
}

func albumServiceName(a Album) string {
	if len(a.Sources) > 0 {
		return a.Sources[0].ServiceName
	}
	return ""
}

func artistServiceName(a Artist) string {
	if len(a.Sources) > 0 {
		return a.Sources[0].ServiceName
	}
	return ""
}

// HandleLibraryPlaylists returns the user's playlists from every connected
// service: GET /v1/library/playlists
func (s *Server) HandleLibraryPlaylists(w http.ResponseWriter, r *http.Request) {
	kind := *LibraryKindByID("playlists")
	handleLibraryAggregate(s, w, r, libraryKindSpec[Playlist]{
		kind: kind,
		targets: func(s *Server) ([]namedLister[Playlist], []string) {
			return collectProviders[Playlist, PlaylistProvider](s, kind.PermType,
				func(p PlaylistProvider) func(context.Context) ([]Playlist, bool, error) {
					return p.ListUserPlaylists
				})
		},
		serviceOf: playlistServiceName,
		sortKey:   func(p Playlist) string { return p.Name },
		merge:     MergePlaylistsSync,
		encode: func(merged []Playlist, bySvc map[string]LibraryServiceStatus) any {
			return PlaylistList{Playlists: merged, Total: len(merged), Synced: true, Truncated: anyServiceTruncated(bySvc), ByService: bySvc}
		},
		markCanonical: s.stampPlaylistCanonical,
	})
}

// HandleLibraryTracks returns liked/saved tracks: GET /v1/library/tracks
func (s *Server) HandleLibraryTracks(w http.ResponseWriter, r *http.Request) {
	kind := *LibraryKindByID("tracks")
	handleLibraryAggregate(s, w, r, libraryKindSpec[Track]{
		kind: kind,
		targets: func(s *Server) ([]namedLister[Track], []string) {
			return collectProviders[Track, LikedTrackProvider](s, kind.PermType,
				func(p LikedTrackProvider) func(context.Context) ([]Track, bool, error) {
					return p.ListUserLikedTracks
				})
		},
		serviceOf: trackServiceName,
		sortKey:   trackIdentityKey,
		merge:     MergeTracksSync,
		enrich: func(ctx context.Context, s *Server, tracks []Track) {
			s.resolveTrackEntities(ctx, tracks)
		},
		encode: func(merged []Track, bySvc map[string]LibraryServiceStatus) any {
			return TrackList{Tracks: merged, Total: len(merged), Synced: true, Truncated: anyServiceTruncated(bySvc), ByService: bySvc}
		},
		markCanonical: s.stampTrackCanonical,
	})
}

// HandleLibraryAlbums returns saved albums: GET /v1/library/albums
func (s *Server) HandleLibraryAlbums(w http.ResponseWriter, r *http.Request) {
	kind := *LibraryKindByID("albums")
	handleLibraryAggregate(s, w, r, libraryKindSpec[Album]{
		kind: kind,
		targets: func(s *Server) ([]namedLister[Album], []string) {
			return collectProviders[Album, AlbumProvider](s, kind.PermType,
				func(p AlbumProvider) func(context.Context) ([]Album, bool, error) {
					return p.ListUserSavedAlbums
				})
		},
		serviceOf: albumServiceName,
		sortKey:   albumIdentityKey,
		merge:     MergeAlbumsSync,
		enrich: func(ctx context.Context, s *Server, albums []Album) {
			s.resolveAlbumEntities(ctx, albums)
		},
		encode: func(merged []Album, bySvc map[string]LibraryServiceStatus) any {
			return AlbumList{Albums: merged, Total: len(merged), Synced: true, Truncated: anyServiceTruncated(bySvc), ByService: bySvc}
		},
	})
}

// HandleLibraryArtists returns followed/top artists: GET /v1/library/artists
func (s *Server) HandleLibraryArtists(w http.ResponseWriter, r *http.Request) {
	kind := *LibraryKindByID("artists")
	handleLibraryAggregate(s, w, r, libraryKindSpec[Artist]{
		kind: kind,
		targets: func(s *Server) ([]namedLister[Artist], []string) {
			return collectProviders[Artist, ArtistProvider](s, kind.PermType,
				func(p ArtistProvider) func(context.Context) ([]Artist, bool, error) {
					return p.ListUserFollowedArtists
				})
		},
		serviceOf: artistServiceName,
		sortKey:   func(a Artist) string { return NormalizeIdentityKey(a.Name) },
		merge:     mergeArtistLibrary,
		enrich: func(ctx context.Context, s *Server, artists []Artist) {
			s.resolveArtistEntities(ctx, artists)
		},
		encode: func(merged []Artist, bySvc map[string]LibraryServiceStatus) any {
			return ArtistList{Artists: merged, Total: len(merged), Synced: true, Truncated: anyServiceTruncated(bySvc), ByService: bySvc}
		},
	})
}

// Compatibility wrappers — thin aliases over the library kind registry.

func (s *Server) hasPlaylistProviders() bool {
	return LibraryKindByID("playlists").HasProviders(s)
}

func (s *Server) hasLikedTrackProviders() bool {
	return LibraryKindByID("tracks").HasProviders(s)
}

func (s *Server) hasAlbumProviders() bool {
	return LibraryKindByID("albums").HasProviders(s)
}

func (s *Server) hasArtistProviders() bool {
	return LibraryKindByID("artists").HasProviders(s)
}

func (s *Server) libraryReadable() bool {
	return LibraryKindByID("playlists").Readable(s)
}

func (s *Server) tracksLibraryReadable() bool {
	return LibraryKindByID("tracks").Readable(s)
}

func (s *Server) albumsLibraryReadable() bool {
	return LibraryKindByID("albums").Readable(s)
}

func (s *Server) artistsLibraryReadable() bool {
	return LibraryKindByID("artists").Readable(s)
}

func (s *Server) librarySystemInstructions() string {
	return s.libraryInstructionsFor("playlists")
}

func (s *Server) libraryTracksSystemInstructions() string {
	return s.libraryInstructionsFor("tracks")
}

func (s *Server) libraryAlbumsSystemInstructions() string {
	return s.libraryInstructionsFor("albums")
}

func (s *Server) libraryArtistsSystemInstructions() string {
	return s.libraryInstructionsFor("artists")
}

func mentionsPlaylists(msg string) bool {
	return LibraryKindByID("playlists").Mentions(msg)
}

func mentionsLikedTracks(msg string) bool {
	return LibraryKindByID("tracks").Mentions(msg)
}

func mentionsAlbums(msg string) bool {
	return LibraryKindByID("albums").Mentions(msg)
}

func mentionsArtists(msg string) bool {
	return LibraryKindByID("artists").Mentions(msg)
}
