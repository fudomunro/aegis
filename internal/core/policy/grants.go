package policy

import (
	"github.com/fudomunro/aegis/internal/shared"
)

// The collapsed authorization model: a recognized key carries one rule map
// (a shared.GrantMap from kind to access level) and every request is decided
// by a single lookup — Allow iff AllowsMethod(Level(kind), method).
// Resolution is uniformly deny-by-default; there is no second policy layer
// to AND or tighten at request time.

// Levels materializes a kind-keyed DataGrants map as the rule map a key
// carries at request time. Each entry's default level is taken; per-provider
// overrides are not part of the collapsed model and are ignored. A listed
// kind must state its own level: empty or malformed levels resolve to
// none, like everything else under the deny-by-default rule. An empty
// result is nil, which Level treats as deny-all.
func (tp DataGrants) Levels() shared.GrantMap {
	if len(tp) == 0 {
		return nil
	}
	out := make(shared.GrantMap, len(tp))
	for kind, grant := range tp {
		level := shared.NormalizeAccess(grant.Default)
		if !shared.IsAccessLevel(level) {
			level = shared.AccessNone
		}
		out[kind] = level
	}
	return out
}
