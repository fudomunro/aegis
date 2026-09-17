package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
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

	// Reuse live library fetch via in-process listers so review matches
	// what /v1/library/* returns (including MusicBrainz enrichment).
	ctx := r.Context()
	var items []MergeReviewItem

	if s.tracksLibraryReadable() {
		trackProviders, _ := collectProviders[Track, LikedTrackProvider](s, "tracks",
			func(p LikedTrackProvider) func(context.Context) ([]Track, bool, error) {
				return p.ListUserLikedTracks
			})
		var all []Track
		for _, t := range trackProviders {
			list, _, err := t.list(ctx)
			if err != nil {
				continue
			}
			all = append(all, list...)
		}
		s.resolveTrackEntities(ctx, all)
		for _, tr := range MergeTracksSync(all) {
			if tr.Match != TrackMatchName || len(tr.Sources) < 2 {
				continue
			}
			items = append(items, MergeReviewItem{
				Kind:    "track",
				Match:   tr.Match,
				Name:    tr.Name,
				Sources: trackSourceRefs(tr),
				Note:    "joined by title+artist (no shared ISRC/MBID)",
			})
		}
	}

	if LibraryKindByID("albums").Readable(s) {
		albumProviders, _ := collectProviders[Album, AlbumProvider](s, "albums",
			func(p AlbumProvider) func(context.Context) ([]Album, bool, error) {
				return p.ListUserSavedAlbums
			})
		var all []Album
		for _, t := range albumProviders {
			list, _, err := t.list(ctx)
			if err != nil {
				continue
			}
			all = append(all, list...)
		}
		s.resolveAlbumEntities(ctx, all)
		for _, al := range MergeAlbumsSync(all) {
			if al.Match != TrackMatchName || len(al.Sources) < 2 {
				continue
			}
			items = append(items, MergeReviewItem{
				Kind:    "album",
				Match:   al.Match,
				Name:    al.Name,
				Sources: albumSourceRefs(al),
				Note:    "joined by title+artist (no shared UPC/MBID)",
			})
		}
	}

	if LibraryKindByID("artists").Readable(s) {
		artistProviders, _ := collectProviders[Artist, ArtistProvider](s, "artists",
			func(p ArtistProvider) func(context.Context) ([]Artist, bool, error) {
				return p.ListUserFollowedArtists
			})
		var all []Artist
		for _, t := range artistProviders {
			list, _, err := t.list(ctx)
			if err != nil {
				continue
			}
			all = append(all, list...)
		}
		s.resolveArtistEntities(ctx, all)
		for _, ar := range mergeArtistLibrary(all) {
			if ar.Match != TrackMatchName || len(ar.Sources) < 2 {
				continue
			}
			items = append(items, MergeReviewItem{
				Kind:    "artist",
				Match:   ar.Match,
				Name:    ar.Name,
				Sources: artistSourceRefs(ar),
				Note:    "joined by name (no shared MBID)",
			})
		}
	}

	if s.libraryReadable() {
		plProviders, _ := collectProviders[Playlist, PlaylistProvider](s, "playlists",
			func(p PlaylistProvider) func(context.Context) ([]Playlist, bool, error) {
				return p.ListUserPlaylists
			})
		var all []Playlist
		for _, t := range plProviders {
			list, _, err := t.list(ctx)
			if err != nil {
				continue
			}
			all = append(all, list...)
		}
		for _, pl := range MergePlaylistsSyncLinked(all, s.links.PlaylistLinkKey) {
			if pl.Match != PlaylistMatchName || len(pl.Sources) < 2 {
				continue
			}
			items = append(items, MergeReviewItem{
				Kind:    "playlist",
				Match:   pl.Match,
				Name:    pl.Name,
				Sources: playlistSourceRefs(pl),
				Note:    "joined by name — POST /v1/library/links to confirm or split by renaming",
			})
		}
	}

	writeJSON(w, MergeReviewResponse{Items: items, Total: len(items)})
}

func trackSourceRefs(t Track) []string {
	out := make([]string, 0, len(t.Sources))
	for _, s := range t.Sources {
		out = append(out, s.Service+":"+s.ID)
	}
	return out
}

func albumSourceRefs(a Album) []string {
	out := make([]string, 0, len(a.Sources))
	for _, s := range a.Sources {
		out = append(out, s.Service+":"+s.ID)
	}
	return out
}

func artistSourceRefs(a Artist) []string {
	out := make([]string, 0, len(a.Sources))
	for _, s := range a.Sources {
		out = append(out, s.Service+":"+s.ID)
	}
	return out
}

func playlistSourceRefs(p Playlist) []string {
	out := make([]string, 0, len(p.Sources))
	for _, s := range p.Sources {
		out = append(out, s.Service+":"+s.ID)
	}
	return out
}
