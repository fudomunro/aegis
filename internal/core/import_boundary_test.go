package core

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// Import boundaries (AGENTS.md "Package boundaries"). The finer per-package
// import table lives in churn_guardrails_test.go; this test enforces the
// dependency direction across the whole module graph:
//
//   - no package under internal/ may depend on the hub (internal/core);
//     only main packages (repo root, cmd/*) import it;
//   - internal/shared imports nothing from the module (stdlib-only
//     plumbing plus seam vocabulary per AGENTS.md);
//   - internal/types imports only stdlib + internal/shared.

const (
	modulePath   = "github.com/fudomunro/aegis"
	hubPath      = modulePath + "/internal/core"
	sharedPath   = modulePath + "/internal/shared"
	typesPath    = modulePath + "/internal/types"
	listPackages = "go list -json ./..."
)

type listedPackage struct {
	ImportPath string
	Name       string
	Imports    []string
	Deps       []string
}

func listedPackages(t *testing.T) []listedPackage {
	t.Helper()
	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("%s: %v\n%s", listPackages, err, ee.Stderr)
		}
		t.Fatalf("%s: %v", listPackages, err)
	}
	var pkgs []listedPackage
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for dec.More() {
		var p listedPackage
		if err := dec.Decode(&p); err != nil {
			t.Fatalf("decode %s output: %v", listPackages, err)
		}
		pkgs = append(pkgs, p)
	}
	if len(pkgs) == 0 {
		t.Fatalf("%s returned no packages", listPackages)
	}
	return pkgs
}

// isStdlib reports whether an import path is in the Go standard library
// (its first path element carries no dot).
func isStdlib(path string) bool {
	first, _, _ := strings.Cut(path, "/")
	return !strings.Contains(first, ".")
}

func underOrIs(root, path string) bool {
	return path == root || strings.HasPrefix(path, root+"/")
}

func TestImportBoundary_HubOnlyFromMain(t *testing.T) {
	var failures []string
	for _, p := range listedPackages(t) {
		if underOrIs(modulePath+"/internal", p.ImportPath) && p.ImportPath != hubPath {
			for _, dep := range p.Deps {
				if dep == hubPath {
					failures = append(failures, p.ImportPath+" depends on the hub ("+hubPath+")")
					break
				}
			}
		}
		for _, imp := range p.Imports {
			if imp == hubPath && p.Name != "main" {
				failures = append(failures,
					p.ImportPath+" (package "+p.Name+") imports the hub; only main packages may")
			}
		}
	}
	if len(failures) > 0 {
		t.Fatalf("package boundaries (AGENTS.md 'Package boundaries') violated:\n%s",
			strings.Join(failures, "\n"))
	}
}

func TestImportBoundary_Leaves(t *testing.T) {
	var failures []string
	for _, p := range listedPackages(t) {
		var leaf string
		switch {
		case underOrIs(sharedPath, p.ImportPath):
			leaf = "shared"
		case underOrIs(typesPath, p.ImportPath):
			leaf = "types"
		default:
			continue
		}
		for _, imp := range p.Imports {
			if isStdlib(imp) || (leaf == "types" && underOrIs(sharedPath, imp)) {
				continue
			}
			failures = append(failures, p.ImportPath+" imports "+imp)
		}
	}
	if len(failures) > 0 {
		t.Fatalf("leaf packages must stay leaves (AGENTS.md 'Package boundaries'):\n%s",
			strings.Join(failures, "\n"))
	}
}
