package main

// The commands a trusted avatar may send, and how a line becomes one.
//
// An instant message that begins with the prefix is a command; anything
// else is somebody talking, which is serve.go's business.  What follows
// the prefix is split into words the way a shell splits them -- quotes
// hold a word together and nothing else is interpreted, because there
// is no shell here and a line that looked like it expanded something
// would be a line that lied.
//
// The shape is slsh's: a table of commands, each with an option struct,
// a one line description and a function that writes to an io.Writer.
// That is not imitation for its own sake.  The writer is what makes the
// answer a thing that can be measured before it is sent: an instant
// message holds about a kilobyte, a listing does not, and a command
// that printed straight to the grid could not be cut off politely.  See
// reply.go.
//
// Every command here runs as the avatar the message was sent to, unless
// "as" says otherwise.  There is no such thing as a command that runs as
// nobody: slbotd's whole business is doing things as one of the avatars
// it holds.

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	"github.com/pborman/getopt/v2"
	"github.com/pborman/options"

	"github.com/quark-idlemind/slgo/msg"
)

// req is one command being run: which avatar is doing it, and who
// asked.
//
// The sender travels with it so that a command that wants to say
// something back to the person who sent it need not be told again who
// that was.  No command reads it; "as" passes it on.
type req struct {
	d *daemon

	// bot is the avatar running this command.  It is the one the
	// message was sent to, or the one "as" named.
	bot *bot

	// from and who are the sender: the id the message really came
	// from, and the name attached to it.
	from msg.UUID
	who  string

	// depth is how many "as" commands deep this is, so that one
	// cannot be made to call itself round a ring of avatars.
	depth int

	// base is the context the command's own deadline was cut from: the
	// session's, which ends when the session does.  A command that
	// takes minutes by design -- a benchmark run -- derives its own
	// deadline from this rather than inheriting one meant for a
	// listing, and still stops when the daemon stops.
	//
	// A context in a struct, which is usually the wrong thing.  It is
	// the exception the rule names: this struct IS the request, it is
	// made for one command and thrown away after it, and the
	// alternative is a second context argument threaded through every
	// command signature for the sake of the one that wants it,
	// runProgram.
	base context.Context
}

// command is one thing slbotd can do.
type command struct {
	// params is what the command takes after its flags, written the
	// way a usage line writes it.  Empty for a command that takes
	// nothing but flags.
	params string

	// flags makes a fresh option struct.  A function rather than a
	// value because parsing fills the struct in and every run wants an
	// empty one.
	flags func() any

	// brief is the one line the help listing prints.
	brief string

	// group is which heading it appears under in that listing.
	group string

	run func(ctx context.Context, r *req, out io.Writer, args []string) error
}

// helpOnly is the option struct of a command whose only flag is --help,
// which is most of them.
type helpOnly struct {
	Help bool `getopt:"--help -h   show what this command takes"`
}

// The headings the help listing uses, in the order it prints them.
const (
	groupLooking   = "looking"
	groupMoving    = "moving"
	groupTalking   = "talking"
	groupInventory = "inventory"
	groupRunning   = "running"
	groupDaemon    = "the daemon"
)

var groupOrder = []string{
	groupLooking, groupMoving, groupTalking, groupInventory, groupRunning, groupDaemon,
}

var commands map[string]*command

func init() {
	commands = map[string]*command{}
	for _, set := range []map[string]*command{
		lookCommands, moveCommands, talkCommands, inventoryCommands, daemonCommands,
	} {
		for n, c := range set {
			if _, twice := commands[n]; twice {
				panic("slbotd: two commands called " + n)
			}
			commands[n] = c
		}
	}
}

// commandNames is every command, sorted, for the help listing.
func commandNames() []string {
	out := make([]string, 0, len(commands))
	for n := range commands {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Run finds the command a line names and runs it.
//
// The line is what followed the prefix.  An empty line is not an error
// and not a command: somebody typed the prefix and nothing else, and
// the help listing is the useful answer to that.
func (r *req) Run(ctx context.Context, out io.Writer, line string) error {
	words, err := splitLine(line)
	if err != nil {
		return err
	}
	if len(words) == 0 {
		words = []string{"help"}
	}
	return r.run(ctx, out, words)
}

func (r *req) run(ctx context.Context, out io.Writer, words []string) error {
	name := words[0]
	if to, ok := r.d.cfg.Aliases[name]; ok {
		name = to
	}
	if c, ok := commands[name]; ok {
		return c.run(ctx, r, out, words[1:])
	}
	// A program is a command whose table entry is in the configuration
	// rather than in this file.  Looked in second, so that a program
	// somebody names "ls" cannot quietly replace the listing.
	if p, ok := r.d.cfg.Programs[name]; ok {
		return runProgram(ctx, r, out, p, words[1:])
	}
	return fmt.Errorf("no command called %q; %shelp lists them", name, r.d.cfg.Prefix)
}

// splitLine breaks a command line into words.
//
// Quotes hold a word together and are removed; there is no escaping, no
// expansion and no redirection.  That is the whole of the syntax,
// deliberately: the line arrives from the grid as a person typed it
// into a viewer, and anything this took to mean something else would be
// a surprise landing on an avatar rather than on a terminal.
//
// A quote is only a quote at the start of a word, or straight after an
// equals sign.  Anywhere else it is the character it looks like, which
// is what lets an apostrophe be typed:
//
//	cat Notecards/Bob's list        one word with an apostrophe in it
//	say "hello there"               one word, quoted
//	slbench --statement="llSin(1);" quoted after the equals
//
// Without that rule every name holding an apostrophe -- and inventory
// is full of them -- is refused as an unclosed quote, which is a
// refusal nobody can do anything about from an instant message.
//
// An empty pair of quotes is a word, which matters for a program
// argument that is meant to be empty: slrun --done "" waits out its
// timeout on purpose.
func splitLine(line string) ([]string, error) {
	var (
		words   []string
		cur     []rune
		started bool
		quote   rune
	)
	flush := func() {
		if started {
			words = append(words, string(cur))
		}
		cur, started = nil, false
	}
	// opens says whether a quote character here begins a quoted run
	// rather than standing for itself.  See the comment above.
	opens := func() bool {
		return !started || (len(cur) > 0 && cur[len(cur)-1] == '=')
	}
	for _, c := range line {
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
				continue
			}
			started = true
			cur = append(cur, c)
		case (c == '"' || c == '\'') && opens():
			quote, started = c, true
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			flush()
		default:
			started = true
			cur = append(cur, c)
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unclosed %c quote", quote)
	}
	flush()
	return words, nil
}

// subOptions parses one command's flags and prints its usage when that
// is what was asked for.
//
// A set of its own per call, for the reason slsh gives: nothing a
// command registers is still registered for the next one, and --help can
// print THIS command's options rather than the daemon's own.
//
// done is true when the usage was printed and there is nothing further
// to do.
func subOptions(name string, opts any, out io.Writer, args []string) (rest []string, done bool, err error) {
	c := commands[name]
	params := ""
	if c != nil {
		params = c.params
	}
	// The struct parsed here has to be the one the table offers, or the
	// usage line would describe one command and the parsing another.
	if c != nil && c.flags != nil {
		if want := reflect.TypeOf(c.flags()); reflect.TypeOf(opts) != want {
			return nil, false, fmt.Errorf("%s parses %T and its table offers %s; "+
				"make both name the same struct", name, opts, want)
		}
	}

	set := getopt.New()
	set.SetProgram(name)
	set.SetParameters(params)
	if err := options.RegisterSet(name, opts, set); err != nil {
		return nil, false, err
	}
	argv := append([]string{name}, args...)
	if err := set.Getopt(argv, nil); err != nil {
		return nil, false, err
	}
	if set.IsSet("help") {
		fmt.Fprintf(out, "usage: %s\n", usageLine(set, name, params))
		set.PrintOptions(out)
		return nil, true, nil
	}
	return set.Args(), false, nil
}

// usageLine composes the one line that says how a command is typed.
//
// One composer, as in slsh, so that the line in the help listing and
// the line a refusal prints cannot drift apart: there is only one of
// them.  The help flag comes out on the way through -- every command has
// it and it says nothing about the command being looked at.
func usageLine(set *getopt.Set, name, params string) string {
	line := strings.TrimSpace(set.UsageLine())
	line = strings.TrimPrefix(line, name)
	line = withoutHelp(strings.TrimSpace(line))

	out := name
	if line != "" {
		out += " " + line
	}
	if params != "" {
		out += " " + params
	}
	return out
}

// withoutHelp takes the help flag out of a composed option summary.
//
// getopt bundles the short flags, so the summary of a command whose only
// flag is --help is "[-h]" and of one with two more is "[-ahl]".  Both
// shapes are handled: the whole bracket goes when h was alone in it, and
// the letter goes when it was not.
func withoutHelp(line string) string {
	if line == "[-h]" {
		return ""
	}
	if strings.HasPrefix(line, "[-") {
		end := strings.IndexByte(line, ']')
		if end > 0 {
			letters := strings.Replace(line[2:end], "h", "", 1)
			rest := strings.TrimSpace(line[end+1:])
			if letters == "" {
				return rest
			}
			out := "[-" + letters + "]"
			if rest != "" {
				out += " " + rest
			}
			return out
		}
	}
	return line
}

// usage composes the refusal a command gives when what it was handed is
// not what it takes.
//
// It ends in the same line --help would print, so that the answer to
// "you typed that wrong" is "here is how it is typed" and neither has to
// be written out twice.
func usage(name string, why ...string) error {
	c := commands[name]
	var set *getopt.Set
	params := ""
	if c != nil {
		params = c.params
		set = getopt.New()
		set.SetProgram(name)
		set.SetParameters(params)
		if c.flags != nil {
			_ = options.RegisterSet(name, c.flags(), set)
		}
	}
	line := name
	if set != nil {
		line = usageLine(set, name, params)
	}
	if len(why) > 0 && why[0] != "" {
		return fmt.Errorf("%s\nusage: %s", why[0], line)
	}
	return fmt.Errorf("usage: %s", line)
}
