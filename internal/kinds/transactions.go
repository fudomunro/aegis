package kinds

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/fudomunro/aegis/internal/core/policy"
)

// transactionsKind owns the financial-transactions aggregate (issue #141):
// summary/read redaction per source, list filters, and category/month
// summaries over the full filtered set.
type transactionsKind struct {
	providerKind
	h    Host
	e    *Engine
	mark func([]Transaction, string)
}

func newTransactionsKind(h Host, e *Engine, d Deps) Kind {
	k := &transactionsKind{h: h, e: e}
	k.mark, _ = Stamps(h, "transactions", forEachTransactionSource)
	def := *BuiltinKindByID("transactions")
	k.providerKind = providerKind{
		def:      def,
		provides: providerProvides[TransactionProvider],
		routes: []Route{{
			Pattern:     def.Path,
			AppReadable: def.AppReadable,
			Handler:     k.serve,
		}},
	}
	return k
}

// serve returns the user's financial transactions from every connected
// source: GET /v1/library/transactions. Query filters: after, before,
// account, q, currency (issue #141). Permission key: transactions.
// AccessSummary returns totals only.
func (k *transactionsKind) serve(w http.ResponseWriter, r *http.Request) {
	kind := k.def
	filters := ParseTransactionFilters(r)
	if kind.Search != nil {
		// Free-text q is owned by the search index (issue #178). after/
		// before/account/currency stay handler-side filters.
		filters.Q = ""
	}
	appLevel, _ := k.h.AppLevel(r, kind.PermType)
	summaryOnly := policy.IsSummaryAccess(appLevel)
	Aggregate(k.h, k.e, w, r, Spec[Transaction]{
		Kind: kind,
		Targets: func(r *http.Request) ([]NamedLister[Transaction], []string) {
			return transactionsProviderTargets(k.h, r, kind.PermType, appLevel)
		},
		ServiceOf: TransactionServiceName,
		SortKey:   TransactionIdentityKey,
		Merge:     MergeTransactions,
		Encode: func(items []Transaction, page PageRequest, bySvc map[string]LibraryServiceStatus) any {
			filtered := filterTransactions(items, filters)
			// Summary covers the full filtered set, not the current page.
			summary := buildTransactionSummary(filtered)
			if summaryOnly {
				return TransactionList{
					Summary:         summary,
					LibraryListMeta: MetaForPage(PageResult{Total: 0}, true, AnyServiceTruncated(bySvc), bySvc),
				}
			}
			pageItems, pr := SlicePage(filtered, page)
			return TransactionList{
				Transactions:    pageItems,
				Summary:         summary,
				LibraryListMeta: MetaForPage(pr, true, AnyServiceTruncated(bySvc), bySvc),
			}
		},
		RedactSearch: func(items []Transaction, r *http.Request) []Transaction {
			return searchRedactTransactions(k.h, items, r)
		},
		MarkCanonical: k.mark,
		CacheKey:      "library:transactions" + filters.CacheKeySuffix() + "|summary:" + strconv.FormatBool(summaryOnly),
	})
}

// transactionsProviderTargets collects providers, applying per-service and
// app-level summary/read redaction so a summary-only surface never
// contributes line items or account identifiers (issue #141).
func transactionsProviderTargets(h Host, r *http.Request, permType string, appLevel string) ([]NamedLister[Transaction], []string) {
	targets, denied := Collect[Transaction, TransactionProvider](h, r, permType,
		func(p TransactionProvider) func(context.Context) ([]Transaction, bool, error) {
			return p.ListUserTransactions
		})
	appSummary := policy.IsSummaryAccess(appLevel)
	appRead := appLevel != "" && policy.NormalizeAccess(appLevel) == policy.AccessRead
	for i := range targets {
		id := targets[i].ID
		inner := targets[i].List
		level := ""
		if _, ok := h.Service(id); ok {
			level = h.RequestLevel(r, id, permType)
		}
		effective := level
		if appSummary {
			effective = policy.AccessSummary
		} else if appRead {
			effective = policy.AccessRead
		}
		if effective == level && !policy.IsSummaryAccess(level) && policy.NormalizeAccess(level) != policy.AccessRead {
			continue
		}
		targets[i].List = func(ctx context.Context) ([]Transaction, bool, error) {
			items, truncated, err := inner(ctx)
			return redactTransactionsForLevel(items, effective), truncated, err
		}
	}
	return targets, denied
}

// searchRedactTransactions re-applies per-source summary/read redaction to
// index-hydrated transactions (issue #178). Applying every source's level
// in turn settles on the most restrictive one (never under-redact).
func searchRedactTransactions(h Host, items []Transaction, r *http.Request) []Transaction {
	appLevel, isApp := h.AppLevel(r, "transactions")
	if !isApp {
		appLevel = ""
	}
	out := make([]Transaction, 0, len(items))
	for _, t := range items {
		skip := false
		for _, src := range t.Sources {
			if _, ok := h.Service(src.Service); !ok {
				continue
			}
			level := h.RequestLevel(r, src.Service, "transactions")
			red := redactTransactionsForLevel([]Transaction{t}, level)
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
		case policy.IsSummaryAccess(appLevel):
			t = redactTransactionsForLevel([]Transaction{t}, policy.AccessSummary)[0]
		case appLevel != "" && policy.NormalizeAccess(appLevel) == policy.AccessRead:
			t = redactTransactionsForLevel([]Transaction{t}, policy.AccessRead)[0]
		}
		out = append(out, t)
	}
	return out
}

func filterTransactions(in []Transaction, f TransactionFilters) []Transaction {
	if f.Empty() {
		return in
	}
	out := make([]Transaction, 0, len(in))
	for _, t := range in {
		if f.Matches(t) {
			out = append(out, t)
		}
	}
	return out
}

// redactTransactionRead strips account identifiers from a line item while
// keeping merchant/description for agent reasoning (issue #141).
func redactTransactionRead(t Transaction) Transaction {
	t.Account = nil
	return t
}

func redactTransactionsForLevel(items []Transaction, level string) []Transaction {
	switch policy.NormalizeAccess(level) {
	case policy.AccessSummary:
		// No line items at all.
		return nil
	case policy.AccessRead:
		out := make([]Transaction, len(items))
		for i, t := range items {
			out[i] = redactTransactionRead(t)
		}
		return out
	default:
		return items
	}
}

// buildTransactionSummary totals filtered line items by category and month.
// Multi-category line items (YNAB splits) credit their full amount to each
// listed category so the transaction "shows up" where the user categorized
// it; dig into YNAB for exact per-category amounts.
func buildTransactionSummary(items []Transaction) *TransactionSummary {
	if len(items) == 0 {
		return nil
	}
	sum := &TransactionSummary{
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
		if day := TransactionDay(t.PostedAt); day != "" {
			month = day[:7]
		} else if day := TransactionDay(t.AuthorizedAt); day != "" {
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
	s := NormalizeTransactionAmount(raw)
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

func forEachTransactionSource(item *Transaction, f func(SourceMut)) {
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
