package types

import "testing"

func TestOverviewDataTypes_PrefersLibraryOrderThenExtras(t *testing.T) {
	got := OverviewDataTypes(
		[]string{"tracks", "photos", "places"},
		[]string{"custom", "tracks", "default", "", "zzz"},
	)
	want := []string{"tracks", "photos", "places", "custom", "zzz"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestAgentOverviewLevels_DefaultsAndOptIn(t *testing.T) {
	dataTypes := []string{"tracks", "places", "events"}
	got := AgentOverviewLevels(TypePolicies{
		"events":  {Default: AccessFreeBusy},
		"places":  {Default: AccessCity},
		"tracks":  {Default: AccessRead},
	}, dataTypes)
	if got["tracks"] != AccessRead {
		t.Fatalf("tracks = %q, want read", got["tracks"])
	}
	if got["places"] != AccessCity {
		t.Fatalf("places = %q, want city", got["places"])
	}
	if got["events"] != AccessFreeBusy {
		t.Fatalf("events = %q, want freebusy", got["events"])
	}

	// Unset normal types fall open to default access; places stay none.
	got = AgentOverviewLevels(TypePolicies{}, dataTypes)
	if got["tracks"] != DefaultAccessType {
		t.Fatalf("unset tracks = %q, want %q", got["tracks"], DefaultAccessType)
	}
	if got["places"] != AccessNone {
		t.Fatalf("unset places = %q, want none", got["places"])
	}
}

func TestAppOverviewLevels_FailClosed(t *testing.T) {
	dataTypes := []string{"tracks", "places", "events"}
	got := AppOverviewLevels(nil, dataTypes)
	for _, dt := range dataTypes {
		if got[dt] != AccessNone {
			t.Fatalf("nil scopes %s = %q, want none", dt, got[dt])
		}
	}

	got = AppOverviewLevels(Permissions{
		PermissionDefaultKey: AccessNone,
		"events":             AccessFreeBusy,
		"places":             AccessPrecise,
		"tracks":             AccessRead,
	}, dataTypes)
	if got["events"] != AccessFreeBusy {
		t.Fatalf("events = %q, want freebusy", got["events"])
	}
	if got["places"] != AccessPrecise {
		t.Fatalf("places = %q, want precise", got["places"])
	}
	if got["tracks"] != AccessRead {
		t.Fatalf("tracks = %q, want read", got["tracks"])
	}

	// default does not open opt-in places when unnamed.
	got = AppOverviewLevels(Permissions{
		PermissionDefaultKey: AccessRead,
	}, dataTypes)
	if got["tracks"] != AccessRead {
		t.Fatalf("tracks via default = %q, want read", got["tracks"])
	}
	if got["places"] != AccessNone {
		t.Fatalf("places via default = %q, want none", got["places"])
	}
}

func TestAccessorSortedLevels_SkipsDefault(t *testing.T) {
	a := Accessor{Levels: map[string]string{
		"tracks":             AccessRead,
		"photos":             AccessRead,
		PermissionDefaultKey: AccessNone,
	}}
	got := a.SortedLevels()
	if len(got) != 2 || got[0] != "photos" || got[1] != "tracks" {
		t.Fatalf("sorted = %v", got)
	}
	if a.AccessorLevel("tracks") != AccessRead {
		t.Fatalf("AccessorLevel(tracks) = %q", a.AccessorLevel("tracks"))
	}
	if a.AccessorLevel("missing") != "" {
		t.Fatalf("missing level = %q, want empty", a.AccessorLevel("missing"))
	}
}
