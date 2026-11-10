package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

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
	case AccessNone, AccessFreeBusy, AccessSummary, AccessCity, AccessPrecise,
		AccessMetadata, AccessRead, AccessWrite, AccessFull:
		return true
	}
	return false
}

// AllowsKindMethod reports whether the data grant for kind permits method.
func (p GrantMap) AllowsKindMethod(kind, method string) bool {
	return AllowsMethod(p.Level(kind), method)
}

// PrefillDefault returns the rule map with a permissive "default" entry
// when it has none. The owner key comes prefilled this way so a fresh
// install is unrestricted out of the box; resolution stays uniformly
// deny-by-default. Opt-in kinds remain closed regardless.
func (p GrantMap) PrefillDefault() GrantMap {
	if _, ok := p[DefaultKey]; ok {
		return p
	}
	out := make(GrantMap, len(p)+1)
	for k, v := range p {
		out[k] = v
	}
	out[DefaultKey] = DefaultAccessType
	return out
}

// Fingerprint is a stable short digest of the rule map (json.Marshal sorts
// map keys), for partitioning caches per request key. Distinct maps must
// not share cached responses. Empty maps share the reserved "none" mark.
func (p GrantMap) Fingerprint() string {
	if len(p) == 0 {
		return "none"
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return "invalid"
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
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
