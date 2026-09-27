package main

// What the avatar is acting as, what it could act as, and asking
// somebody else in.
//
//	group            the active group and the ones joined
//	group NAME|UUID  act as that one
//	invite WHO GROUP ask somebody into one of them
//
// invite is here rather than in a file of its own because it is the
// same list twice over: the group it names is resolved against the
// memberships this file already prints, and whether it is allowed at
// all is a power out of the same list.  What it does is otherwise
// nothing like activating one -- see cmdInvite -- and the two are kept
// apart in the help and in the man pages.
//
// # Why this is worth a command
//
// A parcel usually grants "create objects" to a GROUP rather than to
// individuals, so whether an avatar may build somewhere depends on
// which group it is acting as and not on who it is.  Measured on Agni,
// in Pelmar Reach, with two avatars a couple of metres apart:
//
//	hobb       Pelmar Reach at 33, 75, 2001 / acting as group 488f7e57-...
//	holt  Pelmar Reach at 31, 73, 2001
//
// Rezzing a prim as hobb worked.  As holt it failed, with the
// simulator saying "You cannot create objects here. The owner of this
// land does not allow it. Use the land tool to see land ownership." --
// which is untrue as it stands and unhelpful as a hint.  The land does
// allow it; the request simply arrived from nobody in particular.  A
// viewer remembers the active group across sessions and a headless
// login starts with none, so this is the state of an avatar slgod
// brings up unless its profile or -group names one or it has joined
// only one, and until now there was no way to fix it from the prompt.
//
// # Why the membership list had to cross the wire first
//
// sl.ActivateGroup takes a key and nothing else, and a key is not
// something anybody has: the list of groups an avatar belongs to lived
// in agent.Agent.Groups, on the daemon's side, and PresenceResponse
// carried only the active group.  A command that could take only a key
// would be a command nobody could use.  So the list travels alongside
// the active group, for the reason active_group's own comment in the
// proto gives: the simulator volunteers it and never answers a question
// about it, so a client that attached later would otherwise never hear
// it at all.
//
// # What an empty list means, and why it is not "no groups"
//
// Nothing asks for the list.  AgentGroupDataUpdate arrives of its own
// accord shortly after the handshake and again whenever the membership
// changes (see agent.Agent.Groups), which means an empty list is "not
// told yet" exactly as much as it is "belongs to none" -- the point
// agent.WaitGroups was written around.  So the empty case says both
// rather than picking one, and printing nothing at all would be the
// worst of the three: silence reads as a working command that found
// nothing.
//
// # This is not groups.go
//
// The word means two things in this package.  groups.go is the help
// headings -- what a person is looking for when they type "help" -- and
// has a group type and a groups list of its own.  This file is the
// Second Life sense, the thing an avatar joins and acts as.  Neither
// name can move: help's groups are what its own doc comment argues for,
// and this command is called what the viewer calls it.
//
// # Why the name is matched and the key is taken as given
//
// A name is matched against the joined list, case-insensitively,
// because that is the only place a name can come from -- nothing here
// can turn a word into a group key by asking the grid.  A key is sent
// as typed, without being looked for in the list first: a key is
// unambiguous, and refusing one because a list that may not have
// arrived does not mention it would make this command useless in the
// state it is most needed in.  ActivateGroup waits for the simulator to
// agree and explains a timeout as an avatar that is not a member, which
// is the same answer refusing here would have given, arriving later and
// from the grid rather than from a guess.
//
// # Acting as nobody is spelled "none"
//
// The way back out is to activate the null key, which is what a viewer
// sends for "none" on its group menu.  Typing thirty-two zeros is not
// something anybody would guess at from a listing that says "acting as
// no group", and nothing else in this shell asks for a null key, so the
// word is taken as meaning it.  The vocabulary is not new: perms reads
// "none" as the empty permission mask (permMask, carry.go).
//
// The word wins over a group that happens to be called it.  Such a
// group is still reachable by its key, which the listing prints beside
// every name, and the other way round -- a person who wanted to act as
// nobody and cleared nothing -- leaves them building on land that
// refuses them with no indication of why.

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var groupCommands = map[string]*command{
	"group": {
		params:   "[NAME|UUID|none]",
		flags:    func() any { return new(helpOnly) },
		brief:    "what this avatar is acting as, and what it could act as; land rights hang on it",
		keywords: "active group title tag land rights acting as switch groups joined list which",
		man:      "group",
		run:      cmdGroup,
	},
	"invite": {
		params:   "WHO GROUP",
		flags:    func() any { return new(helpOnly) },
		brief:    "ask somebody into one of this avatar's groups; nothing answers, so it is an asking",
		keywords: "invite ask join group member add somebody role",
		man:      "invite",
		run:      cmdInvite,
	},
}

// cmdGroup lists the groups or activates one.
//
// Both halves start from the same presence, so naming a group and
// listing them cost one call each: the list is what a name is resolved
// against and the active group is what says whether there is anything
// to do.
func cmdGroup(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	args, done, err := subOptions("group", &o, out, args)
	if err != nil || done {
		return err
	}

	p, err := sh.s.Where(ctx)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		printGroups(out, p)
		return nil
	}

	want := strings.Join(args, " ")
	id, name, err := chooseGroup(p, want)
	if err != nil {
		return err
	}
	if id == p.ActiveGroup {
		fmt.Fprintf(out, "already %s\n", actingAs(name, id))
		return nil
	}
	if err := sh.s.ActivateGroup(ctx, id, 0); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s\n", actingAs(name, id))
	return nil
}

// cmdInvite asks somebody into a group.
//
// # Why the group is said out loud every time
//
// It could have defaulted to the group this avatar is acting as, and
// that would have been shorter to type and wrong the first time
// somebody typed it after activating something else.  What this command
// does reaches a person who is not at this prompt: they get an instant
// message naming a group they did not ask about, and it cannot be taken
// back -- there is no "never mind" message, and nothing here can
// cancel an invitation that has gone.  So the group is on the line
// that was typed, where it can be read before pressing return, rather
// than in state that has to be remembered.
//
// # What it can promise, and what it cannot
//
// Nothing answers InviteGroupRequest.  So this reports what it did --
// asked -- and never that anybody was invited, let alone joined.  What
// it can check first it does: sl.InviteToGroup refuses a group this
// avatar is not in, and one whose role does not carry the power to
// invite, because both of those are answered by the grid with silence
// and would otherwise read as an invitation that went.
//
// # Why the person is read off the front
//
// whoAndRest, exactly as give reads it: a name has a space in it far
// more often than not, and so does a group's, so something has to
// decide where one ends and the other begins.  The rule is the same
// one give uses and it is the longest leading run that names somebody
// -- which leaves the rest of the line for the group, spaces and all,
// and needs no quoting anywhere.
func cmdInvite(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	args, done, err := subOptions("invite", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) == 0 {
		return usageError("invite", "somebody to invite, and which group to invite them into")
	}

	who, name, rest, err := sh.whoAndRest(ctx, out, args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return usageError("invite", "which group to invite them into; "+
			"\"group\" lists the ones this avatar has joined")
	}

	p, err := sh.s.Where(ctx)
	if err != nil {
		return err
	}
	id, group, err := chooseGroup(p, strings.Join(rest, " "))
	if err != nil {
		return err
	}
	if id.IsZero() {
		// chooseGroup reads "none" as the null key, which is how the
		// other command spells acting as nobody.  There is no such
		// group to be invited into, and the grid would say nothing
		// about it either way.
		return usageError("invite", "\"none\" is how \"group\" spells acting as nobody, "+
			"and nobody can be invited into it; name a group")
	}

	if err := sh.s.InviteToGroup(ctx, id, sl.RoleEveryone, who); err != nil {
		return err
	}
	// "asked" and not "invited": what went out is a request nothing
	// answers, and whether it reached them is theirs to see and not
	// ours.  The role is said because it is part of what they were
	// asked into and nothing else here mentions roles at all.
	fmt.Fprintf(out, "asked %s into %s, in its everyone role\n",
		name, describeGroup(group, id))
	fmt.Fprintln(out, "nothing answers an invitation, so this says what was sent and not "+
		"what became of it; they see it as an instant message and it waits for them")
	return nil
}

// actingAsNone is the one wording for having no group in force.
//
// The listing says it when nothing is marked and this command says it
// when it has just brought the state about, and the two must be the
// same words: a person reading "acting as no group" in one place and
// something else in the other has been shown two states where there is
// one.  It carries what the state costs because that is the whole
// reason anybody notices being in it -- the land refuses, and blames
// itself.
const actingAsNone = "acting as no group, which is what land that grants building to a group refuses"

// actingAs is what the avatar is acting as now, in words.
//
// The null key never appears.  It is not a group, and printing it as
// one is the mistake "where" used to make by printing a key nobody can
// read -- worse here, because the key it would print is the one that
// looks most like a fault.
func actingAs(name string, id msg.UUID) string {
	if id.IsZero() {
		return actingAsNone
	}
	return "acting as " + describeGroup(name, id)
}

// printGroups is the listing: which one is active, and what else there
// is to choose from.
//
// The mark is the word "active" in a column of its own rather than a
// star, so that the listing needs no legend under it, and the name is
// last because it is the only field with no length worth relying on.
func printGroups(out io.Writer, p *sl.Presence) {
	active := false
	for _, g := range p.Groups {
		mark := ""
		if g.ID == p.ActiveGroup {
			mark, active = "active", true
		}
		fmt.Fprintf(out, "%-7s %-36s %s\n", mark, g.ID, g.Name)
	}

	if len(p.Groups) == 0 {
		fmt.Fprintln(out, "no groups are known here: the simulator sends the list unasked "+
			"shortly after login, so this is either an avatar that has joined none or one "+
			"that has not been told yet")
	}
	switch {
	case p.ActiveGroup.IsZero():
		fmt.Fprintln(out, actingAsNone)
	case !active:
		// The active group arrives in AgentDataUpdate and the list in
		// a message of its own, so one can be here without the other.
		fmt.Fprintf(out, "acting as group %s, which nothing here can name\n", p.ActiveGroup)
	}
}

// chooseGroup is which group a word means, and what to call it.
//
// A name that matches more than one is refused with the keys rather
// than guessed at, the way detach refuses a name worn twice.  The grid
// is unlikely to ever produce it -- a group name is chosen once and the
// simulator sends what it sent -- but the alternative to refusing is
// picking, and picking wrong here is silent: the wrong group activates,
// says so, and the parcel goes on refusing to let you build for a
// reason that now looks impossible.
func chooseGroup(p *sl.Presence, want string) (msg.UUID, string, error) {
	want = strings.TrimSpace(want)
	// The word for the null key, before the list is consulted: a group
	// called "none" would otherwise take the one argument that has to
	// mean acting as nobody.  See the head of this file.
	if strings.EqualFold(want, "none") {
		return msg.UUID{}, "", nil
	}
	if id, err := msg.ParseUUID(want); err == nil {
		return id, nameOfGroup(p.Groups, id), nil
	}

	var found []sl.Group
	for _, g := range p.Groups {
		if strings.EqualFold(g.Name, want) {
			found = append(found, g)
		}
	}
	switch len(found) {
	case 1:
		return found[0].ID, found[0].Name, nil
	case 0:
		if len(p.Groups) == 0 {
			return msg.UUID{}, "", fmt.Errorf("no groups are known here, so %q matches nothing; "+
				"the list arrives on its own shortly after login, so try again in a moment, "+
				"or give the group's key", want)
		}
		return msg.UUID{}, "", fmt.Errorf("this avatar has joined no group called %q; "+
			"\"group\" lists the ones it has", want)
	}
	keys := make([]string, 0, len(found))
	for _, g := range found {
		keys = append(keys, g.ID.String())
	}
	return msg.UUID{}, "", fmt.Errorf("%d of this avatar's groups are called %q; "+
		"say which by its key: %s", len(found), want, strings.Join(keys, ", "))
}

// nameOfGroup is what the list calls a key, or nothing if it does not
// mention it.
func nameOfGroup(groups []sl.Group, id msg.UUID) string {
	for _, g := range groups {
		if g.ID == id {
			return g.Name
		}
	}
	return ""
}

// describeGroup is a group in one phrase.
//
// The key stays in the line even when there is a name for it.  The name
// is what a person recognises and the key is what everything else takes
// -- a second client, a script, this command's own argument -- and a
// group that could not be named at all still has to print as something.
func describeGroup(name string, id msg.UUID) string {
	if name == "" {
		return id.String()
	}
	return fmt.Sprintf("%s (%s)", name, id)
}
