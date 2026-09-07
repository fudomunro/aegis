package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// SpotifyConnector provides intelligent proxying for the Spotify API.
// It transforms verbose Spotify responses into LLM-friendly formats
// and generates rich system instructions for agents.
type SpotifyConnector struct {
	server *Server
}

// NewSpotifyConnector creates a connector wired to the given server.
func NewSpotifyConnector(srv *Server) *SpotifyConnector {
	return &SpotifyConnector{server: srv}
}

// SystemInstructions returns the system message injected into LLM requests
// when Spotify is referenced. This tells the agent exactly how to use the proxy.
func (c *SpotifyConnector) SystemInstructions(baseURL string) string {
	base := strings.TrimRight(baseURL, "/") + "/v1/services/spotify"
	return fmt.Sprintf(`When the user's request involves Spotify, you MUST use the Guardian proxy at %s instead of calling Spotify directly.

Available endpoints (all require Authorization header from Guardian):

GET /me — Current user profile
GET /me/playlists — List user's playlists (paginated, use ?limit=N&offset=N)
GET /me/tracks — List saved/liked tracks (paginated)
GET /me/albums — List saved albums (paginated)
GET /me/artists — List followed artists
GET /me/player — Current playback state
GET /me/player/recently-played — Recently played tracks
GET /me/top/tracks — User's top tracks (use ?time_range=short_term|medium_term|long_term)
GET /me/top/artists — User's top artists
GET /search?type=track,artist,album,playlist&q=QUERY — Search Spotify
GET /tracks/{id} — Get a track by ID
GET /artists/{id} — Get an artist by ID
GET /artists/{id}/top-tracks — Artist's top tracks
GET /albums/{id} — Get an album by ID
GET /playlists/{id} — Get a playlist by ID with tracks

All responses are transformed to a compact, LLM-friendly format.
Paginated endpoints return all items automatically when possible.
Always include the full proxy base URL in your requests.`, base)
}

// HandleProxy processes a Spotify API request through the connector.
// It forwards to the Spotify API, transforms the response, and handles pagination.
func (c *SpotifyConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	logger := slog.With("service", "spotify", "method", r.Method, "path", remainingPath)

	token := c.server.getValidToken("spotify", logger)
	if token == "" {
		writeJSONError(w, http.StatusUnauthorized, "no valid Spotify token — authorize at /v1/auth/spotify/login")
		return
	}

	// Build the upstream Spotify API URL from the configured endpoint.
	svc := c.server.config.GetService("spotify")
	upstreamBase := "https://api.spotify.com/v1"
	if svc != nil && svc.Endpoint != "" {
		upstreamBase = strings.TrimRight(svc.Endpoint, "/")
	}
	upstreamURL := upstreamBase + remainingPath
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}

	// For GET requests, check cache first.
	cacheKey := "spotify:" + r.URL.RequestURI()
	if r.Method == http.MethodGet {
		if cached, ok := c.server.cache.Get(cacheKey); ok {
			logger.Debug("cache hit")
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Guardian-Cache", "hit")
			io.WriteString(w, cached)
			return
		}
	}

	// Execute the request against Spotify.
	data, status, err := c.doSpotifyRequest(r.Method, upstreamURL, r.Body, token)
	if err != nil {
		logger.Error("spotify request failed", "error", err)
		writeJSONError(w, http.StatusBadGateway, "Spotify API request failed")
		return
	}

	logger.Debug("spotify response", "status", status, "bytes", len(data))

	// Handle 401 — token may have been revoked.
	if status == http.StatusUnauthorized {
		logger.Warn("spotify returned 401, token may be revoked")
		writeJSONError(w, http.StatusUnauthorized, "Spotify token is invalid or expired — re-authorize at /v1/auth/spotify/login")
		return
	}

	// Handle 429 — rate limited.
	if status == http.StatusTooManyRequests {
		logger.Warn("spotify rate limited")
		w.Header().Set("Retry-After", "30")
		writeJSONError(w, http.StatusTooManyRequests, "Spotify rate limit exceeded — retry later")
		return
	}

	// Transform the response for LLM consumption.
	transformed := c.transformResponse(remainingPath, data, logger)

	// For paginated GET endpoints, auto-fetch remaining pages.
	if r.Method == http.MethodGet && c.isPaginated(remainingPath) {
		transformed = c.fetchAllPages(remainingPath, r.URL.RawQuery, transformed, token, logger)
	}

	result, err := json.Marshal(transformed)
	if err != nil {
		logger.Error("failed to marshal transformed response", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "response transformation failed")
		return
	}

	// Cache the transformed response.
	if r.Method == http.MethodGet {
		ttl := c.server.config.GetService("spotify").CacheTTL.Duration
		if ttl == 0 {
			ttl = 60 // default 1 minute for Spotify data
		}
		c.server.cache.Set(cacheKey, string(result), ttl)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Guardian-Cache", "miss")
	w.WriteHeader(status)
	w.Write(result)
}

// doSpotifyRequest executes an HTTP request to the Spotify API.
func (c *SpotifyConnector) doSpotifyRequest(method, url string, body io.Reader, token string) ([]byte, int, error) {
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

// transformResponse converts Spotify's verbose JSON into a compact format
// optimized for LLM consumption.
func (c *SpotifyConnector) transformResponse(path string, data []byte, logger *slog.Logger) any {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		// If it's not a map (e.g., an array), return as-is.
		var arr []any
		if err2 := json.Unmarshal(data, &arr); err2 == nil {
			return arr
		}
		return string(data)
	}

	// Handle error responses from Spotify.
	if _, ok := raw["error"]; ok {
		return raw // pass through Spotify errors
	}

	switch {
	case path == "/me":
		return c.transformUser(raw)
	case path == "/me/playlists" || strings.HasPrefix(path, "/users/") && strings.HasSuffix(path, "/playlists"):
		return c.transformPlaylistList(raw)
	case path == "/me/tracks":
		return c.transformSavedTracks(raw)
	case path == "/me/albums":
		return c.transformSavedAlbums(raw)
	case path == "/me/player":
		return c.transformPlayer(raw)
	case path == "/me/player/recently-played":
		return c.transformRecentlyPlayed(raw)
	case path == "/me/top/tracks":
		return c.transformTrackList(raw)
	case path == "/me/top/artists":
		return c.transformArtistList(raw)
	case path == "/search":
		return c.transformSearch(raw)
	case strings.HasPrefix(path, "/playlists/"):
		return c.transformPlaylistDetail(raw)
	case strings.HasPrefix(path, "/tracks/"):
		return c.transformTrack(raw)
	case strings.HasPrefix(path, "/artists/") && !strings.HasSuffix(path, "/top-tracks"):
		return c.transformArtist(raw)
	case strings.HasPrefix(path, "/artists/") && strings.HasSuffix(path, "/top-tracks"):
		return c.transformTrackList(raw)
	case strings.HasPrefix(path, "/albums/"):
		return c.transformAlbum(raw)
	default:
		// Unknown path — return a simplified version of the raw response.
		return c.simplify(raw)
	}
}

func (c *SpotifyConnector) transformUser(raw map[string]any) map[string]any {
	return map[string]any{
		"id":           raw["id"],
		"display_name": raw["display_name"],
		"uri":          raw["uri"],
	}
}

func (c *SpotifyConnector) transformPlaylistList(raw map[string]any) map[string]any {
	items, _ := raw["items"].([]any)
	playlists := make([]map[string]any, 0, len(items))
	for _, item := range items {
		p, ok := item.(map[string]any)
		if !ok {
			continue
		}
		playlists = append(playlists, map[string]any{
			"id":          p["id"],
			"name":        p["name"],
			"description": p["description"],
			"tracks":      extractNested(p, "tracks", "total"),
			"uri":         p["uri"],
		})
	}
	return map[string]any{
		"playlists": playlists,
		"total":     raw["total"],
	}
}

func (c *SpotifyConnector) transformSavedTracks(raw map[string]any) map[string]any {
	items, _ := raw["items"].([]any)
	tracks := make([]map[string]any, 0, len(items))
	for _, item := range items {
		saved, ok := item.(map[string]any)
		if !ok {
			continue
		}
		track, _ := saved["track"].(map[string]any)
		if track == nil {
			continue
		}
		tracks = append(tracks, map[string]any{
			"name":    track["name"],
			"artists": extractNames(track, "artists"),
			"album":   extractNested(track, "album", "name"),
			"id":      track["id"],
			"uri":     track["uri"],
		})
	}
	return map[string]any{
		"tracks": tracks,
		"total":  raw["total"],
	}
}

func (c *SpotifyConnector) transformSavedAlbums(raw map[string]any) map[string]any {
	items, _ := raw["items"].([]any)
	albums := make([]map[string]any, 0, len(items))
	for _, item := range items {
		saved, ok := item.(map[string]any)
		if !ok {
			continue
		}
		album, _ := saved["album"].(map[string]any)
		if album == nil {
			continue
		}
		albums = append(albums, map[string]any{
			"name":    album["name"],
			"artists": extractNames(album, "artists"),
			"id":      album["id"],
			"uri":     album["uri"],
		})
	}
	return map[string]any{
		"albums": albums,
		"total":  raw["total"],
	}
}

func (c *SpotifyConnector) transformPlayer(raw map[string]any) map[string]any {
	if raw == nil || len(raw) == 0 {
		return map[string]any{"is_playing": false, "message": "No active playback"}
	}
	item, _ := raw["item"].(map[string]any)
	result := map[string]any{
		"is_playing": raw["is_playing"],
	}
	if item != nil {
		result["track"] = map[string]any{
			"name":    item["name"],
			"artists": extractNames(item, "artists"),
			"album":   extractNested(item, "album", "name"),
		}
	}
	if device, ok := raw["device"].(map[string]any); ok {
		result["device"] = map[string]any{
			"name":  device["name"],
			"type":  device["type"],
			"volume": device["volume_percent"],
		}
	}
	return result
}

func (c *SpotifyConnector) transformRecentlyPlayed(raw map[string]any) map[string]any {
	items, _ := raw["items"].([]any)
	tracks := make([]map[string]any, 0, len(items))
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		track, _ := entry["track"].(map[string]any)
		if track == nil {
			continue
		}
		tracks = append(tracks, map[string]any{
			"name":      track["name"],
			"artists":   extractNames(track, "artists"),
			"album":     extractNested(track, "album", "name"),
			"played_at": entry["played_at"],
		})
	}
	return map[string]any{"tracks": tracks}
}

func (c *SpotifyConnector) transformTrackList(raw map[string]any) map[string]any {
	items, _ := raw["items"].([]any)
	tracks := make([]map[string]any, 0, len(items))
	for _, item := range items {
		t, ok := item.(map[string]any)
		if !ok {
			continue
		}
		tracks = append(tracks, map[string]any{
			"name":    t["name"],
			"artists": extractNames(t, "artists"),
			"album":   extractNested(t, "album", "name"),
			"id":      t["id"],
			"uri":     t["uri"],
		})
	}
	return map[string]any{"tracks": tracks}
}

func (c *SpotifyConnector) transformArtistList(raw map[string]any) map[string]any {
	items, _ := raw["items"].([]any)
	artists := make([]map[string]any, 0, len(items))
	for _, item := range items {
		a, ok := item.(map[string]any)
		if !ok {
			continue
		}
		artists = append(artists, map[string]any{
			"name": a["name"],
			"id":   a["id"],
			"uri":  a["uri"],
			"genres": a["genres"],
		})
	}
	return map[string]any{"artists": artists}
}

func (c *SpotifyConnector) transformSearch(raw map[string]any) map[string]any {
	result := map[string]any{}
	for key, val := range raw {
		container, ok := val.(map[string]any)
		if !ok {
			continue
		}
		items, _ := container["items"].([]any)
		transformed := make([]map[string]any, 0, len(items))
		for _, item := range items {
			obj, ok := item.(map[string]any)
			if !ok {
				continue
			}
			entry := map[string]any{
				"name": obj["name"],
				"id":   obj["id"],
				"uri":  obj["uri"],
			}
			if obj["artists"] != nil {
				entry["artists"] = extractNames(obj, "artists")
			}
			if obj["album"] != nil {
				entry["album"] = extractNested(obj, "album", "name")
			}
			transformed = append(transformed, entry)
		}
		result[key] = transformed
	}
	return result
}

func (c *SpotifyConnector) transformPlaylistDetail(raw map[string]any) map[string]any {
	tracksObj, _ := raw["tracks"].(map[string]any)
	var tracks []map[string]any
	if tracksObj != nil {
		items, _ := tracksObj["items"].([]any)
		tracks = make([]map[string]any, 0, len(items))
		for _, item := range items {
			entry, ok := item.(map[string]any)
			if !ok {
				continue
			}
			track, _ := entry["track"].(map[string]any)
			if track == nil {
				continue
			}
			tracks = append(tracks, map[string]any{
				"name":    track["name"],
				"artists": extractNames(track, "artists"),
				"album":   extractNested(track, "album", "name"),
				"uri":     track["uri"],
			})
		}
	}
	return map[string]any{
		"name":        raw["name"],
		"description": raw["description"],
		"owner":       extractNested(raw, "owner", "display_name"),
		"tracks":      tracks,
		"total_tracks": len(tracks),
	}
}

func (c *SpotifyConnector) transformTrack(raw map[string]any) map[string]any {
	return map[string]any{
		"name":         raw["name"],
		"artists":      extractNames(raw, "artists"),
		"album":        extractNested(raw, "album", "name"),
		"duration_ms":  raw["duration_ms"],
		"popularity":   raw["popularity"],
		"preview_url":  raw["preview_url"],
		"id":           raw["id"],
		"uri":          raw["uri"],
	}
}

func (c *SpotifyConnector) transformArtist(raw map[string]any) map[string]any {
	return map[string]any{
		"name":      raw["name"],
		"genres":    raw["genres"],
		"followers": extractNested(raw, "followers", "total"),
		"popularity": raw["popularity"],
		"id":        raw["id"],
		"uri":       raw["uri"],
	}
}

func (c *SpotifyConnector) transformAlbum(raw map[string]any) map[string]any {
	tracksObj, _ := raw["tracks"].(map[string]any)
	var tracks []map[string]any
	if tracksObj != nil {
		items, _ := tracksObj["items"].([]any)
		tracks = make([]map[string]any, 0, len(items))
		for _, item := range items {
			t, ok := item.(map[string]any)
			if !ok {
				continue
			}
			tracks = append(tracks, map[string]any{
				"name":         t["name"],
				"track_number": t["track_number"],
				"duration_ms":  t["duration_ms"],
				"uri":          t["uri"],
			})
		}
	}
	return map[string]any{
		"name":         raw["name"],
		"artists":      extractNames(raw, "artists"),
		"release_date": raw["release_date"],
		"total_tracks": raw["total_tracks"],
		"tracks":       tracks,
		"id":           raw["id"],
		"uri":          raw["uri"],
	}
}

// simplify strips a raw Spotify response down to key fields.
func (c *SpotifyConnector) simplify(raw map[string]any) map[string]any {
	result := map[string]any{}
	// Keep common useful fields, drop metadata noise.
	keep := []string{"id", "name", "uri", "type", "display_name", "description",
		"is_playing", "total", "items", "tracks", "artists", "albums"}
	for _, k := range keep {
		if v, ok := raw[k]; ok {
			result[k] = v
		}
	}
	return result
}

// isPaginated returns true if the path points to a paginated Spotify endpoint.
func (c *SpotifyConnector) isPaginated(path string) bool {
	paginatedPaths := []string{
		"/me/playlists", "/me/tracks", "/me/albums",
		"/me/top/tracks", "/me/top/artists",
		"/me/player/recently-played",
	}
	for _, p := range paginatedPaths {
		if path == p {
			return true
		}
	}
	return false
}

// fetchAllPages follows Spotify's pagination `next` URLs and combines items.
func (c *SpotifyConnector) fetchAllPages(path, query string, firstPage any, token string, logger *slog.Logger) any {
	page, ok := firstPage.(map[string]any)
	if !ok {
		return firstPage
	}

	// Find the items key for this endpoint.
	itemsKey := c.itemsKeyForPath(path)
	if itemsKey == "" {
		return firstPage
	}

	allItems, _ := page[itemsKey].([]any)
	nextURL, _ := page["next"].(string)

	// Fetch up to 10 additional pages to prevent runaway.
	maxPages := 10
	for i := 0; i < maxPages && nextURL != ""; i++ {
		data, status, err := c.doSpotifyRequest("GET", nextURL, nil, token)
		if err != nil || status != http.StatusOK {
			logger.Warn("pagination fetch failed", "url", nextURL, "status", status, "error", err)
			break
		}
		var next map[string]any
		if err := json.Unmarshal(data, &next); err != nil {
			break
		}
		items, _ := next[itemsKey].([]any)
		allItems = append(allItems, items...)
		nextURL, _ = next["next"].(string)
	}

	page[itemsKey] = allItems
	delete(page, "next")
	delete(page, "previous")
	delete(page, "href")
	delete(page, "offset")
	delete(page, "limit")
	page["total_fetched"] = len(allItems)

	return page
}

func (c *SpotifyConnector) itemsKeyForPath(path string) string {
	switch path {
	case "/me/playlists":
		return "playlists"
	case "/me/tracks":
		return "tracks"
	case "/me/albums":
		return "albums"
	case "/me/top/tracks":
		return "tracks"
	case "/me/top/artists":
		return "artists"
	case "/me/player/recently-played":
		return "tracks"
	default:
		return ""
	}
}

// Helper functions for extracting nested Spotify response fields.

func extractNames(obj map[string]any, key string) []string {
	arr, ok := obj[key].([]any)
	if !ok {
		return nil
	}
	names := make([]string, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if name, ok := m["name"].(string); ok {
			names = append(names, name)
		}
	}
	return names
}

func extractNested(obj map[string]any, keys ...string) any {
	current := obj
	for _, key := range keys[:len(keys)-1] {
		next, ok := current[key].(map[string]any)
		if !ok {
			return nil
		}
		current = next
	}
	return current[keys[len(keys)-1]]
}
