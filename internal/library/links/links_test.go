package links

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/kinds"
)

func TestStore_AddListDelete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "links.json")
	st, err := NewStore(path)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	link, err := st.Add("playlist", []Source{
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
	if _, err := st.Add("playlist", []Source{
		{Service: "spotify", ID: "pl1"},
		{Service: "amazon-music", ID: "x"},
	}, ""); err == nil {
		t.Fatal("expected duplicate source error")
	}

	// Reload from disk.
	st2, err := NewStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(st2.List()) != 1 {
		t.Fatalf("reloaded links = %+v", st2.List())
	}

	key := st2.PlaylistLinkKey(kinds.Playlist{
		Sources: []kinds.PlaylistSource{{Service: "youtube-music", ID: "PL1"}},
	})
	if key != "link:"+link.ID {
		t.Fatalf("link key = %q", key)
	}
	if st2.PlaylistLinkKey(kinds.Playlist{Sources: []kinds.PlaylistSource{{Service: "spotify", ID: "other"}}}) != "" {
		t.Fatal("unlinked playlist should have empty key")
	}

	if !st2.Delete(link.ID) {
		t.Fatal("delete failed")
	}
	if len(st2.List()) != 0 {
		t.Fatalf("after delete = %+v", st2.List())
	}
}

// entity_links.json was the one state store still written with plain
// os.WriteFile, so a crash could truncate it and silently drop the operator's
// confirmed identities. This asserts the atomic write path: no temp litter,
// and a well-formed file after every save.
func TestStore_SaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "entity_links.json")
	st, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	var ids []string
	for i := 0; i < 5; i++ {
		link, err := st.Add("playlist", []Source{
			{Service: "spotify", ID: fmt.Sprint("pl", i)},
			{Service: "youtube-music", ID: fmt.Sprint("yt", i)},
		}, "test")
		if err != nil {
			t.Fatalf("Add(%d): %v", i, err)
		}
		ids = append(ids, link.ID)
		// A same-directory temp file is how shared.AtomicWriteFile works; a
		// successful save must leave none behind.
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if strings.Contains(e.Name(), ".tmp") {
				t.Fatalf("temp file left behind after save: %s", e.Name())
			}
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		var f file
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatalf("saved file is not valid JSON (truncated write?): %v", err)
		}
		if len(f.Links) != i+1 {
			t.Fatalf("links = %d, want %d", len(f.Links), i+1)
		}
		if !strings.HasSuffix(string(raw), "\n") {
			t.Fatal("saved file must keep its trailing newline")
		}
	}

	// Deletes persist the same way.
	if !st.Delete(ids[0]) {
		t.Fatalf("Delete(%s) must report the row was removed", ids[0])
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("file corrupt after delete: %v", err)
	}
	if len(f.Links) != 4 {
		t.Fatalf("links = %d, want 4", len(f.Links))
	}
}
