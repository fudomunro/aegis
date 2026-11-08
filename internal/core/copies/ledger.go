package copies

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"os"
	"path/filepath"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/shared"
	"github.com/fudomunro/aegis/internal/types"
	_ "modernc.org/sqlite"
)

// Copy ledger (issue #76): durable memory of where copies of library items
// exist and which version of the data sits there. Distinct from sources[]
// (live inventory at read time) and from entity links (identity only).
//
// Sync fidelity is per kind — see syncModeForKind:
//   - full: version token (mod date / snapshot_id / content_hash / item count)
//   - optimistic: snapshot watermark (Takeout date / ingest cursor); only
//     newer material is expected; force-full is a later repair concern (#86).

const (
	// Copy roles describe how the copy got there / its authority.
	CopyRoleCanonical = "canonical"
	CopyRoleMirror    = "mirror"
	CopyRoleImport    = "import"

	// Sync modes (issue #76).
	CopySyncModeFull       = "full"
	CopySyncModeOptimistic = "optimistic"

	// Provenance labels for how Aegis learned about the copy.
	CopyProvenanceMirror   = "mirror"
	CopyProvenanceTakeout  = "takeout"
	CopyProvenanceUser     = "user"
	CopyProvenanceObserved = "observed"
	// CopyProvenanceShare marks a copy created by an explicit share/publish
	// (photo ownership-claim PoC, issue #20).
	CopyProvenanceShare = "share"

	// Copy status values. "unknown" is the default until a check runs.
	CopyStatusUnknown = "unknown"
	CopyStatusInSync  = "in_sync"
	CopyStatusStale   = "stale"
	CopyStatusMissing = "missing"

	copyLedgerSchema = `
CREATE TABLE IF NOT EXISTS copy_records (
  id INTEGER PRIMARY KEY,
  kind TEXT NOT NULL,
  entity_key TEXT NOT NULL,
  source_service TEXT NOT NULL DEFAULT '',
  source_id TEXT NOT NULL DEFAULT '',
  service TEXT NOT NULL,
  external_id TEXT NOT NULL DEFAULT '',
  role TEXT NOT NULL DEFAULT 'mirror',
  sync_mode TEXT NOT NULL DEFAULT 'full',
  version_json TEXT NOT NULL DEFAULT '{}',
  watermark TEXT NOT NULL DEFAULT '',
  provenance TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'unknown',
  note TEXT NOT NULL DEFAULT '',
  last_synced_at TEXT NOT NULL DEFAULT '',
  last_checked_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(kind, entity_key, service, external_id)
);
CREATE INDEX IF NOT EXISTS copy_records_lookup ON copy_records(kind, service, external_id);
CREATE INDEX IF NOT EXISTS copy_records_entity ON copy_records(kind, entity_key);
CREATE INDEX IF NOT EXISTS copy_records_status ON copy_records(status);
`
)

// Version is the per-kind version token used to decide if a copy is
// current (issue #76). Fields are optional; connectors fill what they have.
type Version struct {
	ExternalID  string `json:"external_id,omitempty"`
	ModifiedAt  string `json:"modified_at,omitempty"`
	ETag        string `json:"etag,omitempty"`
	ContentHash string `json:"content_hash,omitempty"`
	SnapshotID  string `json:"snapshot_id,omitempty"`
	ItemCount   int    `json:"item_count,omitempty"`
	Name        string `json:"name,omitempty"`
}

// Record is one known copy of a logical entity at a location.
type Record struct {
	ID            string    `json:"id"`
	Kind          string    `json:"kind"`
	EntityKey     string    `json:"entity_key"`
	SourceService string    `json:"source_service,omitempty"`
	SourceID      string    `json:"source_id,omitempty"`
	Service       string    `json:"service"`
	ExternalID    string    `json:"external_id,omitempty"`
	Role          string    `json:"role"`
	SyncMode      string    `json:"sync_mode"`
	Version       Version   `json:"version"`
	Watermark     string    `json:"watermark,omitempty"`
	Provenance    string    `json:"provenance,omitempty"`
	Status        string    `json:"status"`
	Note          string    `json:"note,omitempty"`
	LastSyncedAt  string    `json:"last_synced_at,omitempty"`
	LastCheckedAt string    `json:"last_checked_at,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Filter narrows List/HandleLibraryCopies queries.
type Filter struct {
	Kind      string
	Service   string
	Status    string
	EntityKey string
}

// Ledger is the SQLite-backed durable copy registry. Empty path disables
// persistence (tests, opt-out); writes then become no-ops (fail-open).
type Ledger struct {
	db   *sql.DB
	path string
}

// Open opens or creates the ledger at path. Empty path returns a
// disabled ledger (nil database) so call sites can stay unconditional.
func Open(path string) (*Ledger, error) {
	if path == "" {
		return &Ledger{}, nil
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
	if _, err := db.Exec(copyLedgerSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("copy ledger schema: %w", err)
	}
	return &Ledger{db: db, path: path}, nil
}

// Close releases the database handle.
func (c *Ledger) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}

// Path returns the on-disk location ("" when disabled).
func (c *Ledger) Path() string {
	if c == nil {
		return ""
	}
	return c.path
}

// Enabled reports whether the ledger can persist rows.
func (c *Ledger) Enabled() bool {
	return c != nil && c.db != nil
}

// DB returns the underlying handle for co-located stores (the sync job
// queue, issue #227) that need one transaction across tables. Nil when
// disabled.
func (c *Ledger) DB() *sql.DB {
	if c == nil {
		return nil
	}
	return c.db
}

// syncModeForKind picks the default fidelity (issue #76). Kinds that
// declare OptimisticCopies (mail-like streams) use snapshot watermarks;
// discrete objects use full. The mode is type data (issue #254).
func SyncModeForKind(kind string) string {
	if k := types.BuiltinKindByID(strings.TrimSpace(kind)); k != nil && k.OptimisticCopies {
		return CopySyncModeOptimistic
	}
	return CopySyncModeFull
}

// normalizeRecord fills defaults so callers can pass partial input.
func NormalizeRecord(rec Record) Record {
	rec.Kind = strings.TrimSpace(rec.Kind)
	rec.EntityKey = strings.TrimSpace(rec.EntityKey)
	rec.Service = strings.TrimSpace(rec.Service)
	rec.ExternalID = strings.TrimSpace(rec.ExternalID)
	rec.SourceService = strings.TrimSpace(rec.SourceService)
	rec.SourceID = strings.TrimSpace(rec.SourceID)
	rec.Role = strings.TrimSpace(rec.Role)
	if rec.Role == "" {
		rec.Role = CopyRoleMirror
	}
	rec.SyncMode = strings.TrimSpace(rec.SyncMode)
	if rec.SyncMode == "" {
		rec.SyncMode = SyncModeForKind(rec.Kind)
	}
	rec.Provenance = strings.TrimSpace(rec.Provenance)
	if rec.Provenance == "" {
		rec.Provenance = CopyProvenanceObserved
	}
	rec.Status = strings.TrimSpace(rec.Status)
	if rec.Status == "" {
		rec.Status = CopyStatusUnknown
	}
	if rec.Version.ExternalID == "" {
		rec.Version.ExternalID = rec.ExternalID
	}
	return rec
}

// Upsert inserts or refreshes one copy row (matched on kind+entity+service+id).
// A successful write stamps LastSyncedAt / Status=in_sync when the caller
// just produced the copy.
func (c *Ledger) Upsert(rec Record) (Record, error) {
	rec = NormalizeRecord(rec)
	if rec.Kind == "" || rec.EntityKey == "" || rec.Service == "" {
		return rec, fmt.Errorf("copy record requires kind, entity_key, and service")
	}
	if !c.Enabled() {
		return rec, nil
	}
	now := time.Now().UTC()
	rec.UpdatedAt = now
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	if rec.LastSyncedAt == "" {
		rec.LastSyncedAt = now.Format(time.RFC3339)
	}
	if rec.Status == CopyStatusUnknown && rec.LastSyncedAt != "" {
		rec.Status = CopyStatusInSync
	}
	versionJSON, err := json.Marshal(rec.Version)
	if err != nil {
		return rec, err
	}
	_, err = c.db.Exec(`
INSERT INTO copy_records (
  kind, entity_key, source_service, source_id, service, external_id,
  role, sync_mode, version_json, watermark, provenance, status, note,
  last_synced_at, last_checked_at, created_at, updated_at
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(kind, entity_key, service, external_id) DO UPDATE SET
  source_service=excluded.source_service,
  source_id=excluded.source_id,
  role=excluded.role,
  sync_mode=excluded.sync_mode,
  version_json=excluded.version_json,
  watermark=excluded.watermark,
  provenance=excluded.provenance,
  status=excluded.status,
  note=excluded.note,
  last_synced_at=excluded.last_synced_at,
  last_checked_at=excluded.last_checked_at,
  updated_at=excluded.updated_at
`,
		rec.Kind, rec.EntityKey, rec.SourceService, rec.SourceID, rec.Service, rec.ExternalID,
		rec.Role, rec.SyncMode, string(versionJSON), rec.Watermark, rec.Provenance, rec.Status, rec.Note,
		rec.LastSyncedAt, rec.LastCheckedAt, rec.CreatedAt.UTC().Format(time.RFC3339Nano), rec.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return rec, err
	}
	return rec, nil
}

// MarkChecked records a freshness check outcome without claiming a new sync.
func (c *Ledger) MarkChecked(kind, entityKey, service, externalID, status string) error {
	if !c.Enabled() {
		return nil
	}
	kind = strings.TrimSpace(kind)
	entityKey = strings.TrimSpace(entityKey)
	service = strings.TrimSpace(service)
	externalID = strings.TrimSpace(externalID)
	status = strings.TrimSpace(status)
	if status == "" {
		status = CopyStatusUnknown
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := c.db.Exec(`
UPDATE copy_records
SET status=?, last_checked_at=?, updated_at=?
WHERE kind=? AND entity_key=? AND service=? AND external_id=?
`, status, now, now, kind, entityKey, service, externalID)
	return err
}

// Get returns one copy row by location identity.
func (c *Ledger) Get(kind, service, externalID string) (Record, bool) {
	if !c.Enabled() {
		return Record{}, false
	}
	row := c.db.QueryRow(`
SELECT id, kind, entity_key, source_service, source_id, service, external_id,
       role, sync_mode, version_json, watermark, provenance, status, note,
       last_synced_at, last_checked_at, created_at, updated_at
FROM copy_records
WHERE kind=? AND service=? AND external_id=?
`, kind, service, externalID)
	rec, err := scanRecord(row)
	if err != nil {
		return Record{}, false
	}
	return rec, true
}

// GetByEntity returns every known copy of one logical entity.
func (c *Ledger) GetByEntity(kind, entityKey string) []Record {
	if !c.Enabled() {
		return nil
	}
	rows, err := c.db.Query(`
SELECT id, kind, entity_key, source_service, source_id, service, external_id,
       role, sync_mode, version_json, watermark, provenance, status, note,
       last_synced_at, last_checked_at, created_at, updated_at
FROM copy_records
WHERE kind=? AND entity_key=?
ORDER BY service, external_id
`, kind, entityKey)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			continue
		}
		out = append(out, rec)
	}
	return out
}

// List returns copy rows matching the filter (empty filter = all).
func (c *Ledger) List(f Filter) []Record {
	if !c.Enabled() {
		return nil
	}
	q := `
SELECT id, kind, entity_key, source_service, source_id, service, external_id,
       role, sync_mode, version_json, watermark, provenance, status, note,
       last_synced_at, last_checked_at, created_at, updated_at
FROM copy_records WHERE 1=1`
	var args []any
	if f.Kind != "" {
		q += ` AND kind=?`
		args = append(args, f.Kind)
	}
	if f.Service != "" {
		q += ` AND service=?`
		args = append(args, f.Service)
	}
	if f.Status != "" {
		q += ` AND status=?`
		args = append(args, f.Status)
	}
	if f.EntityKey != "" {
		q += ` AND entity_key=?`
		args = append(args, f.EntityKey)
	}
	q += ` ORDER BY kind, entity_key, service, external_id`
	rows, err := c.db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			continue
		}
		out = append(out, rec)
	}
	return out
}

// CheckRow is one copy row plus its numeric ledger id. The id pages a
// freshness walk; Record.ID is a display string ("cpy_<hex>") and is not
// sortable, so the walk carries the real rowid.
type CheckRow struct {
	ID  int64
	Rec Record
}

// ListForCheck walks copy rows in stable id order for a freshness pass,
// resuming after afterID so one job step can be bounded. Empty kind walks
// every row. Only rows with a service id are returned — a copy with no
// location cannot be checked.
func (c *Ledger) ListForCheck(kind string, afterID int64, limit int) []CheckRow {
	if !c.Enabled() {
		return nil
	}
	if limit <= 0 {
		limit = 50
	}
	q := `
SELECT id, kind, entity_key, source_service, source_id, service, external_id,
       role, sync_mode, version_json, watermark, provenance, status, note,
       last_synced_at, last_checked_at, created_at, updated_at
FROM copy_records
WHERE id > ? AND service <> ''`
	args := []any{afterID}
	if kind != "" {
		q += ` AND kind=?`
		args = append(args, kind)
	}
	q += ` ORDER BY id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := c.db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []CheckRow
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			continue
		}
		out = append(out, CheckRow{ID: parseSyncRowID(rec.ID), Rec: rec})
	}
	return out
}

// parseSyncRowID recovers the numeric rowid from a display id ("cpy_<hex>").
// Zero when the string does not carry one.
func parseSyncRowID(id string) int64 {
	const prefix = "cpy_"
	s := strings.TrimSpace(id)
	if !strings.HasPrefix(s, prefix) {
		return 0
	}
	s = s[len(prefix):]
	if s == "" {
		return 0
	}
	var n int64
	for i := 0; i < len(s); i++ {
		c := s[i]
		var d int64
		switch {
		case c >= '0' && c <= '9':
			d = int64(c - '0')
		case c >= 'a' && c <= 'f':
			d = int64(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = int64(c-'A') + 10
		default:
			return 0
		}
		n = n*16 + d
	}
	return n
}

// Freshness is one kind's copy-status rollup, used by the admin sync
// surface so the operator can see where copies stand without paging the
// ledger. Unchecked counts rows still at `unknown` — the honest gap signal.
type Freshness struct {
	Kind        string `json:"kind"`
	InSync      int    `json:"in_sync"`
	Stale       int    `json:"stale"`
	Missing     int    `json:"missing"`
	Unknown     int    `json:"unknown"`
	Unchecked   int    `json:"unchecked"`
	Checked     int    `json:"checked"`
	Total       int    `json:"total"`
	LastChecked string `json:"last_checked_at,omitempty"`
}

// FreshnessRollup aggregates copy status per kind. Unchecked is a subset of
// unknown: rows that have never had a successful check stamped on them.
func (c *Ledger) FreshnessRollup() []Freshness {
	if !c.Enabled() {
		return nil
	}
	rows, err := c.db.Query(`
SELECT kind,
  SUM(CASE WHEN status='in_sync' THEN 1 ELSE 0 END),
  SUM(CASE WHEN status='stale' THEN 1 ELSE 0 END),
  SUM(CASE WHEN status='missing' THEN 1 ELSE 0 END),
  SUM(CASE WHEN status='unknown' THEN 1 ELSE 0 END),
  SUM(CASE WHEN last_checked_at='' THEN 1 ELSE 0 END),
  COUNT(*),
  MAX(NULLIF(last_checked_at, ''))
FROM copy_records
WHERE service <> ''
GROUP BY kind
ORDER BY kind
`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]Freshness, 0, 8)
	for rows.Next() {
		var f Freshness
		var inSync, stale, missing, unknown, unchecked, total int
		var lastChecked sql.NullString
		if err := rows.Scan(&f.Kind, &inSync, &stale, &missing, &unknown, &unchecked, &total, &lastChecked); err != nil {
			break
		}
		f.InSync, f.Stale, f.Missing, f.Unknown = inSync, stale, missing, unknown
		f.Unchecked, f.Total = unchecked, total
		f.Checked = total - unchecked
		if lastChecked.Valid {
			f.LastChecked = lastChecked.String
		}
		out = append(out, f)
	}
	return out
}

// Delete removes one copy row.
func (c *Ledger) Delete(kind, entityKey, service, externalID string) bool {
	if !c.Enabled() {
		return false
	}
	res, err := c.db.Exec(`
DELETE FROM copy_records
WHERE kind=? AND entity_key=? AND service=? AND external_id=?
`, kind, entityKey, service, externalID)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func scanRecord(row shared.RowScanner) (Record, error) {
	var (
		rec         Record
		id          int64
		versionJSON string
		createdAt   string
		updatedAt   string
	)
	err := row.Scan(
		&id, &rec.Kind, &rec.EntityKey, &rec.SourceService, &rec.SourceID, &rec.Service, &rec.ExternalID,
		&rec.Role, &rec.SyncMode, &versionJSON, &rec.Watermark, &rec.Provenance, &rec.Status, &rec.Note,
		&rec.LastSyncedAt, &rec.LastCheckedAt, &createdAt, &updatedAt,
	)
	if err != nil {
		return Record{}, err
	}
	rec.ID = fmt.Sprintf("cpy_%x", id)
	if versionJSON != "" {
		_ = json.Unmarshal([]byte(versionJSON), &rec.Version)
	}
	rec.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	}
	rec.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	if rec.UpdatedAt.IsZero() {
		rec.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	}
	return rec, nil
}

// toCopyInfo projects a ledger row onto a library source stamp.
func (rec Record) ToCopyInfo() *types.CopyInfo {
	asOf := rec.LastSyncedAt
	if asOf == "" {
		asOf = rec.Watermark
	}
	return &types.CopyInfo{
		Role:       rec.Role,
		SyncMode:   rec.SyncMode,
		SyncStatus: rec.Status,
		AsOf:       asOf,
		CheckedAt:  rec.LastCheckedAt,
		Provenance: rec.Provenance,
		Note:       rec.Note,
	}
}

// RecordTakeoutSnapshot stores/refreshes the optimistic snapshot row for a
// Google Takeout import (issue #76). Safe on a disabled ledger.
func (c *Ledger) RecordTakeoutSnapshot(path string, scanned, stored, skipped int, done bool) {
	if !c.Enabled() {
		return
	}
	abs := strings.TrimSpace(path)
	if abs != "" {
		if a, err := filepath.Abs(abs); err == nil {
			abs = a
		}
	}
	if abs == "" {
		return
	}
	watermark := "cursor"
	if done {
		watermark = "complete"
	}
	status := CopyStatusInSync
	note := fmt.Sprintf("takeout import scanned=%d stored=%d skipped=%d", scanned, stored, skipped)
	_, _ = c.Upsert(Record{
		Kind:       "messages",
		EntityKey:  "snapshot:google-takeout:" + abs,
		Service:    connectors.TakeoutServiceID,
		ExternalID: abs,
		Role:       CopyRoleImport,
		SyncMode:   CopySyncModeOptimistic,
		Provenance: CopyProvenanceTakeout,
		Status:     status,
		Watermark:  watermark,
		Note:       note,
		Version: Version{
			ExternalID: abs,
			ItemCount:  stored,
			Name:       filepath.Base(abs),
		},
	})
}
