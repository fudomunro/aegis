// Package policy holds Aegis's internal access policy: the access-level
// vocabulary, legacy service GrantMap maps, type-owned DataGrants,
// effective/scope grant resolution, and the permissions-editor view models.
// This is Aegis authorization state and rules — not the `internal/types` layer
// (neutral models, merge, and library kinds stay in internal/types).
package policy

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// Access levels for agent access to a kind.
const (
	// AccessNone denies all operations on the kind.
	AccessNone = "none"
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

// DefaultKey is the reserved permissions map key that sets the level
// for kinds not listed explicitly.
const DefaultKey = "default"

// GrantMap maps kind names to access levels. The reserved key
// "default" applies to types not listed. A nil map means unrestricted access.
type GrantMap map[string]string

// Allows reports whether the given HTTP method is permitted for the kind.
func (p GrantMap) Allows(kind, method string) bool {
	return AllowsMethod(p.Level(kind), method)
}

// AllowsRead reports whether read (GET/HEAD) is permitted for the kind.
func (p GrantMap) AllowsRead(kind string) bool {
	return AllowsMethod(p.Level(kind), http.MethodGet)
}

// AllowsWrite reports whether any mutating method is permitted for the kind.
func (p GrantMap) AllowsWrite(kind string) bool {
	return AllowsMethod(p.Level(kind), http.MethodPost)
}

// AllowedKinds returns sorted kind names whose granted level is not
// "none". The reserved "default" key is omitted.
func (p GrantMap) AllowedKinds() []string {
	if p == nil {
		return nil
	}
	var allowed []string
	for k := range p {
		if k == DefaultKey {
			continue
		}
		if NormalizeAccess(p[k]) != AccessNone {
			allowed = append(allowed, k)
		}
	}
	sort.Strings(allowed)
	return allowed
}

// DeniedKinds returns sorted kind names explicitly set to "none".
// The reserved "default" key is omitted.
func (p GrantMap) DeniedKinds() []string {
	if p == nil {
		return nil
	}
	var denied []string
	for k, v := range p {
		if k == DefaultKey {
			continue
		}
		if NormalizeAccess(v) == AccessNone {
			denied = append(denied, k)
		}
	}
	sort.Strings(denied)
	return denied
}

// Clone returns a shallow copy suitable for admin/API responses.
func (p GrantMap) Clone() map[string]string {
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
		case AccessRead, AccessFull:
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
// The access vocabulary is none / read / write / read-write — narrowed
// disclosure is its own route (issue #348).
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
			if err := ValidateGrantMap(GrantMap{kind: policy.Default}); err != nil {
				return fmt.Errorf("data grant %q: %w", kind, err)
			}
		}
		for provider, level := range policy.Providers {
			if provider == "" {
				return fmt.Errorf("data grant %q: empty provider key", kind)
			}
			if err := ValidateGrantMap(GrantMap{kind: level}); err != nil {
				return fmt.Errorf("data grant %q provider %q: %w", kind, provider, err)
			}
		}
	}
	return nil
}

// ValidateGrantMap checks that every level is a known access type. The
// access vocabulary is none / read / write / read-write: narrowed
// disclosure (availability, totals, city, metadata) is its own route with
// its own grant key (issue #348), not a level. Malformed tokens are
// rejected here and resolve to none at lookup time.
func ValidateGrantMap(p GrantMap) error {
	for k, v := range p {
		if k == "" {
			return fmt.Errorf("permissions: empty kind key")
		}
		switch NormalizeAccess(v) {
		case AccessNone, AccessRead, AccessWrite, AccessFull:
		default:
			return fmt.Errorf("permissions[%s]: unknown access level %q (use none, read, write, or read-write — narrowed views are separate routes)", k, v)
		}
	}
	return nil
}
