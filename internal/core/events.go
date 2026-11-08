package core

import (
	"context"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/types"
)

// calendarEventServiceName returns the first source service name for stable pre-sort.
func calendarEventServiceName(e types.CalendarEvent) string {
	if len(e.Sources) > 0 {
		return e.Sources[0].ServiceName
	}
	return ""
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
func freeBusyListFromEvents(events []types.CalendarEvent, page types.PageRequest, bySvc map[string]types.LibraryServiceStatus) types.FreeBusyList {
	busy := make([]types.FreeBusyBlock, 0, len(events))
	for _, e := range events {
		if !calendarEventIsBusy(e) {
			continue
		}
		busy = append(busy, freeBusyBlockFromEvent(e))
	}
	// Page the busy blocks (not the source events) so cursors walk the
	// scheduling view (issue #185).
	pageItems, pr := types.SlicePage(busy, page)
	return types.FreeBusyList{
		Busy:            pageItems,
		LibraryListMeta: types.MetaForPage(pr, false, anyServiceTruncated(bySvc), bySvc),
	}
}

// eventsProviderTargets collects calendar providers, applying free/busy
// redaction per service so a freebusy-only surface never contributes titles
// or attendees to the aggregate (issue #128).
func eventsProviderTargets(s *Server, r *http.Request, permType string) ([]namedLister[types.CalendarEvent], []string) {
	return collectProviders[types.CalendarEvent, types.CalendarEventProvider](s, r, permType,
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
			targets, denied := eventsProviderTargets(s, r, kind.PermType)
			appFreeBusy := false
			if app := AppIdentityFrom(r); app != nil {
				appFreeBusy = policy.IsFreeBusyAccess(app.Scopes.For(kind.PermType))
			}
			for i := range targets {
				id := targets[i].id
				inner := targets[i].list
				level := ""
				if svc := s.config.GetService(id); svc != nil {
					level = s.requestLevel(r, svc, kind.PermType)
				}
				if !appFreeBusy && !policy.IsFreeBusyAccess(level) {
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
		sortKey:   types.CalendarEventIdentityKey,
		merge:     types.MergeCalendarEvents,
		encode: func(items []types.CalendarEvent, page types.PageRequest, bySvc map[string]types.LibraryServiceStatus) any {
			pageItems, pr := types.SlicePage(items, page)
			return types.CalendarEventList{Events: pageItems, LibraryListMeta: types.MetaForPage(pr, true, anyServiceTruncated(bySvc), bySvc)}
		},
		redactSearch: func(items []types.CalendarEvent, r *http.Request) []types.CalendarEvent {
			return s.searchRedactEvents(items, r)
		},
		markCanonical: s.stampCalendarEventCanonical,
		stampCopies:   s.stampEventCopies,
	})
}

// searchRedactEvents re-applies per-source freebusy redaction to
// index-hydrated events (issue #178): a freebusy-only surface must never see
// titles or descriptions from a stored payload.
func (s *Server) searchRedactEvents(items []types.CalendarEvent, r *http.Request) []types.CalendarEvent {
	appFreeBusy := false
	if app := AppIdentityFrom(r); app != nil {
		appFreeBusy = policy.IsFreeBusyAccess(app.Scopes.For("events"))
	}
	out := make([]types.CalendarEvent, 0, len(items))
	for _, e := range items {
		redact := appFreeBusy
		for _, src := range e.Sources {
			svc := s.config.GetService(src.Service)
			if svc == nil {
				continue
			}
			if policy.IsFreeBusyAccess(s.requestLevel(r, svc, "events")) {
				redact = true
			}
		}
		if redact {
			e = types.RedactCalendarEventsFreeBusy([]types.CalendarEvent{e})[0]
		}
		out = append(out, e)
	}
	return out
}

// HandleLibraryFreeBusy returns only busy intervals for scheduling apps:
// GET /v1/library/freebusy (issue #128).
func (s *Server) HandleLibraryFreeBusy(w http.ResponseWriter, r *http.Request) {
	kind := *LibraryKindByID("events")
	handleLibraryAggregate(s, w, r, libraryKindSpec[types.CalendarEvent]{
		kind: kind,
		targets: func(s *Server, r *http.Request) ([]namedLister[types.CalendarEvent], []string) {
			return eventsProviderTargets(s, r, kind.PermType)
		},
		serviceOf: calendarEventServiceName,
		sortKey:   types.CalendarEventIdentityKey,
		merge:     types.MergeCalendarEvents,
		encode: func(items []types.CalendarEvent, page types.PageRequest, bySvc map[string]types.LibraryServiceStatus) any {
			return freeBusyListFromEvents(items, page, bySvc)
		},
		markCanonical: s.stampCalendarEventCanonical,
		stampCopies:   s.stampEventCopies,
		cacheKey:      "library:freebusy",
	})
}
