package main

// Conferences, from the shell.
//
//	conference                        the conferences this shell knows of, numbered
//	conference start WHO, WHO ...     start one
//	conference from-im WHO, WHO ...   turn the message with the first WHO into one
//	conference add N WHO, WHO ...     invite people into one
//	conference say N TEXT             say something in it
//	conference join N                 accept an invitation
//	conference leave N                leave it, or refuse an invitation
//
// It is a command of its own and not a word after "im": a conference has
// several people, so people are separated by commas and each is read as
// "im" reads one, and it has no name to type, every one this avatar starts
// being called the same, so it is picked by the number in the listing.
// There is no eject, because the viewer has none.
//
// Nothing joins on its own: an invitation is printed once, with how to
// join.  What the viewer does and what is not known: doc/conference.md

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var conferenceCommands = map[string]*command{
	"conference": {
		params:   "[start WHO, WHO ... | from-im WHO, WHO ... | add N WHO, WHO ... | say N TEXT | join N | leave N]",
		flags:    func() any { return new(helpOnly) },
		brief:    "conferences: instant messages with several people; start, add to, say, join, leave",
		keywords: "conference multi person group instant message chat several people add invite ad hoc talk together session",
		man:      "conference",
		run:      cmdConference,
	},
}

func cmdConference(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	args, done, err := subOptions("conference", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) == 0 {
		sh.listConferences(ctx, out)
		return nil
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "start", "from-im":
		return sh.conferenceStart(ctx, out, verb, rest)
	case "add":
		return sh.conferenceAdd(ctx, out, rest)
	case "say":
		return sh.conferenceSay(ctx, out, rest)
	case "join":
		return sh.conferenceJoin(ctx, out, rest)
	case "leave":
		return sh.conferenceLeave(ctx, out, rest)
	}
	return usageError("conference", fmt.Sprintf("%q is not one of start, from-im, add, say, join, leave", verb))
}

// conferenceWho reads people separated by commas, each as "im" reads one:
// a key, a number from the last listing, or a name.
func (sh *Shell) conferenceWho(ctx context.Context, out io.Writer, args []string) ([]msg.UUID, []string, error) {
	var ids []msg.UUID
	var names []string
	for _, piece := range strings.Split(strings.Join(args, " "), ",") {
		piece = strings.TrimSpace(piece)
		if piece == "" {
			return nil, nil, fmt.Errorf("a name is missing between the commas; separate people with commas, " +
				"\"conference start Example Resident, Another Resident\"")
		}
		id, name, err := sh.who(ctx, out, piece)
		if err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
		names = append(names, name)
	}
	return ids, names, nil
}

func (sh *Shell) conferenceStart(ctx context.Context, out io.Writer, verb string, args []string) error {
	if len(args) == 0 {
		return usageError("conference", fmt.Sprintf("who to put in it, separated by commas: "+
			"\"conference %s Example Resident, Another Resident\"", verb))
	}
	ids, names, err := sh.conferenceWho(ctx, out, args)
	if err != nil {
		return err
	}
	if len(ids) < 2 {
		return usageError("conference", "a conference needs two people or more; "+
			"\"im\" is for one")
	}
	known := map[msg.UUID]bool{}
	for _, c := range sh.s.Conferences() {
		known[c.ID] = true
	}
	var id msg.UUID
	if verb == "from-im" {
		id, err = sh.s.ConferenceFromIM(ctx, ids[0], ids[1:]...)
	} else {
		id, err = sh.s.StartConference(ctx, ids...)
	}
	if err != nil {
		return err
	}
	c, n, _ := sh.conferenceByID(id)
	sh.invites.clear(id)
	if verb == "from-im" {
		fmt.Fprintf(out, "left the message with %s\n", names[0])
	}
	if known[id] {
		fmt.Fprintf(out, "already in %s\n", sh.conferenceLabel(c, n))
	} else {
		fmt.Fprintf(out, "started %s\n", sh.conferenceLabel(c, n))
	}
	sh.printPeople(ctx, out, c)
	return nil
}

func (sh *Shell) conferenceAdd(ctx context.Context, out io.Writer, args []string) error {
	if len(args) < 2 {
		return usageError("conference", "which conference, and who to add, separated by commas: "+
			"\"conference add 1 Third Resident\"")
	}
	c, n, err := sh.pickConference(args[0])
	if err != nil {
		return err
	}
	ids, names, err := sh.conferenceWho(ctx, out, args[1:])
	if err != nil {
		return err
	}
	if err := sh.s.AddToConference(ctx, c.ID, ids...); err != nil {
		if errors.Is(err, sl.ErrNotInConference) {
			return sh.notJoined(c, n)
		}
		return err
	}
	fmt.Fprintf(out, "invited %s into %s\n", strings.Join(names, ", "), sh.conferenceLabel(c, n))
	fmt.Fprintln(out, "the grid does not answer an invitation; it says when somebody comes in")
	return nil
}

func (sh *Shell) conferenceSay(ctx context.Context, out io.Writer, args []string) error {
	if len(args) == 0 {
		return usageError("conference", "which conference to speak in")
	}
	c, n, err := sh.pickConference(args[0])
	if err != nil {
		return err
	}
	if len(args) == 1 {
		return usageError("conference", "what to say in it")
	}
	text := strings.Join(args[1:], " ")
	if err := sh.s.SayToConference(ctx, c.ID, text); err != nil {
		if errors.Is(err, sl.ErrNotInConference) {
			return sh.notJoined(c, n)
		}
		return err
	}
	sh.printf("%s > %s: %s", stamp(), sh.conferenceLabel(c, n), text)
	return nil
}

func (sh *Shell) conferenceJoin(ctx context.Context, out io.Writer, args []string) error {
	if len(args) != 1 {
		return usageError("conference", "which invitation to accept")
	}
	c, n, err := sh.pickConference(args[0])
	if err != nil {
		return err
	}
	if err := sh.s.AcceptConference(ctx, c.ID); err != nil {
		if errors.Is(err, sl.ErrNotInvited) {
			return fmt.Errorf("nobody has invited this avatar into %s", sh.conferenceLabel(c, n))
		}
		return err
	}
	sh.invites.clear(c.ID)
	c, n, _ = sh.conferenceByID(c.ID)
	fmt.Fprintf(out, "joined %s\n", sh.conferenceLabel(c, n))
	sh.printPeople(ctx, out, c)
	return nil
}

func (sh *Shell) conferenceLeave(ctx context.Context, out io.Writer, args []string) error {
	if len(args) != 1 {
		return usageError("conference", "which conference to leave")
	}
	c, n, err := sh.pickConference(args[0])
	if err != nil {
		return err
	}
	if err := sh.s.LeaveConference(ctx, c.ID); err != nil {
		return err
	}
	sh.invites.clear(c.ID)
	fmt.Fprintf(out, "left %s\n", sh.conferenceLabel(c, n))
	fmt.Fprintln(out, "nothing answers a leave, so this says what was sent and not what became of it")
	return nil
}

func (sh *Shell) notJoined(c sl.Conference, n int) error {
	if c.Invited {
		return fmt.Errorf("this avatar has not joined %s; \"conference join %d\" accepts the invitation",
			sh.conferenceLabel(c, n), n)
	}
	return fmt.Errorf("this avatar is not in %s", sh.conferenceLabel(c, n))
}

// pickConference is the conference a number of the listing, or a key,
// names.
func (sh *Shell) pickConference(arg string) (sl.Conference, int, error) {
	cs := sh.s.Conferences()
	if n, err := strconv.Atoi(arg); err == nil {
		if n < 1 || n > len(cs) {
			return sl.Conference{}, 0, fmt.Errorf("there is no conference %d; \"conference\" lists them", n)
		}
		return cs[n-1], n, nil
	}
	if id, err := msg.ParseUUID(arg); err == nil {
		if c, n, ok := sh.conferenceByID(id); ok {
			return c, n, nil
		}
		return sl.Conference{}, 0, fmt.Errorf("no conference has the key %s; \"conference\" lists them", arg)
	}
	return sl.Conference{}, 0, fmt.Errorf("%q is not the number of a conference; \"conference\" lists them", arg)
}

// conferenceByID is a conference and its number in the listing.
func (sh *Shell) conferenceByID(id msg.UUID) (sl.Conference, int, bool) {
	for i, c := range sh.s.Conferences() {
		if c.ID == id {
			return c, i + 1, true
		}
	}
	return sl.Conference{ID: id}, 0, false
}

// conferenceLabel is a conference as a line names it: behind its label,
// because its name is whoever's who started it, and with its number,
// because every one this avatar starts is called the same.
func (sh *Shell) conferenceLabel(c sl.Conference, n int) string {
	name := c.Name
	if name == "" {
		name = "conference " + c.ID.String()[:8]
	}
	if n > 0 {
		name = fmt.Sprintf("%s #%d", name, n)
	}
	return sl.SenderConference.Label(name)
}

// askConferencePeople asks for the names of people a conference lists
// that the session has not heard, and waits a moment for them: the
// grid lists people by key.
func (sh *Shell) askConferencePeople(ctx context.Context, cs ...sl.Conference) {
	var ids []msg.UUID
	for _, c := range cs {
		for _, id := range append(append([]msg.UUID(nil), c.Members...), c.Guests...) {
			if id != sh.s.Me() {
				ids = append(ids, id)
			}
		}
	}
	sh.s.Names(ctx, ids, 3*time.Second)
}

// conferencePeople is who is in a conference or was invited into it,
// other than this avatar, by name.
func (sh *Shell) conferencePeople(c sl.Conference) []string {
	seen := map[msg.UUID]bool{sh.s.Me(): true}
	var names []string
	for _, ids := range [][]msg.UUID{c.Members, c.Guests} {
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				names = append(names, sl.SenderPerson.Label(sh.s.NameOr(id)))
			}
		}
	}
	sort.Strings(names)
	return names
}

func (sh *Shell) printPeople(ctx context.Context, out io.Writer, c sl.Conference) {
	sh.askConferencePeople(ctx, c)
	if names := sh.conferencePeople(c); len(names) > 0 {
		fmt.Fprintf(out, "with %s\n", strings.Join(names, ", "))
	}
}

func (sh *Shell) listConferences(ctx context.Context, out io.Writer) {
	cs := sh.s.Conferences()
	if len(cs) == 0 {
		fmt.Fprintln(out, "no conferences; \"conference start WHO, WHO\" starts one")
		return
	}
	sh.askConferencePeople(ctx, cs...)
	for i, c := range cs {
		state := "left"
		switch {
		case c.Joined:
			state = "joined"
		case c.Invited:
			state = "invited"
		}
		line := fmt.Sprintf("%2d  %-7s %s", i+1, state, sh.conferenceLabel(c, 0))
		if names := sh.conferencePeople(c); len(names) > 0 {
			line += "  with " + strings.Join(names, ", ")
		}
		fmt.Fprintf(out, "%s  %s-…\n", line, c.ID.String()[:8])
	}
}

// heardConference prints one thing that happened in a conference.  It
// runs where heardGroupChat does, on the goroutine that delivers
// everything, and prints a conference and a speaker through
// Sender.Label and never bare.
func (sh *Shell) heardConference(g *sl.GroupChat) {
	c, n, _ := sh.conferenceByID(g.Group)
	if c.Name == "" {
		c.Name = g.GroupName
	}
	label := sh.conferenceLabel(c, n)
	who := g.FromName
	if who == "" {
		who = sh.s.NameOr(g.From)
	}

	switch g.Kind {
	case sl.GroupChatSaid, sl.GroupChatInvited:
		switch {
		case g.Mine:
			sh.printf("%s > %s: %s", stamp(), label, g.Text)
		case g.From == sh.s.Me():
		case g.Text != "":
			sh.printf("%s < %s in %s: %s", stamp(), g.Sender().Label(who), label, g.Text)
		}
		if g.Kind == sl.GroupChatInvited && sh.invites.first(g.Group) {
			sh.noticef("%s invites this avatar into %s -- \"conference join %d\" joins it, "+
				"\"conference leave %d\" refuses", sl.SenderPerson.Label(who), label, n, n)
		}
	case sl.GroupChatEntered, sl.GroupChatLeft:
		if g.From == sh.s.Me() {
			return
		}
		verb := "came into"
		if g.Kind == sl.GroupChatLeft {
			verb = "left"
		}
		sh.noticef("%s %s %s", sl.SenderPerson.Label(who), verb, label)
	case sl.GroupChatClosed:
		sh.noticef("this avatar is out of %s: %s", label, g.Text)
	case sl.GroupChatRefused:
		sh.noticef("the grid refused a message to %s: %s", label, g.Text)
	}
}
