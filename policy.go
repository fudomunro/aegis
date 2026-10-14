package main

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
	level := normalizeAccess(tp.Default)
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
	ovLevel := normalizeAccess(ov)
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
	switch normalizeAccess(level) {
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
