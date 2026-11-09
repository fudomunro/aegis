// Package kinds owns the library aggregate pipeline over a narrow Host
// seam that the core hub implements (issue #312) and the kind registry
// data (issue #325). Neutral models still come from internal/types until
// their move lands; authorization vocabulary comes from core/policy; the
// package never imports the hub.
package kinds

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

// ServiceView is the neutral service fact the pipeline needs. No
// *Service, *Config, or provider types cross the seam (issue #312).
type ServiceView struct {
	ID           string
	Name         string
	Action       string // "inject" or "block"
	Dataset      string
	CanonicalFor []string
}

// IsCanonicalFor reports whether this service is the authoritative
// non-Aegis home for kind (issue #16).
func (v ServiceView) IsCanonicalFor(kind string) bool {
	for _, k := range v.CanonicalFor {
		if k == kind {
			return true
		}
	}
	return false
}

// AccessEvent is one redacted agent access record (issue #312). Core
// adapts it onto its metrics/audit rows; the shape mirrors the hub's
// event so the adapter is field-for-field. Never stores request bodies,
// tokens, raw query strings, or full paths.
type AccessEvent struct {
	At      time.Time
	Agent   string
	Surface string
	Target  string // service id or library kind id
	Kind    string
	Cache   string // hit, miss, or empty
	// Decision is DecisionAllow or DecisionDeny. Empty is treated as allow.
	Decision string
	// Reason is a short redacted denial reason. Never free-form user text.
	Reason string
	// Method is the HTTP method when known (audit only).
	Method string
	// Status is the HTTP response status when known (audit only).
	Status int
	// ClientIP is filled hub-side on the request path.
	ClientIP string
}

// Event surfaces and cache outcomes for AccessEvent.
const (
	SurfaceService = "service"
	SurfaceLibrary = "library"
	CacheHit       = "hit"
	CacheMiss      = "miss"
	DecisionAllow  = "allow"
	DecisionDeny   = "deny"
)

// Host is the narrow runtime seam the pipeline and Collect need. Core
// implements it (core/kinds_host.go). Budget: every method must be needed
// by at least two kinds, or by the engine — widen it only under that rule.
type Host interface {
	// Connectors returns service id → connector instance, untyped so this
	// package never imports connectors; Collect type-asserts to the
	// provider interface P.
	Connectors() map[string]any
	// Service returns the configured view for one service id.
	Service(id string) (ServiceView, bool)
	// Services returns every configured service view.
	Services() []ServiceView
	// RequestLevel is the effective access level for kind on a service
	// for this request: owner policy tightened by scope grants.
	RequestLevel(r *http.Request, serviceID, kind string) string
	// OwnerLevel is the owner-tier access level for kind on a service.
	OwnerLevel(serviceID, kind string) string
	// AppLevel returns the third-party app's scope level for kind and
	// whether the request carries an app identity. App allowance and
	// cache partitioning derive from the level in this package.
	AppLevel(r *http.Request, kind string) (level string, isApp bool)
	// RecordAccess records one redacted access event.
	RecordAccess(r *http.Request, ev AccessEvent)
	// Logger returns the request-scoped logger for a named handler.
	Logger(r *http.Request, handler string) *slog.Logger
	// Agent returns the agent identity label for the request.
	Agent(r *http.Request) string
	// SourceName returns the display name for a collection source:
	// connector registry name, else configured service name, else the id.
	SourceName(id string) string
	// CopyInfo returns durable copy-ledger metadata for one item source
	// (issue #76), or nil when untracked.
	CopyInfo(kind, serviceID, externalID string) *types.CopyInfo
}

// ownerViewKey marks an internal owner-tier fetch (issue #272). One owner
// only: core's AppIdentityFrom and requestLevel consult kinds.OwnerView so
// a reduced caller's cold-index bootstrap still stores full rows.
type ownerViewKey struct{}

// OwnerViewRequest returns the request view used to materialize the search
// index: app identity and scope-grant tightening are ignored, so items come
// back at the partition's fullest detail. Data-scope dataset visibility is
// preserved (docs/data-scopes.md). Read-path redaction (redactSearch) stays
// the caller boundary — this view only decides what gets stored.
func OwnerViewRequest(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ownerViewKey{}, true))
}

// OwnerView reports whether r is an internal owner-tier fetch.
func OwnerView(r *http.Request) bool {
	if r == nil {
		return false
	}
	v, _ := r.Context().Value(ownerViewKey{}).(bool)
	return v
}
