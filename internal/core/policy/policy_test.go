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

func TestValidateGrantMap_RedactionTokensRetired(t *testing.T) {
	// Narrowed disclosure is its own route (issue #348) — the redaction
	// level tokens reject everywhere.
	for _, tok := range []string{"freebusy", "summary", "city", "precise", "metadata"} {
		if err := ValidateGrantMap(GrantMap{"events": tok}); err == nil {
			t.Fatalf("retired token %q must reject", tok)
		}
	}
}

func TestValidateGrantMap_Levels(t *testing.T) {
	for _, tok := range []string{"none", "read", "write", "read-write"} {
		if err := ValidateGrantMap(GrantMap{"places": tok}); err != nil {
			t.Fatalf("valid token %q: %v", tok, err)
		}
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
