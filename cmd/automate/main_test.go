package main

// automate is one contract with a script -- it says DONE when it has
// finished -- and a handful of decisions about what to do when it does
// not.  Every one of those decisions is invisible from the outside except
// as an exit status and some lines on stdout, which is exactly why they
// are worth pinning: a script that failed and was reported as having
// succeeded is a benchmark or a probe that quietly measured nothing.
//
// The grid is fake_test.go's.  Nothing here logs in, dials anything or
// reads a profile: what needs a login is runIn's successful half, and it
// is named in coverage-notes/commands.md.
//
// Nothing runs in parallel.  The flags are package level -- one program,
// one set of options -- so two tests at once would be two tests sharing
// them.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pborman/getopt/v2"

	"github.com/quark-idlemind/slgo/internal/session"
	"github.com/quark-idlemind/slgo/sl"
)

// flagDefaults is the flag block as the program starts with it, since
// run() writes to it and every test after would inherit that.
var flagDefaults = flags

// reset puts the program back to how it starts.
func reset(t *testing.T) {
	t.Helper()
	flags = flagDefaults
	getopt.CommandLine = getopt.New()
	t.Cleanup(func() {
		flags = flagDefaults
		getopt.CommandLine = getopt.New()
	})
}

// stdoutOf collects what a call printed.  automate's whole output is
// stdout, one line per line the script said, so this is how what a person
// would have seen is read back.
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

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// ---------------------------------------------------------- one script

// TestALineIsPrintedAsItArrivesAndTaggedWithItsScript: a script that runs
// for a minute is one worth watching, so the lines are printed as they
// come rather than at the end -- and with several scripts named on one
// command line, the only thing saying which said what is the tag.
func TestALineIsPrintedAsItArrivesAndTaggedWithItsScript(t *testing.T) {
	reset(t)
	s, obj, f := newFakeSession(t, flags.Script)
	f.says = []string{"hello", "still here"}

	got := stdoutOf(t, func() {
		if !once(context.Background(), s, obj, "a.lsl", "default {}") {
			t.Error("a script that said DONE was reported as having failed")
		}
	})
	for _, want := range []string{"a.lsl: hello\n", "a.lsl: still here\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("output is missing %q:\n%s", want, got)
		}
	}

	// The sentinel is the script talking to us rather than to the person
	// reading, so it is not printed.
	if strings.Contains(got, "DONE") {
		t.Errorf("the sentinel was printed as output:\n%s", got)
	}
	// And the source really went up: what is being watched is a run.
	if f.ran != 1 {
		t.Errorf("%d scripts ran", f.ran)
	}
	if len(f.sources) != 1 || f.sources[0] != "default {}" {
		t.Errorf("what was sent was %q", f.sources)
	}
}

// TestTheDebugChannelIsTheSimulatorAndNotTheScript: "Could not find
// texture" and its relatives are the simulator commenting on the script,
// not the script speaking, and printing them tagged with the script's
// name would attribute them to it.
func TestTheDebugChannelIsTheSimulatorAndNotTheScript(t *testing.T) {
	reset(t)
	s, obj, f := newFakeSession(t, flags.Script)
	f.says = []string{"hello"}
	// The fault carries its reason on the debug channel, which is the
	// same channel, so this asserts both at once.
	f.fault = "Math Error"

	got := stdoutOf(t, func() {
		if once(context.Background(), s, obj, "a.lsl", "default {}") {
			t.Error("a script that faulted was reported as having got to the end")
		}
	})
	if strings.Contains(got, "Could not find") || strings.Contains(got, "run-time error]") {
		t.Errorf("the debug channel was printed as output:\n%s", got)
	}
	if !strings.Contains(got, "a.lsl: hello") {
		t.Errorf("what the script did say was lost:\n%s", got)
	}
	if !strings.Contains(got, "Math Error") {
		t.Errorf("the fault was not reported:\n%s", got)
	}
}

// TestASentinelOfAnotherNameIsHonoured: --done is what to change when a
// script already says something else at the end, and the line carrying it
// is still the script talking to us -- so changing the word must not
// start printing it.
func TestASentinelOfAnotherNameIsHonoured(t *testing.T) {
	reset(t)
	flags.Done = "FINISHED"
	s, obj, f := newFakeSession(t, flags.Script)
	f.sentinel = "FINISHED"
	f.says = []string{"working"}

	got := stdoutOf(t, func() {
		if !once(context.Background(), s, obj, "a.lsl", "default {}") {
			t.Error("a script that said FINISHED was reported as having failed")
		}
	})
	if strings.Contains(got, "FINISHED") {
		t.Errorf("the sentinel was printed as output:\n%s", got)
	}
}

// TestWithoutASentinelEveryRunCostsTheWholeTimeout: --done "" is the
// deliberate way of waiting out the clock, and a run that ends that way
// is not a failure -- there was nothing to wait for and nothing was
// promised.
func TestWithoutASentinelEveryRunCostsTheWholeTimeout(t *testing.T) {
	reset(t)
	flags.Done = ""
	flags.Timeout = time.Second
	s, obj, f := newFakeSession(t, flags.Script)
	f.silent = true
	f.says = []string{"said something and stopped"}

	got := stdoutOf(t, func() {
		if !once(context.Background(), s, obj, "a.lsl", "default {}") {
			t.Error("waiting out a deliberate timeout was reported as a failure")
		}
	})
	if !strings.Contains(got, "said something and stopped") {
		t.Errorf("what the script said before the clock ran out was lost:\n%s", got)
	}
}

// TestAScriptThatNeverSaysDoneIsAFailureNamingTheWordAndTheWait: the
// contract is one line, so a run that did not get it has to say which
// word it was waiting for and how long -- both are the caller's to change
// and neither is guessable from "timed out".
func TestAScriptThatNeverSaysDoneIsAFailureNamingTheWordAndTheWait(t *testing.T) {
	reset(t)
	flags.Timeout = time.Second
	s, obj, f := newFakeSession(t, flags.Script)
	f.silent = true

	got := stdoutOf(t, func() {
		if once(context.Background(), s, obj, "a.lsl", "default {}") {
			t.Error("a script that never finished was reported as having finished")
		}
	})
	if !strings.Contains(got, "it did not say DONE within 1s") {
		t.Errorf("the failure does not name the word and the wait:\n%s", got)
	}
}

// TestACompilerRefusalIsPrintedInSecondLifesOwnWords: a compiler message
// is evidence, and the only useful form of evidence is the verbatim one
// -- the line and column in it are counted in the source that was sent.
func TestACompilerRefusalIsPrintedInSecondLifesOwnWords(t *testing.T) {
	reset(t)
	s, obj, f := newFakeSession(t, flags.Script)
	f.refuse = []string{"(1,1) : ERROR : Syntax error"}

	got := stdoutOf(t, func() {
		if once(context.Background(), s, obj, "a.lsl", "not lsl at all") {
			t.Error("a script that would not compile was reported as having run")
		}
	})
	if !strings.Contains(got, "a.lsl: (1,1) : ERROR : Syntax error") {
		t.Errorf("what Second Life said was not printed:\n%s", got)
	}

}

// TestARefusalWithNothingSaidStillProducesASentence: Second Life
// declines to compile without saying why more often than one would like,
// and silence there would leave the reader with a script that did not run
// and no reason at all.
func TestARefusalWithNothingSaidStillProducesASentence(t *testing.T) {
	reset(t)
	s, obj, f := newFakeSession(t, flags.Script)
	f.refuseSilently = true

	got := stdoutOf(t, func() {
		if once(context.Background(), s, obj, "a.lsl", "default {}") {
			t.Error("a script that would not compile was reported as having run")
		}
	})
	if !strings.Contains(got, "a.lsl: it would not compile, and the compiler did not say why") {
		t.Errorf("a refusal with nothing attached to it said nothing useful:\n%s", got)
	}
}

// TestARunThatCouldNotBeMadeIsReportedAgainstTheScript: a circuit that
// went away is not the script's fault, but it is still this script's
// failure -- and the tag is the only thing saying which of several it
// happened to.
func TestARunThatCouldNotBeMadeIsReportedAgainstTheScript(t *testing.T) {
	reset(t)
	s, obj, f := newFakeSession(t, "")

	f.mu.Lock()
	f.sendErr = errNoCircuit
	f.mu.Unlock()

	got := stdoutOf(t, func() {
		if once(context.Background(), s, obj, "a.lsl", "default {}") {
			t.Error("a run that never happened was reported as a success")
		}
	})
	if !strings.HasPrefix(got, "a.lsl: ") {
		t.Errorf("the failure was not attributed to the script:\n%s", got)
	}
}

// errNoCircuit is the grid having gone away, which is the ordinary way a
// run fails for a reason that is nothing to do with the script.
var errNoCircuit = errCircuitGone{}

type errCircuitGone struct{}

func (errCircuitGone) Error() string { return "the circuit is gone" }

// ------------------------------------------------------------- the run

// TestNothingToRunIsAUsageErrorAndNotASession: reading every script
// before connecting is deliberate -- a typo in a filename is worth
// finding out about now rather than after a login -- and being given no
// scripts at all is the same rule taken to its end.
func TestNothingToRunIsAUsageErrorAndNotASession(t *testing.T) {
	reset(t)
	os.Args = []string{"automate"}
	t.Cleanup(func() { os.Args = []string{"automate"} })

	err := run()
	if err == nil {
		t.Fatal("run went looking for a session with nothing to run")
	}
	if !strings.Contains(err.Error(), "usage") {
		t.Errorf("run = %v, want it to say how the program is called", err)
	}
}

// TestAFileThatIsNotThereIsFoundBeforeTheLogin: a login costs seconds and
// an avatar; a missing file costs a stat.  Doing them in that order is
// worth an explicit test because the cheap check is the one that gets
// moved.
func TestAFileThatIsNotThereIsFoundBeforeTheLogin(t *testing.T) {
	reset(t)
	// Nothing to dial and nowhere to look for a profile, so a run that
	// got as far as connecting would fail differently.
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()

	good := filepath.Join(dir, "a.lsl")
	if err := os.WriteFile(good, []byte("default {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Args = []string{"automate", good, filepath.Join(dir, "missing.lsl")}
	t.Cleanup(func() { os.Args = []string{"automate"} })

	err := run()
	if err == nil {
		t.Fatal("run carried on past a file that is not there")
	}
	if !strings.Contains(err.Error(), "missing.lsl") {
		t.Errorf("run = %v, want it to name the file it could not read", err)
	}
}

// TestHelpIsAnAnswerAndNotAFailure: --help is the one way of running this
// program that is not a run, and it has to leave without an exit status
// that says something went wrong.
func TestHelpIsAnAnswerAndNotAFailure(t *testing.T) {
	reset(t)
	os.Args = []string{"automate", "--help"}
	t.Cleanup(func() { os.Args = []string{"automate"} })

	var err error
	got := stdoutOf(t, func() { err = run() })
	if err != nil {
		t.Errorf("--help = %v, want it to be an answer", err)
	}
	if !strings.Contains(got, "--object") {
		t.Errorf("--help did not print the usage:\n%s", got)
	}
}

// TestGettingSomewhereToRunFailsBeforeAnythingIsSent: a script needs an
// object to run in, and both ways of getting one go through a daemon.
// Neither can be reached here, so what is checked is that a failure comes
// back as an error rather than as a run against nothing.
func TestGettingSomewhereToRunFailsBeforeAnythingIsSent(t *testing.T) {
	reset(t)
	// A home of the test's own: attaching to slgod reads a shared secret
	// out of one, and the developer running this has a real one with a
	// live daemon behind it.
	t.Setenv("HOME", t.TempDir())
	t.Setenv(sl.EnvAgent, "")

	// Port 1 on loopback: nothing is listening, so the dial fails without
	// anything being asked of the network this machine is on.
	opts := session.Options{Addr: "127.0.0.1:1", Channel: "automate"}

	flags.Object = "workbench"
	if _, _, _, err := runIn(context.Background(), opts); err == nil {
		t.Error("runIn found a named object through a daemon that is not there")
	}

	flags.Object, flags.Rez = "", true
	if _, _, _, err := runIn(context.Background(), opts); err == nil {
		t.Error("runIn rezzed a prim through a daemon that is not there")
	}

	// The shared auto object is the default and goes a different way
	// about it: it asks the daemon who it is holding before it asks for
	// anything to run in.
	flags.Rez = false
	if _, _, _, err := runIn(context.Background(), opts); err == nil {
		t.Error("runIn took an auto object from a daemon that is not there")
	}
}

// TestARunThatCannotStartIsStillAFailedRun: run() is what main turns into
// an exit status, so the one thing that must not happen is a nil error
// from a run that never got as far as a script.
func TestARunThatCannotStartIsStillAFailedRun(t *testing.T) {
	reset(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv(sl.EnvAgent, "")

	path := filepath.Join(t.TempDir(), "a.lsl")
	if err := os.WriteFile(path, []byte("default {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Args = []string{"automate", "--addr", "127.0.0.1:1", "--object", "workbench", path}
	t.Cleanup(func() { os.Args = []string{"automate"} })

	if err := run(); err == nil {
		t.Error("run reported success without a session")
	}
}

// TestTheProgramLeavesQuietlyWhenThereIsNothingWrong: main is four lines
// and all four are about the exit status, which is the only thing a
// caller can act on -- the original always exited 0, which left a script
// driving this no way to tell without scraping stdout.
func TestTheProgramLeavesQuietlyWhenThereIsNothingWrong(t *testing.T) {
	reset(t)
	os.Args = []string{"automate", "--help"}
	t.Cleanup(func() { os.Args = []string{"automate"} })

	if got := stdoutOf(t, main); !strings.Contains(got, "--done") {
		t.Errorf("--help did not print the usage:\n%s", got)
	}
}
