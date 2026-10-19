package types

import (
	"strings"
	"testing"
)

// TestBuiltinLibraryKinds_UniqueIDs keeps registry ids unique and non-empty.
func TestBuiltinLibraryKinds_UniqueIDs(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range BuiltinLibraryKinds {
		if k.ID == "" || k.Path == "" || k.PermType == "" {
			t.Fatalf("kind %+v missing ID/Path/PermType", k)
		}
		if k.ID != k.PermType {
			t.Errorf("kind %q: PermType %q must equal ID", k.ID, k.PermType)
		}
		if k.Path != "/v1/library/"+k.ID {
			t.Errorf("kind %q: Path %q must be /v1/library/%s", k.ID, k.Path, k.ID)
		}
		if seen[k.ID] {
			t.Fatalf("duplicate kind ID %q", k.ID)
		}
		seen[k.ID] = true
	}
}

// TestBuiltinLibraryKinds_Reserved covers the issue #183 bug: every built-in
// id (including documents) must reject a user-defined pack of the same name.
func TestBuiltinLibraryKinds_Reserved(t *testing.T) {
	for _, k := range BuiltinLibraryKinds {
		if !IsReservedUserDefinedID(k.ID) {
			t.Errorf("kind %q is not reserved; a UDT pack could shadow it", k.ID)
		}
		if !IsReservedDataTypeKey(k.ID) {
			t.Errorf("kind %q missing from reserved data-type keys", k.ID)
		}
		if !ReservedDataTypeKeys()[k.ID] {
			t.Errorf("kind %q missing from ReservedDataTypeKeys()", k.ID)
		}
	}
	// Regression for the documents hole (issue #183).
	if !IsReservedUserDefinedID("documents") {
		t.Error("documents must be reserved for user-defined types")
	}
	// Non-kind library path segments stay reserved too.
	for _, id := range []string{"links", "review", "copies", "mirror", "freebusy"} {
		if !IsReservedUserDefinedID(id) {
			t.Errorf("library path id %q must be reserved", id)
		}
	}
	// Proxy-only permission keys that are not library kinds stay reserved.
	for _, id := range []string{"library", "player", "search", "channels", "recommendations", "user"} {
		if !IsReservedDataTypeKey(id) {
			t.Errorf("proxy data-type key %q must be reserved", id)
		}
	}
	if IsReservedUserDefinedID("reading-list") {
		t.Error("unrelated UDT id must not be reserved")
	}
}

func TestUserDefinedTypeSchema_NormalizeRejectsBuiltinIDs(t *testing.T) {
	for _, k := range BuiltinLibraryKinds {
		s := UserDefinedTypeSchema{
			ID:     k.ID,
			Fields: map[string]UserDefinedField{"name": {}},
			Identity: UserDefinedIdentity{
				Fields: []string{"name"},
			},
		}
		if err := s.Normalize(); err == nil {
			t.Errorf("Normalize(%q) succeeded; pack must not claim a built-in id", k.ID)
		}
	}
}

// TestBuiltinLibraryKinds_PolicyFlags documents the app/canonical/level
// decisions for issue #183 (and #131 app tokens).
func TestBuiltinLibraryKinds_PolicyFlags(t *testing.T) {
	// App tokens are data readers of built-in library lists. documents is
	// included (README already promised it; code had drifted). User-defined
	// kinds are never app-readable by default.
	for _, k := range BuiltinLibraryKinds {
		if !k.AppReadable {
			t.Errorf("kind %q should be AppReadable (third-party list read)", k.ID)
		}
		if !k.CanonicalAllowed {
			t.Errorf("kind %q should be CanonicalAllowed", k.ID)
		}
	}
	// Type-scoped redaction levels (issue #183).
	cases := []struct {
		id     string
		levels []string
		none   bool
	}{
		{"events", []string{AccessFreeBusy}, false},
		{"transactions", []string{AccessSummary}, false},
		{"places", []string{AccessCity, AccessPrecise}, true},
		{"documents", []string{AccessMetadata}, false},
		{"tracks", nil, false},
	}
	for _, tc := range cases {
		k := BuiltinKindByID(tc.id)
		if k == nil {
			t.Fatalf("missing kind %q", tc.id)
		}
		if strings.Join(k.AllowedLevels, ",") != strings.Join(tc.levels, ",") {
			t.Errorf("%s.AllowedLevels = %v, want %v", tc.id, k.AllowedLevels, tc.levels)
		}
		if k.DefaultNone != tc.none {
			t.Errorf("%s.DefaultNone = %v, want %v", tc.id, k.DefaultNone, tc.none)
		}
	}

	// Derived allowlists match AllowedLevels.
	if !KindAllowsLevel("events", AccessFreeBusy) {
		t.Error("events should allow freebusy")
	}
	if KindAllowsLevel("tracks", AccessSummary) {
		t.Error("tracks must not allow summary")
	}
	if !KindDefaultsNone("places") {
		t.Error("places should default to none")
	}
	if KindDefaultsNone("documents") {
		t.Error("documents should not default to none")
	}
	if got := KindsAllowingLevel(AccessMetadata); strings.Join(got, ",") != "documents" {
		t.Errorf("KindsAllowingLevel(metadata) = %v, want [documents]", got)
	}

	// ValidatePermissions rejects levels on kinds that cannot honor them.
	err := ValidatePermissions(Permissions{"tracks": AccessSummary})
	if err == nil {
		t.Error("summary on tracks should fail validation")
	}
	err = ValidatePermissions(Permissions{"documents": AccessMetadata})
	if err != nil {
		t.Errorf("metadata on documents should pass: %v", err)
	}
	err = ValidatePermissions(Permissions{"places": AccessCity})
	if err != nil {
		t.Errorf("city on places should pass: %v", err)
	}
}

// TestBuiltinLibraryKinds_DerivedLists checks the hand-maintained copies are
// gone in favor of registry-derived helpers (issue #183).
func TestBuiltinLibraryKinds_DerivedLists(t *testing.T) {
	ids := BuiltinKindIDs()
	if len(ids) != len(BuiltinLibraryKinds) {
		t.Fatalf("BuiltinKindIDs len %d != registry %d", len(ids), len(BuiltinLibraryKinds))
	}
	paths := BuiltinKindPaths()
	if len(paths) != len(BuiltinLibraryKinds) {
		t.Fatalf("BuiltinKindPaths len %d != registry %d", len(paths), len(BuiltinLibraryKinds))
	}
	for _, id := range CanonicalKindIDs() {
		if !IsCanonicalKindID(id) {
			t.Errorf("CanonicalKindIDs has %q but IsCanonicalKindID is false", id)
		}
	}
	// App paths include every AppReadable kind and nothing else from the registry.
	app := map[string]bool{}
	for _, p := range AppReadableKindPaths() {
		app[p] = true
	}
	for _, k := range BuiltinLibraryKinds {
		if k.AppReadable && !app[k.Path] {
			t.Errorf("AppReadable kind %q path missing from AppReadableKindPaths", k.ID)
		}
		if !k.AppReadable && app[k.Path] {
			t.Errorf("non-AppReadable kind %q path in AppReadableKindPaths", k.ID)
		}
	}
}

// TestDataTypePathRules_UseReservedKeys keeps the proxy classifier aligned
// with reserved data-type keys (issue #183).
func TestDataTypePathRules_UseReservedKeys(t *testing.T) {
	for _, rule := range dataTypePathRules {
		if rule.DataType == "" {
			t.Fatalf("path rule %+v has empty DataType", rule)
		}
		if !IsReservedDataTypeKey(rule.DataType) {
			t.Errorf("path rule DataType %q is not a reserved data-type key", rule.DataType)
		}
	}
}
