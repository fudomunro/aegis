package connectors

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

// LinkedIn local import (issue #218). LinkedIn has no public connections
// API, so 1st-degree connections arrive as a file-based export the operator
// prepares (LinkedIn "Get a copy of your data"). Aegis stores neutral
// contact metadata only — never profile photos or other media. The local
// store feeds /v1/library/contacts like other file-based import sources.

const (
	LinkedInServiceID   = "linkedin"
	LinkedInServiceName = "LinkedIn"
)

// LinkedInService is the service card the local LinkedIn import store
// injects into config (issue #266).
func LinkedInService() LocalStoreService {
	return LocalStoreService{
		ID:          LinkedInServiceID,
		Name:        LinkedInServiceName,
		Action:      "inject",
		Description: "Locally imported LinkedIn connections (Advanced → LinkedIn import).",
	}
}

// Admin declares the LinkedIn import admin surface (issue #254).
func (s *LinkedInStore) Admin() ImportAdmin {
	return ImportAdmin{
		FlagName:    "-linkedin-store",
		PathHint:    "a LinkedIn data export file or directory",
		SamplePath:  "~/Downloads/linkedin-export.zip (or Connections.csv extracted from it)",
		LibraryPath: "/v1/library/contacts",
		Counts: func() map[string]int {
			if s == nil || !s.Enabled() {
				return nil
			}
			c, err := s.Counts()
			if err != nil {
				return nil
			}
			return map[string]int{"contacts": c}
		},
		Hints: func(state ImportHintState) []string {
			contacts := state.Counts["contacts"]
			switch {
			case !state.Enabled:
				return []string{"LinkedIn store is disabled — restart with -linkedin-store <path> (default under the data dir)."}
			case contacts == 0:
				return []string{
					"LinkedIn has no public connections API. Export your data, then import the files below.",
					"LinkedIn → Settings & Privacy → Data Privacy → Get a copy of your data → Connections (or the full archive).",
					"Import the archive .zip, or Connections.csv / Contacts.csv extracted from it.",
					"Re-import is safe: rows already stored are skipped. Profile photos and other media are never stored.",
				}
			default:
				return []string{fmt.Sprintf("%d connection(s) imported. Agents can query them via %s when contacts permissions allow.",
					contacts, state.LibraryURL)}
			}
		},
		Import: func(path string) (ImportOutcome, error) {
			return ImportLinkedInPath(s, path)
		},
	}
}

// LinkedInStore is a local SQLite store of imported LinkedIn connections.
// Empty path disables the store (like mail-archive / quicken / timeline).
type LinkedInStore struct {
	localStore
}

// LinkedInImportResult reports one import run.
type LinkedInImportResult struct {
	Path    string `json:"path"`
	Format  string `json:"format,omitempty"`
	Scanned int    `json:"scanned"`
	Stored  int    `json:"stored"`
	Skipped int    `json:"skipped"`
	Errors  int    `json:"errors"`
	Done    bool   `json:"done"`
	Error   string `json:"error,omitempty"`
}

// LogAttrs returns slog keyvals for one finished import run.
func (r LinkedInImportResult) LogAttrs() []any {
	return []any{"format", r.Format,
		"stored", r.Stored, "skipped", r.Skipped, "errors", r.Errors, "done", r.Done}
}

// Failed reports a run that recorded errors but stored nothing.
func (r LinkedInImportResult) Failed() bool {
	return r.Errors > 0 && r.Stored == 0
}

var _ ImportStore = (*LinkedInStore)(nil)
var _ ImportOutcome = LinkedInImportResult{}

// OpenLinkedInStore opens (or creates) the local LinkedIn SQLite store.
// An empty path returns nil (disabled).
func OpenLinkedInStore(path string) (*LinkedInStore, error) {
	s := &LinkedInStore{}
	if err := s.openLocalStore(path, "linkedin", linkedinSchema); err != nil {
		return nil, err
	}
	if !s.Enabled() {
		return nil, nil
	}
	return s, nil
}

// linkedinSchema installs the imported connection table.
const linkedinSchema = `

CREATE TABLE IF NOT EXISTS linkedin_contacts (
  source_id TEXT PRIMARY KEY,
  file_path TEXT NOT NULL,
  name TEXT,
  emails TEXT,
  phones TEXT,
  handles TEXT,
  organization TEXT,
  raw_json TEXT
);
`

func (s *LinkedInStore) Enabled() bool { return s != nil && s.db != nil }

// upsertLinkedInContactTx stores or replaces one imported contact keyed by
// source_id.
func upsertLinkedInContactTx(tx *sql.Tx, filePath string, c kinds.Contact) error {
	id := linkedinContactSourceID(c)
	if id == "" {
		return fmt.Errorf("linkedin contact missing source id")
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`
INSERT INTO linkedin_contacts(
  source_id, file_path, name, emails, phones, handles, organization, raw_json
) VALUES (?,?,?,?,?,?,?,?)
ON CONFLICT(source_id) DO UPDATE SET
  file_path=excluded.file_path,
  name=excluded.name,
  emails=excluded.emails,
  phones=excluded.phones,
  handles=excluded.handles,
  organization=excluded.organization,
  raw_json=excluded.raw_json
`,
		id, filePath, c.Name,
		strings.Join(c.Emails, "\n"),
		strings.Join(c.Phones, "\n"),
		strings.Join(c.Handles, "\n"),
		c.Organization, string(raw),
	)
	return err
}

const linkedinContactHasQuery = `SELECT COUNT(1) FROM linkedin_contacts WHERE source_id = ?`

// HasContact reports whether the contact source_id is already stored.
func (s *LinkedInStore) HasContact(sourceID string) bool {
	if s == nil || s.db == nil {
		return false
	}
	return s.hasRow(linkedinContactHasQuery, sourceID)
}

// ListContacts returns stored connections (name order).
func (s *LinkedInStore) ListContacts(limit int) ([]kinds.Contact, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("linkedin store disabled")
	}
	if limit <= 0 || limit > LibraryItemCap {
		limit = LibraryItemCap
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`
SELECT raw_json FROM linkedin_contacts
ORDER BY name ASC, source_id ASC
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []kinds.Contact
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var c kinds.Contact
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			continue
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Counts returns the stored connection total.
func (s *LinkedInStore) Counts() (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("linkedin store disabled")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var n int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM linkedin_contacts`).Scan(&n)
	return n, err
}

func linkedinContactSourceID(c kinds.Contact) string {
	if len(c.Sources) > 0 {
		if id := strings.TrimSpace(c.Sources[0].ID); id != "" {
			return id
		}
	}
	if k := kinds.PrimaryContactKey(c); k != "" {
		return k
	}
	return ""
}

// linkedinPublicID returns the profile public identifier from a LinkedIn
// profile URL ("janedoe" in https://www.linkedin.com/in/janedoe), or ""
// when the value is not a profile URL.
func linkedinPublicID(raw string) string {
	s := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if s == "" {
		return ""
	}
	const marker = "linkedin.com/in/"
	i := strings.Index(strings.ToLower(s), marker)
	if i < 0 {
		return ""
	}
	id := s[i+len(marker):]
	if j := strings.IndexAny(id, "/?#"); j >= 0 {
		id = id[:j]
	}
	id = strings.TrimSpace(id)
	return strings.ToLower(id)
}

// linkedinRow is one recognized CSV record before neutral mapping.
type linkedinRow struct {
	first, middle, last, name string
	emails, phones, urls      []string
	company, position         string
	connected                 string
}

func (r linkedinRow) displayName() string {
	if n := strings.TrimSpace(r.name); n != "" {
		return n
	}
	var parts []string
	for _, p := range []string{r.first, r.middle, r.last} {
		if t := strings.TrimSpace(p); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, " ")
}

func (r linkedinRow) sourceID() string {
	for _, u := range r.urls {
		if id := linkedinPublicID(u); id != "" {
			return id
		}
	}
	base := strings.ToLower(strings.Join([]string{r.displayName(), strings.Join(r.emails, ","), strings.TrimSpace(r.company)}, "\x00"))
	return "li-" + shortHash(base)
}

// newLinkedInContact maps one export row onto the neutral Contact shape:
// name → Name, email → Emails, profile public id → Handles, company →
// Organization; profile URL, position, and connection date stay
// service-specific on sources[].extra. Rows with no name, email, or phone
// are dropped.
func newLinkedInContact(r linkedinRow) kinds.Contact {
	name := r.displayName()
	emails := kinds.NormalizeEmailList(r.emails)
	phones := kinds.NormalizePhoneList(r.phones)
	if name == "" && len(emails) == 0 && len(phones) == 0 {
		return kinds.Contact{}
	}
	var handles []string
	profileURL := ""
	for _, u := range r.urls {
		u = strings.TrimSpace(u)
		if id := linkedinPublicID(u); id != "" && profileURL == "" {
			profileURL = u
			handles = append(handles, id)
		}
	}
	c := kinds.NewContact(LinkedInServiceID, LinkedInServiceName, r.sourceID(), name, emails, phones)
	if len(c.Sources) != 1 {
		return kinds.Contact{}
	}
	c.Handles = kinds.NormalizeHandleList(handles)
	if org := strings.TrimSpace(r.company); org != "" {
		c.Organization = org
	}
	extra := map[string]any{}
	if profileURL != "" {
		extra["url"] = profileURL
	}
	if p := strings.TrimSpace(r.position); p != "" {
		extra["position"] = p
	}
	if cn := strings.TrimSpace(r.connected); cn != "" {
		extra["connected_on"] = cn
	}
	if len(extra) > 0 {
		c.Sources[0].Extra = extra
	}
	return c
}

// linkedinColumns indexes the recognized header columns of a LinkedIn
// export CSV. Repeating columns (email/phone/url) collect every match so
// Contacts.csv-style "Email 1-Value, Email 2-Value" all land.
type linkedinColumns struct {
	first, middle, last, name int
	emails, phones, urls      []int
	company, position         int
	connected                 int
}

const linkedinColAbsent = -1

func newLinkedInColumns() linkedinColumns {
	return linkedinColumns{
		first: linkedinColAbsent, middle: linkedinColAbsent,
		last: linkedinColAbsent, name: linkedinColAbsent,
		company: linkedinColAbsent, position: linkedinColAbsent,
		connected: linkedinColAbsent,
	}
}

// classifyLinkedInHeader maps normalized header cells onto the row shape.
// Recognized names are the documented Connections.csv columns plus the
// richer Contacts.csv equivalents; unknown columns are ignored.
func classifyLinkedInHeader(cells []string) (linkedinColumns, int) {
	cols := newLinkedInColumns()
	recognized := 0
	for i, raw := range cells {
		switch norm := normalizeLinkedInHeader(raw); norm {
		case "first name", "first":
			cols.first, recognized = i, recognized+1
		case "middle name", "middle":
			cols.middle, recognized = i, recognized+1
		case "last name", "last":
			cols.last, recognized = i, recognized+1
		case "name", "full name":
			cols.name, recognized = i, recognized+1
		case "email address", "email", "e-mail address", "e-mail",
			"email 1-value", "e-mail 1-value", "email 2-value", "e-mail 2-value":
			cols.emails = append(cols.emails, i)
			recognized++
		case "phone number", "phone", "mobile phone", "phone 1-value", "phone 2-value":
			cols.phones = append(cols.phones, i)
			recognized++
		case "url", "profile url", "public profile url", "linkedin profile url":
			cols.urls = append(cols.urls, i)
			recognized++
		case "company", "company name", "organization", "organisation":
			cols.company, recognized = i, recognized+1
		case "position", "title", "job title", "headline":
			cols.position, recognized = i, recognized+1
		case "connected on", "connected on date", "connected date":
			cols.connected, recognized = i, recognized+1
		}
	}
	return cols, recognized
}

func normalizeLinkedInHeader(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.TrimSuffix(s, ":")
	s = strings.Trim(s, `"`)
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

// ParseLinkedInCSV parses one LinkedIn export CSV (Connections.csv,
// Contacts.csv, or any CSV carrying their columns) into neutral contacts.
// The export may carry a prose preamble before the header row, so the
// parser skips records until it recognizes one.
func ParseLinkedInCSV(name string, data []byte) ([]kinds.Contact, error) {
	r := csv.NewReader(strings.NewReader(string(data)))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	var cols linkedinColumns
	seenHeader := false
	var out []kinds.Contact
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return out, fmt.Errorf("parse %s: %w", name, err)
		}
		if !seenHeader {
			var recognized int
			cols, recognized = classifyLinkedInHeader(rec)
			if recognized >= 2 {
				seenHeader = true
			}
			continue
		}
		c := newLinkedInContact(linkedinRowFromRecord(cols, rec))
		if c.Name == "" && len(c.Emails) == 0 && len(c.Phones) == 0 && len(c.Handles) == 0 {
			continue
		}
		out = append(out, c)
	}
	if !seenHeader {
		return nil, fmt.Errorf("no LinkedIn connections header row in %s", name)
	}
	return out, nil
}

func linkedinRowFromRecord(cols linkedinColumns, rec []string) linkedinRow {
	cell := func(i int) string {
		if i == linkedinColAbsent || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}
	row := linkedinRow{
		first:     cell(cols.first),
		middle:    cell(cols.middle),
		last:      cell(cols.last),
		name:      cell(cols.name),
		company:   cell(cols.company),
		position:  cell(cols.position),
		connected: cell(cols.connected),
	}
	for _, i := range cols.emails {
		if v := cell(i); v != "" {
			row.emails = append(row.emails, v)
		}
	}
	for _, i := range cols.phones {
		if v := cell(i); v != "" {
			row.phones = append(row.phones, v)
		}
	}
	for _, i := range cols.urls {
		if v := cell(i); v != "" {
			row.urls = append(row.urls, v)
		}
	}
	return row
}

// ImportLinkedInPath imports a LinkedIn data export: a .zip archive ("Get
// a copy of your data"), a Connections.csv / Contacts.csv, or a directory
// of such CSVs (non-recursive). Re-import is safe: rows already stored are
// skipped.
func ImportLinkedInPath(store *LinkedInStore, path string) (LinkedInImportResult, error) {
	res := LinkedInImportResult{Path: path}
	if store == nil || !store.Enabled() {
		return res, fmt.Errorf("linkedin store disabled")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return res, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return res, err
	}
	var files []string
	switch {
	case st.IsDir():
		entries, err := os.ReadDir(abs)
		if err != nil {
			return res, err
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if strings.EqualFold(filepath.Ext(e.Name()), ".csv") {
				files = append(files, filepath.Join(abs, e.Name()))
			}
		}
		if len(files) == 0 {
			return res, fmt.Errorf("no LinkedIn export CSVs in %s", path)
		}
	case strings.EqualFold(filepath.Ext(abs), ".zip"):
		return importLinkedInZip(store, abs)
	case strings.EqualFold(filepath.Ext(abs), ".csv"):
		files = []string{abs}
	default:
		return res, fmt.Errorf("not a LinkedIn export file or directory: %s", path)
	}
	for _, f := range files {
		if err := importLinkedInCSVFile(store, f, &res); err != nil {
			res.Errors++
			slog.Warn("linkedin import file failed", "path", f, "error", err)
		}
	}
	res.Done = true
	return res, nil
}

func importLinkedInCSVFile(store *LinkedInStore, file string, res *LinkedInImportResult) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	if res.Format == "" {
		res.Format = "csv"
	} else if res.Format != "csv" {
		res.Format = "mixed"
	}
	return storeLinkedInCSV(store, file, data, res)
}

// importLinkedInZip walks a LinkedIn data-export archive for the
// connection CSVs (Relationships/Connections.csv and Contacts.csv).
func importLinkedInZip(store *LinkedInStore, zipPath string) (LinkedInImportResult, error) {
	res := LinkedInImportResult{Path: zipPath, Format: "zip", Done: true}
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return res, err
	}
	defer zr.Close()
	for _, ent := range zr.File {
		if ent.FileInfo().IsDir() || !isLinkedInExportCSVName(ent.Name) {
			continue
		}
		rc, err := ent.Open()
		if err != nil {
			res.Errors++
			continue
		}
		data, err := io.ReadAll(io.LimitReader(rc, 32<<20))
		rc.Close()
		if err != nil {
			res.Errors++
			continue
		}
		storeLinkedInCSV(store, ent.Name, data, &res)
	}
	return res, nil
}

// isLinkedInExportCSVName reports whether a zip entry is one of the
// documented connection CSVs of a LinkedIn data export.
func isLinkedInExportCSVName(name string) bool {
	base := strings.ToLower(filepath.Base(name))
	return base == "connections.csv" || base == "contacts.csv"
}

func storeLinkedInCSV(store *LinkedInStore, file string, data []byte, res *LinkedInImportResult) error {
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	contacts, err := ParseLinkedInCSV(file, data)
	if err != nil {
		return err
	}
	var st ImportStats
	upsertRows(&store.localStore, &st, contacts, linkedinContactSourceID, linkedinContactHasQuery,
		func(tx *sql.Tx, c kinds.Contact) error { return upsertLinkedInContactTx(tx, file, c) })
	res.Scanned += st.Scanned
	res.Stored += st.Stored
	res.Skipped += st.Skipped
	res.Errors += st.Errors
	return nil
}

// --- Connector ---

// LinkedInConnector lists imported LinkedIn connections from the local store.
type LinkedInConnector struct {
	store *LinkedInStore
}

// NewLinkedInConnector wraps the local LinkedIn import store.
func NewLinkedInConnector(store *LinkedInStore) *LinkedInConnector {
	return &LinkedInConnector{store: store}
}

func (c *LinkedInConnector) Capabilities() shared.ServiceCapabilities {
	return shared.ServiceCapabilities{
		Kinds: []shared.KindCapability{
			{
				Name:        "contacts",
				Description: "Imported LinkedIn connections (names, emails, companies from a local export)",
				Operations: []shared.Operation{
					{
						Name:        "list_contacts",
						Description: "Imported LinkedIn connections (permission key: contacts)",
						Method:      "GET",
						Path:        "/v1/library/contacts",
						Response: shared.ResponseSpec{
							Description: "Neutral contacts with sources[]",
							Fields:      []string{"contacts", "total"},
						},
					},
					{
						Name:        "import_status",
						Description: "Count of imported LinkedIn connections",
						Method:      "GET",
						Path:        "/import/status",
						Response: shared.ResponseSpec{
							Description: "Imported row counts",
							Fields:      []string{"contacts"},
						},
					},
				},
			},
		},
	}
}

func (c *LinkedInConnector) SystemInstructions(baseURL string) string {
	root := strings.TrimRight(baseURL, "/")
	return fmt.Sprintf(`When the user's request involves LinkedIn connections or professional contacts, use Aegis. Imported LinkedIn connections are available through GET %s/v1/library/contacts (source service "linkedin"). Profile photos and other media are never stored — contact metadata only. LinkedIn has no live API here: if connections are missing, ask the operator to import a LinkedIn data export (Advanced → LinkedIn import).
`, root)
}

func (c *LinkedInConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	store := c.store
	if store == nil || !store.Enabled() {
		shared.WriteJSONError(w, http.StatusNotFound, "linkedin store disabled")
		return
	}
	path := strings.Trim(remainingPath, "/")
	if path == "" {
		path = strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/services/"+LinkedInServiceID), "/")
	}
	switch {
	case r.Method == http.MethodGet && path == "import/status":
		contacts, err := store.Counts()
		if err != nil {
			shared.WriteJSONError(w, http.StatusInternalServerError, "linkedin store error")
			return
		}
		shared.WriteJSON(w, map[string]any{"contacts": contacts, "path": store.Path()})
	case r.Method == http.MethodGet && (path == "contacts" || path == "contacts/" || path == "me/contacts"):
		limit := LibraryItemCap
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		list, err := store.ListContacts(limit)
		if err != nil {
			shared.WriteJSONError(w, http.StatusInternalServerError, "linkedin store error")
			return
		}
		shared.WriteJSON(w, kinds.ContactList{Contacts: list, LibraryListMeta: kinds.LibraryListMeta{Total: len(list), Synced: true}})
	case r.Method == http.MethodPost && path == "import":
		var req struct {
			Path string `json:"path"`
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err := json.Unmarshal(body, &req); err != nil || strings.TrimSpace(req.Path) == "" {
			shared.WriteJSONError(w, http.StatusBadRequest, "path is required")
			return
		}
		res, err := ImportLinkedInPath(store, req.Path)
		if err != nil {
			shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		shared.WriteJSON(w, res)
	default:
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ListUserContacts returns imported LinkedIn connections as a ContactProvider.
func (c *LinkedInConnector) ListUserContacts(ctx context.Context) (contacts []kinds.Contact, truncated bool, err error) {
	store := c.store
	if store == nil || !store.Enabled() {
		return nil, false, kinds.ErrUnauthorized
	}
	list, err := store.ListContacts(LibraryItemCap)
	if err != nil {
		return nil, false, err
	}
	if n, cerr := store.Counts(); cerr == nil && n > len(list) {
		truncated = true
	}
	return list, truncated, nil
}

var (
	_ ServiceConnector      = (*LinkedInConnector)(nil)
	_ kinds.ContactProvider = (*LinkedInConnector)(nil)
)
