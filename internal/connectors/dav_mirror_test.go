package connectors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/kinds"
)

// refContact builds a contact fixture carrying a service-local ref + etag.
func refContact(service, name, id, etag string, emails ...string) kinds.Contact {
	ct := kinds.NewContact(service, service, id, name, emails, nil)
	if etag != "" {
		ct.Sources[0].Extra = map[string]any{"etag": etag}
	}
	return ct
}

// refEvent builds an event fixture carrying a service-local ref + etag/uid.
func refEvent(service, title, id, etag, uid, startAt, endAt string) kinds.CalendarEvent {
	ev := kinds.NewCalendarEvent(service, service, id, title, startAt, endAt, false)
	extra := map[string]any{}
	if etag != "" {
		extra["etag"] = etag
	}
	if uid != "" {
		extra["uid"] = uid
	}
	if len(extra) > 0 {
		ev.Sources[0].Extra = extra
	}
	return ev
}

func TestCardDAVPlaceContacts_CreateUpdateAndConflict(t *testing.T) {
	var lastMatch string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		lastMatch = r.Header.Get("If-Match")
		if strings.HasSuffix(r.URL.Path, "/ada.vcf") {
			// The remote moved under us: If-Match fails.
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		w.Header().Set("ETag", `"put-1"`)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(ts.Close)

	th := testDAVHost(t, CardDAVServiceID, ts.URL+"/ab/")
	c := NewCardDAVConnector(th)

	base := ts.URL + "/ab/"
	contacts := []kinds.Contact{
		refContact(CardDAVServiceID, "Ada Lovelace", base+"ada.vcf", `"stale"`, "ada@example.com"),
		refContact(CardDAVServiceID, "Grace Hopper", base+"grace.vcf", `"old"`, "grace@example.com"),
	}
	res, err := c.PlaceContacts(context.Background(), contacts)
	if err != nil {
		t.Fatalf("PlaceContacts: %v", err)
	}
	if len(res.Conflicts) != 1 {
		t.Fatalf("conflicts = %+v copied=%d skips=%+v", res.Conflicts, res.Copied, res.Skips)
	}
	if !strings.Contains(res.Conflicts[0].Reason, "If-Match") {
		t.Fatalf("conflict reason = %q", res.Conflicts[0].Reason)
	}
	if res.Copied != 1 || len(res.Placed) != 1 {
		t.Fatalf("result = %+v", res)
	}
	if res.Placed[0].ExternalID != base+"grace.vcf" || res.Placed[0].ETag != `"put-1"` {
		t.Fatalf("placed = %+v", res.Placed[0])
	}
	if lastMatch != `"old"` {
		t.Fatalf("If-Match = %q", lastMatch)
	}
}

func TestCardDAVPlaceContacts_CreateCarriesUID(t *testing.T) {
	var body string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.NotFound(w, r)
			return
		}
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		body = string(buf)
		if r.Header.Get("If-None-Match") != "*" {
			t.Errorf("create must send If-None-Match: *, got %q", r.Header.Get("If-Match"))
		}
		w.Header().Set("ETag", `"put-1"`)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(ts.Close)

	th := testDAVHost(t, CardDAVServiceID, ts.URL+"/ab/")
	th.CacheSet("carddav:collection", ts.URL+"/ab/", time.Hour)
	c := NewCardDAVConnector(th)

	ct := refContact(CardDAVServiceID, "Ada Lovelace", "", "", "ada@example.com")
	ct.Sources[0].Extra = map[string]any{"uid": "uid-1"}
	res, err := c.PlaceContacts(context.Background(), []kinds.Contact{ct})
	if err != nil {
		t.Fatalf("PlaceContacts: %v", err)
	}
	if res.Copied != 1 || len(res.Skips) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(body, "uid-1") {
		t.Fatalf("vCard body must carry the uid: %s", body)
	}
	if res.Placed[0].ExternalID != "" {
		t.Fatalf("create with no ref must not stamp an external id: %+v", res.Placed[0])
	}
}

func TestCalDAVPlaceEvents_IfMatch(t *testing.T) {
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
	t.Cleanup(ts.Close)

	th := testDAVHost(t, CalDAVServiceID, ts.URL+"/cal/")
	th.services[CalDAVServiceID].Headers = map[string]string{"X-DAV-Username": "u", "X-DAV-Password": "p"}
	th.CacheSet("caldav:collection", ts.URL+"/cal/", time.Hour)
	c := NewCalDAVConnector(th)

	events := []kinds.CalendarEvent{
		refEvent(CalDAVServiceID, "S", "", `"stale"`, "u1", "2026-03-02T10:00:00Z", "2026-03-02T11:00:00Z"),
	}
	res, err := c.PlaceEvents(context.Background(), events)
	if err != nil {
		t.Fatalf("PlaceEvents: %v", err)
	}
	if len(res.Conflicts) != 1 {
		t.Fatalf("conflicts = %+v lastMatch=%q", res.Conflicts, lastMatch)
	}

	events[0].Sources[0].Extra["etag"] = `"ok"`
	res2, err := c.PlaceEvents(context.Background(), events)
	if err != nil || res2.Copied != 1 {
		t.Fatalf("copied=%d err=%v", res2.Copied, err)
	}
}

func TestParseRFC3339(t *testing.T) {
	ts, ok := parseRFC3339("2026-03-02T10:00:00Z")
	if !ok || !ts.Equal(time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("ts = %v ok = %v", ts, ok)
	}
	if _, ok := parseRFC3339("not-a-time"); ok {
		t.Fatal("expected failure")
	}
}

func TestPlaceEntityRefsAndUID(t *testing.T) {
	ct := kinds.NewContact("s", "S", "id-1", "N", nil, nil)
	ct.Sources[0].Extra = map[string]any{"etag": `"e"`, "uid": "u1"}
	if id, etag := placeContactRef(ct, "s"); id != "id-1" || etag != `"e"` {
		t.Fatalf("placeContactRef = %q %q", id, etag)
	}
	if kinds.ContactUID(ct) != "u1" {
		t.Fatalf("ContactUID = %q", kinds.ContactUID(ct))
	}
	if placeContactDisplay(kinds.Contact{Emails: []string{"a@x"}}) != "a@x" {
		t.Fatal("display must fall back to email")
	}

	ev := kinds.NewCalendarEvent("s", "S", "ev-1", "T", "2026-03-02T10:00:00Z", "2026-03-02T11:00:00Z", false)
	ev.Sources[0].Extra = map[string]any{"etag": `"x"`}
	if id, etag := placeEventRef(ev, "s"); id != "ev-1" || etag != `"x"` {
		t.Fatalf("placeEventRef = %q %q", id, etag)
	}
	if placeEventUID(ev) != "ev-1" {
		t.Fatalf("placeEventUID fallback = %q", placeEventUID(ev))
	}
	ev.EntityID = "ent"
	if placeEventUID(ev) != "ent" {
		t.Fatalf("placeEventUID entity = %q", placeEventUID(ev))
	}
	ical := kinds.NewCalendarEvent("s", "S", "ev-2", "T", "2026-03-02T10:00:00Z", "2026-03-02T11:00:00Z", false)
	ical.Sources[0].Extra = map[string]any{"ical_uid": "ical-9"}
	if placeEventUID(ical) != "ical-9" {
		t.Fatalf("placeEventUID ical_uid = %q", placeEventUID(ical))
	}
}
