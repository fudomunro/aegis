package kinds

import (
	"sort"
	"strings"
	"time"
)

// calendarEventStartNormalized parses RFC3339 (or RFC3339Nano) and re-emits it
// at second precision in UTC so the same meeting matches across services.
func calendarEventStartNormalized(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return raw
	}
	return t.UTC().Format(time.RFC3339)
}

// CalendarEventIdentityKey is the sort/merge key used by the library aggregate.
// Prefer durable iCalUID when present; otherwise title + start + all_day.
// Events without any identity fall back to service:id so they stay distinct.
func CalendarEventIdentityKey(e CalendarEvent) string {
	if id := strings.TrimSpace(e.EntityID); id != "" {
		return "ical:" + strings.ToLower(id)
	}
	for _, src := range e.Sources {
		if id := strings.TrimSpace(ExtraString(src.Extra, "ical_uid")); id != "" {
			return "ical:" + strings.ToLower(id)
		}
	}
	start := calendarEventStartNormalized(e.StartAt)
	title := NormalizeIdentityKey(e.Title)
	if title == "" || start == "" {
		if len(e.Sources) > 0 && e.Sources[0].Service != "" && e.Sources[0].ID != "" {
			return "src:" + e.Sources[0].Service + ":" + e.Sources[0].ID
		}
		name := ""
		if len(e.Sources) > 0 {
			name = e.Sources[0].ServiceName
		}
		return "anon:" + name + ":" + e.Title + ":" + e.StartAt
	}
	flag := "0"
	if e.AllDay {
		flag = "1"
	}
	return "title:" + title + "|start:" + start + "|allday:" + flag
}

// calendarEventMatchKind records how identity was established for multi-source merges.
func calendarEventMatchKind(key string) string {
	switch {
	case strings.HasPrefix(key, "ical:"):
		return "ical_uid"
	case strings.HasPrefix(key, "title:"):
		return "title_start"
	default:
		return ""
	}
}

func mergeCalendarEventInto(dst *CalendarEvent, src CalendarEvent) {
	if dst.Title == "" {
		dst.Title = src.Title
	}
	if dst.Description == "" {
		dst.Description = src.Description
	}
	if dst.StartAt == "" {
		dst.StartAt = src.StartAt
	}
	if dst.EndAt == "" {
		dst.EndAt = src.EndAt
	}
	if !dst.AllDay {
		dst.AllDay = src.AllDay
	}
	if dst.Location == "" {
		dst.Location = src.Location
	}
	if dst.Status == "" {
		dst.Status = src.Status
	}
	if dst.CalendarID == "" {
		dst.CalendarID = src.CalendarID
	}
	if dst.CalendarName == "" {
		dst.CalendarName = src.CalendarName
	}
	if dst.EntityID == "" {
		dst.EntityID = src.EntityID
	}
	if dst.Organizer == nil && src.Organizer != nil {
		org := *src.Organizer
		dst.Organizer = &org
	}
	dst.Attendees = mergeCalendarAttendees(dst.Attendees, src.Attendees)
	dst.Sources = append(dst.Sources, src.Sources...)
	if len(dst.Extra) == 0 && len(src.Extra) > 0 {
		dst.Extra = src.Extra
	}
}

// mergeCalendarAttendees unions attendees using the same contact identity
// matching as the contacts library (email → phone → handle → name).
func mergeCalendarAttendees(dst, src []Contact) []Contact {
	if len(src) == 0 {
		return dst
	}
	if len(dst) == 0 {
		out := make([]Contact, len(src))
		copy(out, src)
		return out
	}
	return MergeContacts(append(append([]Contact(nil), dst...), src...))
}

// MergeCalendarEvents groups events that share a durable identity (iCalUID)
// or the same normalized title + start (and all-day flag) into one
// user-facing entry with multiple Sources. Events without an identity stay
// distinct via service:id.
func MergeCalendarEvents(in []CalendarEvent) []CalendarEvent {
	if len(in) == 0 {
		return nil
	}

	type slot struct {
		e CalendarEvent
	}
	var order []string
	byKey := map[string]*slot{}

	for _, e := range in {
		key := CalendarEventIdentityKey(e)
		if key == "" {
			continue
		}
		if cur, ok := byKey[key]; ok {
			mergeCalendarEventInto(&cur.e, e)
			continue
		}
		cp := e
		cp.Sources = append([]CalendarEventSource(nil), e.Sources...)
		if e.Organizer != nil {
			org := *e.Organizer
			cp.Organizer = &org
		}
		if len(e.Attendees) > 0 {
			cp.Attendees = append([]Contact(nil), e.Attendees...)
		}
		byKey[key] = &slot{e: cp}
		order = append(order, key)
	}

	out := make([]CalendarEvent, 0, len(order))
	for _, key := range order {
		item := byKey[key]
		if len(item.e.Sources) > 1 {
			if m := calendarEventMatchKind(key); m != "" {
				item.e.Match = m
			}
		}
		sort.SliceStable(item.e.Sources, func(i, j int) bool {
			if item.e.Sources[i].Canonical != item.e.Sources[j].Canonical {
				return item.e.Sources[i].Canonical
			}
			if item.e.Sources[i].ServiceName != item.e.Sources[j].ServiceName {
				return item.e.Sources[i].ServiceName < item.e.Sources[j].ServiceName
			}
			return item.e.Sources[i].Service < item.e.Sources[j].Service
		})
		out = append(out, item.e)
	}

	sort.SliceStable(out, func(i, j int) bool {
		si, sj := out[i].StartAt, out[j].StartAt
		if si != sj {
			return si < sj
		}
		ti, tj := out[i].Title, out[j].Title
		if ti != tj {
			return ti < tj
		}
		return CalendarEventIdentityKey(out[i]) < CalendarEventIdentityKey(out[j])
	})
	return out
}
