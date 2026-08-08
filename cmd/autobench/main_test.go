package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pborman/getopt/v2"

	"github.com/quark-idlemind/slgo/internal/session"
	"github.com/quark-idlemind/slgo/sl"
)

// The one behavioural difference from the original: autobench/bench returned
// only RESULT: lines, with the marker already stripped. slrun/bench returns
// EVERYTHING a script says, so this program strips the marker and skips
// anything that is not a labelled measurement.
func TestResultPayload(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"RESULT:SIZE=4.5", "SIZE=4.5", true},
		{"RESULT: TITLE=global integer", "TITLE=global integer", true},
		{"RESULT:BASE_MEM=1024", "BASE_MEM=1024", true},

		// Not measurements: commentary, the script's leading blank line, and
		// anything else it chose to say.
		{"INFO:COUNT=8", "", false},
		{"", "", false},
		{"just talking", "", false},
	} {
		got, ok := resultPayload(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("resultPayload(%q) = %q,%v; want %q,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// The generated script must satisfy the runner's contract, or every benchmark
// run would be refused before it started.
func TestGeneratedScriptSaysDone(t *testing.T) {
	if !contains(code, `llOwnerSay("DONE")`) {
		t.Error("the benchmark script must end with a literal DONE")
	}
	if !contains(probeScript, `llOwnerSay("DONE")`) {
		t.Error("the probe script must end with a literal DONE")
	}
}

func contains(hay, needle string) bool {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// --------------------------------------------------------- the arguments

// mkVar is how --globals, --params and --locals get a type without the
// caller writing one: the first letter names it, the way LSL benchmark
// shapes have always been written by hand.
func TestAVariablesTypeComesFromItsFirstLetter(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"a", "list a"},
		{"lst", "list lst"},
		{"f", "float f"},
		{"g", "float g"},
		{"i", "integer i"},
		{"j", "integer j"},
		{"x", "integer x"},
		{"k", "key k"},
		{"q", "quaternion q"},
		{"r", "quaternion r"},
		{"s", "string s"},
		{"v", "vector v"},

		// The case is not the type: a benchmark shape written in capitals
		// means the same thing as one written in small letters, and a
		// caller that had to know which would be finding out by running it.
		{"S", "string S"},
		{"I = 3", "integer I = 3"},
	} {
		got, err := mkVar(tc.in)
		if err != nil {
			t.Errorf("mkVar(%q) = %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("mkVar(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// A letter with no type behind it is a benchmark that would compile
	// into something other than what was asked for, so it is refused
	// rather than guessed at.
	for _, bad := range []string{"", "z", "1", "@"} {
		if got, err := mkVar(bad); err == nil {
			t.Errorf("mkVar(%q) = %q, want a refusal", bad, got)
		}
	}
}

// TestOnlyLabelledMeasurementsAreAbsorbed: the runner hands back every
// line the script said, and the script says a good deal that is not a
// measurement -- so anything without a RESULT: label, and anything
// labelled with a word this does not know, must leave the readings alone.
func TestOnlyLabelledMeasurementsAreAbsorbed(t *testing.T) {
	t.Parallel()
	var r Results
	absorbResults([]string{
		"",
		"INFO:COUNT=8",
		"INFO:BASE_MEM=9999",
		"just talking",
		"RESULT: TITLE=global integer",
		"RESULT:BASE_MEM=5412",
		"RESULT:TEST_MEM=5924",
		"RESULT:SIZE=64.0",
		"RESULT:SOMETHING_ELSE=1",
	}, &r)

	if r.Title != "global integer" || r.Base != 5412 || r.Test != 5924 || r.Size != 64 {
		t.Errorf("absorbResults left %+v", r)
	}

	// INFO:BASE_MEM above is the trap: a cnt>0 script reports the base it
	// divided against as commentary, and reading it as a measurement would
	// overwrite the base the search is comparing to.
	if r.Base == 9999 {
		t.Error("an INFO line was absorbed as a measurement")
	}
}

// --------------------------------------------------------- the filler

// TestTheFillerEmitsExactlyThePadAsked: everything autobench reports is a
// distance between two pads, so a filler that emitted a byte more than it
// was asked for would move every Size by that byte with nothing in the
// output to show it.  The shape of what it emits is asserted rather than
// the byte count, since the byte count is SL's compiler's opinion and not
// available here.
func TestTheFillerEmitsExactlyThePadAsked(t *testing.T) {
	resetFlags()
	t.Cleanup(resetFlags)

	// `integer i;` is unconditional: it backs the +i chain, and emitting
	// it only when there is padding made the 0 -> 1 transition cost a
	// declaration instead of a byte.
	bare := buildScript(0, 0)
	if !contains(bare, "integer i;") {
		t.Error("the filler's variable is not declared when the pad is nought")
	}
	if contains(bare, "i+i") || contains(bare, "jump Z") {
		t.Errorf("a pad of nought emitted filler:\n%s", bare)
	}

	// An even pad is the +i chain alone, two bytes a step.
	if got := buildScript(0, 6); !contains(got, "i+i+i;") || contains(got, "jump Z") {
		t.Errorf("pad 6 should be three 2-byte steps and no jump:\n%s", got)
	}
	// An odd one spends the 5-byte jump/label pair first and pads the
	// even remainder with the chain.
	if got := buildScript(0, minpad); !contains(got, "jump Z; @Z;") || contains(got, "i+i") {
		t.Errorf("pad %d should be the jump/label pair alone:\n%s", minpad, got)
	}
	if got := buildScript(0, 9); !contains(got, "jump Z; @Z;") || !contains(got, "i+i;") {
		t.Errorf("pad 9 should be the pair plus two steps:\n%s", got)
	}
}

// TestTheScriptIsTheHarnessAroundTheCodeUnderTest: the copies are
// numbered so that a shape with a name in it can be repeated, and the
// preamble, postamble and title are the caller's to put around them.
func TestTheScriptIsTheHarnessAroundTheCodeUnderTest(t *testing.T) {
	resetFlags()
	t.Cleanup(resetFlags)
	flags.Code = "foo_CNT(){llDie();}"
	flags.Preamble = "integer gBefore;"
	flags.Postamble = "// after"
	flags.Title = "a benchmark"

	got := buildScript(3, minpad)
	for _, want := range []string{
		"integer gBefore;",
		"foo_000(){llDie();}",
		"foo_001(){llDie();}",
		"foo_002(){llDie();}",
		"// after",
		`RESULT: TITLE=a benchmark`,
	} {
		if !contains(got, want) {
			t.Errorf("the script is missing %q:\n%s", want, got)
		}
	}

	// The pad the harness REPORTS is the one it was asked for, not what
	// is left of it after the filler has taken its copy.
	if !contains(got, "result(llGetUsedMemory(), 3, 5)") {
		t.Errorf("the harness does not report the count and pad it was built with:\n%s", got)
	}

	// A shape with no title says nothing rather than saying nothing at
	// length: the title is a string literal and costs its own length.
	flags.Title = ""
	if got := buildScript(0, minpad); contains(got, "TITLE=") {
		t.Errorf("a benchmark with no title emitted one:\n%s", got)
	}
}

// ------------------------------------------------------- the block rule

// TestAPaddingIsTheLastPadInsideItsBlock: paddingHolds is the two runs a
// remembered or supplied padding is confirmed with, and the rule it
// applies is the definition -- memory has to grow between pad and pad+1.
func TestAPaddingIsTheLastPadInsideItsBlock(t *testing.T) {
	setTestInfo(t, 474, 368)

	held, at, above := paddingHolds(nil, 473)
	if !held {
		t.Errorf("473 was not confirmed as a padding: %d then %d", at, above)
	}
	if above-at != blockSize {
		t.Errorf("the two readings are %d apart, want one block", above-at)
	}

	// Anything else in the block is not the padding: it is inside, and so
	// is the byte after it.
	if held, _, _ := paddingHolds(nil, 400); held {
		t.Error("a pad in the middle of a block was confirmed as a padding")
	}

	// A pad the filler cannot emit is refused without spending a run,
	// because measuring at a pad other than the one named and reporting it
	// as the one named is the one wrong answer here that cannot be seen.
	if held, at, above := paddingHolds(nil, 3); held || at != 0 || above != 0 {
		t.Error("an inexpressible pad was measured rather than refused")
	}
}

// TestTheCostIsReportedUnderDebugAndNowhereElse: a run is an upload, a
// compile, an execution and a wait, and everything else this program does
// is free beside it -- but the count is for whoever asked for --debug,
// not for a caller parsing stdout.
func TestTheCostIsReportedUnderDebugAndNowhereElse(t *testing.T) {
	setTestInfo(t, 474, 368)
	spentRuns, spentRereads, spentCompiles = 11, 6, 1

	flags.Debug = false
	t.Cleanup(func() { flags.Debug = false })
	if said := stderrOf(t, reportCost); said != "" {
		t.Errorf("the cost was reported without --debug:\n%s", said)
	}

	flags.Debug = true
	said := stderrOf(t, reportCost)
	if !strings.Contains(said, "Spent 11 runs (6 of them re-reads) and 1 compiles") {
		t.Errorf("--debug did not report the cost:\n%s", said)
	}
}

// stderrOf collects what a call wrote to standard error.  Everything this
// program says about its own workings goes there, so that a caller
// reading a Size off stdout is unaffected by any of it.
func stderrOf(t *testing.T, fn func()) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stderr")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	save := os.Stderr
	os.Stderr = f
	fn()
	os.Stderr = save
	f.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// ---------------------------------------------------------------- main

// flagDefaults is the flag block as the program starts with it.  main
// writes to the flags as it goes -- it trims the code, folds --statement
// into --code and builds the preamble around it -- so a second run in the
// same process starts from what the first one left unless this is put
// back.
var flagDefaults = flags

// autobench runs the whole program in this process, under --test.
//
// In this process rather than as a subprocess because that is the only
// way what is measured is this program rather than a copy of its argument
// handling, and under --test because that is what makes a benchmark
// answerable without a grid: runScript answers from the model above the
// transport, so nothing here logs in, dials anything or reads the
// padding cache.
//
// os.Exit is the one thing a run cannot survive, so the paths that end in
// errf are not driven from here.  They are argument checks, and what they
// check is asserted against the flags directly.
func autobench(t *testing.T, args ...string) (stdout, stderr string) {
	t.Helper()

	// Nothing under --test reads or writes the padding cache, but the
	// developer running this has a real one, and a test that could reach
	// it is a test that will one day be changed into one that does.
	t.Setenv("SLGO_CONFIG_DIR", t.TempDir())

	dir := t.TempDir()
	out, errs := create(t, filepath.Join(dir, "stdout")), create(t, filepath.Join(dir, "stderr"))

	saveArgs, saveOut, saveErr := os.Args, os.Stdout, os.Stderr
	os.Args = append([]string{"autobench"}, args...)
	os.Stdout, os.Stderr = out, errs

	flags = flagDefaults
	getopt.CommandLine = getopt.New()
	clear(cache)
	clear(probeTest)
	testBaseMem, lsdPad = 0, -1
	useTestInfo, oneCopyVerdict, testNoise = nil, nil, nil
	spentRuns, spentRereads, spentCompiles = 0, 0, 0

	t.Cleanup(func() {
		os.Args, os.Stdout, os.Stderr = saveArgs, saveOut, saveErr
		flags = flagDefaults
		clear(cache)
		clear(probeTest)
		testBaseMem, lsdPad = 0, -1
		useTestInfo, oneCopyVerdict, testNoise = nil, nil, nil
	})

	main()

	os.Args, os.Stdout, os.Stderr = saveArgs, saveOut, saveErr
	out.Close()
	errs.Close()
	return readBack(t, out.Name()), readBack(t, errs.Name())
}

func create(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func readBack(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestTheProgramPrintsWhatTheModeMeasured: the output is what everything
// else reads autobench through -- the skill that drives it parses these
// lines -- so the labels and the order they come in are part of what the
// program is.
func TestTheProgramPrintsWhatTheModeMeasured(t *testing.T) {
	out, _ := autobench(t, "--test=474,368", "-1", "--code", "foo_CNT(){llDie();}")
	for _, want := range []string{
		"Base mem: 5412\n",
		"Result mem: 5924\n",
		"Result pad: 144\n",
		"Size: 368\n",
		"Padding: 473\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("-1 mode did not print %q:\n%s", want, out)
		}
	}

	// Copy mode prints the padding it paid for as well, because that is
	// the number to feed back with --ipad on the next benchmark of the
	// same shape, and it prints the quantisation with the size.
	out, _ = autobench(t, "--test=474,368", "--code", "foo_CNT(){llDie();}")
	if !strings.Contains(out, "Padding: 473\n") {
		t.Errorf("copy mode did not report the padding:\n%s", out)
	}
	if !strings.Contains(out, "Size: 368 "+plusminus) {
		t.Errorf("copy mode did not print a size with its error margin:\n%s", out)
	}
}

// TestFastSkipsThePaddingAndSaysSoByNotSayingIt: --fast is for when the
// caller does not want to pay a dozen runs for a boundary, and a Padding:
// line printed anyway would be a number nobody measured.
func TestFastSkipsThePaddingAndSaysSoByNotSayingIt(t *testing.T) {
	out, _ := autobench(t, "--test=474,368", "--fast", "--code", "foo_CNT(){llDie();}")
	if strings.Contains(out, "Padding:") {
		t.Errorf("--fast reported a padding it did not look for:\n%s", out)
	}
	if !strings.Contains(out, "Size:") {
		t.Errorf("--fast measured nothing:\n%s", out)
	}
}

// TestTheCodeUnderTestComesFromAFileOrAFlag: a benchmark shape is usually
// a file, and the file has to reach the generated script exactly as
// --code would -- the two are the same input by different routes.
func TestTheCodeUnderTestComesFromAFileOrAFlag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bench.lsl")
	if err := os.WriteFile(path, []byte("foo_CNT(){llDie();}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	fromFile, _ := autobench(t, "--test=474,368", "-1", path)
	fromFlag, _ := autobench(t, "--test=474,368", "-1", "--code", "foo_CNT(){llDie();}")
	if fromFile != fromFlag {
		t.Errorf("a file and --code measured differently:\n%s\nand\n%s", fromFile, fromFlag)
	}
}

// TestAStatementIsWrappedInAFunctionAroundIt: --statement is the shape
// where the code under test is not a declaration, so it needs somewhere
// to live -- and --params and --locals are how the names it mentions get
// declared without the caller writing LSL types out.
func TestAStatementIsWrappedInAFunctionAroundIt(t *testing.T) {
	autobench(t, "--test=406,16",
		"--globals", "g", "--statement", "g = g;",
		"--params", "s", "--locals", "i")

	// main leaves the assembled shape in the flags, which is what
	// buildScript renders from.
	for _, want := range []string{"float g;", "_(string s) {", "integer i;"} {
		if !strings.Contains(flags.Preamble, want) {
			t.Errorf("the preamble is missing %q:\n%s", want, flags.Preamble)
		}
	}
	if !strings.Contains(flags.Postamble, "}") {
		t.Errorf("the statement's function was never closed: %q", flags.Postamble)
	}
}

// TestDebugSaysWhatTheBenchmarkSpent: the count of runs is the price of a
// benchmark in the only unit that matters, and it is reported on stderr
// so that a caller parsing stdout does not have to know about it.
func TestDebugSaysWhatTheBenchmarkSpent(t *testing.T) {
	out, said := autobench(t, "--test=474,368", "-1", "--debug", "--code", "foo_CNT(){llDie();}")
	if !strings.Contains(said, "Spent ") || !strings.Contains(said, "re-reads") {
		t.Errorf("--debug did not report what was spent:\n%s", said)
	}
	if !strings.Contains(said, "FindPadding") {
		t.Errorf("--debug said nothing about the search:\n%s", said)
	}
	if strings.Contains(out, "Spent ") {
		t.Errorf("the cost was printed on stdout, where a caller parses the size:\n%s", out)
	}
}

// TestAnIPadIsUsedAsGivenAndConfirmedOnRequest: --ipad is an assertion the
// caller already measured, so it costs no search -- and --check-ipad is
// the two runs that confirm it, spent because the caller asked for them
// and not on their behalf.
func TestAnIPadIsUsedAsGivenAndConfirmedOnRequest(t *testing.T) {
	out, _ := autobench(t, "--test=474,368", "-1", "--ipad", "985", "--check-ipad",
		"--code", "foo_CNT(){llDie();}")
	if !strings.Contains(out, "Padding: 985\n") {
		t.Errorf("--ipad was not used as given:\n%s", out)
	}
	if !strings.Contains(out, "Size: 368\n") {
		t.Errorf("a padding a block up measured differently:\n%s", out)
	}
}

// stdoutOf collects what a call wrote to standard output, which is where
// the answer goes and so where -v puts the script it sent.
func stdoutOf(t *testing.T, fn func()) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdout")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	save := os.Stdout
	os.Stdout = f
	fn()
	os.Stdout = save
	f.Close()
	return readBack(t, path)
}

// ------------------------------------------------- somewhere to run

// TestGettingSomewhereToRunFailsBeforeAnythingIsMeasured: every benchmark
// starts by getting a session and an object, and there are two ways of
// doing it -- a named object or the shared auto pool.  Neither can be
// reached without a daemon, so what is checked here is the half that can:
// a failure comes back as an error rather than as a benchmark that runs
// against nothing.
func TestGettingSomewhereToRunFailsBeforeAnythingIsMeasured(t *testing.T) {
	// A home of the test's own.  Attaching to slgod reads a shared secret
	// out of one, and the developer running this has a real one with a
	// live daemon behind it.
	t.Setenv("HOME", t.TempDir())
	t.Setenv(sl.EnvAgent, "")

	flags = flagDefaults
	t.Cleanup(func() { flags = flagDefaults })

	// Port 1 on loopback: nothing is listening, so the dial fails without
	// anything being asked of the network this machine is on.
	opts := session.Options{Addr: "127.0.0.1:1", Channel: "autobench"}

	flags.Object = "workbench"
	if _, _, _, _, err := runIn(context.Background(), opts); err == nil {
		t.Error("runIn found a named object through a daemon that is not there")
	}

	flags.Object, flags.Rez = "", true
	if _, _, _, _, err := runIn(context.Background(), opts); err == nil {
		t.Error("runIn rezzed a prim through a daemon that is not there")
	}

	// And the shared pool, which is the default and goes a different way
	// about it: it asks the daemon who it is holding before it asks for
	// objects.
	flags.Rez = false
	if _, _, _, _, err := runIn(context.Background(), opts); err == nil {
		t.Error("runIn took objects from a daemon that is not there")
	}
}
