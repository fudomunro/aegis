package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeFullLibraryProvider struct {
	playlists []Playlist
	tracks    []Track
	albums    []Album
	artists   []Artist
	err       error
}

func (f *fakeFullLibraryProvider) Capabilities() ServiceCapabilities { return ServiceCapabilities{} }
func (f *fakeFullLibraryProvider) SystemInstructions(string) string  { return "fake" }
func (f *fakeFullLibraryProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}

func (f *fakeFullLibraryProvider) ListUserPlaylists(context.Context) ([]Playlist, bool, error) {
	return f.playlists, false, f.err
}
func (f *fakeFullLibraryProvider) ListUserLikedTracks(context.Context) ([]Track, bool, error) {
	return f.tracks, false, f.err
}
func (f *fakeFullLibraryProvider) ListUserSavedAlbums(context.Context) ([]Album, bool, error) {
	return f.albums, false, f.err
}
func (f *fakeFullLibraryProvider) ListUserFollowedArtists(context.Context) ([]Artist, bool, error) {
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
	tr := Track{Sources: []TrackSource{{Service: "spotify", ID: "t1"}, {Service: "youtube-music", ID: "t2"}}}
	al := Album{Sources: []AlbumSource{{Service: "spotify", ID: "a1"}}}
	ar := Artist{Sources: []ArtistSource{{Service: "google", ID: "p1"}, {Service: "spotify", ID: "p2"}}}
	pl := Playlist{Sources: []PlaylistSource{{Service: "youtube-music", ID: "PL"}}}

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
	if got := trackSourceRefs(Track{}); len(got) != 0 {
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
	srv.connectors = map[string]ServiceConnector{
		"spotify": &fakeFullLibraryProvider{
			playlists: []Playlist{{
				Name:    "Road Trip",
				Sources: []PlaylistSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp-pl"}},
			}},
			tracks: []Track{{
				Name:    "Blinding Lights",
				Artists: []Artist{newArtist("spotify", "Spotify", "ar1", "The Weeknd")},
				Sources: []TrackSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp-bl"}},
			}, {
				Name:    "Solo Hit",
				Artists: []Artist{newArtist("spotify", "Spotify", "ar2", "Solo Artist")},
				Sources: []TrackSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp-solo"}},
			}},
			albums: []Album{{
				Name:    "After Hours",
				Artists: []Artist{newArtist("spotify", "Spotify", "ar1", "The Weeknd")},
				Sources: []AlbumSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp-al"}},
			}},
			artists: []Artist{{
				Name:    "The Weeknd",
				Sources: []ArtistSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp-ar"}},
			}},
		},
		"youtube-music": &fakeFullLibraryProvider{
			playlists: []Playlist{{
				Name:    "road trip",
				Sources: []PlaylistSource{{Service: "youtube-music", ServiceName: "YouTube Music", ID: "yt-pl"}},
			}},
			tracks: []Track{{
				Name:    "Blinding Lights",
				Artists: []Artist{newArtist("youtube-music", "YouTube Music", "ar1", "The Weeknd")},
				Sources: []TrackSource{{Service: "youtube-music", ServiceName: "YouTube Music", ID: "yt-bl"}},
			}},
			albums: []Album{{
				Name:    "After Hours",
				Artists: []Artist{newArtist("youtube-music", "YouTube Music", "ar1", "The Weeknd")},
				Sources: []AlbumSource{{Service: "youtube-music", ServiceName: "YouTube Music", ID: "yt-al"}},
			}},
			artists: []Artist{{
				Name:    "The Weeknd",
				Sources: []ArtistSource{{Service: "youtube-music", ServiceName: "YouTube Music", ID: "yt-ar"}},
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
		if item.Match != TrackMatchName && item.Match != PlaylistMatchName {
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
	srv.connectors = map[string]ServiceConnector{
		"spotify": &fakeFullLibraryProvider{err: ErrUnauthorized},
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
