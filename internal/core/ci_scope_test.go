package core

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// CI lists fuzz targets and per-group watch paths by hand (issue #355). This
// test keeps those lists honest: each group's targets must match the Fuzz
// functions that exist, and each group's watch paths must cover the code those
// targets exercise — their own package and every internal package the Fuzz
// bodies call into. Otherwise a scoped run can silently skip a parser that
// just changed.

type ciFuzzGroup struct {
	listKey  string // bash array of target names in ci.yml
	watchKey string // bash '|'-separated path patterns in ci.yml
	pkgDir   string // package holding the Fuzz functions
}

var ciFuzzGroups = []ciFuzzGroup{
	{"inject_targets", "fuzz_inject_watch", filepath.Join("internal", "core", "inject")},
	{"core_targets", "fuzz_core_watch", filepath.Join("internal", "core")},
	{"connectors_targets", "fuzz_connectors_watch", filepath.Join("internal", "connectors")},
}

const aegisModule = "github.com/fudomunro/aegis/"

// ciWatchCovers mirrors the scope job's path match: a pattern ending in '*'
// matches everything under that prefix ('*' crosses '/'), anything else is an
// exact path.
func ciWatchCovers(patterns []string, path string) bool {
	for _, pat := range patterns {
		if strings.HasSuffix(pat, "*") {
			if strings.HasPrefix(path, strings.TrimSuffix(pat, "*")) {
				return true
			}
			continue
		}
		if pat == path {
			return true
		}
	}
	return false
}

// ciFuzzScan collects the Fuzz functions in a package directory (one level),
// the test files that declare them, and the module-internal packages those
// function bodies reference by qualified name.
func ciFuzzScan(t *testing.T, root, pkgDir string) (targets map[string]bool, files []string, refs map[string]string) {
	t.Helper()
	targets = map[string]bool{}
	refs = map[string]string{}

	entries, err := os.ReadDir(filepath.Join(root, pkgDir))
	if err != nil {
		t.Fatalf("read %s: %v", pkgDir, err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, "_test.go") {
			continue
		}
		rel := filepath.Join(pkgDir, name)
		src, err := parser.ParseFile(fset, filepath.Join(root, pkgDir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", rel, err)
		}

		// Import name -> module-relative directory, for internal packages.
		pkgDirs := map[string]string{}
		for _, imp := range src.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if !strings.HasPrefix(path, aegisModule) {
				continue
			}
			relDir := strings.TrimPrefix(path, aegisModule)
			importName := relDir[strings.LastIndex(relDir, "/")+1:]
			if imp.Name != nil {
				importName = imp.Name.Name
			}
			pkgDirs[importName] = relDir
		}

		for _, decl := range src.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Body == nil || !strings.HasPrefix(fn.Name.Name, "Fuzz") {
				continue
			}
			targets[fn.Name.Name] = true
			files = append(files, filepath.ToSlash(rel))
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				ident, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				if dir, ok := pkgDirs[ident.Name]; ok {
					refs[ident.Name] = dir
				}
				return true
			})
		}
	}
	sort.Strings(files)
	return targets, files, refs
}

func TestCIFuzzScope(t *testing.T) {
	root := repoRoot(t)
	ciPath := filepath.Join(root, ".github", "workflows", "ci.yml")
	raw, err := os.ReadFile(ciPath)
	if err != nil {
		t.Fatalf("read ci.yml: %v", err)
	}
	ci := string(raw)

	for _, g := range ciFuzzGroups {
		listMatch := regexp.MustCompile(`(?s)` + regexp.QuoteMeta(g.listKey) + `=\((.*?)\)`).FindStringSubmatch(ci)
		if listMatch == nil {
			t.Fatalf("ci.yml has no %s=(...) array", g.listKey)
		}
		listed := map[string]bool{}
		for _, name := range regexp.MustCompile(`Fuzz\w+`).FindAllString(listMatch[1], -1) {
			listed[name] = true
		}

		watchMatch := regexp.MustCompile(regexp.QuoteMeta(g.watchKey) + `='([^']+)'`).FindStringSubmatch(ci)
		if watchMatch == nil {
			t.Fatalf("ci.yml has no %s='...' pattern list", g.watchKey)
		}
		watch := strings.Split(watchMatch[1], "|")

		targets, files, refs := ciFuzzScan(t, root, g.pkgDir)
		if len(targets) == 0 {
			t.Fatalf("%s: no Fuzz functions found, but ci.yml lists %v", g.pkgDir, keysOf(listed))
		}

		for name := range targets {
			if !listed[name] {
				t.Errorf("ci.yml %s is missing %s (defined in %s)", g.listKey, name, g.pkgDir)
			}
		}
		for name := range listed {
			if !targets[name] {
				t.Errorf("ci.yml %s lists %s, but no such Fuzz function exists in %s", g.listKey, name, g.pkgDir)
			}
		}

		// The watch set must reach the targets' own files and every internal
		// package their bodies call into, or a scoped run skips them.
		required := append([]string(nil), files...)
		for _, dir := range refs {
			required = append(required, filepath.ToSlash(filepath.Join(dir, "x.go")))
		}
		for _, path := range required {
			if !ciWatchCovers(watch, path) {
				t.Errorf("ci.yml %s=%q does not cover %s (needed by the Fuzz targets in %s)",
					g.watchKey, watchMatch[1], path, g.pkgDir)
			}
		}
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
