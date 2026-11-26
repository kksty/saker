package lifecycle

// The reapers run on timers, with no round, no run row and no prompt. That makes
// any decision they take invisible: nobody can review it, cancel it or attribute
// it. So the negative contract in lifecycle.go is enforced mechanically instead
// of by review.
//
// Falsification: add `import ".../internal/decider"` (or one CreateIntent /
// BuildPrompt call) to any non-test file in this package and the corresponding
// assertion fails with the file and line.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// forbiddenImports are the layers that decide or render. Lifecycle may receive
// their output through Options seams; importing them would let it reach back.
var forbiddenImports = []string{
	"github.com/kksty/saker/internal/decider",
	"github.com/kksty/saker/internal/api",
	"github.com/kksty/saker/internal/board",
}

// forbiddenCalls are the entry points that create, reshape or start work. A
// reaper that needs one of them is not converging a row any more.
var forbiddenCalls = []string{
	"CreateIntent", "AddIntent", "CreateProposal", "AddProposal", "ProposeIntent",
	"BuildPrompt", "StartRun", "StartCommander",
}

func TestReapersKeepTheZeroStrategyContract(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		checked++
		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			for _, forbidden := range forbiddenImports {
				if path == forbidden {
					t.Errorf("%s imports %s: the keep-alive layer must not reach the decision layer",
						fset.Position(imp.Pos()), path)
				}
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			var name string
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				name = fun.Name
			case *ast.SelectorExpr:
				name = fun.Sel.Name
			default:
				return true
			}
			for _, forbidden := range forbiddenCalls {
				if name == forbidden {
					t.Errorf("%s calls %s: a reaper converges rows, it does not create or start work",
						fset.Position(call.Pos()), name)
				}
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("no non-test source files were checked; the contract test is vacuous")
	}
}
