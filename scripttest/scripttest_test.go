package scripttest_test

// The backend's own tests, and what they are for.
//
// Everything a caller is written against is here: that a lease comes
// back when the caller does not, that two callers queue rather than
// share, and that a run says Compiled once, Finished once, and nothing
// after it.  These are the contract in practice.  A second backend --
// the simulator, a viewer -- that disagrees with them disagrees with
// autobench, and this file is the cheapest place to find that out.
//
// They run over a real connection.  Pipe is in-process and needs no
// network, but the messages are still marshalled and the streams are
// still streams, so a caller going away is the server's context being
// cancelled rather than a method returning early.

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/quark-idlemind/slgo/proto/scriptv1"
	"github.com/quark-idlemind/slgo/scripttest"
)

// testSecond is what one second of the contract's timeouts is worth in
// these tests.  Ten milliseconds: long enough that a run over the pipe
// finishes inside one and short enough that the timeout tests cost
// nothing.  A test of the timeout path with real seconds in it would
// take a second, and would then be the slowest thing in the package.
const testSecond = 10 * time.Millisecond

// serve starts a backend and returns a client for it.
func serve(t *testing.T, o scripttest.Options) (*scripttest.Server, scriptv1.RunnerClient) {
	t.Helper()
	if o.Second == 0 {
		o.Second = testSecond
	}
	s := scripttest.New(o)
	conn, err := s.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	t.Cleanup(s.Stop)
	return s, scriptv1.NewRunnerClient(conn)
}

// lease takes a lease and returns what was granted, along with the way
// to end it.  The stream is left open: the targets are only the
// caller's while it is.
func lease(t *testing.T, c scriptv1.RunnerClient, req *scriptv1.LeaseRequest) (*scriptv1.Granted, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := c.Lease(ctx, req)
	if err != nil {
		cancel()
		t.Fatalf("Lease: %v", err)
	}
	ev, err := stream.Recv()
	if err != nil {
		cancel()
		t.Fatalf("Lease: %v", err)
	}
	g := ev.GetGranted()
	if g == nil {
		cancel()
		t.Fatalf("first lease event is %T, want a Granted", ev.GetEvent())
	}
	return g, cancel
}

// eventually waits for something the server does on its own goroutine --
// giving a lease back after the caller has gone, which happens when the
// server notices and not when the client asks.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("%s did not happen within 2s", what)
}

// held is how many groups the pool says are taken.
func held(t *testing.T, c scriptv1.RunnerClient) int {
	t.Helper()
	p, err := c.Pool(context.Background(), &scriptv1.PoolRequest{})
	if err != nil {
		t.Fatalf("Pool: %v", err)
	}
	n := 0
	for _, a := range p.GetAgents() {
		for _, g := range a.GetGroups() {
			if g.GetHeldBy() != "" {
				n++
			}
		}
	}
	return n
}

// benchScript is a benchmark script as autobench renders one: the copy
// count and the pad are in the harness call, which is where this backend
// reads them from and where the real script reports them from.
func benchScript(cnt, pad int) string {
	return fmt.Sprintf("default {\n\tstate_entry() {\n\t\tresult(llGetUsedMemory(), %d, %d);\n\t}\n}\n", cnt, pad)
}

// transcript runs a script and collects the whole stream, which is what
// a caller that wants a transcript rather than a commentary does.
type transcript struct {
	compiled []*scriptv1.Compiled
	lines    []*scriptv1.Line
	faults   []*scriptv1.Fault
	finished []*scriptv1.Finished

	// order is the events in the order they arrived, by kind, so a test
	// can say what came first without unpicking a slice of oneofs.
	order []string
}

func collect(t *testing.T, c scriptv1.RunnerClient, req *scriptv1.RunRequest) (*transcript, error) {
	t.Helper()
	stream, err := c.Run(context.Background(), req)
	if err != nil {
		return nil, err
	}
	tr := &transcript{}
	for {
		ev, err := stream.Recv()
		if err == io.EOF {
			return tr, nil
		}
		if err != nil {
			return tr, err
		}
		switch {
		case ev.GetCompiled() != nil:
			tr.compiled = append(tr.compiled, ev.GetCompiled())
			tr.order = append(tr.order, "compiled")
		case ev.GetLine() != nil:
			tr.lines = append(tr.lines, ev.GetLine())
			tr.order = append(tr.order, "line")
		case ev.GetFault() != nil:
			tr.faults = append(tr.faults, ev.GetFault())
			tr.order = append(tr.order, "fault")
		case ev.GetFinished() != nil:
			tr.finished = append(tr.finished, ev.GetFinished())
			tr.order = append(tr.order, "finished")
		default:
			t.Fatalf("run event with nothing in it: %v", ev)
		}
	}
}

// text is what the object said, joined, for a test that only wants to
// know whether something was in there.
func (tr *transcript) text() string {
	var b strings.Builder
	for _, l := range tr.lines {
		b.WriteString(l.GetText())
		b.WriteString("\n")
	}
	return b.String()
}

// TestHealthSaysWhatTheBackendCanDoRatherThanWhatItIsCalled checks the
// capability flags, because they are what a caller branches on.  Getting
// Grid wrong is the one that matters: it is what a test that would do
// something destructive checks before doing it, and a fake that claimed
// to be a grid would be believed.
func TestHealthSaysWhatTheBackendCanDoRatherThanWhatItIsCalled(t *testing.T) {
	_, c := serve(t, scripttest.Options{Version: "1"})
	h, err := c.Health(context.Background(), &scriptv1.HealthRequest{})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !h.GetReady() {
		t.Errorf("Health says not ready (%q), want ready", h.GetWhy())
	}
	caps := h.GetCapabilities()
	if caps.GetGrid() {
		t.Error("Capabilities.Grid is true; this backend is not a grid and must never say it is")
	}
	for _, x := range []struct {
		name string
		got  bool
	}{
		{"CompileOnly", caps.GetCompileOnly()},
		{"Faults", caps.GetFaults()},
		{"OutOfMemory", caps.GetOutOfMemory()},
		{"PersistentTargets", caps.GetPersistentTargets()},
	} {
		if !x.got {
			t.Errorf("Capabilities.%s is false, want true by default", x.name)
		}
	}
	if caps.GetMaxConcurrentRuns() < 1 {
		t.Errorf("MaxConcurrentRuns = %d, want at least one", caps.GetMaxConcurrentRuns())
	}
}

// TestABackendThatIsUpButNotReadyRefusesToRunAndSaysWhy is the viewer
// that has not been started yet.  The distinction is the point: it
// answers, so it is up, and a caller may reasonably wait rather than
// give up -- but a run against it has to fail rather than pretend.
func TestABackendThatIsUpButNotReadyRefusesToRunAndSaysWhy(t *testing.T) {
	s, c := serve(t, scripttest.Options{NotReady: "the viewer is not started"})

	h, err := c.Health(context.Background(), &scriptv1.HealthRequest{})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.GetReady() || h.GetWhy() != "the viewer is not started" {
		t.Errorf("Health = ready %v %q, want not ready with a reason", h.GetReady(), h.GetWhy())
	}

	_, err = collect(t, c, &scriptv1.RunRequest{Source: benchScript(0, 5), Done: "DONE"})
	if status.Code(err) != codes.Unavailable {
		t.Errorf("Run on a backend that is not ready = %v, want Unavailable", err)
	}

	// And it comes up.  A caller that waited has to find the backend
	// working afterwards, or waiting was pointless.
	s.SetReady(true, "")
	tr, err := collect(t, c, &scriptv1.RunRequest{Source: benchScript(0, 5), Done: "DONE"})
	if err != nil {
		t.Fatalf("Run after the backend came up: %v", err)
	}
	if len(tr.finished) != 1 || !tr.finished[0].GetSentinel() {
		t.Errorf("after the backend came up the run finished %v, want one Finished with the sentinel", tr.finished)
	}
}
