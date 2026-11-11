package syncjobs

import (
	"path/filepath"
	"testing"
	"time"
)

// testFreshnessKinds stands in for the runner-declared kinds a production
// SeedDefaults call passes in.
var testFreshnessKinds = []string{"messages", "playlists", "tracks"}

// --- schedules ---

func testScheduleStore(t *testing.T) *ScheduleStore {
	t.Helper()
	s, err := OpenScheduleStore(nil, filepath.Join(t.TempDir(), "sync_jobs.db"))
	if err != nil {
		t.Fatalf("OpenScheduleStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestScheduleStore_UpsertAndToggle(t *testing.T) {
	s := testScheduleStore(t)
	s.now = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }

	sc, err := s.Upsert(SyncSchedule{
		Name: "Daily copy freshness", Kind: "playlists", JobType: SyncJobTypeFreshness,
		EverySeconds: 3600, Enabled: true,
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if sc.ID == 0 || !sc.Enabled {
		t.Fatalf("schedule = %+v, want a saved enabled row", sc)
	}
	if !sc.NextRunAt.Equal(s.now()) {
		t.Fatalf("next_run_at = %v, want due now so the first run is prompt", sc.NextRunAt)
	}

	// Re-upsert with the same interval keeps the existing cadence/next run.
	s.now = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }
	again, err := s.Upsert(SyncSchedule{
		Kind: "playlists", JobType: SyncJobTypeFreshness, EverySeconds: 3600, Enabled: true,
	})
	if err != nil {
		t.Fatalf("re-Upsert: %v", err)
	}
	if again.ID != sc.ID {
		t.Fatalf("id = %d, want %d (keyed upsert)", again.ID, sc.ID)
	}
	if !again.NextRunAt.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("next_run_at = %v, want the original cadence preserved", again.NextRunAt)
	}

	// A changed interval re-arms from now.
	changed, err := s.Upsert(SyncSchedule{
		Kind: "playlists", JobType: SyncJobTypeFreshness, EverySeconds: 7200, Enabled: true,
	})
	if err != nil {
		t.Fatalf("interval change: %v", err)
	}
	if changed.EverySeconds != 7200 || !changed.NextRunAt.Equal(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("changed = %+v, want 7200s re-armed from now", changed)
	}

	// Toggle by id.
	off, ok, err := s.SetEnabled(changed.ID, false)
	if err != nil || !ok {
		t.Fatalf("SetEnabled(false) = (%v,%v)", ok, err)
	}
	if off.Enabled {
		t.Fatal("schedule must be disabled")
	}
	if got := s.Due(time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)); len(got) != 0 {
		t.Fatalf("Due = %v, want none for a disabled schedule", got)
	}
	on, ok, err := s.SetEnabled(changed.ID, true)
	if err != nil || !ok || !on.Enabled {
		t.Fatalf("SetEnabled(true) = (%+v,%v,%v)", on, ok, err)
	}
	if _, ok, err := s.SetEnabled(9999, true); ok || err != nil {
		t.Fatalf("unknown id = (ok=%v err=%v), want (false, nil)", ok, err)
	}
}

func TestScheduleStore_DueAndRequeue(t *testing.T) {
	s := testScheduleStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }
	sc, err := s.Upsert(SyncSchedule{
		Kind: "messages", JobType: SyncJobTypeFreshness, EverySeconds: 3600, Enabled: true,
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	due := s.Due(base.Add(time.Second))
	if len(due) != 1 || due[0].ID != sc.ID {
		t.Fatalf("Due = %v, want the one schedule", due)
	}

	// Requeue pushes it out a full interval.
	s.now = func() time.Time { return base.Add(time.Second) }
	if err := s.Requeue(sc); err != nil {
		t.Fatalf("Requeue: %v", err)
	}
	if got := s.Due(base.Add(30 * time.Minute)); len(got) != 0 {
		t.Fatalf("Due(30m) = %v, want none (re-armed an hour out)", got)
	}
	if got := s.Due(base.Add(2 * time.Hour)); len(got) != 1 {
		t.Fatalf("Due(2h) = %v, want the schedule again", got)
	}
	after, _ := s.getByID(sc.ID)
	if after.LastEnqueuedAt.IsZero() {
		t.Fatal("Requeue must stamp last_enqueued_at")
	}
}

func TestScheduleStore_SeedDefaultsIsIdempotent(t *testing.T) {
	s := testScheduleStore(t)
	added, err := s.SeedDefaults(testFreshnessKinds)
	if err != nil {
		t.Fatalf("SeedDefaults: %v", err)
	}
	if added != len(testFreshnessKinds) {
		t.Fatalf("added = %d, want %d", added, len(testFreshnessKinds))
	}
	// Re-seeding must not duplicate or clobber a disabled row.
	all := s.List()
	if len(all) != len(testFreshnessKinds) {
		t.Fatalf("List = %d, want %d", len(all), len(testFreshnessKinds))
	}
	if _, ok, _ := s.SetEnabled(all[0].ID, false); !ok {
		t.Fatal("toggle failed")
	}
	added, err = s.SeedDefaults(testFreshnessKinds)
	if err != nil || added != 0 {
		t.Fatalf("re-seed = (%d,%v), want (0,nil)", added, err)
	}
	if !s.List()[0].Enabled && len(s.List()) != len(testFreshnessKinds) {
		t.Fatal("re-seed must not change the operator's choice")
	}

	var nilStore *ScheduleStore
	if nilStore.Enabled() {
		t.Fatal("nil store must be disabled")
	}
	if _, err := nilStore.SeedDefaults(nil); err != nil {
		t.Fatalf("nil SeedDefaults: %v", err)
	}
	if nilStore.List() != nil {
		t.Fatal("nil List must be nil")
	}
}

func TestScheduleStore_SeedConvergeDefaults(t *testing.T) {
	kinds := []string{"contacts", "events"}
	s := testScheduleStore(t)
	added, err := s.SeedConvergeDefaults(kinds)
	if err != nil {
		t.Fatalf("SeedConvergeDefaults: %v", err)
	}
	if added != len(kinds) {
		t.Fatalf("added = %d, want %d", added, len(kinds))
	}
	for _, sc := range s.List() {
		if sc.JobType != SyncJobTypeConverge {
			t.Fatalf("schedule %q carries job type %q, want converge", sc.Name, sc.JobType)
		}
		if sc.EverySeconds != 24*60*60 {
			t.Fatalf("converge cadence = %ds, want daily", sc.EverySeconds)
		}
	}
	// Re-seeding is a no-op; the operator's rows are preserved.
	if _, ok, err := s.SetEnabled(s.List()[0].ID, false); !ok || err != nil {
		t.Fatalf("toggle = (%v,%v), want success", ok, err)
	}
	added, err = s.SeedConvergeDefaults(kinds)
	if err != nil || added != 0 {
		t.Fatalf("re-seed = (%d,%v), want (0,nil)", added, err)
	}
	all := s.List()
	if len(all) != len(kinds) {
		t.Fatalf("List = %d, want %d", len(all), len(kinds))
	}
	var nilStore *ScheduleStore
	if _, err := nilStore.SeedConvergeDefaults(kinds); err != nil {
		t.Fatalf("nil SeedConvergeDefaults: %v", err)
	}
}

func TestScheduleStore_DisabledIsNoop(t *testing.T) {
	s, err := OpenScheduleStore(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if s.Enabled() {
		t.Fatal("empty path must disable the store")
	}
	sc, err := s.Upsert(SyncSchedule{Kind: "messages", JobType: SyncJobTypeFreshness})
	if err != nil {
		t.Fatalf("disabled Upsert: %v", err)
	}
	if sc.Kind != "messages" {
		t.Fatalf("disabled Upsert must echo the input, got %+v", sc)
	}
	if _, err := s.Upsert(SyncSchedule{JobType: "x"}); err == nil {
		t.Fatal("Upsert must require kind")
	}
	if got := s.Due(time.Now()); got != nil {
		t.Fatalf("disabled Due = %v", got)
	}
}
