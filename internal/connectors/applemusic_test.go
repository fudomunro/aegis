package connectors

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/kinds"
)

// testAppleMusicKey returns a fresh P-256 key and its .p8 PEM, the shape the
// operator pastes into the setup form.
func testAppleMusicKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	return key, pemText
}

// newAppleMusicTest wires a connector against a mock upstream with full
// MusicKit credentials in service headers.
func newAppleMusicTest(t *testing.T, up http.Handler) (*AppleMusicConnector, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(up)
	t.Cleanup(ts.Close)
	_, pemText := testAppleMusicKey(t)
	th := newTestHostWithService(AppleMusicServiceID, ts.URL, "https://aegis.example.com")
	svc := th.services[AppleMusicServiceID]
	svc.Headers = map[string]string{
		appleMusicKeyIDHeader:      "KEY1234567",
		appleMusicIssuerIDHeader:   "00000000-0000-0000-0000-000000000000",
		appleMusicPrivateKeyHeader: pemText,
		appleMusicUserTokenHeader:  "mut-token-value",
	}
	return NewAppleMusicConnector(th), ts
}

func TestAppleMusicDeveloperToken_MintsES256JWT(t *testing.T) {
	key, pemText := testAppleMusicKey(t)
	parsed, err := parseAppleMusicPrivateKey(pemText)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	creds := appleMusicCreds{
		KeyID:      "KEY1234567",
		IssuerID:   "issuer-uuid",
		PrivateKey: parsed,
	}
	now := time.Unix(1_700_000_000, 0)
	tok, err := mintAppleMusicDeveloperToken(creds, now)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts, want 3", len(parts))
	}
	enc := base64.RawURLEncoding
	headJSON, err := enc.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	var head map[string]string
	if err := json.Unmarshal(headJSON, &head); err != nil {
		t.Fatal(err)
	}
	if head["alg"] != "ES256" || head["kid"] != "KEY1234567" {
		t.Fatalf("header = %v, want ES256 + kid", head)
	}
	claimsJSON, err := enc.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		t.Fatal(err)
	}
	if claims["iss"] != "issuer-uuid" {
		t.Fatalf("iss = %v", claims["iss"])
	}
	if exp := int64(claims["exp"].(float64)); exp != now.Unix()+int64(appleMusicTokenTTL/time.Second) {
		t.Fatalf("exp = %d, want %d", exp, now.Unix()+int64(appleMusicTokenTTL/time.Second))
	}
	// Verify the raw R||S signature against the minted key.
	sig, err := enc.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	if len(sig) != 64 {
		t.Fatalf("signature length = %d, want 64 (raw R||S)", len(sig))
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&key.PublicKey, digest[:], r, s) {
		t.Fatal("signature does not verify")
	}
}

func TestAppleMusicParsePrivateKey_AcceptsPKCS8AndSEC1(t *testing.T) {
	key, pemText := testAppleMusicKey(t)
	for _, tc := range []struct {
		name string
		pem  string
	}{
		{"pkcs8", pemText},
		{"sec1", string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: marshalSEC1Key(t, key)}))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseAppleMusicPrivateKey(tc.pem)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if !got.Equal(key) {
				t.Fatal("parsed key differs")
			}
		})
	}
	if _, err := parseAppleMusicPrivateKey("not pem"); err == nil {
		t.Fatal("want error for non-PEM")
	}
}

func TestAppleMusicCredentials_RequireAllHalves(t *testing.T) {
	th := newTestHostWithService(AppleMusicServiceID, "http://127.0.0.1:0", "https://aegis.example.com")
	c := NewAppleMusicConnector(th)
	if _, err := c.credentials(); err == nil {
		t.Fatal("want error with no credentials")
	}
	_, pemText := testAppleMusicKey(t)
	th.services[AppleMusicServiceID].Headers = map[string]string{
		appleMusicKeyIDHeader:      "K",
		appleMusicIssuerIDHeader:   "I",
		appleMusicPrivateKeyHeader: pemText,
	}
	creds, err := c.credentials()
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}
	if creds.UserToken != "" {
		t.Fatal("user token should be optional to parse but empty")
	}
	tok, err := c.developerToken(creds)
	if err != nil || tok == "" {
		t.Fatalf("developer token: %v %q", err, tok)
	}
	// Cached: a second mint returns the same token.
	if again, _ := c.developerToken(creds); again != tok {
		t.Fatal("developer token was re-minted")
	}
}

func TestAppleMusicCredentials_CachesParsedKey(t *testing.T) {
	th := newTestHostWithService(AppleMusicServiceID, "http://127.0.0.1:0", "https://aegis.example.com")
	_, pemText := testAppleMusicKey(t)
	th.services[AppleMusicServiceID].Headers = map[string]string{
		appleMusicKeyIDHeader:      "K",
		appleMusicIssuerIDHeader:   "I",
		appleMusicPrivateKeyHeader: pemText,
	}
	c := NewAppleMusicConnector(th)
	first, err := c.credentials()
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}
	again, err := c.credentials()
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}
	if first.PrivateKey != again.PrivateKey {
		t.Fatal("parsed key was re-parsed instead of cached")
	}
}

func TestAppleMusicAuthHeaders_SendsBothCredentials(t *testing.T) {
	var gotAuth, gotUserToken string
	c, _ := newAppleMusicTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotUserToken = r.Header.Get(appleMusicUserTokenHeader)
		writeMirrorJSON(w, map[string]any{"data": []any{}})
	}))
	tok, extra := c.authHeaders(slog.Default())
	if tok == "" {
		t.Fatal("no developer token")
	}
	if extra.Get(appleMusicUserTokenHeader) != "mut-token-value" {
		t.Fatalf("user token = %q", extra.Get(appleMusicUserTokenHeader))
	}
	if _, _, err := c.apiJSON(t.Context(), "GET", "/me/storefront", nil, nil); err != nil {
		t.Fatalf("apiJSON: %v", err)
	}
	if !strings.HasPrefix(gotAuth, "Bearer eyJ") {
		t.Fatalf("Authorization = %q, want Bearer JWT", gotAuth)
	}
	if gotUserToken != "mut-token-value" {
		t.Fatalf("Music-User-Token = %q", gotUserToken)
	}
}

func TestAppleMusicListUserPlaylists(t *testing.T) {
	c, _ := newAppleMusicTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/me/library/playlists" && r.URL.Query().Get("offset") != "2":
			writeMirrorJSON(w, map[string]any{
				"data": []any{
					libraryPlaylistResource("p.1", "Road Trip", "Long drive"),
					libraryPlaylistResource("p.2", "Focus", ""),
				},
				"next": "/v1/me/library/playlists?offset=2",
				"meta": map[string]any{"total": 3},
			})
		case r.URL.Path == "/me/library/playlists":
			writeMirrorJSON(w, map[string]any{
				"data": []any{libraryPlaylistResource("p.3", "Late", "")},
				"meta": map[string]any{"total": 3},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	playlists, truncated, err := c.ListUserPlaylists(t.Context())
	if err != nil {
		t.Fatalf("ListUserPlaylists: %v", err)
	}
	if truncated {
		t.Fatal("unexpected truncation")
	}
	if len(playlists) != 3 {
		t.Fatalf("got %d playlists, want 3", len(playlists))
	}
	if playlists[0].Name != "Road Trip" || playlists[0].Description != "Long drive" {
		t.Fatalf("playlist[0] = %+v", playlists[0])
	}
	if playlists[0].Sources[0].Service != AppleMusicServiceID || playlists[0].Sources[0].ID != "p.1" {
		t.Fatalf("source = %+v", playlists[0].Sources)
	}
}

func TestAppleMusicListUserLikedTracks_ISRCFromCatalogRelationship(t *testing.T) {
	c, _ := newAppleMusicTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/me/library/songs" {
			http.NotFound(w, r)
			return
		}
		writeMirrorJSON(w, map[string]any{
			"data": []any{
				map[string]any{
					"id":   "a.1",
					"type": "library-songs",
					"attributes": map[string]any{
						"name":             "Entropy",
						"artistName":       "Beach Bunny",
						"albumName":        "Emotional Creature",
						"durationInMillis": 221000,
						"playParams":       map[string]any{"id": "a.1", "kind": "song", "isLibrary": true, "catalogId": "1613600188"},
					},
					"relationships": map[string]any{
						"catalog": map[string]any{"data": []any{
							map[string]any{"id": "1613600188", "type": "songs",
								"attributes": map[string]any{"isrc": "USQE92100257"}},
						}},
					},
				},
			},
			"meta": map[string]any{"total": 1},
		})
	}))
	tracks, truncated, err := c.ListUserLikedTracks(t.Context())
	if err != nil {
		t.Fatalf("ListUserLikedTracks: %v", err)
	}
	if truncated || len(tracks) != 1 {
		t.Fatalf("got %d tracks (truncated=%v)", len(tracks), truncated)
	}
	got := tracks[0]
	if got.Name != "Entropy" || got.EntityID != "USQE92100257" {
		t.Fatalf("track = %+v", got)
	}
	if got.Sources[0].Extra["catalog_id"] != "1613600188" {
		t.Fatalf("catalog_id = %v", got.Sources[0].Extra["catalog_id"])
	}
}

func TestAppleMusicListUserSavedAlbumsAndArtists(t *testing.T) {
	c, _ := newAppleMusicTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/me/library/albums":
			writeMirrorJSON(w, map[string]any{"data": []any{
				map[string]any{"id": "al.1", "attributes": map[string]any{"name": "Echoes", "artistName": "Dirk Maassen", "upc": "00886448619161"}},
			}})
		case "/me/library/artists":
			writeMirrorJSON(w, map[string]any{"data": []any{
				map[string]any{"id": "ar.1", "attributes": map[string]any{"name": "Beach Bunny"}},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	albums, _, err := c.ListUserSavedAlbums(t.Context())
	if err != nil || len(albums) != 1 {
		t.Fatalf("albums: %v %d", err, len(albums))
	}
	if albums[0].EntityID != "00886448619161" {
		t.Fatalf("album entity = %q", albums[0].EntityID)
	}
	artists, _, err := c.ListUserFollowedArtists(t.Context())
	if err != nil || len(artists) != 1 || artists[0].Name != "Beach Bunny" {
		t.Fatalf("artists: %v %+v", err, artists)
	}
}

func TestAppleMusicProxy_PlaylistDetail(t *testing.T) {
	c, _ := newAppleMusicTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/me/library/playlists/p.1":
			writeMirrorJSON(w, map[string]any{"data": []any{libraryPlaylistResource("p.1", "Road Trip", "")}})
		case r.URL.Path == "/me/library/playlists/p.1/tracks":
			writeMirrorJSON(w, map[string]any{
				"data": []any{librarySongResource("a.1", "Entropy", "Beach Bunny", 221000, "1613600188")},
				"meta": map[string]any{"total": 1},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/services/apple-music/playlists/p.1", nil)
	c.HandleProxy(rr, req, "/playlists/p.1")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var detail kinds.PlaylistDetail
	if err := json.Unmarshal(rr.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if detail.Name != "Road Trip" || detail.TotalTracks != 1 {
		t.Fatalf("detail = %+v", detail)
	}
	if len(detail.Tracks) != 1 || detail.Tracks[0].Name != "Entropy" {
		t.Fatalf("tracks = %+v", detail.Tracks)
	}
}

func TestAppleMusicSystemInstructions_PointAtLibrary(t *testing.T) {
	c := NewAppleMusicConnector(newTestHost("https://aegis.example.com"))
	got := c.SystemInstructions("https://aegis.example.com")
	for _, want := range []string{"/me/playlists", "/me/tracks", "/v1/services/apple-music/playlists/{id}"} {
		if !strings.Contains(got, want) {
			t.Fatalf("instructions missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "issue #") {
		t.Fatal("agent strings must not cite issue numbers")
	}
}

// --- fixtures ---

func libraryPlaylistResource(id, name, description string) map[string]any {
	attrs := map[string]any{
		"name":       name,
		"playParams": map[string]any{"id": id, "kind": "playlist", "isLibrary": true},
	}
	if description != "" {
		attrs["description"] = map[string]any{"standard": description}
	}
	return map[string]any{"id": id, "type": "library-playlists", "attributes": attrs}
}

func librarySongResource(id, name, artist string, durationMs int, catalogID string) map[string]any {
	playParams := map[string]any{"id": id, "kind": "song", "isLibrary": true}
	if catalogID != "" {
		playParams["catalogId"] = catalogID
	}
	return map[string]any{
		"id":   id,
		"type": "library-songs",
		"attributes": map[string]any{
			"name":             name,
			"artistName":       artist,
			"durationInMillis": durationMs,
			"playParams":       playParams,
		},
	}
}

var _ kinds.PlaylistProvider = (*AppleMusicConnector)(nil)

// marshalSEC1Key exports a key in the SEC1 EC PRIVATE KEY form.
func marshalSEC1Key(t *testing.T, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func TestAppleMusicTransformResponse_ListProjections(t *testing.T) {
	c := NewAppleMusicConnector(newTestHost("https://aegis.example.com"))
	playlistList := `{"data":[{"id":"p.1","attributes":{"name":"Road Trip"}}]}`
	got := c.TransformResponse("GET", "/me/library/playlists", []byte(playlistList), slog.Default())
	out, ok := got.(map[string]any)
	if !ok || len(out["playlists"].([]kinds.Playlist)) != 1 {
		t.Fatalf("playlist list projection = %T %+v", got, got)
	}
	songList := `{"data":[{"id":"a.1","attributes":{"name":"Entropy","playParams":{"catalogId":"9"}}}]}`
	if got := c.TransformResponse("GET", "/me/library/songs", []byte(songList), slog.Default()); got.(map[string]any)["tracks"].([]kinds.Track)[0].Name != "Entropy" {
		t.Fatalf("song list projection = %+v", got)
	}
	albumList := `{"data":[{"id":"al.1","attributes":{"name":"Echoes","upc":"00886448619161"}}]}`
	if got := c.TransformResponse("GET", "/me/library/albums", []byte(albumList), slog.Default()); got.(map[string]any)["albums"].([]kinds.Album)[0].EntityID != "00886448619161" {
		t.Fatalf("album list projection = %+v", got)
	}
	artistList := `{"data":[{"id":"ar.1","attributes":{"name":"Beach Bunny"}}]}`
	if got := c.TransformResponse("GET", "/me/library/artists", []byte(artistList), slog.Default()); got.(map[string]any)["artists"].([]kinds.Artist)[0].Name != "Beach Bunny" {
		t.Fatalf("artist list projection = %+v", got)
	}
	// Errors and unknown shapes pass through untouched.
	errBody := `{"errors":[{"status":"404"}]}`
	if got := c.TransformResponse("GET", "/me/library/songs", []byte(errBody), slog.Default()); got.(map[string]any)["errors"] == nil {
		t.Fatalf("error payload = %+v", got)
	}
	if _, _, err := mapAppleMusicUpstreamPath("/playlists/", nil); err == nil {
		t.Fatal("want invalid playlist path error")
	}
	if p, _, err := mapAppleMusicUpstreamPath("/playlists/p.1", nil); err != nil || p != "/me/library/playlists/p.1" {
		t.Fatalf("mapped path = %q err=%v", p, err)
	}
}
