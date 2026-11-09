package connectors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/kinds"
)

func TestGoogleContactsPlaceContacts_CreateAndConflict(t *testing.T) {
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
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)

	th := newTestHostWithService(GoogleContactsServiceID, ts.URL, "https://aegis.example.com")
	th.setToken(GoogleContactsServiceID, "tok")
	c := NewGoogleContactsConnector(th)

	// Update path with conflict.
	res, err := c.PlaceContacts(context.Background(), []kinds.Contact{
		refContact(GoogleContactsServiceID, "Ada", "people/c1", `"stale"`, "ada@example.com"),
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
		refContact(GoogleContactsServiceID, "Grace", "", "", "grace@example.com"),
	})
	if err != nil || res2.Copied != 1 {
		t.Fatalf("copied=%d err=%v result=%+v", res2.Copied, err, res2)
	}
	if res2.Placed[0].ExternalID != "people/n1" || res2.Placed[0].ETag != "e1" {
		t.Fatalf("placed = %+v", res2.Placed[0])
	}
}

func TestGoogleCalendarPlaceEvents_CreateAndConflict(t *testing.T) {
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
		_, _ = w.Write([]byte(`{"id":"ev-1","etag":"e2"}`))
	}))
	t.Cleanup(ts.Close)

	th := newTestHostWithService(GoogleCalendarServiceID, ts.URL, "https://aegis.example.com")
	th.setToken(GoogleCalendarServiceID, "tok")
	c := NewGoogleCalendarConnector(th)

	// Update path with conflict.
	res, err := c.PlaceEvents(context.Background(), []kinds.CalendarEvent{
		withCalendar(refEvent(GoogleCalendarServiceID, "Doctor", "ev-1", `"stale"`, "",
			"2026-03-02T10:00:00Z", "2026-03-02T10:30:00Z"), "primary"),
	})
	if err != nil {
		t.Fatalf("PlaceEvents: %v", err)
	}
	if len(res.Conflicts) != 1 {
		t.Fatalf("conflicts = %+v", res.Conflicts)
	}

	// Create path.
	res2, err := c.PlaceEvents(context.Background(), []kinds.CalendarEvent{
		withCalendar(refEvent(GoogleCalendarServiceID, "New", "", "", "",
			"2026-03-03T10:00:00Z", "2026-03-03T11:00:00Z"), "primary"),
	})
	if err != nil || res2.Copied != 1 {
		t.Fatalf("copied=%d err=%v result=%+v", res2.Copied, err, res2)
	}
	if res2.Placed[0].ExternalID != "ev-new" {
		t.Fatalf("placed = %+v", res2.Placed[0])
	}
}

func withCalendar(ev kinds.CalendarEvent, calendarID string) kinds.CalendarEvent {
	ev.CalendarID = calendarID
	return ev
}
