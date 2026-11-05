package connectors

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fudomunro/aegis/internal/types"
	_ "modernc.org/sqlite"
)

// Local import stores (issue #266): Signal, Quicken, and Timeline read local
// files/SQLite under the data directory instead of an upstream API. They are
// the store transport of the connector family — same provider interfaces,
// capabilities, and maturity surface as services; what differs is transport
// and lifecycle. This file owns the machinery they share: the SQLite
// lifecycle, the item cap, stable synthetic IDs, and batched import writes.

// LibraryItemCap is the max rows a local store listing (Signal, Quicken,
// Timeline) returns in one call. Distinct from LibraryPageCap — that is an
// upstream page-loop valve, not a product slice (issue #185).
const LibraryItemCap = 500

// importBatchRows is the number of rows committed per transaction during an
// import run, so a large export does not pay one fsync per row.
const importBatchRows = 200

// localStore is the shared SQLite lifecycle for local import stores: an
// empty path disables the store, otherwise the file is created under its
// parent directory with WAL journaling.
type localStore struct {
	db   *sql.DB
	path string
	mu   sync.RWMutex
}

// openLocalStore opens (or creates) a local import store at path. An empty
// path leaves s zero (disabled). initSQL installs the store's schema.
func (s *localStore) openLocalStore(path, label, initSQL string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create %s store dir: %w", label, err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		return err
	}
	if _, err := db.Exec(initSQL); err != nil {
		db.Close()
		return fmt.Errorf("init %s store: %w", label, err)
	}
	s.db, s.path = db, path
	return nil
}

func (s *localStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *localStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *localStore) Enabled() bool { return s != nil && s.db != nil }

// hasRow reports whether query (parameterized on source_id) finds a row.
func (s *localStore) hasRow(query, sourceID string) bool {
	if s == nil || s.db == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var n int
	err := s.db.QueryRow(query, sourceID).Scan(&n)
	return err == nil && n > 0
}

// ImportStore is the seam core's import flags and admin surface use over any
// local import store: open/close, path reporting, and one-shot import runs.
type ImportStore interface {
	Path() string
	Enabled() bool
	Close() error
}

// ImportOutcome is the CLI/admin view of one finished import run. Each
// store's result DTO implements it so core can log and gate exit codes
// without knowing the per-store counter shapes.
type ImportOutcome interface {
	// LogAttrs returns extra slog keyvals beyond path (store-specific).
	LogAttrs() []any
	// Failed reports a run that recorded errors but stored nothing.
	Failed() bool
}

// LocalStoreService is the service card a local import store injects into
// config so /v1/library fan-in sees it as a source (issue #266). Declared
// beside the store so registration, naming, and grants read one table.
type LocalStoreService struct {
	ID          string
	Name        string
	Action      string
	Description string
	GrantLimits types.Permissions
}

// ImportStats counts one import run. Store-specific result DTOs wrap these.
type ImportStats struct {
	Scanned int `json:"scanned"`
	Stored  int `json:"stored"`
	Skipped int `json:"skipped"`
	Errors  int `json:"errors"`
}

// upsertRows folds parsed rows into a local import store: rows the store
// already has (by sourceID, checked with hasQuery) are skipped; the rest are
// upserted in transactions of importBatchRows rows (issue #266). Counters are
// applied per committed batch so a failed transaction reports its rows as
// errors, never as stored.
func upsertRows[T any](s *localStore, stats *ImportStats, rows []T, sourceID func(T) string, hasQuery string, upsert func(*sql.Tx, T) error) {
	for start := 0; start < len(rows); start += importBatchRows {
		end := min(start+importBatchRows, len(rows))
		batch := rows[start:end]
		var stored, skipped, errs int
		s.mu.Lock()
		err := func() error {
			tx, err := s.db.Begin()
			if err != nil {
				return err
			}
			for _, row := range batch {
				if id := sourceID(row); id != "" && rowExists(tx, hasQuery, id) {
					skipped++
					continue
				}
				if err := upsert(tx, row); err != nil {
					errs++
					continue
				}
				stored++
			}
			return tx.Commit()
		}()
		s.mu.Unlock()
		if err != nil {
			errs = len(batch)
			stored, skipped = 0, 0
		}
		stats.Scanned += len(batch)
		stats.Stored += stored
		stats.Skipped += skipped
		stats.Errors += errs
	}
}

func rowExists(tx *sql.Tx, query, sourceID string) bool {
	var n int
	if err := tx.QueryRow(query, sourceID).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// shortHash is a tiny non-crypto fingerprint for stable synthetic IDs.
func shortHash(s string) string {
	var h uint64 = 1469598103934665603
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return fmt.Sprintf("%016x", h)
}
