package connectors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

func newYouTubeMirror(t *testing.T, up http.Handler) *YouTubeMusicConnector {
	t.Helper()
	ts := httptest.NewServer(up)
	t.Cleanup(ts.Close)
	th := newTestHostWithService(YouTubeMusicServiceID, ts.URL, "https://aegis.example.com")
	th.setToken(YouTubeMusicServiceID, "tok")
	return NewYouTubeMusicConnector(th)
}

func TestYouTube_ListPlaylistItems(t *testing.T) {
	var itemPages atomic.Int32
	c := newYouTubeMirror(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/playlists":
			if r.URL.Query().Get("id") != "PL1" {
				http.NotFound(w, r)
				return
			}
			writeMirrorJSON(w, map[string]any{
				"items": []any{map[string]any{
					"id":      "PL1",
					"snippet": map[string]any{"title": "Road Trip Mix"},
				}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/playlistItems":
			itemPages.Add(1)
			if itemPages.Load() == 1 {
				writeMirrorJSON(w, map[string]any{
					"items": []any{
						map[string]any{"snippet": map[string]any{
							"title":                  "Song One",
							"videoOwnerChannelTitle": "Artist One - Topic",
							"resourceId":             map[string]any{"videoId": "vid1"},
						}},
						map[string]any{"snippet": map[string]any{
							"title":      "No Video",
							"resourceId": map[string]any{},
						}},
					},
					"nextPageToken": "page2",
				})
				return
			}
			writeMirrorJSON(w, map[string]any{
				"items": []any{
					map[string]any{"snippet": map[string]any{
						"title":                  "Song Two",
						"videoOwnerChannelTitle": "Artist Two",
						"resourceId":             map[string]any{"videoId": "vid2"},
					}},
				},
				"nextPageToken": "",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	ctx := context.Background()

	ref, page, err := c.ListPlaylistItems(ctx, "PL1", "", 50)
	if err != nil {
		t.Fatalf("ListPlaylistItems: %v", err)
	}
	if ref.Name != "Road Trip Mix" || ref.Sources[0].ID != "PL1" ||
		ref.Sources[0].URL != "https://www.youtube.com/playlist?list=PL1" {
		t.Fatalf("ref = %+v", ref)
	}
	if page.Next != "page2" || len(page.Tracks) != 1 {
		t.Fatalf("page = %+v", page)
	}
	if sourceID(page.Tracks[0]) != "vid1" || page.Tracks[0].Artists[0].Name != "Artist One - Topic" {
		t.Fatalf("track0 = %+v", page.Tracks[0])
	}

	_, page2, err := c.ListPlaylistItems(ctx, "PL1", "page2", 50)
	if err != nil {
		t.Fatalf("ListPlaylistItems(page2): %v", err)
	}
	if page2.Next != "" || len(page2.Tracks) != 1 || sourceID(page2.Tracks[0]) != "vid2" {
		t.Fatalf("page2 = %+v", page2)
	}
}

func TestYouTube_ListPlaylistItems_Pages(t *testing.T) {
	c := newYouTubeMirror(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/playlists":
			json.NewEncoder(w).Encode(map[string]any{
				"items": []any{map[string]any{
					"snippet":        map[string]any{"title": "My list"},
					"contentDetails": map[string]any{"itemCount": 5},
				}},
			})
		case r.URL.Path == "/playlistItems":
			body := map[string]any{
				"items": []any{map[string]any{
					"snippet": map[string]any{
						"title":      "Clip",
						"resourceId": map[string]any{"videoId": "vid1"},
					},
				}},
			}
			if r.URL.Query().Get("pageToken") == "" {
				body["nextPageToken"] = "tok2"
			}
			json.NewEncoder(w).Encode(body)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	ref, page, err := c.ListPlaylistItems(context.Background(), "PLx", "", 5)
	if err != nil {
		t.Fatalf("ListPlaylistItems: %v", err)
	}
	if ref.Name != "My list" || page.Total != 5 || len(page.Tracks) != 1 || page.Next != "tok2" {
		t.Fatalf("page = (%+v, %+v)", ref, page)
	}
	_, page2, err := c.ListPlaylistItems(context.Background(), "PLx", "tok2", 5)
	if err != nil {
		t.Fatalf("ListPlaylistItems(page2): %v", err)
	}
	if page2.Next != "" {
		t.Fatalf("next2 = %q, want empty", page2.Next)
	}

	liked, err := c.ListUserLikedTracksPage(context.Background(), "", 5)
	if err != nil {
		t.Fatalf("ListUserLikedTracksPage: %v", err)
	}
	if liked.Total != 5 || len(liked.Tracks) != 1 {
		t.Fatalf("liked = %+v", liked)
	}
}

func TestYouTube_ListPlaylistItems_NotFound(t *testing.T) {
	c := newYouTubeMirror(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeMirrorJSON(w, map[string]any{"items": []any{}})
	}))
	_, _, err := c.ListPlaylistItems(context.Background(), "missing", "", 50)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing playlist err = %v", err)
	}
}

func TestYouTubePlacePlaylistItems_CreateSearchAndSkips(t *testing.T) {
	var itemsAdded atomic.Int32
	c := newYouTubeMirror(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/playlists":
			writeMirrorJSON(w, map[string]any{"id": "yt-pl"})
		case r.Method == http.MethodGet && r.URL.Path == "/search":
			switch {
			case strings.Contains(q.Get("q"), "Found"):
				writeMirrorJSON(w, map[string]any{
					"items": []any{map[string]any{"id": map[string]any{"videoId": "vid-found"}}},
				})
			case strings.Contains(q.Get("q"), "SearchError"):
				http.Error(w, "bad", http.StatusBadGateway)
			case strings.Contains(q.Get("q"), "BadShape"):
				writeMirrorJSON(w, map[string]any{
					"items": []any{"nope"},
				})
			default:
				writeMirrorJSON(w, map[string]any{"items": []any{}})
			}
		case r.Method == http.MethodPost && r.URL.Path == "/playlistItems":
			itemsAdded.Add(1)
			if q.Get("part") != "snippet" {
				t.Errorf("part = %q", q.Get("part"))
			}
			writeMirrorJSON(w, map[string]any{"id": "item"})
		default:
			http.NotFound(w, r)
		}
	}))

	tracks := []types.Track{
		placeTrack(YouTubeMusicServiceID, "HasID", "vid-direct", ""),
		placeTrack("", "Found Song", "", "", "A"),
		placeTrack("", "Missing Song", "", "", "B"),
		placeTrack("", "SearchError Song", "", "", "C"),
		placeTrack("", "BadShape Song", "", "", "D"),
	}
	res, err := c.PlacePlaylistItems(context.Background(), "", "Backup", tracks)
	if err != nil {
		t.Fatalf("PlacePlaylistItems: %v", err)
	}
	if res.PlaylistID != "yt-pl" || res.URL != "https://www.youtube.com/playlist?list=yt-pl" {
		t.Fatalf("result = %+v", res)
	}
	if res.Copied != 2 || itemsAdded.Load() != 2 {
		t.Fatalf("copied=%d added=%d skips=%+v", res.Copied, itemsAdded.Load(), res.Skips)
	}
	if len(res.Skips) != 3 {
		t.Fatalf("skips = %+v", res.Skips)
	}

	t.Run("missing playlist id", func(t *testing.T) {
		c2 := newYouTubeMirror(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeMirrorJSON(w, map[string]any{"snippet": map[string]any{"title": "x"}})
		}))
		_, err := c2.PlacePlaylistItems(context.Background(), "", "x", nil)
		if err == nil || !strings.Contains(err.Error(), "no id") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("playlistItems fails", func(t *testing.T) {
		c2 := newYouTubeMirror(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/playlists":
				writeMirrorJSON(w, map[string]any{"id": "pl"})
			case "/playlistItems":
				http.Error(w, "no", http.StatusInternalServerError)
			default:
				http.NotFound(w, r)
			}
		}))
		res, err := c2.PlacePlaylistItems(context.Background(), "", "x", []types.Track{
			placeTrack(YouTubeMusicServiceID, "T", "v1", ""),
		})
		if err == nil {
			t.Fatal("expected error")
		}
		if res.Copied != 0 {
			t.Fatalf("copied = %d", res.Copied)
		}
	})
}

func TestYouTubePlacePlaylistItems_CreatesThenAppends(t *testing.T) {
	var mu sync.Mutex
	var created, itemPosts int
	c := newYouTubeMirror(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/playlists":
			created++
			json.NewEncoder(w).Encode(map[string]any{"id": "PLy"})
		case r.Method == http.MethodPost && r.URL.Path == "/playlistItems":
			itemPosts++
			json.NewEncoder(w).Encode(map[string]any{"id": "item"})
		case r.Method == http.MethodPost && r.URL.Path == "/search":
			json.NewEncoder(w).Encode(map[string]any{
				"items": []any{map[string]any{"id": map[string]any{"videoId": "vid"}}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	res, err := c.PlacePlaylistItems(context.Background(), "", "Backup",
		[]types.Track{placeTrack(YouTubeMusicServiceID, "A", "v1", "")})
	if err != nil {
		t.Fatalf("PlacePlaylistItems(create): %v", err)
	}
	if res.PlaylistID != "PLy" || res.URL == "" || res.Copied != 1 {
		t.Fatalf("create = %+v", res)
	}
	if created != 1 || itemPosts != 1 {
		t.Fatalf("created=%d items=%d, want 1/1", created, itemPosts)
	}

	res2, err := c.PlacePlaylistItems(context.Background(), "PLy", "Backup",
		[]types.Track{placeTrack(YouTubeMusicServiceID, "B", "v2", "")})
	if err != nil || res2.Copied != 1 {
		t.Fatalf("append = (%+v,%v)", res2, err)
	}
	if created != 1 || itemPosts != 2 {
		t.Fatalf("append must not create: created=%d items=%d", created, itemPosts)
	}
}

func TestYouTubeSaveLikedTracks(t *testing.T) {
	var rates atomic.Int32
	c := newYouTubeMirror(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/search":
			if strings.Contains(r.URL.Query().Get("q"), "Found") {
				writeMirrorJSON(w, map[string]any{
					"items": []any{map[string]any{"id": map[string]any{"videoId": "vid-found"}}},
				})
				return
			}
			writeMirrorJSON(w, map[string]any{"items": []any{}})
		case r.Method == http.MethodPost && r.URL.Path == "/videos/rate":
			rates.Add(1)
			if r.URL.Query().Get("rating") != "like" {
				t.Errorf("rating = %q", r.URL.Query().Get("rating"))
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))

	res, err := c.SaveLikedTracks(context.Background(), []types.Track{
		placeTrack(YouTubeMusicServiceID, "HasID", "vid1", ""),
		placeTrack("", "Found Song", "", "", "A"),
		placeTrack("", "Missing Song", "", "", "B"),
	})
	if err != nil {
		t.Fatalf("SaveLikedTracks: %v", err)
	}
	if res.Copied != 2 || rates.Load() != 2 {
		t.Fatalf("copied=%d rates=%d skips=%+v", res.Copied, rates.Load(), res.Skips)
	}
	if len(res.Skips) != 1 || !strings.Contains(res.Skips[0].Reason, "no match on YouTube Music") {
		t.Fatalf("skips = %+v", res.Skips)
	}
}

func TestYouTubeSaveLikedTracks_RateError(t *testing.T) {
	c := newYouTubeMirror(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusForbidden)
	}))
	_, err := c.SaveLikedTracks(context.Background(), []types.Track{
		placeTrack(YouTubeMusicServiceID, "T", "v1", ""),
	})
	if err == nil {
		t.Fatal("expected rate error")
	}
}

func TestYouTubePlaylistMeta(t *testing.T) {
	c := newYouTubeMirror(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		switch id {
		case "PLx":
			writeMirrorJSON(w, map[string]any{
				"items": []any{map[string]any{
					"snippet":        map[string]any{"title": "My list"},
					"contentDetails": map[string]any{"itemCount": 4},
				}},
			})
		case "LL":
			writeMirrorJSON(w, map[string]any{
				"items": []any{map[string]any{"contentDetails": map[string]any{"itemCount": 7}}},
			})
		default:
			writeMirrorJSON(w, map[string]any{"items": []any{}})
		}
	}))

	meta, ok, err := c.GetPlaylistMeta(context.Background(), "PLx")
	if err != nil || !ok {
		t.Fatalf("GetPlaylistMeta = (%+v,%v,%v)", meta, ok, err)
	}
	if meta.Name != "My list" || meta.ItemCount != 4 || meta.VersionToken != "" {
		t.Fatalf("meta = %+v", meta)
	}

	if _, ok, err := c.GetPlaylistMeta(context.Background(), "gone"); err != nil || ok {
		t.Fatalf("missing playlist = (ok=%v err=%v), want (false,nil)", ok, err)
	}

	n, err := c.SavedTrackCount(context.Background())
	if err != nil || n != 7 {
		t.Fatalf("SavedTrackCount = %d, %v", n, err)
	}
}
