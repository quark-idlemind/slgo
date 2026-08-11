package main

// The script.v1 transport: how a benchmark script reaches whatever will
// run it, when that is not this program's own grid session.
//
// proto/script.proto is the seam.  A backend is anything that can be
// handed a script and made to say whether it compiled, what it said,
// whether it faulted and whether it got to the end -- the eLSL simulator
// with no grid at all, a viewer driven from outside, a daemon holding a
// real session, or the offline model in scripttest.  This is the one
// piece of autobench that knows the contract, and it is a backend like
// runner.go: everything above backend.go is unchanged by which of them
// is in use.
//
// # What stays on this side of the seam
//
// The RESULT:/INFO: convention and absorbResults.  Nothing about running
// a script requires a script to label its output; that is this program's
// arrangement with the harness it generates, and putting it in the
// contract would make every backend implement a convention only autobench
// has.  So the transport hands back lines and sift applies the rules.
//
// # What crosses it that used to be guessed
//
// Fault.out_of_memory.  A benchmark searching for a size limit treats
// running out of memory as the ANSWER and every other fault as a
// failure, and this program used to tell them apart by looking for
// "Stack-Heap" in the reason -- Second Life's wording, known to a
// program that is not supposed to know Second Life.  The contract says
// it outright, and each backend answers for itself.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/quark-idlemind/slgo/proto/scriptv1"
)

var _ backend = (*scriptRunner)(nil)

// scriptRunner runs benchmark scripts through a script.v1 backend.
type scriptRunner struct {
	c scriptv1.RunnerClient

	// targets are what the lease granted: the first is the measured
	// object and the rest are spares.  They are only ours while the
	// lease stream is open, which is why endLease is the whole of giving
	// them back -- there is no Release to forget to call.
	targets  []*scriptv1.Target
	endLease context.CancelFunc

	// Timeout bounds one run.  The contract counts in whole seconds,
	// which is what the far side is told; the context this end is given
	// room beyond it so that a backend reporting its own overrun is not
	// cut off mid-sentence instead.
	Timeout time.Duration

	// Info surfaces lines containing "INFO:" separately from the rest.
	Info bool

	// grid and compileOnly are what Health said this backend can do.
	// Asked once, at the start, rather than guessed from the name it
	// gave itself.
	grid        bool
	compileOnly bool

	// shut is what closing has to undo, innermost first.
	shut []func()
}

// dialScript opens a connection to a script.v1 backend at addr.
//
// Insecure, and deliberately: the contract carries no credentials and
// the backends it is for are a simulator or a viewer daemon on this
// machine or a trusted one.  A backend that wants authentication puts
// something in front of it; inventing a scheme here would be inventing
// one nobody else implements.
func dialScript(addr string) (*grpc.ClientConn, error) {
	return grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
}

// openBackend is --backend: a script.v1 daemon somewhere else, reached
// over the network and asked for somewhere to run.
//
// What is at the far end is none of this program's business, which is the
// point of there being a contract at all -- the eLSL simulator with no
// grid under it, a viewer driven from outside, a daemon holding a real
// session.  Two things follow, and both are answered by the backend
// rather than assumed here: whether a padding found through it is worth
// remembering in the file every later benchmark reads (Capabilities.grid,
// see basePadding), and whether it can be asked to compile a script
// without running it (see Compile).  A backend that says neither is still
// usable; it is measured against and not written down.
func openBackend(addr string, targets int) (backend, error) {
	conn, err := dialScript(addr)
	if err != nil {
		return nil, fmt.Errorf("dialling the backend at %s: %w", addr, err)
	}
	who := "autobench"
	if flags.Title != "" {
		who += " " + flags.Title
	}
	// Announced, unlike the offline model: a real backend may hold
	// several avatars' objects and chose one for us, and a reading is
	// only comparable with another from the same avatar.
	r, err := openScript(context.Background(), scriptv1.NewRunnerClient(conn), targets, flags.Agent, who, true)
	if err != nil {
		conn.Close()
		return nil, err
	}
	r.Timeout = flags.Timeout
	r.Info = flags.Show
	r.alsoClose(func() { conn.Close() })
	return r, nil
}

// openScript takes a lease on a script.v1 backend and answers with
// something a benchmark can run in.
//
// targets is how many objects to hold: one to measure in and the rest to
// take readings in at once.  They are granted together or not at all,
// which is what a measurement needs -- a benchmark leaves its base
// reading inside the measured object between runs, and a second caller
// in the same object would read somebody else's numbers.
//
// agent asks for a particular avatar's objects; empty takes the first
// free group anywhere.  It is a parameter rather than read from the flags
// because the offline model has one made-up avatar and no opinion about
// which, and a --agent meant for the grid must not turn into a lease
// request for an avatar the model has never heard of.
//
// announce says whether to report which avatar the objects turned out to
// belong to.  Worth saying when a real backend chose for us -- a reading
// is only comparable with another from the same avatar -- and noise when
// the backend is the offline model, which has exactly one and made it up.
//
// c is a client and not a connection, because whether there is a
// connection is not this function's business: --backend dials one, and
// the offline model behind --test is reached in process, where a run
// costs 5 microseconds instead of 100.  Both answer the same interface
// and everything below here is written against that.
func openScript(ctx context.Context, c scriptv1.RunnerClient, targets int, agent, who string, announce bool) (*scriptRunner, error) {
	h, err := c.Health(ctx, &scriptv1.HealthRequest{})
	if err != nil {
		return nil, fmt.Errorf("asking the backend what it is: %w", err)
	}
	if !h.GetReady() {
		// Not an error the backend made: it is up and cannot run
		// anything yet.  A benchmark has nothing useful to do about that
		// but say which of the two it is.
		return nil, fmt.Errorf("the %s backend is not ready to run anything: %s",
			h.GetBackend(), h.GetWhy())
	}
	caps := h.GetCapabilities()
	if !caps.GetPersistentTargets() {
		// The base reading travels from the cnt=0 script to the cnt>0
		// ones through whatever the object keeps between runs.  A backend
		// that keeps nothing would divide every reading against a zero
		// and report the whole of the script's memory as the code's --
		// plausible numbers, and wrong.  Refuse rather than measure.
		return nil, fmt.Errorf("the %s backend does not keep what a script leaves in an "+
			"object between runs, and a benchmark's base reading travels that way",
			h.GetBackend())
	}

	r := &scriptRunner{
		c: c, grid: caps.GetGrid(), compileOnly: caps.GetCompileOnly(),
	}

	// The lease is a stream and stays open for the life of the process.
	// Cancelling it is how the objects go back, and so is dying, which is
	// the point of its being a stream at all.
	lctx, cancel := context.WithCancel(context.Background())
	stream, err := c.Lease(lctx, &scriptv1.LeaseRequest{
		Targets: int32(targets), Agent: agent, Who: who,
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("asking for somewhere to run: %w", err)
	}
	for {
		ev, err := stream.Recv()
		if err != nil {
			cancel()
			return nil, fmt.Errorf("waiting for somewhere to run: %w", err)
		}
		if q := ev.GetQueued(); q != nil {
			// Said out loud, because from outside a queue and a hang look
			// the same and one of them is worth waiting through.
			noticef("every group is busy; %d callers ahead\n", q.GetAhead())
			continue
		}
		if g := ev.GetGranted(); g != nil {
			r.targets = g.GetTargets()
			if announce && g.GetAgent() != "" {
				fmt.Fprintf(os.Stderr, "running as %s, group %d, %d objects\n",
					g.GetAgent(), g.GetGroup(), len(r.targets))
			}
			break
		}
	}
	r.shut = append(r.shut, cancel)
	if len(r.targets) == 0 {
		r.Close()
		return nil, fmt.Errorf("the backend granted a lease with nothing in it")
	}
	return r, nil
}

// alsoClose adds something for Close to undo -- the connection, when
// this program opened it, and the in-process server behind --test.
func (r *scriptRunner) alsoClose(fn func()) { r.shut = append(r.shut, fn) }

func (r *scriptRunner) Spares() int { return len(r.targets) - 1 }
func (r *scriptRunner) Grid() bool  { return r.grid }

func (r *scriptRunner) Send(src string) (results, info []string, err error) {
	return r.runIn(r.targets[0], src)
}

func (r *scriptRunner) SendSpare(n int, src string) (results, info []string, err error) {
	return r.runIn(r.targets[n+1], src)
}

// seconds is the timeout as the contract counts it.  Whole seconds is
// the grid's unit and is right for one; the rounding up is so that a
// caller asking for less than a second gets the shortest run the
// contract can express rather than the backend's default.
func (r *scriptRunner) seconds() int64 {
	if r.Timeout <= 0 {
		return 0
	}
	if n := int64(r.Timeout / time.Second); n > 0 {
		return n
	}
	return 1
}

// runIn runs one script in one object and turns what came back into what
// a benchmark acts on.
func (r *scriptRunner) runIn(t *scriptv1.Target, src string) (results, info []string, err error) {
	var (
		said     []spoken
		fault    *scriptv1.Fault
		refused  []string
		compiled = true
		sentinel bool
	)
	err = r.stream(&scriptv1.RunRequest{
		Target: t.GetId(), Name: scriptName, Source: src,
		Done: "DONE", TimeoutSeconds: r.seconds(),
	}, func(ev *scriptv1.RunEvent) {
		switch {
		case ev.GetCompiled() != nil:
			compiled = ev.GetCompiled().GetOk()
			refused = ev.GetCompiled().GetErrors()
		case ev.GetLine() != nil:
			l := ev.GetLine()
			said = append(said, spoken{text: l.GetText(), debug: l.GetDebug()})
		case ev.GetFault() != nil:
			fault = ev.GetFault()
		case ev.GetFinished() != nil:
			sentinel = ev.GetFinished().GetSentinel()
		}
	})
	if err != nil {
		return nil, nil, err
	}
	if !compiled {
		// Nothing ran, so there is nothing it said.  A refusal is a
		// refusal whatever came before it.
		return nil, nil, &compileError{Errors: refused}
	}

	results, info = sift(said, r.Info)

	// A fault beats the silence it caused: a script that crashed was
	// never going to say DONE, and reporting the timeout rather than the
	// crash loses the one detail a caller acts on.
	if fault != nil {
		return results, info, &runtimeError{
			Object: t.GetName(), Script: fault.GetScript(),
			Detail: fault.GetReason(), OOM: fault.GetOutOfMemory(),
		}
	}
	if !sentinel {
		return results, info, fmt.Errorf("the script did not say DONE within %v", r.Timeout)
	}
	return results, info, nil
}

// Compile installs the script WITHOUT STARTING IT and returns the
// compiler's verdict.
//
// compile_only is a capability rather than a promise, and a backend
// without it gets an error rather than a run: the caller asked for the
// object NOT to be disturbed, and quietly running the script instead
// would destroy the linkset data the answer is about.  Not being able to
// ask is fatal one level up, which is the right answer -- the caller is
// already handling a failed run and a diagnosis that cannot be obtained
// is not one to guess at.
func (r *scriptRunner) Compile(src string) (*compilation, error) {
	if !r.compileOnly {
		return nil, fmt.Errorf("this backend cannot compile a script without running it, " +
			"so whether the code itself is refused cannot be asked without disturbing " +
			"the measurement")
	}
	start := time.Now()
	var (
		ok   bool
		errs []string
	)
	err := r.stream(&scriptv1.RunRequest{
		// A separate name from the running benchmark's, so asking whether
		// something compiles cannot replace the script a measurement is
		// using.
		Target: r.targets[0].GetId(), Name: scriptName + "-compile",
		Source: src, CompileOnly: true, TimeoutSeconds: r.seconds(),
	}, func(ev *scriptv1.RunEvent) {
		if c := ev.GetCompiled(); c != nil {
			ok, errs = c.GetOk(), c.GetErrors()
		}
	})
	if err != nil {
		return nil, err
	}
	return &compilation{OK: ok, Errors: errs, Elapsed: time.Since(start)}, nil
}

// stream makes one Run call and hands every event to fn.
func (r *scriptRunner) stream(req *scriptv1.RunRequest, fn func(*scriptv1.RunEvent)) error {
	ctx := context.Background()
	if r.Timeout > 0 {
		// Room beyond the timeout the backend was given, so that a
		// backend reporting its own overrun is not cut off instead.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout+30*time.Second)
		defer cancel()
	}
	s, err := r.c.Run(ctx, req)
	if err != nil {
		return err
	}
	for {
		ev, err := s.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		fn(ev)
	}
}

// Close gives the lease back and undoes whatever opening it took.
func (r *scriptRunner) Close() error {
	for i := len(r.shut) - 1; i >= 0; i-- {
		r.shut[i]()
	}
	r.shut = nil
	return nil
}
