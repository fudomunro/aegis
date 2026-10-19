package core

import (
	"github.com/fudomunro/aegis/internal/connectors"

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

	"github.com/fudomunro/aegis/internal/types"
	_ "modernc.org/sqlite"
)

// Quicken local import (issue #141). QIF and QFX/OFX exports are parsed into
// a local query store and served as a TransactionProvider so agents can ask
// money questions that span live YNAB data and historical Quicken files.
// Aegis does not store statements or PDFs — only line-item metadata.

const (
	quickenServiceID   = "quicken"
	quickenServiceName = "Quicken"
)

// QuickenStore is a local SQLite store of imported Quicken transactions.
// Empty path disables the store (like mail-archive).
type QuickenStore struct {
	db   *sql.DB
	path string
	mu   sync.RWMutex
}

// QuickenImportResult reports one import run.
type QuickenImportResult struct {
	Path    string `json:"path"`
	Scanned int    `json:"scanned"`
	Stored  int    `json:"stored"`
	Skipped int    `json:"skipped"`
	Errors  int    `json:"errors"`
	Done    bool   `json:"done"`
	Error   string `json:"error,omitempty"`
}

// OpenQuickenStore opens (or creates) the local Quicken SQLite store.
// An empty path returns nil (disabled).
func OpenQuickenStore(path string) (*QuickenStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create quicken store dir: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// WAL lets import and live list share the file.
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		return nil, err
	}
	s := &QuickenStore{db: db, path: path}
	if err := s.init(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *QuickenStore) init() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS quicken_transactions (
  source_id TEXT PRIMARY KEY,
  file_path TEXT NOT NULL,
  posted_at TEXT,
  authorized_at TEXT,
  amount TEXT NOT NULL,
  currency TEXT,
  description TEXT,
  merchant TEXT,
  category TEXT,
  memo TEXT,
  account_name TEXT,
  account_type TEXT,
  account_last4 TEXT,
  status TEXT,
  fitid TEXT,
  raw_json TEXT
);
CREATE INDEX IF NOT EXISTS quicken_tx_posted ON quicken_transactions(posted_at);
CREATE INDEX IF NOT EXISTS quicken_tx_merchant ON quicken_transactions(merchant);
CREATE INDEX IF NOT EXISTS quicken_tx_amount ON quicken_transactions(amount);
`)
	return err
}

func (s *QuickenStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *QuickenStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *QuickenStore) Enabled() bool { return s != nil && s.db != nil }

// Upsert stores or replaces one imported transaction keyed by source_id.
func (s *QuickenStore) Upsert(filePath string, tx types.Transaction) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("quicken store disabled")
	}
	id := transactionSourceID(tx)
	if id == "" {
		return fmt.Errorf("quicken transaction missing source id")
	}
	raw, err := json.Marshal(tx)
	if err != nil {
		return err
	}
	accName, accType, accLast4 := "", "", ""
	if tx.Account != nil {
		accName, accType, accLast4 = tx.Account.Name, tx.Account.Type, tx.Account.Last4
	}
	category := strings.Join(tx.Category, " | ")
	fitid := ""
	if len(tx.Sources) > 0 {
		fitid = messageExtraString(tx.Sources[0].Extra, "fitid")
		if fitid == "" {
			fitid = tx.EntityID
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.Exec(`
INSERT INTO quicken_transactions(
  source_id, file_path, posted_at, authorized_at, amount, currency,
  description, merchant, category, memo,
  account_name, account_type, account_last4, status, fitid, raw_json
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(source_id) DO UPDATE SET
  file_path=excluded.file_path,
  posted_at=excluded.posted_at,
  authorized_at=excluded.authorized_at,
  amount=excluded.amount,
  currency=excluded.currency,
  description=excluded.description,
  merchant=excluded.merchant,
  category=excluded.category,
  memo=excluded.memo,
  account_name=excluded.account_name,
  account_type=excluded.account_type,
  account_last4=excluded.account_last4,
  status=excluded.status,
  fitid=excluded.fitid,
  raw_json=excluded.raw_json
`,
		id, filePath, tx.PostedAt, tx.AuthorizedAt, tx.Amount, tx.Currency,
		tx.Description, tx.Merchant, category, tx.Memo,
		accName, accType, accLast4, tx.Status, fitid, string(raw),
	)
	return err
}

// Has reports whether source_id is already stored.
func (s *QuickenStore) Has(sourceID string) bool {
	if s == nil || s.db == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var n int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM quicken_transactions WHERE source_id = ?`, sourceID).Scan(&n)
	return err == nil && n > 0
}

// List returns stored transactions (newest first), optional filters.
func (s *QuickenStore) List(f TransactionFilters, limit int) ([]types.Transaction, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("quicken store disabled")
	}
	if limit <= 0 || limit > libraryItemCap {
		limit = libraryItemCap
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`
SELECT raw_json FROM quicken_transactions
ORDER BY posted_at DESC, source_id ASC
LIMIT ?`, limit*4) // over-fetch then filter
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.Transaction
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var tx types.Transaction
		if err := json.Unmarshal([]byte(raw), &tx); err != nil {
			continue
		}
		if !f.matches(tx) {
			continue
		}
		out = append(out, tx)
		if len(out) >= limit {
			break
		}
	}
	return out, rows.Err()
}

// Count returns stored row count.
func (s *QuickenStore) Count() (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("quicken store disabled")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var n int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM quicken_transactions`).Scan(&n)
	return n, err
}

func transactionSourceID(tx types.Transaction) string {
	if id := strings.TrimSpace(tx.EntityID); id != "" {
		return id
	}
	for _, src := range tx.Sources {
		if strings.TrimSpace(src.ID) != "" {
			return strings.TrimSpace(src.ID)
		}
	}
	// Deterministic fallback so re-imports of the same line stay stable.
	return "hash:" + transactionIdentityKey(tx)
}

// --- File parsers (QIF + QFX/OFX) ---

// ParseQuickenFile detects QIF vs OFX/QFX and returns neutral transactions.
func ParseQuickenFile(path string) ([]types.Transaction, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseQuickenBytes(path, data)
}

// ParseQuickenBytes parses QIF or OFX/QFX content.
func ParseQuickenBytes(name string, data []byte) ([]types.Transaction, error) {
	head := strings.ToUpper(string(data[:min(512, len(data))]))
	switch {
	case strings.Contains(head, "OFXHEADER") || strings.Contains(head, "<OFX") || strings.Contains(head, "OFXSGML"):
		return ParseOFX(name, data)
	case strings.Contains(string(data[:min(256, len(data))]), "!Type:") || strings.HasPrefix(strings.TrimSpace(string(data)), "!"):
		return ParseQIF(name, data)
	default:
		// QIF without a type header is still common.
		if strings.Contains(string(data), "\nD") && strings.Contains(string(data), "\nT") {
			return ParseQIF(name, data)
		}
		return nil, fmt.Errorf("unrecognized Quicken file format: %s", filepath.Base(name))
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ParseQIF parses Quicken Interchange Format bank/CCard transactions.
func ParseQIF(name string, data []byte) ([]types.Transaction, error) {
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var out []types.Transaction
	var cur map[string]string
	flush := func() {
		if cur == nil {
			return
		}
		tx, ok := qifRecordToTransaction(name, cur)
		if ok {
			out = append(out, tx)
		}
		cur = nil
	}
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			continue
		}
		if line == "^" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "!") {
			// Type header — ignore; bank/ccard both produce the same fields.
			continue
		}
		if len(line) < 1 {
			continue
		}
		field := line[0]
		value := line[1:]
		if cur == nil {
			cur = map[string]string{}
		}
		switch field {
		case 'D', 'T', 'P', 'L', 'M', 'N', 'C', 'A', 'U', 'F', '$', 'Y':
			cur[string(field)] = value
		default:
			// Continuation lines for address (A) or memo (M) are rare; append.
			if prev, ok := cur["M"]; ok && field != 'D' && field != 'T' {
				cur["M"] = prev + " " + line
			}
		}
	}
	flush()
	if err := sc.Err(); err != nil {
		return out, err
	}
	return out, nil
}

func qifRecordToTransaction(name string, rec map[string]string) (types.Transaction, bool) {
	date := strings.TrimSpace(rec["D"])
	amountRaw := strings.TrimSpace(rec["T"])
	if amountRaw == "" {
		amountRaw = strings.TrimSpace(rec["$"])
	}
	if date == "" || amountRaw == "" {
		return types.Transaction{}, false
	}
	payee := strings.TrimSpace(rec["P"])
	category := strings.TrimSpace(rec["L"])
	var categories []string
	if category != "" {
		categories = []string{category}
	}
	memo := strings.TrimSpace(rec["M"])
	status := types.TransactionStatusPosted
	if strings.Contains(strings.ToUpper(rec["C"]), "N") || strings.Contains(strings.ToUpper(rec["C"]), "U") || strings.Contains(strings.ToUpper(rec["C"]), "?") {
		status = types.TransactionStatusPending
	}
	fitid := strings.TrimSpace(rec["N"])
	if fitid == "" {
		// Stable per-file identity when Quicken omitted a check number.
		fitid = "qif:" + shortHash(name+"|"+date+"|"+amountRaw+"|"+payee+"|"+memo)
	}
	amount := normalizeTransactionAmount(amountRaw)
	posted := types.NormalizeTransactionDate(qifDate(date))
	accountName := filepath.Base(name)
	accountName = strings.TrimSuffix(accountName, filepath.Ext(accountName))
	tx := types.Transaction{
		PostedAt:    posted,
		Amount:      amount,
		Currency:    "USD",
		Description: payee,
		Merchant:    payee,
		Category:    categories,
		Memo:        memo,
		Account:     &types.TransactionAccount{Name: accountName, Type: "bank"},
		Status:      status,
		EntityID:    fitid,
	}
	return types.NewTransaction(quickenServiceID, quickenServiceName, fitid, tx), true
}

// qifDate normalizes Quicken date forms (MM/DD/YYYY, M/D/YY, DD/MM/YYYY with
// optional time).
func qifDate(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// Split off optional time.
	datePart := raw
	if i := strings.IndexAny(raw, " "); i > 0 {
		datePart = raw[:i]
	}
	for _, layout := range []string{"01/02/2006", "1/2/2006", "01-02-2006", "2006-01-02", "2006/01/02"} {
		if t, err := time.Parse(layout, datePart); err == nil {
			return t.UTC().Format("2006-01-02")
		}
	}
	// Two-digit year: 03/05/24
	for _, layout := range []string{"01/02/06", "1/2/06"} {
		if t, err := time.Parse(layout, datePart); err == nil {
			return t.UTC().Format("2006-01-02")
		}
	}
	return types.NormalizeTransactionDate(raw)
}

// ParseOFX parses OFX/QFX SGML or XML bank/CC statements.
func ParseOFX(name string, data []byte) ([]types.Transaction, error) {
	text := string(data)
	// Prefer XML-shaped STMTTRN blocks; fall back to SGML tag scan.
	var out []types.Transaction
	out = append(out, parseOFXStmtTrn(name, text)...)
	if len(out) == 0 {
		out = append(out, parseOFXSGML(name, text)...)
	}
	return out, nil
}

// parseOFXStmtTrn extracts <STMTTRN>…</STMTTRN> blocks (XML-ish).
func parseOFXStmtTrn(name, text string) []types.Transaction {
	var out []types.Transaction
	upper := strings.ToUpper(text)
	const open = "<STMTTRN>"
	const closeTag = "</STMTTRN>"
	for {
		i := strings.Index(upper, open)
		if i < 0 {
			break
		}
		rest := upper[i:]
		j := strings.Index(rest, closeTag)
		if j < 0 {
			// SGML form without closing tag — handled below.
			break
		}
		block := text[i : i+j]
		if tx, ok := ofxFieldsToTransaction(name, ofxParseTags(block)); ok {
			out = append(out, tx)
		}
		upper = upper[i+j+len(closeTag):]
		text = text[i+j+len(closeTag):]
	}
	return out
}

// parseOFXSGML walks SGML tags without requiring closing STMTTRN.
func parseOFXSGML(name, text string) []types.Transaction {
	return parseOFXRecords(name, text)
}

type ofxTag struct {
	name  string
	value string
}

func splitOFXTags(text string) []ofxTag {
	var tags []ofxTag
	i := 0
	for i < len(text) {
		lt := strings.IndexByte(text[i:], '<')
		if lt < 0 {
			break
		}
		start := i + lt
		gt := strings.IndexByte(text[start:], '>')
		if gt < 0 {
			break
		}
		end := start + gt
		body := text[start+1 : end]
		i = end + 1
		if strings.HasPrefix(body, "/") || strings.HasPrefix(body, "!") || strings.HasPrefix(body, "?") {
			continue
		}
		// SGML: <TAG>value\n
		name := body
		if j := strings.IndexAny(body, " \t"); j >= 0 {
			name = body[:j]
		}
		// Value runs until the next '<' or newline (SGML).
		valEnd := strings.IndexAny(text[i:], "<\r\n")
		value := ""
		if valEnd > 0 {
			value = strings.TrimSpace(text[i : i+valEnd])
			i += valEnd
		} else if valEnd == 0 {
			value = ""
		} else if i < len(text) {
			value = strings.TrimSpace(text[i:])
			i = len(text)
		}
		tags = append(tags, ofxTag{name: name, value: value})
	}
	return tags
}

// parseOFXRecords walks tags and emits one transaction per FITID record.
func parseOFXRecords(name, text string) []types.Transaction {
	tags := splitOFXTags(text)
	var out []types.Transaction
	cur := map[string]string{}
	inTrn := false
	flush := func() {
		if len(cur) == 0 {
			return
		}
		if tx, ok := ofxFieldsToTransaction(name, cur); ok {
			out = append(out, tx)
		}
		cur = map[string]string{}
	}
	for _, t := range tags {
		n := strings.ToUpper(t.name)
		switch n {
		case "STMTTRN":
			if inTrn {
				flush()
			}
			inTrn = true
			cur = map[string]string{}
		case "TRNTYPE", "DTPOSTED", "DTUSER", "TRNAMT", "FITID", "NAME", "MEMO", "CHECKNUM", "REFNUM", "PAYEEID":
			if inTrn || n == "TRNAMT" || n == "FITID" {
				cur[n] = t.value
			}
		case "STMTRS", "CCSTMTRS", "BANKTRANLIST":
			if inTrn {
				flush()
				inTrn = false
			}
		}
	}
	flush()
	return out
}

func ofxParseTags(block string) map[string]string {
	out := map[string]string{}
	for _, t := range splitOFXTags(block) {
		out[strings.ToUpper(t.name)] = t.value
	}
	return out
}

func ofxFieldsToTransaction(name string, fields map[string]string) (types.Transaction, bool) {
	amountRaw := strings.TrimSpace(fields["TRNAMT"])
	fitid := strings.TrimSpace(fields["FITID"])
	dateRaw := strings.TrimSpace(fields["DTPOSTED"])
	if dateRaw == "" {
		dateRaw = strings.TrimSpace(fields["DTUSER"])
	}
	if amountRaw == "" || dateRaw == "" {
		return types.Transaction{}, false
	}
	payee := strings.TrimSpace(fields["NAME"])
	if payee == "" {
		payee = strings.TrimSpace(fields["PAYEE"])
	}
	memo := strings.TrimSpace(fields["MEMO"])
	trntype := strings.ToUpper(strings.TrimSpace(fields["TRNTYPE"]))
	status := types.TransactionStatusPosted
	switch trntype {
	case "PENDING", "AUTH", "DIRECTDEBIT":
		// Keep posted unless OFX explicitly says pending elsewhere.
	}
	if fitid == "" {
		fitid = "ofx:" + shortHash(name+"|"+dateRaw+"|"+amountRaw+"|"+payee)
	}
	posted := types.NormalizeTransactionDate(dateRaw)
	if d := transactionDay(posted); d != "" {
		posted = d
	}
	accountName := filepath.Base(name)
	accountName = strings.TrimSuffix(accountName, filepath.Ext(accountName))
	var categories []string
	if cat := strings.TrimSpace(fields["CATEGORY"]); cat != "" {
		categories = []string{cat}
	}
	tx := types.Transaction{
		PostedAt:    posted,
		Amount:      normalizeTransactionAmount(amountRaw),
		Currency:    "USD",
		Description: payee,
		Merchant:    payee,
		Category:    categories,
		Memo:        memo,
		Account:     &types.TransactionAccount{Name: accountName, Type: "bank"},
		Status:      status,
		EntityID:    fitid,
	}
	src := types.NewTransaction(quickenServiceID, quickenServiceName, fitid, tx)
	if len(src.Sources) == 1 {
		src.Sources[0].Extra = map[string]any{"fitid": fitid, "trntype": trntype}
	}
	return src, true
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

// ImportQuickenPath imports a QIF/QFX file or a directory of them.
func ImportQuickenPath(store *QuickenStore, path string) (QuickenImportResult, error) {
	res := QuickenImportResult{Path: path}
	if store == nil || !store.Enabled() {
		return res, fmt.Errorf("quicken store disabled")
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
			case ".qif", ".qfx", ".ofx":
				files = append(files, filepath.Join(path, e.Name()))
			}
		}
	} else {
		files = []string{path}
	}
	for _, file := range files {
		txs, err := ParseQuickenFile(file)
		if err != nil {
			res.Errors++
			slog.Warn("quicken parse failed", "path", file, "error", err)
			continue
		}
		for _, tx := range txs {
			res.Scanned++
			id := transactionSourceID(tx)
			if id != "" && store.Has(id) {
				res.Skipped++
				continue
			}
			if err := store.Upsert(file, tx); err != nil {
				res.Errors++
				continue
			}
			res.Stored++
		}
	}
	res.Done = true
	return res, nil
}

// QuickenConnector lists imported Quicken transactions from the local store.
type QuickenConnector struct {
	host  connectors.Host
	store *QuickenStore
}

// NewQuickenConnector creates a connector wired to the given Host and local Quicken store.
func NewQuickenConnector(host connectors.Host, store *QuickenStore) *QuickenConnector {
	return &QuickenConnector{host: host, store: store}
}

// Capabilities returns Quicken import/list operations.
func (c *QuickenConnector) Capabilities() types.ServiceCapabilities {
	return types.ServiceCapabilities{
		DataTypes: []types.DataTypeCapability{
			{
				Name:        "transactions",
				Description: "Locally imported Quicken QIF/QFX transactions (line items only; statements/PDFs are never stored)",
				Operations: []types.Operation{
					{
						Name:        "list_transactions",
						Description: "List imported Quicken transactions (permission key: transactions)",
						Method:      "GET",
						Path:        "/transactions",
						Parameters: []types.Parameter{
							{Name: "after", In: "query", Type: "string", Description: "On/after posted date (YYYY-MM-DD)"},
							{Name: "before", In: "query", Type: "string", Description: "Before posted date (YYYY-MM-DD)"},
							{Name: "q", In: "query", Type: "string", Description: "Free-text on payee/category/memo"},
						},
						Response: types.ResponseSpec{
							Description: "Neutral transactions with sources[]",
							Fields:      []string{"transactions", "total"},
						},
					},
					{
						Name:        "import_status",
						Description: "Count of imported Quicken line items",
						Method:      "GET",
						Path:        "/import/status",
						Response: types.ResponseSpec{
							Description: "Imported row count",
							Fields:      []string{"count"},
						},
					},
				},
			},
		},
	}
}

// SystemInstructions returns the agent hint when Quicken is referenced.
func (c *QuickenConnector) SystemInstructions(baseURL string) string {
	root := strings.TrimRight(baseURL, "/")
	return fmt.Sprintf(`When the user's request involves Quicken files or historical bank exports, use Aegis. Imported QIF/QFX line items are available through GET %s/v1/library/transactions (source service "quicken") merged with live budget data. Statements and PDFs are never stored — metadata only. Ask the user to import files with the -import-quicken flag or admin import if the history is missing.
`, root)
}

// HandleProxy serves local Quicken store reads.
func (c *QuickenConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	store := c.store
	if store == nil || !store.Enabled() {
		writeJSONError(w, http.StatusNotFound, "quicken store disabled")
		return
	}
	switch {
	case r.Method == http.MethodGet && (remainingPath == "/import/status" || remainingPath == "import/status"):
		n, err := store.Count()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "quicken store error")
			return
		}
		writeJSON(w, map[string]any{"count": n, "path": store.Path()})
	case r.Method == http.MethodGet && (remainingPath == "/transactions" || remainingPath == "transactions" || remainingPath == "" || remainingPath == "/"):
		filters := ParseTransactionFilters(r)
		limit := libraryItemCap
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		list, err := store.List(filters, limit)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "quicken store error")
			return
		}
		writeJSON(w, types.TransactionList{Transactions: list, LibraryListMeta: types.LibraryListMeta{Total: len(list), Synced: true}})
	case r.Method == http.MethodPost && (remainingPath == "/import" || remainingPath == "import"):
		var req struct {
			Path string `json:"path"`
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err := json.Unmarshal(body, &req); err != nil || strings.TrimSpace(req.Path) == "" {
			writeJSONError(w, http.StatusBadRequest, "path is required")
			return
		}
		res, err := ImportQuickenPath(store, req.Path)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, res)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ListUserTransactions returns imported Quicken line items.
func (c *QuickenConnector) ListUserTransactions(ctx context.Context) (transactions []types.Transaction, truncated bool, err error) {
	store := c.store
	if store == nil || !store.Enabled() {
		return nil, false, nil
	}
	list, err := store.List(TransactionFilters{}, libraryItemCap)
	if err != nil {
		return nil, false, err
	}
	// Mark canonical: the imported file is the historical home until YNAB
	// (or another live source) supersedes it via merge.
	for i := range list {
		for j := range list[i].Sources {
			list[i].Sources[j].Canonical = true
		}
	}
	n, _ := store.Count()
	if n > len(list) {
		truncated = true
	}
	return list, truncated, nil
}

var (
	_ connectors.ServiceConnector = (*QuickenConnector)(nil)
	_ types.TransactionProvider   = (*QuickenConnector)(nil)
)
