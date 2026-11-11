package policy

import (
	"testing"

	"github.com/fudomunro/aegis/internal/shared"
)

func TestOverviewKinds_PrefersLibraryOrderThenExtras(t *testing.T) {
	got := OverviewKinds(
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
	kinds := []string{"tracks", "places", "events"}
	got := AgentOverviewLevels(DataGrants{
		"events": {Default: shared.AccessRead},
		"places": {Default: shared.AccessRead},
		"tracks": {Default: shared.AccessRead},
	}, kinds)
	if got["tracks"] != shared.AccessRead {
		t.Fatalf("tracks = %q, want read", got["tracks"])
	}
	if got["places"] != shared.AccessRead {
		t.Fatalf("places = %q, want read", got["places"])
	}
	if got["events"] != shared.AccessRead {
		t.Fatalf("events = %q, want read", got["events"])
	}

	got = AgentOverviewLevels(DataGrants{}, kinds)
	if got["tracks"] != shared.DefaultAccessType {
		t.Fatalf("unset tracks = %q, want %q", got["tracks"], shared.DefaultAccessType)
	}
	if got["places"] != shared.AccessNone {
		t.Fatalf("unset places = %q, want none", got["places"])
	}
}

func TestAppOverviewLevels_FailClosedAndExplicit(t *testing.T) {
	kinds := []string{"tracks", "places", "events"}
	levels, explicit := AppOverviewLevels(nil, kinds)
	for _, dt := range kinds {
		if levels[dt] != shared.AccessNone {
			t.Fatalf("nil scopes %s = %q, want none", dt, levels[dt])
		}
		if explicit[dt] {
			t.Fatalf("nil scopes should not mark %s explicit", dt)
		}
	}

	levels, explicit = AppOverviewLevels(shared.GrantMap{
		shared.DefaultKey: shared.AccessNone,
		"events":          shared.AccessRead,
		"places":          shared.AccessRead,
		"tracks":          shared.AccessRead,
	}, kinds)
	if levels["events"] != shared.AccessRead {
		t.Fatalf("events = %q, want read", levels["events"])
	}
	if levels["places"] != shared.AccessRead {
		t.Fatalf("places = %q, want read", levels["places"])
	}
	if !explicit["events"] || !explicit["places"] || !explicit["tracks"] {
		t.Fatalf("explicit = %v", explicit)
	}

	levels, _ = AppOverviewLevels(shared.GrantMap{
		shared.DefaultKey: shared.AccessRead,
	}, kinds)
	if levels["tracks"] != shared.AccessRead {
		t.Fatalf("tracks via default = %q, want read", levels["tracks"])
	}
	if levels["places"] != shared.AccessNone {
		t.Fatalf("places via default = %q, want none", levels["places"])
	}
}

func TestAccessorRelevantTo(t *testing.T) {
	agent := Accessor{
		Kind:   AccessorAgent,
		Levels: map[string]string{"tracks": shared.AccessRead, "places": shared.AccessNone},
	}
	if !agent.RelevantTo("tracks") || !agent.RelevantTo("places") {
		t.Fatal("agent should be relevant to every kind")
	}

	app := Accessor{
		Kind:     AccessorApp,
		Levels:   map[string]string{"tracks": shared.AccessNone, "events": shared.AccessRead, "photos": shared.AccessNone},
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
		t.Fatal("app should not be relevant for unnamed none kind")
	}

	sync := Accessor{
		Kind:     AccessorSync,
		Levels:   map[string]string{"messages": shared.AccessRead, "tracks": shared.AccessNone},
		Explicit: map[string]bool{"messages": true},
	}
	if !sync.RelevantTo("messages") {
		t.Fatal("sync should be relevant for its stored type")
	}
	if sync.RelevantTo("tracks") {
		t.Fatal("sync should not be relevant for unrelated type")
	}
}

func TestBuildKindAccess_OnlyRelevantOthers(t *testing.T) {
	kinds := []string{"tracks", "messages", "places"}
	accessors := []Accessor{
		{
			ID:     agentAccessorForTest,
			Kind:   AccessorAgent,
			Label:  "LLM agents",
			Levels: map[string]string{"tracks": shared.AccessRead, "messages": shared.AccessRead, "places": shared.AccessNone},
		},
		{
			ID:       "app:eco",
			Kind:     AccessorApp,
			Label:    "Eco",
			Levels:   map[string]string{"tracks": shared.AccessNone, "messages": shared.AccessNone, "places": shared.AccessNone},
			Explicit: map[string]bool{"tracks": true},
		},
		{
			ID:     "sync:mail",
			Kind:   AccessorSync,
			Label:  "Mail",
			Levels: map[string]string{"tracks": shared.AccessNone, "messages": shared.AccessRead, "places": shared.AccessNone},
		},
	}
	rows := BuildKindAccess(kinds, accessors)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	if rows[0].ID != "tracks" || rows[0].AgentLevel != shared.AccessRead {
		t.Fatalf("tracks row = %+v", rows[0])
	}
	if len(rows[0].Accessors) != 1 || rows[0].Accessors[0].ID != "app:eco" {
		t.Fatalf("tracks accessors = %+v", rows[0].Accessors)
	}
	if len(rows[1].Accessors) != 1 || rows[1].Accessors[0].ID != "sync:mail" {
		t.Fatalf("messages accessors = %+v", rows[1].Accessors)
	}
	if rows[1].AgentLevel != shared.AccessRead {
		t.Fatalf("messages agent = %q", rows[1].AgentLevel)
	}
	if len(rows[2].Accessors) != 0 {
		t.Fatalf("places should have no other accessors: %+v", rows[2].Accessors)
	}
	if rows[2].AgentLevel != shared.AccessNone {
		t.Fatalf("places agent = %q", rows[2].AgentLevel)
	}
}

const agentAccessorForTest = "agent"
