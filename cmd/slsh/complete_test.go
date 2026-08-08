package main

// Tab completion, in command mode only.
//
// The first word is a command and everything after it is an inventory
// path, which is the whole rule.  It is worth testing because the
// second half reaches the grid: a folder name that cannot be resolved
// has to come back as no completion rather than as an error printed
// over the line being typed.

import (
	"context"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/sl"
)

// TestSplitLastDividesTheLineAtTheLastWord.
func TestSplitLastDividesTheLineAtTheLastWord(t *testing.T) {
	for _, c := range []struct{ line, head, word string }{
		{"", "", ""},
		{"fea", "", "fea"},
		{"cd Obj", "cd ", "Obj"},
		{"cd ", "cd ", ""},
		{"ls -l\tObj", "ls -l\t", "Obj"},
	} {
		head, word := splitLast(c.line)
		if head != c.head || word != c.word {
			t.Errorf("splitLast(%q) = %q, %q; want %q, %q", c.line, head, word, c.head, c.word)
		}
	}
}

// TestCompletingACommandFinishesWhatItCan.
//
// One match is filled in; several are shown, with the line taken as far
// as they agree -- which is what a shell does and what makes tab worth
// pressing before you know the whole name.
func TestCompletingACommandFinishesWhatItCan(t *testing.T) {
	ctx := context.Background()
	x := newTestShell(t)

	x.term.SetLine("featu")
	x.complete(ctx)
	if got := x.term.Line(); got != "features" {
		t.Errorf("one match should be filled in, got %q", got)
	}

	// "of" is offer and offers: as far as they agree, and both shown.
	x.out.Reset()
	x.term.SetLine("of")
	x.complete(ctx)
	if got := x.term.Line(); got != "offer" {
		t.Errorf("several matches should go as far as they agree, got %q", got)
	}
	if got := x.out.String(); !strings.Contains(got, "offer") || !strings.Contains(got, "offers") {
		t.Errorf("the choices should be shown: %q", got)
	}

	// Nothing that starts like that: the line is left alone rather
	// than emptied.
	x.term.SetLine("zzz")
	x.complete(ctx)
	if got := x.term.Line(); got != "zzz" {
		t.Errorf("a word that matches nothing should be left alone, got %q", got)
	}

	// Tab on an empty line lists everything and adds nothing, since
	// the commands have no prefix in common.
	x.out.Reset()
	x.term.SetLine("")
	x.complete(ctx)
	if got := x.term.Line(); got != "" {
		t.Errorf("an empty line has nothing in common to fill in, got %q", got)
	}
	if got := x.out.String(); !strings.Contains(got, "help") {
		t.Errorf("tab on an empty line should list the commands: %q", got)
	}
}

// TestCompletingAPathCarriesOnIntoAFolder.
//
// A folder completes with a separator on the end, so that the next tab
// goes on inside it rather than stopping at its name.
func TestCompletingAPathCarriesOnIntoAFolder(t *testing.T) {
	ctx := context.Background()
	x := newTestShell(t)
	sep := string(sl.PathSeparator)

	x.term.SetLine("cd Obj")
	x.complete(ctx)
	if got, want := x.term.Line(), "cd Objects"+sep; got != want {
		t.Errorf("completing a folder gave %q, want %q", got, want)
	}

	// Inside it, the part already settled is kept in front.  A name
	// with a space in it is beyond this: the word being completed ends
	// at the last space, so "Objects/a la" is completed as "la".
	x.term.SetLine("ls Scripts" + sep + "pro")
	x.complete(ctx)
	if got, want := x.term.Line(), "ls Scripts"+sep+"probe"; got != want {
		t.Errorf("completing inside a folder gave %q, want %q", got, want)
	}

	// The match ignores case, as the rest of the shell does.
	x.term.SetLine("cd obj")
	x.complete(ctx)
	if got, want := x.term.Line(), "cd Objects"+sep; got != want {
		t.Errorf("completion should ignore case, gave %q", got)
	}
}

// TestCompletingAPathThatIsNotThereIsNotAnError.
//
// A completion runs while somebody is typing, so anything it has to say
// lands on top of the line being typed.  A folder that cannot be
// resolved, or an inventory that cannot be read, therefore comes back
// as no completion at all.
func TestCompletingAPathThatIsNotThereIsNotAnError(t *testing.T) {
	ctx := context.Background()
	x := newTestShell(t)
	sep := string(sl.PathSeparator)

	x.term.SetLine("cd Nowhere" + sep + "deep")
	x.complete(ctx)
	if got, want := x.term.Line(), "cd Nowhere"+sep+"deep"; got != want {
		t.Errorf("the line should be untouched, got %q", got)
	}
	if got := x.out.String(); got != "" {
		t.Errorf("completion should not print over what is being typed: %q", got)
	}

	// The same when inventory cannot be read at all.
	x.grid.mu.Lock()
	x.grid.caps = map[string]string{}
	x.grid.mu.Unlock()

	x.term.SetLine("cd Obj")
	x.complete(ctx)
	if got := x.term.Line(); got != "cd Obj" {
		t.Errorf("an unreadable inventory should complete to nothing, got %q", got)
	}
	if got := x.out.String(); got != "" {
		t.Errorf("completion should stay quiet about it: %q", got)
	}
}

// TestCommonPrefixOfNothingIsNothing, which is the guard that stops the
// caller indexing an empty list.
func TestCommonPrefixOfNothingIsNothing(t *testing.T) {
	if got := commonPrefix(nil); got != "" {
		t.Errorf("commonPrefix(nil) = %q", got)
	}
	if got := commonPrefix([]string{"only"}); got != "only" {
		t.Errorf("commonPrefix of one = %q", got)
	}
	if got := commonPrefix([]string{"offer", "offers"}); got != "offer" {
		t.Errorf("commonPrefix = %q", got)
	}
	if got := commonPrefix([]string{"give", "help"}); got != "" {
		t.Errorf("words with nothing in common should share nothing, got %q", got)
	}
}
