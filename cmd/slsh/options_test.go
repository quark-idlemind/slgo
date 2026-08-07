package main

import (
	"io"
	"strings"
	"testing"
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
		rest, _, err := subOptions("say", "TEXT ...", &o, io.Discard, c.args)
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
	if _, _, err := subOptions("friends", "", &o, io.Discard, nil); err != nil || o.All {
		t.Errorf("no flags: got all=%v err=%v", o.All, err)
	}
	o = friendsOptions{}
	if _, _, err := subOptions("friends", "", &o, io.Discard, []string{"-a"}); err != nil || !o.All {
		t.Errorf("-a: got all=%v err=%v", o.All, err)
	}
	if _, _, err := subOptions("friends", "", &friendsOptions{}, io.Discard, []string{"-z"}); err == nil {
		t.Error("-z should be refused")
	}
}

// TestSubOptionsAreNotShared: the shell runs many commands in one
// process, so one command's flags must not still be registered for the
// next -- which is what a package-level flag set would do.
func TestSubOptionsAreNotShared(t *testing.T) {
	var l lsOptions
	if _, _, err := subOptions("ls", "[PATH]", &l, io.Discard, []string{"-l"}); err != nil {
		t.Fatal(err)
	}
	// -l belongs to ls, not to friends.
	if _, _, err := subOptions("friends", "", &friendsOptions{}, io.Discard, []string{"-l"}); err == nil {
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
	rest, done, err := subOptions("ls", "[PATH]", &o, &out, []string{"-h"})
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
	for _, want := range []string{"ls", "[PATH]", "-l", "-r", "-t", "-T", "--help"} {
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
	_, done, err := subOptions("friends", "", &friendsOptions{}, &out, []string{"--help"})
	if err != nil || !done || out.Len() == 0 {
		t.Errorf("--help: done=%v err=%v output=%q", done, err, out.String())
	}
}
