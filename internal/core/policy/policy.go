// Package policy holds Aegis's internal access policy: the stored data-grant
// forms, effective/scope grant resolution, and the permissions-editor view
// models. This is Aegis authorization state and rules — not the neutral
// models and kind metadata in internal/kinds. The access-level vocabulary and
// the request rule map live in internal/shared.
package policy

import (
	"fmt"

	"github.com/fudomunro/aegis/internal/shared"
)

// DataGrant is the stored form of one kind's agent data grant (issue #148).
// At request time it materializes to a single level via DataGrants.Levels;
// authorization never reads this struct directly. The former per-provider
// overrides are gone with the route-grants collapse — `providers` keys in
// old files are ignored on load.
//
// The access vocabulary is none / read / write / read-write — narrowed
// disclosure is its own route (issue #348).
type DataGrant struct {
	// Default is the access level for the kind. Empty or malformed
	// resolves to none at lookup time.
	Default string `json:"default,omitempty"`
}

// DataGrants maps kind / permission keys to their stored agent policy.
type DataGrants map[string]DataGrant

// ValidateDataGrants checks every data grant entry against the access
// vocabulary.
func ValidateDataGrants(policies DataGrants) error {
	for kind, policy := range policies {
		if kind == "" {
			return fmt.Errorf("data grants: empty kind key")
		}
		// Empty default is allowed (it resolves to none at lookup time).
		if policy.Default != "" {
			if err := ValidateGrantMap(shared.GrantMap{kind: policy.Default}); err != nil {
				return fmt.Errorf("data grant %q: %w", kind, err)
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
func ValidateGrantMap(p shared.GrantMap) error {
	for k, v := range p {
		if k == "" {
			return fmt.Errorf("permissions: empty kind key")
		}
		switch shared.NormalizeAccess(v) {
		case shared.AccessNone, shared.AccessRead, shared.AccessWrite, shared.AccessFull:
		default:
			return fmt.Errorf("permissions[%s]: unknown access level %q (use none, read, write, or read-write — narrowed views are separate routes)", k, v)
		}
	}
	return nil
}
