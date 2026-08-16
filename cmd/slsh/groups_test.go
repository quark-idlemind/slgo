package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

// TestEveryCommandIsInAGroup: help is the way commands are found, so a
// command in no group is one nobody will find.  This is the test that
// keeps that true as commands are added -- it fails when a new one is
// registered and not filed.
func TestEveryCommandIsInAGroup(t *testing.T) {
	if missing := ungrouped(); len(missing) > 0 {
		t.Errorf("in no group, so \"help\" cannot lead anyone to them: %s\n"+
			"add them to a group in groups.go", strings.Join(missing, ", "))
	}
}

// TestGroupsNameRealCommands is the other direction: a group listing a
// command that does not exist would print a heading and nothing under
// it, which reads as "there is nothing here" rather than as a mistake.
func TestGroupsNameRealCommands(t *testing.T) {
	for _, g := range groups {
		for _, n := range g.members {
			if _, ok := commands[n]; !ok {
				t.Errorf("group %q lists %q, which is not a command", g.name, n)
			}
		}
	}
}

// TestGroupNamesAreDistinct: two groups of one name would make one of
// them unreachable.  Names may clash with COMMANDS -- help only ever
// means a group -- but not with each other.
func TestGroupNamesAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, g := range groups {
		if seen[g.name] {
			t.Errorf("two groups are called %q", g.name)
		}
		seen[g.name] = true
	}
}

// TestHelpForACommandPointsAtTheCommand: the moment somebody types
// "help cat" is the moment they want to know where a command's own help
// lives, so that is where to tell them.
func TestHelpForACommandPointsAtTheCommand(t *testing.T) {
	var b bytes.Buffer
	if err := cmdHelp(context.Background(), nil, &b, []string{"cat"}); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	if !strings.Contains(got, "cat --help") {
		t.Errorf("help cat does not point at \"cat --help\":\n%s", got)
	}
	if !strings.Contains(got, "not a group") {
		t.Errorf("help cat does not say why it did nothing:\n%s", got)
	}
}

// TestHelpAllListsEverything: "help all" is what help used to be, and
// is the only listing that promises completeness.
func TestHelpAllListsEverything(t *testing.T) {
	var b bytes.Buffer
	if err := cmdHelp(context.Background(), nil, &b, []string{"all"}); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	// An alias shares its command with another name and is listed once,
	// under whichever of the names comes first -- so a command counts as
	// mentioned if its usage line is there under any name it answers to.
	listed := func(name string) bool {
		c := commands[name]
		for _, n := range commandNames() {
			if commands[n] == c && strings.Contains(got, c.usage(n)) {
				return true
			}
		}
		return false
	}
	for _, name := range commandNames() {
		if !listed(name) {
			t.Errorf("help all does not mention %q", name)
		}
	}
}

// TestHelpGroupsMentionsEveryGroup: the front page is the only route to
// the rest, so a group missing from it is a group nobody reaches.
func TestHelpGroupsMentionsEveryGroup(t *testing.T) {
	var b bytes.Buffer
	if err := cmdHelp(context.Background(), nil, &b, nil); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	for _, g := range groups {
		if !strings.Contains(got, g.name) {
			t.Errorf("help does not mention the %q group", g.name)
		}
	}
	if !strings.Contains(got, "COMMAND --help") {
		t.Error("help does not say how to get help for one command")
	}
	if !strings.Contains(got, "help all") {
		t.Error("help does not mention \"help all\"")
	}
}

// TestEveryCommandAnswersHelp is the promise the help text makes on
// every command's behalf.
//
// It has to be kept by all of them, because the front page tells people
// to use it and does not know which ones would ignore it.  Before this,
// "cat --help" answered "nothing called --help here" and "where --help"
// silently did the thing instead -- neither of which is help.
//
// echo is the exception, and deliberately: it exists to put a line into
// a file, so it has to be able to print the word "--help" like any
// other.
func TestEveryCommandAnswersHelp(t *testing.T) {
	sh := &Shell{}
	for _, name := range commandNames() {
		if name == "echo" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			var b bytes.Buffer
			err := commands[name].run(context.Background(), sh, &b, []string{"--help"})
			if err != nil {
				t.Fatalf("%s --help returned an error: %v", name, err)
			}
			got := b.String()
			if !strings.Contains(got, "Usage:") {
				t.Errorf("%s --help did not print a usage line; it printed:\n%s", name, got)
			}
		})
	}
}

// TestEchoDoesNotEatHelp: the one command that must print "--help"
// rather than explain itself.
func TestEchoDoesNotEatHelp(t *testing.T) {
	var b bytes.Buffer
	if err := commands["echo"].run(context.Background(), &Shell{}, &b, []string{"--help"}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(b.String()) != "--help" {
		t.Errorf("echo --help printed %q, want %q", strings.TrimSpace(b.String()), "--help")
	}
}

// TestHelpForOneGroupListsItsCommandsAndWhatTheyTake.
//
// The front page names the groups and nothing else, so this is the
// listing somebody actually reads -- and the usage line is where the
// flags are.
func TestHelpForOneGroupListsItsCommandsAndWhatTheyTake(t *testing.T) {
	var b bytes.Buffer
	if err := cmdHelp(context.Background(), nil, &b, []string{"shell"}); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	if !strings.Contains(got, "slsh itself") {
		t.Errorf("the group's own description is missing:\n%s", got)
	}
	for _, want := range []string{"quit", ". FILE", "echo [text ...]"} {
		if !strings.Contains(got, want) {
			t.Errorf("help shell should list %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, helpTail) {
		t.Errorf("every listing should say how to ask about one command:\n%s", got)
	}
}

// TestHelpForNeitherAGroupNorACommandSuggestsTheGroups, since a name
// that means nothing is usually a group name half remembered.
func TestHelpForNeitherAGroupNorACommandSuggestsTheGroups(t *testing.T) {
	err := cmdHelp(context.Background(), nil, io.Discard, []string{"nothing-of-the-sort"})
	if err == nil {
		t.Fatal("help for a name that means nothing should be an error")
	}
	for _, want := range append(groupNames(), "nothing-of-the-sort") {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should mention %q, got %v", want, err)
		}
	}
}

// TestAGroupThatNamesAMissingCommandPrintsWhatItHas.
//
// TestGroupsNameRealCommands keeps that from happening, but the listing
// skips what it cannot find rather than printing a blank line, because
// half a listing is more use than a heading and a gap.
func TestAGroupThatNamesAMissingCommandPrintsWhatItHas(t *testing.T) {
	var b bytes.Buffer
	err := helpGroup(&b, group{
		name: "invented", brief: "for the test",
		members: []string{"quit", "no-such-command"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := b.String()
	if !strings.Contains(got, "leave slsh") {
		t.Errorf("the command that is there should be listed:\n%s", got)
	}
	if strings.Contains(got, "no-such-command") {
		t.Errorf("the one that is not should be skipped:\n%s", got)
	}
}

// TestUngroupedFindsACommandNobodyFiled, which is the whole point of
// TestEveryCommandIsInAGroup: without this working, that test passes by
// finding nothing rather than by everything being filed.
func TestUngroupedFindsACommandNobodyFiled(t *testing.T) {
	const name = "zz-not-in-any-group"
	c := &command{brief: "invented by a test"}
	commands[name] = c
	defer delete(commands, name)

	got := ungrouped()
	if len(got) != 1 || got[0] != name {
		t.Errorf("ungrouped() = %v, want just %q", got, name)
	}

	// Two ungrouped names for one command are one thing to file, not
	// two: reporting both would have somebody adding an alias to a
	// group to quieten a test.
	commands["zz-also-not-in-any-group"] = c
	defer delete(commands, "zz-also-not-in-any-group")

	if got := ungrouped(); len(got) != 1 {
		t.Errorf("ungrouped() = %v, want the one command under one of its names", got)
	}
}
