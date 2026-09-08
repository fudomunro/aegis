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

func TestYouTubeMusicConnector_SystemInstructions(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	conn := NewYouTubeMusicConnector(srv)

	instructions := conn.SystemInstructions("https://guardian.example.com")

	if !strings.Contains(instructions, "/v1/services/youtube-music") {
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
	if !strings.Contains(instructions, "GET /me/liked") {
		t.Fatal("instructions missing /me/liked endpoint")
	}
}

func TestYouTubeMusicConnector_MapPath(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	conn := NewYouTubeMusicConnector(srv)
	query := map[string][]string{}

	tests := []struct {
		path       string
		wantAPI    string
		wantParams map[string]string
	}{
		{"/me", "/channels", map[string]string{"part": "snippet,contentDetails", "mine": "true"}},
		{"/me/playlists", "/playlists", map[string]string{"part": "snippet,contentDetails", "mine": "true"}},
		{"/me/subscriptions", "/subscriptions", map[string]string{"part": "snippet", "mine": "true"}},
		{"/playlists/PL123", "/playlists", map[string]string{"part": "snippet,contentDetails", "id": "PL123"}},
		{"/playlists/PL123/items", "/playlistItems", map[string]string{"part": "snippet,contentDetails", "playlistId": "PL123"}},
		{"/videos/vid123", "/videos", map[string]string{"part": "snippet,contentDetails,statistics", "id": "vid123"}},
		{"/channels/ch123", "/channels", map[string]string{"part": "snippet,contentDetails,statistics", "id": "ch123"}},
		{"/search", "/search", map[string]string{"part": "snippet", "type": "video", "videoCategoryId": "10"}},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			apiPath, params, err := conn.mapPath(tt.path, query)
			if err != nil {
				t.Fatalf("mapPath(%s) error: %v", tt.path, err)
			}
			if apiPath != tt.wantAPI {
				t.Fatalf("apiPath = %s, want %s", apiPath, tt.wantAPI)
			}
			for k, v := range tt.wantParams {
				if params[k] != v {
					t.Fatalf("params[%s] = %s, want %s", k, params[k], v)
				}
			}
		})
	}
}

func TestYouTubeMusicConnector_MapPath_SearchWithType(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	conn := NewYouTubeMusicConnector(srv)
	query := map[string][]string{"type": {"playlist"}}

	apiPath, params, err := conn.mapPath("/search", query)
	if err != nil {
		t.Fatal(err)
	}
	if apiPath != "/search" {
		t.Fatalf("apiPath = %s", apiPath)
	}
	if params["type"] != "playlist" {
		t.Fatalf("type = %s, want playlist", params["type"])
	}
	// Should NOT add videoCategoryId when type is explicitly playlist.
	if _, ok := params["videoCategoryId"]; ok {
		t.Fatal("should not add videoCategoryId for playlist type")
	}
}

func TestYouTubeMusicConnector_TransformChannel(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	conn := NewYouTubeMusicConnector(srv)

	raw := map[string]any{
		"items": []any{
			map[string]any{
				"id": "ch1",
				"snippet": map[string]any{
					"title":       "Alice Music",
					"description": "My music channel",
				},
				"contentDetails": map[string]any{
					"relatedPlaylists": map[string]any{
						"likes":    "LLikes123",
						"uploads":  "UUUploads456",
						"watchLater": "WL789",
					},
				},
			},
		},
	}

	result := conn.transformChannel(raw)
	if result["id"] != "ch1" {
		t.Fatalf("id = %v", result["id"])
	}
	if result["title"] != "Alice Music" {
		t.Fatalf("title = %v", result["title"])
	}
	playlists, ok := result["playlists"].(map[string]any)
	if !ok {
		t.Fatal("expected playlists in channel response")
	}
	if playlists["likes"] != "LLikes123" {
		t.Fatalf("likes = %v", playlists["likes"])
	}
}

func TestYouTubeMusicConnector_TransformPlaylistList(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	conn := NewYouTubeMusicConnector(srv)

	raw := map[string]any{
		"items": []any{
			map[string]any{
				"id": "PL1",
				"snippet": map[string]any{
					"title":       "Jazz Favorites",
					"description": "Best jazz tracks",
				},
				"contentDetails": map[string]any{
					"itemCount": 25,
				},
			},
		},
		"pageInfo": map[string]any{"totalResults": 1},
	}

	result := conn.transformPlaylistList(raw)
	playlists, ok := result["playlists"].([]map[string]any)
	if !ok {
		t.Fatalf("playlists type = %T", result["playlists"])
	}
	if len(playlists) != 1 {
		t.Fatalf("got %d playlists, want 1", len(playlists))
	}
	if playlists[0]["title"] != "Jazz Favorites" {
		t.Fatalf("title = %v", playlists[0]["title"])
	}
	if playlists[0]["item_count"] != 25 {
		t.Fatalf("item_count = %v", playlists[0]["item_count"])
	}
}

func TestYouTubeMusicConnector_TransformPlaylistItems(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	conn := NewYouTubeMusicConnector(srv)

	raw := map[string]any{
		"items": []any{
			map[string]any{
				"snippet": map[string]any{
					"title":        "So What - Miles Davis",
					"channelTitle": "Jazz Classics",
					"publishedAt":  "2020-01-01T00:00:00Z",
				},
				"contentDetails": map[string]any{
					"videoId": "vid123",
				},
			},
		},
		"pageInfo": map[string]any{"totalResults": 1},
	}

	result := conn.transformPlaylistItems(raw)
	videos, ok := result["videos"].([]map[string]any)
	if !ok {
		t.Fatalf("videos type = %T", result["videos"])
	}
	if len(videos) != 1 {
		t.Fatalf("got %d videos, want 1", len(videos))
	}
	if videos[0]["title"] != "So What - Miles Davis" {
		t.Fatalf("title = %v", videos[0]["title"])
	}
	if videos[0]["video_id"] != "vid123" {
		t.Fatalf("video_id = %v", videos[0]["video_id"])
	}
	if videos[0]["channel"] != "Jazz Classics" {
		t.Fatalf("channel = %v", videos[0]["channel"])
	}
}

func TestYouTubeMusicConnector_TransformSearch(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	conn := NewYouTubeMusicConnector(srv)

	raw := map[string]any{
		"items": []any{
			map[string]any{
				"id": map[string]any{
					"kind":    "youtube#video",
					"videoId": "vid456",
				},
				"snippet": map[string]any{
					"title":        "Blue in Green",
					"channelTitle": "Miles Davis",
					"description":  "Classic jazz track",
				},
			},
		},
		"pageInfo": map[string]any{"totalResults": 1},
	}

	result := conn.transformSearch(raw)
	results, ok := result["results"].([]map[string]any)
	if !ok {
		t.Fatalf("results type = %T", result["results"])
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0]["title"] != "Blue in Green" {
		t.Fatalf("title = %v", results[0]["title"])
	}
	if results[0]["video_id"] != "vid456" {
		t.Fatalf("video_id = %v", results[0]["video_id"])
	}
	if results[0]["type"] != "video" {
		t.Fatalf("type = %v", results[0]["type"])
	}
}

func TestYouTubeMusicConnector_TransformVideo(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	conn := NewYouTubeMusicConnector(srv)

	raw := map[string]any{
		"items": []any{
			map[string]any{
				"id": "vid1",
				"snippet": map[string]any{
					"title":       "So What",
					"channelTitle": "Miles Davis",
					"description": "Kind of Blue",
				},
				"contentDetails": map[string]any{
					"duration": "PT9M22S",
				},
				"statistics": map[string]any{
					"viewCount": "1000000",
					"likeCount": "50000",
				},
			},
		},
	}

	result := conn.transformVideo(raw)
	if result["title"] != "So What" {
		t.Fatalf("title = %v", result["title"])
	}
	if result["duration"] != "PT9M22S" {
		t.Fatalf("duration = %v", result["duration"])
	}
	if result["view_count"] != "1000000" {
		t.Fatalf("view_count = %v", result["view_count"])
	}
}

func TestYouTubeMusicConnector_TransformSubscriptions(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	conn := NewYouTubeMusicConnector(srv)

	raw := map[string]any{
		"items": []any{
			map[string]any{
				"snippet": map[string]any{
					"title":       "Blue Note Records",
					"description": "Jazz label",
					"resourceId": map[string]any{
						"channelId": "chBN123",
					},
				},
			},
		},
		"pageInfo": map[string]any{"totalResults": 1},
	}

	result := conn.transformSubscriptions(raw)
	subs, ok := result["subscriptions"].([]map[string]any)
	if !ok {
		t.Fatalf("subscriptions type = %T", result["subscriptions"])
	}
	if len(subs) != 1 {
		t.Fatalf("got %d subscriptions, want 1", len(subs))
	}
	if subs[0]["title"] != "Blue Note Records" {
		t.Fatalf("title = %v", subs[0]["title"])
	}
	if subs[0]["channel_id"] != "chBN123" {
		t.Fatalf("channel_id = %v", subs[0]["channel_id"])
	}
}

func TestYouTubeMusicConnector_HandleProxy_NoToken(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "youtube-music", Name: "YouTube Music", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.connectors = map[string]ServiceConnector{
		"youtube-music": NewYouTubeMusicConnector(srv),
	}

	req := httptest.NewRequest("GET", "/v1/services/youtube-music/me", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandleServiceProxy(w, req)

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
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.tokens.Save("youtube-music", &OAuthToken{
		AccessToken: "mock-yt-token",
		Expiry:      time.Now().Add(time.Hour),
	})
	srv.connectors = map[string]ServiceConnector{
		"youtube-music": NewYouTubeMusicConnector(srv),
	}

	req := httptest.NewRequest("GET", "/v1/services/youtube-music/me/playlists", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandleServiceProxy(w, req)

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
	if first["title"] != "Jazz Favorites" {
		t.Fatalf("first playlist title = %v", first["title"])
	}
	if first["item_count"] != 25.0 {
		t.Fatalf("first playlist item_count = %v", first["item_count"])
	}

	// Should have fetched both pages.
	if callCount != 2 {
		t.Fatalf("upstream called %d times, want 2 (pagination)", callCount)
	}

	// Verify total_fetched was set.
	if result["total_fetched"] != 3.0 {
		t.Fatalf("total_fetched = %v, want 3", result["total_fetched"])
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
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.tokens.Save("youtube-music", &OAuthToken{
		AccessToken: "mock-yt-token",
		Expiry:      time.Now().Add(time.Hour),
	})
	srv.connectors = map[string]ServiceConnector{
		"youtube-music": NewYouTubeMusicConnector(srv),
	}

	handler := srv.AuthMiddleware(srv.HandleServiceProxy)

	// First request — cache miss.
	req := httptest.NewRequest("GET", "/v1/services/youtube-music/me/playlists", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Header().Get("X-Guardian-Cache") != "miss" {
		t.Fatalf("first request: cache = %q, want miss", w.Header().Get("X-Guardian-Cache"))
	}

	// Second request — cache hit.
	req = httptest.NewRequest("GET", "/v1/services/youtube-music/me/playlists", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w = httptest.NewRecorder()
	handler(w, req)

	if w.Header().Get("X-Guardian-Cache") != "hit" {
		t.Fatalf("second request: cache = %q, want hit", w.Header().Get("X-Guardian-Cache"))
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
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.tokens.Save("youtube-music", &OAuthToken{
		AccessToken: "mock-yt-token",
		Expiry:      time.Now().Add(time.Hour),
	})
	srv.connectors = map[string]ServiceConnector{
		"youtube-music": NewYouTubeMusicConnector(srv),
	}

	req := httptest.NewRequest("GET", "/v1/services/youtube-music/playlists/PL_SPECIAL", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandleServiceProxy(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}

	var result map[string]any
	json.NewDecoder(w.Body).Decode(&result)

	if result["id"] != "PL_SPECIAL" {
		t.Fatalf("id = %v", result["id"])
	}
	if result["title"] != "My Jazz Collection" {
		t.Fatalf("title = %v", result["title"])
	}
	if result["description"] != "Best jazz tracks of all time" {
		t.Fatalf("description = %v", result["description"])
	}
	if result["channel"] != "Alice Music" {
		t.Fatalf("channel = %v", result["channel"])
	}
	if result["item_count"] != 87.0 {
		t.Fatalf("item_count = %v", result["item_count"])
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
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.tokens.Save("youtube-music", &OAuthToken{
		AccessToken: "mock-yt-token",
		Expiry:      time.Now().Add(time.Hour),
	})
	srv.connectors = map[string]ServiceConnector{
		"youtube-music": NewYouTubeMusicConnector(srv),
	}

	req := httptest.NewRequest("GET", "/v1/services/youtube-music/playlists/PL_JAZZ/items", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandleServiceProxy(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}

	var result map[string]any
	json.NewDecoder(w.Body).Decode(&result)

	videos, ok := result["videos"].([]any)
	if !ok {
		t.Fatalf("videos type = %T", result["videos"])
	}
	if len(videos) != 2 {
		t.Fatalf("got %d videos, want 2", len(videos))
	}

	first := videos[0].(map[string]any)
	if first["title"] != "So What" {
		t.Fatalf("title = %v", first["title"])
	}
	if first["video_id"] != "v_so_what" {
		t.Fatalf("video_id = %v", first["video_id"])
	}
	if first["channel"] != "Miles Davis - Topic" {
		t.Fatalf("channel = %v", first["channel"])
	}
	if first["thumbnail"] != "https://i.ytimg.com/vi/xxx/hqdefault.jpg" {
		t.Fatalf("thumbnail = %v", first["thumbnail"])
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
	srv := testServerWithBase(cfg, "https://guardian.example.com")
	srv.tokens.Save("youtube-music", &OAuthToken{
		AccessToken: "mock-yt-token",
		Expiry:      time.Now().Add(time.Hour),
	})
	srv.connectors = map[string]ServiceConnector{
		"youtube-music": NewYouTubeMusicConnector(srv),
	}

	// Test /me — should be transformed.
	req := httptest.NewRequest("GET", "/v1/services/youtube-music/me", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	srv.HandleServiceProxy(w, req)

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

	// Test /search — should be transformed.
	req = httptest.NewRequest("GET", "/v1/services/youtube-music/search?q=miles+davis", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w = httptest.NewRecorder()

	srv.HandleServiceProxy(w, req)

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
