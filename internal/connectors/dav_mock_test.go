package connectors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

// davMock is a minimal CardDAV/CalDAV server for connector tests (issue #146).
// It speaks .well-known redirect, PROPFIND multistatus, and REPORT bodies.

type davMock struct {
	vcards    map[string]string // href → body
	vevents   map[string]string // href → body
	etags     map[string]string
	syncToken string
	failQuery bool
}

func newDAVMock() *davMock {
	return &davMock{
		vcards:  map[string]string{},
		vevents: map[string]string{},
		etags:   map[string]string{},
	}
}

func (m *davMock) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Basic auth check.
		user, pass, ok := r.BasicAuth()
		if !ok || user == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = pass

		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/.well-known/carddav"):
			http.Redirect(w, r, "/remote.php/dav/addressbooks/alice/default/", http.StatusTemporaryRedirect)
		case strings.HasSuffix(path, "/.well-known/caldav"):
			http.Redirect(w, r, "/remote.php/dav/calendars/alice/personal/", http.StatusTemporaryRedirect)
		case r.Method == "PROPFIND":
			m.handlePropfind(w, r, path)
		case r.Method == "REPORT":
			m.handleReport(w, r, path)
		case r.Method == http.MethodGet:
			m.handleGet(w, path)
		case r.Method == http.MethodPut:
			m.handlePut(w, r, path)
		case r.Method == http.MethodDelete:
			delete(m.vcards, path)
			delete(m.vevents, path)
			delete(m.etags, path)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}

func (m *davMock) handlePropfind(w http.ResponseWriter, r *http.Request, path string) {
	depth := r.Header.Get("Depth")
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	sb.WriteString(`<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:carddav" xmlns:a="urn:ietf:params:xml:ns:caldav">`)

	// Principal / home-set discovery.
	if strings.Contains(path, "addressbooks") || strings.Contains(path, "calendars") {
		// Collection itself + children.
		sb.WriteString(m.responseXML(path+"/", true, "", ""))
		if depth == "1" {
			if strings.Contains(path, "addressbooks") {
				for href, body := range m.vcards {
					_ = body
					sb.WriteString(m.responseXML("/"+strings.TrimPrefix(href, "/"), false, m.etags[href], "text/vcard"))
				}
			} else {
				for href := range m.vevents {
					sb.WriteString(m.responseXML("/"+strings.TrimPrefix(href, "/"), false, m.etags[href], "text/calendar"))
				}
			}
		}
	} else if strings.Contains(path, "principals") || path == "/" || strings.HasSuffix(path, "/dav/") {
		// current-user-principal + home-set props.
		sb.WriteString(`<d:response><d:href>` + path + `</d:href><d:propstat><d:prop>`)
		sb.WriteString(`<d:current-user-principal><d:href>/remote.php/dav/principals/alice/</d:href></d:current-user-principal>`)
		sb.WriteString(`<c:addressbook-home-set><d:href>/remote.php/dav/addressbooks/alice/</d:href></c:addressbook-home-set>`)
		sb.WriteString(`<a:calendar-home-set><d:href>/remote.php/dav/calendars/alice/</d:href></a:calendar-home-set>`)
		sb.WriteString(`<d:resourcetype><d:collection/></d:resourcetype>`)
		sb.WriteString(`</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`)
		// Also list home children when Depth=1 on home.
	} else if strings.Contains(path, "addressbooks/alice") && !strings.Contains(path, "default") {
		sb.WriteString(m.responseXML("/remote.php/dav/addressbooks/alice/", true, "", ""))
		sb.WriteString(m.responseXML("/remote.php/dav/addressbooks/alice/default/", true, "", ""))
	} else if strings.Contains(path, "calendars/alice") && !strings.Contains(path, "personal") {
		sb.WriteString(m.responseXML("/remote.php/dav/calendars/alice/", true, "", ""))
		sb.WriteString(m.responseXML("/remote.php/dav/calendars/alice/personal/", true, "", ""))
	} else {
		sb.WriteString(m.responseXML(path, true, "", ""))
	}
	sb.WriteString(`</d:multistatus>`)
	w.Header().Set("Content-Type", "application/xml")
	_, _ = w.Write([]byte(sb.String()))
}

func (m *davMock) responseXML(href string, collection bool, etag, contentType string) string {
	var sb strings.Builder
	sb.WriteString(`<d:response><d:href>` + href + `</d:href><d:propstat><d:prop>`)
	if collection {
		sb.WriteString(`<d:resourcetype><d:collection/>`)
		if strings.Contains(href, "addressbook") {
			sb.WriteString(`<c:addressbook/>`)
		}
		if strings.Contains(href, "calendar") || strings.Contains(href, "personal") {
			sb.WriteString(`<a:calendar/>`)
		}
		sb.WriteString(`</d:resourcetype>`)
		sb.WriteString(`<d:displayname>Mock</d:displayname>`)
	} else {
		if etag != "" {
			sb.WriteString(`<d:getetag>` + etag + `</d:getetag>`)
		}
		if contentType != "" {
			sb.WriteString(`<d:getcontenttype>` + contentType + `</d:getcontenttype>`)
		}
	}
	sb.WriteString(`</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`)
	return sb.String()
}

func (m *davMock) handleReport(w http.ResponseWriter, r *http.Request, path string) {
	if m.failQuery {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	sb.WriteString(`<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:carddav" xmlns:a="urn:ietf:params:xml:ns:caldav">`)
	if strings.Contains(path, "addressbook") {
		for href, body := range m.vcards {
			sb.WriteString(`<d:response><d:href>` + href + `</d:href><d:propstat><d:prop>`)
			sb.WriteString(`<d:getetag>` + m.etags[href] + `</d:getetag>`)
			sb.WriteString(`<c:address-data>` + xmlEscape(body) + `</c:address-data>`)
			sb.WriteString(`</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`)
		}
		if m.syncToken != "" {
			sb.WriteString(`<d:sync-token>` + m.syncToken + `</d:sync-token>`)
		}
	} else {
		for href, body := range m.vevents {
			sb.WriteString(`<d:response><d:href>` + href + `</d:href><d:propstat><d:prop>`)
			sb.WriteString(`<d:getetag>` + m.etags[href] + `</d:getetag>`)
			sb.WriteString(`<c:calendar-data>` + xmlEscape(body) + `</c:calendar-data>`)
			sb.WriteString(`</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`)
		}
	}
	sb.WriteString(`</d:multistatus>`)
	w.Header().Set("Content-Type", "application/xml")
	_, _ = w.Write([]byte(sb.String()))
}

func (m *davMock) handleGet(w http.ResponseWriter, path string) {
	if body, ok := m.vcards[path]; ok {
		w.Header().Set("ETag", m.etags[path])
		w.Header().Set("Content-Type", "text/vcard")
		_, _ = w.Write([]byte(body))
		return
	}
	if body, ok := m.vevents[path]; ok {
		w.Header().Set("ETag", m.etags[path])
		w.Header().Set("Content-Type", "text/calendar")
		_, _ = w.Write([]byte(body))
		return
	}
	http.NotFound(w, nil)
}

func (m *davMock) handlePut(w http.ResponseWriter, r *http.Request, path string) {
	body := make([]byte, r.ContentLength)
	_, _ = r.Body.Read(body)
	if match := r.Header.Get("If-Match"); match != "" {
		if cur, ok := m.etags[path]; ok && cur != match && match != "*" {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		if _, ok := m.etags[path]; !ok {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
	}
	if r.Header.Get("If-None-Match") == "*" {
		if _, ok := m.etags[path]; ok {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
	}
	etag := `"put-1"`
	m.etags[path] = etag
	if strings.HasSuffix(path, ".vcf") {
		m.vcards[path] = string(body)
	} else {
		m.vevents[path] = string(body)
	}
	w.Header().Set("ETag", etag)
	w.WriteHeader(http.StatusCreated)
}

func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func testDAVHost(t *testing.T, serviceID, endpoint string) *testHost {
	t.Helper()
	h := newTestHostWithService(serviceID, endpoint, "https://aegis.example.com")
	h.services[serviceID].Headers = map[string]string{
		"X-DAV-Username": "alice",
		"X-DAV-Password": "app-password",
	}
	return h
}

func TestCardDAVConnector_ListUserContacts_REPORT(t *testing.T) {
	m := newDAVMock()
	m.vcards["/remote.php/dav/addressbooks/alice/default/ada.vcf"] =
		"BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Ada Lovelace\r\nEMAIL:ada@example.com\r\nUID:uid-ada\r\nEND:VCARD\r\n"
	m.vcards["/remote.php/dav/addressbooks/alice/default/grace.vcf"] =
		"BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Grace Hopper\r\nEMAIL:grace@example.com\r\nUID:uid-grace\r\nEND:VCARD\r\n"
	m.etags["/remote.php/dav/addressbooks/alice/default/ada.vcf"] = `"e-ada"`
	m.etags["/remote.php/dav/addressbooks/alice/default/grace.vcf"] = `"e-grace"`
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	h := testDAVHost(t, CardDAVServiceID, srv.URL)
	c := NewCardDAVConnector(h)
	contacts, truncated, err := c.ListUserContacts(context.Background())
	if err != nil {
		t.Fatalf("ListUserContacts: %v", err)
	}
	if truncated {
		t.Fatal("unexpected truncated")
	}
	if len(contacts) != 2 {
		t.Fatalf("got %d contacts", len(contacts))
	}
	names := map[string]string{}
	for _, ct := range contacts {
		names[ct.Name] = ct.Sources[0].Extra["etag"].(string)
	}
	if names["Ada Lovelace"] != `"e-ada"` || names["Grace Hopper"] != `"e-grace"` {
		t.Fatalf("names = %+v", names)
	}
}

func TestCardDAVConnector_UnauthorizedWithoutCredentials(t *testing.T) {
	h := newTestHostWithService(CardDAVServiceID, "https://dav.example.invalid", "https://aegis.example.com")
	_, _, err := NewCardDAVConnector(h).ListUserContacts(context.Background())
	if err != types.ErrUnauthorized {
		t.Fatalf("err = %v", err)
	}
}

func TestCardDAVConnector_PutContact_IfMatchConflict(t *testing.T) {
	m := newDAVMock()
	path := "/remote.php/dav/addressbooks/alice/default/ada.vcf"
	m.vcards[path] = "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Ada\r\nEND:VCARD\r\n"
	m.etags[path] = `"remote-newer"`
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	h := testDAVHost(t, CardDAVServiceID, srv.URL)
	// Point discovery straight at the collection.
	h.services[CardDAVServiceID].Endpoint = srv.URL + "/remote.php/dav/addressbooks/alice/default/"

	c := NewCardDAVConnector(h)
	contact := ParseVCard(CardDAVServiceID, CardDAVServiceName, "ada.vcf",
		"BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Ada L\r\nEMAIL:ada@example.com\r\nEND:VCARD\r\n", `"stale"`)
	_, err := c.PutContact(context.Background(), srv.URL+path, `"stale"`, contact)
	if err != ErrConflict {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestCalDAVConnector_ListCalendarEvents_RRULE(t *testing.T) {
	m := newDAVMock()
	m.vevents["/remote.php/dav/calendars/alice/personal/standup.ics"] =
		"BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:su-1\r\nSUMMARY:Standup\r\n" +
			"DTSTART:20260302T090000Z\r\nDTEND:20260302T091500Z\r\nRRULE:FREQ=WEEKLY;BYDAY=MO;COUNT=2\r\n" +
			"END:VEVENT\r\nEND:VCALENDAR\r\n"
	m.vevents["/remote.php/dav/calendars/alice/personal/doctor.ics"] =
		"BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:dr-1\r\nSUMMARY:Doctor\r\n" +
			"DTSTART:20260302T100000Z\r\nDTEND:20260302T103000Z\r\nLOCATION:Clinic\r\n" +
			"END:VEVENT\r\nEND:VCALENDAR\r\n"
	m.etags["/remote.php/dav/calendars/alice/personal/standup.ics"] = `"e-su"`
	m.etags["/remote.php/dav/calendars/alice/personal/doctor.ics"] = `"e-dr"`
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	h := testDAVHost(t, CalDAVServiceID, srv.URL)
	c := NewCalDAVConnector(h)
	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	events, truncated, err := c.ListEventsWithWindow(context.Background(), start, end)
	if err != nil {
		t.Fatalf("ListEventsWithWindow: %v", err)
	}
	if truncated {
		t.Fatal("unexpected truncated")
	}
	// Standup expands to 2 Mondays + 1 doctor = 3.
	if len(events) != 3 {
		t.Fatalf("got %d events: %+v", len(events), events)
	}
	var standups, doctors int
	for _, ev := range events {
		if strings.Contains(ev.Title, "Standup") {
			standups++
		}
		if ev.Title == "Doctor" {
			doctors++
		}
	}
	if standups != 2 || doctors != 1 {
		t.Fatalf("standups=%d doctors=%d events=%+v", standups, doctors, events)
	}
}

func TestCalDAVConnector_Unauthorized(t *testing.T) {
	h := newTestHostWithService(CalDAVServiceID, "https://dav.example.invalid", "https://aegis.example.com")
	_, _, err := NewCalDAVConnector(h).ListCalendarEvents(context.Background())
	if err != types.ErrUnauthorized {
		t.Fatalf("err = %v", err)
	}
}
