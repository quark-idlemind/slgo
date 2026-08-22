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
// There are two ways to reach the backend and most of these tests are
// run BOTH ways, by bothWays below.  Pipe is a real connection --
// in-process and needing no network, but the messages are marshalled and
// the streams are streams, so a caller going away is the server's
// context being cancelled rather than a method returning early.  Direct
// is the same server with nothing under it, which is what the callers
// that run scripts by the hundred thousand use.  The two are meant to be
// indistinguishable from the caller's side, and a test that passes one
// way and fails the other is a divergence worth failing the build over
// rather than discovering later in autobench.
//
// The two exceptions are named where they are: a caller that DIES needs
// a connection to lose, and a caller that cancels PART WAY THROUGH a run
// needs the run to still be going.  Direct can do neither, so those stay
// on the wire.

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

// reach is one of the two ways of getting at a running backend.
type reach struct {
	name string
	open func(*testing.T, *scripttest.Server) scriptv1.RunnerClient
}

// overAPipe is the real connection, and inProcess is Direct.
var (
	overAPipe = reach{"over a pipe", func(t *testing.T, s *scripttest.Server) scriptv1.RunnerClient {
		t.Helper()
		conn, err := s.Pipe()
		if err != nil {
			t.Fatalf("Pipe: %v", err)
		}
		return scriptv1.NewRunnerClient(conn)
	}}
	inProcess = reach{"in process", func(_ *testing.T, s *scripttest.Server) scriptv1.RunnerClient {
		return s.Direct()
	}}
)

// bothWays runs a test once against each client.
//
// Written as a wrapper rather than as two copies of every test because
// the point is that there is ONE set of expectations: whatever the
// contract means, it has to mean the same thing whether or not there is
// a connection carrying it, and a test that had drifted between two
// copies would be asserting that nobody had noticed.
func bothWays(t *testing.T, fn func(*testing.T, reach)) {
	t.Helper()
	for _, r := range []reach{overAPipe, inProcess} {
		t.Run(r.name, func(t *testing.T) { fn(t, r) })
	}
}

// serve starts a backend and returns a client for it, reached the way r
// says.
func serve(t *testing.T, r reach, o scripttest.Options) (*scripttest.Server, scriptv1.RunnerClient) {
	t.Helper()
	if o.Second == 0 {
		o.Second = testSecond
	}
	s := scripttest.New(o)
	c := r.open(t, s)
	t.Cleanup(s.Stop)
	return s, c
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
// count and the pad are in a comment, which is where this backend reads
// them from and where the real script carries them.  A comment because a
// comment is free -- measured, 604 bytes of one moved llGetUsedMemory
// not at all -- so the digits cannot change what is being measured.
func benchScript(cnt, pad int) string {
	return fmt.Sprintf("// autobench cnt=%d pad=%d\n"+
		"default {\n\tstate_entry() {\n"+
		"\t\tinteger mem = llGetUsedMemory();\n"+
		"\t\tllOwnerSay(\"RESULT:MEM=\" + (string)mem);\n"+
		"\t}\n}\n", cnt, pad)
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
	bothWays(t, func(t *testing.T, r reach) {
		_, c := serve(t, r, scripttest.Options{Version: "1"})
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

		// What Health handed back is the caller's own.  In process there
		// is nothing to marshal, so a caller that edited it would be
		// editing what the server tells the next one -- which over a
		// connection it could not do, and which would be an unpleasant
		// thing to find out from a benchmark.
		caps.Grid = true
		again, err := c.Health(context.Background(), &scriptv1.HealthRequest{})
		if err != nil {
			t.Fatalf("Health: %v", err)
		}
		if again.GetCapabilities().GetGrid() {
			t.Error("editing one caller's Capabilities changed what the backend says it is")
		}
	})
}

// TestABackendThatIsUpButNotReadyRefusesToRunAndSaysWhy is the viewer
// that has not been started yet.  The distinction is the point: it
// answers, so it is up, and a caller may reasonably wait rather than
// give up -- but a run against it has to fail rather than pretend.
func TestABackendThatIsUpButNotReadyRefusesToRunAndSaysWhy(t *testing.T) {
	bothWays(t, func(t *testing.T, r reach) {
		s, c := serve(t, r, scripttest.Options{NotReady: "the viewer is not started"})

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
	})
}

// BenchmarkARunReachedEachWay is where the numbers quoted in direct.go
// come from.  It is a benchmark of the TRANSPORT and not of anything
// this backend does: the script is the smallest one the model
// understands, so what is being timed is a lease's worth of bookkeeping
// and seven messages either marshalled and handed between goroutines or
// not.
func BenchmarkARunReachedEachWay(b *testing.B) {
	for _, r := range []reach{overAPipe, inProcess} {
		b.Run(r.name, func(b *testing.B) {
			s := scripttest.New(scripttest.Options{
				Second: testSecond,
				Memory: scripttest.Memory{Pad: 474, CodeSize: 368},
			})
			defer s.Stop()
			var c scriptv1.RunnerClient
			switch r.name {
			case overAPipe.name:
				conn, err := s.Pipe()
				if err != nil {
					b.Fatalf("Pipe: %v", err)
				}
				c = scriptv1.NewRunnerClient(conn)
			default:
				c = s.Direct()
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stream, err := c.Lease(ctx, &scriptv1.LeaseRequest{Who: "a benchmark of the transport"})
			if err != nil {
				b.Fatalf("Lease: %v", err)
			}
			ev, err := stream.Recv()
			if err != nil {
				b.Fatalf("Lease: %v", err)
			}
			target := ev.GetGranted().GetTargets()[0].GetId()

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				run, err := c.Run(context.Background(), &scriptv1.RunRequest{
					Target: target, Source: benchScript(1, 474), Done: "DONE",
				})
				if err != nil {
					b.Fatalf("Run: %v", err)
				}
				for {
					if _, err := run.Recv(); err != nil {
						if err != io.EOF {
							b.Fatalf("Run: %v", err)
						}
						break
					}
				}
			}
		})
	}
}
