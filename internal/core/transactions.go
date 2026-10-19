package core

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

// Match methods for Transaction.
const (
	TransactionMatchProviderID         = "provider_id"
	TransactionMatchAmountDateMerchant = "amount_date_merchant"
)

// transactionServiceName returns the first source service name for stable sort.
func transactionServiceName(t types.Transaction) string {
	if len(t.Sources) > 0 {
		return t.Sources[0].ServiceName
	}
	return ""
}

// normalizeTransactionAmount parses a signed decimal string into a canonical
// form ("+0.00" / "-12.34") so the same money value matches across providers.
// Accepts optional leading +/-. Thousand separators are stripped.
func normalizeTransactionAmount(raw string) string {
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

// transactionDay returns the calendar day (UTC) of a posted/authorized time.
func transactionDay(raw string) string {
	n := types.NormalizeTransactionDate(raw)
	if n == "" {
		return ""
	}
	if len(n) >= 10 {
		return n[:10]
	}
	return n
}

// transactionMerchantOrDescription returns the best merchant-ish string.
func transactionMerchantOrDescription(t types.Transaction) string {
	if m := strings.TrimSpace(t.Merchant); m != "" {
		return m
	}
	return strings.TrimSpace(t.Description)
}

// unionTransactionCategories unions category labels, preserving order.
func unionTransactionCategories(dst, src []string) []string {
	if len(src) == 0 {
		return dst
	}
	seen := make(map[string]bool, len(dst)+len(src))
	out := make([]string, 0, len(dst)+len(src))
	for _, list := range [][]string{dst, src} {
		for _, c := range list {
			c = strings.TrimSpace(c)
			if c == "" {
				continue
			}
			k := strings.ToLower(c)
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, c)
		}
	}
	return out
}

// transactionIdentityKey is the sort/merge key. Prefer durable provider IDs
// when present; otherwise amount + posted day + normalized merchant.
// Transactions without any identity fall back to service:id so they stay
// distinct.
func transactionIdentityKey(t types.Transaction) string {
	if id := strings.ToLower(strings.TrimSpace(t.EntityID)); id != "" {
		return "pid:" + id
	}
	for _, src := range t.Sources {
		if id := strings.ToLower(strings.TrimSpace(messageExtraString(src.Extra, "provider_id"))); id != "" {
			return "pid:" + id
		}
		if id := strings.ToLower(strings.TrimSpace(messageExtraString(src.Extra, "import_id"))); id != "" {
			return "pid:" + id
		}
		if id := strings.ToLower(strings.TrimSpace(messageExtraString(src.Extra, "fitid"))); id != "" {
			return "pid:" + id
		}
	}
	amt := normalizeTransactionAmount(t.Amount)
	merchant := types.NormalizeIdentityKey(transactionMerchantOrDescription(t))
	day := transactionDay(t.PostedAt)
	if day == "" {
		day = transactionDay(t.AuthorizedAt)
	}
	if amt == "" || merchant == "" || day == "" {
		if len(t.Sources) > 0 && t.Sources[0].Service != "" && t.Sources[0].ID != "" {
			return "src:" + t.Sources[0].Service + ":" + t.Sources[0].ID
		}
		return "anon:" + transactionServiceName(t) + ":" + t.Amount + ":" + t.PostedAt
	}
	return "amd:" + amt + "|d:" + day + "|m:" + merchant
}

// transactionMatchKind records how identity was established for multi-source merges.
func transactionMatchKind(key string) string {
	switch {
	case strings.HasPrefix(key, "pid:"):
		return TransactionMatchProviderID
	case strings.HasPrefix(key, "amd:"):
		return TransactionMatchAmountDateMerchant
	default:
		return ""
	}
}

func mergeTransactionInto(dst *types.Transaction, src types.Transaction) {
	if dst.PostedAt == "" {
		dst.PostedAt = src.PostedAt
	}
	if dst.AuthorizedAt == "" {
		dst.AuthorizedAt = src.AuthorizedAt
	}
	if dst.Amount == "" {
		dst.Amount = src.Amount
	}
	if dst.Currency == "" {
		dst.Currency = src.Currency
	}
	if dst.Description == "" {
		dst.Description = src.Description
	}
	if dst.Merchant == "" {
		dst.Merchant = src.Merchant
	}
	if len(dst.Category) == 0 {
		dst.Category = append([]string(nil), src.Category...)
	} else {
		dst.Category = unionTransactionCategories(dst.Category, src.Category)
	}
	if dst.Memo == "" {
		dst.Memo = src.Memo
	}
	if dst.Status == "" {
		dst.Status = src.Status
	}
	if dst.EntityID == "" {
		dst.EntityID = src.EntityID
	}
	if dst.Account == nil && src.Account != nil {
		acc := *src.Account
		dst.Account = &acc
	}
	dst.Sources = append(dst.Sources, src.Sources...)
	if len(dst.Extra) == 0 && len(src.Extra) > 0 {
		dst.Extra = src.Extra
	}
}

// MergeTransactions groups transactions that share a durable provider ID
// (YNAB import_id, Quicken FITID) or the same amount + posted day + normalized
// merchant into one user-facing entry with multiple Sources. A second pass
// joins matches that land on adjacent days (±1) with the same amount and
// merchant — the issue #141 cross-provider duplicate rule. Provider IDs that
// differ across sources still merge when the amount/merchant/day key agrees,
// because the same card charge is often visible through two providers under
// different local IDs.
func MergeTransactions(in []types.Transaction) []types.Transaction {
	if len(in) == 0 {
		return nil
	}

	type slot struct {
		t   types.Transaction
		key string
	}
	var order []string
	byKey := map[string]*slot{}

	// Phase 1: join identical durable IDs and exact amount+day+merchant keys.
	for _, tx := range in {
		key := transactionIdentityKey(tx)
		if key == "" {
			continue
		}
		if cur, ok := byKey[key]; ok {
			mergeTransactionInto(&cur.t, tx)
			continue
		}
		cp := types.CloneTransaction(tx)
		byKey[key] = &slot{t: cp, key: key}
		order = append(order, key)
	}

	// Phase 2: join amount+merchant matches within ±1 day that did not share
	// an exact key (provider midnight vs local date, different local IDs).
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
			if !transactionsMatchAdjacentDay(a.t, b.t) {
				continue
			}
			mergeTransactionInto(&a.t, b.t)
			delete(byKey, bKey)
		}
	}

	// Rebuild order without deleted keys.
	var keys []string
	for _, key := range order {
		if _, ok := byKey[key]; ok {
			keys = append(keys, key)
		}
	}

	out := make([]types.Transaction, 0, len(keys))
	for _, key := range keys {
		item := byKey[key]
		if len(item.t.Sources) > 1 {
			if mk := transactionMatchKind(key); mk != "" {
				item.t.Match = mk
			} else {
				item.t.Match = TransactionMatchAmountDateMerchant
			}
			// Cross-source joins that used amount/merchant/day even when both
			// sides had provider IDs should record the weaker match kind.
			if strings.HasPrefix(key, "pid:") && !durableIDsAgree(item.t) {
				item.t.Match = TransactionMatchAmountDateMerchant
			}
		}
		sort.SliceStable(item.t.Sources, func(i, j int) bool {
			if item.t.Sources[i].Canonical != item.t.Sources[j].Canonical {
				return item.t.Sources[i].Canonical
			}
			if item.t.Sources[i].ServiceName != item.t.Sources[j].ServiceName {
				return item.t.Sources[i].ServiceName < item.t.Sources[j].ServiceName
			}
			return item.t.Sources[i].Service < item.t.Sources[j].Service
		})
		out = append(out, item.t)
	}

	sort.SliceStable(out, func(i, j int) bool {
		si, sj := out[i].PostedAt, out[j].PostedAt
		if si != sj {
			return si > sj // newest first
		}
		si2, sj2 := normalizeTransactionAmount(out[i].Amount), normalizeTransactionAmount(out[j].Amount)
		if si2 != sj2 {
			return si2 < sj2
		}
		return transactionIdentityKey(out[i]) < transactionIdentityKey(out[j])
	})
	return out
}

// durableIDsAgree reports whether every source's provider ID is the same
// (or only one side carries one).
func durableIDsAgree(t types.Transaction) bool {
	seen := map[string]bool{}
	for _, src := range t.Sources {
		id := ""
		for _, k := range []string{"provider_id", "import_id", "fitid"} {
			if v := strings.ToLower(strings.TrimSpace(messageExtraString(src.Extra, k))); v != "" {
				id = v
				break
			}
		}
		if id == "" {
			id = strings.ToLower(strings.TrimSpace(t.EntityID))
		}
		if id == "" {
			continue
		}
		seen[id] = true
	}
	return len(seen) <= 1
}

// transactionsMatchAdjacentDay reports whether two amount+merchant transactions
// fall on the same or consecutive calendar days.
func transactionsMatchAdjacentDay(a, b types.Transaction) bool {
	if normalizeTransactionAmount(a.Amount) == "" ||
		normalizeTransactionAmount(a.Amount) != normalizeTransactionAmount(b.Amount) {
		return false
	}
	ma := types.NormalizeIdentityKey(transactionMerchantOrDescription(a))
	mb := types.NormalizeIdentityKey(transactionMerchantOrDescription(b))
	if ma == "" || ma != mb {
		return false
	}
	da := transactionDay(a.PostedAt)
	if da == "" {
		da = transactionDay(a.AuthorizedAt)
	}
	db := transactionDay(b.PostedAt)
	if db == "" {
		db = transactionDay(b.AuthorizedAt)
	}
	if da == "" || db == "" {
		return false
	}
	if da == db {
		return true
	}
	ta, errA := time.Parse("2006-01-02", da)
	tb, errB := time.Parse("2006-01-02", db)
	if errA != nil || errB != nil {
		return false
	}
	diff := ta.Sub(tb)
	if diff < 0 {
		diff = -diff
	}
	return diff <= 24*time.Hour
}

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

// CacheKey fragment so filtered requests do not share unfiltered cache entries.
func (f TransactionFilters) cacheKeySuffix() string {
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

// matches reports whether a transaction passes every active filter.
func (f TransactionFilters) matches(t types.Transaction) bool {
	if f.Empty() {
		return true
	}
	day := transactionDay(t.PostedAt)
	if day == "" {
		day = transactionDay(t.AuthorizedAt)
	}
	if f.After != "" {
		if day == "" || !dateAtOrAfter(day, f.After) {
			return false
		}
	}
	if f.Before != "" {
		if day == "" || !dateBefore(day, f.Before) {
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

func dateAtOrAfter(day, bound string) bool {
	b := transactionDay(bound)
	if b == "" {
		return true
	}
	return day >= b
}

func dateBefore(day, bound string) bool {
	b := transactionDay(bound)
	if b == "" {
		return true
	}
	return day < b
}

func filterTransactions(in []types.Transaction, f TransactionFilters) []types.Transaction {
	if f.Empty() {
		return in
	}
	out := make([]types.Transaction, 0, len(in))
	for _, t := range in {
		if f.matches(t) {
			out = append(out, t)
		}
	}
	return out
}

// RedactTransactionRead strips account identifiers from a line item while
// keeping merchant/description for agent reasoning (issue #141).
func RedactTransactionRead(t types.Transaction) types.Transaction {
	t.Account = nil
	return t
}

func redactTransactionsForLevel(items []types.Transaction, level string) []types.Transaction {
	switch types.NormalizeAccess(level) {
	case types.AccessSummary:
		// No line items at all.
		return nil
	case types.AccessRead:
		out := make([]types.Transaction, len(items))
		for i, t := range items {
			out[i] = RedactTransactionRead(t)
		}
		return out
	default:
		return items
	}
}

// buildTransactionSummary totals filtered line items by category and month.
// Multi-category line items (YNAB splits) credit their full amount to each
// listed category so the transaction "shows up" where the user categorized it;
// dig into YNAB for exact per-category amounts.
func buildTransactionSummary(items []types.Transaction) *types.TransactionSummary {
	if len(items) == 0 {
		return nil
	}
	sum := &types.TransactionSummary{
		Count:      len(items),
		ByCategory: map[string]string{},
		ByMonth:    map[string]string{},
	}
	// Accumulate in cents (int64) then format once — no float money.
	var totalCents int64
	byCat := map[string]int64{}
	byMonth := map[string]int64{}
	currencies := map[string]bool{}
	for _, t := range items {
		cents, ok := amountToCents(t.Amount)
		if !ok {
			continue
		}
		totalCents += cents
		cats := t.Category
		if len(cats) == 0 {
			cats = []string{"uncategorized"}
		}
		for _, cat := range cats {
			cat = strings.TrimSpace(cat)
			if cat == "" {
				cat = "uncategorized"
			}
			byCat[cat] += cents
		}
		month := ""
		if day := transactionDay(t.PostedAt); day != "" {
			month = day[:7]
		} else if day := transactionDay(t.AuthorizedAt); day != "" {
			month = day[:7]
		}
		if month != "" {
			byMonth[month] += cents
		}
		if c := strings.TrimSpace(t.Currency); c != "" {
			currencies[strings.ToUpper(c)] = true
		}
	}
	sum.TotalAmount = centsToAmount(totalCents)
	for cat, cents := range byCat {
		sum.ByCategory[cat] = centsToAmount(cents)
	}
	for month, cents := range byMonth {
		sum.ByMonth[month] = centsToAmount(cents)
	}
	if len(currencies) == 1 {
		for c := range currencies {
			sum.Currency = c
		}
	}
	if len(sum.ByCategory) == 0 {
		sum.ByCategory = nil
	}
	if len(sum.ByMonth) == 0 {
		sum.ByMonth = nil
	}
	return sum
}

func amountToCents(raw string) (int64, bool) {
	s := normalizeTransactionAmount(raw)
	if s == "" {
		return 0, false
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	parts := strings.SplitN(s, ".", 2)
	units, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, false
	}
	frac := int64(0)
	if len(parts) == 2 {
		f := parts[1]
		if len(f) > 2 {
			f = f[:2]
		}
		for len(f) < 2 {
			f += "0"
		}
		frac, err = strconv.ParseInt(f, 10, 64)
		if err != nil {
			return 0, false
		}
	}
	cents := units*100 + frac
	if neg {
		cents = -cents
	}
	return cents, true
}

func centsToAmount(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}

// transactionsProviderTargets collects providers, applying per-service and
// app-level summary/read redaction so a summary-only surface never contributes
// line items or account identifiers (issue #141).
func transactionsProviderTargets(s *Server, permType string, appLevel string) ([]namedLister[types.Transaction], []string) {
	targets, denied := collectProviders[types.Transaction, types.TransactionProvider](s, permType,
		func(p types.TransactionProvider) func(context.Context) ([]types.Transaction, bool, error) {
			return p.ListUserTransactions
		})
	appSummary := types.IsSummaryAccess(appLevel)
	appRead := appLevel != "" && types.NormalizeAccess(appLevel) == types.AccessRead
	for i := range targets {
		id := targets[i].id
		inner := targets[i].list
		level := ""
		if svc := s.config.GetService(id); svc != nil {
			level = s.effectivePermissions(svc).For(permType)
		}
		effective := level
		if appSummary {
			effective = types.AccessSummary
		} else if appRead {
			effective = types.AccessRead
		}
		if effective == level && !types.IsSummaryAccess(level) && types.NormalizeAccess(level) != types.AccessRead {
			continue
		}
		targets[i].list = func(ctx context.Context) ([]types.Transaction, bool, error) {
			items, truncated, err := inner(ctx)
			return redactTransactionsForLevel(items, effective), truncated, err
		}
	}
	return targets, denied
}

// HandleLibraryTransactions returns the user's financial transactions from
// every connected source: GET /v1/library/transactions
// Query filters: after, before, account, q, currency (issue #141).
// Permission key: transactions. AccessSummary returns totals only.
func (s *Server) HandleLibraryTransactions(w http.ResponseWriter, r *http.Request) {
	kind := *LibraryKindByID("transactions")
	filters := ParseTransactionFilters(r)
	appLevel := ""
	if app := AppIdentityFrom(r); app != nil {
		appLevel = app.Scopes.For(kind.PermType)
	}
	summaryOnly := types.IsSummaryAccess(appLevel)
	// If the agent surface is unrestricted but every service is summary-only
	// after redaction, encode() still has line items only when services
	// returned them. Force summary-only encode when the app is summary.
	handleLibraryAggregate(s, w, r, libraryKindSpec[types.Transaction]{
		kind: kind,
		targets: func(s *Server, r *http.Request) ([]namedLister[types.Transaction], []string) {
			return transactionsProviderTargets(s, kind.PermType, appLevel)
		},
		serviceOf: transactionServiceName,
		sortKey:   transactionIdentityKey,
		merge:     MergeTransactions,
		encode: func(items []types.Transaction, page types.PageRequest, bySvc map[string]types.LibraryServiceStatus) any {
			filtered := filterTransactions(items, filters)
			// Summary covers the full filtered set, not the current page.
			summary := buildTransactionSummary(filtered)
			if summaryOnly {
				return types.TransactionList{
					Summary:        summary,
					LibraryListMeta: types.MetaForPage(types.PageResult{Total: 0}, true, anyServiceTruncated(bySvc), bySvc),
				}
			}
			pageItems, pr := types.SlicePage(filtered, page)
			return types.TransactionList{
				Transactions:    pageItems,
				Summary:         summary,
				LibraryListMeta: types.MetaForPage(pr, true, anyServiceTruncated(bySvc), bySvc),
			}
		},
		markCanonical: s.stampTransactionCanonical,
		cacheKey:      "library:transactions" + filters.cacheKeySuffix() + "|summary:" + strconv.FormatBool(summaryOnly),
	})
}
