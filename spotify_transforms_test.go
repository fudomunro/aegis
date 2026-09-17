package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSimplify(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	conn := NewSpotifyConnector(srv)

	raw := map[string]any{
		"id":            "x",
		"name":          "Track",
		"external_urls": map[string]any{"spotify": "https://open.spotify.com/track/x"},
		"followers":     map[string]any{"total": 1},
	}
	got := conn.simplify(raw)
	if got["id"] != "x" || got["name"] != "Track" {
		t.Fatalf("simplify = %+v", got)
	}
	if _, ok := got["external_urls"]; ok {
		t.Fatal("should drop unlisted fields")
	}
}

func TestIsPaginated(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	conn := NewSpotifyConnector(srv)

	if !conn.isPaginated("/me/playlists") {
		t.Fatal("expected /me/playlists paginated")
	}
	if conn.isPaginated("/me") {
		t.Fatal("expected /me not paginated")
	}
}

func TestTransformSavedAlbums(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	conn := NewSpotifyConnector(srv)

	raw := map[string]any{
		"items": []any{
			map[string]any{"album": map[string]any{
				"name": "Kind of Blue", "id": "ab1", "uri": "spotify:album:ab1",
				"artists": []any{map[string]any{"name": "Miles Davis"}},
			}},
			"not-an-object",
			map[string]any{"no_album": true},
		},
		"total": 3,
	}
	got := conn.transformSavedAlbums(raw)
	albums, _ := got["albums"].([]map[string]any)
	if len(albums) != 1 {
		t.Fatalf("albums = %+v", got)
	}
	if albums[0]["name"] != "Kind of Blue" || albums[0]["id"] != "ab1" {
		t.Fatalf("album = %+v", albums[0])
	}
}

func TestTransformRecentlyPlayed(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	conn := NewSpotifyConnector(srv)

	raw := map[string]any{
		"items": []any{
			map[string]any{
				"played_at": "2026-01-01T00:00:00Z",
				"track": map[string]any{
					"name":    "So What",
					"artists": []any{map[string]any{"name": "Miles Davis"}},
					"album":   map[string]any{"name": "Kind of Blue"},
				},
			},
			map[string]any{"track": nil},
			"skip",
		},
	}
	got := conn.transformRecentlyPlayed(raw)
	tracks, _ := got["tracks"].([]map[string]any)
	if len(tracks) != 1 {
		t.Fatalf("tracks = %+v", got)
	}
	if tracks[0]["name"] != "So What" || tracks[0]["played_at"] != "2026-01-01T00:00:00Z" {
		t.Fatalf("track = %+v", tracks[0])
	}
}

func TestTransformTrackListAndArtistList(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	conn := NewSpotifyConnector(srv)

	tracks := conn.transformTrackList(map[string]any{
		"items": []any{
			map[string]any{
				"name":    "Blue in Green",
				"id":      "t1",
				"uri":     "spotify:track:t1",
				"artists": []any{map[string]any{"name": "Miles Davis"}},
				"album":   map[string]any{"name": "Kind of Blue"},
			},
		},
	})
	tl, _ := tracks["tracks"].([]map[string]any)
	if len(tl) != 1 || tl[0]["name"] != "Blue in Green" {
		t.Fatalf("tracks = %+v", tracks)
	}

	artists := conn.transformArtistList(map[string]any{
		"items": []any{
			map[string]any{"name": "Miles Davis", "id": "a1", "uri": "spotify:artist:a1", "genres": []any{"jazz"}},
		},
	})
	al, _ := artists["artists"].([]map[string]any)
	if len(al) != 1 || al[0]["name"] != "Miles Davis" {
		t.Fatalf("artists = %+v", artists)
	}
}

func TestTransformTrackAndAlbum(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	conn := NewSpotifyConnector(srv)

	track := conn.transformTrack(map[string]any{
		"name":        "So What",
		"id":          "t1",
		"uri":         "spotify:track:t1",
		"duration_ms": 562000,
		"artists":     []any{map[string]any{"name": "Miles Davis"}},
		"album":       map[string]any{"name": "Kind of Blue"},
	})
	if track["name"] != "So What" || track["duration_ms"] != 562000 {
		t.Fatalf("track = %+v", track)
	}

	album := conn.transformAlbum(map[string]any{
		"name":         "Kind of Blue",
		"id":           "ab1",
		"uri":          "spotify:album:ab1",
		"release_date": "1959-08-17",
		"artists":      []any{map[string]any{"name": "Miles Davis"}},
		"tracks": map[string]any{
			"items": []any{
				map[string]any{"name": "So What", "track_number": 1, "duration_ms": 562000, "uri": "spotify:track:t1"},
			},
		},
	})
	if album["name"] != "Kind of Blue" {
		t.Fatalf("album = %+v", album)
	}
	tracks, _ := album["tracks"].([]map[string]any)
	if len(tracks) != 1 || tracks[0]["name"] != "So What" {
		t.Fatalf("album tracks = %+v", album)
	}
}

func TestSpotifyNeutralTrack(t *testing.T) {
	tr := spotifyNeutralTrack(map[string]any{
		"id":          "t1",
		"name":        "So What",
		"uri":         "spotify:track:t1",
		"duration_ms": float64(222000),
		"external_ids": map[string]any{
			"isrc": "usrc17000001",
		},
		"artists": []any{
			map[string]any{"name": "Miles Davis"},
		},
		"album": map[string]any{"id": "ab1", "name": "Kind of Blue"},
	})
	if tr.Name != "So What" {
		t.Fatalf("name = %q", tr.Name)
	}
	if len(tr.Artists) != 1 || tr.Artists[0].Name != "Miles Davis" {
		t.Fatalf("artists = %+v", tr.Artists)
	}
	if tr.Album == nil || tr.Album.Name != "Kind of Blue" {
		t.Fatalf("album = %+v", tr.Album)
	}
	if tr.EntityID != "USRC17000001" {
		t.Fatalf("entity_id = %q", tr.EntityID)
	}
	if tr.DurationMs != 222000 {
		t.Fatalf("duration_ms = %d", tr.DurationMs)
	}
	if len(tr.Sources) != 1 || tr.Sources[0].Service != "spotify" || tr.Sources[0].URL != "https://open.spotify.com/track/t1" {
		t.Fatalf("sources = %+v", tr.Sources)
	}
	if tr.Sources[0].Extra["uri"] != "spotify:track:t1" {
		t.Fatalf("extra = %+v", tr.Sources[0].Extra)
	}
	if tr.Sources[0].Extra["isrc"] != "usrc17000001" {
		t.Fatalf("source isrc = %+v", tr.Sources[0].Extra)
	}
}

func TestTransformPlaylistDetail(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	conn := NewSpotifyConnector(srv)

	detail := conn.transformPlaylistDetail(map[string]any{
		"id":          "pl1",
		"name":        "Jazz Vibes",
		"description": "Smooth",
		"tracks": map[string]any{
			"total": 10,
			"items": []any{
				map[string]any{
					"track": map[string]any{
						"id":      "t1",
						"name":    "So What",
						"uri":     "spotify:track:t1",
						"artists": []any{map[string]any{"name": "Miles Davis"}},
					},
				},
				map[string]any{"track": nil},
			},
		},
	})
	if detail.Name != "Jazz Vibes" {
		t.Fatalf("name = %q", detail.Name)
	}
	if detail.TrackCount != 10 || detail.TotalTracks != 10 {
		t.Fatalf("counts = %d/%d", detail.TrackCount, detail.TotalTracks)
	}
	if len(detail.Tracks) != 1 || detail.Tracks[0].Name != "So What" {
		t.Fatalf("tracks = %+v", detail.Tracks)
	}
}

func TestFetchAllPagesRaw(t *testing.T) {
	var page2URL string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/page2" {
			fmt.Fprint(w, `{"items":[{"id":"b"},{"id":"c"}],"next":null}`)
			return
		}
		fmt.Fprint(w, `{"items":[],"next":null}`)
	}))
	defer ts.Close()
	page2URL = ts.URL + "/page2"

	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	conn := NewSpotifyConnector(srv)

	first := map[string]any{
		"items":    []any{map[string]any{"id": "a"}},
		"next":     page2URL,
		"href":     "x",
		"limit":    float64(50),
		"offset":   float64(0),
		"previous": nil,
	}
	got := conn.fetchAllPagesRaw(first, "tok", slog.Default())
	items, _ := got["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("items = %d (%+v)", len(items), got["items"])
	}
	if _, ok := got["next"]; ok {
		t.Fatal("next should be removed")
	}
	if got["total_fetched"] != float64(3) {
		t.Fatalf("total_fetched = %v", got["total_fetched"])
	}
}

func TestExtractNamesAndNested(t *testing.T) {
	obj := map[string]any{
		"artists": []any{
			map[string]any{"name": "A"},
			"skip",
			map[string]any{"no_name": true},
			map[string]any{"name": "B"},
		},
		"album": map[string]any{"name": "Al", "id": "1"},
	}
	names := extractNames(obj, "artists")
	if len(names) != 2 || names[0] != "A" || names[1] != "B" {
		t.Fatalf("names = %v", names)
	}
	if extractNames(obj, "missing") != nil {
		t.Fatal("missing key should be nil")
	}
	if got := extractNested(obj, "album", "name"); got != "Al" {
		t.Fatalf("nested = %v", got)
	}
	if got := extractNested(obj, "album", "id"); got != "1" {
		t.Fatalf("nested id = %v", got)
	}
	if got := extractNested(obj, "album", "missing"); got != nil {
		t.Fatalf("missing nested = %v", got)
	}
	if got := extractNested(map[string]any{}, "a", "b"); got != nil {
		t.Fatalf("empty = %v", got)
	}
}

func TestExtraOrNil(t *testing.T) {
	if extraOrNil(nil) != nil {
		t.Fatal("nil map should stay nil")
	}
	if extraOrNil(map[string]any{}) != nil {
		t.Fatal("empty map should be nil")
	}
	m := map[string]any{"uri": "x"}
	if extraOrNil(m)["uri"] != "x" {
		t.Fatal("non-empty map preserved")
	}
}

func TestNewAlbum(t *testing.T) {
	if newAlbum("spotify", "Spotify", "1", "  ") != nil {
		t.Fatal("blank album name should be nil")
	}
	a := newAlbum("spotify", "Spotify", "1", " Kind of Blue ")
	if a == nil || a.Name != "Kind of Blue" || a.Sources[0].Service != "spotify" {
		t.Fatalf("album = %+v", a)
	}
}

func TestFirstSourceID(t *testing.T) {
	if firstSourceID(Playlist{}) != "" {
		t.Fatal("empty sources")
	}
	if firstSourceID(Playlist{Sources: []PlaylistSource{{ID: "pl1"}}}) != "pl1" {
		t.Fatal("id")
	}
}

func TestSpotifyTransformResponse_DefaultAndError(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	conn := NewSpotifyConnector(srv)

	out := conn.transformResponse("/unknown/path", []byte(`{"id":"1","name":"n","href":"drop"}`), slog.Default())
	m, ok := out.(map[string]any)
	if !ok || m["id"] != "1" || m["name"] != "n" {
		t.Fatalf("out = %#v", out)
	}
	if _, ok := m["href"]; ok {
		t.Fatal("simplify should drop href")
	}

	errOut := conn.transformResponse("/me", []byte(`{"error":{"status":401}}`), slog.Default())
	em, ok := errOut.(map[string]any)
	if !ok {
		t.Fatalf("errOut = %#v", errOut)
	}
	if _, ok := em["error"]; !ok {
		t.Fatal("expected error passthrough")
	}
}

func TestSpotify_NeutralPlaylistMissingExternalURL(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://guardian.example.com")
	conn := NewSpotifyConnector(srv)
	p := conn.neutralPlaylist(map[string]any{
		"id":   "pl1",
		"name": "Mix",
	})
	if p.Name != "Mix" || p.Sources[0].ID != "pl1" {
		t.Fatalf("playlist = %+v", p)
	}
	if p.Sources[0].URL != "https://open.spotify.com/playlist/pl1" {
		t.Fatalf("url = %q", p.Sources[0].URL)
	}
}
