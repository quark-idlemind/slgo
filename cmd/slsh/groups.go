package main

// Grouping the commands, because there are enough of them now that one
// list is a wall rather than an answer.
//
// # What a group is for
//
// Somebody typing "help" has a question -- how do I look at inventory,
// how do I talk to somebody, which avatar am I driving -- and the
// groups are those questions.  They are not a taxonomy of the code:
// nothing here follows which file a command lives in, and two of them
// deliberately cut across it.
//
// A command may be in several.  "cp" is an inventory operation and also
// the way an avatar that may not rez gets more objects, so it appears
// under both; looking for it in either place finds it.
//
// # What a group is not
//
// It is not where a command's own options are documented.  That is the
// command's business and it answers for itself:
//
//	COMMAND --help
//
// So "help" never describes one command in detail, and asking it to --
// "help cat" -- is answered by pointing at the command instead.  A name
// may therefore be both a group and a command without ambiguity, since
// help only ever means the group.

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
)

// group is one heading in help.
type group struct {
	name  string
	brief string

	// members are in the order a person would meet them rather than
	// alphabetical: pwd and cd before mkdir and rm, because that is the
	// order somebody learns them in.
	members []string
}

// groups are the headings, in the order help prints them: the ones most
// often wanted first, and the shell itself last.
var groups = []group{
	{
		name:  "inventory",
		brief: "folders and items: what you have, and moving it about",
		members: []string{
			"pwd", "cd", "ls", "find", "cat",
			"mkdir", "mv", "cp", "rm", "emptytrash",
		},
	},
	{
		name:  "people",
		brief: "finding people, talking to them, friendship",
		members: []string{
			"who", "lookup", "friends",
			"offer", "offers", "accept", "decline",
			"chat", "say", "im", "talk",
		},
	},
	{
		name:    "region",
		brief:   "where this avatar is and what is around it",
		members: []string{"where", "tp", "who", "look", "objects", "worn"},
	},
	{
		name:  "giving",
		brief: "handing items to somebody, and taking what is offered",
		// Every one of these is in another group too.  It earns its
		// place anyway: "how do I give somebody an object" is a real
		// question whose answer is otherwise spread between inventory
		// and people.
		members: []string{"give", "cp", "offers", "accept", "decline"},
	},
	{
		name:    "avatars",
		brief:   "which avatar you are driving, and slgod's sessions",
		members: []string{"agents", "host", "logout", "auto"},
	},
	{
		name:    "simulator",
		brief:   "what this grid supports",
		members: []string{"caps", "features", "lsl"},
	},
	{
		name:    "shell",
		brief:   "slsh itself",
		members: []string{"help", "quit", ".", "echo"},
	},
}

// findGroup returns a group by name.
func findGroup(name string) (group, bool) {
	for _, g := range groups {
		if g.name == name {
			return g, true
		}
	}
	return group{}, false
}

// groupNames lists the headings, for an error that has to suggest them.
func groupNames() []string {
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		out = append(out, g.name)
	}
	return out
}

// helpTail is the reminder that a command documents itself.  Printed
// wherever a list of commands is, since that is where somebody is when
// they want to know what one takes.
const helpTail = `"COMMAND --help" for what one command takes.`

// cmdHelp is help, with or without a group.
func cmdHelp(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	// help answers --help too.  It tells everyone else to, so being the
	// one command that did not would be a poor advertisement.
	var flags helpOnly
	args, done, err := subOptions("help", "[GROUP|all]", &flags, out, args)
	if err != nil || done {
		return err
	}

	switch {
	case len(args) == 0:
		return helpGroups(out)
	case args[0] == "all":
		return helpAll(out)
	}

	name := args[0]
	if g, ok := findGroup(name); ok {
		return helpGroup(out, g)
	}

	// Not a group.  If it is a command, say where its help actually
	// lives rather than refusing -- this is the moment somebody wants
	// to know, so it is the moment worth telling them.
	if _, ok := commands[name]; ok {
		fmt.Fprintf(out, "%s is a command, not a group.  Try:  %s --help\n", name, name)
		fmt.Fprintf(out, "Groups: %s\n", strings.Join(groupNames(), ", "))
		return nil
	}
	return fmt.Errorf("no group or command %q; groups are %s",
		name, strings.Join(groupNames(), ", "))
}

// helpGroups is the front page: the headings and nothing else.
func helpGroups(out io.Writer) error {
	fmt.Fprintf(out, "Commands are grouped.  For a group:  help GROUP\n")
	fmt.Fprintf(out, "For everything:        help all\n")
	fmt.Fprintf(out, "For one command:       COMMAND --help\n\n")
	for _, g := range groups {
		fmt.Fprintf(out, "  %-11s %-54s (%d)\n", g.name, g.brief, len(g.members))
	}
	return nil
}

// helpGroup lists one group's commands.
func helpGroup(out io.Writer, g group) error {
	fmt.Fprintf(out, "%s -- %s\n\n", g.name, g.brief)
	for _, n := range g.members {
		c, ok := commands[n]
		if !ok {
			continue
		}
		fmt.Fprintf(out, "  %-28s %s\n", c.usage, c.brief)
	}
	fmt.Fprintf(out, "\n%s\n", helpTail)
	return nil
}

// helpAll is every command, as help printed before there were groups.
func helpAll(out io.Writer) error {
	// Aliases share a command, and listing one twice under two names
	// says nothing extra.
	seen := map[*command]bool{}
	for _, n := range commandNames() {
		c := commands[n]
		if seen[c] {
			continue
		}
		seen[c] = true
		fmt.Fprintf(out, "%-28s %s\n", c.usage, c.brief)
	}
	fmt.Fprintf(out, "\nA path may be quoted, and \\/ is a slash inside a name.\n")
	fmt.Fprintf(out, "Output redirects with > and >>, and \". file\" runs a file of commands.\n")
	fmt.Fprintf(out, "%s\n", helpTail)
	return nil
}

// ungrouped is every command in no group, which is what a test asks
// for: a command nobody can find is the same as one that is not there.
func ungrouped() []string {
	in := map[string]bool{}
	for _, g := range groups {
		for _, n := range g.members {
			in[n] = true
		}
	}
	var out []string
	seen := map[*command]bool{}
	for _, n := range commandNames() {
		if in[n] {
			continue
		}
		// An alias is findable through the name it shares.
		c := commands[n]
		if seen[c] {
			continue
		}
		if aliasOf(c, in) {
			continue
		}
		seen[c] = true
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// aliasOf reports whether some grouped name refers to this same
// command.
func aliasOf(c *command, in map[string]bool) bool {
	for n := range in {
		if commands[n] == c {
			return true
		}
	}
	return false
}
