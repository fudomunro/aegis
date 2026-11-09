package kinds

import (
	"context"
	"net/http"

	"github.com/fudomunro/aegis/internal/core/policy"
)

// placesKind owns the visits/trips aggregate (issue #142): city/read/
// precise redaction per source, query filters, and a filter-scoped cache
// key. The Timeline import store is a declared local source (issue #312).
type placesKind struct {
	providerKind
	h           Host
	e           *Engine
	mark        func([]PlaceVisit, string)
	localSource func() bool
}

func newPlacesKind(h Host, e *Engine, d Deps) Kind {
	k := &placesKind{h: h, e: e, localSource: d.PlacesLocalSource}
	k.mark, _ = Stamps(h, "places", forEachPlaceVisitSource)
	def := *BuiltinKindByID("places")
	k.providerKind = providerKind{
		def:      def,
		provides: providerProvides[PlaceVisitProvider],
		routes: []Route{{
			Pattern:     def.Path,
			AppReadable: def.AppReadable,
			Handler:     k.serve,
		}},
	}
	return k
}

// HasProviders adds the local Timeline import store to the connector scan
// (issue #142).
func (k *placesKind) HasProviders(h Host) bool {
	if k.providerKind.HasProviders(h) {
		return true
	}
	return k.localSource != nil && k.localSource()
}

// serve returns the user's visits and trips from every connected source:
// GET /v1/library/places. Query filters: after, before, locality, kind
// (issue #142). Permission key: places. Default is none. Access
// city|read|precise redacts.
func (k *placesKind) serve(w http.ResponseWriter, r *http.Request) {
	kind := k.def
	filters := ParsePlaceVisitFilters(r)
	appLevel, _ := k.h.AppLevel(r, kind.PermType)
	Aggregate(k.h, k.e, w, r, Spec[PlaceVisit]{
		Kind: kind,
		Targets: func(r *http.Request) ([]NamedLister[PlaceVisit], []string) {
			return placesProviderTargets(k.h, r, kind.PermType, appLevel)
		},
		ServiceOf: PlaceVisitServiceName,
		SortKey:   PlaceIdentityKey,
		Merge:     MergePlaceVisits,
		Encode: func(items []PlaceVisit, page PageRequest, bySvc map[string]LibraryServiceStatus) any {
			filtered := filterPlaceVisits(items, filters)
			pageItems, pr := SlicePage(filtered, page)
			return PlaceVisitList{
				Visits:          pageItems,
				LibraryListMeta: MetaForPage(pr, true, AnyServiceTruncated(bySvc), bySvc),
			}
		},
		RedactSearch: func(items []PlaceVisit, r *http.Request) []PlaceVisit {
			return searchRedactPlaceVisits(k.h, items, r)
		},
		MarkCanonical: k.mark,
		CacheKey:      "library:places" + filters.CacheKeySuffix(),
	})
}

// placesProviderTargets collects providers, applying per-service and
// app-level city/read/precise redaction so a city-only surface never
// contributes place names or coordinates (issue #142).
func placesProviderTargets(h Host, r *http.Request, permType string, appLevel string) ([]NamedLister[PlaceVisit], []string) {
	targets, denied := Collect[PlaceVisit, PlaceVisitProvider](h, r, permType,
		func(p PlaceVisitProvider) func(context.Context) ([]PlaceVisit, bool, error) {
			return p.ListUserPlaceVisits
		})
	appPlaces := policy.PlacesLevelForResponse(appLevel)
	for i := range targets {
		id := targets[i].ID
		inner := targets[i].List
		level := ""
		if _, ok := h.Service(id); ok {
			level = h.RequestLevel(r, id, permType)
		}
		effective := policy.PlacesLevelForResponse(level)
		if appPlaces != "" && appPlaces != policy.AccessNone {
			// App scope is an additional clamp: take the tighter of the two.
			effective = tighterPlacesLevel(appPlaces, effective)
		}
		if effective == policy.PlacesLevelForResponse(level) && effective != policy.AccessCity && effective != policy.AccessRead {
			continue
		}
		targets[i].List = func(ctx context.Context) ([]PlaceVisit, bool, error) {
			items, truncated, err := inner(ctx)
			return redactPlaceVisitsForLevel(items, effective), truncated, err
		}
	}
	return targets, denied
}

// searchRedactPlaceVisits re-applies per-source city/read/precise redaction
// to index-hydrated visits (issue #178). Applying every source's level in
// turn settles on the most restrictive one (never under-redact); the app
// scope is an additional clamp, matching the fetch path.
func searchRedactPlaceVisits(h Host, items []PlaceVisit, r *http.Request) []PlaceVisit {
	appLevel, isApp := h.AppLevel(r, "places")
	appPlaces := ""
	if isApp {
		appPlaces = policy.PlacesLevelForResponse(appLevel)
	}
	out := make([]PlaceVisit, 0, len(items))
	for _, v := range items {
		skip := false
		for _, src := range v.Sources {
			if _, ok := h.Service(src.Service); !ok {
				continue
			}
			level := policy.PlacesLevelForResponse(h.RequestLevel(r, src.Service, "places"))
			if appPlaces != "" && appPlaces != policy.AccessNone {
				level = tighterPlacesLevel(appPlaces, level)
			}
			red := redactPlaceVisitsForLevel([]PlaceVisit{v}, level)
			if len(red) == 0 {
				skip = true
				break
			}
			v = red[0]
		}
		if skip {
			continue
		}
		if appPlaces != "" && appPlaces != policy.AccessNone {
			red := redactPlaceVisitsForLevel([]PlaceVisit{v}, appPlaces)
			if len(red) == 0 {
				continue
			}
			v = red[0]
		}
		out = append(out, v)
	}
	return out
}

func filterPlaceVisits(in []PlaceVisit, f PlaceVisitFilters) []PlaceVisit {
	if f.Empty() {
		return in
	}
	out := make([]PlaceVisit, 0, len(in))
	for _, v := range in {
		if f.Matches(v) {
			out = append(out, v)
		}
	}
	return out
}

// redactPlaceVisitCity keeps only locality, country, and the time window —
// enough for "was I out of town?" without names, addresses, coordinates, or
// raw point IDs (issue #142).
func redactPlaceVisitCity(v PlaceVisit) PlaceVisit {
	v.Place = Place{
		Locality: v.Place.Locality,
		Country:  v.Place.Country,
	}
	v.Confidence = ""
	v.EntityID = ""
	v.Match = ""
	v.Extra = nil
	for i := range v.Sources {
		v.Sources[i].ID = ""
		v.Sources[i].URL = ""
		v.Sources[i].PointIDs = nil
		v.Sources[i].Extra = nil
		v.Sources[i].Copy = nil
	}
	return v
}

// redactPlaceVisitRead keeps full place names and addresses but drops
// coordinates and raw point IDs (issue #142).
func redactPlaceVisitRead(v PlaceVisit) PlaceVisit {
	v.Place.Lat = 0
	v.Place.Lng = 0
	for i := range v.Sources {
		v.Sources[i].PointIDs = nil
	}
	return v
}

func redactPlaceVisitsForLevel(items []PlaceVisit, level string) []PlaceVisit {
	switch policy.PlacesLevelForResponse(level) {
	case policy.AccessNone:
		return nil
	case policy.AccessCity:
		out := make([]PlaceVisit, len(items))
		for i, v := range items {
			out[i] = redactPlaceVisitCity(v)
		}
		return out
	case policy.AccessRead:
		out := make([]PlaceVisit, len(items))
		for i, v := range items {
			out[i] = redactPlaceVisitRead(v)
		}
		return out
	default:
		// precise: full detail including coordinates.
		return items
	}
}

// tighterPlacesLevel returns the more restrictive of two places levels.
func tighterPlacesLevel(a, b string) string {
	rank := map[string]int{
		policy.AccessNone:    0,
		policy.AccessCity:    1,
		policy.AccessRead:    2,
		policy.AccessPrecise: 3,
	}
	ra, okA := rank[policy.PlacesLevelForResponse(a)]
	rb, okB := rank[policy.PlacesLevelForResponse(b)]
	if !okA {
		ra = 0
	}
	if !okB {
		rb = 0
	}
	if ra <= rb {
		return policy.PlacesLevelForResponse(a)
	}
	return policy.PlacesLevelForResponse(b)
}

func forEachPlaceVisitSource(item *PlaceVisit, f func(SourceMut)) {
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
