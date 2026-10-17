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
		"events": {Default: AccessFreeBusy},
		"places": {Default: AccessCity},
		"tracks": {Default: AccessRead},
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

	got = AgentOverviewLevels(TypePolicies{}, dataTypes)
	if got["tracks"] != DefaultAccessType {
		t.Fatalf("unset tracks = %q, want %q", got["tracks"], DefaultAccessType)
	}
	if got["places"] != AccessNone {
		t.Fatalf("unset places = %q, want none", got["places"])
	}
}

func TestAppOverviewLevels_FailClosedAndExplicit(t *testing.T) {
	dataTypes := []string{"tracks", "places", "events"}
	levels, explicit := AppOverviewLevels(nil, dataTypes)
	for _, dt := range dataTypes {
		if levels[dt] != AccessNone {
			t.Fatalf("nil scopes %s = %q, want none", dt, levels[dt])
		}
		if explicit[dt] {
			t.Fatalf("nil scopes should not mark %s explicit", dt)
		}
	}

	levels, explicit = AppOverviewLevels(Permissions{
		PermissionDefaultKey: AccessNone,
		"events":             AccessFreeBusy,
		"places":             AccessPrecise,
		"tracks":             AccessRead,
	}, dataTypes)
	if levels["events"] != AccessFreeBusy {
		t.Fatalf("events = %q, want freebusy", levels["events"])
	}
	if levels["places"] != AccessPrecise {
		t.Fatalf("places = %q, want precise", levels["places"])
	}
	if !explicit["events"] || !explicit["places"] || !explicit["tracks"] {
		t.Fatalf("explicit = %v", explicit)
	}

	levels, _ = AppOverviewLevels(Permissions{
		PermissionDefaultKey: AccessRead,
	}, dataTypes)
	if levels["tracks"] != AccessRead {
		t.Fatalf("tracks via default = %q, want read", levels["tracks"])
	}
	if levels["places"] != AccessNone {
		t.Fatalf("places via default = %q, want none", levels["places"])
	}
}

func TestAccessorRelevantTo(t *testing.T) {
	agent := Accessor{
		Kind:   AccessorAgent,
		Levels: map[string]string{"tracks": AccessRead, "places": AccessNone},
	}
	if !agent.RelevantTo("tracks") || !agent.RelevantTo("places") {
		t.Fatal("agent should be relevant to every data type")
	}

	app := Accessor{
		Kind:     AccessorApp,
		Levels:   map[string]string{"tracks": AccessNone, "events": AccessFreeBusy, "photos": AccessNone},
		Explicit: map[string]bool{"tracks": true, "events": true},
	}
	// Explicit none is still relevant (operator named the type).
	if !app.RelevantTo("tracks") {
		t.Fatal("app should be relevant when type is explicit even if none")
	}
	if !app.RelevantTo("events") {
		t.Fatal("app should be relevant for non-none level")
	}
	// Unnamed, none — not relevant.
	if app.RelevantTo("photos") {
		t.Fatal("app should not be relevant for unnamed none type")
	}

	sync := Accessor{
		Kind:     AccessorSync,
		Levels:   map[string]string{"messages": AccessRead, "tracks": AccessNone},
		Explicit: map[string]bool{"messages": true},
	}
	if !sync.RelevantTo("messages") {
		t.Fatal("sync should be relevant for its stored type")
	}
	if sync.RelevantTo("tracks") {
		t.Fatal("sync should not be relevant for unrelated type")
	}
}

func TestBuildTypeAccess_OnlyRelevantOthers(t *testing.T) {
	dataTypes := []string{"tracks", "messages", "places"}
	accessors := []Accessor{
		{
			ID:     agentAccessorForTest,
			Kind:   AccessorAgent,
			Label:  "LLM agents",
			Levels: map[string]string{"tracks": AccessRead, "messages": AccessRead, "places": AccessNone},
		},
		{
			ID:       "app:eco",
			Kind:     AccessorApp,
			Label:    "Eco",
			Levels:   map[string]string{"tracks": AccessNone, "messages": AccessNone, "places": AccessNone},
			Explicit: map[string]bool{"tracks": true},
		},
		{
			ID:     "sync:mail",
			Kind:   AccessorSync,
			Label:  "Mail",
			Levels: map[string]string{"tracks": AccessNone, "messages": AccessRead, "places": AccessNone},
		},
	}
	rows := BuildTypeAccess(dataTypes, accessors)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	if rows[0].ID != "tracks" || rows[0].AgentLevel != AccessRead {
		t.Fatalf("tracks row = %+v", rows[0])
	}
	if len(rows[0].Accessors) != 1 || rows[0].Accessors[0].ID != "app:eco" {
		t.Fatalf("tracks accessors = %+v", rows[0].Accessors)
	}
	if len(rows[1].Accessors) != 1 || rows[1].Accessors[0].ID != "sync:mail" {
		t.Fatalf("messages accessors = %+v", rows[1].Accessors)
	}
	if rows[1].AgentLevel != AccessRead {
		t.Fatalf("messages agent = %q", rows[1].AgentLevel)
	}
	if len(rows[2].Accessors) != 0 {
		t.Fatalf("places should have no other accessors: %+v", rows[2].Accessors)
	}
	if rows[2].AgentLevel != AccessNone {
		t.Fatalf("places agent = %q", rows[2].AgentLevel)
	}
}

const agentAccessorForTest = "agent"
