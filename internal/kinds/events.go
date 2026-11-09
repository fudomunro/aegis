package kinds

import (
	"context"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/types"
)

// eventsKind owns the calendar-events aggregate and its freebusy view
// (issue #128): two routes over one dataset — the freebusy encode shares
// the events cache key via Spec.CacheKey.
type eventsKind struct {
	providerKind
	h      Host
	e      *Engine
	mark   func([]types.CalendarEvent, string)
	copies func([]types.CalendarEvent)
}

func newEventsKind(h Host, e *Engine, d Deps) Kind {
	k := &eventsKind{h: h, e: e}
	k.mark, k.copies = Stamps(h, "events", forEachCalendarEventSource)
	def := *BuiltinKindByID("events")
	k.providerKind = providerKind{
		def:      def,
		provides: providerProvides[types.CalendarEventProvider],
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
	Aggregate(k.h, k.e, w, r, Spec[types.CalendarEvent]{
		Kind: kind,
		Targets: func(r *http.Request) ([]NamedLister[types.CalendarEvent], []string) {
			targets, denied := eventsProviderTargets(k.h, r, kind.PermType)
			appLevel, isApp := k.h.AppLevel(r, kind.PermType)
			appFreeBusy := isApp && policy.IsFreeBusyAccess(appLevel)
			for i := range targets {
				id := targets[i].ID
				inner := targets[i].List
				if !appFreeBusy && !policy.IsFreeBusyAccess(k.h.RequestLevel(r, id, kind.PermType)) {
					continue
				}
				targets[i].List = func(ctx context.Context) ([]types.CalendarEvent, bool, error) {
					events, truncated, err := inner(ctx)
					return types.RedactCalendarEventsFreeBusy(events), truncated, err
				}
			}
			return targets, denied
		},
		ServiceOf: calendarEventServiceName,
		SortKey:   types.CalendarEventIdentityKey,
		Merge:     types.MergeCalendarEvents,
		Encode: func(items []types.CalendarEvent, page types.PageRequest, bySvc map[string]types.LibraryServiceStatus) any {
			pageItems, pr := types.SlicePage(items, page)
			return types.CalendarEventList{Events: pageItems, LibraryListMeta: types.MetaForPage(pr, true, AnyServiceTruncated(bySvc), bySvc)}
		},
		RedactSearch: func(items []types.CalendarEvent, r *http.Request) []types.CalendarEvent {
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
	Aggregate(k.h, k.e, w, r, Spec[types.CalendarEvent]{
		Kind: kind,
		Targets: func(r *http.Request) ([]NamedLister[types.CalendarEvent], []string) {
			return eventsProviderTargets(k.h, r, kind.PermType)
		},
		ServiceOf: calendarEventServiceName,
		SortKey:   types.CalendarEventIdentityKey,
		Merge:     types.MergeCalendarEvents,
		Encode: func(items []types.CalendarEvent, page types.PageRequest, bySvc map[string]types.LibraryServiceStatus) any {
			return freeBusyListFromEvents(items, page, bySvc)
		},
		MarkCanonical: k.mark,
		StampCopies:   k.copies,
		CacheKey:      "library:freebusy",
	})
}

// eventsProviderTargets collects calendar providers for the kind.
func eventsProviderTargets(h Host, r *http.Request, permType string) ([]NamedLister[types.CalendarEvent], []string) {
	return Collect[types.CalendarEvent, types.CalendarEventProvider](h, r, permType,
		func(p types.CalendarEventProvider) func(context.Context) ([]types.CalendarEvent, bool, error) {
			return p.ListCalendarEvents
		})
}

// searchRedactEvents re-applies per-source freebusy redaction to
// index-hydrated events (issue #178): a freebusy-only surface must never
// see titles or descriptions from a stored payload.
func searchRedactEvents(h Host, items []types.CalendarEvent, r *http.Request) []types.CalendarEvent {
	appLevel, isApp := h.AppLevel(r, "events")
	appFreeBusy := isApp && policy.IsFreeBusyAccess(appLevel)
	out := make([]types.CalendarEvent, 0, len(items))
	for _, e := range items {
		redact := appFreeBusy
		for _, src := range e.Sources {
			if _, ok := h.Service(src.Service); !ok {
				continue
			}
			if policy.IsFreeBusyAccess(h.RequestLevel(r, src.Service, "events")) {
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

// calendarEventServiceName returns the first source service name for
// stable pre-sort.
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
// Cancelled events are omitted; overlapping blocks are left as-is so
// callers can see every busy interval the provider reported.
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
		LibraryListMeta: types.MetaForPage(pr, false, AnyServiceTruncated(bySvc), bySvc),
	}
}

func forEachCalendarEventSource(item *types.CalendarEvent, f func(SourceMut)) {
	for i := range item.Sources {
		src := &item.Sources[i]
		f(SourceMut{
			Service:       src.Service,
			ID:            src.ID,
			MarkCanonical: func() { src.Canonical = true },
			SetCopy:       func(info *types.CopyInfo) { src.Copy = info },
		})
	}
}
