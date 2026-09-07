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

// AmazonMusicConnector provides proxying for Amazon Music.
// Amazon Music does not currently have a public REST API, so this
// connector uses a configurable upstream endpoint and applies
// generic response transformations. It is ready to wire up to
// any Amazon Music API (official or unofficial) when available.
type AmazonMusicConnector struct {
	server *Server
}

// NewAmazonMusicConnector creates a connector wired to the given server.
func NewAmazonMusicConnector(srv *Server) *AmazonMusicConnector {
	return &AmazonMusicConnector{server: srv}
}

// Capabilities returns what Amazon Music data types are recognized.
// Amazon Music has no public API, so no operations are currently supported.
func (c *AmazonMusicConnector) Capabilities() ServiceCapabilities {
	return ServiceCapabilities{
		DataTypes: []DataTypeCapability{
			{Name: "user", Description: "Current user profile", Operations: nil},
			{Name: "tracks", Description: "Individual songs", Operations: nil},
			{Name: "albums", Description: "Albums", Operations: nil},
			{Name: "artists", Description: "Artists", Operations: nil},
			{Name: "playlists", Description: "User playlists", Operations: nil},
			{Name: "search", Description: "Catalog search", Operations: nil},
			{Name: "library", Description: "User's saved music", Operations: nil},
			{Name: "recommendations", Description: "Personalized suggestions", Operations: nil},
		},
	}
}

// SystemInstructions returns the system message injected into LLM requests
// when Amazon Music is referenced.
func (c *AmazonMusicConnector) SystemInstructions(baseURL string) string {
	base := strings.TrimRight(baseURL, "/") + "/v1/services/amazon-music"
	return fmt.Sprintf(`When the user's request involves Amazon Music, you MUST use the Guardian proxy at %s instead of calling Amazon Music directly.

Available endpoints (all require Authorization header from Guardian):

GET /me — Current user profile
GET /me/playlists — List user's playlists
GET /me/library — List saved/liked tracks and albums
GET /me/recommendations — Personalized recommendations
GET /search?q=QUERY — Search Amazon Music catalog
GET /playlists/{id} — Get a playlist by ID with tracks
GET /tracks/{id} — Get track details by ID
GET /albums/{id} — Get album details by ID
GET /artists/{id} — Get artist details by ID

Note: Amazon Music API availability may vary. If an endpoint returns
an error, inform the user that the feature may not be available.
All responses are transformed to a compact, LLM-friendly format.
Always include the full proxy base URL in your requests.`, base)
}

// HandleProxy processes an Amazon Music API request through the connector.
func (c *AmazonMusicConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	logger := slog.With("service", "amazon-music", "method", r.Method, "path", remainingPath)

	token := c.server.getValidToken("amazon-music", logger)
	if token == "" {
		writeJSONError(w, http.StatusUnauthorized, "no valid Amazon Music token — authorize at /v1/auth/amazon-music/login")
		return
	}

	// Build the upstream URL.
	svc := c.server.config.GetService("amazon-music")
	upstreamBase := "https://api.amazonmusic.com/v1"
	if svc != nil && svc.Endpoint != "" {
		upstreamBase = strings.TrimRight(svc.Endpoint, "/")
	}
	upstreamURL := upstreamBase + remainingPath
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}

	// Cache check.
	cacheKey := "amazon-music:" + r.URL.RequestURI()
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
	data, status, err := c.doAmazonRequest(r.Method, upstreamURL, r.Body, token)
	if err != nil {
		logger.Error("amazon music request failed", "error", err)
		writeJSONError(w, http.StatusBadGateway, "Amazon Music API request failed")
		return
	}

	logger.Debug("amazon music response", "status", status, "bytes", len(data))

	if status == http.StatusUnauthorized {
		logger.Warn("amazon music returned 401, token may be revoked")
		writeJSONError(w, http.StatusUnauthorized, "Amazon Music token is invalid or expired — re-authorize at /v1/auth/amazon-music/login")
		return
	}

	if status == http.StatusTooManyRequests {
		logger.Warn("amazon music rate limited")
		w.Header().Set("Retry-After", "30")
		writeJSONError(w, http.StatusTooManyRequests, "Amazon Music rate limit exceeded — retry later")
		return
	}

	// Transform the response.
	transformed := c.transformResponse(remainingPath, data, logger)

	result, err := json.Marshal(transformed)
	if err != nil {
		logger.Error("failed to marshal transformed response", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "response transformation failed")
		return
	}

	// Cache the response.
	if r.Method == http.MethodGet {
		ttl := 60 * time.Second
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

// doAmazonRequest executes an HTTP request to the Amazon Music API.
func (c *AmazonMusicConnector) doAmazonRequest(method, url string, body io.Reader, token string) ([]byte, int, error) {
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

// transformResponse converts Amazon Music's JSON into a compact format.
// Since Amazon Music doesn't have a documented public API, this uses
// generic transformation patterns that work with common API response
// structures. The transforms will be refined when a real API is wired up.
func (c *AmazonMusicConnector) transformResponse(path string, data []byte, logger *slog.Logger) any {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		// Try as array.
		var arr []any
		if err2 := json.Unmarshal(data, &arr); err2 == nil {
			return arr
		}
		return string(data)
	}

	// Pass through errors.
	if _, ok := raw["error"]; ok {
		return raw
	}

	switch {
	case path == "/me":
		return c.transformUser(raw)
	case path == "/me/playlists":
		return c.transformPlaylistList(raw)
	case path == "/me/library":
		return c.transformLibrary(raw)
	case path == "/me/recommendations":
		return c.transformRecommendations(raw)
	case path == "/search":
		return c.transformSearch(raw)
	case strings.HasPrefix(path, "/playlists/"):
		return c.transformPlaylistDetail(raw)
	case strings.HasPrefix(path, "/tracks/"):
		return c.transformTrack(raw)
	case strings.HasPrefix(path, "/albums/"):
		return c.transformAlbum(raw)
	case strings.HasPrefix(path, "/artists/"):
		return c.transformArtist(raw)
	default:
		return c.simplify(raw)
	}
}

func (c *AmazonMusicConnector) transformUser(raw map[string]any) map[string]any {
	result := map[string]any{}
	for _, k := range []string{"id", "name", "email", "customer_id"} {
		if v, ok := raw[k]; ok {
			result[k] = v
		}
	}
	if len(result) == 0 {
		// Try common alternative field names.
		if v, ok := raw["display_name"]; ok {
			result["name"] = v
		}
		if v, ok := raw["user_id"]; ok {
			result["id"] = v
		}
	}
	return result
}

func (c *AmazonMusicConnector) transformPlaylistList(raw map[string]any) map[string]any {
	items := c.extractItems(raw)
	playlists := make([]map[string]any, 0, len(items))
	for _, item := range items {
		p, ok := item.(map[string]any)
		if !ok {
			continue
		}
		playlists = append(playlists, map[string]any{
			"id":          p["id"],
			"name":        c.firstNonNil(p, "name", "title"),
			"description": p["description"],
			"track_count": c.firstNonNil(p, "track_count", "item_count", "total_tracks", "size"),
		})
	}
	return map[string]any{
		"playlists": playlists,
		"total":     c.totalCount(raw),
	}
}

func (c *AmazonMusicConnector) transformLibrary(raw map[string]any) map[string]any {
	items := c.extractItems(raw)
	tracks := make([]map[string]any, 0, len(items))
	for _, item := range items {
		t, ok := item.(map[string]any)
		if !ok {
			continue
		}
		tracks = append(tracks, map[string]any{
			"id":      t["id"],
			"name":    c.firstNonNil(t, "name", "title"),
			"artist":  c.extractArtistName(t),
			"album":   c.extractAlbumName(t),
		})
	}
	return map[string]any{
		"tracks": tracks,
		"total":  c.totalCount(raw),
	}
}

func (c *AmazonMusicConnector) transformRecommendations(raw map[string]any) map[string]any {
	items := c.extractItems(raw)
	tracks := make([]map[string]any, 0, len(items))
	for _, item := range items {
		t, ok := item.(map[string]any)
		if !ok {
			continue
		}
		tracks = append(tracks, map[string]any{
			"id":     t["id"],
			"name":   c.firstNonNil(t, "name", "title"),
			"artist": c.extractArtistName(t),
			"album":  c.extractAlbumName(t),
		})
	}
	return map[string]any{"recommendations": tracks}
}

func (c *AmazonMusicConnector) transformSearch(raw map[string]any) map[string]any {
	// Handle top-level result categories (tracks, albums, artists, playlists).
	result := map[string]any{}
	for key, val := range raw {
		if key == "nextToken" || key == "pageToken" || key == "pageInfo" {
			continue
		}
		container, ok := val.(map[string]any)
		if !ok {
			continue
		}
		items, _ := container["items"].([]any)
		if items == nil {
			items, _ = container["results"].([]any)
		}
		transformed := make([]map[string]any, 0, len(items))
		for _, item := range items {
			obj, ok := item.(map[string]any)
			if !ok {
				continue
			}
			entry := map[string]any{
				"id":   obj["id"],
				"name": c.firstNonNil(obj, "name", "title"),
			}
			if artist := c.extractArtistName(obj); artist != "" {
				entry["artist"] = artist
			}
			if album := c.extractAlbumName(obj); album != "" {
				entry["album"] = album
			}
			transformed = append(transformed, entry)
		}
		result[key] = transformed
	}

	// If the response is a flat list (no categories), transform as tracks.
	if len(result) == 0 {
		items := c.extractItems(raw)
		tracks := make([]map[string]any, 0, len(items))
		for _, item := range items {
			t, ok := item.(map[string]any)
			if !ok {
				continue
			}
			tracks = append(tracks, map[string]any{
				"id":     t["id"],
				"name":   c.firstNonNil(t, "name", "title"),
				"artist": c.extractArtistName(t),
				"album":  c.extractAlbumName(t),
			})
		}
		result["results"] = tracks
	}

	return result
}

func (c *AmazonMusicConnector) transformPlaylistDetail(raw map[string]any) map[string]any {
	result := map[string]any{
		"id":          raw["id"],
		"name":        c.firstNonNil(raw, "name", "title"),
		"description": raw["description"],
		"track_count": c.firstNonNil(raw, "track_count", "item_count", "total_tracks", "size"),
	}

	// Transform tracks if present.
	if tracksObj, ok := raw["tracks"].(map[string]any); ok {
		items := c.extractItemsFromMap(tracksObj)
		tracks := make([]map[string]any, 0, len(items))
		for _, item := range items {
			t, ok := item.(map[string]any)
			if !ok {
				continue
			}
			tracks = append(tracks, map[string]any{
				"id":     t["id"],
				"name":   c.firstNonNil(t, "name", "title"),
				"artist": c.extractArtistName(t),
				"album":  c.extractAlbumName(t),
			})
		}
		result["tracks"] = tracks
	} else if items, ok := raw["items"].([]any); ok {
		tracks := make([]map[string]any, 0, len(items))
		for _, item := range items {
			t, ok := item.(map[string]any)
			if !ok {
				continue
			}
			tracks = append(tracks, map[string]any{
				"id":     t["id"],
				"name":   c.firstNonNil(t, "name", "title"),
				"artist": c.extractArtistName(t),
			})
		}
		result["tracks"] = tracks
	}

	return result
}

func (c *AmazonMusicConnector) transformTrack(raw map[string]any) map[string]any {
	// Handle both single-track response and list-with-id response.
	if items := c.extractItems(raw); len(items) > 0 {
		if t, ok := items[0].(map[string]any); ok {
			return map[string]any{
				"id":           t["id"],
				"name":         c.firstNonNil(t, "name", "title"),
				"artist":       c.extractArtistName(t),
				"album":        c.extractAlbumName(t),
				"duration_ms":  t["duration_ms"],
				"duration":     t["duration"],
			}
		}
	}
	return map[string]any{
		"id":          raw["id"],
		"name":        c.firstNonNil(raw, "name", "title"),
		"artist":      c.extractArtistName(raw),
		"album":       c.extractAlbumName(raw),
		"duration_ms": raw["duration_ms"],
		"duration":    raw["duration"],
	}
}

func (c *AmazonMusicConnector) transformAlbum(raw map[string]any) map[string]any {
	result := map[string]any{
		"id":           raw["id"],
		"name":         c.firstNonNil(raw, "name", "title"),
		"artist":       c.extractArtistName(raw),
		"release_date": c.firstNonNil(raw, "release_date", "released_at"),
		"track_count":  c.firstNonNil(raw, "track_count", "total_tracks"),
	}

	if items := c.extractItems(raw); len(items) > 0 {
		tracks := make([]map[string]any, 0, len(items))
		for _, item := range items {
			t, ok := item.(map[string]any)
			if !ok {
				continue
			}
			tracks = append(tracks, map[string]any{
				"name":        c.firstNonNil(t, "name", "title"),
				"track_number": t["track_number"],
				"duration_ms": t["duration_ms"],
			})
		}
		result["tracks"] = tracks
	}

	return result
}

func (c *AmazonMusicConnector) transformArtist(raw map[string]any) map[string]any {
	return map[string]any{
		"id":         raw["id"],
		"name":       c.firstNonNil(raw, "name", "title"),
		"genres":     raw["genres"],
		"popularity": raw["popularity"],
	}
}

// simplify strips a raw response down to key fields.
func (c *AmazonMusicConnector) simplify(raw map[string]any) map[string]any {
	result := map[string]any{}
	keep := []string{"id", "name", "title", "description", "items", "results",
		"total", "nextToken", "pageToken", "pageInfo"}
	for _, k := range keep {
		if v, ok := raw[k]; ok {
			result[k] = v
		}
	}
	return result
}

// Helper functions for extracting data from Amazon Music's response format.

// extractItems tries multiple common keys for item arrays.
func (c *AmazonMusicConnector) extractItems(raw map[string]any) []any {
	for _, key := range []string{"items", "results", "data", "list"} {
		if items, ok := raw[key].([]any); ok {
			return items
		}
	}
	return nil
}

func (c *AmazonMusicConnector) extractItemsFromMap(m map[string]any) []any {
	return c.extractItems(m)
}

func (c *AmazonMusicConnector) totalCount(raw map[string]any) any {
	for _, key := range []string{"total", "totalResults", "count", "pageInfo"} {
		if v, ok := raw[key]; ok {
			if pageInfo, ok := v.(map[string]any); ok {
				return pageInfo["totalResults"]
			}
			return v
		}
	}
	return nil
}

// extractArtistName extracts the artist name from various response formats.
func (c *AmazonMusicConnector) extractArtistName(obj map[string]any) string {
	// Direct string field.
	for _, key := range []string{"artist", "artistName", "artist_name"} {
		if s, ok := obj[key].(string); ok {
			return s
		}
	}
	// Nested object.
	for _, key := range []string{"artist", "artists"} {
		if a, ok := obj[key].(map[string]any); ok {
			if name, ok := a["name"].(string); ok {
				return name
			}
		}
		// Array of artists.
		if arr, ok := obj[key].([]any); ok && len(arr) > 0 {
			if first, ok := arr[0].(map[string]any); ok {
				if name, ok := first["name"].(string); ok {
					return name
				}
			}
		}
	}
	return ""
}

// extractAlbumName extracts the album name from various response formats.
func (c *AmazonMusicConnector) extractAlbumName(obj map[string]any) string {
	for _, key := range []string{"album", "albumName", "album_name"} {
		if s, ok := obj[key].(string); ok {
			return s
		}
	}
	if a, ok := obj["album"].(map[string]any); ok {
		if name, ok := a["name"].(string); ok {
			return name
		}
	}
	return ""
}

// firstNonNil returns the value of the first key that has a non-nil value.
func (c *AmazonMusicConnector) firstNonNil(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			return v
		}
	}
	return nil
}
