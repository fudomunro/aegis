package policy

import (
	"github.com/fudomunro/aegis/internal/shared"
)

// The collapsed authorization model: a recognized key carries one rule map
// (a GrantMap from kind to access level) and every request is decided by
// a single lookup — Allow iff AllowsMethod(Level(kind), method).
// Resolution is uniformly deny-by-default; there is no second policy layer
// to AND or tighten at request time.

// Level resolves the granted access level for kind:
//
//  1. an explicit kind entry wins (tokens are normalized),
//  2. else the reserved "default" key — which never opens opt-in kinds
//     (those registered with KindDefaultsNone, e.g. places),
//  3. else AccessNone.
//
// A malformed level token resolves to AccessNone: a bad grant denies rather
// than allows (save-time validation rejects the token anyway). An empty kind
// name resolves like any unlisted kind.
func (p GrantMap) Level(kind string) string {
	if shared.KindDefaultsNone(kind) {
		if p != nil {
			if level, ok := p[kind]; ok {
				if level = NormalizeAccess(level); knownAccess(level) {
					return level
				}
			}
		}
		return AccessNone
	}
	if p == nil {
		return AccessNone
	}
	if level, ok := p[kind]; ok {
		if level = NormalizeAccess(level); knownAccess(level) {
			return level
		}
		return AccessNone
	}
	if level, ok := p[DefaultKey]; ok {
		if level = NormalizeAccess(level); knownAccess(level) {
			return level
		}
	}
	return AccessNone
}

// knownAccess reports whether level is one of the access-vocabulary values.
func knownAccess(level string) bool {
	switch level {
	case AccessNone, AccessRead, AccessWrite, AccessFull:
		return true
	}
	return false
}

// AllowsKindMethod reports whether the data grant for kind permits method.
func (p GrantMap) AllowsKindMethod(kind, method string) bool {
	return AllowsMethod(p.Level(kind), method)
}

// PrefillDefault returns the rule map with a permissive "default" entry
// when the map is entirely unconfigured. The owner key comes prefilled this
// way so a fresh install is unrestricted out of the box; a map with any
// entry is the operator's explicit allow list and is left alone. Resolution
// stays uniformly deny-by-default. Opt-in kinds remain closed regardless.
func (p GrantMap) PrefillDefault() GrantMap {
	if len(p) != 0 {
		return p
	}
	out := make(GrantMap, len(p)+1)
	for k, v := range p {
		out[k] = v
	}
	out[DefaultKey] = DefaultAccessType
	return out
}

// Levels materializes a kind-keyed DataGrants map as the rule map a key
// carries at request time. Each entry's default level is taken; per-provider
// overrides are not part of the collapsed model and are ignored. A listed
// kind must state its own level: empty or malformed levels resolve to
// none, like everything else under the deny-by-default rule. An empty
// result is nil, which Level treats as deny-all.
func (tp DataGrants) Levels() GrantMap {
	if len(tp) == 0 {
		return nil
	}
	out := make(GrantMap, len(tp))
	for kind, grant := range tp {
		level := NormalizeAccess(grant.Default)
		if !knownAccess(level) {
			level = AccessNone
		}
		out[kind] = level
	}
	return out
}
