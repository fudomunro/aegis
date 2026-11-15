package connectors

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/fudomunro/aegis/internal/kinds"
)

// --- paged reads ---

func TestAppleMusicListPlaylistItems_PagesAndEnrichesISRC(t *testing.T) {
	var mu sync.Mutex
	var catalogQueries []string
	c, _ := newAppleMusicTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/me/library/playlists/p.1" && r.Method == http.MethodGet && r.URL.Query().Get("limit") == "":
			writeMirrorJSON(w, map[string]any{"data": []any{libraryPlaylistResource("p.1", "Road Trip", "Long drive")}})
		case r.URL.Path == "/me/library/playlists/p.1/tracks" && r.URL.Query().Get("offset") != "1":
			writeMirrorJSON(w, map[string]any{
				"data": []any{librarySongResource("a.1", "Entropy", "Beach Bunny", 221000, "1613600188")},
				"next": "/v1/me/library/playlists/p.1/tracks?offset=1",
				"meta": map[string]any{"total": 2},
			})
		case r.URL.Path == "/me/library/playlists/p.1/tracks":
			writeMirrorJSON(w, map[string]any{
				"data": []any{librarySongResource("a.2", "Nocturne", "Chad Lawson", 362353, "472229257")},
				"meta": map[string]any{"total": 2},
			})
		case r.URL.Path == "/me/storefront":
			writeMirrorJSON(w, map[string]any{"data": []any{map[string]any{"id": "us", "type": "storefronts"}}})
		case strings.HasPrefix(r.URL.Path, "/catalog/us/songs"):
			mu.Lock()
			catalogQueries = append(catalogQueries, r.URL.RawQuery)
			mu.Unlock()
			ids := strings.Split(r.URL.Query().Get("ids"), ",")
			data := []any{}
			for _, id := range ids {
				if id == "" {
					continue
				}
				data = append(data, map[string]any{
					"id": id, "type": "songs",
					"attributes": map[string]any{"isrc": "ISRC" + id, "durationInMillis": 221000},
				})
			}
			writeMirrorJSON(w, map[string]any{"data": data})
		default:
			http.NotFound(w, r)
		}
	}))

	ref, page, err := c.ListPlaylistItems(t.Context(), "p.1", "", 1)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if ref.Name != "Road Trip" || ref.Description != "Long drive" {
		t.Fatalf("ref = %+v", ref)
	}
	if len(page.Tracks) != 1 || page.Next != "1" || page.Total != 2 {
		t.Fatalf("page 1 = %+v", page)
	}
	if page.Tracks[0].EntityID != "ISRC1613600188" {
		t.Fatalf("ISRC not enriched: %+v", page.Tracks[0])
	}

	_, page2, err := c.ListPlaylistItems(t.Context(), "p.1", "1", 1)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(page2.Tracks) != 1 || page2.Next != "" {
		t.Fatalf("page 2 = %+v", page2)
	}
	if page2.Tracks[0].EntityID != "ISRC472229257" {
		t.Fatalf("page 2 ISRC = %q", page2.Tracks[0].EntityID)
	}

	_, _, err = c.ListPlaylistItems(t.Context(), "p.1", "bogus", 1)
	if err == nil {
		t.Fatal("want cursor error")
	}
}

func TestAppleMusicListUserLikedTracksPage(t *testing.T) {
	c, _ := newAppleMusicTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/me/library/songs":
			writeMirrorJSON(w, map[string]any{
				"data": []any{librarySongResource("a.1", "Entropy", "Beach Bunny", 221000, "")},
				"meta": map[string]any{"total": 1},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	page, err := c.ListUserLikedTracksPage(t.Context(), "", 10)
	if err != nil {
		t.Fatalf("ListUserLikedTracksPage: %v", err)
	}
	if len(page.Tracks) != 1 || page.Total != 1 {
		t.Fatalf("page = %+v", page)
	}
}

func TestAppleMusicPlaylistMetaAndSavedTrackCount(t *testing.T) {
	c, _ := newAppleMusicTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/me/library/playlists/p.1":
			res := libraryPlaylistResource("p.1", "Road Trip", "")
			res["attributes"].(map[string]any)["playParams"].(map[string]any)["versionHash"] = "abc123"
			writeMirrorJSON(w, map[string]any{"data": []any{res}})
		case r.URL.Path == "/me/library/playlists/p.1/tracks":
			writeMirrorJSON(w, map[string]any{"data": []any{}, "meta": map[string]any{"total": 7}})
		case r.URL.Path == "/me/library/playlists/missing":
			w.WriteHeader(http.StatusNotFound)
			writeMirrorJSON(w, map[string]any{"errors": []any{map[string]any{"status": "404"}}})
		case r.URL.Path == "/me/library/songs":
			writeMirrorJSON(w, map[string]any{"data": []any{}, "meta": map[string]any{"total": 42}})
		default:
			http.NotFound(w, r)
		}
	}))
	meta, ok, err := c.GetPlaylistMeta(t.Context(), "p.1")
	if err != nil || !ok {
		t.Fatalf("GetPlaylistMeta: %v ok=%v", err, ok)
	}
	if meta.Name != "Road Trip" || meta.ItemCount != 7 || meta.VersionToken != "abc123" {
		t.Fatalf("meta = %+v", meta)
	}
	if _, ok, err := c.GetPlaylistMeta(t.Context(), "missing"); err != nil || ok {
		t.Fatalf("missing playlist: err=%v ok=%v", err, ok)
	}
	n, err := c.SavedTrackCount(t.Context())
	if err != nil || n != 42 {
		t.Fatalf("SavedTrackCount = %d %v", n, err)
	}
}

// --- placement ladder ---

// ladderRecorder captures catalog queries so tests can assert which rung of
// the match ladder ran.
type ladderRecorder struct {
	mu       sync.Mutex
	queries  []string
	isrcHit  map[string]catalogSong
	nameHit  map[string]catalogSong
	upstream http.Handler
}

func (l *ladderRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/me/storefront" {
		writeMirrorJSON(w, map[string]any{"data": []any{map[string]any{"id": "us"}}})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/catalog/us/songs") {
		l.mu.Lock()
		l.queries = append(l.queries, r.URL.RawQuery)
		l.mu.Unlock()
		q := r.URL.Query()
		if isrc := q.Get("filter[isrc]"); isrc != "" {
			if hit, ok := l.isrcHit[isrc]; ok {
				writeMirrorJSON(w, map[string]any{"data": []any{songResourceFrom(hit)}})
				return
			}
			writeMirrorJSON(w, map[string]any{"data": []any{}})
			return
		}
		if term := q.Get("term"); term != "" {
			if hit, ok := l.nameHit[term]; ok {
				writeMirrorJSON(w, map[string]any{"data": []any{songResourceFrom(hit)}})
				return
			}
			writeMirrorJSON(w, map[string]any{"data": []any{}})
			return
		}
		if ids := q.Get("ids"); ids != "" {
			data := []any{}
			for _, id := range strings.Split(ids, ",") {
				if hit, ok := l.isrcHit["id:"+id]; ok {
					data = append(data, songResourceFrom(hit))
				}
			}
			writeMirrorJSON(w, map[string]any{"data": data})
			return
		}
	}
	if l.upstream != nil {
		l.upstream.ServeHTTP(w, r)
		return
	}
	// Default write verbs so placement tests need no upstream of their own.
	if r.Method == http.MethodPost && r.URL.Path == "/me/library/playlists" {
		writeMirrorJSON(w, map[string]any{"data": []any{libraryPlaylistResource("p.created", "Backup", "")}})
		return
	}
	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/tracks") {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/me/library" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeMirrorJSON(w, map[string]any{"data": []any{}})
}

func songResourceFrom(s catalogSong) map[string]any {
	attrs := map[string]any{
		"name":             s.Name,
		"durationInMillis": s.DurationMs,
	}
	if s.ISRC != "" {
		attrs["isrc"] = s.ISRC
	}
	return map[string]any{"id": s.ID, "type": "songs", "attributes": attrs}
}

func isrcTrack(name, isrc string, durationMs int, artists ...string) kinds.Track {
	t := kinds.Track{Name: name, DurationMs: durationMs, EntityID: isrc}
	for _, a := range artists {
		t.Artists = append(t.Artists, kinds.Artist{Name: a})
	}
	return t
}

func TestAppleMusicPlacePlaylistItems_ISRCFirst(t *testing.T) {
	rec := &ladderRecorder{
		isrcHit: map[string]catalogSong{
			"USQE92100257": {ID: "1613600188", Name: "Entropy", DurationMs: 221000, ISRC: "USQE92100257"},
		},
		nameHit: map[string]catalogSong{},
	}
	c, _ := newAppleMusicTest(t, rec)

	track := isrcTrack("Entropy", "USQE92100257", 221000, "Beach Bunny")
	res, err := c.PlacePlaylistItems(t.Context(), "", "Backup", []kinds.Track{track})
	if err != nil {
		t.Fatalf("PlacePlaylistItems: %v", err)
	}
	if res.PlaylistID == "" || res.Copied != 1 {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Placed) != 1 || res.Placed[0].MatchedBy != kinds.TrackMatchISRC {
		t.Fatalf("placed = %+v", res.Placed)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.queries) == 0 || !strings.Contains(rec.queries[0], "filter%5Bisrc%5D=USQE92100257") {
		t.Fatalf("queries = %v, want ISRC filter first", rec.queries)
	}
}

func TestAppleMusicPlacePlaylistItems_NameFallbackDurationGuard(t *testing.T) {
	rec := &ladderRecorder{
		nameHit: map[string]catalogSong{
			// Far longer than the source track: must NOT be placed.
			"Entropy Beach Bunny": {ID: "9", Name: "Entropy", DurationMs: 600000},
		},
	}
	c, _ := newAppleMusicTest(t, rec)

	track := kinds.Track{Name: "Entropy", DurationMs: 221000, Artists: []kinds.Artist{{Name: "Beach Bunny"}}}
	res, err := c.PlacePlaylistItems(t.Context(), "", "Backup", []kinds.Track{track})
	if err != nil {
		t.Fatalf("PlacePlaylistItems: %v", err)
	}
	if res.Copied != 0 || len(res.Skips) != 1 {
		t.Fatalf("result = %+v, want one duration-guarded skip", res)
	}
	if !strings.Contains(res.Skips[0].Reason, "no match") {
		t.Fatalf("skip = %+v", res.Skips[0])
	}

	// A compatible name hit places with MatchedBy=name.
	rec.nameHit["Entropy Beach Bunny"] = catalogSong{ID: "9", Name: "Entropy", DurationMs: 223000}
	res, err = c.PlacePlaylistItems(t.Context(), "", "Backup", []kinds.Track{track})
	if err != nil || res.Copied != 1 {
		t.Fatalf("result = %+v err=%v", res, err)
	}
	if res.Placed[0].MatchedBy != kinds.TrackMatchName {
		t.Fatalf("placed = %+v", res.Placed)
	}
}

func TestAppleMusicPlacePlaylistItems_LocalRefSkipsSearch(t *testing.T) {
	rec := &ladderRecorder{}
	c, _ := newAppleMusicTest(t, rec)
	track := kinds.Track{
		Name: "Entropy",
		Sources: []kinds.TrackSource{{
			Service: AppleMusicServiceID, ID: "a.1",
			Extra: map[string]any{"catalog_id": "1613600188"},
		}},
	}
	res, err := c.PlacePlaylistItems(t.Context(), "", "Backup", []kinds.Track{track})
	if err != nil || res.Copied != 1 {
		t.Fatalf("result = %+v err=%v", res, err)
	}
	if res.Placed[0].MatchedBy != "ref" {
		t.Fatalf("placed = %+v", res.Placed)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.queries) != 0 {
		t.Fatalf("local ref must not search the catalog: %v", rec.queries)
	}
}

func TestAppleMusicPlacePlaylistItems_CreateThenAppend(t *testing.T) {
	var mu sync.Mutex
	var posts []string
	var createdBody, addBody map[string]any
	rec := &ladderRecorder{
		isrcHit: map[string]catalogSong{
			"USQE92100257": {ID: "1613600188", Name: "Entropy", DurationMs: 221000},
		},
		upstream: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/me/library/playlists":
				posts = append(posts, "create")
				_ = json.NewDecoder(r.Body).Decode(&createdBody)
				writeMirrorJSON(w, map[string]any{"data": []any{libraryPlaylistResource("p.new", "Backup", "")}})
			case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/tracks"):
				posts = append(posts, "add:"+r.URL.Path)
				_ = json.NewDecoder(r.Body).Decode(&addBody)
				w.WriteHeader(http.StatusNoContent)
			case r.Method == http.MethodPatch:
				posts = append(posts, "rename")
				w.WriteHeader(http.StatusNoContent)
			default:
				writeMirrorJSON(w, map[string]any{"data": []any{}})
			}
		}),
	}
	c, _ := newAppleMusicTest(t, rec)
	track := isrcTrack("Entropy", "USQE92100257", 221000)

	res, err := c.PlacePlaylistItems(t.Context(), "", "Backup", []kinds.Track{track})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if res.PlaylistID != "p.new" {
		t.Fatalf("created id = %q", res.PlaylistID)
	}
	if createdBody["attributes"].(map[string]any)["name"] != "Backup" {
		t.Fatalf("create body = %+v", createdBody)
	}

	res, err = c.PlacePlaylistItems(t.Context(), "p.new", "Renamed", []kinds.Track{track})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if res.PlaylistID != "p.new" || res.Copied != 1 {
		t.Fatalf("append result = %+v", res)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(posts, ",") != "create,add:/me/library/playlists/p.new/tracks,rename" {
		t.Fatalf("posts = %v", posts)
	}
}

func TestAppleMusicSaveLikedTracks_AddsToLibrary(t *testing.T) {
	var savedQuery string
	rec := &ladderRecorder{
		isrcHit: map[string]catalogSong{
			"USQE92100257": {ID: "1613600188", Name: "Entropy", DurationMs: 221000},
		},
		upstream: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/me/library" {
				savedQuery = r.URL.RawQuery
				w.WriteHeader(http.StatusAccepted)
				return
			}
			writeMirrorJSON(w, map[string]any{"data": []any{}})
		}),
	}
	c, _ := newAppleMusicTest(t, rec)
	res, err := c.SaveLikedTracks(t.Context(), []kinds.Track{
		isrcTrack("Entropy", "USQE92100257", 221000),
		{Name: "Totally Unknown Song Nobody Recorded", DurationMs: 1000},
	})
	if err != nil {
		t.Fatalf("SaveLikedTracks: %v", err)
	}
	if res.Copied != 1 || len(res.Skips) != 1 {
		t.Fatalf("result = %+v", res)
	}
	if savedQuery != "ids%5Bsongs%5D=1613600188" {
		t.Fatalf("library query = %q", savedQuery)
	}
	if res.Placed[0].MatchedBy != kinds.TrackMatchISRC {
		t.Fatalf("placed = %+v", res.Placed)
	}
}

func TestAppleMusicSaveAlbums_UPCFirst(t *testing.T) {
	var mu sync.Mutex
	var queries []string
	rec := &ladderRecorder{
		upstream: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			if strings.HasPrefix(r.URL.Path, "/catalog/us/albums") {
				queries = append(queries, r.URL.RawQuery)
				if r.URL.Query().Get("filter[upc]") != "" {
					writeMirrorJSON(w, map[string]any{"data": []any{
						map[string]any{"id": "1613600183", "type": "albums", "attributes": map[string]any{"name": "Emotional Creature"}},
					}})
					return
				}
				writeMirrorJSON(w, map[string]any{"data": []any{}})
				return
			}
			if r.Method == http.MethodPost && r.URL.Path == "/me/library" {
				w.WriteHeader(http.StatusAccepted)
				return
			}
			writeMirrorJSON(w, map[string]any{"data": []any{}})
		}),
	}
	c, _ := newAppleMusicTest(t, rec)
	album := kinds.Album{Name: "Emotional Creature", EntityID: "00886448619161"}
	album.Artists = []kinds.Artist{{Name: "Beach Bunny"}}
	res, err := c.SaveAlbums(t.Context(), []kinds.Album{album})
	if err != nil || res.Copied != 1 {
		t.Fatalf("result = %+v err=%v", res, err)
	}
	if res.Placed[0].MatchedBy != kinds.AlbumMatchUPC {
		t.Fatalf("placed = %+v", res.Placed)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(queries) == 0 || !strings.Contains(queries[0], "filter%5Bupc%5D") {
		t.Fatalf("queries = %v, want UPC filter first", queries)
	}
}

// --- auth failures ---

func TestAppleMusicWrites_MissingCredentialsIsUnauthorized(t *testing.T) {
	th := newTestHostWithService(AppleMusicServiceID, "http://127.0.0.1:0", "https://aegis.example.com")
	c := NewAppleMusicConnector(th)
	if _, _, err := c.ListUserPlaylists(context.Background()); err != kinds.ErrUnauthorized {
		t.Fatalf("ListUserPlaylists err = %v, want ErrUnauthorized", err)
	}
	if _, err := c.PlacePlaylistItems(context.Background(), "", "x", []kinds.Track{{Name: "n"}}); err != kinds.ErrUnauthorized {
		t.Fatalf("PlacePlaylistItems err = %v, want ErrUnauthorized", err)
	}
}
