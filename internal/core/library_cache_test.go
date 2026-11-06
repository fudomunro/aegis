package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/types"
)

// countingTrackProvider counts ListUserLikedTracks calls and can hold them
// open so parallel requests are forced to overlap (issue #273).
type countingTrackProvider struct {
	tracks []types.Track
	calls  atomic.Int32
	hold   chan struct{} // nil = no hold
}

func (f *countingTrackProvider) Capabilities() types.ServiceCapabilities {
	return types.ServiceCapabilities{}
}
func (f *countingTrackProvider) SystemInstructions(string) string { return "fake" }
func (f *countingTrackProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}

func (f *countingTrackProvider) ListUserLikedTracks(ctx context.Context) ([]types.Track, bool, error) {
	f.calls.Add(1)
	if f.hold != nil {
		select {
		case <-f.hold:
		case <-time.After(10 * time.Second):
			return nil, false, context.DeadlineExceeded
		}
	}
	return f.tracks, false, nil
}

func countingTrackServer(tracks []types.Track, provider *countingTrackProvider) *Server {
	cfg := &Config{Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{"spotify": provider}
	return srv
}

func nTracks(n int) []types.Track {
	tracks := make([]types.Track, n)
	for i := range tracks {
		tracks[i] = types.Track{
			Name:    fmt.Sprintf("T%03d", i),
			Sources: []types.TrackSource{{Service: "spotify", ServiceName: "Spotify", ID: fmt.Sprintf("t%03d", i)}},
		}
	}
	return tracks
}

func getTracksPage(t *testing.T, srv *Server, path string) (types.TrackList, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryTracks)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d body=%s", path, w.Code, w.Body.String())
	}
	var resp types.TrackList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return resp, w
}

// Paging a large library must cost one provider fan-out, not one per page
// (issue #273): the merged aggregate is cached and pages are sliced from it.
func TestLibraryAggregate_PagesShareOneFetch(t *testing.T) {
	provider := &countingTrackProvider{tracks: nTracks(25)}
	srv := countingTrackServer(provider.tracks, provider)

	var seen []string
	path := "/v1/library/tracks?limit=10"
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("cursor loop did not terminate")
		}
		resp, w := getTracksPage(t, srv, path)
		seen = append(seen, resp.Tracks[0].Name)
		if resp.Total != 25 {
			t.Fatalf("total = %d, want 25", resp.Total)
		}
		if resp.NextCursor == "" {
			if len(seen) != 3 {
				t.Fatalf("pages = %d, want 3", len(seen))
			}
			break
		}
		path = "/v1/library/tracks?limit=10&cursor=" + resp.NextCursor
		_ = w
	}
	if got := provider.calls.Load(); got != 1 {
		t.Fatalf("provider fetches across 3 pages = %d, want 1", got)
	}
	// Pages must be windows into one stable list, in order.
	if seen[0] != "T000" || seen[1] != "T010" || seen[2] != "T020" {
		t.Fatalf("page starts = %v", seen)
	}
}

// A repeated identical request must be a pure cache hit — no second fan-out.
func TestLibraryAggregate_RepeatedRequestIsCacheHit(t *testing.T) {
	provider := &countingTrackProvider{tracks: nTracks(3)}
	srv := countingTrackServer(provider.tracks, provider)

	resp, w := getTracksPage(t, srv, "/v1/library/tracks")
	if w.Header().Get("X-Aegis-Cache") != "miss" {
		t.Fatalf("first request cache header = %q", w.Header().Get("X-Aegis-Cache"))
	}
	if resp.Total != 3 {
		t.Fatalf("total = %d", resp.Total)
	}
	_, w2 := getTracksPage(t, srv, "/v1/library/tracks")
	if w2.Header().Get("X-Aegis-Cache") != "hit" {
		t.Fatalf("second request cache header = %q", w2.Header().Get("X-Aegis-Cache"))
	}
	if got := provider.calls.Load(); got != 1 {
		t.Fatalf("provider fetches = %d, want 1", got)
	}
}

// Parallel identical requests (the common agent tool-call pattern) share one
// coalesced provider fan-out (issue #273).
func TestLibraryAggregate_SingleflightParallel(t *testing.T) {
	hold := make(chan struct{})
	provider := &countingTrackProvider{tracks: nTracks(5), hold: hold}
	srv := countingTrackServer(provider.tracks, provider)

	const parallel = 8
	var wg sync.WaitGroup
	errs := make(chan error, parallel)
	for i := 0; i < parallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/v1/library/tracks", nil)
			req.Header.Set("Authorization", "Bearer test-key")
			w := httptest.NewRecorder()
			srv.AuthMiddleware(srv.HandleLibraryTracks)(w, req)
			if w.Code != http.StatusOK {
				errs <- fmt.Errorf("status = %d body=%s", w.Code, w.Body.String())
			}
		}()
	}
	// Let every request reach the coalesced fetch, then release the provider.
	time.Sleep(50 * time.Millisecond)
	close(hold)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := provider.calls.Load(); got != 1 {
		t.Fatalf("provider fetches for %d parallel identical requests = %d, want 1", parallel, got)
	}
}

func TestInvalidateLibrary_SparesMBIDAndTransport(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.cache.Set("library:tracks|agg", "a", time.Minute)
	srv.cache.Set("library:tracks|q=x|page:10:0", "b", time.Minute)
	srv.cache.Set("spotify:/me", "keep", time.Minute)
	srv.mbCache.Set("mbid:artist:a", "keep", time.Minute)

	srv.invalidateLibrary()

	if _, ok := srv.cache.Get("library:tracks|agg"); ok {
		t.Fatal("aggregate must not survive library invalidation")
	}
	if _, ok := srv.cache.Get("library:tracks|q=x|page:10:0"); ok {
		t.Fatal("search view must not survive library invalidation")
	}
	if v, ok := srv.cache.Get("spotify:/me"); !ok || v != "keep" {
		t.Fatal("transport cache must survive library invalidation")
	}
	if v, ok := srv.mbCache.Get("mbid:artist:a"); !ok || v != "keep" {
		t.Fatal("MBID lookups must survive library invalidation")
	}
}

func TestInvalidateService_ClearsServiceAndLibrary(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.cache.Set("library:tracks|agg", "a", time.Minute)
	srv.cache.Set("spotify:/me", "b", time.Minute)
	srv.cache.Set("tidal:/me", "keep", time.Minute)
	srv.mbCache.Set("mbid:rec:x", "keep", time.Minute)

	srv.invalidateService("spotify")

	for _, key := range []string{"library:tracks|agg", "spotify:/me"} {
		if _, ok := srv.cache.Get(key); ok {
			t.Fatalf("%s must not survive invalidateService", key)
		}
	}
	if v, ok := srv.cache.Get("tidal:/me"); !ok || v != "keep" {
		t.Fatal("other services must survive")
	}
	if v, ok := srv.mbCache.Get("mbid:rec:x"); !ok || v != "keep" {
		t.Fatal("MBID lookups must survive")
	}
}

func TestInvalidateLibraryKind_ScopedToOneKind(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.cache.Set("library:tracks|agg", "a", time.Minute)
	srv.cache.Set("library:tracks|q=hi", "b", time.Minute)
	srv.cache.Set("library:albums|agg", "keep", time.Minute)

	srv.invalidateLibraryKind("tracks")

	for _, key := range []string{"library:tracks|agg", "library:tracks|q=hi"} {
		if _, ok := srv.cache.Get(key); ok {
			t.Fatalf("%s must not survive kind invalidation", key)
		}
	}
	if v, ok := srv.cache.Get("library:albums|agg"); !ok || v != "keep" {
		t.Fatal("other kinds must survive")
	}
}

func TestInvalidateAllCaches_FlushesMBID(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.cache.Set("library:tracks|agg", "a", time.Minute)
	srv.mbCache.Set("mbid:rg:x", "b", time.Minute)

	srv.invalidateAllCaches()

	if _, ok := srv.cache.Get("library:tracks|agg"); ok {
		t.Fatal("response cache must be empty")
	}
	if _, ok := srv.mbCache.Get("mbid:rg:x"); ok {
		t.Fatal("admin flush-all must drop MBID lookups too")
	}
}

func TestHandleAdminCacheClear_FlushesBothCaches(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.cache.Set("library:tracks|agg", "a", time.Minute)
	srv.mbCache.Set("mbid:artist:a", "b", time.Minute)

	req := httptest.NewRequest("POST", "/v1/admin/cache/clear", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	dispatchAdmin(srv, w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if live, total, _ := srv.cacheStats(); live != 0 || total != 0 {
		t.Fatalf("after admin clear live=%d total=%d, want 0/0", live, total)
	}
}

// ClearPrefix is the targeted-invalidation primitive behind invalidate*.
func TestCache_ClearPrefix(t *testing.T) {
	c := NewCache()
	c.Set("library:tracks|agg", "a", time.Minute)
	c.Set("library:albums|agg", "b", time.Minute)
	c.Set("spotify:/me", "c", time.Minute)

	c.ClearPrefix("library:")

	if _, ok := c.Get("library:tracks|agg"); ok {
		t.Fatal("library entry survived")
	}
	if _, ok := c.Get("library:albums|agg"); ok {
		t.Fatal("library entry survived")
	}
	if _, ok := c.Get("spotify:/me"); !ok {
		t.Fatal("unrelated entry must survive")
	}
	// Empty prefix is a no-op (never a full wipe by accident).
	c.Set("x", "y", time.Minute)
	c.ClearPrefix("")
	if _, ok := c.Get("x"); !ok {
		t.Fatal("empty prefix must not clear")
	}
}
