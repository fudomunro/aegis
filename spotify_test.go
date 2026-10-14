package main

import (
	"github.com/fudomunro/aegis/internal/connectors"

	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSpotifyConnector_SystemInstructions(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := connectors.NewSpotifyConnector(srv)

	instructions := conn.SystemInstructions("https://aegis.example.com")

	if !strings.Contains(instructions, "/v1/services/spotify") {
		t.Fatal("instructions missing proxy base URL")
	}
	if !strings.Contains(instructions, "GET /me") {
		t.Fatal("instructions missing /me endpoint")
	}
	if !strings.Contains(instructions, "GET /me/playlists") {
		t.Fatal("instructions missing /me/playlists endpoint")
	}
	if !strings.Contains(instructions, "GET /search") {
		t.Fatal("instructions missing /search endpoint")
	}
}

func TestSpotifyConnector_TransformUser(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := connectors.NewSpotifyConnector(srv)

	raw := map[string]any{
		"id":           "alice123",
		"display_name": "Alice",
		"uri":          "spotify:user:alice123",
		"email":        "alice@example.com",
		"followers":    map[string]any{"total": 42},
	}

	result := conn.TransformUser(raw)
	if result["id"] != "alice123" {
		t.Fatalf("id = %v", result["id"])
	}
	if result["display_name"] != "Alice" {
		t.Fatalf("display_name = %v", result["display_name"])
	}
	// Should NOT include email or followers.
	if _, ok := result["email"]; ok {
		t.Fatal("transformed user should not include email")
	}
	if _, ok := result["followers"]; ok {
		t.Fatal("transformed user should not include followers")
	}
}

func TestSpotifyConnector_TransformPlaylistList(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := connectors.NewSpotifyConnector(srv)

	// Legacy shape: tracks.total.
	raw := map[string]any{
		"items": []any{
			map[string]any{
				"id":            "pl1",
				"name":          "Jazz Vibes",
				"description":   "Smooth jazz",
				"tracks":        map[string]any{"total": 42},
				"uri":           "spotify:playlist:pl1",
				"external_urls": map[string]any{"spotify": "https://open.spotify.com/playlist/pl1"},
			},
		},
		"total": 1,
	}

	result := conn.TransformPlaylistList(raw)
	if len(result.Playlists) != 1 {
		t.Fatalf("got %d playlists, want 1", len(result.Playlists))
	}
	p := result.Playlists[0]
	if p.Name != "Jazz Vibes" {
		t.Fatalf("name = %v", p.Name)
	}
	if p.TrackCount != 42 {
		t.Fatalf("track_count = %v", p.TrackCount)
	}
	if len(p.Sources) != 1 || p.Sources[0].Service != "spotify" || p.Sources[0].ServiceName != "Spotify" {
		t.Fatalf("sources = %+v", p.Sources)
	}
	if p.Sources[0].ID != "pl1" {
		t.Fatalf("source id = %v", p.Sources[0].ID)
	}
	if p.Sources[0].URL != "https://open.spotify.com/playlist/pl1" {
		t.Fatalf("url = %v", p.Sources[0].URL)
	}
	if result.Total != 1 {
		t.Fatalf("total = %v", result.Total)
	}
}

func TestSpotifyConnector_TransformPlaylistList_ItemsTotal(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := connectors.NewSpotifyConnector(srv)

	// Current Web API shape: items.total (2026 live check).
	raw := map[string]any{
		"items": []any{
			map[string]any{
				"id":   "pl2",
				"name": "Driving",
				"items": map[string]any{
					"href":  "https://api.spotify.com/v1/playlists/pl2/items",
					"total": 18,
				},
				"uri": "spotify:playlist:pl2",
			},
		},
		"total": 1,
	}

	result := conn.TransformPlaylistList(raw)
	if len(result.Playlists) != 1 {
		t.Fatalf("got %d playlists, want 1", len(result.Playlists))
	}
	if result.Playlists[0].TrackCount != 18 {
		t.Fatalf("track_count = %v, want 18", result.Playlists[0].TrackCount)
	}
}

func TestSpotifyConnector_TransformPlaylistDetail_CurrentItemsShape(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := connectors.NewSpotifyConnector(srv)

	raw := map[string]any{
		"id":          "pl3",
		"name":        "Happy Car Family",
		"description": "Road trip songs",
		"items": map[string]any{
			"href":  "https://api.spotify.com/v1/playlists/pl3/items?offset=0&limit=100",
			"total": 2,
			"items": []any{
				map[string]any{
					"added_at": "2023-01-20T22:35:20Z",
					"item": map[string]any{
						"id":            "t1",
						"name":          "Hayloft",
						"uri":           "spotify:track:t1",
						"type":          "track",
						"artists":       []any{map[string]any{"name": "Mother Mother"}},
						"album":         map[string]any{"name": "O My Heart"},
						"external_urls": map[string]any{"spotify": "https://open.spotify.com/track/t1"},
					},
				},
				map[string]any{
					"added_at": "2023-01-20T22:35:25Z",
					"item": map[string]any{
						"id":            "t2",
						"name":          "Arms Tonite",
						"uri":           "spotify:track:t2",
						"type":          "track",
						"artists":       []any{map[string]any{"name": "Mother Mother"}},
						"album":         map[string]any{"name": "O My Heart"},
						"external_urls": map[string]any{"spotify": "https://open.spotify.com/track/t2"},
					},
				},
			},
		},
		"uri": "spotify:playlist:pl3",
	}

	detail := conn.TransformPlaylistDetail(raw)
	if detail.TrackCount != 2 {
		t.Fatalf("track_count = %v, want 2", detail.TrackCount)
	}
	if detail.TotalTracks != 2 {
		t.Fatalf("total_tracks = %v, want 2", detail.TotalTracks)
	}
	if len(detail.Tracks) != 2 {
		t.Fatalf("got %d tracks, want 2", len(detail.Tracks))
	}
	if detail.Tracks[0].Name != "Hayloft" {
		t.Fatalf("first track = %v", detail.Tracks[0].Name)
	}
	if len(detail.Tracks[0].Artists) == 0 || detail.Tracks[0].Artists[0].Name != "Mother Mother" {
		t.Fatalf("artists = %+v", detail.Tracks[0].Artists)
	}
	if detail.Name != "Happy Car Family" {
		t.Fatalf("name = %v", detail.Name)
	}
}

func TestSpotifyConnector_TransformPlayer(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := connectors.NewSpotifyConnector(srv)

	// Active playback.
	raw := map[string]any{
		"is_playing": true,
		"item": map[string]any{
			"name":    "So What",
			"artists": []any{map[string]any{"name": "Miles Davis"}},
			"album":   map[string]any{"name": "Kind of Blue"},
		},
		"device": map[string]any{
			"name":           "MacBook Pro",
			"type":           "Computer",
			"volume_percent": 75,
		},
	}

	result := conn.TransformPlayer(raw)
	if result["is_playing"] != true {
		t.Fatal("expected is_playing=true")
	}
	track, ok := result["track"].(map[string]any)
	if !ok {
		t.Fatal("expected track in player response")
	}
	if track["name"] != "So What" {
		t.Fatalf("track name = %v", track["name"])
	}

	// No playback.
	result2 := conn.TransformPlayer(map[string]any{})
	if result2["is_playing"] != false {
		t.Fatal("expected is_playing=false for empty player")
	}
}

func TestSpotifyConnector_TransformSearch(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := connectors.NewSpotifyConnector(srv)

	raw := map[string]any{
		"tracks": map[string]any{
			"items": []any{
				map[string]any{
					"name":    "So What",
					"id":      "track1",
					"uri":     "spotify:track:track1",
					"artists": []any{map[string]any{"name": "Miles Davis"}},
					"album":   map[string]any{"name": "Kind of Blue"},
				},
			},
		},
	}

	result := conn.TransformSearch(raw)
	tracks, ok := result["tracks"].([]map[string]any)
	if !ok {
		t.Fatalf("tracks type = %T", result["tracks"])
	}
	if len(tracks) != 1 {
		t.Fatalf("got %d tracks, want 1", len(tracks))
	}
	if tracks[0]["name"] != "So What" {
		t.Fatalf("name = %v", tracks[0]["name"])
	}
}

func TestSpotifyConnector_TransformSavedTracks(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := connectors.NewSpotifyConnector(srv)

	raw := map[string]any{
		"items": []any{
			map[string]any{
				"track": map[string]any{
					"name":    "Blue in Green",
					"id":      "t1",
					"uri":     "spotify:track:t1",
					"artists": []any{map[string]any{"name": "Miles Davis"}},
					"album":   map[string]any{"name": "Kind of Blue"},
				},
			},
		},
		"total": 1,
	}

	result := conn.TransformSavedTracks(raw)
	tracks, ok := result["tracks"].([]map[string]any)
	if !ok {
		t.Fatalf("tracks type = %T", result["tracks"])
	}
	if len(tracks) != 1 {
		t.Fatalf("got %d tracks, want 1", len(tracks))
	}
	if tracks[0]["name"] != "Blue in Green" {
		t.Fatalf("name = %v", tracks[0]["name"])
	}
}

func TestSpotifyConnector_HandleProxy_NoToken(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": connectors.NewSpotifyConnector(srv),
	}

	req := httptest.NewRequest("GET", "/v1/services/spotify/me", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandleServiceProxy(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestSpotifyConnector_HandleProxy_WithMockSpotify(t *testing.T) {
	// Mock Spotify API.
	mockSpotify := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mock-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/me":
			io.WriteString(w, `{"id":"alice","display_name":"Alice","email":"alice@example.com","followers":{"total":10}}`)
		case "/v1/me/playlists":
			io.WriteString(w, `{"items":[{"id":"pl1","name":"Jazz","tracks":{"total":5},"uri":"spotify:playlist:pl1"}],"total":1}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockSpotify.Close()

	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject", Endpoint: mockSpotify.URL + "/v1"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")

	// Store a mock token.
	srv.tokens.Save("spotify", &OAuthToken{
		AccessToken: "mock-token",
		Expiry:      futureTime(),
	})

	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": connectors.NewSpotifyConnector(srv),
	}

	// Test /me — should be transformed.
	req := httptest.NewRequest("GET", "/v1/services/spotify/me", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandleServiceProxy(w, req)

	if w.Code != http.StatusOK {
		body := w.Body.String()
		t.Fatalf("status = %d, want 200, body: %s", w.Code, body)
	}

	var result map[string]any
	json.NewDecoder(w.Body).Decode(&result)

	if result["id"] != "alice" {
		t.Fatalf("id = %v", result["id"])
	}
	if result["display_name"] != "Alice" {
		t.Fatalf("display_name = %v", result["display_name"])
	}
	if _, ok := result["email"]; ok {
		t.Fatal("email should be stripped from transformed response")
	}
}

func TestSpotifyConnector_HandleProxy_PassthroughForUnknownPaths(t *testing.T) {
	mockSpotify := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"artist1","name":"Miles Davis","genres":["jazz"]}`)
	}))
	defer mockSpotify.Close()

	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject", Endpoint: mockSpotify.URL},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save("spotify", &OAuthToken{
		AccessToken: "mock-token",
		Expiry:      futureTime(),
	})
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": connectors.NewSpotifyConnector(srv),
	}

	req := httptest.NewRequest("GET", "/v1/services/spotify/artists/artist1", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandleServiceProxy(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var result map[string]any
	json.NewDecoder(w.Body).Decode(&result)

	if result["name"] != "Miles Davis" {
		t.Fatalf("name = %v", result["name"])
	}
}

func TestExtractNames(t *testing.T) {
	obj := map[string]any{
		"artists": []any{
			map[string]any{"name": "Miles Davis"},
			map[string]any{"name": "John Coltrane"},
		},
	}
	names := connectors.ExtractNames(obj, "artists")
	if len(names) != 2 || names[0] != "Miles Davis" || names[1] != "John Coltrane" {
		t.Fatalf("names = %v", names)
	}
}

func TestExtractNested(t *testing.T) {
	obj := map[string]any{
		"album": map[string]any{
			"name": "Kind of Blue",
		},
	}
	result := connectors.ExtractNested(obj, "album", "name")
	if result != "Kind of Blue" {
		t.Fatalf("result = %v", result)
	}

	// Missing nested key.
	result = connectors.ExtractNested(obj, "album", "missing")
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func futureTime() time.Time {
	return time.Now().Add(time.Hour)
}

func TestNeutralSpotifyAlbum_UPC(t *testing.T) {
	al := connectors.NeutralSpotifyAlbum(map[string]any{
		"id":   "ab1",
		"name": "Kind of Blue",
		"uri":  "spotify:album:ab1",
		"external_ids": map[string]any{
			"upc": "00602537638569",
		},
		"artists": []any{
			map[string]any{"id": "art1", "name": "Miles Davis"},
		},
	})
	if al.EntityID != "00602537638569" {
		t.Fatalf("entity_id = %q", al.EntityID)
	}
	if len(al.Artists) != 1 || al.Artists[0].Sources[0].ID != "art1" {
		t.Fatalf("artists = %+v", al.Artists)
	}
}
