package policy

import (
	"net/http"
	"testing"
)

func TestPermissions_AllowsAndClone(t *testing.T) {
	p := GrantMap{"tracks": AccessRead, "playlists": AccessNone, "default": AccessWrite}
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
	if got := p.DeniedKinds(); len(got) != 1 || got[0] != "playlists" {
		t.Fatalf("DeniedKinds = %v", got)
	}
}

func TestPermissions_RedactionLists(t *testing.T) {
	p := GrantMap{
		"transactions": AccessSummary,
		"places":       AccessCity,
		"tracks":       AccessRead,
	}
	if got := p.SummaryKinds(); len(got) != 1 || got[0] != "transactions" {
		t.Fatalf("SummaryKinds = %v", got)
	}
	if got := p.PlacesAccessLevels(); got["places"] != AccessCity {
		t.Fatalf("PlacesAccessLevels = %v", got)
	}
	if !IsSummaryAccess(AccessSummary) || IsSummaryAccess(AccessRead) {
		t.Fatal("IsSummaryAccess")
	}
	if got := PlacesLevelForResponse(AccessCity); got != AccessCity {
		t.Fatalf("PlacesLevelForResponse city = %q", got)
	}
}

func TestValidateGrantMap_RedactionScoping(t *testing.T) {
	// The freebusy level is retired — availability access is the freebusy
	// route (issue #348). The token rejects everywhere.
	if err := ValidateGrantMap(GrantMap{"events": "freebusy"}); err == nil {
		t.Fatal("retired freebusy token must reject")
	}
	if err := ValidateGrantMap(GrantMap{"tracks": "freebusy"}); err == nil {
		t.Fatal("retired freebusy token must reject")
	}
	if err := ValidateGrantMap(GrantMap{"places": AccessCity}); err != nil {
		t.Fatalf("places city: %v", err)
	}
	if err := ValidateGrantMap(GrantMap{"tracks": AccessCity}); err == nil {
		t.Fatal("city must reject non-places")
	}
}

func TestValidateGrantMap_KindLevels(t *testing.T) {
	if err := ValidateGrantMap(GrantMap{"places": AccessPrecise}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateGrantMap(GrantMap{"places": AccessSummary}); err == nil {
		t.Fatal("summary on places")
	}
	if err := ValidateGrantMap(GrantMap{"transactions": AccessSummary}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateGrantMap(GrantMap{"transactions": AccessCity}); err == nil {
		t.Fatal("city on transactions")
	}
	if err := ValidateGrantMap(GrantMap{"documents": AccessMetadata}); err != nil {
		t.Errorf("metadata on documents should pass: %v", err)
	}
	if err := ValidateGrantMap(GrantMap{"tracks": AccessSummary}); err == nil {
		t.Error("summary on tracks should fail validation")
	}
	if err := ValidateGrantMap(GrantMap{"": AccessRead}); err == nil {
		t.Fatal("empty key")
	}
	if err := ValidateGrantMap(GrantMap{"tracks": "maybe"}); err == nil {
		t.Fatal("unknown level")
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

func TestNormalizeAccess(t *testing.T) {
	if NormalizeAccess("READ") != AccessRead {
		t.Fatal("NormalizeAccess READ")
	}
	if NormalizeAccess("") != "" {
		t.Fatal("empty stays empty")
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
