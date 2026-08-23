package scripttest_test

// What a run has to say, in what order, and how it ends.

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/quark-idlemind/slgo/proto/scriptv1"
	"github.com/quark-idlemind/slgo/scripttest"
)

// TestARunSaysCompiledFirstAndFinishedExactlyOnceAtTheEnd is the shape
// of every run, and a caller may be written as a loop that assumes it.
// Two Finished events would have such a caller reporting one run as two;
// a Line after Finished would be output nobody prints.
func TestARunSaysCompiledFirstAndFinishedExactlyOnceAtTheEnd(t *testing.T) {
	bothWays(t, func(t *testing.T, r reach) {
		_, c := serve(t, r, scripttest.Options{Memory: scripttest.Memory{Pad: 137, CodeSize: 340}})

		tr, err := collect(t, c, &scriptv1.RunRequest{
			Source: benchScript(4, 137), Done: "DONE", Name: "slbench",
		})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(tr.order) < 3 {
			t.Fatalf("the run said %v, want a compile, some lines and a finish", tr.order)
		}
		if tr.order[0] != "compiled" {
			t.Errorf("the run began with %q, want compiled", tr.order[0])
		}
		if got := tr.order[len(tr.order)-1]; got != "finished" {
			t.Errorf("the run ended with %q, want finished", got)
		}
		if len(tr.compiled) != 1 {
			t.Errorf("%d Compiled events, want exactly one", len(tr.compiled))
		}
		if len(tr.finished) != 1 {
			t.Errorf("%d Finished events, want exactly one", len(tr.finished))
		}
		if !tr.compiled[0].GetOk() {
			t.Errorf("the script did not compile: %v", tr.compiled[0].GetErrors())
		}
		if !tr.finished[0].GetSentinel() {
			t.Error("Finished says the sentinel was not seen, and the script said DONE")
		}
		if !strings.Contains(tr.text(), "RESULT:MEM=") {
			t.Errorf("the object said:\n%s\nwant the benchmark harness's own label", tr.text())
		}
		// Which object spoke, which is worth having when several are talking
		// at once and is the only way a caller can attribute a line.
		if tr.lines[0].GetFrom() == "" || tr.lines[0].GetAtUnixNanos() == 0 {
			t.Errorf("line %v has no speaker or no time", tr.lines[0])
		}
	})
}

// TestAScriptThatWillNotCompileSaysNoLines: there was nothing to run, so
// there is nothing to have said.  A caller that saw lines after a failed
// compile would be reading output from the script that was there before.
func TestAScriptThatWillNotCompileSaysNoLines(t *testing.T) {
	bothWays(t, func(t *testing.T, r reach) {
		s, c := serve(t, r, scripttest.Options{})
		s.SetBehaviour("", scripttest.Behaviour{
			CompileErrors: []string{"(3, 12) : ERROR : Syntax error"},
		})

		tr, err := collect(t, c, &scriptv1.RunRequest{Source: benchScript(1, 5), Done: "DONE"})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(tr.compiled) != 1 || tr.compiled[0].GetOk() {
			t.Fatalf("Compiled = %v, want one refusal", tr.compiled)
		}
		if got := tr.compiled[0].GetErrors(); len(got) != 1 || !strings.Contains(got[0], "Syntax error") {
			t.Errorf("the compiler said %v, want the message it was given, verbatim", got)
		}
		if len(tr.lines) != 0 {
			t.Errorf("a script that would not compile said %d lines, want none", len(tr.lines))
		}
		if len(tr.finished) != 1 || tr.finished[0].GetSentinel() {
			t.Errorf("Finished = %v, want one, with the sentinel unseen", tr.finished)
		}
	})
}

// TestTheModelRefusesAScriptPastItsLimit is the compiler's size limit,
// which a caller backs off from by sending fewer copies.  It arrives on
// the RUN and not on a call of its own because that is where the live
// one arrives: the source goes up, Second Life refuses it, and nothing
// executed.
func TestTheModelRefusesAScriptPastItsLimit(t *testing.T) {
	bothWays(t, func(t *testing.T, r reach) {
		_, c := serve(t, r, scripttest.Options{Memory: scripttest.Memory{
			Pad: 137, CodeSize: 340, Limit: 16 * 1024,
		}})

		tr, err := collect(t, c, &scriptv1.RunRequest{Source: benchScript(4, 137), Done: "DONE"})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(tr.compiled) != 1 || !tr.compiled[0].GetOk() {
			t.Errorf("4 copies were refused (%v), want them to fit under a 16KB limit", tr.compiled)
		}

		tr, err = collect(t, c, &scriptv1.RunRequest{Source: benchScript(256, 137), Done: "DONE"})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(tr.compiled) != 1 || tr.compiled[0].GetOk() {
			t.Errorf("256 copies compiled (%v), want a refusal past the limit", tr.compiled)
		}
		if len(tr.lines) != 0 {
			t.Errorf("a refused script said %d lines, want none", len(tr.lines))
		}
	})
}

// TestAFaultEndsTheRunUnlessTheCallerSaysToIgnoreIt: a script that
// faulted has stopped, and the sentinel is never coming -- so waiting
// out the timeout would cost a caller a minute and tell it nothing it
// did not already know.  ignore_fault is for the object where something
// else is expected to carry on talking.
func TestAFaultEndsTheRunUnlessTheCallerSaysToIgnoreIt(t *testing.T) {
	bothWays(t, func(t *testing.T, r reach) {
		s, c := serve(t, r, scripttest.Options{Memory: scripttest.Memory{Pad: 137, CodeSize: 340}})
		s.SetBehaviour("", scripttest.Behaviour{Fault: "Math Error"})

		tr, err := collect(t, c, &scriptv1.RunRequest{
			Source: benchScript(1, 137), Done: "DONE", Name: "slbench",
		})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(tr.faults) != 1 {
			t.Fatalf("%d faults, want one", len(tr.faults))
		}
		if tr.faults[0].GetScript() != "slbench" {
			t.Errorf("the fault blames %q, want the script that was sent", tr.faults[0].GetScript())
		}
		if len(tr.lines) != 0 {
			t.Errorf("the run said %d lines after a fault, want none: the script had stopped", len(tr.lines))
		}
		if len(tr.finished) != 1 || tr.finished[0].GetSentinel() {
			t.Errorf("Finished = %v, want one, with the sentinel unseen", tr.finished)
		}

		tr, err = collect(t, c, &scriptv1.RunRequest{
			Source: benchScript(1, 137), Done: "DONE", IgnoreFault: true,
		})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(tr.faults) != 1 || len(tr.lines) == 0 {
			t.Errorf("with ignore_fault the run said %d faults and %d lines, want the fault and then the lines",
				len(tr.faults), len(tr.lines))
		}
		if len(tr.finished) != 1 || !tr.finished[0].GetSentinel() {
			t.Errorf("Finished = %v, want the run to have carried on to the sentinel", tr.finished)
		}
	})
}

// TestRunningOutOfMemoryIsToldApartFromAnOrdinaryFault is the flag a
// benchmark searching for a size limit turns on: out of memory means the
// answer has been found and a smaller script should be tried, while any
// other fault means the run failed.  A backend that reported both the
// same way would have a benchmark halving its copy count over a
// division by zero.
func TestRunningOutOfMemoryIsToldApartFromAnOrdinaryFault(t *testing.T) {
	bothWays(t, func(t *testing.T, r reach) {
		// Collide below Limit, which is the live arrangement: measured on
		// Agni, 256 copies of the reference shape compiled and then collided
		// stack with heap, and 512 were refused outright.
		_, c := serve(t, r, scripttest.Options{Memory: scripttest.Memory{
			Pad: 137, CodeSize: 340, Collide: 16 * 1024, Limit: 48 * 1024,
		}})

		tr, err := collect(t, c, &scriptv1.RunRequest{Source: benchScript(64, 137), Done: "DONE"})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(tr.compiled) != 1 || !tr.compiled[0].GetOk() {
			t.Fatalf("Compiled = %v, want it to compile and then fail at run time", tr.compiled)
		}
		if len(tr.faults) != 1 || !tr.faults[0].GetOutOfMemory() {
			t.Fatalf("faults = %v, want one marked out of memory", tr.faults)
		}

		// The same shape of failure, from an ordinary fault, must not carry
		// the flag.
		s2, c2 := serve(t, r, scripttest.Options{})
		s2.SetBehaviour("", scripttest.Behaviour{Fault: "Math Error"})
		tr, err = collect(t, c2, &scriptv1.RunRequest{Source: benchScript(1, 5), Done: "DONE"})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(tr.faults) != 1 || tr.faults[0].GetOutOfMemory() {
			t.Errorf("faults = %v, want one that is not out of memory", tr.faults)
		}
	})
}

// TestATimeoutIsNotAnErrorAndSaysTheSentinelWasNeverSeen: a script that
// says nothing is a result worth seeing rather than a failure to report.
// A backend that returned an error here would leave the caller with no
// transcript and nothing to print.
func TestATimeoutIsNotAnErrorAndSaysTheSentinelWasNeverSeen(t *testing.T) {
	bothWays(t, func(t *testing.T, r reach) {
		s, c := serve(t, r, scripttest.Options{Memory: scripttest.Memory{Pad: 137, CodeSize: 340}})
		s.SetBehaviour("", scripttest.Behaviour{Silent: true})

		start := time.Now()
		tr, err := collect(t, c, &scriptv1.RunRequest{
			Source: benchScript(0, 137), Done: "DONE", TimeoutSeconds: 2,
		})
		if err != nil {
			t.Fatalf("a run that timed out returned %v, want the transcript and no error", err)
		}
		if len(tr.finished) != 1 || tr.finished[0].GetSentinel() {
			t.Errorf("Finished = %v, want one, saying the sentinel was never seen", tr.finished)
		}
		if len(tr.lines) == 0 {
			t.Error("the lines heard before the timeout were not sent; they are the whole result")
		}
		if took := time.Since(start); took < 2*testSecond {
			t.Errorf("the run gave up after %v, want the %v it was given", took, 2*testSecond)
		}
		if el := tr.finished[0].GetElapsedMillis(); el < 0 {
			t.Errorf("ElapsedMillis = %d", el)
		}
	})
}

// TestCompileOnlyInstallsTheScriptWithoutRunningIt is how a caller gets
// a verdict without disturbing what the object holds -- no linkset data
// written, no measurement in progress spoiled.  The sentinel is
// unseen, because nothing ran to say it.
func TestCompileOnlyInstallsTheScriptWithoutRunningIt(t *testing.T) {
	bothWays(t, func(t *testing.T, r reach) {
		_, c := serve(t, r, scripttest.Options{Memory: scripttest.Memory{Pad: 137, CodeSize: 340}})

		tr, err := collect(t, c, &scriptv1.RunRequest{
			Source: benchScript(2, 137), Done: "DONE", CompileOnly: true,
		})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(tr.compiled) != 1 || !tr.compiled[0].GetOk() {
			t.Fatalf("Compiled = %v, want one verdict", tr.compiled)
		}
		if len(tr.lines) != 0 || len(tr.finished) != 1 || tr.finished[0].GetSentinel() {
			t.Errorf("compile_only said %d lines and finished %v, want nothing to have run",
				len(tr.lines), tr.finished)
		}
	})
}

// TestAskingForCompileOnlyWhereItIsNotHonouredIsAnError: the contract
// says so, and the reason is that the alternative is worse.  Running the
// script anyway would disturb exactly what the caller asked to leave
// alone, and it would do it silently.
func TestAskingForCompileOnlyWhereItIsNotHonouredIsAnError(t *testing.T) {
	bothWays(t, func(t *testing.T, r reach) {
		_, c := serve(t, r, scripttest.Options{NoCompileOnly: true})

		h, err := c.Health(context.Background(), &scriptv1.HealthRequest{})
		if err != nil {
			t.Fatalf("Health: %v", err)
		}
		if h.GetCapabilities().GetCompileOnly() {
			t.Fatal("Capabilities says compile_only is honoured, and it is not")
		}
		_, err = collect(t, c, &scriptv1.RunRequest{
			Source: benchScript(1, 5), Done: "DONE", CompileOnly: true,
		})
		if status.Code(err) != codes.Unimplemented {
			t.Errorf("compile_only on a backend without it = %v, want an error rather than a run", err)
		}
	})
}

// The base a benchmark carries from one run to the next used to live in
// the OBJECT, written by the cnt=0 script into its linkset data and
// divided against by the cnt>0 ones -- and a test here held the backend
// to modelling that.  The script says one number now and the arithmetic
// happens in the program, so there is no per-object memory to keep and
// nothing left for that test to hold anybody to.

// TestATargetFromAnEndedLeaseNamesNothing. The ids are the only proof
// this backend has that a run belongs to a lease the caller still holds,
// and a stale one has to fail rather than run somewhere that now belongs
// to somebody else.
func TestATargetFromAnEndedLeaseNamesNothing(t *testing.T) {
	bothWays(t, func(t *testing.T, r reach) {
		_, c := serve(t, r, scripttest.Options{})

		// Named, because the wait below is what makes this test a test:
		// the pool reports a group as held by whoever said so, and an
		// unnamed caller would leave held at nought from the start.  The
		// lease then has to have really ended before the stale id is
		// tried, and over the in-process client ending one is a goroutine
		// waking up rather than a message going anywhere.
		g, done := lease(t, c, &scriptv1.LeaseRequest{Who: "a caller about to finish"})
		target := g.GetTargets()[0].GetId()
		done()
		eventually(t, "the lease coming back", func() bool { return held(t, c) == 0 })

		_, err := collect(t, c, &scriptv1.RunRequest{
			Target: target, Source: benchScript(0, 5), Done: "DONE",
		})
		if status.Code(err) != codes.FailedPrecondition {
			t.Errorf("running against a target from an ended lease = %v, want FailedPrecondition", err)
		}
	})
}

// TestARunWithNoTargetTakesOneAndGivesItBack is the whole of what a
// one-off script needs, and it saves a caller holding a lease stream to
// run a single script.  Not giving the object back would empty the pool
// one slrun run at a time.
func TestARunWithNoTargetTakesOneAndGivesItBack(t *testing.T) {
	bothWays(t, func(t *testing.T, r reach) {
		_, c := serve(t, r, scripttest.Options{})

		tr, err := collect(t, c, &scriptv1.RunRequest{
			Source: `default { state_entry() { llOwnerSay("hello"); llOwnerSay("DONE"); } }`,
			Done:   "DONE",
		})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if got := tr.text(); !strings.Contains(got, "hello") {
			t.Errorf("the script said:\n%s\nwant what it was written to say", got)
		}
		if len(tr.finished) != 1 || !tr.finished[0].GetSentinel() {
			t.Errorf("Finished = %v, want one, with the sentinel seen", tr.finished)
		}
		if n := held(t, c); n != 0 {
			t.Errorf("%d groups still held after a one-off run, want none", n)
		}
	})
}

// TestCancellingTheCallStopsTheRun. A caller that has seen enough --
// slrun interrupted, a benchmark that gave up -- stops reading, and
// the backend has to notice rather than go on delivering to nobody.
//
// On the wire only, and this one is worth being exact about.  What is
// being cancelled is a run that is STILL GOING, in reaction to an event
// already heard, and the in-process client hands a run's events back
// when the run has finished.  The same test through Direct would cancel
// a run that was already over, notice that no more events came, and pass
// -- which is a pass for the wrong reason and worse than no test.
func TestCancellingTheCallStopsTheRun(t *testing.T) {
	s, c := serve(t, overAPipe, scripttest.Options{Memory: scripttest.Memory{Pad: 137, CodeSize: 340}})
	s.SetBehaviour("", scripttest.Behaviour{LineDelay: 50 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := c.Run(ctx, &scriptv1.RunRequest{
		Source: benchScript(0, 137), Done: "DONE", TimeoutSeconds: 100,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	cancel()
	for {
		if _, err := stream.Recv(); err != nil {
			if status.Code(err) != codes.Canceled {
				t.Errorf("after cancelling, the stream ended with %v, want Canceled", err)
			}
			break
		}
	}
	// The object it was using has to come back, or a cancelled run would
	// cost the pool a group.
	eventually(t, "the object coming back", func() bool { return held(t, c) == 0 })
}

// ------------------------------------------------ an instrument that lies

// said reads a number the harness labelled out of a transcript, which is
// the only way a caller gets a reading at all: the script says it.
func said(t *testing.T, tr *transcript, label string) int {
	t.Helper()
	for _, l := range tr.lines {
		i := strings.Index(l.GetText(), label)
		if i < 0 {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(l.GetText()[i+len(label):]))
		if err != nil {
			t.Fatalf("%q does not carry a number: %v", l.GetText(), err)
		}
		return n
	}
	t.Fatalf("nothing said %s:\n%s", label, tr.text())
	return 0
}

// TestTheSameScriptCanBeReadTwiceAndAnswerDifferently: llGetUsedMemory
// has been seen to answer one whole block high for a single ask and
// truthfully for every ask after it -- on 2026-08-03, the one-copy
// reference script at pad 602 read 6436 where it reads 5924 every other
// time.  A caller's padding search is a chain of comparisons between
// readings, so one bad reading looks precisely like the memory having
// grown; slbench confirms a crossing by re-reading it for exactly that
// reason.  Without a hook here the contract cannot present the event at
// all -- it cannot be provoked live to order -- and the code written to
// survive it is unreachable from the caller's side of the seam.
func TestTheSameScriptCanBeReadTwiceAndAnswerDifferently(t *testing.T) {
	bothWays(t, func(t *testing.T, r reach) {
		mem := scripttest.Memory{Pad: 137, CodeSize: 340}
		fired := 0
		_, c := serve(t, r, scripttest.Options{
			Memory: mem,
			Noise: func(cnt, pad, reading int) int {
				if cnt == 1 && pad == 137 && fired == 0 {
					fired++
					return reading + 512
				}
				return reading
			},
		})

		g, done := lease(t, c, &scriptv1.LeaseRequest{Who: "a search"})
		defer done()
		target := g.GetTargets()[0].GetId()

		read := func() int {
			t.Helper()
			tr, err := collect(t, c, &scriptv1.RunRequest{
				Target: target, Source: benchScript(1, 137), Done: "DONE",
			})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			return said(t, tr, "RESULT:MEM=")
		}

		first, second := read(), read()
		if fired != 1 {
			t.Fatalf("the hook fired %d times, so this test asserts nothing", fired)
		}
		if first == second {
			t.Fatalf("both asks read %d; a caller cannot see the event this exists for", first)
		}
		if want := mem.Reading(1, 137); second != want {
			t.Errorf("the second ask read %d, want the truth %d: the hook lies once, "+
				"and a hook that went on lying is a different instrument, not a noisy one",
				second, want)
		}
		if first != second+512 {
			t.Errorf("the bad reading was %d against %d, want it a whole block high: "+
				"memory moves a block at a time and so does a misreading of it", first, second)
		}
	})
}

// TestNoiseDoesNotMoveTheCompilersRefusal: the hook stands in for
// llGetUsedMemory misreporting a script that ran, and the compiler's
// limit is a judgement about the source made before anything runs.  If
// noise moved the refusal too, a caller backing off from a refusal would
// be backing off from the very thing it is supposed to treat as noise --
// and its copy count would then depend on it.
func TestNoiseDoesNotMoveTheCompilersRefusal(t *testing.T) {
	bothWays(t, func(t *testing.T, r reach) {
		mem := scripttest.Memory{Pad: 137, CodeSize: 340, Limit: 6 * 1024}
		if r := mem.Reading(1, 137); r > mem.Limit || r+2*512 <= mem.Limit {
			t.Fatalf("the reference reads %d against a limit of %d; this case needs one "+
				"that fits and would not fit if the noise counted", r, mem.Limit)
		}
		_, c := serve(t, r, scripttest.Options{
			Memory: mem,
			Noise:  func(cnt, pad, reading int) int { return reading + 2*512 },
		})

		tr, err := collect(t, c, &scriptv1.RunRequest{Source: benchScript(1, 137), Done: "DONE"})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(tr.compiled) != 1 || !tr.compiled[0].GetOk() {
			t.Fatalf("Compiled = %v, want the script taken: what it really costs is under the limit",
				tr.compiled)
		}
		if got, want := said(t, tr, "RESULT:MEM="), mem.Reading(1, 137)+2*512; got != want {
			t.Errorf("the run reported %d, want the noisy %d", got, want)
		}
	})
}
