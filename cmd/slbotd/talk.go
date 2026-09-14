package main

// Talking, and the things that arrive addressed to a person and wait
// for an answer.
//
// An offer is not state a session can be asked about again.  Every one
// of these -- an item handed over, a teleport, a friendship, a group
// invitation -- is an instant message carrying a transaction id, and
// that id is the only thing an acceptance can quote.  The session keeps
// them for exactly that reason, and these commands are the way to see
// them and answer them from somewhere with no viewer in it.

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var talkCommands = map[string]*command{
	"say": {
		params: "TEXT ...",
		flags:  func() any { return new(sayFlags) },
		brief:  "say something out loud on a channel",
		group:  groupTalking,
		run:    cmdSay,
	},
	"im": {
		params: "NAME|UUID TEXT ...",
		flags:  func() any { return new(helpOnly) },
		brief:  "send somebody an instant message",
		group:  groupTalking,
		run:    cmdIM,
	},
	"offers": {
		flags: func() any { return new(helpOnly) },
		brief: "what is waiting for an answer: items, teleports, friendships, invitations",
		group: groupTalking,
		run:   cmdOffers,
	},
	"accept": {
		params: "N",
		flags:  func() any { return new(helpOnly) },
		brief:  "take up one of the waiting offers, by its number",
		group:  groupTalking,
		run:    cmdAccept,
	},
	"decline": {
		params: "N",
		flags:  func() any { return new(helpOnly) },
		brief:  "refuse one of the waiting offers, by its number",
		group:  groupTalking,
		run:    cmdDecline,
	},
}

// sayFlags is what say was asked for.
type sayFlags struct {
	Channel int  `getopt:"--channel -c=N  the channel to say it on [0]"`
	Shout   bool `getopt:"--shout         shout rather than say"`
	Whisper bool `getopt:"--whisper       whisper rather than say"`
	Help    bool `getopt:"--help -h       show what this command takes"`
}

// Chat types, as the simulator numbers them.  Only the three a person
// chooses between are here; the rest are what scripts and the region
// use and are no business of a command line.
const (
	chatWhisper = 0
	chatNormal  = 1
	chatShout   = 2
)

func cmdSay(ctx context.Context, r *req, out io.Writer, args []string) error {
	var o sayFlags
	rest, done, err := subOptions("say", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(rest) == 0 {
		return usage("say", "something to say")
	}
	if o.Shout && o.Whisper {
		return fmt.Errorf("--shout and --whisper are two different volumes; pick one")
	}
	s, err := r.bot.Need()
	if err != nil {
		return err
	}
	text := strings.Join(rest, " ")
	kind := uint8(chatNormal)
	switch {
	case o.Shout:
		kind = chatShout
	case o.Whisper:
		kind = chatWhisper
	}
	if err := s.SayAs(ctx, text, int32(o.Channel), kind); err != nil {
		return err
	}
	if o.Channel == 0 {
		fmt.Fprintf(out, "said: %s\n", text)
	} else {
		fmt.Fprintf(out, "said on channel %d: %s\n", o.Channel, text)
	}
	return nil
}

func cmdIM(ctx context.Context, r *req, out io.Writer, args []string) error {
	rest, done, err := subOptions("im", new(helpOnly), out, args)
	if err != nil || done {
		return err
	}
	if len(rest) < 2 {
		return usage("im", "somebody to send to, and something to send")
	}
	s, err := r.bot.Need()
	if err != nil {
		return err
	}
	to, err := personNamed(ctx, s, rest[0])
	if err != nil {
		return err
	}
	text := strings.Join(rest[1:], " ")
	if err := s.SendIM(ctx, to, text); err != nil {
		return err
	}
	name := s.NameOr(to)
	if name == "" {
		name = to.String()
	}
	fmt.Fprintf(out, "sent to %s: %s\n", name, text)
	return nil
}

// waiting is one thing addressed to the person and not yet answered.
//
// One type over four different offers, because the commands that list
// and answer them do the same thing to each: number them, print them,
// and hand the number back.  What differs is only the call that answers,
// which is what accept and decline hold.
type waiting struct {
	kind string
	at   time.Time
	what string

	accept  func(context.Context) error
	decline func(context.Context) error
}

// waitingFor is everything this session is holding an answer for,
// oldest first.
//
// The order is the order things arrived in, across all four kinds,
// because that is the order a person saw them in.  It is also what
// makes the numbers mean anything: they are positions in this list, so
// a listing and the accept that follows it must sort the same way.
func waitingFor(s *sl.Session) []waiting {
	var out []waiting

	for _, o := range s.InventoryOffers() {
		out = append(out, waiting{
			kind: "item",
			at:   o.At,
			what: fmt.Sprintf("%s offers %q", o.FromName, o.Name),
			// A zero folder is what a viewer sends when somebody
			// clicks Accept rather than dragging it somewhere: the
			// grid files it under whatever kind of thing it is.
			accept:  func(ctx context.Context) error { return o.Accept(ctx, msg.UUID{}) },
			decline: o.Decline,
		})
	}
	for _, o := range s.Offers() {
		out = append(out, waiting{
			kind:    "friendship",
			at:      o.At,
			what:    o.String(),
			accept:  o.Accept,
			decline: o.Decline,
		})
	}
	for _, l := range s.Lures() {
		out = append(out, waiting{
			kind:    "teleport",
			at:      l.At,
			what:    l.String(),
			accept:  func(ctx context.Context) error { return s.AcceptLure(ctx, l) },
			decline: func(ctx context.Context) error { return s.DeclineLure(ctx, l) },
		})
	}
	for _, i := range s.Invitations() {
		out = append(out, waiting{
			kind:    "group",
			at:      i.At,
			what:    i.String(),
			accept:  func(ctx context.Context) error { return s.AcceptInvitation(ctx, i) },
			decline: func(ctx context.Context) error { return s.DeclineInvitation(ctx, i) },
		})
	}

	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].at.Before(out[j-1].at); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func cmdOffers(ctx context.Context, r *req, out io.Writer, args []string) error {
	if _, done, err := subOptions("offers", new(helpOnly), out, args); err != nil || done {
		return err
	}
	s, err := r.bot.Need()
	if err != nil {
		return err
	}
	ws := waitingFor(s)
	if len(ws) == 0 {
		fmt.Fprintln(out, "nothing is waiting for an answer")
		return nil
	}
	for i, w := range ws {
		fmt.Fprintf(out, "%d  %-10s %s\n", i+1, w.kind, w.what)
	}
	fmt.Fprintf(out, "%saccept N or %sdecline N\n", r.d.cfg.Prefix, r.d.cfg.Prefix)
	return nil
}

func cmdAccept(ctx context.Context, r *req, out io.Writer, args []string) error {
	return answerOffer(ctx, r, out, args, "accept")
}

func cmdDecline(ctx context.Context, r *req, out io.Writer, args []string) error {
	return answerOffer(ctx, r, out, args, "decline")
}

// answerOffer is accept and decline, which differ in one call.
//
// By number and never by name.  A name would have to be matched against
// what the giver called the item, which two offers may share, and
// answering the wrong one of two cannot be undone -- the transaction is
// spent and the other offer is still sitting there looking identical.
// The number comes from a listing the sender has just read.
func answerOffer(ctx context.Context, r *req, out io.Writer, args []string, what string) error {
	rest, done, err := subOptions(what, new(helpOnly), out, args)
	if err != nil || done {
		return err
	}
	if len(rest) != 1 {
		return usage(what, "the number of one waiting offer")
	}
	n, err := strconv.Atoi(rest[0])
	if err != nil {
		return usage(what, fmt.Sprintf("%q is not a number; %soffers lists them", rest[0], r.d.cfg.Prefix))
	}
	s, err := r.bot.Need()
	if err != nil {
		return err
	}
	ws := waitingFor(s)
	if n < 1 || n > len(ws) {
		if len(ws) == 0 {
			return fmt.Errorf("nothing is waiting for an answer")
		}
		return fmt.Errorf("there are %d waiting; %d is not one of them", len(ws), n)
	}
	w := ws[n-1]
	answer := w.accept
	if what == "decline" {
		answer = w.decline
	}
	if err := answer(ctx); err != nil {
		return err
	}
	said := "accepted"
	if what == "decline" {
		said = "declined"
	}
	fmt.Fprintf(out, "%s: %s\n", said, w.what)
	return nil
}
