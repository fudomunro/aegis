package connectors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/kinds"
)

func TestMicrosoftContactsPlaceContacts_CreateAndConflict(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/me/contacts":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["displayName"] != "Grace Hopper" {
				t.Errorf("create body = %+v", body)
			}
			_, _ = w.Write([]byte(`{"id":"c-new","@odata.etag":"W/\"e1\""}`))
		case r.Method == http.MethodPatch:
			if r.Header.Get("If-Match") == `W/"stale"` {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			_, _ = w.Write([]byte(`{"id":"c1","@odata.etag":"W/\"e2\""}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)

	th := newTestHostWithService(MicrosoftContactsServiceID, ts.URL, "https://aegis.example.com")
	th.setToken(MicrosoftContactsServiceID, "tok")
	c := NewMicrosoftContactsConnector(th)

	// Update path with stale etag → conflict, never overwritten.
	res, err := c.PlaceContacts(context.Background(), []kinds.Contact{
		refContact(MicrosoftContactsServiceID, "Ada", "c1", `W/"stale"`, "ada@example.com"),
	})
	if err != nil {
		t.Fatalf("PlaceContacts: %v", err)
	}
	if len(res.Conflicts) != 1 {
		t.Fatalf("conflicts = %+v", res.Conflicts)
	}
	if !strings.Contains(res.Conflicts[0].Reason, "etag mismatch") {
		t.Fatalf("conflict reason = %q", res.Conflicts[0].Reason)
	}

	// Create path.
	res2, err := c.PlaceContacts(context.Background(), []kinds.Contact{
		refContact(MicrosoftContactsServiceID, "Grace Hopper", "", "", "grace@example.com"),
	})
	if err != nil || res2.Copied != 1 {
		t.Fatalf("copied=%d err=%v result=%+v", res2.Copied, err, res2)
	}
	if res2.Placed[0].ExternalID != "c-new" || res2.Placed[0].ETag != `W/"e1"` {
		t.Fatalf("placed = %+v", res2.Placed[0])
	}
}

func TestMicrosoftCalendarPlaceEvents_CreateUpdateAndConflict(t *testing.T) {
	var lastPath, lastIfMatch string
	var lastBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		lastPath = r.URL.Path
		lastIfMatch = r.Header.Get("If-Match")
		_ = json.NewDecoder(r.Body).Decode(&lastBody)
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"id":"ev-new","@odata.etag":"W/\"e1\""}`))
			return
		}
		if lastIfMatch == `W/"stale"` {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		_, _ = w.Write([]byte(`{"id":"ev1","@odata.etag":"W/\"e2\""}`))
	}))
	t.Cleanup(ts.Close)

	th := newTestHostWithService(MicrosoftCalendarServiceID, ts.URL, "https://aegis.example.com")
	th.setToken(MicrosoftCalendarServiceID, "tok")
	c := NewMicrosoftCalendarConnector(th)

	// Update path with conflict.
	res, err := c.PlaceEvents(context.Background(), []kinds.CalendarEvent{
		refEvent(MicrosoftCalendarServiceID, "Design review", "ev1", `W/"stale"`, "ical-1", "2026-01-05T09:00:00Z", "2026-01-05T10:00:00Z"),
	})
	if err != nil {
		t.Fatalf("PlaceEvents: %v", err)
	}
	if len(res.Conflicts) != 1 {
		t.Fatalf("conflicts = %+v", res.Conflicts)
	}

	// Create path lands on the default calendar (no Microsoft source calendar).
	ev := refEvent(MicrosoftCalendarServiceID, "New block", "", "", "ical-2", "2026-01-06T09:00:00Z", "2026-01-06T10:00:00Z")
	res2, err := c.PlaceEvents(context.Background(), []kinds.CalendarEvent{ev})
	if err != nil || res2.Copied != 1 {
		t.Fatalf("copied=%d err=%v result=%+v", res2.Copied, err, res2)
	}
	if lastPath != "/me/events" {
		t.Fatalf("create path = %q, want /me/events", lastPath)
	}
	if res2.Placed[0].ExternalID != "ev-new" {
		t.Fatalf("placed = %+v", res2.Placed[0])
	}
	// Graph times are written as UTC dateTime + timeZone.
	start, _ := lastBody["start"].(map[string]any)
	if start == nil || start["timeZone"] != "UTC" || start["dateTime"] != "2026-01-06T09:00:00" {
		t.Fatalf("start = %+v", lastBody["start"])
	}
}

func TestMicrosoftCalendarPlaceEvents_ForeignCalendarNeverTrusted(t *testing.T) {
	var lastPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		lastPath = r.URL.Path
		_, _ = w.Write([]byte(`{"id":"ev-new","@odata.etag":"W/\"e1\""}`))
	}))
	t.Cleanup(ts.Close)

	th := newTestHostWithService(MicrosoftCalendarServiceID, ts.URL, "https://aegis.example.com")
	th.setToken(MicrosoftCalendarServiceID, "tok")
	c := NewMicrosoftCalendarConnector(th)

	// A Google-sourced event carries a Google calendar id — the Microsoft
	// target must fall back to its own default calendar instead.
	foreign := refEvent(GoogleCalendarServiceID, "Busy block", "", "", "ical-1", "2026-01-05T09:00:00Z", "2026-01-05T10:00:00Z")
	foreign.Sources[0].CalendarID = "primary"
	if _, err := c.PlaceEvents(context.Background(), []kinds.CalendarEvent{foreign}); err != nil {
		t.Fatalf("PlaceEvents: %v", err)
	}
	if lastPath != "/me/events" {
		t.Fatalf("path = %q, want /me/events (default calendar)", lastPath)
	}

	// A Microsoft-sourced event keeps its own service-local calendar.
	own := refEvent(MicrosoftCalendarServiceID, "Busy block", "", "", "ical-2", "2026-01-05T11:00:00Z", "2026-01-05T12:00:00Z")
	own.Sources[0].CalendarID = "cal-7"
	if _, err := c.PlaceEvents(context.Background(), []kinds.CalendarEvent{own}); err != nil {
		t.Fatalf("PlaceEvents: %v", err)
	}
	if lastPath != "/me/calendars/cal-7/events" {
		t.Fatalf("path = %q, want /me/calendars/cal-7/events", lastPath)
	}
}

func TestGoogleCalendarPlaceEvents_ForeignCalendarFallsBackToPrimary(t *testing.T) {
	var lastPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		lastPath = r.URL.Path
		_, _ = w.Write([]byte(`{"id":"ev-new","etag":"e1"}`))
	}))
	t.Cleanup(ts.Close)

	th := newTestHostWithService(GoogleCalendarServiceID, ts.URL, "https://aegis.example.com")
	th.setToken(GoogleCalendarServiceID, "tok")
	c := NewGoogleCalendarConnector(th)

	foreign := refEvent(MicrosoftCalendarServiceID, "Busy block", "", "", "ical-1", "2026-01-05T09:00:00Z", "2026-01-05T10:00:00Z")
	foreign.Sources[0].CalendarID = "AQMkADAw"
	if _, err := c.PlaceEvents(context.Background(), []kinds.CalendarEvent{foreign}); err != nil {
		t.Fatalf("PlaceEvents: %v", err)
	}
	if lastPath != "/calendars/primary/events" {
		t.Fatalf("path = %q, want /calendars/primary/events", lastPath)
	}
}
