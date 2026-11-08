package core

import (
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

// TestLibraryKinds_KindRuntimeInSync keeps the data registry and core
// visibility predicates aligned (issue #15). A kind with no kindRuntime row
// silently disappears from pre-request hints; a leftover runtime key is dead
// code. Both fail this test. Provides is required so the flow map's
// per-service kind listing cannot drift out of step either (issue #254).
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
}

// TestLibraryKinds_RoutesInSync keeps route declarations and the kind
// registry aligned in both directions (issue #312): every registry kind
// owns a declared route at kind.Path with matching app exposure, and every
// registered library route is either kind-declared or a fixed cross-kind
// entry — no route without a kind.
func TestLibraryKinds_RoutesInSync(t *testing.T) {
	for _, k := range libraryKinds {
		rt := kindRuntime[k.ID]
		var primary *Route
		for i := range rt.Routes {
			if rt.Routes[i].Pattern == k.Path {
				primary = &rt.Routes[i]
			}
		}
		if primary == nil {
			t.Errorf("kind %q has no declared route at %s", k.ID, k.Path)
			continue
		}
		if primary.Handle == nil {
			t.Errorf("kind %q primary route has no handler", k.ID)
		}
		if primary.AppReadable != k.AppReadable {
			t.Errorf("kind %q primary route AppReadable = %v, kind data = %v",
				k.ID, primary.AppReadable, k.AppReadable)
		}
	}

	declared := map[string]string{} // pattern → owning kind ID
	for id, rt := range kindRuntime {
		if types.BuiltinKindByID(id) == nil {
			t.Errorf("kindRuntime entry %q declares routes but has no registry kind", id)
		}
		for _, route := range rt.Routes {
			if route.Pattern == "" || route.Handle == nil {
				t.Errorf("kind %q declares an incomplete route (%q)", id, route.Pattern)
				continue
			}
			if prev, dup := declared[route.Pattern]; dup {
				t.Errorf("route %q declared by both %q and %q", route.Pattern, prev, id)
			}
			declared[route.Pattern] = id
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
