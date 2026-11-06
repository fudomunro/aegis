package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

func testJobStore(t *testing.T) *JobStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sync_jobs.db")
	s, err := OpenJobStore(nil, path)
	if err != nil {
		t.Fatalf("OpenJobStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustEnqueue(t *testing.T, s *JobStore, req SyncEnqueue) SyncJob {
	t.Helper()
	job, err := s.Enqueue(req)
	if err != nil {
		t.Fatalf("Enqueue(%+v): %v", req, err)
	}
	return job
}

func TestJobStore_DisabledIsNoop(t *testing.T) {
	s, err := OpenJobStore(nil, "")
	if err != nil {
		t.Fatalf("OpenJobStore(empty): %v", err)
	}
	if s.Enabled() {
		t.Fatal("empty path must yield a disabled store")
	}
	// Enqueue still validates input but must not persist.
	if _, err := s.Enqueue(SyncEnqueue{Kind: "messages", JobType: "ingest"}); err != nil {
		t.Fatalf("disabled Enqueue: %v", err)
	}
	if _, ok, err := s.Claim("w", time.Now()); err != nil || ok {
		t.Fatalf("disabled Claim = (%v,%v), want (zero,false,nil)", ok, err)
	}
	if got := s.List(SyncFilter{}); got != nil {
		t.Fatalf("disabled List = %v, want nil", got)
	}
	if got := s.Counts("", ""); got != (SyncCounts{}) {
		t.Fatalf("disabled Counts = %+v, want zero", got)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("disabled Close: %v", err)
	}
}

func TestJobStore_DisabledRequiresKindAndType(t *testing.T) {
	s, err := OpenJobStore(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []SyncEnqueue{
		{JobType: "ingest"},
		{Kind: "messages"},
		{},
	} {
		if _, err := s.Enqueue(req); err == nil {
			t.Fatalf("Enqueue(%+v) must fail validation", req)
		}
	}
}

func TestJobStore_ClaimLeaseAndReclaim(t *testing.T) {
	s := testJobStore(t)
	now := time.Now().UTC()

	job := mustEnqueue(t, s, SyncEnqueue{Kind: "messages", JobType: "ingest", Service: "google-gmail"})
	if job.State != SyncJobQueued {
		t.Fatalf("state = %s, want queued", job.State)
	}

	// Nothing is due in the future.
	if _, ok, err := s.Claim("w1", now.Add(-time.Hour)); err != nil || ok {
		t.Fatalf("past Claim = (%v,%v), want nothing due", ok, err)
	}

	claimed, ok, err := s.Claim("w1", now.Add(time.Second))
	if err != nil || !ok {
		t.Fatalf("Claim = (%v,%v), want a job", ok, err)
	}
	if claimed.State != SyncJobRunning {
		t.Fatalf("claimed state = %s, want running", claimed.State)
	}
	if claimed.LeaseOwner != "w1" {
		t.Fatalf("lease owner = %q, want w1", claimed.LeaseOwner)
	}
	if claimed.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", claimed.Attempts)
	}
	if claimed.LeaseUntil.IsZero() {
		t.Fatal("lease_until must be set")
	}

	// A second worker cannot steal a live lease.
	if _, ok, err := s.Claim("w2", now.Add(2*time.Second)); err != nil || ok {
		t.Fatalf("steal Claim = (%v,%v), want nothing", ok, err)
	}

	// Expired lease is reclaimable by anyone (crash recovery).
	far := now.Add(syncJobLease + time.Minute)
	reclaimed, ok, err := s.Claim("w2", far)
	if err != nil || !ok {
		t.Fatalf("reclaim Claim = (%v,%v), want the job", ok, err)
	}
	if reclaimed.ID != claimed.ID {
		t.Fatalf("reclaimed id = %s, want %s", reclaimed.ID, claimed.ID)
	}
	if reclaimed.LeaseOwner != "w2" {
		t.Fatalf("reclaimed owner = %q, want w2", reclaimed.LeaseOwner)
	}
	if reclaimed.Attempts != 2 {
		t.Fatalf("reclaimed attempts = %d, want 2", reclaimed.Attempts)
	}
}

func TestJobStore_ReclaimExpired(t *testing.T) {
	s := testJobStore(t)
	now := time.Now().UTC()
	mustEnqueue(t, s, SyncEnqueue{Kind: "messages", JobType: "ingest"})
	if _, ok, _ := s.Claim("w1", now.Add(time.Second)); !ok {
		t.Fatal("expected a claim")
	}
	n, err := s.ReclaimExpired(now.Add(time.Second))
	if err != nil || n != 0 {
		t.Fatalf("ReclaimExpired(live lease) = (%d,%v), want (0,nil)", n, err)
	}
	n, err = s.ReclaimExpired(now.Add(syncJobLease + time.Minute))
	if err != nil || n != 1 {
		t.Fatalf("ReclaimExpired(stale lease) = (%d,%v), want (1,nil)", n, err)
	}
	job, ok := s.GetByKey("messages", "ingest", "", "")
	if !ok {
		t.Fatal("job must still exist")
	}
	if job.State != SyncJobQueued {
		t.Fatalf("state = %s, want queued after reclaim", job.State)
	}
}

func TestJobStore_EnqueueIdempotent(t *testing.T) {
	s := testJobStore(t)
	first := mustEnqueue(t, s, SyncEnqueue{
		Kind: "messages", JobType: "ingest", Service: "google-gmail",
		Payload: map[string]any{"max_messages": 10},
	})

	// Repeat on a queued job must not clobber or duplicate it.
	second := mustEnqueue(t, s, SyncEnqueue{
		Kind: "messages", JobType: "ingest", Service: "google-gmail",
		Payload: map[string]any{"max_messages": 99},
	})
	if second.ID != first.ID {
		t.Fatalf("repeat Enqueue created a new row: %s vs %s", second.ID, first.ID)
	}
	if second.State != SyncJobQueued {
		t.Fatalf("repeat Enqueue state = %s, want queued", second.State)
	}
	if second.Attempts != 0 {
		t.Fatalf("repeat Enqueue attempts = %d, want 0", second.Attempts)
	}
	if got := len(s.List(SyncFilter{})); got != 1 {
		t.Fatalf("List len = %d, want 1", got)
	}

	// A distinct entity key is a distinct job.
	other := mustEnqueue(t, s, SyncEnqueue{
		Kind: "messages", JobType: "ingest", Service: "google-gmail", EntityKey: "batch-2",
	})
	if other.ID == first.ID {
		t.Fatal("distinct entity_key must be a distinct job")
	}
}

func TestJobStore_EnqueueResetsTerminal(t *testing.T) {
	s := testJobStore(t)
	job := mustEnqueue(t, s, SyncEnqueue{Kind: "messages", JobType: "ingest", Service: "google-gmail"})

	claimed, ok, _ := s.Claim("w", time.Now().UTC().Add(time.Second))
	if !ok {
		t.Fatal("expected a claim")
	}
	if _, err := s.Complete(claimed, SyncStep{Done: true}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	done, _ := s.GetByID(job.ID)
	if done.State != SyncJobSucceeded {
		t.Fatalf("state = %s, want succeeded", done.State)
	}

	// Re-enqueue after success resets the row to queued with a fresh budget.
	again := mustEnqueue(t, s, SyncEnqueue{Kind: "messages", JobType: "ingest", Service: "google-gmail"})
	if again.ID != job.ID {
		t.Fatalf("re-enqueue id = %s, want %s", again.ID, job.ID)
	}
	if again.State != SyncJobQueued {
		t.Fatalf("re-enqueue state = %s, want queued", again.State)
	}
	if again.Attempts != 0 {
		t.Fatalf("re-enqueue attempts = %d, want 0", again.Attempts)
	}
	if again.LastError != "" || !again.FinishedAt.IsZero() {
		t.Fatalf("re-enqueue must clear error/finished: %+v", again)
	}
}

func TestJobStore_CompleteChainsSteps(t *testing.T) {
	s := testJobStore(t)
	job := mustEnqueue(t, s, SyncEnqueue{
		Kind: "messages", JobType: "ingest", Service: "google-gmail",
		Payload: map[string]any{"max_messages": 10},
	})
	claimed, ok, _ := s.Claim("w", time.Now().UTC().Add(time.Second))
	if !ok {
		t.Fatal("expected a claim")
	}

	next := SyncStep{
		Done:     false,
		Payload:  json.RawMessage(`{"max_messages":20}`),
		Progress: map[string]any{"stored": 10},
	}
	chained, err := s.Complete(claimed, next)
	if err != nil {
		t.Fatalf("Complete(continue): %v", err)
	}
	if chained.State != SyncJobQueued {
		t.Fatalf("chained state = %s, want queued", chained.State)
	}
	if !strings.Contains(string(chained.Payload), "20") {
		t.Fatalf("chained payload = %s, want the next-step payload", chained.Payload)
	}
	if chained.Attempts != 0 {
		t.Fatalf("chained attempts = %d, want 0 (success resets the budget)", chained.Attempts)
	}
	if !strings.Contains(string(chained.Progress), "stored") {
		t.Fatalf("chained progress = %s, want stored count", chained.Progress)
	}
	if chained.FinishedAt.IsZero() == false {
		t.Fatal("a chained job must not look finished")
	}

	// Same row id — chaining does not spawn a second job.
	if chained.ID != job.ID {
		t.Fatalf("chained id = %s, want %s", chained.ID, job.ID)
	}

	final, ok, _ := s.Claim("w", time.Now().UTC().Add(time.Second))
	if !ok {
		t.Fatal("expected the chained step to be claimable")
	}
	succeeded, err := s.Complete(final, SyncStep{Done: true, Progress: map[string]any{"stored": 20}})
	if err != nil {
		t.Fatalf("Complete(done): %v", err)
	}
	if succeeded.State != SyncJobSucceeded {
		t.Fatalf("final state = %s, want succeeded", succeeded.State)
	}
	if succeeded.FinishedAt.IsZero() {
		t.Fatal("succeeded job must carry finished_at")
	}
}

func TestJobStore_FailRetryAndTerminal(t *testing.T) {
	s := testJobStore(t)

	// Retryable failure re-queues with a backoff and keeps the attempt count.
	job := mustEnqueue(t, s, SyncEnqueue{Kind: "messages", JobType: "ingest", Service: "google-gmail"})
	claimed, ok, _ := s.Claim("w", time.Now().UTC().Add(time.Second))
	if !ok {
		t.Fatal("expected a claim")
	}
	retried, err := s.Fail(claimed, SyncStep{}, errors.New("quota exceeded"))
	if err != nil {
		t.Fatalf("Fail(retryable): %v", err)
	}
	if retried.ID != job.ID {
		t.Fatalf("retried id = %s, want %s", retried.ID, job.ID)
	}
	if retried.State != SyncJobQueued {
		t.Fatalf("retry state = %s, want queued", retried.State)
	}
	if retried.Attempts != 1 {
		t.Fatalf("retry attempts = %d, want 1", retried.Attempts)
	}
	if retried.LastErrorClass != SyncErrorClassRetry {
		t.Fatalf("class = %s, want retryable", retried.LastErrorClass)
	}
	if !retried.NextRunAt.After(time.Now().UTC()) {
		t.Fatalf("next_run_at = %v, want a future backoff", retried.NextRunAt)
	}

	// Explicit SyncRetryAfter overrides the default backoff.
	claimed2, ok, _ := s.Claim("w", time.Now().UTC().Add(24*time.Hour))
	if !ok {
		t.Fatal("expected a second claim after the backoff window")
	}
	soon := time.Now().UTC().Add(time.Hour)
	resched, err := s.Fail(claimed2, SyncStep{}, &SyncRetryAfter{
		Err:   errors.New("quota"),
		After: time.Hour,
	})
	if err != nil {
		t.Fatalf("Fail(retry-after): %v", err)
	}
	if resched.NextRunAt.Before(soon.Add(-time.Minute)) {
		t.Fatalf("next_run_at = %v, want about an hour out", resched.NextRunAt)
	}

	// Terminal failure parks the job regardless of attempts left.
	term := mustEnqueue(t, s, SyncEnqueue{Kind: "playlists", JobType: "ingest", Service: "spotify"})
	tclaim, ok, _ := s.Claim("w", time.Now().UTC().Add(time.Second))
	if !ok {
		t.Fatal("expected a claim")
	}
	failed, err := s.Fail(tclaim, SyncStep{}, fmt.Errorf("%w: service blocked", ErrSyncTerminal))
	if err != nil {
		t.Fatalf("Fail(terminal): %v", err)
	}
	if failed.State != SyncJobFailed {
		t.Fatalf("terminal state = %s, want failed", failed.State)
	}
	if failed.LastErrorClass != SyncErrorClassTerminal {
		t.Fatalf("class = %s, want terminal", failed.LastErrorClass)
	}
	if failed.FinishedAt.IsZero() {
		t.Fatal("failed job must carry finished_at")
	}
	if term.ID != failed.ID {
		t.Fatalf("terminal job id drifted: %s vs %s", term.ID, failed.ID)
	}
}

func TestJobStore_FailExhaustsAttempts(t *testing.T) {
	s := testJobStore(t)
	mustEnqueue(t, s, SyncEnqueue{
		Kind: "messages", JobType: "ingest", MaxAttempts: 2,
	})
	now := time.Now().UTC().Add(time.Second)
	for i := 0; i < 2; i++ {
		claimed, ok, _ := s.Claim("w", now.Add(time.Duration(i)*time.Hour))
		if !ok {
			t.Fatalf("claim %d: expected a job", i)
		}
		res, err := s.Fail(claimed, SyncStep{}, errors.New("boom"))
		if err != nil {
			t.Fatalf("Fail %d: %v", i, err)
		}
		if i == 0 && res.State != SyncJobQueued {
			t.Fatalf("after attempt 1 state = %s, want queued", res.State)
		}
		if i == 1 && res.State != SyncJobFailed {
			t.Fatalf("after attempt 2 state = %s, want failed (budget exhausted)", res.State)
		}
	}
}

func TestJobStore_CancelPruneCountsList(t *testing.T) {
	s := testJobStore(t)
	a := mustEnqueue(t, s, SyncEnqueue{Kind: "messages", JobType: "ingest", Service: "google-gmail"})
	b := mustEnqueue(t, s, SyncEnqueue{Kind: "playlists", JobType: "mirror", Service: "spotify"})

	canceled, err := s.Cancel(a.ID)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if canceled.State != SyncJobCanceled {
		t.Fatalf("cancel state = %s, want canceled", canceled.State)
	}
	// Cancel is idempotent-ish: a canceled job stays canceled.
	again, err := s.Cancel(a.ID)
	if err != nil {
		t.Fatalf("Cancel again: %v", err)
	}
	if again.State != SyncJobCanceled {
		t.Fatalf("second cancel state = %s, want canceled", again.State)
	}

	counts := s.Counts("", "")
	if counts.Canceled != 1 || counts.Queued != 1 {
		t.Fatalf("counts = %+v, want one canceled and one queued", counts)
	}
	if got := s.Counts("playlists", ""); got.Queued != 1 || got.Canceled != 0 {
		t.Fatalf("by-kind counts = %+v, want playlists queued only", got)
	}
	if got := s.Counts("", "ingest"); got.Canceled != 1 {
		t.Fatalf("by-type counts = %+v, want ingest canceled", got)
	}

	if got := s.List(SyncFilter{Kind: "messages"}); len(got) != 1 || got[0].ID != a.ID {
		t.Fatalf("List(kind=messages) = %v, want only %s", got, a.ID)
	}
	if got := s.List(SyncFilter{JobType: "mirror"}); len(got) != 1 || got[0].ID != b.ID {
		t.Fatalf("List(job_type=mirror) = %v, want only %s", got, b.ID)
	}
	if got := s.List(SyncFilter{State: SyncJobCanceled}); len(got) != 1 {
		t.Fatalf("List(state=canceled) = %v, want 1 row", got)
	}

	// Prune drops only finished jobs older than the retention window.
	n, err := s.Prune(time.Hour)
	if err != nil || n != 0 {
		t.Fatalf("Prune(fresh) = (%d,%v), want (0,nil)", n, err)
	}
	n, err = s.Prune(0)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("Prune(0) = %d, want 1 (the canceled row)", n)
	}
	if got := s.List(SyncFilter{}); len(got) != 1 {
		t.Fatalf("after prune List = %v, want the queued row only", got)
	}
}

func TestSyncBackoff(t *testing.T) {
	seen := map[time.Duration]bool{}
	var prevMid time.Duration
	for attempt := 1; attempt <= 8; attempt++ {
		d := syncBackoff(attempt)
		if d <= 0 {
			t.Fatalf("attempt %d backoff = %v, want > 0", attempt, d)
		}
		// 25% jitter keeps us within the cap plus one jitter window.
		if d > 30*time.Minute+30*time.Minute/4+time.Second {
			t.Fatalf("attempt %d backoff = %v, want <= cap + jitter", attempt, d)
		}
		seen[d] = true
		if attempt <= 5 {
			// The delay grows exponentially up to the cap.
			mid := d - time.Duration(0)
			if attempt > 1 && mid < prevMid/2 {
				t.Fatalf("attempt %d backoff = %v, want >= half of %v (exponential)", attempt, d, prevMid)
			}
			prevMid = mid
		}
	}
	if len(seen) < 4 {
		t.Fatalf("backoffs clustered too tightly: %v", seen)
	}
	if syncBackoff(0) <= 0 {
		t.Fatal("attempt 0 must still produce a delay")
	}
	// Attempt 1 vs attempt 4 must differ by roughly the exponential factor.
	if syncBackoff(1) >= syncBackoff(4) {
		t.Fatal("backoff must grow with the attempt number")
	}
}

func TestClassifySyncError(t *testing.T) {
	class, after := classifySyncError(nil, 1, 0)
	if class != SyncErrorClassRetry || after <= 0 {
		t.Fatalf("nil error = (%s,%v), want (retryable, >0)", class, after)
	}
	// The backoff must be exponential in the attempt number, not fixed.
	_, early := classifySyncError(errors.New("boom"), 1, 0)
	_, late := classifySyncError(errors.New("boom"), 5, 0)
	if late <= early {
		t.Fatalf("attempt 5 backoff = %v, want > attempt 1 (%v)", late, early)
	}
	class, _ = classifySyncError(ErrSyncTerminal, 1, 0)
	if class != SyncErrorClassTerminal {
		t.Fatalf("terminal error class = %s", class)
	}
	class, _ = classifySyncError(fmt.Errorf("wrapped: %w", ErrSyncTerminal), 1, 0)
	if class != SyncErrorClassTerminal {
		t.Fatalf("wrapped terminal class = %s", class)
	}
	// No usable token cannot be fixed by retrying — park it.
	class, _ = classifySyncError(types.ErrUnauthorized, 1, 0)
	if class != SyncErrorClassTerminal {
		t.Fatalf("unauthorized class = %s, want terminal", class)
	}
	class, _ = classifySyncError(fmt.Errorf("sync paused: %w", types.ErrUnauthorized), 1, 0)
	if class != SyncErrorClassTerminal {
		t.Fatalf("wrapped unauthorized class = %s, want terminal", class)
	}
	class, after = classifySyncError(&SyncRetryAfter{Err: errors.New("q"), After: time.Minute}, 1, 0)
	if class != SyncErrorClassRetry || after != time.Minute {
		t.Fatalf("retry-after = (%s,%v), want (retryable, 1m)", class, after)
	}
	class, after = classifySyncError(&SyncRetryAfter{Err: errors.New("q")}, 1, 2*time.Second)
	if after != 2*time.Second {
		t.Fatalf("zero RetryAfter keeps the hint: %v", after)
	}
	if class != SyncErrorClassRetry {
		t.Fatalf("class = %s", class)
	}
}

func TestSyncRetryAfterError(t *testing.T) {
	inner := errors.New("inner")
	e := &SyncRetryAfter{Err: inner, After: time.Minute}
	if e.Error() != "inner" {
		t.Fatalf("Error() = %q", e.Error())
	}
	if !errors.Is(e, inner) {
		t.Fatal("Unwrap must reach the inner error")
	}
	var zero *SyncRetryAfter
	if zero.Error() != "sync retry" {
		t.Fatalf("nil Error() = %q", zero.Error())
	}
	if zero.Unwrap() != nil {
		t.Fatal("nil Unwrap must be nil")
	}
}

// --- worker ---

type stubRunner struct {
	kind     string
	jobTypes []string
	fn       func(ctx context.Context, job SyncJob) (SyncStep, error)
	calls    int
}

func (r *stubRunner) JobTypes() []string { return r.jobTypes }
func (r *stubRunner) Kind() string       { return r.kind }
func (r *stubRunner) Run(ctx context.Context, job SyncJob) (SyncStep, error) {
	r.calls++
	return r.fn(ctx, job)
}

func TestRunnerRegistry(t *testing.T) {
	reg := NewRunnerRegistry()
	if _, ok := reg.For("messages", "ingest"); ok {
		t.Fatal("empty registry must miss")
	}
	r := &stubRunner{kind: "messages", jobTypes: []string{"ingest", "repair"}}
	reg.Register(r)
	got, ok := reg.For("messages", "ingest")
	if !ok || got != r {
		t.Fatalf("For(ingest) = (%v,%v), want the runner", got, ok)
	}
	if _, ok := reg.For("messages", "repair"); !ok {
		t.Fatal("For(repair) must hit")
	}
	if _, ok := reg.For("messages", "mirror"); ok {
		t.Fatal("For(mirror) must miss")
	}
	kinds := reg.Kinds()
	if len(kinds) != 1 || kinds[0] != "messages" {
		t.Fatalf("Kinds = %v, want [messages]", kinds)
	}
	// Later registration replaces earlier ones (test hook).
	second := &stubRunner{kind: "messages", jobTypes: []string{"ingest"}}
	reg.Register(second)
	if got, _ := reg.For("messages", "ingest"); got != second {
		t.Fatal("registration must replace")
	}
	// Nil-safe.
	var nilReg *RunnerRegistry
	nilReg.Register(r)
	if _, ok := nilReg.For("x", "y"); ok {
		t.Fatal("nil registry must miss")
	}
	if nilReg.Kinds() != nil {
		t.Fatal("nil registry Kinds must be nil")
	}
}

func TestSyncWorker_RunsJobToSuccess(t *testing.T) {
	store := testJobStore(t)
	reg := NewRunnerRegistry()
	runner := &stubRunner{
		kind:     "messages",
		jobTypes: []string{"ingest"},
		fn: func(ctx context.Context, job SyncJob) (SyncStep, error) {
			return SyncStep{Done: true, Progress: map[string]any{"stored": 3}}, nil
		},
	}
	reg.Register(runner)

	var doneJobs []SyncJob
	w := NewSyncWorker(store, reg, SyncWorkerOptions{
		Workers: 1,
		Prom:    NewPromMetrics(),
		OnDone:  func(job SyncJob, ok bool) { doneJobs = append(doneJobs, job) },
	})
	mustEnqueue(t, store, SyncEnqueue{Kind: "messages", JobType: "ingest", Service: "google-gmail"})

	if !w.RunOnce(context.Background(), "test-owner") {
		t.Fatal("RunOnce must report work done")
	}
	if runner.calls != 1 {
		t.Fatalf("runner calls = %d, want 1", runner.calls)
	}
	job := store.List(SyncFilter{})[0]
	if job.State != SyncJobSucceeded {
		t.Fatalf("state = %s, want succeeded", job.State)
	}
	if len(doneJobs) != 1 {
		t.Fatalf("OnDone calls = %d, want 1", len(doneJobs))
	}
	if got := w.LastSuccess(); got["messages|google-gmail"].IsZero() {
		t.Fatalf("LastSuccess = %v, want a timestamp", got)
	}
	// Nothing left to claim.
	if w.RunOnce(context.Background(), "test-owner") {
		t.Fatal("second RunOnce must find no work")
	}
}

func TestSyncWorker_ChainsUntilDone(t *testing.T) {
	store := testJobStore(t)
	reg := NewRunnerRegistry()
	steps := 0
	runner := &stubRunner{
		kind:     "messages",
		jobTypes: []string{"ingest"},
		fn: func(ctx context.Context, job SyncJob) (SyncStep, error) {
			steps++
			if steps < 3 {
				return SyncStep{Payload: json.RawMessage(fmt.Sprintf(`{"step":%d}`, steps))}, nil
			}
			return SyncStep{Done: true}, nil
		},
	}
	reg.Register(runner)
	w := NewSyncWorker(store, reg, SyncWorkerOptions{Workers: 1})
	mustEnqueue(t, store, SyncEnqueue{Kind: "messages", JobType: "ingest"})

	for i := 0; i < 5 && w.RunOnce(context.Background(), "w"); i++ {
	}
	if steps != 3 {
		t.Fatalf("steps = %d, want 3", steps)
	}
	job := store.List(SyncFilter{})[0]
	if job.State != SyncJobSucceeded {
		t.Fatalf("state = %s, want succeeded", job.State)
	}
	if job.Attempts != 0 {
		t.Fatalf("attempts = %d, want 0 (each success resets the budget)", job.Attempts)
	}
}

func TestSyncWorker_RetriesThenFails(t *testing.T) {
	store := testJobStore(t)
	reg := NewRunnerRegistry()
	runner := &stubRunner{
		kind:     "messages",
		jobTypes: []string{"ingest"},
		fn: func(ctx context.Context, job SyncJob) (SyncStep, error) {
			return SyncStep{}, errors.New("upstream 502")
		},
	}
	reg.Register(runner)
	w := NewSyncWorker(store, reg, SyncWorkerOptions{
		Workers: 1,
		Prom:    NewPromMetrics(),
	})
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return base }
	store.now = w.now
	mustEnqueue(t, store, SyncEnqueue{
		Kind: "messages", JobType: "ingest", MaxAttempts: 2, RunAt: base,
	})

	// First failure schedules a backoff; the worker must not see work again
	// until the clock passes next_run_at.
	if !w.RunOnce(context.Background(), "w") {
		t.Fatal("first RunOnce must claim")
	}
	if w.RunOnce(context.Background(), "w") {
		t.Fatal("second RunOnce must find nothing due during the backoff")
	}
	mid := store.List(SyncFilter{})[0]
	if mid.State != SyncJobQueued || mid.Attempts != 1 {
		t.Fatalf("after 1 failure = %s/%d, want queued/1", mid.State, mid.Attempts)
	}

	// Second failure exhausts max_attempts=2 and parks the job.
	w.now = func() time.Time { return base.Add(2 * time.Hour) }
	if !w.RunOnce(context.Background(), "w") {
		t.Fatal("third RunOnce must claim after the backoff")
	}
	final := store.List(SyncFilter{})[0]
	if final.State != SyncJobFailed {
		t.Fatalf("after 2 failures state = %s, want failed", final.State)
	}
	if runner.calls != 2 {
		t.Fatalf("runner calls = %d, want 2", runner.calls)
	}

	// An unregistered (kind, job_type) parks immediately as terminal.
	realClock := func() time.Time { return time.Now().UTC() }
	w.now = realClock
	store.now = realClock
	mustEnqueue(t, store, SyncEnqueue{Kind: "photos", JobType: "ingest"})
	if !w.RunOnce(context.Background(), "w") {
		t.Fatal("RunOnce must claim the orphan job")
	}
	orphan := store.List(SyncFilter{Kind: "photos"})[0]
	if orphan.State != SyncJobFailed {
		t.Fatalf("orphan state = %s, want failed (no runner is terminal)", orphan.State)
	}
	if orphan.LastErrorClass != SyncErrorClassTerminal {
		t.Fatalf("orphan class = %s, want terminal", orphan.LastErrorClass)
	}
}

func TestSyncWorker_QuotaUsesRetryAfter(t *testing.T) {
	store := testJobStore(t)
	reg := NewRunnerRegistry()
	runner := &stubRunner{
		kind:     "messages",
		jobTypes: []string{"ingest"},
		fn: func(ctx context.Context, job SyncJob) (SyncStep, error) {
			return SyncStep{RetryAfter: 90 * time.Second}, &SyncRetryAfter{
				Err:   errors.New("Quota exceeded"),
				After: 90 * time.Second,
			}
		},
	}
	reg.Register(runner)
	w := NewSyncWorker(store, reg, SyncWorkerOptions{Workers: 1})
	mustEnqueue(t, store, SyncEnqueue{Kind: "messages", JobType: "ingest"})

	if !w.RunOnce(context.Background(), "w") {
		t.Fatal("RunOnce must claim")
	}
	job := store.List(SyncFilter{})[0]
	if job.State != SyncJobQueued {
		t.Fatalf("state = %s, want queued (retry scheduled)", job.State)
	}
	delta := job.NextRunAt.Sub(time.Now().UTC())
	if delta < time.Minute || delta > 2*time.Minute {
		t.Fatalf("next_run_at in %v, want roughly the 90s quota window", delta)
	}
}

func TestSyncWorker_PanicIsRetryableAndPoolSurvives(t *testing.T) {
	store := testJobStore(t)
	reg := NewRunnerRegistry()
	panicking := &stubRunner{
		kind:     "messages",
		jobTypes: []string{"ingest"},
		fn: func(ctx context.Context, job SyncJob) (SyncStep, error) {
			panic("boom")
		},
	}
	healthy := &stubRunner{
		kind:     "playlists",
		jobTypes: []string{"ingest"},
		fn: func(ctx context.Context, job SyncJob) (SyncStep, error) {
			return SyncStep{Done: true}, nil
		},
	}
	reg.Register(panicking)
	reg.Register(healthy)
	w := NewSyncWorker(store, reg, SyncWorkerOptions{Workers: 1})
	mustEnqueue(t, store, SyncEnqueue{Kind: "messages", JobType: "ingest"})
	mustEnqueue(t, store, SyncEnqueue{Kind: "playlists", JobType: "ingest"})

	if !w.RunOnce(context.Background(), "w") {
		t.Fatal("first RunOnce must claim")
	}
	if !w.RunOnce(context.Background(), "w") {
		t.Fatal("second RunOnce must claim (pool alive after panic)")
	}
	msg := store.List(SyncFilter{Kind: "messages"})[0]
	if msg.State != SyncJobQueued {
		t.Fatalf("panicked job state = %s, want queued (retryable)", msg.State)
	}
	if !strings.Contains(msg.LastError, "panic") {
		t.Fatalf("panic not recorded: %q", msg.LastError)
	}
	pl := store.List(SyncFilter{Kind: "playlists"})[0]
	if pl.State != SyncJobSucceeded {
		t.Fatalf("healthy job state = %s, want succeeded", pl.State)
	}
}

func TestSyncWorker_StartStop(t *testing.T) {
	store := testJobStore(t)
	reg := NewRunnerRegistry()
	reg.Register(&stubRunner{
		kind:     "messages",
		jobTypes: []string{"ingest"},
		fn:       func(ctx context.Context, job SyncJob) (SyncStep, error) { return SyncStep{Done: true}, nil },
	})
	w := NewSyncWorker(store, reg, SyncWorkerOptions{Workers: 2, Poll: 10 * time.Millisecond})
	w.Start()
	w.Start() // second Start is a no-op
	mustEnqueue(t, store, SyncEnqueue{Kind: "messages", JobType: "ingest"})
	w.Kick()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if store.Counts("", "").Succeeded == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if store.Counts("", "").Succeeded != 1 {
		t.Fatalf("counts = %+v, want the job succeeded", store.Counts("", ""))
	}
	w.Stop()
	w.Stop() // second Stop is a no-op
}

func TestSyncWorker_NilSafe(t *testing.T) {
	var w *SyncWorker
	w.Start()
	w.Stop()
	w.Kick()
	if w.RunOnce(context.Background(), "x") {
		t.Fatal("nil worker must not run")
	}
	if w.LastSuccess() != nil {
		t.Fatal("nil worker LastSuccess must be nil")
	}
}

func TestSyncWorker_DisabledStore(t *testing.T) {
	store, err := OpenJobStore(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	w := NewSyncWorker(store, NewRunnerRegistry(), SyncWorkerOptions{Workers: 1})
	if w.RunOnce(context.Background(), "w") {
		t.Fatal("disabled store must not produce work")
	}
	w.recordQueueDepth() // must not panic
}

// --- admin surface ---

func syncAdminServer(t *testing.T) *Server {
	t.Helper()
	srv := adminSplitServer(t)
	store := testJobStore(t)
	srv.syncJobs = store
	// Mirror production: the registry lives on the Server so admin enqueue
	// can validate even when the worker pool is off.
	srv.syncRunners = newSyncRunnerRegistry(srv)
	srv.syncWorker = NewSyncWorker(store, srv.syncRunners, SyncWorkerOptions{Workers: 1})
	return srv
}

func TestHandleAdminSync_GetStatus(t *testing.T) {
	srv := syncAdminServer(t)
	mustEnqueue(t, srv.syncJobs, SyncEnqueue{Kind: "messages", JobType: "ingest", Service: "google-gmail"})

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
	if !got.Enabled {
		t.Fatal("enabled must be true")
	}
	if got.Counts.Queued != 1 {
		t.Fatalf("counts = %+v, want one queued", got.Counts)
	}
	if len(got.Jobs) != 1 {
		t.Fatalf("jobs = %v, want one", got.Jobs)
	}
	if got.ByKind["messages"].Queued != 1 {
		t.Fatalf("by_kind = %v, want messages/queued", got.ByKind)
	}
	if got.ByJobType["ingest"].Queued != 1 {
		t.Fatalf("by_job_type = %v, want ingest/queued", got.ByJobType)
	}
	if len(got.Kinds) == 0 || got.Kinds[0] != "messages" {
		t.Fatalf("kinds = %v, want [messages]", got.Kinds)
	}
}

func TestHandleAdminSync_EnqueueAndCancel(t *testing.T) {
	srv := syncAdminServer(t)

	body := `{"kind":"messages","job_type":"ingest","service":"google-gmail","payload":{"max_messages":5}}`
	req := adminRequest(http.MethodPost, "/v1/admin/sync", "admin-key", body)
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminSync)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("enqueue status = %d, body %s", w.Code, w.Body.String())
	}
	var job SyncJob
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if job.ID == "" || job.State != SyncJobQueued {
		t.Fatalf("job = %+v, want a queued job with an id", job)
	}
	if !strings.Contains(string(job.Payload), "5") {
		t.Fatalf("payload = %s, want max_messages=5", job.Payload)
	}

	// Repeat enqueue dedupes on the service entity key.
	req = adminRequest(http.MethodPost, "/v1/admin/sync", "admin-key", body)
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminSync)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("second enqueue status = %d", w.Code)
	}
	var again SyncJob
	_ = json.Unmarshal(w.Body.Bytes(), &again)
	if again.ID != job.ID {
		t.Fatalf("repeat enqueue id = %s, want %s (idempotent)", again.ID, job.ID)
	}

	// Cancel by id.
	req = adminRequest(http.MethodPost, "/v1/admin/sync/"+job.ID+"/cancel", "admin-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminSyncCancel)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, body %s", w.Code, w.Body.String())
	}
	var canceled SyncJob
	_ = json.Unmarshal(w.Body.Bytes(), &canceled)
	if canceled.State != SyncJobCanceled {
		t.Fatalf("cancel state = %s, want canceled", canceled.State)
	}

	// Unknown id is a 404.
	req = adminRequest(http.MethodPost, "/v1/admin/sync/9999/cancel", "admin-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminSyncCancel)(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown cancel status = %d, want 404", w.Code)
	}
}

func TestHandleAdminSync_Validation(t *testing.T) {
	srv := syncAdminServer(t)

	for _, body := range []string{
		`{}`,
		`{"kind":"messages"}`,
		`{"job_type":"ingest"}`,
		`{"kind":"messages","job_type":"freshness_check"}`, // no runner registered
	} {
		req := adminRequest(http.MethodPost, "/v1/admin/sync", "admin-key", body)
		w := httptest.NewRecorder()
		srv.AdminMiddleware(srv.HandleAdminSync)(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body %s → status = %d, want 400", body, w.Code)
		}
	}

	req := adminRequest(http.MethodPost, "/v1/admin/sync", "admin-key", `not-json`)
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminSync)(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad json status = %d, want 400", w.Code)
	}

	req = adminRequest(http.MethodPut, "/v1/admin/sync", "admin-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminSync)(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT status = %d, want 405", w.Code)
	}

	req = adminRequest(http.MethodGet, "/v1/admin/sync/nope/cancel", "admin-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminSyncCancel)(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET cancel status = %d, want 405", w.Code)
	}
}

func TestHandleAdminSync_DisabledQueue(t *testing.T) {
	srv := adminSplitServer(t)
	// No syncJobs wired: enqueue must report the queue is off, not panic.
	req := adminRequest(http.MethodPost, "/v1/admin/sync", "admin-key", `{"kind":"messages","job_type":"ingest"}`)
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminSync)(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled enqueue status = %d, want 503", w.Code)
	}

	req = adminRequest(http.MethodPost, "/v1/admin/sync/1/cancel", "admin-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminSyncCancel)(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled cancel status = %d, want 503", w.Code)
	}

	// GET still answers (fail-open read model).
	req = adminRequest(http.MethodGet, "/v1/admin/sync", "admin-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminSync)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("disabled GET status = %d, want 200", w.Code)
	}
}

func TestHandleAdminSync_RequiresAdminAuth(t *testing.T) {
	srv := adminSplitServer(t)
	store := testJobStore(t)
	srv.syncJobs = store
	srv.syncRunners = newSyncRunnerRegistry(srv)
	srv.syncWorker = NewSyncWorker(store, srv.syncRunners, SyncWorkerOptions{Workers: 1})

	// Agent key must not reach the admin sync surface.
	req := adminRequest(http.MethodGet, "/v1/admin/sync", "agent-key", "")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminSync)(w, req)
	if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
		t.Fatalf("agent GET status = %d, want 401/403", w.Code)
	}

	req = adminRequest(http.MethodPost, "/v1/admin/sync", "agent-key", `{"kind":"messages","job_type":"ingest"}`)
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminSync)(w, req)
	if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
		t.Fatalf("agent POST status = %d, want 401/403", w.Code)
	}
	if store.Counts("", "").Total() != 0 {
		t.Fatal("an unauthenticated enqueue must not create jobs")
	}

	// No credentials at all.
	req = adminRequest(http.MethodGet, "/v1/admin/sync", "", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminSync)(w, req)
	if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
		t.Fatalf("anonymous GET status = %d, want 401/403", w.Code)
	}
}

func TestSyncRoutes_Registered(t *testing.T) {
	srv := syncAdminServer(t)
	mux := http.NewServeMux()
	registerAdminRoutes(mux, srv)

	req := adminRequest(http.MethodGet, "/v1/admin/sync", "admin-key", "")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("routed GET /v1/admin/sync = %d, body %s", w.Code, w.Body.String())
	}

	body := `{"kind":"messages","job_type":"ingest"}`
	req = adminRequest(http.MethodPost, "/v1/admin/sync", "admin-key", body)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("routed POST /v1/admin/sync = %d, body %s", w.Code, w.Body.String())
	}
}

// --- prom metrics ---

func TestPromMetrics_SyncSeries(t *testing.T) {
	p := NewPromMetrics()
	p.observeSyncJob("ingest", "google-gmail", SyncJobSucceeded, 1500*time.Millisecond)
	p.observeSyncJob("ingest", "google-gmail", SyncJobQueued, 10*time.Millisecond)
	p.observeSyncJob("ingest", "", SyncJobSucceeded, time.Millisecond)
	p.observeSyncRetry("ingest", SyncErrorClassRetry)
	p.observeSyncRetry("", "")
	p.setSyncQueueDepth(SyncCounts{Queued: 2, Running: 1, Succeeded: 7})
	p.noteSyncSuccess("messages", "google-gmail", time.Unix(1700000000, 0).UTC())

	out := p.promText()
	for _, want := range []string{
		`aegis_sync_jobs_total{job_type="ingest",state="succeeded"}`,
		`aegis_sync_jobs_total{job_type="ingest",state="queued"}`,
		`aegis_sync_retries_total{job_type="ingest",error_class="retryable"}`,
		`aegis_sync_retries_total{job_type="unknown",error_class="unknown"}`,
		`aegis_sync_job_duration_seconds_count{job_type="ingest",service="google-gmail"} 2`,
		`aegis_sync_job_duration_seconds_count{job_type="ingest",service="none"} 1`,
		`aegis_sync_queue_depth{state="queued"} 2`,
		`aegis_sync_queue_depth{state="running"} 1`,
		`aegis_sync_queue_depth{state="succeeded"} 7`,
		`aegis_sync_last_success_timestamp_seconds{kind="messages",service="google-gmail"}`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("metrics output missing %q\n%s", want, out)
		}
	}

	// Nil-safe.
	var nilProm *PromMetrics
	nilProm.observeSyncJob("ingest", "s", SyncJobSucceeded, time.Second)
	nilProm.observeSyncRetry("ingest", "retryable")
	nilProm.setSyncQueueDepth(SyncCounts{})
	nilProm.noteSyncSuccess("messages", "s", time.Now())
	if nilProm.promText() != "" {
		t.Fatal("nil prom must render empty")
	}
}

func TestJobStore_SharedHandleCoLocates(t *testing.T) {
	dir := t.TempDir()
	ledgerPath := filepath.Join(dir, "copy_ledger.db")
	ledger, err := OpenCopyLedger(ledgerPath)
	if err != nil {
		t.Fatalf("OpenCopyLedger: %v", err)
	}
	defer ledger.Close()
	if ledger.DB() == nil {
		t.Fatal("ledger DB must be exposed for co-located stores")
	}

	// Same path as the ledger → share its handle.
	store, err := OpenJobStore(ledger.DB(), ledgerPath)
	if err != nil {
		t.Fatalf("OpenJobStore(shared): %v", err)
	}
	if !store.Enabled() {
		t.Fatal("shared store must be enabled")
	}
	if store.DB() != ledger.DB() {
		t.Fatal("shared store must reuse the ledger handle")
	}
	// Shared handles are not closed by the job store.
	if err := store.Close(); err != nil {
		t.Fatalf("Close(shared): %v", err)
	}
	if !ledger.Enabled() {
		t.Fatal("ledger must stay open after the shared store closes")
	}

	mustEnqueue(t, store, SyncEnqueue{Kind: "messages", JobType: "ingest", Service: "google-gmail"})
	// Both tables live in one file: a copy row and a job row share the handle.
	if _, err := ledger.Upsert(CopyRecord{
		Kind: "messages", EntityKey: "m1", Service: "google-gmail", ExternalID: "g1",
	}); err != nil {
		t.Fatalf("Upsert on shared handle: %v", err)
	}
	if _, ok := store.GetByKey("messages", "ingest", "google-gmail", ""); !ok {
		t.Fatal("job must be visible from the shared handle")
	}
	if _, ok := ledger.Get("messages", "google-gmail", "g1"); !ok {
		t.Fatal("copy row must be visible from the shared handle")
	}
}

func TestSyncJobIdempotencyKey(t *testing.T) {
	a := syncJobIdempotencyKey("messages", "ingest", "google-gmail", "")
	b := syncJobIdempotencyKey(" messages ", "ingest", "google-gmail", "")
	if a != b {
		t.Fatalf("key must trim input: %q vs %q", a, b)
	}
	if a == syncJobIdempotencyKey("messages", "ingest", "google-gmail", "batch-1") {
		t.Fatal("distinct entity keys must differ")
	}
	if strings.Contains(a, " ") {
		t.Fatalf("key %q must not contain spaces", a)
	}
}

// --- regression: review findings on #228 ---

// A repeat enqueue must resolve to its own row. LastInsertId on an upsert
// still reports a previous insert on the connection (and the handle is shared
// with the copy ledger in co-located mode), so the id must come from
// RETURNING id rather than the driver's last-insert hint.
func TestEnqueue_UpsertResolvesOwnRow(t *testing.T) {
	s := testJobStore(t)
	a := mustEnqueue(t, s, SyncEnqueue{Kind: "messages", JobType: "ingest", Service: "svc-a"})
	b := mustEnqueue(t, s, SyncEnqueue{Kind: "messages", JobType: "ingest", Service: "svc-b"})
	if a.ID == b.ID {
		t.Fatalf("distinct keys must be distinct rows, both %s", a.ID)
	}

	again := mustEnqueue(t, s, SyncEnqueue{Kind: "messages", JobType: "ingest", Service: "svc-a"})
	if again.ID != a.ID {
		t.Fatalf("re-enqueue(a) returned id %s (service %q), want %s (service %q)",
			again.ID, again.Service, a.ID, a.Service)
	}
	if again.Service != "svc-a" {
		t.Fatalf("re-enqueue(a) service = %q, want svc-a", again.Service)
	}

	// Same hazard with a shared handle: a copy-ledger insert between the two
	// enqueues must not leak its rowid into the job lookup.
	ledger, err := OpenCopyLedger(filepath.Join(t.TempDir(), "copy_ledger.db"))
	if err != nil {
		t.Fatalf("OpenCopyLedger: %v", err)
	}
	defer ledger.Close()
	shared, err := OpenJobStore(ledger.DB(), ledger.Path())
	if err != nil {
		t.Fatalf("OpenJobStore(shared): %v", err)
	}
	x := mustEnqueue(t, shared, SyncEnqueue{Kind: "photos", JobType: "ingest", Service: "x"})
	if _, err := ledger.Upsert(CopyRecord{
		Kind: "photos", EntityKey: "p1", Service: "x", ExternalID: "e1",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	y := mustEnqueue(t, shared, SyncEnqueue{Kind: "photos", JobType: "ingest", Service: "y"})
	if _, err := ledger.Upsert(CopyRecord{
		Kind: "photos", EntityKey: "p2", Service: "y", ExternalID: "e2",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	againX := mustEnqueue(t, shared, SyncEnqueue{Kind: "photos", JobType: "ingest", Service: "x"})
	if againX.ID != x.ID || againX.Service != "x" {
		t.Fatalf("shared re-enqueue(x) = %s/%q, want %s/x", againX.ID, againX.Service, x.ID)
	}
	if y.ID == x.ID {
		t.Fatalf("shared distinct keys collided: %s", x.ID)
	}
}

// Cancelling a running job must stick: a stale step finishing afterwards may
// not drag it back to queued, and the step is reported as lost.
func TestComplete_AfterCancelDoesNotResurrect(t *testing.T) {
	s := testJobStore(t)
	job := mustEnqueue(t, s, SyncEnqueue{Kind: "messages", JobType: "ingest", Service: "google-gmail"})
	claimed, ok, _ := s.Claim("w1", time.Now().UTC().Add(time.Second))
	if !ok {
		t.Fatal("expected a claim")
	}
	canceled, err := s.Cancel(job.ID)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if canceled.State != SyncJobCanceled {
		t.Fatalf("cancel state = %s, want canceled", canceled.State)
	}

	if _, err := s.Complete(claimed, SyncStep{Done: false}); !errors.Is(err, ErrSyncJobLost) {
		t.Fatalf("Complete after cancel err = %v, want ErrSyncJobLost", err)
	}
	if _, err := s.Fail(claimed, SyncStep{}, errors.New("boom")); !errors.Is(err, ErrSyncJobLost) {
		t.Fatalf("Fail after cancel err = %v, want ErrSyncJobLost", err)
	}
	final, _ := s.GetByID(job.ID)
	if final.State != SyncJobCanceled {
		t.Fatalf("state = %s, want canceled (a stale step must not resurrect it)", final.State)
	}
}

// A step whose lease expired and was reclaimed by another worker must not
// overwrite the new owner's result.
func TestComplete_LostLeaseDoesNotClobber(t *testing.T) {
	s := testJobStore(t)
	mustEnqueue(t, s, SyncEnqueue{Kind: "messages", JobType: "ingest", Service: "google-gmail"})
	stale, ok, _ := s.Claim("w1", time.Now().UTC().Add(time.Second))
	if !ok {
		t.Fatal("expected a claim")
	}

	// Another worker reclaims after the lease expires.
	if _, err := s.ReclaimExpired(time.Now().UTC().Add(syncJobLease + time.Minute)); err != nil {
		t.Fatalf("ReclaimExpired: %v", err)
	}
	fresh, ok, _ := s.Claim("w2", time.Now().UTC().Add(syncJobLease+2*time.Minute))
	if !ok {
		t.Fatal("expected the reclaimed job to be claimable")
	}
	if fresh.LeaseOwner != "w2" {
		t.Fatalf("fresh owner = %q, want w2", fresh.LeaseOwner)
	}

	if _, err := s.Complete(stale, SyncStep{Done: true}); !errors.Is(err, ErrSyncJobLost) {
		t.Fatalf("stale Complete err = %v, want ErrSyncJobLost", err)
	}
	running, _ := s.GetByID(fresh.ID)
	if running.State != SyncJobRunning || running.LeaseOwner != "w2" {
		t.Fatalf("job = %s/%q, want running/w2 (stale step must not clobber)", running.State, running.LeaseOwner)
	}

	// The rightful owner still finishes normally.
	done, err := s.Complete(fresh, SyncStep{Done: true})
	if err != nil {
		t.Fatalf("Complete(fresh): %v", err)
	}
	if done.State != SyncJobSucceeded {
		t.Fatalf("final state = %s, want succeeded", done.State)
	}
}

// Re-enqueueing a finished job must not inherit the previous run's progress
// totals (mergeMailProgress accumulates, so a carried-over base would double).
func TestEnqueue_ResetsProgressAndStartedAt(t *testing.T) {
	s := testJobStore(t)
	job := mustEnqueue(t, s, SyncEnqueue{
		Kind: "messages", JobType: "ingest", Service: "google-gmail",
		Payload: map[string]any{"stored": 10},
	})
	claimed, ok, _ := s.Claim("w", time.Now().UTC().Add(time.Second))
	if !ok {
		t.Fatal("expected a claim")
	}
	finished, err := s.Complete(claimed, SyncStep{
		Done:     true,
		Progress: map[string]any{"stored": 10},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !strings.Contains(string(finished.Progress), `"stored":10`) {
		t.Fatalf("progress = %s, want the run's totals", finished.Progress)
	}
	if finished.StartedAt.IsZero() {
		t.Fatal("finished job must carry started_at")
	}

	fresh := mustEnqueue(t, s, SyncEnqueue{
		Kind: "messages", JobType: "ingest", Service: "google-gmail",
	})
	if fresh.ID != job.ID {
		t.Fatalf("id = %s, want %s", fresh.ID, job.ID)
	}
	if string(fresh.Progress) != "{}" {
		t.Fatalf("progress = %s, want {} (a new run must not inherit totals)", fresh.Progress)
	}
	if !fresh.StartedAt.IsZero() {
		t.Fatalf("started_at = %v, want zeroed", fresh.StartedAt)
	}
}

// A repeat enqueue must not yank the cursor out of an in-flight chain.
func TestEnqueue_PreservesInFlightPayload(t *testing.T) {
	s := testJobStore(t)
	mustEnqueue(t, s, SyncEnqueue{
		Kind: "messages", JobType: "ingest", Service: "google-gmail",
		Payload:     map[string]any{"reset_cursor": true, "max_messages": 5},
		MaxAttempts: 3,
	})
	claimed, ok, _ := s.Claim("w", time.Now().UTC().Add(time.Second))
	if !ok {
		t.Fatal("expected a claim")
	}
	// Chain to the next step, which drops reset_cursor as designed.
	chained, err := s.Complete(claimed, SyncStep{
		Payload: json.RawMessage(`{"service":"google-gmail","max_messages":5}`),
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if strings.Contains(string(chained.Payload), "reset_cursor") {
		t.Fatalf("chained payload = %s, want reset_cursor dropped after success", chained.Payload)
	}

	// A repeat POST mid-chain must not put it back or change the budget.
	again := mustEnqueue(t, s, SyncEnqueue{
		Kind: "messages", JobType: "ingest", Service: "google-gmail",
		Payload:     map[string]any{"reset_cursor": true, "max_messages": 99},
		MaxAttempts: 1,
	})
	if strings.Contains(string(again.Payload), "reset_cursor") {
		t.Fatalf("payload = %s, want in-flight payload preserved", again.Payload)
	}
	if !strings.Contains(string(again.Payload), `"max_messages":5`) {
		t.Fatalf("payload = %s, want the chain's max_messages preserved", again.Payload)
	}
	if again.MaxAttempts != 3 {
		t.Fatalf("max_attempts = %d, want 3 (in-flight budget preserved)", again.MaxAttempts)
	}
	if again.Attempts != 0 {
		t.Fatalf("attempts = %d, want 0 (a chained success resets it)", again.Attempts)
	}
}

// A queued job is pulled forward by an explicit enqueue so "run now" beats a
// pending backoff; a running job is left alone.
func TestEnqueue_PullsQueuedForwardLeavesRunning(t *testing.T) {
	s := testJobStore(t)
	s.now = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
	base := s.now()
	later := base.Add(30 * time.Minute)

	backedOff := mustEnqueue(t, s, SyncEnqueue{
		Kind: "messages", JobType: "ingest", Service: "google-gmail", RunAt: later,
	})
	if !backedOff.NextRunAt.Equal(later) {
		t.Fatalf("next_run_at = %v, want %v", backedOff.NextRunAt, later)
	}
	pulled := mustEnqueue(t, s, SyncEnqueue{
		Kind: "messages", JobType: "ingest", Service: "google-gmail", RunAt: base,
	})
	if pulled.NextRunAt.After(base.Add(time.Second)) {
		t.Fatalf("next_run_at = %v, want pulled forward to %v", pulled.NextRunAt, base)
	}

	// Isolated store so the claim below targets exactly one job.
	s2 := testJobStore(t)
	s2.now = s.now
	running := mustEnqueue(t, s2, SyncEnqueue{
		Kind: "playlists", JobType: "ingest", Service: "spotify", RunAt: base,
	})
	claimed, ok, _ := s2.Claim("w", base)
	if !ok || claimed.ID != running.ID {
		t.Fatalf("claim = (%v,%s), want job %s", ok, claimed.ID, running.ID)
	}
	untouched := mustEnqueue(t, s2, SyncEnqueue{
		Kind: "playlists", JobType: "ingest", Service: "spotify", RunAt: later,
	})
	if !untouched.NextRunAt.Equal(base) {
		t.Fatalf("running job next_run_at = %v, want left at %v", untouched.NextRunAt, base)
	}
	if untouched.State != SyncJobRunning {
		t.Fatalf("state = %s, want running (untouched)", untouched.State)
	}
	if running.ID != untouched.ID {
		t.Fatalf("id drifted: %s vs %s", running.ID, untouched.ID)
	}
}

// Startup recovery must not wait out the 30-minute lease.
func TestReclaimAllRunning(t *testing.T) {
	s := testJobStore(t)
	mustEnqueue(t, s, SyncEnqueue{Kind: "messages", JobType: "ingest"})
	mustEnqueue(t, s, SyncEnqueue{Kind: "playlists", JobType: "ingest"})
	for i := 0; i < 2; i++ {
		if _, ok, _ := s.Claim("w", time.Now().UTC().Add(time.Second)); !ok {
			t.Fatalf("claim %d: expected a job", i)
		}
	}
	// A live lease is NOT reclaimable by the expiry sweep...
	if n, err := s.ReclaimExpired(time.Now().UTC().Add(time.Second)); err != nil || n != 0 {
		t.Fatalf("ReclaimExpired(live) = (%d,%v), want (0,nil)", n, err)
	}
	// ...but startup recovery reclaims everything the previous process left.
	n, err := s.ReclaimAllRunning()
	if err != nil || n != 2 {
		t.Fatalf("ReclaimAllRunning = (%d,%v), want (2,nil)", n, err)
	}
	if got := s.Counts("", ""); got.Queued != 2 || got.Running != 0 {
		t.Fatalf("counts = %+v, want two queued", got)
	}
}

// Breakdown must roll up the whole table, not just a recent-job window.
func TestJobStore_BreakdownIsGlobal(t *testing.T) {
	s := testJobStore(t)
	for i := 0; i < 5; i++ {
		mustEnqueue(t, s, SyncEnqueue{
			Kind: "messages", JobType: "ingest", Service: "google-gmail",
			EntityKey: fmt.Sprint("batch-", i),
		})
	}
	b := s.Breakdown()
	if b.ByKind["messages"].Queued != 5 {
		t.Fatalf("by_kind = %+v, want messages/queued 5", b.ByKind)
	}
	if b.ByJobType["ingest"].Queued != 5 {
		t.Fatalf("by_job_type = %+v, want ingest/queued 5", b.ByJobType)
	}
	if b.ByService["google-gmail"].Queued != 5 {
		t.Fatalf("by_service = %+v, want google-gmail/queued 5", b.ByService)
	}
}

// -sync-workers 0 leaves the worker nil while the store is enabled. Enqueue
// must still validate and record work without panicking on the runner lookup.
func TestHandleAdminSync_WorkerDisabledStillValidates(t *testing.T) {
	srv := adminSplitServer(t)
	srv.syncJobs = testJobStore(t)
	srv.syncRunners = newSyncRunnerRegistry(srv)
	// No syncWorker on purpose: the pool is off.

	req := adminRequest(http.MethodGet, "/v1/admin/sync", "admin-key", "")
	w := httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminSync)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", w.Code)
	}
	var status SyncStatus
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !status.Enabled {
		t.Fatal("the queue is enabled even with the worker off")
	}
	if status.Workers != 0 {
		t.Fatalf("workers = %d, want 0", status.Workers)
	}

	// Unknown job type is still rejected (validation does not need the pool).
	req = adminRequest(http.MethodPost, "/v1/admin/sync", "admin-key",
		`{"kind":"messages","job_type":"freshness_check"}`)
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminSync)(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown runner status = %d, want 400", w.Code)
	}

	req = adminRequest(http.MethodPost, "/v1/admin/sync", "admin-key",
		`{"kind":"messages","job_type":"ingest","service":"google-gmail"}`)
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminSync)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("enqueue status = %d, body %s", w.Code, w.Body.String())
	}
	if got := srv.syncJobs.Counts("", ""); got.Queued != 1 {
		t.Fatalf("counts = %+v, want one queued", got)
	}

	// Cancel with no worker must not panic either.
	var job SyncJob
	_ = json.Unmarshal(w.Body.Bytes(), &job)
	req = adminRequest(http.MethodPost, "/v1/admin/sync/"+job.ID+"/cancel", "admin-key", "")
	w = httptest.NewRecorder()
	srv.AdminMiddleware(srv.HandleAdminSyncCancel)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, body %s", w.Code, w.Body.String())
	}
}

// A terminal failure on a first attempt is counted as failed, not queued:
// the metric must follow the state the store landed on.
func TestSyncWorker_MetricStateFollowsStore(t *testing.T) {
	store := testJobStore(t)
	reg := NewRunnerRegistry()
	reg.Register(&stubRunner{
		kind:     "messages",
		jobTypes: []string{"ingest"},
		fn: func(ctx context.Context, job SyncJob) (SyncStep, error) {
			return SyncStep{}, fmt.Errorf("%w: nope", ErrSyncTerminal)
		},
	})
	prom := NewPromMetrics()
	w := NewSyncWorker(store, reg, SyncWorkerOptions{Workers: 1, Prom: prom})
	mustEnqueue(t, store, SyncEnqueue{Kind: "messages", JobType: "ingest", MaxAttempts: 5})

	if !w.RunOnce(context.Background(), "w") {
		t.Fatal("RunOnce must claim")
	}
	job := store.List(SyncFilter{})[0]
	if job.State != SyncJobFailed {
		t.Fatalf("state = %s, want failed (terminal on the first attempt)", job.State)
	}
	out := prom.promText()
	if !strings.Contains(out, `aegis_sync_jobs_total{job_type="ingest",state="failed"} 1`) {
		t.Fatalf("metrics must count the terminal failure as failed:\n%s", out)
	}
	if strings.Contains(out, `aegis_sync_jobs_total{job_type="ingest",state="queued"}`) {
		t.Fatal("metrics must not count a terminal failure as queued")
	}
}

// CancelJob stops an in-flight step; the runner sees a cancelled context.
func TestSyncWorker_CancelJobStopsInFlightStep(t *testing.T) {
	store := testJobStore(t)
	reg := NewRunnerRegistry()
	started := make(chan struct{})
	stopped := make(chan struct{}, 1)
	reg.Register(&stubRunner{
		kind:     "messages",
		jobTypes: []string{"ingest"},
		fn: func(ctx context.Context, job SyncJob) (SyncStep, error) {
			close(started)
			<-ctx.Done()
			stopped <- struct{}{}
			return SyncStep{}, ctx.Err()
		},
	})
	w := NewSyncWorker(store, reg, SyncWorkerOptions{Workers: 1})
	mustEnqueue(t, store, SyncEnqueue{Kind: "messages", JobType: "ingest"})

	done := make(chan struct{})
	go func() {
		defer close(done)
		w.RunOnce(context.Background(), "w")
	}()
	<-started

	job := store.List(SyncFilter{})[0]
	if _, err := store.Cancel(job.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if !w.CancelJob(job.ID) {
		t.Fatal("CancelJob must report the in-flight step cancelled")
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("runner context was not cancelled")
	}
	<-done

	final, _ := store.GetByID(job.ID)
	if final.State != SyncJobCanceled {
		t.Fatalf("state = %s, want canceled (the stale step must not resurrect it)", final.State)
	}
}
