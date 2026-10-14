package core

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

// calendarEventServiceName returns the first source service name for stable pre-sort.
func calendarEventServiceName(e types.CalendarEvent) string {
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
func calendarEventIdentityKey(e types.CalendarEvent) string {
	if id := strings.TrimSpace(e.EntityID); id != "" {
		return "ical:" + strings.ToLower(id)
	}
	for _, src := range e.Sources {
		if id := strings.TrimSpace(calendarEventExtraString(src.Extra, "ical_uid")); id != "" {
			return "ical:" + strings.ToLower(id)
		}
	}
	start := calendarEventStartNormalized(e.StartAt)
	title := types.NormalizeIdentityKey(e.Title)
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

func mergeCalendarEventInto(dst *types.CalendarEvent, src types.CalendarEvent) {
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
func mergeCalendarAttendees(dst, src []types.Contact) []types.Contact {
	if len(src) == 0 {
		return dst
	}
	if len(dst) == 0 {
		out := make([]types.Contact, len(src))
		copy(out, src)
		return out
	}
	return MergeContacts(append(append([]types.Contact(nil), dst...), src...))
}

// MergeCalendarEvents groups events that share a durable identity (iCalUID)
// or the same normalized title + start (and all-day flag) into one
// user-facing entry with multiple Sources. Events without an identity stay
// distinct via service:id.
func MergeCalendarEvents(in []types.CalendarEvent) []types.CalendarEvent {
	if len(in) == 0 {
		return nil
	}

	type slot struct {
		e types.CalendarEvent
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
		cp.Sources = append([]types.CalendarEventSource(nil), e.Sources...)
		if e.Organizer != nil {
			org := *e.Organizer
			cp.Organizer = &org
		}
		if len(e.Attendees) > 0 {
			cp.Attendees = append([]types.Contact(nil), e.Attendees...)
		}
		byKey[key] = &slot{e: cp}
		order = append(order, key)
	}

	out := make([]types.CalendarEvent, 0, len(order))
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

// calendarEventIsBusy reports whether the event should block time on a
// free/busy share. Cancelled events do not occupy the calendar.
func calendarEventIsBusy(e types.CalendarEvent) bool {
	return !strings.EqualFold(strings.TrimSpace(e.Status), "cancelled")
}

// freeBusyBlockFromEvent converts a neutral event to an availability block.
func freeBusyBlockFromEvent(e types.CalendarEvent) types.FreeBusyBlock {
	return types.FreeBusyBlock{
		StartAt: e.StartAt,
		EndAt:   e.EndAt,
		AllDay:  e.AllDay,
	}
}

// freeBusyListFromEvents builds the share-friendly busy list (issue #128).
// Cancelled events are omitted; overlapping blocks are left as-is so callers
// can see every busy interval the provider reported.
func freeBusyListFromEvents(events []types.CalendarEvent, bySvc map[string]types.LibraryServiceStatus) types.FreeBusyList {
	busy := make([]types.FreeBusyBlock, 0, len(events))
	for _, e := range events {
		if !calendarEventIsBusy(e) {
			continue
		}
		busy = append(busy, freeBusyBlockFromEvent(e))
	}
	return types.FreeBusyList{
		Busy:      busy,
		Total:     len(busy),
		Truncated: anyServiceTruncated(bySvc),
		ByService: bySvc,
	}
}

// eventsProviderTargets collects calendar providers, applying free/busy
// redaction per service so a freebusy-only surface never contributes titles
// or attendees to the aggregate (issue #128).
func eventsProviderTargets(s *Server, permType string) ([]namedLister[types.CalendarEvent], []string) {
	return collectProviders[types.CalendarEvent, types.CalendarEventProvider](s, permType,
		func(p types.CalendarEventProvider) func(context.Context) ([]types.CalendarEvent, bool, error) {
			return p.ListCalendarEvents
		})
}

// HandleLibraryEvents returns the user's calendar events from every connected
// calendar-capable service: GET /v1/library/events
// When a service's events permission is freebusy, that service's events are
// redacted to availability only (issue #128). Third-party apps whose events
// scope is freebusy get the same redaction across all services (issue #131).
func (s *Server) HandleLibraryEvents(w http.ResponseWriter, r *http.Request) {
	kind := *LibraryKindByID("events")
	handleLibraryAggregate(s, w, r, libraryKindSpec[types.CalendarEvent]{
		kind: kind,
		targets: func(s *Server, r *http.Request) ([]namedLister[types.CalendarEvent], []string) {
			targets, denied := eventsProviderTargets(s, kind.PermType)
			appFreeBusy := false
			if app := AppIdentityFrom(r); app != nil {
				appFreeBusy = types.IsFreeBusyAccess(app.Scopes.For(kind.PermType))
			}
			for i := range targets {
				id := targets[i].id
				inner := targets[i].list
				level := ""
				if svc := s.config.GetService(id); svc != nil {
					level = s.effectivePermissions(svc).For(kind.PermType)
				}
				if !appFreeBusy && !types.IsFreeBusyAccess(level) {
					continue
				}
				targets[i].list = func(ctx context.Context) ([]types.CalendarEvent, bool, error) {
					events, truncated, err := inner(ctx)
					return types.RedactCalendarEventsFreeBusy(events), truncated, err
				}
			}
			return targets, denied
		},
		serviceOf: calendarEventServiceName,
		sortKey:   calendarEventIdentityKey,
		merge:     MergeCalendarEvents,
		encode: func(merged []types.CalendarEvent, bySvc map[string]types.LibraryServiceStatus) any {
			return types.CalendarEventList{Events: merged, Total: len(merged), Synced: true, Truncated: anyServiceTruncated(bySvc), ByService: bySvc}
		},
		markCanonical: s.stampCalendarEventCanonical,
		stampCopies:   s.stampEventCopies,
	})
}

// HandleLibraryFreeBusy returns only busy intervals for scheduling apps:
// GET /v1/library/freebusy (issue #128).
func (s *Server) HandleLibraryFreeBusy(w http.ResponseWriter, r *http.Request) {
	kind := *LibraryKindByID("events")
	handleLibraryAggregate(s, w, r, libraryKindSpec[types.CalendarEvent]{
		kind: kind,
		targets: func(s *Server, r *http.Request) ([]namedLister[types.CalendarEvent], []string) {
			return eventsProviderTargets(s, kind.PermType)
		},
		serviceOf: calendarEventServiceName,
		sortKey:   calendarEventIdentityKey,
		merge:     MergeCalendarEvents,
		encode: func(merged []types.CalendarEvent, bySvc map[string]types.LibraryServiceStatus) any {
			return freeBusyListFromEvents(merged, bySvc)
		},
		markCanonical: s.stampCalendarEventCanonical,
		stampCopies:   s.stampEventCopies,
		cacheKey:      "library:freebusy",
	})
}
