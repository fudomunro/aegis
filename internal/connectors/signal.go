package connectors

import (
	"bufio"
	"database/sql"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

// Signal local import (issue #168). Signal has no public message API, so
// contacts and chat messages arrive as a file-based export the operator
// prepares (Desktop/Android backup → plaintext SQLite, or a JSONL dump).
// Aegis stores neutral contact + message metadata only — never attachments
// or other media. The local store feeds /v1/library/contacts and
// /v1/library/messages like other file-based import sources.

const (
	SignalServiceID   = "signal"
	SignalServiceName = "Signal"
)

// SignalService is the service card the local Signal import store injects
// into config (issue #266).
func SignalService() LocalStoreService {
	return LocalStoreService{
		ID:          SignalServiceID,
		Name:        SignalServiceName,
		Action:      "inject",
		Description: "Locally imported Signal contacts and messages (Advanced → Signal import).",
		GrantLimits: types.Permissions{
			"contacts": types.AccessRead,
			"messages": types.AccessRead,
		},
	}
}

// SignalStore is a local SQLite store of imported Signal contacts/messages.
// Empty path disables the store (like mail-archive / quicken / timeline).
type SignalStore struct {
	localStore
}

// SignalImportResult reports one import run.
type SignalImportResult struct {
	Path            string `json:"path"`
	Format          string `json:"format,omitempty"`
	ContactsScanned int    `json:"contacts_scanned"`
	ContactsStored  int    `json:"contacts_stored"`
	ContactsSkipped int    `json:"contacts_skipped"`
	MessagesScanned int    `json:"messages_scanned"`
	MessagesStored  int    `json:"messages_stored"`
	MessagesSkipped int    `json:"messages_skipped"`
	Errors          int    `json:"errors"`
	Done            bool   `json:"done"`
	Error           string `json:"error,omitempty"`
}

// LogAttrs returns slog keyvals for one finished import run.
func (r SignalImportResult) LogAttrs() []any {
	return []any{"format", r.Format,
		"contacts_stored", r.ContactsStored, "messages_stored", r.MessagesStored,
		"contacts_skipped", r.ContactsSkipped, "messages_skipped", r.MessagesSkipped,
		"errors", r.Errors, "done", r.Done}
}

// Failed reports a run that recorded errors but stored nothing.
func (r SignalImportResult) Failed() bool {
	return r.Errors > 0 && r.ContactsStored == 0 && r.MessagesStored == 0
}

var _ ImportStore = (*SignalStore)(nil)
var _ ImportOutcome = SignalImportResult{}

// OpenSignalStore opens (or creates) the local Signal SQLite store.
// An empty path returns nil (disabled).
func OpenSignalStore(path string) (*SignalStore, error) {
	s := &SignalStore{}
	if err := s.openLocalStore(path, "signal", signalSchema); err != nil {
		return nil, err
	}
	if !s.Enabled() {
		return nil, nil
	}
	return s, nil
}

// signalSchema installs the imported contact/message tables.
const signalSchema = `

CREATE TABLE IF NOT EXISTS signal_contacts (
  source_id TEXT PRIMARY KEY,
  file_path TEXT NOT NULL,
  name TEXT,
  phones TEXT,
  emails TEXT,
  handles TEXT,
  organization TEXT,
  raw_json TEXT
);
CREATE TABLE IF NOT EXISTS signal_messages (
  source_id TEXT PRIMARY KEY,
  file_path TEXT NOT NULL,
  thread_id TEXT,
  sent_at TEXT,
  received_at TEXT,
  direction TEXT,
  body_snippet TEXT,
  from_name TEXT,
  from_phone TEXT,
  raw_json TEXT
);
CREATE INDEX IF NOT EXISTS signal_messages_sent ON signal_messages(sent_at);
CREATE INDEX IF NOT EXISTS signal_messages_thread ON signal_messages(thread_id);
`



func (s *SignalStore) Enabled() bool { return s != nil && s.db != nil }

// upsertSignalContactTx stores or replaces one imported contact keyed by
// source_id.
func upsertSignalContactTx(tx *sql.Tx, filePath string, c types.Contact) error {
	id := signalContactSourceID(c)
	if id == "" {
		return fmt.Errorf("signal contact missing source id")
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`
INSERT INTO signal_contacts(
  source_id, file_path, name, phones, emails, handles, organization, raw_json
) VALUES (?,?,?,?,?,?,?,?)
ON CONFLICT(source_id) DO UPDATE SET
  file_path=excluded.file_path,
  name=excluded.name,
  phones=excluded.phones,
  emails=excluded.emails,
  handles=excluded.handles,
  organization=excluded.organization,
  raw_json=excluded.raw_json
`,
		id, filePath, c.Name,
		strings.Join(c.Phones, "\n"),
		strings.Join(c.Emails, "\n"),
		strings.Join(c.Handles, "\n"),
		c.Organization, string(raw),
	)
	return err
}

// upsertSignalMessageTx stores or replaces one imported message keyed by
// source_id.
func upsertSignalMessageTx(tx *sql.Tx, filePath string, m types.Message) error {
	id := signalMessageSourceID(m)
	if id == "" {
		return fmt.Errorf("signal message missing source id")
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	fromName, fromPhone := "", ""
	if m.From != nil {
		fromName = m.From.Name
		if len(m.From.Phones) > 0 {
			fromPhone = m.From.Phones[0]
		}
	}
	snippet := m.Snippet
	if snippet == "" {
		snippet = m.Body
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
	}
	_, err = tx.Exec(`
INSERT INTO signal_messages(
  source_id, file_path, thread_id, sent_at, received_at, direction,
  body_snippet, from_name, from_phone, raw_json
) VALUES (?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(source_id) DO UPDATE SET
  file_path=excluded.file_path,
  thread_id=excluded.thread_id,
  sent_at=excluded.sent_at,
  received_at=excluded.received_at,
  direction=excluded.direction,
  body_snippet=excluded.body_snippet,
  from_name=excluded.from_name,
  from_phone=excluded.from_phone,
  raw_json=excluded.raw_json
`,
		id, filePath, m.ThreadID, m.SentAt, m.ReceivedAt, m.Direction,
		snippet, fromName, fromPhone, string(raw),
	)
	return err
}

// UpsertContact stores or replaces one imported contact keyed by source_id.
func (s *SignalStore) UpsertContact(filePath string, c types.Contact) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("signal store disabled")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := upsertSignalContactTx(tx, filePath, c); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// UpsertMessage stores or replaces one imported message keyed by source_id.
func (s *SignalStore) UpsertMessage(filePath string, m types.Message) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("signal store disabled")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := upsertSignalMessageTx(tx, filePath, m); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

const (
	signalContactHasQuery = `SELECT COUNT(1) FROM signal_contacts WHERE source_id = ?`
	signalMessageHasQuery = `SELECT COUNT(1) FROM signal_messages WHERE source_id = ?`
)

// HasContact reports whether the contact source_id is already stored.
func (s *SignalStore) HasContact(sourceID string) bool {
	return s.hasRow(`SELECT COUNT(1) FROM signal_contacts WHERE source_id = ?`, sourceID)
}

// HasMessage reports whether the message source_id is already stored.
func (s *SignalStore) HasMessage(sourceID string) bool {
	return s.hasRow(`SELECT COUNT(1) FROM signal_messages WHERE source_id = ?`, sourceID)
}

// ListContacts returns stored contacts (name order).
func (s *SignalStore) ListContacts(limit int) ([]types.Contact, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("signal store disabled")
	}
	if limit <= 0 || limit > LibraryItemCap {
		limit = LibraryItemCap
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`
SELECT raw_json FROM signal_contacts
ORDER BY name ASC, source_id ASC
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.Contact
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var c types.Contact
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			continue
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListMessages returns stored messages (newest first).
func (s *SignalStore) ListMessages(limit int) ([]types.Message, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("signal store disabled")
	}
	if limit <= 0 || limit > LibraryItemCap {
		limit = LibraryItemCap
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`
SELECT raw_json FROM signal_messages
ORDER BY sent_at DESC, source_id ASC
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.Message
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var m types.Message
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			continue
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Counts returns stored contact and message totals.
func (s *SignalStore) Counts() (contacts, messages int, err error) {
	if s == nil || s.db == nil {
		return 0, 0, fmt.Errorf("signal store disabled")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err = s.db.QueryRow(`SELECT COUNT(1) FROM signal_contacts`).Scan(&contacts); err != nil {
		return 0, 0, err
	}
	if err = s.db.QueryRow(`SELECT COUNT(1) FROM signal_messages`).Scan(&messages); err != nil {
		return contacts, 0, err
	}
	return contacts, messages, nil
}

func signalContactSourceID(c types.Contact) string {
	if len(c.Sources) > 0 {
		if id := strings.TrimSpace(c.Sources[0].ID); id != "" {
			return id
		}
	}
	if k := types.PrimaryContactKey(c); k != "" {
		return k
	}
	return ""
}

func signalMessageSourceID(m types.Message) string {
	if len(m.Sources) > 0 {
		if id := strings.TrimSpace(m.Sources[0].ID); id != "" {
			return id
		}
	}
	if id := strings.TrimSpace(m.EntityID); id != "" {
		return id
	}
	// Stable fallback so re-imports still dedupe.
	parts := []string{m.ThreadID, m.SentAt, m.Direction, m.Body, m.Snippet}
	return "sig-" + shortHash(strings.Join(parts, "\x00"))
}

func newSignalContact(id, name string, phones, emails, handles []string, organization string) types.Contact {
	c := types.NewContact(SignalServiceID, SignalServiceName, id, name, emails, phones)
	if c.Name == "" && len(c.Phones) == 0 && len(c.Emails) == 0 && len(handles) == 0 {
		return types.Contact{}
	}
	c.Handles = types.NormalizeHandleList(handles)
	if organization != "" {
		c.Organization = organization
	}
	if id != "" && len(c.Sources) == 1 {
		if c.Sources[0].Extra == nil {
			c.Sources[0].Extra = map[string]any{}
		}
		c.Sources[0].Extra["id"] = id
	}
	return c
}

func newSignalMessage(rec signalMessageRecord) types.Message {
	id := strings.TrimSpace(rec.ID)
	if id == "" {
		id = "sig-" + shortHash(strings.Join([]string{rec.ThreadID, rec.SentAt, rec.Direction, rec.Body}, "\x00"))
	}
	from := rec.From
	if from != nil {
		// Normalize into a Signal-sourced contact spelling.
		nc := types.NewContact(SignalServiceID, SignalServiceName, "", from.Name, from.Emails, from.Phones)
		if nc.Name != "" || len(nc.Phones) > 0 || len(nc.Emails) > 0 {
			from = &nc
		} else {
			from = nil
		}
	}
	to := make([]types.Contact, 0, len(rec.To))
	for _, t := range rec.To {
		nc := types.NewContact(SignalServiceID, SignalServiceName, "", t.Name, t.Emails, t.Phones)
		if nc.Name != "" || len(nc.Phones) > 0 || len(nc.Emails) > 0 {
			to = append(to, nc)
		}
	}
	snippet := rec.Snippet
	if snippet == "" && rec.Body != "" {
		snippet = rec.Body
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
	}
	src := types.MessageSource{
		Service:     SignalServiceID,
		ServiceName: SignalServiceName,
		ID:          id,
		ThreadID:    rec.ThreadID,
		Canonical:   true,
	}
	// Signal has no RFC Message-ID; the service-local id is the durable key.
	return types.Message{
		Channel:    types.MessageChannelSignal,
		Subject:    rec.Subject,
		Snippet:    snippet,
		Body:       rec.Body,
		From:       from,
		To:         to,
		SentAt:     rec.SentAt,
		ReceivedAt: rec.ReceivedAt,
		Direction:  normalizeSignalDirection(rec.Direction, rec.FromMe),
		Read:       rec.Read,
		ThreadID:   rec.ThreadID,
		Labels:     rec.Labels,
		Sources:    []types.MessageSource{src},
	}
}

func normalizeSignalDirection(direction string, fromMe bool) string {
	d := strings.ToLower(strings.TrimSpace(direction))
	switch d {
	case "inbound", "in", "incoming", "received":
		return "inbound"
	case "outbound", "out", "outgoing", "sent":
		return "outbound"
	case "unknown":
		return "unknown"
	}
	if fromMe {
		return "outbound"
	}
	if d != "" {
		return "unknown"
	}
	return "unknown"
}

// signalMessageRecord is the intermediate shape parsers fill before the
// neutral Message is built (issue #168).
type signalMessageRecord struct {
	ID         string
	Subject    string
	Body       string
	Snippet    string
	SentAt     string
	ReceivedAt string
	Direction  string
	FromMe     bool
	Read       bool
	ThreadID   string
	From       *types.Contact
	To         []types.Contact
	Labels     []string
}

// --- Import ---

// signalJSONLLine is one JSONL record. Type is "contact" or "message".
type signalJSONLLine struct {
	Type         string          `json:"type"`
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Phones       []string        `json:"phones"`
	Emails       []string        `json:"emails"`
	Handles      []string        `json:"handles"`
	Organization string          `json:"organization"`
	Subject      string          `json:"subject"`
	Body         string          `json:"body"`
	Snippet      string          `json:"snippet"`
	SentAt       string          `json:"sent_at"`
	ReceivedAt   string          `json:"received_at"`
	Direction    string          `json:"direction"`
	FromMe       bool            `json:"from_me"`
	Read         bool            `json:"read"`
	ThreadID     string          `json:"thread_id"`
	From         *signalPerson   `json:"from"`
	To           []signalPerson  `json:"to"`
	Labels       []string        `json:"labels"`
	Extra        json.RawMessage `json:"extra"`
}

// signalPerson is a participant spelling in JSON exports.
type signalPerson struct {
	Name   string   `json:"name"`
	Phones []string `json:"phones"`
	Emails []string `json:"emails"`
}

// signalJSONBundle is a single JSON object export with contacts[] + messages[].
type signalJSONBundle struct {
	Contacts []signalJSONLLine `json:"contacts"`
	Messages []signalJSONLLine `json:"messages"`
}

func (p signalPerson) contact() types.Contact {
	return types.NewContact(SignalServiceID, SignalServiceName, "", p.Name, p.Emails, p.Phones)
}

func (l signalJSONLLine) toContact() types.Contact {
	return newSignalContact(l.ID, l.Name, l.Phones, l.Emails, l.Handles, l.Organization)
}

func (l signalJSONLLine) toMessage() types.Message {
	rec := signalMessageRecord{
		ID:         l.ID,
		Subject:    l.Subject,
		Body:       l.Body,
		Snippet:    l.Snippet,
		SentAt:     l.SentAt,
		ReceivedAt: l.ReceivedAt,
		Direction:  l.Direction,
		FromMe:     l.FromMe,
		Read:       l.Read,
		ThreadID:   l.ThreadID,
		Labels:     l.Labels,
	}
	if l.From != nil {
		c := l.From.contact()
		if c.Name != "" || len(c.Phones) > 0 || len(c.Emails) > 0 {
			rec.From = &c
		}
	}
	for _, t := range l.To {
		c := t.contact()
		if c.Name != "" || len(c.Phones) > 0 || len(c.Emails) > 0 {
			rec.To = append(rec.To, c)
		}
	}
	return newSignalMessage(rec)
}

// ImportSignalPath imports a Signal export file or directory.
//
// Accepted inputs:
//   - .jsonl / .ndjson: one JSON object per line (type=contact|message)
//   - .json: either JSONL-style array/bundle with contacts[]/messages[]
//   - .db / .sqlite / .sqlite3: plaintext Signal Desktop or Android dump
//     (from signalbackup-tools / a decrypted Desktop copy) — never the
//     encrypted app database
//   - a directory of the above (non-recursive)
//
// Re-import is safe: rows already stored are skipped.
func ImportSignalPath(store *SignalStore, path string) (SignalImportResult, error) {
	res := SignalImportResult{Path: path}
	if store == nil || !store.Enabled() {
		return res, fmt.Errorf("signal store disabled")
	}
	info, err := os.Stat(path)
	if err != nil {
		return res, err
	}
	var files []string
	if info.IsDir() {
		entries, err := os.ReadDir(path)
		if err != nil {
			return res, err
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			ext := strings.ToLower(filepath.Ext(e.Name()))
			switch ext {
			case ".jsonl", ".ndjson", ".json", ".db", ".sqlite", ".sqlite3":
				files = append(files, filepath.Join(path, e.Name()))
			}
		}
	} else {
		files = []string{path}
	}
	if len(files) == 0 {
		return res, fmt.Errorf("no importable Signal export files in %s (.jsonl, .json, or plaintext .db/.sqlite)", path)
	}
	for _, file := range files {
		if err := importSignalFile(store, file, &res); err != nil {
			res.Errors++
			slog.Warn("signal import file failed", "path", file, "error", err)
		}
	}
	res.Done = true
	return res, nil
}

func importSignalFile(store *SignalStore, file string, res *SignalImportResult) error {
	batch := &signalBatch{file: file}
	var err error
	ext := strings.ToLower(filepath.Ext(file))
	switch ext {
	case ".db", ".sqlite", ".sqlite3":
		if res.Format == "" {
			res.Format = "sqlite"
		} else if res.Format != "sqlite" {
			res.Format = "mixed"
		}
		err = importSignalSQLite(store, file, res, batch)
	case ".jsonl", ".ndjson":
		if res.Format == "" {
			res.Format = "jsonl"
		} else if res.Format != "jsonl" {
			res.Format = "mixed"
		}
		err = importSignalJSONL(store, file, res, batch)
	case ".json":
		if res.Format == "" {
			res.Format = "json"
		} else if res.Format != "json" {
			res.Format = "mixed"
		}
		err = importSignalJSON(store, file, res, batch)
	default:
		err = fmt.Errorf("unsupported Signal export type %q", ext)
	}
	batch.flush(store, res)
	return err
}

func importSignalJSONL(store *SignalStore, file string, res *SignalImportResult, batch *signalBatch) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var rec signalJSONLLine
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			res.Errors++
			slog.Warn("signal jsonl line parse failed", "path", file, "line", lineNo, "error", err)
			continue
		}
		if err := storeSignalRecord(rec, res, batch); err != nil {
			res.Errors++
			slog.Warn("signal jsonl store failed", "path", file, "line", lineNo, "error", err)
		}
	}
	return sc.Err()
}

func importSignalJSON(store *SignalStore, file string, res *SignalImportResult, batch *signalBatch) error {
	raw, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil
	}
	// Bundle form: {"contacts":[...],"messages":[...]}
	var bundle signalJSONBundle
	if err := json.Unmarshal(raw, &bundle); err == nil && (len(bundle.Contacts) > 0 || len(bundle.Messages) > 0) {
		for _, rec := range bundle.Contacts {
			rec.Type = "contact"
			if err := storeSignalRecord(rec, res, batch); err != nil {
				res.Errors++
			}
		}
		for _, rec := range bundle.Messages {
			rec.Type = "message"
			if err := storeSignalRecord(rec, res, batch); err != nil {
				res.Errors++
			}
		}
		return nil
	}
	// Array form: [{...},{...}]
	var lines []signalJSONLLine
	if err := json.Unmarshal(raw, &lines); err == nil && len(lines) > 0 {
		for _, rec := range lines {
			if err := storeSignalRecord(rec, res, batch); err != nil {
				res.Errors++
			}
		}
		return nil
	}
	// Single object
	var one signalJSONLLine
	if err := json.Unmarshal(raw, &one); err == nil && (one.Type != "" || one.Body != "" || one.Name != "") {
		return storeSignalRecord(one, res, batch)
	}
	return fmt.Errorf("unrecognized Signal JSON shape in %s", file)
}


// signalBatch collects parsed rows for one import file so contacts and
// messages are written in batched transactions (issue #266).
type signalBatch struct {
	file     string
	contacts []types.Contact
	messages []types.Message
}

// flush writes the collected rows and folds per-kind counters into res.
func (b *signalBatch) flush(store *SignalStore, res *SignalImportResult) {
	if store == nil || !store.Enabled() {
		return
	}
	if len(b.contacts) > 0 {
		var st ImportStats
		upsertRows(&store.localStore, &st, b.contacts, signalContactSourceID, signalContactHasQuery,
			func(tx *sql.Tx, c types.Contact) error { return upsertSignalContactTx(tx, b.file, c) })
		res.ContactsStored += st.Stored
		res.ContactsSkipped += st.Skipped
		res.Errors += st.Errors
		b.contacts = nil
	}
	if len(b.messages) > 0 {
		var st ImportStats
		upsertRows(&store.localStore, &st, b.messages, signalMessageSourceID, signalMessageHasQuery,
			func(tx *sql.Tx, m types.Message) error { return upsertSignalMessageTx(tx, b.file, m) })
		res.MessagesStored += st.Stored
		res.MessagesSkipped += st.Skipped
		res.Errors += st.Errors
		b.messages = nil
	}
}

func storeSignalRecord(rec signalJSONLLine, res *SignalImportResult, b *signalBatch) error {
	switch strings.ToLower(strings.TrimSpace(rec.Type)) {
	case "contact", "recipient", "person":
		res.ContactsScanned++
		c := rec.toContact()
		if signalContactSourceID(c) == "" {
			return fmt.Errorf("contact missing id/identity")
		}
		b.contacts = append(b.contacts, c)
		return nil
	case "message", "chat", "sms":
		res.MessagesScanned++
		m := rec.toMessage()
		if signalMessageSourceID(m) == "" {
			return fmt.Errorf("message missing id")
		}
		b.messages = append(b.messages, m)
		return nil
	default:
		return fmt.Errorf("unknown Signal record type %q", rec.Type)
	}
}

// importSignalSQLite reads a plaintext Signal dump. It auto-detects the
// Signal Desktop schema (conversations/messages) or the Android /
// signalbackup-tools schema (recipient/message/thread). Encrypted app
// databases are not supported — decrypt or export first.
func importSignalSQLite(store *SignalStore, file string, res *SignalImportResult, batch *signalBatch) error {
	db, err := sql.Open("sqlite", file)
	if err != nil {
		return err
	}
	defer db.Close()
	// Read-only view of the export; do not let sqlite create sidecars.
	if _, err := db.Exec(`PRAGMA query_only=1`); err != nil {
		return err
	}
	tables := map[string]bool{}
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		return fmt.Errorf("not a plaintext Signal SQLite dump: %w", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		tables[strings.ToLower(name)] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	switch {
	case tables["messages"] && tables["conversations"]:
		return importSignalDesktopSQLite(db, store, file, res, batch)
	case tables["message"] && tables["recipient"]:
		return importSignalAndroidSQLite(db, store, file, res, batch)
	default:
		return fmt.Errorf("unrecognized Signal SQLite schema in %s (want Desktop conversations/messages or Android recipient/message)", file)
	}
}

func importSignalDesktopSQLite(db *sql.DB, store *SignalStore, file string, res *SignalImportResult, batch *signalBatch) error {
	// Conversations → contacts (private chats with a phone / name).
	crows, err := db.Query(`
SELECT id, IFNULL(name,''), IFNULL(e164,''), IFNULL(serviceId,''), IFNULL(type,''), IFNULL(profileName,'')
FROM conversations`)
	if err != nil {
		return err
	}
	for crows.Next() {
		var id, name, e164, serviceID, typ, profile string
		if err := crows.Scan(&id, &name, &e164, &serviceID, &typ, &profile); err != nil {
			crows.Close()
			return err
		}
		if strings.EqualFold(typ, "group") {
			continue
		}
		display := name
		if display == "" {
			display = profile
		}
		var phones []string
		if e164 != "" {
			phones = []string{e164}
		}
		var handles []string
		if serviceID != "" {
			handles = []string{serviceID}
		}
		rec := signalJSONLLine{
			Type:    "contact",
			ID:      id,
			Name:    display,
			Phones:  phones,
			Handles: handles,
		}
		if err := storeSignalRecord(rec, res, batch); err != nil {
			res.Errors++
		}
	}
	crows.Close()
	if err := crows.Err(); err != nil {
		return err
	}

	mrows, err := db.Query(`
SELECT id, conversationId, IFNULL(type,''), IFNULL(body,''),
       IFNULL(timestamp,0), IFNULL(sent_at,0), IFNULL(received_at,0)
FROM messages`)
	if err != nil {
		return err
	}
	defer mrows.Close()
	for mrows.Next() {
		var id, convID, typ, body string
		var ts, sentAt, recvAt int64
		if err := mrows.Scan(&id, &convID, &typ, &body, &ts, &sentAt, &recvAt); err != nil {
			return err
		}
		if body == "" {
			continue // skip system / attachment-only rows
		}
		direction := "unknown"
		fromMe := false
		switch strings.ToLower(typ) {
		case "incoming":
			direction = "inbound"
		case "outgoing", "outgoing_call", "outgoing_video_call":
			direction = "outbound"
			fromMe = true
		}
		sent := signalMillisRFC3339(sentAt)
		if sent == "" {
			sent = signalMillisRFC3339(ts)
		}
		recv := signalMillisRFC3339(recvAt)
		rec := signalJSONLLine{
			Type:       "message",
			ID:         id,
			Body:       body,
			SentAt:     sent,
			ReceivedAt: recv,
			Direction:  direction,
			FromMe:     fromMe,
			ThreadID:   convID,
			Read:       true,
		}
		if err := storeSignalRecord(rec, res, batch); err != nil {
			res.Errors++
		}
	}
	return mrows.Err()
}

func importSignalAndroidSQLite(db *sql.DB, store *SignalStore, file string, res *SignalImportResult, batch *signalBatch) error {
	// recipient → contacts
	rrows, err := db.Query(`
SELECT _id, IFNULL(display_name,''), IFNULL(phone,'')
FROM recipient`)
	if err != nil {
		return err
	}
	for rrows.Next() {
		var id int64
		var name, phone string
		if err := rrows.Scan(&id, &name, &phone); err != nil {
			rrows.Close()
			return err
		}
		var phones []string
		if phone != "" {
			phones = []string{phone}
		}
		rec := signalJSONLLine{
			Type:   "contact",
			ID:     strconv.FormatInt(id, 10),
			Name:   name,
			Phones: phones,
		}
		if err := storeSignalRecord(rec, res, batch); err != nil {
			res.Errors++
		}
	}
	rrows.Close()
	if err := rrows.Err(); err != nil {
		return err
	}

	// thread → map thread_id to recipient for participant lookup
	threadToRecipient := map[int64]int64{}
	if trows, err := db.Query(`SELECT _id, IFNULL(recipient_id,0) FROM thread`); err == nil {
		for trows.Next() {
			var tid, rid int64
			if err := trows.Scan(&tid, &rid); err == nil {
				threadToRecipient[tid] = rid
			}
		}
		trows.Close()
	}

	// recipient phone/name by id
	recipients := map[int64]signalPerson{}
	if r2, err := db.Query(`SELECT _id, IFNULL(display_name,''), IFNULL(phone,'') FROM recipient`); err == nil {
		for r2.Next() {
			var id int64
			var name, phone string
			if err := r2.Scan(&id, &name, &phone); err == nil {
				recipients[id] = signalPerson{Name: name, Phones: nonEmpty(phone)}
			}
		}
		r2.Close()
	}

	mrows, err := db.Query(`
SELECT _id, thread_id, IFNULL(address_id,0), IFNULL(date_sent,0), IFNULL(date,0),
       IFNULL(type,0), IFNULL(body,'')
FROM message`)
	if err != nil {
		return err
	}
	defer mrows.Close()
	for mrows.Next() {
		var id, threadID, addrID, dateSent, date, typ int64
		var body string
		if err := mrows.Scan(&id, &threadID, &addrID, &dateSent, &date, &typ, &body); err != nil {
			return err
		}
		if body == "" {
			continue
		}
		direction, fromMe := androidMessageDirection(typ)
		sent := signalMillisRFC3339(dateSent)
		if sent == "" {
			sent = signalMillisRFC3339(date)
		}
		var from *signalPerson
		if p, ok := recipients[addrID]; ok && (p.Name != "" || len(p.Phones) > 0) {
			from = &p
		} else if rid, ok := threadToRecipient[threadID]; ok {
			if p, ok := recipients[rid]; ok && (p.Name != "" || len(p.Phones) > 0) {
				from = &p
			}
		}
		rec := signalJSONLLine{
			Type:      "message",
			ID:        strconv.FormatInt(id, 10),
			Body:      body,
			SentAt:    sent,
			Direction: direction,
			FromMe:    fromMe,
			ThreadID:  strconv.FormatInt(threadID, 10),
			Read:      true,
		}
		if from != nil {
			rec.From = from
		}
		if err := storeSignalRecord(rec, res, batch); err != nil {
			res.Errors++
		}
	}
	return mrows.Err()
}

func androidMessageDirection(typ int64) (direction string, fromMe bool) {
	// Signal/Android message type: bit0 inbox, bit1 sent (plus status flags).
	const baseInbox, baseSent = 1, 2
	if typ&baseSent != 0 {
		return "outbound", true
	}
	if typ&baseInbox != 0 {
		return "inbound", false
	}
	return "unknown", false
}

func signalMillisRFC3339(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

func nonEmpty(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return []string{s}
}

// --- Connector ---

// SignalConnector lists imported Signal contacts/messages from the local store.
type SignalConnector struct {
	store *SignalStore
}

// NewSignalConnector wraps the local Signal import store.
func NewSignalConnector(store *SignalStore) *SignalConnector {
	return &SignalConnector{store: store}
}

func (c *SignalConnector) Capabilities() types.ServiceCapabilities {
	return types.ServiceCapabilities{
		DataTypes: []types.DataTypeCapability{
			{
				Name:        "contacts",
				Description: "Imported Signal contacts (names and phones from a local export)",
				Operations: []types.Operation{
					{
						Name:        "list_contacts",
						Description: "Imported Signal contacts (permission key: contacts)",
						Method:      "GET",
						Path:        "/v1/library/contacts",
						Response: types.ResponseSpec{
							Description: "Neutral contacts with sources[]",
							Fields:      []string{"contacts", "total"},
						},
					},
				},
			},
			{
				Name:        "messages",
				Description: "Imported Signal chat messages (text metadata only; attachments are never stored)",
				Operations: []types.Operation{
					{
						Name:        "list_messages",
						Description: "Imported Signal messages (permission key: messages)",
						Method:      "GET",
						Path:        "/v1/library/messages",
						Parameters: []types.Parameter{
							{Name: "after", In: "query", Type: "string", Description: "On/after sent time (RFC3339 or YYYY-MM-DD)"},
							{Name: "before", In: "query", Type: "string", Description: "Before sent time"},
						},
						Response: types.ResponseSpec{
							Description: "Neutral messages with sources[]",
							Fields:      []string{"messages", "total"},
						},
					},
					{
						Name:        "import_status",
						Description: "Count of imported Signal contacts and messages",
						Method:      "GET",
						Path:        "/import/status",
						Response: types.ResponseSpec{
							Description: "Imported row counts",
							Fields:      []string{"contacts", "messages"},
						},
					},
				},
			},
		},
	}
}

func (c *SignalConnector) SystemInstructions(baseURL string) string {
	root := strings.TrimRight(baseURL, "/")
	return fmt.Sprintf(`When the user's request involves Signal chats or Signal contacts, use Aegis. Imported Signal history is available through GET %s/v1/library/messages and GET %s/v1/library/contacts (source service "signal"). Attachments and media are never stored — text metadata only. Signal has no live API here: if history is missing, ask the operator to import an export (Advanced → Signal import).
`, root, root)
}

func (c *SignalConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	store := c.store
	if store == nil || !store.Enabled() {
		WriteJSONError(w, http.StatusNotFound, "signal store disabled")
		return
	}
	path := strings.Trim(remainingPath, "/")
	if path == "" {
		path = strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/services/"+SignalServiceID), "/")
	}
	switch {
	case r.Method == http.MethodGet && (path == "import/status"):
		contacts, messages, err := store.Counts()
		if err != nil {
			WriteJSONError(w, http.StatusInternalServerError, "signal store error")
			return
		}
		WriteJSON(w, map[string]any{"contacts": contacts, "messages": messages, "path": store.Path()})
	case r.Method == http.MethodGet && (path == "messages" || path == "messages/"):
		limit := LibraryItemCap
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		list, err := store.ListMessages(limit)
		if err != nil {
			WriteJSONError(w, http.StatusInternalServerError, "signal store error")
			return
		}
		WriteJSON(w, types.MessageList{Messages: list, LibraryListMeta: types.LibraryListMeta{Total: len(list), Synced: true}})
	case r.Method == http.MethodGet && (path == "contacts" || path == "contacts/" || path == "me/contacts"):
		limit := LibraryItemCap
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		list, err := store.ListContacts(limit)
		if err != nil {
			WriteJSONError(w, http.StatusInternalServerError, "signal store error")
			return
		}
		WriteJSON(w, types.ContactList{Contacts: list, LibraryListMeta: types.LibraryListMeta{Total: len(list), Synced: true}})
	case r.Method == http.MethodPost && path == "import":
		var req struct {
			Path string `json:"path"`
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err := json.Unmarshal(body, &req); err != nil || strings.TrimSpace(req.Path) == "" {
			WriteJSONError(w, http.StatusBadRequest, "path is required")
			return
		}
		res, err := ImportSignalPath(store, req.Path)
		if err != nil {
			WriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		WriteJSON(w, res)
	default:
		WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ListUserContacts returns imported Signal contacts as a ContactProvider.
func (c *SignalConnector) ListUserContacts(ctx context.Context) (contacts []types.Contact, truncated bool, err error) {
	store := c.store
	if store == nil || !store.Enabled() {
		return nil, false, types.ErrUnauthorized
	}
	list, err := store.ListContacts(LibraryItemCap)
	if err != nil {
		return nil, false, err
	}
	if contactsN, _, cerr := store.Counts(); cerr == nil && contactsN > len(list) {
		truncated = true
	}
	return list, truncated, nil
}

// ListMessages returns imported Signal messages as a MessageProvider.
func (c *SignalConnector) ListMessages(ctx context.Context) (messages []types.Message, truncated bool, err error) {
	store := c.store
	if store == nil || !store.Enabled() {
		return nil, false, types.ErrUnauthorized
	}
	list, err := store.ListMessages(LibraryItemCap)
	if err != nil {
		return nil, false, err
	}
	if _, msgN, cerr := store.Counts(); cerr == nil && msgN > len(list) {
		truncated = true
	}
	// Mark canonical: the Signal export is the historical home for these rows.
	for i := range list {
		for j := range list[i].Sources {
			list[i].Sources[j].Canonical = true
		}
	}
	return list, truncated, nil
}

var (
	_ ServiceConnector = (*SignalConnector)(nil)
	_ types.MessageProvider       = (*SignalConnector)(nil)
	_ types.ContactProvider       = (*SignalConnector)(nil)
)

