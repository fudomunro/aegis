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
	Aggregate(k.h, k.e, w, r, Spec[PlaceVisit]{
		Kind: kind,
		Targets: func(r *http.Request) ([]NamedLister[PlaceVisit], []string) {
			return placesProviderTargets(k.h, r, kind.PermType)
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

// placesProviderTargets collects providers, wrapping each target with
// city/read redaction so a reduced grant never contributes place names or
// coordinates (issue #142).
func placesProviderTargets(h Host, r *http.Request, permType string) ([]NamedLister[PlaceVisit], []string) {
	targets, denied := Collect[PlaceVisit, PlaceVisitProvider](h, r, permType,
		func(p PlaceVisitProvider) func(context.Context) ([]PlaceVisit, bool, error) {
			return p.ListUserPlaceVisits
		})
	grants, _ := h.Grants(r)
	level := policy.PlacesLevelForResponse(grants.Level(permType))
	if level != policy.AccessCity && level != policy.AccessRead {
		return targets, denied
	}
	for i := range targets {
		inner := targets[i].List
		targets[i].List = func(ctx context.Context) ([]PlaceVisit, bool, error) {
			items, truncated, err := inner(ctx)
			return redactPlaceVisitsForLevel(items, level), truncated, err
		}
	}
	return targets, denied
}

// searchRedactPlaceVisits re-applies grant-level city/read/precise redaction
// to index-hydrated visits (issue #178): the index stores owner-tier rows,
// so the request's level narrows them on the way out.
func searchRedactPlaceVisits(h Host, items []PlaceVisit, r *http.Request) []PlaceVisit {
	grants, _ := h.Grants(r)
	return redactPlaceVisitsForLevel(items, policy.PlacesLevelForResponse(grants.Level("places")))
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
