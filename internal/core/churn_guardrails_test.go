package core

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Churn guardrails. Each test here pins a decision that was made, reversed,
// and re-decided at least once — so the next session cannot quietly undo it.
//
//  1. Package import boundaries (#15 / #280 / #301 / #303 / #325 / #358):
//     `internal/shared` is the single owner of stdlib-only plumbing AND
//     cross-package seam vocabulary — the shapes two internal packages
//     exchange to agree on a contract (capabilities, onboarding, mail
//     ingest, ownership, grant maps, data-scope views, search documents)
//     plus trivial pure constructors/accessors/predicates over them
//     (kindrules.go is the worked example). No state, no I/O, no
//     configuration resolution, no service registries, no provider paths,
//     no Host. `internal/kinds` imports `internal/shared` only;
//     authorization vocabulary is shared, stored grant resolution is
//     `internal/core/policy`; and connectors depend on `Host` — not the
//     core hub. Surface packages follow audiences (#358): `internal/library`
//     is the agent data plane, `internal/admin` the management surface,
//     `internal/syncjobs` background consistency — see
//     docs/restructure-packaging.md for the frozen map.
//  2. Outbound HTTP has one owner (`shared.HTTPClient`); `http.DefaultClient`
//     and the `http.Get`/`http.Post`/`http.PostForm` helpers have no timeout.

// allowedImports maps a repo-relative package directory to the
// fudomunro/aegis packages it may import. Anything else is a boundary
// violation. Empty means "no fudomunro/aegis imports at all".
var allowedImports = map[string][]string{
	"internal/shared":      nil,
	"internal/core/policy": {"github.com/fudomunro/aegis/internal/shared"},
	"internal/core/syncjobs": {
		"github.com/fudomunro/aegis/internal/shared",
		"github.com/fudomunro/aegis/internal/kinds",
	},
	// metrics consumes the sync rollup shape (SyncCounts) to render the
	// queue-depth gauges, so it may name syncjobs; audit supplies the
	// allow/deny decision vocabulary. Stated reasons, per the package rule.
	"internal/core/metrics": {
		"github.com/fudomunro/aegis/internal/core/audit",
		"github.com/fudomunro/aegis/internal/core/syncjobs",
	},
	// sample datasets are fixture generators plus the sample://library
	// connector; they speak neutral models and the mail store only.
	"internal/core/sampledata": {
		"github.com/fudomunro/aegis/internal/connectors",
		"github.com/fudomunro/aegis/internal/core/mail",
		"github.com/fudomunro/aegis/internal/kinds",
		"github.com/fudomunro/aegis/internal/shared",
	},
	// the UDK registry loads schema packs and applies the admin overlay;
	// kind queries go through kinds, access defaults through the shared
	// vocabulary.
	"internal/core/udk": {
		"github.com/fudomunro/aegis/internal/kinds",
		"github.com/fudomunro/aegis/internal/shared",
	},
	// the hook-health engine records hook traffic and validates pasted
	// Aperture config; request body-shape tokens come from shared seam
	// vocabulary (the inject engine produces the same tokens).
	"internal/core/hookhealth": {
		"github.com/fudomunro/aegis/internal/shared",
	},
	// the injection engine is pure JSON surgery over LLM request bodies;
	// request body-shape tokens come from shared seam vocabulary.
	"internal/core/inject": {
		"github.com/fudomunro/aegis/internal/shared",
	},
	"internal/kinds": {
		"github.com/fudomunro/aegis/internal/shared",
	},
	"internal/connectors": {
		"github.com/fudomunro/aegis/internal/shared",
		"github.com/fudomunro/aegis/internal/kinds",
	},
	// the agent data plane: fixed library routes, content transport, kind
	// composition, and agent-facing copy. Stores it drives are constructor
	// deps; the kinds.Host seam covers request identity/authorization.
	"internal/library": {
		"github.com/fudomunro/aegis/internal/shared",
		"github.com/fudomunro/aegis/internal/kinds",
		"github.com/fudomunro/aegis/internal/connectors",
		"github.com/fudomunro/aegis/internal/core/copies",
		"github.com/fudomunro/aegis/internal/core/udk",
		"github.com/fudomunro/aegis/internal/library/links",
	},
	// user-confirmed entity links (issue #4) — library-owned store.
	"internal/library/links": {
		"github.com/fudomunro/aegis/internal/shared",
		"github.com/fudomunro/aegis/internal/kinds",
	},
	// the local full-text index behind kind search (issue #178).
	"internal/library/search": {
		"github.com/fudomunro/aegis/internal/shared",
	},
	// the management surface: admin API and UI, setup flows, admin-owned
	// writes. It reads the data plane's kind catalog through a narrow
	// interface the hub wires — the surfaces never import each other.
	"internal/admin": {
		"github.com/fudomunro/aegis/internal/shared",
		"github.com/fudomunro/aegis/internal/kinds",
		"github.com/fudomunro/aegis/internal/connectors",
		"github.com/fudomunro/aegis/internal/core/copies",
		"github.com/fudomunro/aegis/internal/core/hookhealth",
		"github.com/fudomunro/aegis/internal/core/mail",
		"github.com/fudomunro/aegis/internal/core/metrics",
		"github.com/fudomunro/aegis/internal/core/ownership",
		"github.com/fudomunro/aegis/internal/core/policy",
		"github.com/fudomunro/aegis/internal/core/syncjobs",
		"github.com/fudomunro/aegis/internal/core/tokens",
		"github.com/fudomunro/aegis/internal/core/udk",
		"github.com/fudomunro/aegis/internal/admin/apps",
		"github.com/fudomunro/aegis/internal/admin/scopes",
	},
	// the third-party app registry (issue #131): mint/verify/consent.
	"internal/admin/apps": {
		"github.com/fudomunro/aegis/internal/shared",
		"github.com/fudomunro/aegis/internal/kinds",
		"github.com/fudomunro/aegis/internal/core/policy",
	},
	// the data-scope store (invite tokens, dataset partitions).
	"internal/admin/scopes": {
		"github.com/fudomunro/aegis/internal/shared",
		"github.com/fudomunro/aegis/internal/core/policy",
	},
}

// coreHubImport is the wiring package (*Server, routes, Host
// implementations). No extracted store/engine or surface package may depend
// on it; that is the dependency direction of AGENTS.md "Package boundaries".
const coreHubImport = "github.com/fudomunro/aegis/internal/core"

func packageImportDecls(t *testing.T, dir string) map[string][]string {
	t.Helper()
	fset := token.NewFileSet()
	out := map[string][]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", filepath.Join(dir, name), err)
		}
		var imps []string
		for _, spec := range f.Imports {
			path, uerr := strconv.Unquote(spec.Path.Value)
			if uerr != nil {
				continue
			}
			if strings.HasPrefix(path, "github.com/fudomunro/aegis") {
				imps = append(imps, path)
			}
		}
		out[name] = imps
	}
	return out
}

func TestPackageImports_RespectLayerBoundaries(t *testing.T) {
	root := repoRoot(t)
	var failures []string

	for dir, allowed := range allowedImports {
		for file, imps := range packageImportDecls(t, filepath.Join(root, filepath.FromSlash(dir))) {
			for _, imp := range imps {
				ok := false
				for _, a := range allowed {
					if imp == a {
						ok = true
						break
					}
				}
				if !ok {
					failures = append(failures, dir+"/"+file+" imports "+imp)
				}
			}
		}
	}

	// Extracted core stores/engines never depend on the hub. Iterate the
	// sub-packages by name so a new sub-package cannot dodge the check.
	coreDir := filepath.Join(root, "internal", "core")
	entries, err := os.ReadDir(coreDir)
	if err != nil {
		t.Fatalf("read %s: %v", coreDir, err)
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "web" {
			continue // web/ is embedded markup, not a Go package boundary
		}
		sub := "internal/core/" + e.Name()
		for file, imps := range packageImportDecls(t, filepath.Join(coreDir, e.Name())) {
			for _, imp := range imps {
				if imp == coreHubImport {
					failures = append(failures, sub+"/"+file+" imports the core hub")
				}
				if allowed, known := allowedImports[sub]; known {
					ok := false
					for _, a := range allowed {
						if imp == a {
							ok = true
							break
						}
					}
					if !ok {
						failures = append(failures, sub+"/"+file+" imports "+imp)
					}
				}
			}
		}
	}

	if len(failures) > 0 {
		t.Fatalf("package layer boundaries (AGENTS.md 'Package boundaries') violated:\n%s",
			strings.Join(failures, "\n"))
	}
}

// unsafeHTTPCalls are the net/http helpers that use http.DefaultClient
// (no timeout) or read/write without a context. Production code goes through
// shared.HTTPClient or a documented specialized client instead.
var unsafeHTTPCalls = map[string]bool{
	"DefaultClient": true,
	"Get":           true,
	"Post":          true,
	"PostForm":      true,
}

func TestOutboundHTTP_SharedClientOwner(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	var failures []string

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if name == ".git" || name == "testdata" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			failures = append(failures, rel+": parse error: "+perr.Error())
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkgIdent, ok := sel.X.(*ast.Ident)
			if !ok || pkgIdent.Name != "http" || !unsafeHTTPCalls[sel.Sel.Name] {
				return true
			}
			pos := fset.Position(sel.Pos())
			failures = append(failures,
				rel+":"+strconv.Itoa(pos.Line)+": http."+sel.Sel.Name+
					" — use shared.HTTPClient (or a documented specialized client) instead")
			return true
		})
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk %s: %v", root, walkErr)
	}
	if len(failures) > 0 {
		t.Fatalf("outbound HTTP must have one owner (shared.HTTPClient + context):\n%s",
			strings.Join(failures, "\n"))
	}
}
