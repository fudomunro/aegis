package kinds

import (
	"net/http"
	"sort"
	"strings"
	"time"
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

// MessageFilters are agent-facing list filters for GET /v1/library/messages
// (issue #311). They carry the unified retrieval surface's query semantics —
// the same parameter meanings the mail archive search always had: q is
// free-text, from/to are sender/recipient addresses only (never time bounds),
// after/before bound sent_at, label is repeatable, sort is asc|desc, and
// include_body fills Message.Body.
type MessageFilters struct {
	Q           string
	From        string
	To          string
	After       string // inclusive lower bound on sent_at (date or RFC3339)
	Before      string // exclusive upper bound on sent_at (date or RFC3339)
	Labels      []string
	Sort        string // "asc" or "desc" (default)
	IncludeBody bool
}

// ParseMessageFilters reads query parameters from the request.
func ParseMessageFilters(r *http.Request) MessageFilters {
	q := r.URL.Query()
	return MessageFilters{
		Q:           strings.TrimSpace(q.Get("q")),
		From:        strings.TrimSpace(q.Get("from")),
		To:          strings.TrimSpace(q.Get("to")),
		After:       strings.TrimSpace(q.Get("after")),
		Before:      strings.TrimSpace(q.Get("before")),
		Labels:      q["label"],
		Sort:        strings.TrimSpace(q.Get("sort")),
		IncludeBody: q.Get("include_body") == "1" || strings.EqualFold(q.Get("include_body"), "true"),
	}
}

// Empty reports whether no filter is active (sort and include_body excluded).
func (f MessageFilters) Empty() bool {
	return f.Q == "" && f.From == "" && f.To == "" && f.After == "" && f.Before == "" && !f.HasLabels()
}

// HasLabels reports whether any non-empty label filter is set.
func (f MessageFilters) HasLabels() bool {
	for _, l := range f.Labels {
		if strings.TrimSpace(l) != "" {
			return true
		}
	}
	return false
}

// Ascending reports the oldest-first sort order.
func (f MessageFilters) Ascending() bool {
	return strings.EqualFold(f.Sort, "asc")
}

// QueryMode reports whether the request needs full-history retrieval: any
// explicitly passed query parameter — history filter, sort order, or body
// inclusion. The bare request stays the capped collection overview (issue
// #311).
func (f MessageFilters) QueryMode() bool {
	return !f.Empty() || f.IncludeBody || f.Sort != ""
}

// NeedBody reports whether archived rows must load body text: either the
// response includes it, or free-text matching has to see it.
func (f MessageFilters) NeedBody() bool {
	return f.IncludeBody || f.Q != ""
}

// CacheKeySuffix is the cache-key fragment so filtered requests do not share
// unfiltered cache entries. An explicitly passed sort participates: it
// switches the request to full-history retrieval (issue #311).
func (f MessageFilters) CacheKeySuffix() string {
	if f.Empty() && !f.IncludeBody && f.Sort == "" {
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
	write("q", strings.ToLower(f.Q))
	write("from", strings.ToLower(f.From))
	write("to", strings.ToLower(f.To))
	write("after", f.After)
	write("before", f.Before)
	for _, l := range f.Labels {
		write("label", strings.ToLower(strings.TrimSpace(l)))
	}
	write("sort", strings.ToLower(f.Sort))
	if f.IncludeBody {
		b.WriteString("|body=1")
	}
	return b.String()
}

// Matches reports whether a message passes every active filter. The q
// matcher is a case-insensitive token AND over the searchable fields
// (subject, snippet, body, participants, labels) — a superset of the
// archive FTS match so index hits survive the merged-identity re-filter.
func (f MessageFilters) Matches(m Message) bool {
	if f.Empty() {
		return true
	}
	if f.After != "" || f.Before != "" {
		if !messageTimeMatches(m.SentAt, f.After, f.Before) {
			return false
		}
	}
	if f.From != "" {
		need := strings.ToLower(f.From)
		var names []string
		if m.From != nil {
			names = append(names, m.From.Name)
			names = append(names, m.From.Emails...)
		}
		hay := strings.ToLower(strings.Join(names, " "))
		if !strings.Contains(hay, need) {
			return false
		}
	}
	if f.To != "" {
		need := strings.ToLower(f.To)
		var names []string
		for _, c := range append(append([]Contact(nil), m.To...), m.CC...) {
			names = append(names, c.Name)
			names = append(names, c.Emails...)
		}
		hay := strings.ToLower(strings.Join(names, " "))
		if !strings.Contains(hay, need) {
			return false
		}
	}
	if f.HasLabels() {
		for _, want := range f.Labels {
			want = strings.ToLower(strings.TrimSpace(want))
			if want == "" {
				continue
			}
			found := false
			for _, l := range m.Labels {
				if strings.Contains(strings.ToLower(l), want) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	if f.Q != "" {
		hay := strings.ToLower(strings.Join([]string{
			m.Subject, m.Snippet, m.Body,
		}, " "))
		if m.From != nil {
			hay += " " + strings.ToLower(m.From.Name+" "+strings.Join(m.From.Emails, " "))
		}
		for _, c := range append(append([]Contact(nil), m.To...), m.CC...) {
			hay += " " + strings.ToLower(c.Name+" "+strings.Join(c.Emails, " "))
		}
		hay += " " + strings.ToLower(strings.Join(m.Labels, " "))
		for _, tok := range messageQueryTokens(f.Q) {
			if !strings.Contains(hay, tok) {
				return false
			}
		}
	}
	return true
}

// messageQueryTokens normalizes free-text into lowercase match tokens the
// same way the archive FTS expression builder does (operator characters
// stripped, whitespace split) so both sides agree on what q means.
func messageQueryTokens(q string) []string {
	var out []string
	for _, t := range strings.Fields(q) {
		cleaned := strings.Map(func(r rune) rune {
			if r == '"' {
				return -1
			}
			if r == '*' || r == '^' || r == ':' || r == '(' || r == ')' || r == '{' || r == '}' {
				return ' '
			}
			return r
		}, t)
		for _, sub := range strings.Fields(cleaned) {
			out = append(out, strings.ToLower(sub))
		}
	}
	return out
}

// messageTimeMatches applies after (inclusive) / before (exclusive) bounds
// to a sent_at value. Date bounds open a whole-day window; RFC3339 bounds
// compare at second precision. Unparseable values fall back to the same
// normalized string comparison the archive store performs.
func messageTimeMatches(sentAt, after, before string) bool {
	sentAt = strings.TrimSpace(sentAt)
	if after == "" && before == "" {
		return true
	}
	if t, err := parseMessageTime(sentAt); err == nil {
		if after != "" {
			if lb, ok := messageTimeBound(after, true); ok && t.Before(lb) {
				return false
			}
		}
		if before != "" {
			if ub, ok := messageTimeBound(before, false); ok && !t.Before(ub) {
				return false
			}
		}
		return true
	}
	// Fallback: normalized string compare (sent_at >= after, sent_at < before).
	s := messageSentNormalized(sentAt)
	if after != "" && s < normalizeMessageBound(after, true) {
		return false
	}
	if before != "" && s >= normalizeMessageBound(before, false) {
		return false
	}
	return true
}

// messageTimeBound converts a filter bound to a time instant. Date bounds
// open at the day start (lower) or the next day start (upper).
func messageTimeBound(raw string, lower bool) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if t, err := parseMessageTime(raw); err == nil {
		return t, true
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		t = t.UTC()
		if !lower {
			t = t.Add(24 * time.Hour)
		}
		return t, true
	}
	return time.Time{}, false
}

// normalizeMessageBound mirrors the archive store's bound normalization for
// the string-compare fallback.
func normalizeMessageBound(raw string, lower bool) string {
	if t, ok := messageTimeBound(raw, lower); ok {
		return t.Format(time.RFC3339)
	}
	return raw
}

func parseMessageTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, raw)
}

// filterMessages keeps the items that pass every active filter.
func filterMessages(in []Message, f MessageFilters) []Message {
	if f.Empty() {
		return in
	}
	out := make([]Message, 0, len(in))
	for _, m := range in {
		if f.Matches(m) {
			out = append(out, m)
		}
	}
	return out
}

// sortMessages orders messages for the response window: oldest-first for
// sort=asc, newest-first otherwise, with a stable identity tiebreak.
func sortMessages(in []Message, asc bool) {
	sort.SliceStable(in, func(i, j int) bool {
		si, sj := in[i].SentAt, in[j].SentAt
		ei, ej := si == "", sj == ""
		if ei != ej {
			return ej // empty sent_at sorts last
		}
		if si != sj {
			if asc {
				return si < sj
			}
			return si > sj
		}
		ki, kj := MessageIdentityKey(in[i]), MessageIdentityKey(in[j])
		if asc {
			return ki < kj
		}
		return ki > kj
	})
}

// stripMessageBodies clears body text on list rows; the fetch subroute and
// include_body are the only body carriers (issue #311).
func stripMessageBodies(in []Message) {
	for i := range in {
		in[i].Body = ""
	}
}
