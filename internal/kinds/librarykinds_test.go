package kinds

import (
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/core/policy"

	"github.com/fudomunro/aegis/internal/shared"
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
			t.Errorf("kind %q is not reserved; a UDK pack could shadow it", k.ID)
		}
		if !IsReservedKindKey(k.ID) {
			t.Errorf("kind %q missing from reserved kind keys", k.ID)
		}
		if !ReservedKindKeys()[k.ID] {
			t.Errorf("kind %q missing from ReservedKindKeys()", k.ID)
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
		if !IsReservedKindKey(id) {
			t.Errorf("proxy kind key %q must be reserved", id)
		}
	}
	if IsReservedUserDefinedID("reading-list") {
		t.Error("unrelated UDK id must not be reserved")
	}
}

func TestUserDefinedKindSchema_NormalizeRejectsBuiltinIDs(t *testing.T) {
	for _, k := range BuiltinLibraryKinds {
		s := UserDefinedKindSchema{
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
		{"events", []string{"freebusy"}, false},
		{"transactions", []string{"summary"}, false},
		{"places", []string{"city", "precise"}, true},
		{"documents", []string{"metadata"}, false},
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
	if !shared.KindAllowsLevel("events", "freebusy") {
		t.Error("events should allow freebusy")
	}
	if shared.KindAllowsLevel("tracks", "summary") {
		t.Error("tracks must not allow summary")
	}
	if !shared.KindDefaultsNone("places") {
		t.Error("places should default to none")
	}
	if shared.KindDefaultsNone("documents") {
		t.Error("documents should not default to none")
	}
	if got := shared.KindsAllowingLevel("metadata"); strings.Join(got, ",") != "documents" {
		t.Errorf("KindsAllowingLevel(metadata) = %v, want [documents]", got)
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

// TestKindLevelSeam_AllowedLevelsArePolicyTokens pins the seam: kinds declare AllowedLevels as bare tokens; each must be a
// canonical policy level or validation would silently reject it.
func TestKindLevelSeam_AllowedLevelsArePolicyTokens(t *testing.T) {
	known := map[string]bool{
		policy.AccessFreeBusy: true, policy.AccessSummary: true,
		policy.AccessCity: true, policy.AccessPrecise: true, policy.AccessMetadata: true,
	}
	for _, k := range BuiltinLibraryKinds {
		for _, l := range k.AllowedLevels {
			if policy.NormalizeAccess(l) != l || !known[l] {
				t.Errorf("kind %q AllowedLevels entry %q is not a canonical policy level", k.ID, l)
			}
		}
	}
}
