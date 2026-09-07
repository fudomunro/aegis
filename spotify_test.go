package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSpotifyConnector_SystemInstructions(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	conn := NewSpotifyConnector(srv)

	instructions := conn.SystemInstructions("https://guardian.example.com")

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
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	conn := NewSpotifyConnector(srv)

	raw := map[string]any{
		"id":           "alice123",
		"display_name": "Alice",
		"uri":          "spotify:user:alice123",
		"email":        "alice@example.com",
		"followers":    map[string]any{"total": 42},
	}

	result := conn.transformUser(raw)
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
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	conn := NewSpotifyConnector(srv)

	raw := map[string]any{
		"items": []any{
			map[string]any{
				"id":          "pl1",
				"name":        "Jazz Vibes",
				"description": "Smooth jazz",
				"tracks":      map[string]any{"total": 42},
				"uri":         "spotify:playlist:pl1",
				"external_urls": map[string]any{"spotify": "https://open.spotify.com/playlist/pl1"},
			},
		},
		"total": 1,
	}

	result := conn.transformPlaylistList(raw)
	playlists, ok := result["playlists"].([]map[string]any)
	if !ok {
		t.Fatalf("playlists type = %T", result["playlists"])
	}
	if len(playlists) != 1 {
		t.Fatalf("got %d playlists, want 1", len(playlists))
	}
	if playlists[0]["name"] != "Jazz Vibes" {
		t.Fatalf("name = %v", playlists[0]["name"])
	}
	// extractNested returns the raw value — could be int or float64.
	tracks := playlists[0]["tracks"]
	if tracks != 42 && tracks != 42.0 {
		t.Fatalf("tracks = %v", tracks)
	}
}

func TestSpotifyConnector_TransformPlayer(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	conn := NewSpotifyConnector(srv)

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

	result := conn.transformPlayer(raw)
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
	result2 := conn.transformPlayer(map[string]any{})
	if result2["is_playing"] != false {
		t.Fatal("expected is_playing=false for empty player")
	}
}

func TestSpotifyConnector_TransformSearch(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	conn := NewSpotifyConnector(srv)

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

	result := conn.transformSearch(raw)
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
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	conn := NewSpotifyConnector(srv)

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

	result := conn.transformSavedTracks(raw)
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
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.connectors = map[string]ServiceConnector{
		"spotify": NewSpotifyConnector(srv),
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
	srv := testServerWithBase(cfg, "https://guardian.example.com")

	// Store a mock token.
	srv.tokens.Save("spotify", &OAuthToken{
		AccessToken: "mock-token",
		Expiry:      futureTime(),
	})

	srv.connectors = map[string]ServiceConnector{
		"spotify": NewSpotifyConnector(srv),
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
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.tokens.Save("spotify", &OAuthToken{
		AccessToken: "mock-token",
		Expiry:      futureTime(),
	})
	srv.connectors = map[string]ServiceConnector{
		"spotify": NewSpotifyConnector(srv),
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
	names := extractNames(obj, "artists")
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
	result := extractNested(obj, "album", "name")
	if result != "Kind of Blue" {
		t.Fatalf("result = %v", result)
	}

	// Missing nested key.
	result = extractNested(obj, "album", "missing")
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func futureTime() time.Time {
	return time.Now().Add(time.Hour)
}
