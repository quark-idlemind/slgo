package main

// The transport, over a grid that is not there.
//
// What the runner has to provide is small -- run a script and return its
// lines, ask whether a script compiles, and tell the two kinds of refusal
// apart -- and each of those is a different answer.  A compile refusal
// means the script was not taken; a Stack-Heap Collision means it was,
// and ran out of memory; a script that said nothing is neither.  Confusing
// any two of them names the wrong cause, which is why they are separated
// here and not in the code that reads them.
//
// Everything runs against fake_test.go.  See there for what stands in for
// the three protocols a run needs.  Nothing here runs in parallel: the
// flags, the run cache and the counters are all package level,
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
	mem, ok := absorbResults(results, &r)
	if want := f.mem(0, 474); !ok || mem != want {
		t.Errorf("the base script read %d (found=%v), want %d", mem, ok, want)
	}
	if f.ran != 1 {
		t.Errorf("%d scripts were sent for one run", f.ran)
	}
}

// TestInfoLinesAreSeparatedWhenAsked: -v is for watching a benchmark work
// rather than reading its answer, and the commentary has to be told from
// the measurements or the reader has to grep for the difference.
//
// The commentary is the fake's here.  The benchmark harness says none of
// its own any more -- it reports one number and the program works out
// the rest -- but sifting what a script says is not about the harness:
// the code under test can say whatever it likes.
func TestInfoLinesAreSeparatedWhenAsked(t *testing.T) {
	resetFlags()
	b, f := newFakeRunner(t, 474, 368, 0)
	f.commentary = "COUNT=4"
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

	// The measurement still arrives: separating the commentary must not
	// take the reading with it.
	var r Results
	mem, ok := absorbResults(results, &r)
	if !ok || mem == 0 {
		t.Errorf("the reading did not survive the separation: %d, %v", mem, ok)
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
	f.refuse[b.places[0].obj.ID] = []string{"(1,1) : ERROR : Syntax error"}

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
	f.refuse[b.places[0].obj.ID] = []string{}
	f.refuse[b.places[0].obj.ID] = nil
	c := &compilation{}
	if got := c.Error(); !strings.Contains(got, "said nothing about why") {
		t.Errorf("a silent refusal reads as %q", got)
	}
}

// TestARunTimeErrorBeatsTheSilenceItCaused: a script that crashed was
// never going to say DONE, so reporting the timeout rather than the crash
// loses the one detail a caller acts on: whether the script ran out of
// memory.
func TestARunTimeErrorBeatsTheSilenceItCaused(t *testing.T) {
	resetFlags()
	b, f := newFakeRunner(t, 474, 368, 0)
	b.Timeout = 2 * time.Second
	f.fault[b.places[0].obj.ID] = "Stack-Heap Collision"

	_, _, err := b.Send(buildScript(256, 474))
	var re *runtimeError
	if !errors.As(err, &re) {
		t.Fatalf("Send = %v (%T), want a *runtimeError", err, err)
	}
	if !re.OutOfMemory() {
		t.Errorf("%v was not recognised as running out of memory", re)
	}
	if re.Script != scriptName {
		t.Errorf("the fault was attributed to %q, want the benchmark script", re.Script)
	}
	if !strings.Contains(re.Error(), "Stack-Heap Collision") {
		t.Errorf("the fault does not say what happened: %v", re)
	}

	// Another kind of fault is not a size limit, and must not be taken
	// for one.
	//
	// Sent through the same path rather than built by hand: what used to
	// be asserted here was that a runtimeError classified its own reason,
	// and it no longer does -- sl reads the simulator's words and the
	// verdict is carried down.  A struct with the field left false would
	// prove nothing about whether anything sets it.
	f.fault[b.places[0].obj.ID] = "Math Error"
	_, _, err = b.Send(buildScript(256, 474))
	var other *runtimeError
	if !errors.As(err, &other) {
		t.Fatalf("Send = %v (%T), want a *runtimeError", err, err)
	}
	if other.OutOfMemory() {
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
	f.silent[b.places[0].obj.ID] = true

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

// TestARunThatCannotReachTheGridIsNotAReading: a circuit that has gone
// away says nothing about the script, so it must not arrive looking like
// one that was refused or one that crashed.
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
// so it must start nothing -- and it is counted as a compile, not as a
// run, for the Spent line.
func TestCompilingDoesNotRunAnything(t *testing.T) {
	resetFlags()
	b, f := newFakeRunner(t, 474, 368, 0)
	spentCompiles = 0
	t.Cleanup(func() { spentCompiles = 0 })

	// A run first, so that the count of scripts started has one in it
	// that the compile check must not add to.
	if _, _, err := b.Send(buildScript(0, 474)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	f.mu.Lock()
	ran := f.ran
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
	alsoRan := f.ran
	f.mu.Unlock()
	if alsoRan != ran {
		t.Error("the compile check started the script")
	}
	if spentCompiles != 1 {
		t.Errorf("spentCompiles = %d after one compile, want 1", spentCompiles)
	}

	// A refusal is a RESULT and not an error: err is for not being able
	// to ask at all.
	f.refuse[b.places[0].obj.ID] = []string{"Internal server compile error"}
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

// TestACompileThroughAScriptBackendIsCounted: the Spent line's compiles
// are counted on either transport, and only when the backend answered.
func TestACompileThroughAScriptBackendIsCounted(t *testing.T) {
	resetFlags()
	b := offline(t, 474, 368)
	spentCompiles = 0
	t.Cleanup(func() { spentCompiles = 0 })

	c, err := b.Compile(buildScript(1, 474))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !c.OK {
		t.Errorf("Compile = %+v, want the script accepted", c)
	}
	if spentCompiles != 1 {
		t.Errorf("spentCompiles = %d after one compile, want 1", spentCompiles)
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

	// A runner that took nothing to set up has nothing to undo.
	b2, _ := newFakeRunner(t, 474, 368, 0)
	if err := b2.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// ---------------------------------------------------- what -vvv shows

// TestShowPrintsTheScriptAndTheCommentary: -vvv is how a person watches
// a benchmark work, and what they need to see is the source that was
// sent and the INFO lines the script said.
func TestShowPrintsTheScriptAndTheCommentary(t *testing.T) {
	resetFlags()
	b, f := newFakeRunner(t, 474, 368, 0)
	f.commentary = "COUNT=4"

	flags.V = 3
	b.Info = true
	t.Cleanup(func() { flags.V = 0 })

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
