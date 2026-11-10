// Package policy holds Aegis's internal access policy: the access-level
// vocabulary, legacy service Permissions maps, type-owned DataGrants,
// effective/scope grant resolution, and the permissions-editor view models.
// This is Aegis authorization state and rules — not the `internal/types` layer
// (neutral models, merge, and library kinds stay in internal/types).
package policy

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/fudomunro/aegis/internal/shared"
)

// Access levels for agent access to a kind.
const (
	// AccessNone denies all operations on the kind.
	AccessNone = "none"
	// AccessFreeBusy allows safe methods on events, but responses are
	// reduced to availability only (start/end/all_day/status) — no titles,
	// attendees, locations, or descriptions (issue #128). Events-only.
	AccessFreeBusy = "freebusy"
	// AccessSummary allows safe methods on transactions, but responses are
	// reduced to totals by category/month only — no line items and no
	// account identifiers (issue #141). Transactions-only.
	AccessSummary = "summary"
	// AccessCity allows safe methods on places, but responses are reduced to
	// locality, country, and the visit/trip time window only — enough to ask
	// "was I out of town?" without names, addresses, or coordinates
	// (issue #142). Places-only.
	AccessCity = "city"
	// AccessPrecise allows safe methods on places with full place names,
	// addresses, and coordinates (issue #142). Places-only. The maximum
	// places read level; there is no agent write surface for places.
	AccessPrecise = "precise"
	// AccessMetadata allows safe methods on documents, but responses are
	// reduced to titles, dates, tags, correspondents, and sources — no
	// excerpt or full text (issue #143). Documents-only.
	AccessMetadata = "metadata"
	// AccessRead allows safe methods (GET, HEAD).
	AccessRead = "read"
	// AccessWrite allows mutating methods (POST, PUT, PATCH, DELETE).
	AccessWrite = "write"
	// AccessFull allows both read and write methods.
	AccessFull = "read-write"
)

// DefaultAccessType is used when a service has no permissions block, or when
// a kind is not listed and no "default" key is set.
const DefaultAccessType = AccessFull

// PermissionDefaultKey is the reserved permissions map key that sets the level
// for kinds not listed explicitly.
const PermissionDefaultKey = "default"

// Permissions maps kind names to access levels. The reserved key
// "default" applies to types not listed. A nil map means unrestricted access.
type Permissions map[string]string

// Allows reports whether the given HTTP method is permitted for the kind.
func (p Permissions) Allows(kind, method string) bool {
	return AllowsMethod(p.Level(kind), method)
}

// AllowsRead reports whether read (GET/HEAD) is permitted for the kind.
func (p Permissions) AllowsRead(kind string) bool {
	return AllowsMethod(p.Level(kind), http.MethodGet)
}

// AllowsWrite reports whether any mutating method is permitted for the kind.
func (p Permissions) AllowsWrite(kind string) bool {
	return AllowsMethod(p.Level(kind), http.MethodPost)
}

// DeniedKinds returns sorted kind names explicitly set to "none".
// The reserved "default" key is omitted.
func (p Permissions) DeniedKinds() []string {
	if p == nil {
		return nil
	}
	var denied []string
	for k, v := range p {
		if k == PermissionDefaultKey {
			continue
		}
		if NormalizeAccess(v) == AccessNone {
			denied = append(denied, k)
		}
	}
	sort.Strings(denied)
	return denied
}

// FreeBusyKinds returns sorted kind names limited to free/busy
// (issue #128). Used in pre-request injection so agents know they only get
// availability, not event content.
func (p Permissions) FreeBusyKinds() []string {
	if p == nil {
		return nil
	}
	var limited []string
	for k, v := range p {
		if k == PermissionDefaultKey {
			continue
		}
		if NormalizeAccess(v) == AccessFreeBusy {
			limited = append(limited, k)
		}
	}
	sort.Strings(limited)
	return limited
}

// SummaryKinds returns sorted kind names limited to totals-only
// summary (issue #141). Used in pre-request injection so agents know they
// only get category/month totals, not line items.
func (p Permissions) SummaryKinds() []string {
	if p == nil {
		return nil
	}
	var limited []string
	for k, v := range p {
		if k == PermissionDefaultKey {
			continue
		}
		if NormalizeAccess(v) == AccessSummary {
			limited = append(limited, k)
		}
	}
	sort.Strings(limited)
	return limited
}

// PlacesAccessLevels returns the effective places access level per service key
// that explicitly enables places (issue #142). Opt-in: keys not listed are
// omitted (they mean none).
func (p Permissions) PlacesAccessLevels() map[string]string {
	if p == nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range p {
		if k == PermissionDefaultKey {
			continue
		}
		if NormalizeAccess(v) == AccessNone {
			continue
		}
		if shared.KindAllowsLevel(k, AccessCity) || shared.KindAllowsLevel(k, AccessPrecise) {
			out[k] = NormalizeAccess(v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Clone returns a shallow copy suitable for admin/API responses.
func (p Permissions) Clone() map[string]string {
	if p == nil {
		return nil
	}
	out := make(map[string]string, len(p))
	for k, v := range p {
		out[k] = NormalizeAccess(v)
	}
	return out
}

// NormalizeAccess canonicalizes a user/config access-level token to one of
// the Access* constants. Unknown tokens are returned lowercased so validation
// can reject them.
func NormalizeAccess(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case AccessNone, "deny", "off", "false":
		return AccessNone
	case AccessFreeBusy, "fb", "busy", "busy-only", "free-busy":
		return AccessFreeBusy
	case AccessSummary, "totals", "totals-only", "sum":
		return AccessSummary
	case AccessCity, "locality", "city-only", "metro":
		return AccessCity
	case AccessPrecise, "coordinates", "coords", "gps", "exact", "full-places":
		return AccessPrecise
	case AccessMetadata, "meta", "metadata-only", "titles":
		return AccessMetadata
	case AccessRead, "ro", "readonly", "read-only":
		return AccessRead
	case AccessWrite, "wo", "writeonly", "write-only", "mutate":
		return AccessWrite
	case AccessFull, "rw", "readwrite", "read_write":
		return AccessFull
	default:
		return strings.ToLower(strings.TrimSpace(level))
	}
}

// AllowsMethod reports whether the access level permits the HTTP method
// (GET/HEAD are reads; POST/PUT/PATCH/DELETE are writes).
func AllowsMethod(level, method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead:
		switch level {
		case AccessFreeBusy, AccessSummary, AccessCity, AccessPrecise, AccessMetadata, AccessRead, AccessFull:
			return true
		}
		return false
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return level == AccessWrite || level == AccessFull
	default:
		// Unknown methods are treated as mutations.
		return level == AccessWrite || level == AccessFull
	}
}

// DataGrant is the stored form of one kind's agent data grant (issue #148).
// At request time it materializes to a single level via DataGrants.Levels;
// authorization never reads this struct directly. Per-provider overrides
// are legacy migration data — Levels ignores them.
//
// Redaction levels (freebusy, summary, city, precise) stay type-scoped:
// ValidatePermissions rejects them on kinds that cannot enforce the
// reduction (see LibraryKind.AllowedLevels).
type DataGrant struct {
	// Default is the access level for the kind. Empty means unrestricted
	// for legacy entries (opt-in kinds stay none until named).
	Default string `json:"default,omitempty"`
	// Providers are legacy per-connector tighten-only overrides. Kept for
	// on-disk compat; ignored by the collapsed model.
	Providers map[string]string `json:"providers,omitempty"`
}

// DataGrants maps kind / permission keys to their stored agent policy.
type DataGrants map[string]DataGrant

// ValidateDataGrants checks every data grant entry (default + provider
// overrides) against the access vocabulary and type-scoped redaction rules.
func ValidateDataGrants(policies DataGrants) error {
	for kind, policy := range policies {
		if kind == "" {
			return fmt.Errorf("data grants: empty kind key")
		}
		// Empty default is allowed (DefaultAccessType / opt-in none).
		if policy.Default != "" {
			if err := ValidatePermissions(Permissions{kind: policy.Default}); err != nil {
				return fmt.Errorf("data grant %q: %w", kind, err)
			}
		}
		for provider, level := range policy.Providers {
			if provider == "" {
				return fmt.Errorf("data grant %q: empty provider key", kind)
			}
			if err := ValidatePermissions(Permissions{kind: level}); err != nil {
				return fmt.Errorf("data grant %q provider %q: %w", kind, provider, err)
			}
		}
	}
	return nil
}

// ValidatePermissions checks that every level is a known access type.
// freebusy is events-only, summary is transactions-only, city/precise are
// places-only, and metadata is documents-only: other types cannot honor
// those redactions.
func ValidatePermissions(p Permissions) error {
	for k, v := range p {
		if k == "" {
			return fmt.Errorf("permissions: empty kind key")
		}
		switch NormalizeAccess(v) {
		case AccessNone, AccessFreeBusy, AccessSummary, AccessCity, AccessPrecise, AccessMetadata, AccessRead, AccessWrite, AccessFull:
		default:
			return fmt.Errorf("permissions[%s]: unknown access level %q (use none, freebusy, summary, city, precise, metadata, read, write, or read-write)", k, v)
		}
		if NormalizeAccess(v) == AccessFreeBusy && k != PermissionDefaultKey && !shared.KindAllowsLevel(k, AccessFreeBusy) {
			return fmt.Errorf("permissions[%s]: freebusy is only supported for %s", k, joinKindsAllowing(AccessFreeBusy))
		}
		if NormalizeAccess(v) == AccessFreeBusy && k == PermissionDefaultKey {
			return fmt.Errorf("permissions[default]: freebusy is only supported for events")
		}
		if NormalizeAccess(v) == AccessSummary && k != PermissionDefaultKey && !shared.KindAllowsLevel(k, AccessSummary) {
			return fmt.Errorf("permissions[%s]: summary is only supported for %s", k, joinKindsAllowing(AccessSummary))
		}
		if NormalizeAccess(v) == AccessSummary && k == PermissionDefaultKey {
			return fmt.Errorf("permissions[default]: summary is only supported for transactions")
		}
		if NormalizeAccess(v) == AccessCity && k != PermissionDefaultKey && !shared.KindAllowsLevel(k, AccessCity) {
			return fmt.Errorf("permissions[%s]: city is only supported for %s", k, joinKindsAllowing(AccessCity))
		}
		if NormalizeAccess(v) == AccessCity && k == PermissionDefaultKey {
			return fmt.Errorf("permissions[default]: city is only supported for places")
		}
		if NormalizeAccess(v) == AccessPrecise && k != PermissionDefaultKey && !shared.KindAllowsLevel(k, AccessPrecise) {
			return fmt.Errorf("permissions[%s]: precise is only supported for %s", k, joinKindsAllowing(AccessPrecise))
		}
		if NormalizeAccess(v) == AccessPrecise && k == PermissionDefaultKey {
			return fmt.Errorf("permissions[default]: precise is only supported for places")
		}
		if NormalizeAccess(v) == AccessMetadata && k != PermissionDefaultKey && !shared.KindAllowsLevel(k, AccessMetadata) {
			return fmt.Errorf("permissions[%s]: metadata is only supported for %s", k, joinKindsAllowing(AccessMetadata))
		}
		if NormalizeAccess(v) == AccessMetadata && k == PermissionDefaultKey {
			return fmt.Errorf("permissions[default]: metadata is only supported for documents")
		}
	}
	return nil
}

// joinKindsAllowing lists built-in kinds that honor an optional level for
// validation error messages.
func joinKindsAllowing(level string) string {
	ids := shared.KindsAllowingLevel(level)
	if len(ids) == 0 {
		return "no built-in type"
	}
	return strings.Join(ids, ", ")
}

// IsFreeBusyAccess reports whether the level is the events free/busy view.
func IsFreeBusyAccess(level string) bool {
	return NormalizeAccess(level) == AccessFreeBusy
}

// IsSummaryAccess reports whether the level is the transactions totals-only
// view (issue #141).
func IsSummaryAccess(level string) bool {
	return NormalizeAccess(level) == AccessSummary
}

// IsMetadataAccess reports whether the level is the documents metadata-only
// view (titles/dates/tags, no text; issue #143).
func IsMetadataAccess(level string) bool {
	return NormalizeAccess(level) == AccessMetadata
}

// DocumentsLevelForResponse picks the redaction to apply for a documents
// response. Metadata strips excerpt/text; read and unrestricted write/full
// keep the short excerpt (full text stays on the gated text endpoint).
func DocumentsLevelForResponse(level string) string {
	switch NormalizeAccess(level) {
	case AccessNone:
		return AccessNone
	case AccessMetadata:
		return AccessMetadata
	case AccessRead, AccessWrite, AccessFull:
		return AccessRead
	default:
		return AccessNone
	}
}

// PlacesLevelForResponse picks the redaction to apply for a places response.
// City strips to locality/country/window; read keeps names without
// coordinates; precise (and unrestricted write/full) keeps coordinates.
// Opt-in types never inherit "default" (issue #142).
func PlacesLevelForResponse(level string) string {
	switch NormalizeAccess(level) {
	case AccessNone:
		return AccessNone
	case AccessCity:
		return AccessCity
	case AccessRead:
		return AccessRead
	case AccessPrecise, AccessWrite, AccessFull:
		return AccessPrecise
	default:
		return AccessNone
	}
}
