package main

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"
)

// CalendarEventProvider is implemented by connectors that can return the
// user's calendar entries in Guardian's neutral format. The aggregate library
// endpoint uses this so agents can ask for "my calendar" once and receive
// every connected service. Each returned CalendarEvent should carry one
// CalendarEventSource for that service. Guardian never stores calendar content
// — the authoritative copy stays on the source service (issue #16).
// https://github.com/fudomunro/aperture-guardian/issues/65
type CalendarEventProvider interface {
	// ListCalendarEvents fetches calendar entries from the upstream service
	// and maps them into neutral CalendarEvent values (service identity in
	// Sources). Return ErrUnauthorized when no valid token is available.
	// truncated is true when the page cap was hit with more items still upstream.
	ListCalendarEvents(ctx context.Context) (events []CalendarEvent, truncated bool, err error)
}

// calendarEventServiceName returns the first source service name for stable pre-sort.
func calendarEventServiceName(e CalendarEvent) string {
	if len(e.Sources) > 0 {
		return e.Sources[0].ServiceName
	}
	return ""
}

// calendarEventExtraString reads a string Extra field.
func calendarEventExtraString(extra map[string]any, key string) string {
	if extra == nil {
		return ""
	}
	v, ok := extra[key]
	if !ok || v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

// calendarEventStartNormalizes parses RFC3339 (or RFC3339Nano) and re-emits it
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

// calendarEventIdentityKey is the sort/merge key used by the library aggregate.
// Prefer durable iCalUID when present; otherwise title + start + all_day.
// Events without any identity fall back to service:id so they stay distinct.
func calendarEventIdentityKey(e CalendarEvent) string {
	if id := strings.TrimSpace(e.EntityID); id != "" {
		return "ical:" + strings.ToLower(id)
	}
	for _, src := range e.Sources {
		if id := strings.TrimSpace(calendarEventExtraString(src.Extra, "ical_uid")); id != "" {
			return "ical:" + strings.ToLower(id)
		}
	}
	start := calendarEventStartNormalized(e.StartAt)
	title := NormalizeIdentityKey(e.Title)
	if title == "" || start == "" {
		if len(e.Sources) > 0 && e.Sources[0].Service != "" && e.Sources[0].ID != "" {
			return "src:" + e.Sources[0].Service + ":" + e.Sources[0].ID
		}
		return "anon:" + calendarEventServiceName(e) + ":" + e.Title + ":" + e.StartAt
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
		key := calendarEventIdentityKey(e)
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
		return calendarEventIdentityKey(out[i]) < calendarEventIdentityKey(out[j])
	})
	return out
}

// newCalendarEvent builds a single-source neutral calendar event.
func newCalendarEvent(service, serviceName, id, title, startAt, endAt string, allDay bool) CalendarEvent {
	src := CalendarEventSource{
		Service:     service,
		ServiceName: serviceName,
		ID:          id,
		Canonical:   true,
	}
	return CalendarEvent{
		Title:   title,
		StartAt: startAt,
		EndAt:   endAt,
		AllDay:  allDay,
		Sources: []CalendarEventSource{src},
	}
}

// HandleLibraryEvents returns the user's calendar events from every connected
// calendar-capable service: GET /v1/library/events
func (s *Server) HandleLibraryEvents(w http.ResponseWriter, r *http.Request) {
	kind := *LibraryKindByID("events")
	handleLibraryAggregate(s, w, r, libraryKindSpec[CalendarEvent]{
		kind: kind,
		targets: func(s *Server) ([]namedLister[CalendarEvent], []string) {
			return collectProviders[CalendarEvent, CalendarEventProvider](s, kind.PermType,
				func(p CalendarEventProvider) func(context.Context) ([]CalendarEvent, bool, error) {
					return p.ListCalendarEvents
				})
		},
		serviceOf: calendarEventServiceName,
		sortKey:   calendarEventIdentityKey,
		merge:     MergeCalendarEvents,
		encode: func(merged []CalendarEvent, bySvc map[string]LibraryServiceStatus) any {
			return CalendarEventList{Events: merged, Total: len(merged), Synced: true, Truncated: anyServiceTruncated(bySvc), ByService: bySvc}
		},
		markCanonical: s.stampCalendarEventCanonical,
	})
}

// hasCalendarEventProviders reports whether any registered connector can list events.
func (s *Server) hasCalendarEventProviders() bool {
	return LibraryKindByID("events").HasProviders(s)
}

// calendarEventsLibraryReadable reports whether any service allows event reads.
func (s *Server) calendarEventsLibraryReadable() bool {
	return LibraryKindByID("events").Readable(s)
}

// libraryCalendarEventsSystemInstructions tells agents about the events endpoint.
func (s *Server) libraryCalendarEventsSystemInstructions() string {
	return s.libraryInstructionsFor("events")
}

// mentionsCalendarEvents detects calendar-oriented questions that don't name a service.
func mentionsCalendarEvents(msg string) bool {
	return LibraryKindByID("events").Mentions(msg)
}
