package syncjobs

import (
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/core/copies"
)

// --- sync conflict store (issue #365 phase 3) ---

func testConflictStore(t *testing.T) *ConflictStore {
	t.Helper()
	s, err := OpenConflictStore(nil, t.TempDir()+"/sync_jobs.db")
	if err != nil {
		t.Fatalf("OpenConflictStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestConflictStore_UpsertResolveReopen(t *testing.T) {
	s := testConflictStore(t)
	rec, err := s.Upsert(SyncConflict{
		Kind: "playlists", Service: "icloud", EntityKey: "playlists:google:P1",
		Subject: "Road trip", Field: ConflictFieldTitle,
		MemberValue: "Mine", LastValue: "Road trip", Source: ConflictSourceConverge,
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if rec.ID == "" || rec.Status != ConflictStatusOpen || rec.DetectedAt.IsZero() {
		t.Fatalf("upserted = %+v, want an open row", rec)
	}

	// Same locus again: one row, refreshed values.
	again, err := s.Upsert(SyncConflict{
		Kind: "playlists", Service: "icloud", EntityKey: "playlists:google:P1",
		Field: ConflictFieldTitle, MemberValue: "Mine", LastValue: "Renamed",
	})
	if err != nil || again.ID != rec.ID {
		t.Fatalf("re-upsert = (%+v,%v), want the same row", again, err)
	}

	// Resolve keeps the row as history.
	resolved, err := s.Resolve(rec.ID, ConflictStatusRejected, "kept local")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Status != ConflictStatusRejected || resolved.Note != "kept local" || resolved.ResolvedAt.IsZero() {
		t.Fatalf("resolved = %+v", resolved)
	}

	// The same member value must not nag again after a decision…
	quiet, err := s.Upsert(SyncConflict{
		Kind: "playlists", Service: "icloud", EntityKey: "playlists:google:P1",
		Field: ConflictFieldTitle, MemberValue: "Mine", LastValue: "Road trip",
	})
	if err != nil || quiet.Status != ConflictStatusRejected {
		t.Fatalf("re-detection after resolve = (%+v,%v), want it left resolved", quiet, err)
	}

	// …but a new member-side value reopens the locus.
	reopen, err := s.Upsert(SyncConflict{
		Kind: "playlists", Service: "icloud", EntityKey: "playlists:google:P1",
		Field: ConflictFieldTitle, MemberValue: "Yours", LastValue: "Road trip",
	})
	if err != nil || reopen.Status != ConflictStatusOpen {
		t.Fatalf("changed value = (%+v,%v), want a reopened row", reopen, err)
	}
}

func TestConflictStore_ListAndGet(t *testing.T) {
	s := testConflictStore(t)
	if _, err := s.Upsert(SyncConflict{Kind: "playlists", Service: "icloud",
		EntityKey: "playlists:google:P1", Field: ConflictFieldTitle, MemberValue: "A"}); err != nil {
		t.Fatal(err)
	}
	b, _ := s.Upsert(SyncConflict{Kind: "contacts", Service: "icloud",
		EntityKey: "contacts:Ada", Field: ConflictFieldRecord, MemberValue: "B"})
	if _, err := s.Resolve(b.ID, ConflictStatusAccepted, "applied"); err != nil {
		t.Fatal(err)
	}

	if got := len(s.List(ConflictFilter{Status: ConflictStatusOpen})); got != 1 {
		t.Fatalf("open rows = %d, want 1", got)
	}
	if got := len(s.List(ConflictFilter{Status: ConflictStatusAccepted})); got != 1 {
		t.Fatalf("accepted rows = %d, want 1", got)
	}
	if got := len(s.List(ConflictFilter{Kind: "contacts"})); got != 1 {
		t.Fatalf("contacts rows = %d, want 1", got)
	}
	if _, ok := s.Get(b.ID); !ok {
		t.Fatal("Get must find the accepted row")
	}
	if _, ok := s.Get("424242"); ok {
		t.Fatal("Get must miss an unknown id")
	}
	var nilStore *ConflictStore
	if nilStore.Enabled() {
		t.Fatal("nil store must be disabled")
	}
	if _, err := nilStore.Upsert(SyncConflict{Kind: "playlists", Service: "x", EntityKey: "e", Field: "f"}); err != nil {
		t.Fatalf("disabled Upsert must be a no-op: %v", err)
	}
	if nilStore.List(ConflictFilter{}) != nil {
		t.Fatal("disabled List must be nil")
	}
}

func TestConflictStore_UpsertRequiresLocus(t *testing.T) {
	s := testConflictStore(t)
	if _, err := s.Upsert(SyncConflict{Kind: "playlists", Service: "x", Field: "f"}); err == nil {
		t.Fatal("Upsert must require an entity key")
	}
	if _, err := s.Resolve("1", "nonsense", ""); err == nil {
		t.Fatal("Resolve must reject an unknown status")
	}
	if _, err := s.Resolve("999", ConflictStatusAccepted, ""); err == nil {
		t.Fatal("Resolve must miss an unknown id")
	}
}

func TestClassifyCopyDivergence(t *testing.T) {
	base := copies.Version{Name: "Road trip", ItemCount: 10, SnapshotID: "snap-1", ETag: "e-1"}
	cases := []struct {
		name   string
		want   copies.Version
		got    copies.Version
		exists bool
		field  string
	}{
		{"unchanged", base, base, true, ""},
		{"vanished", base, copies.Version{}, false, ConflictFieldMissing},
		{"renamed", base, copies.Version{Name: "Mine", ItemCount: 10, SnapshotID: "snap-1", ETag: "e-1"}, true, ConflictFieldTitle},
		{"member gained items", base, copies.Version{Name: "Road trip", ItemCount: 12, SnapshotID: "snap-2"}, true, ConflictFieldItems},
		{"content swapped", base, copies.Version{Name: "Road trip", ItemCount: 10, SnapshotID: "snap-2"}, true, ConflictFieldItems},
		{"member shortfall alone", base, copies.Version{Name: "Road trip", ItemCount: 8}, true, ""},
		{"record edited", base, copies.Version{Name: "Road trip", ItemCount: 10, SnapshotID: "snap-1", ETag: "e-2"}, true, ConflictFieldRecord},
		{"hash edited", copies.Version{ContentHash: "h-1"}, copies.Version{ContentHash: "h-2"}, true, ConflictFieldRecord},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyCopyDivergence(tc.want, tc.got, tc.exists); got != tc.field {
				t.Fatalf("classify = %q, want %q", got, tc.field)
			}
		})
	}
}

func TestConflictStore_ReopenUsesMemberValueOnly(t *testing.T) {
	s := testConflictStore(t)
	rec, err := s.Upsert(SyncConflict{Kind: "playlists", Service: "icloud",
		EntityKey: "playlists:google:P1", Field: ConflictFieldItems,
		MemberValue: "13 items", LastValue: "10 items"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve(rec.ID, ConflictStatusRejected, "kept local"); err != nil {
		t.Fatal(err)
	}
	// The canonical home moved, but the member-side edit is unchanged:
	// the decision stands.
	same, err := s.Upsert(SyncConflict{Kind: "playlists", Service: "icloud",
		EntityKey: "playlists:google:P1", Field: ConflictFieldItems,
		MemberValue: "13 items", LastValue: "11 items"})
	if err != nil || same.Status != ConflictStatusRejected {
		t.Fatalf("= (%+v,%v), want left resolved", same, err)
	}
	// A detected-at refresh on an open row keeps it open.
	open, _ := s.Upsert(SyncConflict{Kind: "playlists", Service: "icloud",
		EntityKey: "playlists:google:P2", Field: ConflictFieldItems,
		MemberValue: "3 items", LastValue: "2 items"})
	refreshed, err := s.Upsert(SyncConflict{Kind: "playlists", Service: "icloud",
		EntityKey: "playlists:google:P2", Field: ConflictFieldItems,
		MemberValue: "3 items", LastValue: "2 items"})
	if err != nil || refreshed.Status != ConflictStatusOpen || refreshed.ID != open.ID {
		t.Fatalf("refresh = (%+v,%v), want the same open row", refreshed, err)
	}
	if !refreshed.DetectedAt.After(open.DetectedAt.Add(-time.Second)) {
		t.Fatalf("detected_at = %v, want it refreshed", refreshed.DetectedAt)
	}
}
