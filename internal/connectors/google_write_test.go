package connectors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

func TestGoogleContacts_CreateAndUpdateContact(t *testing.T) {
	var sawPath, sawIfMatch string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		sawIfMatch = r.Header.Get("If-Match")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, ":createContact"):
			_, _ = w.Write([]byte(`{"resourceName":"people/new1","etag":"e-new","names":[{"displayName":"Ada Lovelace"}]}`))
		case strings.Contains(r.URL.Path, ":updateContact"):
			if sawIfMatch == `"stale"` {
				w.WriteHeader(http.StatusConflict)
				return
			}
			_, _ = w.Write([]byte(`{"resourceName":"people/c9","etag":"e-updated"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	h := newTestHostWithService(GoogleContactsServiceID, ts.URL, "https://aegis.example.com")
	h.setToken(GoogleContactsServiceID, "tok")
	c := NewGoogleContactsConnector(h)

	contact := types.NewContact(GoogleContactsServiceID, GoogleContactsServiceName,
		"", "Ada Lovelace", []string{"ada@example.com"}, []string{"+15550100"})
	contact.Organization = "Engines"

	id, etag, err := c.CreateGoogleContact(context.Background(), contact)
	if err != nil || id != "people/new1" || etag != "e-new" {
		t.Fatalf("create = %q %q %v", id, etag, err)
	}

	// Conflict path.
	_, err = c.UpdateGoogleContact(context.Background(), "people/c9", `"stale"`, contact)
	if err != ErrConflict {
		t.Fatalf("err = %v, want ErrConflict", err)
	}

	// Success path.
	etag, err = c.UpdateGoogleContact(context.Background(), "people/c9", `"ok"`, contact)
	if err != nil || etag != "e-updated" {
		t.Fatalf("update = %q %v", etag, err)
	}
	if !strings.Contains(sawPath, "people/c9:updateContact") {
		t.Fatalf("path = %q", sawPath)
	}
}

func TestGoogleCalendar_CreateAndUpdateEvent(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"id":"ev-new","htmlLink":"https://cal","etag":"e1"}`))
			return
		}
		if r.Header.Get("If-Match") == `"stale"` {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		_, _ = w.Write([]byte(`{"id":"ev-1","etag":"e2"}`))
	}))
	defer ts.Close()

	h := newTestHostWithService(GoogleCalendarServiceID, ts.URL, "https://aegis.example.com")
	h.setToken(GoogleCalendarServiceID, "tok")
	c := NewGoogleCalendarConnector(h)

	ev := types.NewCalendarEvent(GoogleCalendarServiceID, GoogleCalendarServiceName,
		"", "Doctor", "2026-03-02T10:00:00Z", "2026-03-02T10:30:00Z", false)
	id, link, etag, err := c.CreateGoogleEvent(context.Background(), "primary", ev)
	if err != nil || id != "ev-new" || etag != "e1" || link == "" {
		t.Fatalf("create = %q %q %q %v", id, link, etag, err)
	}

	_, err = c.UpdateGoogleEvent(context.Background(), "primary", "ev-1", `"stale"`, ev)
	if err != ErrConflict {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	etag, err = c.UpdateGoogleEvent(context.Background(), "primary", "ev-1", `"ok"`, ev)
	if err != nil || etag != "e2" {
		t.Fatalf("update = %q %v", etag, err)
	}
}

func TestGoogleCalendar_CreateAllDayEvent(t *testing.T) {
	var body map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ev-d","etag":"e"}`))
	}))
	defer ts.Close()
	h := newTestHostWithService(GoogleCalendarServiceID, ts.URL, "https://aegis.example.com")
	h.setToken(GoogleCalendarServiceID, "tok")
	c := NewGoogleCalendarConnector(h)
	ev := types.NewCalendarEvent(GoogleCalendarServiceID, GoogleCalendarServiceName,
		"", "Offsite", "2026-03-05T00:00:00Z", "2026-03-06T00:00:00Z", true)
	if _, _, _, err := c.CreateGoogleEvent(context.Background(), "", ev); err != nil {
		t.Fatalf("CreateGoogleEvent: %v", err)
	}
	start, _ := body["start"].(map[string]any)
	if start == nil || start["date"] != "2026-03-05" {
		t.Fatalf("start = %+v", body["start"])
	}
}

func TestMustJSON_AndContactETag(t *testing.T) {
	if mustJSON(map[string]int{"a": 1}) != `{"a":1}` {
		t.Fatal("mustJSON")
	}
	if mustJSON(make(chan int)) != "{}" {
		t.Fatal("mustJSON fallback")
	}
	c := types.NewContact("s", "S", "id", "N", nil, nil)
	c.Sources[0].Extra = map[string]any{"etag": `"e"`}
	if contactETag(c) != `"e"` {
		t.Fatalf("etag = %q", contactETag(c))
	}
	if contactETag(types.Contact{}) != "" {
		t.Fatal("empty etag")
	}
}

func TestParseICalUntil_AndEventETag(t *testing.T) {
	if _, ok := parseICalUntil("20260302T100000Z"); !ok {
		t.Fatal("until parse")
	}
	if _, ok := parseICalUntil("20260302"); !ok {
		t.Fatal("until date")
	}
	if _, ok := parseICalUntil("bogus"); ok {
		t.Fatal("until bogus")
	}
	if eventETag(ICalEvent{ETag: `"x"`}) != `"x"` {
		t.Fatal("eventETag")
	}
}

func TestParseICalTime_Fallbacks(t *testing.T) {
	// Second return is allDay, not success — check zero time for parse failure.
	if ts, allDay := parseICalTime("2026-03-02T10:00:00Z", nil); ts.IsZero() || allDay {
		t.Fatalf("rfc3339 ts=%v allDay=%v", ts, allDay)
	}
	if ts, allDay := parseICalTime("20260302T100000", nil); ts.IsZero() || allDay {
		t.Fatalf("floating ts=%v allDay=%v", ts, allDay)
	}
	if ts, allDay := parseICalTime("20260302", nil); ts.IsZero() || !allDay {
		t.Fatalf("date ts=%v allDay=%v", ts, allDay)
	}
	if ts, _ := parseICalTime("", nil); !ts.IsZero() {
		t.Fatal("empty")
	}
	if ts, _ := parseICalTime("bogus", nil); !ts.IsZero() {
		t.Fatal("bogus")
	}
}

func TestCalDAVConnector_PutEvent_IfMatchConflict(t *testing.T) {
	var gotMatch string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			// discovery PROPFIND
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:" xmlns:a="urn:ietf:params:xml:ns:caldav">
<d:response><d:href>/cal/</d:href><d:propstat><d:prop>
<d:resourcetype><d:collection/><a:calendar/></d:resourcetype>
</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`))
			return
		}
		gotMatch = r.Header.Get("If-Match")
		if gotMatch == `"stale"` {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		w.Header().Set("ETag", `"n"`)
		w.WriteHeader(http.StatusCreated)
	}))
	defer ts.Close()

	h := newTestHostWithService(CalDAVServiceID, ts.URL+"/cal/", "https://aegis.example.com")
	h.services[CalDAVServiceID].Headers = map[string]string{"X-DAV-Username": "u", "X-DAV-Password": "p"}
	c := NewCalDAVConnector(h)

	ev := ICalEvent{UID: "ev-1", Summary: "S", Start: time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC), End: time.Date(2026, 3, 2, 11, 0, 0, 0, time.UTC)}
	_, err := c.PutEvent(context.Background(), ts.URL+"/cal/ev-1.ics", `"stale"`, ev)
	if err != ErrConflict {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	etag, err := c.PutEvent(context.Background(), ts.URL+"/cal/ev-1.ics", `"ok"`, ev)
	if err != nil || etag != `"n"` {
		t.Fatalf("etag=%q err=%v", etag, err)
	}
}

func TestDAVHelpers_AbsoluteAndPropstat(t *testing.T) {
	if got := davAbsolute("https://x/a/", "b/c"); got != "https://x/a/b/c" {
		t.Fatalf("absolute = %q", got)
	}
	if got := davAbsolute("https://x/a/", "https://y/z"); got != "https://y/z" {
		t.Fatalf("absolute abs = %q", got)
	}
	prop, ok := propstatOK(nil)
	if ok || prop.GetETag != "" {
		t.Fatalf("empty propstat = %+v %v", prop, ok)
	}
	prop, ok = propstatOK([]davPropstat{{Prop: davProp{GetETag: `"e"`}, Status: "HTTP/1.1 200 OK"}})
	if !ok || prop.GetETag != `"e"` {
		t.Fatalf("propstat = %+v %v", prop, ok)
	}
}
