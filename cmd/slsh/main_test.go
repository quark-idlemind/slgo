package main

// Starting up.
//
// run is one long function of decisions taken before there is anything
// to test against: which settings win, which way to connect, and
// whether this is a shell or a single command.  The decisions that do
// not need a grid are all here, and so are whole runs through a fake
// daemon.  Logging in with --direct is not.
//
// One thing shapes the whole file: options.RegisterAndParse registers
// into getopt's package-level set, so a second call in one process is a
// duplicate registration and a fatal one.  Every test that gets that far
// therefore puts a fresh set in place first, which is what lets there be
// more than one of them.

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pborman/getopt/v2"
	"github.com/quark-idlemind/slgo/sl"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// runWith calls run as though it had been typed, with a fresh option
// set and a config directory of its own.
func runWith(t *testing.T, dir string, args ...string) error {
	t.Helper()
	t.Setenv("SLSH_CONFIG_DIR", dir)
	t.Setenv("SLGO_AGENT", "")

	old := os.Args
	t.Cleanup(func() { os.Args = old })
	os.Args = append([]string{"slsh"}, args...)

	getopt.CommandLine = getopt.New()
	return run()
}

// TestARefusedSettingsFileStopsTheShellBeforeAnythingElse.
//
// A misspelled setting is a setting that silently does nothing, and a
// shell that came up anyway would look right and behave as though the
// line were not there.
func TestARefusedSettingsFileStopsTheShellBeforeAnythingElse(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte("adr = localhost:7807\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SLSH_CONFIG_DIR", dir)

	err := run()
	if err == nil {
		t.Fatal("a settings file that will not load should stop the shell")
	}
	if !strings.Contains(err.Error(), "unknown setting") {
		t.Errorf("run = %v", err)
	}
}

// TestHelpIsTheWholeOfThatRun, and is main's own path through run: it
// prints the usage and leaves, without connecting to anything.
func TestHelpIsTheWholeOfThatRun(t *testing.T) {
	// getopt prints to os.Stdout outright, so it is borrowed for the
	// length of the call rather than left to land in the test log.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()

	read := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		read <- string(b)
	}()

	t.Setenv("SLSH_CONFIG_DIR", t.TempDir())
	t.Setenv("SLGO_AGENT", "")
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"slsh", "--help"}
	getopt.CommandLine = getopt.New()

	// main rather than run, so that the one line between them -- what
	// happens to run's error -- is not the only thing never exercised.
	main()

	w.Close()
	os.Stdout = old
	got := <-read
	for _, want := range []string{"--addr", "--direct", "--chat", "--escape"} {
		if !strings.Contains(got, want) {
			t.Errorf("the usage should mention %q:\n%s", want, got)
		}
	}
}

// TestAKeyThatIsNotAKeyIsRefusedBeforeConnecting.
//
// The prefix key is the way out of chat mode, so a shell that came up
// with an unreadable one would be a shell that cannot be left.
func TestAKeyThatIsNotAKeyIsRefusedBeforeConnecting(t *testing.T) {
	err := runWith(t, t.TempDir(), "--escape", "not a key at all")
	if err == nil {
		t.Fatal("an unreadable escape key should stop the shell")
	}
	if !strings.Contains(err.Error(), "is not a key") {
		t.Errorf("run = %v", err)
	}
}

// TestNamingAnAvatarNeedsDirect: through slgod the session already
// knows who it is, so --first there is somebody expecting an avatar
// they will not get.
func TestNamingAnAvatarNeedsDirect(t *testing.T) {
	err := runWith(t, t.TempDir(), "--first", "Quark")
	if err == nil {
		t.Fatal("--first without --direct should be refused")
	}
	if !strings.Contains(err.Error(), "--first and --last are for --direct") {
		t.Errorf("run = %v", err)
	}
}

// TestADaemonThatIsNotThereSaysWhatElseThereIs.
//
// Nothing is listening on port 1, so this is the ordinary "slgod is not
// running" case -- and the answer has to name the other way in, since
// somebody who has just installed this has no daemon at all.
func TestADaemonThatIsNotThereSaysWhatElseThereIs(t *testing.T) {
	err := runWith(t, t.TempDir(), "--addr", "127.0.0.1:1", "--agent", "example")
	if err == nil {
		t.Fatal("dialling a daemon that is not there should fail")
	}
	if !strings.Contains(err.Error(), "--direct logs in without slgod") {
		t.Errorf("run = %v", err)
	}
}

// TestASessionDownForGoodSaysHowToBringItBack: attaching to it is
// refused, so --direct is the wrong advice; login through another
// profile is the way back.
func TestASessionDownForGoodSaysHowToBringItBack(t *testing.T) {
	d, addr := newAuthDaemon(t)
	d.attachFail = status.Error(codes.FailedPrecondition,
		"fake is not connected (logged out); it will not come back on its own")
	err := runWith(t, t.TempDir(), "--addr", addr, "--agent", "fake", "-c", "echo hi")
	if err == nil {
		t.Fatal("attaching to a session down for good should fail")
	}
	if !strings.Contains(err.Error(), "slsh -a OTHER -c 'login fake' brings it back") ||
		strings.Contains(err.Error(), "--direct") {
		t.Errorf("run = %v", err)
	}
}

// TestDirectNeedsSomebodyToLogInAs, and says which profile it looked
// for rather than asking for a password it has nowhere to send.
func TestDirectNeedsSomebodyToLogInAs(t *testing.T) {
	err := runWith(t, t.TempDir(), "--direct", "--agent", "no-such-profile-exists")
	if err == nil {
		t.Fatal("--direct with a profile that is not there should fail")
	}
	if !strings.Contains(err.Error(), "no-such-profile-exists") {
		t.Errorf("run = %v", err)
	}
}

// TestTheProfileInTheEnvironmentBeatsTheFile.
//
// SLGO_AGENT sits between the flag and the file: it describes this
// shell, where the file describes the machine.  The order shows in the
// failure, which names the profile that was actually chosen.
func TestTheProfileInTheEnvironmentBeatsTheFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config"),
		[]byte("agent = from-the-file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SLSH_CONFIG_DIR", dir)
	t.Setenv("SLGO_AGENT", "from-the-environment")

	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"slsh", "--direct"}
	getopt.CommandLine = getopt.New()

	err := run()
	if err == nil {
		t.Fatal("there is no such profile, so this should have failed")
	}
	if !strings.Contains(err.Error(), "from-the-environment") {
		t.Errorf("the environment should have won, got %v", err)
	}
}

// TestChatIsSwallowedByAOneShotRun.
//
// A run that prints one command's output and leaves must not have a
// remark from the region landing in the middle of it, so the relay is
// read and thrown away rather than left to print itself.
//
// The subscription is the test's own and unbuffered, so that handing a
// line over is the moment it is taken, and the second is taken only
// once the first has been dealt with -- printed, if it was going to be.
func TestChatIsSwallowedByAOneShotRun(t *testing.T) {
	x := newTestShell(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lines := make(chan sl.Line)
	stopped := make(chan struct{})
	go func() { x.watchQuietly(ctx, lines); close(stopped) }()

	remark := sl.Line{Source: testSomebody, From: "Somebody", Text: "a remark nobody asked for", Type: 1}
	for i := 0; i < 2; i++ {
		select {
		case lines <- remark:
		case <-time.After(5 * time.Second):
			t.Fatal("watchQuietly did not take the line")
		}
	}
	if got := x.out.String(); got != "" {
		t.Errorf("a one-shot run printed chat: %q", got)
	}

	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Error("watchQuietly did not stop with its context")
	}

	// And with the session, which closes the subscription: a closed
	// channel is always ready, and reading on past it is a loop that
	// spins until the context ends.
	ended := make(chan sl.Line)
	stopped = make(chan struct{})
	go func() { x.watchQuietly(context.Background(), ended); close(stopped) }()
	close(ended)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Error("watchQuietly did not stop when the session closed its subscription")
	}
}

// capturingStdout borrows os.Stdout for the length of a call, since a
// one-shot run prints through a terminal built on it.
func capturingStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w

	read := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		read <- string(b)
	}()

	fn()

	os.Stdout = old
	w.Close()
	return <-read
}

// TestOneCommandAndOut is what makes slsh usable from a script.
//
// It is a whole run: the settings, the dial and its handshake, a
// terminal that reads nothing -- the input is /dev/null on purpose, so
// that two readers do not take alternate lines off stdin -- the
// command, and out.
func TestOneCommandAndOut(t *testing.T) {
	_, addr := newAuthDaemon(t)
	dir := t.TempDir()

	var err error
	got := capturingStdout(t, func() {
		err = runWith(t, dir, "--addr", addr, "--agent", "fake",
			"--escape", "^G", "-c", "echo through a whole run")
	})
	if err != nil {
		t.Fatalf("run = %v", err)
	}
	if !strings.Contains(got, "through a whole run") {
		t.Errorf("the command did not run:\n%s", got)
	}
	// A one-shot run is not a shell: no banner, no prompt.
	if strings.Contains(got, "help for commands") {
		t.Errorf("a one-shot run printed the banner:\n%s", got)
	}
}

// TestAFileOfCommandsAndOut, which is the same thing with the commands
// in a file -- the other half of what redirection is for.
func TestAFileOfCommandsAndOut(t *testing.T) {
	_, addr := newAuthDaemon(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "commands")
	if err := os.WriteFile(script, []byte("echo one\necho two\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var err error
	got := capturingStdout(t, func() {
		err = runWith(t, dir, "--addr", addr, "--agent", "fake", "--file", script)
	})
	if err != nil {
		t.Fatalf("run = %v", err)
	}
	if !strings.Contains(got, "one") || !strings.Contains(got, "two") {
		t.Errorf("the file did not run:\n%s", got)
	}
}

// TestWhatIsLeftOnTheCommandLineIsACommand, so that "slsh ls -l" works
// without the quoting that -c needs.
func TestWhatIsLeftOnTheCommandLineIsACommand(t *testing.T) {
	_, addr := newAuthDaemon(t)

	var err error
	got := capturingStdout(t, func() {
		err = runWith(t, t.TempDir(), "--addr", addr, "--agent", "fake",
			"echo", "left", "on", "the", "line")
	})
	if err != nil {
		t.Fatalf("run = %v", err)
	}
	if !strings.Contains(got, "left on the line") {
		t.Errorf("the arguments were not run as a command:\n%s", got)
	}
}

// TestOneArgumentIsALine, split and redirected as -c would, so that
// slsh "ls -l > list" works as it reads.
func TestOneArgumentIsALine(t *testing.T) {
	_, addr := newAuthDaemon(t)
	path := filepath.Join(t.TempDir(), "list")

	var err error
	got := capturingStdout(t, func() {
		err = runWith(t, t.TempDir(), "--addr", addr, "--agent", "fake",
			"echo one   line > "+path)
	})
	if err != nil {
		t.Fatalf("run = %v\n%s", err, got)
	}
	if b, _ := os.ReadFile(path); string(b) != "one line\n" {
		t.Errorf("the file holds %q; the line should have been split and redirected", b)
	}
}

// TestSeveralArgumentsAreTheCallingShellsWords, and are not split
// again: split, rm "Old Stuff" removes a folder called Old before it
// fails on Stuff.  An apostrophe is a letter and a > inside a word is
// part of it -- split, this one empties a file -- and the transcript
// says the line in a form that reads back as the same words.
func TestSeveralArgumentsAreTheCallingShellsWords(t *testing.T) {
	_, addr := newAuthDaemon(t)
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	path := filepath.Join(t.TempDir(), "old")

	var err error
	got := capturingStdout(t, func() {
		err = runWith(t, t.TempDir(), "--addr", addr, "--agent", "fake",
			"echo", "two  spaces", "it's", "Notes > "+path)
	})
	if err != nil {
		t.Fatalf("run = %v\n%s", err, got)
	}
	if want := "two  spaces it's Notes > " + path; !strings.Contains(got, want) {
		t.Errorf("want %q, got:\n%s", want, got)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a > inside an argument redirected")
	}

	log, _ := os.ReadFile(filepath.Join(data, "slgo", "quark-idlemind.log"))
	line := `$ echo "two  spaces" "it's" "Notes > ` + path + `"`
	if !strings.Contains(string(log), line) {
		t.Errorf("the transcript should say %s:\n%s", line, log)
	}
}

// TestAGreaterThanOnItsOwnRedirects, since slsh's errors are on its
// stdout and the calling shell's > would put them in the file too.
func TestAGreaterThanOnItsOwnRedirects(t *testing.T) {
	_, addr := newAuthDaemon(t)
	path := filepath.Join(t.TempDir(), "list")

	var err error
	got := capturingStdout(t, func() {
		err = runWith(t, t.TempDir(), "--addr", addr, "--agent", "fake",
			"echo", "into the file", ">", path)
	})
	if err != nil {
		t.Fatalf("run = %v\n%s", err, got)
	}
	if b, _ := os.ReadFile(path); string(b) != "into the file\n" {
		t.Errorf("the file holds %q", b)
	}
	if strings.Contains(got, "into the file") {
		t.Errorf("redirected output reached the terminal:\n%s", got)
	}
}

// TestAFailedOneShotRunReturns rather than exiting, so that what run
// deferred still happens: for --direct that is the logout, and here it
// is the daemon seeing the session let go.  main says nothing more,
// since the shell has said what failed.
func TestAFailedOneShotRunReturns(t *testing.T) {
	d, addr := newAuthDaemon(t)

	var err error
	got := capturingStdout(t, func() {
		err = runWith(t, t.TempDir(), "--addr", addr, "--agent", "fake",
			"no-such-command", "at all")
	})
	if !errors.Is(err, errReported) {
		t.Fatalf("run = %v, want errReported", err)
	}
	if n := strings.Count(got, "no such command"); n != 1 {
		t.Errorf("the failure should be said once, said %d times:\n%s", n, got)
	}
	select {
	case <-d.ended:
	case <-time.After(5 * time.Second):
		t.Error("the session was not closed")
	}
}

// TestAFileThatWillNotOpenIsSaid by main, since no line of it ran to
// report anything.
func TestAFileThatWillNotOpenIsSaid(t *testing.T) {
	_, addr := newAuthDaemon(t)
	path := filepath.Join(t.TempDir(), "not-there")

	var err error
	capturingStdout(t, func() {
		err = runWith(t, t.TempDir(), "--addr", addr, "--agent", "fake", "-f", path)
	})
	if err == nil || errors.Is(err, errReported) || !strings.Contains(err.Error(), "not-there") {
		t.Errorf("run = %v, want the open failure itself", err)
	}
}

// TestWhereSLGODRunsIsAQuestionForSLHost.
//
// With nothing said on the command line and nothing in the settings
// file, the address is asked for rather than defaulted -- which is how
// one config works on a machine whose slgod is somewhere else.  An
// sl-host that will not answer therefore stops the run rather than
// leaving it to dial a guess.
func TestWhereSLGODRunsIsAQuestionForSLHost(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "sl-host")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'no idea' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	err := runWith(t, t.TempDir(), "--agent", "fake")
	if err == nil {
		t.Fatal("an sl-host that fails should stop the run")
	}
	if strings.Contains(err.Error(), "--direct logs in without slgod") {
		t.Errorf("that is the dial failing, not sl-host: %v", err)
	}
}

// TestAShellReadsUntilTheInputRunsOut.
//
// The last of run: no command and no file, so it is the loop -- over a
// pipe rather than a terminal, which is what a script driving slsh
// looks like and what the plain terminal exists for.
func TestAShellReadsUntilTheInputRunsOut(t *testing.T) {
	_, addr := newAuthDaemon(t)

	// os.Stdin is what run reads, so it is borrowed rather than
	// trusted to be whatever the test runner left there.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldIn := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = oldIn }()
	io.WriteString(w, "echo one line and out\n")
	w.Close()

	var runErr error
	got := capturingStdout(t, func() {
		runErr = runWith(t, t.TempDir(), "--addr", addr, "--agent", "fake")
	})
	r.Close()

	if runErr != nil {
		t.Fatalf("run = %v", runErr)
	}
	if !strings.Contains(got, "one line and out") {
		t.Errorf("the line was not run:\n%s", got)
	}
	// This one is a shell, so it says who and where first.
	if !strings.Contains(got, "help for commands") {
		t.Errorf("a shell should print the banner:\n%s", got)
	}
}
