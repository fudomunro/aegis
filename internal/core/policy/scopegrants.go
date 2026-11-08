package policy

// Scope grants are an external principal's complete allow list
// (docs/external-access.md). Unlike DataGrants.Access — which keeps
// owner-tier "unset = unrestricted" semantics — scope resolution is
// deny-by-default and fail-closed: every ambiguity resolves to none. The two
// resolvers stay separate so a scope fix cannot silently change owner
// behavior and vice versa.

// ScopeGrantsAccess resolves an external principal's access for a data type
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
// never open a closed type. Unclassified lookups (empty dataType) resolve to
// the strictest level of the grant map so unknown surface cannot leak
// (issue #94).
func ScopeGrantsAccess(grants DataGrants, dataType, provider string) string {
	if dataType == "" {
		return ScopeGrantsPermissions(grants).Strictest()
	}
	if dataType == PermissionDefaultKey {
		return scopeGrantEntryLevel(grants.For(dataType), provider)
	}
	if entry, listed := grants[dataType]; listed {
		return scopeGrantEntryLevel(entry, provider)
	}
	def, hasDefault := grants[PermissionDefaultKey]
	if !hasDefault {
		return AccessNone
	}
	if isOptInDefaultNone(dataType) {
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
	for dataType := range grants {
		if dataType == PermissionDefaultKey {
			continue
		}
		out[dataType] = ScopeGrantsAccess(grants, dataType, "")
	}
	return out
}

// ScopeTighten ANDs a scope grant into an owner-tier level: the effective
// level is the stricter of both layers, so a scope grant can never exceed
// what the agent data grants already allow.
func ScopeTighten(grants DataGrants, ownerLevel, dataType, provider string) string {
	return stricterAccess(ownerLevel, ScopeGrantsAccess(grants, dataType, provider))
}

// ScopeTightenPermissions ANDs scope grants into an owner-tier Permissions
// map key by key (the reserved "default" key included), so notices and
// strictest checks see the effective view for a scoped principal.
func ScopeTightenPermissions(grants DataGrants, owner Permissions) Permissions {
	scope := ScopeGrantsPermissions(grants)
	out := Permissions{}
	out[PermissionDefaultKey] = stricterAccess(unlistedLevel(owner), unlistedLevel(scope))
	for dataType := range owner {
		if dataType == PermissionDefaultKey {
			continue
		}
		out[dataType] = stricterAccess(owner.For(dataType), scope.For(dataType))
	}
	for dataType := range scope {
		if dataType == PermissionDefaultKey {
			continue
		}
		if _, ok := out[dataType]; !ok {
			out[dataType] = stricterAccess(owner.For(dataType), scope.For(dataType))
		}
	}
	return out
}

// unlistedLevel is the map's opinion on data types it does not list.
func unlistedLevel(p Permissions) string {
	if level, ok := p[PermissionDefaultKey]; ok {
		return NormalizeAccess(level)
	}
	return DefaultAccessType
}
