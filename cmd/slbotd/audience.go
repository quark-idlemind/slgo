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
	"strings"

	"github.com/quark-idlemind/slgo/msg"
)

// Anyone is the entry in the configuration that means exactly that.
//
// Spelt as a word nobody is called rather than as an empty list, so
// that "this avatar talks to everybody" is something somebody WROTE
// and not something that happened because a line was missing.
const Anyone = "*"

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
	// person, and Turns how many things have been said in it.  A rule
	// that lets a stranger open a conversation but not go on for ever
	// needs both, and neither can be recovered from the message.
	Known bool
	Turns int
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
	anyone := false
	for _, who := range cfg.Chat {
		if who == Anyone {
			anyone = true
			continue
		}
		if id, err := msg.ParseUUID(who); err == nil {
			ids[id] = true
			continue
		}
		names[strings.ToLower(who)] = true
	}

	return func(ctx context.Context, a *Approach) Verdict {
		switch {
		case anyone:
			return Verdict{true, "the list says anyone"}
		case !a.From.IsZero() && ids[a.From]:
			return Verdict{true, "named in the list by id"}
		case a.Name != "" && names[strings.ToLower(strings.TrimSpace(a.Name))]:
			return Verdict{true, "named in the list"}
		}
		return Verdict{false, "not in the chat list"}
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
