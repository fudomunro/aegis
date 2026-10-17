package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

func TestImportSignalPath_JSONL(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSignalStore(filepath.Join(dir, "signal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	path := filepath.Join(dir, "export.jsonl")
	content := `{"type":"contact","id":"alice","name":"Alice","phones":["+15551234567"]}
{"type":"message","id":"m1","body":"hello there","sent_at":"2024-01-01T12:00:00Z","direction":"inbound","thread_id":"alice","from":{"name":"Alice","phones":["+15551234567"]},"to":[{"name":"Me"}]}
{"type":"message","id":"m2","body":"hi back","sent_at":"2024-01-01T12:01:00Z","direction":"outbound","from_me":true,"thread_id":"alice"}
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := ImportSignalPath(store, path)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Done || res.Format != "jsonl" {
		t.Fatalf("result = %+v, want done jsonl", res)
	}
	if res.ContactsStored != 1 || res.MessagesStored != 2 {
		t.Fatalf("stored contacts=%d messages=%d, want 1/2", res.ContactsStored, res.MessagesStored)
	}

	// Re-import skips existing rows.
	res2, err := ImportSignalPath(store, path)
	if err != nil {
		t.Fatal(err)
	}
	if res2.ContactsSkipped != 1 || res2.MessagesSkipped != 2 {
		t.Fatalf("reskip = %+v, want 1 contact + 2 messages skipped", res2)
	}

	contacts, err := store.ListContacts(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(contacts) != 1 || contacts[0].Name != "Alice" {
		t.Fatalf("contacts = %+v", contacts)
	}
	if len(contacts[0].Sources) != 1 || contacts[0].Sources[0].Service != signalServiceID {
		t.Fatalf("contact sources = %+v", contacts[0].Sources)
	}

	msgs, err := store.ListMessages(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2", len(msgs))
	}
	// Newest first.
	if msgs[0].Body != "hi back" || msgs[0].Direction != "outbound" {
		t.Fatalf("msg[0] = %+v", msgs[0])
	}
	if msgs[0].Channel != types.MessageChannelSignal {
		t.Fatalf("channel = %q", msgs[0].Channel)
	}
}

func TestImportSignalPath_JSONBundle(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSignalStore(filepath.Join(dir, "signal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	path := filepath.Join(dir, "export.json")
	content := `{
  "contacts": [{"id":"bob","name":"Bob","phones":["+15550001111"]}],
  "messages": [{"id":"m1","body":"yo","sent_at":"2024-02-01T08:00:00Z","direction":"outbound","from_me":true}]
}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := ImportSignalPath(store, path)
	if err != nil {
		t.Fatal(err)
	}
	if res.ContactsStored != 1 || res.MessagesStored != 1 {
		t.Fatalf("result = %+v", res)
	}
}

func TestImportSignalPath_DesktopSQLite(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "desktop.db")
	db, err := sql.Open("sqlite", src)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE conversations (id TEXT, name TEXT, e164 TEXT, serviceId TEXT, type TEXT, profileName TEXT);
CREATE TABLE messages (id TEXT, conversationId TEXT, type TEXT, body TEXT, timestamp INTEGER, sent_at INTEGER, received_at INTEGER);
INSERT INTO conversations VALUES ('c1','Alice','+15551234567','aci:alice','private','Ally');
INSERT INTO conversations VALUES ('g1','Family',NULL,NULL,'group',NULL);
INSERT INTO messages VALUES ('m1','c1','incoming','hello','1700000000000','1700000000000','1700000001000');
INSERT INTO messages VALUES ('m2','c1','outgoing','hi','1700000002000','1700000002000','0');
INSERT INTO messages VALUES ('m3','c1','incoming','','1700000003000','1700000003000','0');
`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	store, err := OpenSignalStore(filepath.Join(dir, "signal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	res, err := ImportSignalPath(store, src)
	if err != nil {
		t.Fatal(err)
	}
	if res.ContactsStored != 1 {
		t.Fatalf("contacts stored = %d (group skipped), result=%+v", res.ContactsStored, res)
	}
	if res.MessagesStored != 2 {
		t.Fatalf("messages stored = %d (empty body skipped), result=%+v", res.MessagesStored, res)
	}
	msgs, err := store.ListMessages(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("listed %d", len(msgs))
	}
	foundOut := false
	for _, m := range msgs {
		if m.Body == "hi" {
			foundOut = true
			if m.Direction != "outbound" {
				t.Fatalf("hi direction = %q", m.Direction)
			}
		}
	}
	if !foundOut {
		t.Fatalf("missing outbound message: %+v", msgs)
	}
}

func TestImportSignalPath_AndroidSQLite(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "android.db")
	db, err := sql.Open("sqlite", src)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE recipient (_id INTEGER, display_name TEXT, phone TEXT);
CREATE TABLE thread (_id INTEGER, recipient_id INTEGER);
CREATE TABLE message (_id INTEGER, thread_id INTEGER, address_id INTEGER, date_sent INTEGER, date INTEGER, type INTEGER, body TEXT);
INSERT INTO recipient VALUES (10,'Alice','+15551234567');
INSERT INTO thread VALUES (1,10);
INSERT INTO message VALUES (1,1,10,1700000000000,1700000000000,1,'hello android');
INSERT INTO message VALUES (2,1,10,1700000005000,1700000005000,2,'reply');
INSERT INTO message VALUES (3,1,10,1700000006000,1700000006000,2,'');
`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	store, err := OpenSignalStore(filepath.Join(dir, "signal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	res, err := ImportSignalPath(store, src)
	if err != nil {
		t.Fatal(err)
	}
	if res.ContactsStored != 1 || res.MessagesStored != 2 {
		t.Fatalf("result = %+v", res)
	}
	msgs, _ := store.ListMessages(10)
	for _, m := range msgs {
		switch m.Body {
		case "hello android":
			if m.Direction != "inbound" {
				t.Fatalf("hello direction = %q", m.Direction)
			}
			if m.From == nil || len(m.From.Phones) == 0 {
				t.Fatalf("hello from = %+v", m.From)
			}
		case "reply":
			if m.Direction != "outbound" {
				t.Fatalf("reply direction = %q", m.Direction)
			}
		}
	}
}

func TestSignalConnector_Lists(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSignalStore(filepath.Join(dir, "signal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	path := filepath.Join(dir, "export.jsonl")
	content := `{"type":"contact","id":"alice","name":"Alice","phones":["+15551234567"]}
{"type":"message","id":"m1","body":"hello","sent_at":"2024-01-01T12:00:00Z","direction":"inbound","from":{"name":"Alice","phones":["+15551234567"]}}
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportSignalPath(store, path); err != nil {
		t.Fatal(err)
	}

	conn := NewSignalConnector(store)
	var _ types.MessageProvider = conn
	var _ types.ContactProvider = conn

	contacts, truncated, err := conn.ListUserContacts(context.Background())
	if err != nil || truncated || len(contacts) != 1 {
		t.Fatalf("contacts=%v truncated=%v err=%v", contacts, truncated, err)
	}
	msgs, truncated, err := conn.ListMessages(context.Background())
	if err != nil || truncated || len(msgs) != 1 {
		t.Fatalf("messages=%v truncated=%v err=%v", msgs, truncated, err)
	}
	if msgs[0].Channel != types.MessageChannelSignal {
		t.Fatalf("channel = %q", msgs[0].Channel)
	}
	if !msgs[0].Sources[0].Canonical {
		t.Fatal("signal import should mark sources canonical")
	}

	// Disabled store is unauthorized (like other import connectors).
	empty := NewSignalConnector(nil)
	if _, _, err := empty.ListMessages(context.Background()); err != types.ErrUnauthorized {
		t.Fatalf("disabled ListMessages err = %v", err)
	}
}

func TestSignalLibraryKindRegistered(t *testing.T) {
	if LibraryKindByID("messages") == nil {
		t.Fatal("messages kind missing")
	}
	if LibraryKindByID("contacts") == nil {
		t.Fatal("contacts kind missing")
	}
}

func TestSignalImportStatusHints(t *testing.T) {
	srv := &Server{}
	st := srv.signalStatus()
	if st.Enabled {
		t.Fatal("nil store should be disabled")
	}
	if len(st.Hints) == 0 {
		t.Fatal("expected hints when disabled")
	}

	dir := t.TempDir()
	store, err := OpenSignalStore(filepath.Join(dir, "signal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	srv = &Server{signal: store, baseURL: "http://aegis.test"}
	st = srv.signalStatus()
	if !st.Enabled || !st.StoreEmpty {
		t.Fatalf("status = %+v", st)
	}
	if st.LibraryURL == "" {
		t.Fatal("missing library url")
	}
}

func TestPermissionsOverview_SignalImport(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSignalStore(filepath.Join(dir, "signal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.signal = store
	srv.connectors[signalServiceID] = NewSignalConnector(store)

	overview := srv.permissionsOverview()
	byID := map[string]types.TypeAccess{}
	for _, row := range overview.Types {
		byID[row.ID] = row
	}

	for _, kind := range []string{"messages", "contacts"} {
		row, ok := byID[kind]
		if !ok {
			t.Fatalf("missing %s row", kind)
		}
		found := false
		for _, acc := range row.Accessors {
			if acc.ID != syncSignalID {
				continue
			}
			found = true
			if acc.Kind != types.AccessorSync || acc.Level != types.AccessRead {
				t.Fatalf("%s signal row = %+v", kind, acc)
			}
		}
		if !found {
			t.Fatalf("%s accessors = %+v, want signal import", kind, row.Accessors)
		}
	}

	// Signal connector feeds both library kinds.
	if !kindHasProviders("messages", srv) {
		t.Fatal("messages should have providers when Signal is mounted")
	}
	if !kindHasProviders("contacts", srv) {
		t.Fatal("contacts should have providers when Signal is mounted")
	}
}

func TestResolveStatePaths_SignalStore(t *testing.T) {
	paths, err := ResolveStatePaths(StatePathOverrides{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if paths.Signal == "" || filepath.Base(paths.Signal) != "signal.db" {
		t.Fatalf("default Signal path = %q", paths.Signal)
	}

	empty, err := ResolveStatePaths(StatePathOverrides{Dir: t.TempDir(), SignalSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Signal != "" {
		t.Fatalf("explicit empty Signal path = %q, want disabled", empty.Signal)
	}

	custom := filepath.Join(t.TempDir(), "custom-signal.db")
	got, err := ResolveStatePaths(StatePathOverrides{Dir: t.TempDir(), Signal: custom, SignalSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Signal != custom {
		t.Fatalf("custom Signal path = %q, want %q", got.Signal, custom)
	}
}

func TestSignalCapabilitiesAndInstructions(t *testing.T) {
	c := NewSignalConnector(nil)
	caps := c.Capabilities()
	if len(caps.DataTypes) != 2 {
		t.Fatalf("caps = %+v", caps)
	}
	names := map[string]bool{}
	for _, dt := range caps.DataTypes {
		names[dt.Name] = true
	}
	if !names["contacts"] || !names["messages"] {
		t.Fatalf("data types = %+v", names)
	}
	s := c.SystemInstructions("https://aegis.example.com")
	if !strings.Contains(s, "Signal") || !strings.Contains(s, "/v1/library/messages") {
		t.Fatalf("instructions = %q", s)
	}
}

func TestSignalHandleProxy(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSignalStore(filepath.Join(dir, "signal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	path := filepath.Join(dir, "export.jsonl")
	content := `{"type":"contact","id":"alice","name":"Alice","phones":["+15551234567"]}
{"type":"message","id":"m1","body":"hello","sent_at":"2024-01-01T12:00:00Z","direction":"inbound"}
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportSignalPath(store, path); err != nil {
		t.Fatal(err)
	}
	c := NewSignalConnector(store)

	req := httptest.NewRequest("GET", "/v1/services/signal/import/status", nil)
	w := httptest.NewRecorder()
	c.HandleProxy(w, req, "/import/status")
	if w.Code != http.StatusOK {
		t.Fatalf("status code %d body=%s", w.Code, w.Body.String())
	}

	req2 := httptest.NewRequest("GET", "/v1/services/signal/messages?limit=5", nil)
	w2 := httptest.NewRecorder()
	c.HandleProxy(w2, req2, "/messages")
	if w2.Code != http.StatusOK {
		t.Fatalf("messages code %d body=%s", w2.Code, w2.Body.String())
	}
	var msgs types.MessageList
	if err := json.Unmarshal(w2.Body.Bytes(), &msgs); err != nil {
		t.Fatal(err)
	}
	if msgs.Total != 1 {
		t.Fatalf("messages total = %d", msgs.Total)
	}

	req3 := httptest.NewRequest("GET", "/v1/services/signal/contacts", nil)
	w3 := httptest.NewRecorder()
	c.HandleProxy(w3, req3, "/contacts")
	if w3.Code != http.StatusOK {
		t.Fatalf("contacts code %d body=%s", w3.Code, w3.Body.String())
	}
	var contacts types.ContactList
	if err := json.Unmarshal(w3.Body.Bytes(), &contacts); err != nil {
		t.Fatal(err)
	}
	if contacts.Total != 1 {
		t.Fatalf("contacts total = %d", contacts.Total)
	}

	req4 := httptest.NewRequest("POST", "/v1/services/signal/import", strings.NewReader(`{"path":"`+path+`"}`))
	w4 := httptest.NewRecorder()
	c.HandleProxy(w4, req4, "/import")
	if w4.Code != http.StatusOK {
		t.Fatalf("import code %d body=%s", w4.Code, w4.Body.String())
	}

	req5 := httptest.NewRequest("GET", "/v1/services/signal/nope", nil)
	w5 := httptest.NewRecorder()
	c.HandleProxy(w5, req5, "/nope")
	if w5.Code != http.StatusMethodNotAllowed {
		t.Fatalf("nope code %d", w5.Code)
	}

	// Disabled store.
	empty := NewSignalConnector(nil)
	w6 := httptest.NewRecorder()
	empty.HandleProxy(w6, httptest.NewRequest("GET", "/v1/services/signal/messages", nil), "/messages")
	if w6.Code != http.StatusNotFound {
		t.Fatalf("disabled code %d", w6.Code)
	}
}

func TestHandleAdminSignal(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSignalStore(filepath.Join(dir, "signal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.signal = store
	srv.cache = NewCache()

	// Status
	req := httptest.NewRequest("GET", "/v1/admin/signal/status", nil)
	w := httptest.NewRecorder()
	srv.HandleAdminSignal(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status code %d body=%s", w.Code, w.Body.String())
	}

	// Wrong method on status
	w2 := httptest.NewRecorder()
	srv.HandleAdminSignal(w2, httptest.NewRequest("POST", "/v1/admin/signal/status", nil))
	if w2.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status POST code %d", w2.Code)
	}

	// Import
	path := filepath.Join(dir, "export.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"message","id":"m1","body":"hi","sent_at":"2024-01-01T00:00:00Z"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := strings.NewReader(`{"path":"` + path + `"}`)
	req3 := httptest.NewRequest("POST", "/v1/admin/signal/import", body)
	w3 := httptest.NewRecorder()
	srv.HandleAdminSignal(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("import code %d body=%s", w3.Code, w3.Body.String())
	}

	// Import missing path
	w4 := httptest.NewRecorder()
	srv.HandleAdminSignal(w4, httptest.NewRequest("POST", "/v1/admin/signal/import", strings.NewReader(`{}`)))
	if w4.Code != http.StatusBadRequest {
		t.Fatalf("empty path code %d", w4.Code)
	}

	// Unknown subpath
	w5 := httptest.NewRecorder()
	srv.HandleAdminSignal(w5, httptest.NewRequest("GET", "/v1/admin/signal/nope", nil))
	if w5.Code != http.StatusNotFound {
		t.Fatalf("nope code %d", w5.Code)
	}

	// Disabled store import
	srv2 := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv2.cache = NewCache()
	w6 := httptest.NewRecorder()
	srv2.HandleAdminSignal(w6, httptest.NewRequest("POST", "/v1/admin/signal/import", strings.NewReader(`{"path":"/tmp/x"}`)))
	if w6.Code != http.StatusBadRequest {
		t.Fatalf("disabled import code %d", w6.Code)
	}
}

func TestImportSignalPath_EdgeCases(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSignalStore(filepath.Join(dir, "signal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Disabled store
	if _, err := ImportSignalPath(nil, dir); err == nil {
		t.Fatal("want error for disabled store")
	}

	// Missing path
	if _, err := ImportSignalPath(store, filepath.Join(dir, "missing.jsonl")); err == nil {
		t.Fatal("want error for missing path")
	}

	// Directory with only unreadable extensions
	emptyDir := filepath.Join(dir, "empty")
	if err := os.MkdirAll(emptyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(emptyDir, "readme.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportSignalPath(store, emptyDir); err == nil {
		t.Fatal("want error when directory has no importable files")
	}

	// Directory import mixes files
	dir2 := filepath.Join(dir, "mix")
	if err := os.MkdirAll(dir2, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir2, "a.jsonl"), []byte(`{"type":"contact","id":"c1","name":"C"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir2, "b.json"), []byte(`{"messages":[{"id":"m1","body":"x","sent_at":"2024-01-01T00:00:00Z"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := ImportSignalPath(store, dir2)
	if err != nil {
		t.Fatal(err)
	}
	if res.ContactsStored != 1 || res.MessagesStored != 1 {
		t.Fatalf("dir import = %+v", res)
	}

	// JSON array form
	arrPath := filepath.Join(dir, "arr.json")
	if err := os.WriteFile(arrPath, []byte(`[{"type":"contact","id":"c2","name":"D","phones":["+1"]},{"type":"message","id":"m2","body":"y","sent_at":"2024-01-02T00:00:00Z"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportSignalPath(store, arrPath); err != nil {
		t.Fatal(err)
	}

	// Single-object JSON
	onePath := filepath.Join(dir, "one.json")
	if err := os.WriteFile(onePath, []byte(`{"type":"contact","id":"c3","name":"E","phones":["+2"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportSignalPath(store, onePath); err != nil {
		t.Fatal(err)
	}

	// Unknown JSON shape (recorded as errors, not a hard failure)
	badPath := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(badPath, []byte(`{"nope":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	resBad, err := ImportSignalPath(store, badPath)
	if err != nil {
		t.Fatal(err)
	}
	if resBad.Errors == 0 {
		t.Fatalf("want errors for unknown JSON shape, got %+v", resBad)
	}

	// Unknown record type in JSONL
	badLine := filepath.Join(dir, "bad.jsonl")
	if err := os.WriteFile(badLine, []byte(`{"type":"widget","id":"z"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res2, err := ImportSignalPath(store, badLine)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Errors == 0 {
		t.Fatalf("want errors for unknown type, got %+v", res2)
	}

	// Not a SQLite file
	notDB := filepath.Join(dir, "not.db")
	if err := os.WriteFile(notDB, []byte("this is not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportSignalPath(store, notDB); err == nil && res.Errors == 0 {
		// ImportSignalPath swallows per-file errors into res.Errors
	}
	res3, err := ImportSignalPath(store, notDB)
	if err != nil {
		t.Fatal(err)
	}
	if res3.Errors == 0 {
		t.Fatalf("want sqlite parse error, got %+v", res3)
	}
}

func TestNormalizeSignalDirection(t *testing.T) {
	cases := []struct {
		dir    string
		fromMe bool
		want   string
	}{
		{"incoming", false, "inbound"},
		{"outgoing", true, "outbound"},
		{"INBOUND", false, "inbound"},
		{"", true, "outbound"},
		{"", false, "unknown"},
		{"weird", false, "unknown"},
	}
	for _, tc := range cases {
		if got := normalizeSignalDirection(tc.dir, tc.fromMe); got != tc.want {
			t.Fatalf("normalizeSignalDirection(%q,%v)=%q want %q", tc.dir, tc.fromMe, got, tc.want)
		}
	}
}

func TestSignalSourceIDFallbacks(t *testing.T) {
	c := types.Contact{Name: "Solo"}
	if id := signalContactSourceID(c); id != "name:solo" {
		t.Fatalf("contact id = %q", id)
	}
	m := types.Message{ThreadID: "t", SentAt: "2024-01-01T00:00:00Z", Body: "hi"}
	id := signalMessageSourceID(m)
	if id == "" || !strings.HasPrefix(id, "sig-") {
		t.Fatalf("message id = %q", id)
	}
	// Stable across calls.
	if signalMessageSourceID(m) != id {
		t.Fatal("message id should be stable")
	}
}

func TestAndroidMessageDirection(t *testing.T) {
	if d, me := androidMessageDirection(1); d != "inbound" || me {
		t.Fatalf("type 1 = %q %v", d, me)
	}
	if d, me := androidMessageDirection(2); d != "outbound" || !me {
		t.Fatalf("type 2 = %q %v", d, me)
	}
	if d, me := androidMessageDirection(0); d != "unknown" || me {
		t.Fatalf("type 0 = %q %v", d, me)
	}
}
