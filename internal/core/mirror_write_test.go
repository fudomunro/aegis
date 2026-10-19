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

func TestGoogleContactMirror_WriteContacts_CreateAndConflict(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case strings.Contains(path, ":createContact"):
			_, _ = w.Write([]byte(`{"resourceName":"people/n1","etag":"e1"}`))
		case strings.Contains(path, ":updateContact"):
			if r.Header.Get("If-Match") == `"stale"` {
				w.WriteHeader(http.StatusConflict)
				return
			}
			_, _ = w.Write([]byte(`{"resourceName":"people/c1","etag":"e2"}`))
		case strings.Contains(path, "/people/me/connections"):
			_, _ = w.Write([]byte(`{"connections":[{"resourceName":"people/c1","names":[{"displayName":"Ada"}],"emailAddresses":[{"value":"ada@example.com"}],"etag":"e0"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	srv := testServerWithBase(&Config{Services: []Service{{
		ID: "google", Name: "Google Contacts", Action: "inject", Endpoint: ts.URL,
		GrantLimits: types.Permissions{"contacts": types.AccessWrite},
	}}}, "https://aegis.example.com")
	srv.connectors["google"] = connectors.NewGoogleContactsConnector(srv)
	srv.tokens.Save("google", &OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})

	m := &googleContactMirror{server: srv, serviceID: "google"}

	// Update path with conflict.
	contacts := []mirrorContact{{Name: "Ada", Emails: []string{"ada@example.com"}, SourceID: "people/c1", SourceETag: `"stale"`}}
	_, _, conflicts, err := m.writeContacts(context.Background(), contacts)
	if err != nil {
		t.Fatalf("writeContacts: %v", err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %+v", conflicts)
	}

	// Create path.
	contacts = []mirrorContact{{Name: "Grace", Emails: []string{"grace@example.com"}}}
	copied, _, _, err := m.writeContacts(context.Background(), contacts)
	if err != nil || copied != 1 {
		t.Fatalf("copied=%d err=%v", copied, err)
	}

	// loadContacts maps neutral shape.
	loaded, _, err := m.loadContacts(context.Background())
	if err != nil || len(loaded) != 1 {
		t.Fatalf("loadContacts = %+v err=%v", loaded, err)
	}
}

func TestGoogleEventMirror_WriteEvents_CreateAndConflict(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"id":"ev-new","etag":"e1"}`))
			return
		}
		if r.Header.Get("If-Match") == `"stale"` {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		if strings.Contains(r.URL.Path, "/events") && r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"items":[{"id":"ev-1","summary":"Doctor","start":{"dateTime":"2026-03-02T10:00:00Z"},"end":{"dateTime":"2026-03-02T10:30:00Z"},"etag":"e0"}]}`))
			return
		}
		// calendarList + events
		if strings.Contains(r.URL.Path, "calendarList") {
			_, _ = w.Write([]byte(`{"items":[{"id":"primary","summary":"Primary","accessRole":"owner"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"ev-1","etag":"e2"}`))
	}))
	defer ts.Close()

	srv := testServerWithBase(&Config{Services: []Service{{
		ID: "google-calendar", Name: "Google Calendar", Action: "inject", Endpoint: ts.URL,
		GrantLimits: types.Permissions{"events": types.AccessWrite},
	}}}, "https://aegis.example.com")
	srv.connectors["google-calendar"] = connectors.NewGoogleCalendarConnector(srv)
	srv.tokens.Save("google-calendar", &OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})

	m := &googleEventMirror{server: srv, serviceID: "google-calendar"}

	events := []mirrorEvent{{
		Title: "Doctor", StartAt: "2026-03-02T10:00:00Z", EndAt: "2026-03-02T10:30:00Z",
		SourceID: "ev-1", SourceETag: `"stale"`, CalendarID: "primary",
	}}
	_, _, conflicts, err := m.writeEvents(context.Background(), events)
	if err != nil {
		t.Fatalf("writeEvents: %v", err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %+v", conflicts)
	}

	events = []mirrorEvent{{
		Title: "New", StartAt: "2026-03-03T10:00:00Z", EndAt: "2026-03-03T11:00:00Z",
		CalendarID: "primary",
	}}
	copied, _, _, err := m.writeEvents(context.Background(), events)
	if err != nil || copied != 1 {
		t.Fatalf("copied=%d err=%v", copied, err)
	}
}

func TestDavEventMirror_WriteEvents_IfMatch(t *testing.T) {
	var lastMatch string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			lastMatch = r.Header.Get("If-Match")
			if lastMatch == `"stale"` {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			w.Header().Set("ETag", `"n"`)
			w.WriteHeader(http.StatusCreated)
			return
		}
		// PROPFIND collection
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:" xmlns:a="urn:ietf:params:xml:ns:caldav">
<d:response><d:href>/cal/</d:href><d:propstat><d:prop>
<d:resourcetype><d:collection/><a:calendar/></d:resourcetype>
</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`))
	}))
	defer ts.Close()

	srv := testServerWithBase(&Config{Services: []Service{{
		ID: connectors.CalDAVServiceID, Name: "DAV Calendar", Action: "inject",
		Endpoint:    ts.URL + "/cal/",
		Headers:     map[string]string{"X-DAV-Username": "u", "X-DAV-Password": "p"},
		GrantLimits: types.Permissions{"events": types.AccessWrite},
	}}}, "https://aegis.example.com")
	srv.connectors[connectors.CalDAVServiceID] = connectors.NewCalDAVConnector(srv)
	srv.CacheSet("caldav:collection", ts.URL+"/cal/", time.Hour)

	m := &davEventMirror{server: srv, serviceID: connectors.CalDAVServiceID}
	events := []mirrorEvent{{
		Title: "S", StartAt: "2026-03-02T10:00:00Z", EndAt: "2026-03-02T11:00:00Z",
		SourceETag: `"stale"`, SourceUID: "u1",
	}}
	_, _, conflicts, err := m.writeEvents(context.Background(), events)
	if err != nil {
		t.Fatalf("writeEvents: %v", err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %+v lastMatch=%q", conflicts, lastMatch)
	}

	events[0].SourceETag = `"ok"`
	copied, _, _, err := m.writeEvents(context.Background(), events)
	if err != nil || copied != 1 {
		t.Fatalf("copied=%d err=%v", copied, err)
	}
}

func TestDavURLHelpers(t *testing.T) {
	if got := connectors.TrimRightSlash("https://x/"); got != "https://x" {
		t.Fatalf("trim = %q", got)
	}
}


func TestDavMirrors_LoadContactsAndEvents(t *testing.T) {
	mock := &davWriteMock{}
	// Seed one vCard and one vEvent via GET/REPORT paths in the mock.
	// davWriteMock REPORT returns empty; use GET fallback by failing REPORT.
	// Instead, use the connectors test-style mock inline.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := r.BasicAuth(); !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		path := r.URL.Path
		xmlHdr := `<?xml version="1.0" encoding="UTF-8"?><d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:carddav" xmlns:a="urn:ietf:params:xml:ns:caldav">`
		switch {
		case r.Method == "REPORT" && (strings.Contains(path, "addressbook") || strings.Contains(path, "/ab")):
			w.Header().Set("Content-Type", "application/xml")
			body := "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Ada\r\nEMAIL:ada@example.com\r\nEND:VCARD\r\n"
			_, _ = w.Write([]byte(xmlHdr + `<d:response><d:href>/ab/ada.vcf</d:href><d:propstat><d:prop>
<d:getetag>"e1"</d:getetag><c:address-data>` + body + `</c:address-data>
</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`))
		case r.Method == "REPORT":
			w.Header().Set("Content-Type", "application/xml")
			body := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:ev\r\nSUMMARY:Doctor\r\nDTSTART:20261202T100000Z\r\nDTEND:20261202T103000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
			_, _ = w.Write([]byte(xmlHdr + `<d:response><d:href>/cal/ev.ics</d:href><d:propstat><d:prop>
<d:getetag>"e2"</d:getetag><c:calendar-data>` + body + `</c:calendar-data>
</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`))
		case r.Method == "PROPFIND":
			isCal := strings.Contains(path, "cal")
			res := "<d:collection/>"
			if isCal {
				res = "<d:collection/><a:calendar/>"
			} else {
				res = "<d:collection/><c:addressbook/>"
			}
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(xmlHdr + `<d:response><d:href>` + path + `</d:href><d:propstat><d:prop>
<d:resourcetype>` + res + `</d:resourcetype>
</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	_ = mock

	cfg := &Config{Services: []Service{
		{ID: connectors.CardDAVServiceID, Name: "DAV Contacts", Action: "inject",
			Endpoint: ts.URL + "/ab/", Headers: map[string]string{"X-DAV-Username": "u", "X-DAV-Password": "p"}},
		{ID: connectors.CalDAVServiceID, Name: "DAV Calendar", Action: "inject",
			Endpoint: ts.URL + "/cal/", Headers: map[string]string{"X-DAV-Username": "u", "X-DAV-Password": "p"}},
	}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors[connectors.CardDAVServiceID] = connectors.NewCardDAVConnector(srv)
	srv.connectors[connectors.CalDAVServiceID] = connectors.NewCalDAVConnector(srv)
	srv.CacheSet("carddav:collection", ts.URL+"/ab/", time.Hour)
	srv.CacheSet("caldav:collection", ts.URL+"/cal/", time.Hour)

	cm := &davContactMirror{server: srv, serviceID: connectors.CardDAVServiceID}
	contacts, _, err := cm.loadContacts(context.Background())
	if err != nil || len(contacts) != 1 {
		t.Fatalf("loadContacts = %+v err=%v", contacts, err)
	}
	if contacts[0].Name != "Ada" {
		t.Fatalf("contact = %+v", contacts[0])
	}

	em := &davEventMirror{server: srv, serviceID: connectors.CalDAVServiceID}
	events, _, err := em.loadEvents(context.Background())
	if err != nil || len(events) != 1 {
		t.Fatalf("loadEvents = %+v err=%v", events, err)
	}
	if events[0].Title != "Doctor" {
		t.Fatalf("event = %+v", events[0])
	}
}
