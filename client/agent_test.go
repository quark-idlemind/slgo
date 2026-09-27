package client

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
)

// TestAttachingWhileCallsRunIsNotARace: attach writes the name of the
// session under the lock, and every call names it.  Run under -race,
// this fails on any call that reads the name without the lock.
func TestAttachingWhileCallsRunIsNotARace(t *testing.T) {
	t.Parallel()
	d, conn := dialFake(t)

	// Each attach opens a stream the fake reports on d.attached, which
	// would fill.
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		for {
			select {
			case <-d.attached:
			case <-stop:
				return
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Whether each is answered does not matter; this daemon implements
	// few of them.  That each reads the name is the point.
	calls := []func(){
		func() { conn.Status(ctx) },
		func() { conn.ViewerCredential(ctx) },
		func() { conn.Presence(ctx, 0) },
		func() { conn.Attachments(ctx, "") },
		func() { conn.Objects(ctx, "", "") },
		func() { conn.Friends(ctx) },
		func() { conn.NoteFriend(ctx, theOther, true) },
		func() { conn.Region(ctx) },
		func() { conn.Land(ctx) },
		func() { conn.Ground(ctx, 1, 2, 3, 4) },
		func() { conn.Neighbours(ctx, nil) },
		func() { conn.Control(ctx, 0) },
		func() { conn.Flush(ctx) },
		func() { conn.DoCap(ctx, agent.CapRequest{Cap: "SimulatorFeatures"}) },
		func() { conn.Handled(ctx, "an offer", "", false) },
		func() { conn.Face(ctx, nil, 0) },
		func() { conn.Halt(ctx) },
		func() { conn.Posture(ctx) },
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 5 {
			if _, err := conn.Attach(ctx, "quark"); err != nil {
				t.Errorf("Attach: %v", err)
				return
			}
		}
	}()
	for _, call := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 5 {
				call()
			}
		}()
	}
	wg.Wait()
}

// TestTheAgentIsReadOnlyUnderTheLock: a read of c.agent anywhere but
// agentName skips the lock attach writes it under.
func TestTheAgentIsReadOnlyUnderTheLock(t *testing.T) {
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
		for _, at := range agentReadsIn(fset, f) {
			t.Errorf("%s reads the agent's name without agentName: attach writes "+
				"it under the lock, and a call may run during an attach", at)
		}
	}
}

// TestTheAgentReadCheckFindsARead: a check that found nothing would
// pass on a package full of them.
func TestTheAgentReadCheckFindsARead(t *testing.T) {
	const src = `package p

func (c *Conn) agentName() string { return c.agent }

func (c *Conn) attach(n string) { c.agent = n }
func (c *Conn) a() { use(c.agent) }
func (c *Conn) b() { r := &Req{Agent: c.agent}; use(r) }
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	got := agentReadsIn(fset, f)
	want := []string{"p.go:6:28", "p.go:7:41"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("found\n\t%s\nwant\n\t%s", strings.Join(got, "\n\t"), strings.Join(want, "\n\t"))
	}
}

// agentReadsIn lists the reads of a field called agent in f outside
// agentName: every .agent that is not being assigned to.
func agentReadsIn(fset *token.FileSet, f *ast.File) []string {
	var out []string
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "agentName" {
			continue
		}
		written := map[ast.Expr]bool{}
		ast.Inspect(decl, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.AssignStmt:
				for _, l := range n.Lhs {
					written[l] = true
				}
			case *ast.SelectorExpr:
				if n.Sel.Name == "agent" && !written[n] {
					out = append(out, fset.Position(n.Sel.Pos()).String())
				}
			}
			return true
		})
	}
	return out
}
