package main

// Talking, and everybody there is to talk to.
//
// Chat mode is the inner one: "chat" enters it and the escape key
// leaves.  What is typed there goes to the current conversation --
// the region's open channel, or one person -- and tab moves between
// them.  Everything heard is printed whichever mode is in force, since
// arriving chat has nothing to do with what the keyboard is for.

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var socialCommands = map[string]*command{
	"chat": {
		usage: "chat [WHO]",
		brief: "enter chat mode; with WHO, in an instant message session",
		run:   cmdChat,
	},
	"say": {
		usage: "say TEXT",
		brief: "say one line without leaving command mode",
		run:   cmdSay,
	},
	"im": {
		usage: "im WHO [TEXT]",
		brief: "open a conversation, or send one message to it",
		run:   cmdIM,
	},
	"friends": {
		usage: "friends [-a]",
		brief: "friends who are online, or -a for all of them",
		run:   cmdFriends,
	},
	"lookup": {
		usage: "lookup TEXT",
		brief: "search the grid for people by part of a name",
		run:   cmdLookup,
	},
	"offer": {
		usage: "offer WHO [TEXT]",
		brief: "offer friendship",
		run:   cmdOffer,
	},
	"offers": {
		usage: "offers",
		brief: "friendship and inventory offers waiting for an answer",
		run:   cmdOffers,
	},
	"accept": {
		usage: "accept [WHO|NAME]",
		brief: "accept an offer of friendship, or of an inventory item",
		run:   cmdAccept,
	},
	"decline": {
		usage: "decline [WHO|NAME]",
		brief: "refuse one",
		run:   cmdDecline,
	},
	"talk": {
		usage: "talk",
		brief: "the conversations chat mode cycles between",
		run:   cmdTalk,
	},
}

// Conversation is somewhere to talk: the region's open chat, or one
// person.
type Conversation struct {
	Target msg.UUID // zero for open chat
	Name   string
}

func (c *Conversation) Local() bool { return c.Target.IsZero() }

func (c *Conversation) Label() string {
	if c.Local() {
		return "Local"
	}
	return c.Name
}

// Conversations is the list, always with open chat first.
type Conversations struct {
	mu   sync.Mutex
	list []*Conversation
	cur  int
}

func NewConversations() *Conversations {
	return &Conversations{list: []*Conversation{{Name: "Local"}}}
}

func (cs *Conversations) Current() *Conversation {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.list[cs.cur]
}

func (cs *Conversations) Next() *Conversation {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.cur = (cs.cur + 1) % len(cs.list)
	return cs.list[cs.cur]
}

// Open returns the conversation with somebody, making one if there is
// none, and says whether it had to be made.
func (cs *Conversations) Open(target msg.UUID, name string) (*Conversation, bool) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	for _, c := range cs.list {
		if c.Target == target {
			if name != "" && c.Name != name {
				c.Name = name
			}
			return c, false
		}
	}
	c := &Conversation{Target: target, Name: name}
	cs.list = append(cs.list, c)
	return c, true
}

func (cs *Conversations) Switch(c *Conversation) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	for i, x := range cs.list {
		if x == c {
			cs.cur = i
			return
		}
	}
}

func (cs *Conversations) All() ([]*Conversation, int) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	out := make([]*Conversation, len(cs.list))
	copy(out, cs.list)
	return out, cs.cur
}

// ---------------------------------------------------------------- listening

// watch prints everything that arrives, in either mode.
func (sh *Shell) watch(ctx context.Context) {
	lines := sh.s.Chat(sl.ChatFilter{}, 256)
	ims := sh.s.IMs(0)
	perms := sh.s.Permissions(0)
	sh.s.OnDialog = func(d sl.Dialog) {
		sh.noticef("%s asks: %q %v -- answer with: say -c %d BUTTON",
			d.ObjectName, d.Message, d.Buttons, d.Channel)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case l, ok := <-lines:
			if !ok {
				return
			}
			if l.Source == sh.s.Me() {
				continue // our own words, already shown when sent
			}
			sh.term.Printf("%s < [Local] %s: %s", stamp(), l.From, l.Text)
		case m, ok := <-ims:
			if !ok {
				return
			}
			sh.heard(m)
		case q, ok := <-perms:
			if !ok {
				return
			}
			sh.noticef("%s wants %s -- grant with: accept-perms, or ignore it",
				q.ObjectName, q.Wants)
		}
	}
}

func (sh *Shell) heard(m *sl.IM) {
	name := m.FromName
	if name == "" {
		name = sh.s.NameOr(m.From)
	}
	switch {
	case m.Conversation():
		c, made := sh.talk.Open(m.From, name)
		if made {
			sh.noticef("new conversation with %s", name)
		}
		sh.term.Printf("%s < [IM %s] %s", stamp(), c.Label(), m.Text)
	case m.Dialog == sl.DialogFriendshipOffered:
		sh.noticef("%s offers friendship -- accept %s, or decline %s",
			name, firstWord(name), firstWord(name))
	case m.Dialog == sl.DialogTypingStart, m.Dialog == sl.DialogTypingStop:
		// A line per keystroke is not worth showing.
	default:
		sh.noticef("%s from %s: %s", sl.DialogName(m.Dialog), name, m.Text)
	}
}

// cycle moves to the next conversation, which is what tab does in chat
// mode.
func (sh *Shell) cycle() {
	if sh.term.Line() != "" {
		return
	}
	sh.talk.Next()
	sh.prompt()
}

// send puts a line to the current conversation.
func (sh *Shell) send(ctx context.Context, text string) {
	c := sh.talk.Current()
	if c.Local() {
		if err := sh.s.Say(ctx, text, 0); err != nil {
			sh.errorf("%v", err)
			return
		}
		sh.term.Printf("%s > [Local] %s", stamp(), text)
		return
	}
	if err := sh.s.SendIM(ctx, c.Target, text); err != nil {
		sh.errorf("%v", err)
		return
	}
	sh.term.Printf("%s > [IM %s] %s", stamp(), c.Label(), text)
}

// ---------------------------------------------------------------- commands

func cmdChat(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	if len(args) > 0 {
		id, name, err := sh.who(ctx, strings.Join(args, " "))
		if err != nil {
			return err
		}
		c, _ := sh.talk.Open(id, name)
		sh.talk.Switch(c)
	}
	sh.setMode(modeChat)
	return nil
}

// sayOptions is what say was asked for.
type sayOptions struct {
	Channel int  `getopt:"-c=CHANNEL  the channel to say it on; negative ones reach scripts"`
	Help    bool `getopt:"--help -h   show what this command takes"`
}

func cmdSay(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o sayOptions
	args, done, err := subOptions("say", "TEXT ...", &o, out, args)
	if err != nil || done {
		return err
	}
	channel := int32(o.Channel)
	if len(args) == 0 {
		return fmt.Errorf("usage: say [-c CHANNEL] TEXT")
	}
	text := strings.Join(args, " ")
	if err := sh.s.Say(ctx, text, channel); err != nil {
		return err
	}
	where := "Local"
	if channel != 0 {
		where = fmt.Sprintf("channel %d", channel)
	}
	sh.term.Printf("%s > [%s] %s", stamp(), where, text)
	return nil
}

func cmdIM(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: im WHO [TEXT]")
	}
	id, name, err := sh.who(ctx, args[0])
	if err != nil {
		return err
	}
	c, made := sh.talk.Open(id, name)
	if len(args) == 1 {
		sh.talk.Switch(c)
		sh.setMode(modeChat)
		return nil
	}
	text := strings.Join(args[1:], " ")
	if err := sh.s.SendIM(ctx, id, text); err != nil {
		return err
	}
	if made {
		sh.noticef("new conversation with %s", name)
	}
	sh.term.Printf("%s > [IM %s] %s", stamp(), c.Label(), text)
	return nil
}

// friendsOptions is what friends was asked for.
type friendsOptions struct {
	All  bool `getopt:"-a          everyone, not only whoever is online"`
	Help bool `getopt:"--help -h   show what this command takes"`
}

func cmdFriends(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o friendsOptions
	if _, done, err := subOptions("friends", "", &o, out, args); err != nil || done {
		return err
	}
	all := o.All
	var ps []sl.Person
	var err error
	if all {
		ps, err = sh.s.FriendList(ctx)
	} else {
		ps, err = sh.s.OnlineFriends(ctx)
	}
	if err != nil {
		return err
	}
	if len(ps) == 0 {
		fmt.Fprintln(out, "nobody")
		return nil
	}
	listed := make([]person, 0, len(ps))
	for i, p := range ps {
		fmt.Fprintf(out, "%2d  %-32s %s\n", i+1, p.Name, p.ID)
		listed = append(listed, person{ID: p.ID, Name: p.Name})
	}
	sh.setListed(listed)
	return nil
}

func cmdLookup(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: lookup TEXT")
	}
	found, err := sh.s.Lookup(ctx, strings.Join(args, " "))
	if err != nil {
		return err
	}
	if len(found) == 0 {
		fmt.Fprintln(out, "nobody found")
		return nil
	}
	listed := make([]person, 0, len(found))
	for i, f := range found {
		line := fmt.Sprintf("%2d  %-32s", i+1, f.Name)
		if f.Display != "" && !strings.EqualFold(f.Display, f.Name) {
			line += "  " + f.Display
		}
		fmt.Fprintln(out, strings.TrimRight(line, " "))
		listed = append(listed, person{ID: f.ID, Name: f.Name})
	}
	sh.setListed(listed)
	return nil
}

func cmdOffer(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: offer WHO [TEXT]")
	}
	id, name, err := sh.who(ctx, args[0])
	if err != nil {
		return err
	}
	if err := sh.s.OfferFriendship(ctx, id, strings.Join(args[1:], " ")); err != nil {
		return err
	}
	fmt.Fprintf(out, "offered friendship to %s\n", name)
	return nil
}

// cmdOffers lists both kinds waiting.
//
// Both, in one list, because from where a person sits they are the same
// thing -- something somebody offered that has not been answered --
// and because an offer nobody answers stays pending for ever while the
// other side is told nothing.
func cmdOffers(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	friends := sh.s.Offers()
	items := sh.s.InventoryOffers()
	if len(friends) == 0 && len(items) == 0 {
		fmt.Fprintln(out, "no offers waiting")
		return nil
	}
	for _, o := range friends {
		fmt.Fprintf(out, "friendship  %-28s %s ago\n",
			o.Name, time.Since(o.At).Round(time.Second))
	}
	for _, o := range items {
		fmt.Fprintf(out, "%-11s %-28s %s ago   from %s\n",
			o.Asset.String(), o.Name,
			time.Since(o.At).Round(time.Second), o.FromName)
	}
	return nil
}

func cmdAccept(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	// An inventory offer is looked for first, and only by an argument
	// that names one, so "accept" with a friendship offer waiting still
	// means what it always did.
	if o, ok := sh.inventoryOffer(args); ok {
		// Into the folder the shell is in, so that "cd Objects; accept"
		// puts it where it was wanted.  A zero folder would let the
		// grid choose.
		_, folder, err := sh.resolveDir(ctx, ".")
		if err != nil {
			return err
		}
		if err := o.Accept(ctx, folder); err != nil {
			return err
		}
		fmt.Fprintf(out, "accepted %q from %s\n", o.Name, o.FromName)
		return nil
	}

	o, err := sh.offer(args)
	if err != nil {
		return err
	}
	if err := o.Accept(ctx); err != nil {
		return err
	}
	fmt.Fprintf(out, "accepted %s\n", o.Name)
	return nil
}

func cmdDecline(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	if o, ok := sh.inventoryOffer(args); ok {
		if err := o.Decline(ctx); err != nil {
			return err
		}
		fmt.Fprintf(out, "declined %q from %s\n", o.Name, o.FromName)
		return nil
	}

	o, err := sh.offer(args)
	if err != nil {
		return err
	}
	if err := o.Decline(ctx); err != nil {
		return err
	}
	fmt.Fprintf(out, "declined %s\n", o.Name)
	return nil
}

// inventoryOffer finds the inventory offer a command means.
//
// With no argument it answers only when an inventory offer is the ONLY
// thing waiting, so that "accept" with a friendship offer pending keeps
// meaning the friendship offer.  Ambiguity is resolved by naming.
func (sh *Shell) inventoryOffer(args []string) (*sl.InventoryOffer, bool) {
	items := sh.s.InventoryOffers()
	if len(items) == 0 {
		return nil, false
	}
	want := strings.TrimSpace(strings.Join(args, " "))
	if want == "" {
		if len(sh.s.Offers()) > 0 {
			return nil, false // a friendship offer is waiting too; say which
		}
		if len(items) == 1 {
			return items[0], true
		}
		return nil, false
	}
	return sh.s.InventoryOfferFor(want)
}

func cmdTalk(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	list, cur := sh.talk.All()
	for i, c := range list {
		mark := " "
		if i == cur {
			mark = "*"
		}
		fmt.Fprintf(out, "%s %2d  %s\n", mark, i+1, c.Label())
	}
	return nil
}

// offer finds the offer a command means: the only one, or the one from
// somebody named.
func (sh *Shell) offer(args []string) (*sl.Offer, error) {
	os := sh.s.Offers()
	if len(os) == 0 {
		return nil, fmt.Errorf("no offers waiting")
	}
	if len(args) == 0 {
		if len(os) > 1 {
			return nil, fmt.Errorf("%d offers waiting; name one", len(os))
		}
		return os[0], nil
	}
	want := strings.ToLower(strings.Join(args, " "))
	var hits []*sl.Offer
	for _, o := range os {
		if strings.Contains(strings.ToLower(o.Name), want) {
			hits = append(hits, o)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return nil, fmt.Errorf("no offer from anybody matching %q", want)
	default:
		return nil, fmt.Errorf("%q matches %d offers", want, len(hits))
	}
}

// who turns what was typed into somebody: a uuid, the number from the
// last listing, or a name the session has heard.
func (sh *Shell) who(ctx context.Context, want string) (msg.UUID, string, error) {
	want = strings.TrimSpace(want)
	if id, err := msg.ParseUUID(want); err == nil {
		names := sh.s.Names(ctx, []msg.UUID{id}, 3*time.Second)
		return id, orID(names[id], id), nil
	}
	if n, err := strconv.Atoi(want); err == nil {
		sh.mu.Lock()
		listed := sh.listed
		sh.mu.Unlock()
		if n < 1 || n > len(listed) {
			return msg.UUID{}, "", fmt.Errorf("there is no %d in the last listing", n)
		}
		return listed[n-1].ID, listed[n-1].Name, nil
	}

	hits := sh.s.Find(want)
	switch len(hits) {
	case 1:
		return hits[0], sh.s.NameOr(hits[0]), nil
	case 0:
		return msg.UUID{}, "", fmt.Errorf("nobody called %q is known; try who, friends or lookup", want)
	default:
		names := make([]string, 0, len(hits))
		for _, id := range hits {
			names = append(names, sh.s.NameOr(id))
		}
		return msg.UUID{}, "", fmt.Errorf("%q could be any of: %s", want, strings.Join(names, ", "))
	}
}

func (sh *Shell) setListed(ps []person) {
	sh.mu.Lock()
	sh.listed = ps
	sh.mu.Unlock()
}

func orID(name string, id msg.UUID) string {
	if name != "" {
		return name
	}
	return id.String()
}

func firstWord(s string) string {
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[:i]
	}
	return s
}
