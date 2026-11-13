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
		videoID, how := c.resolveVideoID(ctx, t, &result.Skips)
		if videoID == "" {
			continue
		}
		_, _, err := apiJSON(ctx, c.host, YouTubeMusicServiceID, "POST", "/playlistItems",
			url.Values{"part": []string{"snippet"}},
			map[string]any{
				"snippet": map[string]any{
					"playlistId": playlistID,
					"resourceId": map[string]any{
						"kind":    "youtube#video",
						"videoId": videoID,
					},
				},
			})
		if err != nil {
			return result, err
		}
		result.Copied++
		result.Placed = append(result.Placed, kinds.PlaceRecord{
			Name:       placeTrackDisplay(t),
			ExternalID: videoID,
			MatchedBy:  how,
		})
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
func (c *YouTubeMusicConnector) SaveLikedTracks(ctx context.Context, tracks []kinds.Track) (kinds.SaveLikedTracksResult, error) {
	var result kinds.SaveLikedTracksResult
	for _, t := range tracks {
		videoID, how := c.resolveVideoID(ctx, t, &result.Skips)
		if videoID == "" {
			continue
		}
		_, _, err := apiJSON(ctx, c.host, YouTubeMusicServiceID, "POST", "/videos/rate",
			url.Values{"id": []string{videoID}, "rating": []string{"like"}}, nil)
		if err != nil {
			return result, err
		}
		result.Copied++
		result.Placed = append(result.Placed, kinds.PlaceRecord{
			Name:       placeTrackDisplay(t),
			ExternalID: videoID,
			MatchedBy:  how,
		})
	}
	return result, nil
}

// resolveVideoID maps one track to a YouTube video ID and the identity it
// matched on (issue #366): service-local reference first, then name search
// guarded by the title key. YouTube exposes no ISRC and no durations in
// search, so the name fallback is title-checked rather than
// duration-guarded — a cover or remix under a different title is not
// placed.
func (c *YouTubeMusicConnector) resolveVideoID(ctx context.Context, t kinds.Track, skips *[]kinds.PlaceSkip) (string, string) {
	id, _ := placeTrackLocalRef(t, YouTubeMusicServiceID)
	how := "ref"
	if id == "" {
		found, err := c.searchVideoID(ctx, t)
		if err != nil {
			*skips = append(*skips, kinds.PlaceSkip{Name: placeTrackDisplay(t), Reason: err.Error()})
			return "", ""
		}
		id, how = found, kinds.TrackMatchName
	}
	if id == "" {
		*skips = append(*skips, kinds.PlaceSkip{Name: placeTrackDisplay(t), Reason: "no match on YouTube Music"})
		return "", ""
	}
	return id, how
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

// FollowArtists implements kinds.ArtistWriteProvider by subscribing to the
// resolved channels — YouTube's follow (issue #375). Following is
// create-only — a placement never unsubscribes. YouTube exposes no
// saved-albums library on this API surface, so albums are not placed here.
func (c *YouTubeMusicConnector) FollowArtists(ctx context.Context, artists []kinds.Artist) (kinds.FollowArtistsResult, error) {
	var result kinds.FollowArtistsResult
	for _, a := range artists {
		channelID, how := c.resolveChannelID(ctx, a, &result.Skips)
		if channelID == "" {
			continue
		}
		_, _, err := apiJSON(ctx, c.host, YouTubeMusicServiceID, "POST", "/subscriptions",
			url.Values{"part": []string{"snippet"}},
			map[string]any{
				"snippet": map[string]any{
					"resourceId": map[string]any{
						"kind":      "youtube#channel",
						"channelId": channelID,
					},
				},
			})
		if err != nil {
			return result, err
		}
		result.Copied++
		result.Placed = append(result.Placed, kinds.PlaceRecord{
			Name:       placeArtistDisplay(a),
			ExternalID: channelID,
			MatchedBy:  how,
		})
	}
	return result, nil
}

// resolveChannelID maps one artist to a YouTube channel ID and the identity
// it matched on (issue #375): service-local reference first, then name
// search guarded by normalized-name equality (the channel title may carry
// the "- topic" unit, which the normalizer strips).
func (c *YouTubeMusicConnector) resolveChannelID(ctx context.Context, a kinds.Artist, skips *[]kinds.PlaceSkip) (string, string) {
	id := placeArtistLocalRef(a, YouTubeMusicServiceID)
	how := "ref"
	if id == "" {
		found, err := c.searchChannelID(ctx, a)
		if err != nil {
			*skips = append(*skips, kinds.PlaceSkip{Name: placeArtistDisplay(a), Reason: err.Error()})
			return "", ""
		}
		id, how = found, kinds.TrackMatchName
	}
	if id == "" {
		*skips = append(*skips, kinds.PlaceSkip{Name: placeArtistDisplay(a), Reason: "no match on YouTube Music"})
		return "", ""
	}
	return id, how
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
