package connectors

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"github.com/fudomunro/aegis/internal/types"
)

// YouTube Music reconciliation operations (issue #254): paged list reads
// and placement writes over neutral models against YouTube Data API v3.

// youtubeLikedPlaylistID is the auto-generated liked-videos playlist the
// liked-tracks placement reads and writes.
const youtubeLikedPlaylistID = "LL"

// ListPlaylistItems implements types.PlaylistItemsProvider. One page of a
// YouTube playlist's items. The cursor is YouTube's opaque nextPageToken;
// only the first page also fetches the playlist object for the ref name and
// total item count.
func (c *YouTubeMusicConnector) ListPlaylistItems(ctx context.Context, playlistID, cursor string, limit int) (types.Playlist, types.TrackPage, error) {
	ref := types.Playlist{Sources: []types.PlaylistSource{{
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
			return ref, types.TrackPage{}, err
		}
		items, _ := raw["items"].([]any)
		if len(items) == 0 {
			return ref, types.TrackPage{}, fmt.Errorf("playlist %q not found on youtube-music", playlistID)
		}
		pl, _ := items[0].(map[string]any)
		snippet, _ := pl["snippet"].(map[string]any)
		ref.Name, _ = snippet["title"].(string)
		details, _ := pl["contentDetails"].(map[string]any)
		total, _ = AsInt(details["itemCount"])
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
		return ref, types.TrackPage{}, err
	}
	tracks := youtubeItemsToTracks(raw)
	if total == 0 {
		total = len(tracks)
	}
	ref.TrackCount = total
	next, _ := raw["nextPageToken"].(string)
	return ref, types.TrackPage{Tracks: tracks, Total: total, Next: next}, nil
}

// ListUserLikedTracksPage implements types.LikedTrackPageProvider by paging
// the liked-videos playlist.
func (c *YouTubeMusicConnector) ListUserLikedTracksPage(ctx context.Context, cursor string, limit int) (types.TrackPage, error) {
	_, page, err := c.ListPlaylistItems(ctx, youtubeLikedPlaylistID, cursor, limit)
	return page, err
}

// youtubeItemsToTracks flattens a playlistItems page into neutral tracks.
func youtubeItemsToTracks(raw map[string]any) []types.Track {
	var out []types.Track
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
		artists := []types.Artist{}
		if videoOwnerChannelTitle != "" {
			artists = []types.Artist{types.NewArtist(YouTubeMusicServiceID, YouTubeMusicServiceName, "", videoOwnerChannelTitle)}
		}
		out = append(out, types.Track{
			Name:    title,
			Artists: artists,
			Sources: []types.TrackSource{{Service: YouTubeMusicServiceID, ID: videoID}},
		})
	}
	return out
}

// PlacePlaylistItems implements types.PlaylistItemWriteProvider. Creates
// the target playlist when playlistID is empty, then resolves and appends
// items one by one. An empty playlistID is the single-call path; a known
// playlistID continues a chained job.
func (c *YouTubeMusicConnector) PlacePlaylistItems(ctx context.Context, playlistID, name string, tracks []types.Track) (types.PlacePlaylistItemsResult, error) {
	var result types.PlacePlaylistItemsResult
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

// SaveLikedTracks implements types.LikedTrackWriteProvider by rating the
// resolved videos "like".
func (c *YouTubeMusicConnector) SaveLikedTracks(ctx context.Context, tracks []types.Track) (types.SaveLikedTracksResult, error) {
	var result types.SaveLikedTracksResult
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
func (c *YouTubeMusicConnector) resolveVideoID(ctx context.Context, t types.Track, skips *[]types.PlaceSkip) string {
	id, _ := placeTrackLocalRef(t, YouTubeMusicServiceID)
	if id == "" {
		found, err := c.searchVideoID(ctx, t)
		if err != nil {
			*skips = append(*skips, types.PlaceSkip{Name: placeTrackDisplay(t), Reason: err.Error()})
			return ""
		}
		id = found
	}
	if id == "" {
		*skips = append(*skips, types.PlaceSkip{Name: placeTrackDisplay(t), Reason: "no match on YouTube Music"})
		return ""
	}
	return id
}

func (c *YouTubeMusicConnector) searchVideoID(ctx context.Context, t types.Track) (string, error) {
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
