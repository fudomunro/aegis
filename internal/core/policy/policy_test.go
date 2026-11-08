package policy

import (
	"net/http"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

func TestPermissions_AllowsAndClone(t *testing.T) {
	p := Permissions{"tracks": AccessRead, "playlists": AccessNone, "default": AccessWrite}
	if !p.AllowsRead("tracks") || p.AllowsWrite("tracks") {
		t.Fatalf("tracks read-only: %+v", p)
	}
	if p.AllowsRead("playlists") || p.AllowsWrite("playlists") {
		t.Fatalf("playlists none: %+v", p)
	}
	if !p.AllowsWrite("search") {
		t.Fatalf("default write should allow search: %+v", p)
	}
	if !p.Allows("tracks", http.MethodGet) || p.Allows("tracks", http.MethodPost) {
		t.Fatalf("Allows method: %+v", p)
	}
	c := p.Clone()
	if c["tracks"] != AccessRead || len(c) != 3 {
		t.Fatalf("clone = %+v", c)
	}
	c["tracks"] = AccessNone
	if p["tracks"] != AccessRead {
		t.Fatal("clone must be deep enough to not alias levels")
	}
	if got := p.DeniedDataTypes(); len(got) != 1 || got[0] != "playlists" {
		t.Fatalf("DeniedDataTypes = %v", got)
	}
}

func TestPermissions_RedactionLists(t *testing.T) {
	p := Permissions{
		"events":       AccessFreeBusy,
		"transactions": AccessSummary,
		"places":       AccessCity,
		"tracks":       AccessRead,
	}
	if got := p.FreeBusyDataTypes(); len(got) != 1 || got[0] != "events" {
		t.Fatalf("FreeBusyDataTypes = %v", got)
	}
	if got := p.SummaryDataTypes(); len(got) != 1 || got[0] != "transactions" {
		t.Fatalf("SummaryDataTypes = %v", got)
	}
	if got := p.PlacesAccessLevels(); got["places"] != AccessCity {
		t.Fatalf("PlacesAccessLevels = %v", got)
	}
	if !IsFreeBusyAccess(AccessFreeBusy) || IsFreeBusyAccess(AccessRead) {
		t.Fatal("IsFreeBusyAccess")
	}
	if !IsSummaryAccess(AccessSummary) || IsSummaryAccess(AccessRead) {
		t.Fatal("IsSummaryAccess")
	}
	if got := PlacesLevelForResponse(AccessCity); got != AccessCity {
		t.Fatalf("PlacesLevelForResponse city = %q", got)
	}
}

func TestPermissions_AccessForDataType(t *testing.T) {
	p := Permissions{"tracks": AccessNone, "default": AccessRead}
	if got := p.AccessForDataType("tracks"); got != AccessNone {
		t.Fatalf("classified tracks = %q", got)
	}
	// Unclassified uses Strictest (none), never default.
	if got := p.AccessForDataType(""); got != AccessNone {
		t.Fatalf("unclassified = %q, want strictest none", got)
	}
}

func TestValidatePermissions_RedactionScoping(t *testing.T) {
	if err := ValidatePermissions(Permissions{"events": AccessFreeBusy}); err != nil {
		t.Fatalf("events freebusy: %v", err)
	}
	if err := ValidatePermissions(Permissions{"tracks": AccessFreeBusy}); err == nil {
		t.Fatal("freebusy must reject non-events")
	}
	if err := ValidatePermissions(Permissions{"places": AccessCity}); err != nil {
		t.Fatalf("places city: %v", err)
	}
	if err := ValidatePermissions(Permissions{"tracks": AccessCity}); err == nil {
		t.Fatal("city must reject non-places")
	}
	if err := ValidatePermissions(Permissions{"default": AccessFreeBusy}); err == nil {
		t.Fatal("default freebusy must reject")
	}
}

func TestValidatePermissions_KindLevels(t *testing.T) {
	if err := ValidatePermissions(Permissions{"places": AccessPrecise}); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePermissions(Permissions{"places": AccessSummary}); err == nil {
		t.Fatal("summary on places")
	}
	if err := ValidatePermissions(Permissions{"transactions": AccessSummary}); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePermissions(Permissions{"transactions": AccessCity}); err == nil {
		t.Fatal("city on transactions")
	}
	if err := ValidatePermissions(Permissions{"documents": AccessMetadata}); err != nil {
		t.Errorf("metadata on documents should pass: %v", err)
	}
	if err := ValidatePermissions(Permissions{"tracks": AccessSummary}); err == nil {
		t.Error("summary on tracks should fail validation")
	}
	if err := ValidatePermissions(Permissions{"": AccessRead}); err == nil {
		t.Fatal("empty key")
	}
	if err := ValidatePermissions(Permissions{"tracks": "maybe"}); err == nil {
		t.Fatal("unknown level")
	}
}

// TestKindLevelSeam_AllowedLevelsArePolicyTokens pins the seam: kinds in
// internal/types declare AllowedLevels as bare tokens; each must be a
// canonical policy level or validation would silently reject it.
func TestKindLevelSeam_AllowedLevelsArePolicyTokens(t *testing.T) {
	known := map[string]bool{
		AccessFreeBusy: true, AccessSummary: true,
		AccessCity: true, AccessPrecise: true, AccessMetadata: true,
	}
	for _, k := range types.BuiltinLibraryKinds {
		for _, l := range k.AllowedLevels {
			if NormalizeAccess(l) != l || !known[l] {
				t.Errorf("kind %q AllowedLevels entry %q is not a canonical policy level", k.ID, l)
			}
		}
	}
}

func TestAllowsMethod_Levels(t *testing.T) {
	if !AllowsMethod(AccessRead, http.MethodGet) || AllowsMethod(AccessRead, http.MethodPost) {
		t.Fatal("read")
	}
	if !AllowsMethod(AccessWrite, http.MethodPost) || AllowsMethod(AccessWrite, http.MethodGet) {
		t.Fatal("write")
	}
	if !AllowsMethod(AccessFull, http.MethodDelete) || !AllowsMethod(AccessFull, http.MethodGet) {
		t.Fatal("full")
	}
	if AllowsMethod(AccessNone, http.MethodGet) {
		t.Fatal("none must deny")
	}
}

func TestNormalizeAccessAndDataGrant(t *testing.T) {
	if NormalizeAccess("READ") != AccessRead {
		t.Fatal("NormalizeAccess READ")
	}
	if NormalizeAccess("") != "" {
		t.Fatal("empty stays empty")
	}
	tp := DataGrant{Default: AccessRead, Providers: map[string]string{"spotify": AccessNone}}
	if tp.For("spotify") != AccessNone || tp.For("other") != AccessRead {
		t.Fatalf("DataGrant.For = %+v", tp)
	}
	if (DataGrants{"a": {Default: AccessWrite}}).For("a").Default != AccessWrite {
		t.Fatal("DataGrants.For")
	}
}

func TestStrictestAndForDefaults(t *testing.T) {
	var empty Permissions
	if empty.Strictest() != DefaultAccessType {
		t.Fatalf("empty strictest = %q", empty.Strictest())
	}
	if empty.For("tracks") != DefaultAccessType {
		t.Fatalf("empty For = %q", empty.For("tracks"))
	}
	// Mixed read/write on unknown path must deny.
	mixed := Permissions{"tracks": AccessRead, "playlists": AccessWrite}
	if mixed.Strictest() != AccessNone {
		t.Fatalf("mixed strictest = %q", mixed.Strictest())
	}
	// Single tight level wins.
	if (Permissions{"tracks": AccessNone}).Strictest() != AccessNone {
		t.Fatal("none strictest")
	}
	if (Permissions{"tracks": AccessRead}).Strictest() != AccessRead {
		t.Fatal("read strictest")
	}
	// opt-in places: default key never enables.
	p := Permissions{PermissionDefaultKey: AccessFull}
	if p.For("places") != AccessNone {
		t.Fatalf("places default = %q", p.For("places"))
	}
	p2 := Permissions{"places": AccessCity}
	if p2.For("places") != AccessCity {
		t.Fatalf("places named = %q", p2.For("places"))
	}
}

func TestPlacesLevelForResponse_Levels(t *testing.T) {
	if PlacesLevelForResponse(AccessPrecise) != AccessPrecise {
		t.Fatal("precise keeps precise")
	}
	if PlacesLevelForResponse(AccessRead) != AccessRead {
		t.Fatal("read keeps names")
	}
	if PlacesLevelForResponse(AccessFull) != AccessPrecise {
		t.Fatal("full is precise for places")
	}
	if PlacesLevelForResponse(AccessCity) != AccessCity {
		t.Fatal("city")
	}
}
