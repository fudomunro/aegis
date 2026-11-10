package shared

import "testing"

func TestKindRules_Queries(t *testing.T) {
	RegisterKindRule("places", KindRule{DefaultNone: true})
	RegisterKindRule("tracks", KindRule{})

	if !KindDefaultsNone("places") || KindDefaultsNone("events") || KindDefaultsNone("unknown") {
		t.Fatal("DefaultNone")
	}
}

func TestKindRules_RegisterReplaces(t *testing.T) {
	RegisterKindRule("widgets", KindRule{})
	RegisterKindRule("widgets", KindRule{DefaultNone: true})
	if !KindDefaultsNone("widgets") {
		t.Fatal("replacement sets default none")
	}
	RegisterKindRule("widgets", KindRule{})
	if KindDefaultsNone("widgets") {
		t.Fatal("register must replace the rule")
	}
}

func TestKindRules_MarkKindDefaultNone(t *testing.T) {
	RegisterKindRule("gadgets", KindRule{})
	MarkKindDefaultNone("gadgets")
	if !KindDefaultsNone("gadgets") {
		t.Fatal("mark sets default none")
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
