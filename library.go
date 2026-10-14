package main

import (
	"context"
	"net/http"

	"github.com/fudomunro/aegis/internal/types"
)

func (s *Server) HandleLibraryPlaylists(w http.ResponseWriter, r *http.Request) {
	kind := *LibraryKindByID("playlists")
	handleLibraryAggregate(s, w, r, libraryKindSpec[types.Playlist]{
		kind: kind,
		targets: func(s *Server, r *http.Request) ([]namedLister[types.Playlist], []string) {
			return collectProviders[types.Playlist, types.PlaylistProvider](s, kind.PermType,
				func(p types.PlaylistProvider) func(context.Context) ([]types.Playlist, bool, error) {
					return p.ListUserPlaylists
				})
		},
		serviceOf: types.PlaylistServiceName,
		sortKey:   func(p types.Playlist) string { return p.Name },
		merge: func(in []types.Playlist) []types.Playlist {
			return types.MergePlaylistsSyncLinked(in, s.links.PlaylistLinkKey)
		},
		encode: func(merged []types.Playlist, bySvc map[string]types.LibraryServiceStatus) any {
			return types.PlaylistList{Playlists: merged, Total: len(merged), Synced: true, Truncated: anyServiceTruncated(bySvc), ByService: bySvc}
		},
		markCanonical: s.stampPlaylistCanonical,
		stampCopies:   s.stampPlaylistCopies,
	})
}

// HandleLibraryTracks returns liked/saved tracks: GET /v1/library/tracks
func (s *Server) HandleLibraryTracks(w http.ResponseWriter, r *http.Request) {
	kind := *LibraryKindByID("tracks")
	handleLibraryAggregate(s, w, r, libraryKindSpec[types.Track]{
		kind: kind,
		targets: func(s *Server, r *http.Request) ([]namedLister[types.Track], []string) {
			return collectProviders[types.Track, types.LikedTrackProvider](s, kind.PermType,
				func(p types.LikedTrackProvider) func(context.Context) ([]types.Track, bool, error) {
					return p.ListUserLikedTracks
				})
		},
		serviceOf: types.TrackServiceName,
		sortKey:   types.TrackIdentityKey,
		merge:     types.MergeTracksSync,
		enrich: func(ctx context.Context, s *Server, tracks []types.Track) {
			s.resolveTrackEntities(ctx, tracks)
		},
		encode: func(merged []types.Track, bySvc map[string]types.LibraryServiceStatus) any {
			return types.TrackList{Tracks: merged, Total: len(merged), Synced: true, Truncated: anyServiceTruncated(bySvc), ByService: bySvc}
		},
		markCanonical: s.stampTrackCanonical,
		stampCopies:   s.stampTrackCopies,
	})
}

// HandleLibraryAlbums returns saved albums: GET /v1/library/albums
func (s *Server) HandleLibraryAlbums(w http.ResponseWriter, r *http.Request) {
	kind := *LibraryKindByID("albums")
	handleLibraryAggregate(s, w, r, libraryKindSpec[types.Album]{
		kind: kind,
		targets: func(s *Server, r *http.Request) ([]namedLister[types.Album], []string) {
			return collectProviders[types.Album, types.AlbumProvider](s, kind.PermType,
				func(p types.AlbumProvider) func(context.Context) ([]types.Album, bool, error) {
					return p.ListUserSavedAlbums
				})
		},
		serviceOf: types.AlbumServiceName,
		sortKey:   types.AlbumIdentityKey,
		merge:     types.MergeAlbumsSync,
		enrich: func(ctx context.Context, s *Server, albums []types.Album) {
			s.resolveAlbumEntities(ctx, albums)
		},
		encode: func(merged []types.Album, bySvc map[string]types.LibraryServiceStatus) any {
			return types.AlbumList{Albums: merged, Total: len(merged), Synced: true, Truncated: anyServiceTruncated(bySvc), ByService: bySvc}
		},
	})
}

// HandleLibraryArtists returns followed/top artists: GET /v1/library/artists
func (s *Server) HandleLibraryArtists(w http.ResponseWriter, r *http.Request) {
	kind := *LibraryKindByID("artists")
	handleLibraryAggregate(s, w, r, libraryKindSpec[types.Artist]{
		kind: kind,
		targets: func(s *Server, r *http.Request) ([]namedLister[types.Artist], []string) {
			return collectProviders[types.Artist, types.ArtistProvider](s, kind.PermType,
				func(p types.ArtistProvider) func(context.Context) ([]types.Artist, bool, error) {
					return p.ListUserFollowedArtists
				})
		},
		serviceOf: types.ArtistServiceName,
		sortKey:   func(a types.Artist) string { return types.NormalizeIdentityKey(a.Name) },
		merge:     types.MergeArtistLibrary,
		enrich: func(ctx context.Context, s *Server, artists []types.Artist) {
			s.resolveArtistEntities(ctx, artists)
		},
		encode: func(merged []types.Artist, bySvc map[string]types.LibraryServiceStatus) any {
			return types.ArtistList{Artists: merged, Total: len(merged), Synced: true, Truncated: anyServiceTruncated(bySvc), ByService: bySvc}
		},
	})
}
