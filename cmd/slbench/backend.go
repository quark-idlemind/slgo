package main

// What the benchmark needs of whatever runs its scripts.
//
// The measurement machinery -- the padding search, the block arithmetic,
// the copy count, the backing off -- is about LSL and not about how a
// script reaches whatever runs it.  This is the line that says so.
// Everything above it works in readings and refusals; everything below
// it is a transport, and there are three: a Second Life session
// (runner.go), the script.v1 contract (script.go), and the offline model
// reached through that same contract (--test).
//
// It is stated as an interface rather than left implicit because --test
// used to answer ABOVE the transport -- runScript had a branch that
// returned the model's number without ever calling Send -- so the
// compile-error path, the fault path, absorbResults and the whole of
// runner.go were reachable only with a grid at the far end.  With a seam
// here the model is a backend like any other and the offline tests go
// through the same code the live path does.

import (
	"strings"
	"time"
)

// backend is somewhere benchmark scripts run.
//
// The methods are what a benchmark actually asks for, and no more: run a
// script here, run one over there, ask whether one would compile, and
// say whether the answers are Second Life's.
type backend interface {
	// Send runs the script in the MEASURED object and returns what it
	// said, with the INFO: lines separated out when the backend was told
	// to.
	//
	// A script that will not compile comes back as *compileError and one
	// that faulted as *runtimeError, because a benchmark does different
	// things about them: the first means try a smaller script, the second
	// may mean the reading is the limit being looked for.
	Send(src string) (results, info []string, err error)

	// SendSpare runs the script in the nth spare object, for readings
	// that do not depend on each other and can be taken at once.  Never
	// the measured object: see probe.go.
	//
	// By index and not by object, because what a spare IS differs by
	// transport -- a prim here, an opaque target id there -- and the
	// benchmark only ever wants "another one".
	SendSpare(n int, src string) (results, info []string, err error)

	// Spares is how many of those there are.  Nought is a working
	// benchmark that takes its readings one at a time.
	Spares() int

	// Compile installs the script WITHOUT STARTING IT and returns the
	// compiler's verdict.  Nothing runs, so nothing the measured object
	// holds is disturbed.  A failure to compile is a RESULT; err is for
	// not being able to ask.
	Compile(src string) (*compilation, error)

	// Grid says the readings are a real Second Life session's.
	//
	// It is asked for one thing: whether a padding found here is worth
	// writing to the padding cache, which is a file every later benchmark
	// on this account reads.  A model's paddings are arithmetic and a
	// simulator's are its own, and either would poison that file with
	// numbers that never came from Second Life.  It is the contract's own
	// Capabilities.grid, which is the honest place for the question.
	Grid() bool

	Close() error
}

// spoken is one thing an object said, in the two facts a benchmark cares
// about: the words, and whether it was the script speaking or the region
// commenting on it.
type spoken struct {
	text  string
	debug bool
}

// sift applies the benchmark's conventions to what an object said.
//
// It is HERE and not in the transport, and that is deliberate: nothing
// about running a script requires a script to label its output.  The
// RESULT:/INFO: convention is this program's arrangement with the
// harness it generates, and absorbResults is the other half of it.  A
// backend hands back every line; which of them are measurements is the
// benchmark's business.
//
// The sentinel goes because it is the script talking to us rather than
// to the reader, and the debug channel goes because it is the region
// commenting on the script rather than the script speaking.
func sift(lines []spoken, wantInfo bool) (results, info []string) {
	for _, l := range lines {
		if l.debug || strings.Contains(l.text, "DONE") {
			continue
		}
		if wantInfo && strings.Contains(l.text, "INFO:") {
			info = append(info, strings.TrimSpace(l.text[strings.Index(l.text, "INFO:")+len("INFO:"):]))
			continue
		}
		results = append(results, l.text)
	}
	return results, info
}

// compilation is what a compiler said about a script, and nothing about
// running it.
//
// Errors is the compiler's own words, with its own line and column, and
// is empty when OK is true.  It is not normalised or reworded here: a
// compiler message is evidence, and the only useful form of evidence is
// the verbatim one.
type compilation struct {
	OK     bool
	Errors []string

	// Elapsed is how long the ask took.
	Elapsed time.Duration
}

// Error joins what the compiler said into one string, for a message to a
// person.
func (c *compilation) Error() string {
	if len(c.Errors) == 0 {
		return "the script did not compile and Second Life said nothing about why"
	}
	return strings.Join(c.Errors, "\n")
}

// compileError is a script the compiler would not take, as opposed to
// one that failed to reach the far side or that crashed while running.
// Callers pick it out with errors.As.
//
// It exists because "too big" and "not valid LSL" arrive as the same
// event and often as the same words: a 512-copy benchmark script is
// refused with "Internal server compile error" and nothing else, no line
// and no column.  A caller that can retry smaller needs to see that a
// refusal happened at all; deciding WHICH refusal it was is its
// business, and Compile is how it finds out -- ask about a script small
// enough that size cannot be the reason.
type compileError struct {
	Errors []string
}

func (e *compileError) Error() string {
	c := compilation{Errors: e.Errors}
	return "script does not compile: " + c.Error()
}

// runtimeError is a run-time error raised by the script itself -- "Math
// Error", "Stack-Heap Collision" and the like -- as opposed to a failure
// to compile it or to reach whatever runs it.
type runtimeError struct {
	Object string // the object that raised it, under the name it chats as
	Script string // script name, from the "[script:NAME]" tag
	Detail string // e.g. "Stack-Heap Collision" (may be empty)

	// OOM says the script ran out of the memory a Mono script has, so a
	// smaller one -- fewer copies, a shorter list -- may succeed.  This
	// is the signal a benchmark searching for a size limit turns on.
	//
	// A FACT and not a string to match on.  It used to be worked out here
	// with strings.Contains(Detail, "Stack-Heap"), which meant this
	// program knew a piece of Second Life's vocabulary that nothing else
	// on the caller's side of the seam had any business knowing -- and
	// which would have gone on matching, wrongly and silently, against a
	// simulator that worded it differently.  Now each transport says so:
	// the grid one asks sl.Fault, which is where those words are already
	// understood, and the script.v1 one reads Fault.out_of_memory, which
	// is in the contract for exactly this.
	OOM bool
}

func (e *runtimeError) Error() string {
	s := e.Object + " [script:" + e.Script + "] Script run-time error"
	if e.Detail != "" {
		s += ": " + e.Detail
	}
	return s
}

// OutOfMemory reports whether the script ran out of memory.
func (e *runtimeError) OutOfMemory() bool { return e.OOM }
