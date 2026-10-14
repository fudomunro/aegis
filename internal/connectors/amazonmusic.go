package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

// AmazonMusicConnector provides proxying for Amazon Music.
// Amazon Music does not currently have a public REST API, so this
// connector uses a configurable upstream endpoint and applies
// generic response transformations. It is ready to wire up to
// any Amazon Music API (official or unofficial) when available.
type AmazonMusicConnector struct {
	host Host
}

// NewAmazonMusicConnector creates a connector wired to the given Host (issue #15 seam).
// amazonMusicAPIBase is the default Amazon Music API root; config endpoint overrides it.
const amazonMusicAPIBase = "https://api.amazonmusic.com/v1"

func NewAmazonMusicConnector(host Host) *AmazonMusicConnector {
	return &AmazonMusicConnector{host: host}
}

// Capabilities returns what Amazon Music data types are recognized.
// Amazon Music has no public API, so no operations are currently supported.
func (c *AmazonMusicConnector) Capabilities() types.ServiceCapabilities {
	return types.ServiceCapabilities{
		DataTypes: []types.DataTypeCapability{
			{Name: "user", Description: "Current user profile", Operations: []types.Operation{}},
			{Name: "tracks", Description: "Individual songs", Operations: []types.Operation{}},
			{Name: "albums", Description: "Albums", Operations: []types.Operation{}},
			{Name: "artists", Description: "Artists", Operations: []types.Operation{}},
			{Name: "playlists", Description: "User playlists", Operations: []types.Operation{}},
			{Name: "search", Description: "Catalog search", Operations: []types.Operation{}},
			{Name: "library", Description: "User's saved music", Operations: []types.Operation{}},
			{Name: "recommendations", Description: "Personalized suggestions", Operations: []types.Operation{}},
		},
	}
}

// SystemInstructions returns the system message injected into LLM requests
// when Amazon Music is referenced.
func (c *AmazonMusicConnector) SystemInstructions(baseURL string) string {
	root := strings.TrimRight(baseURL, "/")
	base := root + "/v1/services/amazon-music"
	return fmt.Sprintf(`When the user's request involves Amazon Music, you MUST use the Aegis proxy at %s instead of calling Amazon Music directly.

For "my playlists" across every connected music service (Spotify, YouTube Music, Amazon Music, …), prefer the unified library endpoint first:
GET %s/v1/library/playlists — all user playlists in one neutral list (synced across services; service IDs live in sources[])

Amazon Music-specific endpoints (call the full proxy URL; Aegis authenticates you — do not send Amazon tokens or an API key unless your deployment uses api-key mode):

GET /me — Current user profile
GET /me/playlists — List user's playlists in neutral format
GET /me/library — List saved/liked tracks and albums
GET /me/recommendations — Personalized recommendations
GET /search?q=QUERY — Search Amazon Music catalog
GET /playlists/{id} — Get a playlist by ID with tracks
GET /tracks/{id} — Get track details by ID
GET /albums/{id} — Get album details by ID
GET /artists/{id} — Get artist details by ID

Note: Amazon Music API availability may vary. If an endpoint returns
an error, inform the user that the feature may not be available.
Playlist responses use Aegis's neutral fields: name, description, owner, track_count, sources[] (service, service_name, id, url, extra).
All responses are transformed to a compact, LLM-friendly format.
Always include the full proxy base URL in your requests.`, base, root)
}

// HandleProxy processes an Amazon Music API request through the connector.
func (c *AmazonMusicConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	RunProxy(c.host, w, r, remainingPath, ProxyOpts{
		serviceID:   "amazon-music",
		displayName: "Amazon Music",
		defaultBase: amazonMusicAPIBase,
		defaultTTL:  60 * time.Second,
		transform:   c.TransformResponse,
	})
}

// TransformResponse converts Amazon Music's JSON into a compact format.
// Since Amazon Music doesn't have a documented public API, this uses
// generic transformation patterns that work with common API response
// structures. The transforms will be refined when a real API is wired up.
func (c *AmazonMusicConnector) TransformResponse(path string, data []byte, logger *slog.Logger) any {
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
		return c.TransformUser(raw)
	case path == "/me/playlists":
		return c.TransformPlaylistList(raw)
	case path == "/me/library":
		return c.TransformLibrary(raw)
	case path == "/me/recommendations":
		return c.TransformRecommendations(raw)
	case path == "/search":
		return c.TransformSearch(raw)
	case strings.HasPrefix(path, "/playlists/"):
		return c.TransformPlaylistDetail(raw)
	case strings.HasPrefix(path, "/tracks/"):
		return c.TransformTrack(raw)
	case strings.HasPrefix(path, "/albums/"):
		return c.TransformAlbum(raw)
	case strings.HasPrefix(path, "/artists/"):
		return c.TransformArtist(raw)
	default:
		return c.Simplify(raw)
	}
}

func (c *AmazonMusicConnector) TransformUser(raw map[string]any) map[string]any {
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

func AsInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}

// ListUserPlaylists implements PlaylistProvider. Fetches Amazon Music playlists
// and returns them in Aegis's neutral format.
func (c *AmazonMusicConnector) ListUserPlaylists(ctx context.Context) ([]types.Playlist, bool, error) {
	token := c.host.GetValidToken("amazon-music", slog.Default())
	if token == "" {
		return nil, false, types.ErrUnauthorized
	}

	upstreamBase := c.host.UpstreamBaseFor("amazon-music", amazonMusicAPIBase)

	nextURL := upstreamBase + "/me/playlists"
	var all []types.Playlist
	truncated := false
	for page := 0; page < LibraryPageCap && nextURL != ""; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, false, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := HTTPClient.Do(req)
		if err != nil {
			return nil, false, err
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, false, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, false, fmt.Errorf("amazon music playlists returned %d", resp.StatusCode)
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, false, err
		}
		items := c.ExtractItems(raw)
		for _, item := range items {
			p, ok := item.(map[string]any)
			if !ok {
				continue
			}
			all = append(all, c.NeutralPlaylist(p))
		}
		nextURL, _ = raw["next"].(string)
		if nextURL == "" {
			if tokenVal, ok := raw["nextToken"].(string); ok && tokenVal != "" {
				// Continuation token exists but the documented API shape for
				// following it is unknown — report incomplete rather than
				// silently claiming this is the full library.
				truncated = true
				break
			}
		}
	}
	if nextURL != "" {
		truncated = true
	}
	return all, truncated, nil
}

// neutralPlaylist maps a raw Amazon Music playlist object into the neutral type.
// Service identity lives in Sources.
func (c *AmazonMusicConnector) NeutralPlaylist(p map[string]any) types.Playlist {
	id, _ := p["id"].(string)
	name, _ := c.FirstNonNil(p, "name", "title").(string)
	description, _ := p["description"].(string)

	trackCount := 0
	if n, ok := AsInt(c.FirstNonNil(p, "track_count", "item_count", "total_tracks", "size")); ok {
		trackCount = n
	}

	owner := ""
	if o, ok := p["owner"].(map[string]any); ok {
		owner, _ = o["name"].(string)
	}
	if owner == "" {
		owner, _ = c.FirstNonNil(p, "owner", "ownerName", "owner_name").(string)
	}

	extra := map[string]any{}
	for _, k := range []string{"urn", "asin", "customerId", "privacy"} {
		if v, ok := p[k]; ok && v != nil {
			extra[k] = v
		}
	}

	return types.Playlist{
		Name:        name,
		Description: description,
		Owner:       owner,
		TrackCount:  trackCount,
		Sources: []types.PlaylistSource{
			types.PrimaryPlaylistSource("amazon-music", "Amazon Music", id, "", extra),
		},
	}
}

// neutralAmazonTrack maps a raw Amazon Music track object into the neutral type.
func (c *AmazonMusicConnector) NeutralAmazonTrack(t map[string]any) types.Track {
	id, _ := t["id"].(string)
	name, _ := c.FirstNonNil(t, "name", "title").(string)
	artist := c.ExtractArtistName(t)
	album := c.ExtractAlbumName(t)
	durationMs, _ := AsInt(t["duration_ms"])
	duration, _ := t["duration"].(string)

	extra := map[string]any{}
	for _, k := range []string{"asin", "urn", "popularity"} {
		if v, ok := t[k]; ok && v != nil {
			extra[k] = v
		}
	}
	isrc := ""
	if v, ok := t["isrc"].(string); ok {
		isrc = strings.ToUpper(strings.TrimSpace(v))
		if isrc != "" {
			extra["isrc"] = isrc
		}
	}

	var artists []types.Artist
	if artist != "" {
		artists = []types.Artist{types.NewArtist("amazon-music", "Amazon Music", "", artist)}
	}

	return types.Track{
		Name:       name,
		Artists:    artists,
		Album:      types.NewAlbum("amazon-music", "Amazon Music", "", album),
		DurationMs: durationMs,
		Duration:   duration,
		EntityID:   isrc,
		Sources: []types.TrackSource{{
			Service:     "amazon-music",
			ServiceName: "Amazon Music",
			ID:          id,
			Extra:       ExtraOrNil(extra),
		}},
	}
}

func (c *AmazonMusicConnector) TransformPlaylistList(raw map[string]any) types.PlaylistList {
	items := c.ExtractItems(raw)
	playlists := make([]types.Playlist, 0, len(items))
	for _, item := range items {
		p, ok := item.(map[string]any)
		if !ok {
			continue
		}
		playlists = append(playlists, c.NeutralPlaylist(p))
	}
	total := len(playlists)
	if n, ok := AsInt(c.TotalCount(raw)); ok {
		total = n
	}
	return types.PlaylistList{Playlists: playlists, Total: total}
}

func (c *AmazonMusicConnector) TransformLibrary(raw map[string]any) map[string]any {
	items := c.ExtractItems(raw)
	tracks := make([]map[string]any, 0, len(items))
	for _, item := range items {
		t, ok := item.(map[string]any)
		if !ok {
			continue
		}
		tracks = append(tracks, map[string]any{
			"id":     t["id"],
			"name":   c.FirstNonNil(t, "name", "title"),
			"artist": c.ExtractArtistName(t),
			"album":  c.ExtractAlbumName(t),
		})
	}
	return map[string]any{
		"tracks": tracks,
		"total":  c.TotalCount(raw),
	}
}

func (c *AmazonMusicConnector) TransformRecommendations(raw map[string]any) map[string]any {
	items := c.ExtractItems(raw)
	tracks := make([]map[string]any, 0, len(items))
	for _, item := range items {
		t, ok := item.(map[string]any)
		if !ok {
			continue
		}
		tracks = append(tracks, map[string]any{
			"id":     t["id"],
			"name":   c.FirstNonNil(t, "name", "title"),
			"artist": c.ExtractArtistName(t),
			"album":  c.ExtractAlbumName(t),
		})
	}
	return map[string]any{"recommendations": tracks}
}

func (c *AmazonMusicConnector) TransformSearch(raw map[string]any) map[string]any {
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
				"name": c.FirstNonNil(obj, "name", "title"),
			}
			if artist := c.ExtractArtistName(obj); artist != "" {
				entry["artist"] = artist
			}
			if album := c.ExtractAlbumName(obj); album != "" {
				entry["album"] = album
			}
			transformed = append(transformed, entry)
		}
		result[key] = transformed
	}

	// If the response is a flat list (no categories), transform as tracks.
	if len(result) == 0 {
		items := c.ExtractItems(raw)
		tracks := make([]map[string]any, 0, len(items))
		for _, item := range items {
			t, ok := item.(map[string]any)
			if !ok {
				continue
			}
			tracks = append(tracks, map[string]any{
				"id":     t["id"],
				"name":   c.FirstNonNil(t, "name", "title"),
				"artist": c.ExtractArtistName(t),
				"album":  c.ExtractAlbumName(t),
			})
		}
		result["results"] = tracks
	}

	return result
}

func (c *AmazonMusicConnector) TransformPlaylistDetail(raw map[string]any) types.PlaylistDetail {
	base := c.NeutralPlaylist(raw)

	var tracks []types.Track
	if tracksObj, ok := raw["tracks"].(map[string]any); ok {
		items := c.ExtractItemsFromMap(tracksObj)
		tracks = make([]types.Track, 0, len(items))
		for _, item := range items {
			t, ok := item.(map[string]any)
			if !ok {
				continue
			}
			tracks = append(tracks, c.NeutralAmazonTrack(t))
		}
	} else if items, ok := raw["items"].([]any); ok {
		tracks = make([]types.Track, 0, len(items))
		for _, item := range items {
			t, ok := item.(map[string]any)
			if !ok {
				continue
			}
			tracks = append(tracks, c.NeutralAmazonTrack(t))
		}
	}

	return types.PlaylistDetail{
		Playlist:    base,
		Tracks:      tracks,
		TotalTracks: base.TrackCount,
	}
}

func (c *AmazonMusicConnector) TransformTrack(raw map[string]any) map[string]any {
	// Handle both single-track response and list-with-id response.
	if items := c.ExtractItems(raw); len(items) > 0 {
		if t, ok := items[0].(map[string]any); ok {
			return map[string]any{
				"id":          t["id"],
				"name":        c.FirstNonNil(t, "name", "title"),
				"artist":      c.ExtractArtistName(t),
				"album":       c.ExtractAlbumName(t),
				"duration_ms": t["duration_ms"],
				"duration":    t["duration"],
			}
		}
	}
	return map[string]any{
		"id":          raw["id"],
		"name":        c.FirstNonNil(raw, "name", "title"),
		"artist":      c.ExtractArtistName(raw),
		"album":       c.ExtractAlbumName(raw),
		"duration_ms": raw["duration_ms"],
		"duration":    raw["duration"],
	}
}

func (c *AmazonMusicConnector) TransformAlbum(raw map[string]any) map[string]any {
	result := map[string]any{
		"id":           raw["id"],
		"name":         c.FirstNonNil(raw, "name", "title"),
		"artist":       c.ExtractArtistName(raw),
		"release_date": c.FirstNonNil(raw, "release_date", "released_at"),
		"track_count":  c.FirstNonNil(raw, "track_count", "total_tracks"),
	}

	if items := c.ExtractItems(raw); len(items) > 0 {
		tracks := make([]map[string]any, 0, len(items))
		for _, item := range items {
			t, ok := item.(map[string]any)
			if !ok {
				continue
			}
			tracks = append(tracks, map[string]any{
				"name":         c.FirstNonNil(t, "name", "title"),
				"track_number": t["track_number"],
				"duration_ms":  t["duration_ms"],
			})
		}
		result["tracks"] = tracks
	}

	return result
}

func (c *AmazonMusicConnector) TransformArtist(raw map[string]any) map[string]any {
	return map[string]any{
		"id":         raw["id"],
		"name":       c.FirstNonNil(raw, "name", "title"),
		"genres":     raw["genres"],
		"popularity": raw["popularity"],
	}
}

// simplify strips a raw response down to key fields.
func (c *AmazonMusicConnector) Simplify(raw map[string]any) map[string]any {
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
func (c *AmazonMusicConnector) ExtractItems(raw map[string]any) []any {
	for _, key := range []string{"items", "results", "data", "list"} {
		if items, ok := raw[key].([]any); ok {
			return items
		}
	}
	return nil
}

func (c *AmazonMusicConnector) ExtractItemsFromMap(m map[string]any) []any {
	return c.ExtractItems(m)
}

func (c *AmazonMusicConnector) TotalCount(raw map[string]any) any {
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
func (c *AmazonMusicConnector) ExtractArtistName(obj map[string]any) string {
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
func (c *AmazonMusicConnector) ExtractAlbumName(obj map[string]any) string {
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
func (c *AmazonMusicConnector) FirstNonNil(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			return v
		}
	}
	return nil
}
