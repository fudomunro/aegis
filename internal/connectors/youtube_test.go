package connectors

import (
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

func TestYouTubeMusicConnector_SystemInstructions(t *testing.T) {
	host := newTestHost("https://aegis.example.com")
	conn := NewYouTubeMusicConnector(host)

	instructions := conn.SystemInstructions("https://aegis.example.com")

	if !strings.Contains(instructions, "/v1/library/playlists") {
		t.Fatal("instructions missing library playlists")
	}
	if !strings.Contains(instructions, "/v1/library/tracks") {
		t.Fatal("instructions missing library tracks")
	}
	if !strings.Contains(instructions, "/v1/services/youtube-music/playlists/{id}") {
		t.Fatal("instructions missing playlist content path")
	}
	if strings.Contains(instructions, "GET /search") {
		t.Fatal("instructions must not advertise catalog search (issue #119)")
	}
}

func TestYouTubeMusicConnector_MapPath(t *testing.T) {
	host := newTestHost("https://aegis.example.com")
	conn := NewYouTubeMusicConnector(host)
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
			apiPath, params, err := conn.MapPath(tt.path, query)
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
	host := newTestHost("https://aegis.example.com")
	conn := NewYouTubeMusicConnector(host)
	query := map[string][]string{"type": {"playlist"}}

	apiPath, params, err := conn.MapPath("/search", query)
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
	host := newTestHost("https://aegis.example.com")
	conn := NewYouTubeMusicConnector(host)

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
						"likes":      "LLikes123",
						"uploads":    "UUUploads456",
						"watchLater": "WL789",
					},
				},
			},
		},
	}

	result := conn.TransformChannel(raw)
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
	host := newTestHost("https://aegis.example.com")
	conn := NewYouTubeMusicConnector(host)

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

	result := conn.TransformPlaylistList(raw)
	if len(result.Playlists) != 1 {
		t.Fatalf("got %d playlists, want 1", len(result.Playlists))
	}
	p := result.Playlists[0]
	if p.Name != "Jazz Favorites" {
		t.Fatalf("name = %v", p.Name)
	}
	if p.TrackCount != 25 {
		t.Fatalf("track_count = %v", p.TrackCount)
	}
	if len(p.Sources) != 1 || p.Sources[0].Service != "youtube-music" {
		t.Fatalf("sources = %+v", p.Sources)
	}
	if result.Total != 1 {
		t.Fatalf("total = %v", result.Total)
	}
}

func TestYouTubeMusicConnector_TransformPlaylistItems(t *testing.T) {
	host := newTestHost("https://aegis.example.com")
	conn := NewYouTubeMusicConnector(host)

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

	result := conn.TransformPlaylistItems(raw)
	tracks, ok := result["tracks"].([]types.Track)
	if !ok {
		t.Fatalf("tracks type = %T", result["tracks"])
	}
	if len(tracks) != 1 {
		t.Fatalf("got %d tracks, want 1", len(tracks))
	}
	if tracks[0].Name != "So What - Miles Davis" {
		t.Fatalf("name = %v", tracks[0].Name)
	}
	if len(tracks[0].Sources) != 1 || tracks[0].Sources[0].ID != "vid123" {
		t.Fatalf("sources = %+v", tracks[0].Sources)
	}
	if len(tracks[0].Artists) != 1 || tracks[0].Artists[0].Name != "Jazz Classics" {
		t.Fatalf("artists = %+v", tracks[0].Artists)
	}
}

func TestYouTubeMusicConnector_TransformSearch(t *testing.T) {
	host := newTestHost("https://aegis.example.com")
	conn := NewYouTubeMusicConnector(host)

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

	result := conn.TransformSearch(raw)
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
	host := newTestHost("https://aegis.example.com")
	conn := NewYouTubeMusicConnector(host)

	raw := map[string]any{
		"items": []any{
			map[string]any{
				"id": "vid1",
				"snippet": map[string]any{
					"title":        "So What",
					"channelTitle": "Miles Davis",
					"description":  "Kind of Blue",
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

	result := conn.TransformVideo(raw)
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
	host := newTestHost("https://aegis.example.com")
	conn := NewYouTubeMusicConnector(host)

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

	result := conn.TransformSubscriptions(raw)
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
