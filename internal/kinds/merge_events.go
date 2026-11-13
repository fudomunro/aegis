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

// calendarEventOccurrenceKey keys one occurrence of an iCal series: the UID
// names the series, the normalized start names the instance. Expanded
// recurring instances share one UID (RFC 5545; Google's expanded instances
// share iCalUID) and must stay distinct entries — keying on the UID alone
// folds every occurrence into one entry stacked with duplicate sources.
func calendarEventOccurrenceKey(uid, startAt string) string {
	key := "ical:" + strings.ToLower(strings.TrimSpace(uid))
	if s := calendarEventStartNormalized(startAt); s != "" {
		key += "|start:" + s
	}
	return key
}

// CalendarEventIdentityKey is the sort/merge key used by the library aggregate.
// Prefer durable iCalUID when present, qualified by the occurrence's start so
// recurring instances stay distinct; otherwise title + start + all_day.
// Events without any identity fall back to service:id so they stay distinct.
func CalendarEventIdentityKey(e CalendarEvent) string {
	if id := strings.TrimSpace(e.EntityID); id != "" {
		return calendarEventOccurrenceKey(id, e.StartAt)
	}
	for _, src := range e.Sources {
		if id := strings.TrimSpace(ExtraString(src.Extra, "ical_uid")); id != "" {
			return calendarEventOccurrenceKey(id, e.StartAt)
		}
		if id := strings.TrimSpace(ExtraString(src.Extra, "uid")); id != "" {
			return calendarEventOccurrenceKey(id, e.StartAt)
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

// EventItemKey is the copy-watermark key for one event (issue #384): the
// iCal UID where it has one, so a retitle on the canonical home does not
// orphan the row. Deliberately series-level — unlike the merge key, which
// qualifies the UID with the occurrence's start so recurring instances stay
// distinct entries. An event with no UID keys by its title, what all rows
// used before.
func EventItemKey(e CalendarEvent) string {
	if uid := EventICalUID(e); uid != "" {
		return "ical:" + strings.ToLower(strings.TrimSpace(uid))
	}
	return e.Title
}

// EventICalUID is the event's iCal UID — the one an outgoing copy should
// carry — or "" when it has none.
func EventICalUID(e CalendarEvent) string {
	if id := strings.TrimSpace(e.EntityID); id != "" {
		return id
	}
	for _, src := range e.Sources {
		if id := strings.TrimSpace(ExtraString(src.Extra, "ical_uid")); id != "" {
			return id
		}
		if id := strings.TrimSpace(ExtraString(src.Extra, "uid")); id != "" {
			return id
		}
	}
	return ""
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
	dst.Sources = appendEventSources(dst.Sources, src.Sources)
	if len(dst.Extra) == 0 && len(src.Extra) > 0 {
		dst.Extra = src.Extra
	}
}

// appendEventSources appends only the locations dst does not already hold:
// sources[] is the set of places an event lives, so a repeated (service, id)
// ref is one place however many times a walk lists it — never another copy.
func appendEventSources(dst, src []CalendarEventSource) []CalendarEventSource {
	for _, s := range src {
		dup := false
		for _, d := range dst {
			if d.Service == s.Service && d.ID == s.ID {
				dup = true
				break
			}
		}
		if !dup {
			dst = append(dst, s)
		}
	}
	return dst
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
