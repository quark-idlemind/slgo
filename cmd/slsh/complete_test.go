package main

// Tab completion, at a command and while a typed answer is entered.
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

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// TestWordAtDividesTheLineAtTheWordUnderTheCursor.  A | in the line
// is the cursor, and a line without one has it at the end.
//
// The quoted rows are the ones that matter.  A space inside quotes
// belongs to the word, and the word comes back unquoted, because what
// is being completed is the NAME -- the quotes are only how a name with
// a space in it gets past the parser.
func TestWordAtDividesTheLineAtTheWordUnderTheCursor(t *testing.T) {
	for _, c := range []struct{ line, head, word, tail string }{
		{"", "", "", ""},
		{"fea", "", "fea", ""},
		{"cd Obj", "cd ", "Obj", ""},
		{"cd ", "cd ", "", ""},
		{"ls -l\tObj", "ls -l\t", "Obj", ""},

		// A quote opened and not closed: the head keeps it, so what
		// goes back replaces it rather than nesting inside it.
		{`cd "Current Ou`, "cd ", "Current Ou", ""},
		{`cd 'Current Ou`, "cd ", "Current Ou", ""},
		// And closed, which is what a previous tab will have left.
		{`cd "Current Outfit/"`, "cd ", "Current Outfit/", ""},
		// A quoted word is one word however many spaces are in it.
		{`ls -l "My Outfits/A Sunday`, "ls -l ", "My Outfits/A Sunday", ""},
		// Quoting part of a word is the parser's rule too.
		{`cd "Current Outfit"/Sen`, "cd ", "Current Outfit/Sen", ""},

		// The word under the cursor, not the last one, and all of it.
		{"fea| Objects", "", "fea", " Objects"},
		{"cp Obj|ects/a Scripts", "cp ", "Objects/a", " Scripts"},
		{"cp |Objects Scripts", "cp ", "Objects", " Scripts"},
		{"cp | Scripts", "cp ", "", " Scripts"},
		{`cp "Current O|utfit/a" b`, "cp ", "Current Outfit/a", " b"},
	} {
		before, after, _ := strings.Cut(c.line, "|")
		head, word, tail := wordAt(before, after)
		if head != c.head || word != c.word || tail != c.tail {
			t.Errorf("wordAt(%q) = %q, %q, %q; want %q, %q, %q",
				c.line, head, word, tail, c.head, c.word, c.tail)
		}
	}
}

// TestCompletionIsOfTheWordUnderTheCursor: a tab with more of the line
// after it completes where the cursor is, leaves the rest alone, and
// puts the cursor after what it filled in.
func TestCompletionIsOfTheWordUnderTheCursor(t *testing.T) {
	ctx := context.Background()
	x := newTestShell(t)
	sep := string(sl.PathSeparator)

	x.term.SetSplit("featu", " Objects")
	x.complete(ctx)
	if before, after := x.term.Split(); before != "features" || after != " Objects" {
		t.Errorf("a command before the cursor completed to %q|%q", before, after)
	}

	x.term.SetSplit("cp Ob", "j Scripts")
	x.complete(ctx)
	if before, after := x.term.Split(); before != "cp Objects"+sep || after != " Scripts" {
		t.Errorf("a path under the cursor completed to %q|%q", before, after)
	}
}

// TestQuoteWordQuotesOnlyWhatHasTo.
func TestQuoteWordQuotesOnlyWhatHasTo(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Objects/", "Objects/"},
		{"probe", "probe"},
		{"Current Outfit/", `"Current Outfit/"`},
		{"a\tb", "\"a\tb\""},
		// A redirection character would end the word without quotes.
		{"a>b", `"a>b"`},
		// One kind of quote is written inside the other.
		{`it"s`, `'it"s'`},
		{"it's", `"it's"`},
		// Both kinds cannot be written as one word at all, so it is
		// handed back as it stands rather than mangled.
		{`it's a "thing"`, `it's a "thing"`},
	} {
		if got := quoteWord(c.in); got != c.want {
			t.Errorf("quoteWord(%q) = %q, want %q", c.in, got, c.want)
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

	// Inside it, the part already settled is kept in front.
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

// TestCompletingANameWithASpaceQuotesIt.
//
// The fault this is about: "Current Outfit" is a folder every avatar
// has, and completing "Curr" used to put the whole name in unquoted,
// where the parser read it as two arguments.  Tab took a line that was
// being typed correctly and made it wrong, which is worse than tab
// doing nothing at all.
//
// So the test is not only what the line says afterwards.  It is that
// the line tab produced parses back to the one word tab meant, which
// is the property that was broken.
func TestCompletingANameWithASpaceQuotesIt(t *testing.T) {
	ctx := context.Background()
	hat := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000a8")
	folder := msg.MustParseUUID("b14c7e57-7e57-c0de-2cfb-4466e4da0d79")
	link := msg.MustParseUUID("e7cb7e57-7e57-c0de-df15-9ee8d2a3676d")

	x := newTestShell(t)
	addObjectItem(x, hat, "a hat")
	addOutfitFolder(x, folder, link, "a hat", hat)

	x.term.SetLine("cd An")
	x.complete(ctx)
	if got, want := x.term.Line(), `cd "An outfit/"`; got != want {
		t.Fatalf("completing a folder with a space gave %q, want %q", got, want)
	}
	words, _, _, err := parse(x.term.Line())
	if err != nil {
		t.Fatalf("the line tab produced will not parse: %v", err)
	}
	if len(words) != 2 || words[1] != "An outfit/" {
		t.Errorf("tab produced %q, which parses as %q", x.term.Line(), words)
	}

	// Tabbing again carries on inside it, from the quoted line the
	// previous tab left behind.
	x.complete(ctx)
	if got, want := x.term.Line(), `cd "An outfit/a hat"`; got != want {
		t.Fatalf("completing inside a quoted folder gave %q, want %q", got, want)
	}
	if words, _, _, err = parse(x.term.Line()); err != nil || len(words) != 2 {
		t.Fatalf("the second line will not parse as one argument: %q %v", words, err)
	}
	if words[1] != "An outfit/a hat" {
		t.Errorf("tab produced %q, which parses as %q", x.term.Line(), words[1])
	}

	// A quote the person opened is replaced rather than nested in.
	x.term.SetLine(`cd "An ou`)
	x.complete(ctx)
	if got, want := x.term.Line(), `cd "An outfit/"`; got != want {
		t.Errorf("completing inside a quote the person opened gave %q, want %q", got, want)
	}
}
