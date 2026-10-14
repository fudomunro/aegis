package core

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Durable access audit trail (issue #105). Append-only SQLite store of who
// accessed which service/data type and whether the call was allowed or
// denied. Redacted like AccessEvent: no tokens, bodies, query strings, or
// full paths. Fail-open — a full disk or closed log never breaks a request.

const (
	// Decision values recorded on audit rows and access events.
	DecisionAllow = "allow"
	DecisionDeny  = "deny"

	// defaultAuditRetentionDays is the documented retention window for
	// audit rows. Older rows are pruned on open and periodically on write.
	defaultAuditRetentionDays = 30

	// defaultAuditMaxRows is a hard cap so a noisy agent cannot fill the
	// disk. Oldest rows are dropped first once the cap is hit.
	defaultAuditMaxRows = 500000

	// auditPruneInterval bounds how often retention pruning runs on the
	// write path (startup prune always runs).
	auditPruneInterval = time.Hour
)

const auditSchema = `
CREATE TABLE IF NOT EXISTS audit_events (
  id INTEGER PRIMARY KEY,
  at TEXT NOT NULL,
  agent TEXT NOT NULL DEFAULT '',
  surface TEXT NOT NULL DEFAULT '',
  target TEXT NOT NULL DEFAULT '',
  data_type TEXT NOT NULL DEFAULT '',
  decision TEXT NOT NULL DEFAULT 'allow',
  reason TEXT NOT NULL DEFAULT '',
  cache TEXT NOT NULL DEFAULT '',
  method TEXT NOT NULL DEFAULT '',
  status INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS audit_events_at ON audit_events(at);
CREATE INDEX IF NOT EXISTS audit_events_agent ON audit_events(agent, at);
CREATE INDEX IF NOT EXISTS audit_events_decision ON audit_events(decision, at);
CREATE INDEX IF NOT EXISTS audit_events_target ON audit_events(target, at);
`

// AccessEventToAudit projects a redacted access event onto the durable
// audit row (issue #105).
func AccessEventToAudit(ev AccessEvent) AuditEvent {
	return AuditEvent{
		At:       ev.At,
		Agent:    ev.Agent,
		Surface:  ev.Surface,
		Target:   ev.Target,
		DataType: ev.DataType,
		Decision: ev.Decision,
		Reason:   ev.Reason,
		Cache:    ev.Cache,
		Method:   ev.Method,
		Status:   ev.Status,
	}
}

// AuditEvent is one redacted access record. Mirrors AccessEvent plus
// HTTP method/status for denials. Never holds tokens or bodies.
type AuditEvent struct {
	At       time.Time
	Agent    string
	Surface  string
	Target   string
	DataType string
	Decision string
	Reason   string
	Cache    string
	Method   string
	Status   int
}

// AuditLog is the SQLite-backed durable audit sink. Empty path disables
// persistence (tests, opt-out); writes then become no-ops (fail-open).
type AuditLog struct {
	db         *sql.DB
	path       string
	retention  time.Duration
	maxRows    int
	mu         sync.Mutex
	lastPrune  time.Time
	pruneEvery time.Duration
}

// OpenAuditLog opens or creates the audit database at path. Empty path
// returns a disabled log so call sites can stay unconditional.
func OpenAuditLog(path string, retentionDays int) (*AuditLog, error) {
	if path == "" {
		return &AuditLog{}, nil
	}
	if retentionDays <= 0 {
		retentionDays = defaultAuditRetentionDays
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil && !os.IsExist(err) {
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
	if _, err := db.Exec(auditSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("audit schema: %w", err)
	}
	a := &AuditLog{
		db:         db,
		path:       path,
		retention:  time.Duration(retentionDays) * 24 * time.Hour,
		maxRows:    defaultAuditMaxRows,
		pruneEvery: auditPruneInterval,
	}
	// Startup prune always runs so retention is applied even when the
	// process was down past the window.
	if err := a.prune(time.Now()); err != nil {
		// Fail-open: a prune error must not prevent the server from starting.
		_ = err
	}
	a.lastPrune = time.Now()
	return a, nil
}

// Close releases the database handle.
func (a *AuditLog) Close() error {
	if a == nil || a.db == nil {
		return nil
	}
	return a.db.Close()
}

// Path returns the on-disk location ("" when disabled).
func (a *AuditLog) Path() string {
	if a == nil {
		return ""
	}
	return a.path
}

// Enabled reports whether the log can persist rows.
func (a *AuditLog) Enabled() bool {
	return a != nil && a.db != nil
}

// Retention returns the configured retention window (0 when disabled).
func (a *AuditLog) Retention() time.Duration {
	if a == nil {
		return 0
	}
	return a.retention
}

// Write appends one audit event. Safe for concurrent use. Fail-open: any
// database error is logged and swallowed so agent traffic is never blocked
// by the audit sink.
func (a *AuditLog) Write(ev AuditEvent) {
	if a == nil || a.db == nil {
		return
	}
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	if ev.Decision == "" {
		ev.Decision = DecisionAllow
	}
	if ev.Agent == "" {
		ev.Agent = "api-key"
	}
	// RFC3339Nano keeps ordering stable and is what the rest of the
	// SQLite state stores use for timestamps.
	at := ev.At.UTC().Format(time.RFC3339Nano)
	created := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := a.db.Exec(`
INSERT INTO audit_events (
  at, agent, surface, target, data_type, decision, reason, cache, method, status, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		at, ev.Agent, ev.Surface, ev.Target, ev.DataType, ev.Decision,
		ev.Reason, ev.Cache, ev.Method, ev.Status, created,
	)
	if err != nil {
		// Fail-open: never surface audit write errors to the caller.
		fmt.Fprintf(os.Stderr, "aegis: audit write failed: %v\n", err)
		return
	}
	a.maybePrune()
}

// maybePrune applies retention when the prune interval has elapsed. Uses
// wall-clock time for the schedule so backdated events cannot suppress
// pruning.
func (a *AuditLog) maybePrune() {
	now := time.Now()
	a.mu.Lock()
	due := a.lastPrune.IsZero() || now.Sub(a.lastPrune) >= a.pruneEvery
	if due {
		a.lastPrune = now
	}
	a.mu.Unlock()
	if !due {
		return
	}
	_ = a.prune(now)
}

// prune deletes rows past the retention window and enforces the row cap.
func (a *AuditLog) prune(now time.Time) error {
	if a == nil || a.db == nil {
		return nil
	}
	cutoff := now.Add(-a.retention).UTC().Format(time.RFC3339Nano)
	if _, err := a.db.Exec(`DELETE FROM audit_events WHERE at < ?`, cutoff); err != nil {
		return err
	}
	// Hard cap: drop oldest rows when over maxRows.
	if a.maxRows > 0 {
		var n int
		if err := a.db.QueryRow(`SELECT COUNT(*) FROM audit_events`).Scan(&n); err == nil && n > a.maxRows {
			excess := n - a.maxRows
			_, _ = a.db.Exec(`
DELETE FROM audit_events WHERE id IN (
  SELECT id FROM audit_events ORDER BY at ASC, id ASC LIMIT ?
)`, excess)
		}
	}
	return nil
}

// Count returns the number of retained audit rows (0 when disabled).
func (a *AuditLog) Count() int {
	if a == nil || a.db == nil {
		return 0
	}
	var n int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM audit_events`).Scan(&n); err != nil {
		return 0
	}
	return n
}

// AuditFilter narrows List queries.
type AuditFilter struct {
	Agent    string
	Decision string
	Target   string
	Limit    int
}

// List returns recent audit rows newest-first. Used by tests and admin
// tooling; never returns token material.
func (a *AuditLog) List(f AuditFilter) ([]AuditEvent, error) {
	if a == nil || a.db == nil {
		return nil, nil
	}
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	query := `SELECT at, agent, surface, target, data_type, decision, reason, cache, method, status
FROM audit_events WHERE 1=1`
	args := make([]any, 0, 4)
	if f.Agent != "" {
		query += ` AND agent = ?`
		args = append(args, f.Agent)
	}
	if f.Decision != "" {
		query += ` AND decision = ?`
		args = append(args, f.Decision)
	}
	if f.Target != "" {
		query += ` AND target = ?`
		args = append(args, f.Target)
	}
	query += ` ORDER BY at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := a.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEvent
	for rows.Next() {
		var (
			ev AuditEvent
			at string
		)
		if err := rows.Scan(&at, &ev.Agent, &ev.Surface, &ev.Target, &ev.DataType,
			&ev.Decision, &ev.Reason, &ev.Cache, &ev.Method, &ev.Status); err != nil {
			return nil, err
		}
		if t, err := time.Parse(time.RFC3339Nano, at); err == nil {
			ev.At = t
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}
