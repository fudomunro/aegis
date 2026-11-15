package connectors

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

// Apple Music reconciliation operations (issue #254 pattern): paged list
// reads, metadata probes for freshness, and placement writes over neutral
// models. Core plans and schedules; these methods are the service-local
// operations it calls. Placement matches ISRC-first (issue #4): Apple's
// catalog filter[isrc] lookup is exact, so a track that carries a durable
// id lands without a name search.

// --- reads ---

// appleMusicPage parses a page cursor and clamps the limit: the cursor is a
// plain offset so the caller's paging stays provider-free.
func appleMusicPage(cursor string, limit int) (int, int, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	offset := 0
	if cursor != "" {
		var err error
		offset, err = strconv.Atoi(cursor)
		if err != nil || offset < 0 {
			return 0, 0, errAppleMusicCursor(cursor)
		}
	}
	return offset, limit, nil
}

// ListPlaylistItems implements kinds.PlaylistItemsProvider. One page of a
// library playlist's tracks starting at the offset named by cursor. Only the
// first page also fetches the playlist object, so the caller gets the ref
// name/URL without paying for it on every step.
func (c *AppleMusicConnector) ListPlaylistItems(ctx context.Context, playlistID, cursor string, limit int) (kinds.Playlist, kinds.TrackPage, error) {
	ref := kinds.Playlist{Sources: []kinds.PlaylistSource{{Service: AppleMusicServiceID, ID: playlistID}}}
	offset, limit, err := appleMusicPage(cursor, limit)
	if err != nil {
		return ref, kinds.TrackPage{}, err
	}
	if offset == 0 {
		raw, _, err := c.apiJSON(ctx, "GET", "/me/library/playlists/"+url.PathEscape(playlistID), nil, nil)
		if err != nil {
			return ref, kinds.TrackPage{}, err
		}
		ref = neutralAppleMusicPlaylist(firstResource(raw))
	}
	q := url.Values{"limit": []string{strconv.Itoa(limit)}, "offset": []string{strconv.Itoa(offset)}}
	raw, _, err := c.apiJSON(ctx, "GET", "/me/library/playlists/"+url.PathEscape(playlistID)+"/tracks", q, nil)
	if err != nil {
		return ref, kinds.TrackPage{}, err
	}
	page, err := c.trackPageFrom(ctx, raw)
	if err != nil {
		return ref, kinds.TrackPage{}, err
	}
	return ref, page, nil
}

// ListUserLikedTracksPage implements kinds.LikedTrackPageProvider: one page
// of library songs. Chained sync jobs step one upstream page at a time.
func (c *AppleMusicConnector) ListUserLikedTracksPage(ctx context.Context, cursor string, limit int) (kinds.TrackPage, error) {
	offset, limit, err := appleMusicPage(cursor, limit)
	if err != nil {
		return kinds.TrackPage{}, err
	}
	q := url.Values{"limit": []string{strconv.Itoa(limit)}, "offset": []string{strconv.Itoa(offset)}}
	raw, _, err := c.apiJSON(ctx, "GET", "/me/library/songs", q, nil)
	if err != nil {
		return kinds.TrackPage{}, err
	}
	return c.trackPageFrom(ctx, raw)
}

// trackPageFrom maps a library-songs/playlist-tracks response and fills
// missing ISRCs from the catalog by catalog id (batched), so Apple-sourced
// tracks match durably on the other side of a mirror.
func (c *AppleMusicConnector) trackPageFrom(ctx context.Context, raw map[string]any) (kinds.TrackPage, error) {
	page := kinds.TrackPage{}
	for _, item := range appleMusicData(raw) {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		page.Tracks = append(page.Tracks, neutralAppleMusicSong(obj))
	}
	if meta, ok := raw["meta"].(map[string]any); ok {
		page.Total, _ = shared.AsInt(meta["total"])
	}
	next, _ := raw["next"].(string)
	page.Next = c.offsetCursor(next, len(page.Tracks))
	c.enrichTrackISRCs(ctx, page.Tracks)
	return page, nil
}

// offsetCursor turns a MusicKit "next" path into the offset cursor the
// chained job carries. "" ends the list.
func (c *AppleMusicConnector) offsetCursor(next string, fetched int) string {
	if next == "" {
		return ""
	}
	// The next path is provider-shaped ("/v1/me/...?offset=N"); the job
	// cursor is just the offset so the caller's paging stays provider-free.
	if u, err := url.Parse(next); err == nil {
		if v := u.Query().Get("offset"); v != "" {
			return v
		}
	}
	return strconv.Itoa(fetched)
}

// enrichTrackISRCs fills EntityID (ISRC) for tracks that only carry a
// catalog id, in batched catalog lookups. Fail-open: a lookup failure leaves
// the track name-matchable, exactly as before.
func (c *AppleMusicConnector) enrichTrackISRCs(ctx context.Context, tracks []kinds.Track) {
	type slot struct {
		idx int
		id  string
	}
	var missing []slot
	for i := range tracks {
		if tracks[i].EntityID != "" {
			continue
		}
		id := appleMusicTrackCatalogID(tracks[i])
		if id == "" {
			continue
		}
		missing = append(missing, slot{idx: i, id: id})
	}
	const batch = 100
	byID := map[string]string{}
	for start := 0; start < len(missing); start += batch {
		end := min(start+batch, len(missing))
		ids := make([]string, 0, end-start)
		for _, m := range missing[start:end] {
			ids = append(ids, m.id)
		}
		hits, err := c.catalogByIDs(ctx, ids)
		if err != nil {
			return
		}
		for _, h := range hits {
			if h.ISRC != "" {
				byID[h.ID] = h.ISRC
			}
		}
	}
	for _, m := range missing {
		if isrc := byID[m.id]; isrc != "" {
			tracks[m.idx].EntityID = isrc
			if tracks[m.idx].Extra == nil {
				tracks[m.idx].Extra = map[string]any{}
			}
			tracks[m.idx].Extra["isrc"] = isrc
		}
	}
}

// appleMusicTrackCatalogID returns the catalog id an Apple-sourced track
// carries in its source extras.
func appleMusicTrackCatalogID(t kinds.Track) string {
	for _, src := range t.Sources {
		if src.Service != AppleMusicServiceID || src.Extra == nil {
			continue
		}
		if id, _ := src.Extra["catalog_id"].(string); id != "" {
			return id
		}
	}
	return ""
}

// GetPlaylistMeta implements kinds.PlaylistMetaProvider: name, item count,
// and the playParams version hash when Apple exposes one.
func (c *AppleMusicConnector) GetPlaylistMeta(ctx context.Context, playlistID string) (kinds.PlaylistMeta, bool, error) {
	raw, status, err := c.apiJSON(ctx, "GET", "/me/library/playlists/"+url.PathEscape(playlistID), nil, nil)
	if err != nil {
		if status == 404 {
			return kinds.PlaylistMeta{}, false, nil
		}
		return kinds.PlaylistMeta{}, false, err
	}
	res := firstResource(raw)
	if res == nil {
		return kinds.PlaylistMeta{}, false, nil
	}
	p := neutralAppleMusicPlaylist(res)
	meta := kinds.PlaylistMeta{Name: p.Name}
	if len(p.Sources) > 0 && p.Sources[0].Extra != nil {
		meta.VersionToken, _ = p.Sources[0].Extra["version_hash"].(string)
	}
	q := url.Values{"limit": []string{"1"}}
	tracksRaw, _, err := c.apiJSON(ctx, "GET", "/me/library/playlists/"+url.PathEscape(playlistID)+"/tracks", q, nil)
	if err == nil {
		if m, ok := tracksRaw["meta"].(map[string]any); ok {
			meta.ItemCount, _ = shared.AsInt(m["total"])
		}
	}
	return meta, true, nil
}

// SavedTrackCount implements kinds.PlaylistMetaProvider: the size of the
// library-songs list, the comparable token for liked-tracks copies.
func (c *AppleMusicConnector) SavedTrackCount(ctx context.Context) (int, error) {
	q := url.Values{"limit": []string{"1"}}
	raw, _, err := c.apiJSON(ctx, "GET", "/me/library/songs", q, nil)
	if err != nil {
		return 0, err
	}
	if m, ok := raw["meta"].(map[string]any); ok {
		n, _ := shared.AsInt(m["total"])
		return n, nil
	}
	return 0, nil
}

// --- writes ---

// PlacePlaylistItems implements kinds.PlaylistItemWriteProvider over library
// playlists: create when playlistID is empty, else append (and rename when
// name is set). Each track resolves to a song id before it is placed —
// service-local reference, then exact ISRC catalog lookup, then a
// duration-guarded name search.
func (c *AppleMusicConnector) PlacePlaylistItems(ctx context.Context, playlistID, name string, tracks []kinds.Track) (kinds.PlacePlaylistItemsResult, error) {
	result := kinds.PlacePlaylistItemsResult{}
	if len(tracks) == 0 {
		result.PlaylistID = playlistID
		return result, nil
	}
	ids := make([]appleMusicPlacement, 0, len(tracks))
	for _, t := range tracks {
		placement, ok := c.resolveSongPlacement(ctx, t, &result.Skips)
		if !ok {
			continue
		}
		ids = append(ids, placement)
	}

	if playlistID == "" {
		created, err := c.createPlaylist(ctx, name, ids)
		if err != nil {
			return result, err
		}
		result.PlaylistID = created.id
		result.URL = created.url
	} else {
		if err := c.addPlaylistTracks(ctx, playlistID, ids); err != nil {
			return result, err
		}
		result.PlaylistID = playlistID
		if name != "" {
			// Title write is how playlist renames propagate to a converged
			// copy; fail-open — the append already landed.
			_ = c.renamePlaylist(ctx, playlistID, name)
		}
	}
	result.Copied = len(ids)
	for _, p := range ids {
		result.Placed = append(result.Placed, p.record)
	}
	return result, nil
}

// saveAppleMusicLibrary is the create-only save shape shared by
// SaveLikedTracks and SaveAlbums: resolve each entity through its ladder,
// dedupe by catalog id, then add the batch to the library. Placed and Skips
// are returned even when the add fails, so the caller reports partial work.
// addToLibraryBatch is the create-only write behind SaveLikedTracks and
// SaveAlbums: one batched add-a-resource call for the resolved catalog ids.
func (c *AppleMusicConnector) addToLibraryBatch(kind string) func(context.Context, []string) (int, error) {
	return func(ctx context.Context, ids []string) (int, error) {
		if err := c.addToLibrary(ctx, kind, ids); err != nil {
			return 0, err
		}
		return len(ids), nil
	}
}

// SaveLikedTracks implements kinds.LikedTrackWriteProvider: add resolved
// catalog songs to the library. Saving is create-only — a placement never
// un-saves (issue #375), and "loved" has no enumerable read surface, so the
// library is the liked/saved home (docs/connect/apple-music.md).
func (c *AppleMusicConnector) SaveLikedTracks(ctx context.Context, tracks []kinds.Track) (kinds.SaveLikedTracksResult, error) {
	copied, placed, skips, err := SaveOnLadder(ctx, tracks,
		func(ctx context.Context, t kinds.Track, skips *[]kinds.PlaceSkip) (LadderRef, bool) {
			p, ok := c.resolveSongPlacement(ctx, t, skips)
			return p.ladderRef(), ok
		},
		c.addToLibraryBatch("songs"))
	return kinds.SaveLikedTracksResult{Copied: copied, Placed: placed, Skips: skips}, err
}

// SaveAlbums implements kinds.AlbumWriteProvider: add resolved catalog
// albums to the library. Create-only, like SaveLikedTracks.
func (c *AppleMusicConnector) SaveAlbums(ctx context.Context, albums []kinds.Album) (kinds.SaveAlbumsResult, error) {
	copied, placed, skips, err := SaveOnLadder(ctx, albums,
		func(ctx context.Context, a kinds.Album, skips *[]kinds.PlaceSkip) (LadderRef, bool) {
			p, ok := c.resolveAlbumPlacement(ctx, a, skips)
			return p.ladderRef(), ok
		},
		c.addToLibraryBatch("albums"))
	return kinds.SaveAlbumsResult{Copied: copied, Placed: placed, Skips: skips}, err
}

// --- placement matching ---

// appleMusicPlacement is one resolved write: the provider id to send plus
// the match record the copy ledger stamps from.
type appleMusicPlacement struct {
	libraryID string
	catalog   string
	record    kinds.PlaceRecord
}

func (p appleMusicPlacement) catalogID() string {
	if p.catalog != "" {
		return p.catalog
	}
	return p.libraryID
}

// ladderRef collapses the placement onto the shared resolve shape: the
// write id is the catalog id when one is known, else the library id.
func (p appleMusicPlacement) ladderRef() LadderRef {
	return LadderRef{ID: p.catalogID(), Record: p.record}
}

// songLadder is Apple Music's track catalog search behind the shared match
// ladder: an exact filter[isrc] lookup (duration-guarded among several
// releases of one ISRC), then a name search guarded by duration.
func (c *AppleMusicConnector) songLadder() SearchLadder[kinds.Track] {
	return SearchLadder[kinds.Track]{
		Service:     AppleMusicServiceName,
		Display:     placeTrackDisplay,
		IdentityKey: kinds.TrackIdentityKey,
		DurableID: func(t kinds.Track) (string, string) {
			if _, how := kinds.TrackMatchKey(t); how == kinds.TrackMatchISRC {
				return kinds.TrackEntityID(t), kinds.TrackMatchISRC
			}
			return "", ""
		},
		ByDurableID: func(ctx context.Context, t kinds.Track, isrc string) (string, bool, error) {
			hits, err := c.catalogByISRC(ctx, isrc)
			if err != nil {
				return "", false, err
			}
			hit, ok := appleMusicDurationGuard(t, hits, true)
			return hit.ID, ok, nil
		},
		ByName: func(ctx context.Context, t kinds.Track) (string, error) {
			hits, err := c.catalogSearch(ctx, placeTrackQuery(t), 5)
			if err != nil {
				return "", err
			}
			hit, ok := appleMusicDurationGuard(t, hits, false)
			if !ok {
				return "", nil
			}
			return hit.ID, nil
		},
	}
}

// albumLadder is Apple Music's album catalog search behind the shared
// match ladder: an exact filter[upc] lookup, then a name search guarded by
// title + primary artist.
func (c *AppleMusicConnector) albumLadder() SearchLadder[kinds.Album] {
	return SearchLadder[kinds.Album]{
		Service:     AppleMusicServiceName,
		Display:     placeAlbumDisplay,
		IdentityKey: kinds.AlbumIdentityKey,
		DurableID: func(a kinds.Album) (string, string) {
			if _, how := kinds.AlbumMatchKey(a); how == kinds.AlbumMatchUPC {
				return kinds.AlbumEntityID(a), kinds.AlbumMatchUPC
			}
			return "", ""
		},
		ByDurableID: func(ctx context.Context, _ kinds.Album, upc string) (string, bool, error) {
			q := url.Values{"filter[upc]": []string{upc}}
			raw, _, err := c.apiJSON(ctx, "GET", "/catalog/"+url.PathEscape(c.storefront(ctx))+"/albums", q, nil)
			if err != nil {
				return "", false, err
			}
			id := ""
			if hit := firstResource(raw); hit != nil {
				id, _ = hit["id"].(string)
			}
			return id, id != "", nil
		},
		ByName: func(ctx context.Context, a kinds.Album) (string, error) {
			q := url.Values{"term": []string{placeAlbumQuery(a)}, "types": []string{"albums"}, "limit": []string{"3"}}
			raw, _, err := c.apiJSON(ctx, "GET", "/catalog/"+url.PathEscape(c.storefront(ctx))+"/albums", q, nil)
			if err != nil {
				return "", err
			}
			// Name-rung guard: only an album whose title and primary artist
			// agree with the wanted one may stand in — the first hit alone
			// may be a different record of the same name.
			for _, item := range appleMusicData(raw) {
				obj, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if id, _ := obj["id"].(string); id != "" && albumTitleArtistCompatible(neutralAppleMusicAlbum(obj), a) {
					return id, nil
				}
			}
			return "", nil
		},
	}
}

// resolveSongPlacement runs the match ladder for one track: the
// service-local reference rung (whose write id may be a catalog id behind a
// library id), then the shared search rungs.
func (c *AppleMusicConnector) resolveSongPlacement(ctx context.Context, t kinds.Track, skips *[]kinds.PlaceSkip) (appleMusicPlacement, bool) {
	ladder := c.songLadder()
	id, _ := placeTrackLocalRef(t, AppleMusicServiceID)
	if id != "" {
		placement := appleMusicPlacement{libraryID: id}
		if catalogID := appleMusicTrackCatalogID(t); catalogID != "" {
			placement.catalog = catalogID
		} else if !strings.Contains(id, ".") {
			// A bare id in an Apple source is already a catalog id.
			placement.catalog = id
		}
		placement.record = ladder.Ref(t, id).Record
		return placement, true
	}
	ref, ok := ladder.Resolve(ctx, t, skips)
	if !ok {
		return appleMusicPlacement{}, false
	}
	return appleMusicPlacement{catalog: ref.ID, record: ref.Record}, true
}

// resolveAlbumPlacement matches an album: the service-local reference rung,
// then the shared search rungs.
func (c *AppleMusicConnector) resolveAlbumPlacement(ctx context.Context, a kinds.Album, skips *[]kinds.PlaceSkip) (appleMusicPlacement, bool) {
	ladder := c.albumLadder()
	for _, src := range a.Sources {
		if src.Service != AppleMusicServiceID {
			continue
		}
		id := src.ID
		if id == "" && src.Extra != nil {
			id, _ = src.Extra["catalog_id"].(string)
		}
		if id == "" {
			continue
		}
		return appleMusicPlacement{catalog: id, record: ladder.Ref(a, id).Record}, true
	}
	ref, ok := ladder.Resolve(ctx, a, skips)
	if !ok {
		return appleMusicPlacement{}, false
	}
	return appleMusicPlacement{catalog: ref.ID, record: ref.Record}, true
}

// --- write verbs ---

type createdPlaylist struct {
	id  string
	url string
}

// createPlaylist creates a library playlist, optionally seeded with tracks.
func (c *AppleMusicConnector) createPlaylist(ctx context.Context, name string, tracks []appleMusicPlacement) (createdPlaylist, error) {
	body := map[string]any{
		"attributes": map[string]any{"name": name},
	}
	if len(tracks) > 0 {
		body["relationships"] = map[string]any{
			"tracks": map[string]any{"data": appleMusicTrackRefs(tracks)},
		}
	}
	raw, status, err := c.apiJSON(ctx, "POST", "/me/library/playlists", nil, body)
	if err != nil {
		return createdPlaylist{}, err
	}
	created := createdPlaylist{}
	for _, item := range appleMusicData(raw) {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		created.id, _ = obj["id"].(string)
		if attrs := appleMusicAttributes(obj); attrs != nil {
			created.url, _ = attrs["url"].(string)
		}
		break
	}
	if created.id == "" {
		return createdPlaylist{}, errAppleMusicCreate(status)
	}
	return created, nil
}

// addPlaylistTracks appends tracks to a library playlist.
func (c *AppleMusicConnector) addPlaylistTracks(ctx context.Context, playlistID string, tracks []appleMusicPlacement) error {
	body := map[string]any{
		"relationships": map[string]any{
			"tracks": map[string]any{"data": appleMusicTrackRefs(tracks)},
		},
	}
	_, _, err := c.apiJSON(ctx, "POST", "/me/library/playlists/"+url.PathEscape(playlistID)+"/tracks", nil, body)
	return err
}

// renamePlaylist sets a library playlist's title (create-only elsewhere; a
// rename is an explicit title write).
func (c *AppleMusicConnector) renamePlaylist(ctx context.Context, playlistID, name string) error {
	body := map[string]any{
		"attributes": map[string]any{"name": name},
	}
	_, _, err := c.apiJSON(ctx, "PATCH", "/me/library/playlists/"+url.PathEscape(playlistID), nil, body)
	return err
}

// addToLibrary adds catalog resources to the user's library
// ("Add a Resource to a Library").
func (c *AppleMusicConnector) addToLibrary(ctx context.Context, kind string, ids []string) error {
	q := url.Values{"ids[" + kind + "]": []string{strings.Join(ids, ",")}}
	_, _, err := c.apiJSON(ctx, "POST", "/me/library", q, nil)
	return err
}

// appleMusicLibrarySongPrefix marks a MusicKit library-song id ("a." + the
// song id). A source id carrying it is a library resource, so playlist
// writes send it as a library-songs ref rather than a catalog song.
const appleMusicLibrarySongPrefix = "a."

// appleMusicTrackRefs maps placements onto the resource-identifier shapes
// the playlist write takes: library songs keep their library id, everything
// else is a catalog song id.
func appleMusicTrackRefs(tracks []appleMusicPlacement) []map[string]string {
	refs := make([]map[string]string, 0, len(tracks))
	for _, t := range tracks {
		switch {
		case t.libraryID != "" && strings.HasPrefix(t.libraryID, appleMusicLibrarySongPrefix):
			refs = append(refs, map[string]string{"id": t.libraryID, "type": "library-songs"})
		case t.catalog != "":
			refs = append(refs, map[string]string{"id": t.catalog, "type": "songs"})
		case t.libraryID != "":
			refs = append(refs, map[string]string{"id": t.libraryID, "type": "songs"})
		}
	}
	return refs
}

// firstResource returns the first data[] object of a MusicKit response.
func firstResource(raw map[string]any) map[string]any {
	for _, item := range appleMusicData(raw) {
		if obj, ok := item.(map[string]any); ok {
			return obj
		}
	}
	return nil
}

// errAppleMusicCursor reports a malformed page cursor.
func errAppleMusicCursor(cursor string) error {
	return fmt.Errorf("bad apple music cursor %q", cursor)
}

// errAppleMusicCreate reports a create that returned no resource.
func errAppleMusicCreate(status int) error {
	return fmt.Errorf("apple music playlist create returned no resource (status %d)", status)
}
