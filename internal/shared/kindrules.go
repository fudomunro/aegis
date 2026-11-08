package shared

import (
	"sort"
	"sync"
)

// KindRule is the generic rule set for one named kind key: the optional
// level tokens it honors and whether it stays closed unless a permission
// map / data grant names it explicitly. Domain owners register rules —
// built-in kinds at package init, user-defined packs at load — and policy
// queries here as the single resolver.
type KindRule struct {
	// Levels lists the optional redaction tokens the key honors between
	// none and full (freebusy, summary, city, precise, metadata). Empty
	// means only none/read/write/read-write apply.
	Levels []string
	// DefaultNone keeps the key hidden until it is named explicitly
	// (places pattern, issue #142).
	DefaultNone bool
}

var (
	kindRulesMu sync.RWMutex
	kindRules   = map[string]KindRule{}
)

// RegisterKindRule installs the rule for id, replacing any previous rule.
// Empty ids are ignored; the Levels slice is copied.
func RegisterKindRule(id string, rule KindRule) {
	if id == "" {
		return
	}
	if rule.Levels != nil {
		rule.Levels = append([]string(nil), rule.Levels...)
	}
	kindRulesMu.Lock()
	defer kindRulesMu.Unlock()
	kindRules[id] = rule
}

// MarkKindDefaultNone marks a key opt-in (hidden until named explicitly)
// without disturbing its level tokens. Empty ids are ignored.
func MarkKindDefaultNone(id string) {
	if id == "" {
		return
	}
	kindRulesMu.Lock()
	defer kindRulesMu.Unlock()
	rule := kindRules[id]
	rule.DefaultNone = true
	kindRules[id] = rule
}

// KindAllowsLevel reports whether the key's rule honors level. Unknown
// keys honor nothing.
func KindAllowsLevel(id, level string) bool {
	kindRulesMu.RLock()
	defer kindRulesMu.RUnlock()
	rule, ok := kindRules[id]
	if !ok {
		return false
	}
	for _, l := range rule.Levels {
		if l == level {
			return true
		}
	}
	return false
}

// KindDefaultsNone reports whether the key is opt-in: hidden until a
// permission map / data grant names it explicitly.
func KindDefaultsNone(id string) bool {
	kindRulesMu.RLock()
	defer kindRulesMu.RUnlock()
	rule, ok := kindRules[id]
	return ok && rule.DefaultNone
}

// KindsAllowingLevel lists registered key IDs that honor level, sorted
// (for error messages and docs).
func KindsAllowingLevel(level string) []string {
	kindRulesMu.RLock()
	defer kindRulesMu.RUnlock()
	var out []string
	for id, rule := range kindRules {
		for _, l := range rule.Levels {
			if l == level {
				out = append(out, id)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}
