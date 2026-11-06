package connectors

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/fudomunro/aegis/internal/shared"
	"github.com/fudomunro/aegis/internal/types"
)

// Spotify reconciliation operations (issue #254): paged list reads,
// metadata probes for freshness checks, and placement writes over neutral
// models. Core plans and schedules; these methods are the service-local
// operations it calls.

// GetPlaylistMeta implements types.PlaylistMetaProvider in one metadata
// call: name, snapshot_id version token, and item count.
func (c *SpotifyConnector) GetPlaylistMeta(ctx context.Context, playlistID string) (types.PlaylistMeta, bool, error) {
	raw, status, err := apiJSON(ctx, c.host, SpotifyServiceID, "GET", "/playlists/"+url.PathEscape(playlistID), nil, nil)
	if err != nil {
		if status == http.StatusNotFound {
			return types.PlaylistMeta{}, false, nil
		}
		return types.PlaylistMeta{}, false, err
	}
	name, _ := raw["name"].(string)
	snapshot, _ := raw["snapshot_id"].(string)
	tracks, _ := raw["tracks"].(map[string]any)
	total, _ := shared.AsInt(tracks["total"])
	return types.PlaylistMeta{Name: name, ItemCount: total, VersionToken: snapshot}, true, nil
}

// SavedTrackCount implements types.PlaylistMetaProvider: the size of the
// liked/saved list, the comparable token for liked-tracks copies.
func (c *SpotifyConnector) SavedTrackCount(ctx context.Context) (int, error) {
	raw, _, err := apiJSON(ctx, c.host, SpotifyServiceID, "GET", "/me/tracks",
		url.Values{"limit": []string{"1"}}, nil)
	if err != nil {
		return 0, err
	}
	total, _ := shared.AsInt(raw["total"])
	return total, nil
}

// ListPlaylistItems implements types.PlaylistItemsProvider. One page of a
// Spotify playlist's tracks starting at the offset named by cursor. Only
// the first page also fetches the playlist object, so the caller gets the
// ref name/URL without paying for it on every step.
func (c *SpotifyConnector) ListPlaylistItems(ctx context.Context, playlistID, cursor string, limit int) (types.Playlist, types.TrackPage, error) {
	ref := types.Playlist{Sources: []types.PlaylistSource{{Service: SpotifyServiceID, ID: playlistID}}}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	offset := 0
	if cursor != "" {
		var err error
		offset, err = strconv.Atoi(cursor)
		if err != nil || offset < 0 {
			return ref, types.TrackPage{}, fmt.Errorf("bad spotify cursor %q", cursor)
		}
	}
	if offset == 0 {
		raw, _, err := apiJSON(ctx, c.host, SpotifyServiceID, "GET", "/playlists/"+url.PathEscape(playlistID), nil, nil)
		if err != nil {
			return ref, types.TrackPage{}, err
		}
		ref.Name, _ = raw["name"].(string)
		if ext, ok := raw["external_urls"].(map[string]any); ok {
			ref.Sources[0].URL, _ = ext["spotify"].(string)
		}
	}
	q := url.Values{"limit": []string{strconv.Itoa(limit)}, "offset": []string{strconv.Itoa(offset)}}
	raw, _, err := apiJSON(ctx, c.host, SpotifyServiceID, "GET",
		"/playlists/"+url.PathEscape(playlistID)+"/tracks", q, nil)
	if err != nil {
		return ref, types.TrackPage{}, err
	}
	tracks, total := spotifyItemsToTracks(raw)
	ref.TrackCount = total
	page := types.TrackPage{Tracks: tracks, Total: total}
	if offset+len(tracks) < total {
		page.Next = strconv.Itoa(offset + len(tracks))
	}
	return ref, page, nil
}

// ListUserLikedTracksPage implements types.LikedTrackPageProvider.
func (c *SpotifyConnector) ListUserLikedTracksPage(ctx context.Context, cursor string, limit int) (types.TrackPage, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset := 0
	if cursor != "" {
		var err error
		offset, err = strconv.Atoi(cursor)
		if err != nil || offset < 0 {
			return types.TrackPage{}, fmt.Errorf("bad spotify cursor %q", cursor)
		}
	}
	q := url.Values{"limit": []string{strconv.Itoa(limit)}, "offset": []string{strconv.Itoa(offset)}}
	raw, _, err := apiJSON(ctx, c.host, SpotifyServiceID, "GET", "/me/tracks", q, nil)
	if err != nil {
		return types.TrackPage{}, err
	}
	tracks, total := spotifyItemsToTracks(raw)
	page := types.TrackPage{Tracks: tracks, Total: total}
	if offset+len(tracks) < total {
		page.Next = strconv.Itoa(offset + len(tracks))
	}
	return page, nil
}

// spotifyItemsToTracks flattens a Spotify paging object's items[] into
// neutral tracks and reports the overall total.
func spotifyItemsToTracks(raw map[string]any) ([]types.Track, int) {
	var out []types.Track
	for _, item := range SpotifyPagingItems(raw) {
		e, ok := item.(map[string]any)
		if !ok {
			continue
		}
		t, _ := e["track"].(map[string]any)
		if t == nil {
			continue
		}
		out = append(out, NeutralSpotifyTrack(t))
	}
	total, _ := shared.AsInt(raw["total"])
	return out, total
}

// PlacePlaylistItems implements types.PlaylistItemWriteProvider. Creates
// the target playlist when playlistID is empty, then resolves and appends
// tracks in chunks. An empty playlistID is the single-call path; a known
// playlistID continues a chained job.
func (c *SpotifyConnector) PlacePlaylistItems(ctx context.Context, playlistID, name string, tracks []types.Track) (types.PlacePlaylistItemsResult, error) {
	var result types.PlacePlaylistItemsResult
	if playlistID == "" {
		me, _, err := apiJSON(ctx, c.host, SpotifyServiceID, "GET", "/me", nil, nil)
		if err != nil {
			return result, err
		}
		userID, _ := me["id"].(string)
		if userID == "" {
			return result, fmt.Errorf("spotify /me returned no user id")
		}

		created, _, err := apiJSON(ctx, c.host, SpotifyServiceID, "POST",
			"/users/"+url.PathEscape(userID)+"/playlists", nil,
			map[string]any{"name": name, "public": false, "description": "Copied by Aegis"})
		if err != nil {
			return result, err
		}
		playlistID, _ = created["id"].(string)
		if playlistID == "" {
			return result, fmt.Errorf("spotify create playlist returned no id")
		}
		if ext, ok := created["external_urls"].(map[string]any); ok {
			result.URL, _ = ext["spotify"].(string)
		}
	}
	result.PlaylistID = playlistID

	var uris []string
	for _, t := range tracks {
		uri := c.resolveTrackURI(ctx, t, &result)
		if uri == "" {
			continue
		}
		uris = append(uris, uri)
	}

	const chunk = 100
	for i := 0; i < len(uris); i += chunk {
		end := i + chunk
		if end > len(uris) {
			end = len(uris)
		}
		_, _, err := apiJSON(ctx, c.host, SpotifyServiceID, "POST",
			"/playlists/"+url.PathEscape(playlistID)+"/tracks", nil,
			map[string]any{"uris": uris[i:end]})
		if err != nil {
			return result, err
		}
		result.Copied += end - i
	}
	return result, nil
}

// SaveLikedTracks implements types.LikedTrackWriteProvider.
func (c *SpotifyConnector) SaveLikedTracks(ctx context.Context, tracks []types.Track) (types.SaveLikedTracksResult, error) {
	var result types.SaveLikedTracksResult
	var ids []string
	for _, t := range tracks {
		id, uri := placeTrackLocalRef(t, SpotifyServiceID)
		if id == "" && strings.HasPrefix(uri, "spotify:track:") {
			id = strings.TrimPrefix(uri, "spotify:track:")
		}
		if id == "" {
			found, err := c.searchTrackURI(ctx, t)
			if err != nil {
				result.Skips = append(result.Skips, types.PlaceSkip{Name: placeTrackDisplay(t), Reason: err.Error()})
				continue
			}
			if strings.HasPrefix(found, "spotify:track:") {
				id = strings.TrimPrefix(found, "spotify:track:")
			}
		}
		if id == "" {
			result.Skips = append(result.Skips, types.PlaceSkip{Name: placeTrackDisplay(t), Reason: "no match on Spotify"})
			continue
		}
		ids = append(ids, id)
	}

	const chunk = 50
	for i := 0; i < len(ids); i += chunk {
		end := i + chunk
		if end > len(ids) {
			end = len(ids)
		}
		q := url.Values{"ids": []string{strings.Join(ids[i:end], ",")}}
		_, _, err := apiJSON(ctx, c.host, SpotifyServiceID, "PUT", "/me/tracks", q, nil)
		if err != nil {
			return result, err
		}
		result.Copied += end - i
	}
	return result, nil
}

// resolveTrackURI maps one track to a Spotify URI, appending a skip to the
// result when it cannot be resolved.
func (c *SpotifyConnector) resolveTrackURI(ctx context.Context, t types.Track, result *types.PlacePlaylistItemsResult) string {
	id, uri := placeTrackLocalRef(t, SpotifyServiceID)
	if uri == "" && id != "" {
		uri = "spotify:track:" + id
	}
	if uri == "" {
		found, err := c.searchTrackURI(ctx, t)
		if err != nil {
			result.Skips = append(result.Skips, types.PlaceSkip{Name: placeTrackDisplay(t), Reason: err.Error()})
			return ""
		}
		uri = found
	}
	if uri == "" {
		result.Skips = append(result.Skips, types.PlaceSkip{Name: placeTrackDisplay(t), Reason: "no match on Spotify"})
		return ""
	}
	return uri
}

func (c *SpotifyConnector) searchTrackURI(ctx context.Context, t types.Track) (string, error) {
	q := url.Values{
		"q":     []string{placeTrackQuery(t)},
		"type":  []string{"track"},
		"limit": []string{"1"},
	}
	raw, _, err := apiJSON(ctx, c.host, SpotifyServiceID, "GET", "/search", q, nil)
	if err != nil {
		return "", err
	}
	tracks, _ := raw["tracks"].(map[string]any)
	items, _ := tracks["items"].([]any)
	if len(items) == 0 {
		return "", nil
	}
	first, ok := items[0].(map[string]any)
	if !ok {
		return "", nil
	}
	uri, _ := first["uri"].(string)
	return uri, nil
}
