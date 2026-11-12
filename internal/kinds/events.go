package kinds

import (
	"context"
	"net/http"
	"strings"
)

// eventsKind owns the calendar-events aggregate and its freebusy view
// (issue #128): two routes over one dataset. Each route is its own grant
// key (issue #348) — the freebusy encode narrows what that route serves.
type eventsKind struct {
	providerKind
	h      Host
	e      *Engine
	mark   func([]CalendarEvent, string)
	copies func([]CalendarEvent)
}

// Grant keys are routes: the library-relative path (issue #348).
const (
	eventsRouteKey   = "events"
	freebusyRouteKey = "freebusy"
)

func newEventsKind(h Host, e *Engine, d Deps) Kind {
	k := &eventsKind{h: h, e: e}
	k.mark, k.copies = Stamps(h, "events", forEachCalendarEventSource)
	def := *BuiltinKindByID("events")
	k.providerKind = providerKind{
		def:      def,
		provides: providerProvides[CalendarEventProvider],
		writeCapable: providerProvides[CalendarEventWriteProvider],
		converge: eventsConvergePlan(h, def.ID),
		routes: []Route{
			{Pattern: def.Path, AppReadable: def.AppReadable, Handler: k.serve, GrantKey: eventsRouteKey},
			// Freebusy is the events availability view, not a LibraryKind —
			// declared as an events subroute so its exposure is declaration
			// data (issue #312).
			{Pattern: "/v1/library/freebusy", AppReadable: true, Handler: k.serveFreeBusy, GrantKey: freebusyRouteKey},
		},
	}
	return k
}

// serve returns the user's calendar events from every connected
// calendar-capable service: GET /v1/library/events. The route serves full
// events — availability-only access is the separate freebusy route
// (issue #348).
func (k *eventsKind) serve(w http.ResponseWriter, r *http.Request) {
	kind := k.def
	Aggregate(k.h, k.e, w, r, Spec[CalendarEvent]{
		Kind:     kind,
		GrantKey: eventsRouteKey,
		Targets: func(r *http.Request) ([]NamedLister[CalendarEvent], []string) {
			return eventsProviderTargets(k.h, r, eventsRouteKey)
		},
		ServiceOf: calendarEventServiceName,
		SortKey:   CalendarEventIdentityKey,
		Merge:     MergeCalendarEvents,
		Encode: func(items []CalendarEvent, page PageRequest, bySvc map[string]LibraryServiceStatus) any {
			pageItems, pr := SlicePage(items, page)
			return CalendarEventList{Events: pageItems, LibraryListMeta: MetaForPage(pr, true, AnyServiceTruncated(bySvc), bySvc)}
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
		Kind:     kind,
		GrantKey: freebusyRouteKey,
		Targets: func(r *http.Request) ([]NamedLister[CalendarEvent], []string) {
			return eventsProviderTargets(k.h, r, freebusyRouteKey)
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

// eventsConvergePlan is the events kind's converge plan (issue #382): the
// etag-entity shape, same as contacts — whole-collection placement with
// If-Match refs and record-conflict resolution across hub and member.
func eventsConvergePlan(h Host, kind string) *ConvergePlan {
	return &ConvergePlan{
		Shape:   ConvergeEntities,
		RowName: kind,
		ReadSource: func(ctx context.Context, hub, _, _ string, _ int) (SourcePage, error) {
			src, err := OpFor[CalendarEventProvider](h, hub, "converge source")
			if err != nil {
				return SourcePage{}, err
			}
			events, truncated, err := src.ListCalendarEvents(ctx)
			if err != nil {
				return SourcePage{}, err
			}
			return SourcePage{Items: batchOf(events), HubTotal: len(events), Truncated: truncated}, nil
		},
		AttachRefs: func(items Batch, member string, refs map[string]ItemRef) Batch {
			events := batchItems[CalendarEvent](items)
			out := make([]CalendarEvent, len(events))
			copy(out, events)
			for i := range out {
				ref, ok := refs[ItemEntityKey(kind, out[i].Title)]
				if !ok {
					continue
				}
				out[i].Sources = upsertEventRef(out[i].Sources, member, ref.ExternalID, ref.ETag)
			}
			return batchOf(out)
		},
		Place: func(ctx context.Context, service, _, _ string, items Batch) (PlaceResult, error) {
			dst, err := OpFor[CalendarEventWriteProvider](h, service, "converge target")
			if err != nil {
				return PlaceResult{}, err
			}
			res, err := dst.PlaceEvents(ctx, batchItems[CalendarEvent](items))
			if err != nil {
				return PlaceResult{}, err
			}
			return PlaceResult{
				Copied:    res.Copied,
				Placed:    res.Placed,
				Skips:     res.Skips,
				Conflicts: res.Conflicts,
			}, nil
		},
		MemberExtras: func(ctx context.Context, hub, member, _, _ string) (int, func(context.Context) error, error) {
			memberList, err := eventListFor(ctx, h, member, "conflict source")
			if err != nil {
				return 0, nil, err
			}
			hubList, err := eventListFor(ctx, h, hub, "conflict source")
			if err != nil {
				return 0, nil, err
			}
			hubKeys := make(map[string]bool, len(hubList))
			for _, e := range hubList {
				hubKeys[CalendarEventIdentityKey(e)] = true
			}
			var extras []CalendarEvent
			for _, e := range memberList {
				if !hubKeys[CalendarEventIdentityKey(e)] {
					extras = append(extras, e)
				}
			}
			adopt := func(ctx context.Context) error {
				dst, err := OpFor[CalendarEventWriteProvider](h, hub, "conflict target")
				if err != nil {
					return err
				}
				_, err = dst.PlaceEvents(ctx, extras)
				return err
			}
			return len(extras), adopt, nil
		},
		RecordResolve: func(ctx context.Context, hub, member, memberID, subject string, accept bool) ([]ItemStamp, bool, error) {
			memberList, err := eventListFor(ctx, h, member, "conflict source")
			if err != nil {
				return nil, false, err
			}
			memberEV, found := findEventByRef(memberList, member, memberID)
			if !found {
				return nil, false, nil
			}
			hubList, err := eventListFor(ctx, h, hub, "conflict source")
			if err != nil {
				return nil, false, err
			}
			hubEV := findEventByKey(hubList, CalendarEventIdentityKey(memberEV))
			if hubEV.Title == "" {
				hubEV = findEventByName(hubList, subject)
			}
			if accept {
				dst, err := OpFor[CalendarEventWriteProvider](h, hub, "conflict target")
				if err != nil {
					return nil, true, err
				}
				merged := memberEV
				merged.Sources = upsertEventRef(memberEV.Sources, hub,
					eventRefID(hubEV, hub), eventRefETag(hubEV, hub))
				if _, err := dst.PlaceEvents(ctx, []CalendarEvent{merged}); err != nil {
					return nil, true, err
				}
				return []ItemStamp{{
					EntityName: subject,
					Service:    member,
					ExternalID: memberID,
					Name:       subject,
					ETag:       eventRefETag(memberEV, member),
				}}, true, nil
			}
			dst, err := OpFor[CalendarEventWriteProvider](h, member, "conflict target")
			if err != nil {
				return nil, true, err
			}
			merged := hubEV
			merged.Sources = upsertEventRef(hubEV.Sources, member, memberID, eventRefETag(memberEV, member))
			res, err := dst.PlaceEvents(ctx, []CalendarEvent{merged})
			if err != nil {
				return nil, true, err
			}
			return itemStampsFrom(res.Placed, member), true, nil
		},
	}
}

// eventListFor reads one service's full calendar list.
func eventListFor(ctx context.Context, h Host, service, what string) ([]CalendarEvent, error) {
	src, err := OpFor[CalendarEventProvider](h, service, what)
	if err != nil {
		return nil, err
	}
	events, _, err := src.ListCalendarEvents(ctx)
	return events, err
}

// upsertEventRef patches an event's source for service with id+etag,
// replacing any previous entry for that service.
func upsertEventRef(srcs []CalendarEventSource, service, id, etag string) []CalendarEventSource {
	out := make([]CalendarEventSource, 0, len(srcs)+1)
	replaced := false
	for _, src := range srcs {
		if src.Service == service {
			if !replaced {
				src.ID = id
				src.Extra = withETag(src.Extra, etag)
				out = append(out, src)
				replaced = true
			}
			continue
		}
		out = append(out, src)
	}
	if !replaced {
		out = append(out, CalendarEventSource{Service: service, ID: id, Extra: withETag(nil, etag)})
	}
	return out
}

// findEventByRef finds the event a member copy row points at.
func findEventByRef(list []CalendarEvent, service, externalID string) (CalendarEvent, bool) {
	for _, e := range list {
		for _, src := range e.Sources {
			if src.Service == service && src.ID == externalID {
				return e, true
			}
		}
	}
	return CalendarEvent{}, false
}

// findEventByKey finds the event with the given identity key.
func findEventByKey(list []CalendarEvent, key string) CalendarEvent {
	if key == "" {
		return CalendarEvent{}
	}
	for _, e := range list {
		if CalendarEventIdentityKey(e) == key {
			return e
		}
	}
	return CalendarEvent{}
}

// findEventByName finds the event with the given title.
func findEventByName(list []CalendarEvent, title string) CalendarEvent {
	for _, e := range list {
		if e.Title == title {
			return e
		}
	}
	return CalendarEvent{}
}

// eventRefID reads an event's service-local id for one service.
func eventRefID(e CalendarEvent, service string) string {
	for _, src := range e.Sources {
		if src.Service == service {
			return src.ID
		}
	}
	return ""
}

// eventRefETag reads an event's etag watermark for one service.
func eventRefETag(e CalendarEvent, service string) string {
	for _, src := range e.Sources {
		if src.Service == service {
			return ExtraString(src.Extra, "etag")
		}
	}
	return ""
}
