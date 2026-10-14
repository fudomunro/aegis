package core

import (
	"github.com/fudomunro/aegis/internal/connectors"

	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

func TestAmazonSimplify(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := connectors.NewAmazonMusicConnector(srv)
	raw := map[string]any{
		"id":            "1",
		"name":          "n",
		"external_urls": map[string]any{},
		"items":         []any{},
	}
	got := conn.Simplify(raw)
	if got["id"] != "1" || got["name"] != "n" {
		t.Fatalf("got = %+v", got)
	}
	if _, ok := got["external_urls"]; ok {
		t.Fatal("should drop unlisted keys")
	}
}

func TestAmazonListUserPlaylists_Unauthorized(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := connectors.NewAmazonMusicConnector(srv)
	if _, _, err := conn.ListUserPlaylists(context.Background()); err != types.ErrUnauthorized {
		t.Fatalf("err = %v", err)
	}
}

func TestAmazonListUserPlaylists_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{
			"items":[{
				"id":"amz1",
				"name":"Chill",
				"description":"relax",
				"item_count":8,
				"owner":{"name":"Alice"},
				"urn":"urn:playlist:1",
				"asin":"B001"
			}],
			"total":1
		}`)
	}))
	defer ts.Close()

	srv := testServerWithBase(&Config{
		Services: []Service{{ID: "amazon-music", Endpoint: ts.URL}},
	}, "https://aegis.example.com")
	srv.tokens.Tokens["amazon-music"] = &OAuthToken{
		AccessToken: "tok",
		Expiry:      time.Now().Add(time.Hour),
	}
	conn := connectors.NewAmazonMusicConnector(srv)
	playlists, _, err := conn.ListUserPlaylists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(playlists) != 1 || playlists[0].Name != "Chill" || playlists[0].TrackCount != 8 {
		t.Fatalf("playlists = %+v", playlists)
	}
	if playlists[0].Owner != "Alice" {
		t.Fatalf("owner = %q", playlists[0].Owner)
	}
	if playlists[0].Sources[0].Service != "amazon-music" {
		t.Fatalf("source = %+v", playlists[0].Sources[0])
	}
}

func TestAmazonListUserPlaylists_NextTokenTruncated(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{
			"items":[{"id":"amz1","name":"Chill","item_count":8}],
			"nextToken":"opaque-continuation"
		}`)
	}))
	defer ts.Close()

	srv := testServerWithBase(&Config{
		Services: []Service{{ID: "amazon-music", Endpoint: ts.URL}},
	}, "https://aegis.example.com")
	srv.tokens.Tokens["amazon-music"] = &OAuthToken{
		AccessToken: "tok",
		Expiry:      time.Now().Add(time.Hour),
	}
	conn := connectors.NewAmazonMusicConnector(srv)
	playlists, truncated, err := conn.ListUserPlaylists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(playlists) != 1 {
		t.Fatalf("playlists = %+v", playlists)
	}
	if !truncated {
		t.Fatal("expected truncated=true when Amazon nextToken is present but unfollowed")
	}
}

func TestAmazonListUserPlaylists_HTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer ts.Close()

	srv := testServerWithBase(&Config{
		Services: []Service{{ID: "amazon-music", Endpoint: ts.URL}},
	}, "https://aegis.example.com")
	srv.tokens.Tokens["amazon-music"] = &OAuthToken{
		AccessToken: "tok",
		Expiry:      time.Now().Add(time.Hour),
	}
	conn := connectors.NewAmazonMusicConnector(srv)
	if _, _, err := conn.ListUserPlaylists(context.Background()); err == nil {
		t.Fatal("expected HTTP error")
	}
}

func TestAmazonNeutralAmazonTrack(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := connectors.NewAmazonMusicConnector(srv)

	tr := conn.NeutralAmazonTrack(map[string]any{
		"id":          "t1",
		"title":       "Blue Train",
		"artistName":  "John Coltrane",
		"albumName":   "Blue Train",
		"duration_ms": 643000,
		"duration":    "PT10M43S",
		"asin":        "B0TRACK",
		"popularity":  float64(10),
	})
	if tr.Name != "Blue Train" {
		t.Fatalf("name = %q", tr.Name)
	}
	if len(tr.Artists) != 1 || tr.Artists[0].Name != "John Coltrane" {
		t.Fatalf("artists = %+v", tr.Artists)
	}
	if tr.Album == nil || tr.Album.Name != "Blue Train" {
		t.Fatalf("album = %+v", tr.Album)
	}
	if tr.DurationMs != 643000 || tr.Duration != "PT10M43S" {
		t.Fatalf("duration = %d %q", tr.DurationMs, tr.Duration)
	}
	if tr.Sources[0].Service != "amazon-music" || tr.Sources[0].ID != "t1" {
		t.Fatalf("sources = %+v", tr.Sources)
	}
}

func TestAmazonTransformLibrary(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := connectors.NewAmazonMusicConnector(srv)

	got := conn.TransformLibrary(map[string]any{
		"items": []any{
			map[string]any{"id": "1", "title": "Song", "artist": "A", "album": "B"},
		},
		"total": 1,
	})
	tracks, _ := got["tracks"].([]map[string]any)
	if len(tracks) != 1 || tracks[0]["name"] != "Song" || tracks[0]["artist"] != "A" {
		t.Fatalf("got = %+v", got)
	}
}

func TestAmazonTransformRecommendations(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := connectors.NewAmazonMusicConnector(srv)

	got := conn.TransformRecommendations(map[string]any{
		"results": []any{
			map[string]any{"id": "1", "name": "Rec", "artist": map[string]any{"name": "A"}},
		},
	})
	recs, _ := got["recommendations"].([]map[string]any)
	if len(recs) != 1 || recs[0]["name"] != "Rec" {
		t.Fatalf("got = %+v", got)
	}
}

func TestAmazonTransformPlaylistDetail(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := connectors.NewAmazonMusicConnector(srv)

	detail := conn.TransformPlaylistDetail(map[string]any{
		"id":         "pl1",
		"name":       "Focus",
		"item_count": 2,
		"tracks": map[string]any{
			"items": []any{
				map[string]any{"id": "t1", "title": "One", "artistName": "A"},
			},
		},
	})
	if detail.Name != "Focus" {
		t.Fatalf("name = %q", detail.Name)
	}
	if len(detail.Tracks) != 1 || detail.Tracks[0].Name != "One" {
		t.Fatalf("tracks = %+v", detail.Tracks)
	}

	// Top-level items path
	detail2 := conn.TransformPlaylistDetail(map[string]any{
		"id":    "pl2",
		"title": "Alt",
		"items": []any{
			map[string]any{"id": "t2", "name": "Two"},
		},
	})
	if detail2.Name != "Alt" || len(detail2.Tracks) != 1 {
		t.Fatalf("detail2 = %+v", detail2)
	}
}

func TestAmazonTransformTrackListAndSingle(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := connectors.NewAmazonMusicConnector(srv)

	listed := conn.TransformTrack(map[string]any{
		"items": []any{
			map[string]any{"id": "t1", "title": "One", "artist": "A", "album": "B", "duration_ms": float64(100)},
		},
	})
	if listed["name"] != "One" {
		t.Fatalf("listed = %+v", listed)
	}

	single := conn.TransformTrack(map[string]any{
		"id":     "t2",
		"name":   "Two",
		"artist": "C",
	})
	if single["id"] != "t2" || single["name"] != "Two" {
		t.Fatalf("single = %+v", single)
	}
}

func TestAmazonTransformAlbum(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := connectors.NewAmazonMusicConnector(srv)

	got := conn.TransformAlbum(map[string]any{
		"id":           "ab1",
		"title":        "Album",
		"artistName":   "A",
		"release_date": "1959",
		"items": []any{
			map[string]any{"title": "T1", "track_number": float64(1), "duration_ms": float64(10)},
		},
	})
	if got["name"] != "Album" || got["artist"] != "A" {
		t.Fatalf("got = %+v", got)
	}
	tracks, _ := got["tracks"].([]map[string]any)
	if len(tracks) != 1 || tracks[0]["name"] != "T1" {
		t.Fatalf("tracks = %+v", got)
	}
}

func TestAmazonExtractHelpers(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := connectors.NewAmazonMusicConnector(srv)

	if items := conn.ExtractItems(map[string]any{"results": []any{1}}); len(items) != 1 {
		t.Fatalf("results items = %v", items)
	}
	if items := conn.ExtractItems(map[string]any{"data": []any{1, 2}}); len(items) != 2 {
		t.Fatalf("data items = %v", items)
	}
	if items := conn.ExtractItems(map[string]any{}); items != nil {
		t.Fatalf("empty items = %v", items)
	}

	if n := conn.TotalCount(map[string]any{"pageInfo": map[string]any{"totalResults": float64(9)}}); n != float64(9) {
		t.Fatalf("pageInfo total = %v", n)
	}
	if n := conn.TotalCount(map[string]any{}); n != nil {
		t.Fatalf("empty total = %v", n)
	}

	if got := conn.ExtractArtistName(map[string]any{"artists": []any{map[string]any{"name": "A"}}}); got != "A" {
		t.Fatalf("artists array = %q", got)
	}
	if got := conn.ExtractArtistName(map[string]any{"artist": map[string]any{"name": "B"}}); got != "B" {
		t.Fatalf("artist obj = %q", got)
	}
	if got := conn.ExtractAlbumName(map[string]any{"album": map[string]any{"name": "Al"}}); got != "Al" {
		t.Fatalf("album obj = %q", got)
	}
}

func TestAmazonFirstNonNil(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := connectors.NewAmazonMusicConnector(srv)
	m := map[string]any{"a": nil, "b": "val"}
	if got := conn.FirstNonNil(m, "a", "b"); got != "val" {
		t.Fatalf("got = %v", got)
	}
	if got := conn.FirstNonNil(m, "missing"); got != nil {
		t.Fatalf("got = %v", got)
	}
}
