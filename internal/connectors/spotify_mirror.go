package connectors

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

// Spotify reconciliation operations (issue #254): paged list reads,
// metadata probes for freshness checks, and placement writes over neutral
// models. Core plans and schedules; these methods are the service-local
// operations it calls.

// GetPlaylistMeta implements kinds.PlaylistMetaProvider in one metadata
// call: name, snapshot_id version token, and item count.
func (c *SpotifyConnector) GetPlaylistMeta(ctx context.Context, playlistID string) (kinds.PlaylistMeta, bool, error) {
	raw, status, err := apiJSON(ctx, c.host, SpotifyServiceID, "GET", "/playlists/"+url.PathEscape(playlistID), nil, nil)
	if err != nil {
		if status == http.StatusNotFound {
			return kinds.PlaylistMeta{}, false, nil
		}
		return kinds.PlaylistMeta{}, false, err
	}
	name, _ := raw["name"].(string)
	snapshot, _ := raw["snapshot_id"].(string)
	tracks, _ := raw["tracks"].(map[string]any)
	total, _ := shared.AsInt(tracks["total"])
	return kinds.PlaylistMeta{Name: name, ItemCount: total, VersionToken: snapshot}, true, nil
}

// SavedTrackCount implements kinds.PlaylistMetaProvider: the size of the
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

// ListPlaylistItems implements kinds.PlaylistItemsProvider. One page of a
// Spotify playlist's tracks starting at the offset named by cursor. Only
// the first page also fetches the playlist object, so the caller gets the
// ref name/URL without paying for it on every step.
func (c *SpotifyConnector) ListPlaylistItems(ctx context.Context, playlistID, cursor string, limit int) (kinds.Playlist, kinds.TrackPage, error) {
	ref := kinds.Playlist{Sources: []kinds.PlaylistSource{{Service: SpotifyServiceID, ID: playlistID}}}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	offset := 0
	if cursor != "" {
		var err error
		offset, err = strconv.Atoi(cursor)
		if err != nil || offset < 0 {
			return ref, kinds.TrackPage{}, fmt.Errorf("bad spotify cursor %q", cursor)
		}
	}
	if offset == 0 {
		raw, _, err := apiJSON(ctx, c.host, SpotifyServiceID, "GET", "/playlists/"+url.PathEscape(playlistID), nil, nil)
		if err != nil {
			return ref, kinds.TrackPage{}, err
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
		return ref, kinds.TrackPage{}, err
	}
	tracks, total := spotifyItemsToTracks(raw)
	ref.TrackCount = total
	page := kinds.TrackPage{Tracks: tracks, Total: total}
	if offset+len(tracks) < total {
		page.Next = strconv.Itoa(offset + len(tracks))
	}
	return ref, page, nil
}

// ListUserLikedTracksPage implements kinds.LikedTrackPageProvider.
func (c *SpotifyConnector) ListUserLikedTracksPage(ctx context.Context, cursor string, limit int) (kinds.TrackPage, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset := 0
	if cursor != "" {
		var err error
		offset, err = strconv.Atoi(cursor)
		if err != nil || offset < 0 {
			return kinds.TrackPage{}, fmt.Errorf("bad spotify cursor %q", cursor)
		}
	}
	q := url.Values{"limit": []string{strconv.Itoa(limit)}, "offset": []string{strconv.Itoa(offset)}}
	raw, _, err := apiJSON(ctx, c.host, SpotifyServiceID, "GET", "/me/tracks", q, nil)
	if err != nil {
		return kinds.TrackPage{}, err
	}
	tracks, total := spotifyItemsToTracks(raw)
	page := kinds.TrackPage{Tracks: tracks, Total: total}
	if offset+len(tracks) < total {
		page.Next = strconv.Itoa(offset + len(tracks))
	}
	return page, nil
}

// spotifyItemsToTracks flattens a Spotify paging object's items[] into
// neutral tracks and reports the overall total.
func spotifyItemsToTracks(raw map[string]any) ([]kinds.Track, int) {
	var out []kinds.Track
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

// PlacePlaylistItems implements kinds.PlaylistItemWriteProvider. Creates
// the target playlist when playlistID is empty, then resolves and appends
// tracks in chunks. An empty playlistID is the single-call path; a known
// playlistID continues a chained job and, with a non-empty name, sets the
// playlist's title so renames propagate to the copy (issue #365).
func (c *SpotifyConnector) PlacePlaylistItems(ctx context.Context, playlistID, name string, tracks []kinds.Track) (kinds.PlacePlaylistItemsResult, error) {
	var result kinds.PlacePlaylistItemsResult
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
	} else if strings.TrimSpace(name) != "" {
		// Title write on the append path (issue #365): a partial update,
		// so a rename on the source renames the converged copy.
		if _, _, err := apiJSON(ctx, c.host, SpotifyServiceID, "PUT",
			"/playlists/"+url.PathEscape(playlistID), nil,
			map[string]any{"name": name}); err != nil {
			return result, err
		}
	}
	result.PlaylistID = playlistID

	var uris []string
	var recs []kinds.PlaceRecord
	for _, t := range tracks {
		ref, ok := c.resolvePlaylistTrack(ctx, t, &result.Skips)
		if !ok {
			continue
		}
		uris = append(uris, ref.ID)
		recs = append(recs, ref.Record)
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
		result.Placed = append(result.Placed, recs[i:end]...)
	}
	return result, nil
}

// SetPlaylistOrder implements kinds.PlaylistOrderProvider (issue #378)
// with positional moves — the reorder mode of the playlist-items PUT — so
// the write is a permutation of what the playlist already holds and never
// changes membership. The desired ids are matched onto the playlist's own
// entries in listing order; unknown ids are ignored and unlisted entries
// keep their relative order at the end.
func (c *SpotifyConnector) SetPlaylistOrder(ctx context.Context, playlistID string, orderedIDs []string) error {
	current, err := spotifyPlaylistEntryIDs(ctx, c, playlistID)
	if err != nil {
		return err
	}
	target := orderEntryIndexes(orderedIDs, current)
	// Insertion sort on the working order: every move pulls an entry from
	// the right of its target slot and inserts it there. insert_before
	// then never points past the removed entry, which is the one shift
	// the endpoint's position semantics make murky.
	work := make([]int, len(target))
	for i := range work {
		work[i] = i
	}
	for i, want := range target {
		j := i
		for work[j] != want {
			j++
		}
		if j == i {
			continue
		}
		if _, _, err := apiJSON(ctx, c.host, SpotifyServiceID, "PUT",
			"/playlists/"+url.PathEscape(playlistID)+"/tracks", nil,
			map[string]any{"range_start": j, "insert_before": i}); err != nil {
			return err
		}
		work = append(work[:j], work[j+1:]...)
		work = append(work[:i], append([]int{want}, work[i:]...)...)
	}
	return nil
}

// spotifyPlaylistEntryIDs pages a playlist's entry ids in order — the
// move positions SetPlaylistOrder computes against. Entries the list maps
// to no track id (local files) come back as "" so the caller can still
// count them in the position space.
func spotifyPlaylistEntryIDs(ctx context.Context, c *SpotifyConnector, playlistID string) ([]string, error) {
	var out []string
	cursor := ""
	for i := 0; i < shared.LibraryPageCap; i++ {
		_, page, err := c.ListPlaylistItems(ctx, playlistID, cursor, 100)
		if err != nil {
			return nil, err
		}
		for _, t := range page.Tracks {
			id := ""
			for _, src := range t.Sources {
				if src.Service == SpotifyServiceID {
					id = src.ID
				}
			}
			out = append(out, id)
		}
		if page.Next == "" {
			return out, nil
		}
		cursor = page.Next
	}
	return out, nil
}

// orderEntryIndexes maps the desired id sequence onto the playlist's
// current entries: for each wanted id, the next not-yet-taken entry
// carrying it (positional for the "" ids of untargetable entries). Ids
// with no entry are ignored; entries no id names are appended at the end
// in their current order — a reorder never changes membership.
func orderEntryIndexes(orderedIDs, current []string) []int {
	taken := make([]bool, len(current))
	out := make([]int, 0, len(current))
	for _, id := range orderedIDs {
		for i, have := range current {
			if !taken[i] && have == id {
				taken[i] = true
				out = append(out, i)
				break
			}
		}
	}
	for i := range current {
		if !taken[i] {
			out = append(out, i)
		}
	}
	return out
}

// SaveLikedTracks implements kinds.LikedTrackWriteProvider.
// trackLadder is Spotify's track catalog search behind the shared match
// ladder: an exact ISRC query (no duration guard — the ISRC proves
// identity), then a name query guarded by duration.
func (c *SpotifyConnector) trackLadder() SearchLadder[kinds.Track] {
	return SearchLadder[kinds.Track]{
		Service:     SpotifyServiceName,
		Display:     placeTrackDisplay,
		IdentityKey: kinds.TrackIdentityKey,
		DurableID: func(t kinds.Track) (string, string) {
			if _, how := kinds.TrackMatchKey(t); how == kinds.TrackMatchISRC {
				return kinds.TrackEntityID(t), kinds.TrackMatchISRC
			}
			return "", ""
		},
		ByDurableID: func(ctx context.Context, _ kinds.Track, isrc string) (string, bool, error) {
			id, _, err := c.searchTrackFirst(ctx, "isrc:"+isrc)
			return id, id != "", err
		},
		ByName: func(ctx context.Context, t kinds.Track) (string, error) {
			id, durationMs, err := c.searchTrackFirst(ctx, placeTrackQuery(t))
			if err != nil || id == "" {
				return "", err
			}
			if !kinds.DurationCompatible(t, kinds.Track{DurationMs: durationMs}) {
				return "", nil
			}
			return id, nil
		},
	}
}

// resolveSavedTrack is the save-side resolve: the bare-id ref rung, else
// the track ladder.
func (c *SpotifyConnector) resolveSavedTrack(ctx context.Context, t kinds.Track, skips *[]kinds.PlaceSkip) (LadderRef, bool) {
	id, uri := placeTrackLocalRef(t, SpotifyServiceID)
	if id == "" && strings.HasPrefix(uri, "spotify:track:") {
		id = strings.TrimPrefix(uri, "spotify:track:")
	}
	if id != "" {
		return c.trackLadder().Ref(t, id), true
	}
	return c.trackLadder().Resolve(ctx, t, skips)
}

// resolvePlaylistTrack is the playlist-write resolve: the write id is the
// Spotify URI form, while the record stamps the bare id.
func (c *SpotifyConnector) resolvePlaylistTrack(ctx context.Context, t kinds.Track, skips *[]kinds.PlaceSkip) (LadderRef, bool) {
	id, uri := placeTrackLocalRef(t, SpotifyServiceID)
	if uri == "" && id != "" {
		uri = "spotify:track:" + id
	}
	if uri != "" {
		ref := c.trackLadder().Ref(t, strings.TrimPrefix(uri, "spotify:track:"))
		ref.ID = uri
		return ref, true
	}
	ref, ok := c.trackLadder().Resolve(ctx, t, skips)
	if !ok {
		return LadderRef{}, false
	}
	ref.ID = "spotify:track:" + ref.ID
	return ref, true
}

// spotifySaveChunks PUTs the id batch to a library endpoint in chunks of
// 50, reporting how many landed before any failure.
func spotifySaveChunks(ctx context.Context, host Host, path string, extra url.Values, ids []string) (int, error) {
	const chunk = 50
	written := 0
	for i := 0; i < len(ids); i += chunk {
		end := min(i+chunk, len(ids))
		q := url.Values{"ids": []string{strings.Join(ids[i:end], ",")}}
		for k, vs := range extra {
			q[k] = vs
		}
		if _, _, err := apiJSON(ctx, host, SpotifyServiceID, "PUT", path, q, nil); err != nil {
			return written, err
		}
		written += end - i
	}
	return written, nil
}

func (c *SpotifyConnector) SaveLikedTracks(ctx context.Context, tracks []kinds.Track) (kinds.SaveLikedTracksResult, error) {
	copied, placed, skips, err := SaveOnLadder(ctx, tracks, c.resolveSavedTrack,
		func(ctx context.Context, ids []string) (int, error) {
			return spotifySaveChunks(ctx, c.host, "/me/tracks", nil, ids)
		})
	return kinds.SaveLikedTracksResult{Copied: copied, Placed: placed, Skips: skips}, err
}

// searchTrackFirst returns the first catalog hit's bare track id and
// duration.
func (c *SpotifyConnector) searchTrackFirst(ctx context.Context, query string) (string, int, error) {
	q := url.Values{
		"q":     []string{query},
		"type":  []string{"track"},
		"limit": []string{"1"},
	}
	raw, _, err := apiJSON(ctx, c.host, SpotifyServiceID, "GET", "/search", q, nil)
	if err != nil {
		return "", 0, err
	}
	tracks, _ := raw["tracks"].(map[string]any)
	items, _ := tracks["items"].([]any)
	if len(items) == 0 {
		return "", 0, nil
	}
	first, ok := items[0].(map[string]any)
	if !ok {
		return "", 0, nil
	}
	uri, _ := first["uri"].(string)
	durationMs, _ := shared.AsInt(first["duration_ms"])
	return strings.TrimPrefix(uri, "spotify:track:"), durationMs, nil
}

// albumLadder is Spotify's album catalog search behind the shared match
// ladder: an exact UPC query, then a title + primary artist query guarded
// by that fallback's identity.
func (c *SpotifyConnector) albumLadder() SearchLadder[kinds.Album] {
	return SearchLadder[kinds.Album]{
		Service:     SpotifyServiceName,
		Display:     placeAlbumDisplay,
		IdentityKey: kinds.AlbumIdentityKey,
		DurableID: func(a kinds.Album) (string, string) {
			if _, how := kinds.AlbumMatchKey(a); how == kinds.AlbumMatchUPC {
				return kinds.AlbumEntityID(a), kinds.AlbumMatchUPC
			}
			return "", ""
		},
		ByDurableID: func(ctx context.Context, _ kinds.Album, upc string) (string, bool, error) {
			hit, err := c.searchAlbumHit(ctx, "upc:"+upc)
			if err != nil {
				return "", false, err
			}
			id := placeAlbumLocalRef(hit, SpotifyServiceID)
			return id, id != "", nil
		},
		ByName: func(ctx context.Context, a kinds.Album) (string, error) {
			hit, err := c.searchAlbumHit(ctx, placeAlbumQuery(a))
			if err != nil {
				return "", err
			}
			if !albumTitleArtistCompatible(hit, a) {
				return "", nil
			}
			return placeAlbumLocalRef(hit, SpotifyServiceID), nil
		},
	}
}

// artistLadder is Spotify's artist catalog search behind the shared match
// ladder — name search guarded by normalized-name equality (the MBID path
// is cover-letter only: Spotify search exposes no MBID query).
func (c *SpotifyConnector) artistLadder() SearchLadder[kinds.Artist] {
	return SearchLadder[kinds.Artist]{
		Service:     SpotifyServiceName,
		Display:     placeArtistDisplay,
		IdentityKey: kinds.ArtistIdentityKey,
		DurableID:   func(kinds.Artist) (string, string) { return "", "" },
		ByName: func(ctx context.Context, a kinds.Artist) (string, error) {
			return c.searchArtistID(ctx, a)
		},
	}
}

// SaveAlbums implements kinds.AlbumWriteProvider over Spotify's saved-albums
// library. Saving is create-only — a placement never un-saves (issue #375).
func (c *SpotifyConnector) SaveAlbums(ctx context.Context, albums []kinds.Album) (kinds.SaveAlbumsResult, error) {
	ladder := c.albumLadder()
	copied, placed, skips, err := SaveOnLadder(ctx, albums,
		func(ctx context.Context, a kinds.Album, skips *[]kinds.PlaceSkip) (LadderRef, bool) {
			if id := placeAlbumLocalRef(a, SpotifyServiceID); id != "" {
				return ladder.Ref(a, id), true
			}
			return ladder.Resolve(ctx, a, skips)
		},
		func(ctx context.Context, ids []string) (int, error) {
			return spotifySaveChunks(ctx, c.host, "/me/albums", nil, ids)
		})
	return kinds.SaveAlbumsResult{Copied: copied, Placed: placed, Skips: skips}, err
}

// FollowArtists implements kinds.ArtistWriteProvider over Spotify's
// following list. Following is create-only — a placement never unfollows
// (issue #375).
func (c *SpotifyConnector) FollowArtists(ctx context.Context, artists []kinds.Artist) (kinds.FollowArtistsResult, error) {
	ladder := c.artistLadder()
	copied, placed, skips, err := SaveOnLadder(ctx, artists,
		func(ctx context.Context, a kinds.Artist, skips *[]kinds.PlaceSkip) (LadderRef, bool) {
			if id := placeArtistLocalRef(a, SpotifyServiceID); id != "" {
				return ladder.Ref(a, id), true
			}
			return ladder.Resolve(ctx, a, skips)
		},
		func(ctx context.Context, ids []string) (int, error) {
			return spotifySaveChunks(ctx, c.host, "/me/following", url.Values{"type": []string{"artist"}}, ids)
		})
	return kinds.FollowArtistsResult{Copied: copied, Placed: placed, Skips: skips}, err
}

// searchAlbumHit returns the first catalog hit as a neutral album (empty
// when there is none) so the caller can guard it against the placement
// ladder before accepting it.
func (c *SpotifyConnector) searchAlbumHit(ctx context.Context, query string) (kinds.Album, error) {
	q := url.Values{
		"q":     []string{query},
		"type":  []string{"album"},
		"limit": []string{"1"},
	}
	raw, _, err := apiJSON(ctx, c.host, SpotifyServiceID, "GET", "/search", q, nil)
	if err != nil {
		return kinds.Album{}, err
	}
	albums, _ := raw["albums"].(map[string]any)
	items, _ := albums["items"].([]any)
	if len(items) == 0 {
		return kinds.Album{}, nil
	}
	first, ok := items[0].(map[string]any)
	if !ok {
		return kinds.Album{}, nil
	}
	return NeutralSpotifyAlbum(first), nil
}

// searchArtistID maps one artist to a Spotify catalog id: name search
// guarded by normalized-name equality. "" means the guard rejected the hit.
func (c *SpotifyConnector) searchArtistID(ctx context.Context, a kinds.Artist) (string, error) {
	q := url.Values{
		"q":     []string{placeArtistDisplay(a)},
		"type":  []string{"artist"},
		"limit": []string{"1"},
	}
	raw, _, err := apiJSON(ctx, c.host, SpotifyServiceID, "GET", "/search", q, nil)
	if err != nil {
		return "", err
	}
	artists, _ := raw["artists"].(map[string]any)
	items, _ := artists["items"].([]any)
	if len(items) == 0 {
		return "", nil
	}
	first, ok := items[0].(map[string]any)
	if !ok {
		return "", nil
	}
	hit := SpotifyNeutralArtist(first)
	if !artistNameCompatible(hit, a) {
		return "", nil
	}
	return placeArtistLocalRef(hit, SpotifyServiceID), nil
}
