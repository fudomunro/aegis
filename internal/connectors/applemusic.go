package connectors

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

const (
	AppleMusicServiceID   = "apple-music"
	AppleMusicServiceName = "Apple Music"
)

// appleMusicAPIBase is the default Apple Music API root; the configured
// endpoint wins (AGENTS.md: connectors read GetService(id).Endpoint).
const appleMusicAPIBase = "https://api.music.apple.com/v1"

// Static-credential names on the service-connections overlay (Immich/YNAB
// pattern, issue #184): values live in the encrypted token store and are
// never echoed back or forwarded upstream. Aegis mints the developer token
// itself — agents never see any of these.
const (
	appleMusicKeyIDHeader      = "X-Apple-Music-Key-Id"
	appleMusicIssuerIDHeader   = "X-Apple-Music-Issuer-Id"
	appleMusicPrivateKeyHeader = "X-Apple-Music-Private-Key"
	appleMusicUserTokenHeader  = "Music-User-Token"
)

// appleMusicTokenTTL is the developer-token lifetime Aegis mints. Apple
// accepts up to six months; one hour keeps a leaked JWT short-lived and the
// mint is cached until near expiry so signing is not per-request.
const appleMusicTokenTTL = time.Hour

// appleMusicStorefrontCacheTTL bounds how long the user's storefront id is
// reused; it changes only when the user moves storefronts.
const appleMusicStorefrontCacheTTL = 24 * time.Hour

// AppleMusicConnector serves Apple Music through the library and the
// mirror/converge placement surface. Apple Music is not an OAuth provider:
// Aegis signs a developer token (MusicKit JWT) from the operator's key and
// sends the user's Music User Token beside it, so agents never hold either.
type AppleMusicConnector struct {
	host Host

	mu           sync.Mutex
	devToken     string
	devTokenTime time.Time
}

// NewAppleMusicConnector creates a connector wired to the given Host (issue #15 seam).
func NewAppleMusicConnector(host Host) *AppleMusicConnector {
	return &AppleMusicConnector{host: host}
}

// --- credentials ---

// appleMusicCreds is the resolved MusicKit signing material and user token.
type appleMusicCreds struct {
	KeyID      string
	IssuerID   string
	PrivateKey *ecdsa.PrivateKey
	UserToken  string
}

func (c *AppleMusicConnector) headerValue(name string) string {
	svc := c.host.ServiceConfig(AppleMusicServiceID)
	if svc == nil {
		return ""
	}
	for k, v := range svc.Headers {
		if strings.EqualFold(k, name) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// credentials resolves the stored MusicKit key material and Music User
// Token. The parsed key is cached inside the returned value only; the raw
// PEM never leaves the connector.
func (c *AppleMusicConnector) credentials() (appleMusicCreds, error) {
	creds := appleMusicCreds{
		KeyID:     c.headerValue(appleMusicKeyIDHeader),
		IssuerID:  c.headerValue(appleMusicIssuerIDHeader),
		UserToken: c.headerValue(appleMusicUserTokenHeader),
	}
	pemText := c.headerValue(appleMusicPrivateKeyHeader)
	if creds.KeyID == "" || creds.IssuerID == "" || pemText == "" {
		return appleMusicCreds{}, fmt.Errorf("apple music key material missing (key id, issuer id, private key)")
	}
	key, err := parseAppleMusicPrivateKey(pemText)
	if err != nil {
		return appleMusicCreds{}, err
	}
	creds.PrivateKey = key
	return creds, nil
}

// parseAppleMusicPrivateKey reads a MusicKit .p8 (PKCS#8 EC key); SEC1 EC
// keys parse too so a re-exported PEM still works. The JWS header claims
// ES256, so only P-256 keys are accepted.
func parseAppleMusicPrivateKey(pemText string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, fmt.Errorf("apple music private key is not PEM")
	}
	var ec *ecdsa.PrivateKey
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		var ok bool
		ec, ok = key.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("apple music private key is not an EC key")
		}
	} else {
		sec1, err2 := x509.ParseECPrivateKey(block.Bytes)
		if err2 != nil {
			return nil, fmt.Errorf("apple music private key: %w", err)
		}
		ec = sec1
	}
	if ec.Curve != elliptic.P256() {
		return nil, fmt.Errorf("apple music private key must be P-256 (ES256)")
	}
	return ec, nil
}

// developerToken returns the cached MusicKit developer token, minting a new
// ES256 JWT when the cached one is near expiry. The token authorizes Aegis
// against the Apple Music API; it is not a user credential.
func (c *AppleMusicConnector) developerToken() (string, error) {
	creds, err := c.credentials()
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.devToken != "" && time.Since(c.devTokenTime) < appleMusicTokenTTL-5*time.Minute {
		return c.devToken, nil
	}
	tok, err := mintAppleMusicDeveloperToken(creds, time.Now())
	if err != nil {
		return "", err
	}
	c.devToken = tok
	c.devTokenTime = time.Now()
	return tok, nil
}

// mintAppleMusicDeveloperToken signs the MusicKit JWT: ES256 over
// header/payload, claims iss + iat + exp. Signature bytes are the raw R||S
// concatenation JWS expects, not ASN.1.
func mintAppleMusicDeveloperToken(creds appleMusicCreds, now time.Time) (string, error) {
	header := map[string]string{"alg": "ES256", "kid": creds.KeyID}
	claims := map[string]any{
		"iss": creds.IssuerID,
		"iat": now.Unix(),
		"exp": now.Add(appleMusicTokenTTL).Unix(),
	}
	headJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	signing := enc.EncodeToString(headJSON) + "." + enc.EncodeToString(claimsJSON)
	digest := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, creds.PrivateKey, digest[:])
	if err != nil {
		return "", err
	}
	curveBytes := (creds.PrivateKey.Curve.Params().BitSize + 7) / 8
	sig := make([]byte, 2*curveBytes)
	r.FillBytes(sig[:curveBytes])
	s.FillBytes(sig[curveBytes:])
	return signing + "." + enc.EncodeToString(sig), nil
}

// authHeaders resolves the upstream credential pair: the bearer value is the
// minted developer token; the Music User Token rides beside it. Nothing here
// is agent-visible.
func (c *AppleMusicConnector) authHeaders(logger *slog.Logger) (string, http.Header) {
	creds, err := c.credentials()
	if err != nil {
		logger.Debug("apple music credentials missing", "error", err)
		return "", nil
	}
	tok, err := c.developerToken()
	if err != nil {
		logger.Error("apple music developer token", "error", err)
		return "", nil
	}
	extra := http.Header{}
	if creds.UserToken != "" {
		extra.Set(appleMusicUserTokenHeader, creds.UserToken)
	}
	return tok, extra
}

// --- ServiceConnector ---

// Capabilities returns the library kinds and transport operations Apple
// Music serves. Library reads are backed by the neutral providers; playlist
// contents ride the content transport.
func (c *AppleMusicConnector) Capabilities() shared.ServiceCapabilities {
	return shared.ServiceCapabilities{
		Kinds: []shared.KindCapability{
			{
				Name:        "playlists",
				Description: "Apple Music library playlists",
				Operations: []shared.Operation{
					{
						Name:        "list_library_playlists",
						Description: "List the user's library playlists (permission key: playlists)",
						Method:      "GET",
						Path:        "/me/library/playlists",
						Parameters: []shared.Parameter{
							{Name: "limit", In: "query", Type: "integer", Description: "Page size (max 100)", Default: 100},
							{Name: "offset", In: "query", Type: "integer", Description: "Page offset"},
						},
						Response: shared.ResponseSpec{
							Description: "Apple Music library playlist page; use GET /v1/library/playlists for neutral Playlist values",
							Fields:      []string{"data", "next", "meta.total"},
						},
					},
					{
						Name:        "get_playlist_tracks",
						Description: "Tracks of one library playlist (permission key: playlists)",
						Method:      "GET",
						Path:        "/playlists/{id}",
						Parameters: []shared.Parameter{
							{Name: "id", In: "path", Type: "string", Description: "Library playlist id", Required: true},
						},
						Response: shared.ResponseSpec{
							Description: "Neutral playlist with its tracks",
							Fields:      []string{"name", "tracks", "total_tracks"},
						},
					},
				},
			},
			{
				Name:        "tracks",
				Description: "Library songs (liked/saved tracks)",
				Operations: []shared.Operation{
					{
						Name:        "list_library_songs",
						Description: "List the user's library songs (permission key: tracks)",
						Method:      "GET",
						Path:        "/me/library/songs",
						Parameters: []shared.Parameter{
							{Name: "limit", In: "query", Type: "integer", Description: "Page size (max 100)", Default: 100},
							{Name: "offset", In: "query", Type: "integer", Description: "Page offset"},
						},
						Response: shared.ResponseSpec{
							Description: "Apple Music library song page; use GET /v1/library/tracks for neutral Track values",
							Fields:      []string{"data", "next", "meta.total"},
						},
					},
				},
			},
			{
				Name:        "albums",
				Description: "Library albums",
				Operations: []shared.Operation{
					{
						Name:        "list_library_albums",
						Description: "List the user's library albums (permission key: albums)",
						Method:      "GET",
						Path:        "/me/library/albums",
						Response: shared.ResponseSpec{
							Description: "Apple Music library album page; use GET /v1/library/albums for neutral Album values",
							Fields:      []string{"data", "next", "meta.total"},
						},
					},
				},
			},
			{
				Name:        "artists",
				Description: "Library artists",
				Operations: []shared.Operation{
					{
						Name:        "list_library_artists",
						Description: "List the user's library artists (permission key: artists)",
						Method:      "GET",
						Path:        "/me/library/artists",
						Response: shared.ResponseSpec{
							Description: "Apple Music library artist page; use GET /v1/library/artists for neutral Artist values",
							Fields:      []string{"data", "next", "meta.total"},
						},
					},
				},
			},
		},
	}
}

// SystemInstructions returns the system message injected when Apple Music is
// referenced.
func (c *AppleMusicConnector) SystemInstructions(baseURL string) string {
	root := strings.TrimRight(baseURL, "/")
	return fmt.Sprintf(`When the user's request involves Apple Music or their music library, use Aegis's unified data API. Do not call Apple Music directly.

GET %s/v1/library/playlists — user playlists across every connected music service (synced; service IDs live in sources[])
GET %s/v1/library/tracks — liked/saved tracks
GET %s/v1/library/albums — saved albums
GET %s/v1/library/artists — artists

sources[] records where each item lives for sync/backup — prefer the library over service-specific calls.

Only when you need full playlist contents (library entries carry track_count only):
GET %s/v1/services/apple-music/playlists/{id} — playlist with tracks

Aegis does not expose catalog search, playback control, or other Apple Music account APIs.`, root, root, root, root, root)
}

// HandleProxy processes an Apple Music API request through the connector.
// Agent-facing shorthand paths map onto the MusicKit library API; the
// developer token and Music User Token are attached on the way out.
func (c *AppleMusicConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	RunProxy(c.host, w, r, remainingPath, ProxyOpts{
		serviceID:    AppleMusicServiceID,
		displayName:  AppleMusicServiceName,
		defaultBase:  appleMusicAPIBase,
		defaultTTL:   60 * time.Second,
		token:        func(logger *slog.Logger) string { tok, _ := c.authHeaders(logger); return tok },
		extraHeaders: func(logger *slog.Logger) http.Header { _, h := c.authHeaders(logger); return h },
		missingToken: "no Apple Music credentials — add a MusicKit key and Music User Token in the admin setup",
		rejectToken:  "Apple Music rejected the credentials — check the MusicKit key and Music User Token in the admin setup",
		mapUpstream:  mapAppleMusicUpstreamPath,
		expandPages:  c.expandPlaylistDetail,
		transform:    c.TransformResponse,
	})
}

// mapAppleMusicUpstreamPath rewrites agent shorthand onto MusicKit paths.
func mapAppleMusicUpstreamPath(path string, query url.Values) (string, url.Values, error) {
	switch {
	case strings.HasPrefix(path, "/playlists/"):
		id := strings.TrimPrefix(path, "/playlists/")
		if id == "" || strings.Contains(id, "/") {
			return "", nil, fmt.Errorf("invalid apple music playlist path")
		}
		return "/me/library/playlists/" + url.PathEscape(id), query, nil
	case path == "/me" || strings.HasPrefix(path, "/me/") || strings.HasPrefix(path, "/catalog/"):
		return path, query, nil
	default:
		return path, query, nil
	}
}

// expandPlaylistDetail pulls a library playlist's tracks into the raw
// playlist object so one agent call returns the whole detail. Capped: the
// transport is a convenience view, not a sync path (that is ListPlaylistItems).
// path is the agent-facing shorthand (/playlists/{id}).
func (c *AppleMusicConnector) expandPlaylistDetail(ctx context.Context, path string, query url.Values, raw map[string]any, token string, logger *slog.Logger) map[string]any {
	id := strings.TrimPrefix(path, "/playlists/")
	id = strings.TrimPrefix(id, "/me/library/playlists/")
	if id == "" || strings.Contains(id, "/") {
		return raw
	}
	const trackCap = 200
	var items []any
	offset := 0
	total := 0
	for offset < trackCap {
		q := url.Values{"limit": []string{strconv.Itoa(100)}, "offset": []string{strconv.Itoa(offset)}}
		page, _, err := c.apiJSON(ctx, "GET", "/me/library/playlists/"+url.PathEscape(id)+"/tracks", q, nil)
		if err != nil {
			logger.Debug("apple music playlist tracks", "error", err)
			break
		}
		data, _ := page["data"].([]any)
		items = append(items, data...)
		if meta, ok := page["meta"].(map[string]any); ok {
			total, _ = shared.AsInt(meta["total"])
		}
		next, _ := page["next"].(string)
		if next == "" || len(data) == 0 {
			break
		}
		offset += len(data)
	}
	if items == nil {
		items = []any{}
	}
	raw["tracks"] = items
	raw["tracks_total"] = total
	return raw
}

// TransformResponse maps Apple Music JSON onto the compact agent-facing
// shapes: neutral playlist detail for the content transport, light
// projections for library lists. path is the agent-facing shorthand.
// Fail-open: unrecognized payloads pass through unchanged.
func (c *AppleMusicConnector) TransformResponse(method, path string, data []byte, logger *slog.Logger) any {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		var arr []any
		if err2 := json.Unmarshal(data, &arr); err2 == nil {
			return arr
		}
		return string(data)
	}
	if _, ok := raw["errors"]; ok {
		return raw
	}
	switch {
	case strings.HasPrefix(path, "/playlists/"):
		return c.neutralPlaylistDetail(raw)
	case path == "/me/library/playlists":
		return c.neutralPlaylistList(raw)
	case path == "/me/library/songs":
		tracks := make([]kinds.Track, 0)
		for _, item := range appleMusicData(raw) {
			if obj, ok := item.(map[string]any); ok {
				tracks = append(tracks, neutralAppleMusicSong(obj))
			}
		}
		return map[string]any{"tracks": tracks}
	case path == "/me/library/albums":
		albums := make([]kinds.Album, 0)
		for _, item := range appleMusicData(raw) {
			if obj, ok := item.(map[string]any); ok {
				albums = append(albums, neutralAppleMusicAlbum(obj))
			}
		}
		return map[string]any{"albums": albums}
	case path == "/me/library/artists":
		artists := make([]kinds.Artist, 0)
		for _, item := range appleMusicData(raw) {
			if obj, ok := item.(map[string]any); ok {
				artists = append(artists, neutralAppleMusicArtist(obj))
			}
		}
		return map[string]any{"artists": artists}
	default:
		return raw
	}
}

// neutralPlaylistDetail builds the content-transport payload: the playlist
// with its (capped) tracks and the full count. raw is the response envelope;
// expandPages merged the track array in.
func (c *AppleMusicConnector) neutralPlaylistDetail(raw map[string]any) kinds.PlaylistDetail {
	res := firstResource(raw)
	if res == nil {
		return kinds.PlaylistDetail{}
	}
	if total, ok := shared.AsInt(raw["tracks_total"]); ok {
		res["tracks_total"] = total
	}
	base := neutralAppleMusicPlaylist(res)
	detail := kinds.PlaylistDetail{Playlist: base}
	for _, item := range appleMusicTracksField(raw) {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		detail.Tracks = append(detail.Tracks, neutralAppleMusicSong(obj))
	}
	if total, ok := shared.AsInt(raw["tracks_total"]); ok {
		detail.TotalTracks = total
	}
	return detail
}

// appleMusicTracksField returns the track resource array expandPlaylistDetail
// merged in, falling back to a tracks relationship when present.
func appleMusicTracksField(raw map[string]any) []any {
	if items, ok := raw["tracks"].([]any); ok {
		return items
	}
	if rel, ok := raw["relationships"].(map[string]any); ok {
		if tracks, ok := rel["tracks"].(map[string]any); ok {
			return appleMusicData(tracks)
		}
	}
	return nil
}

// neutralPlaylistList projects a library playlist page.
func (c *AppleMusicConnector) neutralPlaylistList(raw map[string]any) map[string]any {
	playlists := make([]kinds.Playlist, 0)
	for _, item := range appleMusicData(raw) {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		playlists = append(playlists, neutralAppleMusicPlaylist(obj))
	}
	out := map[string]any{"playlists": playlists}
	if meta, ok := raw["meta"].(map[string]any); ok {
		out["total"] = meta["total"]
	}
	return out
}

// --- shared transport ---

// apiJSON calls the configured Apple Music endpoint with the minted
// developer token and Music User Token. Same contract as the shared
// placement helper: ErrUnauthorized on 401, capped error text, empty body OK.
func (c *AppleMusicConnector) apiJSON(ctx context.Context, method, path string, query url.Values, body any) (map[string]any, int, error) {
	return apiJSONAuth(ctx, c.host, AppleMusicServiceID, method, path, query, body, c.authHeaders)
}

// --- neutral mapping ---

// appleMusicData returns the resource array of a MusicKit response.
func appleMusicData(raw map[string]any) []any {
	data, _ := raw["data"].([]any)
	return data
}

// appleMusicAttributes reads a resource's attributes object.
func appleMusicAttributes(res map[string]any) map[string]any {
	attrs, _ := res["attributes"].(map[string]any)
	return attrs
}

// appleMusicCatalogID returns the catalog song id behind a library song,
// when it has one (playParams.catalogId).
func appleMusicCatalogID(res map[string]any) string {
	playParams, _ := appleMusicAttributes(res)["playParams"].(map[string]any)
	id, _ := playParams["catalogId"].(string)
	return id
}

// appleMusicISRC returns the ISRC a resource carries, if any: catalog songs
// keep it on attributes; library songs may surface it through an included
// catalog relationship.
func appleMusicISRC(res map[string]any) string {
	if isrc, _ := appleMusicAttributes(res)["isrc"].(string); isrc != "" {
		return strings.ToUpper(strings.TrimSpace(isrc))
	}
	if rel, ok := res["relationships"].(map[string]any); ok {
		if catalog, ok := rel["catalog"].(map[string]any); ok {
			for _, item := range appleMusicData(catalog) {
				obj, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if isrc, _ := appleMusicAttributes(obj)["isrc"].(string); isrc != "" {
					return strings.ToUpper(strings.TrimSpace(isrc))
				}
			}
		}
	}
	return ""
}

// neutralAppleMusicSong maps one library or catalog song resource.
func neutralAppleMusicSong(res map[string]any) kinds.Track {
	attrs := appleMusicAttributes(res)
	id, _ := res["id"].(string)
	name, _ := attrs["name"].(string)
	artistName, _ := attrs["artistName"].(string)
	albumName, _ := attrs["albumName"].(string)
	durationMs, _ := shared.AsInt(attrs["durationInMillis"])
	isrc := appleMusicISRC(res)
	catalogID := appleMusicCatalogID(res)
	urlStr, _ := attrs["url"].(string)

	extra := map[string]any{}
	if catalogID != "" {
		extra["catalog_id"] = catalogID
	}
	if isrc != "" {
		extra["isrc"] = isrc
	}

	artists := []kinds.Artist{}
	if artistName != "" {
		artists = append(artists, kinds.NewArtist(AppleMusicServiceID, AppleMusicServiceName, "", artistName))
	}
	return kinds.Track{
		Name:       name,
		Artists:    kinds.MergeArtists(artists...),
		Album:      kinds.NewAlbum(AppleMusicServiceID, AppleMusicServiceName, "", albumName),
		DurationMs: durationMs,
		EntityID:   isrc,
		Sources: []kinds.TrackSource{{
			Service:     AppleMusicServiceID,
			ServiceName: AppleMusicServiceName,
			ID:          id,
			URL:         urlStr,
			Extra:       shared.ExtraOrNil(extra),
		}},
	}
}

// neutralAppleMusicAlbum maps one library/catalog album resource. Album
// identity is the UPC when the resource carries one.
func neutralAppleMusicAlbum(res map[string]any) kinds.Album {
	attrs := appleMusicAttributes(res)
	id, _ := res["id"].(string)
	name, _ := attrs["name"].(string)
	artistName, _ := attrs["artistName"].(string)
	upc, _ := attrs["upc"].(string)
	urlStr, _ := attrs["url"].(string)

	album := kinds.NewAlbum(AppleMusicServiceID, AppleMusicServiceName, id, name)
	if album == nil {
		return kinds.Album{}
	}
	if upc != "" {
		album.EntityID = strings.ToUpper(strings.TrimSpace(upc))
	}
	if artistName != "" {
		album.Artists = kinds.MergeArtists(kinds.NewArtist(AppleMusicServiceID, AppleMusicServiceName, "", artistName))
	}
	if len(album.Sources) > 0 {
		album.Sources[0].URL = urlStr
	}
	return *album
}

// neutralAppleMusicArtist maps one library/catalog artist resource.
func neutralAppleMusicArtist(res map[string]any) kinds.Artist {
	attrs := appleMusicAttributes(res)
	id, _ := res["id"].(string)
	name, _ := attrs["name"].(string)
	urlStr, _ := attrs["url"].(string)
	artist := kinds.NewArtist(AppleMusicServiceID, AppleMusicServiceName, id, name)
	if len(artist.Sources) > 0 {
		artist.Sources[0].URL = urlStr
	}
	return artist
}

// neutralAppleMusicPlaylist maps one library/catalog playlist resource.
func neutralAppleMusicPlaylist(res map[string]any) kinds.Playlist {
	attrs := appleMusicAttributes(res)
	id, _ := res["id"].(string)
	name, _ := attrs["name"].(string)
	desc := ""
	if d, ok := attrs["description"].(map[string]any); ok {
		desc, _ = d["standard"].(string)
	}
	owner := ""
	trackCount := 0
	if meta, ok := res["tracks_meta"].(map[string]any); ok {
		trackCount, _ = shared.AsInt(meta["total"])
	}
	if total, ok := shared.AsInt(res["tracks_total"]); ok {
		trackCount = total
	}
	extra := map[string]any{}
	if catalogID := appleMusicCatalogID(res); catalogID != "" {
		extra["catalog_id"] = catalogID
	}
	if version, _ := attrs["playParams"].(map[string]any); version != nil {
		if hash, _ := version["versionHash"].(string); hash != "" {
			extra["version_hash"] = hash
		}
	}
	urlStr := ""
	if id != "" {
		urlStr = "https://music.apple.com/" + "playlist/" + id
	}
	if u, _ := attrs["url"].(string); u != "" {
		urlStr = u
	}
	return kinds.Playlist{
		Name:        name,
		Description: desc,
		Owner:       owner,
		TrackCount:  trackCount,
		Sources: []kinds.PlaylistSource{
			kinds.PrimaryPlaylistSource(AppleMusicServiceID, AppleMusicServiceName, id, urlStr, extra),
		},
	}
}

// --- library providers ---

// ListUserPlaylists implements PlaylistProvider over the library playlists
// listing. Truncated reports the page cap was hit.
func (c *AppleMusicConnector) ListUserPlaylists(ctx context.Context) ([]kinds.Playlist, bool, error) {
	return listAppleMusicPaged(ctx, c, "/me/library/playlists", 100,
		func(res map[string]any) (kinds.Playlist, bool) {
			p := neutralAppleMusicPlaylist(res)
			return p, p.Name != ""
		})
}

// ListUserLikedTracks implements LikedTrackProvider: the user's library
// songs — Apple Music's saved-track surface. Favorites ("loved") are not
// enumerable through the documented API, so the library is the read/write
// home for liked/saved tracks (docs/connect/apple-music.md).
func (c *AppleMusicConnector) ListUserLikedTracks(ctx context.Context) ([]kinds.Track, bool, error) {
	return listAppleMusicPaged(ctx, c, "/me/library/songs", 100,
		func(res map[string]any) (kinds.Track, bool) {
			t := neutralAppleMusicSong(res)
			return t, t.Name != ""
		})
}

// ListUserSavedAlbums implements AlbumProvider over library albums.
func (c *AppleMusicConnector) ListUserSavedAlbums(ctx context.Context) ([]kinds.Album, bool, error) {
	return listAppleMusicPaged(ctx, c, "/me/library/albums", 100,
		func(res map[string]any) (kinds.Album, bool) {
			a := neutralAppleMusicAlbum(res)
			return a, a.Name != ""
		})
}

// ListUserFollowedArtists implements ArtistProvider: Apple Music has no
// follow graph; library artists are the user's collection.
func (c *AppleMusicConnector) ListUserFollowedArtists(ctx context.Context) ([]kinds.Artist, bool, error) {
	return listAppleMusicPaged(ctx, c, "/me/library/artists", 100,
		func(res map[string]any) (kinds.Artist, bool) {
			a := neutralAppleMusicArtist(res)
			return a, a.Name != ""
		})
}

// listAppleMusicPaged walks a MusicKit list endpoint under the shared page
// cap. map returns the neutral value and whether it is worth keeping.
func listAppleMusicPaged[T any](ctx context.Context, c *AppleMusicConnector, path string, pageSize int,
	mapFn func(res map[string]any) (T, bool)) ([]T, bool, error) {
	var out []T
	offset := 0
	for {
		q := url.Values{"limit": []string{strconv.Itoa(pageSize)}, "offset": []string{strconv.Itoa(offset)}}
		raw, _, err := c.apiJSON(ctx, "GET", path, q, nil)
		if err != nil {
			return out, false, err
		}
		data := appleMusicData(raw)
		for _, item := range data {
			obj, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if v, keep := mapFn(obj); keep {
				out = append(out, v)
			}
		}
		next, _ := raw["next"].(string)
		if next == "" || len(data) == 0 {
			return out, false, nil
		}
		offset += len(data)
		if len(out) >= shared.LibraryPageCap {
			return out[:shared.LibraryPageCap], true, nil
		}
	}
}

// --- catalog helpers ---

// storefront resolves the user's storefront id (needed for catalog calls),
// cached; "us" is the fail-open fallback so name/ISRC lookups still run.
func (c *AppleMusicConnector) storefront(ctx context.Context) string {
	const cacheKey = "apple-music:storefront"
	if cached, ok := c.host.CacheGet(cacheKey); ok && cached != "" {
		return cached
	}
	raw, _, err := c.apiJSON(ctx, "GET", "/me/storefront", nil, nil)
	if err != nil {
		return "us"
	}
	for _, item := range appleMusicData(raw) {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if id, _ := obj["id"].(string); id != "" {
			c.host.CacheSet(cacheKey, id, appleMusicStorefrontCacheTTL)
			return id
		}
	}
	return "us"
}

// catalogSong is one catalog hit used for placement matching.
type catalogSong struct {
	ID         string
	Name       string
	DurationMs int
	ISRC       string
	URL        string
}

// catalogSongFrom maps a catalog songs resource.
func catalogSongFrom(res map[string]any) catalogSong {
	attrs := appleMusicAttributes(res)
	id, _ := res["id"].(string)
	durationMs, _ := shared.AsInt(attrs["durationInMillis"])
	urlStr, _ := attrs["url"].(string)
	name, _ := attrs["name"].(string)
	return catalogSong{
		ID:         id,
		Name:       name,
		DurationMs: durationMs,
		ISRC:       appleMusicISRC(res),
		URL:        urlStr,
	}
}

// catalogByISRC looks songs up by ISRC — the reliable identity lookup that
// makes Apple Music a high-fidelity mirror target. Returns every hit so the
// caller can duration-guard the pick (the same ISRC can appear on several
// album releases).
func (c *AppleMusicConnector) catalogByISRC(ctx context.Context, isrc string) ([]catalogSong, error) {
	if isrc == "" {
		return nil, nil
	}
	q := url.Values{"filter[isrc]": []string{isrc}}
	raw, _, err := c.apiJSON(ctx, "GET", "/catalog/"+url.PathEscape(c.storefront(ctx))+"/songs", q, nil)
	if err != nil {
		return nil, err
	}
	var out []catalogSong
	for _, item := range appleMusicData(raw) {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if s := catalogSongFrom(obj); s.ID != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

// catalogSearch runs a name query against the catalog songs search.
func (c *AppleMusicConnector) catalogSearch(ctx context.Context, term string, limit int) ([]catalogSong, error) {
	if strings.TrimSpace(term) == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 25 {
		limit = 5
	}
	q := url.Values{"term": []string{term}, "types": []string{"songs"}, "limit": []string{strconv.Itoa(limit)}}
	raw, _, err := c.apiJSON(ctx, "GET", "/catalog/"+url.PathEscape(c.storefront(ctx))+"/songs", q, nil)
	if err != nil {
		return nil, err
	}
	var out []catalogSong
	for _, item := range appleMusicData(raw) {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if s := catalogSongFrom(obj); s.ID != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

// catalogByIDs fetches catalog songs by id in one call (ISRC enrichment).
func (c *AppleMusicConnector) catalogByIDs(ctx context.Context, ids []string) ([]catalogSong, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	q := url.Values{"ids": []string{strings.Join(ids, ",")}}
	raw, _, err := c.apiJSON(ctx, "GET", "/catalog/"+url.PathEscape(c.storefront(ctx))+"/songs", q, nil)
	if err != nil {
		return nil, err
	}
	var out []catalogSong
	for _, item := range appleMusicData(raw) {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if s := catalogSongFrom(obj); s.ID != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

// --- misc helpers ---

// appleMusicDurationGuard picks the hit whose duration is compatible with
// the requested track, else the first hit of an ISRC lookup (durable id
// match) and nothing for a name lookup (a wildly different length is a
// different recording — issue #366's guard).
func appleMusicDurationGuard(want kinds.Track, hits []catalogSong, byISRC bool) (catalogSong, bool) {
	for _, h := range hits {
		if kinds.DurationCompatible(want, kinds.Track{DurationMs: h.DurationMs}) {
			return h, true
		}
	}
	if byISRC && len(hits) > 0 {
		return hits[0], true
	}
	return catalogSong{}, false
}

var (
	_ ServiceConnector                = (*AppleMusicConnector)(nil)
	_ kinds.PlaylistProvider          = (*AppleMusicConnector)(nil)
	_ kinds.LikedTrackProvider        = (*AppleMusicConnector)(nil)
	_ kinds.AlbumProvider             = (*AppleMusicConnector)(nil)
	_ kinds.ArtistProvider            = (*AppleMusicConnector)(nil)
	_ kinds.PlaylistItemsProvider     = (*AppleMusicConnector)(nil)
	_ kinds.LikedTrackPageProvider    = (*AppleMusicConnector)(nil)
	_ kinds.PlaylistMetaProvider      = (*AppleMusicConnector)(nil)
	_ kinds.PlaylistItemWriteProvider = (*AppleMusicConnector)(nil)
	_ kinds.LikedTrackWriteProvider   = (*AppleMusicConnector)(nil)
	_ kinds.AlbumWriteProvider        = (*AppleMusicConnector)(nil)
)
