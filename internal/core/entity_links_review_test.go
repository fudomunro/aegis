package core

import (
	"github.com/fudomunro/aegis/internal/connectors"

	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

type fakeFullLibraryProvider struct {
	playlists []types.Playlist
	tracks    []types.Track
	albums    []types.Album
	artists   []types.Artist
	err       error
}

func (f *fakeFullLibraryProvider) Capabilities() types.ServiceCapabilities {
	return types.ServiceCapabilities{}
}
func (f *fakeFullLibraryProvider) SystemInstructions(string) string { return "fake" }
func (f *fakeFullLibraryProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}

func (f *fakeFullLibraryProvider) ListUserPlaylists(context.Context) ([]types.Playlist, bool, error) {
	return f.playlists, false, f.err
}
func (f *fakeFullLibraryProvider) ListUserLikedTracks(context.Context) ([]types.Track, bool, error) {
	return f.tracks, false, f.err
}
func (f *fakeFullLibraryProvider) ListUserSavedAlbums(context.Context) ([]types.Album, bool, error) {
	return f.albums, false, f.err
}
func (f *fakeFullLibraryProvider) ListUserFollowedArtists(context.Context) ([]types.Artist, bool, error) {
	return f.artists, false, f.err
}

func TestListProvidersParallel_MergesAndSkipsErrors(t *testing.T) {
	providers := []namedLister[int]{
		{id: "a", list: func(context.Context) ([]int, bool, error) { return []int{1, 2}, false, nil }},
		{id: "b", list: func(context.Context) ([]int, bool, error) { return nil, false, errors.New("boom") }},
		{id: "c", list: func(context.Context) ([]int, bool, error) { return []int{3}, false, nil }},
		{id: "d", list: func(context.Context) ([]int, bool, error) { return []int{}, false, nil }},
	}
	got := listProvidersParallel[int](context.Background(), providers)
	if len(got) != 3 {
		t.Fatalf("got %v", got)
	}
	sum := 0
	for _, v := range got {
		sum += v
	}
	if sum != 6 {
		t.Fatalf("sum = %d got=%v", sum, got)
	}

	if listProvidersParallel[int](context.Background(), nil) != nil {
		t.Fatal("empty providers should return nil")
	}
}

func TestSourceRefHelpers(t *testing.T) {
	tr := types.Track{Sources: []types.TrackSource{{Service: "spotify", ID: "t1"}, {Service: "youtube-music", ID: "t2"}}}
	al := types.Album{Sources: []types.AlbumSource{{Service: "spotify", ID: "a1"}}}
	ar := types.Artist{Sources: []types.ArtistSource{{Service: "google", ID: "p1"}, {Service: "spotify", ID: "p2"}}}
	pl := types.Playlist{Sources: []types.PlaylistSource{{Service: "youtube-music", ID: "PL"}}}

	if got := trackSourceRefs(tr); len(got) != 2 || got[0] != "spotify:t1" || got[1] != "youtube-music:t2" {
		t.Fatalf("trackSourceRefs = %v", got)
	}
	if got := albumSourceRefs(al); len(got) != 1 || got[0] != "spotify:a1" {
		t.Fatalf("albumSourceRefs = %v", got)
	}
	if got := artistSourceRefs(ar); len(got) != 2 || got[0] != "google:p1" {
		t.Fatalf("artistSourceRefs = %v", got)
	}
	if got := playlistSourceRefs(pl); len(got) != 1 || got[0] != "youtube-music:PL" {
		t.Fatalf("playlistSourceRefs = %v", got)
	}
	if got := trackSourceRefs(types.Track{}); len(got) != 0 {
		t.Fatalf("empty track refs = %v", got)
	}
}

func TestHandleMergeReview_MethodNotAllowed(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest(http.MethodPost, "/v1/library/review", nil)
	w := httptest.NewRecorder()
	srv.HandleMergeReview(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandleMergeReview_NameOnlyMultiSourceEntities(t *testing.T) {
	links, err := NewEntityLinkStore(filepath.Join(t.TempDir(), "links.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		Services: []Service{
			{ID: "spotify", Name: "Spotify", Action: "inject"},
			{ID: "youtube-music", Name: "YouTube Music", Action: "inject"},
			{ID: "google", Name: "Google", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.links = links
	// Seed negative MusicBrainz cache entries so merge review stays offline.
	srv.cache.Set(artistMBIDCacheKey("The Weeknd"), "-", time.Hour)
	srv.cache.Set(recordingMBIDCacheKey("Blinding Lights", "The Weeknd"), "-", time.Hour)
	srv.cache.Set(releaseGroupMBIDCacheKey("After Hours", "The Weeknd"), "-", time.Hour)
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": &fakeFullLibraryProvider{
			playlists: []types.Playlist{{
				Name:    "Road Trip",
				Sources: []types.PlaylistSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp-pl"}},
			}},
			tracks: []types.Track{{
				Name:    "Blinding Lights",
				Artists: []types.Artist{types.NewArtist("spotify", "Spotify", "ar1", "The Weeknd")},
				Sources: []types.TrackSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp-bl"}},
			}, {
				Name:    "Solo Hit",
				Artists: []types.Artist{types.NewArtist("spotify", "Spotify", "ar2", "Solo Artist")},
				Sources: []types.TrackSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp-solo"}},
			}},
			albums: []types.Album{{
				Name:    "After Hours",
				Artists: []types.Artist{types.NewArtist("spotify", "Spotify", "ar1", "The Weeknd")},
				Sources: []types.AlbumSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp-al"}},
			}},
			artists: []types.Artist{{
				Name:    "The Weeknd",
				Sources: []types.ArtistSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp-ar"}},
			}},
		},
		"youtube-music": &fakeFullLibraryProvider{
			playlists: []types.Playlist{{
				Name:    "road trip",
				Sources: []types.PlaylistSource{{Service: "youtube-music", ServiceName: "YouTube Music", ID: "yt-pl"}},
			}},
			tracks: []types.Track{{
				Name:    "Blinding Lights",
				Artists: []types.Artist{types.NewArtist("youtube-music", "YouTube Music", "ar1", "The Weeknd")},
				Sources: []types.TrackSource{{Service: "youtube-music", ServiceName: "YouTube Music", ID: "yt-bl"}},
			}},
			albums: []types.Album{{
				Name:    "After Hours",
				Artists: []types.Artist{types.NewArtist("youtube-music", "YouTube Music", "ar1", "The Weeknd")},
				Sources: []types.AlbumSource{{Service: "youtube-music", ServiceName: "YouTube Music", ID: "yt-al"}},
			}},
			artists: []types.Artist{{
				Name:    "The Weeknd",
				Sources: []types.ArtistSource{{Service: "youtube-music", ServiceName: "YouTube Music", ID: "yt-ar"}},
			}},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/library/review", nil)
	w := httptest.NewRecorder()
	srv.HandleMergeReview(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}

	var resp MergeReviewResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total == 0 || len(resp.Items) != resp.Total {
		t.Fatalf("resp = %+v", resp)
	}

	byKind := map[string][]MergeReviewItem{}
	for _, item := range resp.Items {
		byKind[item.Kind] = append(byKind[item.Kind], item)
		if item.Match != types.TrackMatchName && item.Match != types.PlaylistMatchName {
			t.Fatalf("unexpected match %q on %+v", item.Match, item)
		}
		if len(item.Sources) < 2 {
			t.Fatalf("item should list multi sources: %+v", item)
		}
		for _, src := range item.Sources {
			if !strings.Contains(src, ":") {
				t.Fatalf("source ref should be service:id, got %q", src)
			}
		}
	}

	for _, kind := range []string{"track", "album", "artist", "playlist"} {
		if len(byKind[kind]) == 0 {
			t.Fatalf("expected %s review items, got %+v", kind, resp.Items)
		}
	}

	// Name-only playlist join should mention the links endpoint.
	var plItem MergeReviewItem
	for _, item := range byKind["playlist"] {
		if strings.EqualFold(item.Name, "Road Trip") || strings.EqualFold(item.Name, "road trip") {
			plItem = item
		}
	}
	if plItem.Name == "" || !strings.Contains(plItem.Note, "/v1/library/links") {
		t.Fatalf("playlist item = %+v", plItem)
	}

	// Single-source Solo Hit must not appear.
	for _, item := range byKind["track"] {
		if strings.Contains(item.Name, "Solo Hit") {
			t.Fatalf("single-source track should not be reviewable: %+v", item)
		}
	}
}

func TestHandleMergeReview_ProviderErrorsStillReturn200(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": &fakeFullLibraryProvider{err: types.ErrUnauthorized},
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/library/review", nil)
	w := httptest.NewRecorder()
	srv.HandleMergeReview(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp MergeReviewResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 0 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestHandleEntityLinks_MethodAndMissingPaths(t *testing.T) {
	dir := t.TempDir()
	st, err := NewEntityLinkStore(filepath.Join(dir, "links.json"))
	if err != nil {
		t.Fatal(err)
	}
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.links = st

	// Not configured
	srv2 := testServerWithBase(&Config{}, "https://aegis.example.com")
	req := httptest.NewRequest(http.MethodGet, "/v1/library/links", nil)
	w := httptest.NewRecorder()
	srv2.HandleEntityLinks(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unconfigured status = %d", w.Code)
	}

	// Method not allowed
	req = httptest.NewRequest(http.MethodPut, "/v1/library/links", nil)
	w = httptest.NewRecorder()
	srv.HandleEntityLinks(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method status = %d", w.Code)
	}

	// Invalid JSON
	req = httptest.NewRequest(http.MethodPost, "/v1/library/links", strings.NewReader("{"))
	w = httptest.NewRecorder()
	srv.HandleEntityLinks(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad json status = %d", w.Code)
	}

	// Add requires ≥2 sources
	req = httptest.NewRequest(http.MethodPost, "/v1/library/links",
		strings.NewReader(`{"kind":"playlist","sources":[{"service":"spotify","id":"only"}]}`))
	w = httptest.NewRecorder()
	srv.HandleEntityLinks(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("one source status = %d body=%s", w.Code, w.Body.String())
	}

	// GET missing id
	req = httptest.NewRequest(http.MethodGet, "/v1/library/links/nope", nil)
	w = httptest.NewRecorder()
	srv.HandleEntityLinks(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing get status = %d", w.Code)
	}

	// DELETE missing id
	req = httptest.NewRequest(http.MethodDelete, "/v1/library/links/nope", nil)
	w = httptest.NewRecorder()
	srv.HandleEntityLinks(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing delete status = %d", w.Code)
	}

	// Create then GET by id
	body := `{"kind":"playlist","sources":[{"service":"spotify","id":"p1"},{"service":"youtube-music","id":"P1"}]}`
	req = httptest.NewRequest(http.MethodPost, "/v1/library/links", strings.NewReader(body))
	w = httptest.NewRecorder()
	srv.HandleEntityLinks(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", w.Code, w.Body.String())
	}
	var created EntityLink
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/v1/library/links/"+created.ID, nil)
	w = httptest.NewRecorder()
	srv.HandleEntityLinks(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get status = %d", w.Code)
	}
}
