package core

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/kinds"
)

// Kind literals (AGENTS.md "Non-negotiable product rules"): the hub never
// names a built-in kind. Kinds declare their routes, filters, redaction,
// and stamping beside their data — per-kind branches in core are what the
// internal/kinds campaign (issue #312) removed. This scan covers exact
// string literals in non-test hub sources; comments and identifiers may
// name kinds (they document, they do not own).
//
// The per-file allowlist is the documented remainder (writes, ownership
// derivation, sync runners, fixtures, and keys that merely collide with
// kind names). It may only shrink: new hub files must not name kinds, and
// an allowlisted file loses its entry when its last kind literal moves.

var kindLiteralAllowances = []struct{ file, why string }{
	{"copy_ledger.go", "photo-share copy rows are stamped in the share write path"},
	{"entity_links_handlers.go", "cross-kind entity-link review fans out over the music kinds"},
	{"handler.go", "pre-request payload key names chat messages, not the kind"},
	{"hookhealth.go", "hook-health JSON shape key names hook events, not the kind"},
	{"import_admin.go", "local-import admin counters report per-kind import stats"},
	{"mail_archive_http.go", "the /v1/mail surface shares the messages grant key"},
	{"mirror.go", "mirror write adapters are kind-specific writes"},
	{"mirror_entity.go", "mirror write adapters are kind-specific writes"},
	{"ownership.go", "ownership derivation maps kind evidence rows"},
	{"permissions_overview.go", "admin permissions overview renders per-kind grant rows"},
	{"photos_share.go", "photo-share write adapter"},
	{"sample_library.go", "sample fixture dataset keys"},
	{"sampledata.go", "per-kind sample fixtures"},
	{"syncfreshness_runner.go", "freshness runners name the kinds they track"},
	{"syncrunner_canary.go", "canary sync runner is the tracks kind's check"},
	{"syncrunner_mail.go", "mail sync runner is the messages kind's ingest"},
}

func kindLiteralAllowed(file string) (string, bool) {
	for _, a := range kindLiteralAllowances {
		if a.file == file {
			return a.why, true
		}
	}
	return "", false
}

func TestKindLiterals_HubNeverNamesAKind(t *testing.T) {
	kindIDs := map[string]bool{}
	for _, k := range kinds.BuiltinLibraryKinds {
		kindIDs[k.ID] = true
	}
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
		if _, ok := kindLiteralAllowed(name); ok {
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
			if kindIDs[val] {
				failures = append(failures,
					name+": kind literal "+lit.Value+" — kinds declare their own behavior "+
						"(issue #312); extend kindLiteralAllowances only with a documented reason")
			}
			return true
		})
	}
	if len(failures) > 0 {
		t.Fatalf("core must not name a built-in kind (AGENTS.md non-negotiables):\n%s",
			strings.Join(failures, "\n"))
	}
}
