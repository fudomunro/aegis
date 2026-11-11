package core

import (
	"fmt"
	"strings"

	"github.com/fudomunro/aegis/internal/kinds"
)

func isCanonicalKindID(id string) bool {
	return kinds.IsCanonicalKindID(id)
}

// IsCanonicalFor reports whether this service is the configured authoritative
// location for the given library kind (issue #16). Blocked services never count.
func (s Service) IsCanonicalFor(kind string) bool {
	if strings.EqualFold(s.Action, "block") {
		return false
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return false
	}
	for _, k := range s.CanonicalFor {
		if strings.EqualFold(strings.TrimSpace(k), kind) {
			return true
		}
	}
	return false
}

// normalizeCanonicalFor trims entries and drops empties / duplicates.
func normalizeCanonicalFor(list []string) []string {
	if len(list) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(list))
	for _, raw := range list {
		k := strings.TrimSpace(raw)
		if k == "" || seen[strings.ToLower(k)] {
			continue
		}
		seen[strings.ToLower(k)] = true
		out = append(out, k)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// validateCanonicalFor checks kind IDs and that at most one non-blocked service
// claims each kind.
func validateCanonicalFor(services []Service) error {
	owner := map[string]string{} // kind -> service id
	for _, svc := range services {
		for _, raw := range svc.CanonicalFor {
			k := strings.TrimSpace(raw)
			if k == "" {
				continue
			}
			if !isCanonicalKindID(k) {
				return fmt.Errorf("service %q: unknown canonical_for kind %q", svc.ID, k)
			}
			if strings.EqualFold(svc.Action, "block") {
				continue
			}
			lower := strings.ToLower(k)
			if prev, ok := owner[lower]; ok && prev != svc.ID {
				return fmt.Errorf("canonical_for %q claimed by both %q and %q", k, prev, svc.ID)
			}
			owner[lower] = svc.ID
		}
	}
	return nil
}
