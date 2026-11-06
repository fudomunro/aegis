package types

import "testing"

func TestCalendarEventIdentityKey_PrefersICalUID(t *testing.T) {
	e := NewCalendarEvent("google-calendar", "Google Calendar", "id1", "Standup", "2024-06-01T09:00:00Z", "2024-06-01T09:30:00Z", false)
	e.EntityID = "uid@google.com"
	if got := CalendarEventIdentityKey(e); got != "ical:uid@google.com" {
		t.Fatalf("key = %q", got)
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
