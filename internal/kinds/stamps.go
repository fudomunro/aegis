package kinds

import "github.com/fudomunro/aegis/internal/types"

// SourceMut is one mutable item source row as generic stamping sees it.
// Kinds supply a ForEachSource projection over their typed sources
// (issue #312); the per-kind mark*/stamp* pairs are gone.
type SourceMut struct {
	Service       string
	ID            string
	MarkCanonical func()
	SetCopy       func(*types.CopyInfo)
}

// ForEachSource projects one item's source rows for generic stamping.
type ForEachSource[T any] func(item *T, f func(SourceMut))

// Stamps returns the Spec stamping closures for a kind: config-declared
// canonical marking (issue #16) and copy-ledger freshness (issue #76).
// Missing ledger rows leave sources unchanged — fail-open.
func Stamps[T any](h Host, kind string, forEach ForEachSource[T]) (mark func([]T, string), stampCopies func([]T)) {
	mark = func(items []T, serviceID string) {
		svc, ok := h.Service(serviceID)
		if !ok || !svc.IsCanonicalFor(kind) {
			return
		}
		for i := range items {
			forEach(&items[i], func(src SourceMut) {
				if src.Service == serviceID {
					src.MarkCanonical()
				}
			})
		}
	}
	stampCopies = func(items []T) {
		for i := range items {
			forEach(&items[i], func(src SourceMut) {
				src.SetCopy(h.CopyInfo(kind, src.Service, src.ID))
			})
		}
	}
	return mark, stampCopies
}
