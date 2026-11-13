package syncjobs

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
	"github.com/fudomunro/aegis/internal/core/copies"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/library"
)

// --- version comparison ---

func TestCompareCopyVersion(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode string
		want copies.Version
		got  copies.Version
		live bool
		out  string
	}{
		{"gone is missing", copies.CopySyncModeFull, copies.Version{ItemCount: 3}, copies.Version{}, false, copies.CopyStatusMissing},
		{"match is in_sync", copies.CopySyncModeFull, copies.Version{SnapshotID: "s1", ItemCount: 3}, copies.Version{SnapshotID: "s1", ItemCount: 3}, true, copies.CopyStatusInSync},
		{"snapshot drift is stale", copies.CopySyncModeFull, copies.Version{SnapshotID: "s1"}, copies.Version{SnapshotID: "s2"}, true, copies.CopyStatusStale},
		{"count drift is stale", copies.CopySyncModeFull, copies.Version{ItemCount: 3}, copies.Version{ItemCount: 4}, true, copies.CopyStatusStale},
		{"etag drift is stale", copies.CopySyncModeFull, copies.Version{ETag: "e1"}, copies.Version{ETag: "e2"}, true, copies.CopyStatusStale},
		{"hash drift is stale", copies.CopySyncModeFull, copies.Version{ContentHash: "h1"}, copies.Version{ContentHash: "h2"}, true, copies.CopyStatusStale},
		// A token the provider does not report must not produce a false stale.
		{"unreported snapshot ignored", copies.CopySyncModeFull, copies.Version{SnapshotID: "s1", ItemCount: 3}, copies.Version{ItemCount: 3}, true, copies.CopyStatusInSync},
		{"unreported count ignored", copies.CopySyncModeFull, copies.Version{SnapshotID: "s1", ItemCount: 3}, copies.Version{SnapshotID: "s1"}, true, copies.CopyStatusInSync},
		// A renamed playlist is not a drifted backup.
		{"name is not staleness", copies.CopySyncModeFull, copies.Version{ItemCount: 3, Name: "old"}, copies.Version{ItemCount: 3, Name: "new"}, true, copies.CopyStatusInSync},
		// Optimistic copies can only report presence.
		{"optimistic drift is not stale", copies.CopySyncModeOptimistic, copies.Version{ItemCount: 3}, copies.Version{ItemCount: 99}, true, copies.CopyStatusInSync},
		{"optimistic gone is missing", copies.CopySyncModeOptimistic, copies.Version{ItemCount: 3}, copies.Version{}, false, copies.CopyStatusMissing},
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
	fn       func(ctx context.Context, rec copies.Record) (copies.Version, bool, bool, error)
	calls    int
}

func (c *stubChecker) Services() []string { return c.services }
func (c *stubChecker) Check(ctx context.Context, rec copies.Record) (copies.Version, bool, bool, error) {
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

func freshServerWithLedger(t *testing.T) (*Runners, *copies.Ledger) {
	t.Helper()
	ledger, err := copies.Open(filepath.Join(t.TempDir(), "copy_ledger.db"))
	if err != nil {
		t.Fatalf("copies.Open: %v", err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	srv := &Runners{Copies: ledger}
	return srv, ledger
}

func TestFreshnessEngine_StampsStatuses(t *testing.T) {
	run, ledger := freshServerWithLedger(t)
	_, _ = ledger.Upsert(copies.Record{
		Kind: "playlists", EntityKey: "p1", Service: "spotify", ExternalID: "pl1",
		Role: copies.CopyRoleMirror, SyncMode: copies.CopySyncModeFull,
		Version: copies.Version{SnapshotID: "s1", ItemCount: 3},
	})
	_, _ = ledger.Upsert(copies.Record{
		Kind: "playlists", EntityKey: "p2", Service: "spotify", ExternalID: "pl2",
		Role: copies.CopyRoleMirror, SyncMode: copies.CopySyncModeFull,
		Version: copies.Version{SnapshotID: "s2", ItemCount: 5},
	})
	_, _ = ledger.Upsert(copies.Record{
		Kind: "playlists", EntityKey: "p3", Service: "spotify", ExternalID: "pl3",
		Role: copies.CopyRoleMirror, SyncMode: copies.CopySyncModeFull,
		Version: copies.Version{SnapshotID: "s3", ItemCount: 1},
	})
	_, _ = ledger.Upsert(copies.Record{
		Kind: "playlists", EntityKey: "p4", Service: "bluesky", ExternalID: "at://post",
		Role: copies.CopyRoleMirror, SyncMode: copies.CopySyncModeFull,
		Version: copies.Version{ContentHash: "h"},
	})

	checker := &stubChecker{
		services: []string{"spotify"},
		fn: func(ctx context.Context, rec copies.Record) (copies.Version, bool, bool, error) {
			switch rec.ExternalID {
			case "pl1":
				return copies.Version{SnapshotID: "s1", ItemCount: 3}, true, true, nil // in sync
			case "pl2":
				return copies.Version{SnapshotID: "s2", ItemCount: 9}, true, true, nil // stale
			case "pl3":
				return copies.Version{}, false, true, nil // missing
			}
			return copies.Version{}, false, false, nil
		},
	}
	reg := NewCheckerRegistry()
	reg.Register(checker)
	engine := NewFreshnessEngine(run, reg)

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
	assertStatus("pl1", copies.CopyStatusInSync)
	assertStatus("pl2", copies.CopyStatusStale)
	assertStatus("pl3", copies.CopyStatusMissing)

	// A service with no checker is left exactly as it was — fail-open. A
	// freshly placed copy is in_sync by construction (we just wrote it); the
	// gap signal is the missing last_checked_at, which the rollup reports as
	// "unchecked".
	skipped, _ := ledger.Get("playlists", "bluesky", "at://post")
	if skipped.Status != copies.CopyStatusInSync || skipped.LastCheckedAt != "" {
		t.Fatalf("unchecked copy = %s/%q, want in_sync/empty (must be left alone)",
			skipped.Status, skipped.LastCheckedAt)
	}
}

func TestFreshnessEngine_ChainsBatches(t *testing.T) {
	run, ledger := freshServerWithLedger(t)
	for i := 0; i < 5; i++ {
		_, _ = ledger.Upsert(copies.Record{
			Kind: "playlists", EntityKey: fmt.Sprint("p", i), Service: "spotify",
			ExternalID: fmt.Sprint("pl", i), Role: copies.CopyRoleMirror, SyncMode: copies.CopySyncModeFull,
			Version: copies.Version{ItemCount: 1},
		})
	}
	checker := &stubChecker{
		services: []string{"spotify"},
		fn: func(ctx context.Context, rec copies.Record) (copies.Version, bool, bool, error) {
			return copies.Version{ItemCount: 1}, true, true, nil
		},
	}
	reg := NewCheckerRegistry()
	reg.Register(checker)
	engine := NewFreshnessEngine(run, reg)

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
	run, ledger := freshServerWithLedger(t)
	_, _ = ledger.Upsert(copies.Record{
		Kind: "playlists", EntityKey: "p1", Service: "spotify", ExternalID: "pl1",
		Role: copies.CopyRoleMirror, SyncMode: copies.CopySyncModeFull, Version: copies.Version{ItemCount: 3},
	})
	checker := &stubChecker{
		services: []string{"spotify"},
		fn: func(ctx context.Context, rec copies.Record) (copies.Version, bool, bool, error) {
			return copies.Version{}, false, false, kinds.ErrUnauthorized
		},
	}
	reg := NewCheckerRegistry()
	reg.Register(checker)
	engine := NewFreshnessEngine(run, reg)

	batch, err := engine.RunBatch(context.Background(), "playlists", 0, 10)
	if err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	if batch.Checked != 0 || batch.Skipped != 1 {
		t.Fatalf("batch = %+v, want 1 skipped", batch)
	}
	rec, _ := ledger.Get("playlists", "spotify", "pl1")
	if rec.Status != copies.CopyStatusInSync || rec.LastCheckedAt != "" {
		t.Fatalf("auth failure must leave the row alone, got %s/%q", rec.Status, rec.LastCheckedAt)
	}
}

func TestFreshnessEngine_DisabledLedgerIsNoop(t *testing.T) {
	engine := NewFreshnessEngine(&Runners{Copies: &copies.Ledger{}}, NewCheckerRegistry())
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
	run, ledger := freshServerWithLedger(t)
	for i := 0; i < 3; i++ {
		_, _ = ledger.Upsert(copies.Record{
			Kind: "playlists", EntityKey: fmt.Sprint("p", i), Service: "spotify",
			ExternalID: fmt.Sprint("pl", i), Role: copies.CopyRoleMirror, SyncMode: copies.CopySyncModeFull,
			Version: copies.Version{ItemCount: 1},
		})
	}
	reg := NewCheckerRegistry()
	reg.Register(&stubChecker{
		services: []string{"spotify"},
		fn: func(ctx context.Context, rec copies.Record) (copies.Version, bool, bool, error) {
			return copies.Version{ItemCount: 1}, true, true, nil
		},
	})
	runner := &freshnessRunner{engine: NewFreshnessEngine(run, reg), kind: "playlists"}

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
	r := &freshnessRunner{engine: NewFreshnessEngine(&Runners{Copies: &copies.Ledger{}}, NewCheckerRegistry()), kind: "playlists"}
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
	runners := newFreshnessRunners(&Runners{Copies: &copies.Ledger{}})
	if len(runners) != len(SyncFreshnessKinds) {
		t.Fatalf("runners = %d, want %d", len(runners), len(SyncFreshnessKinds))
	}
	reg := NewRunnerRegistry()
	registerFreshnessRunners(reg, &Runners{Copies: &copies.Ledger{}})
	for _, kind := range SyncFreshnessKinds {
		if _, ok := reg.For(kind, SyncJobTypeFreshness); !ok {
			t.Fatalf("kind %s has no freshness runner", kind)
		}
	}
	if _, ok := reg.For("photos", SyncJobTypeFreshness); ok {
		t.Fatal("photos has no checker yet and must not advertise a runner")
	}
}

// --- checkers ---

func TestSnapshotChecker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "export.mbox")
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	run := &Runners{MailIngests: map[string]connectors.MailIngest{
		connectors.TakeoutServiceID: connectors.NewTakeoutIngest(),
	}}
	c := &snapshotChecker{run: run, service: connectors.TakeoutServiceID}
	if got := c.Services(); len(got) != 1 || got[0] != connectors.TakeoutServiceID {
		t.Fatalf("Services = %v", got)
	}

	cur, exists, ok, err := c.Check(context.Background(), copies.Record{ExternalID: path})
	if err != nil || !ok || !exists {
		t.Fatalf("Check(existing) = (%+v,%v,%v,%v)", cur, exists, ok, err)
	}
	if cur.Name != path || cur.ExternalID != path {
		t.Fatalf("Check(existing) = %+v, want the recorded export", cur)
	}

	_, exists, ok, err = c.Check(context.Background(), copies.Record{ExternalID: filepath.Join(dir, "gone.mbox")})
	if err != nil || !ok || exists {
		t.Fatalf("Check(missing) = (exists=%v ok=%v err=%v), want (false,true,nil)", exists, ok, err)
	}

	// No path recorded: cannot check, leave the row alone.
	if _, _, ok, _ := c.Check(context.Background(), copies.Record{ExternalID: ""}); ok {
		t.Fatal("empty path must not report a successful check")
	}

	// A source that is not a SnapshotProber is not applicable.
	noop := &snapshotChecker{run: run, service: "nope"}
	if _, _, ok, _ := noop.Check(context.Background(), copies.Record{ExternalID: path}); ok {
		t.Fatal("non-prober source must not report a successful check")
	}
}

func TestMusicMirrorChecker_SpotifyShapes(t *testing.T) {
	var gotPath string
	run, _, _ := connectorTestRunners(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	}), "spotify")

	c := &musicMirrorChecker{run: run, service: "spotify"}

	cur, exists, ok, err := c.Check(context.Background(), copies.Record{ExternalID: "pl1"})
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
	cur, exists, ok, err = c.Check(context.Background(), copies.Record{ExternalID: ""})
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
	svc := run.Host.(*fixtureHost).services["spotify"]
	svc.Endpoint = up2.URL
	run.Host.(*fixtureHost).services["spotify"] = svc
	_, exists, ok, err = c.Check(context.Background(), copies.Record{ExternalID: "pl1"})
	if err != nil || !ok || exists {
		t.Fatalf("Check(404) = (exists=%v ok=%v err=%v), want (false,true,nil)", exists, ok, err)
	}
}

func TestMusicMirrorChecker_YouTubeShapes(t *testing.T) {
	run, _, _ := connectorTestRunners(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	}), "youtube-music")

	c := &musicMirrorChecker{run: run, service: "youtube-music"}
	cur, exists, ok, err := c.Check(context.Background(), copies.Record{ExternalID: "PLx"})
	if err != nil || !ok || !exists {
		t.Fatalf("Check = (%+v,%v,%v,%v)", cur, exists, ok, err)
	}
	if cur.ItemCount != 4 || cur.Name != "My list" {
		t.Fatalf("cur = %+v, want 4/My list", cur)
	}

	cur, exists, ok, err = c.Check(context.Background(), copies.Record{ExternalID: ""})
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
	svc := run.Host.(*fixtureHost).services["youtube-music"]
	svc.Endpoint = up2.URL
	run.Host.(*fixtureHost).services["youtube-music"] = svc
	_, exists, ok, err = c.Check(context.Background(), copies.Record{ExternalID: "PLx"})
	if err != nil || !ok || exists {
		t.Fatalf("Check(empty) = (exists=%v ok=%v err=%v), want (false,true,nil)", exists, ok, err)
	}
}

// --- rollup ---

func TestCopyLedger_FreshnessRollup(t *testing.T) {
	ledger, err := copies.Open(filepath.Join(t.TempDir(), "copy_ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()

	_, _ = ledger.Upsert(copies.Record{Kind: "playlists", EntityKey: "a", Service: "spotify", ExternalID: "1",
		Role: copies.CopyRoleMirror, SyncMode: copies.CopySyncModeFull, Status: copies.CopyStatusInSync})
	_, _ = ledger.Upsert(copies.Record{Kind: "playlists", EntityKey: "b", Service: "spotify", ExternalID: "2",
		Role: copies.CopyRoleMirror, SyncMode: copies.CopySyncModeFull, Status: copies.CopyStatusStale})
	_, _ = ledger.Upsert(copies.Record{Kind: "messages", EntityKey: "c", Service: connectors.TakeoutServiceID, ExternalID: "/tmp/x",
		Role: copies.CopyRoleImport, SyncMode: copies.CopySyncModeOptimistic})
	if err := ledger.MarkChecked("playlists", "a", "spotify", "1", copies.CopyStatusInSync); err != nil {
		t.Fatalf("MarkChecked: %v", err)
	}
	// MarkChecked can also record an inconclusive result as `unknown`.
	if err := ledger.MarkChecked("messages", "c", connectors.TakeoutServiceID, "/tmp/x", copies.CopyStatusUnknown); err != nil {
		t.Fatalf("MarkChecked(unknown): %v", err)
	}

	got := ledger.FreshnessRollup()
	if len(got) != 2 {
		t.Fatalf("rollup = %+v, want 2 kinds", got)
	}
	byKind := map[string]copies.Freshness{}
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

	if got := (&copies.Ledger{}).FreshnessRollup(); got != nil {
		t.Fatalf("disabled rollup = %v, want nil", got)
	}
}

// --- admin surface ---

func TestFreshness_EndToEndThroughQueue(t *testing.T) {
	run, ledger := freshServerWithLedger(t)
	_, _ = ledger.Upsert(copies.Record{
		Kind: "playlists", EntityKey: "p1", Service: "spotify", ExternalID: "pl1",
		Role: copies.CopyRoleMirror, SyncMode: copies.CopySyncModeFull, Version: copies.Version{SnapshotID: "s1", ItemCount: 3},
	})
	reg := NewCheckerRegistry()
	reg.Register(&stubChecker{
		services: []string{"spotify"},
		fn: func(ctx context.Context, rec copies.Record) (copies.Version, bool, bool, error) {
			return copies.Version{SnapshotID: "s1", ItemCount: 3}, true, true, nil
		},
	})
	store := testJobStore(t)
	runners := NewRunnerRegistry()
	runners.Register(&freshnessRunner{engine: NewFreshnessEngine(run, reg), kind: "playlists"})
	w := NewSyncWorker(store, runners, SyncWorkerOptions{Workers: 1})

	mustEnqueue(t, store, SyncEnqueue{Kind: "playlists", JobType: SyncJobTypeFreshness})
	for i := 0; i < 3 && w.RunOnce(context.Background(), "w"); i++ {
	}
	job := store.List(SyncFilter{})[0]
	if job.State != SyncJobSucceeded {
		t.Fatalf("job state = %s, want succeeded (progress %s)", job.State, job.Progress)
	}
	rec, _ := ledger.Get("playlists", "spotify", "pl1")
	if rec.Status != copies.CopyStatusInSync || rec.LastCheckedAt == "" {
		t.Fatalf("copy = %s/%q, want in_sync and stamped", rec.Status, rec.LastCheckedAt)
	}
}

func TestFreshnessEngine_UnknownIDIsNotAFailure(t *testing.T) {
	// A checker reporting ok=false (unsupported shape) must skip, not fail.
	run, ledger := freshServerWithLedger(t)
	_, _ = ledger.Upsert(copies.Record{
		Kind: "tracks", EntityKey: "liked:spotify", Service: "spotify", ExternalID: "",
		Role: copies.CopyRoleMirror, SyncMode: copies.CopySyncModeFull, Version: copies.Version{ItemCount: 3},
	})
	reg := NewCheckerRegistry()
	reg.Register(&stubChecker{
		services: []string{"spotify"},
		fn: func(ctx context.Context, rec copies.Record) (copies.Version, bool, bool, error) {
			return copies.Version{}, false, false, nil
		},
	})
	engine := NewFreshnessEngine(run, reg)
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
	run, ledger := freshServerWithLedger(t)
	_, _ = ledger.Upsert(copies.Record{
		Kind: "playlists", EntityKey: "p", Service: "spotify", ExternalID: "pl",
		Role: copies.CopyRoleMirror, SyncMode: copies.CopySyncModeFull, Version: copies.Version{ItemCount: 1},
	})
	reg := NewCheckerRegistry()
	reg.Register(&stubChecker{
		services: []string{"spotify"},
		fn: func(ctx context.Context, rec copies.Record) (copies.Version, bool, bool, error) {
			return copies.Version{}, false, false, errCheckerBoom
		},
	})
	engine := NewFreshnessEngine(run, reg)
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
	run, _, _ := connectorTestRunners(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	}), "spotify")
	ledger, err := copies.Open(filepath.Join(t.TempDir(), "copy_ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	run.Copies = ledger

	for _, rec := range []copies.Record{
		{Kind: "playlists", EntityKey: "a", Service: "spotify", ExternalID: "pl-sync",
			Role: copies.CopyRoleMirror, SyncMode: copies.CopySyncModeFull, Version: copies.Version{SnapshotID: "snap-1", ItemCount: 12}},
		{Kind: "playlists", EntityKey: "b", Service: "spotify", ExternalID: "pl-stale",
			Role: copies.CopyRoleMirror, SyncMode: copies.CopySyncModeFull, Version: copies.Version{SnapshotID: "snap-1", ItemCount: 12}},
		{Kind: "playlists", EntityKey: "c", Service: "spotify", ExternalID: "pl-gone",
			Role: copies.CopyRoleMirror, SyncMode: copies.CopySyncModeFull, Version: copies.Version{SnapshotID: "snap-1", ItemCount: 12}},
		{Kind: "tracks", EntityKey: "liked:spotify", Service: "spotify", ExternalID: "",
			Role: copies.CopyRoleMirror, SyncMode: copies.CopySyncModeFull, Version: copies.Version{ItemCount: 3}},
		{Kind: "tracks", EntityKey: "liked:spotify2", Service: "spotify", ExternalID: "",
			Role: copies.CopyRoleMirror, SyncMode: copies.CopySyncModeFull, Version: copies.Version{ItemCount: 7}},
	} {
		if _, err := ledger.Upsert(rec); err != nil {
			t.Fatalf("Upsert(%s): %v", rec.EntityKey, err)
		}
	}

	store := testJobStore(t)
	runners := NewRunnerRegistry()
	registerFreshnessRunners(runners, run)
	w := NewSyncWorker(store, runners, SyncWorkerOptions{Workers: 1})

	for _, kind := range []string{"playlists", "tracks"} {
		mustEnqueue(t, store, SyncEnqueue{Kind: kind, JobType: SyncJobTypeFreshness})
		for i := 0; i < 3 && w.RunOnce(context.Background(), "w"); i++ {
		}
	}

	wantStatus := map[string]string{
		"playlists|pl-sync":  copies.CopyStatusInSync,
		"playlists|pl-stale": copies.CopyStatusStale,
		"playlists|pl-gone":  copies.CopyStatusMissing,
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
	byKind := map[string]copies.Freshness{}
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

// --- entity checker (issue #377) ---

// entityContact builds a listed contact carrying the service-local ref and
// etag watermark the probe reads.
func entityContact(service, id, name, etag string) kinds.Contact {
	ct := kinds.NewContact(service, service, id, name, nil, nil)
	ct.Sources[0].Extra = map[string]any{"etag": etag}
	return ct
}

// entityEvent builds a listed event carrying the service-local ref and etag.
func entityEvent(service, id, title, etag string) kinds.CalendarEvent {
	return kinds.CalendarEvent{
		Title: title,
		Sources: []kinds.CalendarEventSource{{
			Service: service,
			ID:      id,
			Extra:   map[string]any{"etag": etag},
		}},
	}
}

func TestEntityChecker_ProbesItemETagAndCount(t *testing.T) {
	member := &entityStubConn{contacts: []kinds.Contact{
		entityContact("icloud", "ada.vcf", "Ada", "e-new"),
		entityContact("icloud", "bob.vcf", "Bob", "e-bob"),
	}}
	run := &Runners{Connectors: map[string]connectors.ServiceConnector{"icloud": member}}
	c := &entityChecker{run: run, service: "icloud"}

	// Per-item rows probe the live etag — the token the DAV list already
	// returns. Only the etag is comparable: entities have no rename
	// resolution, so a name change must land as a record divergence.
	cur, exists, ok, err := c.Check(context.Background(), copies.Record{
		Kind: "contacts", Service: "icloud", ExternalID: "ada.vcf",
	})
	if err != nil || !ok || !exists {
		t.Fatalf("Check(item) = (%+v,%v,%v,%v)", cur, exists, ok, err)
	}
	if cur.ETag != "e-new" || cur.ExternalID != "ada.vcf" {
		t.Fatalf("Check(item) = %+v, want the live etag", cur)
	}
	if cur.Name != "" || cur.ItemCount != 0 {
		t.Fatalf("Check(item) = %+v, want the etag only", cur)
	}

	// The collection row probes the live count — where a member-side
	// create surfaces (the created contact has no watermark row).
	cur, exists, ok, err = c.Check(context.Background(), copies.Record{Kind: "contacts", Service: "icloud"})
	if err != nil || !ok || !exists || cur.ItemCount != 2 {
		t.Fatalf("Check(collection) = (%+v,%v,%v,%v), want 2 items", cur, exists, ok, err)
	}

	// A record the member no longer holds reports missing.
	_, exists, ok, err = c.Check(context.Background(), copies.Record{
		Kind: "contacts", Service: "icloud", ExternalID: "gone.vcf",
	})
	if err != nil || !ok || exists {
		t.Fatalf("Check(gone) = (exists=%v ok=%v err=%v), want (false,true,nil)", exists, ok, err)
	}

	// A provider error is not evidence about the copy.
	member.failList = errors.New("boom")
	if _, _, _, err := c.Check(context.Background(), copies.Record{Kind: "contacts", Service: "icloud"}); err == nil {
		t.Fatal("list failure must surface as an error so the row is left alone")
	}
	member.failList = nil

	// Other kinds and services are not this checker's shape.
	if _, _, ok, _ := c.Check(context.Background(), copies.Record{Kind: "photos", Service: "icloud", ExternalID: "x"}); ok {
		t.Fatal("photos rows must not report a successful check")
	}
	noConn := &entityChecker{run: run, service: "bluesky"}
	if _, _, ok, _ := noConn.Check(context.Background(), copies.Record{Kind: "contacts", Service: "bluesky"}); ok {
		t.Fatal("a service that cannot list contacts must not report a successful check")
	}
}

func TestEntityChecker_EventsShape(t *testing.T) {
	member := &entityStubConn{events: []kinds.CalendarEvent{
		entityEvent("dav-calendar", "ev1.ics", "Standup", "e-1"),
		entityEvent("dav-calendar", "ev2.ics", "Offsite", "e-2"),
	}}
	run := &Runners{Connectors: map[string]connectors.ServiceConnector{"dav-calendar": member}}
	c := &entityChecker{run: run, service: "dav-calendar"}

	cur, exists, ok, err := c.Check(context.Background(), copies.Record{
		Kind: "events", Service: "dav-calendar", ExternalID: "ev1.ics",
	})
	if err != nil || !ok || !exists || cur.ETag != "e-1" {
		t.Fatalf("Check(item) = (%+v,%v,%v,%v), want the live etag", cur, exists, ok, err)
	}
	cur, exists, ok, err = c.Check(context.Background(), copies.Record{Kind: "events", Service: "dav-calendar"})
	if err != nil || !ok || !exists || cur.ItemCount != 2 {
		t.Fatalf("Check(collection) = (%+v,%v,%v,%v), want 2 items", cur, exists, ok, err)
	}
}

func TestEntityChecker_TruncatedListIsUnchecked(t *testing.T) {
	member := &entityStubConn{
		contacts:  []kinds.Contact{entityContact("icloud", "ada.vcf", "Ada", "e")},
		truncated: true,
	}
	run := &Runners{Connectors: map[string]connectors.ServiceConnector{"icloud": member}}
	c := &entityChecker{run: run, service: "icloud"}

	// A capped list can neither prove absence nor a count: rows stay as
	// they are rather than reporting a false missing or a false create.
	if _, _, ok, _ := c.Check(context.Background(), copies.Record{
		Kind: "contacts", Service: "icloud", ExternalID: "ada.vcf",
	}); ok {
		t.Fatal("truncated list must leave item rows alone")
	}
	if _, _, ok, _ := c.Check(context.Background(), copies.Record{Kind: "contacts", Service: "icloud"}); ok {
		t.Fatal("truncated list must leave the collection row alone")
	}
}

func TestEntityChecker_OneListServesTheWalk(t *testing.T) {
	member := &entityStubConn{contacts: []kinds.Contact{
		entityContact("icloud", "ada.vcf", "Ada", "e-ada"),
	}}
	run := &Runners{Connectors: map[string]connectors.ServiceConnector{"icloud": member}}
	c := &entityChecker{run: run, service: "icloud", maxAge: time.Minute}

	coll := copies.Record{Kind: "contacts", Service: "icloud"}
	cur, _, ok, _ := c.Check(context.Background(), coll)
	if !ok || cur.ItemCount != 1 {
		t.Fatalf("first check = %+v (ok=%v), want 1 item", cur, ok)
	}
	// The walk's later rows still read that one list.
	member.contacts = append(member.contacts, entityContact("icloud", "bob.vcf", "Bob", "e-bob"))
	cur, _, ok, _ = c.Check(context.Background(), coll)
	if !ok || cur.ItemCount != 1 {
		t.Fatalf("cached check = %+v (ok=%v), want the walk's first list", cur, ok)
	}

	// No cache bound: every check reads live state.
	fresh := &entityChecker{run: run, service: "icloud"}
	if cur, _, ok, _ := fresh.Check(context.Background(), coll); !ok || cur.ItemCount != 2 {
		t.Fatalf("uncached check = %+v (ok=%v), want live count 2", cur, ok)
	}
}

func TestFreshnessEntities_ClassifiesDivergence(t *testing.T) {
	// The member holds an edited contact (etag diverged from its
	// watermark), a contact created on the member (no watermark row —
	// surfaced by the collection count), and has lost a contact the
	// watermark still records.
	member := &entityStubConn{contacts: []kinds.Contact{
		entityContact("icloud", "ada.vcf", "Ada", "e-new"),
		entityContact("icloud", "cal.vcf", "Cal", "e-cal"),
	}}
	hub := &entityStubConn{contacts: []kinds.Contact{entityContact("google", "g-ada", "Ada", "e-old")}}
	run, _ := convergeStubRunners(t, "contacts", "google", []string{"google", "icloud"},
		map[string]connectors.ServiceConnector{"google": hub, "icloud": member})

	run.Mirror.RecordMirrorItemCopy("contacts", "Ada", "icloud", "ada.vcf", "Ada", "e-old")
	run.Mirror.RecordMirrorItemCopy("contacts", "Gone", "icloud", "gone.vcf", "Gone", "e-gone")
	run.Mirror.RecordMirrorCopy("contacts", "google", "", "icloud", "", "contacts", 1, "", "")

	engine := NewFreshnessEngine(run, newCheckerRegistry(run))
	batch, err := engine.RunBatch(context.Background(), "contacts", 0, 50)
	if err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	if !batch.Done || batch.Checked != 3 {
		t.Fatalf("batch = %+v, want 3 rows checked", batch)
	}
	if batch.Stale != 2 || batch.Missing != 1 {
		t.Fatalf("batch = %+v, want 2 stale (edit + create) and 1 missing", batch)
	}

	open := run.Conflicts.List(ConflictFilter{Status: ConflictStatusOpen})
	if len(open) != 3 {
		t.Fatalf("open conflicts = %+v, want record + missing + items", open)
	}
	byField := map[string]SyncConflict{}
	for _, c := range open {
		byField[c.Field] = c
	}
	rec := byField[ConflictFieldRecord]
	if rec.Subject != "Ada" || rec.ExternalID != "ada.vcf" || rec.Service != "icloud" {
		t.Fatalf("record conflict = %+v, want the member-side edit", rec)
	}
	if rec.EntityKey != kinds.ItemEntityKey("contacts", "Ada") {
		t.Fatalf("record entity key = %q", rec.EntityKey)
	}
	if rec.Source != ConflictSourceFreshness {
		t.Fatalf("record source = %q", rec.Source)
	}
	gone := byField[ConflictFieldMissing]
	if gone.Subject != "Gone" || gone.ExternalID != "gone.vcf" {
		t.Fatalf("missing conflict = %+v, want the vanished member copy", gone)
	}
	items := byField[ConflictFieldItems]
	if items.MemberValue != "2 items" || items.LastValue != "1 items" {
		t.Fatalf("items conflict = %+v, want member 2 vs watermark 1", items)
	}
	if items.EntityKey != library.MirrorEntityKey("contacts", "google", "") {
		t.Fatalf("items entity key = %q", items.EntityKey)
	}
}
