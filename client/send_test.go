package client

// One send at a time on the attach stream.
//
// gRPC allows a stream one sender at a time, and a connection sends from
// every caller it has at once: messages, subscriptions, locks, places,
// and the grants its receive loop gives back.

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// TestSendsFromManyCallersAllArriveWhole: every path that sends on the
// stream, all at once, and the daemon has to receive each frame.
func TestSendsFromManyCallersAllArriveWhole(t *testing.T) {
	t.Parallel()
	d, conn := dialFake(t)
	d.sent = make(chan *pb.ClientPacket, 1024)
	d.locked = grants
	d.slotted = func(s *pb.Slots) *pb.SlotsGranted {
		return &pb.SlotsGranted{
			Request: s.Request, Grant: fmt.Sprintf("g%d", s.Request),
			Held: []*pb.SlotHeld{{Agent: "quark"}},
		}
	}
	if _, err := conn.Attach(context.Background(), "quark"); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const each = 25
	var wg sync.WaitGroup
	for i := 0; i < each; i++ {
		wg.Add(4)
		go func() {
			defer wg.Done()
			if err := conn.SendRaw(ctx, 1, []byte{byte(i)}, false); err != nil {
				t.Errorf("SendRaw: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("lock %d", i)
			if err := conn.Lock(ctx, name); err != nil {
				t.Errorf("Lock: %v", err)
			}
			if err := conn.Unlock(name); err != nil {
				t.Errorf("Unlock: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			g, err := conn.TrySlots(ctx, 1, time.Minute, "")
			if err != nil {
				t.Errorf("TrySlots: %v", err)
				return
			}
			if err := conn.ReleaseSlots(g.ID, true); err != nil {
				t.Errorf("ReleaseSlots: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			if err := conn.Watch("ChatFromSimulator"); err != nil {
				t.Errorf("Watch: %v", err)
			}
		}()
	}
	wg.Wait()

	want := map[string]int{"message": each, "lock": each, "unlock": each,
		"slots": each, "release": each, "subscribe": each}
	got := map[string]int{}
	for n := 0; n < 6*each; n++ {
		select {
		case p := <-d.sent:
			switch {
			case p.GetMessage() != nil:
				got["message"]++
			case p.GetLock() != nil:
				got["lock"]++
			case p.GetUnlock() != nil:
				got["unlock"]++
			case p.GetSlots() != nil:
				got["slots"]++
			case p.GetReleaseSlots() != nil:
				got["release"]++
			case p.GetSubscribe() != nil:
				got["subscribe"]++
			default:
				got[fmt.Sprintf("%T", p.Body)]++
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("the daemon received %v, want %v", got, want)
		}
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("the daemon received %v, want %v", got, want)
	}
}

// TestEverySendOnTheStreamIsOneAtATime: a send on the stream anywhere
// but sendPacket skips the lock that keeps them apart.
func TestEverySendOnTheStreamIsOneAtATime(t *testing.T) {
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
		for _, at := range streamSendsIn(fset, f) {
			t.Errorf("%s sends on a stream without sendPacket: gRPC allows one "+
				"send at a time on a stream, and this connection sends from "+
				"many goroutines", at)
		}
	}
}

// TestTheStreamSendCheckFindsASend: a check that found nothing would
// pass on a package full of them.  A Send taking a context first is a
// unary call or Conn.Send, not a stream's.
func TestTheStreamSendCheckFindsASend(t *testing.T) {
	const src = `package p

func (c *Conn) sendPacket(s stream, p packet) error { return s.Send(p) }

func a(s stream, p packet) { s.Send(p) }
func b(c *Conn, p packet) { c.stream.SendMsg(p) }
func d(s stream) { s.CloseSend() }
func e(c *Conn, ctx context, p packet) { c.grid.Send(ctx, p); c.Send(ctx, p, true) }
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	got := streamSendsIn(fset, f)
	want := []string{"p.go:5:30", "p.go:6:29", "p.go:7:20"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("found\n\t%s\nwant\n\t%s", strings.Join(got, "\n\t"), strings.Join(want, "\n\t"))
	}
}

// streamSendsIn lists the sends on a stream in f outside sendPacket: a
// Send with one argument, which only a stream's takes, and SendMsg and
// CloseSend.
func streamSendsIn(fset *token.FileSet, f *ast.File) []string {
	var out []string
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "sendPacket" {
			continue
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch name := sel.Sel.Name; {
			case name == "Send" && len(call.Args) == 1, name == "SendMsg", name == "CloseSend":
				out = append(out, fset.Position(call.Pos()).String())
			}
			return true
		})
	}
	return out
}
