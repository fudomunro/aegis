package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Copy ledger (issue #76): durable memory of where copies of library items
// exist and which version of the data sits there. Distinct from sources[]
// (live inventory at read time) and from entity links (identity only).
//
// Sync fidelity is per kind — see copySyncModeForKind:
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

// CopyVersion is the per-kind version token used to decide if a copy is
// current (issue #76). Fields are optional; connectors fill what they have.
type CopyVersion struct {
	ExternalID  string `json:"external_id,omitempty"`
	ModifiedAt  string `json:"modified_at,omitempty"`
	ETag        string `json:"etag,omitempty"`
	ContentHash string `json:"content_hash,omitempty"`
	SnapshotID  string `json:"snapshot_id,omitempty"`
	ItemCount   int    `json:"item_count,omitempty"`
	Name        string `json:"name,omitempty"`
}

// CopyRecord is one known copy of a logical entity at a location.
type CopyRecord struct {
	ID            string      `json:"id"`
	Kind          string      `json:"kind"`
	EntityKey     string      `json:"entity_key"`
	SourceService string      `json:"source_service,omitempty"`
	SourceID      string      `json:"source_id,omitempty"`
	Service       string      `json:"service"`
	ExternalID    string      `json:"external_id,omitempty"`
	Role          string      `json:"role"`
	SyncMode      string      `json:"sync_mode"`
	Version       CopyVersion `json:"version"`
	Watermark     string      `json:"watermark,omitempty"`
	Provenance    string      `json:"provenance,omitempty"`
	Status        string      `json:"status"`
	Note          string      `json:"note,omitempty"`
	LastSyncedAt  string      `json:"last_synced_at,omitempty"`
	LastCheckedAt string      `json:"last_checked_at,omitempty"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
}

// CopyInfo is the library-facing projection of a ledger row, stamped onto
// sources[] only when a matching copy exists (issue #76 / #90).
type CopyInfo struct {
	Role       string `json:"role,omitempty"`
	SyncMode   string `json:"sync_mode,omitempty"`
	SyncStatus string `json:"sync_status,omitempty"`
	AsOf       string `json:"as_of,omitempty"`
	CheckedAt  string `json:"checked_at,omitempty"`
	Provenance string `json:"provenance,omitempty"`
	Note       string `json:"note,omitempty"`
}

// CopyFilter narrows List/HandleLibraryCopies queries.
type CopyFilter struct {
	Kind      string
	Service   string
	Status    string
	EntityKey string
}

// CopyLedger is the SQLite-backed durable copy registry. Empty path disables
// persistence (tests, opt-out); writes then become no-ops (fail-open).
type CopyLedger struct {
	db   *sql.DB
	path string
}

// OpenCopyLedger opens or creates the ledger at path. Empty path returns a
// disabled ledger (nil database) so call sites can stay unconditional.
func OpenCopyLedger(path string) (*CopyLedger, error) {
	if path == "" {
		return &CopyLedger{}, nil
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
	return &CopyLedger{db: db, path: path}, nil
}

// Close releases the database handle.
func (c *CopyLedger) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}

// Path returns the on-disk location ("" when disabled).
func (c *CopyLedger) Path() string {
	if c == nil {
		return ""
	}
	return c.path
}

// Enabled reports whether the ledger can persist rows.
func (c *CopyLedger) Enabled() bool {
	return c != nil && c.db != nil
}

// copySyncModeForKind picks the default fidelity (issue #76). Mail-like
// kinds use optimistic snapshot watermarks; discrete objects use full.
func copySyncModeForKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "messages", "mail", "message":
		return CopySyncModeOptimistic
	default:
		return CopySyncModeFull
	}
}

// normalizeCopyRecord fills defaults so callers can pass partial input.
func normalizeCopyRecord(rec CopyRecord) CopyRecord {
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
		rec.SyncMode = copySyncModeForKind(rec.Kind)
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
func (c *CopyLedger) Upsert(rec CopyRecord) (CopyRecord, error) {
	rec = normalizeCopyRecord(rec)
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
func (c *CopyLedger) MarkChecked(kind, entityKey, service, externalID, status string) error {
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
func (c *CopyLedger) Get(kind, service, externalID string) (CopyRecord, bool) {
	if !c.Enabled() {
		return CopyRecord{}, false
	}
	row := c.db.QueryRow(`
SELECT id, kind, entity_key, source_service, source_id, service, external_id,
       role, sync_mode, version_json, watermark, provenance, status, note,
       last_synced_at, last_checked_at, created_at, updated_at
FROM copy_records
WHERE kind=? AND service=? AND external_id=?
`, kind, service, externalID)
	rec, err := scanCopyRecord(row)
	if err != nil {
		return CopyRecord{}, false
	}
	return rec, true
}

// GetByEntity returns every known copy of one logical entity.
func (c *CopyLedger) GetByEntity(kind, entityKey string) []CopyRecord {
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
	var out []CopyRecord
	for rows.Next() {
		rec, err := scanCopyRecord(rows)
		if err != nil {
			continue
		}
		out = append(out, rec)
	}
	return out
}

// List returns copy rows matching the filter (empty filter = all).
func (c *CopyLedger) List(f CopyFilter) []CopyRecord {
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
	var out []CopyRecord
	for rows.Next() {
		rec, err := scanCopyRecord(rows)
		if err != nil {
			continue
		}
		out = append(out, rec)
	}
	return out
}

// Delete removes one copy row.
func (c *CopyLedger) Delete(kind, entityKey, service, externalID string) bool {
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

type copyRowScanner interface {
	Scan(dest ...any) error
}

func scanCopyRecord(row copyRowScanner) (CopyRecord, error) {
	var (
		rec         CopyRecord
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
		return CopyRecord{}, err
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
func (rec CopyRecord) toCopyInfo() *CopyInfo {
	asOf := rec.LastSyncedAt
	if asOf == "" {
		asOf = rec.Watermark
	}
	return &CopyInfo{
		Role:       rec.Role,
		SyncMode:   rec.SyncMode,
		SyncStatus: rec.Status,
		AsOf:       asOf,
		CheckedAt:  rec.LastCheckedAt,
		Provenance: rec.Provenance,
		Note:       rec.Note,
	}
}

// copyInfoFor looks up a stamp for one service-local location. Returns nil
// when disabled or unknown — library responses stay unchanged (fail-open).
func (s *Server) copyInfoFor(kind, service, externalID string) *CopyInfo {
	if s == nil || s.copies == nil || service == "" || externalID == "" {
		return nil
	}
	rec, ok := s.copies.Get(kind, service, externalID)
	if !ok {
		return nil
	}
	return rec.toCopyInfo()
}

// recordMirrorCopy persists a successful playlist/liked-tracks mirror
// (issue #76). Errors are logged by the caller and never fail the mirror.
func (s *Server) recordMirrorCopy(kind, sourceService, sourceID, targetService, targetID, name string, itemCount int, snapshotID string) {
	if s == nil || s.copies == nil {
		return
	}
	entity := mirrorEntityKey(kind, sourceService, sourceID)
	_, err := s.copies.Upsert(CopyRecord{
		Kind:          kind,
		EntityKey:     entity,
		SourceService: sourceService,
		SourceID:      sourceID,
		Service:       targetService,
		ExternalID:    targetID,
		Role:          CopyRoleMirror,
		SyncMode:      copySyncModeForKind(kind),
		Provenance:    CopyProvenanceMirror,
		Status:        CopyStatusInSync,
		Version: CopyVersion{
			ExternalID: targetID,
			SnapshotID: snapshotID,
			ItemCount:  itemCount,
			Name:       name,
		},
	})
	if err != nil {
		// Fail-open: the copy already exists upstream; ledger is best-effort.
		return
	}
}

// mirrorEntityKey builds a stable logical id for a mirrored source list.
func mirrorEntityKey(kind, sourceService, sourceID string) string {
	kind = strings.TrimSpace(kind)
	sourceService = strings.TrimSpace(sourceService)
	sourceID = strings.TrimSpace(sourceID)
	if kind == MirrorKindTracks || sourceID == "" {
		return "liked:" + sourceService
	}
	return kind + ":" + sourceService + ":" + sourceID
}

// recordTakeoutSnapshot stores/refreshes the optimistic snapshot row for a
// Google Takeout import into the mail archive (issue #76).
func (s *Server) recordTakeoutSnapshot(path string, scanned, stored, skipped int, done bool) {
	if s == nil || s.copies == nil {
		return
	}
	s.copies.RecordTakeoutSnapshot(path, scanned, stored, skipped, done)
}

// RecordTakeoutSnapshot stores/refreshes the optimistic snapshot row for a
// Google Takeout import (issue #76). Safe on a disabled ledger.
func (c *CopyLedger) RecordTakeoutSnapshot(path string, scanned, stored, skipped int, done bool) {
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
	_, _ = c.Upsert(CopyRecord{
		Kind:       "messages",
		EntityKey:  "snapshot:google-takeout:" + abs,
		Service:    takeoutServiceID,
		ExternalID: abs,
		Role:       CopyRoleImport,
		SyncMode:   CopySyncModeOptimistic,
		Provenance: CopyProvenanceTakeout,
		Status:     status,
		Watermark:  watermark,
		Note:       note,
		Version: CopyVersion{
			ExternalID: abs,
			ItemCount:  stored,
			Name:       filepath.Base(abs),
		},
	})
}
