package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
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

// Capabilities returns what Spotify data types and operations are supported.
func (c *SpotifyConnector) Capabilities() ServiceCapabilities {
	return ServiceCapabilities{
		DataTypes: []DataTypeCapability{
			{
				Name:        "user",
				Description: "Current user profile",
				Operations: []Operation{
					{Name: "read", Description: "Get current user's profile", Method: "GET", Path: "/me",
						Response: ResponseSpec{Description: "User profile with id, display_name, uri", Fields: []string{"id", "display_name", "uri"}}},
				},
			},
			{
				Name:        "tracks",
				Description: "Individual songs — saved tracks, top tracks, search results",
				Operations: []Operation{
					{Name: "read_saved", Description: "List user's saved/liked tracks (paginated, auto-fetches all pages)", Method: "GET", Path: "/me/tracks",
						Parameters: []Parameter{
							{Name: "limit", In: "query", Type: "integer", Description: "Max tracks per page (1-50)", Default: 20},
							{Name: "offset", In: "query", Type: "integer", Description: "Index of first track to return", Default: 0},
						},
						Response: ResponseSpec{Description: "Saved tracks with name, artists, album, id, uri", Fields: []string{"tracks", "total"}}},
					{Name: "read_top", Description: "List user's top tracks by time range", Method: "GET", Path: "/me/top/tracks",
						Parameters: []Parameter{
							{Name: "time_range", In: "query", Type: "string", Description: "short_term (4 weeks), medium_term (6 months), or long_term (years)", Default: "medium_term"},
							{Name: "limit", In: "query", Type: "integer", Description: "Max tracks (1-50)", Default: 20},
						},
						Response: ResponseSpec{Description: "Top tracks with name, artists, album, id", Fields: []string{"tracks"}}},
					{Name: "get_by_id", Description: "Get a track by its Spotify ID", Method: "GET", Path: "/tracks/{id}",
						Parameters: []Parameter{
							{Name: "id", In: "path", Type: "string", Description: "Spotify track ID", Required: true},
						},
						Response: ResponseSpec{Description: "Track details with name, artists, album, duration, popularity", Fields: []string{"name", "artists", "album", "duration_ms", "popularity", "id", "uri"}}},
				},
			},
			{
				Name:        "albums",
				Description: "Albums — saved albums, album details",
				Operations: []Operation{
					{Name: "read_saved", Description: "List user's saved albums (paginated)", Method: "GET", Path: "/me/albums",
						Parameters: []Parameter{
							{Name: "limit", In: "query", Type: "integer", Description: "Max albums per page (1-50)", Default: 20},
						},
						Response: ResponseSpec{Description: "Saved albums with name, artists, id", Fields: []string{"albums", "total"}}},
					{Name: "get_by_id", Description: "Get an album by its Spotify ID", Method: "GET", Path: "/albums/{id}",
						Parameters: []Parameter{
							{Name: "id", In: "path", Type: "string", Description: "Spotify album ID", Required: true},
						},
						Response: ResponseSpec{Description: "Album with tracks, artists, release date", Fields: []string{"name", "artists", "tracks", "release_date", "total_tracks"}}},
				},
			},
			{
				Name:        "artists",
				Description: "Artists — followed artists, top tracks, related artists",
				Operations: []Operation{
					{Name: "read_top", Description: "List user's top artists by time range", Method: "GET", Path: "/me/top/artists",
						Parameters: []Parameter{
							{Name: "time_range", In: "query", Type: "string", Description: "short_term, medium_term, or long_term", Default: "medium_term"},
						},
						Response: ResponseSpec{Description: "Top artists with name, id, genres", Fields: []string{"artists"}}},
					{Name: "get_by_id", Description: "Get an artist by its Spotify ID", Method: "GET", Path: "/artists/{id}",
						Parameters: []Parameter{
							{Name: "id", In: "path", Type: "string", Description: "Spotify artist ID", Required: true},
						},
						Response: ResponseSpec{Description: "Artist with name, genres, followers, popularity", Fields: []string{"name", "genres", "followers", "popularity"}}},
					{Name: "get_top_tracks", Description: "Get an artist's top tracks", Method: "GET", Path: "/artists/{id}/top-tracks",
						Parameters: []Parameter{
							{Name: "id", In: "path", Type: "string", Description: "Spotify artist ID", Required: true},
						},
						Response: ResponseSpec{Description: "Artist's top tracks", Fields: []string{"tracks"}}},
				},
			},
			{
				Name:        "playlists",
				Description: "User playlists — list, detail with tracks",
				Operations: []Operation{
					{Name: "read_list", Description: "List user's playlists (paginated)", Method: "GET", Path: "/me/playlists",
						Parameters: []Parameter{
							{Name: "limit", In: "query", Type: "integer", Description: "Max playlists per page (1-50)", Default: 20},
						},
						Response: ResponseSpec{Description: "Neutral playlist list: name, description, owner, track_count, sources[] (service, service_name, id, url, extra)", Fields: []string{"playlists", "total"}}},
					{Name: "get_by_id", Description: "Get a playlist by ID with its tracks", Method: "GET", Path: "/playlists/{id}",
						Parameters: []Parameter{
							{Name: "id", In: "path", Type: "string", Description: "Spotify playlist ID", Required: true},
						},
						Response: ResponseSpec{Description: "Neutral playlist detail: name, description, owner, track_count, sources[], tracks", Fields: []string{"name", "description", "owner", "track_count", "sources", "tracks", "extra"}}},
				},
			},
			{
				Name:        "search",
				Description: "Search the Spotify catalog",
				Operations: []Operation{
					{Name: "search", Description: "Search for tracks, albums, artists, or playlists", Method: "GET", Path: "/search",
						Parameters: []Parameter{
							{Name: "q", In: "query", Type: "string", Description: "Search query", Required: true},
							{Name: "type", In: "query", Type: "string", Description: "Comma-separated types: track, album, artist, playlist", Default: "track"},
							{Name: "limit", In: "query", Type: "integer", Description: "Max results per type (1-50)", Default: 10},
						},
						Response: ResponseSpec{Description: "Search results grouped by type", Fields: []string{"tracks", "albums", "artists", "playlists"}}},
				},
			},
			{
				Name:        "player",
				Description: "Playback state, currently playing, recently played",
				Operations: []Operation{
					{Name: "read_state", Description: "Get current playback state", Method: "GET", Path: "/me/player",
						Response: ResponseSpec{Description: "Playback state with track, device, is_playing", Fields: []string{"is_playing", "track", "device"}}},
					{Name: "read_recent", Description: "Get recently played tracks", Method: "GET", Path: "/me/player/recently-played",
						Response: ResponseSpec{Description: "Recently played tracks with played_at timestamps", Fields: []string{"tracks"}}},
				},
			},
			{
				Name:        "library",
				Description: "User's saved tracks, albums, and artists",
				Operations: []Operation{
					{Name: "read_tracks", Description: "List saved tracks (alias for tracks.read_saved)", Method: "GET", Path: "/me/tracks",
						Response: ResponseSpec{Description: "Saved tracks", Fields: []string{"tracks", "total"}}},
					{Name: "read_albums", Description: "List saved albums (alias for albums.read_saved)", Method: "GET", Path: "/me/albums",
						Response: ResponseSpec{Description: "Saved albums", Fields: []string{"albums", "total"}}},
				},
			},
			{
				Name:        "recommendations",
				Description: "Personalized recommendations based on listening history",
				Operations: []Operation{
					{Name: "read_top_tracks", Description: "Top tracks (alias for tracks.read_top)", Method: "GET", Path: "/me/top/tracks",
						Response: ResponseSpec{Description: "Top tracks", Fields: []string{"tracks"}}},
					{Name: "read_top_artists", Description: "Top artists (alias for artists.read_top)", Method: "GET", Path: "/me/top/artists",
						Response: ResponseSpec{Description: "Top artists", Fields: []string{"artists"}}},
				},
			},
		},
	}
}

// SystemInstructions returns the system message injected into LLM requests
// when Spotify is referenced. This tells the agent exactly how to use the proxy.
func (c *SpotifyConnector) SystemInstructions(baseURL string) string {
	root := strings.TrimRight(baseURL, "/")
	base := root + "/v1/services/spotify"
	return fmt.Sprintf(`When the user's request involves Spotify, you MUST use the Aegis proxy at %s instead of calling Spotify directly.

For "my playlists" across every connected music service (Spotify, YouTube Music, Amazon Music, …), prefer the unified library endpoint first:
GET %s/v1/library/playlists — all user playlists in one neutral list (synced across services; service IDs live in sources[])

For "liked songs" / saved tracks across every connected music service:
GET %s/v1/library/tracks — all liked/saved tracks in one neutral list (name, artists, album, sources[])

For saved albums and followed artists across every connected music service:
GET %s/v1/library/albums — all saved albums in one neutral list (name, artists, sources[])
GET %s/v1/library/artists — all followed artists in one neutral list (name, sources[])

Spotify-specific endpoints (call the full proxy URL; Aegis authenticates you — do not send Spotify tokens or an API key unless your deployment uses api-key mode):

GET /me — Current user profile
GET /me/playlists — List user's playlists in neutral format (paginated, use ?limit=N&offset=N)
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

Playlist responses use Aegis's neutral fields: name, description, owner, track_count, sources[] (service, service_name, id, url, extra).
All responses are transformed to a compact, LLM-friendly format.
Paginated endpoints return all items automatically when possible.
Always include the full proxy base URL in your requests.`, base, root, root, root, root)
}

// HandleProxy processes a Spotify API request through the connector.
func (c *SpotifyConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	runConnectorProxy(c.server, w, r, remainingPath, connectorProxyOpts{
		serviceID:   "spotify",
		displayName: "Spotify",
		defaultBase: "https://api.spotify.com/v1",
		defaultTTL:  60 * time.Second,
		expandPages: func(ctx context.Context, path string, _ url.Values, raw map[string]any, token string, logger *slog.Logger) map[string]any {
			if c.isPaginated(path) {
				return c.fetchAllPagesRaw(ctx, raw, token, logger)
			}
			return raw
		},
		transform: c.transformResponse,
	})
}

// doSpotifyRequest executes an HTTP request to the Spotify API.
func (c *SpotifyConnector) doSpotifyRequest(ctx context.Context, method, url string, body io.Reader, token string) ([]byte, int, error) {
	return doUpstreamRequest(ctx, method, url, body, token)
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

// ListUserSavedAlbums implements AlbumProvider. Fetches Spotify saved albums
// and returns them in Aegis's neutral format.
func (c *SpotifyConnector) ListUserSavedAlbums(ctx context.Context) ([]Album, bool, error) {
	token := c.server.getValidToken("spotify", slog.Default())
	if token == "" {
		return nil, false, ErrUnauthorized
	}

	upstreamBase := "https://api.spotify.com/v1"
	if svc := c.server.config.GetService("spotify"); svc != nil && svc.Endpoint != "" {
		upstreamBase = strings.TrimRight(svc.Endpoint, "/")
	}

	var all []Album
	nextURL := upstreamBase + "/me/albums?limit=50"
	for page := 0; page < libraryPageCap && nextURL != ""; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, false, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := aegisHTTPClient.Do(req)
		if err != nil {
			return nil, false, err
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, false, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, false, fmt.Errorf("spotify saved albums returned %d", resp.StatusCode)
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, false, err
		}
		items, _ := raw["items"].([]any)
		for _, item := range items {
			saved, ok := item.(map[string]any)
			if !ok {
				continue
			}
			album, _ := saved["album"].(map[string]any)
			if album == nil {
				continue
			}
			all = append(all, spotifyNeutralAlbum(album))
		}
		nextURL, _ = raw["next"].(string)
	}
	return all, nextURL != "", nil
}

// ListUserFollowedArtists implements ArtistProvider. Fetches Spotify followed
// artists and returns them in Aegis's neutral format.
func (c *SpotifyConnector) ListUserFollowedArtists(ctx context.Context) ([]Artist, bool, error) {
	token := c.server.getValidToken("spotify", slog.Default())
	if token == "" {
		return nil, false, ErrUnauthorized
	}

	upstreamBase := "https://api.spotify.com/v1"
	if svc := c.server.config.GetService("spotify"); svc != nil && svc.Endpoint != "" {
		upstreamBase = strings.TrimRight(svc.Endpoint, "/")
	}

	var all []Artist
	nextURL := upstreamBase + "/me/following?type=artist&limit=50"
	for page := 0; page < libraryPageCap && nextURL != ""; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, false, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := aegisHTTPClient.Do(req)
		if err != nil {
			return nil, false, err
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, false, err
		}
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
			return nil, false, fmt.Errorf(
				"spotify following returned %d — token is missing the user-follow-read scope; reconnect at /v1/auth/spotify/login",
				resp.StatusCode,
			)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, false, fmt.Errorf("spotify following returned %d", resp.StatusCode)
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, false, err
		}
		// Spotify nests followed artists under artists.items
		container := raw
		if artistsObj, ok := raw["artists"].(map[string]any); ok {
			container = artistsObj
		}
		items, _ := container["items"].([]any)
		for _, item := range items {
			artist, ok := item.(map[string]any)
			if !ok {
				continue
			}
			all = append(all, spotifyNeutralArtist(artist))
		}
		nextURL, _ = container["next"].(string)
	}
	return all, nextURL != "", nil
}

// spotifyNeutralAlbum maps a raw Spotify album into the neutral Album type.
func spotifyNeutralAlbum(album map[string]any) Album {
	id, _ := album["id"].(string)
	name, _ := album["name"].(string)
	uri, _ := album["uri"].(string)
	upc, _ := extractNested(album, "external_ids", "upc").(string)

	var artists []Artist
	if rawArtists, ok := album["artists"].([]any); ok {
		for _, item := range rawArtists {
			obj, ok := item.(map[string]any)
			if !ok {
				continue
			}
			aName, _ := obj["name"].(string)
			artistID, _ := obj["id"].(string)
			a := newArtist("spotify", "Spotify", artistID, aName)
			if artistID != "" && len(a.Sources) > 0 {
				a.Sources[0].URL = "https://open.spotify.com/artist/" + artistID
			}
			if a.Name != "" {
				artists = append(artists, a)
			}
		}
	}
	if len(artists) == 0 {
		for _, n := range extractNames(album, "artists") {
			artists = append(artists, newArtist("spotify", "Spotify", "", n))
		}
	}

	url := ""
	if id != "" {
		url = "https://open.spotify.com/album/" + id
	}
	extra := map[string]any{}
	if uri != "" {
		extra["uri"] = uri
	}
	if upc != "" {
		extra["upc"] = upc
	}

	return Album{
		Name:     name,
		Artists:  artists,
		EntityID: strings.ToUpper(strings.TrimSpace(upc)),
		Sources: []AlbumSource{{
			Service:     "spotify",
			ServiceName: "Spotify",
			ID:          id,
			Name:        name,
			URL:         url,
			Extra:       extraOrNil(extra),
		}},
	}
}

// spotifyNeutralArtist maps a raw Spotify artist into the neutral Artist type.
func spotifyNeutralArtist(artist map[string]any) Artist {
	id, _ := artist["id"].(string)
	name, _ := artist["name"].(string)
	url := ""
	if id != "" {
		url = "https://open.spotify.com/artist/" + id
	}
	a := newArtist("spotify", "Spotify", id, name)
	if len(a.Sources) > 0 {
		a.Sources[0].URL = url
	}
	return a
}

// ListUserLikedTracks implements LikedTrackProvider. Fetches Spotify saved
// tracks and returns them in Aegis's neutral format.
func (c *SpotifyConnector) ListUserLikedTracks(ctx context.Context) ([]Track, bool, error) {
	token := c.server.getValidToken("spotify", slog.Default())
	if token == "" {
		return nil, false, ErrUnauthorized
	}

	upstreamBase := "https://api.spotify.com/v1"
	if svc := c.server.config.GetService("spotify"); svc != nil && svc.Endpoint != "" {
		upstreamBase = strings.TrimRight(svc.Endpoint, "/")
	}

	var all []Track
	nextURL := upstreamBase + "/me/tracks?limit=50"
	for page := 0; page < libraryPageCap && nextURL != ""; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, false, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := aegisHTTPClient.Do(req)
		if err != nil {
			return nil, false, err
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, false, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, false, fmt.Errorf("spotify saved tracks returned %d", resp.StatusCode)
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, false, err
		}
		items, _ := raw["items"].([]any)
		for _, item := range items {
			saved, ok := item.(map[string]any)
			if !ok {
				continue
			}
			track, _ := saved["track"].(map[string]any)
			if track == nil {
				continue
			}
			all = append(all, spotifyNeutralTrack(track))
		}
		nextURL, _ = raw["next"].(string)
	}
	return all, nextURL != "", nil
}

// ListUserPlaylists implements PlaylistProvider. Fetches Spotify playlists
// and returns them in Aegis's neutral format.
func (c *SpotifyConnector) ListUserPlaylists(ctx context.Context) ([]Playlist, bool, error) {
	token := c.server.getValidToken("spotify", slog.Default())
	if token == "" {
		return nil, false, ErrUnauthorized
	}

	upstreamBase := "https://api.spotify.com/v1"
	if svc := c.server.config.GetService("spotify"); svc != nil && svc.Endpoint != "" {
		upstreamBase = strings.TrimRight(svc.Endpoint, "/")
	}

	var all []Playlist
	nextURL := upstreamBase + "/me/playlists?limit=50"
	for page := 0; page < libraryPageCap && nextURL != ""; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, false, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := aegisHTTPClient.Do(req)
		if err != nil {
			return nil, false, err
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, false, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, false, fmt.Errorf("spotify playlists returned %d", resp.StatusCode)
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
		nextURL, _ = raw["next"].(string)
	}
	return all, nextURL != "", nil
}

// spotifyPlaylistURL builds a public Spotify playlist link.
func spotifyPlaylistURL(id string) string {
	if id == "" {
		return ""
	}
	return "https://open.spotify.com/playlist/" + id
}

// playlistTrackTotal reads the playlist's track count. Current Spotify Web API
// reports it as items.total; older responses used tracks.total.
func playlistTrackTotal(p map[string]any) int {
	for _, key := range []string{"items", "tracks"} {
		if obj, ok := p[key].(map[string]any); ok {
			if n, ok := asInt(obj["total"]); ok {
				return n
			}
		}
	}
	if n, ok := asInt(p["track_count"]); ok {
		return n
	}
	return 0
}

// playlistTrackEntries returns the raw playlist-item objects when embedded.
// Current API nests them under items.items; older under tracks.items.
func playlistTrackEntries(p map[string]any) []any {
	for _, key := range []string{"items", "tracks"} {
		obj, ok := p[key].(map[string]any)
		if !ok {
			continue
		}
		if items, ok := obj["items"].([]any); ok {
			return items
		}
	}
	return nil
}

// playlistEntryTrack extracts the track object from a playlist item entry.
// Current API puts it under item; older under track.
func playlistEntryTrack(entry map[string]any) map[string]any {
	if t, ok := entry["item"].(map[string]any); ok && t != nil {
		return t
	}
	if t, ok := entry["track"].(map[string]any); ok {
		return t
	}
	return nil
}

// neutralPlaylist maps a raw Spotify playlist object into the neutral type.
// Service identity (id, url, uri) lives in Sources / Extra, not top-level.
func (c *SpotifyConnector) neutralPlaylist(p map[string]any) Playlist {
	id, _ := p["id"].(string)
	name, _ := p["name"].(string)
	description, _ := p["description"].(string)
	uri, _ := p["uri"].(string)

	trackCount := playlistTrackTotal(p)

	owner, _ := extractNested(p, "owner", "display_name").(string)

	extra := map[string]any{}
	if uri != "" {
		extra["uri"] = uri
	}
	if public, ok := p["public"]; ok {
		extra["public"] = public
	}
	if snapshot, ok := p["snapshot_id"]; ok {
		extra["snapshot_id"] = snapshot
	}

	return Playlist{
		Name:        name,
		Description: description,
		Owner:       owner,
		TrackCount:  trackCount,
		Sources: []PlaylistSource{
			primaryPlaylistSource("spotify", "Spotify", id, spotifyPlaylistURL(id), extra),
		},
	}
}

func (c *SpotifyConnector) transformPlaylistList(raw map[string]any) PlaylistList {
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
	if n, ok := asInt(raw["total"]); ok {
		total = n
	}
	if n, ok := asInt(raw["total_fetched"]); ok {
		total = n
	}
	return PlaylistList{Playlists: playlists, Total: total}
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
			"name":   device["name"],
			"type":   device["type"],
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
			"name":   a["name"],
			"id":     a["id"],
			"uri":    a["uri"],
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

// spotifyNeutralTrack maps a raw Spotify track object into the neutral Track.
// Artist/album names become unified identities; Spotify IDs live in Sources.
func spotifyNeutralTrack(track map[string]any) Track {
	id, _ := track["id"].(string)
	name, _ := track["name"].(string)
	uri, _ := track["uri"].(string)
	albumName, _ := extractNested(track, "album", "name").(string)
	albumID, _ := extractNested(track, "album", "id").(string)
	isrc, _ := extractNested(track, "external_ids", "isrc").(string)
	durationMs, _ := asInt(track["duration_ms"])

	extra := map[string]any{}
	if uri != "" {
		extra["uri"] = uri
	}
	if isrc != "" {
		extra["isrc"] = isrc
	}

	var artists []Artist
	if rawArtists, ok := track["artists"].([]any); ok {
		for _, a := range rawArtists {
			obj, ok := a.(map[string]any)
			if !ok {
				continue
			}
			name, _ := obj["name"].(string)
			artistID, _ := obj["id"].(string)
			artist := newArtist("spotify", "Spotify", artistID, name)
			if artistID != "" && len(artist.Sources) > 0 {
				artist.Sources[0].URL = "https://open.spotify.com/artist/" + artistID
			}
			if artist.Name != "" {
				artists = append(artists, artist)
			}
		}
	}
	if len(artists) == 0 {
		for _, n := range extractNames(track, "artists") {
			artists = append(artists, newArtist("spotify", "Spotify", "", n))
		}
	}
	artists = mergeArtists(artists...)

	url := ""
	if id != "" {
		url = "https://open.spotify.com/track/" + id
	}

	return Track{
		Name:       name,
		Artists:    artists,
		Album:      newAlbum("spotify", "Spotify", albumID, albumName),
		DurationMs: durationMs,
		EntityID:   strings.ToUpper(strings.TrimSpace(isrc)),
		Sources: []TrackSource{{
			Service:     "spotify",
			ServiceName: "Spotify",
			ID:          id,
			URL:         url,
			Extra:       extraOrNil(extra),
		}},
	}
}

func extraOrNil(m map[string]any) map[string]any {
	if len(m) == 0 {
		return nil
	}
	return m
}

func (c *SpotifyConnector) transformPlaylistDetail(raw map[string]any) PlaylistDetail {
	base := c.neutralPlaylist(raw)

	var tracks []Track
	for _, entry := range playlistTrackEntries(raw) {
		e, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		track := playlistEntryTrack(e)
		if track == nil {
			continue
		}
		tracks = append(tracks, spotifyNeutralTrack(track))
	}
	return PlaylistDetail{
		Playlist:    base,
		Tracks:      tracks,
		TotalTracks: base.TrackCount,
	}
}

func (c *SpotifyConnector) transformTrack(raw map[string]any) map[string]any {
	return map[string]any{
		"name":        raw["name"],
		"artists":     extractNames(raw, "artists"),
		"album":       extractNested(raw, "album", "name"),
		"duration_ms": raw["duration_ms"],
		"popularity":  raw["popularity"],
		"preview_url": raw["preview_url"],
		"id":          raw["id"],
		"uri":         raw["uri"],
	}
}

func (c *SpotifyConnector) transformArtist(raw map[string]any) map[string]any {
	return map[string]any{
		"name":       raw["name"],
		"genres":     raw["genres"],
		"followers":  extractNested(raw, "followers", "total"),
		"popularity": raw["popularity"],
		"id":         raw["id"],
		"uri":        raw["uri"],
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

// fetchAllPagesRaw follows Spotify's pagination `next` URLs on the raw API
// response and combines items under the standard "items" key. Must run before
// transform, so subsequent pages share the same shape as the first page.
func (c *SpotifyConnector) fetchAllPagesRaw(ctx context.Context, first map[string]any, token string, logger *slog.Logger) map[string]any {
	allItems, _ := first["items"].([]any)
	nextURL, _ := first["next"].(string)

	// Follow up to libraryPageCap additional pages so large libraries are
	// not silently cut off. Hit the cap → mark truncated for the caller.
	maxPages := libraryPageCap
	for i := 0; i < maxPages && nextURL != ""; i++ {
		data, status, err := c.doSpotifyRequest(ctx, "GET", nextURL, nil, token)
		if err != nil || status != http.StatusOK {
			logger.Warn("pagination fetch failed", "url", nextURL, "status", status, "error", err)
			break
		}
		var next map[string]any
		if err := json.Unmarshal(data, &next); err != nil {
			break
		}
		items, _ := next["items"].([]any)
		allItems = append(allItems, items...)
		nextURL, _ = next["next"].(string)
	}

	first["items"] = allItems
	first["total_fetched"] = float64(len(allItems))
	if nextURL != "" {
		first["truncated"] = true
	}
	delete(first, "next")
	delete(first, "previous")
	delete(first, "href")
	delete(first, "offset")
	delete(first, "limit")
	return first
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
