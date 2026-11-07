package core

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// Provider literals (issue #286; docs/mail-ingest.md): the Gmail+Takeout
// ingest pair lives in internal/connectors. Core references the exported
// constants and never re-states provider strings — service ids, display
// names, or semantics — in its own string literals. Comments and
// identifiers may name providers (they document, they do not own); this
// scan covers string literals in non-test core sources only.
//
// The pattern is docs_hygiene_test.go / the onboarding-copy hygiene: one
// owner per provider contract, with explicit documented allowances below
// for stored-data keys and CLI/API surface copy that legitimately names
// the provider it is the surface of.

var mailProviderLiterals = []string{
	"google-takeout",
	"google-gmail",
	"Google Takeout",
	"Quota exceeded",
	"X-GM-LABELS",
	"in:anywhere",
	"TAKEOUT",
	"Gmail",
}

var mailLiteralAllowances = []struct{ file, want, why string }{
	{"copy_ledger.go", "snapshot:google-takeout:",
		"stored copy-ledger entity-key prefix; renaming it orphans existing rows"},
	{"run.go", "Google Takeout",
		"CLI flag help for -import-takeout names the provider it imports (like -import-quicken)"},
	{"sampledata.go", "TAKEOUT",
		"demo fixture labels mirror the provider's export shape (data values, not semantics)"},
	{"sampledata.go", "X-GM-LABELS",
		"demo fixture generation writes provider-format mbox exports"},
}

func mailLiteralAllowed(file, lit string) (string, bool) {
	for _, a := range mailLiteralAllowances {
		if a.file == file && strings.Contains(lit, a.want) {
			return a.why, true
		}
	}
	return "", false
}

func TestMailProviderLiterals_SingleOwner(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var failures []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			val, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			for _, bad := range mailProviderLiterals {
				if !strings.Contains(val, bad) {
					continue
				}
				if _, ok := mailLiteralAllowed(name, val); ok {
					continue
				}
				failures = append(failures,
					name+": provider literal "+strconv.Quote(bad)+" in "+lit.Value)
			}
			return true
		})
	}
	if len(failures) > 0 {
		t.Fatalf("core must not re-state provider strings (issue #286) — "+
			"reference the connectors constants or extend mailLiteralAllowances "+
			"with a reason:\n%s", strings.Join(failures, "\n"))
	}
}
