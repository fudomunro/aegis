package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

func TestLibraryHandlers_MethodNotAllowed(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	for _, h := range []http.HandlerFunc{
		srv.HandleLibraryTracks,
		srv.HandleLibraryAlbums,
		srv.HandleLibraryArtists,
		srv.HandleLibraryPlaylists,
		srv.HandleLibraryContacts,
		srv.HandleLibraryPhotos,
		srv.HandleLibraryEvents,
	} {
		req := httptest.NewRequest("POST", "/v1/library/x", nil)
		w := httptest.NewRecorder()
		h(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want 405", w.Code)
		}
	}
}

func TestLibraryTracks_CacheHit(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]ServiceConnector{
		"spotify": &fakeLikedTrackProvider{tracks: []types.Track{{Name: "Solo"}}},
	}
	srv.cache.Set("library:tracks", `{"tracks":[{"name":"Cached"}],"total":1}`, time.Minute)

	req := httptest.NewRequest("GET", "/v1/library/tracks", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryTracks)(w, req)

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
	srv.connectors = map[string]ServiceConnector{
		"spotify": &fakeLikedTrackProvider{err: errors.New("boom")},
	}

	req := httptest.NewRequest("GET", "/v1/library/tracks", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryTracks)(w, req)

	var resp types.TrackList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ByService["spotify"].Status != types.LibraryStatusError {
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
	srv.AuthMiddleware(srv.HandleLibraryTracks)(w, req)
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
	srv.connectors = map[string]ServiceConnector{
		"spotify":       &fakeAlbumProvider{err: types.ErrUnauthorized},
		"youtube-music": &fakeAlbumProvider{err: errors.New("upstream")},
	}

	req := httptest.NewRequest("GET", "/v1/library/albums", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryAlbums)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp types.AlbumList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ByService["spotify"].Status != types.LibraryStatusUnauthorized {
		t.Fatalf("spotify = %+v", resp.ByService["spotify"])
	}
	if resp.ByService["youtube-music"].Status != types.LibraryStatusError {
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
	srv.AuthMiddleware(srv.HandleLibraryAlbums)(w, req)
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
	srv.connectors = map[string]ServiceConnector{
		"spotify": &fakeAlbumProvider{albums: []types.Album{{Name: "A", Sources: []types.AlbumSource{{Service: "spotify", ID: "1"}}}}},
		"tidal":   &fakeAlbumProvider{albums: []types.Album{{Name: "Blocked", Sources: []types.AlbumSource{{Service: "tidal", ID: "2"}}}}},
	}

	req := httptest.NewRequest("GET", "/v1/library/albums", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryAlbums)(w, req)

	var resp types.AlbumList
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
	srv.connectors = map[string]ServiceConnector{
		"spotify": &fakeArtistProvider{err: types.ErrUnauthorized},
	}

	req := httptest.NewRequest("GET", "/v1/library/artists", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryArtists)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp types.ArtistList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ByService["spotify"].Status != types.LibraryStatusUnauthorized {
		t.Fatalf("status = %+v", resp.ByService["spotify"])
	}

	// Degraded aggregates must not be cached (issue #104). After the provider
	// recovers, the next request re-fetches instead of replaying unauthorized.
	srv.connectors["spotify"] = &fakeArtistProvider{artists: []types.Artist{types.NewArtist("spotify", "Spotify", "1", "X")}}
	w2 := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryArtists)(w2, req)
	if w2.Header().Get("X-Aegis-Cache") != "miss" {
		t.Fatalf("expected cache miss after recovery, got %q", w2.Header().Get("X-Aegis-Cache"))
	}
	var recovered types.ArtistList
	if err := json.Unmarshal(w2.Body.Bytes(), &recovered); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if recovered.ByService["spotify"].Status != types.LibraryStatusOK {
		t.Fatalf("status = %+v", recovered.ByService["spotify"])
	}

	// Healthy aggregate is cached.
	w3 := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryArtists)(w3, req)
	if w3.Header().Get("X-Aegis-Cache") != "hit" {
		t.Fatalf("expected cache hit on healthy aggregate, got %q", w3.Header().Get("X-Aegis-Cache"))
	}
}

func TestLibraryArtists_ErrorService(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]ServiceConnector{
		"spotify": &fakeArtistProvider{err: errors.New("nope")},
	}

	req := httptest.NewRequest("GET", "/v1/library/artists", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryArtists)(w, req)

	var resp types.ArtistList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ByService["spotify"].Status != types.LibraryStatusError {
		t.Fatalf("status = %+v", resp.ByService["spotify"])
	}
	// Error bodies must not stick in the cache (issue #104).
	if _, ok := srv.cache.Get("library:artists"); ok {
		t.Fatal("degraded library aggregate should not be cached")
	}
}

func TestLibraryArtists_PermissionDenied(t *testing.T) {
	cfg := &Config{
		Services: []Service{{
			ID: "spotify", Name: "Spotify", Action: "inject",
			Permissions: types.Permissions{"artists": types.AccessNone},
		}},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]ServiceConnector{
		"spotify": &fakeArtistProvider{artists: []types.Artist{types.NewArtist("spotify", "Spotify", "1", "X")}},
	}

	req := httptest.NewRequest("GET", "/v1/library/artists", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryArtists)(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestLibraryArtists_NoProviders(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest("GET", "/v1/library/artists", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryArtists)(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestLibraryReadableHelpers(t *testing.T) {
	srv := testServerWithBase(&Config{
		Services: []Service{
			{ID: "a", Name: "A", Action: "inject"},
			{ID: "b", Name: "B", Action: "block"},
			{ID: "c", Name: "C", Action: "inject", Permissions: types.Permissions{
				"tracks": types.AccessNone, "albums": types.AccessNone, "artists": types.AccessNone,
				"playlists": types.AccessNone, "contacts": types.AccessNone, "photos": types.AccessNone,
			}},
		},
	}, "https://aegis.example.com")

	if !kindReadable("tracks", srv) {
		t.Fatal("service a should allow tracks read")
	}
	if !kindReadable("albums", srv) || !kindReadable("artists", srv) || !kindReadable("playlists", srv) ||
		!kindReadable("contacts", srv) || !kindReadable("photos", srv) {
		t.Fatal("service a should allow all library reads")
	}

	// Only the blocked + denied services remain → nothing readable.
	srv2 := testServerWithBase(&Config{
		Services: []Service{
			{ID: "b", Name: "B", Action: "block"},
			{ID: "c", Name: "C", Action: "inject", Permissions: types.Permissions{"default": types.AccessNone}},
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
	srv.connectors = map[string]ServiceConnector{
		"spotify": NewSpotifyConnector(srv),
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
	srv.connectors = map[string]ServiceConnector{"spotify": NewSpotifyConnector(srv)}

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
	merged := types.MergeTracksSync([]types.Track{
		{
			Name:    "Song",
			Artists: []types.Artist{types.NewArtist("spotify", "Spotify", "", "A")},
			Sources: []types.TrackSource{{Service: "spotify", ServiceName: "Spotify", ID: "1"}},
		},
		{
			Name:       "Song",
			Artists:    []types.Artist{types.NewArtist("youtube-music", "YouTube Music", "", "A")},
			Album:      types.NewAlbum("youtube-music", "YouTube Music", "al", "LP"),
			DurationMs: 210000,
			Duration:   "PT3M30S",
			Sources:    []types.TrackSource{{Service: "youtube-music", ServiceName: "YouTube Music", ID: "2"}},
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
	merged := types.MergeTracksSync([]types.Track{
		{Sources: []types.TrackSource{{Service: "spotify", ID: "abc"}}},
		{Sources: []types.TrackSource{{Service: "spotify", ID: "abc"}}},
	})
	if len(merged) != 1 {
		t.Fatalf("got %d, want 1 merged empty-name track", len(merged))
	}
	if len(merged[0].Sources) != 2 {
		t.Fatalf("sources = %+v", merged[0].Sources)
	}
}

func TestTrackIdentityKey_Variants(t *testing.T) {
	if types.TrackIdentityKey(types.Track{Name: "  Hello  World ", Artists: []types.Artist{{Name: "The  Artist"}}}) != "hello world|the artist" {
		t.Fatal("normalize failed")
	}
	if types.TrackIdentityKey(types.Track{Name: "Solo"}) != "solo" {
		t.Fatal("title-only key")
	}
	if !strings.HasPrefix(types.TrackIdentityKey(types.Track{Sources: []types.TrackSource{{Service: "spotify", ID: "x"}}}), "\x00spotify:x") {
		t.Fatal("empty title fallback")
	}
	if types.TrackIdentityKey(types.Track{}) != "\x00" {
		t.Fatal("fully empty key")
	}
}

func TestAlbumIdentityKey_Variants(t *testing.T) {
	if types.AlbumIdentityKey(types.Album{Name: "LP", Artists: []types.Artist{{Name: "Band"}}}) != "lp|band" {
		t.Fatal("title+artist")
	}
	if types.AlbumIdentityKey(types.Album{Name: "LP"}) != "lp" {
		t.Fatal("title only")
	}
	if !strings.HasPrefix(types.AlbumIdentityKey(types.Album{Sources: []types.AlbumSource{{Service: "spotify", ID: "z"}}}), "\x00spotify:z") {
		t.Fatal("empty fallback")
	}
}

func TestMergeAlbumsSync_MergesSourcesAndArtists(t *testing.T) {
	merged := types.MergeAlbumsSync([]types.Album{
		{
			Name:    "LP",
			Artists: []types.Artist{types.NewArtist("spotify", "Spotify", "", "Band")},
			Sources: []types.AlbumSource{{Service: "spotify", ServiceName: "Spotify", ID: "1"}},
		},
		{
			Name:    "LP",
			Artists: []types.Artist{types.NewArtist("youtube-music", "YouTube Music", "", "Band")},
			Sources: []types.AlbumSource{{Service: "youtube-music", ServiceName: "YouTube Music", ID: "2"}},
		},
		{
			Name:    "Other",
			Sources: []types.AlbumSource{{Service: "spotify", ServiceName: "Spotify", ID: "3"}},
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
	merged := types.MergeArtistLibrary([]types.Artist{
		{Name: ""},
		types.NewArtist("spotify", "Spotify", "1", "A"),
		types.NewArtist("youtube-music", "YouTube Music", "2", "A"),
	})
	if len(merged) != 1 || merged[0].Name != "A" || len(merged[0].Sources) != 2 {
		t.Fatalf("merged = %+v", merged)
	}
}

func TestPrimaryService_Empty(t *testing.T) {
	if (types.Playlist{}).PrimaryService() != "" {
		t.Fatal("expected empty primary service")
	}
	if (types.Playlist{Sources: []types.PlaylistSource{{Service: "spotify"}}}).PrimaryService() != "spotify" {
		t.Fatal("expected spotify")
	}
}

func TestNormalizeHandleList(t *testing.T) {
	got := normalizeHandleList([]string{"@Alice", "alice", "  ", "bob"})
	if len(got) != 2 || got[0] != "alice" || got[1] != "bob" {
		t.Fatalf("got = %#v", got)
	}
	if normalizeHandleList(nil) != nil {
		t.Fatal("nil in → nil out")
	}
	if normalizeHandleList([]string{"  "}) != nil {
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
	srv.connectors = map[string]ServiceConnector{"spotify": NewSpotifyConnector(srv)}

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
	conn := NewSpotifyConnector(srv)
	if _, _, err := conn.ListUserSavedAlbums(context.Background()); !errors.Is(err, types.ErrUnauthorized) {
		t.Fatalf("albums err = %v", err)
	}
	if _, _, err := conn.ListUserFollowedArtists(context.Background()); !errors.Is(err, types.ErrUnauthorized) {
		t.Fatalf("artists err = %v", err)
	}
	if _, _, err := conn.ListUserLikedTracks(context.Background()); !errors.Is(err, types.ErrUnauthorized) {
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
	srv.tokens.Save("spotify", &OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})
	conn := NewSpotifyConnector(srv)

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
	conn := NewYouTubeMusicConnector(srv)
	if _, _, err := conn.ListUserFollowedArtists(context.Background()); !errors.Is(err, types.ErrUnauthorized) {
		t.Fatalf("artists err = %v", err)
	}
	if _, _, err := conn.ListUserLikedTracks(context.Background()); !errors.Is(err, types.ErrUnauthorized) {
		t.Fatalf("tracks err = %v", err)
	}

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer mock.Close()
	cfg := &Config{Services: []Service{{ID: "youtube-music", Name: "YouTube Music", Action: "inject", Endpoint: mock.URL}}}
	srv2 := testServerWithBase(cfg, "https://aegis.example.com")
	srv2.tokens.Save("youtube-music", &OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})
	conn2 := NewYouTubeMusicConnector(srv2)
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
	srv.tokens.Save("spotify", &OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})

	artists, _, err := NewSpotifyConnector(srv).ListUserFollowedArtists(context.Background())
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
	srv.tokens.Save("spotify", &OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})

	_, _, err := NewSpotifyConnector(srv).ListUserFollowedArtists(context.Background())
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
	srv.connectors = map[string]ServiceConnector{
		"spotify": &fakeAlbumProvider{albums: []types.Album{{Name: "Fresh"}}},
	}
	srv.cache.Set("library:albums", `{"albums":[{"name":"CachedAlbum"}],"total":1}`, time.Minute)

	req := httptest.NewRequest("GET", "/v1/library/albums", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryAlbums)(w, req)
	if w.Header().Get("X-Aegis-Cache") != "hit" {
		t.Fatalf("cache header = %q", w.Header().Get("X-Aegis-Cache"))
	}
	if !strings.Contains(w.Body.String(), "CachedAlbum") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestSpotifyURLHelpers(t *testing.T) {
	if spotifyPlaylistURL("") != "" {
		t.Fatal("empty playlist id produces empty URL")
	}
	if spotifyPlaylistURL("abc") != "https://open.spotify.com/playlist/abc" {
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
	svc := &Service{
		ID: "spotify", Name: "Spotify",
		Permissions: types.Permissions{"default": types.AccessRead, "artists": types.AccessNone},
	}
	srv := testServerWithBase(&Config{Services: []Service{*svc}}, "https://aegis.example.com")

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
	conn := NewYouTubeMusicConnector(testServerWithBase(&Config{}, "https://aegis.example.com"))
	track, ok := conn.neutralYouTubeTrack(map[string]any{
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

func (panickingArtistProvider) Capabilities() types.ServiceCapabilities {
	return types.ServiceCapabilities{}
}
func (panickingArtistProvider) SystemInstructions(string) string { return "x" }
func (panickingArtistProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}

func (panickingArtistProvider) ListUserFollowedArtists(context.Context) ([]types.Artist, bool, error) {
	panic("boom")
}

func TestHandleLibraryArtists_ProviderPanicDoesNotKillProcess(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]ServiceConnector{"spotify": panickingArtistProvider{}}

	req := httptest.NewRequest("GET", "/v1/library/artists", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryArtists)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp types.ArtistList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ByService["spotify"].Status != types.LibraryStatusError {
		t.Fatalf("status = %+v, want error after panic", resp.ByService["spotify"])
	}
}

func TestLibraryTracks_TruncatedFlag(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]ServiceConnector{
		"spotify": &truncatingTrackProvider{},
	}

	req := httptest.NewRequest("GET", "/v1/library/tracks", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryTracks)(w, req)

	var resp types.TrackList
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

func (truncatingTrackProvider) Capabilities() types.ServiceCapabilities {
	return types.ServiceCapabilities{}
}
func (truncatingTrackProvider) SystemInstructions(string) string { return "x" }
func (truncatingTrackProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}

func (truncatingTrackProvider) ListUserLikedTracks(context.Context) ([]types.Track, bool, error) {
	return []types.Track{{Name: "Only Prefix"}}, true, nil
}

func TestAdminHTML_SimplePermForm(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest("GET", "/v1/admin", nil)
	w := httptest.NewRecorder()
	srv.HandleAdmin(w, req)
	body := w.Body.String()
	// Issue #61: form is a simple per-type availability control for core Map types.
	// Issue #69: form is always visible and saves live on change.
	for _, want := range []string{
		// Shared help above filter (not repeated per card)
		`id="agent-data-access-help"`,
		"Agent data access",
		"Available",
		"Editable",
		"data-perm-key",
		"data-original-perms",
		"Liked tracks",
		"Followed artists",
		"Saved albums",
		"LIBRARY_DATA_TYPES",
		"writable_data_types",
		"allowWrite",
		"never add capabilities",
		"Changes save immediately",
		"perm-live",
		"applyPermissionsLocally",
		`select[data-perm-key]`,
		// Icons next to data types on permission rows
		"dataTypeIconHTML",
		"perm-type-icon",
		"perm-type-label",
		`data-type-icon=`,
		`events: "M3.5 3h9v10.5h-9zM3.5 6h9M6.5 2v2M9.5 2v2M6.5 9.5h3"`,
		`messages: "M2.5 3.5h11v9h-11zM2.5 4 8 8.5 13.5 4"`,
		// Form still on each card under the title
		"svc-name",
		"renderPermEditor(svc)",
		"svc-advanced",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("admin HTML missing simplified permissions form marker %q", want)
		}
	}
	// Advanced clutter from the old dialog should stay gone.
	// Toggle/Save/Allow-all chrome is gone: form is always-on under the title.
	for _, gone := range []string{
		"perm-custom-key",
		"perm-locked-select",
		"custom type (advanced)",
		"Other data types",
		"class=\"perm-key\"",
		"Playback",
		"Library aliases",
		"perm-toggle",
		"perm-save",
		"perm-clear",
		"Allow all",
		"Edit permissions",
		"Hide permissions",
		">Save</button>",
		"openPermEditors",
		"perm-actions",
		// Card forms no longer duplicate the instructions paragraph
		"<h4>Agent data access</h4>",
		"Choose what agents can use on this service",
	} {
		if strings.Contains(body, gone) {
			t.Fatalf("admin HTML still contains old permissions dialog piece %q", gone)
		}
	}
}
