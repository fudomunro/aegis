package connectors

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

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
// playlistID continues a chained job.
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
	}
	result.PlaylistID = playlistID

	for _, t := range tracks {
		videoID := c.resolveVideoID(ctx, t, &result.Skips)
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
	}
	return result, nil
}

// SaveLikedTracks implements kinds.LikedTrackWriteProvider by rating the
// resolved videos "like".
func (c *YouTubeMusicConnector) SaveLikedTracks(ctx context.Context, tracks []kinds.Track) (kinds.SaveLikedTracksResult, error) {
	var result kinds.SaveLikedTracksResult
	for _, t := range tracks {
		videoID := c.resolveVideoID(ctx, t, &result.Skips)
		if videoID == "" {
			continue
		}
		_, _, err := apiJSON(ctx, c.host, YouTubeMusicServiceID, "POST", "/videos/rate",
			url.Values{"id": []string{videoID}, "rating": []string{"like"}}, nil)
		if err != nil {
			return result, err
		}
		result.Copied++
	}
	return result, nil
}

// resolveVideoID maps one track to a YouTube video ID, appending a skip
// when it cannot be resolved.
func (c *YouTubeMusicConnector) resolveVideoID(ctx context.Context, t kinds.Track, skips *[]kinds.PlaceSkip) string {
	id, _ := placeTrackLocalRef(t, YouTubeMusicServiceID)
	if id == "" {
		found, err := c.searchVideoID(ctx, t)
		if err != nil {
			*skips = append(*skips, kinds.PlaceSkip{Name: placeTrackDisplay(t), Reason: err.Error()})
			return ""
		}
		id = found
	}
	if id == "" {
		*skips = append(*skips, kinds.PlaceSkip{Name: placeTrackDisplay(t), Reason: "no match on YouTube Music"})
		return ""
	}
	return id
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
	id, _ := first["id"].(map[string]any)
	videoID, _ := id["videoId"].(string)
	return videoID, nil
}
