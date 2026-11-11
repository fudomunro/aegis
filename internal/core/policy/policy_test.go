package policy

import (
	"testing"

	"github.com/fudomunro/aegis/internal/shared"
)

func TestValidateGrantMap_RedactionTokensRetired(t *testing.T) {
	// Narrowed disclosure is its own route (issue #348) — the redaction
	// level tokens reject everywhere.
	for _, tok := range []string{"freebusy", "summary", "city", "precise", "metadata"} {
		if err := ValidateGrantMap(shared.GrantMap{"events": tok}); err == nil {
			t.Fatalf("retired token %q must reject", tok)
		}
	}
}

func TestValidateGrantMap_Levels(t *testing.T) {
	for _, tok := range []string{"none", "read", "write", "read-write"} {
		if err := ValidateGrantMap(shared.GrantMap{"places": tok}); err != nil {
			t.Fatalf("valid token %q: %v", tok, err)
		}
	}
	if err := ValidateGrantMap(shared.GrantMap{"": shared.AccessRead}); err == nil {
		t.Fatal("empty key")
	}
	if err := ValidateGrantMap(shared.GrantMap{"tracks": "maybe"}); err == nil {
		t.Fatal("unknown level")
	}
}
