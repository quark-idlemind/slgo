package main

// The transport: how a benchmark script gets into Second Life and what
// it said comes back.
//
// This is the part that was slrund's, and is now sl's.  What it has to
// provide is small -- run a script and return its lines, ask whether a
// script compiles, and tell the two kinds of refusal apart -- and none
// of it knows anything about padding or block sizes.  That separation
// is the reason the measurement machinery came over unchanged.
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
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/sl"
)

// scriptName is what the benchmark script is called inside the object.
// It is fixed, so each run replaces the last rather than filling the
// object with numbered copies -- and it is what a run-time error names,
// which is how a fault is attributed to us and not to whatever else the
// object may be running.
const scriptName = "autobench"

// runner runs benchmark scripts in one object.
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

// compilation is what Second Life's compiler said about a script, and
// nothing about running it.
//
// Errors is SL's own words, with SL's own line and column, and is empty
// when OK is true.  It is not normalised or reworded here: a compiler
// message is evidence, and the only useful form of evidence is the
// verbatim one.
type compilation struct {
	OK     bool
	Errors []string

	// Elapsed is how long the ask took.
	Elapsed time.Duration
}

// Error joins what SL said into one string, for a message to a person.
func (c *compilation) Error() string {
	if len(c.Errors) == 0 {
		return "the script did not compile and Second Life said nothing about why"
	}
	return strings.Join(c.Errors, "\n")
}

// compileError is a script Second Life would not compile, as opposed to
// one that failed to reach the grid or that crashed while running.
// Callers pick it out with errors.As.
//
// It exists because "too big" and "not valid LSL" arrive as the same
// event and often as the same words: a 512-copy benchmark script is
// refused with "Internal server compile error" and nothing else, no
// line and no column.  A caller that can retry smaller needs to see
// that a refusal happened at all; deciding WHICH refusal it was is its
// business, and Compile is how it finds out -- ask about a script small
// enough that size cannot be the reason.
type compileError struct {
	Errors []string
}

func (e *compileError) Error() string {
	c := compilation{Errors: e.Errors}
	return "script does not compile: " + c.Error()
}

// runtimeError is a run-time error raised by the script itself --
// "Math Error", "Stack-Heap Collision" and the like -- as opposed to a
// failure to compile it or to reach the grid.
type runtimeError struct {
	Object string // the prim that raised it, under the name it chats as
	Script string // script name, from the "[script:NAME]" tag
	Detail string // e.g. "Stack-Heap Collision" (may be empty)
}

func (e *runtimeError) Error() string {
	s := e.Object + " [script:" + e.Script + "] Script run-time error"
	if e.Detail != "" {
		s += ": " + e.Detail
	}
	return s
}

// StackHeap reports whether the script ran out of memory, so a smaller
// one -- fewer copies, a shorter list -- may succeed.  This is the
// signal a benchmark binary-searching for a size limit turns on.
func (e *runtimeError) StackHeap() bool {
	return strings.Contains(e.Detail, "Stack-Heap")
}

// Send runs the script and returns what it said, with the INFO: lines
// separated out when Info is set.
//
// A script that will not compile comes back as *compileError and one
// that crashed as *runtimeError, because a benchmark does different
// things about them: the first means try a smaller script, the second
// may mean the reading is the limit being looked for.
// Send runs the script in the measured object.
func (r *runner) Send(src string) (results, info []string, err error) {
	return r.sendIn(r.obj, src)
}

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

	for _, l := range res.Lines {
		// The debug channel is the simulator commenting on the script
		// rather than the script speaking, and the sentinel is the
		// script talking to us rather than to the reader.  Neither is
		// output.
		if l.Debug() || strings.Contains(l.Text, "DONE") {
			continue
		}
		if r.Info && strings.Contains(l.Text, "INFO:") {
			info = append(info, strings.TrimSpace(l.Text[strings.Index(l.Text, "INFO:")+len("INFO:"):]))
			continue
		}
		results = append(results, l.Text)
	}

	// A fault beats the silence it caused: a script that crashed was
	// never going to say DONE, and reporting the timeout rather than
	// the crash loses the one detail a caller acts on.
	if res.Fault != nil {
		return results, info, &runtimeError{
			Object: obj.Name, Script: res.Fault.Script, Detail: res.Fault.Reason,
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
