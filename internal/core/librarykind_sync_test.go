package core

import (
	"github.com/fudomunro/aegis/internal/library"
	"testing"
)

// TestLibraryKinds_BuiltinInSync keeps the data registry and the kinds
// package's built-in list aligned (issue #15). A kind with no wiring
// silently disappears from pre-request hints; leftover wiring is dead
// code. Both fail this test.
func TestLibraryKinds_BuiltinInSync(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	builtin := map[string]bool{}
	for _, k := range srv.library().BuiltinKinds() {
		if k.Def().ID == "" {
			t.Fatal("kinds.Builtin entry with empty ID")
		}
		if builtin[k.Def().ID] {
			t.Fatalf("duplicate kinds.Builtin ID %q", k.Def().ID)
		}
		builtin[k.Def().ID] = true
	}

	seen := map[string]bool{}
	for _, k := range library.LibraryKinds {
		if k.ID == "" {
			t.Fatal("library.LibraryKinds entry with empty ID")
		}
		if seen[k.ID] {
			t.Fatalf("duplicate library.LibraryKinds ID %q", k.ID)
		}
		seen[k.ID] = true
		if !builtin[k.ID] {
			t.Errorf("kind %q missing from kinds.Builtin()", k.ID)
		}
	}
	for id := range builtin {
		if !seen[id] {
			t.Errorf("kinds.Builtin has extra kind %q with no library.LibraryKinds entry", id)
		}
	}
}

// TestLibraryKinds_RoutesInSync keeps route declarations and the kind
// registry aligned in both directions (issue #312): every registry kind
// owns a declared route at kind.Path with matching app exposure, and every
// registered library route is either kind-declared or a fixed cross-kind
// entry — no route without a kind.
func TestLibraryKinds_RoutesInSync(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")

	declared := map[string]string{} // pattern → owning kind ID
	appReadable := map[string]bool{}
	for _, bk := range srv.library().BuiltinKinds() {
		id := bk.Def().ID
		for _, route := range bk.Routes() {
			if route.Pattern == "" || route.Handler == nil {
				t.Errorf("kind %q declares an incomplete route (%q)", id, route.Pattern)
				continue
			}
			if prev, dup := declared[route.Pattern]; dup {
				t.Errorf("route %q declared by both %q and %q", route.Pattern, prev, id)
			}
			declared[route.Pattern] = id
			appReadable[route.Pattern] = route.AppReadable
		}
	}

	for _, k := range library.LibraryKinds {
		owner, ok := declared[k.Path]
		if !ok {
			t.Errorf("kind %q has no declared route at %s", k.ID, k.Path)
			continue
		}
		if owner != k.ID {
			t.Errorf("kind %q route at %s is owned by %q", k.ID, k.Path, owner)
		}
		if appReadable[k.Path] != k.AppReadable {
			t.Errorf("kind %q primary route AppReadable = %v, kind data = %v",
				k.ID, appReadable[k.Path], k.AppReadable)
		}
	}

	fixed := map[string]bool{
		"/v1/library/copies": true,
		"/v1/library/review": true,
		"/v1/library/links":  true,
		"/v1/library/links/": true,
	}
	for path := range (&Server{}).library().RouteHandlers() {
		if _, ok := declared[path]; ok || fixed[path] {
			continue
		}
		t.Errorf("route %q is neither kind-declared nor a fixed cross-kind route", path)
	}
}
