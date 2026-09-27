package main

// The long help: what a command is for, rather than what it takes.
//
// --help answers "what are the flags", which is the question somebody
// has while they are typing.  It is the wrong shape for the other
// question -- why does wear add rather than replace, why does place
// take exactly one argument, what does a group invitation with a fee
// need typed at it -- because the answers are paragraphs and getopt's
// output is a column of flags.  Those answers existed, in the file
// comments, argued out and measured live, and before this, meeting
// them meant reading the source.
//
// So a command names a man page as well as carrying a brief, and
// "man NAME" prints it.
//
// # What is written here and what is not
//
// A page never repeats the two things that are already derived.  man
// prints the name and brief as its heading and the usage line under it,
// both composed the same way help composes them (options.go).
//
// The flags are not one of those two.  A page with flags lists them
// under Options, a paragraph each, saying the part getopt's column has
// no room for; the column stays the quick answer and is still the only
// answer for a command whose one flag is --help.  What a page adds
// beyond that is the reasoning, the traps and the worked examples.
// Why: doc/slsh.md#what-a-man-page-repeats-and-what-it-does-not
//
// # Where a page lives
//
// In cmd/slsh/man, one markdown file per command, NAME.md, embedded
// into the binary: a page is prose, written and read as prose, and a
// shell that is installed cannot have lost its documentation on the
// way.  The whole directory is embedded rather than a pattern like
// "man/*.md", because a pattern is a thing a file can silently fall
// outside of and a directory is not.
// Why: doc/slsh.md#why-a-man-page-is-a-markdown-file
//
// The command's man field names the page rather than holding it.  Naming
// is what lets two names share one command -- quit and exit are one
// entry, and "." is not a filename anybody wants -- so the page for "."
// is source.md and the field says so.  An empty field is a command with
// no page yet.
//
// # How a page is laid out
//
// As markdown, rendered by package md.  Paragraphs are wrapped to the
// terminal, a "## " line is a heading and comes out in the case it was
// written, an indented block is an example and is printed exactly as it
// stands, and a table is drawn.  A heading longer than the width wraps
// like anything else.
//
// The house rule of two spaces after a full stop is held across the
// join between two lines of one paragraph as well as inside a line;
// CommonMark makes a soft line break one space, so md/parse.go puts the
// second one back rather than have a sentence boundary come out
// differently depending on where the author pressed return.
//
// Options come near the top: under the opening description, above the
// sections that reason about the command, and always above Examples,
// which stay last.  A flag is the thing somebody most often opens a
// page to look up, and pages had drifted to putting them wherever they
// happened to be written -- tp listed its one flag at line 169 of 191,
// behind eight sections of reasoning.
//
// Three pages keep a section in front of Options, because the flags
// cannot be read without it.  perms defines its four letters first and
// every flag takes them; answer explains what a text box is before
// --file offers to answer one from a file; and sit keeps the section
// that says a sit moves the avatar, which opens by calling itself the
// part to know before anything else and is right.
//
// Wrapping is to the terminal's own width less a margin, and no wider
// than 78 whatever the terminal says: a paragraph 200 columns wide is
// technically fitted to the screen and unreadable, because the eye
// loses the line on its way back to the left.
//
// # Why a missing page is not an error
//
// Every command has a page today, and this is kept for the day one
// does not: a command added tomorrow arrives before its page does, and
// the arrangement should not be that it cannot be committed until the
// prose is written.  "man where" answering "no such thing" would read
// as man being broken rather than as the page being unwritten, so it
// says which it is and prints what it does have -- the usage line and
// the brief -- instead of nothing.
// A name that is no command at all is the other case, and that is an
// error, because it is a typo.

import (
	"context"
	"embed"
	"fmt"
	"io"
	"strings"

	"github.com/quark-idlemind/slgo/md"
)

// manPages is cmd/slsh/man, as it was on the day this was built.
//
// The directory and not a pattern: "man/*.md" would embed whatever
// matched and say nothing about whatever did not, and a page missing
// from the binary is exactly the failure this is meant not to have.
// manFileNames and the tests beside it turn the remaining ways to get
// it wrong -- a field naming no file, a file no field names -- into
// test failures rather than into something a person finds by typing
// "man place".
//
//go:embed man
var manPages embed.FS

// manDir is where the pages are, in the embedded copy and on disk.
const manDir = "man"

// manOpen is one page's path and text.
//
// An error here means the field names a file that is not there, which
// TestEveryManFieldNamesAPageThatIsThere makes impossible to ship; the
// message says which page all the same, since a wrong answer about
// where the text went would send somebody looking in the wrong place.
func manOpen(page string) (file, text string, err error) {
	file = manDir + "/" + page + ".md"
	b, err := manPages.ReadFile(file)
	if err != nil {
		return "", "", fmt.Errorf("the page %s is not in this build; it should be cmd/slsh/%s", page, file)
	}
	return file, string(b), nil
}

// manRead is one page's text.
func manRead(page string) (string, error) {
	_, text, err := manOpen(page)
	return text, err
}

// manFileNames is every page in the directory, for the test that asks
// whether anything there is unreachable.
func manFileNames() ([]string, error) {
	es, err := manPages.ReadDir(manDir)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(es))
	for _, e := range es {
		if e.IsDir() {
			continue
		}
		out = append(out, e.Name())
	}
	return out, nil
}

var manCommands = map[string]*command{
	"man": {
		params:   "[NAME]",
		flags:    func() any { return new(helpOnly) },
		brief:    "the long description of one command; no name lists the ones that have one",
		keywords: "manual documentation page long description explain command details",
		man:      "man",
		run:      cmdMan,
	},
}

// cmdMan prints one command's long description.
func cmdMan(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	args, done, err := subOptions("man", &o, out, args)
	if err != nil || done {
		return err
	}
	width := manWidth(sh)

	if len(args) == 0 {
		var body strings.Builder
		if err := manContents(&body, width); err != nil {
			return err
		}
		return manPrint(ctx, sh, out, body.String())
	}
	// One name.  A page is prose about one command, so there is nothing
	// sensible to do with two, and a name with a space in it is not a
	// command.
	if len(args) != 1 {
		return usageError("man", "one command at a time")
	}
	name := args[0]
	c, ok := commands[name]
	if !ok {
		return fmt.Errorf("no command called %q; \"help all\" lists them", name)
	}

	var body strings.Builder
	fmt.Fprintf(&body, "%s -- %s\n", name, c.brief)
	fmt.Fprintf(&body, "usage: %s\n", c.usage(name))
	if c.man == "" {
		fmt.Fprintf(&body, "\nThere is no man page for %s yet; %q lists what it takes.\n", name, name+" --help")
		return manPrint(ctx, sh, out, body.String())
	}
	// The heading and the usage line are already out, so a page that
	// cannot be read leaves a person with the two lines and a reason
	// rather than with nothing at all.
	_, text, err := manOpen(c.man)
	if err != nil {
		return err
	}
	fmt.Fprintln(&body)
	body.WriteString(md.RenderWidth(text, width))
	return manPrint(ctx, sh, out, body.String())
}

// manPrint writes a finished page.  On a real terminal it is shown a
// screenful at a time; on a pipe or a redirect the whole thing goes
// out, because there is nobody there to press space.  A file does not
// get the SGR sequences a terminal uses for bold: they are rubbish in
// a listing, and Unicode already carries the bullets and rules.
func manPrint(ctx context.Context, sh *Shell, out io.Writer, text string) error {
	if manPaged(sh, out) {
		return page(ctx, sh.term, text)
	}
	text = stripANSI(text)
	_, err := io.WriteString(out, text)
	return err
}

func manPaged(sh *Shell, out io.Writer) bool {
	if sh == nil || sh.term == nil || sh.term.Plain() {
		return false
	}
	_, ok := out.(*termWriter)
	return ok
}

// manContents is what man says when it is asked nothing: which commands
// can be asked about.
//
// Every command has a page, so what is listed is currently all of
// them, and the sentence above the listing is written for a remainder
// that does not exist at the moment.  The filter stays anyway: a
// command added tomorrow arrives before its page does, and a name
// offered here that answers with nothing is worse than a name left
// out until it has something to say.  help lists everything either
// way, and says so here.
func manContents(out io.Writer, width int) error {
	// commandNames is already sorted, so the listing comes out
	// alphabetical without asking.
	var have []string
	for _, n := range commandNames() {
		if commands[n].man != "" {
			have = append(have, n)
		}
	}
	if len(have) == 0 {
		fmt.Fprintln(out, "no command has a man page yet")
		return nil
	}
	// "The rest" is empty as it stands, every command having a page, and
	// a sentence pointing at an empty set reads as though something were
	// missing.  Which of the two it says is worked out rather than
	// fixed, because a command added tomorrow arrives before its page
	// does and the sentence should be right on that day too.
	blurb := "\"man NAME\" describes one command at length.  Every command has " +
		"a page; \"COMMAND --help\" is the shorter version, and \"help\" lists " +
		"them by group."
	if len(have) < len(commandNames()) {
		blurb = "\"man NAME\" describes one command at length.  These have " +
			"a page; the rest answer \"COMMAND --help\", and \"help\" lists them all."
	}
	fmt.Fprintln(out, wrapText(blurb, width))
	fmt.Fprintln(out)
	for _, line := range strings.Split(wrapText(strings.Join(have, " "), width-4), "\n") {
		fmt.Fprintln(out, "    "+line)
	}
	return nil
}

// manWidth is how wide a page is laid out.
//
// The terminal's, less a margin so that a wrapped line never sits hard
// against the right edge, and never more than 78 however wide the
// window is: prose is read by the eye going back to the left margin and
// finding the next line, and past about eighty columns it stops finding
// it.  A shell with no terminal -- a test, a script -- gets the same 80
// a pipe reports.
func manWidth(sh *Shell) int {
	w := 80
	if sh != nil && sh.term != nil {
		w = sh.term.Cols()
	}
	w -= 2
	if w > 78 {
		w = 78
	}
	if w < 40 {
		w = 40
	}
	return w
}

// wrapText breaks one paragraph at spaces.
//
// The run of spaces between two words is kept rather than squashed to
// one, so that the two spaces after a full stop survive the wrapping;
// only the run a line is broken at is thrown away, which is what a line
// break is.
//
// A word longer than the width is left whole rather than cut: it is a
// path, a name or a uuid, and a uuid broken across two lines cannot be
// copied.
func wrapText(text string, width int) string {
	var b strings.Builder
	line, gap := 0, ""
	for i := 0; i < len(text); {
		// One word, and the run of spaces after it -- which is the gap
		// in front of the word that comes next, and is written out with
		// that word or thrown away with the line break that replaces it.
		j := i
		for j < len(text) && text[j] != ' ' {
			j++
		}
		word := text[i:j]
		k := j
		for k < len(text) && text[k] == ' ' {
			k++
		}
		next := text[j:k]
		i = k

		if word == "" {
			gap = next
			continue
		}
		n := len([]rune(word))
		switch {
		case line == 0:
			b.WriteString(word)
			line = n
		case line+len(gap)+n > width:
			b.WriteString("\n")
			b.WriteString(word)
			line = n
		default:
			b.WriteString(gap)
			b.WriteString(word)
			line += len(gap) + n
		}
		gap = next
	}
	return b.String()
}
