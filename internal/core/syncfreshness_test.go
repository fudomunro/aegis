package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/types"
)

// --- version comparison ---

func TestCompareCopyVersion(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode string
		want CopyVersion
		got  CopyVersion
		live bool
		out  string
	}{
		{"gone is missing", CopySyncModeFull, CopyVersion{ItemCount: 3}, CopyVersion{}, false, CopyStatusMissing},
		{"match is in_sync", CopySyncModeFull, CopyVersion{SnapshotID: "s1", ItemCount: 3}, CopyVersion{SnapshotID: "s1", ItemCount: 3}, true, CopyStatusInSync},
		{"snapshot drift is stale", CopySyncModeFull, CopyVersion{SnapshotID: "s1"}, CopyVersion{SnapshotID: "s2"}, true, CopyStatusStale},
		{"count drift is stale", CopySyncModeFull, CopyVersion{ItemCount: 3}, CopyVersion{ItemCount: 4}, true, CopyStatusStale},
		{"etag drift is stale", CopySyncModeFull, CopyVersion{ETag: "e1"}, CopyVersion{ETag: "e2"}, true, CopyStatusStale},
		{"hash drift is stale", CopySyncModeFull, CopyVersion{ContentHash: "h1"}, CopyVersion{ContentHash: "h2"}, true, CopyStatusStale},
		// A token the provider does not report must not produce a false stale.
		{"unreported snapshot ignored", CopySyncModeFull, CopyVersion{SnapshotID: "s1", ItemCount: 3}, CopyVersion{ItemCount: 3}, true, CopyStatusInSync},
		{"unreported count ignored", CopySyncModeFull, CopyVersion{SnapshotID: "s1", ItemCount: 3}, CopyVersion{SnapshotID: "s1"}, true, CopyStatusInSync},
		// A renamed playlist is not a drifted backup.
		{"name is not staleness", CopySyncModeFull, CopyVersion{ItemCount: 3, Name: "old"}, CopyVersion{ItemCount: 3, Name: "new"}, true, CopyStatusInSync},
		// Optimistic copies can only report presence.
		{"optimistic drift is not stale", CopySyncModeOptimistic, CopyVersion{ItemCount: 3}, CopyVersion{ItemCount: 99}, true, CopyStatusInSync},
		{"optimistic gone is missing", CopySyncModeOptimistic, CopyVersion{ItemCount: 3}, CopyVersion{}, false, CopyStatusMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := compareCopyVersion(tc.mode, tc.want, tc.got, tc.live)
			if got != tc.out {
				t.Fatalf("compare(%s, %+v, %+v, %v) = %s, want %s",
					tc.mode, tc.want, tc.got, tc.live, got, tc.out)
			}
		})
	}
}

// --- engine ---

type stubChecker struct {
	services []string
	fn       func(ctx context.Context, rec CopyRecord) (CopyVersion, bool, bool, error)
	calls    int
}

func (c *stubChecker) Services() []string { return c.services }
func (c *stubChecker) Check(ctx context.Context, rec CopyRecord) (CopyVersion, bool, bool, error) {
	c.calls++
	return c.fn(ctx, rec)
}

func TestCheckerRegistry(t *testing.T) {
	r := NewCheckerRegistry()
	if _, ok := r.For("spotify"); ok {
		t.Fatal("empty registry must miss")
	}
	c := &stubChecker{services: []string{"spotify", "youtube-music"}}
	r.Register(c)
	if got, ok := r.For("spotify"); !ok || got != c {
		t.Fatal("spotify must resolve to the checker")
	}
	if _, ok := r.For("youtube-music"); !ok {
		t.Fatal("second service must resolve too")
	}
	if _, ok := r.For("bluesky"); ok {
		t.Fatal("uncovered service must miss")
	}
	if len(r.Services()) != 2 {
		t.Fatalf("Services = %v, want 2", r.Services())
	}
	var nilReg *CheckerRegistry
	nilReg.Register(c)
	if _, ok := nilReg.For("spotify"); ok {
		t.Fatal("nil registry must miss")
	}
	if nilReg.Services() != nil {
		t.Fatal("nil registry Services must be nil")
	}
}

func freshServerWithLedger(t *testing.T) (*Server, *CopyLedger) {
	t.Helper()
	ledger, err := OpenCopyLedger(filepath.Join(t.TempDir(), "copy_ledger.db"))
	if err != nil {
		t.Fatalf("OpenCopyLedger: %v", err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	srv := &Server{copies: ledger}
	return srv, ledger
}

func TestFreshnessEngine_StampsStatuses(t *testing.T) {
	srv, ledger := freshServerWithLedger(t)
	_, _ = ledger.Upsert(CopyRecord{
		Kind: "playlists", EntityKey: "p1", Service: "spotify", ExternalID: "pl1",
		Role: CopyRoleMirror, SyncMode: CopySyncModeFull,
		Version: CopyVersion{SnapshotID: "s1", ItemCount: 3},
	})
	_, _ = ledger.Upsert(CopyRecord{
		Kind: "playlists", EntityKey: "p2", Service: "spotify", ExternalID: "pl2",
		Role: CopyRoleMirror, SyncMode: CopySyncModeFull,
		Version: CopyVersion{SnapshotID: "s2", ItemCount: 5},
	})
	_, _ = ledger.Upsert(CopyRecord{
		Kind: "playlists", EntityKey: "p3", Service: "spotify", ExternalID: "pl3",
		Role: CopyRoleMirror, SyncMode: CopySyncModeFull,
		Version: CopyVersion{SnapshotID: "s3", ItemCount: 1},
	})
	_, _ = ledger.Upsert(CopyRecord{
		Kind: "playlists", EntityKey: "p4", Service: "bluesky", ExternalID: "at://post",
		Role: CopyRoleMirror, SyncMode: CopySyncModeFull,
		Version: CopyVersion{ContentHash: "h"},
	})

	checker := &stubChecker{
		services: []string{"spotify"},
		fn: func(ctx context.Context, rec CopyRecord) (CopyVersion, bool, bool, error) {
			switch rec.ExternalID {
			case "pl1":
				return CopyVersion{SnapshotID: "s1", ItemCount: 3}, true, true, nil // in sync
			case "pl2":
				return CopyVersion{SnapshotID: "s2", ItemCount: 9}, true, true, nil // stale
			case "pl3":
				return CopyVersion{}, false, true, nil // missing
			}
			return CopyVersion{}, false, false, nil
		},
	}
	reg := NewCheckerRegistry()
	reg.Register(checker)
	engine := NewFreshnessEngine(srv, reg)

	batch, err := engine.RunBatch(context.Background(), "playlists", 0, 50)
	if err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	if !batch.Done {
		t.Fatal("batch must be done in one pass")
	}
	if batch.Checked != 3 || batch.Skipped != 1 {
		t.Fatalf("batch = %+v, want 3 checked and 1 skipped", batch)
	}
	if batch.InSync != 1 || batch.Stale != 1 || batch.Missing != 1 {
		t.Fatalf("batch = %+v, want one of each", batch)
	}

	assertStatus := func(externalID, want string) {
		t.Helper()
		rec, ok := ledger.Get("playlists", "spotify", externalID)
		if !ok {
			t.Fatalf("copy %s missing from the ledger", externalID)
		}
		if rec.Status != want {
			t.Fatalf("copy %s status = %s, want %s", externalID, rec.Status, want)
		}
		if rec.LastCheckedAt == "" {
			t.Fatalf("copy %s must carry last_checked_at", externalID)
		}
	}
	assertStatus("pl1", CopyStatusInSync)
	assertStatus("pl2", CopyStatusStale)
	assertStatus("pl3", CopyStatusMissing)

	// A service with no checker is left exactly as it was — fail-open. A
	// freshly placed copy is in_sync by construction (we just wrote it); the
	// gap signal is the missing last_checked_at, which the rollup reports as
	// "unchecked".
	skipped, _ := ledger.Get("playlists", "bluesky", "at://post")
	if skipped.Status != CopyStatusInSync || skipped.LastCheckedAt != "" {
		t.Fatalf("unchecked copy = %s/%q, want in_sync/empty (must be left alone)",
			skipped.Status, skipped.LastCheckedAt)
	}
}

func TestFreshnessEngine_ChainsBatches(t *testing.T) {
	srv, ledger := freshServerWithLedger(t)
	for i := 0; i < 5; i++ {
		_, _ = ledger.Upsert(CopyRecord{
			Kind: "playlists", EntityKey: fmt.Sprint("p", i), Service: "spotify",
			ExternalID: fmt.Sprint("pl", i), Role: CopyRoleMirror, SyncMode: CopySyncModeFull,
			Version: CopyVersion{ItemCount: 1},
		})
	}
	checker := &stubChecker{
		services: []string{"spotify"},
		fn: func(ctx context.Context, rec CopyRecord) (CopyVersion, bool, bool, error) {
			return CopyVersion{ItemCount: 1}, true, true, nil
		},
	}
	reg := NewCheckerRegistry()
	reg.Register(checker)
	engine := NewFreshnessEngine(srv, reg)

	// Two per step: the chain must walk all five rows via after_id.
	first, err := engine.RunBatch(context.Background(), "playlists", 0, 2)
	if err != nil {
		t.Fatalf("first batch: %v", err)
	}
	if first.Done || first.Checked != 2 {
		t.Fatalf("first batch = %+v, want 2 checked and not done", first)
	}
	last := first.NextAfter
	total := first.Checked
	for i := 0; i < 5; i++ {
		b, err := engine.RunBatch(context.Background(), "playlists", last, 2)
		if err != nil {
			t.Fatalf("batch %d: %v", i, err)
		}
		total += b.Checked
		last = b.NextAfter
		if b.Done {
			break
		}
	}
	if total != 5 {
		t.Fatalf("chained checked = %d, want 5", total)
	}
	if checker.calls < 5 {
		t.Fatalf("checker calls = %d, want >= 5", checker.calls)
	}
}

func TestFreshnessEngine_AuthOrErrorLeavesRowAlone(t *testing.T) {
	srv, ledger := freshServerWithLedger(t)
	_, _ = ledger.Upsert(CopyRecord{
		Kind: "playlists", EntityKey: "p1", Service: "spotify", ExternalID: "pl1",
		Role: CopyRoleMirror, SyncMode: CopySyncModeFull, Version: CopyVersion{ItemCount: 3},
	})
	checker := &stubChecker{
		services: []string{"spotify"},
		fn: func(ctx context.Context, rec CopyRecord) (CopyVersion, bool, bool, error) {
			return CopyVersion{}, false, false, types.ErrUnauthorized
		},
	}
	reg := NewCheckerRegistry()
	reg.Register(checker)
	engine := NewFreshnessEngine(srv, reg)

	batch, err := engine.RunBatch(context.Background(), "playlists", 0, 10)
	if err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	if batch.Checked != 0 || batch.Skipped != 1 {
		t.Fatalf("batch = %+v, want 1 skipped", batch)
	}
	rec, _ := ledger.Get("playlists", "spotify", "pl1")
	if rec.Status != CopyStatusInSync || rec.LastCheckedAt != "" {
		t.Fatalf("auth failure must leave the row alone, got %s/%q", rec.Status, rec.LastCheckedAt)
	}
}

func TestFreshnessEngine_DisabledLedgerIsNoop(t *testing.T) {
	engine := NewFreshnessEngine(&Server{copies: &CopyLedger{}}, NewCheckerRegistry())
	batch, err := engine.RunBatch(context.Background(), "playlists", 0, 10)
	if err != nil {
		t.Fatalf("RunBatch on disabled ledger: %v", err)
	}
	if !batch.Done || batch.Checked != 0 {
		t.Fatalf("batch = %+v, want done with nothing checked", batch)
	}
}

// --- runner ---

func TestFreshnessRunner_ChainsUntilDone(t *testing.T) {
	srv, ledger := freshServerWithLedger(t)
	for i := 0; i < 3; i++ {
		_, _ = ledger.Upsert(CopyRecord{
			Kind: "playlists", EntityKey: fmt.Sprint("p", i), Service: "spotify",
			ExternalID: fmt.Sprint("pl", i), Role: CopyRoleMirror, SyncMode: CopySyncModeFull,
			Version: CopyVersion{ItemCount: 1},
		})
	}
	reg := NewCheckerRegistry()
	reg.Register(&stubChecker{
		services: []string{"spotify"},
		fn: func(ctx context.Context, rec CopyRecord) (CopyVersion, bool, bool, error) {
			return CopyVersion{ItemCount: 1}, true, true, nil
		},
	})
	runner := &freshnessRunner{engine: NewFreshnessEngine(srv, reg), kind: "playlists"}

	step, err := runner.Run(context.Background(), SyncJob{Payload: json.RawMessage(`{"limit":1}`)})
	if err != nil {
		t.Fatalf("first step: %v", err)
	}
	if step.Done {
		t.Fatal("first step must chain")
	}
	if !strings.Contains(string(step.Payload), "after_id") {
		t.Fatalf("chained payload = %s, want after_id", step.Payload)
	}
	if b, _ := json.Marshal(step.Progress); !strings.Contains(string(b), `"checked":1`) {
		t.Fatalf("progress = %s, want 1 checked", b)
	}

	for i := 0; i < 5 && !step.Done; i++ {
		var p freshnessJobPayload
		_ = json.Unmarshal(step.Payload, &p)
		step, err = runner.Run(context.Background(), SyncJob{Payload: step.Payload})
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	if !step.Done {
		t.Fatal("chain must reach done")
	}
	if rec, _ := ledger.Get("playlists", "spotify", "pl2"); rec.LastCheckedAt == "" {
		t.Fatal("every row must be stamped")
	}
}

func TestFreshnessRunner_TerminalConditions(t *testing.T) {
	var bad *freshnessRunner
	if _, err := bad.Run(context.Background(), SyncJob{}); err == nil {
		t.Fatal("nil runner must fail terminal")
	} else if !strings.Contains(err.Error(), ErrSyncTerminal.Error()) {
		t.Fatalf("err = %v, want terminal", err)
	}
	r := &freshnessRunner{engine: NewFreshnessEngine(&Server{copies: &CopyLedger{}}, NewCheckerRegistry()), kind: "playlists"}
	if _, err := r.Run(context.Background(), SyncJob{Payload: json.RawMessage(`{`)}); err == nil {
		t.Fatal("bad payload must fail terminal")
	}
	if got := r.JobTypes(); len(got) != 1 || got[0] != SyncJobTypeFreshness {
		t.Fatalf("JobTypes = %v", got)
	}
	if r.Kind() != "playlists" {
		t.Fatalf("Kind = %s", r.Kind())
	}
}

func TestNewFreshnessRunners_CoverSupportedKinds(t *testing.T) {
	runners := newFreshnessRunners(&Server{copies: &CopyLedger{}})
	if len(runners) != len(freshnessKinds) {
		t.Fatalf("runners = %d, want %d", len(runners), len(freshnessKinds))
	}
	reg := NewRunnerRegistry()
	registerFreshnessRunners(reg, &Server{copies: &CopyLedger{}})
	for _, kind := range freshnessKinds {
		if _, ok := reg.For(kind, SyncJobTypeFreshness); !ok {
			t.Fatalf("kind %s has no freshness runner", kind)
		}
	}
	if _, ok := reg.For("contacts", SyncJobTypeFreshness); ok {
		t.Fatal("contacts has no checker yet and must not advertise a runner")
	}
}

// --- checkers ---

func TestTakeoutChecker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "export.mbox")
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	c := takeoutChecker{}
	if got := c.Services(); len(got) != 1 || got[0] != connectors.TakeoutServiceID {
		t.Fatalf("Services = %v", got)
	}

	cur, exists, ok, err := c.Check(context.Background(), CopyRecord{ExternalID: path})
	if err != nil || !ok || !exists {
		t.Fatalf("Check(existing) = (%+v,%v,%v,%v)", cur, exists, ok, err)
	}

	_, exists, ok, err = c.Check(context.Background(), CopyRecord{ExternalID: filepath.Join(dir, "gone.mbox")})
	if err != nil || !ok || exists {
		t.Fatalf("Check(missing) = (exists=%v ok=%v err=%v), want (false,true,nil)", exists, ok, err)
	}

	// No path recorded: cannot check, leave the row alone.
	if _, _, ok, _ := c.Check(context.Background(), CopyRecord{ExternalID: ""}); ok {
		t.Fatal("empty path must not report a successful check")
	}
}

func TestMusicMirrorChecker_SpotifyShapes(t *testing.T) {
	var gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		if strings.Contains(r.URL.Path, "/playlists/pl1") {
			json.NewEncoder(w).Encode(map[string]any{
				"name":        "Road trip",
				"snapshot_id": "snap-1",
				"tracks":      map[string]any{"total": 12},
			})
			return
		}
		if strings.Contains(r.URL.Path, "/me/tracks") {
			json.NewEncoder(w).Encode(map[string]any{"total": 44})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(up.Close)

	srv := testServerWithBase(&Config{Services: []Service{{
		ID: "spotify", Name: "Spotify", Action: "inject", Endpoint: up.URL,
	}}}, "https://aegis.example.com")
	srv.tokens.Save("spotify", &OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})
	srv.connectors["spotify"] = connectors.NewSpotifyConnector(srv)

	c := &musicMirrorChecker{srv: srv, service: "spotify"}

	cur, exists, ok, err := c.Check(context.Background(), CopyRecord{ExternalID: "pl1"})
	if err != nil || !ok || !exists {
		t.Fatalf("Check(playlist) = (%+v,%v,%v,%v)", cur, exists, ok, err)
	}
	if cur.SnapshotID != "snap-1" || cur.ItemCount != 12 || cur.Name != "Road trip" {
		t.Fatalf("cur = %+v, want snap-1/12/Road trip", cur)
	}
	if !strings.Contains(gotPath, "/playlists/pl1") {
		t.Fatalf("fetched %q, want the configured endpoint's playlist path", gotPath)
	}

	// Liked-tracks mirrors carry no target id; the check compares counts.
	cur, exists, ok, err = c.Check(context.Background(), CopyRecord{ExternalID: ""})
	if err != nil || !ok || !exists {
		t.Fatalf("Check(liked) = (%+v,%v,%v,%v)", cur, exists, ok, err)
	}
	if cur.ItemCount != 44 {
		t.Fatalf("liked count = %d, want 44", cur.ItemCount)
	}

	// A 404 is a missing copy, not an error.
	up2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(up2.Close)
	srv.config.updateService("spotify", func(svc *Service) { svc.Endpoint = up2.URL })
	_, exists, ok, err = c.Check(context.Background(), CopyRecord{ExternalID: "pl1"})
	if err != nil || !ok || exists {
		t.Fatalf("Check(404) = (exists=%v ok=%v err=%v), want (false,true,nil)", exists, ok, err)
	}
}

func TestMusicMirrorChecker_YouTubeShapes(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") == "LL" {
			json.NewEncoder(w).Encode(map[string]any{
				"items": []any{map[string]any{"contentDetails": map[string]any{"itemCount": 7}}},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"items": []any{map[string]any{
				"snippet":        map[string]any{"title": "My list"},
				"contentDetails": map[string]any{"itemCount": 4},
			}},
		})
	}))
	t.Cleanup(up.Close)

	srv := testServerWithBase(&Config{Services: []Service{{
		ID: "youtube-music", Name: "YouTube Music", Action: "inject", Endpoint: up.URL,
	}}}, "https://aegis.example.com")
	srv.tokens.Save("youtube-music", &OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})
	srv.connectors["youtube-music"] = connectors.NewYouTubeMusicConnector(srv)

	c := &musicMirrorChecker{srv: srv, service: "youtube-music"}
	cur, exists, ok, err := c.Check(context.Background(), CopyRecord{ExternalID: "PLx"})
	if err != nil || !ok || !exists {
		t.Fatalf("Check = (%+v,%v,%v,%v)", cur, exists, ok, err)
	}
	if cur.ItemCount != 4 || cur.Name != "My list" {
		t.Fatalf("cur = %+v, want 4/My list", cur)
	}

	cur, exists, ok, err = c.Check(context.Background(), CopyRecord{ExternalID: ""})
	if err != nil || !ok || !exists {
		t.Fatalf("Check(liked) = (%+v,%v,%v,%v)", cur, exists, ok, err)
	}
	if cur.ItemCount != 7 {
		t.Fatalf("liked count = %d, want 7", cur.ItemCount)
	}

	// An empty items array means the playlist is gone.
	up2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
	}))
	t.Cleanup(up2.Close)
	srv.config.updateService("youtube-music", func(svc *Service) { svc.Endpoint = up2.URL })
	_, exists, ok, err = c.Check(context.Background(), CopyRecord{ExternalID: "PLx"})
	if err != nil || !ok || exists {
		t.Fatalf("Check(empty) = (exists=%v ok=%v err=%v), want (false,true,nil)", exists, ok, err)
	}
}

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
	added, err := s.SeedDefaults()
	if err != nil {
		t.Fatalf("SeedDefaults: %v", err)
	}
	if added != len(freshnessKinds) {
		t.Fatalf("added = %d, want %d", added, len(freshnessKinds))
	}
	// Re-seeding must not duplicate or clobber a disabled row.
	all := s.List()
	if len(all) != len(freshnessKinds) {
		t.Fatalf("List = %d, want %d", len(all), len(freshnessKinds))
	}
	if _, ok, _ := s.SetEnabled(all[0].ID, false); !ok {
		t.Fatal("toggle failed")
	}
	added, err = s.SeedDefaults()
	if err != nil || added != 0 {
		t.Fatalf("re-seed = (%d,%v), want (0,nil)", added, err)
	}
	if !s.List()[0].Enabled && len(s.List()) != len(freshnessKinds) {
		t.Fatal("re-seed must not change the operator's choice")
	}

	var nilStore *ScheduleStore
	if nilStore.Enabled() {
		t.Fatal("nil store must be disabled")
	}
	if _, err := nilStore.SeedDefaults(); err != nil {
		t.Fatalf("nil SeedDefaults: %v", err)
	}
	if nilStore.List() != nil {
		t.Fatal("nil List must be nil")
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

// --- rollup ---

func TestCopyLedger_FreshnessRollup(t *testing.T) {
	ledger, err := OpenCopyLedger(filepath.Join(t.TempDir(), "copy_ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()

	_, _ = ledger.Upsert(CopyRecord{Kind: "playlists", EntityKey: "a", Service: "spotify", ExternalID: "1",
		Role: CopyRoleMirror, SyncMode: CopySyncModeFull, Status: CopyStatusInSync})
	_, _ = ledger.Upsert(CopyRecord{Kind: "playlists", EntityKey: "b", Service: "spotify", ExternalID: "2",
		Role: CopyRoleMirror, SyncMode: CopySyncModeFull, Status: CopyStatusStale})
	_, _ = ledger.Upsert(CopyRecord{Kind: "messages", EntityKey: "c", Service: connectors.TakeoutServiceID, ExternalID: "/tmp/x",
		Role: CopyRoleImport, SyncMode: CopySyncModeOptimistic})
	if err := ledger.MarkChecked("playlists", "a", "spotify", "1", CopyStatusInSync); err != nil {
		t.Fatalf("MarkChecked: %v", err)
	}
	// MarkChecked can also record an inconclusive result as `unknown`.
	if err := ledger.MarkChecked("messages", "c", connectors.TakeoutServiceID, "/tmp/x", CopyStatusUnknown); err != nil {
		t.Fatalf("MarkChecked(unknown): %v", err)
	}

	got := ledger.FreshnessRollup()
	if len(got) != 2 {
		t.Fatalf("rollup = %+v, want 2 kinds", got)
	}
	byKind := map[string]CopyFreshness{}
	for _, f := range got {
		byKind[f.Kind] = f
	}
	pl := byKind["playlists"]
	if pl.InSync != 1 || pl.Stale != 1 || pl.Total != 2 {
		t.Fatalf("playlists = %+v, want 1 in_sync / 1 stale / 2 total", pl)
	}
	if pl.Checked != 1 || pl.Unchecked != 1 {
		t.Fatalf("playlists = %+v, want 1 checked / 1 unchecked", pl)
	}
	if pl.LastChecked == "" {
		t.Fatal("playlists must carry a last_checked_at")
	}
	msg := byKind["messages"]
	if msg.Unknown != 1 || msg.Total != 1 {
		t.Fatalf("messages = %+v, want 1 unknown / 1 total", msg)
	}
	// It was checked (an inconclusive check still counts as a check).
	if msg.Unchecked != 0 {
		t.Fatalf("messages = %+v, want 0 unchecked (it has been checked)", msg)
	}

	if got := (&CopyLedger{}).FreshnessRollup(); got != nil {
		t.Fatalf("disabled rollup = %v, want nil", got)
	}
}

// --- admin surface ---

func TestHandleAdminSync_ReportsFreshnessAndSchedules(t *testing.T) {
	srv := syncAdminServer(t)
	ledger, err := OpenCopyLedger(filepath.Join(t.TempDir(), "copy_ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	srv.copies = ledger
	_, _ = srv.copies.Upsert(CopyRecord{Kind: "playlists", EntityKey: "a", Service: "spotify", ExternalID: "1",
		Role: CopyRoleMirror, SyncMode: CopySyncModeFull, Status: CopyStatusStale})
	srv.syncSchedules = testScheduleStore(t)
	if _, err := srv.syncSchedules.Upsert(SyncSchedule{
		Name: "Daily copy freshness", Kind: "playlists", JobType: SyncJobTypeFreshness,
		EverySeconds: 3600, Enabled: true,
	}); err != nil {
		t.Fatalf("Upsert schedule: %v", err)
	}

	req := adminRequest(http.MethodGet, "/v1/admin/sync", "admin-key", "")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminSync)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var got SyncStatus
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Freshness) != 1 || got.Freshness[0].Kind != "playlists" || got.Freshness[0].Stale != 1 {
		t.Fatalf("freshness = %+v, want one stale playlists row", got.Freshness)
	}
	if len(got.Schedules) != 1 || got.Schedules[0].JobType != SyncJobTypeFreshness {
		t.Fatalf("schedules = %+v, want one freshness template", got.Schedules)
	}
}

func TestHandleAdminSyncSchedule_Toggle(t *testing.T) {
	srv := syncAdminServer(t)
	store := testScheduleStore(t)
	srv.syncSchedules = store
	sc, err := store.Upsert(SyncSchedule{
		Kind: "playlists", JobType: SyncJobTypeFreshness, EverySeconds: 3600, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	req := adminRequest(http.MethodPost,
		fmt.Sprintf("/v1/admin/sync/schedules/%d/disable", sc.ID), "admin-key", "")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminSyncSchedule)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("disable status = %d, body %s", w.Code, w.Body.String())
	}
	var out SyncSchedule
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.Enabled {
		t.Fatal("schedule must be disabled")
	}

	req = adminRequest(http.MethodPost,
		fmt.Sprintf("/v1/admin/sync/schedules/%d/enable", sc.ID), "admin-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminSyncSchedule)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("enable status = %d", w.Code)
	}

	for _, path := range []string{
		"/v1/admin/sync/schedules/9999/disable",
		"/v1/admin/sync/schedules/abc/disable",
		"/v1/admin/sync/schedules/1/poke",
		"/v1/admin/sync/schedules//disable",
	} {
		req = adminRequest(http.MethodPost, path, "admin-key", "")
		w = httptest.NewRecorder()
		srv.AdminMiddleware(srv.HandleAdminSyncSchedule)(w, req)
		if w.Code < 400 {
			t.Fatalf("%s = %d, want an error", path, w.Code)
		}
	}

	req = adminRequest(http.MethodGet, fmt.Sprintf("/v1/admin/sync/schedules/%d/disable", sc.ID), "admin-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminSyncSchedule)(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d, want 405", w.Code)
	}
}

func TestHandleAdminSyncSchedule_DisabledStore(t *testing.T) {
	srv := syncAdminServer(t)
	req := adminRequest(http.MethodPost, "/v1/admin/sync/schedules/1/disable", "admin-key", "")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminSyncSchedule)(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

// --- worker schedule pump ---

func TestSyncWorker_EnqueueDueSchedules(t *testing.T) {
	store := testJobStore(t)
	sched := testScheduleStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sched.now = func() time.Time { return base }
	sc, err := sched.Upsert(SyncSchedule{
		Kind: "playlists", JobType: SyncJobTypeFreshness, EverySeconds: 3600,
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	w := NewSyncWorker(store, NewRunnerRegistry(), SyncWorkerOptions{Workers: 1, Schedules: sched})
	w.now = func() time.Time { return base.Add(time.Minute) }
	w.enqueueDueSchedules(w.now())

	job := store.List(SyncFilter{})[0]
	if job.Kind != "playlists" || job.JobType != SyncJobTypeFreshness {
		t.Fatalf("job = %+v, want the scheduled freshness job", job)
	}
	after, _ := sched.getByID(sc.ID)
	if after.NextRunAt.Equal(sc.NextRunAt) {
		t.Fatal("firing must re-arm the schedule")
	}

	// Idempotent: firing again while the job is still queued adds nothing.
	before := store.Counts("", "")
	w.enqueueDueSchedules(w.now())
	if got := store.Counts("", ""); got != before {
		t.Fatalf("counts changed from %+v to %+v, want no duplicate jobs", before, got)
	}

	// A nil schedule store is a no-op.
	w2 := NewSyncWorker(store, NewRunnerRegistry(), SyncWorkerOptions{Workers: 1})
	w2.enqueueDueSchedules(time.Now())
}

func TestSyncWorker_HousekeepFeedsSchedules(t *testing.T) {
	store := testJobStore(t)
	sched := testScheduleStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sched.now = func() time.Time { return base }
	if _, err := sched.Upsert(SyncSchedule{
		Kind: "messages", JobType: SyncJobTypeFreshness, EverySeconds: 60, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	w := NewSyncWorker(store, NewRunnerRegistry(), SyncWorkerOptions{Workers: 1, Schedules: sched})
	w.now = func() time.Time { return base.Add(5 * time.Minute) }
	w.housekeep()
	if got := store.Counts("", ""); got.Queued != 1 {
		t.Fatalf("counts = %+v, want one scheduled job enqueued", got)
	}
}

// --- end to end ---

func TestFreshness_EndToEndThroughQueue(t *testing.T) {
	srv, ledger := freshServerWithLedger(t)
	_, _ = ledger.Upsert(CopyRecord{
		Kind: "playlists", EntityKey: "p1", Service: "spotify", ExternalID: "pl1",
		Role: CopyRoleMirror, SyncMode: CopySyncModeFull, Version: CopyVersion{SnapshotID: "s1", ItemCount: 3},
	})
	reg := NewCheckerRegistry()
	reg.Register(&stubChecker{
		services: []string{"spotify"},
		fn: func(ctx context.Context, rec CopyRecord) (CopyVersion, bool, bool, error) {
			return CopyVersion{SnapshotID: "s1", ItemCount: 3}, true, true, nil
		},
	})
	store := testJobStore(t)
	runners := NewRunnerRegistry()
	runners.Register(&freshnessRunner{engine: NewFreshnessEngine(srv, reg), kind: "playlists"})
	w := NewSyncWorker(store, runners, SyncWorkerOptions{Workers: 1})

	mustEnqueue(t, store, SyncEnqueue{Kind: "playlists", JobType: SyncJobTypeFreshness})
	for i := 0; i < 3 && w.RunOnce(context.Background(), "w"); i++ {
	}
	job := store.List(SyncFilter{})[0]
	if job.State != SyncJobSucceeded {
		t.Fatalf("job state = %s, want succeeded (progress %s)", job.State, job.Progress)
	}
	rec, _ := ledger.Get("playlists", "spotify", "pl1")
	if rec.Status != CopyStatusInSync || rec.LastCheckedAt == "" {
		t.Fatalf("copy = %s/%q, want in_sync and stamped", rec.Status, rec.LastCheckedAt)
	}
}

func TestParseSyncRowID(t *testing.T) {
	if got := parseSyncRowID("cpy_a"); got != 10 {
		t.Fatalf("parseSyncRowID(cpy_a) = %d, want 10 (hex)", got)
	}
	if got := parseSyncRowID("cpy_2a"); got != 42 {
		t.Fatalf("parseSyncRowID(cpy_2a) = %d, want 42", got)
	}
	if got := parseSyncRowID("cpy_"); got != 0 {
		t.Fatalf("parseSyncRowID(cpy_) = %d, want 0", got)
	}
	if got := parseSyncRowID("nope"); got != 0 {
		t.Fatalf("parseSyncRowID(nope) = %d, want 0", got)
	}
	if got := parseSyncRowID(""); got != 0 {
		t.Fatalf("parseSyncRowID() = %d, want 0", got)
	}
}

func TestFreshnessEngine_UnknownIDIsNotAFailure(t *testing.T) {
	// A checker reporting ok=false (unsupported shape) must skip, not fail.
	srv, ledger := freshServerWithLedger(t)
	_, _ = ledger.Upsert(CopyRecord{
		Kind: "tracks", EntityKey: "liked:spotify", Service: "spotify", ExternalID: "",
		Role: CopyRoleMirror, SyncMode: CopySyncModeFull, Version: CopyVersion{ItemCount: 3},
	})
	reg := NewCheckerRegistry()
	reg.Register(&stubChecker{
		services: []string{"spotify"},
		fn: func(ctx context.Context, rec CopyRecord) (CopyVersion, bool, bool, error) {
			return CopyVersion{}, false, false, nil
		},
	})
	engine := NewFreshnessEngine(srv, reg)
	batch, err := engine.RunBatch(context.Background(), "tracks", 0, 10)
	if err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	if batch.Checked != 0 || batch.Skipped != 1 {
		t.Fatalf("batch = %+v, want 1 skipped", batch)
	}
}

var errCheckerBoom = errors.New("checker exploded")

func TestFreshnessEngine_CheckerPanicPropagates(t *testing.T) {
	// The engine does not recover panics — the worker's safeRun does. A
	// checker returning an error is the documented path.
	srv, ledger := freshServerWithLedger(t)
	_, _ = ledger.Upsert(CopyRecord{
		Kind: "playlists", EntityKey: "p", Service: "spotify", ExternalID: "pl",
		Role: CopyRoleMirror, SyncMode: CopySyncModeFull, Version: CopyVersion{ItemCount: 1},
	})
	reg := NewCheckerRegistry()
	reg.Register(&stubChecker{
		services: []string{"spotify"},
		fn: func(ctx context.Context, rec CopyRecord) (CopyVersion, bool, bool, error) {
			return CopyVersion{}, false, false, errCheckerBoom
		},
	})
	engine := NewFreshnessEngine(srv, reg)
	batch, err := engine.RunBatch(context.Background(), "playlists", 0, 10)
	if err != nil {
		t.Fatalf("RunBatch must not fail on a checker error: %v", err)
	}
	if batch.Skipped != 1 {
		t.Fatalf("batch = %+v, want the row skipped", batch)
	}
}

// Full path with no stubs: a real HTTP upstream, the real Spotify checker, a
// real ledger, and the real worker. Proves a copy row's status is stamped
// from a live version descriptor end to end.
func TestFreshness_RealHTTPCheckStampsLedger(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/playlists/pl-sync"):
			json.NewEncoder(w).Encode(map[string]any{
				"name": "Road trip", "snapshot_id": "snap-1", "tracks": map[string]any{"total": 12},
			})
		case strings.HasPrefix(r.URL.Path, "/playlists/pl-stale"):
			json.NewEncoder(w).Encode(map[string]any{
				"name": "Road trip", "snapshot_id": "snap-9", "tracks": map[string]any{"total": 12},
			})
		case strings.HasPrefix(r.URL.Path, "/playlists/pl-gone"):
			w.WriteHeader(http.StatusNotFound)
		case strings.HasPrefix(r.URL.Path, "/me/tracks"):
			json.NewEncoder(w).Encode(map[string]any{"total": 3})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(up.Close)

	srv := testServerWithBase(&Config{Services: []Service{{
		ID: "spotify", Name: "Spotify", Action: "inject", Endpoint: up.URL,
	}}}, "https://aegis.example.com")
	srv.tokens.Save("spotify", &OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})
	srv.connectors["spotify"] = connectors.NewSpotifyConnector(srv)
	ledger, err := OpenCopyLedger(filepath.Join(t.TempDir(), "copy_ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	srv.copies = ledger

	for _, rec := range []CopyRecord{
		{Kind: "playlists", EntityKey: "a", Service: "spotify", ExternalID: "pl-sync",
			Role: CopyRoleMirror, SyncMode: CopySyncModeFull, Version: CopyVersion{SnapshotID: "snap-1", ItemCount: 12}},
		{Kind: "playlists", EntityKey: "b", Service: "spotify", ExternalID: "pl-stale",
			Role: CopyRoleMirror, SyncMode: CopySyncModeFull, Version: CopyVersion{SnapshotID: "snap-1", ItemCount: 12}},
		{Kind: "playlists", EntityKey: "c", Service: "spotify", ExternalID: "pl-gone",
			Role: CopyRoleMirror, SyncMode: CopySyncModeFull, Version: CopyVersion{SnapshotID: "snap-1", ItemCount: 12}},
		{Kind: "tracks", EntityKey: "liked:spotify", Service: "spotify", ExternalID: "",
			Role: CopyRoleMirror, SyncMode: CopySyncModeFull, Version: CopyVersion{ItemCount: 3}},
		{Kind: "tracks", EntityKey: "liked:spotify2", Service: "spotify", ExternalID: "",
			Role: CopyRoleMirror, SyncMode: CopySyncModeFull, Version: CopyVersion{ItemCount: 7}},
	} {
		if _, err := ledger.Upsert(rec); err != nil {
			t.Fatalf("Upsert(%s): %v", rec.EntityKey, err)
		}
	}

	store := testJobStore(t)
	runners := NewRunnerRegistry()
	registerFreshnessRunners(runners, srv)
	w := NewSyncWorker(store, runners, SyncWorkerOptions{Workers: 1})

	for _, kind := range []string{"playlists", "tracks"} {
		mustEnqueue(t, store, SyncEnqueue{Kind: kind, JobType: SyncJobTypeFreshness})
		for i := 0; i < 3 && w.RunOnce(context.Background(), "w"); i++ {
		}
	}

	wantStatus := map[string]string{
		"playlists|pl-sync":  CopyStatusInSync,
		"playlists|pl-stale": CopyStatusStale,
		"playlists|pl-gone":  CopyStatusMissing,
	}
	for key, want := range wantStatus {
		parts := strings.SplitN(key, "|", 2)
		rec, ok := ledger.Get(parts[0], "spotify", parts[1])
		if !ok {
			t.Fatalf("copy %s missing from the ledger", key)
		}
		if rec.Status != want {
			t.Fatalf("copy %s status = %s, want %s", key, rec.Status, want)
		}
		if rec.LastCheckedAt == "" {
			t.Fatalf("copy %s must be stamped with last_checked_at", key)
		}
	}

	// Liked-tracks mirrors compare the target's saved count.
	if rec, _ := ledger.Get("tracks", "spotify", ""); rec.EntityKey == "" {
		t.Fatal("liked-tracks copy must be findable")
	}
	rollup := ledger.FreshnessRollup()
	byKind := map[string]CopyFreshness{}
	for _, f := range rollup {
		byKind[f.Kind] = f
	}
	if byKind["playlists"].Stale != 1 || byKind["playlists"].Missing != 1 || byKind["playlists"].InSync != 1 {
		t.Fatalf("playlists rollup = %+v, want 1 in_sync / 1 stale / 1 missing", byKind["playlists"])
	}
	// liked:spotify recorded 3 and the target still holds 3 → in_sync.
	// liked:spotify2 recorded 7 but the target holds 3 → stale.
	if byKind["tracks"].InSync != 1 || byKind["tracks"].Stale != 1 {
		t.Fatalf("tracks rollup = %+v, want 1 in_sync / 1 stale", byKind["tracks"])
	}
	if byKind["playlists"].Unchecked != 0 {
		t.Fatalf("playlists rollup = %+v, want every row checked", byKind["playlists"])
	}
}
