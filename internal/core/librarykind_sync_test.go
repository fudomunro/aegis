package core

import (
	"testing"
)

// TestLibraryKinds_KindRuntimeInSync keeps the data registry and the
// visibility predicates aligned (issue #15). A kind with no wiring silently
// disappears from pre-request hints; leftover wiring is dead code. Both
// fail this test. // transitional (#312): kinds live in kindRuntime until
// they move into kinds.Builtin(); this test pins the union with no gaps
// and no extras in either registry.
func TestLibraryKinds_KindRuntimeInSync(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	builtin := map[string]bool{}
	for _, k := range srv.builtinKinds() {
		builtin[k.Def().ID] = true
	}

	seen := map[string]bool{}
	for _, k := range libraryKinds {
		if k.ID == "" {
			t.Fatal("libraryKinds entry with empty ID")
		}
		if seen[k.ID] {
			t.Fatalf("duplicate libraryKinds ID %q", k.ID)
		}
		seen[k.ID] = true
		if builtin[k.ID] {
			// Moved kinds expose Provides/HasProviders/Readable as
			// interface methods — they cannot be nil.
			continue
		}
		rt, ok := kindRuntime[k.ID]
		if !ok {
			t.Errorf("kind %q missing from kindRuntime and kinds.Builtin()", k.ID)
			continue
		}
		if rt.Provides == nil {
			t.Errorf("kind %q kindRuntime.Provides is nil", k.ID)
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
	for id := range builtin {
		if !seen[id] {
			t.Errorf("kinds.Builtin has extra kind %q with no libraryKinds entry", id)
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
	for id, rt := range kindRuntime {
		for _, route := range rt.Routes {
			if route.Pattern == "" || route.Handle == nil {
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
	for _, bk := range srv.builtinKinds() {
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

	for _, k := range libraryKinds {
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
	for path := range (&Server{}).libraryRouteHandlers() {
		if _, ok := declared[path]; ok || fixed[path] {
			continue
		}
		t.Errorf("route %q is neither kind-declared nor a fixed cross-kind route", path)
	}
}
