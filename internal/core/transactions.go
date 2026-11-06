package core

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/fudomunro/aegis/internal/types"
)

func filterTransactions(in []types.Transaction, f types.TransactionFilters) []types.Transaction {
	if f.Empty() {
		return in
	}
	out := make([]types.Transaction, 0, len(in))
	for _, t := range in {
		if f.Matches(t) {
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
		if day := types.TransactionDay(t.PostedAt); day != "" {
			month = day[:7]
		} else if day := types.TransactionDay(t.AuthorizedAt); day != "" {
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
	s := types.NormalizeTransactionAmount(raw)
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
func transactionsProviderTargets(s *Server, r *http.Request, permType string, appLevel string) ([]namedLister[types.Transaction], []string) {
	targets, denied := collectProviders[types.Transaction, types.TransactionProvider](s, r, permType,
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
			level = s.requestLevel(r, svc, permType)
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
	filters := types.ParseTransactionFilters(r)
	if kind.Search != nil {
		// Free-text q is owned by the search index (issue #178). after/
		// before/account/currency stay handler-side filters.
		filters.Q = ""
	}
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
			return transactionsProviderTargets(s, r, kind.PermType, appLevel)
		},
		serviceOf: types.TransactionServiceName,
		sortKey:   types.TransactionIdentityKey,
		merge:     types.MergeTransactions,
		encode: func(items []types.Transaction, page types.PageRequest, bySvc map[string]types.LibraryServiceStatus) any {
			filtered := filterTransactions(items, filters)
			// Summary covers the full filtered set, not the current page.
			summary := buildTransactionSummary(filtered)
			if summaryOnly {
				return types.TransactionList{
					Summary:         summary,
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
		redactSearch: func(items []types.Transaction, r *http.Request) []types.Transaction {
			return s.searchRedactTransactions(items, r)
		},
		markCanonical: s.stampTransactionCanonical,
		cacheKey:      "library:transactions" + filters.CacheKeySuffix() + "|summary:" + strconv.FormatBool(summaryOnly),
	})
}

// searchRedactTransactions re-applies per-source summary/read redaction to
// index-hydrated transactions (issue #178). Applying every source's level in
// turn settles on the most restrictive one (never under-redact).
func (s *Server) searchRedactTransactions(items []types.Transaction, r *http.Request) []types.Transaction {
	appLevel := ""
	if app := AppIdentityFrom(r); app != nil {
		appLevel = app.Scopes.For("transactions")
	}
	out := make([]types.Transaction, 0, len(items))
	for _, t := range items {
		skip := false
		for _, src := range t.Sources {
			svc := s.config.GetService(src.Service)
			if svc == nil {
				continue
			}
			level := s.requestLevel(r, svc, "transactions")
			red := redactTransactionsForLevel([]types.Transaction{t}, level)
			if len(red) == 0 {
				skip = true
				break
			}
			t = red[0]
		}
		if skip {
			continue
		}
		switch {
		case types.IsSummaryAccess(appLevel):
			t = redactTransactionsForLevel([]types.Transaction{t}, types.AccessSummary)[0]
		case appLevel != "" && types.NormalizeAccess(appLevel) == types.AccessRead:
			t = redactTransactionsForLevel([]types.Transaction{t}, types.AccessRead)[0]
		}
		out = append(out, t)
	}
	return out
}
