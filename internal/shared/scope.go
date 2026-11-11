package shared

import (
	"context"
	"net/http"
)

// DataScope is the request-side view of a data scope (docs/data-scopes.md):
// which dataset partition the authenticated principal is bound to. The hub
// resolves the stored data-scope record (credentials, stored grants live in
// the hub's scopes store) and attaches this view; the kinds pipeline reads it
// to partition fetches and index writes. A nil DataScope is the owner
// principal — no partition filter.

type DataScope struct {
	// Dataset partitions local stores and service visibility. Empty is the
	// default partition; it does not see dataset-bound services.
	Dataset string
}

type dataScopeKey struct{}

// WithDataScope attaches the request's data-scope view. A nil view marks
// the owner principal.
func WithDataScope(r *http.Request, ds *DataScope) *http.Request {
	if ds == nil {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), dataScopeKey{}, ds))
}

// DataScopeFrom returns the request's data-scope view, or nil for the owner
// principal (legacy API key or tailnet identity).
func DataScopeFrom(r *http.Request) *DataScope {
	ds, _ := r.Context().Value(dataScopeKey{}).(*DataScope)
	return ds
}

// Dataset returns the partition id for the request ("" = no filter).
func Dataset(r *http.Request) string {
	ds := DataScopeFrom(r)
	if ds == nil {
		return ""
	}
	return ds.Dataset
}

// ServiceVisible reports whether a service's connector may feed this
// principal. Services without a dataset are shared (every scope). A dataset
// bound service is visible only to scopes in that partition. The default
// scope (empty dataset) sees every service.
func ServiceVisible(dataset string, ds *DataScope) bool {
	if dataset == "" {
		return true
	}
	if ds == nil {
		// Default principal: no partition filter, sees shared and all
		// dataset-bound services (owner view).
		return true
	}
	return ds.Dataset == dataset
}
