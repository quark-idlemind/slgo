package main

// A group's chat, joined and spoken to from the shell.
//
//	group chat NAME        join a group's chat
//	group say NAME TEXT    say something in it
//	group leave-chat NAME  leave it
//
// They are words after "group" rather than commands of their own: the
// group is named exactly as the rest of that command names one, and
// the help lists them under the one heading.  A line that is the whole
// name of a group keeps meaning that group, so "group chat lounge" for
// a group called that still activates it; the words only take over a
// line that is not one.
//
// Nothing joins on its own.  Somebody speaking in a group this avatar
// has not joined is announced once, and heard as a line, and joining is
// "group chat".  What the viewer does and what is not known:
// doc/group-chat.md

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// groupChatWord says whether a line's first word is one of the three.
func groupChatWord(w string) bool {
	return w == "chat" || w == "say" || w == "leave-chat"
}

// namesAGroup says a line is the whole name of a group that has been
// joined, or a key.
func namesAGroup(p *sl.Presence, line string) bool {
	line = strings.TrimSpace(line)
	for _, g := range p.Groups {
		if strings.EqualFold(g.Name, line) {
			return true
		}
	}
	return false
}

// cmdGroupChat runs one of the three, given the words after "group".
func cmdGroupChat(ctx context.Context, sh *Shell, out io.Writer, p *sl.Presence, args []string) error {
	verb, rest := args[0], args[1:]
	if len(rest) == 0 {
		return usageError("group", fmt.Sprintf("which group to %s; \"group\" lists the ones "+
			"this avatar has joined", groupChatVerb(verb)))
	}

	if verb == "say" {
		id, name, text, err := groupAndRest(p, rest)
		if err != nil {
			return err
		}
		if len(text) == 0 {
			return usageError("group", "what to say in it")
		}
		line := strings.Join(text, " ")
		if err := sh.s.SayToGroup(ctx, id, line); err != nil {
			if errors.Is(err, sl.ErrNotInGroupChat) {
				return fmt.Errorf("this shell has not joined the chat of %s; "+
					"\"group chat %s\" joins it", describeGroup(name, id), chatArg(name, id))
			}
			return err
		}
		sh.printf("%s > %s: %s", stamp(), sl.SenderGroup.Label(groupWord(name, id)), line)
		return nil
	}

	id, name, err := chooseGroup(p, strings.Join(rest, " "))
	if err != nil {
		return err
	}
	if id.IsZero() {
		return usageError("group", "\"none\" is how \"group\" spells acting as nobody; name a group")
	}
	label := sl.SenderGroup.Label(groupWord(name, id))
	if verb == "leave-chat" {
		if err := sh.s.LeaveGroupChat(ctx, id); err != nil {
			return err
		}
		sh.invites.clear(id)
		fmt.Fprintf(out, "left the chat of %s\n", label)
		fmt.Fprintln(out, "nothing answers a leave, so this says what was sent and not what became of it")
		return nil
	}
	if err := sh.s.JoinGroupChat(ctx, id); err != nil {
		return err
	}
	sh.invites.clear(id)
	// The start reply was measured listing nobody, members or not, so
	// no count is printed from it.
	// Why: doc/group-chat.md#measured
	fmt.Fprintf(out, "joined the chat of %s\n", label)
	return nil
}

func groupChatVerb(verb string) string {
	if verb == "leave-chat" {
		return "leave the chat of"
	}
	if verb == "say" {
		return "speak to"
	}
	return "join the chat of"
}

// groupAndRest reads a group off the front of a line and leaves the rest
// as what to say: a key, or the longest run of words that is the whole
// name of a joined group, as whoAndRest reads a person.
func groupAndRest(p *sl.Presence, args []string) (msg.UUID, string, []string, error) {
	if id, err := msg.ParseUUID(args[0]); err == nil {
		return id, nameOfGroup(p.Groups, id), args[1:], nil
	}
	for n := len(args); n >= 1; n-- {
		want := strings.Join(args[:n], " ")
		var found []sl.Group
		for _, g := range p.Groups {
			if strings.EqualFold(g.Name, want) {
				found = append(found, g)
			}
		}
		switch len(found) {
		case 0:
			continue
		case 1:
			return found[0].ID, found[0].Name, args[n:], nil
		}
		// Two of one name is refused with the keys, as chooseGroup does.
		_, _, err := chooseGroup(p, want)
		return msg.UUID{}, "", nil, err
	}
	if len(p.Groups) == 0 {
		return msg.UUID{}, "", nil, fmt.Errorf("no groups are known here, so %q matches nothing; "+
			"the list arrives on its own shortly after login, so try again in a moment, "+
			"or give the group's key", args[0])
	}
	return msg.UUID{}, "", nil, fmt.Errorf("this avatar has joined no group whose name is the start of "+
		"%q; \"group\" lists the ones it has", strings.Join(args, " "))
}

// groupWord is a group as a line names it: by name, else by the start of
// its key.
func groupWord(name string, id msg.UUID) string {
	if name != "" {
		return name
	}
	return "group " + id.String()[:8]
}

// chatArg is what to type after "group chat" to mean this group.
func chatArg(name string, id msg.UUID) string {
	if name != "" {
		return name
	}
	return id.String()
}

// chatInvites is the groups whose invitation has been announced, so that
// a busy group is announced once and not at every message.
type chatInvites struct {
	mu   sync.Mutex
	told map[msg.UUID]bool
}

// first says this is the first invitation since the chat was joined or
// left, and remembers it.
func (c *chatInvites) first(id msg.UUID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.told[id] {
		return false
	}
	if c.told == nil {
		c.told = map[msg.UUID]bool{}
	}
	c.told[id] = true
	return true
}

func (c *chatInvites) clear(id msg.UUID) {
	c.mu.Lock()
	delete(c.told, id)
	c.mu.Unlock()
}

// heardGroupChat prints one thing that happened in a group's chat.
//
// It runs on the goroutine that delivers everything, so the group is
// named from the cache and a miss asks for the list in the background.
// A group and a speaker are printed through Sender.Label and never bare.
// Why: doc/group-chat.md
func (sh *Shell) heardGroupChat(g *sl.GroupChat) {
	name, ok := sh.groups.name(g.Group)
	if !ok || name == "" {
		name = g.GroupName
	}
	if !ok {
		sh.groups.kick()
	}
	label := sl.SenderGroup.Label(groupWord(name, g.Group))

	switch g.Kind {
	case sl.GroupChatSaid, sl.GroupChatInvited:
		who := g.FromName
		if who == "" {
			who = sh.s.NameOr(g.From)
		}
		switch {
		case g.Mine:
			// This avatar, through another client of the same session:
			// shown as this shell shows its own words.
			sh.printf("%s > %s: %s", stamp(), label, g.Text)
		case g.From == sh.s.Me():
			// The grid sending our own words back: already shown when sent.
		default:
			sh.printf("%s < %s in %s: %s", stamp(), g.Sender().Label(who), label, g.Text)
		}
		if g.Kind == sl.GroupChatInvited && sh.invites.first(g.Group) {
			sh.noticef("%s is talking and this avatar is not in its chat -- "+
				"\"group chat %s\" joins it", label, chatArg(name, g.Group))
		}
	case sl.GroupChatClosed:
		sh.noticef("this avatar is out of the chat of %s: %s", label, g.Text)
	case sl.GroupChatRefused:
		if g.Group.IsZero() {
			sh.noticef("the grid refused a group chat message: %s", g.Text)
		} else {
			sh.noticef("the grid refused a message to %s: %s", label, g.Text)
		}
	}
}
