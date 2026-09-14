package main

import (
	"context"
	"strings"
	"testing"
)

// A line arrives from the grid as somebody typed it into a viewer.
// What it splits into is the whole of the syntax, so the rules are
// worth nailing down: quotes hold a word together, nothing else is
// interpreted, and an unclosed quote is refused rather than guessed at.
func TestALineSplitsIntoWords(t *testing.T) {
	for _, tc := range []struct {
		line string
		want []string
	}{
		{"where", []string{"where"}},
		{"  where  ", []string{"where"}},
		{"", nil},
		{"say hello there", []string{"say", "hello", "there"}},
		{`say "hello there"`, []string{"say", "hello there"}},
		{`say 'hello there'`, []string{"say", "hello there"}},
		{`slbench --statement "llSin(1.0);"`, []string{"slbench", "--statement", "llSin(1.0);"}},
		{`slrun --done ""`, []string{"slrun", "--done", ""}},
		{`tp "Example Region" 128 128 25`, []string{"tp", "Example Region", "128", "128", "25"}},
		{"say a\tb", []string{"say", "a", "b"}},
		// A quote is only a quote at the start of a word or after an
		// equals sign.  Inventory is full of apostrophes and none of
		// them should be an unclosed quote.
		{`cat Notecards/Bob's list`, []string{"cat", "Notecards/Bob's", "list"}},
		{`slbench --statement="llSin(1.0);"`, []string{"slbench", "--statement=llSin(1.0);"}},
		{`say it"s fine`, []string{"say", `it"s`, "fine"}},
	} {
		got, err := splitLine(tc.line)
		if err != nil {
			t.Errorf("splitLine(%q): %v", tc.line, err)
			continue
		}
		if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
			t.Errorf("splitLine(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
}

func TestAnUnclosedQuoteIsRefused(t *testing.T) {
	if _, err := splitLine(`say "hello`); err == nil {
		t.Fatal("an unclosed quote was accepted")
	}
}

// Nothing in a command line reaches a shell, so nothing in it can mean
// anything to one.  The characters that would are ordinary characters
// here, and this is the test that says so.
func TestNothingIsExpanded(t *testing.T) {
	got, err := splitLine("say $HOME `id` *.go >out; rm -rf /")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"say", "$HOME", "`id`", "*.go", ">out;", "rm", "-rf", "/"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAnUnknownCommandSaysSo(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	got := send(t, d, b, "flurble")
	if !strings.Contains(got, `no command called "flurble"`) {
		t.Errorf("got %q", got)
	}
	if !strings.Contains(got, ":help") {
		t.Errorf("the refusal does not say how to find the commands: %q", got)
	}
}

// The prefix and nothing else is somebody asking what this thing is.
func TestAnEmptyLineIsHelp(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	got := send(t, d, b, "")
	if !strings.Contains(got, "looking") || !strings.Contains(got, "where") {
		t.Errorf("an empty line did not print the help: %q", got)
	}
}

func TestAnAliasReachesTheCommand(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	d.cfg.Aliases["position"] = "where"
	if got := send(t, d, b, "position"); !strings.Contains(got, "Nowhere at 128") {
		t.Errorf("the alias did not reach where: %q", got)
	}
}

// Every command has to be able to say what it takes, and the line it
// says it with is composed rather than written down -- so a command
// whose composer produced nothing would be one nobody could ask about.
func TestEveryCommandAnswersHelp(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	for _, name := range commandNames() {
		got := send(t, d, b, name+" --help")
		if !strings.HasPrefix(got, "usage: "+name) {
			t.Errorf("%s --help printed %q", name, got)
		}
	}
}

// help COMMAND is the same line, reached the other way round, and is
// what the listing tells people to type.
func TestHelpDescribesOneCommand(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	got := send(t, d, b, "help tp")
	if !strings.Contains(got, "usage: tp") {
		t.Errorf("help tp printed %q", got)
	}
	if !strings.Contains(got, "home") {
		t.Errorf("help tp did not mention what it takes: %q", got)
	}

	if got := send(t, d, b, "help autobench"); !strings.Contains(got, "slbench") {
		t.Errorf("help autobench did not say what it is: %q", got)
	}
	if got := send(t, d, b, "help flurble"); !strings.Contains(got, "no command") {
		t.Errorf("help for nothing printed %q", got)
	}
}

// The help flag is on every command, so it says nothing about the one
// being looked at, and it is taken out of the composed line.  The
// options underneath still list it, which is where a person looks.
func TestTheUsageLineLeavesOutTheHelpFlag(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"[-h]", ""},
		{"[-hl]", "[-l]"},
		{"[-lh] PATH", "[-l] PATH"},
		{"[-lR] PATH", "[-lR] PATH"},
		{"[--wait SECONDS]", "[--wait SECONDS]"},
	} {
		if got := withoutHelp(tc.in); got != tc.want {
			t.Errorf("withoutHelp(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A command handed the wrong number of things says so and then says how
// it is typed, out of the same composer --help uses.
func TestARefusalEndsInTheUsageLine(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	got := send(t, d, b, "im")
	if !strings.Contains(got, "usage: im") {
		t.Errorf("got %q", got)
	}
}

// "as" is what lets one avatar be driven through another, and a ring of
// avatars handing a command round is the one way it does not end.
func TestAsWillNotNestForEver(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	ctx := context.Background()
	r := &req{d: d, bot: b, from: testSender, who: "Trusted Resident", base: ctx, depth: asDepth}
	var out strBuilder
	err := r.Run(ctx, &out, "as example where")
	if err == nil || !strings.Contains(err.Error(), "as far as it goes") {
		t.Fatalf("err = %v, want the depth limit", err)
	}
}

func TestAsNeedsAnAvatarThisDaemonHolds(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	got := send(t, d, b, "as somebody where")
	if !strings.Contains(got, "does not hold") {
		t.Errorf("got %q", got)
	}
}

func TestAsRunsAsTheOtherAvatar(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	got := send(t, d, b, "as example where")
	if !strings.Contains(got, "as example:") || !strings.Contains(got, "Nowhere at 128") {
		t.Errorf("got %q", got)
	}
}

// A command that names an avatar this daemon has lost should say that,
// rather than failing somewhere further in with a nil session.
func TestACommandOnADetachedAvatarSaysSo(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	b.setSession(nil)
	b.setState(stateWaiting, "the simulator went quiet")
	got := send(t, d, b, "where")
	if !strings.Contains(got, "is not connected") || !strings.Contains(got, "went quiet") {
		t.Errorf("got %q", got)
	}
}
