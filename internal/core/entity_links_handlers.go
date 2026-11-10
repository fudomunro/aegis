package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/fudomunro/aegis/internal/core/links"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

// linkGrantKey maps entity-link kind labels ("playlist", "track") onto the
// data-grant vocabulary ("playlists", "tracks"). Unknown labels pass through
// and resolve against the grant map as-is.
func linkGrantKey(kind string) string {
	switch kind {
	case "playlist":
		return "playlists"
	case "track":
		return "tracks"
	}
	return kind
}

// HandleEntityLinks manages explicit cross-service identity links:
//
//	GET    /v1/library/links
//	POST   /v1/library/links          {"kind":"playlist","sources":[{"service","id"},...],"note"?}
//	DELETE /v1/library/links/{id}
func (s *Server) HandleEntityLinks(w http.ResponseWriter, r *http.Request) {
	if s.links == nil {
		shared.WriteJSONError(w, http.StatusNotFound, "entity links not configured")
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/v1/library/links")
	path = strings.Trim(path, "/")

	switch {
	case path == "" && r.Method == http.MethodGet:
		shared.WriteJSON(w, map[string]any{
			"links": s.links.List(),
			"total": len(s.links.List()),
		})
	case path == "" && r.Method == http.MethodPost:
		var body struct {
			Kind    string         `json:"kind"`
			Sources []links.Source `json:"sources"`
			Note    string         `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			shared.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if body.Kind == "" {
			body.Kind = "playlist"
		}
		grants := s.requestGrants(r)
		if !grants.AllowsKindMethod(linkGrantKey(body.Kind), r.Method) {
			shared.WriteJSONError(w, http.StatusForbidden, fmt.Sprintf(
				"denied: data grant for %s is %q", linkGrantKey(body.Kind), grants.Level(linkGrantKey(body.Kind))))
			return
		}
		link, err := s.links.Add(body.Kind, body.Sources, body.Note)
		if err != nil {
			shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(link)
	case path != "" && r.Method == http.MethodDelete:
		var linkKind string
		for _, link := range s.links.List() {
			if link.ID == path {
				linkKind = link.Kind
				break
			}
		}
		if linkKind == "" {
			shared.WriteJSONError(w, http.StatusNotFound, "link not found")
			return
		}
		grants := s.requestGrants(r)
		if !grants.AllowsKindMethod(linkGrantKey(linkKind), r.Method) {
			shared.WriteJSONError(w, http.StatusForbidden, fmt.Sprintf(
				"denied: data grant for %s is %q", linkGrantKey(linkKind), grants.Level(linkGrantKey(linkKind))))
			return
		}
		if !s.links.Delete(path) {
			shared.WriteJSONError(w, http.StatusNotFound, "link not found")
			return
		}
		shared.WriteJSON(w, map[string]any{"deleted": path})
	case path != "" && r.Method == http.MethodGet:
		for _, link := range s.links.List() {
			if link.ID == path {
				shared.WriteJSON(w, link)
				return
			}
		}
		shared.WriteJSONError(w, http.StatusNotFound, "link not found")
	default:
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
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
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
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
			trackProviders, _ := kinds.Collect[kinds.Track, kinds.LikedTrackProvider](s, r, "tracks",
				func(p kinds.LikedTrackProvider) func(context.Context) ([]kinds.Track, bool, error) {
					return p.ListUserLikedTracks
				})
			all := listProvidersParallel[kinds.Track](ctx, trackProviders)
			s.resolveTrackEntities(ctx, all)
			var batch []MergeReviewItem
			for _, tr := range kinds.MergeTracksSync(all) {
				if tr.Match != kinds.TrackMatchName || len(tr.Sources) < 2 {
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
			albumProviders, _ := kinds.Collect[kinds.Album, kinds.AlbumProvider](s, r, "albums",
				func(p kinds.AlbumProvider) func(context.Context) ([]kinds.Album, bool, error) {
					return p.ListUserSavedAlbums
				})
			all := listProvidersParallel[kinds.Album](ctx, albumProviders)
			s.resolveAlbumEntities(ctx, all)
			var batch []MergeReviewItem
			for _, al := range kinds.MergeAlbumsSync(all) {
				if al.Match != kinds.TrackMatchName || len(al.Sources) < 2 {
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
			artistProviders, _ := kinds.Collect[kinds.Artist, kinds.ArtistProvider](s, r, "artists",
				func(p kinds.ArtistProvider) func(context.Context) ([]kinds.Artist, bool, error) {
					return p.ListUserFollowedArtists
				})
			all := listProvidersParallel[kinds.Artist](ctx, artistProviders)
			s.resolveArtistEntities(ctx, all)
			var batch []MergeReviewItem
			for _, ar := range kinds.MergeArtistLibrary(all) {
				if ar.Match != kinds.TrackMatchName || len(ar.Sources) < 2 {
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
			plProviders, _ := kinds.Collect[kinds.Playlist, kinds.PlaylistProvider](s, r, "playlists",
				func(p kinds.PlaylistProvider) func(context.Context) ([]kinds.Playlist, bool, error) {
					return p.ListUserPlaylists
				})
			all := listProvidersParallel[kinds.Playlist](ctx, plProviders)
			var batch []MergeReviewItem
			for _, pl := range kinds.MergePlaylistsSyncLinked(all, s.links.PlaylistLinkKey) {
				if pl.Match != kinds.PlaylistMatchName || len(pl.Sources) < 2 {
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
	shared.WriteJSON(w, MergeReviewResponse{Items: items, Total: len(items)})
}

// listProvidersParallel fans out named listers like handleLibraryAggregate.
// Provider errors are skipped (same as the previous sequential review path).
func listProvidersParallel[T any](ctx context.Context, providers []kinds.NamedLister[T]) []T {
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
		go func(t kinds.NamedLister[T]) {
			defer wg.Done()
			list, _, err := t.List(ctx)
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

func trackSourceRefs(t kinds.Track) []string {
	out := make([]string, 0, len(t.Sources))
	for _, s := range t.Sources {
		out = append(out, s.Service+":"+s.ID)
	}
	return out
}

func albumSourceRefs(a kinds.Album) []string {
	out := make([]string, 0, len(a.Sources))
	for _, s := range a.Sources {
		out = append(out, s.Service+":"+s.ID)
	}
	return out
}

func artistSourceRefs(a kinds.Artist) []string {
	out := make([]string, 0, len(a.Sources))
	for _, s := range a.Sources {
		out = append(out, s.Service+":"+s.ID)
	}
	return out
}

func playlistSourceRefs(p kinds.Playlist) []string {
	out := make([]string, 0, len(p.Sources))
	for _, s := range p.Sources {
		out = append(out, s.Service+":"+s.ID)
	}
	return out
}
