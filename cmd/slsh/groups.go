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
//	COMMAND --help    what it takes
//	man COMMAND       what it is for
//
// So "help" never describes one command in detail, and asking it to --
// "help cat" -- is answered by pointing at those two instead.  A name
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

	// members are written in the order a person would meet them --
	// pwd and cd before mkdir and rm -- and are LISTED in alphabetical
	// order, which is not the same thing and is deliberate.
	//
	// A teaching order is the right one for a page somebody reads
	// through once.  This is not that: help is opened by somebody who
	// knows what they want and is looking for its name, and a
	// twenty-six line list in an order only its author can predict is
	// one they have to read all of.  So the order that can be guessed
	// wins over the order that has to be learned.  See sortedNames.
	members []string
}

// groups are the headings.  They are listed alphabetically wherever
// they are listed, for members' reason above; the order here is the
// order they were written in and decides nothing.
var groups = []group{
	{
		name:  "inventory",
		brief: "folders and items: what you have, and moving it about",
		members: []string{
			"pwd", "cd", "ls", "find", "cat", "get", "put",
			"mkdir", "mv", "cp", "rm", "new", "save", "emptytrash",
		},
	},
	{
		name:  "waiting",
		brief: "things that want an answer, and how to give one",
		// offers, accept and decline are here too: they are the older
		// way to the same two kinds, and a person looking for "how do
		// I say yes to this" should find both in one place.
		members: []string{"waiting", "answer", "no", "ignore", "offers", "accept", "decline"},
	},
	{
		name:  "people",
		brief: "finding people, talking to them, friendship",
		// invite is here as well as under region.  What it does is done
		// to a person -- they get an instant message and decide -- and
		// somebody looking for "how do I get somebody into my group"
		// is asking about the person; what it is FOR is land rights,
		// which is the question asked next to group.
		members: []string{
			"who", "lookup", "profile", "friends",
			"offer", "offers", "accept", "decline",
			"invite", "chat", "say", "im", "talk", "lure",
		},
	},
	{
		name:  "region",
		brief: "where this avatar is and what is around it",
		// group is here rather than under people.  It is a social thing
		// in Second Life and a land permission in practice: what it
		// decides is whether this parcel will let the avatar rez, so
		// the question it answers -- "why has building stopped working
		// here" -- is asked next to where and rez and nowhere near
		// friends.
		//
		// take and place are listed together because they are one
		// question asked in two directions, and move sits with the
		// other things done to an object that is already standing
		// there rather than beside them.
		//
		// landmark is beside tp because the two are one question asked
		// in two ways: tp is a place said in numbers and landmark is
		// the same place said by name, and the name is the half that
		// survives being written down.  It is also an inventory
		// command, and it is not listed under inventory: what a person
		// wants from it is to be somewhere else.
		//
		// sit and stand are here beside tp and touch rather than in a
		// group of their own: a sit MOVES the avatar, up to about ten
		// metres, so the question it answers is tp's -- how do I get
		// this avatar over there -- and the thing it names is touch's.
		// "unsit" is stand under another name and is not listed, since
		// a listing that showed one command twice would say nothing
		// extra about either.
		//
		// walk, face and halt follow them for the same reason: walking
		// is the other way of getting this avatar over there, and the
		// short one.
		// maturity is here because the question it answers is asked
		// about a place: an avatar refused entry to a region is told
		// its maturity rating is wrong, and this is where somebody
		// goes next.  What it sets belongs to the account rather than
		// to the region, which is why it is also under avatars.
		members: []string{"where", "parcel", "group", "invite", "maturity", "tp", "landmark", "sit", "stand", "walk", "face", "halt", "who", "look", "map", "regions", "neighbours",
			"objects", "worn", "wear",
			"detach", "dress", "move", "dump", "rez", "reform", "touch", "texture",
			"take", "place", "perms", "drop", "fetch", "start", "stop", "link", "unlink"},
	},
	{
		name:  "giving",
		brief: "handing items to somebody, and taking what is offered",
		// Every one of these is in another group too.  It earns its
		// place anyway: "how do I give somebody an object" is a real
		// question whose answer is otherwise spread between inventory
		// and people.
		members: []string{"give", "cp", "offers", "accept", "decline", "perms"},
	},
	{
		name:  "avatars",
		brief: "the avatars slgod holds, and how their sessions are doing",
		// viewer is here rather than under shell: what it reports is
		// the daemon's, not slsh's -- where slgod serves logins and
		// whether one of its sessions has been taken over -- and the
		// question it answers, "how do I get eyes on this avatar", is
		// asked next to status and watch.
		members: []string{"agents", "login", "logout", "auto", "status", "watch", "viewer", "maturity"},
	},
	{
		name:    "simulator",
		brief:   "what this grid supports",
		members: []string{"caps", "features", "lsl"},
	},
	{
		name:  "shell",
		brief: "slsh itself",
		// set is here rather than under avatars: what it changes is
		// this shell's own settings -- which map it draws, which key
		// leaves chat -- and the question it answers, "how do I stop
		// having to say this every time", is asked about slsh and not
		// about any avatar.
		members: []string{"help", "man", "how", "set", "quit", ".", "echo"},
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
	return sortedNames(out)
}

// sortedNames is any list of names help prints, in the one order a
// reader can predict.
//
// Alphabetical, and everywhere: the groups, the commands in a group,
// and the whole list.  Every one of these is read by somebody looking
// for a name they already have in mind, and a list they cannot guess
// the shape of is a list they have to read from the top.  The lists
// here were curated once -- most-wanted first, and the order somebody
// learns them in -- and it turns out that reads as no order at all to
// anybody who did not write it.
//
// The curation is still in the source, where it costs nothing and
// documents what belongs together; it is only the printing that is
// sorted.
func sortedNames(names []string) []string {
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })
	return names
}

// helpTail is the reminder that a command documents itself.  Printed
// wherever a list of commands is, since that is where somebody is when
// they want to know what one takes.
const helpTail = `"COMMAND --help" for what one command takes, "man COMMAND" for what it is for.`

// cmdHelp is help, with or without a group.
func cmdHelp(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	// help answers --help too.  It tells everyone else to, so being the
	// one command that did not would be a poor advertisement.
	var flags helpOnly
	args, done, err := subOptions("help", &flags, out, args)
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
		fmt.Fprintf(out, "%s is a command, not a group.  Try:  %s --help, or man %s\n", name, name, name)
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
	fmt.Fprintf(out, "For one command:       COMMAND --help, or man COMMAND\n\n")
	for _, name := range groupNames() {
		g, ok := findGroup(name)
		if !ok {
			continue
		}
		fmt.Fprintf(out, "  %-11s %-54s (%d)\n", g.name, g.brief, len(g.members))
	}
	return nil
}

// helpGroup lists one group's commands.
func helpGroup(out io.Writer, g group) error {
	fmt.Fprintf(out, "%s -- %s\n\n", g.name, g.brief)
	for _, n := range sortedNames(append([]string(nil), g.members...)) {
		c, ok := commands[n]
		if !ok {
			continue
		}
		listCommand(out, "  ", n, c)
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
		listCommand(out, "", n, c)
	}
	fmt.Fprintf(out, "\nA path may be quoted, and \\/ is a slash inside a name.\n")
	fmt.Fprintf(out, "Output redirects with > and >>, and \". file\" runs a file of commands.\n")
	fmt.Fprintf(out, "%s\n", helpTail)
	return nil
}

// listCommand prints one command in a listing: how it is typed, and
// what it is for.
//
// The usage line is derived now (options.go), which made some of them
// far longer than the hand-written strings they replaced -- perms names
// four flags where its old line named three, and put names eight.  A
// line that does not fit the column takes one of its own and the
// description goes underneath, rather than pushing every description on
// the page out to where the longest line ends.
func listCommand(out io.Writer, indent, name string, c *command) {
	const col = 30
	line := c.usage(name)
	if len(line) > col {
		fmt.Fprintf(out, "%s%s\n%s%*s%s\n", indent, line, indent, col+1, "", c.brief)
		return
	}
	fmt.Fprintf(out, "%s%-*s %s\n", indent, col, line, c.brief)
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
