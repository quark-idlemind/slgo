package main

// The checks between what the model says and what ask prints.
//
// A model small enough to run beside everything else will, asked about
// a shell, sometimes describe the shell it expected: a "sethome"
// command, a --long flag on ls, a -r on landmark because -r is what
// recursive flags are called.  Each of those reads perfectly well and
// every one of them is refused the moment it is typed.  The prompt asks
// the model not to (askprompt.go), and asking is not enough, so nothing
// the model proposes is printed as a command until it has been through
// here.
//
// # What is checked
//
// The line, by the shell's own machinery.  It is split by parse, the
// same function a typed line goes through, its first word is looked up
// in the same table -- which is how "exit" is quit and "unsit" is stand
// -- and its flags are parsed by getopt with that command's own option
// struct, the one its usage line is composed from.  So a flag passes
// here exactly when the command would take it, and a refusal is worded
// exactly as the shell would word it: "ls: unknown option: --long".
// Nothing is run.  parseOptions (options.go) is the parse with nothing
// done afterwards, and it is the same parse subOptions does before a
// command acts.
//
// The operands are not checked, and cannot be.  How many a command
// takes and what they mean is decided by the command's own code after
// the flags are parsed -- "tp" alone takes a region, a position, or the
// word home -- and the usage line's parameters are prose written for a
// person rather than a grammar.  A check on them would refuse lines the
// command takes.  What IS checked is one thing about them: that nothing
// after the first operand looks like a flag.  getopt stops at the first
// operand, so "landmark Example Workshop --go" is a request to read a
// landmark called "Example Workshop --go"; the shell would take it
// without complaint and do something other than what the model said it
// would.  That is exactly the case a checker is for.
//
// The quote, against the command's own words.  The model is asked to
// support each suggestion with a quotation from what it was shown, and
// the quotation has to be found -- in that command's man page, usage
// line or brief -- after both sides have had whitespace, case and
// markdown emphasis taken out of them.  A quotation cannot be invented
// and still pass, and a model that has to find the sentence that says a
// command does a thing is a model that has read it.  Very short quotes
// are refused, because "the landmark" is in every page about landmarks
// and proves nothing.

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/pborman/getopt/v2"
)

// askQuoteMinWords is the shortest quote that is evidence of anything.
// The prompt asks for five and this accepts three, so that a model that
// quotes a short, exact phrase -- "sit on the ground" -- is not refused
// for it, while "the landmark" still is.
const askQuoteMinWords = 3

// checkCommandLine says whether the shell would take this line's
// command and flags, without running it.  The error is the one the
// shell would print, less its "slsh: " prefix.
func checkCommandLine(line string) error {
	line = strings.TrimSpace(line)
	if line == "" {
		return errors.New("the line is empty")
	}
	if strings.HasPrefix(line, "#") {
		// Do skips these, so the line would do nothing at all.
		return errors.New("the line is a comment, not a command")
	}
	words, _, _, err := parse(line)
	if err != nil {
		return err
	}
	if len(words) == 0 {
		return errors.New("the line has no command in it")
	}
	name, args := words[0], words[1:]
	c, ok := commands[name]
	if !ok {
		// run's own words.
		return fmt.Errorf("%s: no such command; try help", name)
	}
	if c.flags == nil {
		// echo, which takes every word as it comes: there are no flags
		// to get wrong, and "--help" is text.
		return nil
	}
	if fix := askBeforeParse(c); fix != nil {
		args = fix(args)
	}
	set, err := parseOptions(name, c.params, c.flags(), args)
	if err != nil {
		return fmt.Errorf("%s: %v", name, err)
	}
	return checkNoFlagAfterOperands(name, c, args, set.Args())
}

// askBeforeParse is whatever a command does to its words before it
// hands them to getopt, so that the check parses what the command would
// parse.  tp is the one: a position with a negative number in it has a
// "--" put in front of it (objects.go), without which "tp 10 -5 20"
// would be refused here as an unknown option -5 and taken by tp.
func askBeforeParse(c *command) func([]string) []string {
	if c == commands["tp"] {
		return endOptionsAtANegativeNumber
	}
	return nil
}

// checkNoFlagAfterOperands refuses a word that looks like a flag after
// the first operand, where getopt has stopped looking and the command
// will take it as text.  See the head of this file for why.
//
// After an explicit "--" nothing is a flag, which is what the "--" is
// for.  A negative number is not a flag either: tp's positions and
// say's text can hold one.
func checkNoFlagAfterOperands(name string, c *command, args, rest []string) error {
	used := len(args) - len(rest)
	if used > 0 && args[used-1] == "--" {
		return nil
	}
	for _, w := range rest {
		if !askLooksLikeFlag(w) {
			continue
		}
		// Whether the command has such a flag decides which mistake it
		// is, and the parse that says so is the command's own.
		_, err := parseOptions(name, c.params, c.flags(), []string{w})
		var ge *getopt.Error
		if errors.As(err, &ge) && ge.ErrorCode == getopt.UnknownOption {
			return fmt.Errorf("%s: %v", name, err)
		}
		return fmt.Errorf("%s: %s comes after %q, where it is taken as text and not as a flag; flags go first",
			name, w, rest[0])
	}
	return nil
}

// askLooksLikeFlag is a word getopt would have read as a flag had it
// come first: a dash and something after it that is not a number.
func askLooksLikeFlag(w string) bool {
	if len(w) < 2 || w[0] != '-' || w == "--" {
		return false
	}
	r := rune(w[1])
	if w[1] == '-' && len(w) > 2 {
		r = rune(w[2])
	}
	return !unicode.IsDigit(r) && r != '.'
}

// checkQuote says whether quote is in what the command named says about
// itself: its man page, its usage line, or its brief.
//
// An ellipsis in the quote is allowed and taken at its word: the pieces
// either side of it must each be found, in that order, in the same one
// of those three, and each must be long enough to be evidence on its
// own.  Models elide; what they must not do is join two sentences from
// two places into one that neither says.
func checkQuote(command, quote string) error {
	c, ok := commands[command]
	if !ok {
		return fmt.Errorf("%s: no such command", command)
	}
	if strings.TrimSpace(quote) == "" {
		return errors.New("there is no quote")
	}
	var pieces []string
	for _, p := range askSplitEllipsis(quote) {
		if p = quoteNormal(p); p != "" {
			pieces = append(pieces, p)
		}
	}
	if len(pieces) == 0 {
		return errors.New("there is no quote")
	}
	page, whole := askQuoteSources(c)

	// A whole brief is evidence however short it is: "leave slsh" is
	// everything quit says about itself, and not a fragment picked
	// because it happened to be somewhere.  A whole usage line is not
	// given the same allowance, because for pwd or look it is one word,
	// and the command's own name proves nothing about what it does.
	if len(pieces) == 1 && quoteNormal(c.brief) == pieces[0] {
		return nil
	}
	for _, p := range pieces {
		if len(strings.Fields(p)) < askQuoteMinWords {
			return fmt.Errorf("the quote %q is too short to show anything; it needs at least %d words together",
				strings.TrimSpace(quote), askQuoteMinWords)
		}
	}

	for _, src := range append([]string{page}, whole...) {
		if askInOrder(quoteNormal(src), pieces) {
			return nil
		}
	}
	return fmt.Errorf("the quote %q is not in %s's man page, usage line or brief",
		strings.TrimSpace(quote), command)
}

// askQuoteSources is everything a quote about c may come from: the
// page, which is "" for a command without one, and the short texts --
// the brief, and the usage line under every name c answers to, since a
// line written with "exit" may quote the usage line help prints for it.
func askQuoteSources(c *command) (page string, whole []string) {
	if c.man != "" {
		page, _ = manRead(c.man)
	}
	whole = append(whole, c.brief)
	for _, n := range commandNames() {
		if commands[n] == c {
			whole = append(whole, c.usage(n))
		}
	}
	return page, whole
}

// askInOrder is whether every piece is in src, each after the last.
func askInOrder(src string, pieces []string) bool {
	at := 0
	for _, p := range pieces {
		i := strings.Index(src[at:], p)
		if i < 0 {
			return false
		}
		at += i + len(p)
	}
	return true
}

// askSplitEllipsis cuts a quote at "..." and at the one-character
// ellipsis.  "[...]" is the same cut with brackets round it.
func askSplitEllipsis(q string) []string {
	q = strings.NewReplacer("[...]", "\x00", "[\u2026]", "\x00", "...", "\x00", "\u2026", "\x00").Replace(q)
	return strings.Split(q, "\x00")
}

// quoteNormal is the form a quote and a page are compared in.
//
// Case goes, and runs of white space become one space, so that a quote
// is found whatever the page's line breaks were.  Markdown's emphasis
// and code marks go, because the page writes **--go** and *NAME* and a
// model quoting it writes --go and NAME; a heading's hashes go for the
// same reason, and a table's bars become spaces.  Typographic quotes and
// dashes are brought back to the ASCII the pages are written in, since
// that is the change a model most often makes to a quotation without
// being asked.  Quote marks and full stops round the ends go last: a
// model puts the one round a quotation and leaves the other off the end
// of a sentence it cut short.
//
// Both sides get the same treatment, so what is forgiven here is
// forgiven symmetrically and nothing in a page can become unfindable by
// it.
func quoteNormal(s string) string {
	s = strings.NewReplacer(
		"\u2018", "'", "\u2019", "'", "\u201c", `"`, "\u201d", `"`,
		"\u2014", "--", "\u2013", "-",
		"*", "", "`", "", "_", "", "#", "", "|", " ", "\\", "",
	).Replace(s)
	s = strings.ToLower(strings.Join(strings.Fields(s), " "))
	return strings.Trim(s, ` "'.,;:`)
}

// askCleanLine takes off what a model puts round a command line that is
// not part of it: backticks, and a prompt's "$ " in front.  Nothing
// inside the line is touched.
func askCleanLine(line string) string {
	line = strings.TrimSpace(line)
	line = strings.TrimSpace(strings.Trim(line, "`"))
	line = strings.TrimPrefix(line, "$ ")
	return strings.TrimSpace(line)
}

// askRejection is a suggestion that did not survive, and why.
type askRejection struct {
	Suggestion askSuggestion
	Reasons    []string
}

// filterAskAnswer puts every suggestion through the checks.  kept is
// what may be printed, in the model's order, with each line cleaned up
// as askCleanLine does; rejected is the rest, with every reason each
// one failed rather than only the first, since the retry message is
// the one chance to say all of it.
//
// The command a suggestion is about is the one its line runs.  The
// command field is the model's own label and is checked against the
// line rather than trusted: a line that runs one command with a quote
// from another's page is a model that has muddled two of them, whatever
// each half looks like alone.  Names that share a command -- quit and
// exit, stand and unsit -- are the same command here.
//
// A line suggested twice is kept once, and the second is not a
// rejection: it is not wrong, only repeated, and it should not cost a
// retry.
func filterAskAnswer(a askAnswer) (kept []askSuggestion, rejected []askRejection) {
	seen := map[string]bool{}
	for _, s := range a.Suggestions {
		s.CommandLine = askCleanLine(s.CommandLine)
		s.Command = strings.TrimSpace(s.Command)

		var reasons []string
		if err := checkCommandLine(s.CommandLine); err != nil {
			reasons = append(reasons, err.Error())
		}
		name := askFirstWord(s.CommandLine)
		lineCmd, lineOK := commands[name]
		if s.Command != "" {
			labelCmd, ok := commands[s.Command]
			switch {
			case !ok:
				reasons = append(reasons, fmt.Sprintf("%s: no such command", s.Command))
			case lineOK && labelCmd != lineCmd:
				reasons = append(reasons, fmt.Sprintf("the suggestion is labelled %s but the line runs %s", s.Command, name))
			}
			if !lineOK && ok {
				// The line is refused already; the quote is still worth
				// checking against the command it claims to be about,
				// so the retry can say whether that half was right.
				name, lineOK = s.Command, true
			}
		}
		if lineOK {
			if err := checkQuote(name, s.Quote); err != nil {
				reasons = append(reasons, err.Error())
			}
		}

		if len(reasons) > 0 {
			rejected = append(rejected, askRejection{Suggestion: s, Reasons: reasons})
			continue
		}
		key := strings.Join(strings.Fields(s.CommandLine), " ")
		if seen[key] {
			continue
		}
		seen[key] = true
		kept = append(kept, s)
	}
	return kept, rejected
}
