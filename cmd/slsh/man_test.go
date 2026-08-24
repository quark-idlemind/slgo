package main

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"testing"
)

// TestAMarkdownPageIsRenderedNotShouted.
//
// cd has both forms, and markdown wins.  The point of preferring it is
// that a heading can stay in the case it was written -- the .txt layout
// has to shout, because it has no other mark.  If man cd still came out
// in capitals, we would be reading the .txt or laying the .md out as
// one, and neither is the thing we just added markdown for.
func TestAMarkdownPageIsRenderedNotShouted(t *testing.T) {
	var b bytes.Buffer
	if err := cmdMan(context.Background(), &Shell{}, &b, []string{"cd"}); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	if strings.Contains(got, "ONLY A FOLDER") {
		t.Errorf("man cd shouted a markdown heading:\n%s", got)
	}
	if !strings.Contains(got, "Only a folder, and only by name") {
		t.Errorf("man cd is missing the markdown heading:\n%s", got)
	}
	if !strings.Contains(got, "\x1b[1m") {
		t.Errorf("man cd should be rendered, not printed raw:\n%s", got)
	}
	if !strings.Contains(got, "cd Objects/lanterns") {
		t.Errorf("man cd lost its examples:\n%s", got)
	}
	if !strings.Contains(got, "cd -- ") {
		t.Errorf("man cd should still say the name and brief:\n%s", got)
	}
}

// TestATextPageIsStillLaidOutAsText.
//
// cmdMan prefers markdown now, so this talks to manText directly: that
// is still the layout a .txt page gets, and it must not pick up SGR
// from the markdown path.  A heading that stayed in ordinary case
// would be the first sign that it had.
func TestATextPageIsStillLaidOutAsText(t *testing.T) {
	var b bytes.Buffer
	manText(&b, "# One object, and only one\nA paragraph.\n", 78)
	got := b.String()
	if strings.Contains(got, "\x1b[") {
		t.Errorf("txt layout should not carry SGR:\n%q", got)
	}
	if !strings.Contains(got, "ONE OBJECT, AND ONLY ONE") {
		t.Errorf("txt heading should be shouted:\n%q", got)
	}
}

// TestManPrintsThePageAndSaysHowTheCommandIsTyped.
//
// A page is prose and nothing else, deliberately: it does not repeat
// the name, the brief or the flags, because those are derived and a
// fourth hand-kept copy of them is what this series has just finished
// removing.  So man has to supply the heading itself, and this is what
// keeps it doing so -- a page printed on its own would begin in the
// middle of a sentence about a command nobody had named.
func TestManPrintsThePageAndSaysHowTheCommandIsTyped(t *testing.T) {
	var b bytes.Buffer
	if err := cmdMan(context.Background(), &Shell{}, &b, []string{"place"}); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	for _, want := range []string{
		"place -- " + commands["place"].brief,
		"usage: " + commands["place"].usage("place"),
		"take undoes",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("man place should contain %q:\n%s", want, got)
		}
	}
}

// TestManWithNoNameListsThePagesThatExist.
//
// Most commands have no page and will not for a while, so a listing of
// all sixty would send most people to a page that is not there.  What
// man knows and help does not is which ones can be asked for.
func TestManWithNoNameListsThePagesThatExist(t *testing.T) {
	var b bytes.Buffer
	if err := cmdMan(context.Background(), &Shell{}, &b, nil); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	// The names are the indented block; the sentence above it is prose
	// and mentions commands of its own.
	listed := map[string]bool{}
	for _, line := range strings.Split(got, "\n") {
		if !strings.HasPrefix(line, "    ") {
			continue
		}
		for _, n := range strings.Fields(line) {
			listed[n] = true
		}
	}
	for _, name := range commandNames() {
		switch {
		case commands[name].man != "" && !listed[name]:
			t.Errorf("%q has a page and is not listed:\n%s", name, got)
		case commands[name].man == "" && listed[name]:
			t.Errorf("%q has no page and is listed as though it had:\n%s", name, got)
		}
	}
}

// TestManForACommandWithNoPageSaysWhichItIs.
//
// "no such thing" would read as man being broken rather than as the
// page being unwritten, which for the next while is the commoner case
// by six to one.  So it says so, and gives what it does have.
func TestManForACommandWithNoPageSaysWhichItIs(t *testing.T) {
	// A command of the test's own, because every real one has a page
	// now.  Borrowing whichever was still unwritten made this test skip
	// itself the moment somebody wrote that page, which is exactly when
	// a test should be running rather than standing aside.
	const name = "unwritten"
	commands[name] = &command{brief: "a command whose page nobody has written",
		flags: func() any { return &helpOnly{} }}
	defer delete(commands, name)

	var b bytes.Buffer
	if err := cmdMan(context.Background(), &Shell{}, &b, []string{name}); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	if !strings.Contains(got, "no man page for "+name+" yet") {
		t.Errorf("man %s should say the page is unwritten:\n%s", name, got)
	}
	if !strings.Contains(got, name+" --help") {
		t.Errorf("man %s should point at the flags it does have:\n%s", name, got)
	}
	if !strings.Contains(got, commands[name].brief) {
		t.Errorf("man %s should still say what it is for:\n%s", name, got)
	}
}

// TestManForSomethingThatIsNotACommandIsAnError, since that one is a
// typo rather than a gap, and pointing at the listing is the answer.
func TestManForSomethingThatIsNotACommandIsAnError(t *testing.T) {
	var b bytes.Buffer
	err := cmdMan(context.Background(), &Shell{}, &b, []string{"nothing-of-the-sort"})
	if err == nil {
		t.Fatal("man for a name that is not a command should be an error")
	}
	for _, want := range []string{"nothing-of-the-sort", "help all"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should mention %q, got %v", want, err)
		}
	}
}

// TestAPageWrapsItsProseAndLeavesItsExamplesAlone.
//
// The two have to be told apart or the page is useless either way: a
// paragraph that is not wrapped runs off the screen, and an example
// that IS wrapped becomes a command line that cannot be typed.
func TestAPageWrapsItsProseAndLeavesItsExamplesAlone(t *testing.T) {
	const page = `# A heading that is left exactly as it was written
one two three four five six seven eight nine ten eleven twelve
thirteen fourteen fifteen sixteen seventeen eighteen

	place --at 128,128,25 "Objects/a name with spaces in it and more"`

	var b bytes.Buffer
	manText(&b, page, 40)
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")

	for _, l := range lines {
		if strings.HasPrefix(l, "    ") || strings.HasPrefix(l, "A HEADING") {
			continue
		}
		if len(l) > 40 {
			t.Errorf("prose wrapped to more than 40 columns: %q", l)
		}
	}
	if lines[0] != "A HEADING THAT IS LEFT EXACTLY AS IT WAS WRITTEN" {
		t.Errorf("the heading was wrapped or left in ordinary case: %q", lines[0])
	}
	want := `    place --at 128,128,25 "Objects/a name with spaces in it and more"`
	if last := lines[len(lines)-1]; last != want {
		t.Errorf("the example came out as %q, want %q", last, want)
	}
	if strings.Contains(b.String(), "# ") {
		t.Errorf("the heading marker should not reach the screen:\n%s", b.String())
	}
}

// TestAHeadingDoesNotComeOutLookingLikeAParagraph.
//
// This is what the "# " is for.  Printed as it was written, a heading
// is a short sentence with a blank line over it, which is also what the
// first line of a paragraph is -- so a page rendered that way is one
// undifferentiated column and cannot be skimmed for the trap it exists
// to warn about.  Capitals are the only mark available: term.go has no
// styling in it, and a page is redirected to a file as often as it is
// read on a screen.
func TestAHeadingDoesNotComeOutLookingLikeAParagraph(t *testing.T) {
	const words = "One object, and only one"

	var heading, paragraph bytes.Buffer
	manText(&heading, "# "+words, 78)
	manText(&paragraph, words, 78)

	if heading.String() == paragraph.String() {
		t.Errorf("a heading and a paragraph of the same words render identically: %q", heading.String())
	}
	if got := strings.TrimSpace(heading.String()); got != strings.ToUpper(words) {
		t.Errorf("the heading rendered as %q, want %q", got, strings.ToUpper(words))
	}
}

// TestNoManPageShoutsAFlagName.
//
// Headings are uppercased, so a flag or a path in one becomes something
// nobody can type -- "--REPLACE".  The rule is to keep them in the
// paragraph underneath, and this is what keeps the rule.
func TestNoManPageShoutsAFlagName(t *testing.T) {
	// Only .txt pages: those headings are uppercased.  Markdown keeps
	// the case it was written in, so a flag in one is still the flag.
	files, err := manFileNames()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if !strings.HasSuffix(f, ".txt") {
			continue
		}
		body, err := manPages.ReadFile(manDir + "/" + f)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			if !strings.HasPrefix(line, "# ") {
				continue
			}
			for _, w := range strings.Fields(line[2:]) {
				if strings.HasPrefix(w, "-") {
					t.Errorf("cmd/slsh/%s/%s: the heading %q names %q, which uppercasing would make untypeable",
						manDir, f, line[2:], w)
				}
			}
		}
	}
}

// TestWrappingKeepsTheSpaceAfterAFullStop, which is two everywhere else
// in this shell and should not become one on its way to the screen.
func TestWrappingKeepsTheSpaceAfterAFullStop(t *testing.T) {
	var b bytes.Buffer
	manText(&b, "One sentence.  Another one, on the same line.\nAnd a third, on the next.", 200)
	got := strings.TrimSpace(b.String())
	if !strings.Contains(got, "sentence.  Another") {
		t.Errorf("the two spaces inside a line were squashed: %q", got)
	}
	if !strings.Contains(got, "line.  And") {
		t.Errorf("the join between two lines should be two spaces after a full stop: %q", got)
	}
}

// TestALongWordIsNotBroken: a uuid or a path that has been cut in half
// cannot be copied off the screen, which is the only reason it was
// printed.
func TestALongWordIsNotBroken(t *testing.T) {
	const id = "91a97e57-7e57-c0de-76b6-411da672b021"
	var b bytes.Buffer
	manText(&b, "an id is "+id+" and that is that", 20)
	if !strings.Contains(b.String(), id) {
		t.Errorf("the id was broken across lines:\n%s", b.String())
	}
}

// TestAPageIsWrappedToThisTerminal, which is the reason a shell can do
// this better than a file of text can.
func TestAPageIsWrappedToThisTerminal(t *testing.T) {
	if got := manWidth(nil); got != 78 {
		t.Errorf("with no terminal the width is %d, want the 80 a pipe reports less a margin", got)
	}
	sh := &Shell{term: &Term{width: 50, height: 24}}
	if got := manWidth(sh); got != 48 {
		t.Errorf("a 50-column terminal gives %d, want 48", got)
	}
	// A very wide window is not an invitation to a very wide paragraph.
	sh.term.width = 300
	if got := manWidth(sh); got != 78 {
		t.Errorf("a 300-column terminal gives %d, want the 78 cap", got)
	}
}

// TestHelpPointsAtManOnlyWhereThereIsAPage.
//
// The foot of --help is the moment somebody is looking for more, so it
// is worth saying where more is -- but only when there is some, since a
// pointer to an unwritten page is worse than no pointer at all.
func TestHelpPointsAtManOnlyWhereThereIsAPage(t *testing.T) {
	var with bytes.Buffer
	var o placeFlags
	if _, _, err := subOptions("place", &o, &with, []string{"--help"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(with.String(), `"man place"`) {
		t.Errorf("place --help should point at its page:\n%s", with.String())
	}

	// And a command with no page must not send anybody looking for one.
	// It is the test's own command for the reason given above.
	const name = "unwritten"
	commands[name] = &command{brief: "a command whose page nobody has written",
		flags: func() any { return &helpOnly{} }}
	defer delete(commands, name)

	var without bytes.Buffer
	var h helpOnly
	if _, _, err := subOptions(name, &h, &without, []string{"--help"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(without.String(), "man "+name) {
		t.Errorf("%s has no page and --help should not send anybody to one:\n%s", name, without.String())
	}
}

// TestEveryManFieldNamesAPageThatIsThere.
//
// The field is a filename now, so a typo in it or a page that never got
// written is a command that answers "man place" with an error instead of
// a page.  That is not a thing to find out about at the prompt: it is
// fixed here, at build time, where the person who moved the file is
// still holding it.
func TestEveryManFieldNamesAPageThatIsThere(t *testing.T) {
	for _, name := range commandNames() {
		page := commands[name].man
		if page == "" {
			continue
		}
		if _, _, err := manOpen(page); err != nil {
			t.Errorf("%s names the page %q, and neither cmd/slsh/%s.md nor cmd/slsh/%s.txt is there",
				name, page, manDir+"/"+page, manDir+"/"+page)
		}
	}
}

// TestEveryPageInTheDirectoryIsNamedBySomeCommand is the other
// direction: a page nothing points at is prose nobody will ever be
// shown, and it goes wrong silently -- most likely a command renamed
// without its page, which is exactly the case this series created when
// host became login.  Both .md and .txt of one stem count as named:
// markdown wins at print time, and the text is the fallback, not a
// page of its own.
func TestEveryPageInTheDirectoryIsNamedBySomeCommand(t *testing.T) {
	files, err := manFileNames()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("cmd/slsh/%s embedded nothing at all", manDir)
	}
	named := map[string]bool{}
	for _, name := range commandNames() {
		if page := commands[name].man; page != "" {
			named[page] = true
		}
	}
	for _, f := range files {
		stem, ok := manStem(f)
		if !ok {
			t.Errorf("cmd/slsh/%s/%s is not a .md or .txt page; man reads nothing else", manDir, f)
			continue
		}
		if !named[stem] {
			t.Errorf("cmd/slsh/%s/%s is a page no command names, so nobody can reach it", manDir, f)
		}
	}
}

// manStem is the command a page file is named for.
func manStem(filename string) (string, bool) {
	switch {
	case strings.HasSuffix(filename, ".md"):
		return strings.TrimSuffix(filename, ".md"), true
	case strings.HasSuffix(filename, ".txt"):
		return strings.TrimSuffix(filename, ".txt"), true
	}
	return "", false
}

// TestNoManPageQuotesAWholeKey.
//
// A page is written for whoever reads it and not about whoever wrote
// it, so it carries invented names -- Example Resident, Testville, a
// lantern -- and never the avatars, groups or objects of the account it
// was written on.  That rule cannot be tested in general, since a test
// cannot know a real name from an invented one, but one form of it can:
// a whole key, all thirty-six characters of it, is almost never typed
// by hand.  It arrives by being pasted out of live output, and whatever
// else came with it was real too.
//
// So the truncated form the shell itself prints -- d8467e57-... -- is
// what a page uses when it has to show a key at all.
func TestNoManPageQuotesAWholeKey(t *testing.T) {
	whole := regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	files, err := manPages.ReadDir(manDir)
	if err != nil {
		t.Fatalf("reading %s: %v", manDir, err)
	}
	for _, f := range files {
		body, err := manPages.ReadFile(manDir + "/" + f.Name())
		if err != nil {
			t.Errorf("%s: %v", f.Name(), err)
			continue
		}
		if found := whole.FindString(string(body)); found != "" {
			t.Errorf("cmd/slsh/%s/%s quotes the whole key %s; a page shows a key as %q, "+
				"and a whole one usually means live output was pasted in",
				manDir, f.Name(), found, "d8467e57-...")
		}
	}
}

// TestManContentsSaysWhetherAnythingIsMissing: the listing's opening
// sentence used to say "the rest answer COMMAND --help" when there was
// no rest, which reads as though a page were missing.
//
// It is worked out rather than fixed, so this checks both halves: as the
// tree stands every command has a page, and the sentence says so; take a
// page away and it goes back to naming a remainder.
func TestManContentsSaysWhetherAnythingIsMissing(t *testing.T) {
	// The blurb is wrapped to the width, so a phrase can arrive with a
	// newline in the middle of it; compare against the words alone.
	flat := func(s string) string { return strings.Join(strings.Fields(s), " ") }

	var full strings.Builder
	if err := manContents(&full, 78); err != nil {
		t.Fatalf("manContents: %v", err)
	}
	if !strings.Contains(flat(full.String()), "Every command has a page") {
		t.Errorf("every command has a page, and the listing did not say so:\n%s", full.String())
	}

	// One command without a page, put back afterwards.
	name := commandNames()[0]
	c := commands[name]
	was := c.man
	c.man = ""
	t.Cleanup(func() { c.man = was })

	var short strings.Builder
	if err := manContents(&short, 78); err != nil {
		t.Fatalf("manContents: %v", err)
	}
	if !strings.Contains(flat(short.String()), "the rest answer") {
		t.Errorf("with %q lacking a page, the listing did not name a remainder:\n%s",
			name, short.String())
	}
	if strings.Contains(flat(short.String()), " "+name+" ") {
		t.Errorf("%q has no page and was listed as though it had one", name)
	}
}
