package core

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

// SearchIndex is the local full-text index over library kinds (issue #178).
// One SQLite file in the data directory, dataset-partitioned like the mail
// archive. Rows hold neutral-item metadata (JSON payloads) so search hits
// can hydrate without a live provider fetch — never media bytes.
//
// Kind participation is declared by types.SearchSpec on the library kind;
// this store is the engine behind that declaration. A different engine can
// replace it without changing the type surface.
type SearchIndex struct {
	db   *sql.DB
	path string
}

// SearchIndexQuery is one full-text lookup over a kind.
type SearchIndexQuery struct {
	Kind    string
	Dataset string
	Q       string
	Limit   int
	Offset  int
}

// SearchIndexResult is the page of matching documents plus the full hit count.
type SearchIndexResult struct {
	Docs  []types.SearchDocument
	Total int
}

// OpenSearchIndex opens (creating if needed) the search index at path.
// An empty path returns (nil, nil) so callers can treat search as disabled.
func OpenSearchIndex(path string) (*SearchIndex, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
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
	// Same single-writer posture as the mail archive: one connection avoids
	// SQLITE_BUSY between agent reads and background indexing.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		db.Close()
		return nil, err
	}
	x := &SearchIndex{db: db, path: path}
	if err := x.init(); err != nil {
		db.Close()
		return nil, err
	}
	return x, nil
}

func (x *SearchIndex) init() error {
	schema := `
CREATE TABLE IF NOT EXISTS search_docs (
  id INTEGER PRIMARY KEY,
  kind TEXT NOT NULL,
  dataset TEXT NOT NULL DEFAULT '',
  item_id TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '',
  body TEXT NOT NULL DEFAULT '',
  services_json TEXT NOT NULL DEFAULT '[]',
  payload_json BLOB NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT '',
  UNIQUE(kind, dataset, item_id)
);
CREATE INDEX IF NOT EXISTS search_docs_kind_dataset ON search_docs(kind, dataset);
CREATE VIRTUAL TABLE IF NOT EXISTS search_fts USING fts5(title, body);
`
	if _, err := x.db.Exec(schema); err != nil {
		return fmt.Errorf("search index schema: %w", err)
	}
	return nil
}

// Close releases the database handle.
func (x *SearchIndex) Close() error {
	if x == nil || x.db == nil {
		return nil
	}
	return x.db.Close()
}

// Path returns the on-disk location ("" when disabled).
func (x *SearchIndex) Path() string {
	if x == nil {
		return ""
	}
	return x.path
}

// Enabled reports whether the index is usable.
func (x *SearchIndex) Enabled() bool {
	return x != nil && x.db != nil
}

// Upsert stores or replaces documents and refreshes their FTS rows.
func (x *SearchIndex) Upsert(kind, dataset string, docs []types.SearchDocument) error {
	if !x.Enabled() {
		return fmt.Errorf("search index disabled")
	}
	if kind == "" {
		return fmt.Errorf("search index upsert requires kind")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := x.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, doc := range docs {
		if doc.ItemID == "" {
			continue
		}
		servicesJSON, err := json.Marshal(doc.Services)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`
INSERT INTO search_docs (kind, dataset, item_id, title, body, services_json, payload_json, updated_at)
VALUES (?,?,?,?,?,?,?,?)
ON CONFLICT(kind, dataset, item_id) DO UPDATE SET
  title=excluded.title,
  body=excluded.body,
  services_json=excluded.services_json,
  payload_json=excluded.payload_json,
  updated_at=excluded.updated_at
`, kind, dataset, doc.ItemID, doc.Title, doc.Body, string(servicesJSON), doc.Payload, now); err != nil {
			return err
		}
		// ON CONFLICT DO UPDATE keeps the original rowid; re-read it for FTS.
		var id int64
		if err := tx.QueryRow(
			`SELECT id FROM search_docs WHERE kind=? AND dataset=? AND item_id=?`,
			kind, dataset, doc.ItemID,
		).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM search_fts WHERE rowid=?`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT INTO search_fts(rowid, title, body) VALUES (?,?,?)`,
			id, doc.Title, doc.Body,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Count reports how many documents are indexed for (kind, dataset). Used to
// distinguish "no hits" from "never materialized" on a cold search.
func (x *SearchIndex) Count(kind, dataset string) (int, error) {
	if !x.Enabled() {
		return 0, fmt.Errorf("search index disabled")
	}
	var n int
	err := x.db.QueryRow(
		`SELECT COUNT(*) FROM search_docs WHERE kind=? AND dataset=?`,
		kind, dataset,
	).Scan(&n)
	return n, err
}

// Search runs a full-text match over one kind. Limit defaults to 25 (cap
// types.MaxPageLimit). dataset="" searches every partition; a non-empty
// dataset restricts to that partition (docs/data-scopes.md).
func (x *SearchIndex) Search(q SearchIndexQuery) (SearchIndexResult, error) {
	if !x.Enabled() {
		return SearchIndexResult{}, fmt.Errorf("search index disabled")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 25
	}
	if limit > types.MaxPageLimit {
		limit = types.MaxPageLimit
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}
	match, _ := ftsMatchExpr(q.Q)
	if match == "" {
		match = `""`
	}
	where := []string{"d.kind = ?"}
	args := []any{match, q.Kind}
	if ds := strings.TrimSpace(q.Dataset); ds != "" {
		where = append(where, "d.dataset = ?")
		args = append(args, ds)
	}
	base := `FROM search_fts f JOIN search_docs d ON d.id = f.rowid WHERE search_fts MATCH ? AND ` +
		strings.Join(where, " AND ")

	var total int
	if err := x.db.QueryRow(`SELECT COUNT(*) `+base, args...).Scan(&total); err != nil {
		return SearchIndexResult{}, err
	}

	// rank orders FTS5 hits by relevance; id tiebreak keeps pages stable.
	querySQL := `SELECT d.item_id, d.title, d.body, d.services_json, d.payload_json ` + base +
		` ORDER BY rank, d.id LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := x.db.Query(querySQL, args...)
	if err != nil {
		return SearchIndexResult{}, err
	}
	defer rows.Close()
	docs := make([]types.SearchDocument, 0, limit)
	for rows.Next() {
		var (
			doc         types.SearchDocument
			servicesRaw []byte
			payload     []byte
		)
		if err := rows.Scan(&doc.ItemID, &doc.Title, &doc.Body, &servicesRaw, &payload); err != nil {
			return SearchIndexResult{}, err
		}
		doc.Payload = payload
		if len(servicesRaw) > 0 {
			_ = json.Unmarshal(servicesRaw, &doc.Services)
		}
		docs = append(docs, doc)
	}
	if err := rows.Err(); err != nil {
		return SearchIndexResult{}, err
	}
	return SearchIndexResult{Docs: docs, Total: total}, nil
}

// ftsMatchExpr turns user text into a safe FTS5 MATCH expression: AND of
// quoted tokens, with the final token prefix-matched so partial words hit
// ("rain" finds "Raining"). Operator characters are stripped so agents
// cannot inject NEAR/OR/column filters.
func ftsMatchExpr(q string) (string, []any) {
	q = strings.TrimSpace(q)
	if q == "" {
		return "", nil
	}
	var tokens []string
	for _, t := range strings.Fields(q) {
		cleaned := strings.Map(func(r rune) rune {
			if r == '"' {
				return -1
			}
			if r == '*' || r == '^' || r == ':' || r == '(' || r == ')' || r == '{' || r == '}' {
				return ' '
			}
			return r
		}, t)
		tokens = append(tokens, strings.Fields(cleaned)...)
	}
	if len(tokens) == 0 {
		return "", nil
	}
	parts := make([]string, 0, len(tokens))
	for i, sub := range tokens {
		if i == len(tokens)-1 {
			parts = append(parts, `"`+sub+`"*`)
		} else {
			parts = append(parts, `"`+sub+`"`)
		}
	}
	return strings.Join(parts, " AND "), nil
}
