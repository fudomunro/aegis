package core

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	SyncJobTypeIngest        = "ingest"
	SyncJobTypeImport        = "import_snapshot"
	SyncJobTypeMirror        = "mirror"
	SyncJobTypeFreshness     = "freshness_check"
	SyncJobTypeRepair        = "repair"
	SyncJobTypeErrorRetry    = "retryable"
	SyncJobTypeErrorTerminal = "terminal"
)

// syncJobDefaultMaxAttempts bounds retries before a job parks in failed.
const syncJobDefaultMaxAttempts = 5

// syncJobLease is how long a claimed job stays owned before another worker
// may reclaim it. Generous enough for one paced mail page (200 messages at
// the Gmail get interval), short enough that a crash does not stall the queue.
const syncJobLease = 30 * time.Minute

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
	Kind       string
	JobType    string
	Service    string
	EntityKey  string
	Payload    map[string]any
	Priority   int
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

// Path returns the on-disk location ("" when disabled or shared).
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
// A repeat request on a terminal job (succeeded/failed/canceled) resets it to
// queued with a fresh attempt budget; queued/running/paused jobs are left
// alone so an in-flight chain is never clobbered.
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
	res, err := s.db.Exec(`
INSERT INTO sync_jobs (
  kind, job_type, service, idempotency_key, payload, state,
  attempts, max_attempts, priority, next_run_at,
  progress, created_at, updated_at
) VALUES (?,?,?,?,?, 'queued', 0, ?, ?, ?, '{}', ?, ?)
ON CONFLICT(idempotency_key) DO UPDATE SET
  payload=excluded.payload,
  max_attempts=excluded.max_attempts,
  priority=excluded.priority,
  state=CASE
    WHEN sync_jobs.state IN ('succeeded','failed','canceled') THEN 'queued'
    ELSE sync_jobs.state
  END,
  attempts=CASE
    WHEN sync_jobs.state IN ('succeeded','failed','canceled') THEN 0
    ELSE sync_jobs.attempts
  END,
  next_run_at=CASE
    WHEN sync_jobs.state IN ('succeeded','failed','canceled') THEN excluded.next_run_at
    ELSE sync_jobs.next_run_at
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
`,
		req.Kind, req.JobType, req.Service, key, payload,
		req.MaxAttempts, req.Priority, runAtS, nowS, nowS,
	)
	if err != nil {
		return SyncJob{}, err
	}
	id, err := lastInsertID(res, s.db, key)
	if err != nil {
		return SyncJob{}, err
	}
	return s.GetByID(id)
}

// lastInsertID resolves the row id for an upsert. sqlite reports the
// rowid of the insert or the conflicting row on upsert, but go-sqlite
// drivers differ; fall back to a lookup by key.
func lastInsertID(res sql.Result, db *sql.DB, key string) (string, error) {
	if res != nil {
		if id, err := res.LastInsertId(); err == nil && id > 0 {
			return fmt.Sprint(id), nil
		}
	}
	var id int64
	if err := db.QueryRow(`SELECT id FROM sync_jobs WHERE idempotency_key=?`, key).Scan(&id); err != nil {
		return "", err
	}
	return fmt.Sprint(id), nil
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
WHERE (state = 'queued' AND next_run_at <= ?)
   OR (state = 'running' AND lease_until <> '' AND lease_until < ?)
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

// Complete records a successful step. step.Done finishes the job; otherwise
// the same row is re-armed as queued with the next payload (job chaining).
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
	// A successful step resets the attempt budget so a long chain of
	// healthy pages is not killed by max_attempts (the budget bounds
	// failures on the *current* step, not the whole chain).
	_, err := s.db.Exec(`
UPDATE sync_jobs
SET state=?, payload=?, progress=?, next_run_at=?,
    lease_owner='', lease_until='',
    attempts=0,
    last_error='', last_error_class='',
    finished_at=?, updated_at=?
WHERE id=?
`, state, payload, progress, nextRun, finished, nowS, job.ID)
	if err != nil {
		return job, err
	}
	return s.GetByID(job.ID)
}

// Fail records a failed step. Retryable failures schedule a backoff; terminal
// failures (or an exhausted attempt budget) park the job in failed.
func (s *JobStore) Fail(job SyncJob, step SyncStep, runErr error) (SyncJob, error) {
	if !s.Enabled() {
		return job, nil
	}
	class, after := classifySyncError(runErr, step.RetryAfter)
	errText := ""
	if runErr != nil {
		errText = runErr.Error()
	}
	now := s.clock()
	nowS := formatSyncTime(now)

	state := SyncJobFailed
	nextRun := nowS
	finished := nowS
	if class == SyncJobTypeErrorRetry && job.Attempts < job.MaxAttempts {
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
	_, err := s.db.Exec(`
UPDATE sync_jobs
SET state=?, payload=?, progress=?, next_run_at=?,
    lease_owner='', lease_until='',
    last_error=?, last_error_class=?,
    finished_at=?, updated_at=?
WHERE id=?
`, state, payload, progress, nextRun, errText, class, finished, nowS, job.ID)
	if err != nil {
		return job, err
	}
	return s.GetByID(job.ID)
}

// classifySyncError buckets a step failure and resolves the backoff.
// Terminal: explicit marker, no usable credentials, canceled lease owner.
// Retryable: everything else, including provider quota windows — a job parked
// forever after a transient OAuth gap would need manual re-enqueue, so those
// get a long backoff instead (see SyncRetryAfter).
func classifySyncError(runErr error, retryAfter time.Duration) (class string, after time.Duration) {
	after = syncBackoff(1)
	if retryAfter > 0 {
		after = retryAfter
	}
	if runErr == nil {
		return SyncJobTypeErrorRetry, after
	}
	if errors.Is(runErr, ErrSyncTerminal) {
		return SyncJobTypeErrorTerminal, after
	}
	// No usable token at all: retrying cannot help until the user
	// re-authorizes, so park it rather than burn the attempt budget.
	if errors.Is(runErr, types.ErrUnauthorized) {
		return SyncJobTypeErrorTerminal, after
	}
	var ra *SyncRetryAfter
	if errors.As(runErr, &ra) {
		if ra.After > 0 {
			after = ra.After
		}
		return SyncJobTypeErrorRetry, after
	}
	return SyncJobTypeErrorRetry, after
}

// syncBackoff returns an exponential delay with jitter for attempt n (1-based),
// capped so a flapping provider cannot park a job for hours.
func syncBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	base := 30 * time.Second
	d := base << (attempt - 1)
	if d <= 0 || d > 30*time.Minute {
		d = 30 * time.Minute
	}
	// Deterministic jitter (attempt-scaled) keeps the shape testable while
	// still spreading retries across workers.
	jitter := time.Duration(attempt*17%7) * time.Second
	return d + jitter
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

// ReclaimExpired moves leased-but-stale running jobs back to queued. Called
// on startup and periodically so a crash mid-step loses no work.
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
