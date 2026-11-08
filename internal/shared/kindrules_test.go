package shared

import "testing"

func TestKindRules_Queries(t *testing.T) {
	RegisterKindRule("events", KindRule{Levels: []string{"freebusy"}})
	RegisterKindRule("places", KindRule{Levels: []string{"city", "precise"}, DefaultNone: true})
	RegisterKindRule("tracks", KindRule{})

	if !KindAllowsLevel("events", "freebusy") || KindAllowsLevel("events", "city") {
		t.Fatal("events levels")
	}
	if KindAllowsLevel("tracks", "freebusy") || KindAllowsLevel("unknown", "freebusy") {
		t.Fatal("closed/unknown keys honor nothing")
	}
	if !KindDefaultsNone("places") || KindDefaultsNone("events") || KindDefaultsNone("unknown") {
		t.Fatal("DefaultNone")
	}
	if got := KindsAllowingLevel("city"); len(got) != 1 || got[0] != "places" {
		t.Fatalf("KindsAllowingLevel(city) = %v", got)
	}
}

func TestKindRules_RegisterReplaces(t *testing.T) {
	RegisterKindRule("widgets", KindRule{Levels: []string{"city"}})
	RegisterKindRule("widgets", KindRule{DefaultNone: true})
	if KindAllowsLevel("widgets", "city") {
		t.Fatal("register must replace levels")
	}
	if !KindDefaultsNone("widgets") {
		t.Fatal("replacement keeps default none")
	}
}

func TestKindRules_MarkKindDefaultNone(t *testing.T) {
	RegisterKindRule("gadgets", KindRule{Levels: []string{"metadata"}})
	MarkKindDefaultNone("gadgets")
	if !KindDefaultsNone("gadgets") || !KindAllowsLevel("gadgets", "metadata") {
		t.Fatal("mark sets default none and keeps levels")
	}
	MarkKindDefaultNone("hithere")
	if !KindDefaultsNone("hithere") {
		t.Fatal("mark registers unknown ids")
	}
}

func TestKindRules_EmptyIDIgnored(t *testing.T) {
	RegisterKindRule("", KindRule{DefaultNone: true})
	MarkKindDefaultNone("")
	if KindDefaultsNone("") {
		t.Fatal("empty id must not register")
	}
}

func TestKindRules_KindsAllowingLevelSorted(t *testing.T) {
	RegisterKindRule("zeta", KindRule{Levels: []string{"summary"}})
	RegisterKindRule("alpha", KindRule{Levels: []string{"summary"}})
	got := KindsAllowingLevel("summary")
	want := map[string]bool{"alpha": true, "zeta": true}
	if len(got) != 2 || !want[got[0]] || !want[got[1]] || got[0] >= got[1] {
		t.Fatalf("KindsAllowingLevel(summary) = %v, want sorted [alpha zeta]", got)
	}
}
