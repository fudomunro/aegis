package core

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

// Match methods for PlaceVisit.
const (
	PlaceVisitMatchProviderID = "provider_id"
	PlaceVisitMatchWindow     = "window_place"
)






// placeVisitMatchKind records how identity was established.
func placeVisitMatchKind(key string) string {
	switch {
	case strings.HasPrefix(key, "pid:"):
		return PlaceVisitMatchProviderID
	case strings.HasPrefix(key, "wp:"):
		return PlaceVisitMatchWindow
	default:
		return ""
	}
}

func mergePlaceInto(dst *types.Place, src types.Place) {
	if dst.Name == "" {
		dst.Name = src.Name
	}
	if dst.Address == "" {
		dst.Address = src.Address
	}
	if dst.Locality == "" {
		dst.Locality = src.Locality
	}
	if dst.Country == "" {
		dst.Country = src.Country
	}
	if dst.Lat == 0 && dst.Lng == 0 && (src.Lat != 0 || src.Lng != 0) {
		dst.Lat, dst.Lng = src.Lat, src.Lng
	}
}

func mergePlaceVisitInto(dst *types.PlaceVisit, src types.PlaceVisit) {
	if dst.EndAt == "" {
		dst.EndAt = src.EndAt
	}
	if dst.StartAt == "" {
		dst.StartAt = src.StartAt
	} else if src.StartAt != "" && types.NormalizePlaceVisitTime(src.StartAt) < types.NormalizePlaceVisitTime(dst.StartAt) {
		dst.StartAt = src.StartAt
	}
	if src.EndAt != "" && types.NormalizePlaceVisitTime(src.EndAt) > types.NormalizePlaceVisitTime(dst.EndAt) {
		dst.EndAt = src.EndAt
	}
	mergePlaceInto(&dst.Place, src.Place)
	if dst.Kind == "" {
		dst.Kind = src.Kind
	}
	if dst.Mode == "" {
		dst.Mode = src.Mode
	}
	if dst.Confidence == "" {
		dst.Confidence = src.Confidence
	}
	if dst.EntityID == "" {
		dst.EntityID = src.EntityID
	}
	dst.Sources = append(dst.Sources, src.Sources...)
	if len(dst.Extra) == 0 && len(src.Extra) > 0 {
		dst.Extra = src.Extra
	}
}

// MergePlaceVisits groups visits that share a durable provider ID or the same
// start/end window + place key into one user-facing entry with multiple
// Sources. A second pass joins same-place visits whose windows overlap or sit
// within 15 minutes — the cross-provider near-duplicate rule (issue #142).
// Name-only place keys still merge when the window agrees; a visit at a
// different time is never merged.
func MergePlaceVisits(in []types.PlaceVisit) []types.PlaceVisit {
	if len(in) == 0 {
		return nil
	}
	type slot struct {
		v   types.PlaceVisit
		key string
	}
	var order []string
	byKey := map[string]*slot{}
	for _, v := range in {
		key := types.PlaceIdentityKey(v)
		if key == "" {
			continue
		}
		if cur, ok := byKey[key]; ok {
			mergePlaceVisitInto(&cur.v, v)
			continue
		}
		cp := types.ClonePlaceVisit(v)
		byKey[key] = &slot{v: cp, key: key}
		order = append(order, key)
	}

	// Phase 2: join same-place windows that overlap or sit within 15 minutes
	// (provider clock skew / different local IDs for the same stay).
	const windowSlack = 15 * 60 // seconds
	for i := 0; i < len(order); i++ {
		aKey := order[i]
		a, ok := byKey[aKey]
		if !ok {
			continue
		}
		for j := i + 1; j < len(order); j++ {
			bKey := order[j]
			b, ok := byKey[bKey]
			if !ok {
				continue
			}
			if !placeVisitsMatchNearbyWindow(a.v, b.v, windowSlack) {
				continue
			}
			mergePlaceVisitInto(&a.v, b.v)
			delete(byKey, bKey)
		}
	}

	var keys []string
	for _, key := range order {
		if _, ok := byKey[key]; ok {
			keys = append(keys, key)
		}
	}

	out := make([]types.PlaceVisit, 0, len(keys))
	for _, key := range keys {
		item, ok := byKey[key]
		if !ok {
			continue
		}
		if len(item.v.Sources) > 1 {
			if mk := placeVisitMatchKind(key); mk != "" {
				item.v.Match = mk
			} else {
				item.v.Match = PlaceVisitMatchWindow
			}
			// Cross-source joins that used window+place even when both sides
			// had provider IDs should record the weaker match kind.
			if strings.HasPrefix(key, "pid:") && !placeDurableIDsAgree(item.v) {
				item.v.Match = PlaceVisitMatchWindow
			}
			sort.SliceStable(item.v.Sources, func(i, j int) bool {
				if item.v.Sources[i].Canonical != item.v.Sources[j].Canonical {
					return item.v.Sources[i].Canonical
				}
				if item.v.Sources[i].ServiceName != item.v.Sources[j].ServiceName {
					return item.v.Sources[i].ServiceName < item.v.Sources[j].ServiceName
				}
				return item.v.Sources[i].Service < item.v.Sources[j].Service
			})
		}
		out = append(out, item.v)
	}

	sort.SliceStable(out, func(i, j int) bool {
		si, sj := types.NormalizePlaceVisitTime(out[i].StartAt), types.NormalizePlaceVisitTime(out[j].StartAt)
		if si != sj {
			return si < sj // chronological
		}
		return types.PlaceIdentityKey(out[i]) < types.PlaceIdentityKey(out[j])
	})
	return out
}

// placeVisitsMatchNearbyWindow reports whether two visits describe the same
// place within a small time slack (overlapping or gap ≤ slack seconds).
func placeVisitsMatchNearbyWindow(a, b types.PlaceVisit, slackSec int64) bool {
	// Only merge like with like — a trip into a town is not the visit there.
	if types.NormalizePlaceVisitKind(a.Kind) != types.NormalizePlaceVisitKind(b.Kind) {
		return false
	}
	// Same durable ID is already handled in phase 1; only window+place here.
	pkA, pkB := types.PlaceMergeKey(a.Place), types.PlaceMergeKey(b.Place)
	if pkA == "" || pkA != pkB {
		return false
	}
	as, ae := placeWindowSecs(a)
	bs, be := placeWindowSecs(b)
	if as == 0 && bs == 0 {
		return false
	}
	// Overlap or near-miss gap.
	if ae == 0 {
		ae = as
	}
	if be == 0 {
		be = bs
	}
	// a before b
	if ae+slackSec < bs {
		return false
	}
	// b before a
	if be+slackSec < as {
		return false
	}
	return true
}

func placeWindowSecs(v types.PlaceVisit) (start, end int64) {
	parse := func(raw string) int64 {
		t := types.NormalizePlaceVisitTime(raw)
		if t == "" {
			return 0
		}
		ts, err := time.Parse(time.RFC3339, t)
		if err != nil {
			return 0
		}
		return ts.Unix()
	}
	start = parse(v.StartAt)
	end = parse(v.EndAt)
	if end == 0 {
		end = start
	}
	return start, end
}

// placeDurableIDsAgree reports whether every source's provider ID is the same
// (or only one side carries one).
func placeDurableIDsAgree(v types.PlaceVisit) bool {
	seen := map[string]bool{}
	for _, src := range v.Sources {
		id := ""
		for _, k := range []string{"provider_id", "place_id"} {
			if val := strings.ToLower(strings.TrimSpace(types.ExtraString(src.Extra, k))); val != "" {
				id = val
				break
			}
		}
		if id == "" {
			id = strings.ToLower(strings.TrimSpace(src.ID))
		}
		if id == "" {
			id = strings.ToLower(strings.TrimSpace(v.EntityID))
		}
		if id == "" {
			continue
		}
		seen[id] = true
	}
	return len(seen) <= 1
}

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
	switch types.PlacesLevelForResponse(level) {
	case types.AccessNone:
		return nil
	case types.AccessCity:
		out := make([]types.PlaceVisit, len(items))
		for i, v := range items {
			out[i] = RedactPlaceVisitCity(v)
		}
		return out
	case types.AccessRead:
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
	appPlaces := types.PlacesLevelForResponse(appLevel)
	for i := range targets {
		id := targets[i].id
		inner := targets[i].list
		level := ""
		if svc := s.config.GetService(id); svc != nil {
			level = s.requestLevel(r, svc, permType)
		}
		effective := types.PlacesLevelForResponse(level)
		if appPlaces != "" && appPlaces != types.AccessNone {
			// App scope is an additional clamp: take the tighter of the two.
			effective = tighterPlacesLevel(appPlaces, effective)
		}
		if effective == types.PlacesLevelForResponse(level) && effective != types.AccessCity && effective != types.AccessRead {
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
		types.AccessNone:    0,
		types.AccessCity:    1,
		types.AccessRead:    2,
		types.AccessPrecise: 3,
	}
	ra, okA := rank[types.PlacesLevelForResponse(a)]
	rb, okB := rank[types.PlacesLevelForResponse(b)]
	if !okA {
		ra = 0
	}
	if !okB {
		rb = 0
	}
	if ra <= rb {
		return types.PlacesLevelForResponse(a)
	}
	return types.PlacesLevelForResponse(b)
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
		merge:     MergePlaceVisits,
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
		appPlaces = types.PlacesLevelForResponse(app.Scopes.For("places"))
	}
	out := make([]types.PlaceVisit, 0, len(items))
	for _, v := range items {
		skip := false
		for _, src := range v.Sources {
			svc := s.config.GetService(src.Service)
			if svc == nil {
				continue
			}
			level := types.PlacesLevelForResponse(s.requestLevel(r, svc, "places"))
			if appPlaces != "" && appPlaces != types.AccessNone {
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
		if appPlaces != "" && appPlaces != types.AccessNone {
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
