package main

// The long help: what a command is for, rather than what it takes.
//
// --help answers "what are the flags", which is the question somebody
// has while they are typing.  It is the wrong shape for the other
// question -- why does wear add rather than replace, why does place
// take exactly one argument, what does a group invitation with a fee
// need typed at it -- because the answers are paragraphs and getopt's
// output is a column of flags.  Those answers exist: they are in the
// file comments, argued out and measured live.  Before this, meeting
// them meant reading the source.
//
// So a command names a man page as well as carrying a brief, and
// "man NAME" prints it.
//
// # What is written here and what is not
//
// A page never repeats the two things that are already derived.  man
// prints the name and brief as its heading and the usage line under it,
// both composed the same way help composes them (options.go), so a page
// that listed its own flags would be a fourth copy of the thing this
// series has just finished reducing to one.  What a page adds is the
// reasoning, the traps and the worked examples.
//
// # Where a page lives
//
// In cmd/slsh/man, one text file per command, named after it, embedded
// into the binary.  A page is prose and is written and read as prose;
// as a Go string constant it was prose being edited inside a quoting
// construct, where a stray backtick is a compile error and no editor
// wraps or spell-checks a paragraph.
//
// The whole directory is embedded rather than a pattern like "man/*.txt",
// because a pattern is a thing a file can silently fall outside of and a
// directory is not.  The page still ends up inside the binary either way,
// which is the property worth keeping: a shell that is installed cannot
// have lost its documentation on the way.
//
// A generator writing the pages back out as Go source was the other
// answer -- msg/messages_gen.go is done that way -- and buys nothing
// here.  That generator exists because the message template is somebody
// else's file in somebody else's format; these are ours, in no format at
// all, and a generated file is one more thing to keep in step.
//
// The command's man field names the page rather than holding it.  Naming
// is what lets two names share one command -- quit and exit are one
// entry, and "." is not a filename anybody wants -- so the page for "."
// is source.txt and the field says so.  An empty field is a command with
// no page yet, which is most of them.
//
// # How a page is laid out
//
// As a Go doc comment is, because that is what everybody writing one
// here has just been reading:
//
//   - a blank line separates paragraphs, and a paragraph is wrapped to
//     the terminal;
//   - a line starting with "# " is a heading, and is not wrapped;
//   - a line starting with a tab is an example, and is printed as it
//     was written.
//
// A heading is printed in capitals, which is man(7)'s own convention and
// the only one available: nothing here has styling to reach for -- term.go
// emits "\r\x1b[K" and nothing else -- and a page goes to a file or a pipe
// as often as to a screen, so bold would arrive as rubbish where it did
// not simply vanish.  A blank line above a sentence in ordinary case is
// not a heading; it is a paragraph, and a page written that way is one
// column nobody can skim for the trap.
//
// The consequence for whoever writes a page: keep flag names, paths and
// anything else case-sensitive out of a heading, because the heading is
// going to be shouted and "--REPLACE" is not a thing anybody can type.
// Say it in the paragraph underneath, where the case survives.
//
// Wrapping is to the terminal's own width less a margin, and no wider
// than 78 whatever the terminal says: a paragraph 200 columns wide is
// technically fitted to the screen and unreadable, because the eye
// loses the line on its way back to the left.
//
// # Why a missing page is not an error
//
// Most commands have no page yet and will not for a while.  "man where"
// answering "no such thing" would read as man being broken rather than
// as the page being unwritten, so it says which it is and prints what
// it does have -- the usage line and the brief -- instead of nothing.
// A name that is no command at all is the other case, and that is an
// error, because it is a typo.

import (
	"context"
	"embed"
	"fmt"
	"io"
	"strings"
)

// manPages is cmd/slsh/man, as it was on the day this was built.
//
// The directory and not a pattern: "man/*.txt" would embed whatever
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

// manFile is the page a command's man field names.
func manFile(page string) string { return manDir + "/" + page + ".txt" }

// manRead is one page's text.
//
// An error here means the field names a file that is not there, which
// TestEveryManFieldNamesAPageThatIsThere makes impossible to ship; the
// message says which page all the same, since a wrong answer about
// where the text went would send somebody looking in the wrong place.
func manRead(page string) (string, error) {
	b, err := manPages.ReadFile(manFile(page))
	if err != nil {
		return "", fmt.Errorf("the page %s is not in this build; it should be cmd/slsh/%s", page, manFile(page))
	}
	return string(b), nil
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
		params: "[NAME]",
		flags:  func() any { return new(helpOnly) },
		brief:  "the long description of one command; no name lists the ones that have one",
		man:    "man",
		run:    cmdMan,
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
		return manContents(out, width)
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

	fmt.Fprintf(out, "%s -- %s\n", name, c.brief)
	fmt.Fprintf(out, "usage: %s\n", c.usage(name))
	if c.man == "" {
		fmt.Fprintf(out, "\nThere is no man page for %s yet; %q lists what it takes.\n", name, name+" --help")
		return nil
	}
	// The heading and the usage line are already out, so a page that
	// cannot be read leaves a person with the two lines and a reason
	// rather than with nothing at all.
	text, err := manRead(c.man)
	if err != nil {
		return err
	}
	fmt.Fprintln(out)
	manText(out, text, width)
	return nil
}

// manContents is what man says when it is asked nothing: which commands
// can be asked about.
//
// Not every command, because a listing of sixty names of which a
// handful answer would send most people to a page that is not there.
// help is the listing of everything, and says so here.
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
	manText(out, "\"man NAME\" describes one command at length.  These have "+
		"a page; the rest answer \"COMMAND --help\", and \"help\" lists them all.", width)
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

// manText lays a page out: paragraphs wrapped, headings and examples
// left as they were written.  See the head of this file for the three
// kinds of line and why they are the ones a Go doc comment has.
func manText(out io.Writer, text string, width int) {
	var para string
	add := func(line string) {
		switch {
		case para == "":
			para = line
		case endsASentence(para):
			// Two spaces after a full stop, as everywhere else here.
			// Whatever an author wrote inside a line is kept as it is by
			// the wrapping; this is the join between two of them, where
			// the newline was standing in for the space.
			para += "  " + line
		default:
			para += " " + line
		}
	}
	flush := func() {
		if para == "" {
			return
		}
		fmt.Fprintln(out, wrapText(para, width))
		para = ""
	}

	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		switch {
		case strings.TrimSpace(line) == "":
			flush()
			fmt.Fprintln(out)
		case strings.HasPrefix(line, "# "):
			flush()
			// Capitals, because there is nothing else a heading can be
			// made of that survives being written to a file.  See the
			// head of this file.
			fmt.Fprintln(out, strings.ToUpper(strings.TrimPrefix(line, "# ")))
		case strings.HasPrefix(line, "\t"):
			flush()
			// Four spaces rather than the tab itself, because a tab is
			// eight columns wide in some terminals and the example then
			// wraps where the page did not mean it to.
			fmt.Fprintln(out, "    "+strings.TrimPrefix(line, "\t"))
		default:
			add(strings.TrimSpace(line))
		}
	}
	flush()
}

// endsASentence is the join between two lines of one paragraph: a full
// stop at the end of one wants two spaces before the next, and anything
// else -- a colon included, which takes one space -- wants one.
func endsASentence(s string) bool {
	if s == "" {
		return false
	}
	switch s[len(s)-1] {
	case '.', '!', '?':
		return true
	}
	return false
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
