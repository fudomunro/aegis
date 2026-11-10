// External test package so this binary can import internal/kinds without an
// import cycle: kinds registers the built-in kind rules into the shared kind-
// rule registry from its package init, and internal/core/policy resolves
// those rules as a leaf (issue #325). Internal tests in this package see the
// registered rules once this import pulls the package into the test binary.
package policy_test

import (
	"testing"

	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

func TestBuiltinKindRulesRegistered(t *testing.T) {
	if len(kinds.BuiltinLibraryKinds) == 0 {
		t.Fatal("builtin kind registry empty")
	}
	// The places pattern: hidden until a permission map / data grant names it.
	if !shared.KindDefaultsNone("places") {
		t.Fatal("places must register as default-none")
	}
	// The freebusy level is retired — availability access is the freebusy
	// route (issue #348).
	if shared.KindAllowsLevel("events", "freebusy") {
		t.Fatal("events must not declare the retired freebusy level")
	}
}
