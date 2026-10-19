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

func TestCardDAVConnector_CapabilitiesAndInstructions(t *testing.T) {
	c := NewCardDAVConnector(newTestHost("https://aegis.example.com"))
	caps := c.Capabilities()
	if len(caps.DataTypes) != 1 || caps.DataTypes[0].Name != "contacts" {
		t.Fatalf("caps = %+v", caps)
	}
	instr := c.SystemInstructions("https://aegis.example.com")
	if !strings.Contains(instr, "/v1/library/contacts") {
		t.Fatalf("instructions = %s", instr)
	}
	w := httptest.NewRecorder()
	c.HandleProxy(w, httptest.NewRequest(http.MethodGet, "/x", nil), "/me/contacts")
	if w.Code != http.StatusNotFound {
		t.Fatalf("HandleProxy code = %d", w.Code)
	}
}

func TestCardDAVConnector_ApplyAuth(t *testing.T) {
	h := newTestHostWithService(CardDAVServiceID, "https://dav.example.com", "https://aegis.example.com")
	h.services[CardDAVServiceID].Headers = map[string]string{
		"X-DAV-Username": "alice",
		"X-DAV-Password": "pw",
	}
	c := NewCardDAVConnector(h)
	req := httptest.NewRequest(http.MethodGet, "https://dav.example.com/", nil)
	c.ApplyCardDAVAuth(req, nil)
	user, pass, ok := req.BasicAuth()
	if !ok || user != "alice" || pass != "pw" {
		t.Fatalf("basic = %q %q %v", user, pass, ok)
	}
}

func TestCalDAVConnector_CapabilitiesAndInstructions(t *testing.T) {
	c := NewCalDAVConnector(newTestHost("https://aegis.example.com"))
	caps := c.Capabilities()
	if len(caps.DataTypes) != 1 || caps.DataTypes[0].Name != "events" {
		t.Fatalf("caps = %+v", caps)
	}
	instr := c.SystemInstructions("https://aegis.example.com")
	if !strings.Contains(instr, "/v1/library/events") || !strings.Contains(instr, "freebusy") {
		t.Fatalf("instructions = %s", instr)
	}
	w := httptest.NewRecorder()
	c.HandleProxy(w, httptest.NewRequest(http.MethodGet, "/x", nil), "/me/events")
	if w.Code != http.StatusNotFound {
		t.Fatalf("HandleProxy code = %d", w.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "https://dav.example.com/", nil)
	h := newTestHostWithService(CalDAVServiceID, "https://dav.example.com", "https://aegis.example.com")
	h.services[CalDAVServiceID].Headers = map[string]string{"Authorization": "Basic YWxpY2U6cHc="}
	NewCalDAVConnector(h).ApplyCalDAVAuth(req, nil)
	user, pass, ok := req.BasicAuth()
	if !ok || user != "alice" || pass != "pw" {
		t.Fatalf("basic = %q %q %v", user, pass, ok)
	}
}

func TestCardDAV_ListViaPropfind_WhenReportFails(t *testing.T) {
	m := newDAVMock()
	m.failQuery = true
	m.vcards["/remote.php/dav/addressbooks/alice/default/ada.vcf"] =
		"BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Ada\r\nEMAIL:ada@example.com\r\nEND:VCARD\r\n"
	m.etags["/remote.php/dav/addressbooks/alice/default/ada.vcf"] = `"e1"`
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	h := testDAVHost(t, CardDAVServiceID, srv.URL)
	// Force collection URL so we skip discovery.
	h.services[CardDAVServiceID].Endpoint = srv.URL + "/remote.php/dav/addressbooks/alice/default/"
	c := NewCardDAVConnector(h)
	// Warm collection cache with a positive TTL via resolve through list.
	contacts, _, err := c.ListUserContacts(context.Background())
	if err != nil {
		t.Fatalf("ListUserContacts: %v", err)
	}
	if len(contacts) != 1 {
		t.Fatalf("got %d contacts (propfind fallback)", len(contacts))
	}
}

func TestCalDAV_ListViaPropfind_WhenReportFails(t *testing.T) {
	m := newDAVMock()
	m.failQuery = true
	m.vevents["/remote.php/dav/calendars/alice/personal/doctor.ics"] =
		"BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:dr\r\nSUMMARY:Doctor\r\nDTSTART:20260302T100000Z\r\nDTEND:20260302T103000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	m.etags["/remote.php/dav/calendars/alice/personal/doctor.ics"] = `"e1"`
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	h := testDAVHost(t, CalDAVServiceID, srv.URL)
	h.services[CalDAVServiceID].Endpoint = srv.URL + "/remote.php/dav/calendars/alice/personal/"
	c := NewCalDAVConnector(h)
	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	events, _, err := c.ListEventsWithWindow(context.Background(), start, end)
	if err != nil {
		t.Fatalf("ListEventsWithWindow: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events (propfind fallback)", len(events))
	}
}

func TestCardDAVConnector_DeleteContact(t *testing.T) {
	m := newDAVMock()
	path := "/remote.php/dav/addressbooks/alice/default/ada.vcf"
	m.vcards[path] = "BEGIN:VCARD\r\nFN:Ada\r\nEND:VCARD\r\n"
	m.etags[path] = `"e1"`
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	h := testDAVHost(t, CardDAVServiceID, srv.URL)
	c := NewCardDAVConnector(h)
	if err := c.DeleteContact(context.Background(), srv.URL+path, `"e1"`); err != nil {
		t.Fatalf("DeleteContact: %v", err)
	}
}

func TestSanitizeHrefName_AndContactCardName(t *testing.T) {
	if got := sanitizeHrefName("Ada Lovelace <ada@example.com>"); got == "" {
		t.Fatal("empty sanitize")
	}
	c := types.NewContact("dav-contacts", "DAV Contacts", "x", "Ada Lovelace", []string{"ada@example.com"}, nil)
	name := contactCardName(c)
	if !strings.HasSuffix(name, ".vcf") {
		t.Fatalf("name = %q", name)
	}
}

func TestEncodeICalendar_RoundTrip(t *testing.T) {
	ev := ICalEvent{
		UID:         "ev-9",
		Summary:     "Standup",
		Description: "Daily",
		Location:    "Room A",
		Status:      "confirmed",
		Start:       time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC),
		End:         time.Date(2026, 3, 2, 9, 15, 0, 0, time.UTC),
		Organizer:   "lead@example.com",
		OrganizerCN: "Lead",
		Attendees:   []ICalAttendee{{Email: "me@example.com", CN: "Me"}},
	}
	body := EncodeICalendar(ev)
	parsed := ParseICalendar(body)
	if len(parsed) != 1 {
		t.Fatalf("parsed = %+v", parsed)
	}
	if parsed[0].Summary != "Standup" || parsed[0].UID != "ev-9" {
		t.Fatalf("roundtrip = %+v", parsed[0])
	}
}

func TestDavPutIfMatch_CreateUsesIfNoneMatch(t *testing.T) {
	var saw string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		saw = r.Header.Get("If-None-Match")
		if r.Header.Get("If-Match") != "" {
			t.Errorf("unexpected If-Match on create")
		}
		w.Header().Set("ETag", `"n1"`)
		w.WriteHeader(http.StatusCreated)
	}))
	defer ts.Close()
	etag, err := davPutIfMatch(context.Background(), ts.URL+"/x.vcf", "", "text/vcard", "BEGIN:VCARD\r\nEND:VCARD\r\n", davAuth{})
	if err != nil || etag != `"n1"` {
		t.Fatalf("etag=%q err=%v", etag, err)
	}
	if saw != "*" {
		t.Fatalf("If-None-Match = %q", saw)
	}
}

func TestExpandICalEvent_Monthly(t *testing.T) {
	ev := ICalEvent{
		UID:   "m",
		Start: time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 1, 15, 13, 0, 0, 0, time.UTC),
		RRule: "FREQ=MONTHLY;COUNT=3",
	}
	got := ExpandICalEvent(ev, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC))
	if len(got) != 3 {
		t.Fatalf("got %d", len(got))
	}
}


func TestDavCollectionAndResourceURL(t *testing.T) {
	if got := davCollectionURL("https://x/a"); got != "https://x/a/" {
		t.Fatalf("collection = %q", got)
	}
	if got := davCollectionURL("https://x/a/"); got != "https://x/a/" {
		t.Fatalf("collection slash = %q", got)
	}
	if got := davCollectionURL(""); got != "" {
		t.Fatalf("empty = %q", got)
	}
	if got := davResourceURL("https://x/a/", "b.vcf"); got != "https://x/a/b.vcf" {
		t.Fatalf("resource = %q", got)
	}
}

func TestContactCardName_Variants(t *testing.T) {
	c := types.NewContact("s", "S", "", "Ada Lovelace", nil, nil)
	if n := contactCardName(c); !strings.Contains(n, "Ada") && !strings.Contains(n, "ada") {
		t.Fatalf("name = %q", n)
	}
	c = types.NewContact("s", "S", "", "", []string{"bob@example.com"}, nil)
	if n := contactCardName(c); !strings.Contains(n, "bob") {
		t.Fatalf("email name = %q", n)
	}
	c = types.NewContact("s", "S", "uid-9", "X", nil, nil)
	if n := contactCardName(c); !strings.HasPrefix(n, "uid-9") {
		t.Fatalf("uid name = %q", n)
	}
}


func TestDavDiscover_WellKnownAndWalk(t *testing.T) {
	// Full discovery walk: well-known redirect → principal → home-set → collection.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _, ok := r.BasicAuth()
		if !ok || user == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		path := r.URL.Path
		xmlHdr := `<?xml version="1.0" encoding="UTF-8"?><d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:carddav" xmlns:a="urn:ietf:params:xml:ns:caldav">`
		switch {
		case strings.HasSuffix(path, "/.well-known/carddav"):
			http.Redirect(w, r, "/dav/", http.StatusTemporaryRedirect)
		case (path == "/dav/" || path == "/dav") && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
		case r.Method == "PROPFIND" && (path == "/dav/" || path == "/dav"):
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(xmlHdr + `<d:response><d:href>/dav/</d:href><d:propstat><d:prop>
<d:current-user-principal><d:href>/dav/principals/alice/</d:href></d:current-user-principal>
<c:addressbook-home-set><d:href>/dav/addressbooks/alice/</d:href></c:addressbook-home-set>
<a:calendar-home-set><d:href>/dav/calendars/alice/</d:href></a:calendar-home-set>
<d:resourcetype><d:collection/></d:resourcetype>
</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`))
		case r.Method == "PROPFIND" && strings.Contains(path, "principals"):
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(xmlHdr + `<d:response><d:href>` + path + `</d:href><d:propstat><d:prop>
<c:addressbook-home-set><d:href>/dav/addressbooks/alice/</d:href></c:addressbook-home-set>
</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`))
		case r.Method == "PROPFIND" && strings.Contains(path, "addressbooks"):
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(xmlHdr + `<d:response><d:href>/dav/addressbooks/alice/default/</d:href><d:propstat><d:prop>
<d:resourcetype><d:collection/><c:addressbook/></d:resourcetype>
</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	auth := davAuth{Username: "u", Password: "p"}
	col, err := davDiscover(context.Background(), ts.URL, "carddav", auth)
	if err != nil {
		t.Fatalf("davDiscover: %v", err)
	}
	if !strings.Contains(col, "addressbooks") {
		t.Fatalf("collection = %q", col)
	}
}

func TestParseVCard_EmptyAndNick(t *testing.T) {
	c := ParseVCard("s", "S", "x.vcf", "BEGIN:VCARD\r\nVERSION:3.0\r\nNICKNAME:ada\r\nEND:VCARD\r\n", "")
	if c.Name != "ada" {
		t.Fatalf("nick name = %q", c.Name)
	}
	c = ParseVCard("s", "S", "x.vcf", "BEGIN:VCARD\r\nEND:VCARD\r\n", "")
	if c.Name != "" {
		t.Fatalf("empty = %+v", c)
	}
}
