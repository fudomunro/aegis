package shared

import (
	"net/http"
	"sort"
	"strings"
)

// Grant vocabulary: the access levels a data grant carries, the rule map a
// recognized key brings to a request (kind → access level), and the trivial
// pure lookups over it. Resolution of stored policy into one of these maps
// is internal/core/policy; nothing here reads state or configuration.

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
	if KindDefaultsNone(kind) {
		if p != nil {
			if level, ok := p[kind]; ok {
				if level = NormalizeAccess(level); IsAccessLevel(level) {
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
		if level = NormalizeAccess(level); IsAccessLevel(level) {
			return level
		}
		return AccessNone
	}
	if level, ok := p[DefaultKey]; ok {
		if level = NormalizeAccess(level); IsAccessLevel(level) {
			return level
		}
	}
	return AccessNone
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

// IsAccessLevel reports whether level is one of the access-vocabulary values.
func IsAccessLevel(level string) bool {
	switch level {
	case AccessNone, AccessRead, AccessWrite, AccessFull:
		return true
	}
	return false
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
