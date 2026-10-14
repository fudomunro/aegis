package types

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// Access levels for agent access to a data type.
const (
	// AccessNone denies all operations on the data type.
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
	// AccessRead allows safe methods (GET, HEAD).
	AccessRead = "read"
	// AccessWrite allows mutating methods (POST, PUT, PATCH, DELETE).
	AccessWrite = "write"
	// AccessFull allows both read and write methods.
	AccessFull = "read-write"
)

// freeBusyOnlyTypes lists data types that honor AccessFreeBusy redaction.
var freeBusyOnlyTypes = map[string]bool{"events": true}

// summaryOnlyTypes lists data types that honor AccessSummary redaction.
var summaryOnlyTypes = map[string]bool{"transactions": true}

// cityOnlyTypes lists data types that honor AccessCity redaction.
var cityOnlyTypes = map[string]bool{"places": true}

// preciseOnlyTypes lists data types that honor AccessPrecise.
var preciseOnlyTypes = map[string]bool{"places": true}

// optInDefaultNone lists data types that stay hidden unless the permissions
// map names them explicitly.
var optInDefaultNone = map[string]bool{"places": true}

// DefaultAccessType is used when a service has no permissions block, or when
// a data type is not listed and no "default" key is set.
const DefaultAccessType = AccessFull

// PermissionDefaultKey is the reserved permissions map key that sets the level
// for data types not listed explicitly.
const PermissionDefaultKey = "default"

// Permissions maps data-type names to access levels. The reserved key
// "default" applies to types not listed. A nil map means unrestricted access.
type Permissions map[string]string

// For returns the access level for a data type.
// Unknown/empty data types fall through to "default" (or DefaultAccessType),
// except opt-in types (places) which stay "none" unless named explicitly —
// the "default" key never enables them (issue #142).
// Proxy callers with an unclassified path must use Strictest instead so
// unknown surface cannot outrank a tighter type (issue #94).
func (p Permissions) For(dataType string) string {
	if optInDefaultNone[dataType] {
		if p != nil {
			if level, ok := p[dataType]; ok {
				return NormalizeAccess(level)
			}
		}
		return AccessNone
	}
	if p == nil {
		return DefaultAccessType
	}
	level, ok := p[dataType]
	if ok {
		return NormalizeAccess(level)
	}
	if level, ok := p[PermissionDefaultKey]; ok {
		return NormalizeAccess(level)
	}
	return DefaultAccessType
}

// Strictest returns the most restrictive access level configured on the map.
// Used when a proxy path does not map to a known data type: unknown endpoints
// must not inherit "default" if any type is tighter (issue #94). A nil or empty
// map is unrestricted. If reads and writes both appear, no single level is
// tighter than every known type for an unknown method class — deny.
func (p Permissions) Strictest() string {
	if len(p) == 0 {
		return DefaultAccessType
	}
	hasNone, hasFreeBusy, hasSummary, hasCity, hasPrecise, hasRead, hasWrite, hasFull := false, false, false, false, false, false, false, false
	for _, v := range p {
		switch NormalizeAccess(v) {
		case AccessNone:
			hasNone = true
		case AccessFreeBusy:
			hasFreeBusy = true
		case AccessSummary:
			hasSummary = true
		case AccessCity:
			hasCity = true
		case AccessPrecise:
			hasPrecise = true
		case AccessRead:
			hasRead = true
		case AccessWrite:
			hasWrite = true
		case AccessFull:
			hasFull = true
		}
	}
	if hasNone || (hasRead && hasWrite) {
		return AccessNone
	}
	// freebusy/summary/city/precise redactions are type-specific. When one is
	// the tightest level, unknown paths cannot be redacted — deny them rather
	// than leak detail (issue #94, #128, #141, #142).
	if hasFreeBusy && !hasRead && !hasWrite && !hasFull {
		return AccessNone
	}
	if hasSummary && !hasRead && !hasWrite && !hasFull {
		return AccessNone
	}
	if hasCity && !hasRead && !hasWrite && !hasFull {
		return AccessNone
	}
	if hasPrecise && !hasRead && !hasWrite && !hasFull {
		return AccessNone
	}
	if hasRead {
		return AccessRead
	}
	if hasWrite {
		return AccessWrite
	}
	if hasFull {
		return AccessFull
	}
	if hasFreeBusy {
		return AccessFreeBusy
	}
	if hasSummary {
		return AccessSummary
	}
	if hasCity {
		return AccessCity
	}
	if hasPrecise {
		return AccessPrecise
	}
	return DefaultAccessType
}

// AccessForDataType resolves the effective level for a classified path.
// Unclassified paths use Strictest, never the default key (issue #94).
func (p Permissions) AccessForDataType(dataType string) string {
	if dataType == "" {
		return p.Strictest()
	}
	return p.For(dataType)
}

// Allows reports whether the given HTTP method is permitted for the data type.
func (p Permissions) Allows(dataType, method string) bool {
	return AllowsMethod(p.For(dataType), method)
}

// AllowsRead reports whether read (GET/HEAD) is permitted for the data type.
func (p Permissions) AllowsRead(dataType string) bool {
	return AllowsMethod(p.For(dataType), http.MethodGet)
}

// AllowsWrite reports whether any mutating method is permitted for the data type.
func (p Permissions) AllowsWrite(dataType string) bool {
	return AllowsMethod(p.For(dataType), http.MethodPost)
}

// DeniedDataTypes returns sorted data-type names explicitly set to "none".
// The reserved "default" key is omitted.
func (p Permissions) DeniedDataTypes() []string {
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

// FreeBusyDataTypes returns sorted data-type names limited to free/busy
// (issue #128). Used in pre-request injection so agents know they only get
// availability, not event content.
func (p Permissions) FreeBusyDataTypes() []string {
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

// SummaryDataTypes returns sorted data-type names limited to totals-only
// summary (issue #141). Used in pre-request injection so agents know they
// only get category/month totals, not line items.
func (p Permissions) SummaryDataTypes() []string {
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
		if cityOnlyTypes[k] || preciseOnlyTypes[k] {
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
		case AccessFreeBusy, AccessSummary, AccessCity, AccessPrecise, AccessRead, AccessFull:
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

// TypePolicy is the agent-access policy owned by a data type (issue #148).
// Service-level Permissions remain a minority override during migration;
// the type default is the primary model. Enforcement still applies on
// proxy, library, capabilities output, and pre-request injection.
//
// Redaction levels (freebusy, summary, city, precise) stay type-scoped:
// ValidatePermissions rejects them on kinds that cannot enforce the
// reduction (see freeBusyOnlyTypes and friends).
type TypePolicy struct {
	// Default is the access level for agents when no provider override applies.
	// Empty means DefaultAccessType (preserves pre-permission behavior).
	Default string `json:"default,omitempty"`
	// Providers optionally further restrict (never grant) access for a single
	// connector id. Missing entry means the type default applies.
	Providers map[string]string `json:"providers,omitempty"`
}

// For returns the effective access level for a provider (or the type default
// when provider is empty / has no override). Provider overrides can only
// tighten the type default.
func (tp TypePolicy) For(provider string) string {
	level := NormalizeAccess(tp.Default)
	if level == "" {
		level = DefaultAccessType
	}
	if provider == "" || len(tp.Providers) == 0 {
		return level
	}
	ov, ok := tp.Providers[provider]
	if !ok {
		return level
	}
	ovLevel := NormalizeAccess(ov)
	if ovLevel == "" {
		return level
	}
	return stricterAccess(level, ovLevel)
}

// TypePolicies maps data-type / permission keys to their agent policy.
type TypePolicies map[string]TypePolicy

// For returns the policy for a data type key (zero value when unset).
func (tp TypePolicies) For(dataType string) TypePolicy {
	if tp == nil {
		return TypePolicy{}
	}
	return tp[dataType]
}

// stricterAccess returns the more restrictive of two access levels.
// Used so provider overrides can only tighten a type default (issue #148).
func stricterAccess(a, b string) string {
	if accessRank(a) <= accessRank(b) {
		return a
	}
	return b
}

// accessRank orders levels from most to least restrictive for clamping.
// Redaction levels sit between none and read because they allow a narrowed
// read but never a write.
func accessRank(level string) int {
	switch NormalizeAccess(level) {
	case AccessNone:
		return 0
	case AccessFreeBusy, AccessSummary, AccessCity, AccessPrecise:
		return 1
	case AccessRead:
		return 2
	case AccessWrite:
		return 3
	case AccessFull:
		return 4
	default:
		return 0
	}
}

// ValidatePermissions checks that every level is a known access type.
// freebusy is events-only, summary is transactions-only, and city/precise are
// places-only: other types cannot honor those redactions.
func ValidatePermissions(p Permissions) error {
	for k, v := range p {
		if k == "" {
			return fmt.Errorf("permissions: empty data type key")
		}
		switch NormalizeAccess(v) {
		case AccessNone, AccessFreeBusy, AccessSummary, AccessCity, AccessPrecise, AccessRead, AccessWrite, AccessFull:
		default:
			return fmt.Errorf("permissions[%s]: unknown access level %q (use none, freebusy, summary, city, precise, read, write, or read-write)", k, v)
		}
		if NormalizeAccess(v) == AccessFreeBusy && k != PermissionDefaultKey && !freeBusyOnlyTypes[k] {
			return fmt.Errorf("permissions[%s]: freebusy is only supported for events", k)
		}
		if NormalizeAccess(v) == AccessFreeBusy && k == PermissionDefaultKey {
			return fmt.Errorf("permissions[default]: freebusy is only supported for events")
		}
		if NormalizeAccess(v) == AccessSummary && k != PermissionDefaultKey && !summaryOnlyTypes[k] {
			return fmt.Errorf("permissions[%s]: summary is only supported for transactions", k)
		}
		if NormalizeAccess(v) == AccessSummary && k == PermissionDefaultKey {
			return fmt.Errorf("permissions[default]: summary is only supported for transactions")
		}
		if NormalizeAccess(v) == AccessCity && k != PermissionDefaultKey && !cityOnlyTypes[k] {
			return fmt.Errorf("permissions[%s]: city is only supported for places", k)
		}
		if NormalizeAccess(v) == AccessCity && k == PermissionDefaultKey {
			return fmt.Errorf("permissions[default]: city is only supported for places")
		}
		if NormalizeAccess(v) == AccessPrecise && k != PermissionDefaultKey && !preciseOnlyTypes[k] {
			return fmt.Errorf("permissions[%s]: precise is only supported for places", k)
		}
		if NormalizeAccess(v) == AccessPrecise && k == PermissionDefaultKey {
			return fmt.Errorf("permissions[default]: precise is only supported for places")
		}
	}
	return nil
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

// IsCityAccess reports whether the level is the places city-only view
// (issue #142).
func IsCityAccess(level string) bool {
	return NormalizeAccess(level) == AccessCity
}

// IsPreciseAccess reports whether the level is the places precise view
// (coordinates included, issue #142).
func IsPreciseAccess(level string) bool {
	return NormalizeAccess(level) == AccessPrecise
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
