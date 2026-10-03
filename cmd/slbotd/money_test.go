package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// payingIn is every place a file pays: a call of Pay or PayObject, or a
// MoneyTransferRequest named.
func payingIn(fset *token.FileSet, f *ast.File) []string {
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			switch n.Sel.Name {
			case "Pay", "PayObject", "MoneyTransferRequest":
				out = append(out, fset.Position(n.Pos()).String())
			}
		}
		return true
	})
	return out
}

// TestNothingHerePays: slbotd attends avatars with a language model
// behind them, and paying is not a thing the model may ask for or the
// daemon may do.
// Why: doc/money.md#slbotd
func TestNothingHerePays(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, at := range payingIn(fset, f) {
			t.Errorf("%s pays, and slbotd does not", at)
		}
	}
}

// TestThePayingCheckFindsOne: a check that found nothing would pass on a
// daemon that paid everywhere.
func TestThePayingCheckFindsOne(t *testing.T) {
	const src = `package p

func a(s *sl.Session) {
	s.Pay(ctx, who, 5, "")
	s.PayObject(ctx, o, 5, "")
	_ = &msg.MoneyTransferRequest{}
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := payingIn(fset, f); len(got) != 3 {
		t.Errorf("found %v, want three", got)
	}
}

// TestNothingHereAsksForAccountDetails: slbotd answers IMs with a local
// language model, so anything it can read can be talked out of it, and
// an avatar's email address is not for it to read.
// Why: doc/account.md#nothing-logs-it
func TestNothingHereAsksForAccountDetails(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, at := range askingForAccountIn(fset, f) {
			t.Errorf("%s asks for the account details, and slbotd does not", at)
		}
	}
}

// askingForAccountIn is every place a file asks: a selector named
// UserInfo (the session's call and the capability constant's family) or
// UserInfoRequest.
func askingForAccountIn(fset *token.FileSet, f *ast.File) []string {
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		if s, ok := n.(*ast.SelectorExpr); ok {
			switch s.Sel.Name {
			case "UserInfo", "UserInfoCap", "UserInfoRequest":
				out = append(out, fset.Position(n.Pos()).String())
			}
		}
		return true
	})
	return out
}

// TestTheAccountCheckFindsOne: a check that found nothing would pass on
// a daemon that asked everywhere.
func TestTheAccountCheckFindsOne(t *testing.T) {
	const src = `package p

func a(s *sl.Session) {
	s.UserInfo(ctx)
	_ = &msg.UserInfoRequest{}
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := askingForAccountIn(fset, f); len(got) != 2 {
		t.Errorf("found %v, want two", got)
	}
}
