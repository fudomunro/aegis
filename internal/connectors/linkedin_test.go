package connectors

import (
	"archive/zip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/kinds"
)

const linkedinConnectionsCSV = `Notes:
""

First Name,Last Name,URL,Email Address,Company,Position,Connected On
Jane,Doe,https://www.linkedin.com/in/janedoe,jane@example.com,Acme Corp,Software Engineer,01 Jan 2020
John,Smith,https://www.linkedin.com/in/johnsmith,,Widgets Inc,Product Manager,15 Mar 2021
,,, , ,
`

func TestOpenLinkedInStore_Disabled(t *testing.T) {
	store, err := OpenLinkedInStore("")
	if err != nil {
		t.Fatal(err)
	}
	if store != nil {
		t.Fatal("empty path should disable the store")
	}
	if store.Enabled() {
		t.Fatal("nil store must report disabled")
	}
	if _, err := store.Counts(); err == nil {
		t.Fatal("Counts on disabled store should error")
	}
	if _, err := store.ListContacts(10); err == nil {
		t.Fatal("ListContacts on disabled store should error")
	}
	if store.HasContact("x") {
		t.Fatal("HasContact on disabled store should be false")
	}
	if got := store.Admin().FlagName; got != "-linkedin-store" {
		t.Fatalf("FlagName = %q", got)
	}
}

func TestParseLinkedInCSV_ConnectionsExport(t *testing.T) {
	contacts, err := ParseLinkedInCSV("Connections.csv", []byte(linkedinConnectionsCSV))
	if err != nil {
		t.Fatal(err)
	}
	if len(contacts) != 2 {
		t.Fatalf("contacts = %d, want 2", len(contacts))
	}
	jane := contacts[0]
	if jane.Name != "Jane Doe" {
		t.Fatalf("name = %q", jane.Name)
	}
	if len(jane.Emails) != 1 || jane.Emails[0] != "jane@example.com" {
		t.Fatalf("emails = %v", jane.Emails)
	}
	if jane.Organization != "Acme Corp" {
		t.Fatalf("organization = %q", jane.Organization)
	}
	if len(jane.Handles) != 1 || jane.Handles[0] != "janedoe" {
		t.Fatalf("handles = %v", jane.Handles)
	}
	if len(jane.Sources) != 1 || jane.Sources[0].Service != LinkedInServiceID {
		t.Fatalf("sources = %+v", jane.Sources)
	}
	if jane.Sources[0].ServiceName != LinkedInServiceName {
		t.Fatalf("service name = %q", jane.Sources[0].ServiceName)
	}
	extra := jane.Sources[0].Extra
	if extra["url"] != "https://www.linkedin.com/in/janedoe" {
		t.Fatalf("extra url = %v", extra["url"])
	}
	if extra["position"] != "Software Engineer" {
		t.Fatalf("extra position = %v", extra["position"])
	}
	if extra["connected_on"] != "01 Jan 2020" {
		t.Fatalf("extra connected_on = %v", extra["connected_on"])
	}
	john := contacts[1]
	if john.Name != "John Smith" || john.Organization != "Widgets Inc" {
		t.Fatalf("john = %+v", john)
	}
	if len(john.Emails) != 0 {
		t.Fatalf("john emails = %v", john.Emails)
	}
	if len(john.Handles) != 1 || john.Handles[0] != "johnsmith" {
		t.Fatalf("john handles = %v", john.Handles)
	}
}

func TestParseLinkedInCSV_ContactsCSV(t *testing.T) {
	src := "First Name,Middle Name,Last Name,Title,Company Name,Email 1-Value,Email 2-Value,Phone 1-Value,URL\n" +
		"Ada,M,Lovelace,Analyst,Computing Ltd,ada@example.com,ada@work.example.com,+1 (555) 123-4567,https://www.linkedin.com/in/ada\n"
	contacts, err := ParseLinkedInCSV("Contacts.csv", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(contacts) != 1 {
		t.Fatalf("contacts = %d, want 1", len(contacts))
	}
	c := contacts[0]
	if c.Name != "Ada M Lovelace" {
		t.Fatalf("name = %q", c.Name)
	}
	if len(c.Emails) != 2 {
		t.Fatalf("emails = %v", c.Emails)
	}
	if len(c.Phones) != 1 || c.Phones[0] != "+15551234567" {
		t.Fatalf("phones = %v", c.Phones)
	}
	if c.Organization != "Computing Ltd" {
		t.Fatalf("organization = %q", c.Organization)
	}
	if len(c.Handles) != 1 || c.Handles[0] != "ada" {
		t.Fatalf("handles = %v", c.Handles)
	}
	if got := c.Sources[0].Extra["position"]; got != "Analyst" {
		t.Fatalf("position = %v", got)
	}
}

func TestParseLinkedInCSV_FullNameColumn(t *testing.T) {
	src := "Name,Email Address,Company\nGrace Hopper,grace@example.com,US Navy\n"
	contacts, err := ParseLinkedInCSV("x.csv", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(contacts) != 1 || contacts[0].Name != "Grace Hopper" {
		t.Fatalf("contacts = %+v", contacts)
	}
}

func TestParseLinkedInCSV_NoHeader(t *testing.T) {
	if _, err := ParseLinkedInCSV("x.csv", []byte("just,some,notes\n1,2,3\n")); err == nil {
		t.Fatal("expected an error for a CSV with no connections header")
	}
	if _, err := ParseLinkedInCSV("x.csv", nil); err == nil {
		t.Fatal("expected an error for empty input")
	}
}

func TestLinkedInPublicID(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://www.linkedin.com/in/janedoe", "janedoe"},
		{"https://www.linkedin.com/in/JohnDoe/", "johndoe"},
		{"https://uk.linkedin.com/in/jane-doe?trk=x", "jane-doe"},
		{"https://www.linkedin.com/in/janedoe/details/experience/", "janedoe"},
		{"https://example.com/not-linkedin", ""},
		{"", ""},
	} {
		if got := linkedinPublicID(tc.in); got != tc.want {
			t.Errorf("linkedinPublicID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLinkedInContactSourceID(t *testing.T) {
	withID := kinds.Contact{Sources: []kinds.ContactSource{{ID: "janedoe"}}}
	if got := linkedinContactSourceID(withID); got != "janedoe" {
		t.Fatalf("id-based source id = %q", got)
	}
	// Fallback: no source id, but an email identity key.
	byEmail := kinds.Contact{Emails: []string{"jane@example.com"}}
	got := linkedinContactSourceID(byEmail)
	if got == "" {
		t.Fatal("email fallback should produce a source id")
	}
	if empty := linkedinContactSourceID(kinds.Contact{}); empty != "" {
		t.Fatalf("empty contact source id = %q", empty)
	}
}

func TestImportLinkedInPath_CSVAndReimport(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenLinkedInStore(filepath.Join(dir, "linkedin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	path := filepath.Join(dir, "Connections.csv")
	if err := os.WriteFile(path, []byte(linkedinConnectionsCSV), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := ImportLinkedInPath(store, path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stored != 2 || res.Errors != 0 || !res.Done || res.Format != "csv" {
		t.Fatalf("result = %+v", res)
	}
	n, err := store.Counts()
	if err != nil || n != 2 {
		t.Fatalf("counts = %d err=%v", n, err)
	}
	if !store.HasContact("janedoe") {
		t.Fatal("janedoe should be stored")
	}
	// Re-import skips rows already present.
	res2, err := ImportLinkedInPath(store, path)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Stored != 0 || res2.Skipped != 2 {
		t.Fatalf("reimport result = %+v", res2)
	}
	list, err := store.ListContacts(10)
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %v err=%v", list, err)
	}
	if res.Failed() {
		t.Fatal("a clean run must not report Failed")
	}
	if attrs := res.LogAttrs(); len(attrs) == 0 {
		t.Fatal("LogAttrs should carry counters")
	}
}

func TestImportLinkedInPath_Zip(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenLinkedInStore(filepath.Join(dir, "linkedin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	zipPath := filepath.Join(dir, "linkedin-export.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range map[string]string{
		"Relationships/Connections.csv": linkedinConnectionsCSV,
		"Read Me First.txt":             "not a csv",
		"Ads and content/Shares.csv":    "Share,Date\na,b\n",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	res, err := ImportLinkedInPath(store, zipPath)
	if err != nil {
		t.Fatal(err)
	}
	if res.Format != "zip" || res.Stored != 2 || res.Errors != 0 {
		t.Fatalf("result = %+v", res)
	}
	// Only Connections.csv is imported — Shares.csv is not contact data.
	if n, err := store.Counts(); err != nil || n != 2 {
		t.Fatalf("counts = %d err=%v", n, err)
	}
}

func TestImportLinkedInPath_Directory(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenLinkedInStore(filepath.Join(dir, "linkedin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	inbox := filepath.Join(dir, "exports")
	if err := os.Mkdir(inbox, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"Connections.csv": linkedinConnectionsCSV,
		"Contacts.csv":    "Name,Email Address\nAda Lovelace,ada@example.com\n",
		"notes.txt":       "ignored",
	} {
		if err := os.WriteFile(filepath.Join(inbox, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	res, err := ImportLinkedInPath(store, inbox)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stored != 3 {
		t.Fatalf("result = %+v", res)
	}
}

func TestImportLinkedInPath_EdgeCases(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenLinkedInStore(filepath.Join(dir, "linkedin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if _, err := ImportLinkedInPath(nil, "x.csv"); err == nil {
		t.Fatal("nil store should error")
	}
	var disabled *LinkedInStore
	if _, err := ImportLinkedInPath(disabled, "x.csv"); err == nil {
		t.Fatal("disabled store should error")
	}
	if _, err := ImportLinkedInPath(store, filepath.Join(dir, "missing.csv")); err == nil {
		t.Fatal("missing path should error")
	}
	if _, err := ImportLinkedInPath(store, filepath.Join(dir, "x.txt")); err == nil {
		t.Fatal("unsupported extension should error")
	}
	emptyDir := filepath.Join(dir, "empty")
	if err := os.Mkdir(emptyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportLinkedInPath(store, emptyDir); err == nil {
		t.Fatal("directory without CSVs should error")
	}
	// A CSV without a recognizable header counts as an error, not rows.
	bad := filepath.Join(dir, "bad.csv")
	if err := os.WriteFile(bad, []byte("a,b,c\n1,2,3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := ImportLinkedInPath(store, bad)
	if err != nil {
		t.Fatal(err)
	}
	if res.Errors == 0 || res.Stored != 0 {
		t.Fatalf("result = %+v", res)
	}
	if !res.Failed() {
		t.Fatal("an error-only run must report Failed")
	}
	// Empty file is a quiet no-op.
	quiet := filepath.Join(dir, "quiet.csv")
	if err := os.WriteFile(quiet, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res2, err := ImportLinkedInPath(store, quiet)
	if err != nil || res2.Errors != 0 || res2.Scanned != 0 {
		t.Fatalf("quiet result = %+v err=%v", res2, err)
	}
}

func TestLinkedInConnector_Lists(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenLinkedInStore(filepath.Join(dir, "linkedin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	path := filepath.Join(dir, "Connections.csv")
	if err := os.WriteFile(path, []byte(linkedinConnectionsCSV), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportLinkedInPath(store, path); err != nil {
		t.Fatal(err)
	}

	conn := NewLinkedInConnector(store)
	var _ kinds.ContactProvider = conn
	var _ ServiceConnector = conn

	contacts, truncated, err := conn.ListUserContacts(context.Background())
	if err != nil || truncated || len(contacts) != 2 {
		t.Fatalf("contacts=%v truncated=%v err=%v", contacts, truncated, err)
	}

	// Disabled store is unauthorized (like other import connectors).
	empty := NewLinkedInConnector(nil)
	if _, _, err := empty.ListUserContacts(context.Background()); err != kinds.ErrUnauthorized {
		t.Fatalf("disabled ListUserContacts err = %v", err)
	}
}

func TestLinkedInCapabilitiesAndInstructions(t *testing.T) {
	c := NewLinkedInConnector(nil)
	caps := c.Capabilities()
	if len(caps.Kinds) != 1 || caps.Kinds[0].Name != "contacts" {
		t.Fatalf("caps = %+v", caps)
	}
	if len(caps.Kinds[0].Operations) != 2 {
		t.Fatalf("operations = %+v", caps.Kinds[0].Operations)
	}
	s := c.SystemInstructions("https://aegis.example.com")
	if !strings.Contains(s, "LinkedIn") || !strings.Contains(s, "/v1/library/contacts") {
		t.Fatalf("instructions = %q", s)
	}
	for _, banned := range []string{"-linkedin-store", "-import-linkedin"} {
		if strings.Contains(s, banned) {
			t.Fatalf("agent instructions must not name CLI flag %q", banned)
		}
	}
}

func TestLinkedInHandleProxy(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenLinkedInStore(filepath.Join(dir, "linkedin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	path := filepath.Join(dir, "Connections.csv")
	if err := os.WriteFile(path, []byte(linkedinConnectionsCSV), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportLinkedInPath(store, path); err != nil {
		t.Fatal(err)
	}
	c := NewLinkedInConnector(store)

	req := httptest.NewRequest("GET", "/v1/services/linkedin/import/status", nil)
	w := httptest.NewRecorder()
	c.HandleProxy(w, req, "/import/status")
	if w.Code != http.StatusOK {
		t.Fatalf("status code %d body=%s", w.Code, w.Body.String())
	}

	req2 := httptest.NewRequest("GET", "/v1/services/linkedin/contacts?limit=5", nil)
	w2 := httptest.NewRecorder()
	c.HandleProxy(w2, req2, "/contacts")
	if w2.Code != http.StatusOK {
		t.Fatalf("contacts code %d body=%s", w2.Code, w2.Body.String())
	}
	var contacts kinds.ContactList
	if err := json.Unmarshal(w2.Body.Bytes(), &contacts); err != nil {
		t.Fatal(err)
	}
	if contacts.Total != 2 {
		t.Fatalf("contacts total = %d", contacts.Total)
	}

	req3 := httptest.NewRequest("POST", "/v1/services/linkedin/import", strings.NewReader(`{"path":"`+path+`"}`))
	w3 := httptest.NewRecorder()
	c.HandleProxy(w3, req3, "/import")
	if w3.Code != http.StatusOK {
		t.Fatalf("import code %d body=%s", w3.Code, w3.Body.String())
	}

	req4 := httptest.NewRequest("POST", "/v1/services/linkedin/import", strings.NewReader(`{}`))
	w4 := httptest.NewRecorder()
	c.HandleProxy(w4, req4, "/import")
	if w4.Code != http.StatusBadRequest {
		t.Fatalf("import without path code %d", w4.Code)
	}

	req5 := httptest.NewRequest("GET", "/v1/services/linkedin/nope", nil)
	w5 := httptest.NewRecorder()
	c.HandleProxy(w5, req5, "/nope")
	if w5.Code != http.StatusMethodNotAllowed {
		t.Fatalf("nope code %d", w5.Code)
	}

	// Disabled store.
	empty := NewLinkedInConnector(nil)
	w6 := httptest.NewRecorder()
	empty.HandleProxy(w6, httptest.NewRequest("GET", "/v1/services/linkedin/contacts", nil), "/contacts")
	if w6.Code != http.StatusNotFound {
		t.Fatalf("disabled code %d", w6.Code)
	}
}

func TestLinkedInServiceAndAdmin(t *testing.T) {
	svc := LinkedInService()
	if svc.ID != LinkedInServiceID || svc.Name != LinkedInServiceName || svc.Action != "inject" {
		t.Fatalf("service card = %+v", svc)
	}
	if svc.Description == "" {
		t.Fatal("service card needs a description")
	}

	var nilStore *LinkedInStore
	admin := nilStore.Admin()
	if admin.PathHint == "" || admin.SamplePath == "" || admin.LibraryPath != "/v1/library/contacts" {
		t.Fatalf("admin = %+v", admin)
	}
	if admin.Counts() != nil {
		t.Fatal("disabled store must report nil counts")
	}
	hints := admin.Hints(ImportHintState{Enabled: false})
	if len(hints) == 0 {
		t.Fatal("disabled state needs hints")
	}
	emptyHints := admin.Hints(ImportHintState{Enabled: true, Counts: map[string]int{"contacts": 0}})
	if len(emptyHints) == 0 {
		t.Fatal("empty state needs hints")
	}
	fullHints := admin.Hints(ImportHintState{Enabled: true, Counts: map[string]int{"contacts": 5}, LibraryURL: "https://aegis.example.com/v1/library/contacts"})
	if len(fullHints) == 0 || !strings.Contains(fullHints[0], "5") {
		t.Fatalf("populated hints = %v", fullHints)
	}
}
