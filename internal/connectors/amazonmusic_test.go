package connectors

import (
	"strings"
	"testing"
)

func TestAmazonMusicConnector_SystemInstructions(t *testing.T) {
	host := newTestHost("https://aegis.example.com")
	conn := NewAmazonMusicConnector(host)

	instructions := conn.SystemInstructions("https://aegis.example.com")

	if !strings.Contains(instructions, "/v1/library/playlists") {
		t.Fatal("instructions missing library playlists")
	}
	if !strings.Contains(instructions, "/v1/services/amazon-music/playlists/{id}") {
		t.Fatal("instructions missing playlist content path")
	}
	if strings.Contains(instructions, "GET /search") {
		t.Fatal("instructions must not advertise catalog search (issue #119)")
	}
}

func TestAmazonMusicConnector_TransformUser(t *testing.T) {
	host := newTestHost("https://aegis.example.com")
	conn := NewAmazonMusicConnector(host)

	// Standard format.
	raw := map[string]any{
		"id":          "amz123",
		"name":        "Alice",
		"email":       "alice@example.com",
		"customer_id": "CUST456",
		"extra_field": "should be stripped",
	}
	result := conn.TransformUser(raw)
	if result["id"] != "amz123" {
		t.Fatalf("id = %v", result["id"])
	}
	if result["name"] != "Alice" {
		t.Fatalf("name = %v", result["name"])
	}
	if _, ok := result["extra_field"]; ok {
		t.Fatal("extra_field should be stripped")
	}

	// Alternative field names.
	raw2 := map[string]any{
		"user_id":      "u1",
		"display_name": "Bob",
	}
	result2 := conn.TransformUser(raw2)
	if result2["id"] != "u1" {
		t.Fatalf("id = %v", result2["id"])
	}
	if result2["name"] != "Bob" {
		t.Fatalf("name = %v", result2["name"])
	}
}

func TestAmazonMusicConnector_TransformPlaylistList(t *testing.T) {
	host := newTestHost("https://aegis.example.com")
	conn := NewAmazonMusicConnector(host)

	raw := map[string]any{
		"items": []any{
			map[string]any{
				"id":          "pl1",
				"name":        "Jazz Favorites",
				"description": "Best jazz tracks",
				"track_count": 25,
			},
		},
		"total": 1,
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
	if len(p.Sources) != 1 || p.Sources[0].Service != "amazon-music" || p.Sources[0].ServiceName != "Amazon Music" {
		t.Fatalf("sources = %+v", p.Sources)
	}
}

func TestAmazonMusicConnector_TransformSearch(t *testing.T) {
	host := newTestHost("https://aegis.example.com")
	conn := NewAmazonMusicConnector(host)

	// Categorized results.
	raw := map[string]any{
		"tracks": map[string]any{
			"items": []any{
				map[string]any{
					"id":     "t1",
					"name":   "So What",
					"artist": "Miles Davis",
					"album":  "Kind of Blue",
				},
			},
		},
		"albums": map[string]any{
			"items": []any{
				map[string]any{
					"id":   "a1",
					"name": "Kind of Blue",
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
	if tracks[0]["artist"] != "Miles Davis" {
		t.Fatalf("artist = %v", tracks[0]["artist"])
	}
}

func TestAmazonMusicConnector_TransformSearch_FlatList(t *testing.T) {
	host := newTestHost("https://aegis.example.com")
	conn := NewAmazonMusicConnector(host)

	// Flat items list (no categories).
	raw := map[string]any{
		"items": []any{
			map[string]any{
				"id":   "t1",
				"name": "Blue in Green",
			},
		},
	}

	result := conn.TransformSearch(raw)
	results, ok := result["results"].([]map[string]any)
	if !ok {
		t.Fatalf("results type = %T", result["results"])
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
}

func TestAmazonMusicConnector_TransformTrack(t *testing.T) {
	host := newTestHost("https://aegis.example.com")
	conn := NewAmazonMusicConnector(host)

	// Direct track object.
	raw := map[string]any{
		"id":          "t1",
		"name":        "So What",
		"artist":      "Miles Davis",
		"album":       "Kind of Blue",
		"duration_ms": 562000,
	}
	result := conn.TransformTrack(raw)
	if result["name"] != "So What" {
		t.Fatalf("name = %v", result["name"])
	}
	if result["artist"] != "Miles Davis" {
		t.Fatalf("artist = %v", result["artist"])
	}
}

func TestAmazonMusicConnector_TransformAlbum(t *testing.T) {
	host := newTestHost("https://aegis.example.com")
	conn := NewAmazonMusicConnector(host)

	raw := map[string]any{
		"id":           "a1",
		"name":         "Kind of Blue",
		"artist":       "Miles Davis",
		"release_date": "1959-08-17",
		"track_count":  5,
		"items": []any{
			map[string]any{
				"name":         "So What",
				"track_number": 1,
				"duration_ms":  562000,
			},
		},
	}
	result := conn.TransformAlbum(raw)
	if result["name"] != "Kind of Blue" {
		t.Fatalf("name = %v", result["name"])
	}
	tracks, ok := result["tracks"].([]map[string]any)
	if !ok {
		t.Fatalf("tracks type = %T", result["tracks"])
	}
	if len(tracks) != 1 {
		t.Fatalf("got %d tracks, want 1", len(tracks))
	}
}

func TestAmazonMusicConnector_TransformArtist(t *testing.T) {
	host := newTestHost("https://aegis.example.com")
	conn := NewAmazonMusicConnector(host)

	raw := map[string]any{
		"id":         "ar1",
		"name":       "Miles Davis",
		"genres":     []any{"jazz", "bebop"},
		"popularity": 85,
	}
	result := conn.TransformArtist(raw)
	if result["name"] != "Miles Davis" {
		t.Fatalf("name = %v", result["name"])
	}
}

func TestAmazonMusicConnector_ExtractArtistName(t *testing.T) {
	host := newTestHost("https://aegis.example.com")
	conn := NewAmazonMusicConnector(host)

	// Direct string.
	if conn.ExtractArtistName(map[string]any{"artist": "Miles"}) != "Miles" {
		t.Fatal("direct string failed")
	}
	// Nested object.
	if conn.ExtractArtistName(map[string]any{"artist": map[string]any{"name": "Miles"}}) != "Miles" {
		t.Fatal("nested object failed")
	}
	// Array.
	if conn.ExtractArtistName(map[string]any{"artists": []any{map[string]any{"name": "Miles"}}}) != "Miles" {
		t.Fatal("array failed")
	}
	// Missing.
	if conn.ExtractArtistName(map[string]any{}) != "" {
		t.Fatal("expected empty for missing")
	}
}

func TestAmazonMusicConnector_FirstNonNil(t *testing.T) {
	host := newTestHost("https://aegis.example.com")
	conn := NewAmazonMusicConnector(host)

	m := map[string]any{"a": nil, "b": "found", "c": "also"}
	if conn.FirstNonNil(m, "a", "b", "c") != "found" {
		t.Fatal("expected 'found'")
	}
	if conn.FirstNonNil(m, "missing") != nil {
		t.Fatal("expected nil")
	}
}
