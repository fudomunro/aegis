package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

func TestEntityLinkStore_AddListDelete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "links.json")
	st, err := NewEntityLinkStore(path)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	link, err := st.Add("playlist", []EntityLinkSource{
		{Service: "spotify", ID: "pl1"},
		{Service: "youtube-music", ID: "PL1"},
	}, "same mix")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if link.ID == "" || len(link.Sources) != 2 {
		t.Fatalf("link = %+v", link)
	}

	// Duplicate source rejected.
	if _, err := st.Add("playlist", []EntityLinkSource{
		{Service: "spotify", ID: "pl1"},
		{Service: "amazon-music", ID: "x"},
	}, ""); err == nil {
		t.Fatal("expected duplicate source error")
	}

	// Reload from disk.
	st2, err := NewEntityLinkStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(st2.List()) != 1 {
		t.Fatalf("reloaded links = %+v", st2.List())
	}

	key := st2.PlaylistLinkKey(types.Playlist{
		Sources: []types.PlaylistSource{{Service: "youtube-music", ID: "PL1"}},
	})
	if key != "link:"+link.ID {
		t.Fatalf("link key = %q", key)
	}
	if st2.PlaylistLinkKey(types.Playlist{Sources: []types.PlaylistSource{{Service: "spotify", ID: "other"}}}) != "" {
		t.Fatal("unlinked playlist should have empty key")
	}

	if !st2.Delete(link.ID) {
		t.Fatal("delete failed")
	}
	if len(st2.List()) != 0 {
		t.Fatalf("after delete = %+v", st2.List())
	}
}

func TestMergePlaylistsSyncLinked_ExplicitLinkWins(t *testing.T) {
	// Different names but explicitly linked → one entity, match=link.
	merged := types.MergePlaylistsSyncLinked([]types.Playlist{
		{Name: "Road Trip", Sources: []types.PlaylistSource{{Service: "spotify", ID: "sp1"}}},
		{Name: "Vacation Mix", Sources: []types.PlaylistSource{{Service: "youtube-music", ID: "yt1"}}},
	}, func(p types.Playlist) string {
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
	if merged[0].Match != types.PlaylistMatchLink {
		t.Fatalf("match = %q", merged[0].Match)
	}
	if len(merged[0].Sources) != 2 {
		t.Fatalf("sources = %+v", merged[0].Sources)
	}

	// Same name without link → match=name.
	merged = types.MergePlaylistsSync([]types.Playlist{
		{Name: "Road Trip", Sources: []types.PlaylistSource{{Service: "spotify", ID: "a"}}},
		{Name: "road trip", Sources: []types.PlaylistSource{{Service: "youtube-music", ID: "b"}}},
	})
	if len(merged) != 1 || merged[0].Match != types.PlaylistMatchName {
		t.Fatalf("name merge = %+v", merged)
	}
}

func TestHandleEntityLinks_HTTP(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "links.json")
	st, err := NewEntityLinkStore(path)
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
	var created EntityLink
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
