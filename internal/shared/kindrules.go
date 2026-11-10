package shared

import (
	"sync"
)

// KindRule is the generic rule set for one named kind key: whether it
// stays closed unless a data grant names it explicitly. Domain owners
// register rules — built-in kinds at package init, user-defined packs at
// load — and policy queries here as the single resolver. Disclosure is
// route-based (issue #348): a key needs no level tokens.
type KindRule struct {
	// DefaultNone keeps the key hidden until it is named explicitly
	// (places pattern, issue #142).
	DefaultNone bool
}

var (
	kindRulesMu sync.RWMutex
	kindRules   = map[string]KindRule{}
)

// RegisterKindRule installs the rule for id, replacing any previous rule.
// Empty ids are ignored.
func RegisterKindRule(id string, rule KindRule) {
	if id == "" {
		return
	}
	kindRulesMu.Lock()
	defer kindRulesMu.Unlock()
	kindRules[id] = rule
}

// MarkKindDefaultNone marks a key opt-in (hidden until named explicitly).
// Empty ids are ignored.
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

// KindDefaultsNone reports whether the key is opt-in: hidden until a
// permission map / data grant names it explicitly.
func KindDefaultsNone(id string) bool {
	kindRulesMu.RLock()
	defer kindRulesMu.RUnlock()
	rule, ok := kindRules[id]
	return ok && rule.DefaultNone
}

// KindsAllowingLevel lists registered key IDs that honor level, sorted
