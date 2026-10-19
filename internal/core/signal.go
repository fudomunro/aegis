package core

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/types"
	_ "modernc.org/sqlite"
)

// Signal local import (issue #168). Signal has no public message API, so
// contacts and chat messages arrive as a file-based export the operator
// prepares (Desktop/Android backup → plaintext SQLite, or a JSONL dump).
// Aegis stores neutral contact + message metadata only — never attachments
// or other media. The local store feeds /v1/library/contacts and
// /v1/library/messages like other file-based import sources.

const (
	signalServiceID   = "signal"
	signalServiceName = "Signal"
)

// SignalStore is a local SQLite store of imported Signal contacts/messages.
// Empty path disables the store (like mail-archive / quicken / timeline).
type SignalStore struct {
	db   *sql.DB
	path string
	mu   sync.RWMutex
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

// OpenSignalStore opens (or creates) the local Signal SQLite store.
// An empty path returns nil (disabled).
func OpenSignalStore(path string) (*SignalStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create signal store dir: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		return nil, err
	}
	s := &SignalStore{db: db, path: path}
	if err := s.init(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SignalStore) init() error {
	_, err := s.db.Exec(`
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
`)
	return err
}

func (s *SignalStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *SignalStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *SignalStore) Enabled() bool { return s != nil && s.db != nil }

// UpsertContact stores or replaces one imported contact keyed by source_id.
func (s *SignalStore) UpsertContact(filePath string, c types.Contact) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("signal store disabled")
	}
	id := signalContactSourceID(c)
	if id == "" {
		return fmt.Errorf("signal contact missing source id")
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.Exec(`
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

// UpsertMessage stores or replaces one imported message keyed by source_id.
func (s *SignalStore) UpsertMessage(filePath string, m types.Message) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("signal store disabled")
	}
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
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.Exec(`
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

// HasContact reports whether the contact source_id is already stored.
func (s *SignalStore) HasContact(sourceID string) bool {
	return s.hasRow(`SELECT COUNT(1) FROM signal_contacts WHERE source_id = ?`, sourceID)
}

// HasMessage reports whether the message source_id is already stored.
func (s *SignalStore) HasMessage(sourceID string) bool {
	return s.hasRow(`SELECT COUNT(1) FROM signal_messages WHERE source_id = ?`, sourceID)
}

func (s *SignalStore) hasRow(query, sourceID string) bool {
	if s == nil || s.db == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var n int
	err := s.db.QueryRow(query, sourceID).Scan(&n)
	return err == nil && n > 0
}

// ListContacts returns stored contacts (name order).
func (s *SignalStore) ListContacts(limit int) ([]types.Contact, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("signal store disabled")
	}
	if limit <= 0 || limit > libraryItemCap {
		limit = libraryItemCap
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
	if limit <= 0 || limit > libraryItemCap {
		limit = libraryItemCap
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
	c := types.NewContact(signalServiceID, signalServiceName, id, name, emails, phones)
	if c.Name == "" && len(c.Phones) == 0 && len(c.Emails) == 0 && len(handles) == 0 {
		return types.Contact{}
	}
	c.Handles = normalizeHandleList(handles)
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
		nc := types.NewContact(signalServiceID, signalServiceName, "", from.Name, from.Emails, from.Phones)
		if nc.Name != "" || len(nc.Phones) > 0 || len(nc.Emails) > 0 {
			from = &nc
		} else {
			from = nil
		}
	}
	to := make([]types.Contact, 0, len(rec.To))
	for _, t := range rec.To {
		nc := types.NewContact(signalServiceID, signalServiceName, "", t.Name, t.Emails, t.Phones)
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
		Service:     signalServiceID,
		ServiceName: signalServiceName,
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
	return types.NewContact(signalServiceID, signalServiceName, "", p.Name, p.Emails, p.Phones)
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
	ext := strings.ToLower(filepath.Ext(file))
	switch ext {
	case ".db", ".sqlite", ".sqlite3":
		if res.Format == "" {
			res.Format = "sqlite"
		} else if res.Format != "sqlite" {
			res.Format = "mixed"
		}
		return importSignalSQLite(store, file, res)
	case ".jsonl", ".ndjson":
		if res.Format == "" {
			res.Format = "jsonl"
		} else if res.Format != "jsonl" {
			res.Format = "mixed"
		}
		return importSignalJSONL(store, file, res)
	case ".json":
		if res.Format == "" {
			res.Format = "json"
		} else if res.Format != "json" {
			res.Format = "mixed"
		}
		return importSignalJSON(store, file, res)
	default:
		return fmt.Errorf("unsupported Signal export type %q", ext)
	}
}

func importSignalJSONL(store *SignalStore, file string, res *SignalImportResult) error {
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
		if err := storeSignalRecord(store, file, rec, res); err != nil {
			res.Errors++
			slog.Warn("signal jsonl store failed", "path", file, "line", lineNo, "error", err)
		}
	}
	return sc.Err()
}

func importSignalJSON(store *SignalStore, file string, res *SignalImportResult) error {
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
			if err := storeSignalRecord(store, file, rec, res); err != nil {
				res.Errors++
			}
		}
		for _, rec := range bundle.Messages {
			rec.Type = "message"
			if err := storeSignalRecord(store, file, rec, res); err != nil {
				res.Errors++
			}
		}
		return nil
	}
	// Array form: [{...},{...}]
	var lines []signalJSONLLine
	if err := json.Unmarshal(raw, &lines); err == nil && len(lines) > 0 {
		for _, rec := range lines {
			if err := storeSignalRecord(store, file, rec, res); err != nil {
				res.Errors++
			}
		}
		return nil
	}
	// Single object
	var one signalJSONLLine
	if err := json.Unmarshal(raw, &one); err == nil && (one.Type != "" || one.Body != "" || one.Name != "") {
		return storeSignalRecord(store, file, one, res)
	}
	return fmt.Errorf("unrecognized Signal JSON shape in %s", file)
}

func storeSignalRecord(store *SignalStore, file string, rec signalJSONLLine, res *SignalImportResult) error {
	switch strings.ToLower(strings.TrimSpace(rec.Type)) {
	case "contact", "recipient", "person":
		res.ContactsScanned++
		c := rec.toContact()
		id := signalContactSourceID(c)
		if id == "" {
			return fmt.Errorf("contact missing id/identity")
		}
		if store.HasContact(id) {
			res.ContactsSkipped++
			return nil
		}
		if err := store.UpsertContact(file, c); err != nil {
			return err
		}
		res.ContactsStored++
		return nil
	case "message", "chat", "sms":
		res.MessagesScanned++
		m := rec.toMessage()
		id := signalMessageSourceID(m)
		if id == "" {
			return fmt.Errorf("message missing id")
		}
		if store.HasMessage(id) {
			res.MessagesSkipped++
			return nil
		}
		if err := store.UpsertMessage(file, m); err != nil {
			return err
		}
		res.MessagesStored++
		return nil
	default:
		return fmt.Errorf("unknown Signal record type %q", rec.Type)
	}
}

// importSignalSQLite reads a plaintext Signal dump. It auto-detects the
// Signal Desktop schema (conversations/messages) or the Android /
// signalbackup-tools schema (recipient/message/thread). Encrypted app
// databases are not supported — decrypt or export first.
func importSignalSQLite(store *SignalStore, file string, res *SignalImportResult) error {
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
		return importSignalDesktopSQLite(db, store, file, res)
	case tables["message"] && tables["recipient"]:
		return importSignalAndroidSQLite(db, store, file, res)
	default:
		return fmt.Errorf("unrecognized Signal SQLite schema in %s (want Desktop conversations/messages or Android recipient/message)", file)
	}
}

func importSignalDesktopSQLite(db *sql.DB, store *SignalStore, file string, res *SignalImportResult) error {
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
		if err := storeSignalRecord(store, file, rec, res); err != nil {
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
		if err := storeSignalRecord(store, file, rec, res); err != nil {
			res.Errors++
		}
	}
	return mrows.Err()
}

func importSignalAndroidSQLite(db *sql.DB, store *SignalStore, file string, res *SignalImportResult) error {
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
		if err := storeSignalRecord(store, file, rec, res); err != nil {
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
		if err := storeSignalRecord(store, file, rec, res); err != nil {
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
		writeJSONError(w, http.StatusNotFound, "signal store disabled")
		return
	}
	path := strings.Trim(remainingPath, "/")
	if path == "" {
		path = strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/services/"+signalServiceID), "/")
	}
	switch {
	case r.Method == http.MethodGet && (path == "import/status"):
		contacts, messages, err := store.Counts()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "signal store error")
			return
		}
		writeJSON(w, map[string]any{"contacts": contacts, "messages": messages, "path": store.Path()})
	case r.Method == http.MethodGet && (path == "messages" || path == "messages/"):
		limit := libraryItemCap
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		list, err := store.ListMessages(limit)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "signal store error")
			return
		}
		writeJSON(w, types.MessageList{Messages: list, LibraryListMeta: types.LibraryListMeta{Total: len(list), Synced: true}})
	case r.Method == http.MethodGet && (path == "contacts" || path == "contacts/" || path == "me/contacts"):
		limit := libraryItemCap
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		list, err := store.ListContacts(limit)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "signal store error")
			return
		}
		writeJSON(w, types.ContactList{Contacts: list, LibraryListMeta: types.LibraryListMeta{Total: len(list), Synced: true}})
	case r.Method == http.MethodPost && path == "import":
		var req struct {
			Path string `json:"path"`
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err := json.Unmarshal(body, &req); err != nil || strings.TrimSpace(req.Path) == "" {
			writeJSONError(w, http.StatusBadRequest, "path is required")
			return
		}
		res, err := ImportSignalPath(store, req.Path)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, res)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ListUserContacts returns imported Signal contacts as a ContactProvider.
func (c *SignalConnector) ListUserContacts(ctx context.Context) (contacts []types.Contact, truncated bool, err error) {
	store := c.store
	if store == nil || !store.Enabled() {
		return nil, false, types.ErrUnauthorized
	}
	list, err := store.ListContacts(libraryItemCap)
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
	list, err := store.ListMessages(libraryItemCap)
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
	_ connectors.ServiceConnector = (*SignalConnector)(nil)
	_ types.MessageProvider       = (*SignalConnector)(nil)
	_ types.ContactProvider       = (*SignalConnector)(nil)
)

// --- Admin import status / run (issue #168) ---

// SignalImportStatus is GET /v1/admin/signal/status.
type SignalImportStatus struct {
	Enabled    bool     `json:"enabled"`
	Path       string   `json:"path,omitempty"`
	Contacts   int      `json:"contacts"`
	Messages   int      `json:"messages"`
	StoreEmpty bool     `json:"store_empty"`
	LibraryURL string   `json:"library_url,omitempty"`
	SamplePath string   `json:"sample_hint,omitempty"`
	Hints      []string `json:"hints,omitempty"`
}

// SignalImportRequest is POST /v1/admin/signal/import.
type SignalImportRequest struct {
	Path string `json:"path"`
}

// HandleAdminSignal serves /v1/admin/signal/*.
func (s *Server) HandleAdminSignal(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/admin/signal"), "/")
	switch {
	case path == "" || path == "status":
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s.signalStatus())
	case path == "import":
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.handleSignalImport(w, r)
	default:
		writeJSONError(w, http.StatusNotFound, "unknown admin endpoint")
	}
}

func (s *Server) signalStatus() SignalImportStatus {
	store := s.signal
	st := SignalImportStatus{
		Enabled:    store != nil && store.Enabled(),
		SamplePath: "~/Downloads/signal-export (JSONL, or plaintext .db from signalbackup-tools)",
	}
	if st.Enabled {
		st.Path = store.Path()
		if c, m, err := store.Counts(); err == nil {
			st.Contacts, st.Messages = c, m
		}
		st.StoreEmpty = st.Contacts == 0 && st.Messages == 0
	}
	root := strings.TrimRight(s.baseURL, "/")
	st.LibraryURL = root + "/v1/library/messages"
	if !st.Enabled {
		st.Hints = append(st.Hints, "Signal store is disabled — restart with -signal-store <path> (default under the data dir).")
	} else if st.StoreEmpty {
		st.Hints = append(st.Hints,
			"Signal has no public chat API. Export on the device, decrypt if needed, then import the files below.",
			"Android: Settings → Chats → Chat backups → Create backup, then decrypt with signalbackup-tools to a plaintext database.sqlite.",
			"Desktop: copy the Signal data directory's sql/db.sqlite after decrypting it (community exporters can dump JSONL).",
			"Aegis also accepts a JSONL export: one object per line with \"type\":\"contact\"|\"message\".",
			"Re-import is safe: rows already stored are skipped. Attachments are never stored.")
	} else {
		st.Hints = append(st.Hints,
			fmt.Sprintf("%d contact(s) and %d message(s) imported. Agents can query them via %s when contacts/messages permissions allow.",
				st.Contacts, st.Messages, st.LibraryURL))
	}
	return st
}

func (s *Server) handleSignalImport(w http.ResponseWriter, r *http.Request) {
	store := s.signal
	if store == nil || !store.Enabled() {
		writeJSONError(w, http.StatusBadRequest, "signal store disabled — set -signal-store")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		return
	}
	var req SignalImportRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Path = strings.TrimSpace(req.Path)
	if req.Path == "" {
		writeJSONError(w, http.StatusBadRequest, "path is required — a Signal export file or directory")
		return
	}
	res, err := ImportSignalPath(store, req.Path)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.cache.Clear()
	slog.Info("signal import from admin UI",
		"path", res.Path, "format", res.Format,
		"contacts_stored", res.ContactsStored, "messages_stored", res.MessagesStored,
		"contacts_skipped", res.ContactsSkipped, "messages_skipped", res.MessagesSkipped,
		"errors", res.Errors)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"result": res,
		"status": s.signalStatus(),
	})
}
