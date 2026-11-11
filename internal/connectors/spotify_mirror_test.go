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

	"github.com/fudomunro/aegis/internal/kinds"
)

func writeMirrorJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// placeTrack builds a neutral track with an optional service-local ref.
func placeTrack(service, name, id, uri string, artists ...string) kinds.Track {
	t := kinds.Track{Name: name}
	if id != "" || uri != "" {
		src := kinds.TrackSource{Service: service, ID: id}
		if uri != "" {
			src.Extra = map[string]any{"uri": uri}
		}
		t.Sources = []kinds.TrackSource{src}
	}
	for _, a := range artists {
		t.Artists = append(t.Artists, kinds.Artist{Name: a})
	}
	return t
}

func sourceID(t kinds.Track) string {
	if len(t.Sources) == 0 {
		return ""
	}
	return t.Sources[0].ID
}

func newSpotifyMirror(t *testing.T, up http.Handler) *SpotifyConnector {
	t.Helper()
	ts := httptest.NewServer(up)
	t.Cleanup(ts.Close)
	th := newTestHostWithService(SpotifyServiceID, ts.URL, "https://aegis.example.com")
	th.setToken(SpotifyServiceID, "tok")
	return NewSpotifyConnector(th)
}

func TestSpotify_ListPlaylistItems(t *testing.T) {
	c := newSpotifyMirror(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/playlists/p1":
			writeMirrorJSON(w, map[string]any{
				"id": "p1", "name": "Jazz",
				"external_urls": map[string]any{"spotify": "https://open.spotify.com/playlist/p1"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/playlists/p1/tracks":
			writeMirrorJSON(w, map[string]any{
				"items": []any{
					map[string]any{"track": map[string]any{
						"id": "t1", "name": "So What", "uri": "spotify:track:t1",
						"artists":     []any{map[string]any{"name": "Miles Davis"}},
						"album":       map[string]any{"name": "Kind of Blue"},
						"duration_ms": 562000,
					}},
				},
				"total": 1,
				"next":  nil,
			})
		default:
			http.NotFound(w, r)
		}
	}))

	ref, page, err := c.ListPlaylistItems(context.Background(), "p1", "", 100)
	if err != nil {
		t.Fatalf("ListPlaylistItems: %v", err)
	}
	if ref.Name != "Jazz" || ref.TrackCount != 1 {
		t.Fatalf("ref = %+v", ref)
	}
	if len(ref.Sources) != 1 || ref.Sources[0].ID != "p1" || ref.Sources[0].URL == "" {
		t.Fatalf("ref sources = %+v", ref.Sources)
	}
	if page.Next != "" || len(page.Tracks) != 1 {
		t.Fatalf("page = %+v", page)
	}
	if sourceID(page.Tracks[0]) != "t1" || page.Tracks[0].DurationMs != 562000 {
		t.Fatalf("track = %+v", page.Tracks[0])
	}
}

func TestSpotify_ListPlaylistItems_Pages(t *testing.T) {
	var gotPaths []string
	c := newSpotifyMirror(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path+"?"+r.URL.RawQuery)
		switch {
		case strings.HasPrefix(r.URL.Path, "/playlists/PL1/tracks"):
			json.NewEncoder(w).Encode(map[string]any{
				"total": 3,
				"items": []any{
					map[string]any{"track": map[string]any{"id": "t1", "name": "One", "uri": "spotify:track:t1"}},
					map[string]any{"track": map[string]any{"id": "t2", "name": "Two", "uri": "spotify:track:t2"}},
				},
			})
		case strings.HasPrefix(r.URL.Path, "/playlists/PL1"):
			json.NewEncoder(w).Encode(map[string]any{
				"name": "Road trip", "external_urls": map[string]any{"spotify": "u"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	ref, page, err := c.ListPlaylistItems(context.Background(), "PL1", "", 2)
	if err != nil {
		t.Fatalf("ListPlaylistItems(first): %v", err)
	}
	if ref.Name != "Road trip" || page.Total != 3 || len(page.Tracks) != 2 {
		t.Fatalf("first page = (%+v, %+v)", ref, page)
	}
	if page.Next != "2" {
		t.Fatalf("next = %q, want offset 2", page.Next)
	}

	// Second page must not refetch the playlist object.
	before := len(gotPaths)
	_, page2, err := c.ListPlaylistItems(context.Background(), "PL1", "2", 2)
	if err != nil {
		t.Fatalf("ListPlaylistItems(second): %v", err)
	}
	if page2.Total != 3 || len(page2.Tracks) != 2 {
		t.Fatalf("second page = %+v", page2)
	}
	// The mock returns the same 2 items regardless of offset, but next is
	// computed from offset+len < total, so offset 2 + 2 > 3 must stop.
	if page2.Next != "" {
		t.Fatalf("next2 = %q, want empty (past the end)", page2.Next)
	}
	if len(gotPaths) <= before {
		t.Fatal("second page must issue its own request")
	}
	for _, p := range gotPaths[before:] {
		if !strings.Contains(p, "/tracks") {
			t.Fatalf("second page refetched %q, want only the tracks page", p)
		}
	}

	if _, _, err := c.ListPlaylistItems(context.Background(), "PL1", "bogus", 1); err == nil {
		t.Fatal("a non-numeric cursor must be rejected")
	}
}

func TestSpotify_ListUserLikedTracksPage_Pages(t *testing.T) {
	var likedPages atomic.Int32
	c := newSpotifyMirror(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/me/tracks" {
			likedPages.Add(1)
			if r.URL.Query().Get("limit") != "50" {
				t.Errorf("liked tracks limit = %q", r.URL.Query().Get("limit"))
			}
			if likedPages.Load() == 1 {
				writeMirrorJSON(w, map[string]any{
					"items": []any{
						map[string]any{"track": map[string]any{
							"id": "L1", "name": "Liked One", "uri": "spotify:track:L1",
							"artists": []any{map[string]any{"name": "Artist One"}},
						}},
					},
					"next":  "https://api.spotify.com/v1/me/tracks?offset=1&limit=50",
					"total": 2,
				})
				return
			}
			writeMirrorJSON(w, map[string]any{
				"items": []any{
					map[string]any{"track": map[string]any{
						"id": "L2", "name": "Liked Two", "uri": "spotify:track:L2",
						"artists": []any{map[string]any{"name": "Artist Two"}},
					}},
				},
				"next":  nil,
				"total": 2,
			})
			return
		}
		http.NotFound(w, r)
	}))

	page, err := c.ListUserLikedTracksPage(context.Background(), "", 50)
	if err != nil {
		t.Fatalf("ListUserLikedTracksPage: %v", err)
	}
	if page.Total != 2 || len(page.Tracks) != 1 || page.Next != "1" {
		t.Fatalf("liked = %+v", page)
	}
	if sourceID(page.Tracks[0]) != "L1" {
		t.Fatalf("track = %+v", page.Tracks[0])
	}

	page2, err := c.ListUserLikedTracksPage(context.Background(), "1", 50)
	if err != nil {
		t.Fatalf("ListUserLikedTracksPage(page2): %v", err)
	}
	if page2.Next != "" || len(page2.Tracks) != 1 || sourceID(page2.Tracks[0]) != "L2" {
		t.Fatalf("liked2 = %+v", page2)
	}

	if _, err := c.ListUserLikedTracksPage(context.Background(), "bogus", 1); err == nil {
		t.Fatal("a non-numeric liked cursor must be rejected")
	}
}

// TestSpotify_ListUserLikedTracksPage_Repro replays the issue #240 repro
// fixture: the saved-tracks page arrives with the items array renamed to
// entries while total still claims a library. Expected: three tracks. The
// parse reads items[] only and goes silently empty — the shape drift the
// canary flagged.
func TestSpotify_ListUserLikedTracksPage_Repro(t *testing.T) {
	fx, err := LoadReproFixture(
		"testdata/repro/spotify/me-tracks-items-renamed/fixture.json")
	if err != nil {
		t.Fatalf("LoadReproFixture: %v", err)
	}
	up := NewReplayServer(fx)
	t.Cleanup(up.Close)

	th := newTestHostWithService(SpotifyServiceID, up.URL+"/v1", "https://aegis.example.com")
	th.setToken(SpotifyServiceID, "tok")
	c := NewSpotifyConnector(th)

	page, err := c.ListUserLikedTracksPage(context.Background(), "", 50)
	if err != nil {
		t.Fatalf("ListUserLikedTracksPage: %v", err)
	}
	if page.Total != 3 {
		t.Fatalf("total = %d, want 3", page.Total)
	}
	if len(page.Tracks) != 3 {
		t.Fatalf("tracks = %d (%+v), want 3 (renamed items array must be read)", len(page.Tracks), page.Tracks)
	}
	if page.Tracks[0].Name != "Fixture Track One" || page.Tracks[2].Name != "Fixture Track Three" {
		t.Fatalf("tracks = %+v", page.Tracks)
	}
}

func TestSpotifyPlacePlaylistItems_CreateSearchFallbackAndSkips(t *testing.T) {
	var addCalls atomic.Int32
	var addURIs [][]string
	c := newSpotifyMirror(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/me":
			writeMirrorJSON(w, map[string]any{"id": "user-1"})
		case r.Method == http.MethodPost && r.URL.Path == "/users/user-1/playlists":
			writeMirrorJSON(w, map[string]any{
				"id":            "pl-created",
				"external_urls": map[string]any{"spotify": "https://open.spotify.com/playlist/pl-created"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/search":
			if strings.Contains(q.Get("q"), "Found Song") {
				writeMirrorJSON(w, map[string]any{
					"tracks": map[string]any{
						"items": []any{map[string]any{"uri": "spotify:track:found1"}},
					},
				})
				return
			}
			if strings.Contains(q.Get("q"), "Search Error") {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			writeMirrorJSON(w, map[string]any{
				"tracks": map[string]any{"items": []any{}},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/playlists/pl-created/tracks":
			addCalls.Add(1)
			var body struct {
				URIs []string `json:"uris"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			addURIs = append(addURIs, body.URIs)
			writeMirrorJSON(w, map[string]any{"snapshot_id": "snap"})
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))

	tracks := []kinds.Track{
		placeTrack(SpotifyServiceID, "Direct", "", "spotify:track:direct"),
		placeTrack(SpotifyServiceID, "FromID", "from-id", ""),
		placeTrack("", "Found Song", "", "", "Finder"),
		placeTrack("", "Missing Song", "", "", "Nobody"),
		placeTrack("", "Search Error", "", "", "Broken"),
	}
	res, err := c.PlacePlaylistItems(context.Background(), "", "Backup", tracks)
	if err != nil {
		t.Fatalf("PlacePlaylistItems: %v", err)
	}
	if res.PlaylistID != "pl-created" || res.URL == "" {
		t.Fatalf("result = %+v", res)
	}
	if res.Copied != 3 {
		t.Fatalf("copied = %d skips=%+v", res.Copied, res.Skips)
	}
	if addCalls.Load() != 1 || len(addURIs) != 1 {
		t.Fatalf("addCalls=%d uris=%v", addCalls.Load(), addURIs)
	}
	got := strings.Join(addURIs[0], ",")
	for _, want := range []string{"spotify:track:direct", "spotify:track:from-id", "spotify:track:found1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %v", want, addURIs[0])
		}
	}
	if len(res.Skips) != 2 {
		t.Fatalf("skips = %+v", res.Skips)
	}
	reasons := map[string]string{}
	for _, s := range res.Skips {
		reasons[s.Name] = s.Reason
	}
	if !strings.Contains(reasons["Missing Song — Nobody"], "no match on Spotify") {
		t.Fatalf("missing skip reason: %+v", reasons)
	}
	if reasons["Search Error — Broken"] == "" {
		t.Fatalf("search error skip missing: %+v", reasons)
	}
}

func TestSpotifyPlacePlaylistItems_UpstreamErrors(t *testing.T) {
	t.Run("no user id", func(t *testing.T) {
		c := newSpotifyMirror(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeMirrorJSON(w, map[string]any{})
		}))
		_, err := c.PlacePlaylistItems(context.Background(), "", "x", nil)
		if err == nil || !strings.Contains(err.Error(), "user id") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("create playlist missing id", func(t *testing.T) {
		c := newSpotifyMirror(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/me":
				writeMirrorJSON(w, map[string]any{"id": "u"})
			default:
				writeMirrorJSON(w, map[string]any{"name": "no-id"})
			}
		}))
		_, err := c.PlacePlaylistItems(context.Background(), "", "x", nil)
		if err == nil || !strings.Contains(err.Error(), "no id") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("tracks add fails mid-copy", func(t *testing.T) {
		c := newSpotifyMirror(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/me":
				writeMirrorJSON(w, map[string]any{"id": "u"})
			case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/users/"):
				writeMirrorJSON(w, map[string]any{"id": "pl"})
			case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/tracks"):
				http.Error(w, "nope", http.StatusInternalServerError)
			default:
				http.NotFound(w, r)
			}
		}))
		res, err := c.PlacePlaylistItems(context.Background(), "", "x", []kinds.Track{
			placeTrack(SpotifyServiceID, "T", "", "spotify:track:t"),
		})
		if err == nil {
			t.Fatal("expected add error")
		}
		if res.Copied != 0 {
			t.Fatalf("copied = %d", res.Copied)
		}
	})
}

func TestSpotifyPlacePlaylistItems_CreatesThenAppends(t *testing.T) {
	var mu sync.Mutex
	var created, addBodies, renames []string
	c := newSpotifyMirror(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/me":
			json.NewEncoder(w).Encode(map[string]any{"id": "user1"})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/users/user1/playlists"):
			created = append(created, r.URL.Path)
			json.NewEncoder(w).Encode(map[string]any{
				"id": "PL1", "external_urls": map[string]any{"spotify": "https://open.spotify.com/playlist/PL1"},
			})
		case r.Method == http.MethodPut && r.URL.Path == "/playlists/PL1":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			name, _ := body["name"].(string)
			renames = append(renames, name)
			json.NewEncoder(w).Encode(map[string]any{})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/playlists/PL1/tracks"):
			addBodies = append(addBodies, r.URL.Path)
			json.NewEncoder(w).Encode(map[string]any{"snapshot_id": "snap"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	res, err := c.PlacePlaylistItems(context.Background(), "", "Backup",
		[]kinds.Track{placeTrack(SpotifyServiceID, "A", "", "spotify:track:a")})
	if err != nil {
		t.Fatalf("PlacePlaylistItems(create): %v", err)
	}
	if res.PlaylistID != "PL1" || res.URL == "" || res.Copied != 1 || len(res.Skips) != 0 {
		t.Fatalf("create = %+v", res)
	}
	if len(created) != 1 || len(addBodies) != 1 {
		t.Fatalf("created=%v add=%v, want one of each", created, addBodies)
	}
	if len(renames) != 0 {
		t.Fatalf("create must not issue a title write, renames=%v", renames)
	}

	// A second step with a known playlist id must append, not create — and
	// a non-empty name sets the title (issue #365 rename propagation).
	res2, err := c.PlacePlaylistItems(context.Background(), "PL1", "Backup",
		[]kinds.Track{placeTrack(SpotifyServiceID, "B", "", "spotify:track:b")})
	if err != nil {
		t.Fatalf("PlacePlaylistItems(append): %v", err)
	}
	if res2.PlaylistID != "PL1" || res2.Copied != 1 {
		t.Fatalf("append = %+v", res2)
	}
	if len(created) != 1 {
		t.Fatalf("append must not create another playlist, created=%v", created)
	}
	if len(addBodies) != 2 {
		t.Fatalf("append must post to the same playlist, add=%v", addBodies)
	}
	if len(renames) != 1 || renames[0] != "Backup" {
		t.Fatalf("renames = %v, want one title write of Backup", renames)
	}

	// An empty name on the append path keeps the existing title.
	res3, err := c.PlacePlaylistItems(context.Background(), "PL1", "",
		[]kinds.Track{placeTrack(SpotifyServiceID, "C", "", "spotify:track:c")})
	if err != nil || res3.Copied != 1 {
		t.Fatalf("append(keep title) = (%+v,%v)", res3, err)
	}
	if len(renames) != 1 {
		t.Fatalf("empty name must not rename, renames=%v", renames)
	}
}

func TestSpotifySaveLikedTracks(t *testing.T) {
	var puts atomic.Int32
	var putIDs []string
	c := newSpotifyMirror(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/search":
			if strings.Contains(r.URL.Query().Get("q"), "Found") {
				writeMirrorJSON(w, map[string]any{
					"tracks": map[string]any{
						"items": []any{map[string]any{"uri": "spotify:track:found-liked"}},
					},
				})
				return
			}
			writeMirrorJSON(w, map[string]any{"tracks": map[string]any{"items": []any{}}})
		case r.Method == http.MethodPut && r.URL.Path == "/me/tracks":
			puts.Add(1)
			putIDs = append(putIDs, r.URL.Query().Get("ids"))
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))

	tracks := []kinds.Track{
		placeTrack(SpotifyServiceID, "HasID", "id-1", ""),
		placeTrack(SpotifyServiceID, "HasURI", "", "spotify:track:uri-1"),
		placeTrack("", "Found Song", "", "", "A"),
		placeTrack("", "Missing Song", "", "", "B"),
	}
	res, err := c.SaveLikedTracks(context.Background(), tracks)
	if err != nil {
		t.Fatalf("SaveLikedTracks: %v", err)
	}
	if res.Copied != 3 {
		t.Fatalf("copied = %d skips=%+v", res.Copied, res.Skips)
	}
	if puts.Load() != 1 || len(putIDs) != 1 {
		t.Fatalf("puts=%d ids=%v", puts.Load(), putIDs)
	}
	joined := putIDs[0]
	for _, want := range []string{"id-1", "uri-1", "found-liked"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %q", want, joined)
		}
	}
	if len(res.Skips) != 1 || !strings.Contains(res.Skips[0].Reason, "no match on Spotify") {
		t.Fatalf("skips = %+v", res.Skips)
	}
}

func TestSpotifySaveLikedTracks_PutError(t *testing.T) {
	c := newSpotifyMirror(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/me/tracks" {
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		http.NotFound(w, r)
	}))
	_, err := c.SaveLikedTracks(context.Background(), []kinds.Track{
		placeTrack(SpotifyServiceID, "T", "id", ""),
	})
	if err == nil {
		t.Fatal("expected put error")
	}
}

func TestSpotifySearchTrackURI_EdgeCases(t *testing.T) {
	c := newSpotifyMirror(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		switch {
		case strings.Contains(q, "BadItem"):
			writeMirrorJSON(w, map[string]any{
				"tracks": map[string]any{"items": []any{"not-a-map"}},
			})
		case strings.Contains(q, "EmptyURI"):
			writeMirrorJSON(w, map[string]any{
				"tracks": map[string]any{"items": []any{map[string]any{"id": "x"}}},
			})
		default:
			writeMirrorJSON(w, map[string]any{"tracks": map[string]any{"items": []any{}}})
		}
	}))
	ctx := context.Background()

	uri, err := c.searchTrackURI(ctx, kinds.Track{Name: "Nope", Artists: []kinds.Artist{{Name: "X"}}})
	if err != nil || uri != "" {
		t.Fatalf("empty result = %q %v", uri, err)
	}
	uri, err = c.searchTrackURI(ctx, kinds.Track{Name: "BadItem"})
	if err != nil || uri != "" {
		t.Fatalf("bad item = %q %v", uri, err)
	}
	uri, err = c.searchTrackURI(ctx, kinds.Track{Name: "EmptyURI"})
	if err != nil || uri != "" {
		t.Fatalf("empty uri = %q %v", uri, err)
	}
}

func TestNeutralSpotifyTrack_PartialShapes(t *testing.T) {
	got := NeutralSpotifyTrack(map[string]any{
		"name": "Partial",
		"artists": []any{
			map[string]any{"name": "A"},
			"not-a-map",
			map[string]any{},
		},
		"duration_ms": float64(120000),
	})
	if got.Name != "Partial" || len(got.Artists) != 1 || got.DurationMs != 120000 {
		t.Fatalf("got = %+v", got)
	}
}

func TestSpotifyPlaylistMeta(t *testing.T) {
	c := newSpotifyMirror(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/playlists/pl1":
			writeMirrorJSON(w, map[string]any{
				"name": "Road trip", "snapshot_id": "snap-1",
				"tracks": map[string]any{"total": 12},
			})
		case r.URL.Path == "/playlists/gone":
			http.NotFound(w, r)
		case r.URL.Path == "/me/tracks":
			if r.URL.Query().Get("limit") != "1" {
				t.Errorf("saved count limit = %q", r.URL.Query().Get("limit"))
			}
			writeMirrorJSON(w, map[string]any{"total": 44})
		default:
			http.NotFound(w, r)
		}
	}))

	meta, ok, err := c.GetPlaylistMeta(context.Background(), "pl1")
	if err != nil || !ok {
		t.Fatalf("GetPlaylistMeta = (%+v,%v,%v)", meta, ok, err)
	}
	if meta.Name != "Road trip" || meta.VersionToken != "snap-1" || meta.ItemCount != 12 {
		t.Fatalf("meta = %+v", meta)
	}

	if _, ok, err := c.GetPlaylistMeta(context.Background(), "gone"); err != nil || ok {
		t.Fatalf("missing playlist = (ok=%v err=%v), want (false,nil)", ok, err)
	}

	n, err := c.SavedTrackCount(context.Background())
	if err != nil || n != 44 {
		t.Fatalf("SavedTrackCount = %d, %v", n, err)
	}
}
