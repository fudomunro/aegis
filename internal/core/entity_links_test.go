package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/core/links"
	"github.com/fudomunro/aegis/internal/kinds"
)

func TestMergePlaylistsSyncLinked_ExplicitLinkWins(t *testing.T) {
	// Different names but explicitly linked → one entity, match=link.
	merged := kinds.MergePlaylistsSyncLinked([]kinds.Playlist{
		{Name: "Road Trip", Sources: []kinds.PlaylistSource{{Service: "spotify", ID: "sp1"}}},
		{Name: "Vacation Mix", Sources: []kinds.PlaylistSource{{Service: "youtube-music", ID: "yt1"}}},
	}, func(p kinds.Playlist) string {
		if len(p.Sources) > 0 && p.Sources[0].ID == "sp1" {
			return "link:abc"
		}
		if len(p.Sources) > 0 && p.Sources[0].ID == "yt1" {
			return "link:abc"
		}
		return ""
	})
	if len(merged) != 1 {
		t.Fatalf("want linked merge, got %+v", merged)
	}
	if merged[0].Match != kinds.PlaylistMatchLink {
		t.Fatalf("match = %q", merged[0].Match)
	}
	if len(merged[0].Sources) != 2 {
		t.Fatalf("sources = %+v", merged[0].Sources)
	}

	// Same name without link → match=name.
	merged = kinds.MergePlaylistsSyncLinked([]kinds.Playlist{
		{Name: "Road Trip", Sources: []kinds.PlaylistSource{{Service: "spotify", ID: "a"}}},
		{Name: "road trip", Sources: []kinds.PlaylistSource{{Service: "youtube-music", ID: "b"}}},
	}, nil)
	if len(merged) != 1 || merged[0].Match != kinds.PlaylistMatchName {
		t.Fatalf("name merge = %+v", merged)
	}
}

func TestHandleEntityLinks_HTTP(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "links.json")
	st, err := links.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.links = st

	// Create
	body := `{"kind":"playlist","sources":[{"service":"spotify","id":"p1"},{"service":"youtube-music","id":"P1"}],"note":"n"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/library/links", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.HandleEntityLinks(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", w.Code, w.Body.String())
	}
	var created links.Link
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	// List
	req = httptest.NewRequest(http.MethodGet, "/v1/library/links", nil)
	w = httptest.NewRecorder()
	srv.HandleEntityLinks(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d", w.Code)
	}

	// Delete
	req = httptest.NewRequest(http.MethodDelete, "/v1/library/links/"+created.ID, nil)
	w = httptest.NewRecorder()
	srv.HandleEntityLinks(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("delete status = %d body=%s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("link file should persist: %v", err)
	}
}
