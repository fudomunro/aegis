package connectors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/kinds"
)

func TestMicrosoftCalendar_ImplementsCalendarEventProvider(t *testing.T) {
	var _ kinds.CalendarEventProvider = NewMicrosoftCalendarConnector(newTestHost(""))
	var _ kinds.CalendarEventWriteProvider = NewMicrosoftCalendarConnector(newTestHost(""))
}

func TestMicrosoftCalendar_ListCalendarEvents_MapsNeutral(t *testing.T) {
	page := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		page++
		if page == 1 {
			if !strings.Contains(r.URL.RawQuery, "startDateTime=") {
				t.Errorf("calendarView missing window: %s", r.URL.RawQuery)
			}
			next := "http://" + r.Host + "/me/calendarView?%24skiptoken=2"
			_, _ = w.Write([]byte(`{"value":[
				{"id":"ev1","iCalUId":"ical-1","@odata.etag":"W/\"e1\"",
				 "subject":"Design review","bodyPreview":"Bring the spec",
				 "start":{"dateTime":"2026-01-05T09:00:00.0000000","timeZone":"UTC"},
				 "end":{"dateTime":"2026-01-05T10:00:00.0000000","timeZone":"UTC"},
				 "isAllDay":false,"isCancelled":false,"showAs":"busy",
				 "location":{"displayName":"Room 4"},
				 "organizer":{"emailAddress":{"name":"Ada","address":"ada@example.com"}},
				 "attendees":[{"emailAddress":{"name":"Grace","address":"grace@example.com"},"status":{"response":"accepted"}}],
				 "webLink":"https://outlook.example.com/ev1","type":"singleInstance"},
				{"id":"ev2","iCalUId":"ical-2",
				 "start":{"dateTime":"2026-01-06T00:00:00.0000000","timeZone":"UTC"},
				 "end":{"dateTime":"2026-01-07T00:00:00.0000000","timeZone":"UTC"},
				 "isAllDay":true,"isCancelled":false,"showAs":"tentative","subject":"Offsite"}
			],"@odata.nextLink":"` + next + `"}`))
			return
		}
		_, _ = w.Write([]byte(`{"value":[
			{"id":"ev3","iCalUId":"ical-3","subject":"Cancelled sync",
			 "start":{"dateTime":"2026-01-08T14:00:00.0000000","timeZone":"UTC"},
			 "end":{"dateTime":"2026-01-08T14:30:00.0000000","timeZone":"UTC"},
			 "isCancelled":true,"showAs":"busy"}
		]}`))
	}))
	t.Cleanup(ts.Close)

	th := newTestHostWithService(MicrosoftCalendarServiceID, ts.URL, "https://aegis.example.com")
	th.setToken(MicrosoftCalendarServiceID, "tok")
	c := NewMicrosoftCalendarConnector(th)

	events, truncated, err := c.ListCalendarEvents(context.Background())
	if err != nil {
		t.Fatalf("ListCalendarEvents: %v", err)
	}
	if truncated {
		t.Fatal("expected truncated=false")
	}
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3: %+v", len(events), events)
	}

	ev := events[0]
	if ev.Title != "Design review" || ev.Description != "Bring the spec" {
		t.Fatalf("ev1 = %+v", ev)
	}
	if ev.StartAt != "2026-01-05T09:00:00Z" || ev.EndAt != "2026-01-05T10:00:00Z" {
		t.Fatalf("ev1 times = %q..%q", ev.StartAt, ev.EndAt)
	}
	if ev.Location != "Room 4" || ev.Status != "confirmed" {
		t.Fatalf("ev1 location/status = %q/%q", ev.Location, ev.Status)
	}
	// iCalUId is the durable cross-service id for merge with Google.
	if ev.EntityID != "ical-1" {
		t.Fatalf("ev1 entity = %q", ev.EntityID)
	}
	if ev.Sources[0].Extra["ical_uid"] != "ical-1" || ev.Sources[0].Extra["etag"] != `W/"e1"` {
		t.Fatalf("ev1 source extra = %v", ev.Sources[0].Extra)
	}
	if ev.Organizer == nil || ev.Organizer.Emails[0] != "ada@example.com" {
		t.Fatalf("ev1 organizer = %+v", ev.Organizer)
	}
	if len(ev.Attendees) != 1 || ev.Attendees[0].Name != "Grace" {
		t.Fatalf("ev1 attendees = %+v", ev.Attendees)
	}

	if !events[1].AllDay || events[1].Status != "tentative" {
		t.Fatalf("ev2 = %+v", events[1])
	}
	if events[2].Status != "cancelled" {
		t.Fatalf("ev3 status = %q", events[2].Status)
	}
}

func TestMicrosoftCalendar_ListCalendarEvents_Unauthorized(t *testing.T) {
	th := newTestHostWithService(MicrosoftCalendarServiceID, "https://graph.example.com", "https://aegis.example.com")
	c := NewMicrosoftCalendarConnector(th)
	if _, _, err := c.ListCalendarEvents(context.Background()); err != kinds.ErrUnauthorized {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestMicrosoftCalendar_TimesRoundTrip(t *testing.T) {
	got := microsoftTimeToRFC3339(microsoftTime{DateTime: "2026-01-05T09:00:00.0000000", TimeZone: "UTC"})
	if got != "2026-01-05T09:00:00Z" {
		t.Fatalf("parse = %q", got)
	}
	// Offset form (no Prefer applied) normalizes to UTC.
	got = microsoftTimeToRFC3339(microsoftTime{DateTime: "2026-01-05T09:00:00+02:00"})
	if got != "2026-01-05T07:00:00Z" {
		t.Fatalf("offset parse = %q", got)
	}
	back := rfc3339ToMicrosoftTime("2026-01-05T09:00:00Z")
	if back.DateTime != "2026-01-05T09:00:00" || back.TimeZone != "UTC" {
		t.Fatalf("format = %+v", back)
	}
}

func TestMicrosoftCalendar_StatusMapping(t *testing.T) {
	if microsoftEventStatus("busy", false) != "confirmed" {
		t.Fatal("busy should map to confirmed")
	}
	if microsoftEventStatus("tentative", false) != "tentative" {
		t.Fatal("tentative should map to tentative")
	}
	if microsoftEventStatus("free", true) != "cancelled" {
		t.Fatal("cancelled wins")
	}
	cancelled, _ := graphStatusToMicrosoft("cancelled")
	if !cancelled {
		t.Fatal("cancelled should map to isCancelled")
	}
	if _, showAs := graphStatusToMicrosoft("tentative"); showAs != "tentative" {
		t.Fatalf("showAs = %q", showAs)
	}
}

func TestMicrosoftCalendar_SystemInstructions_MentionLibraryOnly(t *testing.T) {
	c := NewMicrosoftCalendarConnector(newTestHost(""))
	s := c.SystemInstructions("https://aegis.example.com")
	for _, want := range []string{"/v1/library/events", "/v1/library/freebusy", "Microsoft"} {
		if !strings.Contains(s, want) {
			t.Fatalf("instructions missing %q: %s", want, s)
		}
	}
	if strings.Contains(s, "#145") {
		t.Fatalf("instructions leak issue ref: %s", s)
	}
}

func TestMicrosoftCalendar_ListCalendarEventsDelta(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("startDateTime") == "" && r.URL.Query().Get("$deltatoken") == "" {
			t.Errorf("fresh delta missing window: %s", r.URL.RawQuery)
		}
		if r.URL.Query().Get("$deltatoken") == "t1" {
			_, _ = w.Write([]byte(`{"value":[],"@odata.deltaLink":"DELTA2"}`))
			return
		}
		_, _ = w.Write([]byte(`{"value":[
			{"id":"ev1","iCalUId":"ical-1","subject":"Design review",
			 "start":{"dateTime":"2026-01-05T09:00:00.0000000","timeZone":"UTC"},
			 "end":{"dateTime":"2026-01-05T10:00:00.0000000","timeZone":"UTC"}},
			{"id":"ev9","@removed":{"reason":"deleted"}}
		],"@odata.deltaLink":"http://` + r.Host + `/me/calendarView/delta?%24deltatoken=t1"}`))
	}))
	t.Cleanup(ts.Close)

	th := newTestHostWithService(MicrosoftCalendarServiceID, ts.URL, "https://aegis.example.com")
	th.setToken(MicrosoftCalendarServiceID, "tok")
	c := NewMicrosoftCalendarConnector(th)

	events, next, truncated, err := c.ListCalendarEventsDelta(context.Background(), "")
	if err != nil {
		t.Fatalf("delta: %v", err)
	}
	if truncated || !strings.Contains(next, "deltatoken=t1") {
		t.Fatalf("next = %q truncated = %v", next, truncated)
	}
	if len(events) != 1 || events[0].EntityID != "ical-1" {
		t.Fatalf("events = %+v, want 1 change (deletion dropped)", events)
	}

	// Resumed feed.
	events, next, _, err = c.ListCalendarEventsDelta(context.Background(), next)
	if err != nil {
		t.Fatalf("resumed delta: %v", err)
	}
	if len(events) != 0 || next != "DELTA2" {
		t.Fatalf("resumed events=%+v next=%q", events, next)
	}
}
