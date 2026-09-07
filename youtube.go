package main

import (
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

// SystemInstructions returns the system message injected into LLM requests
// when YouTube Music is referenced.
func (c *YouTubeMusicConnector) SystemInstructions(baseURL string) string {
	base := strings.TrimRight(baseURL, "/") + "/v1/services/youtube-music"
	return fmt.Sprintf(`When the user's request involves YouTube Music, you MUST use the Guardian proxy at %s instead of calling YouTube directly.

Available endpoints (all require Authorization header from Guardian):

GET /me — Current user's YouTube channel info
GET /me/playlists — List user's playlists (paginated, use ?pageToken=N)
GET /me/liked — List liked/favorite music videos (paginated)
GET /me/subscriptions — List subscribed music channels (paginated)
GET /playlists/{id} — Get a playlist by ID
GET /playlists/{id}/items — List items in a playlist (paginated)
GET /search?q=QUERY — Search YouTube Music (filters to music category)
GET /videos/{id} — Get video details by ID
GET /channels/{id} — Get channel details by ID

All responses are transformed to a compact, LLM-friendly format.
Paginated endpoints return all items automatically when possible.
Always include the full proxy base URL in your requests.`, base)
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

	// Transform the response.
	transformed := c.transformResponse(remainingPath, data, logger)

	// Auto-fetch remaining pages for paginated endpoints.
	if r.Method == http.MethodGet {
		transformed = c.fetchAllPages(remainingPath, q, transformed, token, logger)
	}

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
			"part":        "snippet,contentDetails",
			"playlistId":  likedPlaylistID,
			"maxResults":  "50",
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

	url := "https://www.googleapis.com/youtube/v3/channels?part=contentDetails&mine=true"
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

	resp, err := http.DefaultClient.Do(req)
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
			"likes":     related["likes"],
			"uploads":   related["uploads"],
			"watch_later": related["watchLater"],
		}
	}
	return result
}

func (c *YouTubeMusicConnector) transformPlaylistList(raw map[string]any) map[string]any {
	items, _ := raw["items"].([]any)
	playlists := make([]map[string]any, 0, len(items))
	for _, item := range items {
		p, ok := item.(map[string]any)
		if !ok {
			continue
		}
		snippet, _ := p["snippet"].(map[string]any)
		contentDetails, _ := p["contentDetails"].(map[string]any)
		playlists = append(playlists, map[string]any{
			"id":           p["id"],
			"title":        snippet["title"],
			"description":  snippet["description"],
			"item_count":   contentDetails["itemCount"],
		})
	}
	return map[string]any{
		"playlists":     playlists,
		"total_results": raw["pageInfo"],
	}
}

func (c *YouTubeMusicConnector) transformPlaylistItems(raw map[string]any) map[string]any {
	items, _ := raw["items"].([]any)
	videos := make([]map[string]any, 0, len(items))
	for _, item := range items {
		p, ok := item.(map[string]any)
		if !ok {
			continue
		}
		snippet, _ := p["snippet"].(map[string]any)
		contentDetails, _ := p["contentDetails"].(map[string]any)
		if snippet == nil {
			continue
		}
		video := map[string]any{
			"title":       snippet["title"],
			"channel":     snippet["channelTitle"],
			"published_at": snippet["publishedAt"],
			"video_id":    contentDetails["videoId"],
		}
		if thumbnails, ok := snippet["thumbnails"].(map[string]any); ok {
			if high, ok := thumbnails["high"].(map[string]any); ok {
				video["thumbnail"] = high["url"]
			}
		}
		videos = append(videos, video)
	}
	return map[string]any{
		"videos": videos,
		"total":  raw["pageInfo"],
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
			"title":       snippet["title"],
			"channel":     snippet["channelTitle"],
			"description": snippet["description"],
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
		"results": results,
		"total":   raw["pageInfo"],
	}
}

func (c *YouTubeMusicConnector) transformPlaylistDetail(raw map[string]any) map[string]any {
	items, _ := raw["items"].([]any)
	if len(items) == 0 {
		return map[string]any{"message": "Playlist not found"}
	}
	p, _ := items[0].(map[string]any)
	snippet, _ := p["snippet"].(map[string]any)
	contentDetails, _ := p["contentDetails"].(map[string]any)
	return map[string]any{
		"id":          p["id"],
		"title":       snippet["title"],
		"description": snippet["description"],
		"channel":     snippet["channelTitle"],
		"item_count":  contentDetails["itemCount"],
		"published_at": snippet["publishedAt"],
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
		"id":            v["id"],
		"title":         snippet["title"],
		"channel":       snippet["channelTitle"],
		"description":   snippet["description"],
		"published_at":  snippet["publishedAt"],
		"duration":      contentDetails["duration"],
		"view_count":    statistics["viewCount"],
		"like_count":    statistics["likeCount"],
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

	// Fetch up to 10 additional pages.
	maxPages := 10
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
