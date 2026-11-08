package policy

import "testing"

func TestScopeGrantsAccess_ResolutionTable(t *testing.T) {
	// Every row of the resolution table in docs/external-access.md.
	tests := []struct {
		name     string
		grants   DataGrants
		dataType string
		provider string
		want     string
	}{
		{
			name:     "type absent, no default key",
			grants:   DataGrants{"music": {Default: AccessRead}},
			dataType: "photos",
			want:     AccessNone,
		},
		{
			name:     "no grants at all",
			grants:   nil,
			dataType: "music",
			want:     AccessNone,
		},
		{
			name:     "type absent, default key present",
			grants:   DataGrants{PermissionDefaultKey: {Default: AccessRead}},
			dataType: "photos",
			want:     AccessRead,
		},
		{
			name:     "listed, empty entry",
			grants:   DataGrants{"music": {}},
			dataType: "music",
			want:     AccessNone,
		},
		{
			name:     "listed, provider overrides only",
			grants:   DataGrants{"music": {Providers: map[string]string{"spotify": AccessRead}}},
			dataType: "music",
			provider: "spotify",
			want:     AccessNone,
		},
		{
			name:     "listed, provider overrides only, other provider",
			grants:   DataGrants{"music": {Providers: map[string]string{"spotify": AccessRead}}},
			dataType: "music",
			provider: "youtube",
			want:     AccessNone,
		},
		{
			name: "listed with empty default under default key",
			grants: DataGrants{
				PermissionDefaultKey: {Default: AccessRead},
				"music":              {},
			},
			dataType: "music",
			want:     AccessNone,
		},
		{
			name: "default key covers unlisted types only",
			grants: DataGrants{
				PermissionDefaultKey: {Default: AccessRead},
				"music":              {},
			},
			dataType: "photos",
			want:     AccessRead,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ScopeGrantsAccess(tt.grants, tt.dataType, tt.provider); got != tt.want {
				t.Fatalf("ScopeGrantsAccess() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestScopeGrantsAccess_ListedLevel(t *testing.T) {
	grants := DataGrants{"music": {Default: AccessRead}}
	if got := ScopeGrantsAccess(grants, "music", ""); got != AccessRead {
		t.Fatalf("listed type = %q, want read", got)
	}
}

func TestScopeGrantsAccess_OptInTypes(t *testing.T) {
	// The default key never enables opt-in types (places).
	grants := DataGrants{PermissionDefaultKey: {Default: AccessRead}}
	if got := ScopeGrantsAccess(grants, "places", ""); got != AccessNone {
		t.Fatalf("places via default key = %q, want none", got)
	}

	// An explicit name with a stated level grants it.
	grants = DataGrants{"places": {Default: AccessCity}}
	if got := ScopeGrantsAccess(grants, "places", ""); got != AccessCity {
		t.Fatalf("explicit places = %q, want city", got)
	}

	// Provider-only entries never open a closed type (opt-in included).
	grants = DataGrants{"places": {Providers: map[string]string{"owntracks": AccessPrecise}}}
	if got := ScopeGrantsAccess(grants, "places", "owntracks"); got != AccessNone {
		t.Fatalf("provider-only places = %q, want none", got)
	}
}

func TestScopeGrantsAccess_ProviderOverridesTightenOnly(t *testing.T) {
	grants := DataGrants{"music": {
		Default:   AccessRead,
		Providers: map[string]string{"spotify": AccessNone, "amazonmusic": AccessRead},
	}}
	if got := ScopeGrantsAccess(grants, "music", "spotify"); got != AccessNone {
		t.Fatalf("tightened provider = %q, want none", got)
	}
	if got := ScopeGrantsAccess(grants, "music", "amazonmusic"); got != AccessRead {
		t.Fatalf("equal provider override = %q, want read", got)
	}
	if got := ScopeGrantsAccess(grants, "music", "youtube"); got != AccessRead {
		t.Fatalf("unlisted provider = %q, want read", got)
	}

	// A wider provider override cannot open a closed type.
	grants = DataGrants{"music": {
		Default:   AccessNone,
		Providers: map[string]string{"spotify": AccessRead},
	}}
	if got := ScopeGrantsAccess(grants, "music", "spotify"); got != AccessNone {
		t.Fatalf("widening provider override = %q, want none", got)
	}
}

func TestScopeGrantsAccess_Unclassified(t *testing.T) {
	// Unclassified lookups use the strictest level of the grant map (issue #94).
	grants := DataGrants{PermissionDefaultKey: {Default: AccessRead}}
	if got := ScopeGrantsAccess(grants, "", ""); got != AccessRead {
		t.Fatalf("unclassified with default read = %q, want read", got)
	}
	grants = DataGrants{"music": {Default: AccessRead}}
	if got := ScopeGrantsAccess(grants, "", ""); got != AccessNone {
		t.Fatalf("unclassified with closed unlisted types = %q, want none", got)
	}
	if got := ScopeGrantsAccess(nil, "", ""); got != AccessNone {
		t.Fatalf("unclassified with no grants = %q, want none", got)
	}
}

func TestScopeGrantsPermissions_MaterializedView(t *testing.T) {
	grants := DataGrants{
		PermissionDefaultKey: {Default: AccessRead},
		"music":              {},
		"contacts":           {Default: AccessRead},
	}
	p := ScopeGrantsPermissions(grants)
	if got := p.For("photos"); got != AccessRead {
		t.Fatalf("unlisted type = %q, want read", got)
	}
	if got := p.For("music"); got != AccessNone {
		t.Fatalf("listed-empty type = %q, want none", got)
	}
	if got := p.For("contacts"); got != AccessRead {
		t.Fatalf("listed type = %q, want read", got)
	}
	if got := p.For("places"); got != AccessNone {
		t.Fatalf("opt-in type = %q, want none", got)
	}

	// No default key: unlisted types are none.
	p = ScopeGrantsPermissions(DataGrants{"music": {Default: AccessRead}})
	if got := p.For("photos"); got != AccessNone {
		t.Fatalf("unlisted without default key = %q, want none", got)
	}
}

func TestScopeTighten_NeverExceedsOwner(t *testing.T) {
	grants := DataGrants{"music": {Default: AccessRead}}
	// Scope tighter than owner wins.
	if got := ScopeTighten(grants, AccessFull, "music", ""); got != AccessRead {
		t.Fatalf("scope read under owner full = %q, want read", got)
	}
	// Owner tighter than scope wins — a scope grant never opens owner policy.
	if got := ScopeTighten(grants, AccessNone, "music", ""); got != AccessNone {
		t.Fatalf("scope read under owner none = %q, want none", got)
	}
	// Unlisted type: scope deny-by-default wins over unrestricted owner.
	if got := ScopeTighten(grants, AccessFull, "photos", ""); got != AccessNone {
		t.Fatalf("unlisted scope type = %q, want none", got)
	}
	// Redaction levels clamp: owner read, scope summary.
	grants = DataGrants{"events": {Default: AccessSummary}}
	if got := ScopeTighten(grants, AccessRead, "events", ""); got != AccessSummary {
		t.Fatalf("scope summary under owner read = %q, want summary", got)
	}
}

func TestScopeTightenPermissions_ANDView(t *testing.T) {
	grants := DataGrants{"music": {Default: AccessRead}}
	owner := Permissions{"music": AccessFull, "photos": AccessFull}

	merged := ScopeTightenPermissions(grants, owner)
	if got := merged.For("music"); got != AccessRead {
		t.Fatalf("music = %q, want read", got)
	}
	if got := merged.For("photos"); got != AccessNone {
		t.Fatalf("unlisted type = %q, want none", got)
	}
	if got := merged.For("contacts"); got != AccessNone {
		t.Fatalf("closed unlisted type = %q, want none", got)
	}

	// A principal holding no grants sees nothing, even under an
	// unrestricted owner tier.
	merged = ScopeTightenPermissions(nil, nil)
	if got := merged.For("music"); got != AccessNone {
		t.Fatalf("no-grants music = %q, want none", got)
	}
	if !allNone(merged) {
		t.Fatalf("no-grants view is not all none: %v", merged)
	}

	// Owner denial survives a granting scope.
	merged = ScopeTightenPermissions(DataGrants{"music": {Default: AccessFull}}, Permissions{"music": AccessNone})
	if got := merged.For("music"); got != AccessNone {
		t.Fatalf("owner none under scope full = %q, want none", got)
	}
}

func allNone(p Permissions) bool {
	for _, v := range p {
		if NormalizeAccess(v) != AccessNone {
			return false
		}
	}
	return true
}
