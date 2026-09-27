package main

// The shell itself: the loop, the two modes, and the line that becomes
// a command.
//
// pty_test.go drives the same code through a real terminal, which is
// the only way to see what a person would see -- but it does it in a
// subprocess, so none of it can be observed from here and none of it is
// counted.  This file is the same shell in this process: the keys are
// handed to key() rather than typed, and what would have been drawn is
// read out of a buffer.
//
// The part worth the most is parse.  Redirection and quoting are what
// make "ls > listing", an editor, and ". listing" a workflow rather
// than a special case, and every mistake in a splitter of that kind
// comes out as a command that ran on the wrong words.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// TestParseSplitsAndRedirects is the whole of the command line syntax.
func TestParseSplitsAndRedirects(t *testing.T) {
	for _, c := range []struct {
		line   string
		words  []string
		file   string
		append bool
		bad    string // a fragment of the refusal, when it is one
	}{
		{line: "", words: nil},
		{line: "   \t ", words: nil},
		{line: "ls -l Objects", words: []string{"ls", "-l", "Objects"}},
		{line: "ls\t-l", words: []string{"ls", "-l"}},

		// Quotes group, and either kind will do.
		{line: `say "hello there"`, words: []string{"say", "hello there"}},
		{line: `say 'hello there'`, words: []string{"say", "hello there"}},
		{line: `cd "Objects"/deeper`, words: []string{"cd", "Objects/deeper"}},
		// A quoted empty string is still a word: "say" with nothing to
		// say is not the same command as "say".
		{line: `say ""`, words: []string{"say", ""}},

		// A backslash is left alone, because an inventory path uses it
		// to escape a separator and a shell that ate it would make
		// those paths untypeable.
		{line: `ls a\/b`, words: []string{"ls", `a\/b`}},

		{line: "ls > listing", words: []string{"ls"}, file: "listing"},
		{line: "ls>listing", words: []string{"ls"}, file: "listing"},
		{line: "ls >> listing", words: []string{"ls"}, file: "listing", append: true},
		{line: "ls>>listing", words: []string{"ls"}, file: "listing", append: true},
		{line: `ls > "a listing"`, words: []string{"ls"}, file: "a listing"},

		// A > inside quotes is text.  Chat is a command here, and
		// "say i > you" said out loud is not a redirection.
		{line: `say "i > you"`, words: []string{"say", "i > you"}},

		{line: "ls > a > b", bad: "only one redirection"},
		{line: "ls > a >> b", bad: "only one redirection"},
		{line: "ls >", bad: "no file after >"},
		{line: `say "unfinished`, bad: "unclosed \" quote"},
		{line: `say 'unfinished`, bad: "unclosed ' quote"},
	} {
		words, file, appending, err := parse(c.line)
		if c.bad != "" {
			if err == nil {
				t.Errorf("%q: should have been refused, got %q", c.line, words)
			} else if !strings.Contains(err.Error(), c.bad) {
				t.Errorf("%q: refusal should mention %q, got %v", c.line, c.bad, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.line, err)
			continue
		}
		if strings.Join(words, "\x00") != strings.Join(c.words, "\x00") {
			t.Errorf("%q: words %q, want %q", c.line, words, c.words)
		}
		if file != c.file || appending != c.append {
			t.Errorf("%q: redirect %q append=%v, want %q %v",
				c.line, file, appending, c.file, c.append)
		}
	}
}

// TestParseKeepsAQuotedGreaterThanOutOfTheFilename.
//
// This is the case the "quoted" flag exists for and the one nobody
// types on purpose: once a word has been quoted, a > that follows it
// without a space is part of the word.  Worth pinning because the
// alternative -- a redirection -- would write a file named after
// whatever came next.
func TestParseKeepsAQuotedGreaterThanOutOfTheFilename(t *testing.T) {
	words, file, _, err := parse(`say "hello">there`)
	if err != nil {
		t.Fatal(err)
	}
	if file != "" {
		t.Errorf("that should not have been a redirection, got %q", file)
	}
	if len(words) != 2 || words[1] != "hello>there" {
		t.Errorf("words = %q", words)
	}
}

// TestDoIgnoresBlanksAndComments, which is what lets an edited listing
// keep its notes.
func TestDoIgnoresBlanksAndComments(t *testing.T) {
	x := newTestShell(t)
	for _, line := range []string{"", "   ", "# a note", "  # indented"} {
		if err := x.Do(context.Background(), line); err != nil {
			t.Errorf("%q: %v", line, err)
		}
		if got := x.out.String(); got != "" {
			t.Errorf("%q printed %q", line, got)
		}
	}
	// A line of nothing but a redirection has no command in it, and is
	// not an error either -- nor is it a file created for nothing.
	path := filepath.Join(t.TempDir(), "listing")
	if err := x.Do(context.Background(), "> "+path); err != nil {
		t.Errorf("a bare redirection: %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a line with no command in it should not have opened the file")
	}
}

// TestDoReportsWhatItCouldNotParse at the moment it happens, since the
// person watching is the one who typed it.
func TestDoReportsWhatItCouldNotParse(t *testing.T) {
	x := newTestShell(t)
	if err := x.Do(context.Background(), `say "unfinished`); err == nil {
		t.Error("an unclosed quote should be an error")
	}
	if got := x.out.String(); !strings.Contains(got, "unclosed") {
		t.Errorf("the complaint should reach the terminal, got %q", got)
	}
}

// TestUnknownCommandSaysSoAndFails.
//
// Both halves matter: the message is for the person, and the error is
// for a file of commands, which stops rather than running the rest
// against a state nobody intended.
func TestUnknownCommandSaysSoAndFails(t *testing.T) {
	x := newTestShell(t)
	err := x.Do(context.Background(), "nosuchthing")
	if err == nil {
		t.Fatal("an unknown command should be an error")
	}
	if got := x.out.String(); !strings.Contains(got, "no such command") {
		t.Errorf("it should say so on the terminal, got %q", got)
	}
}

// TestRedirectionWritesTheCommandsOutputToAFile, which with source is
// the point of the whole arrangement.
func TestRedirectionWritesTheCommandsOutputToAFile(t *testing.T) {
	x := newTestShell(t)
	path := filepath.Join(t.TempDir(), "listing")

	if err := x.Do(context.Background(), "echo first > "+path); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != "first\n" {
		t.Errorf("the file holds %q", got)
	}
	// Nothing reached the terminal: that is what redirection is.
	if got := x.out.String(); got != "" {
		t.Errorf("the output should have gone to the file, not the screen: %q", got)
	}

	// > starts again, >> adds.
	if err := x.Do(context.Background(), "echo second > "+path); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != "second\n" {
		t.Errorf("> should truncate, the file holds %q", got)
	}
	if err := x.Do(context.Background(), "echo third >> "+path); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != "second\nthird\n" {
		t.Errorf(">> should append, the file holds %q", got)
	}
}

// TestRedirectionToSomewhereUnwritableIsReported rather than the
// command running and its output going nowhere.
func TestRedirectionToSomewhereUnwritableIsReported(t *testing.T) {
	x := newTestShell(t)
	path := filepath.Join(t.TempDir(), "nosuchdirectory", "listing")

	err := x.Do(context.Background(), "echo something > "+path)
	if err == nil {
		t.Fatal("writing into a directory that is not there should fail")
	}
	if got := x.out.String(); !strings.Contains(got, "no such") {
		t.Errorf("the failure should reach the terminal, got %q", got)
	}
}

// TestSplitRedirectLooksOnlyAtWholeArguments.
//
// These are words the calling shell has already split, so the only
// thing looked for is an argument that is > or >> and nothing else;
// after that the rules and refusals are parse's.  Each is also written
// back as the transcript writes it, and parse has to read that line as
// the same words.
func TestSplitRedirectLooksOnlyAtWholeArguments(t *testing.T) {
	for _, c := range []struct {
		args   []string
		words  []string
		file   string
		append bool
		bad    string
	}{
		{args: []string{"rm", "Old Stuff"}, words: []string{"rm", "Old Stuff"}},
		{args: []string{"cp", "Old Hat", "New Hat"}, words: []string{"cp", "Old Hat", "New Hat"}},
		{args: []string{"cat", "Notes > old"}, words: []string{"cat", "Notes > old"}},
		{args: []string{"cat", ">old"}, words: []string{"cat", ">old"}},
		{args: []string{"say", "it's"}, words: []string{"say", "it's"}},
		{args: []string{"say", `a "b"`}, words: []string{"say", `a "b"`}},
		{args: []string{"say", `it's "b"`}, words: []string{"say", `it's "b"`}},
		{args: []string{"say", `"'`}, words: []string{"say", `"'`}},
		{args: []string{"say", ""}, words: []string{"say", ""}},
		{args: []string{"say", "a\tb"}, words: []string{"say", "a\tb"}},
		{args: []string{"ls", "a\\/b"}, words: []string{"ls", "a\\/b"}},

		{args: []string{"ls", "-l", ">", "list.txt"}, words: []string{"ls", "-l"}, file: "list.txt"},
		{args: []string{"ls", ">>", "list.txt"}, words: []string{"ls"}, file: "list.txt", append: true},
		{args: []string{"ls", ">", "a list"}, words: []string{"ls"}, file: "a list"},
		{args: []string{">", "list", "ls"}, words: []string{"ls"}, file: "list"},

		{args: []string{"ls", ">", "a", ">", "b"}, bad: "only one redirection"},
		{args: []string{"ls", ">", "a", ">>", "b"}, bad: "only one redirection"},
		{args: []string{"ls", ">", ">", "b"}, bad: "only one redirection"},
		{args: []string{"ls", ">"}, bad: "no file after >"},
		{args: []string{"ls", ">>"}, bad: "no file after >"},
	} {
		words, file, appending, err := splitRedirect(c.args)
		line := quoteWords(c.args)
		pWords, pFile, pAppending, pErr := parse(line)

		if c.bad != "" {
			if err == nil || !strings.Contains(err.Error(), c.bad) {
				t.Errorf("%q: want a refusal mentioning %q, got %v", c.args, c.bad, err)
			}
			if pErr == nil || pErr.Error() != err.Error() {
				t.Errorf("%q: parse(%s) refused with %v, not %v", c.args, line, pErr, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.args, err)
			continue
		}
		if strings.Join(words, "\x00") != strings.Join(c.words, "\x00") ||
			file != c.file || appending != c.append {
			t.Errorf("%q: words %q > %q append=%v, want %q > %q %v",
				c.args, words, file, appending, c.words, c.file, c.append)
		}
		if pErr != nil || strings.Join(pWords, "\x00") != strings.Join(words, "\x00") ||
			pFile != file || pAppending != appending {
			t.Errorf("%q: the transcript's %s reads back as %q > %q append=%v, %v",
				c.args, line, pWords, pFile, pAppending, pErr)
		}
	}
}

// TestDoWordsRemovesTheOneNameItWasGiven.
//
// rm deletes each path as it resolves it, so a name split in two
// deletes whatever the first half names before the second half fails.
func TestDoWordsRemovesTheOneNameItWasGiven(t *testing.T) {
	x := newTestShell(t)
	x.grid.mu.Lock()
	x.grid.inv.Dirs = append(x.grid.inv.Dirs, &invDir{
		ID: msg.MustParseUUID("fe2b7e57-7e57-c0de-f4fa-5d5311cdbf2c"), Name: "Old"})
	x.grid.inv.Items = append(x.grid.inv.Items, &invItem{
		ID: msg.MustParseUUID("fe747e57-7e57-c0de-c038-b5baebcd0e85"), Name: "Old Stuff",
		Type: int(sl.AssetNotecard), Created: 1754000300})
	x.grid.mu.Unlock()

	if err := x.DoWords(context.Background(), []string{"rm", "Old Stuff"}); err != nil {
		t.Fatalf("rm: %v\n%s", err, x.out.String())
	}
	listed := x.do(t, "ls")
	if strings.Contains(listed, "/Old Stuff") {
		t.Errorf("Old Stuff should be gone:\n%s", listed)
	}
	if !strings.Contains("\n"+listed, "\n/Old\n") {
		t.Errorf("the folder Old should still be there:\n%s", listed)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestSourceRunsAFileAndSkipsItsNotes.
func TestSourceRunsAFileAndSkipsItsNotes(t *testing.T) {
	x := newTestShell(t)
	path := writeScript(t, "# what this file is for\n\necho one\n\n  # and a note\necho two\n")

	if err := x.Source(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if got, want := x.out.String(), "one\ntwo\n"; got != want {
		t.Errorf("the file printed %q, want %q", got, want)
	}
}

// TestSourceStopsAtTheFirstFailureAndSaysHowMuchDidNotHappen.
//
// A file of moves usually begins by changing folder, and carrying on
// after that failed runs every remaining line somewhere else -- which,
// when the lines are removals, is not a thing to find out about
// afterwards.
func TestSourceStopsAtTheFirstFailureAndSaysHowMuchDidNotHappen(t *testing.T) {
	x := newTestShell(t)
	path := writeScript(t, "echo before\nnosuchthing\necho after\n# a note is not a line that did not run\n")

	err := x.Source(context.Background(), path)
	if !errors.Is(err, errStopped) {
		t.Fatalf("Source = %v, want errStopped", err)
	}
	got := x.out.String()
	if strings.Contains(got, "after") {
		t.Errorf("the file carried on past the failure:\n%s", got)
	}
	if !strings.Contains(got, ":2: nosuchthing") {
		t.Errorf("the report should name the line that failed:\n%s", got)
	}
	if !strings.Contains(got, "1 line was not run") {
		t.Errorf("one line left should read as one line:\n%s", got)
	}
}

// TestSourceCountsMoreThanOneLineInThePlural, because "1 lines were not
// run" reads like a bug in the thing reporting the bug.
func TestSourceCountsMoreThanOneLineInThePlural(t *testing.T) {
	x := newTestShell(t)
	path := writeScript(t, "nosuchthing\necho one\necho two\n")

	x.Source(context.Background(), path)
	if got := x.out.String(); !strings.Contains(got, "2 lines were not run") {
		t.Errorf("two lines left should read as two lines:\n%s", got)
	}
}

// TestSourceRefusesAFileThatIsNotThere, and says so as itself rather
// than as a command that failed.
func TestSourceRefusesAFileThatIsNotThere(t *testing.T) {
	x := newTestShell(t)
	if err := x.Source(context.Background(), filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("sourcing a file that is not there should fail")
	}
}

// TestSourceRefusesToGoRoundForever: a file that sources itself would
// otherwise recurse until the stack ran out.
func TestSourceRefusesToGoRoundForever(t *testing.T) {
	x := newTestShell(t)
	path := filepath.Join(t.TempDir(), "loop")
	if err := os.WriteFile(path, []byte(". "+path+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := x.Source(context.Background(), path)
	if err == nil {
		t.Fatal("a file that sources itself should be stopped")
	}
	if !strings.Contains(x.out.String(), "sourcing is nested too deep") {
		t.Errorf("it should say why:\n%s", x.out.String())
	}
}

// TestSourceStopsWhenTheShellIsAskedTo: quit halfway down a file of
// four hundred removals means quit, not "quit when the file ends".
func TestSourceStopsWhenTheShellIsAskedTo(t *testing.T) {
	x := newTestShell(t)
	path := writeScript(t, "echo one\necho two\n")
	x.Quit()

	if err := x.Source(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if got := x.out.String(); got != "" {
		t.Errorf("nothing in the file should have run, got %q", got)
	}
	// Quit is safe more than once, since the usual path is a command
	// and a deferred call.
	x.Quit()
}

// TestAStoppedFileIsNotComplainedAboutTwice: where and why is already
// on the screen, and "source: stopped" underneath it adds nothing.
func TestAStoppedFileIsNotComplainedAboutTwice(t *testing.T) {
	x := newTestShell(t)
	path := writeScript(t, "nosuchthing\n")

	x.Do(context.Background(), ". "+path)
	if got := x.out.String(); strings.Contains(got, ".: stopped") {
		t.Errorf("the stop was reported a second time:\n%s", got)
	}
}

// TestSourceWantsExactlyOneFile.
func TestSourceWantsExactlyOneFile(t *testing.T) {
	x := newTestShell(t)
	for _, line := range []string{".", ". one two"} {
		if err := x.Do(context.Background(), line); err == nil {
			t.Errorf("%q should be refused", line)
		}
	}
	if got := x.do(t, ". --help"); !strings.Contains(got, "FILE") {
		t.Errorf(". --help should say what it takes, got %q", got)
	}
}

func writeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "script")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestEchoHasNoHelpOfItsOwn.
//
// echo exists to put a line into a file, so it has to be able to print
// the word "--help" like any other; a usage message there would be a
// command refusing to do the one thing it is for.
func TestEchoHasNoHelpOfItsOwn(t *testing.T) {
	x := newTestShell(t)
	if got, want := x.do(t, "echo --help"), "--help\n"; got != want {
		t.Errorf("echo printed %q, want %q", got, want)
	}
	if got, want := x.do(t, "echo"), "\n"; got != want {
		t.Errorf("echo with nothing to say printed %q, want %q", got, want)
	}
}

// TestQuitAndExitAreTheSameCommand, and both take --help.
func TestQuitAndExitAreTheSameCommand(t *testing.T) {
	if commands["quit"] != commands["exit"] {
		t.Error("exit should be quit, not a copy of it")
	}
	if commands["."] != commands["source"] {
		t.Error("source should be ., not a copy of it")
	}

	x := newTestShell(t)
	if got := x.do(t, "quit --help"); !strings.Contains(got, "quit") {
		t.Errorf("quit --help printed %q", got)
	}
	select {
	case <-x.quit:
		t.Error("--help should not have left the shell")
	default:
	}

	x.do(t, "quit")
	select {
	case <-x.quit:
	default:
		t.Error("quit should have left the shell")
	}
}

// TestCommandNamesIsEverythingSorted, which is what help and completion
// both read.
func TestCommandNamesIsEverythingSorted(t *testing.T) {
	names := commandNames()
	if len(names) != len(commands) {
		t.Errorf("commandNames has %d of %d commands", len(names), len(commands))
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] >= names[i] {
			t.Fatalf("not sorted at %d: %q then %q", i, names[i-1], names[i])
		}
	}
}

// ----------------------------------------------------------- the keys

// TestTheWorkingDirectoryIsThePrompt: the prompt is the only thing
// standing between a command and a remark said out loud.
func TestTheWorkingDirectoryIsThePrompt(t *testing.T) {
	x := newTestShell(t)
	if got := x.Pwd(); got != "/" {
		t.Errorf("Pwd = %q at the root", got)
	}
	x.prompt()
	if got := x.term.prompt; got != "/$ " {
		t.Errorf("prompt = %q", got)
	}

	x.cwd = []string{"Objects", "deeper"}
	if got, want := x.Pwd(), "/Objects/deeper"; got != want {
		t.Errorf("Pwd = %q, want %q", got, want)
	}
	x.prompt()
	if got, want := x.term.prompt, "/Objects/deeper$ "; got != want {
		t.Errorf("prompt = %q, want %q", got, want)
	}
}

// TestChatModeHoldsTheOtherModesLine.
//
// The two modes share one line editor, so a half-typed command has to
// be put aside when chat takes the keyboard and be there again on the
// way back -- otherwise switching modes to say something loses it.
func TestChatModeHoldsTheOtherModesLine(t *testing.T) {
	x := newTestShell(t)
	x.term.SetLine("cd Obj")

	x.setMode(modeChat)
	if !x.chatting() {
		t.Fatal("setMode did not take")
	}
	if got := x.term.Line(); got != "" {
		t.Errorf("the command line followed us into chat: %q", got)
	}
	if got := x.term.prompt; got != "Local> " {
		t.Errorf("the chat prompt is %q", got)
	}

	x.term.SetLine("half a sentence")
	x.setMode(modeCommand)
	if got := x.term.Line(); got != "cd Obj" {
		t.Errorf("the command line was not given back: %q", got)
	}

	// Setting the mode it is already in changes nothing, and above all
	// does not swap the held line for the one on the screen.
	x.setMode(modeCommand)
	if got := x.term.Line(); got != "cd Obj" {
		t.Errorf("a mode change to the same mode disturbed the line: %q", got)
	}
}

// TestChatConfiguredAtStartup, for somebody who lives in chat.
func TestChatConfiguredAtStartup(t *testing.T) {
	x := newTestShellOn(t, newFakeGrid(t), Config{Chat: true, Prefix: 27})
	if !x.chatting() {
		t.Error("--chat should start in chat mode")
	}
}

// TestTheKeysThatAreNotTheLineEditors.
//
// Enter, tab, the prefix key and the two interrupts are the shell's
// rather than the editor's; everything else is typing.
func TestTheKeysThatAreNotTheLineEditors(t *testing.T) {
	ctx := context.Background()
	x := newTestShell(t)

	// An ordinary key is typing.
	for _, r := range "pwd" {
		x.key(ctx, r)
	}
	if got := x.term.Line(); got != "pwd" {
		t.Fatalf("typing left %q", got)
	}

	// Ctrl-C with something typed clears it and stays.
	x.key(ctx, 3)
	if got := x.term.Line(); got != "" {
		t.Errorf("Ctrl-C left %q on the line", got)
	}
	select {
	case <-x.quit:
		t.Fatal("Ctrl-C on a typed line should not leave the shell")
	default:
	}

	// Enter runs it.
	x.term.SetLine("echo run me")
	x.key(ctx, '\r')
	if got := x.out.String(); !strings.Contains(got, "run me") {
		t.Errorf("Enter did not run the line: %q", got)
	}
	// A line of nothing is not a command, and does not go into history.
	x.out.Reset()
	x.key(ctx, '\n')
	if got := x.out.String(); got != "" {
		t.Errorf("an empty line printed %q", got)
	}

	// Ctrl-D on an empty line is end of input.
	x.key(ctx, 4)
	select {
	case <-x.quit:
	default:
		t.Error("Ctrl-D on an empty line should leave the shell")
	}
}

// TestCtrlCLeavesChatRatherThanTheShell.
//
// In chat mode there is somewhere to go back to, so the interrupt goes
// there; from a command prompt there is nowhere left and it quits.
func TestCtrlCLeavesChatRatherThanTheShell(t *testing.T) {
	ctx := context.Background()
	x := newTestShell(t)

	x.setMode(modeChat)
	x.key(ctx, 3)
	if x.chatting() {
		t.Error("Ctrl-C should have left chat mode")
	}
	select {
	case <-x.quit:
		t.Fatal("Ctrl-C in chat should not leave the shell")
	default:
	}

	// The prefix key is the other way out, and does nothing outside
	// chat -- it is an ordinary key there.
	x.setMode(modeChat)
	x.key(ctx, x.cfg.Prefix)
	if x.chatting() {
		t.Error("the prefix key should have left chat mode")
	}

	x.key(ctx, 3)
	select {
	case <-x.quit:
	default:
		t.Error("Ctrl-C at an empty command prompt should leave the shell")
	}
}

// TestHistoryWalksWhatWasTyped, at a command prompt.  The chat ring is
// TestChatHasAHistoryOfItsOwn, below.
func TestHistoryWalksWhatWasTyped(t *testing.T) {
	ctx := context.Background()
	x := newTestShell(t)

	// Nothing typed yet: the arrows have nothing to walk.
	x.key(ctx, keyUp)
	if got := x.term.Line(); got != "" {
		t.Errorf("an empty history put %q on the line", got)
	}

	for _, line := range []string{"echo one", "echo two", "echo two"} {
		x.term.SetLine(line)
		x.key(ctx, '\r')
	}

	// The same command twice running is one entry, as in a shell.
	if len(x.history.lines) != 2 {
		t.Fatalf("history is %q, want the repeat collapsed", x.history.lines)
	}

	x.key(ctx, keyUp)
	if got := x.term.Line(); got != "echo two" {
		t.Errorf("up gave %q", got)
	}
	x.key(ctx, keyUp)
	if got := x.term.Line(); got != "echo one" {
		t.Errorf("up twice gave %q", got)
	}
	x.key(ctx, keyUp) // and no further
	if got := x.term.Line(); got != "echo one" {
		t.Errorf("up past the start gave %q", got)
	}
	x.key(ctx, keyDown)
	if got := x.term.Line(); got != "echo two" {
		t.Errorf("down gave %q", got)
	}
	// Down past the end is a fresh line, not the last command again.
	x.key(ctx, keyDown)
	if got := x.term.Line(); got != "" {
		t.Errorf("down past the end gave %q", got)
	}
	x.key(ctx, keyDown)
	if got := x.term.Line(); got != "" {
		t.Errorf("down past the end twice gave %q", got)
	}

	// None of it is reachable from chat, where nothing has been said
	// yet: up leaves the half-typed sentence where it is rather than
	// offering a command to say out loud.
	x.setMode(modeChat)
	x.term.SetLine("mid sentence")
	x.key(ctx, keyUp)
	if got := x.term.Line(); got != "mid sentence" {
		t.Errorf("the command history reached a chat prompt: %q", got)
	}
	x.key(ctx, keyDown)
	if got := x.term.Line(); got != "mid sentence" {
		t.Errorf("down on an empty chat history cleared the line: %q", got)
	}
}

// TestChatHasAHistoryOfItsOwn.
//
// A line said in chat used to be unrecallable: the arrows were guarded
// with "if !chat" and did nothing there at all.  Now they walk what was
// said -- a ring of its own, because a command recalled at a chat
// prompt would be said out loud to whoever is listening.
func TestChatHasAHistoryOfItsOwn(t *testing.T) {
	ctx := context.Background()
	x := newTestShell(t)

	// One command, so there is something in the other ring to leak.
	x.term.SetLine("echo a command")
	x.key(ctx, '\r')

	x.setMode(modeChat)
	for _, line := range []string{"the door is open", "   ", "the lamp is lit", "the lamp is lit"} {
		x.term.SetLine(line)
		x.key(ctx, '\r')
	}

	// A line of nothing was not said and is not kept, and the same
	// line twice running is one entry -- both the rules the command
	// ring already had.
	want := []string{"the door is open", "the lamp is lit"}
	if got := x.said.lines; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("what was said is %q, want %q", got, want)
	}

	x.key(ctx, keyUp)
	if got := x.term.Line(); got != "the lamp is lit" {
		t.Errorf("up in chat gave %q", got)
	}
	x.key(ctx, keyUp)
	if got := x.term.Line(); got != "the door is open" {
		t.Errorf("up twice in chat gave %q", got)
	}
	x.key(ctx, keyUp) // and no further, and never into the commands
	if got := x.term.Line(); got != "the door is open" {
		t.Errorf("up past the start of the chat history gave %q", got)
	}
	x.key(ctx, keyDown)
	if got := x.term.Line(); got != "the lamp is lit" {
		t.Errorf("down in chat gave %q", got)
	}
	x.key(ctx, keyDown)
	if got := x.term.Line(); got != "" {
		t.Errorf("down past the end in chat gave %q", got)
	}

	// Recalling put the line back to be edited and sent nothing.  What
	// went out is the three lines that were entered -- the repeat was
	// said twice and is collapsed only in the ring, exactly as a
	// command run twice runs twice.
	if got := len(x.grid.Sent()); got != 3 {
		t.Errorf("%d things went to the grid, want the 3 lines that were entered", got)
	}

	// And the other direction: the command ring is where it was left,
	// with no remark in it.
	x.setMode(modeCommand)
	x.key(ctx, keyUp)
	if got := x.term.Line(); got != "echo a command" {
		t.Errorf("up at a command prompt gave %q", got)
	}
}

// TestARecalledLineIsEditedAndSentByReturn, which is the difference
// between a history and a repeat key: what comes back is on the line
// to be changed, and nothing goes until Enter.
func TestARecalledLineIsEditedAndSentByReturn(t *testing.T) {
	ctx := context.Background()
	x := newTestShell(t)
	x.setMode(modeChat)

	x.term.SetLine("the door is open")
	x.key(ctx, '\r')
	x.out.Reset()

	x.key(ctx, keyUp)
	if got := x.term.Line(); got != "the door is open" {
		t.Fatalf("up gave %q", got)
	}
	if got := x.out.String(); got != "" {
		t.Errorf("the recall itself said something: %q", got)
	}

	for range len("open") {
		x.key(ctx, 127) // backspace
	}
	for _, r := range "shut" {
		x.key(ctx, r)
	}
	x.key(ctx, '\r')
	if got := x.out.String(); !strings.Contains(got, "> [Local] the door is shut") {
		t.Errorf("the edited line went out as %q", got)
	}

	// Both are kept, in the order they were said.
	want := []string{"the door is open", "the door is shut"}
	if got := x.said.lines; len(got) != len(want) || got[1] != want[1] {
		t.Fatalf("what was said is %q, want %q", got, want)
	}
}

// TestALineSaidInTheWrongPlaceIsResentInTheRight.
//
// This is the case the whole thing was asked for.  A remark goes to
// local chat when it was meant for one person: tab moves to that
// conversation, up brings the line back, and Enter sends it there.  It
// works because the ring belongs to the shell and not to the
// conversation, so tab leaves what is on offer alone.
func TestALineSaidInTheWrongPlaceIsResentInTheRight(t *testing.T) {
	ctx := context.Background()
	x := newTestShell(t)
	c, _ := x.talk.Open(testSomebody, "Some Body")
	x.setMode(modeChat)

	// Said out loud, and it should not have been.
	x.term.SetLine("the key is under the mat")
	x.key(ctx, '\r')
	if got := x.out.String(); !strings.Contains(got, "> [Local] the key is under the mat") {
		t.Fatalf("the remark went %q", got)
	}

	// Tab to the conversation it was meant for.
	x.key(ctx, '\t')
	if got := x.talk.Current(); got != c {
		t.Fatalf("tab left us in %q", got.Label())
	}

	// Up, and there it is: the ring did not change under the tab.
	x.key(ctx, keyUp)
	if got := x.term.Line(); got != "the key is under the mat" {
		t.Fatalf("up after tabbing gave %q", got)
	}

	x.out.Reset()
	x.key(ctx, '\r')
	if got := x.out.String(); !strings.Contains(got, "> [IM Some Body] the key is under the mat") {
		t.Errorf("the resent line went %q", got)
	}
	if _, ok := x.grid.Sent()[1].(*msg.ImprovedInstantMessage); !ok {
		t.Errorf("the resent line left as %T, want an instant message", x.grid.Sent()[1])
	}
}

// TestEnterInChatDoesNotEchoTheLine: send prints it in its own marked
// form, so echoing it here would say it twice and lose which of the two
// actually went.
func TestEnterInChatDoesNotEchoTheLine(t *testing.T) {
	x := newTestShell(t)
	x.setMode(modeChat)

	x.term.SetLine("   ")
	x.key(context.Background(), '\r')
	if got := x.out.String(); got != "" {
		t.Errorf("a line of spaces was sent: %q", got)
	}

	x.term.SetLine("hello everyone")
	x.key(context.Background(), '\r')
	got := x.out.String()
	if !strings.Contains(got, "[Local] hello everyone") {
		t.Errorf("the line should be printed in its marked form: %q", got)
	}
	if strings.Contains(got, "Local> hello everyone") {
		t.Errorf("chat echoed the line as well as marking it: %q", got)
	}
}

// TestTabIsTwoThingsInTwoModes: completion where there are commands to
// complete, conversations where there are not.  A typed answer takes
// completion.
func TestTabIsTwoThingsInTwoModes(t *testing.T) {
	ctx := context.Background()
	x := newTestShell(t)

	x.term.SetLine("featu")
	x.key(ctx, '\t')
	if got := x.term.Line(); got != "features" {
		t.Errorf("tab in command mode gave %q", got)
	}

	x.setMode(modeChat)
	x.term.SetLine("featu")
	x.key(ctx, '\t')
	if got := x.term.Line(); got != "featu" {
		t.Errorf("tab in chat mode edited the line: %q", got)
	}

	// A typed answer is not chat, and completes.
	x.setMode(modeText)
	x.term.SetLine("featu")
	x.key(ctx, '\t')
	if got := x.term.Line(); got != "features" {
		t.Errorf("tab while typing an answer gave %q", got)
	}
}

// ------------------------------------------------------------ the loop

// TestRunEndsWhenTheInputDoes, which is what a pipe reaching its end
// looks like.
func TestRunEndsWhenTheInputDoes(t *testing.T) {
	x := newTestShell(t)
	go func() {
		for _, r := range "echo through the loop\r" {
			x.keys <- r
		}
		close(x.keys)
	}()

	if err := x.Run(context.Background()); err != nil {
		t.Fatalf("Run = %v", err)
	}
	got := x.out.String()
	if !strings.Contains(got, "through the loop") {
		t.Errorf("the loop did not run the line:\n%s", got)
	}
	// The banner says who and where, and which key comes back.
	if !strings.Contains(got, "Quark Idlemind in Test Region") {
		t.Errorf("no banner:\n%s", got)
	}
	if !strings.Contains(got, "ESC to come back") {
		t.Errorf("the banner should name the prefix key:\n%s", got)
	}
}

// TestRunEndsWhenTheShellIsAskedTo.
func TestRunEndsWhenTheShellIsAskedTo(t *testing.T) {
	x := newTestShell(t)
	go func() {
		for _, r := range "quit\r" {
			x.keys <- r
		}
	}()
	if err := x.Run(context.Background()); err != nil {
		t.Fatalf("Run = %v", err)
	}
}

// TestRunEndsWhenTheSessionDoes, which is a daemon going away underneath
// the shell rather than anything the person did.
func TestRunEndsWhenTheSessionDoes(t *testing.T) {
	x := newTestShell(t)
	go func() {
		time.Sleep(10 * time.Millisecond)
		x.grid.Close()
	}()
	if err := x.Run(context.Background()); err != nil {
		t.Fatalf("Run = %v", err)
	}
}

// TestRunSaysTheSessionEndedAndWhy: slgod ends the stream of a session
// that is stopped for good or no longer hosted, and says why in a
// sentence for a person.  The shell passes the sentence on as the reason
// it stopped, without the rpc status wrapped round it.
func TestRunSaysTheSessionEndedAndWhy(t *testing.T) {
	x := newTestShell(t)
	x.grid.mu.Lock()
	x.grid.endedWith = status.Error(codes.FailedPrecondition,
		"example is not connected (logged out); it will not come back on its own")
	x.grid.mu.Unlock()
	go func() {
		time.Sleep(10 * time.Millisecond)
		x.grid.Close()
	}()
	err := x.Run(context.Background())
	want := "the session ended: example is not connected (logged out); it will not come back on its own"
	if err == nil || err.Error() != want {
		t.Fatalf("Run = %v, want %q", err, want)
	}
}

// TestRunEndsWithItsContext: a SIGTERM cancels the context, and the
// shell has to come out rather than sit waiting for a key.
func TestRunEndsWithItsContext(t *testing.T) {
	x := newTestShell(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	if err := x.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want the cancellation", err)
	}
}

// --------------------------------------------------------- the output

// TestOutputIsWrittenALineAtATime.
//
// Everything a command prints goes to an io.Writer, and the one that
// reaches the terminal has to turn writes into whole lines: a message
// arriving mid-write would otherwise land inside one.
func TestOutputIsWrittenALineAtATime(t *testing.T) {
	x := newTestShell(t)
	w := x.stdout()

	if n, err := w.Write([]byte("one\ntwo\n")); n != 8 || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if got, want := x.out.String(), "one\ntwo\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// A write with no newline on the end is still flushed, since the
	// command may have nothing more to say.
	x.out.Reset()
	w.Write([]byte("no newline"))
	if got, want := x.out.String(), "no newline\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestNoticesAreStampedWithTheTime, so that a line that arrived while
// something else was being read can be placed afterwards.
func TestNoticesAreStampedWithTheTime(t *testing.T) {
	x := newTestShell(t)
	x.noticef("%s came online", "Somebody")

	got := strings.TrimRight(x.out.String(), "\n")
	if !strings.HasSuffix(got, " * Somebody came online") {
		t.Errorf("a notice reads %q", got)
	}
	if stamp := strings.SplitN(got, " ", 2)[0]; len(stamp) != len("15:04:05") {
		t.Errorf("the stamp is %q", stamp)
	}
}

// TestTheBannerSaysHowThisSessionWasReached.
//
// Directly is worth saying out loud, because quitting then logs the
// avatar out -- which through a daemon it does not.
func TestTheBannerSaysHowThisSessionWasReached(t *testing.T) {
	x := newTestShell(t)
	x.banner()
	if got := x.out.String(); !strings.Contains(got, "through fake:7807") {
		t.Errorf("the banner should name the daemon: %q", got)
	}

	x = newTestShellOn(t, newFakeGrid(t), Config{Direct: true, Prefix: 7})
	x.banner()
	got := x.out.String()
	if !strings.Contains(got, "quitting logs out") {
		t.Errorf("a direct session should say what quitting costs: %q", got)
	}
	if !strings.Contains(got, "^G to come back") {
		t.Errorf("the banner should name the configured prefix key: %q", got)
	}
}

// TestTheBannerNamesARegionItWasNotTold: a session that attached before
// the handshake arrived has no region name, and an empty one would read
// as a region called nothing.
func TestTheBannerNamesARegionItWasNotTold(t *testing.T) {
	f := newFakeGrid(t)
	f.info.Region = ""
	x := newTestShellOn(t, f, Config{Addr: "fake:7807", Prefix: 27})

	x.banner()
	if got := x.out.String(); !strings.Contains(got, "in an unnamed region") {
		t.Errorf("the banner reads %q", got)
	}
}
