package core

import (
	"net/http"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

// TestLibraryKinds_RegistryConsistency (issue #183) proves the hand-copied
// kind lists are gone: every built-in kind is routed, reserved for UDTs,
// canonical-checkable when declared, and present in the permissions overview
// library set.
func TestLibraryKinds_RegistryConsistency(t *testing.T) {
	srv := &Server{}
	handlers := srv.libraryRouteHandlers()

	for _, k := range types.BuiltinLibraryKinds {
		if !types.IsReservedUserDefinedID(k.ID) {
			t.Errorf("kind %q not reserved for user-defined types", k.ID)
		}
		if _, ok := handlers[k.Path]; !ok {
			t.Errorf("kind %q has no libraryRouteHandlers entry at %s", k.ID, k.Path)
		}
		if k.CanonicalAllowed && !isCanonicalKindID(k.ID) {
			t.Errorf("kind %q CanonicalAllowed but isCanonicalKindID is false", k.ID)
		}
		if !k.CanonicalAllowed && isCanonicalKindID(k.ID) {
			t.Errorf("kind %q not CanonicalAllowed but isCanonicalKindID is true", k.ID)
		}
		if !types.IsReservedDataTypeKey(k.ID) {
			t.Errorf("kind %q missing reserved data-type key", k.ID)
		}
	}
}

func TestServiceProvidedTypes_IncludesDocuments(t *testing.T) {
	var conn any = &fakeDocumentProvider{}
	got := serviceProvidedTypes(conn)
	found := false
	for _, name := range got {
		if name == "documents" {
			found = true
		}
	}
	if !found {
		t.Errorf("serviceProvidedTypes = %v, want documents", got)
	}
}

func TestLibraryRouteHandlers_UDTCannotShadowBuiltIn(t *testing.T) {
	safeCalled := false
	builtin := map[string]http.HandlerFunc{
		"/v1/library/documents": func(http.ResponseWriter, *http.Request) {},
	}
	udt := map[string]http.HandlerFunc{
		"/v1/library/documents": func(http.ResponseWriter, *http.Request) {
			t.Error("UDT shadow handler must not be installed")
		},
		"/v1/library/safe-udt": func(http.ResponseWriter, *http.Request) {
			safeCalled = true
		},
	}
	skipped := mergeUDTRoutes(builtin, udt, nil)
	if len(skipped) != 1 || skipped[0] != "/v1/library/documents" {
		t.Fatalf("skipped = %v, want [/v1/library/documents]", skipped)
	}
	if builtin["/v1/library/documents"] == nil {
		t.Fatal("built-in documents handler was dropped")
	}
	builtin["/v1/library/documents"](nil, nil) // must be the built-in no-op
	if builtin["/v1/library/safe-udt"] == nil {
		t.Fatal("non-colliding UDT route not registered")
	}
	builtin["/v1/library/safe-udt"](nil, nil)
	if !safeCalled {
		t.Fatal("safe UDT handler not invoked")
	}
}

func TestAppPathAllowed_IncludesDerivedKinds(t *testing.T) {
	for _, k := range types.BuiltinLibraryKinds {
		if !k.AppReadable {
			continue
		}
		if !appPathAllowed(k.Path) {
			t.Errorf("app token cannot read AppReadable kind path %s", k.Path)
		}
	}
	// freebusy is a non-kind app path (events availability).
	if !appPathAllowed("/v1/library/freebusy") {
		t.Error("app token should read freebusy")
	}
	// Document full-text and admin routes stay out of the app scope.
	for _, p := range []string{
		"/v1/library/documents/acme/1/text",
		"/v1/admin/summary",
		"/v1/library/review",
		"/v1/library/links",
	} {
		if appPathAllowed(p) {
			t.Errorf("app token must not access %s", p)
		}
	}
	// User-defined kinds are not app-readable (documented decision).
	if appPathAllowed("/v1/library/reading-list") {
		t.Error("UDT paths must not be app-readable by default")
	}
}

func TestAppPathAllowed_ExactOnly(t *testing.T) {
	if !appPathAllowed("/v1/library/tracks") {
		t.Error("tracks should be allowed")
	}
	if !appPathAllowed("/v1/library/tracks/") {
		t.Error("trailing slash should still match")
	}
	if appPathAllowed("/v1/library/tracks/extra") {
		t.Error("subpaths must not be app-allowed")
	}
}

func TestIsCanonicalKindID_Derived(t *testing.T) {
	for _, k := range types.BuiltinLibraryKinds {
		if got := isCanonicalKindID(k.ID); got != k.CanonicalAllowed {
			t.Errorf("isCanonicalKindID(%q)=%v, want %v", k.ID, got, k.CanonicalAllowed)
		}
	}
	if isCanonicalKindID("not-a-kind") {
		t.Error("unknown id must not be canonical-checkable")
	}
}

func TestUDTSchemaRejectsEveryBuiltinKindID(t *testing.T) {
	// A pack named after any built-in (including documents) is rejected.
	for _, k := range types.BuiltinLibraryKinds {
		s := types.UserDefinedTypeSchema{
			ID:     k.ID,
			Fields: map[string]types.UserDefinedField{"name": {}},
			Identity: types.UserDefinedIdentity{
				Fields: []string{"name"},
			},
		}
		if err := s.Normalize(); err == nil {
			t.Errorf("UDT schema id %q should be rejected", k.ID)
		}
	}
}
