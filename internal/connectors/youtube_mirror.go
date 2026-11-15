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

// YouTube Music reconciliation operations (issue #254): paged list reads,
// metadata probes for freshness checks, and placement writes over neutral
// models against YouTube Data API v3.

// youtubeLikedPlaylistID is the auto-generated liked-videos playlist the
// liked-tracks placement and probe read and write.
const youtubeLikedPlaylistID = "LL"

// GetPlaylistMeta implements kinds.PlaylistMetaProvider in one metadata
// call: title and item count (YouTube exposes no change token).
func (c *YouTubeMusicConnector) GetPlaylistMeta(ctx context.Context, playlistID string) (kinds.PlaylistMeta, bool, error) {
	raw, status, err := apiJSON(ctx, c.host, YouTubeMusicServiceID, "GET", "/playlists",
		url.Values{"part": []string{"snippet,contentDetails"}, "id": []string{playlistID}}, nil)
	if err != nil {
		if status == http.StatusNotFound {
			return kinds.PlaylistMeta{}, false, nil
		}
		return kinds.PlaylistMeta{}, false, err
	}
	items, _ := raw["items"].([]any)
	if len(items) == 0 {
		return kinds.PlaylistMeta{}, false, nil
	}
	pl, _ := items[0].(map[string]any)
	snippet, _ := pl["snippet"].(map[string]any)
	title, _ := snippet["title"].(string)
	details, _ := pl["contentDetails"].(map[string]any)
	count, _ := shared.AsInt(details["itemCount"])
	return kinds.PlaylistMeta{Name: title, ItemCount: count}, true, nil
}

// SavedTrackCount implements kinds.PlaylistMetaProvider via the liked-videos
// playlist — the mirror target for saved tracks.
func (c *YouTubeMusicConnector) SavedTrackCount(ctx context.Context) (int, error) {
	raw, _, err := apiJSON(ctx, c.host, YouTubeMusicServiceID, "GET", "/playlists",
		url.Values{"part": []string{"contentDetails"}, "id": []string{youtubeLikedPlaylistID}}, nil)
	if err != nil {
		return 0, err
	}
	items, _ := raw["items"].([]any)
	if len(items) == 0 {
		return 0, nil
	}
	pl, _ := items[0].(map[string]any)
	details, _ := pl["contentDetails"].(map[string]any)
	count, _ := shared.AsInt(details["itemCount"])
	return count, nil
}

// ListPlaylistItems implements kinds.PlaylistItemsProvider. One page of a
// YouTube playlist's items. The cursor is YouTube's opaque nextPageToken;
// only the first page also fetches the playlist object for the ref name and
// total item count.
func (c *YouTubeMusicConnector) ListPlaylistItems(ctx context.Context, playlistID, cursor string, limit int) (kinds.Playlist, kinds.TrackPage, error) {
	ref := kinds.Playlist{Sources: []kinds.PlaylistSource{{
		Service: YouTubeMusicServiceID,
		ID:      playlistID,
		URL:     "https://www.youtube.com/playlist?list=" + playlistID,
	}}}
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	total := 0
	if cursor == "" {
		raw, _, err := apiJSON(ctx, c.host, YouTubeMusicServiceID, "GET", "/playlists",
			url.Values{"part": []string{"snippet,contentDetails"}, "id": []string{playlistID}}, nil)
		if err != nil {
			return ref, kinds.TrackPage{}, err
		}
		items, _ := raw["items"].([]any)
		if len(items) == 0 {
			return ref, kinds.TrackPage{}, fmt.Errorf("playlist %q not found on youtube-music", playlistID)
		}
		pl, _ := items[0].(map[string]any)
		snippet, _ := pl["snippet"].(map[string]any)
		ref.Name, _ = snippet["title"].(string)
		details, _ := pl["contentDetails"].(map[string]any)
		total, _ = shared.AsInt(details["itemCount"])
	}
	q := url.Values{
		"part":       []string{"snippet,contentDetails"},
		"playlistId": []string{playlistID},
		"maxResults": []string{strconv.Itoa(limit)},
	}
	if cursor != "" {
		q.Set("pageToken", cursor)
	}
	raw, _, err := apiJSON(ctx, c.host, YouTubeMusicServiceID, "GET", "/playlistItems", q, nil)
	if err != nil {
		return ref, kinds.TrackPage{}, err
	}
	tracks := youtubeItemsToTracks(raw)
	if total == 0 {
		total = len(tracks)
	}
	ref.TrackCount = total
	next, _ := raw["nextPageToken"].(string)
	return ref, kinds.TrackPage{Tracks: tracks, Total: total, Next: next}, nil
}

// ListUserLikedTracksPage implements kinds.LikedTrackPageProvider by paging
// the liked-videos playlist.
func (c *YouTubeMusicConnector) ListUserLikedTracksPage(ctx context.Context, cursor string, limit int) (kinds.TrackPage, error) {
	_, page, err := c.ListPlaylistItems(ctx, youtubeLikedPlaylistID, cursor, limit)
	return page, err
}

// youtubeItemsToTracks flattens a playlistItems page into neutral tracks.
func youtubeItemsToTracks(raw map[string]any) []kinds.Track {
	var out []kinds.Track
	items, _ := raw["items"].([]any)
	for _, item := range items {
		e, ok := item.(map[string]any)
		if !ok {
			continue
		}
		snippet, _ := e["snippet"].(map[string]any)
		if snippet == nil {
			continue
		}
		title, _ := snippet["title"].(string)
		videoOwnerChannelTitle, _ := snippet["videoOwnerChannelTitle"].(string)
		res, _ := snippet["resourceId"].(map[string]any)
		videoID, _ := res["videoId"].(string)
		if videoID == "" {
			continue
		}
		artists := []kinds.Artist{}
		if videoOwnerChannelTitle != "" {
			artists = []kinds.Artist{kinds.NewArtist(YouTubeMusicServiceID, YouTubeMusicServiceName, "", videoOwnerChannelTitle)}
		}
		out = append(out, kinds.Track{
			Name:    title,
			Artists: artists,
			Sources: []kinds.TrackSource{{Service: YouTubeMusicServiceID, ID: videoID}},
		})
	}
	return out
}

// PlacePlaylistItems implements kinds.PlaylistItemWriteProvider. Creates
// the target playlist when playlistID is empty, then resolves and appends
// items one by one. An empty playlistID is the single-call path; a known
// playlistID continues a chained job and, with a non-empty name, sets the
// playlist's title so renames propagate to the copy (issue #365).
func (c *YouTubeMusicConnector) PlacePlaylistItems(ctx context.Context, playlistID, name string, tracks []kinds.Track) (kinds.PlacePlaylistItemsResult, error) {
	var result kinds.PlacePlaylistItemsResult
	if playlistID == "" {
		created, _, err := apiJSON(ctx, c.host, YouTubeMusicServiceID, "POST", "/playlists",
			url.Values{"part": []string{"snippet,status"}},
			map[string]any{
				"snippet": map[string]any{
					"title":       name,
					"description": "Copied by Aegis",
				},
				"status": map[string]any{"privacyStatus": "private"},
			})
		if err != nil {
			return result, err
		}
		playlistID, _ = created["id"].(string)
		if playlistID == "" {
			return result, fmt.Errorf("youtube create playlist returned no id")
		}
		result.URL = "https://www.youtube.com/playlist?list=" + playlistID
	} else if strings.TrimSpace(name) != "" {
		// Title write on the append path (issue #365): a rename on the
		// source renames the converged copy.
		if err := c.renamePlaylist(ctx, playlistID, name); err != nil {
			return result, err
		}
	}
	result.PlaylistID = playlistID

	for _, t := range tracks {
		ref, ok := c.resolveVideo(ctx, t, &result.Skips)
		if !ok {
			continue
		}
		_, _, err := apiJSON(ctx, c.host, YouTubeMusicServiceID, "POST", "/playlistItems",
			url.Values{"part": []string{"snippet"}},
			map[string]any{
				"snippet": map[string]any{
					"playlistId": playlistID,
					"resourceId": map[string]any{
						"kind":    "youtube#video",
						"videoId": ref.ID,
					},
				},
			})
		if err != nil {
			return result, err
		}
		result.Copied++
		result.Placed = append(result.Placed, ref.Record)
	}
	return result, nil
}

// renamePlaylist sets a YouTube playlist's title. The snippet PUT is a
// full replace and requires categoryId, so the current snippet is fetched
// and carried over; an already-matching title skips the write.
func (c *YouTubeMusicConnector) renamePlaylist(ctx context.Context, playlistID, name string) error {
	raw, _, err := apiJSON(ctx, c.host, YouTubeMusicServiceID, "GET", "/playlists",
		url.Values{"part": []string{"snippet"}, "id": []string{playlistID}}, nil)
	if err != nil {
		return err
	}
	items, _ := raw["items"].([]any)
	if len(items) == 0 {
		return fmt.Errorf("youtube playlist %q not found", playlistID)
	}
	pl, _ := items[0].(map[string]any)
	snippet := map[string]any{"title": name}
	if s, ok := pl["snippet"].(map[string]any); ok {
		if title, _ := s["title"].(string); title == name {
			return nil
		}
		if cat, ok := s["categoryId"]; ok {
			snippet["categoryId"] = cat
		}
		if desc, ok := s["description"]; ok {
			snippet["description"] = desc
		}
	}
	if _, ok := snippet["categoryId"]; !ok {
		snippet["categoryId"] = "0"
	}
	_, _, err = apiJSON(ctx, c.host, YouTubeMusicServiceID, "PUT", "/playlists",
		url.Values{"part": []string{"snippet"}},
		map[string]any{"id": playlistID, "snippet": snippet})
	return err
}

// youtubePlaylistEntry is one playlistItems.list entry: the playlist-item
// id a delete names and the video id the order works in.
type youtubePlaylistEntry struct {
	itemID  string
	videoID string
}

// SetPlaylistOrder implements kinds.PlaylistOrderProvider (issue #378).
// YouTube exposes no move operation, so each move reinserts the video at
// its target position and then removes the old entry: the playlist never
// holds fewer items than before (a transient duplicate is recoverable —
// a transient loss is not). The desired ids are matched onto the
// playlist's own entries in listing order; unknown ids are ignored and
// unlisted entries keep their relative order at the end.
func (c *YouTubeMusicConnector) SetPlaylistOrder(ctx context.Context, playlistID string, orderedIDs []string) error {
	entries, err := youtubePlaylistEntries(ctx, c, playlistID)
	if err != nil {
		return err
	}
	ids := make([]string, len(entries))
	for i, e := range entries {
		ids[i] = e.videoID
	}
	target := orderEntryIndexes(orderedIDs, ids)
	// Insertion sort on the working order: every move pulls an entry from
	// the right of its target slot, reinserts the video at the slot, and
	// removes the old entry — in that order, so the video is never absent.
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
		if _, _, err := apiJSON(ctx, c.host, YouTubeMusicServiceID, "POST", "/playlistItems",
			url.Values{"part": []string{"snippet"}},
			map[string]any{"snippet": map[string]any{
				"playlistId": playlistID,
				"resourceId": map[string]any{"kind": "youtube#video", "videoId": entries[want].videoID},
				"position":   i,
			}}); err != nil {
			return err
		}
		if _, _, err := apiJSON(ctx, c.host, YouTubeMusicServiceID, "DELETE", "/playlistItems",
			url.Values{"id": []string{entries[want].itemID}}, nil); err != nil {
			return err
		}
		work = append(work[:j], work[j+1:]...)
		work = append(work[:i], append([]int{want}, work[i:]...)...)
	}
	return nil
}

// youtubePlaylistEntries pages a playlist's entries in order — the move
// positions SetPlaylistOrder computes against. An entry with no video id
// cannot be reinserted (and a reorder is refused rather than risked:
// order fidelity never outranks the never-delete rule).
func youtubePlaylistEntries(ctx context.Context, c *YouTubeMusicConnector, playlistID string) ([]youtubePlaylistEntry, error) {
	var out []youtubePlaylistEntry
	cursor := ""
	for i := 0; i < shared.LibraryPageCap; i++ {
		q := url.Values{
			"part":       []string{"snippet"},
			"playlistId": []string{playlistID},
			"maxResults": []string{"50"},
		}
		if cursor != "" {
			q.Set("pageToken", cursor)
		}
		raw, _, err := apiJSON(ctx, c.host, YouTubeMusicServiceID, "GET", "/playlistItems", q, nil)
		if err != nil {
			return nil, err
		}
		items, _ := raw["items"].([]any)
		for _, item := range items {
			e, ok := item.(map[string]any)
			if !ok {
				continue
			}
			itemID, _ := e["id"].(string)
			snippet, _ := e["snippet"].(map[string]any)
			res, _ := snippet["resourceId"].(map[string]any)
			videoID, _ := res["videoId"].(string)
			if itemID == "" || videoID == "" {
				return nil, fmt.Errorf("youtube playlist %q holds an item that cannot be reordered", playlistID)
			}
			out = append(out, youtubePlaylistEntry{itemID: itemID, videoID: videoID})
		}
		next, _ := raw["nextPageToken"].(string)
		if next == "" {
			return out, nil
		}
		cursor = next
	}
	return out, nil
}

// SaveLikedTracks implements kinds.LikedTrackWriteProvider by rating the
// resolved videos "like".
// videoLadder is YouTube Music's track catalog search behind the shared
// match ladder: name search only (YouTube exposes no ISRC in search and no
// durations), guarded by the cleaned title so a cover or remix under a
// different title is not placed (issue #366).
func (c *YouTubeMusicConnector) videoLadder() SearchLadder[kinds.Track] {
	return SearchLadder[kinds.Track]{
		Service:     YouTubeMusicServiceName,
		Display:     placeTrackDisplay,
		IdentityKey: kinds.TrackIdentityKey,
		DurableID:   func(kinds.Track) (string, string) { return "", "" },
		ByName:      c.searchVideoID,
	}
}

// channelLadder is YouTube Music's artist catalog search behind the shared
// match ladder: name search guarded by normalized-name equality (the
// channel title may carry the "- topic" unit, which the normalizer strips).
func (c *YouTubeMusicConnector) channelLadder() SearchLadder[kinds.Artist] {
	return SearchLadder[kinds.Artist]{
		Service:     YouTubeMusicServiceName,
		Display:     placeArtistDisplay,
		IdentityKey: kinds.ArtistIdentityKey,
		DurableID:   func(kinds.Artist) (string, string) { return "", "" },
		ByName:      c.searchChannelID,
	}
}

// resolveVideo maps one track to a placed video: service-local reference
// first, else the video ladder.
func (c *YouTubeMusicConnector) resolveVideo(ctx context.Context, t kinds.Track, skips *[]kinds.PlaceSkip) (LadderRef, bool) {
	if id, _ := placeTrackLocalRef(t, YouTubeMusicServiceID); id != "" {
		return c.videoLadder().Ref(t, id), true
	}
	return c.videoLadder().Resolve(ctx, t, skips)
}

// resolveChannel maps one artist to a placed channel: service-local
// reference first, else the channel ladder.
func (c *YouTubeMusicConnector) resolveChannel(ctx context.Context, a kinds.Artist, skips *[]kinds.PlaceSkip) (LadderRef, bool) {
	if id := placeArtistLocalRef(a, YouTubeMusicServiceID); id != "" {
		return c.channelLadder().Ref(a, id), true
	}
	return c.channelLadder().Resolve(ctx, a, skips)
}

// SaveLikedTracks implements kinds.LikedTrackWriteProvider: rate the
// resolved videos — YouTube's like is the saved-track surface. Saving is
// create-only — a placement never un-saves (issue #375).
func (c *YouTubeMusicConnector) SaveLikedTracks(ctx context.Context, tracks []kinds.Track) (kinds.SaveLikedTracksResult, error) {
	copied, placed, skips, err := SaveOnLadder(ctx, tracks, c.resolveVideo,
		func(ctx context.Context, ids []string) (int, error) {
			written := 0
			for _, id := range ids {
				if _, _, err := apiJSON(ctx, c.host, YouTubeMusicServiceID, "POST", "/videos/rate",
					url.Values{"id": []string{id}, "rating": []string{"like"}}, nil); err != nil {
					return written, err
				}
				written++
			}
			return written, nil
		})
	return kinds.SaveLikedTracksResult{Copied: copied, Placed: placed, Skips: skips}, err
}

// FollowArtists implements kinds.ArtistWriteProvider by subscribing to the
// resolved channels — YouTube's follow (issue #375). Following is
// create-only — a placement never unsubscribes. YouTube exposes no
// saved-albums library on this API surface, so albums are not placed here.
func (c *YouTubeMusicConnector) FollowArtists(ctx context.Context, artists []kinds.Artist) (kinds.FollowArtistsResult, error) {
	copied, placed, skips, err := SaveOnLadder(ctx, artists, c.resolveChannel,
		func(ctx context.Context, ids []string) (int, error) {
			written := 0
			for _, id := range ids {
				if _, _, err := apiJSON(ctx, c.host, YouTubeMusicServiceID, "POST", "/subscriptions",
					url.Values{"part": []string{"snippet"}},
					map[string]any{
						"snippet": map[string]any{
							"resourceId": map[string]any{
								"kind":      "youtube#channel",
								"channelId": id,
							},
						},
					}); err != nil {
					return written, err
				}
				written++
			}
			return written, nil
		})
	return kinds.FollowArtistsResult{Copied: copied, Placed: placed, Skips: skips}, err
}

func (c *YouTubeMusicConnector) searchVideoID(ctx context.Context, t kinds.Track) (string, error) {
	q := url.Values{
		"part":            []string{"snippet"},
		"q":               []string{placeTrackQuery(t)},
		"type":            []string{"video"},
		"maxResults":      []string{"1"},
		"videoCategoryId": []string{"10"}, // Music
	}
	raw, _, err := apiJSON(ctx, c.host, YouTubeMusicServiceID, "GET", "/search", q, nil)
	if err != nil {
		return "", err
	}
	items, _ := raw["items"].([]any)
	if len(items) == 0 {
		return "", nil
	}
	first, ok := items[0].(map[string]any)
	if !ok {
		return "", nil
	}
	// Title guard (issue #366): the name fallback may not land on a cover
	// or remix that spells the title differently.
	snippet, _ := first["snippet"].(map[string]any)
	hitTitle, _ := snippet["title"].(string)
	if kinds.NormalizeIdentityKey(kinds.CleanYouTubeTitle(hitTitle)) != kinds.NormalizeIdentityKey(t.Name) {
		return "", nil
	}
	id, _ := first["id"].(map[string]any)
	videoID, _ := id["videoId"].(string)
	return videoID, nil
}

func (c *YouTubeMusicConnector) searchChannelID(ctx context.Context, a kinds.Artist) (string, error) {
	q := url.Values{
		"part":       []string{"snippet"},
		"q":          []string{placeArtistDisplay(a)},
		"type":       []string{"channel"},
		"maxResults": []string{"1"},
	}
	raw, _, err := apiJSON(ctx, c.host, YouTubeMusicServiceID, "GET", "/search", q, nil)
	if err != nil {
		return "", err
	}
	items, _ := raw["items"].([]any)
	if len(items) == 0 {
		return "", nil
	}
	first, ok := items[0].(map[string]any)
	if !ok {
		return "", nil
	}
	snippet, _ := first["snippet"].(map[string]any)
	hitTitle, _ := snippet["channelTitle"].(string)
	if !artistNameCompatible(kinds.NewArtist(YouTubeMusicServiceID, YouTubeMusicServiceName, "", hitTitle), a) {
		return "", nil
	}
	id, _ := first["id"].(map[string]any)
	channelID, _ := id["channelId"].(string)
	return channelID, nil
}
