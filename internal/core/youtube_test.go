package core

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/tokens"
)

func TestYouTubeMusicConnector_HandleProxy_NoToken(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "youtube-music", Name: "YouTube Music", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"youtube-music": connectors.NewYouTubeMusicConnector(srv),
	}

	req := httptest.NewRequest("GET", "/v1/services/youtube-music/playlists/PL1", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.library().HandleServiceProxy(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestYouTubeMusicConnector_Playlists_ReadList(t *testing.T) {
	callCount := 0
	mockYT := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")

		// Verify the request maps correctly.
		if r.URL.Path != "/playlists" {
			t.Errorf("path = %s, want /playlists", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("mine") != "true" {
			t.Errorf("mine = %s, want true", q.Get("mine"))
		}
		if q.Get("part") != "snippet,contentDetails" {
			t.Errorf("part = %s", q.Get("part"))
		}

		// Return page 1 or page 2 based on pageToken.
		if q.Get("pageToken") == "" {
			io.WriteString(w, `{
				"items": [
					{"id":"PL1","snippet":{"title":"Jazz Favorites"},"contentDetails":{"itemCount":25}},
					{"id":"PL2","snippet":{"title":"Morning Coffee"},"contentDetails":{"itemCount":18}}
				],
				"nextPageToken": "page2",
				"pageInfo": {"totalResults": 3}
			}`)
		} else {
			io.WriteString(w, `{
				"items": [
					{"id":"PL3","snippet":{"title":"Deep Focus"},"contentDetails":{"itemCount":42}}
				],
				"pageInfo": {"totalResults": 3}
			}`)
		}
	}))
	defer mockYT.Close()

	cfg := &Config{
		Services: []Service{
			{ID: "youtube-music", Name: "YouTube Music", Action: "inject", Endpoint: mockYT.URL},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save("youtube-music", &tokens.OAuthToken{
		AccessToken: "mock-yt-token",
		Expiry:      time.Now().Add(time.Hour),
	})
	srv.connectors = map[string]connectors.ServiceConnector{
		"youtube-music": connectors.NewYouTubeMusicConnector(srv),
	}

	req := httptest.NewRequest("GET", "/v1/services/youtube-music/me/playlists", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	// List paths are not agent API (issue #119); call the connector directly
	// to verify neutral list transform + pagination still work for library use.
	srv.connectors["youtube-music"].HandleProxy(w, req, "/me/playlists")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}

	var result map[string]any
	json.NewDecoder(w.Body).Decode(&result)

	playlists, ok := result["playlists"].([]any)
	if !ok {
		t.Fatalf("playlists type = %T", result["playlists"])
	}

	// Should have all 3 playlists from both pages.
	if len(playlists) != 3 {
		t.Fatalf("got %d playlists, want 3 (auto-paginated)", len(playlists))
	}

	// Verify transformed fields.
	first := playlists[0].(map[string]any)
	if first["name"] != "Jazz Favorites" {
		t.Fatalf("first playlist name = %v", first["name"])
	}
	if first["track_count"] != 25.0 {
		t.Fatalf("first playlist track_count = %v", first["track_count"])
	}
	sources, _ := first["sources"].([]any)
	if len(sources) != 1 {
		t.Fatalf("sources = %v", first["sources"])
	}
	src0 := sources[0].(map[string]any)
	if src0["service"] != "youtube-music" {
		t.Fatalf("source service = %v", src0["service"])
	}

	// Should have fetched both pages.
	if callCount != 2 {
		t.Fatalf("upstream called %d times, want 2 (pagination)", callCount)
	}

	// PlaylistList exposes the combined count as total.
	if result["total"] != 3.0 {
		t.Fatalf("total = %v, want 3", result["total"])
	}
}

func TestYouTubeMusicConnector_Playlists_ReadList_Cached(t *testing.T) {
	callCount := 0
	mockYT := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"items":[{"id":"PL1","snippet":{"title":"Cached"},"contentDetails":{"itemCount":5}}],"pageInfo":{"totalResults":1}}`)
	}))
	defer mockYT.Close()

	cfg := &Config{
		Services: []Service{
			{ID: "youtube-music", Name: "YouTube Music", Action: "inject", Endpoint: mockYT.URL},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save("youtube-music", &tokens.OAuthToken{
		AccessToken: "mock-yt-token",
		Expiry:      time.Now().Add(time.Hour),
	})
	srv.connectors = map[string]connectors.ServiceConnector{
		"youtube-music": connectors.NewYouTubeMusicConnector(srv),
	}

	handler := func(w *httptest.ResponseRecorder, req *http.Request) {
		srv.connectors["youtube-music"].HandleProxy(w, req, "/me/playlists")
	}

	// First request — cache miss.
	req := httptest.NewRequest("GET", "/v1/services/youtube-music/me/playlists", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Header().Get("X-Aegis-Cache") != "miss" {
		t.Fatalf("first request: cache = %q, want miss", w.Header().Get("X-Aegis-Cache"))
	}

	// Second request — cache hit.
	req = httptest.NewRequest("GET", "/v1/services/youtube-music/me/playlists", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w = httptest.NewRecorder()
	handler(w, req)

	if w.Header().Get("X-Aegis-Cache") != "hit" {
		t.Fatalf("second request: cache = %q, want hit", w.Header().Get("X-Aegis-Cache"))
	}

	if callCount != 1 {
		t.Fatalf("upstream called %d times, want 1 (cached)", callCount)
	}
}

func TestYouTubeMusicConnector_Playlists_GetByID(t *testing.T) {
	mockYT := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// Verify the request maps correctly.
		if r.URL.Path != "/playlists" {
			t.Errorf("path = %s, want /playlists", r.URL.Path)
		}
		if r.URL.Query().Get("id") != "PL_SPECIAL" {
			t.Errorf("id = %s, want PL_SPECIAL", r.URL.Query().Get("id"))
		}

		io.WriteString(w, `{
			"items": [{
				"id": "PL_SPECIAL",
				"snippet": {
					"title": "My Jazz Collection",
					"description": "Best jazz tracks of all time",
					"channelTitle": "Alice Music",
					"publishedAt": "2024-01-15T00:00:00Z"
				},
				"contentDetails": {"itemCount": 87}
			}]
		}`)
	}))
	defer mockYT.Close()

	cfg := &Config{
		Services: []Service{
			{ID: "youtube-music", Name: "YouTube Music", Action: "inject", Endpoint: mockYT.URL},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save("youtube-music", &tokens.OAuthToken{
		AccessToken: "mock-yt-token",
		Expiry:      time.Now().Add(time.Hour),
	})
	srv.connectors = map[string]connectors.ServiceConnector{
		"youtube-music": connectors.NewYouTubeMusicConnector(srv),
	}

	req := httptest.NewRequest("GET", "/v1/services/youtube-music/playlists/PL_SPECIAL", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.library().HandleServiceProxy(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}

	var result map[string]any
	json.NewDecoder(w.Body).Decode(&result)

	if result["name"] != "My Jazz Collection" {
		t.Fatalf("name = %v", result["name"])
	}
	if result["description"] != "Best jazz tracks of all time" {
		t.Fatalf("description = %v", result["description"])
	}
	if result["owner"] != "Alice Music" {
		t.Fatalf("owner = %v", result["owner"])
	}
	if result["track_count"] != 87.0 {
		t.Fatalf("track_count = %v", result["track_count"])
	}
	sources, _ := result["sources"].([]any)
	if len(sources) != 1 {
		t.Fatalf("sources = %v", result["sources"])
	}
	src0 := sources[0].(map[string]any)
	if src0["service"] != "youtube-music" || src0["id"] != "PL_SPECIAL" {
		t.Fatalf("source = %v", src0)
	}
}

func TestYouTubeMusicConnector_Playlists_ReadItems(t *testing.T) {
	mockYT := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.URL.Path != "/playlistItems" {
			t.Errorf("path = %s, want /playlistItems", r.URL.Path)
		}
		if r.URL.Query().Get("playlistId") != "PL_JAZZ" {
			t.Errorf("playlistId = %s", r.URL.Query().Get("playlistId"))
		}

		io.WriteString(w, `{
			"items": [
				{
					"snippet": {
						"title": "So What",
						"channelTitle": "Miles Davis - Topic",
						"publishedAt": "2019-01-01T00:00:00Z",
						"thumbnails": {"high": {"url": "https://i.ytimg.com/vi/xxx/hqdefault.jpg"}}
					},
					"contentDetails": {"videoId": "v_so_what"}
				},
				{
					"snippet": {
						"title": "Blue in Green",
						"channelTitle": "Miles Davis - Topic",
						"publishedAt": "2019-01-01T00:00:00Z"
					},
					"contentDetails": {"videoId": "v_blue_green"}
				}
			],
			"pageInfo": {"totalResults": 2}
		}`)
	}))
	defer mockYT.Close()

	cfg := &Config{
		Services: []Service{
			{ID: "youtube-music", Name: "YouTube Music", Action: "inject", Endpoint: mockYT.URL},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save("youtube-music", &tokens.OAuthToken{
		AccessToken: "mock-yt-token",
		Expiry:      time.Now().Add(time.Hour),
	})
	srv.connectors = map[string]connectors.ServiceConnector{
		"youtube-music": connectors.NewYouTubeMusicConnector(srv),
	}

	req := httptest.NewRequest("GET", "/v1/services/youtube-music/playlists/PL_JAZZ/items", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.library().HandleServiceProxy(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}

	var result map[string]any
	json.NewDecoder(w.Body).Decode(&result)

	tracks, ok := result["tracks"].([]any)
	if !ok {
		t.Fatalf("tracks type = %T", result["tracks"])
	}
	if len(tracks) != 2 {
		t.Fatalf("got %d tracks, want 2", len(tracks))
	}

	first := tracks[0].(map[string]any)
	if first["name"] != "So What" {
		t.Fatalf("name = %v", first["name"])
	}
	tsources, _ := first["sources"].([]any)
	if len(tsources) != 1 {
		t.Fatalf("track sources = %v", first["sources"])
	}
	tsrc := tsources[0].(map[string]any)
	if tsrc["id"] != "v_so_what" {
		t.Fatalf("track source id = %v", tsrc["id"])
	}
	artists, _ := first["artists"].([]any)
	if len(artists) != 1 {
		t.Fatalf("artists = %v", first["artists"])
	}
	artist := artists[0].(map[string]any)
	if artist["name"] != "Miles Davis - Topic" {
		t.Fatalf("artist name = %v", artist["name"])
	}
	extra, _ := tsrc["extra"].(map[string]any)
	if extra["thumbnail"] != "https://i.ytimg.com/vi/xxx/hqdefault.jpg" {
		t.Fatalf("source extra.thumbnail = %v", extra["thumbnail"])
	}
}

func TestYouTubeMusicConnector_HandleProxy_WithMockYouTube(t *testing.T) {
	mockYT := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mock-yt-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/channels"):
			io.WriteString(w, `{"items":[{"id":"ch1","snippet":{"title":"Alice"},"contentDetails":{"relatedPlaylists":{"likes":"LL123"}}}]}`)
		case strings.HasPrefix(r.URL.Path, "/playlists"):
			io.WriteString(w, `{"items":[{"id":"PL1","snippet":{"title":"Jazz"},"contentDetails":{"itemCount":10}}],"pageInfo":{"totalResults":1}}`)
		case strings.HasPrefix(r.URL.Path, "/search"):
			io.WriteString(w, `{"items":[{"id":{"kind":"youtube#video","videoId":"v1"},"snippet":{"title":"So What","channelTitle":"Miles Davis"}}],"pageInfo":{"totalResults":1}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockYT.Close()

	cfg := &Config{
		Services: []Service{
			{ID: "youtube-music", Name: "YouTube Music", Action: "inject", Endpoint: mockYT.URL},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.tokens.Save("youtube-music", &tokens.OAuthToken{
		AccessToken: "mock-yt-token",
		Expiry:      time.Now().Add(time.Hour),
	})
	srv.connectors = map[string]connectors.ServiceConnector{
		"youtube-music": connectors.NewYouTubeMusicConnector(srv),
	}

	// Connector-level /me and /search (not agent API under issue #119).
	req := httptest.NewRequest("GET", "/v1/services/youtube-music/me", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.connectors["youtube-music"].HandleProxy(w, req, "/me")

	if w.Code != http.StatusOK {
		body := w.Body.String()
		t.Fatalf("status = %d, want 200, body: %s", w.Code, body)
	}

	var result map[string]any
	json.NewDecoder(w.Body).Decode(&result)

	if result["id"] != "ch1" {
		t.Fatalf("id = %v", result["id"])
	}
	if result["title"] != "Alice" {
		t.Fatalf("title = %v", result["title"])
	}

	// Test /search — connector transform only.
	req = httptest.NewRequest("GET", "/v1/services/youtube-music/search?q=miles+davis", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w = httptest.NewRecorder()

	srv.connectors["youtube-music"].HandleProxy(w, req, "/search")

	if w.Code != http.StatusOK {
		t.Fatalf("search status = %d, want 200", w.Code)
	}

	var searchResult map[string]any
	json.NewDecoder(w.Body).Decode(&searchResult)

	results, ok := searchResult["results"].([]any)
	if !ok || len(results) == 0 {
		t.Fatal("expected results in search response")
	}
	first := results[0].(map[string]any)
	if first["title"] != "So What" {
		t.Fatalf("title = %v", first["title"])
	}
}
