package sl

// A subscription borrowed for the length of a call is taken out and given
// back in one place, Session.borrow, which counts the callers.
// Why: doc/sounds.md#what-is-heard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryBorrowedSubscriptionIsTakenOutByBorrow: a Watch or an Unwatch
// anywhere else in the package is a copy of the count, and two copies
// disagree the first time two calls overlap.  Hosted's own are the
// methods the daemon is spoken through.
func TestEveryBorrowedSubscriptionIsTakenOutByBorrow(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") || name == "hosted.go" {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Name.Name == "borrow" {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch name := sel.Sel.String(); name {
				case "Watch", "Unwatch":
					t.Errorf("%s calls %s: take a subscription out with Session.borrow, "+
						"which counts the callers that share it", fset.Position(sel.Pos()), name)
				}
				return true
			})
		}
	}
}
