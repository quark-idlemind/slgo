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
// Most operands are not checked, and cannot be.  How many a command
// takes and what they mean is decided by the command's own code after
// the flags are parsed -- "tp" alone takes a region, a position, or the
// word home -- and the usage line's parameters are prose written for a
// person rather than a grammar.  A check on them would refuse lines the
// command takes.  What IS checked of every command is two things about
// them.  One is that nothing after the first operand looks like a flag:
// getopt stops at the first operand, so "landmark Example Workshop --go"
// is a request to read a landmark called "Example Workshop --go"; the
// shell would take it without complaint and do something other than
// what the model said it would.  That is exactly the case a checker is
// for.  The other is that no operand is the usage line's own syntax copied
// out as it stands -- "unlink NAME|UUID", "put [-lN] FILE" -- which the
// shell would take as names in just the same way (checkNoUsageSyntax).
//
// # Operands from a list the shell holds
//
// Some operands are not names of things in the world but words from a
// list the shell itself keeps: set's setting names, maturity's ratings,
// help's groups, man's commands, neighbours' on and off, and the values
// of put's --round and --filter, wear's --at and perms' letters.  Those
// ARE checked, because a model will write "set display_name ..." with
// every appearance of confidence, and the shell's answer to it is a
// refusal.  Each is checked by the code the command itself would use
// -- findSetting, sl.ParseMaturity, attachPointArg, permMask -- and
// never by a copy of its list, which would be free to disagree the day
// a setting is added.
//
// A capitalised placeholder the usage line itself uses -- "set NAME
// VALUE", "maturity RATING", "--at=POINT" -- passes, because that is
// what the prompt asks a model to write where a person fills something
// in.  One the usage line does not use, like YOUR_DISPLAY_NAME, is an
// operand like any other and has to be in the list.
//
// new's --kind is the one such list this cannot reach: it is a switch
// inside cmdNew rather than a list anything else can ask, and a copy of
// it here is the thing not to write.
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
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/pborman/getopt/v2"

	"github.com/quark-idlemind/slgo/sl"
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
	if r, ok := askUntypeable(line); ok {
		// Nobody can type an escape sequence or a second line into one
		// line, and a model that puts one there has written something
		// that would do more to a terminal than print a command: the
		// line is printed as it is once it passes.
		return fmt.Errorf("the line has %U in it, which is not a character anybody types", r)
	}
	words, quoted, _, _, err := parseQuoted(line)
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
	if err := checkNoUsageSyntax(name, c, args, quoted[1:]); err != nil {
		return err
	}
	if fix := askBeforeParse(c); fix != nil {
		args = fix(args)
	}
	opts := c.flags()
	set, err := parseOptions(name, c.params, opts, args)
	if err != nil {
		return fmt.Errorf("%s: %v", name, err)
	}
	if err := checkNoFlagAfterOperands(name, c, args, set.Args()); err != nil {
		return err
	}
	if err := checkOperands(name, c, opts, set.Args()); err != nil {
		return fmt.Errorf("%s: %v", name, err)
	}
	return nil
}

// askUntypeable is the first character in line that is not one a
// person could type into it: a control character other than tab, which
// the shell splits on like a space, or a formatting character, which
// includes the ones that turn text round on the screen.
func askUntypeable(line string) (rune, bool) {
	for _, r := range line {
		if r == '\t' {
			continue
		}
		if unicode.In(r, unicode.Cc, unicode.Cf) {
			return r, true
		}
	}
	return 0, false
}

// askOperandRule checks the operands and flag values of one command
// whose words come from a list the shell holds.  opts is the option
// struct parseOptions filled in, operands what getopt left, and
// placeholder says whether a word is one the usage line writes in
// capitals.  The error is the command's own refusal where it has one,
// without the command's name, which the caller puts in front.
type askOperandRule func(opts any, operands []string, placeholder func(string) bool) error

// askOperandRules is every command with such a list, by name.  See the
// head of this file for why each is checked by the command's own code.
var askOperandRules = map[string]askOperandRule{
	"set":        askCheckSet,
	"maturity":   askCheckMaturity,
	"help":       askCheckHelp,
	"man":        askCheckMan,
	"neighbours": askCheckNeighbours,
	"put":        askCheckPut,
	"wear":       askCheckWear,
	"perms":      askCheckPerms,
}

// checkOperands runs c's rule, if it has one, under whichever name the
// line used: a rule is for the command, and a second name for it is the
// same command.
func checkOperands(name string, c *command, opts any, operands []string) error {
	for n, rule := range askOperandRules {
		if commands[n] != c {
			continue
		}
		ph := askPlaceholders(c.usage(name))
		return rule(opts, operands, func(w string) bool { return ph[w] })
	}
	return nil
}

// askPlaceholderWord is a word in capitals, as a usage line writes what
// a person fills in: NAME, VALUE, L$FEE's FEE.
var askPlaceholderWord = regexp.MustCompile(`[A-Z][A-Z0-9_]*`)

// askPlaceholders is every capitalised word in a usage line.  Only
// whole words count: the D in "--dry-run" is not in capitals and is
// not a placeholder, and nothing in lower case ever is.
func askPlaceholders(usage string) map[string]bool {
	out := map[string]bool{}
	for _, loc := range askPlaceholderWord.FindAllStringIndex(usage, -1) {
		i, j := loc[0], loc[1]
		if i > 0 && unicode.IsLetter(rune(usage[i-1])) || j < len(usage) && unicode.IsLetter(rune(usage[j])) {
			continue
		}
		out[usage[i:j]] = true
	}
	return out
}

// askCheckSet is set NAME VALUE: the name must be a setting findSetting
// knows, under any spelling the file takes, and a value must be one that
// setting's own parse accepts -- into a copy of the defaults, so that
// nothing is changed by asking.  "auto" is map_ratio's, as cmdSet says;
// it is measured when it is typed and so passes here only there.
func askCheckSet(_ any, operands []string, placeholder func(string) bool) error {
	if len(operands) == 0 || placeholder(operands[0]) {
		return nil
	}
	s, ok := findSetting(operands[0])
	if !ok {
		return fmt.Errorf("no setting called %q; there is %s",
			operands[0], strings.Join(settingNames(), ", "))
	}
	if len(operands) == 1 {
		return nil
	}
	value := strings.Join(operands[1:], " ")
	if len(operands) == 2 && placeholder(value) {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(value), autoWord) {
		if s.name != autoSetting {
			return fmt.Errorf("%s: only %s takes %q; every other setting takes the value itself",
				s.name, autoSetting, autoWord)
		}
		return nil
	}
	scratch := DefaultConfig()
	if err := s.parse(&scratch, value); err != nil {
		return fmt.Errorf("%s: %v", s.name, err)
	}
	return nil
}

// askCheckMaturity is maturity RATING, by sl.ParseMaturity, which is
// what the command reads the rating with.
func askCheckMaturity(_ any, operands []string, placeholder func(string) bool) error {
	if len(operands) == 0 || len(operands) == 1 && placeholder(operands[0]) {
		return nil
	}
	_, err := sl.ParseMaturity(strings.Join(operands, " "))
	return err
}

// askCheckHelp is help GROUP: a group, "all", or -- which help answers
// by saying where that command's help is -- a command.
func askCheckHelp(_ any, operands []string, placeholder func(string) bool) error {
	if len(operands) == 0 || placeholder(operands[0]) || operands[0] == "all" {
		return nil
	}
	if _, ok := findGroup(operands[0]); ok {
		return nil
	}
	if _, ok := commands[operands[0]]; ok {
		return nil
	}
	return fmt.Errorf("no group or command %q; groups are %s",
		operands[0], strings.Join(groupNames(), ", "))
}

// askCheckMan is man NAME: one command, as cmdMan takes it.
func askCheckMan(_ any, operands []string, placeholder func(string) bool) error {
	switch {
	case len(operands) == 0:
		return nil
	case len(operands) > 1:
		return errors.New("one command at a time")
	case placeholder(operands[0]):
		return nil
	}
	if _, ok := commands[operands[0]]; !ok {
		return fmt.Errorf("no command called %q; \"help all\" lists them", operands[0])
	}
	return nil
}

// askCheckNeighbours is neighbours [on|off].  The words are read off the
// command's own parameters, which name them, rather than written out
// again here; the command compares them without regard to case.
func askCheckNeighbours(_ any, operands []string, _ func(string) bool) error {
	words := strings.Split(strings.Trim(commands["neighbours"].params, "[]"), "|")
	switch len(operands) {
	case 0:
		return nil
	case 1:
		for _, w := range words {
			if strings.EqualFold(operands[0], w) {
				return nil
			}
		}
		return fmt.Errorf("%q is neither %s", operands[0], strings.Join(words, " nor "))
	}
	return errors.New("one word at most")
}

// askCheckPut is put's --round, --round-x, --round-y and --filter, by
// the method put reads them with.  A placeholder in one of them is
// taken out first, so that the rest are still checked.
func askCheckPut(opts any, _ []string, placeholder func(string) bool) error {
	o, ok := opts.(*putFlags)
	if !ok {
		return nil
	}
	c := *o
	for _, f := range []*string{&c.Round, &c.RX, &c.RY, &c.Filter} {
		if placeholder(*f) {
			*f = ""
		}
	}
	_, err := c.resize()
	return err
}

// askCheckWear is wear's --at, by attachPointArg, which is how wear
// reads it: a point's name as the viewer writes it, or its number.
func askCheckWear(opts any, _ []string, placeholder func(string) bool) error {
	o, ok := opts.(*wearFlags)
	if !ok || o.At == "" || placeholder(o.At) {
		return nil
	}
	_, err := attachPointArg(o.At)
	return err
}

// askCheckPerms is perms' four sets of letters, by permMask.
func askCheckPerms(opts any, _ []string, placeholder func(string) bool) error {
	o, ok := opts.(*permsFlags)
	if !ok {
		return nil
	}
	for _, f := range []struct{ flag, text string }{
		{"--owner", o.Owner}, {"--group", o.Group}, {"--everyone", o.Everyone}, {"--next", o.Next},
	} {
		if f.text == "" || placeholder(f.text) {
			continue
		}
		if _, err := permMask(f.text); err != nil {
			return fmt.Errorf("%s: %w", f.flag, err)
		}
	}
	return nil
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

// checkNoUsageSyntax refuses a word copied out of the command's usage
// line as syntax rather than filled in: a bracketed group, or half of
// one, like "[-lN]", "[-d", "TEXT]" or "[NAME]", and an alternative
// like "NAME|UUID" or "on|off".  The shell takes every one of them as
// an operand -- "unlink NAME|UUID" goes looking for an object called
// that -- so a line with one in it does something other than it seems
// to say, while every other check passes it.
//
// Only what the usage line itself has counts, so that this refuses the
// copying and not the characters: a bracketed flag, whatever it is,
// since no flag is written with brackets; and otherwise a word whose
// inside, brackets and a trailing "..." taken off, is a word of that
// usage line or, for an alternative, has one of its placeholders in it.
// "say [OOC] back soon" passes, and so does "say this|that", because
// neither is in say's usage line.  A word with quotes anywhere in it is
// not looked at at all: it is a value written as it stands on purpose,
// such as a JSON value for set.  No example line in any man page has
// brackets or a bar in it (TestEveryManPageExampleIsTakenByTheChecker).
//
// The shell's own capitalised placeholders, NAME and PATH alone, pass
// here as they do everywhere else.
func checkNoUsageSyntax(name string, c *command, args []string, quoted []bool) error {
	usage := c.usage(name)
	fields := askUsageFields(usage)
	ph := askPlaceholders(usage)
	for i, w := range args {
		if quoted[i] {
			continue
		}
		var what string
		isFlag := false
		inner := strings.TrimSuffix(strings.TrimRight(strings.TrimLeft(w, "["), "]"), "...")
		alternatives := strings.Split(inner, "|")
		switch {
		case (strings.HasPrefix(w, "[") || strings.HasSuffix(w, "]")) &&
			(askLooksLikeFlag(inner) || inner == "" && strings.Contains(w, "...") || fields[inner]):
			what, isFlag = "[...], which marks what may be left out", askLooksLikeFlag(inner)
		case len(alternatives) > 1 && !slices.Contains(alternatives, "") &&
			(fields[inner] || slices.ContainsFunc(alternatives, func(a string) bool { return ph[a] })):
			what = "A|B, which means one of them"
		default:
			continue
		}
		msg := fmt.Sprintf("%s: %q is the usage line's %s; write one real value, not the usage line's [..] or A|B", name, w, what)
		if isFlag {
			msg += ", and a flag without the brackets or not at all"
		}
		return errors.New(msg)
	}
	return nil
}

// askUsageFields is every word of a usage line with its brackets and a
// trailing "..." taken off, whole and, where it has a bar in it, as each
// of its alternatives: "[NAME|UUID]" is NAME|UUID, NAME and UUID.
func askUsageFields(usage string) map[string]bool {
	out := map[string]bool{}
	for _, f := range strings.Fields(usage) {
		f = strings.TrimSuffix(strings.TrimRight(strings.TrimLeft(f, "["), "]"), "...")
		if f == "" {
			continue
		}
		out[f] = true
		for _, a := range strings.Split(f, "|") {
			if a != "" {
				out[a] = true
			}
		}
	}
	return out
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
	if joined, ok := askJoinHeadings(page, pieces); ok && askInOrder(quoteNormal(page), joined) {
		return nil
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

// askJoinHeadings is pieces with the mark taken out that a model puts
// between one of the page's headings and the paragraph under it, and
// whether there was one to take out.
//
// The prompt shows a section's heading outside its text, as "manual,
// Setting home:" (askUser), and a model quoting both writes "Setting
// home: Make where ...", or puts a full stop or a dash there instead.
// The page has the heading on a line of its own, which quoteNormal makes
// "setting home make where ...", and no colon.  So where a piece starts
// with a heading of the page -- a "#" line, or an option's "**--go**"
// line -- followed by one of those marks, the mark goes.  The piece must
// still be found whole, so this accepts a heading run into the paragraph
// directly under it and never one joined to a paragraph from somewhere
// else in the page.
func askJoinHeadings(page string, pieces []string) ([]string, bool) {
	var headings []string
	for _, line := range strings.Split(page, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "**") {
			if h := quoteNormal(line); h != "" {
				headings = append(headings, h)
			}
		}
	}
	out := make([]string, len(pieces))
	changed := false
	for i, p := range pieces {
		out[i] = p
		for _, h := range headings {
			rest, ok := strings.CutPrefix(p, h)
			if !ok {
				continue
			}
			body := strings.TrimLeft(rest, ":.- ")
			if body == rest || body == "" || !strings.HasPrefix(rest, ":") && !strings.HasPrefix(rest, ".") && !strings.HasPrefix(rest, " -") {
				continue
			}
			out[i], changed = h+" "+body, true
			break
		}
	}
	return out, changed
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
