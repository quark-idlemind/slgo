package sl

// Waiting for something by looking for it, and stopping when the caller
// does.
//
// What poll promises is tested once, here.  Each call built on it is
// tested beside its other tests for having used it: cancelled part way
// through its wait, it has to come back at once with the caller's
// cancel, not at its own deadline with a timeout.

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// promptly is how soon a call has to return once its caller gives up.
// The shortest wait any of them has of its own is fifteen seconds, so a
// call still going after this has not noticed.
const promptly = 5 * time.Second

// whenCancelled runs call aside on ctx and returns what it returned,
// failing the test unless it returns within promptly of ctx being
// cancelled.  Whatever cancels ctx -- usually the fake grid, in the
// middle of answering the look a test is interested in -- decides
// where in the wait the cancel lands.
func whenCancelled[T any](t *testing.T, ctx context.Context, call func(context.Context) (T, error)) (T, error) {
	t.Helper()
	type answer struct {
		v   T
		err error
	}
	done := make(chan answer, 1)
	go func() {
		v, err := call(ctx)
		done <- answer{v, err}
	}()

	select {
	case a := <-done:
		if ctx.Err() == nil {
			t.Fatalf("the call returned before its caller gave up: %v, %v", a.v, a.err)
		}
		return a.v, a.err
	case <-ctx.Done():
	case <-time.After(60 * time.Second):
		t.Fatal("nothing gave up on the call")
	}
	select {
	case a := <-done:
		return a.v, a.err
	case <-time.After(promptly):
		t.Fatalf("the call was still waiting %s after its caller gave up", promptly)
	}
	panic("unreachable")
}

// TestPollStopsWhenTheCallerGivesUp: the read here never looks at its
// context, as Direct's Presence does not, and the pause between reads
// is an hour.  Only the wait itself hearing the cancel gets it back in
// time.
func TestPollStopsWhenTheCallerGivesUp(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var reads atomic.Int32
	_, err := whenCancelled(t, ctx, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, poll(ctx, time.Hour, time.Hour, "nothing at all",
			func(context.Context) (bool, error) {
				// Given up on while poll is pausing, which is where
				// a sleep would not have heard it.
				if reads.Add(1) == 1 {
					time.AfterFunc(50*time.Millisecond, cancel)
				}
				return false, nil
			})
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("poll = %v, want the caller's cancel", err)
	}
	if errors.Is(err, ErrTimeout) {
		t.Error("a caller that gave up was told the grid timed out")
	}
	if n := reads.Load(); n != 1 {
		t.Errorf("read %d times, want once: the cancel came during the pause", n)
	}
}

// TestPollAsksAgainAfterAFailedRead: what is read is a round trip to
// AIS or the backend, and one that failed once may not fail twice.  A
// read's own ErrTimeout means "not yet", and so does a deadline of the
// read's own: only the caller's context being done is the caller
// giving up.
func TestPollAsksAgainAfterAFailedRead(t *testing.T) {
	t.Parallel()
	fails := []error{
		ErrTimeout,
		errors.New("the far end is having a moment"),
		context.DeadlineExceeded,
	}
	reads := 0
	err := poll(context.Background(), time.Minute, time.Millisecond, "the fourth look",
		func(context.Context) (bool, error) {
			reads++
			if reads <= len(fails) {
				return false, fails[reads-1]
			}
			return true, nil
		})
	if err != nil {
		t.Fatalf("poll = %v, want the fourth look to have held", err)
	}
	if reads != 4 {
		t.Errorf("read %d times, want 4", reads)
	}
}

// TestPollTimesOutSayingWhatItWaitedFor: the deadline is the one other
// way out, and says what was being waited for.  A last read that failed
// says so too, or a read that never worked would be reported as a thing
// that never appeared.
func TestPollTimesOutSayingWhatItWaitedFor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		failed error
		quoted bool
	}{
		{"the reads worked", nil, false},
		{"the last read failed", errors.New("the far end is down"), true},
		{"the last read timed out", ErrTimeout, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := poll(context.Background(), time.Nanosecond, time.Millisecond,
				"the lantern to appear", func(context.Context) (bool, error) {
					return false, tc.failed
				})
			if !errors.Is(err, ErrTimeout) {
				t.Fatalf("poll = %v, want a timeout", err)
			}
			if !strings.Contains(err.Error(), "the lantern to appear") {
				t.Errorf("poll = %v, want it to say what it waited for", err)
			}
			if got := strings.Contains(err.Error(), "failed"); got != tc.quoted {
				t.Errorf("poll = %v; quoting a failed read: %v, want %v", err, got, tc.quoted)
			}
		})
	}
}

// TestPollBelievesAReadThatHolds: a caller that gave up a moment too
// late is still told the thing happened, because it did.
func TestPollBelievesAReadThatHolds(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := poll(ctx, time.Minute, time.Hour, "something already there",
		func(context.Context) (bool, error) { return true, nil })
	if err != nil {
		t.Errorf("poll = %v, want the read that held believed", err)
	}
}

// TestLastLookIsOnlyForACallerThatGaveUp: after a timeout poll has only
// just looked; after a cancel, the look has to be on a context the
// cancel does not reach, or it could see nothing, and bounded, or a
// caller that gave up would wait on it indefinitely.
func TestLastLookIsOnlyForACallerThatGaveUp(t *testing.T) {
	t.Parallel()
	reads := 0
	read := func(ctx context.Context) (bool, error) {
		reads++
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if at, ok := ctx.Deadline(); !ok || time.Until(at) > lastLookFor {
			t.Errorf("the last look may take until %v; want no more than %s", at, lastLookFor)
		}
		return true, nil
	}

	if lastLook(context.Background(), read) || reads != 0 {
		t.Errorf("looked %d times after a timeout", reads)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !lastLook(ctx, read) || reads != 1 {
		t.Errorf("looked %d times for a caller that gave up, and did not see it", reads)
	}
}

// sleepsIn finds every sleep in one file of Go that cannot hear a
// cancel: time.Sleep, called or handed on, and a receive from
// time.After standing alone as a statement, which is the same thing.
func sleepsIn(fset *token.FileSet, f *ast.File) []string {
	name := ""
	for _, imp := range f.Imports {
		if imp.Path.Value == `"time"` {
			name = "time"
			if imp.Name != nil {
				name = imp.Name.Name
			}
		}
	}
	if name == "" {
		return nil
	}
	is := func(e ast.Expr, fn string) bool {
		sel, ok := e.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != fn {
			return false
		}
		pkg, ok := sel.X.(*ast.Ident)
		return ok && pkg.Name == name
	}

	var out []string
	var visit func(ast.Node) bool
	visit = func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CommClause:
			// A receive from time.After as one case of a select is a
			// timeout beside something else, which is the right way.
			for _, s := range n.Body {
				ast.Inspect(s, visit)
			}
			return false
		case *ast.SelectorExpr:
			if is(n, "Sleep") {
				out = append(out, fset.Position(n.Pos()).String()+": time.Sleep")
			}
		case *ast.ExprStmt:
			recv, ok := n.X.(*ast.UnaryExpr)
			if !ok || recv.Op != token.ARROW {
				break
			}
			if call, ok := recv.X.(*ast.CallExpr); ok && is(call.Fun, "After") {
				out = append(out, fset.Position(n.Pos()).String()+": <-time.After")
			}
		}
		return true
	}
	ast.Inspect(f, visit)
	return out
}

// TestNothingInThisPackageSleeps: a sleep cannot hear the caller give
// up.  A loop that polls with one keeps going when it is cancelled --
// every read fails at once, or never looks at the context -- until its
// own deadline, and then reports a timeout, which is neither true nor
// quick.  Six calls here were that loop.
func TestNothingInThisPackageSleeps(t *testing.T) {
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
		for _, at := range sleepsIn(fset, f) {
			t.Errorf("%s ignores cancellation: a caller that gives up is kept "+
				"until the sleep ends, and a polling loop built on it runs to its own "+
				"deadline and reports a timeout.  Poll with poll (session.go), which "+
				"stops when the caller does; pause with Settle.", at)
		}
	}
}

// TestTheSleepCheckFindsASleep: a check that found nothing would pass
// on a package full of them.  A comment naming time.Sleep, like poll's
// own, is not one.
func TestTheSleepCheckFindsASleep(t *testing.T) {
	const src = `package p

import clock "time"

// Not this: clock.Sleep(time.Second).
func a() { clock.Sleep(clock.Second) }
func b() { <-clock.After(clock.Second) }
func c() { nap := clock.Sleep; nap(1) }
func d(ctx interface{ Done() <-chan struct{} }) {
	select {
	case <-clock.After(clock.Second):
		clock.Sleep(clock.Second)
	case <-ctx.Done():
	}
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	got := sleepsIn(fset, f)
	want := []string{
		"p.go:6:12: time.Sleep",
		"p.go:7:12: <-time.After",
		"p.go:8:19: time.Sleep",
		"p.go:12:3: time.Sleep",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("found\n\t%s\nwant\n\t%s", strings.Join(got, "\n\t"), strings.Join(want, "\n\t"))
	}
}
