package core

import (
	"context"
	"net/http"

	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/types"
)

func (s *Server) HandleLibraryPlaylists(w http.ResponseWriter, r *http.Request) {
	kind := *LibraryKindByID("playlists")
	kinds.Aggregate(s, s.libraryEngine(), w, r, kinds.Spec[types.Playlist]{
		Kind: kind,
		Targets: func(r *http.Request) ([]kinds.NamedLister[types.Playlist], []string) {
			return kinds.Collect[types.Playlist, types.PlaylistProvider](s, r, kind.PermType,
				func(p types.PlaylistProvider) func(context.Context) ([]types.Playlist, bool, error) {
					return p.ListUserPlaylists
				})
		},
		ServiceOf: types.PlaylistServiceName,
		SortKey:   func(p types.Playlist) string { return p.Name },
		Merge: func(in []types.Playlist) []types.Playlist {
			return types.MergePlaylistsSyncLinked(in, s.links.PlaylistLinkKey)
		},
		Encode: func(items []types.Playlist, page types.PageRequest, bySvc map[string]types.LibraryServiceStatus) any {
			pageItems, pr := types.SlicePage(items, page)
			return types.PlaylistList{Playlists: pageItems, LibraryListMeta: types.MetaForPage(pr, true, kinds.AnyServiceTruncated(bySvc), bySvc)}
		},
		MarkCanonical: s.stampPlaylistCanonical,
		StampCopies:   s.stampPlaylistCopies,
	})
}

// HandleLibraryTracks returns liked/saved tracks: GET /v1/library/tracks
func (s *Server) HandleLibraryTracks(w http.ResponseWriter, r *http.Request) {
	kind := *LibraryKindByID("tracks")
	kinds.Aggregate(s, s.libraryEngine(), w, r, kinds.Spec[types.Track]{
		Kind: kind,
		Targets: func(r *http.Request) ([]kinds.NamedLister[types.Track], []string) {
			return kinds.Collect[types.Track, types.LikedTrackProvider](s, r, kind.PermType,
				func(p types.LikedTrackProvider) func(context.Context) ([]types.Track, bool, error) {
					return p.ListUserLikedTracks
				})
		},
		ServiceOf: types.TrackServiceName,
		SortKey:   types.TrackIdentityKey,
		Merge:     types.MergeTracksSync,
		Enrich: func(ctx context.Context, tracks []types.Track) {
			s.resolveTrackEntities(ctx, tracks)
		},
		Encode: func(items []types.Track, page types.PageRequest, bySvc map[string]types.LibraryServiceStatus) any {
			pageItems, pr := types.SlicePage(items, page)
			return types.TrackList{Tracks: pageItems, LibraryListMeta: types.MetaForPage(pr, true, kinds.AnyServiceTruncated(bySvc), bySvc)}
		},
		MarkCanonical: s.stampTrackCanonical,
		StampCopies:   s.stampTrackCopies,
	})
}

// HandleLibraryAlbums returns saved albums: GET /v1/library/albums
func (s *Server) HandleLibraryAlbums(w http.ResponseWriter, r *http.Request) {
	kind := *LibraryKindByID("albums")
	kinds.Aggregate(s, s.libraryEngine(), w, r, kinds.Spec[types.Album]{
		Kind: kind,
		Targets: func(r *http.Request) ([]kinds.NamedLister[types.Album], []string) {
			return kinds.Collect[types.Album, types.AlbumProvider](s, r, kind.PermType,
				func(p types.AlbumProvider) func(context.Context) ([]types.Album, bool, error) {
					return p.ListUserSavedAlbums
				})
		},
		ServiceOf: types.AlbumServiceName,
		SortKey:   types.AlbumIdentityKey,
		Merge:     types.MergeAlbumsSync,
		Enrich: func(ctx context.Context, albums []types.Album) {
			s.resolveAlbumEntities(ctx, albums)
		},
		Encode: func(items []types.Album, page types.PageRequest, bySvc map[string]types.LibraryServiceStatus) any {
			pageItems, pr := types.SlicePage(items, page)
			return types.AlbumList{Albums: pageItems, LibraryListMeta: types.MetaForPage(pr, true, kinds.AnyServiceTruncated(bySvc), bySvc)}
		},
	})
}

// HandleLibraryArtists returns followed/top artists: GET /v1/library/artists
func (s *Server) HandleLibraryArtists(w http.ResponseWriter, r *http.Request) {
	kind := *LibraryKindByID("artists")
	kinds.Aggregate(s, s.libraryEngine(), w, r, kinds.Spec[types.Artist]{
		Kind: kind,
		Targets: func(r *http.Request) ([]kinds.NamedLister[types.Artist], []string) {
			return kinds.Collect[types.Artist, types.ArtistProvider](s, r, kind.PermType,
				func(p types.ArtistProvider) func(context.Context) ([]types.Artist, bool, error) {
					return p.ListUserFollowedArtists
				})
		},
		ServiceOf: types.ArtistServiceName,
		SortKey:   func(a types.Artist) string { return types.NormalizeIdentityKey(a.Name) },
		Merge:     types.MergeArtistLibrary,
		Enrich: func(ctx context.Context, artists []types.Artist) {
			s.resolveArtistEntities(ctx, artists)
		},
		Encode: func(items []types.Artist, page types.PageRequest, bySvc map[string]types.LibraryServiceStatus) any {
			pageItems, pr := types.SlicePage(items, page)
			return types.ArtistList{Artists: pageItems, LibraryListMeta: types.MetaForPage(pr, true, kinds.AnyServiceTruncated(bySvc), bySvc)}
		},
	})
}
