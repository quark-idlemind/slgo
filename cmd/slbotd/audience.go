package main

// Who an avatar will hold a conversation with.
//
// This is a function and not a lookup at the call site, deliberately.
// The list in the configuration is the FIRST answer to the question
// rather than the last one: whether to answer somebody is the kind of
// decision that will want to weigh who they are, what they said, how
// often they have said it today, whether this avatar is in the middle
// of something, and whether the region is one to be talking in at all.
// Every one of those wants to arrive here, at one decision with one
// place to change it, rather than as another condition bolted onto the
// message handler -- which is where such conditions go when there is no
// obvious place for them, and where they are impossible to find
// afterwards.
//
// So the message handler asks one question and gets one answer, and
// what is behind it can grow without anything above it moving.

import (
	"context"
	"fmt"
	"strings"

	"github.com/quark-idlemind/slgo/msg"
)

// Anyone is the entry in the configuration that means exactly that.
//
// Spelt as a word nobody is called rather than as an empty list, so
// that "this avatar talks to everybody" is something somebody WROTE
// and not something that happened because a line was missing.
const Anyone = "*"

// Not marks an entry in the chat list as somebody NOT to talk to.
//
//	chat = *
//	chat = !Quark Idlemind
//
// A refusal always beats a permission, whatever order they are written
// in and however the permission was granted.  That is the only rule
// that makes "everybody except" sayable at all, and it is the rule
// somebody writing a "!" in front of a name plainly means.
const Not = "!"

// Approach is somebody saying something to an avatar that is not a
// command: everything known at the moment the decision is made.
//
// More than any current rule reads.  The fields are what a better rule
// would want -- and a struct that has to grow before it can be
// consulted about anything new is a struct that gets bypassed instead.
type Approach struct {
	// Avatar is the profile the message arrived at, and AvatarName
	// what that avatar is called on the grid.
	Avatar     string
	AvatarName string

	// From and Name are who said it.
	From msg.UUID
	Name string

	// Text is what they said.
	Text string

	// Trusted is whether this person may also send commands.  It is
	// NOT the same question as whether the avatar will talk to them,
	// and the two lists are kept apart on purpose: driving an avatar
	// and being spoken to by one are different powers, and somebody
	// may reasonably have either without the other.
	Trusted bool

	// Known is whether there is already a conversation with this
	// person, and Turns how many things have been said in it -- ALL of
	// them, including those long since folded into the summary.  A rule
	// that lets a stranger open a conversation but not go on for ever
	// needs both, and neither can be recovered from the message.
	//
	// Everything ever said and not what is still held word for word,
	// because the second resets at every compaction: a bound written
	// against it would bound nothing.
	Known bool
	Turns int

	// Recent is how many things have been said since the two last
	// rested -- see Conversation.Recent and chat-own-rest.  What bounds
	// two models talking, where Turns would bound them for good.
	Recent int
}

// Verdict is the answer, and why.
//
// The reason is not decoration.  An avatar that declines to speak does
// so silently -- that is what declining to speak is -- so the line in
// the log is the only evidence the decision happened at all, and "no"
// with nothing after it is the hardest kind of fault to look into.
type Verdict struct {
	Talk bool
	Why  string
}

// Audience decides whether an avatar holds a conversation with
// somebody.  See the head of this file for why it is a function.
type Audience func(ctx context.Context, a *Approach) Verdict

// listAudience is the rule the configuration describes: a list of
// people, or the word that means everybody.
//
// Names are matched without regard to case and ids exactly, the same
// way the trusted list is matched, so that one file reads consistently
// whichever list a line is in.
func listAudience(cfg Config) Audience {
	ids := map[msg.UUID]bool{}
	names := map[string]bool{}
	noIDs := map[msg.UUID]bool{}
	noNames := map[string]bool{}
	anyone := false

	for _, who := range cfg.Chat {
		deny := strings.HasPrefix(who, Not)
		who = strings.TrimSpace(strings.TrimPrefix(who, Not))
		switch {
		case who == Anyone && !deny:
			anyone = true
		case who == "":
			// A bare "!" says nothing about anybody.
		default:
			id, err := msg.ParseUUID(who)
			switch {
			case err == nil && deny:
				noIDs[id] = true
			case err == nil:
				ids[id] = true
			case deny:
				noNames[strings.ToLower(who)] = true
			default:
				names[strings.ToLower(who)] = true
			}
		}
	}

	return func(ctx context.Context, a *Approach) Verdict {
		name := strings.ToLower(strings.TrimSpace(a.Name))

		// Refused first, because a refusal beats a permission.  An
		// entry written with "!" is the one thing in the list somebody
		// went out of their way to say.
		switch {
		case !a.From.IsZero() && noIDs[a.From]:
			return Verdict{false, "refused in the list by id"}
		case name != "" && noNames[name]:
			return Verdict{false, "refused in the list by name"}
		}

		switch {
		case !a.From.IsZero() && ids[a.From]:
			return Verdict{true, "named in the list by id"}
		case name != "" && names[name]:
			return Verdict{true, "named in the list"}
		case anyone:
			return Verdict{true, "the list says anyone"}
		}
		return Verdict{false, "not in the chat list"}
	}
}

// ownAvatarsBounded lets two avatars this daemon drives talk to each
// other, and makes the conversation end.
//
// A flat refusal was the first answer here and it was the wrong one.
// Two of these talking is not a malfunction -- what actually happened
// was a dull but perfectly ordinary conversation -- and an operator who
// wants them not to has a way to say so now, by writing the name with a
// "!" in front of it.  What is wrong with it is only that it does not
// stop: a reply from one is an ordinary remark to the other, neither is
// answering ITSELF, and neither will ever be the one to get bored.
//
// So it is bounded rather than banned.  They may say a few things to
// each other and then one of them stops answering, which is what ends
// it -- there is no other end available, since the far side is as
// tireless as this one.
//
// Measured before there was any bound: with "chat = *" on three
// avatars, ONE message typed by hand from one of them to another ran to
// 26 exchanges in ninety seconds on the live grid, and was still going
// when it was stopped by hand.  Starting it took a person; stopping it
// was never going to happen on its own.
//
// The count is of everything ever said in that conversation, folded
// turns included.  Counting what is still held word for word would
// reset the bound at every compaction, which is to say it would bound
// nothing at all.
func ownAvatarsBounded(d *daemon, limit int, next Audience) Audience {
	return func(ctx context.Context, a *Approach) Verdict {
		who, ours := d.AvatarFor(a.From)
		what := "which this daemon drives as well"
		if !ours {
			if !d.cfg.ChatBot(a.From, a.Name) {
				return next(ctx, a)
			}
			who, what = a.Name, "which another program's model drives"
		}
		if limit <= 0 {
			return Verdict{false, "that is " + who + ", " + what}
		}
		if a.Recent >= limit {
			rest := "and will not until they have said nothing for " + d.cfg.ChatOwnRest.String()
			if d.cfg.ChatOwnRest <= 0 {
				rest = "and chat-own-rest is 0, so not again"
			}
			return Verdict{false, fmt.Sprintf(
				"that is %s, %s, and they have said %d things to each other already, %s",
				who, what, a.Recent, rest)}
		}
		return next(ctx, a)
	}
}

// silentAudience is what an avatar with no model behind it uses.
//
// A daemon with no llm configured should not be deciding who it would
// talk to if it could, and this says so in one place rather than
// leaving every caller to check whether chat is switched on.
func silentAudience() Audience {
	return func(ctx context.Context, a *Approach) Verdict {
		return Verdict{false, "no model is configured"}
	}
}
