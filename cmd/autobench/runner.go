package main

// The Second Life transport: how a benchmark script gets into the grid
// and what it said comes back.
//
// This is the part that was slrund's, and is now sl's.  It is one
// implementation of backend and the only one that talks to Second Life;
// see backend.go for what a benchmark asks of any of them, and script.go
// for the one that goes through the script.v1 contract instead.
//
// # A session per object
//
// The objects a benchmark runs in need not belong to one avatar, so
// each is held with the session that can reach it.  It used to be one
// session and a list of objects, back when a benchmark carried its base
// reading between scripts through the measured object's LINKSET DATA:
// that made one object special, and one avatar enough.  The script
// reports one number now and the arithmetic is done here, so a reading
// is a reading wherever it was taken -- and what a benchmark asks for is
// N places to run scripts, exactly as automate does.

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

// place is one object and the session that reaches it.  Two places can
// be two avatars' -- which is why the session travels with the object
// rather than sitting beside the list.
type place struct {
	s   *sl.Session
	obj *sl.Object
}

// runner runs benchmark scripts in the objects of a live grid.
type runner struct {
	// places[0] is where the measured sequence runs and the rest are
	// spares, for readings that can be taken at the same time.  The
	// first is first and nothing more: no object is special to the
	// arithmetic any more.
	places []place

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
	return r.sendIn(r.places[0], src)
}

// SendSpare runs the script in the nth spare object.
func (r *runner) SendSpare(n int, src string) (results, info []string, err error) {
	return r.sendIn(r.places[n+1], src)
}

func (r *runner) Spares() int { return len(r.places) - 1 }

// Grid: these readings are Second Life's, which is what makes a padding
// found here worth remembering in a file later benchmarks read.
func (r *runner) Grid() bool { return true }

// sendIn runs a script in a named place.
func (r *runner) sendIn(p place, src string) (results, info []string, err error) {
	obj := p.obj
	ctx := context.Background()
	if r.Timeout > 0 {
		// Room for sl to report its own overrun rather than having the
		// call cancelled out from under it.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout+30*time.Second)
		defer cancel()
	}

	res, err := p.s.Run(ctx, sl.Script{
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
	up, err := r.places[0].s.InstallScript(ctx,
		r.places[0].obj, scriptName+"-compile", src, false)
	if err != nil {
		return nil, err
	}
	return &compilation{OK: up.Compiled, Errors: up.Errors, Elapsed: time.Since(start)}, nil
}

// Close gives back whatever getting the places took, which includes the
// sessions: a grant covering several avatars holds a session for each,
// and they go back together with the grant.
func (r *runner) Close() error {
	if r.cleanup != nil {
		r.cleanup()
	}
	return nil
}
