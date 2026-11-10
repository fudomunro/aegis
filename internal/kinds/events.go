package kinds

import (
	"context"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/core/policy"
)

// eventsKind owns the calendar-events aggregate and its freebusy view
// (issue #128): two routes over one dataset — the freebusy encode shares
// the events cache key via Spec.CacheKey.
type eventsKind struct {
	providerKind
	h      Host
	e      *Engine
	mark   func([]CalendarEvent, string)
	copies func([]CalendarEvent)
}

func newEventsKind(h Host, e *Engine, d Deps) Kind {
	k := &eventsKind{h: h, e: e}
	k.mark, k.copies = Stamps(h, "events", forEachCalendarEventSource)
	def := *BuiltinKindByID("events")
	k.providerKind = providerKind{
		def:      def,
		provides: providerProvides[CalendarEventProvider],
		routes: []Route{
			{Pattern: def.Path, AppReadable: def.AppReadable, Handler: k.serve},
			// Freebusy is the events availability view, not a LibraryKind —
			// declared as an events subroute so its exposure is declaration
			// data (issue #312).
			{Pattern: "/v1/library/freebusy", AppReadable: true, Handler: k.serveFreeBusy},
		},
	}
	return k
}

// serve returns the user's calendar events from every connected
// calendar-capable service: GET /v1/library/events. When a service's
// events permission is freebusy, that service's events are redacted to
// availability only (issue #128). Third-party apps whose events scope is
// freebusy get the same redaction across all services (issue #131).
func (k *eventsKind) serve(w http.ResponseWriter, r *http.Request) {
	kind := k.def
	Aggregate(k.h, k.e, w, r, Spec[CalendarEvent]{
		Kind: kind,
		Targets: func(r *http.Request) ([]NamedLister[CalendarEvent], []string) {
			targets, denied := eventsProviderTargets(k.h, r, kind.PermType)
			grants, _ := k.h.Grants(r)
			if !policy.IsFreeBusyAccess(grants.Level(kind.PermType)) {
				return targets, denied
			}
			for i := range targets {
				inner := targets[i].List
				targets[i].List = func(ctx context.Context) ([]CalendarEvent, bool, error) {
					events, truncated, err := inner(ctx)
					return RedactCalendarEventsFreeBusy(events), truncated, err
				}
			}
			return targets, denied
		},
		ServiceOf: calendarEventServiceName,
		SortKey:   CalendarEventIdentityKey,
		Merge:     MergeCalendarEvents,
		Encode: func(items []CalendarEvent, page PageRequest, bySvc map[string]LibraryServiceStatus) any {
			pageItems, pr := SlicePage(items, page)
			return CalendarEventList{Events: pageItems, LibraryListMeta: MetaForPage(pr, true, AnyServiceTruncated(bySvc), bySvc)}
		},
		RedactSearch: func(items []CalendarEvent, r *http.Request) []CalendarEvent {
			return searchRedactEvents(k.h, items, r)
		},
		MarkCanonical: k.mark,
		StampCopies:   k.copies,
	})
}

// serveFreeBusy returns only busy intervals for scheduling apps:
// GET /v1/library/freebusy (issue #128).
func (k *eventsKind) serveFreeBusy(w http.ResponseWriter, r *http.Request) {
	kind := k.def
	Aggregate(k.h, k.e, w, r, Spec[CalendarEvent]{
		Kind: kind,
		Targets: func(r *http.Request) ([]NamedLister[CalendarEvent], []string) {
			return eventsProviderTargets(k.h, r, kind.PermType)
		},
		ServiceOf: calendarEventServiceName,
		SortKey:   CalendarEventIdentityKey,
		Merge:     MergeCalendarEvents,
		Encode: func(items []CalendarEvent, page PageRequest, bySvc map[string]LibraryServiceStatus) any {
			return freeBusyListFromEvents(items, page, bySvc)
		},
		MarkCanonical: k.mark,
		StampCopies:   k.copies,
		CacheKey:      "library:freebusy",
	})
}

// eventsProviderTargets collects calendar providers for the kind.
func eventsProviderTargets(h Host, r *http.Request, permType string) ([]NamedLister[CalendarEvent], []string) {
	return Collect[CalendarEvent, CalendarEventProvider](h, r, permType,
		func(p CalendarEventProvider) func(context.Context) ([]CalendarEvent, bool, error) {
			return p.ListCalendarEvents
		})
}

// searchRedactEvents re-applies freebusy redaction to index-hydrated
// events (issue #178): a freebusy-only surface must never see titles or
// descriptions from a stored payload.
func searchRedactEvents(h Host, items []CalendarEvent, r *http.Request) []CalendarEvent {
	grants, _ := h.Grants(r)
	if !policy.IsFreeBusyAccess(grants.Level("events")) {
		return items
	}
	return RedactCalendarEventsFreeBusy(items)
}

// calendarEventServiceName returns the first source service name for
// stable pre-sort.
func calendarEventServiceName(e CalendarEvent) string {
	if len(e.Sources) > 0 {
		return e.Sources[0].ServiceName
	}
	return ""
}

// calendarEventIsBusy reports whether the event should block time on a
// free/busy share. Cancelled events do not occupy the calendar.
func calendarEventIsBusy(e CalendarEvent) bool {
	return !strings.EqualFold(strings.TrimSpace(e.Status), "cancelled")
}

// freeBusyBlockFromEvent converts a neutral event to an availability block.
func freeBusyBlockFromEvent(e CalendarEvent) FreeBusyBlock {
	return FreeBusyBlock{
		StartAt: e.StartAt,
		EndAt:   e.EndAt,
		AllDay:  e.AllDay,
	}
}

// freeBusyListFromEvents builds the share-friendly busy list (issue #128).
// Cancelled events are omitted; overlapping blocks are left as-is so
// callers can see every busy interval the provider reported.
func freeBusyListFromEvents(events []CalendarEvent, page PageRequest, bySvc map[string]LibraryServiceStatus) FreeBusyList {
	busy := make([]FreeBusyBlock, 0, len(events))
	for _, e := range events {
		if !calendarEventIsBusy(e) {
			continue
		}
		busy = append(busy, freeBusyBlockFromEvent(e))
	}
	// Page the busy blocks (not the source events) so cursors walk the
	// scheduling view (issue #185).
	pageItems, pr := SlicePage(busy, page)
	return FreeBusyList{
		Busy:            pageItems,
		LibraryListMeta: MetaForPage(pr, false, AnyServiceTruncated(bySvc), bySvc),
	}
}

func forEachCalendarEventSource(item *CalendarEvent, f func(SourceMut)) {
	for i := range item.Sources {
		src := &item.Sources[i]
		f(SourceMut{
			Service:       src.Service,
			ID:            src.ID,
			MarkCanonical: func() { src.Canonical = true },
			SetCopy:       func(info *CopyInfo) { src.Copy = info },
		})
	}
}
