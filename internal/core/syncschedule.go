package core

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Sync schedules (issue #227 phase 2): recurring job templates that keep the
// queue fed without anyone clicking anything. A schedule is a row with its
// own next_run_at; when it comes due the worker enqueues the job (idempotent,
// so a still-queued job is not duplicated) and re-arms the schedule. Same
// table family and same disable semantics as the job store.

const syncSchedulesSchema = `
CREATE TABLE IF NOT EXISTS sync_schedules (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL,
  job_type TEXT NOT NULL,
  service TEXT NOT NULL DEFAULT '',
  entity_key TEXT NOT NULL DEFAULT '',
  payload TEXT NOT NULL DEFAULT '{}',
  every_seconds INTEGER NOT NULL DEFAULT 86400,
  enabled INTEGER NOT NULL DEFAULT 1,
  next_run_at TEXT NOT NULL,
  last_enqueued_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(kind, job_type, service, entity_key)
);
CREATE INDEX IF NOT EXISTS sync_schedules_due ON sync_schedules(enabled, next_run_at);
`

// SyncSchedule is one recurring job template.
type SyncSchedule struct {
	ID             int64           `json:"id"`
	Name           string          `json:"name,omitempty"`
	Kind           string          `json:"kind"`
	JobType        string          `json:"job_type"`
	Service        string          `json:"service,omitempty"`
	EntityKey      string          `json:"entity_key,omitempty"`
	Payload        json.RawMessage `json:"payload,omitempty"`
	EverySeconds   int             `json:"every_seconds"`
	Enabled        bool            `json:"enabled"`
	NextRunAt      time.Time       `json:"next_run_at"`
	LastEnqueuedAt time.Time       `json:"last_enqueued_at,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

// ScheduleStore holds recurring sync job templates alongside the job queue.
type ScheduleStore struct {
	db   *sql.DB
	path string
	own  bool
	now  func() time.Time
}

// OpenScheduleStore opens the schedule table. A non-nil shared handle is
// co-located with the job/copy stores (the default layout); a nil shared
// handle with a non-empty path opens its own file. Empty path and nil shared
// returns a disabled store.
func OpenScheduleStore(shared *sql.DB, path string) (*ScheduleStore, error) {
	clock := func() time.Time { return time.Now().UTC() }
	if shared != nil {
		if err := initSyncSchedulesSchema(shared); err != nil {
			return nil, err
		}
		return &ScheduleStore{db: shared, path: path, now: clock}, nil
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return &ScheduleStore{now: clock}, nil
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
	if err := initSyncSchedulesSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return &ScheduleStore{db: db, path: path, own: true, now: clock}, nil
}

func initSyncSchedulesSchema(db *sql.DB) error {
	if _, err := db.Exec(syncSchedulesSchema); err != nil {
		return fmt.Errorf("sync schedules schema: %w", err)
	}
	return nil
}

// Close releases the handle when this store owns it.
func (s *ScheduleStore) Close() error {
	if s == nil || s.db == nil || !s.own {
		return nil
	}
	return s.db.Close()
}

// Enabled reports whether the store can persist rows.
func (s *ScheduleStore) Enabled() bool {
	return s != nil && s.db != nil
}

func (s *ScheduleStore) clock() time.Time {
	if s == nil || s.now == nil {
		return time.Now().UTC()
	}
	return s.now()
}

// Upsert adds or refreshes one schedule, keyed on (kind, job_type, service,
// entity_key). An existing schedule keeps its cadence and next run unless the
// caller passes a new EverySeconds, in which case it is re-armed from now.
func (s *ScheduleStore) Upsert(sc SyncSchedule) (SyncSchedule, error) {
	sc.Kind = strings.TrimSpace(sc.Kind)
	sc.JobType = strings.TrimSpace(sc.JobType)
	sc.Service = strings.TrimSpace(sc.Service)
	sc.EntityKey = strings.TrimSpace(sc.EntityKey)
	sc.Name = strings.TrimSpace(sc.Name)
	if sc.Kind == "" || sc.JobType == "" {
		return sc, fmt.Errorf("sync schedule requires kind and job_type")
	}
	if sc.EverySeconds <= 0 {
		sc.EverySeconds = 86400
	}
	if !s.Enabled() {
		return sc, nil
	}
	now := s.clock()
	nowS := formatSyncTime(now)
	payload := "{}"
	if len(sc.Payload) > 0 {
		if b, err := json.Marshal(sc.Payload); err == nil {
			payload = string(b)
		}
	}
	enabled := 0
	if sc.Enabled {
		enabled = 1
	}
	// A new schedule starts due now so the first check does not wait a full
	// interval. Re-upserting with a changed interval re-arms from now too.
	nextRun := formatSyncTime(now)
	_, err := s.db.Exec(`
INSERT INTO sync_schedules (
  name, kind, job_type, service, entity_key, payload, every_seconds,
  enabled, next_run_at, created_at, updated_at
) VALUES (?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(kind, job_type, service, entity_key) DO UPDATE SET
  name=excluded.name,
  payload=excluded.payload,
  enabled=excluded.enabled,
  every_seconds=CASE
    WHEN excluded.every_seconds <> sync_schedules.every_seconds THEN excluded.every_seconds
    ELSE sync_schedules.every_seconds
  END,
  next_run_at=CASE
    WHEN excluded.every_seconds <> sync_schedules.every_seconds THEN excluded.next_run_at
    ELSE sync_schedules.next_run_at
  END,
  updated_at=excluded.updated_at
`,
		sc.Name, sc.Kind, sc.JobType, sc.Service, sc.EntityKey, payload,
		sc.EverySeconds, enabled, nextRun, nowS, nowS,
	)
	if err != nil {
		return sc, err
	}
	return s.getByKey(sc.Kind, sc.JobType, sc.Service, sc.EntityKey)
}

func (s *ScheduleStore) getByKey(kind, jobType, service, entityKey string) (SyncSchedule, error) {
	row := s.db.QueryRow(`
SELECT `+syncScheduleColumns+` FROM sync_schedules
WHERE kind=? AND job_type=? AND service=? AND entity_key=?
`, kind, jobType, service, entityKey)
	return scanSyncSchedule(row)
}

const syncScheduleColumns = `id, name, kind, job_type, service, entity_key, payload,
  every_seconds, enabled, next_run_at, last_enqueued_at, created_at, updated_at`

func scanSyncSchedule(row rowScanner) (SyncSchedule, error) {
	var (
		sc                   SyncSchedule
		payload              string
		enabled              int
		nextRun, lastEnq     string
		createdAt, updatedAt string
	)
	err := row.Scan(&sc.ID, &sc.Name, &sc.Kind, &sc.JobType, &sc.Service, &sc.EntityKey,
		&payload, &sc.EverySeconds, &enabled, &nextRun, &lastEnq, &createdAt, &updatedAt)
	if err != nil {
		return SyncSchedule{}, err
	}
	sc.Payload = json.RawMessage(payload)
	sc.Enabled = enabled != 0
	sc.NextRunAt = parseSyncTime(nextRun)
	sc.LastEnqueuedAt = parseSyncTime(lastEnq)
	sc.CreatedAt = parseSyncTime(createdAt)
	sc.UpdatedAt = parseSyncTime(updatedAt)
	return sc, nil
}

// List returns every schedule, enabled first then by kind.
func (s *ScheduleStore) List() []SyncSchedule {
	if !s.Enabled() {
		return nil
	}
	rows, err := s.db.Query(`SELECT ` + syncScheduleColumns + ` FROM sync_schedules ORDER BY enabled DESC, kind, job_type`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]SyncSchedule, 0, 4)
	for rows.Next() {
		sc, err := scanSyncSchedule(rows)
		if err != nil {
			break
		}
		out = append(out, sc)
	}
	return out
}

// SetEnabled flips one schedule on or off by id. Returns false when unknown.
func (s *ScheduleStore) SetEnabled(id int64, enabled bool) (SyncSchedule, bool, error) {
	if !s.Enabled() {
		return SyncSchedule{}, false, nil
	}
	val := 0
	if enabled {
		val = 1
	}
	nowS := formatSyncTime(s.clock())
	res, err := s.db.Exec(`UPDATE sync_schedules SET enabled=?, updated_at=? WHERE id=?`, val, nowS, id)
	if err != nil {
		return SyncSchedule{}, false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return SyncSchedule{}, false, err
	}
	sc, err := s.getByID(id)
	return sc, err == nil, err
}

func (s *ScheduleStore) getByID(id int64) (SyncSchedule, error) {
	row := s.db.QueryRow(`SELECT `+syncScheduleColumns+` FROM sync_schedules WHERE id=?`, id)
	return scanSyncSchedule(row)
}

// Due returns enabled schedules whose next_run_at has passed.
func (s *ScheduleStore) Due(now time.Time) []SyncSchedule {
	if !s.Enabled() {
		return nil
	}
	nowS := formatSyncTime(now)
	rows, err := s.db.Query(`
SELECT `+syncScheduleColumns+` FROM sync_schedules
WHERE enabled=1 AND next_run_at <= ?
ORDER BY next_run_at ASC
`, nowS)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]SyncSchedule, 0, 2)
	for rows.Next() {
		sc, err := scanSyncSchedule(rows)
		if err != nil {
			break
		}
		out = append(out, sc)
	}
	return out
}

// Requeue re-arms a schedule's next run from now after it has been enqueued.
func (s *ScheduleStore) Requeue(sc SyncSchedule) error {
	if !s.Enabled() {
		return nil
	}
	now := s.clock()
	_, err := s.db.Exec(`
UPDATE sync_schedules
SET next_run_at=?, last_enqueued_at=?, updated_at=?
WHERE id=?
`, formatSyncTime(now.Add(time.Duration(sc.EverySeconds)*time.Second)),
		formatSyncTime(now), formatSyncTime(now), sc.ID)
	return err
}

// defaultFreshnessSchedules are the recurring checks seeded on first start so
// freshness data accumulates without anyone configuring anything. Operators
// can disable them; the seed only fills in what is missing.
func defaultFreshnessSchedules() []SyncSchedule {
	out := make([]SyncSchedule, 0, len(freshnessKinds))
	for _, kind := range freshnessKinds {
		out = append(out, SyncSchedule{
			Name:         "Daily copy freshness",
			Kind:         kind,
			JobType:      SyncJobTypeFreshness,
			EverySeconds: 24 * 60 * 60,
			Enabled:      true,
		})
	}
	return out
}

// SeedDefaults inserts any missing default schedules. Existing rows are left
// exactly as the operator left them.
func (s *ScheduleStore) SeedDefaults() (int, error) {
	if !s.Enabled() {
		return 0, nil
	}
	added := 0
	for _, sc := range defaultFreshnessSchedules() {
		if _, err := s.getByKey(sc.Kind, sc.JobType, sc.Service, sc.EntityKey); err == nil {
			continue
		}
		if _, err := s.Upsert(sc); err != nil {
			return added, err
		}
		added++
	}
	return added, nil
}
