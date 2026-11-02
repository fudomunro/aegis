package core

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

// Sync job queue (issue #227): durable, restartable units of data-movement
// work — ingest, import, mirror, freshness check, repair. The job row is the
// work; the copy ledger row is the fact. A long job is a chain of small
// leased steps: a runner returns Continue plus a payload, and the queue
// re-arms the same row. Fail-open: a disabled or broken queue degrades to
// today's request-driven behavior and never blocks library reads.
//
// The table lives in the copy-ledger SQLite file by default so a job's
// completion and its copy-ledger stamp can commit in one transaction
// (SQLite's one real weakness for a queue — see the store evaluation on
// issue #227). A separate -sync-jobs path is supported; the JobStore seam
// keeps callers stable if the engine ever changes.

// Job states.
const (
	SyncJobQueued    = "queued"
	SyncJobRunning   = "running"
	SyncJobSucceeded = "succeeded"
	SyncJobFailed    = "failed"
	SyncJobCanceled  = "canceled"
	SyncJobPaused    = "paused"
)

// Job types. Open strings on purpose — user-defined types can register their
// own ingest without core changes (issue #15).
const (
	SyncJobTypeIngest    = "ingest"
	SyncJobTypeImport    = "import_snapshot"
	SyncJobTypeMirror    = "mirror"
	SyncJobTypeFreshness = "freshness_check"
	SyncJobTypeRepair    = "repair"
)

// Error classes recorded on a failed step (last_error_class).
const (
	SyncErrorClassRetry    = "retryable"
	SyncErrorClassTerminal = "terminal"
)

// syncJobDefaultMaxAttempts bounds retries before a job parks in failed.
const syncJobDefaultMaxAttempts = 5

// syncJobLease is how long a claimed job stays owned before another worker
// may reclaim it. Generous enough for one paced mail page (200 messages at
// the Gmail get interval), short enough that a crash does not stall the queue.
const syncJobLease = 30 * time.Minute

// syncJobRetention is how long finished jobs stay queryable before the
// housekeeping sweep drops them.
const syncJobRetention = 7 * 24 * time.Hour

// ErrSyncTerminal marks a failure retrying cannot fix (revoked token, bad
// params, unsupported service). The job parks in failed instead of burning
// its attempt budget.
var ErrSyncTerminal = errors.New("terminal sync failure")

// SyncRetryAfter wraps a retryable failure with a suggested backoff, e.g. a
// provider quota window. Zero After means "use the default backoff".
type SyncRetryAfter struct {
	Err   error
	After time.Duration
}

func (e *SyncRetryAfter) Error() string {
	if e == nil || e.Err == nil {
		return "sync retry"
	}
	return e.Err.Error()
}

func (e *SyncRetryAfter) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

const syncJobsSchema = `
CREATE TABLE IF NOT EXISTS sync_jobs (
  id INTEGER PRIMARY KEY,
  kind TEXT NOT NULL,
  job_type TEXT NOT NULL,
  service TEXT NOT NULL DEFAULT '',
  idempotency_key TEXT NOT NULL,
  payload TEXT NOT NULL DEFAULT '{}',
  state TEXT NOT NULL DEFAULT 'queued',
  attempts INTEGER NOT NULL DEFAULT 0,
  max_attempts INTEGER NOT NULL DEFAULT 5,
  priority INTEGER NOT NULL DEFAULT 0,
  next_run_at TEXT NOT NULL,
  lease_owner TEXT NOT NULL DEFAULT '',
  lease_until TEXT NOT NULL DEFAULT '',
  progress TEXT NOT NULL DEFAULT '{}',
  last_error TEXT NOT NULL DEFAULT '',
  last_error_class TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  started_at TEXT NOT NULL DEFAULT '',
  finished_at TEXT NOT NULL DEFAULT '',
  UNIQUE(idempotency_key)
);
CREATE INDEX IF NOT EXISTS sync_jobs_due ON sync_jobs(state, next_run_at);
CREATE INDEX IF NOT EXISTS sync_jobs_state ON sync_jobs(state);
CREATE INDEX IF NOT EXISTS sync_jobs_kind ON sync_jobs(kind, job_type);
CREATE TABLE IF NOT EXISTS sync_pause (
  scope TEXT PRIMARY KEY,
  reason TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
`

// SyncJob is one durable unit of data-movement work.
type SyncJob struct {
	ID             string          `json:"id"`
	Kind           string          `json:"kind"`
	JobType        string          `json:"job_type"`
	Service        string          `json:"service,omitempty"`
	IdempotencyKey string          `json:"idempotency_key"`
	Payload        json.RawMessage `json:"payload,omitempty"`
	State          string          `json:"state"`
	Attempts       int             `json:"attempts"`
	MaxAttempts    int             `json:"max_attempts"`
	Priority       int             `json:"priority,omitempty"`
	NextRunAt      time.Time       `json:"next_run_at"`
	LeaseOwner     string          `json:"lease_owner,omitempty"`
	LeaseUntil     time.Time       `json:"lease_until,omitempty"`
	Progress       json.RawMessage `json:"progress,omitempty"`
	LastError      string          `json:"last_error,omitempty"`
	LastErrorClass string          `json:"last_error_class,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	StartedAt      time.Time       `json:"started_at,omitempty"`
	FinishedAt     time.Time       `json:"finished_at,omitempty"`
}

// SyncStep is what a runner returns for one bounded unit of work.
// Done closes the job; !Done re-arms it with Payload as the next step.
type SyncStep struct {
	Done     bool
	Payload  json.RawMessage
	Progress map[string]any
	// RetryAfter suggests a backoff when used with a retryable error.
	RetryAfter time.Duration
}

// SyncEnqueue describes work to add to the queue. Enqueue is idempotent on
// (kind, job_type, service, entity key) — a repeat request refreshes a
// terminal job back to queued and leaves queued/running jobs untouched.
type SyncEnqueue struct {
	Kind        string
	JobType     string
	Service     string
	EntityKey   string
	Payload     map[string]any
	Priority    int
	MaxAttempts int
	// RunAt defers the first attempt. Zero means now.
	RunAt time.Time
}

// SyncFilter narrows List / admin queries.
type SyncFilter struct {
	Kind    string
	JobType string
	Service string
	State   string
	Limit   int
}

// JobStore is the durable sync queue. Empty path and nil db disable
// persistence (tests, opt-out); writes become no-ops (fail-open).
type JobStore struct {
	db   *sql.DB
	path string
	// own is true when this store opened its own handle and must close it.
	own bool
	// now is the clock for timestamps and backoff. Tests replace it so retry
	// windows can be walked without sleeping.
	now func() time.Time
}

// OpenJobStore opens the sync job table. When shared is non-nil the table is
// created in that handle (co-located with the copy ledger so job completion
// and copy stamps can commit in one transaction) and path is informational.
// With a nil shared handle and a non-empty path the store opens its own
// database. Empty path with nil shared returns a disabled store.
func OpenJobStore(shared *sql.DB, path string) (*JobStore, error) {
	clock := func() time.Time { return time.Now().UTC() }
	if shared != nil {
		if err := initSyncJobsSchema(shared); err != nil {
			return nil, err
		}
		return &JobStore{db: shared, path: path, now: clock}, nil
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return &JobStore{now: clock}, nil
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil && !os.IsExist(err) {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		db.Close()
		return nil, err
	}
	if err := initSyncJobsSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return &JobStore{db: db, path: path, own: true, now: clock}, nil
}

// clock returns the store's time source (never nil).
func (s *JobStore) clock() time.Time {
	if s == nil || s.now == nil {
		return time.Now().UTC()
	}
	return s.now()
}

func initSyncJobsSchema(db *sql.DB) error {
	if _, err := db.Exec(syncJobsSchema); err != nil {
		return fmt.Errorf("sync jobs schema: %w", err)
	}
	return nil
}

// Close releases the handle when this store owns it. Shared handles stay
// open for their owner (the copy ledger).
func (s *JobStore) Close() error {
	if s == nil || s.db == nil || !s.own {
		return nil
	}
	return s.db.Close()
}

// Path returns the on-disk location ("" when disabled). For a store sharing
// the copy ledger's handle this is that same path.
func (s *JobStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Enabled reports whether the store can persist rows.
func (s *JobStore) Enabled() bool {
	return s != nil && s.db != nil
}

// DB returns the underlying handle for co-located stores that need one
// transaction across jobs and facts. Nil when disabled.
func (s *JobStore) DB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

// syncJobIdempotencyKey builds the dedupe key: one live job per
// (kind, job_type, service, entity). Entity keys come from the payload.
func syncJobIdempotencyKey(kind, jobType, service, entityKey string) string {
	return strings.Join([]string{
		strings.TrimSpace(kind),
		strings.TrimSpace(jobType),
		strings.TrimSpace(service),
		strings.TrimSpace(entityKey),
	}, "\x00")
}

func normalizeSyncEnqueue(req SyncEnqueue) (SyncEnqueue, error) {
	req.Kind = strings.TrimSpace(req.Kind)
	req.JobType = strings.TrimSpace(req.JobType)
	req.Service = strings.TrimSpace(req.Service)
	req.EntityKey = strings.TrimSpace(req.EntityKey)
	if req.Kind == "" || req.JobType == "" {
		return req, fmt.Errorf("sync job requires kind and job_type")
	}
	if req.MaxAttempts <= 0 {
		req.MaxAttempts = syncJobDefaultMaxAttempts
	}
	return req, nil
}

// Enqueue adds work to the queue, deduplicated on the idempotency key.
//
// A repeat request on a terminal job (succeeded/failed/canceled) resets it to
// queued with a fresh attempt budget and cleared progress, so a new run never
// inherits the previous run's totals. A queued job is pulled forward to the
// requested run time (an explicit "run now" beats a pending backoff) but keeps
// its in-flight payload, attempt budget, and priority, so a repeat POST cannot
// yank the cursor out of a chain mid-flight. Running and paused jobs are left
// untouched.
func (s *JobStore) Enqueue(req SyncEnqueue) (SyncJob, error) {
	req, err := normalizeSyncEnqueue(req)
	if err != nil {
		return SyncJob{}, err
	}
	key := syncJobIdempotencyKey(req.Kind, req.JobType, req.Service, req.EntityKey)
	payload := "{}"
	if len(req.Payload) > 0 {
		b, err := json.Marshal(req.Payload)
		if err != nil {
			return SyncJob{}, err
		}
		payload = string(b)
	}
	if !s.Enabled() {
		return SyncJob{Kind: req.Kind, JobType: req.JobType, Service: req.Service,
			IdempotencyKey: key, State: SyncJobQueued, Payload: json.RawMessage(payload)}, nil
	}
	now := s.clock()
	runAt := req.RunAt
	if runAt.IsZero() {
		runAt = now
	}
	runAtS := runAt.UTC().Format(time.RFC3339Nano)
	nowS := now.Format(time.RFC3339Nano)

	// New work for a paused scope lands in `paused` rather than `queued`, so
	// a pause holds enqueues made while it is on — not just what was queued
	// when it was pulled.
	initialState := SyncJobQueued
	if s.IsPaused(req.Service) {
		initialState = SyncJobPaused
	}

	// A terminal row is the only one that takes new payload/budget/progress;
	// a queued row only gets pulled forward in time. RETURNING id avoids
	// LastInsertId, which on an upsert still reports a previous insert on the
	// connection (shared with the copy ledger in co-located mode).
	var id int64
	err = s.db.QueryRow(`
INSERT INTO sync_jobs (
  kind, job_type, service, idempotency_key, payload, state,
  attempts, max_attempts, priority, next_run_at,
  progress, started_at, created_at, updated_at
) VALUES (?,?,?,?,?, ?, 0, ?, ?, ?, '{}', '', ?, ?)
ON CONFLICT(idempotency_key) DO UPDATE SET
  state=CASE
    WHEN sync_jobs.state IN ('succeeded','failed','canceled') THEN ?
    ELSE sync_jobs.state
  END,
  payload=CASE
    WHEN sync_jobs.state IN ('succeeded','failed','canceled') THEN excluded.payload
    ELSE sync_jobs.payload
  END,
  attempts=CASE
    WHEN sync_jobs.state IN ('succeeded','failed','canceled') THEN 0
    ELSE sync_jobs.attempts
  END,
  max_attempts=CASE
    WHEN sync_jobs.state IN ('succeeded','failed','canceled') THEN excluded.max_attempts
    ELSE sync_jobs.max_attempts
  END,
  priority=CASE
    WHEN sync_jobs.state IN ('succeeded','failed','canceled') THEN excluded.priority
    ELSE sync_jobs.priority
  END,
  next_run_at=CASE
    WHEN sync_jobs.state IN ('succeeded','failed','canceled') THEN excluded.next_run_at
    WHEN sync_jobs.state = 'queued' THEN excluded.next_run_at
    ELSE sync_jobs.next_run_at
  END,
  progress=CASE
    WHEN sync_jobs.state IN ('succeeded','failed','canceled') THEN '{}'
    ELSE sync_jobs.progress
  END,
  started_at=CASE
    WHEN sync_jobs.state IN ('succeeded','failed','canceled') THEN ''
    ELSE sync_jobs.started_at
  END,
  last_error=CASE
    WHEN sync_jobs.state IN ('succeeded','failed','canceled') THEN ''
    ELSE sync_jobs.last_error
  END,
  last_error_class=CASE
    WHEN sync_jobs.state IN ('succeeded','failed','canceled') THEN ''
    ELSE sync_jobs.last_error_class
  END,
  finished_at=CASE
    WHEN sync_jobs.state IN ('succeeded','failed','canceled') THEN ''
    ELSE sync_jobs.finished_at
  END,
  updated_at=excluded.updated_at
RETURNING id
`,
		req.Kind, req.JobType, req.Service, key, payload,
		initialState, req.MaxAttempts, req.Priority, runAtS, nowS, nowS, initialState,
	).Scan(&id)
	if err != nil {
		return SyncJob{}, err
	}
	return s.GetByID(fmt.Sprint(id))
}

// ErrSyncJobNotFound is returned when no job matches the id.
var ErrSyncJobNotFound = errors.New("sync job not found")

// GetByID returns one job by id.
func (s *JobStore) GetByID(id string) (SyncJob, error) {
	if !s.Enabled() {
		return SyncJob{}, fmt.Errorf("sync job store disabled")
	}
	row := s.db.QueryRow(`SELECT `+syncJobColumns+` FROM sync_jobs WHERE id=?`, id)
	job, err := scanSyncJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return SyncJob{}, ErrSyncJobNotFound
	}
	return job, err
}

// GetByKey returns one job by idempotency key.
func (s *JobStore) GetByKey(kind, jobType, service, entityKey string) (SyncJob, bool) {
	if !s.Enabled() {
		return SyncJob{}, false
	}
	key := syncJobIdempotencyKey(kind, jobType, service, entityKey)
	row := s.db.QueryRow(`SELECT `+syncJobColumns+` FROM sync_jobs WHERE idempotency_key=?`, key)
	job, err := scanSyncJob(row)
	if err != nil {
		return SyncJob{}, false
	}
	return job, true
}

const syncJobColumns = `id, kind, job_type, service, idempotency_key, payload, state,
  attempts, max_attempts, priority, next_run_at, lease_owner, lease_until,
  progress, last_error, last_error_class,
  created_at, updated_at, started_at, finished_at`

func scanSyncJob(row rowScanner) (SyncJob, error) {
	var (
		j                  SyncJob
		id                 int64
		payload, progress  string
		nextRun            string
		leaseUntil         string
		createdAt, updated string
		startedAt, finAt   string
	)
	err := row.Scan(&id, &j.Kind, &j.JobType, &j.Service, &j.IdempotencyKey,
		&payload, &j.State, &j.Attempts, &j.MaxAttempts, &j.Priority, &nextRun,
		&j.LeaseOwner, &leaseUntil, &progress, &j.LastError, &j.LastErrorClass,
		&createdAt, &updated, &startedAt, &finAt)
	if err != nil {
		return SyncJob{}, err
	}
	j.ID = fmt.Sprint(id)
	j.Payload = json.RawMessage(payload)
	j.Progress = json.RawMessage(progress)
	j.NextRunAt = parseSyncTime(nextRun)
	j.LeaseUntil = parseSyncTime(leaseUntil)
	j.CreatedAt = parseSyncTime(createdAt)
	j.UpdatedAt = parseSyncTime(updated)
	j.StartedAt = parseSyncTime(startedAt)
	j.FinishedAt = parseSyncTime(finAt)
	return j, nil
}

func parseSyncTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func formatSyncTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// Claim atomically takes the next due job: a queued job whose next_run_at has
// passed, or a running job whose lease expired (crash recovery). The caller
// owns the job until LeaseUntil. Returns false when nothing is due.
func (s *JobStore) Claim(owner string, now time.Time) (SyncJob, bool, error) {
	if !s.Enabled() {
		return SyncJob{}, false, nil
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return SyncJob{}, false, fmt.Errorf("claim requires a lease owner")
	}
	nowS := now.UTC().Format(time.RFC3339Nano)
	leaseUntil := now.Add(syncJobLease).UTC().Format(time.RFC3339Nano)

	tx, err := s.db.Begin()
	if err != nil {
		return SyncJob{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	var id int64
	err = tx.QueryRow(`
SELECT id FROM sync_jobs
WHERE ((state = 'queued' AND next_run_at <= ?)
   OR (state = 'running' AND lease_until <> '' AND lease_until < ?))
  AND service NOT IN (SELECT scope FROM sync_pause)
  AND NOT EXISTS (SELECT 1 FROM sync_pause WHERE scope = 'global')
ORDER BY priority DESC, next_run_at ASC
LIMIT 1
`, nowS, nowS).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return SyncJob{}, false, nil
	}
	if err != nil {
		return SyncJob{}, false, err
	}

	res, err := tx.Exec(`
UPDATE sync_jobs
SET state='running', lease_owner=?, lease_until=?,
    attempts=attempts+1,
    started_at=CASE WHEN started_at='' THEN ? ELSE started_at END,
    updated_at=?
WHERE id=? AND (
  (state='queued' AND next_run_at <= ?)
  OR (state='running' AND lease_until <> '' AND lease_until < ?)
)
`, owner, leaseUntil, nowS, nowS, id, nowS, nowS)
	if err != nil {
		return SyncJob{}, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return SyncJob{}, false, err
	}
	if n == 0 {
		// Lost the race to another worker; caller retries the claim.
		return SyncJob{}, false, nil
	}
	if err := tx.Commit(); err != nil {
		return SyncJob{}, false, err
	}
	job, err := s.GetByID(fmt.Sprint(id))
	if err != nil {
		return SyncJob{}, false, err
	}
	return job, true, nil
}

// ErrSyncJobLost reports that a finished step no longer owned its job: the
// lease expired and was reclaimed by another worker, or the job was canceled
// mid-flight. The step's outcome is discarded and no completion side effects
// (metrics, onDone, cache invalidation) should run.
var ErrSyncJobLost = errors.New("sync job ownership lost")

// finishOwnedJob applies a step outcome only while this caller still owns the
// job. A cancel or a reclaimed lease moves the row on without us, and a stale
// step must not drag it back to queued. Returns ErrSyncJobLost when the
// update matched no row. success resets the attempt budget so a long chain of
// healthy pages is not killed by max_attempts (the budget bounds failures on
// the *current* step, not the whole chain).
func (s *JobStore) finishOwnedJob(job SyncJob, success bool, state, payload, progress, nextRun, finished, nowS, errText, errClass string) (SyncJob, error) {
	attempts := "attempts"
	if success {
		attempts = "0"
	}
	res, err := s.db.Exec(`
UPDATE sync_jobs
SET state=?, payload=?, progress=?, next_run_at=?,
    lease_owner='', lease_until='',
    attempts=`+attempts+`,
    last_error=?, last_error_class=?,
    finished_at=?, updated_at=?
WHERE id=? AND state='running' AND lease_owner=?
`, state, payload, progress, nextRun, errText, errClass, finished, nowS,
		job.ID, job.LeaseOwner)
	if err != nil {
		return job, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return job, err
	}
	if n == 0 {
		return job, ErrSyncJobLost
	}
	return s.GetByID(job.ID)
}

// Complete records a successful step. step.Done finishes the job; otherwise
// the same row is re-armed as queued with the next payload (job chaining).
// Returns ErrSyncJobLost when the job was canceled or reclaimed first.
func (s *JobStore) Complete(job SyncJob, step SyncStep) (SyncJob, error) {
	if !s.Enabled() {
		return job, nil
	}
	now := s.clock()
	nowS := formatSyncTime(now)
	progress := "{}"
	if len(step.Progress) > 0 {
		b, err := json.Marshal(step.Progress)
		if err != nil {
			return job, err
		}
		progress = string(b)
	}
	state := SyncJobQueued
	finished := ""
	nextRun := nowS
	if step.Done {
		state = SyncJobSucceeded
		finished = nowS
	}
	payload := string(job.Payload)
	if len(step.Payload) > 0 {
		payload = string(step.Payload)
	}
	// A successful step resets the attempt budget (success=true below).
	return s.finishOwnedJob(job, true, state, payload, progress, nextRun, finished, nowS, "", "")
}

// Fail records a failed step. Retryable failures schedule a backoff; terminal
// failures (or an exhausted attempt budget) park the job in failed. Returns
// ErrSyncJobLost when the job was canceled or reclaimed first.
func (s *JobStore) Fail(job SyncJob, step SyncStep, runErr error) (SyncJob, error) {
	if !s.Enabled() {
		return job, nil
	}
	class, after := classifySyncError(runErr, job.Attempts, step.RetryAfter)
	errText := ""
	if runErr != nil {
		errText = runErr.Error()
	}
	now := s.clock()
	nowS := formatSyncTime(now)

	state := SyncJobFailed
	nextRun := nowS
	finished := nowS
	if class == SyncErrorClassRetry && job.Attempts < job.MaxAttempts {
		state = SyncJobQueued
		finished = ""
		nextRun = formatSyncTime(now.Add(after))
	}
	progress := "{}"
	if len(step.Progress) > 0 {
		if b, err := json.Marshal(step.Progress); err == nil {
			progress = string(b)
		}
	}
	payload := string(job.Payload)
	if len(step.Payload) > 0 {
		payload = string(step.Payload)
	}
	return s.finishOwnedJob(job, false, state, payload, progress, nextRun, finished, nowS, errText, class)
}

// classifySyncError buckets a step failure and resolves the backoff.
// Terminal: explicit marker, or no usable credentials at all — retrying
// cannot help until the user re-authorizes, so park the job rather than burn
// the attempt budget (providers only surface ErrUnauthorized when
// GetValidToken finds nothing; a refreshable 401 mid-run comes back as an
// upstream HTTP error and stays retryable).
// Retryable: everything else, including provider quota windows, which get a
// long explicit backoff via SyncRetryAfter.
// attempt is the job's post-claim attempt count (1-based); the delay grows
// exponentially from it.
func classifySyncError(runErr error, attempt int, retryAfter time.Duration) (class string, after time.Duration) {
	after = syncBackoff(attempt)
	if retryAfter > 0 {
		after = retryAfter
	}
	if runErr == nil {
		return SyncErrorClassRetry, after
	}
	if errors.Is(runErr, ErrSyncTerminal) {
		return SyncErrorClassTerminal, after
	}
	if errors.Is(runErr, types.ErrUnauthorized) {
		return SyncErrorClassTerminal, after
	}
	var ra *SyncRetryAfter
	if errors.As(runErr, &ra) {
		if ra.After > 0 {
			after = ra.After
		}
		return SyncErrorClassRetry, after
	}
	return SyncErrorClassRetry, after
}

// syncBackoff returns an exponential delay for attempt n (1-based) with real
// jitter, capped so a flapping provider cannot park a job for hours.
func syncBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	base := 30 * time.Second
	d := base << (attempt - 1)
	if d <= 0 || d > 30*time.Minute {
		d = 30 * time.Minute
	}
	// Up to 25% random jitter so concurrent workers retrying the same
	// failure do not collide on the same tick.
	return d + time.Duration(rand.Int64N(int64(d/4)+1))
}

// Cancel parks a job that should not run again. Returns ErrSyncJobNotFound
// when no such job exists.
func (s *JobStore) Cancel(id string) (SyncJob, error) {
	if !s.Enabled() {
		return SyncJob{}, fmt.Errorf("sync job store disabled")
	}
	now := formatSyncTime(s.clock())
	_, err := s.db.Exec(`
UPDATE sync_jobs SET state=?, lease_owner='', lease_until='', finished_at=?, updated_at=?
WHERE id=? AND state IN ('queued','running','paused')
`, SyncJobCanceled, now, now, id)
	if err != nil {
		return SyncJob{}, err
	}
	return s.GetByID(id)
}

// SyncPauseScopeGlobal is the pause key that stops the whole queue.
const SyncPauseScopeGlobal = "global"

// Pause stops new work for a scope ("global" or a service id). Queued jobs
// for that scope move to `paused` so the count stays honest, and Enqueue
// routes new work there too. In-flight steps are not interrupted — use
// CancelJob for that.
func (s *JobStore) Pause(scope, reason string) error {
	if !s.Enabled() {
		return nil
	}
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = SyncPauseScopeGlobal
	}
	now := formatSyncTime(s.clock())
	_, err := s.db.Exec(`
INSERT INTO sync_pause (scope, reason, created_at) VALUES (?,?,?)
ON CONFLICT(scope) DO UPDATE SET reason=excluded.reason, created_at=excluded.created_at
`, scope, strings.TrimSpace(reason), now)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
UPDATE sync_jobs SET state='paused', updated_at=?
WHERE state='queued' AND (? = ? OR service = ?)
`, now, scope, SyncPauseScopeGlobal, scope)
	return err
}

// Resume re-arms a paused scope: the pause marker is dropped and its `paused`
// jobs go back to `queued`.
func (s *JobStore) Resume(scope string) error {
	if !s.Enabled() {
		return nil
	}
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = SyncPauseScopeGlobal
	}
	now := formatSyncTime(s.clock())
	if _, err := s.db.Exec(`DELETE FROM sync_pause WHERE scope=?`, scope); err != nil {
		return err
	}
	// A job is still held back if the global pause is on and this was only a
	// per-service resume.
	globalHeld := scope != SyncPauseScopeGlobal && s.isPausedLocked(SyncPauseScopeGlobal)
	if globalHeld {
		return nil
	}
	_, err := s.db.Exec(`
UPDATE sync_jobs SET state='queued', updated_at=?
WHERE state='paused' AND (? = ? OR service = ?)
`, now, scope, SyncPauseScopeGlobal, scope)
	return err
}

// Paused lists the active pause scopes ("global" and/or service ids).
func (s *JobStore) Paused() []string {
	if !s.Enabled() {
		return nil
	}
	rows, err := s.db.Query(`SELECT scope FROM sync_pause ORDER BY scope`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]string, 0, 2)
	for rows.Next() {
		var scope string
		if err := rows.Scan(&scope); err != nil {
			break
		}
		out = append(out, scope)
	}
	return out
}

// isPausedLocked reports whether a scope currently has a pause marker.
func (s *JobStore) isPausedLocked(scope string) bool {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_pause WHERE scope=?`, scope).Scan(&n)
	return err == nil && n > 0
}

// IsPaused reports whether work for a scope (or the whole queue via
// "global") is paused.
func (s *JobStore) IsPaused(scope string) bool {
	if !s.Enabled() {
		return false
	}
	if s.isPausedLocked(SyncPauseScopeGlobal) {
		return true
	}
	return s.isPausedLocked(strings.TrimSpace(scope))
}

// ReclaimExpired moves leased-but-stale running jobs back to queued. Runs
// periodically from the worker so a wedged step does not stall the queue for
// the whole lease window.
func (s *JobStore) ReclaimExpired(now time.Time) (int, error) {
	if !s.Enabled() {
		return 0, nil
	}
	nowS := now.UTC().Format(time.RFC3339Nano)
	res, err := s.db.Exec(`
UPDATE sync_jobs
SET state='queued', lease_owner='', lease_until='', updated_at=?
WHERE state='running' AND lease_until <> '' AND lease_until < ?
`, nowS, nowS)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// ReclaimAllRunning returns every running job to queued. Called at startup: a
// single-process restart means no in-flight step survived, so waiting out a
// 30-minute lease would stall the queue for nothing.
func (s *JobStore) ReclaimAllRunning() (int, error) {
	if !s.Enabled() {
		return 0, nil
	}
	nowS := formatSyncTime(s.clock())
	res, err := s.db.Exec(`
UPDATE sync_jobs
SET state='queued', lease_owner='', lease_until='', updated_at=?
WHERE state='running'
`, nowS)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// Prune drops finished jobs older than retain. Matches the audit log's
// retention posture so a noisy pipeline cannot fill the disk.
func (s *JobStore) Prune(retain time.Duration) (int, error) {
	if !s.Enabled() {
		return 0, nil
	}
	cutoff := formatSyncTime(s.clock().Add(-retain))
	res, err := s.db.Exec(`
DELETE FROM sync_jobs
WHERE state IN ('succeeded','failed','canceled')
  AND finished_at <> '' AND finished_at < ?
`, cutoff)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// List returns jobs newest-first, optionally filtered.
func (s *JobStore) List(f SyncFilter) []SyncJob {
	if !s.Enabled() {
		return nil
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT ` + syncJobColumns + ` FROM sync_jobs WHERE 1=1`
	args := make([]any, 0, 4)
	if f.Kind != "" {
		q += ` AND kind=?`
		args = append(args, f.Kind)
	}
	if f.JobType != "" {
		q += ` AND job_type=?`
		args = append(args, f.JobType)
	}
	if f.Service != "" {
		q += ` AND service=?`
		args = append(args, f.Service)
	}
	if f.State != "" {
		q += ` AND state=?`
		args = append(args, f.State)
	}
	q += ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	return scanSyncJobs(rows)
}

func scanSyncJobs(rows *sql.Rows) []SyncJob {
	out := make([]SyncJob, 0, 8)
	for rows.Next() {
		job, err := scanSyncJob(rows)
		if err != nil {
			break
		}
		out = append(out, job)
	}
	return out
}

// SyncCounts is the queue rollup used by the admin surface and metrics.
type SyncCounts struct {
	Queued    int `json:"queued"`
	Running   int `json:"running"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Canceled  int `json:"canceled"`
	Paused    int `json:"paused"`
}

// Total returns the sum of all state counts.
func (c SyncCounts) Total() int {
	return c.Queued + c.Running + c.Succeeded + c.Failed + c.Canceled + c.Paused
}

// SyncBreakdown is a global rollup of job states grouped by kind and by job
// type. Computed from the whole table, not just recent rows, so the admin
// summary matches Counts.
type SyncBreakdown struct {
	ByKind    map[string]SyncCounts `json:"by_kind,omitempty"`
	ByJobType map[string]SyncCounts `json:"by_job_type,omitempty"`
}

// Breakdown rolls up states grouped by kind and by job type.
func (s *JobStore) Breakdown() SyncBreakdown {
	out := SyncBreakdown{
		ByKind:    make(map[string]SyncCounts),
		ByJobType: make(map[string]SyncCounts),
	}
	if !s.Enabled() {
		return out
	}
	rows, err := s.db.Query(`SELECT kind, job_type, state, COUNT(*) FROM sync_jobs GROUP BY kind, job_type, state`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var kind, jobType, state string
		var n int
		if err := rows.Scan(&kind, &jobType, &state, &n); err != nil {
			break
		}
		k := out.ByKind[kind]
		bumpSyncCount(&k, state, n)
		out.ByKind[kind] = k
		jt := out.ByJobType[jobType]
		bumpSyncCount(&jt, state, n)
		out.ByJobType[jobType] = jt
	}
	return out
}

// bumpSyncCount adds n jobs of the given state to c.
func bumpSyncCount(c *SyncCounts, state string, n int) {
	switch state {
	case SyncJobQueued:
		c.Queued += n
	case SyncJobRunning:
		c.Running += n
	case SyncJobSucceeded:
		c.Succeeded += n
	case SyncJobFailed:
		c.Failed += n
	case SyncJobCanceled:
		c.Canceled += n
	case SyncJobPaused:
		c.Paused += n
	}
}

// Counts rolls up job states, optionally scoped to one kind or job type.
func (s *JobStore) Counts(kind, jobType string) SyncCounts {
	var c SyncCounts
	if !s.Enabled() {
		return c
	}
	q := `SELECT state, COUNT(*) FROM sync_jobs WHERE 1=1`
	args := make([]any, 0, 2)
	if kind != "" {
		q += ` AND kind=?`
		args = append(args, kind)
	}
	if jobType != "" {
		q += ` AND job_type=?`
		args = append(args, jobType)
	}
	q += ` GROUP BY state`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return c
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			break
		}
		switch state {
		case SyncJobQueued:
			c.Queued += n
		case SyncJobRunning:
			c.Running += n
		case SyncJobSucceeded:
			c.Succeeded += n
		case SyncJobFailed:
			c.Failed += n
		case SyncJobCanceled:
			c.Canceled += n
		case SyncJobPaused:
			c.Paused += n
		}
	}
	return c
}
