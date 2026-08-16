package main

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/pborman/getopt/v2"
	"github.com/pborman/options"
)

// TestSayOptions: a NEGATIVE channel has to survive the option parser.
//
// Negative channels are how a script is spoken to, so "say -c -12345
// ok" is the case that matters, and a parser that read -12345 as a
// bundle of flags would break the thing say is most used for.
func TestSayOptions(t *testing.T) {
	for _, c := range []struct {
		args    []string
		channel int
		rest    string
		bad     bool
	}{
		{args: []string{"hello", "there"}, rest: "hello there"},
		{args: []string{"-c", "5", "hi"}, channel: 5, rest: "hi"},
		{args: []string{"-c", "-12345", "ok"}, channel: -12345, rest: "ok"},
		{args: []string{"-c-12345", "ok"}, channel: -12345, rest: "ok"},
		{args: []string{"-c", "notanumber", "x"}, bad: true},
	} {
		var o sayOptions
		rest, _, err := subOptions("say", &o, io.Discard, c.args)
		if c.bad {
			if err == nil {
				t.Errorf("%v: should have been refused", c.args)
			}
			continue
		}
		if err != nil {
			t.Errorf("%v: %v", c.args, err)
			continue
		}
		if o.Channel != c.channel {
			t.Errorf("%v: channel %d, want %d", c.args, o.Channel, c.channel)
		}
		if got := join(rest); got != c.rest {
			t.Errorf("%v: text %q, want %q", c.args, got, c.rest)
		}
	}
}

// TestFriendsOptions is the whole of that command's surface.
func TestFriendsOptions(t *testing.T) {
	var o friendsOptions
	if _, _, err := subOptions("friends", &o, io.Discard, nil); err != nil || o.All {
		t.Errorf("no flags: got all=%v err=%v", o.All, err)
	}
	o = friendsOptions{}
	if _, _, err := subOptions("friends", &o, io.Discard, []string{"-a"}); err != nil || !o.All {
		t.Errorf("-a: got all=%v err=%v", o.All, err)
	}
	if _, _, err := subOptions("friends", &friendsOptions{}, io.Discard, []string{"-z"}); err == nil {
		t.Error("-z should be refused")
	}
}

// TestSubOptionsAreNotShared: the shell runs many commands in one
// process, so one command's flags must not still be registered for the
// next -- which is what a package-level flag set would do.
func TestSubOptionsAreNotShared(t *testing.T) {
	var l lsOptions
	if _, _, err := subOptions("ls", &l, io.Discard, []string{"-l"}); err != nil {
		t.Fatal(err)
	}
	// -l belongs to ls, not to friends.
	if _, _, err := subOptions("friends", &friendsOptions{}, io.Discard, []string{"-l"}); err == nil {
		t.Error("friends accepted -l, so the sets are shared")
	}
}

func join(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out
}

// TestHelpPrintsTheCommandsOwnOptions, and not the program's.
//
// options.Help would have printed getopt's global set here, which holds
// slsh's own flags -- so "ls -h" answered with --addr. It also exits the
// process, which in a shell is the session.
func TestHelpPrintsTheCommandsOwnOptions(t *testing.T) {
	var out strings.Builder
	var o lsOptions
	rest, done, err := subOptions("ls", &o, &out, []string{"-h"})
	if err != nil {
		t.Fatal(err)
	}
	if !done {
		t.Error("--help should say the command is finished")
	}
	if rest != nil {
		t.Errorf("nothing should be left to do, got %q", rest)
	}

	got := out.String()
	for _, want := range []string{"ls", "[PATH]", "-l", "-r", "-t", "--help"} {
		if !strings.Contains(got, want) {
			t.Errorf("the usage should mention %q:\n%s", want, got)
		}
	}
	// The program's own flags belong to slsh, not to ls.
	if strings.Contains(got, "--addr") {
		t.Errorf("that is slsh's usage, not ls's:\n%s", got)
	}
}

// TestLongHelpToo, since a shell is typed at by people who write it out.
func TestLongHelpToo(t *testing.T) {
	var out strings.Builder
	_, done, err := subOptions("friends", &friendsOptions{}, &out, []string{"--help"})
	if err != nil || !done || out.Len() == 0 {
		t.Errorf("--help: done=%v err=%v output=%q", done, err, out.String())
	}
}

// TestSomethingThatIsNotAnOptionSetIsReportedRatherThanPanicking.
//
// The option structs are read by reflection, so what a command passes
// is only checked when it runs.  It comes back as an error, which a
// shell can print, rather than as a panic, which would take the session
// with it.
func TestSomethingThatIsNotAnOptionSetIsReportedRatherThanPanicking(t *testing.T) {
	if _, _, err := subOptions("odd", new(int), io.Discard, nil); err == nil {
		t.Error("an int is not a set of options and should be refused")
	}
}

// TestTheUsageLineIsTheSameInAllThreePlaces.
//
// The whole point of deriving it.  A help listing, the command's own
// refusal and its --help used to be three hand-kept strings, and they
// had already drifted; this is what stops them drifting again, for
// every command at once rather than for the ones somebody remembers.
func TestTheUsageLineIsTheSameInAllThreePlaces(t *testing.T) {
	var listing bytes.Buffer
	if err := helpAll(&listing); err != nil {
		t.Fatal(err)
	}
	for _, name := range commandNames() {
		c := commands[name]
		want := c.usage(name)

		if !strings.Contains(usageError(name).Error(), want) {
			t.Errorf("%s: its refusal says %q, not %q", name, usageError(name), want)
		}
		if c.flags == nil {
			// echo, which has no options and no --help to print one.
			continue
		}
		var help bytes.Buffer
		if _, done, err := subOptions(name, c.flags(), &help, []string{"--help"}); err != nil || !done {
			t.Fatalf("%s --help: done=%v err=%v", name, done, err)
		}
		if !strings.Contains(help.String(), "Usage: "+want) {
			t.Errorf("%s: --help says\n%s\nrather than %q", name, help.String(), want)
		}
	}
	// The listing dedups aliases, so a command is there under one of its
	// names rather than under all of them.
	seen := map[*command]bool{}
	for _, name := range commandNames() {
		c := commands[name]
		if seen[c] {
			continue
		}
		seen[c] = true
		if !strings.Contains(listing.String(), c.usage(name)) {
			t.Errorf("%s: the help listing does not carry %q", name, c.usage(name))
		}
	}
}

// TestAFlagAddedToAStructReachesTheUsageLineWithNoOtherEdit, which is
// the property the derivation exists to give and the one thing no
// amount of correcting the existing strings would have bought.
func TestAFlagAddedToAStructReachesTheUsageLineWithNoOtherEdit(t *testing.T) {
	type before struct {
		Help bool `getopt:"--help -h  show what this command takes"`
	}
	type after struct {
		Wobble string `getopt:"--wobble=DEGREES  invented by a test"`
		Help   bool   `getopt:"--help -h         show what this command takes"`
	}
	c := &command{params: "PATH", flags: func() any { return new(before) }}
	if got := c.usage("wobbler"); got != "wobbler PATH" {
		t.Fatalf("usage before the flag is %q", got)
	}
	c.flags = func() any { return new(after) }
	if got := c.usage("wobbler"); got != "wobbler [--wobble DEGREES] PATH" {
		t.Errorf("the new flag did not reach the usage line: %q", got)
	}
}

// TestACommandThatParsesADifferentStructFromTheOneItAdvertises.
//
// The one way left to make the usage line describe a command the
// parsing does not.  It is a mistake in this package's own tables
// rather than anything a person typed, so it is refused loudly and by
// name instead of quietly printing the wrong flags.
func TestACommandThatParsesADifferentStructFromTheOneItAdvertises(t *testing.T) {
	_, _, err := subOptions("place", &takeFlags{}, io.Discard, nil)
	if err == nil {
		t.Fatal("place parsing take's flags should be refused")
	}
	for _, want := range []string{"place", "takeFlags", "placeFlags"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should name %q, got %v", want, err)
		}
	}
}

// TestEveryCommandsOptionStructRegisters.
//
// usage() throws away what RegisterSet said, because a help listing is
// better with a line missing its flags than not printed at all -- so
// nothing else would notice a struct with a bad tag in it until
// somebody ran that command.  This notices.
func TestEveryCommandsOptionStructRegisters(t *testing.T) {
	for _, name := range commandNames() {
		c := commands[name]
		if c.flags == nil {
			continue
		}
		set := getopt.New()
		if err := options.RegisterSet(name, c.flags(), set); err != nil {
			t.Errorf("%s: its option struct will not register: %v", name, err)
		}
	}
}

// TestUsageErrorPutsItsNotesUnderTheLine, since a note says what to do
// instead and the line says what the command takes; running the two
// together made the line the thing nobody read.
func TestUsageErrorPutsItsNotesUnderTheLine(t *testing.T) {
	err := usageError("place", "one object", "move is the other one")
	want := "usage: " + commands["place"].usage("place") +
		"\n        one object" +
		"\n        move is the other one"
	if err.Error() != want {
		t.Errorf("usageError produced\n%s\nwant\n%s", err, want)
	}
}

// TestTheHelpFlagIsNotInTheUsageLineButIsStillUnderIt.
//
// Every command has --help, so naming it in the line distinguishes none
// of them and costs the width on every row of every listing -- the
// waiting group read "waiting [-ah]", "no [-h] N", "ignore [-h] N", and
// answer's line was pushed onto a second row to make room for it.  It
// comes out in the composer so that all three places lose it together;
// --help goes on listing it underneath, which is where somebody looks
// for a flag, and is the ordinary shape of a Unix tool.
func TestTheHelpFlagIsNotInTheUsageLineButIsStillUnderIt(t *testing.T) {
	for _, name := range commandNames() {
		c := commands[name]
		if c.flags == nil {
			continue // echo, which has no flags at all
		}
		line := c.usage(name)
		if strings.Contains(line, "--help") || strings.Contains(line, "-h") {
			t.Errorf("%s: the help flag is still in the usage line: %q", name, line)
		}

		var help bytes.Buffer
		if _, _, err := subOptions(name, c.flags(), &help, []string{"--help"}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(help.String(), "--help") {
			t.Errorf("%s --help stopped listing --help:\n%s", name, help.String())
		}
	}
}

// TestTakingTheHelpFlagOutLeavesTheOthersAlone.
//
// The flag has to come out of a cluster as well as on its own, and a
// cluster with nothing left in it has to go rather than become "[-]".
// These are the four shapes getopt produces, worked through the one
// function that has to get them right.
func TestTakingTheHelpFlagOutLeavesTheOthersAlone(t *testing.T) {
	for _, c := range []struct {
		name string
		want string
	}{
		{"pwd", "pwd"},                                   // help alone
		{"waiting", "waiting [-a]"},                      // help in a cluster
		{"login", "login [-f] NAME"},                     // help in a cluster, with parameters
		{"place", "place [--at X,Y,Z] PATH|UUID"},        // help alone beside a long option
		{"watch", "watch [-t D] [NAME...]"},              // help alone beside a short one with a value
		{"objects", "objects [-c] [--owner WHO] [TEXT]"}, // both at once
	} {
		if got := commands[c.name].usage(c.name); got != c.want {
			t.Errorf("%s: usage is %q, want %q", c.name, got, c.want)
		}
	}
}
