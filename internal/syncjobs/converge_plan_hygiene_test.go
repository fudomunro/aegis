package syncjobs

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

// The converge engine never names a kind (issue #382): per-kind
// composition is the kind-owned converge plan; this package schedules,
// chains, retries, and records. The engine files are scanned for built-in
// kind ids (string literals) and the library's kind constants — the two
// forms the per-kind switch regrew in before the plans absorbed it. Job
// types, conflict fields, and shape names are engine vocabulary and pass.

var convergeEngineFiles = []string{"converge_runner.go", "resolve.go"}

func TestConvergeEngineNeverNamesAKind(t *testing.T) {
	kindIDs := map[string]bool{}
	for _, k := range kinds.BuiltinLibraryKinds {
		kindIDs[k.ID] = true
	}
	fset := token.NewFileSet()
	var failures []string
	for _, name := range convergeEngineFiles {
		if _, err := os.Stat(name); err != nil {
			t.Fatalf("engine file %s: %v", name, err)
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.SelectorExpr:
				if sel, ok := v.X.(*ast.Ident); ok && sel.Name == "library" &&
					strings.HasPrefix(v.Sel.Name, "MirrorKind") {
					failures = append(failures,
						name+": library."+v.Sel.Name+" — kinds declare their converge "+
							"behavior in a converge plan (issue #382); the engine iterates shapes")
				}
			case *ast.BasicLit:
				if v.Kind != token.STRING {
					return true
				}
				val, err := strconv.Unquote(v.Value)
				if err == nil && kindIDs[val] {
					failures = append(failures,
						name+": kind literal "+v.Value+" — kinds declare their converge "+
							"behavior in a converge plan (issue #382); the engine iterates shapes")
				}
			}
			return true
		})
	}
	if len(failures) > 0 {
		t.Fatalf("the converge engine must not name a built-in kind:\n%s",
			strings.Join(failures, "\n"))
	}
}
