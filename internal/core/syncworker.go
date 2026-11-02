package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

// Sync worker (issue #227): a small in-process pool that drains the job
// queue. Started and stopped with core.Run(). Stdlib only, no external
// broker — this is a single-process tailnet service. Fail-open: a missing
// runner, a disabled store, or a failed step never blocks library reads.

// SyncRunner executes one kind of data-movement work. Implementations are
// registered next to connectors and are responsible for their own provider
// serialization (the mail runner takes the mail-sync mutex).
//
// Run executes one bounded step. A nil error with step.Done == false chains
// the job with step.Payload as the next step. A non-nil error fails the step;
// wrap it in *SyncRetryAfter for a custom backoff or mark it ErrSyncTerminal
// when retrying cannot help.
type SyncRunner interface {
	// JobTypes lists the job types this runner executes.
	JobTypes() []string
	// Kind is the library kind this runner serves ("messages", "playlists", …).
	Kind() string
	Run(ctx context.Context, job SyncJob) (SyncStep, error)
}

// RunnerRegistry maps (kind, job_type) to a runner.
type RunnerRegistry struct {
	mu      sync.RWMutex
	runners map[string]SyncRunner
}

// NewRunnerRegistry creates an empty registry.
func NewRunnerRegistry() *RunnerRegistry {
	return &RunnerRegistry{runners: make(map[string]SyncRunner)}
}

// Register adds a runner for each of its job types. Later registrations for
// the same (kind, job_type) replace earlier ones — tests rely on that.
func (r *RunnerRegistry) Register(runner SyncRunner) {
	if r == nil || runner == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, jt := range runner.JobTypes() {
		r.runners[runner.Kind()+"\x00"+strings.TrimSpace(jt)] = runner
	}
}

// For returns the runner for a (kind, job_type) pair.
func (r *RunnerRegistry) For(kind, jobType string) (SyncRunner, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	runner, ok := r.runners[strings.TrimSpace(kind)+"\x00"+strings.TrimSpace(jobType)]
	return runner, ok
}

// Kinds lists the registered kinds (sorted), for the admin surface.
func (r *RunnerRegistry) Kinds() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := make(map[string]bool, len(r.runners))
	for k := range r.runners {
		seen[strings.SplitN(k, "\x00", 2)[0]] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// SyncWorker drains the job queue. Zero value is not usable; build with
// NewSyncWorker and drive with Start/Stop.
type SyncWorker struct {
	store   *JobStore
	runners *RunnerRegistry
	prom    *PromMetrics
	logger  *slog.Logger

	// workers is the pool size. Per-service provider limits live in the
	// runners (see mailSyncMu), not here.
	workers int
	// poll is the idle poll interval. Enqueues kick the loop sooner.
	poll time.Duration
	// owner identifies this process in lease_owner.
	owner string
	// onDone is called after a job reaches a terminal state (cache
	// invalidation hook). Never required.
	onDone func(job SyncJob, succeeded bool)

	kick chan struct{}

	// now is the clock for lease/claim decisions. Tests replace it to walk
	// past backoff windows without sleeping.
	now func() time.Time

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	// lastSuccess tracks the newest successful finish per kind|service for
	// the aegis_sync_last_success_timestamp_seconds gauge.
	lastSuccessMu sync.Mutex
	lastSuccess   map[string]time.Time

	// active holds the cancel func per in-flight job id, so CancelJob can
	// stop a running step instead of only blocking the next claim.
	activeMu sync.Mutex
	active   map[string]context.CancelFunc
}

// SyncWorkerOptions configures a worker. Zero values get sane defaults.
type SyncWorkerOptions struct {
	Workers int
	Poll    time.Duration
	Owner   string
	Logger  *slog.Logger
	Prom    *PromMetrics
	// OnDone runs after a job reaches succeeded/failed/canceled.
	OnDone func(job SyncJob, succeeded bool)
}

// NewSyncWorker builds a worker over a job store and runner registry.
func NewSyncWorker(store *JobStore, runners *RunnerRegistry, opts SyncWorkerOptions) *SyncWorker {
	if opts.Workers <= 0 {
		opts.Workers = 2
	}
	if opts.Poll <= 0 {
		opts.Poll = 2 * time.Second
	}
	if opts.Owner == "" {
		opts.Owner = "aegis"
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &SyncWorker{
		store:       store,
		runners:     runners,
		prom:        opts.Prom,
		logger:      opts.Logger.With("component", "sync_worker"),
		workers:     opts.Workers,
		poll:        opts.Poll,
		owner:       opts.Owner,
		onDone:      opts.OnDone,
		kick:        make(chan struct{}, 1),
		lastSuccess: make(map[string]time.Time),
		active:      make(map[string]context.CancelFunc),
		now:         func() time.Time { return time.Now().UTC() },
	}
}

// Start launches the worker pool. Safe to call once; later calls no-op.
func (w *SyncWorker) Start() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.started = true

	// Startup recovery: a restart orphans every in-flight step, so reclaim
	// all running rows immediately rather than waiting out the lease.
	if w.store != nil && w.store.Enabled() {
		if n, err := w.store.ReclaimAllRunning(); err == nil && n > 0 {
			w.logger.Info("reclaimed orphaned sync jobs after restart", "count", n)
		}
	}

	for i := 0; i < w.workers; i++ {
		w.wg.Add(1)
		go w.loop(ctx, i)
	}
	w.logger.Info("sync worker started", "workers", w.workers, "poll", w.poll.String())
}

// Stop drains the pool. In-flight steps get ctx cancellation; their leases
// expire or are reclaimed on the next start.
func (w *SyncWorker) Stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	if !w.started {
		w.mu.Unlock()
		return
	}
	w.started = false
	cancel := w.cancel
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	w.wg.Wait()
	w.logger.Info("sync worker stopped")
}

// Kick nudges the pool to look for work now (after an enqueue).
func (w *SyncWorker) Kick() {
	if w == nil {
		return
	}
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

func (w *SyncWorker) loop(ctx context.Context, slot int) {
	defer w.wg.Done()
	owner := fmt.Sprintf("%s-%d-%d", w.owner, time.Now().UnixNano()%1_000_000, slot)
	ticker := time.NewTicker(w.poll)
	defer ticker.Stop()
	ticks := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.kick:
		case <-ticker.C:
		}
		// Drain whatever is due before going back to sleep.
		for ctx.Err() == nil {
			ran := w.RunOnce(ctx, owner)
			if !ran {
				break
			}
		}
		ticks++
		// Housekeep roughly every ~50 idle polls: reclaim wedged leases and
		// prune finished rows past retention. Cheap and not on the hot path.
		if ticks%50 == 0 {
			w.housekeep()
		}
		w.recordQueueDepth()
	}
}

// housekeep reclaims leases a wedged step never gave back and drops finished
// jobs past retention, so a noisy pipeline cannot fill the disk.
func (w *SyncWorker) housekeep() {
	if w.store == nil || !w.store.Enabled() {
		return
	}
	now := w.now()
	if n, err := w.store.ReclaimExpired(now); err == nil && n > 0 {
		w.logger.Info("reclaimed expired sync leases", "count", n)
	}
	if n, err := w.store.Prune(syncJobRetention); err == nil && n > 0 {
		w.logger.Info("pruned finished sync jobs", "count", n)
	}
}

// RunOnce claims and executes at most one due job. Returns false when the
// queue is empty or the store is disabled. Exposed for tests and for a
// future synchronous façade over POST /v1/mail/sync.
func (w *SyncWorker) RunOnce(ctx context.Context, owner string) bool {
	if w == nil || w.store == nil || !w.store.Enabled() {
		return false
	}
	job, ok, err := w.store.Claim(owner, w.now())
	if err != nil {
		w.logger.Warn("sync claim failed", "error", err)
		return false
	}
	if !ok {
		return false
	}
	w.runJob(ctx, job)
	return true
}

func (w *SyncWorker) runJob(ctx context.Context, job SyncJob) {
	runner, ok := w.runners.For(job.Kind, job.JobType)
	if !ok {
		// A runner can disappear across a config change; park the job as
		// terminal rather than spinning on it.
		next, ferr := w.store.Fail(job, SyncStep{}, fmt.Errorf("%w: no runner for %s/%s", ErrSyncTerminal, job.Kind, job.JobType))
		if ferr != nil {
			w.logger.Warn("sync fail recorded", "job_id", job.ID, "error", ferr)
		}
		w.observe(job, SyncStep{}, ErrSyncTerminal, next.State, time.Duration(0))
		if next.State == SyncJobFailed {
			w.callOnDone(next, false)
		}
		return
	}

	// Give the step its own cancelable context so an admin cancel can stop
	// an in-flight runner rather than only blocking the next claim.
	stepCtx, cancel := context.WithCancel(ctx)
	w.trackActive(job.ID, cancel)
	defer func() {
		w.trackActive(job.ID, nil)
		cancel()
	}()

	start := time.Now()
	step, err := w.safeRun(stepCtx, runner, job)
	elapsed := time.Since(start)

	if err == nil {
		next, cerr := w.store.Complete(job, step)
		switch {
		case errors.Is(cerr, ErrSyncJobLost):
			// Canceled or reclaimed mid-flight: the step's outcome is void.
			w.observe(job, step, nil, SyncJobCanceled, elapsed)
			w.logger.Info("sync step discarded (job no longer owned)",
				"job_id", job.ID, "kind", job.Kind, "job_type", job.JobType)
			return
		case cerr != nil:
			w.logger.Warn("sync complete failed", "job_id", job.ID, "error", cerr)
			w.observe(job, step, nil, job.State, elapsed)
			return
		}
		w.observe(job, step, nil, next.State, elapsed)
		if step.Done {
			w.noteSuccess(next)
			w.callOnDone(next, true)
			w.logger.Info("sync job done",
				"job_id", job.ID, "kind", job.Kind, "job_type", job.JobType,
				"service", job.Service, "attempts", job.Attempts)
		}
		return
	}

	next, ferr := w.store.Fail(job, step, err)
	switch {
	case errors.Is(ferr, ErrSyncJobLost):
		w.observe(job, step, err, SyncJobCanceled, elapsed)
		w.logger.Info("sync failure discarded (job no longer owned)",
			"job_id", job.ID, "kind", job.Kind, "job_type", job.JobType, "error", err)
		return
	case ferr != nil:
		w.logger.Warn("sync fail recorded", "job_id", job.ID, "error", ferr)
		w.observe(job, step, err, job.State, elapsed)
		return
	}
	w.observe(job, step, err, next.State, elapsed)
	if next.State == SyncJobFailed {
		w.callOnDone(next, false)
		w.logger.Error("sync job failed",
			"job_id", job.ID, "kind", job.Kind, "job_type", job.JobType,
			"service", job.Service, "attempts", next.Attempts, "error", err)
		return
	}
	w.logger.Warn("sync job retry scheduled",
		"job_id", job.ID, "kind", job.Kind, "job_type", job.JobType,
		"service", job.Service, "attempts", next.Attempts,
		"next_run_at", next.NextRunAt, "error", err)
}

// trackActive registers (or clears, when cancel is nil) the cancel func for an
// in-flight job so CancelJob can stop a running step.
func (w *SyncWorker) trackActive(id string, cancel context.CancelFunc) {
	w.activeMu.Lock()
	defer w.activeMu.Unlock()
	if cancel == nil {
		delete(w.active, id)
		return
	}
	w.active[id] = cancel
}

// CancelJob cancels an in-flight step for a job id. The job row is moved on by
// the caller (JobStore.Cancel); this only stops the runner. Returns true when
// a running step was actually cancelled.
func (w *SyncWorker) CancelJob(id string) bool {
	if w == nil {
		return false
	}
	w.activeMu.Lock()
	cancel, ok := w.active[id]
	w.activeMu.Unlock()
	if !ok {
		return false
	}
	cancel()
	return true
}

// safeRun keeps a panicking runner from taking the worker pool down. A panic
// is recorded as a retryable failure (fail-open on the next attempt).
func (w *SyncWorker) safeRun(ctx context.Context, runner SyncRunner, job SyncJob) (step SyncStep, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("sync runner panic: %v", r)
			w.logger.Error("sync runner panicked", "job_id", job.ID, "panic", r)
		}
	}()
	return runner.Run(ctx, job)
}

func (w *SyncWorker) callOnDone(job SyncJob, succeeded bool) {
	if w.onDone == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			w.logger.Error("sync onDone panicked", "job_id", job.ID, "panic", r)
		}
	}()
	w.onDone(job, succeeded)
}

func (w *SyncWorker) noteSuccess(job SyncJob) {
	if job.Kind == "" {
		return
	}
	key := job.Kind + "|" + job.Service
	now := time.Now().UTC()
	w.lastSuccessMu.Lock()
	prev, ok := w.lastSuccess[key]
	if !ok || now.After(prev) {
		w.lastSuccess[key] = now
	}
	w.lastSuccessMu.Unlock()
	if w.prom != nil {
		w.prom.noteSyncSuccess(job.Kind, job.Service, now)
	}
}

// LastSuccess returns the newest successful finish per "kind|service".
func (w *SyncWorker) LastSuccess() map[string]time.Time {
	if w == nil {
		return nil
	}
	w.lastSuccessMu.Lock()
	defer w.lastSuccessMu.Unlock()
	out := make(map[string]time.Time, len(w.lastSuccess))
	for k, v := range w.lastSuccess {
		out[k] = v
	}
	return out
}

// observe records one step outcome. state is the job state the store
// actually landed on (a terminal failure on a first attempt is "failed", not
// "queued", even though attempts is still below the budget).
func (w *SyncWorker) observe(job SyncJob, step SyncStep, err error, state string, elapsed time.Duration) {
	if w.prom == nil {
		return
	}
	if state == "" {
		state = job.State
	}
	w.prom.observeSyncJob(job.JobType, job.Service, state, elapsed)
	if err != nil {
		class, _ := classifySyncError(err, job.Attempts, step.RetryAfter)
		w.prom.observeSyncRetry(job.JobType, class)
	}
}

func (w *SyncWorker) recordQueueDepth() {
	if w.prom == nil || w.store == nil {
		return
	}
	c := w.store.Counts("", "")
	w.prom.setSyncQueueDepth(c)
}
