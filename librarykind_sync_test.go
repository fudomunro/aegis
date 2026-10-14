package main

import "testing"

// TestLibraryKinds_KindRuntimeInSync keeps the data registry and core
// visibility predicates aligned (issue #15). A kind with no kindRuntime row
// silently disappears from pre-request hints; a leftover runtime key is dead
// code. Both fail this test.
func TestLibraryKinds_KindRuntimeInSync(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range libraryKinds {
		if k.ID == "" {
			t.Fatal("libraryKinds entry with empty ID")
		}
		if seen[k.ID] {
			t.Fatalf("duplicate libraryKinds ID %q", k.ID)
		}
		seen[k.ID] = true
		rt, ok := kindRuntime[k.ID]
		if !ok {
			t.Errorf("kind %q missing kindRuntime entry", k.ID)
			continue
		}
		if rt.HasProviders == nil {
			t.Errorf("kind %q kindRuntime.HasProviders is nil", k.ID)
		}
		if rt.Readable == nil {
			t.Errorf("kind %q kindRuntime.Readable is nil", k.ID)
		}
	}
	for id := range kindRuntime {
		if !seen[id] {
			t.Errorf("kindRuntime has extra key %q with no libraryKinds entry", id)
		}
	}
}
