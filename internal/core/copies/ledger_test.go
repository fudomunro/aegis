package copies

import (
	"path/filepath"
	"testing"
)

func mustLedger(t *testing.T) *Ledger {
	t.Helper()
	path := filepath.Join(t.TempDir(), "copy_ledger.db")
	c, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestLedger_UpsertGetListDelete(t *testing.T) {
	c := mustLedger(t)
	rec, err := c.Upsert(Record{
		Kind:          "playlists",
		EntityKey:     "playlists:spotify:abc",
		SourceService: "spotify",
		SourceID:      "abc",
		Service:       "youtube-music",
		ExternalID:    "xyz",
		Role:          CopyRoleMirror,
		Version:       Version{ItemCount: 12, Name: "Road Trip"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != CopyStatusInSync {
		t.Fatalf("status = %q, want %q", rec.Status, CopyStatusInSync)
	}
	if rec.SyncMode != CopySyncModeFull {
		t.Fatalf("sync_mode = %q, want full", rec.SyncMode)
	}
	if rec.LastSyncedAt == "" {
		t.Fatal("expected last_synced_at on upsert")
	}

	got, ok := c.Get("playlists", "youtube-music", "xyz")
	if !ok {
		t.Fatal("expected row")
	}
	if got.Version.ItemCount != 12 || got.Version.Name != "Road Trip" {
		t.Fatalf("version = %+v", got.Version)
	}
	if got.SourceService != "spotify" || got.SourceID != "abc" {
		t.Fatalf("source = %s:%s", got.SourceService, got.SourceID)
	}

	// Upsert again is an update, not a second row.
	if _, err := c.Upsert(Record{
		Kind:       "playlists",
		EntityKey:  "playlists:spotify:abc",
		Service:    "youtube-music",
		ExternalID: "xyz",
		Version:    Version{ItemCount: 15, Name: "Road Trip"},
	}); err != nil {
		t.Fatal(err)
	}
	rows := c.List(Filter{Kind: "playlists"})
	if len(rows) != 1 {
		t.Fatalf("list len = %d, want 1", len(rows))
	}
	if rows[0].Version.ItemCount != 15 {
		t.Fatalf("item_count = %d, want 15", rows[0].Version.ItemCount)
	}

	byEntity := c.GetByEntity("playlists", "playlists:spotify:abc")
	if len(byEntity) != 1 {
		t.Fatalf("entity rows = %d", len(byEntity))
	}

	if !c.Delete("playlists", "playlists:spotify:abc", "youtube-music", "xyz") {
		t.Fatal("delete should succeed")
	}
	if _, ok := c.Get("playlists", "youtube-music", "xyz"); ok {
		t.Fatal("row should be gone")
	}
}

func TestLedger_ValidationAndDisabled(t *testing.T) {
	var disabled Ledger
	if _, err := disabled.Upsert(Record{Kind: "playlists", EntityKey: "k", Service: "spotify"}); err != nil {
		t.Fatalf("disabled upsert should fail-open: %v", err)
	}
	if _, err := (&Ledger{}).Upsert(Record{Kind: "playlists", EntityKey: "k"}); err == nil {
		t.Fatal("expected error when service missing")
	}
	empty := &Ledger{}
	if empty.Enabled() {
		t.Fatal("empty ledger should be disabled")
	}
	rec, err := empty.Upsert(Record{Kind: "playlists", EntityKey: "k", Service: "spotify"})
	if err != nil {
		t.Fatalf("disabled upsert should fail-open: %v", err)
	}
	if rec.EntityKey != "k" {
		t.Fatalf("entity = %q", rec.EntityKey)
	}
	if empty.List(Filter{}) != nil {
		t.Fatal("disabled list should be nil")
	}
	if empty.Delete("a", "b", "c", "d") {
		t.Fatal("disabled delete should be false")
	}
}

func TestCopySyncModeForKind(t *testing.T) {
	if SyncModeForKind("messages") != CopySyncModeOptimistic {
		t.Fatal("messages should be optimistic")
	}
	if SyncModeForKind("playlists") != CopySyncModeFull {
		t.Fatal("playlists should be full")
	}
	if SyncModeForKind("photos") != CopySyncModeFull {
		t.Fatal("photos should be full")
	}
}

func TestLedger_MarkChecked(t *testing.T) {
	c := mustLedger(t)
	if _, err := c.Upsert(Record{
		Kind: "photos", EntityKey: "photos:immich:1", Service: "google-photos", ExternalID: "p1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.MarkChecked("photos", "photos:immich:1", "google-photos", "p1", CopyStatusStale); err != nil {
		t.Fatal(err)
	}
	got, ok := c.Get("photos", "google-photos", "p1")
	if !ok || got.Status != CopyStatusStale {
		t.Fatalf("got %+v", got)
	}
	if got.LastCheckedAt == "" {
		t.Fatal("expected last_checked_at")
	}
}

func TestOpen_EmptyPathDisables(t *testing.T) {
	c, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	if c.Enabled() {
		t.Fatal("empty path should disable")
	}
	if c.Path() != "" {
		t.Fatalf("path = %q", c.Path())
	}
}
