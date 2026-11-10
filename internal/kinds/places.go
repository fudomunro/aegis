package kinds

import (
	"context"
	"net/http"
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

// Grant keys are routes: the library-relative path (issue #348).
const (
	placesRouteKey      = "places"
	placesNamesRouteKey = "places/names"
	placesCityRouteKey  = "places/city"
)

func newPlacesKind(h Host, e *Engine, d Deps) Kind {
	k := &placesKind{h: h, e: e, localSource: d.PlacesLocalSource}
	k.mark, _ = Stamps(h, "places", forEachPlaceVisitSource)
	def := *BuiltinKindByID("places")
	k.providerKind = providerKind{
		def:      def,
		provides: providerProvides[PlaceVisitProvider],
		routes: []Route{
			{Pattern: def.Path, AppReadable: def.AppReadable, Handler: k.serve, GrantKey: placesRouteKey},
			{Pattern: "/v1/library/places/names", AppReadable: true, Handler: k.serveNames, GrantKey: placesNamesRouteKey},
			{Pattern: "/v1/library/places/city", AppReadable: true, Handler: k.serveCity, GrantKey: placesCityRouteKey},
		},
	}
	return k
}

// placeShape selects what a route returns (issue #348).
type placeShape int

const (
	// placeFull is names, addresses, and coordinates.
	placeFull placeShape = iota
	// placeNames is names and addresses without coordinates.
	placeNames
	// placeCity is locality, country, and the time window only.
	placeCity
)

// HasProviders adds the local Timeline import store to the connector scan
// (issue #142).
func (k *placesKind) HasProviders(h Host) bool {
	if k.providerKind.HasProviders(h) {
		return true
	}
	return k.localSource != nil && k.localSource()
}

// serve returns visits and trips with full detail including coordinates:
// GET /v1/library/places. Query filters: after, before, locality, kind
// (issue #142).
func (k *placesKind) serve(w http.ResponseWriter, r *http.Request) {
	k.serveShape(w, r, placesRouteKey, placeFull)
}

// serveNames returns visits with place names and addresses but no
// coordinates: GET /v1/library/places/names (issue #348).
func (k *placesKind) serveNames(w http.ResponseWriter, r *http.Request) {
	k.serveShape(w, r, placesNamesRouteKey, placeNames)
}

// serveCity returns locality, country, and the visit/trip window only:
// GET /v1/library/places/city (issue #348).
func (k *placesKind) serveCity(w http.ResponseWriter, r *http.Request) {
	k.serveShape(w, r, placesCityRouteKey, placeCity)
}

func (k *placesKind) serveShape(w http.ResponseWriter, r *http.Request, grantKey string, shape placeShape) {
	kind := k.def
	filters := ParsePlaceVisitFilters(r)
	Aggregate(k.h, k.e, w, r, Spec[PlaceVisit]{
		Kind:     kind,
		GrantKey: grantKey,
		Targets: func(r *http.Request) ([]NamedLister[PlaceVisit], []string) {
			return placesProviderTargets(k.h, r, grantKey)
		},
		ServiceOf: PlaceVisitServiceName,
		SortKey:   PlaceIdentityKey,
		Merge:     MergePlaceVisits,
		Encode: func(items []PlaceVisit, page PageRequest, bySvc map[string]LibraryServiceStatus) any {
			filtered := filterPlaceVisits(items, filters)
			switch shape {
			case placeCity:
				filtered = redactPlaceVisits(filtered, redactPlaceVisitCity)
			case placeNames:
				filtered = redactPlaceVisits(filtered, redactPlaceVisitRead)
			}
			pageItems, pr := SlicePage(filtered, page)
			return PlaceVisitList{
				Visits:          pageItems,
				LibraryListMeta: MetaForPage(pr, true, AnyServiceTruncated(bySvc), bySvc),
			}
		},
		MarkCanonical: k.mark,
		CacheKey:      "library:" + grantKey + filters.CacheKeySuffix(),
	})
}

// placesProviderTargets collects providers; the route's encode owns the
// disclosure shape (issue #348).
func placesProviderTargets(h Host, r *http.Request, grantKey string) ([]NamedLister[PlaceVisit], []string) {
	return Collect[PlaceVisit, PlaceVisitProvider](h, r, grantKey,
		func(p PlaceVisitProvider) func(context.Context) ([]PlaceVisit, bool, error) {
			return p.ListUserPlaceVisits
		})
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

// redactPlaceVisits applies one disclosure shape to visits.
func redactPlaceVisits(items []PlaceVisit, shape func(PlaceVisit) PlaceVisit) []PlaceVisit {
	out := make([]PlaceVisit, len(items))
	for i, v := range items {
		out[i] = shape(v)
	}
	return out
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
