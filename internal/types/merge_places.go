package types

import (
	"sort"
	"strings"
	"time"
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
	} else if src.StartAt != "" && NormalizePlaceVisitTime(src.StartAt) < NormalizePlaceVisitTime(dst.StartAt) {
		dst.StartAt = src.StartAt
	}
	if src.EndAt != "" && NormalizePlaceVisitTime(src.EndAt) > NormalizePlaceVisitTime(dst.EndAt) {
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
		key := PlaceIdentityKey(v)
		if key == "" {
			continue
		}
		if cur, ok := byKey[key]; ok {
			mergePlaceVisitInto(&cur.v, v)
			continue
		}
		cp := ClonePlaceVisit(v)
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
		si, sj := NormalizePlaceVisitTime(out[i].StartAt), NormalizePlaceVisitTime(out[j].StartAt)
		if si != sj {
			return si < sj // chronological
		}
		return PlaceIdentityKey(out[i]) < PlaceIdentityKey(out[j])
	})
	return out
}

// placeVisitsMatchNearbyWindow reports whether two visits describe the same
// place within a small time slack (overlapping or gap ≤ slack seconds).
func placeVisitsMatchNearbyWindow(a, b PlaceVisit, slackSec int64) bool {
	// Only merge like with like — a trip into a town is not the visit there.
	if NormalizePlaceVisitKind(a.Kind) != NormalizePlaceVisitKind(b.Kind) {
		return false
	}
	// Same durable ID is already handled in phase 1; only window+place here.
	pkA, pkB := PlaceMergeKey(a.Place), PlaceMergeKey(b.Place)
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
		t := NormalizePlaceVisitTime(raw)
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
			if val := strings.ToLower(strings.TrimSpace(ExtraString(src.Extra, k))); val != "" {
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
