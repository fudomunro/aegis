package core

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/fudomunro/aegis/internal/types"
)

// HandleEntityLinks manages explicit cross-service identity links:
//
//	GET    /v1/library/links
//	POST   /v1/library/links          {"kind":"playlist","sources":[{"service","id"},...],"note"?}
//	DELETE /v1/library/links/{id}
func (s *Server) HandleEntityLinks(w http.ResponseWriter, r *http.Request) {
	if s.links == nil {
		writeJSONError(w, http.StatusNotFound, "entity links not configured")
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/v1/library/links")
	path = strings.Trim(path, "/")

	switch {
	case path == "" && r.Method == http.MethodGet:
		writeJSON(w, map[string]any{
			"links": s.links.List(),
			"total": len(s.links.List()),
		})
	case path == "" && r.Method == http.MethodPost:
		var body struct {
			Kind    string             `json:"kind"`
			Sources []EntityLinkSource `json:"sources"`
			Note    string             `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if body.Kind == "" {
			body.Kind = "playlist"
		}
		link, err := s.links.Add(body.Kind, body.Sources, body.Note)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(link)
	case path != "" && r.Method == http.MethodDelete:
		if !s.links.Delete(path) {
			writeJSONError(w, http.StatusNotFound, "link not found")
			return
		}
		writeJSON(w, map[string]any{"deleted": path})
	case path != "" && r.Method == http.MethodGet:
		for _, link := range s.links.List() {
			if link.ID == path {
				writeJSON(w, link)
				return
			}
		}
		writeJSONError(w, http.StatusNotFound, "link not found")
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// MergeReviewItem is one multi-source entity that was joined without a
// durable catalog ID — candidates for user review / explicit linking.
type MergeReviewItem struct {
	Kind    string   `json:"kind"`
	Match   string   `json:"match"`
	Name    string   `json:"name"`
	Sources []string `json:"sources"` // "service:id"
	Note    string   `json:"note,omitempty"`
}

// MergeReviewResponse lists low-confidence joins across the library.
type MergeReviewResponse struct {
	Items []MergeReviewItem `json:"items"`
	Total int               `json:"total"`
}

// HandleMergeReview returns multi-source entities joined by name (not by
// ISRC/UPC/MBID/explicit link). GET /v1/library/review
func (s *Server) HandleMergeReview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx := r.Context()
	var (
		itemsMu sync.Mutex
		items   []MergeReviewItem
		wg      sync.WaitGroup
	)
	appendItems := func(batch []MergeReviewItem) {
		if len(batch) == 0 {
			return
		}
		itemsMu.Lock()
		items = append(items, batch...)
		itemsMu.Unlock()
	}

	if kindReadable("tracks", s) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			trackProviders, _ := collectProviders[types.Track, types.LikedTrackProvider](s, r, "tracks",
				func(p types.LikedTrackProvider) func(context.Context) ([]types.Track, bool, error) {
					return p.ListUserLikedTracks
				})
			all := listProvidersParallel[types.Track](ctx, trackProviders)
			s.resolveTrackEntities(ctx, all)
			var batch []MergeReviewItem
			for _, tr := range types.MergeTracksSync(all) {
				if tr.Match != types.TrackMatchName || len(tr.Sources) < 2 {
					continue
				}
				batch = append(batch, MergeReviewItem{
					Kind:    "track",
					Match:   tr.Match,
					Name:    tr.Name,
					Sources: trackSourceRefs(tr),
					Note:    "joined by title+artist (no shared ISRC/MBID)",
				})
			}
			appendItems(batch)
		}()
	}

	if kindReadable("albums", s) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			albumProviders, _ := collectProviders[types.Album, types.AlbumProvider](s, r, "albums",
				func(p types.AlbumProvider) func(context.Context) ([]types.Album, bool, error) {
					return p.ListUserSavedAlbums
				})
			all := listProvidersParallel[types.Album](ctx, albumProviders)
			s.resolveAlbumEntities(ctx, all)
			var batch []MergeReviewItem
			for _, al := range types.MergeAlbumsSync(all) {
				if al.Match != types.TrackMatchName || len(al.Sources) < 2 {
					continue
				}
				batch = append(batch, MergeReviewItem{
					Kind:    "album",
					Match:   al.Match,
					Name:    al.Name,
					Sources: albumSourceRefs(al),
					Note:    "joined by title+artist (no shared UPC/MBID)",
				})
			}
			appendItems(batch)
		}()
	}

	if kindReadable("artists", s) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			artistProviders, _ := collectProviders[types.Artist, types.ArtistProvider](s, r, "artists",
				func(p types.ArtistProvider) func(context.Context) ([]types.Artist, bool, error) {
					return p.ListUserFollowedArtists
				})
			all := listProvidersParallel[types.Artist](ctx, artistProviders)
			s.resolveArtistEntities(ctx, all)
			var batch []MergeReviewItem
			for _, ar := range types.MergeArtistLibrary(all) {
				if ar.Match != types.TrackMatchName || len(ar.Sources) < 2 {
					continue
				}
				batch = append(batch, MergeReviewItem{
					Kind:    "artist",
					Match:   ar.Match,
					Name:    ar.Name,
					Sources: artistSourceRefs(ar),
					Note:    "joined by name (no shared MBID)",
				})
			}
			appendItems(batch)
		}()
	}

	if kindReadable("playlists", s) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			plProviders, _ := collectProviders[types.Playlist, types.PlaylistProvider](s, r, "playlists",
				func(p types.PlaylistProvider) func(context.Context) ([]types.Playlist, bool, error) {
					return p.ListUserPlaylists
				})
			all := listProvidersParallel[types.Playlist](ctx, plProviders)
			var batch []MergeReviewItem
			for _, pl := range types.MergePlaylistsSyncLinked(all, s.links.PlaylistLinkKey) {
				if pl.Match != types.PlaylistMatchName || len(pl.Sources) < 2 {
					continue
				}
				batch = append(batch, MergeReviewItem{
					Kind:    "playlist",
					Match:   pl.Match,
					Name:    pl.Name,
					Sources: playlistSourceRefs(pl),
					Note:    "joined by name — POST /v1/library/links to confirm or split by renaming",
				})
			}
			appendItems(batch)
		}()
	}

	wg.Wait()
	writeJSON(w, MergeReviewResponse{Items: items, Total: len(items)})
}

// listProvidersParallel fans out named listers like handleLibraryAggregate.
// Provider errors are skipped (same as the previous sequential review path).
func listProvidersParallel[T any](ctx context.Context, providers []namedLister[T]) []T {
	if len(providers) == 0 {
		return nil
	}
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		all []T
	)
	for _, t := range providers {
		wg.Add(1)
		go func(t namedLister[T]) {
			defer wg.Done()
			list, _, err := t.list(ctx)
			if err != nil || len(list) == 0 {
				return
			}
			mu.Lock()
			all = append(all, list...)
			mu.Unlock()
		}(t)
	}
	wg.Wait()
	return all
}

func trackSourceRefs(t types.Track) []string {
	out := make([]string, 0, len(t.Sources))
	for _, s := range t.Sources {
		out = append(out, s.Service+":"+s.ID)
	}
	return out
}

func albumSourceRefs(a types.Album) []string {
	out := make([]string, 0, len(a.Sources))
	for _, s := range a.Sources {
		out = append(out, s.Service+":"+s.ID)
	}
	return out
}

func artistSourceRefs(a types.Artist) []string {
	out := make([]string, 0, len(a.Sources))
	for _, s := range a.Sources {
		out = append(out, s.Service+":"+s.ID)
	}
	return out
}

func playlistSourceRefs(p types.Playlist) []string {
	out := make([]string, 0, len(p.Sources))
	for _, s := range p.Sources {
		out = append(out, s.Service+":"+s.ID)
	}
	return out
}
