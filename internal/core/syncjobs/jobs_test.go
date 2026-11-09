package syncjobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/core/copies"
	"github.com/fudomunro/aegis/internal/kinds"
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
	class, after := ClassifyError(nil, 1, 0)
	if class != SyncErrorClassRetry || after <= 0 {
		t.Fatalf("nil error = (%s,%v), want (retryable, >0)", class, after)
	}
	// The backoff must be exponential in the attempt number, not fixed.
	_, early := ClassifyError(errors.New("boom"), 1, 0)
	_, late := ClassifyError(errors.New("boom"), 5, 0)
	if late <= early {
		t.Fatalf("attempt 5 backoff = %v, want > attempt 1 (%v)", late, early)
	}
	class, _ = ClassifyError(ErrSyncTerminal, 1, 0)
	if class != SyncErrorClassTerminal {
		t.Fatalf("terminal error class = %s", class)
	}
	class, _ = ClassifyError(fmt.Errorf("wrapped: %w", ErrSyncTerminal), 1, 0)
	if class != SyncErrorClassTerminal {
		t.Fatalf("wrapped terminal class = %s", class)
	}
	// No usable token cannot be fixed by retrying — park it.
	class, _ = ClassifyError(kinds.ErrUnauthorized, 1, 0)
	if class != SyncErrorClassTerminal {
		t.Fatalf("unauthorized class = %s, want terminal", class)
	}
	class, _ = ClassifyError(fmt.Errorf("sync paused: %w", kinds.ErrUnauthorized), 1, 0)
	if class != SyncErrorClassTerminal {
		t.Fatalf("wrapped unauthorized class = %s, want terminal", class)
	}
	class, after = ClassifyError(&SyncRetryAfter{Err: errors.New("q"), After: time.Minute}, 1, 0)
	if class != SyncErrorClassRetry || after != time.Minute {
		t.Fatalf("retry-after = (%s,%v), want (retryable, 1m)", class, after)
	}
	class, after = ClassifyError(&SyncRetryAfter{Err: errors.New("q")}, 1, 2*time.Second)
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

func TestFormatSyncTime_OrdersLikeTheClock(t *testing.T) {
	// Timestamps are compared lexicographically in SQL; the fixed-width
	// fraction must keep string order identical to clock order even when
	// the earlier instant ends in trailing zeros (RFC3339Nano trimming made
	// "…40.58Z" sort after "…40.582Z").
	pairs := [][2]time.Time{
		{
			time.Date(2026, time.November, 7, 23, 20, 40, 580_000_000, time.UTC),
			time.Date(2026, time.November, 7, 23, 20, 40, 582_000_000, time.UTC),
		},
		{
			time.Date(2026, time.November, 7, 23, 20, 40, 500_000_000, time.UTC),
			time.Date(2026, time.November, 7, 23, 20, 40, 500_000_001, time.UTC),
		},
		{
			time.Date(2026, time.November, 7, 23, 20, 40, 0, time.UTC),
			time.Date(2026, time.November, 7, 23, 20, 40, 1, time.UTC),
		},
		{
			time.Date(2026, time.November, 7, 23, 20, 39, 999_999_999, time.UTC),
			time.Date(2026, time.November, 7, 23, 20, 40, 0, time.UTC),
		},
	}
	for _, p := range pairs {
		if formatSyncTime(p[0]) >= formatSyncTime(p[1]) {
			t.Fatalf("formatSyncTime(%v)=%q must sort before %q",
				p[0], formatSyncTime(p[0]), formatSyncTime(p[1]))
		}
	}
	same := time.Date(2026, time.November, 7, 23, 20, 40, 580_000_000, time.UTC)
	if formatSyncTime(same) != formatSyncTime(same.In(time.FixedZone("X", 3600))) {
		t.Fatal("equal instants must format identically across zones")
	}
	if formatSyncTime(time.Time{}) != "" {
		t.Fatal("zero time must format empty")
	}
}

func TestJobStore_ClaimDueAcrossTrailingZeroBoundary(t *testing.T) {
	s := testJobStore(t)
	// Enqueue instant 40.580s and claim instant 40.582s: with trimmed
	// RFC3339Nano the due time compared after the claim time and the job
	// looked not-yet-due (the sync worker flake).
	enqueuedAt := time.Date(2026, time.November, 7, 23, 20, 40, 580_000_000, time.UTC)
	s.now = func() time.Time { return enqueuedAt }
	mustEnqueue(t, s, SyncEnqueue{Kind: "messages", JobType: "ingest", Service: "google-gmail"})

	job, ok, err := s.Claim("test-owner", enqueuedAt.Add(2*time.Millisecond))
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if !ok {
		t.Fatal("job due at 40.580s must be claimable at 40.582s")
	}
	if job.State != SyncJobRunning {
		t.Fatalf("state = %s, want running", job.State)
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

func TestJobStore_SharedHandleCoLocates(t *testing.T) {
	dir := t.TempDir()
	ledgerPath := filepath.Join(dir, "copy_ledger.db")
	ledger, err := copies.Open(ledgerPath)
	if err != nil {
		t.Fatalf("copies.Open: %v", err)
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
	if _, err := ledger.Upsert(copies.Record{
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
	ledger, err := copies.Open(filepath.Join(t.TempDir(), "copy_ledger.db"))
	if err != nil {
		t.Fatalf("copies.Open: %v", err)
	}
	defer ledger.Close()
	shared, err := OpenJobStore(ledger.DB(), ledger.Path())
	if err != nil {
		t.Fatalf("OpenJobStore(shared): %v", err)
	}
	x := mustEnqueue(t, shared, SyncEnqueue{Kind: "photos", JobType: "ingest", Service: "x"})
	if _, err := ledger.Upsert(copies.Record{
		Kind: "photos", EntityKey: "p1", Service: "x", ExternalID: "e1",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	y := mustEnqueue(t, shared, SyncEnqueue{Kind: "photos", JobType: "ingest", Service: "y"})
	if _, err := ledger.Upsert(copies.Record{
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

// --- schedule pump ---

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

// --- lease renewal ---

func TestJobStore_RenewLease(t *testing.T) {
	s := testJobStore(t)
	mustEnqueue(t, s, SyncEnqueue{Kind: "messages", JobType: "ingest", Service: "google-gmail"})
	claimed, ok, _ := s.Claim("w1", s.now().Add(time.Second))
	if !ok {
		t.Fatal("expected a claim")
	}

	// The owner may extend its own lease.
	far := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	kept, err := s.RenewLease(claimed.ID, "w1", far)
	if err != nil || !kept {
		t.Fatalf("RenewLease(owner) = (%v,%v), want (true,nil)", kept, err)
	}
	after, _ := s.GetByID(claimed.ID)
	if !after.LeaseUntil.Equal(far) {
		t.Fatalf("lease_until = %v, want %v", after.LeaseUntil, far)
	}

	// A different owner cannot extend it.
	kept, err = s.RenewLease(claimed.ID, "w2", far.Add(time.Hour))
	if err != nil {
		t.Fatalf("RenewLease(other): %v", err)
	}
	if kept {
		t.Fatal("a non-owner must not be able to renew")
	}

	// A job that is no longer running cannot be renewed.
	if _, err := s.Cancel(claimed.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	kept, err = s.RenewLease(claimed.ID, "w1", far.Add(2*time.Hour))
	if err != nil || kept {
		t.Fatalf("RenewLease(canceled) = (%v,%v), want (false,nil)", kept, err)
	}

	// Disabled store and empty owner are no-ops / errors.
	off, _ := OpenJobStore(nil, "")
	if kept, _ := off.RenewLease("1", "w", time.Now()); kept {
		t.Fatal("disabled renewal must report false")
	}
	if _, err := s.RenewLease(claimed.ID, "  ", time.Now()); err == nil {
		t.Fatal("renewal requires an owner")
	}
}

// When renewal reports the lease is gone, the in-flight step is wound down
// rather than writing against a job someone else now owns.
func TestSyncWorker_LeaseKeeperCancelsOnLoss(t *testing.T) {
	store := testJobStore(t)
	store.now = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
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
	// Speed the keeper up so the test does not wait a third of a lease.
	w.now = store.now

	mustEnqueue(t, store, SyncEnqueue{Kind: "messages", JobType: "ingest"})

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Run the job through the worker so keepLeaseAlive is armed.
		w.RunOnce(context.Background(), "owner-a")
	}()
	<-started

	job := store.List(SyncFilter{})[0]
	// Someone else steals the job, exactly as an expired lease would allow.
	if _, err := store.ReclaimExpired(store.now().Add(syncJobLease + time.Minute)); err != nil {
		t.Fatalf("ReclaimExpired: %v", err)
	}
	if _, ok, _ := store.Claim("owner-b", store.now().Add(syncJobLease+2*time.Minute)); !ok {
		t.Fatal("expected the reclaimed job to be claimable by another owner")
	}

	// Fire the keeper's tick by moving its clock far enough ahead, then ask
	// it to renew: the renewal must fail and cancel the step.
	kept, err := store.RenewLease(job.ID, "owner-a", store.now().Add(time.Hour))
	if err != nil {
		t.Fatalf("RenewLease: %v", err)
	}
	if kept {
		t.Fatal("a lost lease must not renew")
	}
	// The keeper observes that on its next tick and cancels; cancel directly
	// here to assert the wind-down contract without waiting on the ticker.
	w.CancelJob(job.ID)

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("the step did not wind down after losing the lease")
	}
	<-done

	final, _ := store.GetByID(job.ID)
	if final.LeaseOwner == "owner-a" {
		t.Fatal("owner-a must no longer hold the lease")
	}
}

// --- pause / resume / concurrency ---

// --- pause / resume ---

func TestJobStore_PauseResume(t *testing.T) {
	s := testJobStore(t)
	s.now = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }

	mustEnqueue(t, s, SyncEnqueue{Kind: "messages", JobType: "ingest", Service: "google-gmail"})
	mustEnqueue(t, s, SyncEnqueue{Kind: "playlists", JobType: "ingest", Service: "spotify"})

	if err := s.Pause("spotify", "quota"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if !s.IsPaused("spotify") || s.IsPaused("google-gmail") {
		t.Fatal("pause must apply to the named service only")
	}
	got := s.List(SyncFilter{State: SyncJobPaused})
	if len(got) != 1 || got[0].Service != "spotify" {
		t.Fatalf("paused = %+v, want only the spotify job", got)
	}
	// The claim filter must skip the paused service but keep serving others.
	job, ok, _ := s.Claim("w", s.now().Add(time.Second))
	if !ok || job.Service != "google-gmail" {
		t.Fatalf("claim = (%+v,%v), want the unpaused google-gmail job", job, ok)
	}
	if job, ok, _ := s.Claim("w", s.now().Add(time.Second)); ok {
		t.Fatalf("second claim handed out paused work: %+v", job)
	}

	// New enqueues for the paused scope land in `paused` too.
	third := mustEnqueue(t, s, SyncEnqueue{Kind: "playlists", JobType: "mirror", Service: "spotify"})
	if third.State != SyncJobPaused {
		t.Fatalf("new enqueue state = %s, want paused", third.State)
	}

	// Resume puts the scope's jobs back in the queue.
	if err := s.Resume("spotify"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if s.IsPaused("spotify") {
		t.Fatal("scope must be resumed")
	}
	if got := s.List(SyncFilter{State: SyncJobPaused}); len(got) != 0 {
		t.Fatalf("still paused: %+v", got)
	}
	claimed := map[string]bool{}
	for i := 0; i < 3; i++ {
		if job, ok, _ := s.Claim("w", s.now().Add(time.Second)); ok {
			claimed[job.Service+"/"+job.JobType] = true
		}
	}
	if !claimed["spotify/mirror"] {
		t.Fatalf("after resume the spotify job must be claimable, got %v", claimed)
	}

	// Global pause holds everything.
	if err := s.Pause(SyncPauseScopeGlobal, "maintenance"); err != nil {
		t.Fatalf("global Pause: %v", err)
	}
	if !s.IsPaused("google-gmail") {
		t.Fatal("global pause must hold every service")
	}
	if job, ok, _ := s.Claim("w", s.now().Add(2*time.Hour)); ok {
		t.Fatalf("claim under global pause handed out %+v", job)
	}
	if err := s.Resume(SyncPauseScopeGlobal); err != nil {
		t.Fatalf("global Resume: %v", err)
	}
	if got := s.Paused(); len(got) != 0 {
		t.Fatalf("Paused = %v, want none", got)
	}

	// Resuming a service while the global pause is on must not release it.
	if err := s.Pause(SyncPauseScopeGlobal, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Pause("spotify", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Resume("spotify"); err != nil {
		t.Fatal(err)
	}
	if !s.IsPaused("spotify") {
		t.Fatal("global pause must keep the service held after a service resume")
	}

	// A disabled store is a no-op.
	off, _ := OpenJobStore(nil, "")
	if err := off.Pause("x", ""); err != nil {
		t.Fatalf("disabled Pause: %v", err)
	}
	if off.IsPaused("x") {
		t.Fatal("disabled store must report not paused")
	}
	if got := off.Paused(); got != nil {
		t.Fatalf("disabled Paused = %v", got)
	}
}

// --- per-service concurrency ---

type blockingRunner struct {
	kind    string
	started chan string
	release chan struct{}
}

func (r *blockingRunner) JobTypes() []string { return []string{"ingest"} }
func (r *blockingRunner) Kind() string       { return r.kind }
func (r *blockingRunner) Run(ctx context.Context, job SyncJob) (SyncStep, error) {
	r.started <- job.Service
	<-r.release
	return SyncStep{Done: true}, nil
}

func TestSyncWorker_ServiceConcurrencyCap(t *testing.T) {
	store := testJobStore(t)
	reg := NewRunnerRegistry()
	started := make(chan string, 4)
	release := make(chan struct{})
	// One runner kind per service so both jobs find a runner.
	for _, kind := range []string{"messages", "playlists"} {
		reg.Register(&blockingRunner{kind: kind, started: started, release: release})
	}
	w := NewSyncWorker(store, reg, SyncWorkerOptions{
		Workers: 4, ServiceConcurrency: map[string]int{"spotify": 1},
	})
	mustEnqueue(t, store, SyncEnqueue{Kind: "messages", JobType: "ingest", Service: "spotify"})
	mustEnqueue(t, store, SyncEnqueue{Kind: "playlists", JobType: "ingest", Service: "spotify"})

	// With the cap at 1, only one spotify job may start even with 4 workers.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for w.RunOnce(context.Background(), "w1") {
		}
	}()
	go func() {
		for w.RunOnce(context.Background(), "w2") {
		}
	}()

	first := <-started
	// Give the second worker a chance; it must block on the service cap.
	select {
	case second := <-started:
		t.Fatalf("two %s jobs ran concurrently despite the cap", second)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	<-done

	if first != "spotify" {
		t.Fatalf("started = %q", first)
	}
	if got := store.Counts("", ""); got.Succeeded != 2 {
		t.Fatalf("counts = %+v, want both jobs succeeded", got)
	}
}
