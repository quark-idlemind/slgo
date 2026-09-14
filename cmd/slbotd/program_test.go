package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The programs are the one thing here that leaves the process, so what
// is worth pinning down is what they are started with: an argv and no
// shell, the avatar in the environment, and a bound on how long they
// may take.

// script writes a shell script that can be run, and returns its path.
func script(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("this test runs a shell script")
	}
	path := filepath.Join(t.TempDir(), "prog")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// runs a program command and returns everything it printed, error
// included.
func runs(t *testing.T, d *daemon, b *bot, line string) string {
	t.Helper()
	return send(t, d, b, line)
}

func TestAProgramRunsAndItsOutputComesBack(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	d.cfg.Programs["measure"] = &Program{
		Name: "measure", Argv: []string{script(t, `echo "measured $*"`)},
	}
	got := runs(t, d, b, "measure --statement llSin(1.0);")
	if !strings.Contains(got, "measured --statement llSin(1.0);") {
		t.Errorf("got %q", got)
	}
	if !strings.Contains(got, "measure finished in") {
		t.Errorf("the answer does not say it finished: %q", got)
	}
}

// The fixed arguments in the configuration come first and what
// somebody typed comes after, so an operator can pin a flag on every
// run without it being possible to type in front of it.
func TestTheConfiguredArgumentsComeFirst(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	d.cfg.Programs["measure"] = &Program{
		Name: "measure", Argv: []string{script(t, `echo "args: $*"`), "--quiet"},
	}
	got := runs(t, d, b, "measure now")
	if !strings.Contains(got, "args: --quiet now") {
		t.Errorf("got %q", got)
	}
}

// Which avatar the run is for is said in the environment, because that
// is what every program in this tree reads to decide which session to
// attach to.  A daemon that did not say it would run a benchmark as
// whichever avatar slgod happened to hand over.
func TestTheAvatarIsPassedInTheEnvironment(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	d.cfg.Programs["whoami"] = &Program{
		Name: "whoami", Argv: []string{script(t, `echo "agent=$SLGO_AGENT"`)},
	}
	got := runs(t, d, b, "whoami")
	if !strings.Contains(got, "agent=example") {
		t.Errorf("got %q", got)
	}
}

// The daemon's own environment may already name an avatar -- it is
// whatever the shell that started it was pointed at -- and every run
// would otherwise be measured as that one whoever asked for it.
func TestTheAvatarWinsOverTheOneInTheDaemonsEnvironment(t *testing.T) {
	t.Setenv("SLGO_AGENT", "somebody-else")
	d, b, _ := newTestDaemon(t)
	d.cfg.Programs["whoami"] = &Program{
		Name: "whoami", Argv: []string{script(t, `echo "agent=$SLGO_AGENT"`)},
	}
	got := runs(t, d, b, "whoami")
	if !strings.Contains(got, "agent=example") {
		t.Errorf("got %q", got)
	}
}

// Nothing reaches a shell, so a word that would mean something to one
// is an argument like any other.
func TestNothingReachesAShell(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	d.cfg.Programs["echoes"] = &Program{
		Name: "echoes", Argv: []string{script(t, `echo "got [$1]"`)},
	}
	got := runs(t, d, b, `echoes "; touch /tmp/slbotd-should-not-exist"`)
	if !strings.Contains(got, "got [; touch /tmp/slbotd-should-not-exist]") {
		t.Errorf("got %q", got)
	}
	if _, err := os.Stat("/tmp/slbotd-should-not-exist"); err == nil {
		os.Remove("/tmp/slbotd-should-not-exist")
		t.Fatal("a command line reached a shell")
	}
}

// A program that failed says so with its exit status, because that is
// the only thing distinguishing a benchmark that measured nothing from
// one that measured zero.
func TestAFailedProgramSaysItsExitStatus(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	d.cfg.Programs["fails"] = &Program{
		Name: "fails", Argv: []string{script(t, "echo trouble >&2\nexit 3\n")},
	}
	got := runs(t, d, b, "fails")
	if !strings.Contains(got, "trouble") {
		t.Errorf("what it said on standard error was lost: %q", got)
	}
	if !strings.Contains(got, "fails exited 3") {
		t.Errorf("got %q", got)
	}
}

// A program that will not finish is stopped, and the answer says that
// is what happened rather than reporting an exit status nobody set.
func TestAProgramThatWillNotFinishIsStopped(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	d.cfg.RunTimeout = 200 * time.Millisecond
	d.cfg.Programs["forever"] = &Program{
		Name: "forever", Argv: []string{script(t, "sleep 30\n")},
	}
	started := time.Now()
	got := runs(t, d, b, "forever")
	if took := time.Since(started); took > 10*time.Second {
		t.Errorf("it took %s to give up", took)
	}
	if !strings.Contains(got, "was still running") {
		t.Errorf("got %q", got)
	}
}

// A program cannot be allowed to fill the daemon's memory, and what is
// dropped is said rather than quietly lost.
func TestOutputIsBounded(t *testing.T) {
	var w limitedBuffer
	w.limit = 10
	n, err := w.Write([]byte("0123456789abcdef"))
	if err != nil || n != 16 {
		t.Fatalf("Write = %d, %v, want the whole length and no error", n, err)
	}
	if got := w.b.String(); got != "0123456789" {
		t.Errorf("kept %q", got)
	}
	if w.cut != 6 {
		t.Errorf("cut = %d, want 6", w.cut)
	}
	if n, err := w.Write([]byte("more")); err != nil || n != 4 {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if w.cut != 10 {
		t.Errorf("cut = %d, want 10", w.cut)
	}
}

func TestTheDroppedOutputIsReported(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	d.cfg.Programs["noisy"] = &Program{
		Name: "noisy",
		Argv: []string{script(t, "i=0\nwhile [ $i -lt 40000 ]; do echo 0123456789; i=$((i+1)); done\n")},
	}
	got := runs(t, d, b, "noisy")
	if !strings.Contains(got, "were dropped") {
		t.Errorf("a program that printed half a megabyte said nothing about it: %q",
			got[max(0, len(got)-200):])
	}
}

// The path in the configuration is the one thing about a failed start
// that nobody typing the command can see.
func TestAProgramThatIsNotThereSaysWhereItLooked(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	d.cfg.Programs["missing"] = &Program{
		Name: "missing", Argv: []string{"/no/such/program"},
	}
	got := runs(t, d, b, "missing")
	if !strings.Contains(got, "/no/such/program") {
		t.Errorf("got %q", got)
	}
}

// A run needs a session, because what it will do first is attach to
// one.  Saying so at once is better than a program failing three
// minutes later against a daemon that already knew.
func TestAProgramNeedsTheAvatarToBeConnected(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	b.setSession(nil)
	b.setState(stateWaiting, "slgod is restarting")
	d.cfg.Programs["measure"] = &Program{Name: "measure", Argv: []string{"/bin/echo"}}
	got := runs(t, d, b, "measure")
	if !strings.Contains(got, "is not connected") {
		t.Errorf("got %q", got)
	}
}

// The command timeout is a bound on a listing and the run timeout is a
// bound on a benchmark.  A run that inherited the first would be killed
// at two minutes by a number nobody would think to look at.
func TestARunIsNotBoundedByTheCommandTimeout(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	d.cfg.Timeout = 100 * time.Millisecond
	d.cfg.RunTimeout = 30 * time.Second
	d.cfg.Programs["slow"] = &Program{
		Name: "slow", Argv: []string{script(t, "sleep 1\necho done\n")},
	}

	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	run, cancelRun := context.WithTimeout(base, d.cfg.Timeout)
	defer cancelRun()

	r := &req{d: d, bot: b, from: testSender, who: "Trusted Resident", base: base}
	var out strBuilder
	if err := r.run(run, &out, []string{"slow"}); err != nil {
		t.Fatalf("the run was killed by the command timeout: %v", err)
	}
	if !strings.Contains(out.String(), "done") {
		t.Errorf("got %q", out.String())
	}
}
