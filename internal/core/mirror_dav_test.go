package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/types"
)

// davWriteMock is a CardDAV PUT sink for mirror tests (issue #146).
type davWriteMock struct {
	putCount   int
	conflictOn string
	lastBody   string
	lastMatch  string
}

func (d *davWriteMock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	user, _, ok := r.BasicAuth()
	if !ok || user == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	path := r.URL.Path
	switch {
	case strings.HasSuffix(path, "/.well-known/carddav"):
		http.Redirect(w, r, "/remote.php/dav/addressbooks/alice/default/", http.StatusTemporaryRedirect)
	case r.Method == "PROPFIND":
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:carddav">
<d:response><d:href>/remote.php/dav/addressbooks/alice/default/</d:href><d:propstat><d:prop>
<d:resourcetype><d:collection/><c:addressbook/></d:resourcetype>
<d:displayname>Default</d:displayname>
</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
</d:multistatus>`))
	case r.Method == http.MethodPut:
		d.putCount++
		d.lastMatch = r.Header.Get("If-Match")
		if d.conflictOn != "" && strings.Contains(path, d.conflictOn) {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		d.lastBody = string(buf)
		w.Header().Set("ETag", `"new-1"`)
		w.WriteHeader(http.StatusCreated)
	case r.Method == "REPORT":
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:carddav"></d:multistatus>`))
	default:
		http.NotFound(w, nil)
	}
}

func TestRecordMirrorItemCopy_StoresETagWatermark(t *testing.T) {
	ledger, err := OpenCopyLedger(t.TempDir() + "/copy_ledger.db")
	if err != nil {
		t.Fatalf("OpenCopyLedger: %v", err)
	}
	defer ledger.Close()
	s := &Server{copies: ledger}
	s.recordMirrorItemCopy(MirrorKindContacts, "Ada Lovelace", "dav-contacts", "ada.vcf", "Ada Lovelace", `"etag-1"`)
	recs := ledger.List(CopyFilter{Kind: MirrorKindContacts})
	if len(recs) != 1 {
		t.Fatalf("got %d records", len(recs))
	}
	if recs[0].Watermark != `"etag-1"` || recs[0].Version.ETag != `"etag-1"` {
		t.Fatalf("watermark = %+v", recs[0])
	}
}

func TestCardDAVConnector_PutContact_ReportsIfMatchConflict(t *testing.T) {
	mock := &davWriteMock{conflictOn: "ada"}
	ts := httptest.NewServer(mock)
	defer ts.Close()

	srv := testServerWithBase(&Config{Services: []Service{{
		ID:       connectors.CardDAVServiceID,
		Name:     connectors.CardDAVServiceName,
		Action:   "inject",
		Endpoint: ts.URL,
		Headers: map[string]string{
			"X-DAV-Username": "alice",
			"X-DAV-Password": "pw",
		},
	}}}, "https://aegis.example.com")
	srv.connectors[connectors.CardDAVServiceID] = connectors.NewCardDAVConnector(srv)
	conn := connectors.NewCardDAVConnector(srv)

	// Conflict: If-Match fails against a newer remote edit.
	contact := types.NewContact(connectors.CardDAVServiceID, connectors.CardDAVServiceName,
		"ada.vcf", "Ada Lovelace", []string{"ada@example.com"}, nil)
	_, err := conn.PutContact(context.Background(), ts.URL+"/ada.vcf", `"stale"`, contact)
	if err != connectors.ErrConflict {
		t.Fatalf("err = %v, want ErrConflict", err)
	}

	// Success path stamps If-Match and returns the new etag.
	contact = types.NewContact(connectors.CardDAVServiceID, connectors.CardDAVServiceName,
		"grace.vcf", "Grace Hopper", []string{"grace@example.com"}, nil)
	etag, err := conn.PutContact(context.Background(), ts.URL+"/grace.vcf", `"old"`, contact)
	if err != nil {
		t.Fatalf("PutContact: %v", err)
	}
	if etag != `"new-1"` {
		t.Fatalf("etag = %q", etag)
	}
	if mock.lastMatch != `"old"` {
		t.Fatalf("If-Match = %q", mock.lastMatch)
	}
}

func TestDavContactMirror_WriteContacts_ReportsConflict(t *testing.T) {
	mock := &davWriteMock{conflictOn: "ada"}
	ts := httptest.NewServer(mock)
	defer ts.Close()

	srv := testServerWithBase(&Config{Services: []Service{{
		ID:       connectors.CardDAVServiceID,
		Name:     connectors.CardDAVServiceName,
		Action:   "inject",
		Endpoint: ts.URL + "/remote.php/dav/addressbooks/alice/default/",
		Headers: map[string]string{
			"X-DAV-Username": "alice",
			"X-DAV-Password": "pw",
		},
	}}}, "https://aegis.example.com")
	srv.connectors[connectors.CardDAVServiceID] = connectors.NewCardDAVConnector(srv)

	// Point discovery cache at the mock collection so writeContacts does not
	// need a full principal walk.
	srv.CacheSet("carddav:collection", ts.URL+"/remote.php/dav/addressbooks/alice/default/", 0)

	m := &davContactMirror{server: srv, serviceID: connectors.CardDAVServiceID}
	base := ts.URL + "/remote.php/dav/addressbooks/alice/default/"
	contacts := []mirrorContact{
		{Name: "Ada Lovelace", Emails: []string{"ada@example.com"}, SourceID: base + "ada.vcf", SourceETag: `"stale"`},
		{Name: "Grace Hopper", Emails: []string{"grace@example.com"}, SourceID: base + "grace.vcf", SourceETag: `"old"`},
	}
	copied, skips, conflicts, err := m.writeContacts(context.Background(), contacts)
	if err != nil {
		t.Fatalf("writeContacts: %v", err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %+v copied=%d skips=%+v", conflicts, copied, skips)
	}
	if !strings.Contains(conflicts[0].Reason, "If-Match") {
		t.Fatalf("conflict reason = %q", conflicts[0].Reason)
	}
	if copied != 1 {
		t.Fatalf("copied = %d", copied)
	}
	if mock.lastMatch == "" {
		t.Fatal("expected If-Match header on successful PUT")
	}
}

func TestEntityMirrorAdaptersImplementInterfaces(t *testing.T) {
	var (
		_ contactMirrorSource = (*davContactMirror)(nil)
		_ contactMirrorTarget = (*davContactMirror)(nil)
		_ contactMirrorSource = (*googleContactMirror)(nil)
		_ contactMirrorTarget = (*googleContactMirror)(nil)
		_ eventMirrorSource   = (*davEventMirror)(nil)
		_ eventMirrorTarget   = (*davEventMirror)(nil)
		_ eventMirrorSource   = (*googleEventMirror)(nil)
		_ eventMirrorTarget   = (*googleEventMirror)(nil)
	)
}

func TestParseRFC3339_Helpers(t *testing.T) {
	ts, ok := parseRFC3339("2026-03-02T10:00:00Z")
	if !ok || !ts.Equal(time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("ts = %v ok = %v", ts, ok)
	}
	if _, ok := parseRFC3339("not-a-time"); ok {
		t.Fatal("expected failure")
	}
}


func TestEntityMirrorFactory_KnownAndUnknown(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	if _, err := srv.contactMirrorSourceFor(connectors.CardDAVServiceID); err != nil {
		t.Fatalf("carddav source: %v", err)
	}
	if _, err := srv.contactMirrorTargetFor(connectors.GoogleContactsServiceID); err != nil {
		t.Fatalf("google contacts target: %v", err)
	}
	if _, err := srv.eventMirrorSourceFor(connectors.CalDAVServiceID); err != nil {
		t.Fatalf("caldav source: %v", err)
	}
	if _, err := srv.eventMirrorTargetFor(connectors.GoogleCalendarServiceID); err != nil {
		t.Fatalf("google calendar target: %v", err)
	}
	if _, err := srv.contactMirrorSourceFor("nope"); err == nil {
		t.Fatal("expected unknown source error")
	}
	if _, err := srv.eventMirrorTargetFor("nope"); err == nil {
		t.Fatal("expected unknown target error")
	}
}

func TestFirstSourceIDAndExtraHelpers(t *testing.T) {
	c := types.NewContact("s", "S", "id-1", "N", nil, nil)
	c.Sources[0].Extra = map[string]any{"etag": `"e"`}
	if firstSourceID(c.Sources) != "id-1" {
		t.Fatalf("firstSourceID = %q", firstSourceID(c.Sources))
	}
	if sourceExtraString(c.Sources, "etag") != `"e"` {
		t.Fatalf("extra = %q", sourceExtraString(c.Sources, "etag"))
	}
	ev := types.NewCalendarEvent("s", "S", "ev-1", "T", "2026-03-02T10:00:00Z", "2026-03-02T11:00:00Z", false)
	ev.Sources[0].Extra = map[string]any{"etag": `"x"`}
	if firstSourceIDEvents(ev.Sources) != "ev-1" {
		t.Fatalf("firstSourceIDEvents = %q", firstSourceIDEvents(ev.Sources))
	}
	if sourceExtraStringEvents(ev.Sources, "etag") != `"x"` {
		t.Fatalf("extra events = %q", sourceExtraStringEvents(ev.Sources, "etag"))
	}
}

func TestHandleEntityMirror_ContactsDryRun(t *testing.T) {
	mock := &davWriteMock{}
	ts := httptest.NewServer(mock)
	defer ts.Close()

	people := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"connections":[{"resourceName":"people/c1","names":[{"displayName":"Ada"}],"emailAddresses":[{"value":"ada@example.com"}]}]}`))
	}))
	defer people.Close()

	srv := testServerWithBase(&Config{Services: []Service{
		{ID: connectors.CardDAVServiceID, Name: "DAV Contacts", Action: "inject",
			Endpoint: ts.URL + "/remote.php/dav/addressbooks/alice/default/",
			Headers:  map[string]string{"X-DAV-Username": "u", "X-DAV-Password": "p"},
			GrantLimits: types.Permissions{"contacts": types.AccessRead}},
		{ID: "google", Name: "Google Contacts", Action: "inject",
			Endpoint:    people.URL,
			GrantLimits: types.Permissions{"contacts": types.AccessWrite}},
	}}, "https://aegis.example.com")
	srv.connectors[connectors.CardDAVServiceID] = connectors.NewCardDAVConnector(srv)
	srv.connectors["google"] = connectors.NewGoogleContactsConnector(srv)
	srv.tokens.Save("google", &OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})
	srv.CacheSet("carddav:collection", ts.URL+"/remote.php/dav/addressbooks/alice/default/", time.Hour)

	req := MirrorRequest{Kind: MirrorKindContacts, SourceService: connectors.CardDAVServiceID,
		TargetService: "google", DryRun: true}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/library/mirror", strings.NewReader(`{}`))
	srv.handleEntityMirror(w, r, req)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"kind":"contacts"`) {
		t.Fatalf("body = %s", w.Body.String())
	}
}
