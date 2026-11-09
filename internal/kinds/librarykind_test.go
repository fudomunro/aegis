package kinds

import "testing"

func TestLibraryKind_MentionsInstructionsCacheKey(t *testing.T) {
	k := LibraryKind{
		ID: "widgets", Path: "/v1/library/widgets", Label: "widgets",
		Usage: "widgets", Summary: "widgets", SyncRule: "merge",
		ExampleQuestion: "what widgets?",
		Keywords:        []string{"my widgets"},
	}
	if !k.Mentions("show my widgets please") || k.Mentions("play some jazz") {
		t.Fatal("Mentions")
	}
	s := k.Instructions("https://aegis.example.com/")
	if s == "" {
		t.Fatal("Instructions empty")
	}
	if k.CacheKey() != "library:widgets" {
		t.Fatalf("CacheKey = %q", k.CacheKey())
	}
}
