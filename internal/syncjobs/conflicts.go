package syncjobs

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/core/copies"
)

// Sync conflicts (issue #365 phase 3): a detected non-hub edit awaiting
// manual resolution. Detection comes from watermark/freshness divergence
// (the freshness pass, and the checks converge runs make as it goes); the
// admin library page offers accept (apply to the canonical home, then fan
// out) or reject (re-converge that member). Records are one row per
// divergence locus — (kind, member, entity, field) — re-detected values
// update the row and reopen a resolved one when the member diverges again.
//
// Deletes are not resolved here: member-side deletion of a canonical item
// is restored by convergence (never deletes means the reverse never runs),
// and adopting a member's delete into the canonical home is delete
// propagation (#369). A vanished member copy is recorded so the operator
// can reject it (re-create from the canonical home).

// Conflict fields name what diverged on the member.
const (
	ConflictFieldTitle   = "title"
	ConflictFieldItems   = "items"
	ConflictFieldRecord  = "record"
	ConflictFieldMissing = "missing"
)

// Conflict statuses.
const (
	ConflictStatusOpen     = "open"
	ConflictStatusAccepted = "accepted"
	ConflictStatusRejected = "rejected"
)

// Conflict detection sources.
const (
	ConflictSourceConverge  = "converge"
	ConflictSourceFreshness = "freshness"
)

// SyncConflict is one detected non-hub edit on a member copy.
type SyncConflict struct {
	ID string `json:"id"`
	// Kind is the library kind ("playlists", "contacts", …).
	Kind string `json:"kind"`
	// Service is the member whose copy diverged.
	Service string `json:"service"`
	// EntityKey locates the copy row (the same key the copy ledger uses).
	EntityKey string `json:"entity_key"`
	// ExternalID is the member-side id of the diverged copy, when known.
	ExternalID string `json:"external_id,omitempty"`
	// Subject is a human label (playlist title, contact name).
	Subject string `json:"subject,omitempty"`
	// Field names the divergence: title, items, record, or missing.
	Field string `json:"field"`
	// MemberValue and LastValue describe the divergence for the panel
	// (titles, counts, tokens — formatted by the detector).
	MemberValue string `json:"member_value,omitempty"`
	LastValue   string `json:"last_value,omitempty"`
	// Source names the detection channel: converge or freshness.
	Source     string    `json:"source,omitempty"`
	Status     string    `json:"status"`
	Note       string    `json:"note,omitempty"`
	DetectedAt time.Time `json:"detected_at"`
	ResolvedAt time.Time `json:"resolved_at,omitempty"`
}

const syncConflictsSchema = `
CREATE TABLE IF NOT EXISTS sync_conflicts (
  id INTEGER PRIMARY KEY,
  kind TEXT NOT NULL,
  service TEXT NOT NULL,
  entity_key TEXT NOT NULL,
  external_id TEXT NOT NULL DEFAULT '',
  subject TEXT NOT NULL DEFAULT '',
  field TEXT NOT NULL,
  member_value TEXT NOT NULL DEFAULT '',
  last_value TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'open',
  note TEXT NOT NULL DEFAULT '',
  detected_at TEXT NOT NULL,
  resolved_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(kind, service, entity_key, field)
);
CREATE INDEX IF NOT EXISTS sync_conflicts_status ON sync_conflicts(status, kind);
`

// ConflictStore is the durable conflict queue. Co-located with the job
// queue by default so a detection and its job state share one file.
type ConflictStore struct {
	db   *sql.DB
	path string
	own  bool
	now  func() time.Time
}

// OpenConflictStore opens the conflict table. When shared is non-nil the
// table is created in that handle and path is informational; a nil shared
// with a non-empty path opens the store's own database; empty path with nil
// shared returns a disabled store.
func OpenConflictStore(shared *sql.DB, path string) (*ConflictStore, error) {
	clock := func() time.Time { return time.Now().UTC() }
	if shared != nil {
		if err := initSyncConflictsSchema(shared); err != nil {
			return nil, err
		}
		return &ConflictStore{db: shared, path: path, now: clock}, nil
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return &ConflictStore{now: clock}, nil
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
	if err := initSyncConflictsSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return &ConflictStore{db: db, path: path, own: true, now: clock}, nil
}

func initSyncConflictsSchema(db *sql.DB) error {
	if _, err := db.Exec(syncConflictsSchema); err != nil {
		return fmt.Errorf("sync conflicts schema: %w", err)
	}
	return nil
}

// Close releases the handle when this store owns it.
func (s *ConflictStore) Close() error {
	if s == nil || s.db == nil || !s.own {
		return nil
	}
	return s.db.Close()
}

// Enabled reports whether the store can persist rows.
func (s *ConflictStore) Enabled() bool {
	return s != nil && s.db != nil
}

func (s *ConflictStore) clock() time.Time {
	if s == nil || s.now == nil {
		return time.Now().UTC()
	}
	return s.now()
}

// Upsert records a detection. A row for the same locus (kind, service,
// entity, field) is refreshed in place. A resolved row is reopened only
// when the member-side value changed — the member diverged again — so a
// rejected edit that stays (member-only items are never removed) is not
// re-flagged every run, and re-accepting after the canonical home moved
// is not forced either (the member's edit is what was decided about).
func (s *ConflictStore) Upsert(rec SyncConflict) (SyncConflict, error) {
	if !s.Enabled() {
		return rec, nil
	}
	rec.Kind = strings.TrimSpace(rec.Kind)
	rec.Service = strings.TrimSpace(rec.Service)
	rec.EntityKey = strings.TrimSpace(rec.EntityKey)
	rec.Field = strings.TrimSpace(rec.Field)
	if rec.Kind == "" || rec.Service == "" || rec.EntityKey == "" || rec.Field == "" {
		return SyncConflict{}, fmt.Errorf("conflict requires kind, service, entity_key, and field")
	}
	now := s.clock()
	nowS := formatSyncTime(now)
	if rec.DetectedAt.IsZero() {
		rec.DetectedAt = now
	}
	if prev, err := s.getByLocus(rec.Kind, rec.Service, rec.EntityKey, rec.Field); err == nil &&
		prev.Status != ConflictStatusOpen &&
		prev.MemberValue == rec.MemberValue {
		return prev, nil
	}
	rec.Status = ConflictStatusOpen
	rec.ResolvedAt = time.Time{}

	// Reopen semantics: the locus keeps its identity (and resolution
	// history in the row's lifetime) but a fresh detection is open again.
	_, err := s.db.Exec(`
INSERT INTO sync_conflicts (
  kind, service, entity_key, external_id, subject, field,
  member_value, last_value, source, status, note,
  detected_at, resolved_at, created_at, updated_at
) VALUES (?,?,?,?,?,?, ?,?,?,?,'', ?, '', ?, ?)
ON CONFLICT(kind, service, entity_key, field) DO UPDATE SET
  external_id=excluded.external_id,
  subject=excluded.subject,
  member_value=excluded.member_value,
  last_value=excluded.last_value,
  source=excluded.source,
  status=excluded.status,
  note='',
  detected_at=excluded.detected_at,
  resolved_at='',
  updated_at=excluded.updated_at
`, rec.Kind, rec.Service, rec.EntityKey, rec.ExternalID, rec.Subject, rec.Field,
		rec.MemberValue, rec.LastValue, rec.Source, rec.Status,
		formatSyncTime(rec.DetectedAt), nowS, nowS)
	if err != nil {
		return SyncConflict{}, err
	}
	return s.getByLocus(rec.Kind, rec.Service, rec.EntityKey, rec.Field)
}

// Resolve flips a conflict to accepted or rejected, keeping the row as
// history. A later detection of the same locus reopens it.
func (s *ConflictStore) Resolve(id, status, note string) (SyncConflict, error) {
	if !s.Enabled() {
		return SyncConflict{}, fmt.Errorf("conflict store is disabled")
	}
	status = strings.TrimSpace(status)
	if status != ConflictStatusAccepted && status != ConflictStatusRejected {
		return SyncConflict{}, fmt.Errorf("unknown conflict status %q", status)
	}
	res, err := s.db.Exec(`
UPDATE sync_conflicts SET status=?, note=?, resolved_at=?, updated_at=?
WHERE id=?
`, status, strings.TrimSpace(note), formatSyncTime(s.clock()), formatSyncTime(s.clock()), id)
	if err != nil {
		return SyncConflict{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return SyncConflict{}, err
	}
	if n == 0 {
		return SyncConflict{}, fmt.Errorf("conflict %q not found", id)
	}
	rec, ok := s.Get(id)
	if !ok {
		return SyncConflict{}, fmt.Errorf("conflict %q not found", id)
	}
	return rec, nil
}

// Get returns one conflict by id.
func (s *ConflictStore) Get(id string) (SyncConflict, bool) {
	if !s.Enabled() {
		return SyncConflict{}, false
	}
	row := s.db.QueryRow(`SELECT `+conflictColumns+` FROM sync_conflicts WHERE id=?`, id)
	rec, err := scanConflict(row)
	if err != nil {
		return SyncConflict{}, false
	}
	return rec, true
}

// ConflictFilter narrows List.
type ConflictFilter struct {
	Status string
	Kind   string
}

// List returns conflicts matching the filter (empty filter = every status).
func (s *ConflictStore) List(f ConflictFilter) []SyncConflict {
	if !s.Enabled() {
		return nil
	}
	q := `SELECT ` + conflictColumns + ` FROM sync_conflicts`
	var args []any
	var where []string
	if st := strings.TrimSpace(f.Status); st != "" {
		where = append(where, "status=?")
		args = append(args, st)
	}
	if k := strings.TrimSpace(f.Kind); k != "" {
		where = append(where, "kind=?")
		args = append(args, k)
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY detected_at DESC, id DESC"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]SyncConflict, 0, 4)
	for rows.Next() {
		rec, err := scanConflict(rows)
		if err != nil {
			break
		}
		out = append(out, rec)
	}
	return out
}

func (s *ConflictStore) getByLocus(kind, service, entityKey, field string) (SyncConflict, error) {
	row := s.db.QueryRow(`SELECT `+conflictColumns+` FROM sync_conflicts
WHERE kind=? AND service=? AND entity_key=? AND field=?`,
		kind, service, entityKey, field)
	return scanConflict(row)
}

const conflictColumns = `id, kind, service, entity_key, external_id, subject, field,
  member_value, last_value, source, status, note, detected_at, resolved_at`

func scanConflict(row interface{ Scan(...any) error }) (SyncConflict, error) {
	var (
		rec                SyncConflict
		id                 int64
		detectedS, resolvS string
	)
	err := row.Scan(&id, &rec.Kind, &rec.Service, &rec.EntityKey, &rec.ExternalID,
		&rec.Subject, &rec.Field, &rec.MemberValue, &rec.LastValue,
		&rec.Source, &rec.Status, &rec.Note, &detectedS, &resolvS)
	if err != nil {
		return SyncConflict{}, err
	}
	rec.ID = fmt.Sprint(id)
	rec.DetectedAt = parseSyncTime(detectedS)
	rec.ResolvedAt = parseSyncTime(resolvS)
	return rec, nil
}

// classifyCopyDivergence names what diverged between a recorded copy and
// its live tokens (issue #365 phase 3), or "" when nothing did. Staleness
// stays content-only (a rename is not a drifted backup) — this is the
// conflict signal, which is wider: renames and membership changes are
// non-hub edits too. Member-side creates and content swaps land as items;
// item-level content edits as record. A pure shortfall (the member holds
// fewer items) is not flagged: convergence restores it on the next run,
// and adopting a member's deletions is delete propagation (#369).
func classifyCopyDivergence(want, got copies.Version, exists bool) string {
	if !exists {
		return ConflictFieldMissing
	}
	if want.Name != "" && got.Name != "" && want.Name != got.Name {
		return ConflictFieldTitle
	}
	if want.ItemCount > 0 && got.ItemCount > want.ItemCount {
		return ConflictFieldItems
	}
	if want.SnapshotID != "" && got.SnapshotID != "" && want.SnapshotID != got.SnapshotID {
		return ConflictFieldItems
	}
	if want.ETag != "" && got.ETag != "" && want.ETag != got.ETag {
		return ConflictFieldRecord
	}
	if want.ContentHash != "" && got.ContentHash != "" && want.ContentHash != got.ContentHash {
		return ConflictFieldRecord
	}
	return ""
}

// recordConflict upserts one detection (issue #365 phase 3). Detection is
// best-effort: a ledger failure is logged and the divergence shows up
// again on the next run.
func (s *Runners) recordConflict(rec SyncConflict) {
	if s == nil || s.Conflicts == nil || !s.Conflicts.Enabled() {
		return
	}
	if _, err := s.Conflicts.Upsert(rec); err != nil {
		slog.Warn("sync conflict record failed",
			"kind", rec.Kind, "service", rec.Service,
			"entity", rec.EntityKey, "field", rec.Field, "error", err)
	}
}
