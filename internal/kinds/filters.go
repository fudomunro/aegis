package kinds

import (
	"net/http"
	"strings"
)

// Agent-facing list filters over neutral models. Both the core library
// endpoints and the connector-family import stores parse and apply these,
// so they live beside the models they filter (issue #266).

// TransactionFilters are agent-facing list filters for GET /v1/library/transactions.
type TransactionFilters struct {
	After    string // inclusive lower bound on posted_at (date or RFC3339)
	Before   string // exclusive upper bound on posted_at (date or RFC3339)
	Account  string // account name / last4 substring
	Q        string // free-text on description/merchant/category/memo
	Currency string // ISO currency filter
}

// ParseTransactionFilters reads query parameters from the request.
func ParseTransactionFilters(r *http.Request) TransactionFilters {
	q := r.URL.Query()
	return TransactionFilters{
		After:    strings.TrimSpace(q.Get("after")),
		Before:   strings.TrimSpace(q.Get("before")),
		Account:  strings.TrimSpace(q.Get("account")),
		Q:        strings.TrimSpace(q.Get("q")),
		Currency: strings.TrimSpace(q.Get("currency")),
	}
}

// Empty reports whether no filter is active.
func (f TransactionFilters) Empty() bool {
	return f.After == "" && f.Before == "" && f.Account == "" && f.Q == "" && f.Currency == ""
}

// CacheKeySuffix is the cache-key fragment so filtered requests do not share
// unfiltered cache entries.
func (f TransactionFilters) CacheKeySuffix() string {
	if f.Empty() {
		return ""
	}
	var b strings.Builder
	write := func(k, v string) {
		if v == "" {
			return
		}
		b.WriteByte('|')
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(v)
	}
	write("after", f.After)
	write("before", f.Before)
	write("account", f.Account)
	write("q", strings.ToLower(f.Q))
	write("currency", strings.ToUpper(f.Currency))
	return b.String()
}

// Matches reports whether a transaction passes every active filter.
func (f TransactionFilters) Matches(t Transaction) bool {
	if f.Empty() {
		return true
	}
	day := TransactionDay(t.PostedAt)
	if day == "" {
		day = TransactionDay(t.AuthorizedAt)
	}
	if f.After != "" {
		if day == "" || !DateAtOrAfter(day, f.After) {
			return false
		}
	}
	if f.Before != "" {
		if day == "" || !DateBefore(day, f.Before) {
			return false
		}
	}
	if f.Currency != "" && !strings.EqualFold(t.Currency, f.Currency) {
		return false
	}
	if f.Account != "" {
		if t.Account == nil {
			return false
		}
		hay := strings.ToLower(t.Account.Name + " " + t.Account.Type + " " + t.Account.Last4)
		if !strings.Contains(hay, strings.ToLower(f.Account)) {
			return false
		}
	}
	if f.Q != "" {
		need := strings.ToLower(f.Q)
		hay := strings.ToLower(strings.Join([]string{
			t.Description, t.Merchant, t.Memo,
		}, " ") + " " + strings.Join(t.Category, " "))
		if !strings.Contains(hay, need) {
			return false
		}
	}
	return true
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
		After:    NormalizePlaceVisitTime(q.Get("after")),
		Before:   NormalizePlaceVisitTime(q.Get("before")),
		Locality: strings.TrimSpace(q.Get("locality")),
		Kind:     NormalizePlaceVisitKind(q.Get("kind")),
	}
}

// Empty reports whether no filter is active.
func (f PlaceVisitFilters) Empty() bool {
	return f.After == "" && f.Before == "" && f.Locality == "" && f.Kind == ""
}

// Matches reports whether a place visit passes every active filter.
func (f PlaceVisitFilters) Matches(v PlaceVisit) bool {
	if f.Kind != "" && NormalizePlaceVisitKind(v.Kind) != f.Kind {
		return false
	}
	if f.Locality != "" {
		loc := NormalizeIdentityKey(v.Place.Locality)
		want := NormalizeIdentityKey(f.Locality)
		if loc == "" || !strings.Contains(loc, want) {
			return false
		}
	}
	start := NormalizePlaceVisitTime(v.StartAt)
	end := NormalizePlaceVisitTime(v.EndAt)
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

// CacheKeySuffix is the cache-key fragment so filtered requests do not share
// unfiltered cache entries.
func (f PlaceVisitFilters) CacheKeySuffix() string {
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
