package policy

import "github.com/fudomunro/aegis/internal/shared"

// Scope grants are an external principal's complete allow list
// (docs/external-access.md). Unlike DataGrants.Access — which keeps
// owner-tier "unset = unrestricted" semantics — scope resolution is
// deny-by-default and fail-closed: every ambiguity resolves to none. The two
// resolvers stay separate so a scope fix cannot silently change owner
// behavior and vice versa.

// ScopeGrantsAccess resolves an external principal's access for a kind
// from its scope grants. Resolution table (docs/external-access.md):
//
//	type absent, no "default" key                     → none
//	type absent, "default" key present                → the default key's level
//	type listed with an empty entry                   → none
//	type listed with only provider overrides          → none (overrides tighten only)
//	type listed with an empty default, under "default" → none (listing is explicit)
//
// Opt-in types (places) require an explicit name — the reserved "default" key
// never enables them. Provider overrides can only tighten a stated level,
// never open a closed type. Unclassified lookups (empty kind) resolve to
// the strictest level of the grant map so unknown surface cannot leak
// (issue #94).
func ScopeGrantsAccess(grants DataGrants, kind, provider string) string {
	if kind == "" {
		return ScopeGrantsPermissions(grants).Strictest()
	}
	if kind == PermissionDefaultKey {
		return scopeGrantEntryLevel(grants.For(kind), provider)
	}
	if entry, listed := grants[kind]; listed {
		return scopeGrantEntryLevel(entry, provider)
	}
	def, hasDefault := grants[PermissionDefaultKey]
	if !hasDefault {
		return AccessNone
	}
	if shared.KindDefaultsNone(kind) {
		return AccessNone
	}
	return scopeGrantEntryLevel(def, provider)
}

// scopeGrantEntryLevel resolves one grant entry to a level. An entry with no
// stated default resolves to none — scope grants have no implicit "unset
// means unrestricted" (that contract stays in DataGrants.Access). Provider
// overrides tighten the stated level only.
func scopeGrantEntryLevel(entry DataGrant, provider string) string {
	level := NormalizeAccess(entry.Default)
	if level == "" {
		return AccessNone
	}
	if provider == "" {
		return level
	}
	ov, ok := entry.Providers[provider]
	if !ok {
		return level
	}
	ovLevel := NormalizeAccess(ov)
	if ovLevel == "" {
		return level
	}
	return stricterAccess(level, ovLevel)
}

// ScopeGrantsPermissions materializes the deny-by-default permission view of
// a scope grant map: the reserved "default" key holds the level for unlisted
// types (none when the key is absent) and every listed type holds its
// resolved level. Used for notices, strictest checks, and ANDing with
// owner-tier policy.
func ScopeGrantsPermissions(grants DataGrants) Permissions {
	out := Permissions{}
	if def, ok := grants[PermissionDefaultKey]; ok {
		level := NormalizeAccess(def.Default)
		if level == "" {
			level = AccessNone
		}
		out[PermissionDefaultKey] = level
	} else {
		out[PermissionDefaultKey] = AccessNone
	}
	for kind := range grants {
		if kind == PermissionDefaultKey {
			continue
		}
		out[kind] = ScopeGrantsAccess(grants, kind, "")
	}
	return out
}

// ScopeTighten ANDs a scope grant into an owner-tier level: the effective
// level is the stricter of both layers, so a scope grant can never exceed
// what the agent data grants already allow.
func ScopeTighten(grants DataGrants, ownerLevel, kind, provider string) string {
	return stricterAccess(ownerLevel, ScopeGrantsAccess(grants, kind, provider))
}

// ScopeTightenPermissions ANDs scope grants into an owner-tier Permissions
// map key by key (the reserved "default" key included), so notices and
// strictest checks see the effective view for a scoped principal.
func ScopeTightenPermissions(grants DataGrants, owner Permissions) Permissions {
	scope := ScopeGrantsPermissions(grants)
	out := Permissions{}
	out[PermissionDefaultKey] = stricterAccess(unlistedLevel(owner), unlistedLevel(scope))
	for kind := range owner {
		if kind == PermissionDefaultKey {
			continue
		}
		out[kind] = stricterAccess(owner.For(kind), scope.For(kind))
	}
	for kind := range scope {
		if kind == PermissionDefaultKey {
			continue
		}
		if _, ok := out[kind]; !ok {
			out[kind] = stricterAccess(owner.For(kind), scope.For(kind))
		}
	}
	return out
}

// unlistedLevel is the map's opinion on kinds it does not list.
func unlistedLevel(p Permissions) string {
	if level, ok := p[PermissionDefaultKey]; ok {
		return NormalizeAccess(level)
	}
	return DefaultAccessType
}
