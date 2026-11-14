// Package kinds owns the library aggregate pipeline over a narrow Host
// seam that the core hub implements (issue #312), and the kind registry,
// neutral models, and merge for library kinds (issue #325). Authorization
// vocabulary and the request rule map come from internal/shared; stored
// grant resolution is internal/core/policy. The package imports
// internal/shared only — never the hub.
package kinds

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/fudomunro/aegis/internal/shared"
)

// Principal says which kind of key a request presented. It decides only
// who the rule map belongs to; allowance and redaction come from the map.
type Principal int

const (
	// PrincipalOwner is the agent key, a tailnet peer, or an internal
	// owner-tier fetch.
	PrincipalOwner Principal = iota
	// PrincipalApp is a third-party app token.
	PrincipalApp
	// PrincipalScope is an invite token or scoped API key.
	PrincipalScope
)

// ServiceView is the neutral service fact the pipeline needs. No
// *Service, *Config, or provider types cross the seam (issue #312).
type ServiceView struct {
	ID           string
	Name         string
	Action       string // "inject" or "block"
	Description  string // block/explain copy shown in deny messages
	Dataset      string
	CanonicalFor []string
	// SyncProjections is the copy projection this source declares per kind
	// (issue #145): "freebusy" narrows event copies placed on it to busy
	// blocks. Empty map / missing entry = full copies.
	SyncProjections map[string]string
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

// SyncProjection returns the copy projection this source declares for kind
// ("" = full copy).
func (v ServiceView) SyncProjection(kind string) string {
	return v.SyncProjections[kind]
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
	// Grants is the single authorization lookup: the rule map (kind →
	// access level) carried by the request's recognized key, plus which
	// kind of key it is. The map is deny-by-default; allowance is
	// AllowsKindMethod and redaction follows Level. App scopes, data-scope
	// grants, and the owner map all resolve here — there is no second
	// policy layer to merge at request time.
	Grants(r *http.Request) (shared.GrantMap, Principal)
	// OwnerGrants is the owner-tier rule map: the view used for internal
	// fetches and for what a reduced principal is being narrowed from.
	OwnerGrants() shared.GrantMap
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
	CopyInfo(kind, serviceID, externalID string) *CopyInfo
}

// ownerViewKey marks an internal owner-tier fetch (issue #272). One owner
// only: core's Grants consults kinds.OwnerView so a reduced caller's
// cold-index bootstrap still stores full rows.
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
