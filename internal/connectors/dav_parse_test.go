package connectors

import (
	"testing"
	"time"
)

func TestParseVCard_MapsNeutralContact(t *testing.T) {
	body := "BEGIN:VCARD\r\n" +
		"VERSION:3.0\r\n" +
		"FN:Ada Lovelace\r\n" +
		"N:Lovelace;Ada;;;\r\n" +
		"EMAIL;TYPE=HOME:ada@example.com\r\n" +
		"EMAIL;TYPE=WORK:ada@work.example.com\r\n" +
		"TEL;TYPE=CELL:+1 555 0100\r\n" +
		"ORG:Analytical Engines\r\n" +
		"UID:uid-ada-1\r\n" +
		"END:VCARD\r\n"
	c := ParseVCard("dav-contacts", "DAV Contacts", "ada.vcf", body, `"etag-1"`)
	if c.Name != "Ada Lovelace" {
		t.Fatalf("name = %q", c.Name)
	}
	if len(c.Emails) != 2 || c.Emails[0] != "ada@example.com" {
		t.Fatalf("emails = %+v", c.Emails)
	}
	if len(c.Phones) != 1 || c.Phones[0] != "+15550100" {
		t.Fatalf("phones = %+v", c.Phones)
	}
	if c.Organization != "Analytical Engines" {
		t.Fatalf("org = %q", c.Organization)
	}
	if len(c.Sources) != 1 || c.Sources[0].Service != "dav-contacts" {
		t.Fatalf("sources = %+v", c.Sources)
	}
	if c.Sources[0].Extra["etag"] != `"etag-1"` {
		t.Fatalf("etag = %v", c.Sources[0].Extra["etag"])
	}
}

func TestParseVCard_NameFromNWhenFNMissing(t *testing.T) {
	body := "BEGIN:VCARD\r\nVERSION:3.0\r\nN:Hopper;Grace;;;\r\nEMAIL:grace@example.com\r\nEND:VCARD\r\n"
	c := ParseVCard("dav-contacts", "DAV Contacts", "g.vcf", body, "")
	if c.Name != "Grace Hopper" {
		t.Fatalf("name = %q", c.Name)
	}
}

func TestParseVCard_UnfoldsContinuation(t *testing.T) {
	body := "BEGIN:VCARD\r\nFN:Long\r\n  Name\r\nEMAIL:a@b.c\r\nEND:VCARD\r\n"
	c := ParseVCard("dav-contacts", "DAV Contacts", "x.vcf", body, "")
	if c.Name != "Long Name" {
		t.Fatalf("name = %q", c.Name)
	}
}

func TestEncodeVCard_RoundTrip(t *testing.T) {
	in := ParseVCard("dav-contacts", "DAV Contacts", "x.vcf",
		"BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Grace Hopper\r\nEMAIL:grace@example.com\r\nTEL:+15550199\r\nORG:USN\r\nUID:uid-g\r\nEND:VCARD\r\n", "e1")
	body := EncodeVCard(in)
	out := ParseVCard("dav-contacts", "DAV Contacts", "x.vcf", body, "e1")
	if out.Name != "Grace Hopper" {
		t.Fatalf("roundtrip name = %q body=%s", out.Name, body)
	}
	if len(out.Emails) != 1 || out.Emails[0] != "grace@example.com" {
		t.Fatalf("roundtrip emails = %+v", out.Emails)
	}
}

func TestParseICalendar_MapsEvent(t *testing.T) {
	body := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\n" +
		"UID:ev-1\r\nSUMMARY:Doctor\r\nDESCRIPTION:Bring card\r\n" +
		"DTSTART:20260302T100000Z\r\nDTEND:20260302T103000Z\r\n" +
		"LOCATION:Clinic\r\nSTATUS:CONFIRMED\r\n" +
		"ORGANIZER;CN=Dr. Ada:mailto:ada@example.com\r\n" +
		"ATTENDEE;CN=Me;ROLE=REQ-PARTICIPANT:mailto:me@example.com\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	evs := ParseICalendar(body)
	if len(evs) != 1 {
		t.Fatalf("got %d events", len(evs))
	}
	ev := evs[0]
	if ev.Summary != "Doctor" || ev.UID != "ev-1" || ev.Location != "Clinic" {
		t.Fatalf("event = %+v", ev)
	}
	if !ev.Start.Equal(time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("start = %v", ev.Start)
	}
	if ev.Organizer != "ada@example.com" || ev.OrganizerCN != "Dr. Ada" {
		t.Fatalf("organizer = %q %q", ev.Organizer, ev.OrganizerCN)
	}
	if len(ev.Attendees) != 1 || ev.Attendees[0].Email != "me@example.com" {
		t.Fatalf("attendees = %+v", ev.Attendees)
	}

	neutral := MapICalEvent("dav-calendar", "DAV Calendar", "cal-1", "Personal", ev)
	if neutral.Title != "Doctor" || neutral.EntityID != "ev-1" {
		t.Fatalf("neutral = %+v", neutral)
	}
	if neutral.StartAt != "2026-03-02T10:00:00Z" || neutral.EndAt != "2026-03-02T10:30:00Z" {
		t.Fatalf("times = %q %q", neutral.StartAt, neutral.EndAt)
	}
	if neutral.Organizer == nil || neutral.Organizer.Emails[0] != "ada@example.com" {
		t.Fatalf("organizer = %+v", neutral.Organizer)
	}
}

func TestParseICalendar_AllDay(t *testing.T) {
	body := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:d1\r\nSUMMARY:Offsite\r\n" +
		"DTSTART;VALUE=DATE:20260305\r\nDTEND;VALUE=DATE:20260306\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	evs := ParseICalendar(body)
	if len(evs) != 1 || !evs[0].AllDay {
		t.Fatalf("events = %+v", evs)
	}
	n := MapICalEvent("dav-calendar", "DAV Calendar", "", "", evs[0])
	if !n.AllDay || n.StartAt != "2026-03-05T00:00:00Z" {
		t.Fatalf("all-day = %+v", n)
	}
}

func TestExpandICalEvent_NonRecurringInWindow(t *testing.T) {
	ev := ICalEvent{UID: "e", Start: time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC), End: time.Date(2026, 3, 2, 11, 0, 0, 0, time.UTC)}
	got := ExpandICalEvent(ev, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC))
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	outside := ExpandICalEvent(ev, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC))
	if len(outside) != 0 {
		t.Fatalf("outside = %+v", outside)
	}
}

func TestExpandICalEvent_DailyRRule(t *testing.T) {
	ev := ICalEvent{
		UID:   "r",
		Start: time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 3, 2, 9, 30, 0, 0, time.UTC),
		RRule: "FREQ=DAILY;COUNT=3",
	}
	got := ExpandICalEvent(ev, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC))
	if len(got) != 3 {
		t.Fatalf("got %d occurrences: %+v", len(got), got)
	}
	if !got[0].Start.Equal(ev.Start) || !got[2].Start.Equal(ev.Start.AddDate(0, 0, 2)) {
		t.Fatalf("starts = %v %v %v", got[0].Start, got[1].Start, got[2].Start)
	}
	if !got[0].End.Equal(ev.End) || !got[1].End.Equal(ev.Start.AddDate(0, 0, 1).Add(30*time.Minute)) {
		t.Fatalf("ends = %v %v", got[0].End, got[1].End)
	}
}

func TestExpandICalEvent_WeeklyByDay(t *testing.T) {
	// Monday 2026-03-02 09:00 UTC, MO,WE weekly, 4 occurrences.
	ev := ICalEvent{
		UID:   "w",
		Start: time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC),
		RRule: "FREQ=WEEKLY;BYDAY=MO,WE;COUNT=4",
	}
	got := ExpandICalEvent(ev, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC))
	if len(got) != 4 {
		t.Fatalf("got %d: %+v", len(got), got)
	}
	// MO, WE, MO, WE
	wantWeekdays := []time.Weekday{time.Monday, time.Wednesday, time.Monday, time.Wednesday}
	for i, occ := range got {
		if occ.Start.Weekday() != wantWeekdays[i] {
			t.Fatalf("occ %d weekday = %v want %v (%v)", i, occ.Start.Weekday(), wantWeekdays[i], occ.Start)
		}
	}
}

func TestExpandICalEvent_EXDates(t *testing.T) {
	ev := ICalEvent{
		UID:   "x",
		Start: time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 3, 2, 9, 30, 0, 0, time.UTC),
		RRule: "FREQ=DAILY;COUNT=3",
		EXDates: []time.Time{
			time.Date(2026, 3, 3, 9, 0, 0, 0, time.UTC),
		},
	}
	got := ExpandICalEvent(ev, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC))
	if len(got) != 2 {
		t.Fatalf("got %d: %+v", len(got), got)
	}
}

func TestParseICalDuration(t *testing.T) {
	d, ok := parseICalDuration("PT30M")
	if !ok || d != 30*time.Minute {
		t.Fatalf("PT30M = %v %v", d, ok)
	}
	d, ok = parseICalDuration("P1DT2H")
	if !ok || d != 26*time.Hour {
		t.Fatalf("P1DT2H = %v %v", d, ok)
	}
}

func TestDavCredentials_FromBasicAndHeaders(t *testing.T) {
	// Basic auth: user:secret → dXNlcjpzZWNyZXQ=
	svc := &ServiceInfo{Headers: map[string]string{"Authorization": "Basic dXNlcjpzZWNyZXQ="}}
	a := davCredentials(svc)
	if a.Username != "user" || a.Password != "secret" {
		t.Fatalf("basic = %+v", a)
	}

	svc = &ServiceInfo{Headers: map[string]string{
		"X-DAV-Username": "alice",
		"X-DAV-Password": "app-pass",
	}}
	a = davCredentials(svc)
	if a.Username != "alice" || a.Password != "app-pass" {
		t.Fatalf("headers = %+v", a)
	}
}
