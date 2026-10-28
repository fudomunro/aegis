package core

import (
	"github.com/fudomunro/aegis/internal/connectors"

	"bytes"
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/types"
	_ "modernc.org/sqlite"
)

// MailArchive is Aegis's local queryable mail mirror.
//
// Design (issue: local compressed mailbox for agents):
//   - Gmail (or another named provider) stays canonical for the home copy and
//     any writes (issue #16). This store is a local archive/index so agents can
//     search history without paging the provider on every turn.
//   - Metadata lives in SQLite columns; body text is FTS5-indexed; the full
//     provider payload is gzip-compressed for cheap retention.
//   - Path is a single file (default <data-dir>/mail-archive.db). Empty path disables the
//     archive and the /v1/mail/* routes return a clear error.
//
// Optimized for Aegis/agent queries (search, filter, fetch by id) rather
// than for offline mail clients.
type MailArchive struct {
	db   *sql.DB
	path string
}

// MailArchiveRecord is one archived message. Neutral Message fields are
// denormalized for SQL filters; PayloadGzip holds the provider full payload.
type MailArchiveRecord struct {
	Service     string
	ServiceName string
	ID          string
	ThreadID    string
	MessageID   string
	Mailbox     string
	Labels      []string
	Subject     string
	Snippet     string
	FromName    string
	FromEmail   string
	To          []types.Contact
	CC          []types.Contact
	SentAt      string
	ReceivedAt  string
	Direction   string
	Read        bool
	BodyText    string
	PayloadGzip []byte
	// Dataset is the data-scope partition id (docs/data-scopes.md). Empty is
	// the owner/default partition.
	Dataset string
}

// Message converts the row into the neutral library shape (body included when
// BodyText is non-empty).
func (r MailArchiveRecord) Message() types.Message {
	var from *types.Contact
	if r.FromName != "" || r.FromEmail != "" {
		c := types.NewContact(r.Service, r.ServiceName, "", r.FromName, []string{r.FromEmail}, nil)
		if r.FromName == "" && r.FromEmail == "" {
			c = types.Contact{}
		}
		if c.Name != "" || len(c.Emails) > 0 {
			from = &c
		}
	}
	// Takeout is a read-only sync location, never the mailbox home (issue #16/#139).
	src := types.MessageSource{
		Service:     r.Service,
		ServiceName: r.ServiceName,
		ID:          r.ID,
		ThreadID:    r.ThreadID,
		Mailbox:     r.Mailbox,
		Canonical:   r.Service != takeoutServiceID,
	}
	if r.MessageID != "" {
		src.Extra = map[string]any{"message_id": r.MessageID}
	}
	if r.Snippet != "" {
		if src.Extra == nil {
			src.Extra = map[string]any{}
		}
		src.Extra["snippet"] = r.Snippet
	}
	return types.Message{
		Channel:    types.MessageChannelEmail,
		Subject:    r.Subject,
		Snippet:    r.Snippet,
		Body:       r.BodyText,
		From:       from,
		To:         r.To,
		CC:         r.CC,
		SentAt:     r.SentAt,
		ReceivedAt: r.ReceivedAt,
		Direction:  r.Direction,
		Read:       r.Read,
		ThreadID:   r.ThreadID,
		EntityID:   r.MessageID,
		Labels:     r.Labels,
		Sources:    []types.MessageSource{src},
	}
}

// MailSearchQuery filters an archive search. Q is an FTS5 match expression
// (or plain words — the store tokenizes safely). Empty Q lists by filters.
type MailSearchQuery struct {
	Q        string
	From     string
	To       string
	After    string // RFC3339 or date prefix, inclusive lower bound on sent_at
	Before   string // RFC3339 or date prefix, exclusive upper bound on sent_at
	Labels   []string
	Limit    int
	Offset   int
	WithBody bool
	// Sort is "desc" (default, newest first) or "asc" (oldest first).
	Sort string
	// Dataset, when non-empty, restricts results to one data-scope
	// partition (docs/data-scopes.md). Empty sees every row.
	Dataset string
}

// MailSearchResponse is the agent-facing search/list result.
// Paging matches the library contract (issue #185): limit + opaque
// cursor/next_cursor. offset remains accepted for admin UI callers.
type MailSearchResponse struct {
	Messages []types.Message `json:"messages"`
	Total    int             `json:"total"`
	Limit    int             `json:"limit"`
	Offset   int             `json:"offset"`
	// NextCursor continues the walk; omitted at the end (issue #185).
	NextCursor string `json:"next_cursor,omitempty"`
	Query      string `json:"query,omitempty"`
}

// MailArchiveStatus reports store health for agents and admin.
type MailArchiveStatus struct {
	Enabled       bool              `json:"enabled"`
	Path          string            `json:"path,omitempty"`
	MessageCount  int               `json:"message_count"`
	ByService     map[string]int    `json:"by_service,omitempty"`
	LastSyncAt    string            `json:"last_sync_at,omitempty"`
	LastSyncError string            `json:"last_sync_error,omitempty"`
	SyncPageToken map[string]string `json:"sync_page_token,omitempty"`
	SyncDone      map[string]bool   `json:"sync_done,omitempty"`
	// TakeoutPath is the configured read-only Google Takeout export location.
	TakeoutPath string `json:"takeout_path,omitempty"`
}

// MailSyncCursor is the durable per-service ingest cursor.
type MailSyncCursor struct {
	PageToken  string `json:"page_token,omitempty"`
	Done       bool   `json:"done,omitempty"`
	LastSyncAt string `json:"last_sync_at,omitempty"`
	Error      string `json:"error,omitempty"`
}

// OpenMailArchive opens (creating if needed) the SQLite mail archive at path.
// An empty path returns (nil, nil) so callers can treat the archive as disabled.
func OpenMailArchive(path string) (*MailArchive, error) {
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
	// Single-process writer; keep the connection simple and avoid SQLITE_BUSY
	// surprises under concurrent agent reads + background sync.
	db.SetMaxOpenConns(1)
	// WAL lets the long-running Takeout importer share the file with a live
	// server instead of blocking on a full schema lock.
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		db.Close()
		return nil, err
	}
	a := &MailArchive{db: db, path: path}
	if err := a.init(); err != nil {
		db.Close()
		return nil, err
	}
	return a, nil
}

// Close releases the database handle.
func (a *MailArchive) Close() error {
	if a == nil || a.db == nil {
		return nil
	}
	return a.db.Close()
}

// Path returns the on-disk location ("" when disabled).
func (a *MailArchive) Path() string {
	if a == nil {
		return ""
	}
	return a.path
}

func (a *MailArchive) init() error {
	schema := `
CREATE TABLE IF NOT EXISTS mail_messages (
  id INTEGER PRIMARY KEY,
  service TEXT NOT NULL,
  service_msg_id TEXT NOT NULL,
  thread_id TEXT NOT NULL DEFAULT '',
  message_id TEXT NOT NULL DEFAULT '',
  mailbox TEXT NOT NULL DEFAULT '',
  labels_json TEXT NOT NULL DEFAULT '[]',
  subject TEXT NOT NULL DEFAULT '',
  snippet TEXT NOT NULL DEFAULT '',
  from_name TEXT NOT NULL DEFAULT '',
  from_email TEXT NOT NULL DEFAULT '',
  to_json TEXT NOT NULL DEFAULT '[]',
  cc_json TEXT NOT NULL DEFAULT '[]',
  sent_at TEXT NOT NULL DEFAULT '',
  received_at TEXT NOT NULL DEFAULT '',
  direction TEXT NOT NULL DEFAULT '',
  is_read INTEGER NOT NULL DEFAULT 1,
  body_text TEXT NOT NULL DEFAULT '',
  payload_gzip BLOB,
  dataset TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT '',
  UNIQUE(service, service_msg_id)
);
CREATE INDEX IF NOT EXISTS mail_messages_sent ON mail_messages(sent_at);
CREATE INDEX IF NOT EXISTS mail_messages_thread ON mail_messages(service, thread_id);
CREATE INDEX IF NOT EXISTS mail_messages_from ON mail_messages(from_email);
CREATE INDEX IF NOT EXISTS mail_messages_message_id ON mail_messages(message_id);
CREATE VIRTUAL TABLE IF NOT EXISTS mail_fts USING fts5(
  subject,
  from_email,
  participants,
  body_text
);
CREATE TABLE IF NOT EXISTS mail_meta (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
`
	if _, err := a.db.Exec(schema); err != nil {
		return fmt.Errorf("mail archive schema: %w", err)
	}
	// Older archives predate dataset partitioning (docs/data-scopes.md).
	// The dataset index must come after the column exists — CREATE TABLE
	// IF NOT EXISTS is a no-op on old DBs, and indexing a missing column
	// aborts the schema script before ALTER could run.
	if _, err := a.db.Exec(`ALTER TABLE mail_messages ADD COLUMN dataset TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("mail archive dataset column: %w", err)
	}
	if _, err := a.db.Exec(`CREATE INDEX IF NOT EXISTS mail_messages_dataset ON mail_messages(dataset)`); err != nil {
		return fmt.Errorf("mail archive dataset index: %w", err)
	}
	return nil
}

// Upsert stores or replaces one message and refreshes its FTS row.
func (a *MailArchive) Upsert(rec MailArchiveRecord) error {
	if a == nil || a.db == nil {
		return fmt.Errorf("mail archive disabled")
	}
	if rec.Service == "" || rec.ID == "" {
		return fmt.Errorf("mail archive upsert requires service and id")
	}
	// Store Message-ID in the same normalized form identity matching uses so
	// Gmail and Takeout copies of one message join (issue #139).
	rec.MessageID = types.NormalizeMessageID(rec.MessageID)
	if rec.Labels == nil {
		rec.Labels = []string{}
	}
	labelsJSON, err := json.Marshal(rec.Labels)
	if err != nil {
		return err
	}
	toJSON, err := json.Marshal(rec.To)
	if err != nil {
		return err
	}
	ccJSON, err := json.Marshal(rec.CC)
	if err != nil {
		return err
	}
	read := 0
	if rec.Read {
		read = 1
	}
	now := time.Now().UTC().Format(time.RFC3339)
	participants := mailParticipantsText(rec)

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
INSERT INTO mail_messages (
  service, service_msg_id, thread_id, message_id, mailbox, labels_json,
  subject, snippet, from_name, from_email, to_json, cc_json,
  sent_at, received_at, direction, is_read, body_text, payload_gzip, dataset, updated_at
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(service, service_msg_id) DO UPDATE SET
  thread_id=excluded.thread_id,
  message_id=excluded.message_id,
  mailbox=excluded.mailbox,
  labels_json=excluded.labels_json,
  subject=excluded.subject,
  snippet=excluded.snippet,
  from_name=excluded.from_name,
  from_email=excluded.from_email,
  to_json=excluded.to_json,
  cc_json=excluded.cc_json,
  sent_at=excluded.sent_at,
  received_at=excluded.received_at,
  direction=excluded.direction,
  is_read=excluded.is_read,
  body_text=excluded.body_text,
  payload_gzip=COALESCE(excluded.payload_gzip, mail_messages.payload_gzip),
  dataset=excluded.dataset,
  updated_at=excluded.updated_at
`, rec.Service, rec.ID, rec.ThreadID, rec.MessageID, rec.Mailbox, string(labelsJSON),
		rec.Subject, rec.Snippet, rec.FromName, strings.ToLower(rec.FromEmail),
		string(toJSON), string(ccJSON), rec.SentAt, rec.ReceivedAt, rec.Direction,
		read, rec.BodyText, rec.PayloadGzip, rec.Dataset, now); err != nil {
		return err
	}

	// ON CONFLICT DO UPDATE keeps the original rowid; re-read it for FTS.
	var id int64
	if err := tx.QueryRow(`SELECT id FROM mail_messages WHERE service=? AND service_msg_id=?`,
		rec.Service, rec.ID).Scan(&id); err != nil {
		return err
	}

	if _, err := tx.Exec(`DELETE FROM mail_fts WHERE rowid=?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO mail_fts(rowid, subject, from_email, participants, body_text) VALUES (?,?,?,?,?)`,
		id, rec.Subject, strings.ToLower(rec.FromEmail), participants, rec.BodyText); err != nil {
		return err
	}
	return tx.Commit()
}

// Get returns one archived message by service-local id.
func (a *MailArchive) Get(service, id string) (MailArchiveRecord, error) {
	if a == nil || a.db == nil {
		return MailArchiveRecord{}, fmt.Errorf("mail archive disabled")
	}
	row := a.db.QueryRow(`
SELECT service, service_msg_id, thread_id, message_id, mailbox, labels_json,
       subject, snippet, from_name, from_email, to_json, cc_json,
       sent_at, received_at, direction, is_read, body_text, dataset
FROM mail_messages WHERE service=? AND service_msg_id=?`, service, id)
	rec, err := scanMailRecord(row, true)
	if err != nil {
		return MailArchiveRecord{}, err
	}
	var payload []byte
	if err := a.db.QueryRow(`SELECT payload_gzip FROM mail_messages WHERE service=? AND service_msg_id=?`,
		service, id).Scan(&payload); err == nil {
		rec.PayloadGzip = payload
	}
	return rec, nil
}

// mailSearchMergeCap bounds how many matching source rows are loaded before
// multi-source identity merge, so pagination applies to user-facing messages
// rather than raw (service, id) copies (issue #139).
const mailSearchMergeCap = 5000

// Search runs FTS + structured filters. Limit defaults to 25 (cap
// types.MaxPageLimit). Source rows that share an identity (RFC Message-ID,
// else subject+from+sent_at) are merged into one Message with multi-source
// sources[] before pagination.
func (a *MailArchive) Search(q MailSearchQuery) (MailSearchResponse, error) {
	if a == nil || a.db == nil {
		return MailSearchResponse{}, fmt.Errorf("mail archive disabled")
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
	// Oldest-first is a first-class query ("what is the earliest email?").
	// Default remains newest-first to match live provider listings.
	// Rows with empty sent_at sort last in both directions so they cannot
	// win "oldest"/"newest" answers (issue #77 edge-missing-sent).
	orderBy := `CASE WHEN m.sent_at = '' THEN 1 ELSE 0 END, m.sent_at DESC, m.id DESC`
	if strings.EqualFold(strings.TrimSpace(q.Sort), "asc") {
		orderBy = `CASE WHEN m.sent_at = '' THEN 1 ELSE 0 END, m.sent_at ASC, m.id ASC`
	}

	where := []string{"1=1"}
	args := []any{}
	if ds := strings.TrimSpace(q.Dataset); ds != "" {
		where = append(where, "m.dataset = ?")
		args = append(args, ds)
	}
	if q.After != "" {
		where = append(where, "m.sent_at >= ?")
		args = append(args, normalizeMailTimeBound(q.After, true))
	}
	if q.Before != "" {
		where = append(where, "m.sent_at < ?")
		args = append(args, normalizeMailTimeBound(q.Before, false))
	}
	if from := strings.TrimSpace(q.From); from != "" {
		where = append(where, "(m.from_email = ? OR m.from_email LIKE ? OR m.from_name LIKE ?)")
		lf := strings.ToLower(from)
		args = append(args, lf, "%"+lf+"%", "%"+from+"%")
	}
	if to := strings.TrimSpace(q.To); to != "" {
		where = append(where, "(m.to_json LIKE ? OR m.cc_json LIKE ?)")
		lt := strings.ToLower(to)
		args = append(args, "%"+lt+"%", "%"+lt+"%")
	}
	for _, label := range q.Labels {
		label = strings.TrimSpace(label)
		if label == "" {
			continue
		}
		where = append(where, "m.labels_json LIKE ?")
		args = append(args, `%"`+label+`"%`)
	}

	fts := strings.TrimSpace(q.Q)
	querySQL := `SELECT m.service, m.service_msg_id, m.thread_id, m.message_id, m.mailbox, m.labels_json,
       m.subject, m.snippet, m.from_name, m.from_email, m.to_json, m.cc_json,
       m.sent_at, m.received_at, m.direction, m.is_read, m.body_text, m.dataset `
	queryArgs := args
	if fts != "" {
		match, _ := mailFTSMatch(fts)
		if match == "" {
			match = `""`
		}
		querySQL += `FROM mail_fts f JOIN mail_messages m ON m.id = f.rowid WHERE mail_fts MATCH ? AND ` +
			strings.Join(where, " AND ")
		queryArgs = append([]any{match}, args...)
	} else {
		querySQL += `FROM mail_messages m WHERE ` + strings.Join(where, " AND ")
	}
	querySQL += ` ORDER BY ` + orderBy + ` LIMIT ?`
	queryArgs = append(queryArgs, mailSearchMergeCap)

	rows, err := a.db.Query(querySQL, queryArgs...)
	if err != nil {
		return MailSearchResponse{}, err
	}
	msgs := make([]types.Message, 0, limit)
	for rows.Next() {
		rec, err := scanMailRows(rows, q.WithBody)
		if err != nil {
			rows.Close()
			return MailSearchResponse{}, err
		}
		msgs = append(msgs, rec.Message())
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return MailSearchResponse{}, err
	}
	rows.Close()

	// Collapse gmail + takeout (and other) source rows into one collection entry.
	// FTS/filters may hit only one source; pull Message-ID siblings so the
	// user still sees every source of that message (issue #139).
	merged := MergeMessages(a.hydrateMessageSiblings(msgs, q.Dataset))
	sortMailMessages(merged, strings.EqualFold(strings.TrimSpace(q.Sort), "asc"))
	total := len(merged)
	if offset >= total {
		return MailSearchResponse{Messages: []types.Message{}, Total: total, Limit: limit, Offset: offset, Query: fts}, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	resp := MailSearchResponse{Messages: merged[offset:end], Total: total, Limit: limit, Offset: offset, Query: fts}
	if end < total {
		resp.NextCursor = types.EncodeCursor(end)
	}
	return resp, nil
}

// sortMailMessages orders merged messages like the SQL sent_at sort, with
// empty timestamps last and a stable identity tiebreak (issue #139).
func sortMailMessages(msgs []types.Message, asc bool) {
	sort.SliceStable(msgs, func(i, j int) bool {
		si, sj := msgs[i].SentAt, msgs[j].SentAt
		ei, ej := si == "", sj == ""
		if ei != ej {
			return ej // empty sent_at sorts last
		}
		if si != sj {
			if asc {
				return si < sj
			}
			return si > sj
		}
		ki, kj := messageIdentityKey(msgs[i]), messageIdentityKey(msgs[j])
		if asc {
			return ki < kj
		}
		return ki > kj
	})
}

// hydrateMessageSiblings loads other source rows that share an RFC Message-ID
// with any message already selected, so multi-source identity survives
// filter/FTS that only matched one copy (issue #139). dataset, when non-empty,
// keeps siblings inside one data-scope partition (docs/data-scopes.md).
func (a *MailArchive) hydrateMessageSiblings(msgs []types.Message, dataset string) []types.Message {
	if len(msgs) == 0 {
		return msgs
	}
	want := map[string]bool{}
	have := map[string]bool{}
	for _, m := range msgs {
		for _, src := range m.Sources {
			if src.Service != "" && src.ID != "" {
				have[src.Service+"\x00"+src.ID] = true
			}
		}
		if id := types.NormalizeMessageID(m.EntityID); id != "" {
			want[id] = true
			continue
		}
		for _, src := range m.Sources {
			if id := types.NormalizeMessageID(messageExtraString(src.Extra, "message_id")); id != "" {
				want[id] = true
			}
		}
	}
	if len(want) == 0 {
		return msgs
	}
	placeholders := make([]string, 0, len(want))
	args := make([]any, 0, len(want))
	for id := range want {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	querySQL := `SELECT service, service_msg_id, thread_id, message_id, mailbox, labels_json,
       subject, snippet, from_name, from_email, to_json, cc_json,
       sent_at, received_at, direction, is_read, body_text, dataset
       FROM mail_messages WHERE message_id IN (` + strings.Join(placeholders, ",") + `)`
	if dataset != "" {
		querySQL += ` AND dataset = ?`
		args = append(args, dataset)
	}
	rows, err := a.db.Query(querySQL, args...)
	if err != nil {
		return msgs
	}
	defer rows.Close()
	out := msgs
	for rows.Next() {
		rec, err := scanMailRows(rows, false)
		if err != nil {
			return msgs
		}
		key := rec.Service + "\x00" + rec.ID
		if have[key] {
			continue
		}
		have[key] = true
		out = append(out, rec.Message())
	}
	return out
}

// ListMessagesForLibrary returns merged archive messages for /v1/library/messages
// so Takeout and Gmail archive rows join live providers as one user collection
// (issue #139). limit bounds the page; truncated reports that more archive
// rows exist (full history remains /v1/mail/search). dataset restricts the
// data-scope partition when non-empty (docs/data-scopes.md).
func (a *MailArchive) ListMessagesForLibrary(limit int, dataset string) (msgs []types.Message, truncated bool, err error) {
	if a == nil || a.db == nil {
		return nil, false, fmt.Errorf("mail archive disabled")
	}
	if limit <= 0 {
		limit = 200
	}
	// Load one extra merged identity to detect truncation after merge.
	resp, err := a.Search(MailSearchQuery{Limit: limit, WithBody: true, Dataset: dataset})
	if err != nil {
		return nil, false, err
	}
	if resp.Total > len(resp.Messages) {
		truncated = true
	}
	return resp.Messages, truncated, nil
}

// Status summarizes the archive for /v1/mail/status.
func (a *MailArchive) Status() (MailArchiveStatus, error) {
	st := MailArchiveStatus{
		Enabled:       a != nil && a.db != nil,
		SyncPageToken: map[string]string{},
		SyncDone:      map[string]bool{},
	}
	if !st.Enabled {
		return st, nil
	}
	st.Path = a.path
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM mail_messages`).Scan(&st.MessageCount); err != nil {
		return st, err
	}
	st.ByService = map[string]int{}
	rows, err := a.db.Query(`SELECT service, COUNT(*) FROM mail_messages GROUP BY service`)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var svc string
		var n int
		if err := rows.Scan(&svc, &n); err != nil {
			return st, err
		}
		st.ByService[svc] = n
	}
	if err := rows.Err(); err != nil {
		return st, err
	}
	st.LastSyncAt, _ = a.getMeta("sync:last_at")
	st.LastSyncError, _ = a.getMeta("sync:last_error")
	st.TakeoutPath = a.TakeoutPath()
	metaRows, err := a.db.Query(`SELECT key, value FROM mail_meta WHERE key LIKE 'sync:%:page_token' OR key LIKE 'sync:%:done'`)
	if err != nil {
		return st, err
	}
	defer metaRows.Close()
	for metaRows.Next() {
		var k, v string
		if err := metaRows.Scan(&k, &v); err != nil {
			return st, err
		}
		switch {
		case strings.HasSuffix(k, ":page_token"):
			svc := strings.TrimSuffix(strings.TrimPrefix(k, "sync:"), ":page_token")
			st.SyncPageToken[svc] = v
		case strings.HasSuffix(k, ":done"):
			svc := strings.TrimSuffix(strings.TrimPrefix(k, "sync:"), ":done")
			st.SyncDone[svc] = v == "1"
		}
	}
	return st, metaRows.Err()
}

// LoadSyncCursor reads the durable ingest cursor for a service.
func (a *MailArchive) LoadSyncCursor(service string) (MailSyncCursor, error) {
	var cur MailSyncCursor
	if a == nil || a.db == nil {
		return cur, fmt.Errorf("mail archive disabled")
	}
	cur.PageToken, _ = a.getMeta("sync:" + service + ":page_token")
	done, _ := a.getMeta("sync:" + service + ":done")
	cur.Done = done == "1"
	cur.LastSyncAt, _ = a.getMeta("sync:" + service + ":last_at")
	cur.Error, _ = a.getMeta("sync:" + service + ":error")
	return cur, nil
}

// SaveSyncCursor persists ingest progress (fail-open callers still record errors).
func (a *MailArchive) SaveSyncCursor(service string, cur MailSyncCursor) error {
	if a == nil || a.db == nil {
		return fmt.Errorf("mail archive disabled")
	}
	if err := a.setMeta("sync:"+service+":page_token", cur.PageToken); err != nil {
		return err
	}
	done := "0"
	if cur.Done {
		done = "1"
	}
	if err := a.setMeta("sync:"+service+":done", done); err != nil {
		return err
	}
	if cur.LastSyncAt != "" {
		if err := a.setMeta("sync:"+service+":last_at", cur.LastSyncAt); err != nil {
			return err
		}
		if ifErr := a.setMeta("sync:last_at", cur.LastSyncAt); ifErr != nil {
			return ifErr
		}
	}
	if err := a.setMeta("sync:"+service+":error", cur.Error); err != nil {
		return err
	}
	return a.setMeta("sync:last_error", cur.Error)
}

func (a *MailArchive) getMeta(key string) (string, error) {
	var v string
	err := a.db.QueryRow(`SELECT value FROM mail_meta WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (a *MailArchive) setMeta(key, value string) error {
	_, err := a.db.Exec(`
INSERT INTO mail_meta(key, value) VALUES(?,?)
ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// Payload decompresses the stored provider payload JSON.
func (r MailArchiveRecord) Payload() ([]byte, error) {
	if len(r.PayloadGzip) == 0 {
		return nil, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(r.PayloadGzip))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

// GzipPayload compresses provider JSON for storage.
func GzipPayload(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func mailParticipantsText(rec MailArchiveRecord) string {
	var b strings.Builder
	writeContacts := func(cs []types.Contact) {
		for _, c := range cs {
			if c.Name != "" {
				b.WriteString(c.Name)
				b.WriteByte(' ')
			}
			for _, e := range c.Emails {
				b.WriteString(strings.ToLower(e))
				b.WriteByte(' ')
			}
		}
	}
	b.WriteString(strings.ToLower(rec.FromEmail))
	if rec.FromName != "" {
		b.WriteByte(' ')
		b.WriteString(rec.FromName)
	}
	b.WriteByte(' ')
	writeContacts(rec.To)
	writeContacts(rec.CC)
	return strings.TrimSpace(b.String())
}

// mailFTSMatch turns user text into a safe FTS5 MATCH expression: AND of
// quoted tokens across all columns. Operator characters are stripped so agents
// cannot inject NEAR/OR/column filters.
func mailFTSMatch(q string) (string, []any) {
	q = strings.TrimSpace(q)
	if q == "" {
		return "", nil
	}
	var parts []string
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
		for _, sub := range strings.Fields(cleaned) {
			if sub == "" {
				continue
			}
			parts = append(parts, `"`+sub+`"`)
		}
	}
	if len(parts) == 0 {
		return "", nil
	}
	return strings.Join(parts, " AND "), nil
}

func normalizeMailTimeBound(raw string, lower bool) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		if lower {
			return t.UTC().Format(time.RFC3339)
		}
		return t.UTC().Add(24 * time.Hour).Format(time.RFC3339)
	}
	return raw
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMailRecord(row rowScanner, withBody bool) (MailArchiveRecord, error) {
	var (
		rec        MailArchiveRecord
		labelsJSON string
		toJSON     string
		ccJSON     string
		isRead     int
		body       string
	)
	scanDest := []any{
		&rec.Service, &rec.ID, &rec.ThreadID, &rec.MessageID, &rec.Mailbox, &labelsJSON,
		&rec.Subject, &rec.Snippet, &rec.FromName, &rec.FromEmail, &toJSON, &ccJSON,
		&rec.SentAt, &rec.ReceivedAt, &rec.Direction, &isRead, &body, &rec.Dataset,
	}
	if err := row.Scan(scanDest...); err != nil {
		return MailArchiveRecord{}, err
	}
	rec.Read = isRead == 1
	if withBody {
		rec.BodyText = body
	} else {
		rec.BodyText = ""
	}
	if labelsJSON != "" {
		_ = json.Unmarshal([]byte(labelsJSON), &rec.Labels)
	}
	if toJSON != "" {
		_ = json.Unmarshal([]byte(toJSON), &rec.To)
	}
	if ccJSON != "" {
		_ = json.Unmarshal([]byte(ccJSON), &rec.CC)
	}
	rec.ServiceName = mailServiceName(rec.Service)
	return rec, nil
}

func scanMailRows(rows *sql.Rows, withBody bool) (MailArchiveRecord, error) {
	return scanMailRecord(rows, withBody)
}

func mailServiceName(service string) string {
	switch service {
	case connectors.GoogleGmailServiceID:
		return connectors.GoogleGmailServiceName
	case takeoutServiceID:
		return takeoutServiceName
	default:
		return service
	}
}

// RecordFromMessage builds an archive row from a neutral Message plus optional
// gzip provider payload. Missing fields stay empty rather than failing ingest.
func RecordFromMessage(service, serviceName string, msg types.Message, payloadGzip []byte) MailArchiveRecord {
	id := ""
	if len(msg.Sources) > 0 {
		id = msg.Sources[0].ID
		if serviceName == "" {
			serviceName = msg.Sources[0].ServiceName
		}
		if service == "" {
			service = msg.Sources[0].Service
		}
	}
	rec := MailArchiveRecord{
		Service:     service,
		ServiceName: serviceName,
		ID:          id,
		ThreadID:    msg.ThreadID,
		MessageID:   msg.EntityID,
		Subject:     msg.Subject,
		Snippet:     msg.Snippet,
		To:          msg.To,
		CC:          msg.CC,
		SentAt:      msg.SentAt,
		ReceivedAt:  msg.ReceivedAt,
		Direction:   msg.Direction,
		Read:        msg.Read,
		BodyText:    msg.Body,
		PayloadGzip: payloadGzip,
		Labels:      msg.Labels,
	}
	if len(msg.Sources) > 0 {
		rec.Mailbox = msg.Sources[0].Mailbox
	}
	if msg.From != nil {
		rec.FromName = msg.From.Name
		if len(msg.From.Emails) > 0 {
			rec.FromEmail = msg.From.Emails[0]
		}
	}
	if rec.Labels == nil {
		rec.Labels = []string{}
	}
	return rec
}
