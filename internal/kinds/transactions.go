package kinds

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// transactionsKind owns the financial-transactions aggregate (issue #141):
// three disclosure routes over one dataset (issue #348) — totals, line
// items without account identifiers, and full rows — plus filters and
// category/month summaries.
type transactionsKind struct {
	providerKind
	h    Host
	e    *Engine
	mark func([]Transaction, string)
}

// Grant keys are routes: the library-relative path (issue #348).
const (
	transactionsRouteKey       = "transactions"
	transactionsItemsRouteKey  = "transactions/items"
	transactionsTotalsRouteKey = "transactions/totals"
)

func newTransactionsKind(h Host, e *Engine, d Deps) Kind {
	k := &transactionsKind{h: h, e: e}
	k.mark, _ = Stamps(h, "transactions", forEachTransactionSource)
	def := *BuiltinKindByID("transactions")
	k.providerKind = providerKind{
		def:      def,
		provides: providerProvides[TransactionProvider],
		routes: []Route{
			{Pattern: def.Path, AppReadable: def.AppReadable, Handler: k.serve, GrantKey: transactionsRouteKey},
			{Pattern: "/v1/library/transactions/items", AppReadable: true, Handler: k.serveItems, GrantKey: transactionsItemsRouteKey},
			{Pattern: "/v1/library/transactions/totals", AppReadable: true, Handler: k.serveTotals, GrantKey: transactionsTotalsRouteKey},
		},
	}
	return k
}

// transactionShape selects what a route returns (issue #348).
type transactionShape int

const (
	// transactionFull is line items with account identifiers.
	transactionFull transactionShape = iota
	// transactionItems is line items without account identifiers.
	transactionItems
	// transactionTotals is category/month totals only — no line items.
	transactionTotals
)

// serve returns full line items, including account identifiers:
// GET /v1/library/transactions. Query filters: after, before, account, q,
// currency (issue #141).
func (k *transactionsKind) serve(w http.ResponseWriter, r *http.Request) {
	k.serveShape(w, r, transactionsRouteKey, transactionFull)
}

// serveItems returns line items with account identifiers stripped:
// GET /v1/library/transactions/items.
func (k *transactionsKind) serveItems(w http.ResponseWriter, r *http.Request) {
	k.serveShape(w, r, transactionsItemsRouteKey, transactionItems)
}

// serveTotals returns category/month totals only:
// GET /v1/library/transactions/totals.
func (k *transactionsKind) serveTotals(w http.ResponseWriter, r *http.Request) {
	k.serveShape(w, r, transactionsTotalsRouteKey, transactionTotals)
}

func (k *transactionsKind) serveShape(w http.ResponseWriter, r *http.Request, grantKey string, shape transactionShape) {
	kind := k.def
	filters := ParseTransactionFilters(r)
	if kind.Search != nil {
		// Free-text q is owned by the search index (issue #178). after/
		// before/account/currency stay handler-side filters.
		filters.Q = ""
	}
	Aggregate(k.h, k.e, w, r, Spec[Transaction]{
		Kind:     kind,
		GrantKey: grantKey,
		Targets: func(r *http.Request) ([]NamedLister[Transaction], []string) {
			return transactionsProviderTargets(k.h, r, grantKey)
		},
		ServiceOf: TransactionServiceName,
		SortKey:   TransactionIdentityKey,
		Merge:     MergeTransactions,
		Encode: func(items []Transaction, page PageRequest, bySvc map[string]LibraryServiceStatus) any {
			filtered := filterTransactions(items, filters)
			// Totals cover the full filtered fetch, not the current page —
			// and are computed before any line-item stripping.
			summary := buildTransactionSummary(filtered)
			if shape == transactionTotals {
				return TransactionList{
					Summary:         summary,
					LibraryListMeta: MetaForPage(PageResult{Total: 0}, true, AnyServiceTruncated(bySvc), bySvc),
				}
			}
			if shape == transactionItems {
				filtered = redactTransactionsRead(filtered)
			}
			pageItems, pr := SlicePage(filtered, page)
			return TransactionList{
				Transactions:    pageItems,
				Summary:         summary,
				LibraryListMeta: MetaForPage(pr, true, AnyServiceTruncated(bySvc), bySvc),
			}
		},
		MarkCanonical: k.mark,
		CacheKey:      "library:" + grantKey + filters.CacheKeySuffix(),
	})
}

// transactionsProviderTargets collects providers; the route's encode owns
// the disclosure shape (issue #348).
func transactionsProviderTargets(h Host, r *http.Request, grantKey string) ([]NamedLister[Transaction], []string) {
	return Collect[Transaction, TransactionProvider](h, r, grantKey,
		func(p TransactionProvider) func(context.Context) ([]Transaction, bool, error) {
			return p.ListUserTransactions
		})
}

// redactTransactionsRead strips account identifiers from line items while
// keeping merchant/description for agent reasoning (issue #141).
func redactTransactionsRead(items []Transaction) []Transaction {
	out := make([]Transaction, len(items))
	for i, t := range items {
		out[i] = redactTransactionRead(t)
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
