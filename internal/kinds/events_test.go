package kinds

import (
	"context"
	"testing"

	"github.com/fudomunro/aegis/internal/shared"
)

// TestFreeBusyListFromEvents_OmitsCancelled pins the freebusy encode
// (issue #128): cancelled events never block time, all-day blocks survive.
func TestFreeBusyListFromEvents_OmitsCancelled(t *testing.T) {
	full := NewCalendarEvent("google-calendar", "Google Calendar", "e1", "Doctor appointment", "2026-03-02T10:00:00Z", "2026-03-02T10:30:00Z", false)
	cancelled := NewCalendarEvent("google-calendar", "Google Calendar", "e2", "Cancelled standup", "2026-03-02T15:00:00Z", "2026-03-02T15:15:00Z", false)
	cancelled.Status = "cancelled"
	allday := NewCalendarEvent("google-calendar", "Google Calendar", "e3", "Offsite", "2026-03-05T00:00:00Z", "2026-03-06T00:00:00Z", true)
	events := []CalendarEvent{full, cancelled, allday}

	list := freeBusyListFromEvents(events, PageRequest{Limit: MaxPageLimit}, nil)
	if list.Total != 2 || len(list.Busy) != 2 {
		t.Fatalf("total = %d busy = %+v", list.Total, list.Busy)
	}
	for _, b := range list.Busy {
		if b.StartAt == "" {
			t.Fatalf("empty block: %+v", b)
		}
	}
	if !list.Busy[1].AllDay {
		t.Fatalf("expected all-day block: %+v", list.Busy[1])
	}
}

// TestProjectCalendarEventFreeBusy_StripsContentKeepsIdentity pins the copy
// projection (issue #145): the copy carries busy time only — no title,
// description, attendees, organizer, or location — while cross-service
// identity and the target ref survive so re-runs match instead of duplicating.
func TestProjectCalendarEventFreeBusy_StripsContentKeepsIdentity(t *testing.T) {
	ev := NewCalendarEvent("microsoft-calendar", "Microsoft Calendar", "ev1", "Comp review", "2026-03-02T10:00:00Z", "2026-03-02T11:00:00Z", false)
	ev.Description = "secret notes"
	ev.Location = "Room 4"
	ev.EntityID = "ical-9"
	ev.Organizer = &Contact{Name: "Ada"}
	ev.Attendees = []Contact{{Name: "Grace"}}
	ev.Sources = []CalendarEventSource{{
		Service: "google-calendar", ID: "g1",
		Extra: map[string]any{"etag": "e1", "ical_uid": "ical-9"},
	}}

	got := ProjectCalendarEventFreeBusy(ev)
	if got.Title != "" || got.Description != "" || got.Location != "" {
		t.Fatalf("content leaked: %+v", got)
	}
	if got.Organizer != nil || len(got.Attendees) != 0 {
		t.Fatalf("people leaked: %+v", got)
	}
	if got.StartAt != ev.StartAt || got.EndAt != ev.EndAt {
		t.Fatalf("times = %q..%q", got.StartAt, got.EndAt)
	}
	if got.EntityID != "ical-9" || len(got.Sources) != 1 || got.Sources[0].ID != "g1" {
		t.Fatalf("identity lost: %+v", got)
	}
}

// recordingEventWriter captures what PlaceEvents received.
type recordingEventWriter struct {
	got []CalendarEvent
}

func (r *recordingEventWriter) ListCalendarEvents(context.Context) ([]CalendarEvent, bool, error) {
	return nil, false, nil
}

func (r *recordingEventWriter) PlaceEvents(_ context.Context, events []CalendarEvent) (PlaceEntitiesResult, error) {
	r.got = append(r.got, events...)
	return PlaceEntitiesResult{Copied: len(events)}, nil
}

// TestEventsConvergePlan_FreeBusyProjection pins the placement rule (issue
// #145): a member whose sync row declares the freebusy projection receives
// busy blocks; other members receive the full events.
func TestEventsConvergePlan_FreeBusyProjection(t *testing.T) {
	projected := &recordingEventWriter{}
	plain := &recordingEventWriter{}
	h := &fakeHost{
		connectors: map[string]any{"ms": projected, "google": plain},
		services: []ServiceView{
			{ID: "ms", SyncProjections: map[string]string{"events": shared.SyncProjectionFreeBusy}},
			{ID: "google"},
		},
		owner: shared.GrantMap{"events": shared.AccessWrite},
	}
	plan := eventsConvergePlan(h, "events")

	ev := NewCalendarEvent("hub", "Hub", "ev1", "Comp review", "2026-03-02T10:00:00Z", "2026-03-02T11:00:00Z", false)
	ev.EntityID = "ical-9"

	if _, err := plan.Place(context.Background(), "ms", "", "", batchOf([]CalendarEvent{ev})); err != nil {
		t.Fatalf("place projected: %v", err)
	}
	if _, err := plan.Place(context.Background(), "google", "", "", batchOf([]CalendarEvent{ev})); err != nil {
		t.Fatalf("place plain: %v", err)
	}

	if len(projected.got) != 1 || projected.got[0].Title != "" {
		t.Fatalf("projected member received %+v", projected.got)
	}
	if projected.got[0].EntityID != "ical-9" {
		t.Fatalf("projection dropped identity: %+v", projected.got[0])
	}
	if len(plain.got) != 1 || plain.got[0].Title != "Comp review" {
		t.Fatalf("plain member received %+v", plain.got)
	}
}
