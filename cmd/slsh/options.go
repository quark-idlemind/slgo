package main

// Options for the commands inside the shell, and the one line that says
// how a command is typed.
//
// The same getopt the programs themselves use, so that a flag means the
// same thing in "slsh -c ..." as at the prompt, and clusters like -lrT
// work without every combination being spelled out by hand.
//
// # Why the usage line is derived and not written down
//
// It used to be written down three times: a string in the command
// table, the same words again in the command's own "usage:" refusal, and
// a third wording from getopt in --help.  Three copies kept by hand, and
// they had already drifted -- perms named three of its four permission
// flags in the table, touch left its trailing points out of --help, and
// put's three forms appeared in one place only.  A person who read one
// of them and typed what it said was sometimes wrong.
//
// So there is one composer, usageLine, and everything goes through it:
// the name, whatever getopt's Set.UsageLine makes of the option struct,
// and the parameters.  A flag added to a struct appears in the help
// listing, in the refusal and in --help with no other edit, because none
// of those three has any words of its own to change.
//
// One thing is dropped on the way through, and only one: the help flag.
// Every command has it, so it tells nobody anything about the command
// they are looking at, and it is not free -- getopt bundles the short
// flags, so a listing of the waiting group read "waiting [-ah]",
// "no [-h] N", "ignore [-h] N" down the page and pushed answer's line
// onto a second row to make room for a flag all four of them share.
// The foot of every listing already says "COMMAND --help", and --help
// itself still lists -h underneath the line, which is where a person
// looks for it; a usage line that leaves it out while the options under
// it name it is the ordinary shape of a Unix tool rather than a
// disagreement.
//
// It comes out inside the composer, in withoutHelp, and nowhere else.
// Doing it at the call sites would give the three places three chances
// to disagree again, which is the whole of what this arrangement is
// for.
//
// The parameters -- the part after the flags -- stay a written string,
// on the command, because nothing can derive them: getopt parses flags
// and hands back the rest as words, and only the command knows whether
// the rest is "PATH DEST" or "N [BUTTON|TEXT|L$FEE]".

import (
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/pborman/getopt/v2"
	"github.com/pborman/options"
)

// subOptions parses one command's flags, and prints its usage if that
// is what was asked for.
//
// Each call gets a set of its own, which matters twice over: nothing a
// command registers is still registered for the next one, and --help
// can print THIS command's options.  The shell runs many commands in
// one process, which is the case a package-level set gets wrong.
//
// The parameters come from the command table rather than from the
// caller, so that the line printed here and the line help prints are
// composed from the same two pieces.  A name that is not a command --
// which happens only in tests -- has none, and gets a set with no
// parameters rather than a refusal.
//
// done is true when the usage was printed and the command has nothing
// further to do.
//
// options.Help is deliberately not used for the --help flag. It calls
// os.Exit(0), which in a shell would take the whole session down when
// somebody typed "ls -h"; and it prints getopt's GLOBAL set, which here
// holds slsh's own program flags, so "ls -h" answered with --addr and
// --agent. Printing this set to the command's own writer is what was
// wanted from it, and redirects with > like any other output.
func subOptions(name string, opts any, out io.Writer, args []string) (rest []string, done bool, err error) {
	c := commands[name]
	params := ""
	if c != nil {
		params = c.params
	}

	// The struct parsed here has to be the struct the table offers, or
	// the usage line would describe one command and the parsing another
	// -- which is exactly the drift this arrangement exists to end, and
	// the one way left to reintroduce it.  It is a mistake in the tables
	// in this package rather than anything a person typed, so it is
	// worth saying plainly which two disagree.
	if c != nil && c.flags != nil {
		if want := reflect.TypeOf(c.flags()); reflect.TypeOf(opts) != want {
			return nil, false, fmt.Errorf("%s parses %T and its table offers %s; "+
				"make both name the same struct", name, opts, want)
		}
	}

	set, err := parseOptions(name, params, opts, args)
	if err != nil {
		return nil, false, err
	}
	if set.IsSet("help") {
		// getopt's PrintUsage would compose the same line from the same
		// three pieces, and is not used, because then this package would
		// have two composers and only one of them under test.
		fmt.Fprintf(out, "Usage: %s\n", usageLine(set, name, params))
		set.PrintOptions(out)
		if c != nil && c.man != "" {
			fmt.Fprintf(out, "\n\"man %s\" describes it at length.\n", name)
		}
		return nil, true, nil
	}
	return set.Args(), false, nil
}

// sawOperand is whether these words hold anything that is not an option
// or an option's value -- the first word of a region's name, in the one
// place this is asked.
//
// It asks getopt rather than working it out, because the question is
// the one getopt already answers when it stops: parsing ends at the
// first operand and everything from there on is left in Args.  Working
// it out here would be a second copy of the rule that --wait takes the
// word after it, and two copies of that rule are free to disagree the
// day a flag is added.
//
// The struct is the caller's to throw away.  This parse sets whatever
// it finds, and the parse that counts has not run yet.
//
// A refusal -- an option nobody has heard of, a --wait with nothing
// after it -- is neither an operand nor an answer, and it says yes.
// The line then goes on exactly as it was typed, and the real parse
// refuses it in its own words rather than in words about a "--" this
// put there.
func sawOperand(name string, opts any, args []string) bool {
	set, err := parseOptions(name, "", opts, args)
	if err != nil {
		return true
	}
	return len(set.Args()) > 0
}

// parseOptions is the parse itself and nothing else: a set of the
// command's own, the option struct registered on it, and getopt run over
// the words.  It prints nothing and acts on nothing, not even --help,
// which is what lets it be asked about a line nobody is going to run --
// sawOperand asks it where the operands begin, and ask's checker
// (askcheck.go) asks it whether a suggested line would be refused.
//
// subOptions is this plus the help flag.  Keeping the two as one parse
// and one thing done with its result, rather than a parse in each, is
// what keeps a line the checker passes a line the command will take.
func parseOptions(name, params string, opts any, args []string) (*getopt.Set, error) {
	set := getopt.New()
	set.SetProgram(name)
	set.SetParameters(params)
	if err := options.RegisterSet(name, opts, set); err != nil {
		return nil, err
	}
	argv := make([]string, 0, len(args)+1)
	argv = append(argv, name)
	argv = append(argv, args...)
	if err := set.Getopt(argv, nil); err != nil {
		return nil, err
	}
	return set, nil
}

// usage is the one line that says how a command is typed: its name, its
// flags, and what it takes after them.
//
// The name is the caller's rather than the command's own, because a
// command does not have one -- the table is keyed by name and two names
// can share a command.  So "exit" says exit, which is what was typed.
func (c *command) usage(name string) string {
	set := getopt.New()
	set.SetProgram(name)
	set.SetParameters(c.params)
	if c.flags != nil {
		// An option struct that will not register is a mistake in this
		// package's own tables, and the command says so with an error
		// the moment anybody runs it (subOptions returns what
		// RegisterSet returned).  Here it would cost a line in a help
		// listing, and a listing that is missing a command's flags is
		// still a listing.
		_ = options.RegisterSet(name, c.flags(), set)
	}
	return usageLine(set, name, c.params)
}

// usageLine joins the three pieces.  The only composer: help listings,
// a command's own refusal and --help all end up here, which is what
// makes them agree.
func usageLine(set *getopt.Set, name, params string) string {
	parts := make([]string, 0, 3)
	parts = append(parts, name)
	if flags := withoutHelp(set); flags != "" {
		parts = append(parts, flags)
	}
	if params != "" {
		parts = append(parts, params)
	}
	return strings.Join(parts, " ")
}

// withoutHelp is getopt's flag part with the help flag taken out of it.
// See the head of this file for why that one and nothing else.
//
// The spellings are asked of the set rather than assumed to be -h and
// --help, so a command that named its help flag something else would
// still have the right thing removed and a set with no help flag at all
// is left exactly as getopt wrote it.
//
// Two shapes have to be handled, because getopt writes the boolean short
// flags as one cluster and everything else as a group of its own
// (getopt.go:296-322): the flag is a letter inside that cluster, and it
// is a group in its own right when it was declared with no short name.
// A cluster with nothing left in it goes altogether, so a command whose
// only flag is help reads "pwd" rather than "pwd []".
func withoutHelp(set *getopt.Set) string {
	line := set.UsageLine()
	help := set.Lookup("help")
	// getopt hands back its concrete option type, so a name it does not
	// have comes back as a typed nil inside a non-nil interface
	// (set.go:196) and "help == nil" would not catch it.  Calling
	// IsFlag on that panics, which in a shell is the session.
	if line == "" || help == nil || reflect.ValueOf(help).IsNil() || !help.IsFlag() {
		return line
	}
	long, short := "--"+help.LongName(), help.ShortName()

	// getopt joins the groups with "] [" and wraps the lot, so taking
	// the ends off and splitting on the join gives them back exactly.
	groups := strings.Split(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"), "] [")
	kept := make([]string, 0, len(groups))
	for _, g := range groups {
		switch {
		case g == long:
		case short != "" && isShortCluster(g) && strings.Contains(g, short):
			// Short names are unique within a set, so the one occurrence
			// removed here is the help flag's and nothing else's.
			if rest := "-" + strings.Replace(g[1:], short, "", 1); rest != "-" {
				kept = append(kept, rest)
			}
		default:
			kept = append(kept, g)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	return "[" + strings.Join(kept, "] [") + "]"
}

// isShortCluster tells the bundle of boolean short flags -- "-ah" --
// from an option that carries a value, "-w SECONDS", and from a long
// one.  Only the bundle has a letter to take out of it.
func isShortCluster(g string) bool {
	return len(g) > 1 && strings.HasPrefix(g, "-") &&
		!strings.HasPrefix(g, "--") && !strings.Contains(g, " ")
}

// usageError is a command refusing what it was given.
//
// The line is the one help prints and the one --help prints, so
// somebody who reads it here and then asks the command itself is not
// told two different things.  Lower case and no full stop, like every
// other error in the shell.
//
// Notes are the sentences a usage line cannot carry: which of two
// commands was probably meant, why the count is exact, what to type
// instead.  They are indented under the line so that the line itself
// stays the thing the eye lands on.
func usageError(name string, notes ...string) error {
	c, ok := commands[name]
	if !ok {
		// Nothing sensible to derive from, which means a command asked
		// about a name it is not registered under.  Say the name rather
		// than nothing at all.
		return fmt.Errorf("usage: %s", name)
	}
	line := "usage: " + c.usage(name)
	for _, n := range notes {
		line += "\n        " + n
	}
	return fmt.Errorf("%s", line)
}

// helpOnly is for a command whose only flag is --help.
type helpOnly struct {
	Help bool `getopt:"--help -h  show what this command takes"`
}
