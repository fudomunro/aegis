package types

import (
	"sort"
	"strings"
	"time"
)

// Match methods for Transaction.
const (
	TransactionMatchProviderID         = "provider_id"
	TransactionMatchAmountDateMerchant = "amount_date_merchant"
)

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

func mergeTransactionInto(dst *Transaction, src Transaction) {
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
func MergeTransactions(in []Transaction) []Transaction {
	if len(in) == 0 {
		return nil
	}

	type slot struct {
		t   Transaction
		key string
	}
	var order []string
	byKey := map[string]*slot{}

	// Phase 1: join identical durable IDs and exact amount+day+merchant keys.
	for _, tx := range in {
		key := TransactionIdentityKey(tx)
		if key == "" {
			continue
		}
		if cur, ok := byKey[key]; ok {
			mergeTransactionInto(&cur.t, tx)
			continue
		}
		cp := CloneTransaction(tx)
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

	out := make([]Transaction, 0, len(keys))
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
		si2, sj2 := NormalizeTransactionAmount(out[i].Amount), NormalizeTransactionAmount(out[j].Amount)
		if si2 != sj2 {
			return si2 < sj2
		}
		return TransactionIdentityKey(out[i]) < TransactionIdentityKey(out[j])
	})
	return out
}

// durableIDsAgree reports whether every source's provider ID is the same
// (or only one side carries one).
func durableIDsAgree(t Transaction) bool {
	seen := map[string]bool{}
	for _, src := range t.Sources {
		id := ""
		for _, k := range []string{"provider_id", "import_id", "fitid"} {
			if v := strings.ToLower(strings.TrimSpace(ExtraString(src.Extra, k))); v != "" {
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
func transactionsMatchAdjacentDay(a, b Transaction) bool {
	if NormalizeTransactionAmount(a.Amount) == "" ||
		NormalizeTransactionAmount(a.Amount) != NormalizeTransactionAmount(b.Amount) {
		return false
	}
	ma := NormalizeIdentityKey(TransactionMerchantOrDescription(a))
	mb := NormalizeIdentityKey(TransactionMerchantOrDescription(b))
	if ma == "" || ma != mb {
		return false
	}
	da := TransactionDay(a.PostedAt)
	if da == "" {
		da = TransactionDay(a.AuthorizedAt)
	}
	db := TransactionDay(b.PostedAt)
	if db == "" {
		db = TransactionDay(b.AuthorizedAt)
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
