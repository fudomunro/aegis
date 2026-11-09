package kinds

import (
	"math"
	"strconv"
	"strings"
)

// Identity, normalization, and query-filter helpers shared by the core merge
// pipeline and the connector-family import stores. Identity keys live beside
// TrackIdentityKey in this package so every transport computes them the same
// way (issue #266).

// ExtraString reads a string Extra field.
func ExtraString(extra map[string]any, key string) string {
	if extra == nil {
		return ""
	}
	v, ok := extra[key]
	if !ok || v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

// NormalizeHandleList normalizes contact handles and drops duplicates.
func NormalizeHandleList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, h := range in {
		n := NormalizeContactHandle(h)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// TransactionDay returns the calendar day (UTC) of a posted/authorized time.
func TransactionDay(raw string) string {
	n := NormalizeTransactionDate(raw)
	if n == "" {
		return ""
	}
	if len(n) >= 10 {
		return n[:10]
	}
	return n
}

// DateAtOrAfter reports whether day is on/after the bound's calendar day.
func DateAtOrAfter(day, bound string) bool {
	b := TransactionDay(bound)
	if b == "" {
		return true
	}
	return day >= b
}

// DateBefore reports whether day is strictly before the bound's calendar day.
func DateBefore(day, bound string) bool {
	b := TransactionDay(bound)
	if b == "" {
		return true
	}
	return day < b
}

// NormalizeTransactionAmount parses a signed decimal string into a canonical
// form ("+0.00" / "-12.34") so the same money value matches across providers.
// Accepts optional leading +/-. Thousand separators are stripped.
func NormalizeTransactionAmount(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, ",", "")
	s = strings.ReplaceAll(s, " ", "")
	neg := false
	if strings.HasPrefix(s, "-") {
		neg = true
		s = s[1:]
	} else if strings.HasPrefix(s, "+") {
		s = s[1:]
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Normalize to at least two decimal places without floating money.
	if !strings.Contains(s, ".") {
		s += ".00"
	} else {
		parts := strings.SplitN(s, ".", 2)
		frac := parts[1]
		for len(frac) < 2 {
			frac += "0"
		}
		s = parts[0] + "." + frac
	}
	if _, err := strconv.ParseFloat(s, 64); err != nil {
		return strings.TrimSpace(raw)
	}
	if neg {
		return "-" + s
	}
	return s
}

// TransactionMerchantOrDescription returns the best merchant-ish string.
func TransactionMerchantOrDescription(t Transaction) string {
	if m := strings.TrimSpace(t.Merchant); m != "" {
		return m
	}
	return strings.TrimSpace(t.Description)
}

// TransactionServiceName returns the first source service name for stable sort.
func TransactionServiceName(t Transaction) string {
	if len(t.Sources) > 0 {
		return t.Sources[0].ServiceName
	}
	return ""
}

// TransactionIdentityKey is the sort/merge key. Prefer durable provider IDs
// when present; otherwise amount + posted day + normalized merchant.
// Transactions without any identity fall back to service:id so they stay
// distinct.
func TransactionIdentityKey(t Transaction) string {
	if id := strings.ToLower(strings.TrimSpace(t.EntityID)); id != "" {
		return "pid:" + id
	}
	for _, src := range t.Sources {
		if id := strings.ToLower(strings.TrimSpace(ExtraString(src.Extra, "provider_id"))); id != "" {
			return "pid:" + id
		}
		if id := strings.ToLower(strings.TrimSpace(ExtraString(src.Extra, "import_id"))); id != "" {
			return "pid:" + id
		}
		if id := strings.ToLower(strings.TrimSpace(ExtraString(src.Extra, "fitid"))); id != "" {
			return "pid:" + id
		}
	}
	amt := NormalizeTransactionAmount(t.Amount)
	merchant := NormalizeIdentityKey(TransactionMerchantOrDescription(t))
	day := TransactionDay(t.PostedAt)
	if day == "" {
		day = TransactionDay(t.AuthorizedAt)
	}
	if amt == "" || merchant == "" || day == "" {
		if len(t.Sources) > 0 && t.Sources[0].Service != "" && t.Sources[0].ID != "" {
			return "src:" + t.Sources[0].Service + ":" + t.Sources[0].ID
		}
		return "anon:" + TransactionServiceName(t) + ":" + t.Amount + ":" + t.PostedAt
	}
	return "amd:" + amt + "|d:" + day + "|m:" + merchant
}

// PlaceVisitServiceName returns the first source service name for stable sort.
func PlaceVisitServiceName(v PlaceVisit) string {
	if len(v.Sources) > 0 {
		return v.Sources[0].ServiceName
	}
	return ""
}

// PlaceMergeKey is the durable-ish place identity: locality+name when named,
// otherwise coordinates rounded to ~100m.
func PlaceMergeKey(p Place) string {
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
		return "g:" + strconv.FormatFloat(RoundCoord(p.Lat), 'f', 3, 64) +
			"," + strconv.FormatFloat(RoundCoord(p.Lng), 'f', 3, 64)
	}
	return ""
}

// RoundCoord rounds to ~0.001° (~100m) so provider jitter does not split
// the same place across merge keys.
func RoundCoord(v float64) float64 {
	return float64(int(v*1000+math.Copysign(0.5, v))) / 1000
}

// PlaceIdentityKey is the sort/merge key. Prefer durable provider IDs when
// present; otherwise start day + end day + normalized place key. Visits
// without any identity fall back to service:id so they stay distinct.
func PlaceIdentityKey(v PlaceVisit) string {
	if id := strings.ToLower(strings.TrimSpace(v.EntityID)); id != "" {
		return "pid:" + id
	}
	for _, src := range v.Sources {
		if id := strings.ToLower(strings.TrimSpace(ExtraString(src.Extra, "provider_id"))); id != "" {
			return "pid:" + id
		}
		if id := strings.ToLower(strings.TrimSpace(ExtraString(src.Extra, "place_id"))); id != "" {
			return "pid:" + id
		}
		if id := strings.ToLower(strings.TrimSpace(src.ID)); id != "" {
			return "pid:" + id
		}
	}
	start := NormalizePlaceVisitTime(v.StartAt)
	end := NormalizePlaceVisitTime(v.EndAt)
	placeKey := PlaceMergeKey(v.Place)
	if start == "" || placeKey == "" {
		if len(v.Sources) > 0 && v.Sources[0].Service != "" && v.Sources[0].ID != "" {
			return "src:" + v.Sources[0].Service + ":" + v.Sources[0].ID
		}
		return "anon:" + PlaceVisitServiceName(v) + ":" + v.StartAt
	}
	return "wp:" + start + "|" + end + "|" + placeKey
}
