package kinds

import (
	"context"
	"net/http"

	"github.com/fudomunro/aegis/internal/core/scopes"
)

// NamedLister is one collection source's typed fetch for a library
// aggregate. Local marks a source the kind owns directly (the mail
// archive) rather than through a connector: it gates its own readability
// when the target registers (issue #312).
type NamedLister[T any] struct {
	ID    string
	Local bool
	List  func(ctx context.Context) (items []T, truncated bool, err error)
}

// Collect type-asserts each connector to P and builds fetch targets that
// pass the permission check for kind. Services outside the request's
// data-scope dataset partition are skipped silently (docs/data-scopes.md).
func Collect[T any, P any](
	h Host,
	r *http.Request,
	kind string,
	lister func(P) func(ctx context.Context) ([]T, bool, error),
) (targets []NamedLister[T], denied []string) {
	scope := scopes.From(r)
	grants, _ := h.Grants(r)
	for id, connector := range h.Connectors() {
		svc, ok := h.Service(id)
		if !ok || svc.Action == "block" {
			continue
		}
		if !scopes.ServiceVisible(svc.Dataset, scope) {
			continue
		}
		provider, ok := any(connector).(P)
		if !ok {
			continue
		}
		if !grants.AllowsKindMethod(kind, http.MethodGet) {
			denied = append(denied, id)
			continue
		}
		targets = append(targets, NamedLister[T]{ID: id, List: lister(provider)})
	}
	return targets, denied
}
