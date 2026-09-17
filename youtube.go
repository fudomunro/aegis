package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// YouTubeMusicConnector provides intelligent proxying for the YouTube Data API v3
// focused on music-related content. It transforms verbose YouTube responses into
// LLM-friendly formats and generates rich system instructions for agents.
type YouTubeMusicConnector struct {
	server *Server
}

// NewYouTubeMusicConnector creates a connector wired to the given server.
func NewYouTubeMusicConnector(srv *Server) *YouTubeMusicConnector {
	return &YouTubeMusicConnector{server: srv}
}

// Capabilities returns what YouTube Music data types and operations are supported.
func (c *YouTubeMusicConnector) Capabilities() ServiceCapabilities {
	return ServiceCapabilities{
		DataTypes: []DataTypeCapability{
			{
				Name:        "user",
				Description: "Current user's YouTube channel",
				Operations: []Operation{
					{Name: "read", Description: "Get current user's channel info", Method: "GET", Path: "/me",
						Response: ResponseSpec{Description: "Channel with id, title, playlists", Fields: []string{"id", "title", "description", "playlists"}}},
				},
			},
			{
				Name:        "tracks",
				Description: "Music videos and tracks — liked, search results",
				Operations: []Operation{
					{Name: "read_liked", Description: "List liked YouTube videos as neutral tracks (paginated; music-only filtering lives on /v1/library/tracks)", Method: "GET", Path: "/me/liked",
						Parameters: []Parameter{
							{Name: "pageToken", In: "query", Type: "string", Description: "Page token for pagination"},
						},
						Response: ResponseSpec{Description: "Neutral tracks from liked videos (unfiltered playlist)", Fields: []string{"tracks", "total"}}},
					{Name: "get_by_id", Description: "Get video details by ID", Method: "GET", Path: "/videos/{id}",
						Parameters: []Parameter{
							{Name: "id", In: "path", Type: "string", Description: "YouTube video ID", Required: true},
						},
						Response: ResponseSpec{Description: "Video with title, channel, duration, stats", Fields: []string{"id", "title", "channel", "duration", "view_count", "like_count"}}},
				},
			},
			{
				Name:        "playlists",
				Description: "User playlists — list, detail with items",
				Operations: []Operation{
					{Name: "read_list", Description: "List user's playlists (paginated)", Method: "GET", Path: "/me/playlists",
						Parameters: []Parameter{
							{Name: "pageToken", In: "query", Type: "string", Description: "Page token for pagination"},
						},
						Response: ResponseSpec{Description: "Neutral playlist list: name, description, owner, track_count, sources[] (service, service_name, id, url, extra)", Fields: []string{"playlists", "total"}}},
					{Name: "get_by_id", Description: "Get a playlist by ID", Method: "GET", Path: "/playlists/{id}",
						Parameters: []Parameter{
							{Name: "id", In: "path", Type: "string", Description: "YouTube playlist ID", Required: true},
						},
						Response: ResponseSpec{Description: "Neutral playlist detail: name, description, owner, track_count, sources[]", Fields: []string{"name", "description", "owner", "track_count", "sources", "extra"}}},
					{Name: "read_items", Description: "List items in a playlist (paginated)", Method: "GET", Path: "/playlists/{id}/items",
						Parameters: []Parameter{
							{Name: "id", In: "path", Type: "string", Description: "YouTube playlist ID", Required: true},
							{Name: "pageToken", In: "query", Type: "string", Description: "Page token for pagination"},
						},
						Response: ResponseSpec{Description: "Neutral tracks: name, artists[], album, sources[]", Fields: []string{"tracks", "total"}}},
				},
			},
			{
				Name:        "artists",
				Description: "Followed artists / subscribed channels (feeds /v1/library/artists)",
				Operations: []Operation{
					{Name: "read_followed", Description: "List followed artists via channel subscriptions (paginated). Permission key: artists.", Method: "GET", Path: "/me/subscriptions",
						Parameters: []Parameter{
							{Name: "pageToken", In: "query", Type: "string", Description: "Page token for pagination"},
						},
						Response: ResponseSpec{Description: "Subscribed channels with title, channel_id", Fields: []string{"subscriptions", "total"}}},
				},
			},
			{
				Name:        "channels",
				Description: "YouTube channels — lookup by id",
				Operations: []Operation{
					{Name: "get_by_id", Description: "Get channel details by ID", Method: "GET", Path: "/channels/{id}",
						Parameters: []Parameter{
							{Name: "id", In: "path", Type: "string", Description: "YouTube channel ID", Required: true},
						},
						Response: ResponseSpec{Description: "Channel with title, description, stats", Fields: []string{"id", "title", "description"}}},
				},
			},
			{
				Name:        "search",
				Description: "Search YouTube Music catalog",
				Operations: []Operation{
					{Name: "search", Description: "Search YouTube Music (defaults to music videos)", Method: "GET", Path: "/search",
						Parameters: []Parameter{
							{Name: "q", In: "query", Type: "string", Description: "Search query", Required: true},
							{Name: "type", In: "query", Type: "string", Description: "Resource type: video, playlist, channel", Default: "video"},
							{Name: "pageToken", In: "query", Type: "string", Description: "Page token for pagination"},
						},
						Response: ResponseSpec{Description: "Search results with title, channel, type", Fields: []string{"results", "total"}}},
				},
			},
			{
				Name:        "library",
				Description: "User's liked videos and subscriptions",
				Operations: []Operation{
					{Name: "read_liked", Description: "List liked videos (alias for tracks.read_liked)", Method: "GET", Path: "/me/liked",
						Response: ResponseSpec{Description: "Liked videos", Fields: []string{"tracks", "total"}}},
					{Name: "read_followed", Description: "List subscriptions (alias for artists.read_followed)", Method: "GET", Path: "/me/subscriptions",
						Response: ResponseSpec{Description: "Subscriptions", Fields: []string{"subscriptions", "total"}}},
				},
			},
		},
	}
}

// SystemInstructions returns the system message injected into LLM requests
// when YouTube Music is referenced.
func (c *YouTubeMusicConnector) SystemInstructions(baseURL string) string {
	root := strings.TrimRight(baseURL, "/")
	base := root + "/v1/services/youtube-music"
	return fmt.Sprintf(`When the user's request involves YouTube Music, you MUST use the Guardian proxy at %s instead of calling YouTube directly.

For "my playlists" across every connected music service (Spotify, YouTube Music, Amazon Music, …), prefer the unified library endpoint first:
GET %s/v1/library/playlists — all user playlists in one neutral list (synced across services; service IDs live in sources[])

For "liked songs" / liked music across every connected music service:
GET %s/v1/library/tracks — all liked/saved tracks in one neutral list (name, artists, album, sources[]). YouTube likes are filtered to music-shaped videos (not every liked YouTube video).

YouTube Music-specific endpoints (call the full proxy URL; Guardian authenticates you — do not send YouTube tokens or an API key unless your deployment uses api-key mode):

GET /me — Current user's YouTube channel info
GET /me/playlists — List user's playlists in neutral format (paginated, use ?pageToken=N)
GET /me/liked — List liked YouTube videos as neutral tracks (paginated; proxy shows the raw liked-videos playlist — use /v1/library/tracks for music-only liked songs)
GET /me/subscriptions — List followed artists / subscribed music channels (paginated; permission key: artists)
GET /playlists/{id} — Get a playlist by ID
GET /playlists/{id}/items — List items in a playlist as neutral tracks (paginated)
GET /search?q=QUERY — Search YouTube Music (filters to music category)
GET /videos/{id} — Get video details by ID
GET /channels/{id} — Get channel details by ID

Playlist responses use Guardian's neutral fields: name, description, owner, track_count, sources[] (service, service_name, id, url, extra).
All responses are transformed to a compact, LLM-friendly format.
Paginated endpoints return all items automatically when possible.
Always include the full proxy base URL in your requests.`, base, root, root)
}

// HandleProxy processes a YouTube Music API request through the connector.
func (c *YouTubeMusicConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	logger := slog.With("service", "youtube-music", "method", r.Method, "path", remainingPath)

	token := c.server.getValidToken("youtube-music", logger)
	if token == "" {
		writeJSONError(w, http.StatusUnauthorized, "no valid YouTube token — authorize at /v1/auth/youtube-music/login")
		return
	}

	// Map friendly paths to YouTube Data API v3 calls.
	apiPath, extraParams, err := c.mapPath(remainingPath, r.URL.Query())
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Build the upstream URL.
	svc := c.server.config.GetService("youtube-music")
	upstreamBase := "https://www.googleapis.com/youtube/v3"
	if svc != nil && svc.Endpoint != "" {
		upstreamBase = strings.TrimRight(svc.Endpoint, "/")
	}

	upstreamURL := upstreamBase + apiPath
	// Merge extra params with original query.
	q := r.URL.Query()
	for k, v := range extraParams {
		q.Set(k, v)
	}
	if encoded := q.Encode(); encoded != "" {
		upstreamURL += "?" + encoded
	}

	// Cache check.
	cacheKey := "youtube-music:" + r.URL.RequestURI()
	if r.Method == http.MethodGet {
		if cached, ok := c.server.cache.Get(cacheKey); ok {
			logger.Debug("cache hit")
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Guardian-Cache", "hit")
			io.WriteString(w, cached)
			return
		}
	}

	// Execute the request.
	data, status, err := c.doYouTubeRequest(r.Method, upstreamURL, r.Body, token)
	if err != nil {
		logger.Error("youtube request failed", "error", err)
		writeJSONError(w, http.StatusBadGateway, "YouTube API request failed")
		return
	}

	logger.Debug("youtube response", "status", status, "bytes", len(data))

	if status == http.StatusUnauthorized {
		logger.Warn("youtube returned 401, token may be revoked")
		writeJSONError(w, http.StatusUnauthorized, "YouTube token is invalid or expired — re-authorize at /v1/auth/youtube-music/login")
		return
	}

	if status == http.StatusTooManyRequests {
		logger.Warn("youtube rate limited")
		w.Header().Set("Retry-After", "30")
		writeJSONError(w, http.StatusTooManyRequests, "YouTube rate limit exceeded — retry later")
		return
	}

	// Auto-fetch remaining pages from raw data (before transform strips nextPageToken).
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err == nil {
		if r.Method == http.MethodGet {
			raw = c.fetchAllPagesRaw(remainingPath, q, raw, token, logger)
		}
		data, _ = json.Marshal(raw)
	}

	// Transform the (possibly paginated) response.
	transformed := c.transformResponse(remainingPath, data, logger)

	result, err := json.Marshal(transformed)
	if err != nil {
		logger.Error("failed to marshal transformed response", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "response transformation failed")
		return
	}

	// Cache the response.
	if r.Method == http.MethodGet {
		ttl := 60 * time.Second // default 1 minute
		if svc != nil && svc.CacheTTL.Duration > 0 {
			ttl = svc.CacheTTL.Duration
		}
		c.server.cache.Set(cacheKey, string(result), ttl)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Guardian-Cache", "miss")
	w.WriteHeader(status)
	w.Write(result)
}

// mapPath translates friendly proxy paths to YouTube Data API v3 calls.
// Returns the API path and any extra query parameters needed.
func (c *YouTubeMusicConnector) mapPath(path string, query map[string][]string) (string, map[string]string, error) {
	switch {
	case path == "/me":
		return "/channels", map[string]string{"part": "snippet,contentDetails", "mine": "true"}, nil

	case path == "/me/playlists":
		return "/playlists", map[string]string{"part": "snippet,contentDetails", "mine": "true", "maxResults": "50"}, nil

	case path == "/me/liked":
		// Liked videos require the likedVideos playlist ID from the channel.
		// We'll handle this in the transform layer by first fetching the channel.
		likedPlaylistID, err := c.getLikedPlaylistID()
		if err != nil {
			return "", nil, fmt.Errorf("failed to get liked playlist ID: %w", err)
		}
		return "/playlistItems", map[string]string{
			"part":       "snippet,contentDetails",
			"playlistId": likedPlaylistID,
			"maxResults": "50",
		}, nil

	case path == "/me/subscriptions":
		return "/subscriptions", map[string]string{"part": "snippet", "mine": "true", "maxResults": "50"}, nil

	case strings.HasPrefix(path, "/playlists/") && !strings.HasSuffix(path, "/items"):
		playlistID := strings.TrimPrefix(path, "/playlists/")
		return "/playlists", map[string]string{"part": "snippet,contentDetails", "id": playlistID}, nil

	case strings.HasPrefix(path, "/playlists/") && strings.HasSuffix(path, "/items"):
		// /playlists/{id}/items
		parts := strings.Split(strings.TrimPrefix(path, "/playlists/"), "/")
		if len(parts) < 2 {
			return "", nil, fmt.Errorf("invalid playlist items path")
		}
		playlistID := parts[0]
		return "/playlistItems", map[string]string{
			"part":       "snippet,contentDetails",
			"playlistId": playlistID,
			"maxResults": "50",
		}, nil

	case path == "/search":
		// Default to music video search if no type specified.
		params := map[string]string{"part": "snippet"}
		if types, ok := query["type"]; ok {
			params["type"] = types[0]
		} else {
			params["type"] = "video"
			if _, ok := query["videoCategoryId"]; !ok {
				params["videoCategoryId"] = "10" // Music category
			}
		}
		return "/search", params, nil

	case strings.HasPrefix(path, "/videos/"):
		videoID := strings.TrimPrefix(path, "/videos/")
		return "/videos", map[string]string{"part": "snippet,contentDetails,statistics", "id": videoID}, nil

	case strings.HasPrefix(path, "/channels/"):
		channelID := strings.TrimPrefix(path, "/channels/")
		return "/channels", map[string]string{"part": "snippet,contentDetails,statistics", "id": channelID}, nil

	default:
		// Pass through to YouTube API as-is.
		return path, nil, nil
	}
}

// getLikedPlaylistID fetches the user's channel to get the likedVideos playlist ID.
func (c *YouTubeMusicConnector) getLikedPlaylistID() (string, error) {
	token := c.server.getValidToken("youtube-music", slog.Default())
	if token == "" {
		return "", fmt.Errorf("no valid token")
	}

	upstreamBase := "https://www.googleapis.com/youtube/v3"
	if svc := c.server.config.GetService("youtube-music"); svc != nil && svc.Endpoint != "" {
		upstreamBase = strings.TrimRight(svc.Endpoint, "/")
	}
	url := upstreamBase + "/channels?part=contentDetails&mine=true"
	data, status, err := c.doYouTubeRequest("GET", url, nil, token)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("channels API returned %d", status)
	}

	var resp struct {
		Items []struct {
			ContentDetails struct {
				RelatedPlaylists struct {
					Likes string `json:"likes"`
				} `json:"relatedPlaylists"`
			} `json:"contentDetails"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", err
	}
	if len(resp.Items) == 0 {
		return "", fmt.Errorf("no channel found")
	}
	return resp.Items[0].ContentDetails.RelatedPlaylists.Likes, nil
}

// doYouTubeRequest executes an HTTP request to the YouTube Data API.
func (c *YouTubeMusicConnector) doYouTubeRequest(method, url string, body io.Reader, token string) ([]byte, int, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := guardianHTTPClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

// transformResponse converts YouTube's verbose JSON into a compact format.
func (c *YouTubeMusicConnector) transformResponse(path string, data []byte, logger *slog.Logger) any {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return string(data)
	}

	// Pass through errors.
	if _, ok := raw["error"]; ok {
		return raw
	}

	switch {
	case path == "/me":
		return c.transformChannel(raw)
	case path == "/me/playlists":
		return c.transformPlaylistList(raw)
	case path == "/me/liked":
		return c.transformPlaylistItems(raw)
	case path == "/me/subscriptions":
		return c.transformSubscriptions(raw)
	case path == "/search":
		return c.transformSearch(raw)
	case strings.HasPrefix(path, "/playlists/") && !strings.HasSuffix(path, "/items"):
		return c.transformPlaylistDetail(raw)
	case strings.HasPrefix(path, "/playlists/") && strings.HasSuffix(path, "/items"):
		return c.transformPlaylistItems(raw)
	case strings.HasPrefix(path, "/videos/"):
		return c.transformVideo(raw)
	case strings.HasPrefix(path, "/channels/"):
		return c.transformChannel(raw)
	default:
		return c.simplify(raw)
	}
}

func (c *YouTubeMusicConnector) transformChannel(raw map[string]any) map[string]any {
	items, _ := raw["items"].([]any)
	if len(items) == 0 {
		return map[string]any{"message": "No channel found"}
	}
	item, _ := items[0].(map[string]any)
	snippet, _ := item["snippet"].(map[string]any)
	contentDetails, _ := item["contentDetails"].(map[string]any)

	result := map[string]any{
		"id":          item["id"],
		"title":       snippet["title"],
		"description": snippet["description"],
	}
	if related, ok := contentDetails["relatedPlaylists"].(map[string]any); ok {
		result["playlists"] = map[string]any{
			"likes":       related["likes"],
			"uploads":     related["uploads"],
			"watch_later": related["watchLater"],
		}
	}
	return result
}

// ListUserFollowedArtists implements ArtistProvider. YouTube subscriptions
// are the closest match to followed artists/channels.
func (c *YouTubeMusicConnector) ListUserFollowedArtists(ctx context.Context) ([]Artist, bool, error) {
	token := c.server.getValidToken("youtube-music", slog.Default())
	if token == "" {
		return nil, false, ErrUnauthorized
	}

	upstreamBase := "https://www.googleapis.com/youtube/v3"
	if svc := c.server.config.GetService("youtube-music"); svc != nil && svc.Endpoint != "" {
		upstreamBase = strings.TrimRight(svc.Endpoint, "/")
	}

	var all []Artist
	pageToken := ""
	for page := 0; page < libraryPageCap; page++ {
		u := upstreamBase + "/subscriptions?part=snippet&mine=true&maxResults=50"
		if pageToken != "" {
			u += "&pageToken=" + pageToken
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, false, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := guardianHTTPClient.Do(req)
		if err != nil {
			return nil, false, err
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, false, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, false, fmt.Errorf("youtube subscriptions returned %d", resp.StatusCode)
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, false, err
		}
		items, _ := raw["items"].([]any)
		for _, item := range items {
			s, ok := item.(map[string]any)
			if !ok {
				continue
			}
			snippet, _ := s["snippet"].(map[string]any)
			if snippet == nil {
				continue
			}
			title, _ := snippet["title"].(string)
			channelID, _ := extractNested(snippet, "resourceId", "channelId").(string)
			if channelID == "" {
				if id, ok := snippet["channelId"].(string); ok {
					channelID = id
				}
			}
			a := newArtist("youtube-music", "YouTube Music", channelID, title)
			if len(a.Sources) > 0 && channelID != "" {
				a.Sources[0].URL = "https://www.youtube.com/channel/" + channelID
			}
			if a.Name != "" {
				all = append(all, a)
			}
		}
		pageToken, _ = raw["nextPageToken"].(string)
		if pageToken == "" {
			break
		}
	}
	return all, pageToken != "", nil
}

// youtubeMusicCategoryID is YouTube Data API v3 categoryId for Music.
const youtubeMusicCategoryID = "10"

// isMusicShapedYouTubeVideo reports whether a videos.list resource looks like
// music (category Music, or an official catalog channel ending in " - Topic").
// Used to keep non-music YouTube likes out of the neutral liked-tracks library.
// See https://github.com/fudomunro/aperture-guardian/issues/43.
func isMusicShapedYouTubeVideo(categoryID, channelTitle string) bool {
	if categoryID == youtubeMusicCategoryID {
		return true
	}
	return strings.HasSuffix(strings.TrimSpace(channelTitle), " - Topic")
}

// fetchVideoSnippets batch-fetches videos.list snippet metadata (categoryId,
// channelTitle, …) for the given video IDs. IDs not returned upstream are absent
// from the map (deleted/private).
func (c *YouTubeMusicConnector) fetchVideoSnippets(ctx context.Context, token, upstreamBase string, videoIDs []string) (map[string]map[string]any, error) {
	out := make(map[string]map[string]any, len(videoIDs))
	const batch = 50
	for start := 0; start < len(videoIDs); start += batch {
		end := start + batch
		if end > len(videoIDs) {
			end = len(videoIDs)
		}
		u := upstreamBase + "/videos?part=snippet,contentDetails&id=" + strings.Join(videoIDs[start:end], ",")
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := guardianHTTPClient.Do(req)
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("youtube videos lookup returned %d", resp.StatusCode)
		}
		var raw struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, err
		}
		for _, item := range raw.Items {
			id, _ := item["id"].(string)
			if id == "" {
				continue
			}
			out[id] = item
		}
	}
	return out, nil
}

// ListUserLikedTracks implements LikedTrackProvider. The YouTube liked-videos
// playlist is all-time YouTube likes (not music-only). Items are filtered to
// music-shaped videos via videos.list so the neutral library, agent answers,
// and mirror do not treat tutorials/vlogs as liked songs.
// https://github.com/fudomunro/aperture-guardian/issues/43
func (c *YouTubeMusicConnector) ListUserLikedTracks(ctx context.Context) ([]Track, bool, error) {
	token := c.server.getValidToken("youtube-music", slog.Default())
	if token == "" {
		return nil, false, ErrUnauthorized
	}

	likedPlaylistID, err := c.getLikedPlaylistID()
	if err != nil {
		return nil, false, err
	}

	upstreamBase := "https://www.googleapis.com/youtube/v3"
	if svc := c.server.config.GetService("youtube-music"); svc != nil && svc.Endpoint != "" {
		upstreamBase = strings.TrimRight(svc.Endpoint, "/")
	}

	var likedItems []map[string]any
	var videoIDs []string
	seenID := make(map[string]bool)
	pageToken := ""
	for page := 0; page < libraryPageCap; page++ {
		u := upstreamBase + "/playlistItems?part=snippet,contentDetails&playlistId=" +
			likedPlaylistID + "&maxResults=50"
		if pageToken != "" {
			u += "&pageToken=" + pageToken
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, false, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := guardianHTTPClient.Do(req)
		if err != nil {
			return nil, false, err
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, false, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, false, fmt.Errorf("youtube liked videos returned %d", resp.StatusCode)
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, false, err
		}
		items, _ := raw["items"].([]any)
		for _, item := range items {
			p, ok := item.(map[string]any)
			if !ok {
				continue
			}
			likedItems = append(likedItems, p)
			if id := youtubePlaylistItemVideoID(p); id != "" && !seenID[id] {
				seenID[id] = true
				videoIDs = append(videoIDs, id)
			}
		}
		pageToken, _ = raw["nextPageToken"].(string)
		if pageToken == "" {
			break
		}
	}

	snippets, err := c.fetchVideoSnippets(ctx, token, upstreamBase, videoIDs)
	if err != nil {
		return nil, false, err
	}

	var all []Track
	for _, p := range likedItems {
		id := youtubePlaylistItemVideoID(p)
		video, ok := snippets[id]
		if !ok {
			continue
		}
		snippet, _ := video["snippet"].(map[string]any)
		if snippet == nil {
			continue
		}
		categoryID, _ := snippet["categoryId"].(string)
		channelTitle, _ := snippet["channelTitle"].(string)
		if !isMusicShapedYouTubeVideo(categoryID, channelTitle) {
			continue
		}
		if track, ok := c.neutralYouTubeTrack(p); ok {
			all = append(all, track)
		}
	}
	return all, pageToken != "", nil
}

// youtubePlaylistItemVideoID extracts the video ID from a playlistItem resource.
func youtubePlaylistItemVideoID(item map[string]any) string {
	if contentDetails, ok := item["contentDetails"].(map[string]any); ok {
		if id, _ := contentDetails["videoId"].(string); id != "" {
			return id
		}
	}
	if snippet, ok := item["snippet"].(map[string]any); ok {
		if rid, ok := snippet["resourceId"].(map[string]any); ok {
			id, _ := rid["videoId"].(string)
			return id
		}
	}
	return ""
}

// ListUserPlaylists implements PlaylistProvider. Fetches YouTube playlists
// and returns them in Guardian's neutral format.
func (c *YouTubeMusicConnector) ListUserPlaylists(ctx context.Context) ([]Playlist, bool, error) {
	token := c.server.getValidToken("youtube-music", slog.Default())
	if token == "" {
		return nil, false, ErrUnauthorized
	}

	upstreamBase := "https://www.googleapis.com/youtube/v3"
	if svc := c.server.config.GetService("youtube-music"); svc != nil && svc.Endpoint != "" {
		upstreamBase = strings.TrimRight(svc.Endpoint, "/")
	}

	var all []Playlist
	pageToken := ""
	for page := 0; page < libraryPageCap; page++ {
		u := upstreamBase + "/playlists?part=snippet,contentDetails&mine=true&maxResults=50"
		if pageToken != "" {
			u += "&pageToken=" + pageToken
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, false, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := guardianHTTPClient.Do(req)
		if err != nil {
			return nil, false, err
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, false, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, false, fmt.Errorf("youtube playlists returned %d", resp.StatusCode)
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, false, err
		}
		items, _ := raw["items"].([]any)
		for _, item := range items {
			p, ok := item.(map[string]any)
			if !ok {
				continue
			}
			all = append(all, c.neutralPlaylist(p))
		}
		pageToken, _ = raw["nextPageToken"].(string)
		if pageToken == "" {
			break
		}
	}
	return all, pageToken != "", nil
}

// youtubePlaylistURL builds a public YouTube playlist link.
func youtubePlaylistURL(id string) string {
	if id == "" {
		return ""
	}
	return "https://www.youtube.com/playlist?list=" + id
}

// neutralPlaylist maps a raw YouTube playlist resource into the neutral type.
// Service identity lives in Sources.
func (c *YouTubeMusicConnector) neutralPlaylist(p map[string]any) Playlist {
	id, _ := p["id"].(string)
	snippet, _ := p["snippet"].(map[string]any)
	contentDetails, _ := p["contentDetails"].(map[string]any)

	name, _ := snippet["title"].(string)
	description, _ := snippet["description"].(string)
	owner, _ := snippet["channelTitle"].(string)
	publishedAt, _ := snippet["publishedAt"].(string)

	trackCount := 0
	if n, ok := asInt(contentDetails["itemCount"]); ok {
		trackCount = n
	}

	extra := map[string]any{}
	if publishedAt != "" {
		extra["published_at"] = publishedAt
	}
	if channelID, ok := snippet["channelId"]; ok {
		extra["channel_id"] = channelID
	}

	return Playlist{
		Name:        name,
		Description: description,
		Owner:       owner,
		TrackCount:  trackCount,
		Sources: []PlaylistSource{
			primaryPlaylistSource("youtube-music", "YouTube Music", id, youtubePlaylistURL(id), extra),
		},
	}
}

// neutralYouTubeTrack maps a raw playlistItem / liked video into the neutral Track.
func (c *YouTubeMusicConnector) neutralYouTubeTrack(item map[string]any) (Track, bool) {
	snippet, _ := item["snippet"].(map[string]any)
	contentDetails, _ := item["contentDetails"].(map[string]any)
	if snippet == nil {
		return Track{}, false
	}
	name, _ := snippet["title"].(string)
	channel, _ := snippet["channelTitle"].(string)
	publishedAt, _ := snippet["publishedAt"].(string)
	videoID, _ := contentDetails["videoId"].(string)
	if videoID == "" {
		// Deleted/private items often omit contentDetails; resourceId may be absent.
		if rid, ok := snippet["resourceId"].(map[string]any); ok {
			videoID, _ = rid["videoId"].(string)
		}
	}

	extra := map[string]any{}
	if channel != "" {
		extra["channel"] = channel
	}
	if publishedAt != "" {
		extra["published_at"] = publishedAt
	}
	if videoID != "" {
		extra["video_id"] = videoID
	}
	if thumbnails, ok := snippet["thumbnails"].(map[string]any); ok {
		if high, ok := thumbnails["high"].(map[string]any); ok {
			if url, ok := high["url"].(string); ok {
				extra["thumbnail"] = url
			}
		}
	}

	var artists []Artist
	if channel != "" {
		artists = []Artist{newArtist("youtube-music", "YouTube Music", "", channel)}
	}

	url := ""
	if videoID != "" {
		url = "https://www.youtube.com/watch?v=" + videoID
	}

	return Track{
		Name:    name,
		Artists: artists,
		Sources: []TrackSource{{
			Service:     "youtube-music",
			ServiceName: "YouTube Music",
			ID:          videoID,
			URL:         url,
			Extra:       extraOrNil(extra),
		}},
	}, true
}

func (c *YouTubeMusicConnector) transformPlaylistList(raw map[string]any) PlaylistList {
	items, _ := raw["items"].([]any)
	playlists := make([]Playlist, 0, len(items))
	for _, item := range items {
		p, ok := item.(map[string]any)
		if !ok {
			continue
		}
		playlists = append(playlists, c.neutralPlaylist(p))
	}
	total := len(playlists)
	if n, ok := asInt(raw["total_fetched"]); ok {
		total = n
	} else if pageInfo, ok := raw["pageInfo"].(map[string]any); ok {
		if n, ok := asInt(pageInfo["totalResults"]); ok {
			total = n
		}
	}
	return PlaylistList{Playlists: playlists, Total: total}
}

func (c *YouTubeMusicConnector) transformPlaylistItems(raw map[string]any) map[string]any {
	items, _ := raw["items"].([]any)
	tracks := make([]Track, 0, len(items))
	for _, item := range items {
		p, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if track, ok := c.neutralYouTubeTrack(p); ok {
			tracks = append(tracks, track)
		}
	}
	total := len(tracks)
	if n, ok := asInt(raw["total_fetched"]); ok {
		total = n
	} else if pageInfo, ok := raw["pageInfo"].(map[string]any); ok {
		if n, ok := asInt(pageInfo["totalResults"]); ok {
			total = n
		}
	}
	return map[string]any{
		"tracks": tracks,
		"total":  total,
	}
}

func (c *YouTubeMusicConnector) transformSubscriptions(raw map[string]any) map[string]any {
	items, _ := raw["items"].([]any)
	channels := make([]map[string]any, 0, len(items))
	for _, item := range items {
		s, ok := item.(map[string]any)
		if !ok {
			continue
		}
		snippet, _ := s["snippet"].(map[string]any)
		if snippet == nil {
			continue
		}
		channels = append(channels, map[string]any{
			"title":       snippet["title"],
			"description": snippet["description"],
			"channel_id":  extractNested(snippet, "resourceId", "channelId"),
		})
	}
	return map[string]any{
		"subscriptions": channels,
		"total":         raw["pageInfo"],
		"total_fetched": raw["total_fetched"],
	}
}

func (c *YouTubeMusicConnector) transformSearch(raw map[string]any) map[string]any {
	items, _ := raw["items"].([]any)
	results := make([]map[string]any, 0, len(items))
	for _, item := range items {
		s, ok := item.(map[string]any)
		if !ok {
			continue
		}
		snippet, _ := s["snippet"].(map[string]any)
		id, _ := s["id"].(map[string]any)
		if snippet == nil || id == nil {
			continue
		}
		entry := map[string]any{
			"title":        snippet["title"],
			"channel":      snippet["channelTitle"],
			"description":  snippet["description"],
			"published_at": snippet["publishedAt"],
		}
		// Extract the actual resource ID based on kind.
		if videoID, ok := id["videoId"].(string); ok {
			entry["video_id"] = videoID
			entry["type"] = "video"
		} else if playlistID, ok := id["playlistId"].(string); ok {
			entry["playlist_id"] = playlistID
			entry["type"] = "playlist"
		} else if channelID, ok := id["channelId"].(string); ok {
			entry["channel_id"] = channelID
			entry["type"] = "channel"
		}
		results = append(results, entry)
	}
	return map[string]any{
		"results":       results,
		"total":         raw["pageInfo"],
		"total_fetched": raw["total_fetched"],
	}
}

func (c *YouTubeMusicConnector) transformPlaylistDetail(raw map[string]any) any {
	items, _ := raw["items"].([]any)
	if len(items) == 0 {
		return map[string]any{"message": "Playlist not found"}
	}
	p, _ := items[0].(map[string]any)
	base := c.neutralPlaylist(p)
	return PlaylistDetail{
		Playlist:    base,
		TotalTracks: base.TrackCount,
	}
}

func (c *YouTubeMusicConnector) transformVideo(raw map[string]any) map[string]any {
	items, _ := raw["items"].([]any)
	if len(items) == 0 {
		return map[string]any{"message": "Video not found"}
	}
	v, _ := items[0].(map[string]any)
	snippet, _ := v["snippet"].(map[string]any)
	contentDetails, _ := v["contentDetails"].(map[string]any)
	statistics, _ := v["statistics"].(map[string]any)

	result := map[string]any{
		"id":           v["id"],
		"title":        snippet["title"],
		"channel":      snippet["channelTitle"],
		"description":  snippet["description"],
		"published_at": snippet["publishedAt"],
		"duration":     contentDetails["duration"],
		"view_count":   statistics["viewCount"],
		"like_count":   statistics["likeCount"],
	}
	if tags, ok := snippet["tags"].([]any); ok {
		result["tags"] = tags
	}
	return result
}

// simplify strips a raw YouTube response down to key fields.
func (c *YouTubeMusicConnector) simplify(raw map[string]any) map[string]any {
	result := map[string]any{}
	keep := []string{"items", "pageInfo", "nextPageToken", "prevPageToken", "totalResults"}
	for _, k := range keep {
		if v, ok := raw[k]; ok {
			result[k] = v
		}
	}
	return result
}

// isPaginated returns true if the response has a nextPageToken.
func (c *YouTubeMusicConnector) isPaginated(raw map[string]any) bool {
	_, ok := raw["nextPageToken"]
	return ok
}

// fetchAllPagesRaw follows YouTube's pagination tokens on raw (untransformed) data.
// It combines items arrays from all pages into the first page's response.
func (c *YouTubeMusicConnector) fetchAllPagesRaw(path string, query map[string][]string, firstPage map[string]any, token string, logger *slog.Logger) map[string]any {
	nextToken, _ := firstPage["nextPageToken"].(string)
	if nextToken == "" {
		return firstPage
	}

	allItems, _ := firstPage["items"].([]any)

	svc := c.server.config.GetService("youtube-music")
	upstreamBase := "https://www.googleapis.com/youtube/v3"
	if svc != nil && svc.Endpoint != "" {
		upstreamBase = strings.TrimRight(svc.Endpoint, "/")
	}

	apiPath, extraParams, _ := c.mapPath(path, query)

	// Follow up to libraryPageCap additional pages so large libraries are
	// not silently cut off. Hit the cap → mark truncated for the caller.
	maxPages := libraryPageCap
	for i := 0; i < maxPages && nextToken != ""; i++ {
		q := map[string][]string{}
		for k, v := range query {
			q[k] = v
		}
		for k, v := range extraParams {
			q[k] = []string{v}
		}
		q["pageToken"] = []string{nextToken}

		upstreamURL := upstreamBase + apiPath + "?" + mapToQuery(q)

		data, status, err := c.doYouTubeRequest("GET", upstreamURL, nil, token)
		if err != nil || status != http.StatusOK {
			logger.Warn("pagination fetch failed", "url", upstreamURL, "status", status, "error", err)
			break
		}
		var next map[string]any
		if err := json.Unmarshal(data, &next); err != nil {
			break
		}
		items, _ := next["items"].([]any)
		allItems = append(allItems, items...)
		nextToken, _ = next["nextPageToken"].(string)
	}

	firstPage["items"] = allItems
	if nextToken != "" {
		firstPage["truncated"] = true
	}
	delete(firstPage, "nextPageToken")
	firstPage["total_fetched"] = len(allItems)

	return firstPage
}

// fetchAllPages follows YouTube's pagination tokens and combines items.
func (c *YouTubeMusicConnector) fetchAllPages(path string, query map[string][]string, firstPage any, token string, logger *slog.Logger) any {
	page, ok := firstPage.(map[string]any)
	if !ok {
		return firstPage
	}

	nextToken, _ := page["nextPageToken"].(string)
	if nextToken == "" {
		return firstPage
	}

	// Determine the items key based on path.
	itemsKey := c.itemsKeyForPath(path)
	if itemsKey == "" {
		return firstPage
	}

	allItems, _ := page[itemsKey].([]any)

	// Follow up to libraryPageCap additional pages so large libraries are
	// not silently cut off. Hit the cap → mark truncated for the caller.
	maxPages := libraryPageCap
	for i := 0; i < maxPages && nextToken != ""; i++ {
		// Build the next page URL.
		svc := c.server.config.GetService("youtube-music")
		upstreamBase := "https://www.googleapis.com/youtube/v3"
		if svc != nil && svc.Endpoint != "" {
			upstreamBase = strings.TrimRight(svc.Endpoint, "/")
		}

		apiPath, extraParams, _ := c.mapPath(path, query)
		q := map[string][]string{}
		for k, v := range query {
			q[k] = v
		}
		for k, v := range extraParams {
			q[k] = []string{v}
		}
		q["pageToken"] = []string{nextToken}

		upstreamURL := upstreamBase + apiPath + "?" + mapToQuery(q)

		data, status, err := c.doYouTubeRequest("GET", upstreamURL, nil, token)
		if err != nil || status != http.StatusOK {
			logger.Warn("pagination fetch failed", "url", upstreamURL, "status", status, "error", err)
			break
		}
		var next map[string]any
		if err := json.Unmarshal(data, &next); err != nil {
			break
		}
		items, _ := next[itemsKey].([]any)
		allItems = append(allItems, items...)
		nextToken, _ = next["nextPageToken"].(string)
	}

	page[itemsKey] = allItems
	if nextToken != "" {
		page["truncated"] = true
	}
	delete(page, "nextPageToken")
	page["total_fetched"] = len(allItems)

	return page
}

func (c *YouTubeMusicConnector) itemsKeyForPath(path string) string {
	switch path {
	case "/me/playlists":
		return "playlists"
	case "/me/liked", "/playlists/*/items":
		return "videos"
	case "/me/subscriptions":
		return "subscriptions"
	case "/search":
		return "results"
	default:
		return ""
	}
}

// mapToQuery converts a map[string][]string to a URL query string.
func mapToQuery(params map[string][]string) string {
	var parts []string
	for k, vs := range params {
		for _, v := range vs {
			parts = append(parts, k+"="+v)
		}
	}
	return strings.Join(parts, "&")
}
