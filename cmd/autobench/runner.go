package main

// The Second Life transport: how a benchmark script gets into the grid
// and what it said comes back.
//
// This is the part that was slrund's, and is now sl's.  It is one
// implementation of backend and the only one that talks to Second Life;
// see backend.go for what a benchmark asks of any of them, and script.go
// for the one that goes through the script.v1 contract instead.
//
// # One object, every run
//
// A benchmark carries its base reading from the cnt=0 script to the
// cnt>0 scripts through the object's LINKSET DATA, so every run of one
// benchmark has to happen in the SAME object or the difference being
// measured is between two unrelated numbers.  Under slrund that was a
// guarantee the server made and the client checked.  Here it is
// structural: the runner holds one object for its lifetime, and there
// is no pool to hand it a different one.

import (
	"context"
	"fmt"
	"time"

	"github.com/quark-idlemind/slgo/sl"
)

// scriptName is what the benchmark script is called inside the object.
// It is fixed, so each run replaces the last rather than filling the
// object with numbered copies -- and it is what a run-time error names,
// which is how a fault is attributed to us and not to whatever else the
// object may be running.
const scriptName = "autobench"

var _ backend = (*runner)(nil)

// runner runs benchmark scripts in one object of a live grid session.
type runner struct {
	s   *sl.Session
	obj *sl.Object

	// spare are further objects to run in, for probes that can be
	// taken at the same time.  They are never obj: obj is the object
	// the measured sequence runs in, and its LINKSET DATA carries the
	// base reading from the cnt=0 script to the cnt>0 ones.  A probe
	// dropped into it would overwrite that.
	spare []*sl.Object

	// Timeout bounds one run.  Zero means sl's own default.
	Timeout time.Duration

	// Info surfaces lines containing "INFO:" separately from the rest,
	// matching autobench's convention for out-of-band commentary.
	Info bool

	// cleanup undoes whatever getting the object took.
	cleanup func()
}

// Send runs the script in the measured object.
func (r *runner) Send(src string) (results, info []string, err error) {
	return r.sendIn(r.obj, src)
}

// SendSpare runs the script in the nth spare object.
func (r *runner) SendSpare(n int, src string) (results, info []string, err error) {
	return r.sendIn(r.spare[n], src)
}

func (r *runner) Spares() int { return len(r.spare) }

// Grid: these readings are Second Life's, which is what makes a padding
// found here worth remembering in a file later benchmarks read.
func (r *runner) Grid() bool { return true }

// sendIn runs a script in a named object.
func (r *runner) sendIn(obj *sl.Object, src string) (results, info []string, err error) {
	ctx := context.Background()
	if r.Timeout > 0 {
		// Room for sl to report its own overrun rather than having the
		// call cancelled out from under it.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout+30*time.Second)
		defer cancel()
	}

	res, err := r.s.Run(ctx, sl.Script{
		In:      obj,
		Name:    scriptName,
		Source:  src,
		Done:    "DONE",
		Timeout: r.Timeout,
	})
	if err != nil {
		return nil, nil, err
	}
	if !res.Compiled {
		return nil, nil, &compileError{Errors: res.Errors}
	}

	said := make([]spoken, 0, len(res.Lines))
	for _, l := range res.Lines {
		said = append(said, spoken{text: l.Text, debug: l.Debug()})
	}
	results, info = sift(said, r.Info)

	// A fault beats the silence it caused: a script that crashed was
	// never going to say DONE, and reporting the timeout rather than
	// the crash loses the one detail a caller acts on.
	if res.Fault != nil {
		return results, info, &runtimeError{
			Object: obj.Name, Script: res.Fault.Script,
			Detail: res.Fault.Reason,
			// sl reads the simulator's words; nothing above here has to.
			OOM: res.Fault.OutOfMemory(),
		}
	}
	if !res.Finished {
		return results, info, fmt.Errorf("the script did not say DONE within %v", r.Timeout)
	}
	return results, info, nil
}

// Compile installs the script WITHOUT STARTING IT and returns Second
// Life's verdict.
//
// Nothing runs, so nothing the object holds is disturbed: no linkset
// data is written, no chat is produced, and a measurement in progress
// in that object is not affected.  A failure to compile is a RESULT and
// not an error -- err is for not being able to ask.
func (r *runner) Compile(src string) (*compilation, error) {
	ctx := context.Background()
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout+30*time.Second)
		defer cancel()
	}

	// A separate name from the running benchmark's, so asking whether
	// something compiles cannot replace the script a measurement is
	// using.
	start := time.Now()
	up, err := r.s.InstallScript(ctx, r.obj, scriptName+"-compile", src, false)
	if err != nil {
		return nil, err
	}
	return &compilation{OK: up.Compiled, Errors: up.Errors, Elapsed: time.Since(start)}, nil
}

func (r *runner) Close() error {
	if r.cleanup != nil {
		r.cleanup()
	}
	return r.s.Close()
}
