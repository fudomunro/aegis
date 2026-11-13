package kinds

import (
	"fmt"
	"testing"
	"time"
)

func TestCalendarEventIdentityKey_PrefersICalUID(t *testing.T) {
	e := NewCalendarEvent("google-calendar", "Google Calendar", "id1", "Standup", "2024-06-01T09:00:00Z", "2024-06-01T09:30:00Z", false)
	e.EntityID = "uid@google.com"
	if got := CalendarEventIdentityKey(e); got != "ical:uid@google.com|start:2024-06-01T09:00:00Z" {
		t.Fatalf("key = %q", got)
	}
}

func TestCalendarEventIdentityKey_ReadsBothExtraSpellings(t *testing.T) {
	// Connectors spell the durable UID two ways in source extras; either
	// spelling establishes the same identity.
	for _, key := range []string{"ical_uid", "uid"} {
		e := NewCalendarEvent("dav", "DAV", "id1", "Standup", "2024-06-01T09:00:00Z", "2024-06-01T09:30:00Z", false)
		e.Sources[0].Extra = map[string]any{key: "uid@dav"}
		if got := CalendarEventIdentityKey(e); got != "ical:uid@dav|start:2024-06-01T09:00:00Z" {
			t.Fatalf("key via %s = %q", key, got)
		}
	}
}

func TestCalendarEventIdentityKey_TitleStartFallback(t *testing.T) {
	e := NewCalendarEvent("google-calendar", "Google Calendar", "id1", "  Standup  ", "2024-06-01T09:00:00+00:00", "2024-06-01T09:30:00Z", false)
	if got := CalendarEventIdentityKey(e); got != "title:standup|start:2024-06-01T09:00:00Z|allday:0" {
		t.Fatalf("key = %q", got)
	}
}

func TestMergeCalendarEvents_ByICalUIDAndTitleStart(t *testing.T) {
	a := NewCalendarEvent("google-calendar", "Google Calendar", "a1", "Standup", "2024-06-01T09:00:00Z", "2024-06-01T09:30:00Z", false)
	a.EntityID = "shared@ical"
	b := NewCalendarEvent("outlook-calendar", "Outlook Calendar", "b1", "Different title", "2024-06-01T09:00:00Z", "2024-06-01T09:30:00Z", false)
	b.EntityID = "shared@ical"
	c := NewCalendarEvent("google-calendar", "Google Calendar", "c1", "Planning", "2024-06-02T14:00:00Z", "2024-06-02T15:00:00Z", false)
	d := NewCalendarEvent("outlook-calendar", "Outlook Calendar", "d1", "Planning", "2024-06-02T14:00:00+00:00", "2024-06-02T15:00:00Z", false)

	merged := MergeCalendarEvents([]CalendarEvent{a, b, c, d})
	if len(merged) != 2 {
		t.Fatalf("merged = %d events: %+v", len(merged), merged)
	}
	// Sorted by start: Standup first, Planning second.
	if merged[0].Title != "Standup" || len(merged[0].Sources) != 2 || merged[0].Match != "ical_uid" {
		t.Fatalf("first = %+v", merged[0])
	}
	if merged[1].Title != "Planning" || len(merged[1].Sources) != 2 || merged[1].Match != "title_start" {
		t.Fatalf("second = %+v", merged[1])
	}
}

func TestMergeCalendarEvents_DistinctWhenDifferentStart(t *testing.T) {
	a := NewCalendarEvent("google-calendar", "Google Calendar", "a", "Meeting", "2024-06-01T09:00:00Z", "", false)
	b := NewCalendarEvent("google-calendar", "Google Calendar", "b", "Meeting", "2024-06-01T10:00:00Z", "", false)
	merged := MergeCalendarEvents([]CalendarEvent{a, b})
	if len(merged) != 2 {
		t.Fatalf("merged = %d, want 2", len(merged))
	}
}

func TestMergeCalendarEvents_MergesAttendeesAsContacts(t *testing.T) {
	a := NewCalendarEvent("google-calendar", "Google Calendar", "a", "Allhands", "2024-06-01T09:00:00Z", "", false)
	a.EntityID = "allhands@ical"
	a.Attendees = []Contact{{Name: "Ada", Emails: []string{"Ada@Example.com"}}}
	b := NewCalendarEvent("outlook-calendar", "Outlook Calendar", "b", "Allhands", "2024-06-01T09:00:00Z", "", false)
	b.EntityID = "allhands@ical"
	b.Attendees = []Contact{{Name: "Ada L", Emails: []string{"ada@example.com"}, Phones: []string{"+1 555 0100"}}}
	org := Contact{Name: "Grace", Emails: []string{"grace@example.com"}}
	b.Organizer = &org

	merged := MergeCalendarEvents([]CalendarEvent{a, b})
	if len(merged) != 1 {
		t.Fatalf("merged = %+v", merged)
	}
	if len(merged[0].Attendees) != 1 {
		t.Fatalf("attendees = %+v", merged[0].Attendees)
	}
	if len(merged[0].Attendees[0].Emails) != 1 || merged[0].Attendees[0].Emails[0] != "ada@example.com" {
		t.Fatalf("merged attendee = %+v", merged[0].Attendees[0])
	}
	if merged[0].Organizer == nil || merged[0].Organizer.Name != "Grace" {
		t.Fatalf("organizer = %+v", merged[0].Organizer)
	}
}

// Recurring instances share one iCal UID (RFC 5545; Google's expanded
// instances share iCalUID too). Each occurrence must stay its own entry —
// collapsing them stacked one entry with a source chip per occurrence.
func TestMergeCalendarEvents_RecurringInstancesStayDistinct(t *testing.T) {
	var in []CalendarEvent
	for i := 0; i < 12; i++ {
		day := time.Date(2024, 6, 1+i, 9, 0, 0, 0, time.UTC)
		e := NewCalendarEvent("google-calendar", "Google Calendar",
			fmt.Sprintf("standup_%d", i), "Standup",
			day.Format(time.RFC3339), day.Add(15*time.Minute).Format(time.RFC3339), false)
		e.EntityID = "standup@ical"
		in = append(in, e)
	}
	merged := MergeCalendarEvents(in)
	if len(merged) != 12 {
		t.Fatalf("merged = %d events, want 12 (one per occurrence)", len(merged))
	}
	for _, e := range merged {
		if len(e.Sources) != 1 {
			t.Fatalf("occurrence %q carries %d sources, want 1: %+v", e.StartAt, len(e.Sources), e.Sources)
		}
	}
}

// The same occurrence on two services still merges — the start qualifies
// the series UID, it does not split a genuine cross-service match.
func TestMergeCalendarEvents_SameOccurrenceAcrossServicesMerges(t *testing.T) {
	a := NewCalendarEvent("google-calendar", "Google Calendar", "a1", "Standup", "2024-06-03T09:00:00Z", "2024-06-03T09:15:00Z", false)
	a.EntityID = "standup@ical"
	b := NewCalendarEvent("caldav", "CalDAV", "standup@ical", "Standup", "2024-06-03T09:00:00Z", "2024-06-03T09:15:00Z", false)
	b.EntityID = "standup@ical"

	merged := MergeCalendarEvents([]CalendarEvent{a, b})
	if len(merged) != 1 {
		t.Fatalf("merged = %d events, want 1: %+v", len(merged), merged)
	}
	if len(merged[0].Sources) != 2 || merged[0].Match != "ical_uid" {
		t.Fatalf("entry = %+v", merged[0])
	}
}

func TestMergeCalendarEvents_DedupsRepeatSourceRefs(t *testing.T) {
	a := NewCalendarEvent("google-calendar", "Google Calendar", "e1", "Standup", "2024-06-01T09:00:00Z", "2024-06-01T09:30:00Z", false)
	a.EntityID = "standup@ical"
	b := NewCalendarEvent("google-calendar", "Google Calendar", "e1", "Standup", "2024-06-01T09:00:00Z", "2024-06-01T09:30:00Z", false)
	b.EntityID = "standup@ical"

	merged := MergeCalendarEvents([]CalendarEvent{a, b})
	if len(merged) != 1 {
		t.Fatalf("merged = %d events, want 1", len(merged))
	}
	if len(merged[0].Sources) != 1 {
		t.Fatalf("sources = %+v, want one ref for the one place the event lives", merged[0].Sources)
	}
}
