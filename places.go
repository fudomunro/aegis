package main

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// PlaceVisitProvider is implemented by connectors that can return the user's
// location history as visits and trips in Aegis's neutral format (issue #142).
// The aggregate library endpoint uses this so agents can answer "where was I?"
// with evidence rather than booking intent. Each returned PlaceVisit should
// carry one PlaceVisitSource for that service. Aegis models visits and trips —
// never raw GPS firehoses — and defaults the places permission to none.
type PlaceVisitProvider interface {
	// ListUserPlaceVisits returns neutral visits/trips (one
	// PlaceVisitSource each). Return ErrUnauthorized when no valid token
	// is available. truncated is true when the page cap was hit with more
	// items still upstream.
	ListUserPlaceVisits(ctx context.Context) (visits []PlaceVisit, truncated bool, err error)
}

// Match methods for PlaceVisit.
const (
	PlaceVisitMatchProviderID = "provider_id"
	PlaceVisitMatchWindow     = "window_place"
)

// placeVisitServiceName returns the first source service name for stable sort.
func placeVisitServiceName(v PlaceVisit) string {
	if len(v.Sources) > 0 {
		return v.Sources[0].ServiceName
	}
	return ""
}

// normalizePlaceVisitKind maps provider labels onto visit/trip.
func normalizePlaceVisitKind(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case PlaceKindVisit, "placevisit", "place_visit", "stay", "dwell", "stop":
		return PlaceKindVisit
	case PlaceKindTrip, "activitysegment", "activity_segment", "travel", "journey":
		return PlaceKindTrip
	case "":
		return PlaceKindVisit
	default:
		return PlaceKindVisit
	}
}

// normalizeTripMode maps provider travel modes onto the neutral mode list.
func normalizeTripMode(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, " ", "_")
	switch s {
	case "":
		return ""
	case "flight", "flying", "in_airplane", "airplane", "air", "plane":
		return TripModeFlight
	case "drive", "driving", "in_passenger_vehicle", "in_vehicle", "car", "vehicle", "motorcycle":
		return TripModeDrive
	case "walk", "walking", "on_foot", "foot":
		return TripModeWalk
	case "bicycle", "biking", "cycling", "on_bicycle":
		return TripModeBicycle
	case "train", "rail", "in_train":
		return TripModeTrain
	case "bus", "in_bus":
		return TripModeBus
	default:
		return TripModeUnknown
	}
}

// normalizePlaceVisitTime re-emits a provider timestamp at RFC3339 second
// precision so the same window matches across services.
func normalizePlaceVisitTime(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	if t, err := time.Parse("2006-01-02T15:04:05", raw); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	// Unix seconds as decimal string.
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 1_000_000_000 && n < 4_000_000_000 {
		return time.Unix(n, 0).UTC().Format(time.RFC3339)
	}
	return raw
}

// placeIdentityKey is the sort/merge key. Prefer durable provider IDs when
// present; otherwise start day + end day + normalized place key. Visits
// without any identity fall back to service:id so they stay distinct.
func placeIdentityKey(v PlaceVisit) string {
	if id := strings.ToLower(strings.TrimSpace(v.EntityID)); id != "" {
		return "pid:" + id
	}
	for _, src := range v.Sources {
		if id := strings.ToLower(strings.TrimSpace(messageExtraString(src.Extra, "provider_id"))); id != "" {
			return "pid:" + id
		}
		if id := strings.ToLower(strings.TrimSpace(messageExtraString(src.Extra, "place_id"))); id != "" {
			return "pid:" + id
		}
		if id := strings.ToLower(strings.TrimSpace(src.ID)); id != "" {
			return "pid:" + id
		}
	}
	start := normalizePlaceVisitTime(v.StartAt)
	end := normalizePlaceVisitTime(v.EndAt)
	placeKey := placeMergeKey(v.Place)
	if start == "" || placeKey == "" {
		if len(v.Sources) > 0 && v.Sources[0].Service != "" && v.Sources[0].ID != "" {
			return "src:" + v.Sources[0].Service + ":" + v.Sources[0].ID
		}
		return "anon:" + placeVisitServiceName(v) + ":" + v.StartAt
	}
	return "wp:" + start + "|" + end + "|" + placeKey
}

// placeMergeKey is the durable-ish place identity: locality+name when named,
// otherwise coordinates rounded to ~100m.
func placeMergeKey(p Place) string {
	name := NormalizeIdentityKey(p.Name)
	if name == "" {
		name = NormalizeIdentityKey(p.Address)
	}
	loc := NormalizeIdentityKey(p.Locality)
	country := NormalizeIdentityKey(p.Country)
	if name != "" || loc != "" {
		return "n:" + name + "|l:" + loc + "|c:" + country
	}
	if p.Lat != 0 || p.Lng != 0 {
		return "g:" + strconv.FormatFloat(roundCoord(p.Lat), 'f', 3, 64) +
			"," + strconv.FormatFloat(roundCoord(p.Lng), 'f', 3, 64)
	}
	return ""
}

// roundCoord rounds to ~0.001° (~100m) so provider jitter does not split
// the same place across merge keys.
func roundCoord(v float64) float64 {
	return float64(int(v*1000+copysign(0.5, v))) / 1000
}

func copysign(mag, sign float64) float64 {
	if sign < 0 {
		return -mag
	}
	return mag
}

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

func mergePlaceInto(dst *Place, src Place) {
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

func mergePlaceVisitInto(dst *PlaceVisit, src PlaceVisit) {
	if dst.EndAt == "" {
		dst.EndAt = src.EndAt
	}
	if dst.StartAt == "" {
		dst.StartAt = src.StartAt
	} else if src.StartAt != "" && normalizePlaceVisitTime(src.StartAt) < normalizePlaceVisitTime(dst.StartAt) {
		dst.StartAt = src.StartAt
	}
	if src.EndAt != "" && normalizePlaceVisitTime(src.EndAt) > normalizePlaceVisitTime(dst.EndAt) {
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

func clonePlaceVisit(v PlaceVisit) PlaceVisit {
	cp := v
	cp.Sources = append([]PlaceVisitSource(nil), v.Sources...)
	if len(v.Extra) > 0 {
		cp.Extra = make(map[string]any, len(v.Extra))
		for k, val := range v.Extra {
			cp.Extra[k] = val
		}
	}
	return cp
}

// MergePlaceVisits groups visits that share a durable provider ID or the same
// start/end window + place key into one user-facing entry with multiple
// Sources. A second pass joins same-place visits whose windows overlap or sit
// within 15 minutes — the cross-provider near-duplicate rule (issue #142).
// Name-only place keys still merge when the window agrees; a visit at a
// different time is never merged.
func MergePlaceVisits(in []PlaceVisit) []PlaceVisit {
	if len(in) == 0 {
		return nil
	}
	type slot struct {
		v   PlaceVisit
		key string
	}
	var order []string
	byKey := map[string]*slot{}
	for _, v := range in {
		key := placeIdentityKey(v)
		if key == "" {
			continue
		}
		if cur, ok := byKey[key]; ok {
			mergePlaceVisitInto(&cur.v, v)
			continue
		}
		cp := clonePlaceVisit(v)
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

	out := make([]PlaceVisit, 0, len(keys))
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
		si, sj := normalizePlaceVisitTime(out[i].StartAt), normalizePlaceVisitTime(out[j].StartAt)
		if si != sj {
			return si < sj // chronological
		}
		return placeIdentityKey(out[i]) < placeIdentityKey(out[j])
	})
	return out
}

// placeVisitsMatchNearbyWindow reports whether two visits describe the same
// place within a small time slack (overlapping or gap ≤ slack seconds).
func placeVisitsMatchNearbyWindow(a, b PlaceVisit, slackSec int64) bool {
	// Only merge like with like — a trip into a town is not the visit there.
	if normalizePlaceVisitKind(a.Kind) != normalizePlaceVisitKind(b.Kind) {
		return false
	}
	// Same durable ID is already handled in phase 1; only window+place here.
	pkA, pkB := placeMergeKey(a.Place), placeMergeKey(b.Place)
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

func placeWindowSecs(v PlaceVisit) (start, end int64) {
	parse := func(raw string) int64 {
		t := normalizePlaceVisitTime(raw)
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
func placeDurableIDsAgree(v PlaceVisit) bool {
	seen := map[string]bool{}
	for _, src := range v.Sources {
		id := ""
		for _, k := range []string{"provider_id", "place_id"} {
			if val := strings.ToLower(strings.TrimSpace(messageExtraString(src.Extra, k))); val != "" {
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

// PlaceVisitFilters is the query filter set for GET /v1/library/places.
type PlaceVisitFilters struct {
	After    string
	Before   string
	Locality string
	Kind     string
}

// ParsePlaceVisitFilters reads after, before, locality, and kind query params.
// The issue's API shape is GET /v1/library/places?after=&before=&locality=.
func ParsePlaceVisitFilters(r *http.Request) PlaceVisitFilters {
	q := r.URL.Query()
	return PlaceVisitFilters{
		After:    normalizePlaceVisitTime(q.Get("after")),
		Before:   normalizePlaceVisitTime(q.Get("before")),
		Locality: strings.TrimSpace(q.Get("locality")),
		Kind:     normalizePlaceVisitKind(q.Get("kind")),
	}
}

func (f PlaceVisitFilters) Empty() bool {
	return f.After == "" && f.Before == "" && f.Locality == "" && f.Kind == ""
}

func (f PlaceVisitFilters) matches(v PlaceVisit) bool {
	if f.Kind != "" && normalizePlaceVisitKind(v.Kind) != f.Kind {
		return false
	}
	if f.Locality != "" {
		loc := NormalizeIdentityKey(v.Place.Locality)
		want := NormalizeIdentityKey(f.Locality)
		if loc == "" || !strings.Contains(loc, want) {
			return false
		}
	}
	start := normalizePlaceVisitTime(v.StartAt)
	end := normalizePlaceVisitTime(v.EndAt)
	if end == "" {
		end = start
	}
	if f.After != "" {
		// Visit overlaps [after, ∞).
		if end != "" && end < f.After {
			return false
		}
		if end == "" && start < f.After {
			return false
		}
	}
	if f.Before != "" {
		// Visit overlaps (-∞, before).
		if start > f.Before {
			return false
		}
	}
	return true
}

func (f PlaceVisitFilters) cacheKeySuffix() string {
	if f.Empty() {
		return ""
	}
	var b strings.Builder
	if f.After != "" {
		b.WriteString("|after:" + f.After)
	}
	if f.Before != "" {
		b.WriteString("|before:" + f.Before)
	}
	if f.Locality != "" {
		b.WriteString("|loc:" + strings.ToLower(f.Locality))
	}
	if f.Kind != "" {
		b.WriteString("|kind:" + f.Kind)
	}
	return b.String()
}

func filterPlaceVisits(in []PlaceVisit, f PlaceVisitFilters) []PlaceVisit {
	if f.Empty() {
		return in
	}
	out := make([]PlaceVisit, 0, len(in))
	for _, v := range in {
		if f.matches(v) {
			out = append(out, v)
		}
	}
	return out
}

// RedactPlaceVisitCity keeps only locality, country, and the time window —
// enough for "was I out of town?" without names, addresses, coordinates, or
// raw point IDs (issue #142).
func RedactPlaceVisitCity(v PlaceVisit) PlaceVisit {
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

// RedactPlaceVisitRead keeps full place names and addresses but drops
// coordinates and raw point IDs (issue #142).
func RedactPlaceVisitRead(v PlaceVisit) PlaceVisit {
	v.Place.Lat = 0
	v.Place.Lng = 0
	for i := range v.Sources {
		v.Sources[i].PointIDs = nil
	}
	return v
}

func redactPlaceVisitsForLevel(items []PlaceVisit, level string) []PlaceVisit {
	switch PlacesLevelForResponse(level) {
	case AccessNone:
		return nil
	case AccessCity:
		out := make([]PlaceVisit, len(items))
		for i, v := range items {
			out[i] = RedactPlaceVisitCity(v)
		}
		return out
	case AccessRead:
		out := make([]PlaceVisit, len(items))
		for i, v := range items {
			out[i] = RedactPlaceVisitRead(v)
		}
		return out
	default:
		// precise: full detail including coordinates.
		return items
	}
}

// newPlaceVisit builds a single-source neutral visit.
func newPlaceVisit(service, serviceName, id string, v PlaceVisit) PlaceVisit {
	out := clonePlaceVisit(v)
	out.Kind = normalizePlaceVisitKind(out.Kind)
	out.Mode = normalizeTripMode(out.Mode)
	out.StartAt = normalizePlaceVisitTime(out.StartAt)
	out.EndAt = normalizePlaceVisitTime(out.EndAt)
	out.Sources = []PlaceVisitSource{{
		Service:     service,
		ServiceName: serviceName,
		ID:          id,
		Canonical:   true,
	}}
	return out
}

// placesProviderTargets collects providers, applying per-service and
// app-level city/read/precise redaction so a city-only surface never
// contributes place names or coordinates (issue #142).
func placesProviderTargets(s *Server, permType string, appLevel string) ([]namedLister[PlaceVisit], []string) {
	targets, denied := collectProviders[PlaceVisit, PlaceVisitProvider](s, permType,
		func(p PlaceVisitProvider) func(context.Context) ([]PlaceVisit, bool, error) {
			return p.ListUserPlaceVisits
		})
	appPlaces := PlacesLevelForResponse(appLevel)
	for i := range targets {
		id := targets[i].id
		inner := targets[i].list
		level := ""
		if svc := s.config.GetService(id); svc != nil {
			level = svc.Permissions.For(permType)
		}
		effective := PlacesLevelForResponse(level)
		if appPlaces != "" && appPlaces != AccessNone {
			// App scope is an additional clamp: take the tighter of the two.
			effective = tighterPlacesLevel(appPlaces, effective)
		}
		if effective == PlacesLevelForResponse(level) && effective != AccessCity && effective != AccessRead {
			continue
		}
		targets[i].list = func(ctx context.Context) ([]PlaceVisit, bool, error) {
			items, truncated, err := inner(ctx)
			return redactPlaceVisitsForLevel(items, effective), truncated, err
		}
	}
	return targets, denied
}

// tighterPlacesLevel returns the more restrictive of two places levels.
func tighterPlacesLevel(a, b string) string {
	rank := map[string]int{
		AccessNone:   0,
		AccessCity:   1,
		AccessRead:   2,
		AccessPrecise: 3,
	}
	ra, okA := rank[PlacesLevelForResponse(a)]
	rb, okB := rank[PlacesLevelForResponse(b)]
	if !okA {
		ra = 0
	}
	if !okB {
		rb = 0
	}
	if ra <= rb {
		return PlacesLevelForResponse(a)
	}
	return PlacesLevelForResponse(b)
}

// PhotosTakenDuring returns photos whose taken_at falls inside the visit or
// trip window (issue #142). This is the photos/places join demo: sample
// fixtures pair visits with photos captured during them.
func PhotosTakenDuring(photos []Photo, v PlaceVisit) []Photo {
	start := normalizePlaceVisitTime(v.StartAt)
	end := normalizePlaceVisitTime(v.EndAt)
	if end == "" {
		end = start
	}
	if start == "" {
		return nil
	}
	out := make([]Photo, 0, 8)
	for _, p := range photos {
		taken := normalizePlaceVisitTime(p.TakenAt)
		if taken == "" {
			continue
		}
		if taken < start {
			continue
		}
		// Open-ended visit (no end_at): any photo at or after start.
		if v.EndAt != "" && end != "" && taken > end {
			continue
		}
		out = append(out, p)
	}
	return out
}

// HandleLibraryPlaceVisits returns the user's visits and trips from every
// connected source: GET /v1/library/places
// Query filters: after, before, locality, kind (issue #142).
// Permission key: places. Default is none. Access city|read|precise redacts.
func (s *Server) HandleLibraryPlaceVisits(w http.ResponseWriter, r *http.Request) {
	kind := *LibraryKindByID("places")
	filters := ParsePlaceVisitFilters(r)
	appLevel := ""
	if app := AppIdentityFrom(r); app != nil {
		appLevel = app.Scopes.For(kind.PermType)
	}
	handleLibraryAggregate(s, w, r, libraryKindSpec[PlaceVisit]{
		kind: kind,
		targets: func(s *Server, r *http.Request) ([]namedLister[PlaceVisit], []string) {
			return placesProviderTargets(s, kind.PermType, appLevel)
		},
		serviceOf: placeVisitServiceName,
		sortKey:   placeIdentityKey,
		merge:     MergePlaceVisits,
		encode: func(merged []PlaceVisit, bySvc map[string]LibraryServiceStatus) any {
			filtered := filterPlaceVisits(merged, filters)
			return PlaceVisitList{
				Visits:    filtered,
				Total:     len(filtered),
				Synced:    true,
				Truncated: anyServiceTruncated(bySvc),
				ByService: bySvc,
			}
		},
		markCanonical: s.stampPlaceVisitCanonical,
		cacheKey:      "library:places" + filters.cacheKeySuffix(),
	})
}
