package core

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/copies"
	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/core/tokens"
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
	ledger, err := copies.Open(t.TempDir() + "/copy_ledger.db")
	if err != nil {
		t.Fatalf("copies.Open: %v", err)
	}
	defer ledger.Close()
	s := &Server{copyLedger: ledger}
	s.recordMirrorItemCopy(MirrorKindContacts, "Ada Lovelace", "dav-contacts", "ada.vcf", "Ada Lovelace", `"etag-1"`)
	recs := ledger.List(copies.Filter{Kind: MirrorKindContacts})
	if len(recs) != 1 {
		t.Fatalf("got %d records", len(recs))
	}
	if recs[0].Watermark != `"etag-1"` || recs[0].Version.ETag != `"etag-1"` {
		t.Fatalf("watermark = %+v", recs[0])
	}
}

func TestEntityMirrorLookup_KnownAndUnknown(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.connectors[connectors.CardDAVServiceID] = connectors.NewCardDAVConnector(srv)
	srv.connectors[connectors.GoogleContactsServiceID] = connectors.NewGoogleContactsConnector(srv)
	srv.connectors[connectors.CalDAVServiceID] = connectors.NewCalDAVConnector(srv)
	srv.connectors[connectors.GoogleCalendarServiceID] = connectors.NewGoogleCalendarConnector(srv)

	if _, err := mirrorOpFor[types.ContactProvider](srv, connectors.CardDAVServiceID, "contact mirror source"); err != nil {
		t.Fatalf("carddav source: %v", err)
	}
	if _, err := mirrorOpFor[types.ContactWriteProvider](srv, connectors.GoogleContactsServiceID, "contact mirror target"); err != nil {
		t.Fatalf("google contacts target: %v", err)
	}
	if _, err := mirrorOpFor[types.CalendarEventProvider](srv, connectors.CalDAVServiceID, "event mirror source"); err != nil {
		t.Fatalf("caldav source: %v", err)
	}
	if _, err := mirrorOpFor[types.CalendarEventWriteProvider](srv, connectors.GoogleCalendarServiceID, "event mirror target"); err != nil {
		t.Fatalf("google calendar target: %v", err)
	}
	if _, err := mirrorOpFor[types.ContactProvider](srv, "nope", "contact mirror source"); err == nil {
		t.Fatal("expected unknown source error")
	}
	if _, err := mirrorOpFor[types.CalendarEventWriteProvider](srv, "nope", "event mirror target"); err == nil {
		t.Fatal("expected unknown target error")
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
			Endpoint:    ts.URL + "/remote.php/dav/addressbooks/alice/default/",
			Headers:     map[string]string{"X-DAV-Username": "u", "X-DAV-Password": "p"},
			GrantLimits: policy.Permissions{"contacts": policy.AccessRead}},
		{ID: "google", Name: "Google Contacts", Action: "inject",
			Endpoint:    people.URL,
			GrantLimits: policy.Permissions{"contacts": policy.AccessWrite}},
	}}, "https://aegis.example.com")
	srv.connectors[connectors.CardDAVServiceID] = connectors.NewCardDAVConnector(srv)
	srv.connectors["google"] = connectors.NewGoogleContactsConnector(srv)
	srv.tokens.Save("google", &tokens.OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})
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
