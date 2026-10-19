package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/types"
)

// pagingTrackProvider returns n uniquely named tracks so merge cannot collapse
// them; used to walk the agent page window with no duplicates or gaps.
type pagingTrackProvider struct {
	tracks []types.Track
}

func (f *pagingTrackProvider) Capabilities() types.ServiceCapabilities {
	return types.ServiceCapabilities{}
}
func (f *pagingTrackProvider) SystemInstructions(string) string { return "fake" }
func (f *pagingTrackProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}

func (f *pagingTrackProvider) ListUserLikedTracks(context.Context) ([]types.Track, bool, error) {
	return f.tracks, false, nil
}

func pagingTracks(n int) []types.Track {
	out := make([]types.Track, 0, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("Track %03d", i)
		out = append(out, types.Track{
			Name:    name,
			Artists: []types.Artist{types.NewArtist("spotify", "Spotify", "", "Artist")},
			Sources: []types.TrackSource{{Service: "spotify", ServiceName: "Spotify", ID: fmt.Sprintf("t%03d", i)}},
		})
	}
	return out
}

func TestLibraryTracks_PagesWithoutDuplicatesOrGaps(t *testing.T) {
	const total = 250
	cfg := &Config{Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": &pagingTrackProvider{tracks: pagingTracks(total)},
	}

	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		url := "/v1/library/tracks?limit=100"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		req := httptest.NewRequest("GET", url, nil)
		req.Header.Set("Authorization", "Bearer test-key")
		w := httptest.NewRecorder()
		srv.AuthMiddleware(srv.HandleLibraryTracks)(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
		}
		var resp types.TrackList
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Total != total {
			t.Fatalf("total = %d, want %d", resp.Total, total)
		}
		for _, tr := range resp.Tracks {
			if seen[tr.Name] {
				t.Fatalf("duplicate track %q", tr.Name)
			}
			seen[tr.Name] = true
		}
		pages++
		if resp.NextCursor == "" {
			break
		}
		cursor = resp.NextCursor
		if pages > 10 {
			t.Fatal("runaway paging")
		}
	}
	if len(seen) != total {
		t.Fatalf("walked %d tracks, want %d", len(seen), total)
	}
	if pages != 3 {
		t.Fatalf("pages = %d, want 3", pages)
	}
}

func TestLibraryTracks_InvalidCursorIs400(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": &pagingTrackProvider{tracks: pagingTracks(1)},
	}
	req := httptest.NewRequest("GET", "/v1/library/tracks?cursor=not-a-cursor", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryTracks)(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestLibraryTracks_LimitClampsAndCachesPerWindow(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: "spotify", Name: "Spotify", Action: "inject"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"spotify": &pagingTrackProvider{tracks: pagingTracks(10)},
	}

	req := httptest.NewRequest("GET", "/v1/library/tracks?limit=9999", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryTracks)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp types.TrackList
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 10 || len(resp.Tracks) != 10 {
		t.Fatalf("total=%d len=%d", resp.Total, len(resp.Tracks))
	}
	if resp.NextCursor != "" {
		t.Fatalf("next_cursor = %q, want empty for full set", resp.NextCursor)
	}

	// Default page of a larger set must leave next_cursor set.
	srv.connectors["spotify"] = &pagingTrackProvider{tracks: pagingTracks(150)}
	srv.cache.Clear()
	req2 := httptest.NewRequest("GET", "/v1/library/tracks", nil)
	req2.Header.Set("Authorization", "Bearer test-key")
	w2 := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleLibraryTracks)(w2, req2)
	var page1 types.TrackList
	if err := json.Unmarshal(w2.Body.Bytes(), &page1); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(page1.Tracks) != types.DefaultPageLimit {
		t.Fatalf("default page size = %d", len(page1.Tracks))
	}
	if page1.NextCursor == "" {
		t.Fatal("want next_cursor on first page of 150")
	}
	if page1.Total != 150 {
		t.Fatalf("total = %d", page1.Total)
	}
}
