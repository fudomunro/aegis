package core

import (
	"context"
	"net/http"

	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/types"
)

func filterPlaceVisits(in []types.PlaceVisit, f types.PlaceVisitFilters) []types.PlaceVisit {
	if f.Empty() {
		return in
	}
	out := make([]types.PlaceVisit, 0, len(in))
	for _, v := range in {
		if f.Matches(v) {
			out = append(out, v)
		}
	}
	return out
}

// RedactPlaceVisitCity keeps only locality, country, and the time window —
// enough for "was I out of town?" without names, addresses, coordinates, or
// raw point IDs (issue #142).
func RedactPlaceVisitCity(v types.PlaceVisit) types.PlaceVisit {
	v.Place = types.Place{
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

// RedactPlaceVisitRead keeps full place names and addresses but drops
// coordinates and raw point IDs (issue #142).
func RedactPlaceVisitRead(v types.PlaceVisit) types.PlaceVisit {
	v.Place.Lat = 0
	v.Place.Lng = 0
	for i := range v.Sources {
		v.Sources[i].PointIDs = nil
	}
	return v
}

func redactPlaceVisitsForLevel(items []types.PlaceVisit, level string) []types.PlaceVisit {
	switch policy.PlacesLevelForResponse(level) {
	case policy.AccessNone:
		return nil
	case policy.AccessCity:
		out := make([]types.PlaceVisit, len(items))
		for i, v := range items {
			out[i] = RedactPlaceVisitCity(v)
		}
		return out
	case policy.AccessRead:
		out := make([]types.PlaceVisit, len(items))
		for i, v := range items {
			out[i] = RedactPlaceVisitRead(v)
		}
		return out
	default:
		// precise: full detail including coordinates.
		return items
	}
}

// placesProviderTargets collects providers, applying per-service and
// app-level city/read/precise redaction so a city-only surface never
// contributes place names or coordinates (issue #142).
func placesProviderTargets(s *Server, r *http.Request, permType string, appLevel string) ([]namedLister[types.PlaceVisit], []string) {
	targets, denied := collectProviders[types.PlaceVisit, types.PlaceVisitProvider](s, r, permType,
		func(p types.PlaceVisitProvider) func(context.Context) ([]types.PlaceVisit, bool, error) {
			return p.ListUserPlaceVisits
		})
	appPlaces := policy.PlacesLevelForResponse(appLevel)
	for i := range targets {
		id := targets[i].id
		inner := targets[i].list
		level := ""
		if svc := s.config.GetService(id); svc != nil {
			level = s.requestLevel(r, svc, permType)
		}
		effective := policy.PlacesLevelForResponse(level)
		if appPlaces != "" && appPlaces != policy.AccessNone {
			// App scope is an additional clamp: take the tighter of the two.
			effective = tighterPlacesLevel(appPlaces, effective)
		}
		if effective == policy.PlacesLevelForResponse(level) && effective != policy.AccessCity && effective != policy.AccessRead {
			continue
		}
		targets[i].list = func(ctx context.Context) ([]types.PlaceVisit, bool, error) {
			items, truncated, err := inner(ctx)
			return redactPlaceVisitsForLevel(items, effective), truncated, err
		}
	}
	return targets, denied
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

// HandleLibraryPlaceVisits returns the user's visits and trips from every
// connected source: GET /v1/library/places
// Query filters: after, before, locality, kind (issue #142).
// Permission key: places. Default is none. Access city|read|precise redacts.
func (s *Server) HandleLibraryPlaceVisits(w http.ResponseWriter, r *http.Request) {
	kind := *LibraryKindByID("places")
	filters := types.ParsePlaceVisitFilters(r)
	appLevel := ""
	if app := AppIdentityFrom(r); app != nil {
		appLevel = app.Scopes.For(kind.PermType)
	}
	handleLibraryAggregate(s, w, r, libraryKindSpec[types.PlaceVisit]{
		kind: kind,
		targets: func(s *Server, r *http.Request) ([]namedLister[types.PlaceVisit], []string) {
			return placesProviderTargets(s, r, kind.PermType, appLevel)
		},
		serviceOf: types.PlaceVisitServiceName,
		sortKey:   types.PlaceIdentityKey,
		merge:     types.MergePlaceVisits,
		encode: func(items []types.PlaceVisit, page types.PageRequest, bySvc map[string]types.LibraryServiceStatus) any {
			filtered := filterPlaceVisits(items, filters)
			pageItems, pr := types.SlicePage(filtered, page)
			return types.PlaceVisitList{
				Visits:          pageItems,
				LibraryListMeta: types.MetaForPage(pr, true, anyServiceTruncated(bySvc), bySvc),
			}
		},
		redactSearch: func(items []types.PlaceVisit, r *http.Request) []types.PlaceVisit {
			return s.searchRedactPlaceVisits(items, r)
		},
		markCanonical: s.stampPlaceVisitCanonical,
		cacheKey:      "library:places" + filters.CacheKeySuffix(),
	})
}

// searchRedactPlaceVisits re-applies per-source city/read/precise redaction
// to index-hydrated visits (issue #178). Applying every source's level in
// turn settles on the most restrictive one (never under-redact); the app
// scope is an additional clamp, matching the fetch path.
func (s *Server) searchRedactPlaceVisits(items []types.PlaceVisit, r *http.Request) []types.PlaceVisit {
	appPlaces := ""
	if app := AppIdentityFrom(r); app != nil {
		appPlaces = policy.PlacesLevelForResponse(app.Scopes.For("places"))
	}
	out := make([]types.PlaceVisit, 0, len(items))
	for _, v := range items {
		skip := false
		for _, src := range v.Sources {
			svc := s.config.GetService(src.Service)
			if svc == nil {
				continue
			}
			level := policy.PlacesLevelForResponse(s.requestLevel(r, svc, "places"))
			if appPlaces != "" && appPlaces != policy.AccessNone {
				level = tighterPlacesLevel(appPlaces, level)
			}
			red := redactPlaceVisitsForLevel([]types.PlaceVisit{v}, level)
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
			red := redactPlaceVisitsForLevel([]types.PlaceVisit{v}, appPlaces)
			if len(red) == 0 {
				continue
			}
			v = red[0]
		}
		out = append(out, v)
	}
	return out
}
