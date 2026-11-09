package connectors

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

	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

const (
	SpotifyServiceID   = "spotify"
	SpotifyServiceName = "Spotify"
)

// SpotifyConnector provides intelligent proxying for the Spotify API.
// It transforms verbose Spotify responses into LLM-friendly formats
// and generates rich system instructions for agents.
type SpotifyConnector struct {
	host Host
}

// NewSpotifyConnector creates a connector wired to the given Host (issue #15 seam).
// spotifyAPIBase is the default Spotify Web API root; config endpoint overrides it.
const spotifyAPIBase = "https://api.spotify.com/v1"

func NewSpotifyConnector(host Host) *SpotifyConnector {
	return &SpotifyConnector{host: host}
}

// Capabilities returns what Spotify kinds and operations are supported.
func (c *SpotifyConnector) Capabilities() shared.ServiceCapabilities {
	return shared.ServiceCapabilities{
		Kinds: []shared.KindCapability{
			{
				Name:        "user",
				Description: "Current user profile",
				Operations: []shared.Operation{
					{Name: "read", Description: "Get current user's profile", Method: "GET", Path: "/me",
						Response: shared.ResponseSpec{Description: "User profile with id, display_name, uri", Fields: []string{"id", "display_name", "uri"}}},
				},
			},
			{
				Name:        "tracks",
				Description: "Individual songs — saved tracks, top tracks, search results",
				Operations: []shared.Operation{
					{Name: "read_saved", Description: "List user's saved/liked tracks (paginated, auto-fetches all pages)", Method: "GET", Path: "/me/tracks",
						Parameters: []shared.Parameter{
							{Name: "limit", In: "query", Type: "integer", Description: "Max tracks per page (1-50)", Default: 20},
							{Name: "offset", In: "query", Type: "integer", Description: "Index of first track to return", Default: 0},
						},
						Response: shared.ResponseSpec{Description: "Saved tracks with name, artists, album, id, uri", Fields: []string{"tracks", "total"}}},
					{Name: "read_top", Description: "List user's top tracks by time range", Method: "GET", Path: "/me/top/tracks",
						Parameters: []shared.Parameter{
							{Name: "time_range", In: "query", Type: "string", Description: "short_term (4 weeks), medium_term (6 months), or long_term (years)", Default: "medium_term"},
							{Name: "limit", In: "query", Type: "integer", Description: "Max tracks (1-50)", Default: 20},
						},
						Response: shared.ResponseSpec{Description: "Top tracks with name, artists, album, id", Fields: []string{"tracks"}}},
					{Name: "get_by_id", Description: "Get a track by its Spotify ID", Method: "GET", Path: "/tracks/{id}",
						Parameters: []shared.Parameter{
							{Name: "id", In: "path", Type: "string", Description: "Spotify track ID", Required: true},
						},
						Response: shared.ResponseSpec{Description: "Track details with name, artists, album, duration, popularity", Fields: []string{"name", "artists", "album", "duration_ms", "popularity", "id", "uri"}}},
				},
			},
			{
				Name:        "albums",
				Description: "Albums — saved albums, album details",
				Operations: []shared.Operation{
					{Name: "read_saved", Description: "List user's saved albums (paginated)", Method: "GET", Path: "/me/albums",
						Parameters: []shared.Parameter{
							{Name: "limit", In: "query", Type: "integer", Description: "Max albums per page (1-50)", Default: 20},
						},
						Response: shared.ResponseSpec{Description: "Saved albums with name, artists, id", Fields: []string{"albums", "total"}}},
					{Name: "get_by_id", Description: "Get an album by its Spotify ID", Method: "GET", Path: "/albums/{id}",
						Parameters: []shared.Parameter{
							{Name: "id", In: "path", Type: "string", Description: "Spotify album ID", Required: true},
						},
						Response: shared.ResponseSpec{Description: "Album with tracks, artists, release date", Fields: []string{"name", "artists", "tracks", "release_date", "total_tracks"}}},
				},
			},
			{
				Name:        "artists",
				Description: "Artists — followed artists, top tracks, related artists",
				Operations: []shared.Operation{
					{Name: "read_top", Description: "List user's top artists by time range", Method: "GET", Path: "/me/top/artists",
						Parameters: []shared.Parameter{
							{Name: "time_range", In: "query", Type: "string", Description: "short_term, medium_term, or long_term", Default: "medium_term"},
						},
						Response: shared.ResponseSpec{Description: "Top artists with name, id, genres", Fields: []string{"artists"}}},
					{Name: "get_by_id", Description: "Get an artist by its Spotify ID", Method: "GET", Path: "/artists/{id}",
						Parameters: []shared.Parameter{
							{Name: "id", In: "path", Type: "string", Description: "Spotify artist ID", Required: true},
						},
						Response: shared.ResponseSpec{Description: "Artist with name, genres, followers, popularity", Fields: []string{"name", "genres", "followers", "popularity"}}},
					{Name: "get_top_tracks", Description: "Get an artist's top tracks", Method: "GET", Path: "/artists/{id}/top-tracks",
						Parameters: []shared.Parameter{
							{Name: "id", In: "path", Type: "string", Description: "Spotify artist ID", Required: true},
						},
						Response: shared.ResponseSpec{Description: "Artist's top tracks", Fields: []string{"tracks"}}},
				},
			},
			{
				Name:        "playlists",
				Description: "User playlists — list, detail with tracks",
				Operations: []shared.Operation{
					{Name: "read_list", Description: "List user's playlists (paginated)", Method: "GET", Path: "/me/playlists",
						Parameters: []shared.Parameter{
							{Name: "limit", In: "query", Type: "integer", Description: "Max playlists per page (1-50)", Default: 20},
						},
						Response: shared.ResponseSpec{Description: "Neutral playlist list: name, description, owner, track_count, sources[] (service, service_name, id, url, extra)", Fields: []string{"playlists", "total"}}},
					{Name: "get_by_id", Description: "Get a playlist by ID with its tracks", Method: "GET", Path: "/playlists/{id}",
						Parameters: []shared.Parameter{
							{Name: "id", In: "path", Type: "string", Description: "Spotify playlist ID", Required: true},
						},
						Response: shared.ResponseSpec{Description: "Neutral playlist detail: name, description, owner, track_count, sources[], tracks", Fields: []string{"name", "description", "owner", "track_count", "sources", "tracks", "extra"}}},
				},
			},
			{
				Name:        "search",
				Description: "Search the Spotify catalog",
				Operations: []shared.Operation{
					{Name: "search", Description: "Search for tracks, albums, artists, or playlists", Method: "GET", Path: "/search",
						Parameters: []shared.Parameter{
							{Name: "q", In: "query", Type: "string", Description: "Search query", Required: true},
							{Name: "type", In: "query", Type: "string", Description: "Comma-separated types: track, album, artist, playlist", Default: "track"},
							{Name: "limit", In: "query", Type: "integer", Description: "Max results per type (1-50)", Default: 10},
						},
						Response: shared.ResponseSpec{Description: "Search results grouped by type", Fields: []string{"tracks", "albums", "artists", "playlists"}}},
				},
			},
			{
				Name:        "player",
				Description: "Playback state, currently playing, recently played",
				Operations: []shared.Operation{
					{Name: "read_state", Description: "Get current playback state", Method: "GET", Path: "/me/player",
						Response: shared.ResponseSpec{Description: "Playback state with track, device, is_playing", Fields: []string{"is_playing", "track", "device"}}},
					{Name: "read_recent", Description: "Get recently played tracks", Method: "GET", Path: "/me/player/recently-played",
						Response: shared.ResponseSpec{Description: "Recently played tracks with played_at timestamps", Fields: []string{"tracks"}}},
				},
			},
			{
				Name:        "library",
				Description: "User's saved tracks, albums, and artists",
				Operations: []shared.Operation{
					{Name: "read_tracks", Description: "List saved tracks (alias for tracks.read_saved)", Method: "GET", Path: "/me/tracks",
						Response: shared.ResponseSpec{Description: "Saved tracks", Fields: []string{"tracks", "total"}}},
					{Name: "read_albums", Description: "List saved albums (alias for albums.read_saved)", Method: "GET", Path: "/me/albums",
						Response: shared.ResponseSpec{Description: "Saved albums", Fields: []string{"albums", "total"}}},
				},
			},
			{
				Name:        "recommendations",
				Description: "Personalized recommendations based on listening history",
				Operations: []shared.Operation{
					{Name: "read_top_tracks", Description: "Top tracks (alias for tracks.read_top)", Method: "GET", Path: "/me/top/tracks",
						Response: shared.ResponseSpec{Description: "Top tracks", Fields: []string{"tracks"}}},
					{Name: "read_top_artists", Description: "Top artists (alias for artists.read_top)", Method: "GET", Path: "/me/top/artists",
						Response: shared.ResponseSpec{Description: "Top artists", Fields: []string{"artists"}}},
				},
			},
		},
	}
}

// SystemInstructions returns the system message injected into LLM requests
// when Spotify is referenced. This tells the agent exactly how to use the proxy.
func (c *SpotifyConnector) SystemInstructions(baseURL string) string {
	root := strings.TrimRight(baseURL, "/")
	return fmt.Sprintf(`When the user's request involves Spotify or their music library, use Aegis's unified data API. Do not call Spotify directly.

GET %s/v1/library/playlists — user playlists across every connected music service (synced; service IDs live in sources[])
GET %s/v1/library/tracks — liked/saved tracks (name, artists, album, sources[])
GET %s/v1/library/albums — saved albums
GET %s/v1/library/artists — followed artists

sources[] records where each item lives for sync/backup — prefer the library over service-specific calls.

Only when you need full playlist contents (library entries carry track_count only):
GET %s/v1/services/spotify/playlists/{id} — playlist with tracks
GET %s/v1/services/spotify/playlists/{id}/items — playlist items as neutral tracks

Aegis does not expose playback control, catalog search, profiles, or other Spotify account APIs.`, root, root, root, root, root, root)
}

// HandleProxy processes a Spotify API request through the connector.
func (c *SpotifyConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	RunProxy(c.host, w, r, remainingPath, ProxyOpts{
		serviceID:   "spotify",
		displayName: "Spotify",
		defaultBase: spotifyAPIBase,
		defaultTTL:  60 * time.Second,
		expandPages: func(ctx context.Context, path string, _ url.Values, raw map[string]any, token string, logger *slog.Logger) map[string]any {
			if c.IsPaginated(path) {
				return c.FetchAllPagesRaw(ctx, raw, token, logger)
			}
			return raw
		},
		transform: c.TransformResponse,
	})
}

// TransformResponse converts Spotify's verbose JSON into a compact format
// optimized for LLM consumption.
func (c *SpotifyConnector) TransformResponse(method, path string, data []byte, logger *slog.Logger) any {
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
		return c.TransformUser(raw)
	case path == "/me/playlists" || strings.HasPrefix(path, "/users/") && strings.HasSuffix(path, "/playlists"):
		return c.TransformPlaylistList(raw)
	case path == "/me/tracks":
		return c.TransformSavedTracks(raw)
	case path == "/me/albums":
		return c.TransformSavedAlbums(raw)
	case path == "/me/player":
		return c.TransformPlayer(raw)
	case path == "/me/player/recently-played":
		return c.TransformRecentlyPlayed(raw)
	case path == "/me/top/tracks":
		return c.TransformTrackList(raw)
	case path == "/me/top/artists":
		return c.TransformArtistList(raw)
	case path == "/search":
		return c.TransformSearch(raw)
	case strings.HasPrefix(path, "/playlists/"):
		return c.TransformPlaylistDetail(raw)
	case strings.HasPrefix(path, "/tracks/"):
		return c.TransformTrack(raw)
	case strings.HasPrefix(path, "/artists/") && !strings.HasSuffix(path, "/top-tracks"):
		return c.TransformArtist(raw)
	case strings.HasPrefix(path, "/artists/") && strings.HasSuffix(path, "/top-tracks"):
		return c.TransformTrackList(raw)
	case strings.HasPrefix(path, "/albums/"):
		return c.TransformAlbum(raw)
	default:
		// Unknown path — return a simplified version of the raw response.
		return c.Simplify(raw)
	}
}

func (c *SpotifyConnector) TransformUser(raw map[string]any) map[string]any {
	return map[string]any{
		"id":           raw["id"],
		"display_name": raw["display_name"],
		"uri":          raw["uri"],
	}
}

// ListUserSavedAlbums implements AlbumProvider. Fetches Spotify saved albums
// and returns them in Aegis's neutral format.
func (c *SpotifyConnector) ListUserSavedAlbums(ctx context.Context) ([]kinds.Album, bool, error) {
	token := c.host.GetValidToken("spotify", slog.Default())
	if token == "" {
		return nil, false, kinds.ErrUnauthorized
	}

	upstreamBase := c.host.UpstreamBaseFor("spotify", spotifyAPIBase)

	var all []kinds.Album
	nextURL := upstreamBase + "/me/albums?limit=50"
	for page := 0; page < shared.LibraryPageCap && nextURL != ""; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, false, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := shared.HTTPClient.Do(req)
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
		items := SpotifyPagingItems(raw)
		for _, item := range items {
			saved, ok := item.(map[string]any)
			if !ok {
				continue
			}
			album, _ := saved["album"].(map[string]any)
			if album == nil {
				continue
			}
			all = append(all, NeutralSpotifyAlbum(album))
		}
		nextURL, _ = raw["next"].(string)
	}
	return all, nextURL != "", nil
}

// ListUserFollowedArtists implements ArtistProvider. Fetches Spotify followed
// artists and returns them in Aegis's neutral format.
func (c *SpotifyConnector) ListUserFollowedArtists(ctx context.Context) ([]kinds.Artist, bool, error) {
	token := c.host.GetValidToken("spotify", slog.Default())
	if token == "" {
		return nil, false, kinds.ErrUnauthorized
	}

	upstreamBase := c.host.UpstreamBaseFor("spotify", spotifyAPIBase)

	var all []kinds.Artist
	nextURL := upstreamBase + "/me/following?type=artist&limit=50"
	for page := 0; page < shared.LibraryPageCap && nextURL != ""; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, false, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := shared.HTTPClient.Do(req)
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
			all = append(all, SpotifyNeutralArtist(artist))
		}
		nextURL, _ = container["next"].(string)
	}
	return all, nextURL != "", nil
}

// NeutralSpotifyAlbum maps a raw Spotify album into the neutral Album type.
func NeutralSpotifyAlbum(album map[string]any) kinds.Album {
	id, _ := album["id"].(string)
	name, _ := album["name"].(string)
	uri, _ := album["uri"].(string)
	upc, _ := shared.ExtractNested(album, "external_ids", "upc").(string)

	var artists []kinds.Artist
	if rawArtists, ok := album["artists"].([]any); ok {
		for _, item := range rawArtists {
			obj, ok := item.(map[string]any)
			if !ok {
				continue
			}
			aName, _ := obj["name"].(string)
			artistID, _ := obj["id"].(string)
			a := kinds.NewArtist("spotify", "Spotify", artistID, aName)
			if artistID != "" && len(a.Sources) > 0 {
				a.Sources[0].URL = "https://open.spotify.com/artist/" + artistID
			}
			if a.Name != "" {
				artists = append(artists, a)
			}
		}
	}
	if len(artists) == 0 {
		for _, n := range shared.ExtractNames(album, "artists") {
			artists = append(artists, kinds.NewArtist("spotify", "Spotify", "", n))
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

	return kinds.Album{
		Name:     name,
		Artists:  artists,
		EntityID: strings.ToUpper(strings.TrimSpace(upc)),
		Sources: []kinds.AlbumSource{{
			Service:     "spotify",
			ServiceName: "Spotify",
			ID:          id,
			Name:        name,
			URL:         url,
			Extra:       shared.ExtraOrNil(extra),
		}},
	}
}

// SpotifyNeutralArtist maps a raw Spotify artist into the neutral Artist type.
func SpotifyNeutralArtist(artist map[string]any) kinds.Artist {
	id, _ := artist["id"].(string)
	name, _ := artist["name"].(string)
	url := ""
	if id != "" {
		url = "https://open.spotify.com/artist/" + id
	}
	a := kinds.NewArtist("spotify", "Spotify", id, name)
	if len(a.Sources) > 0 {
		a.Sources[0].URL = url
	}
	return a
}

// ListUserLikedTracks implements LikedTrackProvider. Fetches Spotify saved
// tracks and returns them in Aegis's neutral format.
func (c *SpotifyConnector) ListUserLikedTracks(ctx context.Context) ([]kinds.Track, bool, error) {
	token := c.host.GetValidToken("spotify", slog.Default())
	if token == "" {
		return nil, false, kinds.ErrUnauthorized
	}

	upstreamBase := c.host.UpstreamBaseFor("spotify", spotifyAPIBase)

	var all []kinds.Track
	nextURL := upstreamBase + "/me/tracks?limit=50"
	for page := 0; page < shared.LibraryPageCap && nextURL != ""; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, false, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := shared.HTTPClient.Do(req)
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
		items := SpotifyPagingItems(raw)
		for _, item := range items {
			saved, ok := item.(map[string]any)
			if !ok {
				continue
			}
			track, _ := saved["track"].(map[string]any)
			if track == nil {
				continue
			}
			all = append(all, NeutralSpotifyTrack(track))
		}
		nextURL, _ = raw["next"].(string)
	}
	return all, nextURL != "", nil
}

// ListUserPlaylists implements PlaylistProvider. Fetches Spotify playlists
// and returns them in Aegis's neutral format.
func (c *SpotifyConnector) ListUserPlaylists(ctx context.Context) ([]kinds.Playlist, bool, error) {
	token := c.host.GetValidToken("spotify", slog.Default())
	if token == "" {
		return nil, false, kinds.ErrUnauthorized
	}

	upstreamBase := c.host.UpstreamBaseFor("spotify", spotifyAPIBase)

	var all []kinds.Playlist
	nextURL := upstreamBase + "/me/playlists?limit=50"
	for page := 0; page < shared.LibraryPageCap && nextURL != ""; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, false, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := shared.HTTPClient.Do(req)
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
		items := SpotifyPagingItems(raw)
		for _, item := range items {
			p, ok := item.(map[string]any)
			if !ok {
				continue
			}
			all = append(all, c.NeutralPlaylist(p))
		}
		nextURL, _ = raw["next"].(string)
	}
	return all, nextURL != "", nil
}

// SpotifyPlaylistURL builds a public Spotify playlist link.
func SpotifyPlaylistURL(id string) string {
	if id == "" {
		return ""
	}
	return "https://open.spotify.com/playlist/" + id
}

// PlaylistTrackTotal reads the playlist's track count. Current Spotify Web API
// reports it as items.total; older responses used tracks.total.
func PlaylistTrackTotal(p map[string]any) int {
	for _, key := range []string{"items", "tracks"} {
		if obj, ok := p[key].(map[string]any); ok {
			if n, ok := shared.AsInt(obj["total"]); ok {
				return n
			}
		}
	}
	if n, ok := shared.AsInt(p["track_count"]); ok {
		return n
	}
	return 0
}

// PlaylistTrackEntries returns the raw playlist-item objects when embedded.
// Current API nests them under items.items; older under tracks.items.
func PlaylistTrackEntries(p map[string]any) []any {
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

// PlaylistEntryTrack extracts the track object from a playlist item entry.
// Current API puts it under item; older under track.
func PlaylistEntryTrack(entry map[string]any) map[string]any {
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
func (c *SpotifyConnector) NeutralPlaylist(p map[string]any) kinds.Playlist {
	id, _ := p["id"].(string)
	name, _ := p["name"].(string)
	description, _ := p["description"].(string)
	uri, _ := p["uri"].(string)

	trackCount := PlaylistTrackTotal(p)

	owner, _ := shared.ExtractNested(p, "owner", "display_name").(string)

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

	return kinds.Playlist{
		Name:        name,
		Description: description,
		Owner:       owner,
		TrackCount:  trackCount,
		Sources: []kinds.PlaylistSource{
			kinds.PrimaryPlaylistSource("spotify", "Spotify", id, SpotifyPlaylistURL(id), extra),
		},
	}
}

func (c *SpotifyConnector) TransformPlaylistList(raw map[string]any) kinds.PlaylistList {
	items := SpotifyPagingItems(raw)
	playlists := make([]kinds.Playlist, 0, len(items))
	for _, item := range items {
		p, ok := item.(map[string]any)
		if !ok {
			continue
		}
		playlists = append(playlists, c.NeutralPlaylist(p))
	}
	total := len(playlists)
	if n, ok := shared.AsInt(raw["total"]); ok {
		total = n
	}
	if n, ok := shared.AsInt(raw["total_fetched"]); ok {
		total = n
	}
	return kinds.PlaylistList{Playlists: playlists, LibraryListMeta: kinds.LibraryListMeta{Total: total}}
}

func (c *SpotifyConnector) TransformSavedTracks(raw map[string]any) map[string]any {
	items := SpotifyPagingItems(raw)
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
			"artists": shared.ExtractNames(track, "artists"),
			"album":   shared.ExtractNested(track, "album", "name"),
			"id":      track["id"],
			"uri":     track["uri"],
		})
	}
	return map[string]any{
		"tracks": tracks,
		"total":  raw["total"],
	}
}

func (c *SpotifyConnector) TransformSavedAlbums(raw map[string]any) map[string]any {
	items := SpotifyPagingItems(raw)
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
			"artists": shared.ExtractNames(album, "artists"),
			"id":      album["id"],
			"uri":     album["uri"],
		})
	}
	return map[string]any{
		"albums": albums,
		"total":  raw["total"],
	}
}

func (c *SpotifyConnector) TransformPlayer(raw map[string]any) map[string]any {
	if len(raw) == 0 {
		return map[string]any{"is_playing": false, "message": "No active playback"}
	}
	item, _ := raw["item"].(map[string]any)
	result := map[string]any{
		"is_playing": raw["is_playing"],
	}
	if item != nil {
		result["track"] = map[string]any{
			"name":    item["name"],
			"artists": shared.ExtractNames(item, "artists"),
			"album":   shared.ExtractNested(item, "album", "name"),
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

func (c *SpotifyConnector) TransformRecentlyPlayed(raw map[string]any) map[string]any {
	items := SpotifyPagingItems(raw)
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
			"artists":   shared.ExtractNames(track, "artists"),
			"album":     shared.ExtractNested(track, "album", "name"),
			"played_at": entry["played_at"],
		})
	}
	return map[string]any{"tracks": tracks}
}

func (c *SpotifyConnector) TransformTrackList(raw map[string]any) map[string]any {
	items := SpotifyPagingItems(raw)
	tracks := make([]map[string]any, 0, len(items))
	for _, item := range items {
		t, ok := item.(map[string]any)
		if !ok {
			continue
		}
		tracks = append(tracks, map[string]any{
			"name":    t["name"],
			"artists": shared.ExtractNames(t, "artists"),
			"album":   shared.ExtractNested(t, "album", "name"),
			"id":      t["id"],
			"uri":     t["uri"],
		})
	}
	return map[string]any{"tracks": tracks}
}

func (c *SpotifyConnector) TransformArtistList(raw map[string]any) map[string]any {
	items := SpotifyPagingItems(raw)
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

func (c *SpotifyConnector) TransformSearch(raw map[string]any) map[string]any {
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
				entry["artists"] = shared.ExtractNames(obj, "artists")
			}
			if obj["album"] != nil {
				entry["album"] = shared.ExtractNested(obj, "album", "name")
			}
			transformed = append(transformed, entry)
		}
		result[key] = transformed
	}
	return result
}

// NeutralSpotifyTrack maps a raw Spotify track object into the neutral Track.
// Artist/album names become unified identities; Spotify IDs live in Sources.
// SpotifyPagingItems returns a Spotify paging object's item array. The
// provider has renamed this field before (issue #240); known migrations live
// here so every paging read shares one answer instead of each silently
// inventing its own.
func SpotifyPagingItems(raw map[string]any) []any {
	if items, ok := raw["items"].([]any); ok {
		return items
	}
	items, _ := raw["entries"].([]any)
	return items
}

func NeutralSpotifyTrack(track map[string]any) kinds.Track {
	id, _ := track["id"].(string)
	name, _ := track["name"].(string)
	uri, _ := track["uri"].(string)
	albumName, _ := shared.ExtractNested(track, "album", "name").(string)
	albumID, _ := shared.ExtractNested(track, "album", "id").(string)
	isrc, _ := shared.ExtractNested(track, "external_ids", "isrc").(string)
	durationMs, _ := shared.AsInt(track["duration_ms"])

	extra := map[string]any{}
	if uri != "" {
		extra["uri"] = uri
	}
	if isrc != "" {
		extra["isrc"] = isrc
	}

	var artists []kinds.Artist
	if rawArtists, ok := track["artists"].([]any); ok {
		for _, a := range rawArtists {
			obj, ok := a.(map[string]any)
			if !ok {
				continue
			}
			name, _ := obj["name"].(string)
			artistID, _ := obj["id"].(string)
			artist := kinds.NewArtist("spotify", "Spotify", artistID, name)
			if artistID != "" && len(artist.Sources) > 0 {
				artist.Sources[0].URL = "https://open.spotify.com/artist/" + artistID
			}
			if artist.Name != "" {
				artists = append(artists, artist)
			}
		}
	}
	if len(artists) == 0 {
		for _, n := range shared.ExtractNames(track, "artists") {
			artists = append(artists, kinds.NewArtist("spotify", "Spotify", "", n))
		}
	}
	artists = kinds.MergeArtists(artists...)

	url := ""
	if id != "" {
		url = "https://open.spotify.com/track/" + id
	}

	return kinds.Track{
		Name:       name,
		Artists:    artists,
		Album:      kinds.NewAlbum("spotify", "Spotify", albumID, albumName),
		DurationMs: durationMs,
		EntityID:   strings.ToUpper(strings.TrimSpace(isrc)),
		Sources: []kinds.TrackSource{{
			Service:     "spotify",
			ServiceName: "Spotify",
			ID:          id,
			URL:         url,
			Extra:       shared.ExtraOrNil(extra),
		}},
	}
}

func (c *SpotifyConnector) TransformPlaylistDetail(raw map[string]any) kinds.PlaylistDetail {
	base := c.NeutralPlaylist(raw)

	var tracks []kinds.Track
	for _, entry := range PlaylistTrackEntries(raw) {
		e, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		track := PlaylistEntryTrack(e)
		if track == nil {
			continue
		}
		tracks = append(tracks, NeutralSpotifyTrack(track))
	}
	return kinds.PlaylistDetail{
		Playlist:    base,
		Tracks:      tracks,
		TotalTracks: base.TrackCount,
	}
}

func (c *SpotifyConnector) TransformTrack(raw map[string]any) map[string]any {
	return map[string]any{
		"name":        raw["name"],
		"artists":     shared.ExtractNames(raw, "artists"),
		"album":       shared.ExtractNested(raw, "album", "name"),
		"duration_ms": raw["duration_ms"],
		"popularity":  raw["popularity"],
		"preview_url": raw["preview_url"],
		"id":          raw["id"],
		"uri":         raw["uri"],
	}
}

func (c *SpotifyConnector) TransformArtist(raw map[string]any) map[string]any {
	return map[string]any{
		"name":       raw["name"],
		"genres":     raw["genres"],
		"followers":  shared.ExtractNested(raw, "followers", "total"),
		"popularity": raw["popularity"],
		"id":         raw["id"],
		"uri":        raw["uri"],
	}
}

func (c *SpotifyConnector) TransformAlbum(raw map[string]any) map[string]any {
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
		"artists":      shared.ExtractNames(raw, "artists"),
		"release_date": raw["release_date"],
		"total_tracks": raw["total_tracks"],
		"tracks":       tracks,
		"id":           raw["id"],
		"uri":          raw["uri"],
	}
}

// simplify strips a raw Spotify response down to key fields.
func (c *SpotifyConnector) Simplify(raw map[string]any) map[string]any {
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

// IsPaginated returns true if the path points to a paginated Spotify endpoint.
func (c *SpotifyConnector) IsPaginated(path string) bool {
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

// FetchAllPagesRaw follows Spotify's pagination `next` URLs on the raw API
// response and combines items under the standard "items" key. Must run before
// transform, so subsequent pages share the same shape as the first page.
func (c *SpotifyConnector) FetchAllPagesRaw(ctx context.Context, first map[string]any, token string, logger *slog.Logger) map[string]any {
	allItems, _ := first["items"].([]any)
	nextURL, _ := first["next"].(string)

	// Follow up to shared.LibraryPageCap additional pages so large libraries are
	// not silently cut off. Hit the cap → mark truncated for the caller.
	maxPages := shared.LibraryPageCap
	for i := 0; i < maxPages && nextURL != ""; i++ {
		data, status, err := DoUpstreamRequest(ctx, "GET", nextURL, nil, token)
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
