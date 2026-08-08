package main

// The transport, over a grid that is not there.
//
// What the runner has to provide is small -- run a script and return its
// lines, ask whether a script compiles, and tell the two kinds of refusal
// apart -- and every one of those is a decision a benchmark acts on.  A
// compile refusal means try a smaller script; a Stack-Heap Collision may
// mean the reading IS the limit being looked for; a script that said
// nothing is neither.  Confusing any two of them produces a plausible
// number rather than a failure, which is why they are separated here and
// not in the code that reads them.
//
// Everything runs against fake_test.go.  See there for what stands in for
// the three protocols a run needs.  Nothing here runs in parallel: the
// flags, the run cache and the compiler's verdict are all package level,
// which is what a program with one benchmark to run wants and what makes
// two tests at once two tests sharing a benchmark.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestARunReturnsWhatTheScriptSaidAndNothingElse: the runner hands back
// every line, so the benchmark can apply its own convention to them --
// but the sentinel is the script talking to us rather than to the reader,
// and the debug channel is the simulator commenting on the script rather
// than the script speaking.  Neither is output.
func TestARunReturnsWhatTheScriptSaidAndNothingElse(t *testing.T) {
	resetFlags()
	b, f := newFakeRunner(t, 474, 368, 0)

	results, info, err := b.Send(buildScript(0, 474))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(info) != 0 {
		t.Errorf("INFO lines were separated out without Info set: %q", info)
	}
	for _, line := range results {
		if strings.Contains(line, "DONE") {
			t.Errorf("the sentinel was returned as output: %q", line)
		}
	}

	var r Results
	absorbResults(results, &r)
	if want := f.mem(0, 474); r.Base != want {
		t.Errorf("the base script read %d, want %d", r.Base, want)
	}
	if f.ran != 1 {
		t.Errorf("%d scripts were sent for one run", f.ran)
	}
}

// TestInfoLinesAreSeparatedWhenAsked: -v is for watching a benchmark work
// rather than reading its answer, and the commentary has to be told from
// the measurements or the reader has to grep for the difference.
func TestInfoLinesAreSeparatedWhenAsked(t *testing.T) {
	resetFlags()
	b, _ := newFakeRunner(t, 474, 368, 0)
	b.Info = true

	results, info, err := b.Send(buildScript(4, 474))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(info) == 0 {
		t.Fatal("Info was set and nothing was separated out")
	}
	for _, s := range info {
		if strings.HasPrefix(s, "INFO:") {
			t.Errorf("the marker was left on a separated line: %q", s)
		}
	}
	for _, s := range results {
		if strings.Contains(s, "INFO:") {
			t.Errorf("commentary was returned as a measurement: %q", s)
		}
	}

	// The measurements still arrive: separating the commentary must not
	// take a reading with it.
	var r Results
	absorbResults(results, &r)
	if r.Test == 0 || r.Size == 0 {
		t.Errorf("the readings did not survive the separation: %+v", r)
	}
}

// TestAScriptSecondLifeWillNotCompileComesBackAsARefusal: "too big" and
// "not valid LSL" arrive as the same event and often as the same words,
// so what a caller needs from here is that a refusal happened at all --
// deciding which one it was is its business, and Compile is how it finds
// out.
func TestAScriptSecondLifeWillNotCompileComesBackAsARefusal(t *testing.T) {
	resetFlags()
	b, f := newFakeRunner(t, 474, 368, 0)
	f.refuse[b.obj.ID] = []string{"(1,1) : ERROR : Syntax error"}

	_, _, err := b.Send(buildScript(1, 474))
	var ce *compileError
	if !errors.As(err, &ce) {
		t.Fatalf("Send = %v (%T), want a *compileError", err, err)
	}
	if !strings.Contains(ce.Error(), "Syntax error") {
		t.Errorf("the refusal lost what Second Life said: %v", ce)
	}
	// A run-time error is a different thing entirely and must not be
	// found in a script that never started.
	var re *runtimeError
	if errors.As(err, &re) {
		t.Error("a compile refusal presented as a run-time error")
	}

	// Second Life refusing without saying why is the case that actually
	// happens at 512 copies, where its entire message is "Internal server
	// compile error" -- and sometimes there is not even that.
	f.refuse[b.obj.ID] = []string{}
	f.refuse[b.obj.ID] = nil
	c := &compilation{}
	if got := c.Error(); !strings.Contains(got, "said nothing about why") {
		t.Errorf("a silent refusal reads as %q", got)
	}
}

// TestARunTimeErrorBeatsTheSilenceItCaused: a script that crashed was
// never going to say DONE, so reporting the timeout rather than the crash
// loses the one detail a caller acts on -- and a Stack-Heap Collision is
// the detail the copy search turns on.
func TestARunTimeErrorBeatsTheSilenceItCaused(t *testing.T) {
	resetFlags()
	b, f := newFakeRunner(t, 474, 368, 0)
	b.Timeout = 2 * time.Second
	f.fault[b.obj.ID] = "Stack-Heap Collision"

	_, _, err := b.Send(buildScript(256, 474))
	var re *runtimeError
	if !errors.As(err, &re) {
		t.Fatalf("Send = %v (%T), want a *runtimeError", err, err)
	}
	if !re.StackHeap() {
		t.Errorf("%v was not recognised as running out of memory", re)
	}
	if re.Script != scriptName {
		t.Errorf("the fault was attributed to %q, want the benchmark script", re.Script)
	}
	if !strings.Contains(re.Error(), "Stack-Heap Collision") {
		t.Errorf("the fault does not say what happened: %v", re)
	}

	// Another kind of fault is not a size limit, and treating it as one
	// would halve the copy count for ever without ever succeeding.
	other := &runtimeError{Object: "a prim", Script: scriptName, Detail: "Math Error"}
	if other.StackHeap() {
		t.Errorf("%v was taken for running out of memory", other)
	}
	// A fault with no reason still has to produce a sentence.
	bare := &runtimeError{Object: "a prim", Script: scriptName}
	if got := bare.Error(); !strings.Contains(got, "Script run-time error") || strings.HasSuffix(got, ": ") {
		t.Errorf("a reasonless fault reads as %q", got)
	}
}

// TestAScriptThatSaysNothingIsATimeoutAndNotAFault: the DONE contract is
// enforced here -- nothing on the far side decides a script has finished
// -- so a script that ran and never spoke has to be reported as the wait
// it was, naming the bound that was reached.
func TestAScriptThatSaysNothingIsATimeoutAndNotAFault(t *testing.T) {
	resetFlags()
	b, f := newFakeRunner(t, 474, 368, 0)
	b.Timeout = time.Second
	f.silent[b.obj.ID] = true

	results, _, err := b.Send(buildScript(0, 474))
	if err == nil {
		t.Fatal("a script that never said DONE was taken for one that finished")
	}
	if !strings.Contains(err.Error(), "did not say DONE within 1s") {
		t.Errorf("Send = %v, want it to name the bound it waited to", err)
	}
	// What it did say is still returned: a script that stopped part way
	// through has usually said the interesting part already.
	if len(results) == 0 {
		t.Error("the lines heard before the timeout were thrown away")
	}
	var re *runtimeError
	if errors.As(err, &re) {
		t.Error("a silence presented as a run-time error")
	}
}

// TestARunThatCannotReachTheGridIsNotAReading: everything above this
// treats an error as something to retry smaller, so a circuit that has
// gone away must not arrive looking like a script that was too big.
func TestARunThatCannotReachTheGridIsNotAReading(t *testing.T) {
	resetFlags()
	b, f := newFakeRunner(t, 474, 368, 0)

	f.mu.Lock()
	f.sendErr = fmt.Errorf("the circuit is gone")
	f.mu.Unlock()

	_, _, err := b.Send(buildScript(0, 474))
	if err == nil {
		t.Fatal("a run reached a grid that is not answering")
	}
	var ce *compileError
	var re *runtimeError
	if errors.As(err, &ce) || errors.As(err, &re) {
		t.Errorf("a transport failure presented as %v", err)
	}
}

// TestCompilingDoesNotRunAnything: the compile check exists to be asked
// about a script while a measurement is in progress in the same object,
// so it must install under its own name and start nothing -- otherwise
// asking the question destroys the linkset data the answer is about.
func TestCompilingDoesNotRunAnything(t *testing.T) {
	resetFlags()
	b, f := newFakeRunner(t, 474, 368, 0)

	// A base run first, so the object is carrying a reading that a
	// compile check must not disturb.
	if _, _, err := b.Send(buildScript(0, 474)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	f.mu.Lock()
	before, ran := f.objects[100].mem, f.ran
	f.mu.Unlock()

	c, err := b.Compile(buildScript(1, 474))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !c.OK || len(c.Errors) != 0 {
		t.Errorf("Compile = %+v, want the script accepted", c)
	}
	if c.Elapsed <= 0 {
		t.Error("Compile did not say how long the ask took")
	}

	f.mu.Lock()
	after, alsoRan := f.objects[100].mem, f.ran
	f.mu.Unlock()
	if after != before {
		t.Errorf("the object's reading moved from %d to %d over a compile", before, after)
	}
	if alsoRan != ran {
		t.Error("the compile check started the script")
	}

	// A refusal is a RESULT and not an error: err is for not being able
	// to ask at all.
	f.refuse[b.obj.ID] = []string{"Internal server compile error"}
	if c, err = b.Compile(buildScript(512, 474)); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if c.OK || !strings.Contains(c.Error(), "Internal server") {
		t.Errorf("Compile = %+v, want the refusal as a result", c)
	}

	// Not being able to ask is the other case, and it is an error.
	f.mu.Lock()
	f.capErr = fmt.Errorf("the capability is not answering")
	f.mu.Unlock()
	if _, err := b.Compile(buildScript(1, 474)); err == nil {
		t.Error("Compile answered without being able to ask")
	}
}

// TestClosingUndoesWhateverGettingTheObjectTook: a benchmark that failed
// is exactly when a stray prim is least welcome, and the session goes
// with it -- a process that left one open would hold the auto objects
// until it was killed.
func TestClosingUndoesWhateverGettingTheObjectTook(t *testing.T) {
	resetFlags()
	b, _ := newFakeRunner(t, 474, 368, 0)

	undone := false
	b.cleanup = func() { undone = true }
	if err := b.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if !undone {
		t.Error("Close did not undo what getting the object took")
	}

	// A runner that took nothing to set up closes just the session.
	b2, _ := newFakeRunner(t, 474, 368, 0)
	if err := b2.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// ------------------------------------------ what a refusal means

// TestABenchmarkAsksTheCompilerWhichRefusalThisIs: a script too large to
// compile and a script that is not valid LSL come back from Second Life
// as the same event -- at 512 copies of a real shape its entire message
// is "Internal server compile error", with no line and no column.  One
// copy is the smallest script a benchmark can be, so whether THAT
// compiles is what tells the two apart, and it is asked at most once.
func TestABenchmarkAsksTheCompilerWhichRefusalThisIs(t *testing.T) {
	resetFlags()
	b, f := newFakeRunner(t, 474, 368, 0)
	f.refuseOver = 64

	oneCopyVerdict, spentCompiles = nil, 0
	t.Cleanup(func() { oneCopyVerdict, spentCompiles = nil, 0 })

	var r Results
	if got := runShrink(b, 128, 474, &r); got != 64 {
		t.Errorf("runShrink(128) = %d, want 64 (128 refused, 64 taken)", got)
	}
	if spentCompiles != 1 {
		t.Errorf("spent %d compiles, want the one that diagnosed the refusal", spentCompiles)
	}
	if v := oneCopyCompiles(b, 474); !v.OK {
		t.Errorf("one copy of the reference shape was refused: %v", v)
	}
	if spentCompiles != 1 {
		t.Error("the diagnosis was asked for twice; the code under test does not change")
	}
}

// TestACollisionIsARunTimeLimitAndStillMeansTrySmaller: a Stack-Heap
// Collision is the run-time limit, looser than nothing the compiler said
// -- SL took the script and it ran out of the 64KB a Mono script has.
// Either way a smaller count is the thing to try, so the search backs off
// from both.
func TestACollisionIsARunTimeLimitAndStillMeansTrySmaller(t *testing.T) {
	resetFlags()
	b, f := newFakeRunner(t, 474, 368, 0)
	b.Timeout = 2 * time.Second
	f.faultOver = 4

	oneCopyVerdict, spentCompiles = nil, 0
	t.Cleanup(func() { oneCopyVerdict, spentCompiles = nil, 0 })

	var r Results
	if got := runShrink(b, 16, 474, &r); got != 4 {
		t.Errorf("runShrink(16) = %d, want 4 (16 and 8 collided, 4 ran)", got)
	}
	// Nothing was asked of the compiler: a collision is a script that
	// compiled, so there is nothing to diagnose.
	if spentCompiles != 0 {
		t.Errorf("a collision cost %d compiles", spentCompiles)
	}
}

// TestARunThatFailedForSomeOtherReasonIsNotBackedOffFrom: halving is for
// a script that was too big, and everything else -- a circuit that went
// away, a capability that will not answer -- is not something a smaller
// script fixes.  It comes out as a panic, which main turns into the
// failure the reader sees.
func TestARunThatFailedForSomeOtherReasonIsNotBackedOffFrom(t *testing.T) {
	resetFlags()
	b, f := newFakeRunner(t, 474, 368, 0)

	f.mu.Lock()
	f.sendErr = fmt.Errorf("the circuit is gone")
	f.mu.Unlock()

	var r Results
	if got := recovered(func() { runShrink(b, 8, 474, &r) }); got == nil {
		t.Error("runShrink carried on past a failure a smaller script cannot fix")
	}
	// mustRun is the same rule where an error is not expected at all.
	if got := recovered(func() { mustRun(b, 0, 474, &r) }); got == nil {
		t.Error("mustRun returned from a run that failed")
	}
	// And a diagnosis that cannot be obtained is not one to guess at: the
	// caller is already handling a failed run.
	oneCopyVerdict = nil
	t.Cleanup(func() { oneCopyVerdict = nil })
	if got := recovered(func() { oneCopyCompiles(b, 474) }); got == nil {
		t.Error("oneCopyCompiles answered without being able to ask")
	}
}

// recovered runs fn and answers with what it panicked with, if anything.
func recovered(fn func()) (p any) {
	defer func() { p = recover() }()
	fn()
	return nil
}

// TestShowPrintsTheScriptAndTheCommentary: -v is how a person watches a
// benchmark work, and what they need to see is the source that was sent
// and the INFO lines the harness says about it.
func TestShowPrintsTheScriptAndTheCommentary(t *testing.T) {
	resetFlags()
	b, _ := newFakeRunner(t, 474, 368, 0)

	flags.Show = true
	b.Info = true
	t.Cleanup(func() { flags.Show = false })

	clear(cache)
	t.Cleanup(func() { clear(cache) })

	var r Results
	said := stdoutOf(t, func() { mustRun(b, 4, 474, &r) })
	if !strings.Contains(said, "llGetUsedMemory") {
		t.Errorf("-v did not print the script:\n%s", said)
	}
	if !strings.Contains(said, "INFO: COUNT=4") {
		t.Errorf("-v did not print the harness's commentary:\n%s", said)
	}
}
