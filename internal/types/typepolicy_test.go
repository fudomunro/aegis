package types

import "testing"

func TestTypePolicies_Access_OptInProviderGrant(t *testing.T) {
	policies := DataGrants{
		"places": {
			Default:   AccessNone,
			Providers: map[string]string{"google-timeline": AccessCity, "owntracks": AccessNone},
		},
	}
	if got := policies.Access("places", "google-timeline"); got != AccessCity {
		t.Fatalf("explicit provider opt-in = %q, want city", got)
	}
	if got := policies.Access("places", "owntracks"); got != AccessNone {
		t.Fatalf("provider none = %q", got)
	}
	if got := policies.Access("places", "immich"); got != AccessNone {
		t.Fatalf("unnamed provider = %q", got)
	}
}

func TestTypePolicies_Access_OptInDefaultNeverLoosened(t *testing.T) {
	policies := DataGrants{
		"places": {
			Default:   AccessPrecise,
			Providers: map[string]string{"owntracks": AccessCity, "other": AccessFull},
		},
	}
	if got := policies.Access("places", "owntracks"); got != AccessCity {
		t.Fatalf("tighten precise→city = %q", got)
	}
	if got := policies.Access("places", "other"); got != AccessPrecise {
		t.Fatalf("provider cannot loosen type default: %q", got)
	}
}

func TestTypePolicies_Access_UnsetDefaultIsFull(t *testing.T) {
	policies := DataGrants{"tracks": {}}
	if got := policies.Access("tracks", "spotify"); got != DefaultAccessType {
		t.Fatalf("unset default = %q", got)
	}
	policies = DataGrants{"tracks": {Default: AccessRead, Providers: map[string]string{"spotify": AccessNone}}}
	if got := policies.Access("tracks", "spotify"); got != AccessNone {
		t.Fatalf("tighten read→none = %q", got)
	}
	if got := policies.Access("tracks", "immich"); got != AccessRead {
		t.Fatalf("missing provider uses default: %q", got)
	}
}

func TestEffectiveAccess_DataGrantPrimaryServiceLimitsTighten(t *testing.T) {
	policies := DataGrants{
		"tracks": {Default: AccessFull, Providers: map[string]string{"spotify": AccessRead}},
	}

	// Spotify's legacy map can only tighten the resolved type policy.
	spotifyLegacy := Permissions{"tracks": AccessNone, "player": AccessNone}
	if got := EffectiveAccess(policies, spotifyLegacy, "tracks", "spotify"); got != AccessNone {
		t.Fatalf("legacy tighten = %q", got)
	}
	if got := EffectiveAccess(policies, spotifyLegacy, "player", "spotify"); got != AccessNone {
		t.Fatalf("legacy player = %q", got)
	}

	// Provider with no legacy map keeps the type default / provider override.
	if got := EffectiveAccess(policies, nil, "tracks", "spotify"); got != AccessRead {
		t.Fatalf("provider override = %q", got)
	}
	if got := EffectiveAccess(policies, nil, "tracks", "immich"); got != AccessFull {
		t.Fatalf("type default = %q", got)
	}

	// Legacy read tightens type default full.
	if got := EffectiveAccess(policies, Permissions{"tracks": AccessRead}, "tracks", "spotify"); got != AccessRead {
		t.Fatalf("legacy read = %q", got)
	}
}

func TestEffectiveAccess_ServiceLimitsWithoutDataGrant(t *testing.T) {
	legacy := Permissions{"places": AccessCity, "tracks": AccessRead}
	if got := EffectiveAccess(nil, legacy, "places", "google-timeline"); got != AccessCity {
		t.Fatalf("legacy opt-in = %q", got)
	}
	if got := EffectiveAccess(nil, legacy, "tracks", "spotify"); got != AccessRead {
		t.Fatalf("legacy read = %q", got)
	}
	if got := EffectiveAccess(nil, legacy, "player", "spotify"); got != DefaultAccessType {
		t.Fatalf("legacy default key missing → DefaultAccessType, got %q", got)
	}
	if got := EffectiveAccess(nil, Permissions{"default": AccessNone}, "player", "spotify"); got != AccessNone {
		t.Fatalf("legacy default none = %q", got)
	}
	if got := EffectiveAccess(nil, nil, "places", "x"); got != AccessNone {
		t.Fatalf("opt-in without policy = %q", got)
	}
	if got := EffectiveAccess(nil, nil, "tracks", "x"); got != DefaultAccessType {
		t.Fatalf("normal type without policy = %q", got)
	}
}

func TestEffectiveAccess_UnclassifiedUsesStrictest(t *testing.T) {
	policies := DataGrants{
		"tracks": {Default: AccessRead},
		"player": {Default: AccessNone},
	}
	got := EffectiveAccess(policies, nil, "", "spotify")
	if got != AccessNone {
		t.Fatalf("unclassified = %q, want none (strictest)", got)
	}
	got = EffectiveAccess(nil, Permissions{"tracks": AccessRead, "player": AccessNone}, "", "spotify")
	if got != AccessNone {
		t.Fatalf("legacy unclassified = %q", got)
	}
	got = EffectiveAccess(DataGrants{"tracks": {Default: AccessRead}}, nil, "", "spotify")
	if got != AccessRead {
		t.Fatalf("single read type unclassified = %q", got)
	}
}

func TestEffectivePermissions_Materializes(t *testing.T) {
	policies := DataGrants{
		"tracks": {Default: AccessFull, Providers: map[string]string{"spotify": AccessRead}},
		"places": {Default: AccessNone, Providers: map[string]string{"spotify": AccessCity}},
	}
	legacy := Permissions{"player": AccessNone}
	perms := EffectivePermissions(policies, legacy, "spotify")
	if perms.For("tracks") != AccessRead {
		t.Fatalf("tracks = %q", perms.For("tracks"))
	}
	if perms.For("places") != AccessCity {
		t.Fatalf("places = %q", perms.For("places"))
	}
	if perms.For("player") != AccessNone {
		t.Fatalf("player = %q", perms.For("player"))
	}
}

func TestValidateDataGrants(t *testing.T) {
	if err := ValidateDataGrants(DataGrants{
		"tracks":       {Default: AccessRead},
		"places":       {Default: AccessCity, Providers: map[string]string{"a": AccessPrecise}},
		"events":       {Default: AccessFreeBusy},
		"transactions": {Default: AccessSummary},
	}); err != nil {
		t.Fatalf("valid policies: %v", err)
	}
	if err := ValidateDataGrants(DataGrants{"tracks": {Default: "maybe"}}); err == nil {
		t.Fatal("unknown level should fail")
	}
	if err := ValidateDataGrants(DataGrants{"tracks": {Default: AccessFreeBusy}}); err == nil {
		t.Fatal("freebusy on tracks should fail")
	}
	if err := ValidateDataGrants(DataGrants{"places": {Providers: map[string]string{"": AccessCity}}}); err == nil {
		t.Fatal("empty provider key should fail")
	}
	if err := ValidateDataGrants(DataGrants{"places": {Providers: map[string]string{"a": AccessFreeBusy}}}); err == nil {
		t.Fatal("freebusy on places should fail")
	}
}

func TestTypePolicies_PermissionsFor(t *testing.T) {
	policies := DataGrants{
		"tracks": {Default: AccessRead},
		"player": {Default: AccessNone},
	}
	p := policies.PermissionsFor("spotify")
	if p.For("tracks") != AccessRead || p.For("player") != AccessNone {
		t.Fatalf("PermissionsFor = %+v", p)
	}
	if (DataGrants{}).PermissionsFor("x") != nil {
		t.Fatal("empty policies should materialize nil")
	}
}
