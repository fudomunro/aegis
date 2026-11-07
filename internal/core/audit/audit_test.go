package audit

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestOpen_Disabled(t *testing.T) {
	a, err := Open("", 30)
	if err != nil {
		t.Fatal(err)
	}
	if a.Enabled() || a.Path() != "" {
		t.Fatalf("empty path should disable: enabled=%v path=%q", a.Enabled(), a.Path())
	}
	a.Write(Event{Agent: "x", Surface: "library", Target: "tracks", Decision: DecisionAllow})
	if n := a.Count(); n != 0 {
		t.Fatalf("disabled write should no-op, count=%d", n)
	}
	if rows, err := a.List(Filter{}); err != nil || rows != nil {
		t.Fatalf("disabled list = %v %v", rows, err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	var nilLog *Log
	if nilLog.Enabled() || nilLog.Path() != "" || nilLog.Retention() != 0 || nilLog.Count() != 0 {
		t.Fatal("nil audit log should be inert")
	}
	nilLog.Write(Event{})
	if err := nilLog.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := nilLog.List(Filter{}); err != nil {
		t.Fatal(err)
	}
}

func TestLog_RetentionPrune(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.db")
	a, err := Open(path, 1) // 1 day
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.pruneEvery = 0 // force prune on every write

	a.Write(Event{
		At:       time.Now().Add(-48 * time.Hour),
		Agent:    "old",
		Surface:  "library",
		Target:   "tracks",
		Decision: DecisionAllow,
	})
	a.Write(Event{
		At:       time.Now(),
		Agent:    "new",
		Surface:  "library",
		Target:   "tracks",
		Decision: DecisionAllow,
	})
	if n := a.Count(); n != 1 {
		t.Fatalf("count after prune = %d, want 1", n)
	}
	rows, err := a.List(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Agent != "new" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestLog_RowCap(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(filepath.Join(dir, "audit.db"), 30)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.maxRows = 3
	a.pruneEvery = 0

	base := time.Now().Add(-time.Hour)
	for i := 0; i < 6; i++ {
		a.Write(Event{
			At:       base.Add(time.Duration(i) * time.Minute),
			Agent:    "a",
			Surface:  "library",
			Target:   "tracks",
			Decision: DecisionAllow,
		})
	}
	if n := a.Count(); n > 3 {
		t.Fatalf("count = %d, want <= 3 after cap prune", n)
	}
}

func TestLog_MigratesLegacySchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	// Legacy shape: no client_ip column (pre issue #253).
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := `CREATE TABLE audit_events (
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
);`
	if _, err := db.Exec(legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO audit_events (at, agent, created_at) VALUES (?, 'old-row', ?)`,
		time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	a, err := Open(path, 30)
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	defer a.Close()
	a.Write(Event{Agent: "scope:family", Surface: "library", ClientIP: "203.0.113.9"})

	rows, err := a.List(Filter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	byAgent := map[string]Event{}
	for _, row := range rows {
		byAgent[row.Agent] = row
	}
	if got := byAgent["scope:family"].ClientIP; got != "203.0.113.9" {
		t.Fatalf("client_ip = %q", got)
	}
	if _, ok := byAgent["old-row"]; !ok {
		t.Fatalf("legacy row missing after migration: %+v", rows)
	}
}
