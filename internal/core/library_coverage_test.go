package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/core/tokens"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

func TestLibraryHandlers_MethodNotAllowed(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	for _, h := range []http.HandlerFunc{
		srv.libraryHandler("tracks"),
		srv.libraryHandler("albums"),
		srv.libraryHandler("artists"),
		srv.libraryHandler("playlists"),
		srv.libraryHandler("contacts"),
		srv.libraryHandler("photos"),
		srv.libraryHandler("events"),
	} {
		req := httptest.NewRequest("POST", "/v1/library/x", nil)
		w := httptest.NewRecorder()
		h(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want 405", w.Code)
		}
	}
}

// seedLibraryAggregate stores a merged aggregate under the kind's cache key
// so a handler request can be served from cache (issue #273 format).
func seedLibraryAggregate[T any](t *testing.T, srv *Server, kindID string, items []T) {
	t.Helper()
	raw, err := json.Marshal(kinds.Fetch[T]{Items: items, BySvc: map[string]kinds.LibraryServiceStatus{}, NTargets: 1})
	if err != nil {
		t.Fatal(err)
	}
	srv.cache.Set("library:"+kindID+"|agg", string(raw), time.Minute)
}

func TestLibraryTracks_CacheHit(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": &fakeLikedTrackProvider{tracks: []kinds.Track{{Name: "Solo"}}},
	}
	seedLibraryAggregate(t, srv, "tracks", []kinds.Track{{Name: "Cached"}})

	req := httptest.NewRequest("GET", "/v1/library/tracks", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("tracks"))(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if w.Header().Get("X-Aegis-Cache") != "hit" {
		t.Fatalf("cache header = %q", w.Header().Get("X-Aegis-Cache"))
	}
	if !strings.Contains(w.Body.String(), "Cached") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestLibraryTracks_ErrorService(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": &fakeLikedTrackProvider{err: errors.New("boom")},
	}

	req := httptest.NewRequest("GET", "/v1/library/tracks", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("tracks"))(w, req)

	var resp kinds.TrackList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ByService["spotify"].Status != kinds.LibraryStatusError {
		t.Fatalf("status = %+v", resp.ByService["spotify"])
	}
	if resp.Tracks == nil || len(resp.Tracks) != 0 {
		t.Fatalf("tracks = %+v", resp.Tracks)
	}
	if _, ok := srv.cache.Get("library:tracks"); ok {
		t.Fatal("degraded library aggregate should not be cached")
	}
}

func TestLibraryTracks_NoProviders(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest("GET", "/v1/library/tracks", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("tracks"))(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestLibraryAlbums_UnauthorizedAndError(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
			{ID: "youtube-music", Name: "YouTube Music", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify":       &fakeAlbumProvider{err: kinds.ErrUnauthorized},
		"youtube-music": &fakeAlbumProvider{err: errors.New("upstream")},
	}

	req := httptest.NewRequest("GET", "/v1/library/albums", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("albums"))(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp kinds.AlbumList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ByService["spotify"].Status != kinds.LibraryStatusUnauthorized {
		t.Fatalf("spotify = %+v", resp.ByService["spotify"])
	}
	if resp.ByService["youtube-music"].Status != kinds.LibraryStatusError {
		t.Fatalf("youtube = %+v", resp.ByService["youtube-music"])
	}
	if _, ok := srv.cache.Get("library:albums"); ok {
		t.Fatal("degraded library aggregate should not be cached")
	}
}

func TestLibraryAlbums_NoProviders(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest("GET", "/v1/library/albums", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("albums"))(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestLibraryAlbums_SkipsBlockedService(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
			{ID: "tidal", Name: "Tidal", Action: "block"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": &fakeAlbumProvider{albums: []kinds.Album{{Name: "A", Sources: []kinds.AlbumSource{{Service: "spotify", ID: "1"}}}}},
		"tidal":   &fakeAlbumProvider{albums: []kinds.Album{{Name: "Blocked", Sources: []kinds.AlbumSource{{Service: "tidal", ID: "2"}}}}},
	}

	req := httptest.NewRequest("GET", "/v1/library/albums", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("albums"))(w, req)

	var resp kinds.AlbumList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 1 || resp.Albums[0].Name != "A" {
		t.Fatalf("resp = %+v", resp)
	}
	if _, ok := resp.ByService["tidal"]; ok {
		t.Fatalf("blocked service should not appear: %+v", resp.ByService)
	}
}

func TestLibraryArtists_UnauthorizedErrorAndCache(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": &fakeArtistProvider{err: kinds.ErrUnauthorized},
	}

	req := httptest.NewRequest("GET", "/v1/library/artists", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("artists"))(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp kinds.ArtistList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ByService["spotify"].Status != kinds.LibraryStatusUnauthorized {
		t.Fatalf("status = %+v", resp.ByService["spotify"])
	}

	// Degraded aggregates must not be cached (issue #104). After the provider
	// recovers, the next request re-fetches instead of replaying unauthorized.
	srv.connectors["spotify"] = &fakeArtistProvider{artists: []kinds.Artist{kinds.NewArtist("spotify", "Spotify", "1", "X")}}
	w2 := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("artists"))(w2, req)
	if w2.Header().Get("X-Aegis-Cache") != "miss" {
		t.Fatalf("expected cache miss after recovery, got %q", w2.Header().Get("X-Aegis-Cache"))
	}
	var recovered kinds.ArtistList
	if err := json.Unmarshal(w2.Body.Bytes(), &recovered); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if recovered.ByService["spotify"].Status != kinds.LibraryStatusOK {
		t.Fatalf("status = %+v", recovered.ByService["spotify"])
	}

	// Healthy aggregate is cached.
	w3 := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("artists"))(w3, req)
	if w3.Header().Get("X-Aegis-Cache") != "hit" {
		t.Fatalf("expected cache hit on healthy aggregate, got %q", w3.Header().Get("X-Aegis-Cache"))
	}
}

func TestLibraryArtists_ErrorService(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": &fakeArtistProvider{err: errors.New("nope")},
	}

	req := httptest.NewRequest("GET", "/v1/library/artists", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("artists"))(w, req)

	var resp kinds.ArtistList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ByService["spotify"].Status != kinds.LibraryStatusError {
		t.Fatalf("status = %+v", resp.ByService["spotify"])
	}
	// Error bodies must not stick in the cache (issue #104).
	if _, ok := srv.cache.Get("library:artists"); ok {
		t.Fatal("degraded library aggregate should not be cached")
	}
}

func TestLibraryArtists_PermissionDenied(t *testing.T) {
	cfg := &Config{
		typePolicies: policy.DataGrants{"artists": {Default: shared.AccessNone}},
		Services: []Service{{
			ID: "spotify", Name: "Spotify", Action: "inject",
		}},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": &fakeArtistProvider{artists: []kinds.Artist{kinds.NewArtist("spotify", "Spotify", "1", "X")}},
	}

	req := httptest.NewRequest("GET", "/v1/library/artists", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("artists"))(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestLibraryArtists_NoProviders(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest("GET", "/v1/library/artists", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("artists"))(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestLibraryReadableHelpers(t *testing.T) {
	srv := testServerWithBase(&Config{
		Services: []Service{
			{ID: "a", Name: "A", Action: "inject"},
			{ID: "b", Name: "B", Action: "block"},
		},
	}, "https://aegis.example.com")

	if !kindReadable("tracks", srv) {
		t.Fatal("service a should allow tracks read")
	}
	if !kindReadable("albums", srv) || !kindReadable("artists", srv) || !kindReadable("playlists", srv) ||
		!kindReadable("contacts", srv) || !kindReadable("photos", srv) {
		t.Fatal("service a should allow all library reads")
	}

	// Denied owner grant + only a blocked service remains → nothing readable.
	srv2 := testServerWithBase(&Config{
		typePolicies: policy.DataGrants{shared.DefaultKey: {Default: shared.AccessNone}},
		Services: []Service{
			{ID: "b", Name: "B", Action: "block"},
			{ID: "c", Name: "C", Action: "inject"},
		},
	}, "https://aegis.example.com")
	if kindReadable("tracks", srv2) || kindReadable("albums", srv2) || kindReadable("artists", srv2) ||
		kindReadable("playlists", srv2) || kindReadable("contacts", srv2) || kindReadable("photos", srv2) {
		t.Fatal("expected no readable library types")
	}
}

func TestHasLibraryProviders(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	if kindHasProviders("playlists", srv) || kindHasProviders("tracks", srv) || kindHasProviders("albums", srv) ||
		kindHasProviders("artists", srv) || kindHasProviders("contacts", srv) || kindHasProviders("photos", srv) {
		t.Fatal("empty connectors should report no providers")
	}
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": connectors.NewSpotifyConnector(srv),
	}
	if !kindHasProviders("playlists", srv) || !kindHasProviders("tracks", srv) || !kindHasProviders("albums", srv) || !kindHasProviders("artists", srv) {
		t.Fatal("spotify connector implements playlist/track/album/artist providers")
	}
}

func TestLibrarySystemInstructions_ContainEndpoints(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	for _, s := range []string{
		srv.libraryInstructionsFor("playlists"),
		srv.libraryInstructionsFor("tracks"),
		srv.libraryInstructionsFor("albums"),
		srv.libraryInstructionsFor("artists"),
		srv.libraryInstructionsFor("contacts"),
	} {
		if !strings.Contains(s, "https://aegis.example.com/v1/library/") {
			t.Fatalf("instructions missing base library path: %s", s)
		}
	}
	if !strings.Contains(srv.libraryInstructionsFor("artists"), "/v1/library/artists") {
		t.Fatal("artists instructions missing endpoint")
	}
	if !strings.Contains(srv.libraryInstructionsFor("albums"), "/v1/library/albums") {
		t.Fatal("albums instructions missing endpoint")
	}
}

func TestPreRequest_ArtistsWithoutServiceName(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{"spotify": connectors.NewSpotifyConnector(srv)}

	payload := PreRequestPayload{
		Metadata:    RequestMetadata{RequestID: "req-artist", LoginName: "alice"},
		UserMessage: "What artists do I follow?",
		RequestBody: mustJSON(t, map[string]any{
			"messages": []map[string]string{{"role": "user", "content": "What artists do I follow?"}},
		}),
	}
	req := httptest.NewRequest("POST", "/v1/pre-request", strings.NewReader(string(mustJSON(t, payload))))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandlePreRequest)(w, req)

	var resp GuardrailResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Action != ActionModify {
		t.Fatalf("action = %s", resp.Action)
	}
	if !strings.Contains(string(resp.RequestBody), "/v1/library/artists") {
		t.Fatalf("missing artists endpoint: %s", resp.RequestBody)
	}
}

func TestMergeTracksSync_MergesAlbumAndDuration(t *testing.T) {
	merged := kinds.MergeTracksSync([]kinds.Track{
		{
			Name:    "Song",
			Artists: []kinds.Artist{kinds.NewArtist("spotify", "Spotify", "", "A")},
			Sources: []kinds.TrackSource{{Service: "spotify", ServiceName: "Spotify", ID: "1"}},
		},
		{
			Name:       "Song",
			Artists:    []kinds.Artist{kinds.NewArtist("youtube-music", "YouTube Music", "", "A")},
			Album:      kinds.NewAlbum("youtube-music", "YouTube Music", "al", "LP"),
			DurationMs: 210000,
			Duration:   "PT3M30S",
			Sources:    []kinds.TrackSource{{Service: "youtube-music", ServiceName: "YouTube Music", ID: "2"}},
			Extra:      map[string]any{"kind": "video"},
		},
	})
	if len(merged) != 1 {
		t.Fatalf("got %d tracks", len(merged))
	}
	t0 := merged[0]
	if t0.Album == nil || t0.Album.Name != "LP" {
		t.Fatalf("album = %+v", t0.Album)
	}
	if t0.DurationMs != 210000 || t0.Duration != "PT3M30S" {
		t.Fatalf("duration = %d %s", t0.DurationMs, t0.Duration)
	}
	if t0.Extra["kind"] != "video" {
		t.Fatalf("extra = %+v", t0.Extra)
	}
	if len(t0.Sources) != 2 {
		t.Fatalf("sources = %+v", t0.Sources)
	}
}

func TestMergeTracksSync_EmptyNameFallsBackToSourceID(t *testing.T) {
	merged := kinds.MergeTracksSync([]kinds.Track{
		{Sources: []kinds.TrackSource{{Service: "spotify", ID: "abc"}}},
		{Sources: []kinds.TrackSource{{Service: "spotify", ID: "abc"}}},
	})
	if len(merged) != 1 {
		t.Fatalf("got %d, want 1 merged empty-name track", len(merged))
	}
	if len(merged[0].Sources) != 2 {
		t.Fatalf("sources = %+v", merged[0].Sources)
	}
}

func TestTrackIdentityKey_Variants(t *testing.T) {
	if kinds.TrackIdentityKey(kinds.Track{Name: "  Hello  World ", Artists: []kinds.Artist{{Name: "The  Artist"}}}) != "hello world|the artist" {
		t.Fatal("normalize failed")
	}
	if kinds.TrackIdentityKey(kinds.Track{Name: "Solo"}) != "solo" {
		t.Fatal("title-only key")
	}
	if !strings.HasPrefix(kinds.TrackIdentityKey(kinds.Track{Sources: []kinds.TrackSource{{Service: "spotify", ID: "x"}}}), "\x00spotify:x") {
		t.Fatal("empty title fallback")
	}
	if kinds.TrackIdentityKey(kinds.Track{}) != "\x00" {
		t.Fatal("fully empty key")
	}
}

func TestAlbumIdentityKey_Variants(t *testing.T) {
	if kinds.AlbumIdentityKey(kinds.Album{Name: "LP", Artists: []kinds.Artist{{Name: "Band"}}}) != "lp|band" {
		t.Fatal("title+artist")
	}
	if kinds.AlbumIdentityKey(kinds.Album{Name: "LP"}) != "lp" {
		t.Fatal("title only")
	}
	if !strings.HasPrefix(kinds.AlbumIdentityKey(kinds.Album{Sources: []kinds.AlbumSource{{Service: "spotify", ID: "z"}}}), "\x00spotify:z") {
		t.Fatal("empty fallback")
	}
}

func TestMergeAlbumsSync_MergesSourcesAndArtists(t *testing.T) {
	merged := kinds.MergeAlbumsSync([]kinds.Album{
		{
			Name:    "LP",
			Artists: []kinds.Artist{kinds.NewArtist("spotify", "Spotify", "", "Band")},
			Sources: []kinds.AlbumSource{{Service: "spotify", ServiceName: "Spotify", ID: "1"}},
		},
		{
			Name:    "LP",
			Artists: []kinds.Artist{kinds.NewArtist("youtube-music", "YouTube Music", "", "Band")},
			Sources: []kinds.AlbumSource{{Service: "youtube-music", ServiceName: "YouTube Music", ID: "2"}},
		},
		{
			Name:    "Other",
			Sources: []kinds.AlbumSource{{Service: "spotify", ServiceName: "Spotify", ID: "3"}},
		},
	})
	if len(merged) != 2 {
		t.Fatalf("got %d albums, want 2", len(merged))
	}
	// Sorted by identity key: lp|band then other
	if merged[0].Name != "LP" || len(merged[0].Sources) != 2 {
		t.Fatalf("first = %+v", merged[0])
	}
	if merged[1].Name != "Other" {
		t.Fatalf("second = %+v", merged[1])
	}
}

func TestMergeArtistLibrary_EmptyNameSkipped(t *testing.T) {
	merged := kinds.MergeArtistLibrary([]kinds.Artist{
		{Name: ""},
		kinds.NewArtist("spotify", "Spotify", "1", "A"),
		kinds.NewArtist("youtube-music", "YouTube Music", "2", "A"),
	})
	if len(merged) != 1 || merged[0].Name != "A" || len(merged[0].Sources) != 2 {
		t.Fatalf("merged = %+v", merged)
	}
}

func TestPrimaryService_Empty(t *testing.T) {
	if (kinds.Playlist{}).PrimaryService() != "" {
		t.Fatal("expected empty primary service")
	}
	if (kinds.Playlist{Sources: []kinds.PlaylistSource{{Service: "spotify"}}}).PrimaryService() != "spotify" {
		t.Fatal("expected spotify")
	}
}

func TestNormalizeHandleList(t *testing.T) {
	got := kinds.NormalizeHandleList([]string{"@Alice", "alice", "  ", "bob"})
	if len(got) != 2 || got[0] != "alice" || got[1] != "bob" {
		t.Fatalf("got = %#v", got)
	}
	if kinds.NormalizeHandleList(nil) != nil {
		t.Fatal("nil in → nil out")
	}
	if kinds.NormalizeHandleList([]string{"  "}) != nil {
		t.Fatal("all-empty → nil")
	}
}

func TestHandleAdminCache_ListsEntries(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.cache.Set("library:tracks", `{"tracks":[]}`, time.Minute)

	req := httptest.NewRequest("GET", "/v1/admin/cache", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminCache)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var payload struct {
		Live    int         `json:"live"`
		Total   int         `json:"total"`
		Entries []CacheItem `json:"entries"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Live != 1 || payload.Total != 1 {
		t.Fatalf("live/total = %d/%d", payload.Live, payload.Total)
	}
	if len(payload.Entries) != 1 || payload.Entries[0].Key != "library:tracks" {
		t.Fatalf("entries = %+v", payload.Entries)
	}
}

func TestHandleAdmin_CaseAndMethod(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")

	req := httptest.NewRequest("POST", "/v1/admin", nil)
	w := httptest.NewRecorder()
	srv.HandleAdmin(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d", w.Code)
	}

	req = httptest.NewRequest("GET", "/v1/admin/", nil)
	w = httptest.NewRecorder()
	srv.HandleAdmin(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("trailing slash status = %d", w.Code)
	}

	req = httptest.NewRequest("GET", "/v1/admin/nope", nil)
	w = httptest.NewRecorder()
	srv.HandleAdmin(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown path status = %d", w.Code)
	}
}

func TestAdminSummary_ReportsTasteProviders(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{"spotify": connectors.NewSpotifyConnector(srv)}

	req := httptest.NewRequest("GET", "/v1/admin/summary", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminSummary)(w, req)

	var payload AdminSummary
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Services) != 1 {
		t.Fatalf("services = %+v", payload.Services)
	}
	s0 := payload.Services[0]
	if !s0.HasPlaylist || !s0.HasLikedTrack || !s0.HasAlbum || !s0.HasArtist {
		t.Fatalf("provider flags = playlist=%v track=%v album=%v artist=%v",
			s0.HasPlaylist, s0.HasLikedTrack, s0.HasAlbum, s0.HasArtist)
	}
}

func TestSpotifyProviders_Unauthorized(t *testing.T) {
	srv := testServerWithBase(&Config{Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject"}}}, "https://aegis.example.com")
	conn := connectors.NewSpotifyConnector(srv)
	if _, _, err := conn.ListUserSavedAlbums(context.Background()); !errors.Is(err, kinds.ErrUnauthorized) {
		t.Fatalf("albums err = %v", err)
	}
	if _, _, err := conn.ListUserFollowedArtists(context.Background()); !errors.Is(err, kinds.ErrUnauthorized) {
		t.Fatalf("artists err = %v", err)
	}
	if _, _, err := conn.ListUserLikedTracks(context.Background()); !errors.Is(err, kinds.ErrUnauthorized) {
		t.Fatalf("tracks err = %v", err)
	}
}

func TestSpotifyProviders_HTTPError(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer mock.Close()

	cfg := &Config{Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject", Endpoint: mock.URL}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save("spotify", &tokens.OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})
	conn := connectors.NewSpotifyConnector(srv)

	if _, _, err := conn.ListUserSavedAlbums(context.Background()); err == nil {
		t.Fatal("expected albums HTTP error")
	}
	if _, _, err := conn.ListUserFollowedArtists(context.Background()); err == nil {
		t.Fatal("expected artists HTTP error")
	}
	if _, _, err := conn.ListUserLikedTracks(context.Background()); err == nil {
		t.Fatal("expected tracks HTTP error")
	}
}

func TestYouTubeProviders_UnauthorizedAndError(t *testing.T) {
	srv := testServerWithBase(&Config{Services: []Service{{ID: "youtube-music", Name: "YouTube Music", Action: "inject"}}}, "https://aegis.example.com")
	conn := connectors.NewYouTubeMusicConnector(srv)
	if _, _, err := conn.ListUserFollowedArtists(context.Background()); !errors.Is(err, kinds.ErrUnauthorized) {
		t.Fatalf("artists err = %v", err)
	}
	if _, _, err := conn.ListUserLikedTracks(context.Background()); !errors.Is(err, kinds.ErrUnauthorized) {
		t.Fatalf("tracks err = %v", err)
	}

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer mock.Close()
	cfg := &Config{Services: []Service{{ID: "youtube-music", Name: "YouTube Music", Action: "inject", Endpoint: mock.URL}}}
	srv2 := testServerWithBase(cfg, "https://aegis.example.com")
	srv2.tokens.Save("youtube-music", &tokens.OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})
	conn2 := connectors.NewYouTubeMusicConnector(srv2)
	if _, _, err := conn2.ListUserFollowedArtists(context.Background()); err == nil {
		t.Fatal("expected artists HTTP error")
	}
	if _, _, err := conn2.ListUserLikedTracks(context.Background()); err == nil {
		t.Fatal("expected liked HTTP error (channel fetch fails)")
	}
}

func TestSpotifyFollowing_FlatItemsShape(t *testing.T) {
	// Some responses nest under artists; flat items should also work.
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"items":[{"id":"ar9","name":"Flat Artist"}],"next":null}`))
	}))
	defer mock.Close()

	cfg := &Config{Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject", Endpoint: mock.URL}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save("spotify", &tokens.OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})

	artists, _, err := connectors.NewSpotifyConnector(srv).ListUserFollowedArtists(context.Background())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(artists) != 1 || artists[0].Name != "Flat Artist" {
		t.Fatalf("artists = %+v", artists)
	}
}

func TestSpotifyFollowing_MissingFollowScopeExplainsReconnect(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer mock.Close()

	cfg := &Config{Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject", Endpoint: mock.URL}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save("spotify", &tokens.OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})

	_, _, err := connectors.NewSpotifyConnector(srv).ListUserFollowedArtists(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "user-follow-read") || !strings.Contains(err.Error(), "/v1/auth/spotify/login") {
		t.Fatalf("error should explain missing scope and reconnect: %v", err)
	}
}

func TestHandleAdminCache_MethodNotAllowed(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest("POST", "/v1/admin/cache", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminCache)(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

func TestLibraryAlbums_CacheHit(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": &fakeAlbumProvider{albums: []kinds.Album{{Name: "Fresh"}}},
	}
	seedLibraryAggregate(t, srv, "albums", []kinds.Album{{Name: "CachedAlbum"}})

	req := httptest.NewRequest("GET", "/v1/library/albums", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("albums"))(w, req)
	if w.Header().Get("X-Aegis-Cache") != "hit" {
		t.Fatalf("cache header = %q", w.Header().Get("X-Aegis-Cache"))
	}
	if !strings.Contains(w.Body.String(), "CachedAlbum") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestSpotifyURLHelpers(t *testing.T) {
	if connectors.SpotifyPlaylistURL("") != "" {
		t.Fatal("empty playlist id produces empty URL")
	}
	if connectors.SpotifyPlaylistURL("abc") != "https://open.spotify.com/playlist/abc" {
		t.Fatal("playlist URL")
	}
}

func TestMentionsAlbumsAndArtists_Keywords(t *testing.T) {
	cases := map[string]bool{
		"what albums have i saved?": true,
		"my albums":                 true,
		"tell me a joke":            false,
		"artists do i follow?":      true,
		"top artists this year":     true,
	}
	for msg, want := range cases {
		got := LibraryKindByID("albums").Mentions(msg) || LibraryKindByID("artists").Mentions(msg) || LibraryKindByID("playlists").Mentions(msg) || LibraryKindByID("tracks").Mentions(msg)
		if got != want {
			t.Fatalf("%q got %v want %v", msg, got, want)
		}
	}
	if !LibraryKindByID("albums").Mentions("favorite album right now") {
		t.Fatal("favorite album")
	}
	if !LibraryKindByID("artists").Mentions("favorite artist") {
		t.Fatal("favorite artist")
	}
}

func TestAllowDataPermission_FollowingUsesArtists(t *testing.T) {
	svc := &Service{ID: "spotify", Name: "Spotify"}
	srv := testServerWithBase(&Config{
		typePolicies: policy.DataGrants{
			shared.DefaultKey: {Default: shared.AccessRead},
			"artists":         {Default: shared.AccessNone},
		},
		Services: []Service{*svc},
	}, "https://aegis.example.com")

	req := httptest.NewRequest("GET", "/v1/services/spotify/me/following?type=artist", nil)
	w := httptest.NewRecorder()
	if srv.AllowDataPermission(w, req, svc, "/me/following") {
		t.Fatal("artists:none should deny /me/following")
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest("GET", "/v1/services/youtube-music/me/subscriptions", nil)
	w = httptest.NewRecorder()
	if srv.AllowDataPermission(w, req, svc, "/me/subscriptions") {
		t.Fatal("artists:none should deny /me/subscriptions")
	}
}

func TestNeutralYouTubeTrack_DeletedItemNoPanic(t *testing.T) {
	// No contentDetails and no resourceId — used to panic on type assertion.
	conn := connectors.NewYouTubeMusicConnector(testServerWithBase(&Config{}, "https://aegis.example.com"))
	track, ok := conn.NeutralYouTubeTrack(map[string]any{
		"snippet": map[string]any{"title": "Deleted video"},
	})
	if !ok {
		t.Fatal("expected a track entry even without video id")
	}
	if track.Name != "Deleted video" {
		t.Fatalf("name = %q", track.Name)
	}
}

type panickingArtistProvider struct{}

func (panickingArtistProvider) Capabilities() shared.ServiceCapabilities {
	return shared.ServiceCapabilities{}
}
func (panickingArtistProvider) SystemInstructions(string) string { return "x" }
func (panickingArtistProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}

func (panickingArtistProvider) ListUserFollowedArtists(context.Context) ([]kinds.Artist, bool, error) {
	panic("boom")
}

func TestHandleLibraryArtists_ProviderPanicDoesNotKillProcess(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{"spotify": panickingArtistProvider{}}

	req := httptest.NewRequest("GET", "/v1/library/artists", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("artists"))(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp kinds.ArtistList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ByService["spotify"].Status != kinds.LibraryStatusError {
		t.Fatalf("status = %+v, want error after panic", resp.ByService["spotify"])
	}
}

func TestLibraryTracks_TruncatedFlag(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": &truncatingTrackProvider{},
	}

	req := httptest.NewRequest("GET", "/v1/library/tracks", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.libraryHandler("tracks"))(w, req)

	var resp kinds.TrackList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.ByService["spotify"].Truncated {
		t.Fatalf("expected truncated=true, got %+v", resp.ByService["spotify"])
	}
	if !resp.Truncated {
		t.Fatal("expected top-level truncated=true when a service is truncated")
	}
}

func TestLibrarySystemInstructions_MentionTruncated(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	instr := srv.libraryInstructionsFor("tracks")
	if !strings.Contains(instr, "truncated") {
		t.Fatalf("tracks instructions should tell agents to check truncated: %s", instr)
	}
}

type truncatingTrackProvider struct{}

func (truncatingTrackProvider) Capabilities() shared.ServiceCapabilities {
	return shared.ServiceCapabilities{}
}
func (truncatingTrackProvider) SystemInstructions(string) string { return "x" }
func (truncatingTrackProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}

func (truncatingTrackProvider) ListUserLikedTracks(context.Context) ([]kinds.Track, bool, error) {
	return []kinds.Track{{Name: "Only Prefix"}}, true, nil
}
