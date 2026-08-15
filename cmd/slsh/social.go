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
	"profile": {
		usage: "profile WHO",
		brief: "what somebody's profile says: born, payment, partner, about, groups",
		run:   cmdProfile,
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
		// A text box carries a sentinel where its buttons would be, and
		// printing that at somebody is showing them the plumbing.
		if d.IsTextBox() {
			sh.noticef("%s asks: %q -- waiting lists it, answer N TEXT replies",
				d.ObjectName, d.Message)
			return
		}
		sh.noticef("%s asks: %q %v -- waiting lists it, answer N picks one",
			d.ObjectName, d.Message, d.Buttons)
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
			sh.noticef("%s wants %s -- waiting lists it, answer N grants it",
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
	case m.Dialog == sl.DialogGroupInvitation:
		// Deliberately not "answer N joins": whether a bare answer
		// joins depends on the fee, which the listing has and a line
		// of notice has no room for.
		sh.noticef("%s invites you into a group -- waiting lists it, and what joining costs", name)
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
	var o helpOnly
	args, done, err := subOptions("chat", "[WHO]", &o, out, args)
	if err != nil || done {
		return err
	}
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
	var flags helpOnly
	args, done, err := subOptions("im", "WHO [TEXT]", &flags, out, args)
	if err != nil || done {
		return err
	}
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
	var o helpOnly
	args, done, err := subOptions("lookup", "TEXT", &o, out, args)
	if err != nil || done {
		return err
	}
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
	sh.printFound(out, found)
	return nil
}

// printFound prints what a name search turned up and makes it the last
// listing, so that a number typed afterwards means one of these lines.
//
// This is lookup's listing, shared rather than copied: whoOrSearch shows
// the same rows for the same reason -- somebody has to pick one of them
// -- and two spellings of the same list would be two lists as far as
// anybody reading the screen is concerned.  The display name is shown
// only where it differs from the name, since a second column repeating
// the first is a column of noise.
//
// A full page says so.  The search answers with at most sl.LookupLimit
// rows and nothing in the reply says how many it left behind, so one
// letter typed into it comes back looking exactly like the whole of the
// grid -- and a person scrolling a hundred names for one that is not
// there deserves to know the search stopped counting rather than that
// their friend has left.
func (sh *Shell) printFound(out io.Writer, found []sl.Found) {
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
	if len(found) >= sl.LookupLimit {
		fmt.Fprintf(out, "that is as many as one search answers with, "+
			"so there are probably more; try more of the name\n")
	}
}

// profileOptions is what profile was asked for.
type profileOptions struct {
	Wait int  `getopt:"--wait -w=SECONDS  how long to give the grid to answer [15]"`
	Help bool `getopt:"--help -h          show what this command takes"`
}

// cmdProfile prints what a profile says about somebody.
//
// # What it prints, and what it says instead of nothing
//
// A block of labelled fields with the group listing under it:
//
//	Hobb Resident
//	  key       d22b7e57-7e57-c0de-0e4e-000000000001
//	  born      5/21/2010
//	  payment   on file, and used
//	  partner   a0c27e57-7e57-c0de-9773-c8eaa2e796f4  Somebody Resident
//	  about     I build things.
//	  groups    1 listed, which is not every group they are in
//	  5adb7e57-7e57-c0de-f28b-a359208f6cdd  Officer   Lorn Rangers
//
// Every field says what it does not know rather than printing an empty
// column, because a blank beside "born" reads as a shell that lost the
// answer where "not said" reads as a profile that does not have one.
// The two of them a person is most likely to misread are worth the
// words: "none on file" and "not revealed" are different facts about
// payment -- a captioned account has its payment information withheld
// rather than absent -- and the groups are only the ones their owner
// chose to list, which is why the count says so.
//
// The keys stay in the lines.  A key is what everything else in this
// shell takes, including this command's own argument, so a partner or a
// group worth reading about is one more line to type and not a search.
//
// # A key nobody knows is a sentence
//
// The grid has no way of saying it has never heard of somebody: it
// answers the question with an empty group list and simply never sends
// the profile (see sl.Profile).  So the answer for a made-up key is the
// deadline passing, and it is printed as the plain sentence it is rather
// than as a failure -- nothing went wrong, and the question was
// answered.  It is also the one case worth a --wait: the whole of the
// wait is spent on it, where a profile that exists arrives in
// milliseconds.
func cmdProfile(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o profileOptions
	args, done, err := subOptions("profile", "WHO", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) == 0 {
		return fmt.Errorf("usage: profile WHO")
	}
	id, name, err := sh.whoOrSearch(ctx, out, strings.Join(args, " "))
	if err != nil {
		return err
	}

	p, err := sh.s.Profile(ctx, id, time.Duration(o.Wait)*time.Second)
	if err != nil {
		return err
	}
	if !p.Known {
		fmt.Fprintf(out, "the grid says nothing at all about %s: it answered with an "+
			"empty group list and no profile, which is how it says it has never "+
			"heard of a key\n", id)
		return nil
	}

	fmt.Fprintln(out, name)
	field := func(label, value string) {
		fmt.Fprintf(out, profileField, label, value)
	}
	field("key", id.String())
	field("born", orUnknown(p.BornOn, "not said"))
	field("payment", p.Payment().String())
	if p.Caption != "" {
		// Beside payment, since a caption is why payment can be
		// withheld: read on its own the line would look like a title
		// somebody chose.
		field("account", p.Caption)
	}
	if p.Partner.IsZero() {
		field("partner", "nobody")
	} else {
		names := sh.s.Names(ctx, []msg.UUID{p.Partner}, 3*time.Second)
		field("partner", strings.TrimRight(
			fmt.Sprintf("%-36s %s", p.Partner, names[p.Partner]), " "))
	}
	if p.URL != "" {
		field("web", p.URL)
	}
	field("about", indented(orUnknown(p.About, "nothing said")))

	// The interests are shown only when there are any.  The current
	// viewer throws the whole reply away unread -- its handler is empty,
	// under a comment wondering whether the interests panel is still
	// part of the design (llavatarpropertiesprocessor.cpp:467-477) -- so
	// almost nobody has been able to set one for years, and three lines
	// saying so on every profile would be noise.  Somebody who did fill
	// them in has text no viewer will show them, and it costs a line.
	for _, in := range []struct{ label, text string }{
		{"wants", p.WantTo}, {"skills", p.Skills}, {"speaks", p.Languages},
	} {
		if in.text != "" {
			field(in.label, indented(in.text))
		}
	}

	if len(p.Groups) == 0 {
		field("groups", "none listed")
		return nil
	}
	field("groups", fmt.Sprintf("%d listed, which is not every group they are in",
		len(p.Groups)))
	for _, g := range p.Groups {
		fmt.Fprintln(out, strings.TrimRight(
			fmt.Sprintf("  %-36s %-20s %s", g.ID, g.Title, g.Name), " "))
	}
	return nil
}

// whoOrSearch is who a command means, falling back to the grid's own
// search when nothing here has heard the name.
//
// # Why the fallback exists
//
// sh.who reaches whoever has been mentioned and whoever is standing in
// the region, which is everybody a shell usually talks about and not
// everybody there is.  Somebody on the other side of the grid has been
// mentioned to nobody and is standing nowhere near, so
//
//	slsh -a qi -c "profile Perrick Hobb"
//
// would refuse a name that "lookup" finds at once.  A profile is exactly
// the question one asks about somebody who is not here, so the command
// that answers it should not be the one command that cannot find them.
//
// # Why it is not in sh.who
//
// Because of what the other callers do with the answer.  Reading a
// profile is a public question about somebody, answered by the grid to
// anybody who asks: nothing reaches the person, nothing is spent, and
// guessing wrong costs a wasted listing on the screen.  im, offer and
// give reach OUT -- a message arrives, a friendship is offered, an item
// changes hands -- and a name guessed at there delivers it to a
// stranger, which is a different kind of mistake and not one to make on
// somebody's behalf because a search was convenient.  The region is a
// different matter and is in sh.who for everybody: see there.
//
// # What it does with what it finds
//
// One hit is the answer.  A name that matches one row exactly is that
// row even when the search returned others, which is what chooseGroup
// does with a group name and for the same reason: a name typed in full
// is not an ambiguous name.
//
// Several are printed, as lookup's own numbered listing and through
// lookup's own code, and the refusal after them says only that a number
// picks one.  Naming them in the sentence instead is what this did
// first, and one letter typed on Agni made it ninety-five names joined
// by commas into a single line -- ending with a promise about numbers
// that were nowhere on the screen.  A list that somebody is meant to
// choose from has to look like a list.
//
// A key or a number is never searched for.  A key needs no search, and
// a number that was not in the last listing means the listing, not
// somebody called "3".
func (sh *Shell) whoOrSearch(ctx context.Context, out io.Writer, want string) (msg.UUID, string, error) {
	id, name, err := sh.who(ctx, want)
	if err == nil {
		return id, name, nil
	}
	want = strings.TrimSpace(want)
	if _, e := msg.ParseUUID(want); e == nil {
		return msg.UUID{}, "", err
	}
	if _, e := strconv.Atoi(want); e == nil {
		return msg.UUID{}, "", err
	}

	found, ferr := sh.s.Lookup(ctx, want)
	if ferr != nil {
		return msg.UUID{}, "", ferr
	}

	var exact []sl.Found
	for _, f := range found {
		if strings.EqualFold(f.Name, want) || strings.EqualFold(f.Username, want) {
			exact = append(exact, f)
		}
	}
	switch {
	case len(exact) == 1:
		return exact[0].ID, exact[0].Name, nil
	case len(found) == 1:
		return found[0].ID, found[0].Name, nil
	case len(found) == 0:
		// Not "try lookup": this has just done that.  What is left is
		// less of the name, since the search matches part of one, or the
		// key for somebody the search will not turn up.
		return msg.UUID{}, "", fmt.Errorf("nobody here or on the grid is called %q; "+
			"try less of the name, or give the key", want)
	}

	sh.printFound(out, found)
	return msg.UUID{}, "", fmt.Errorf("%d people answer to %q; a number picks one",
		len(found), want)
}

// How a profile's lines are laid out: two spaces, a label wide enough
// for the widest of them, and the value.  The margin is where the value
// starts, and the two have to agree -- see indented.
const (
	profileField  = "  %-9s %s\n"
	profileMargin = "            "
)

// orUnknown is what to print when a field is empty, which is a sentence
// rather than a blank: a column with nothing in it reads as an answer
// that went missing on the way.
func orUnknown(s, missing string) string {
	if s == "" {
		return missing
	}
	return s
}

// indented lines up the second and later lines of a value with the
// first.  An about text is whatever somebody typed into a box, newlines
// and all, and a second line starting at the margin would read as a
// field of its own with a very long name.
func indented(s string) string {
	return strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n"+profileMargin)
}

func cmdOffer(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	args, done, err := subOptions("offer", "WHO [TEXT]", &o, out, args)
	if err != nil || done {
		return err
	}
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
	var o helpOnly
	args, done, err := subOptions("offers", "", &o, out, args)
	if err != nil || done {
		return err
	}
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
	var flags helpOnly
	args, done, err := subOptions("accept", "[WHO|NAME]", &flags, out, args)
	if err != nil || done {
		return err
	}
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
	var flags helpOnly
	args, done, err := subOptions("decline", "[WHO|NAME]", &flags, out, args)
	if err != nil || done {
		return err
	}
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
	var o helpOnly
	args, done, err := subOptions("talk", "", &o, out, args)
	if err != nil || done {
		return err
	}
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
// last listing, a name the session has heard, or somebody standing in
// the region.
//
// # Why the region is looked at, and why the grid is not
//
// The session's name cache holds whoever has been mentioned to it: a
// listing printed, somebody who has spoken, a conversation opened.
// Nothing evicts from it -- the daemon keeps avatars whatever the
// distance (agent/objects.go:319) and the cache is kept for the life of
// the session (sl/names.go) -- but a shell that has just started has had
// nothing mentioned to it, so "slsh -c" would refuse a name that "who"
// would have listed a moment later.  The daemon has known that avatar
// all along; only this process had not asked.
//
// So asking is the second step.  It is safe for every command that
// resolves a person because it is not a guess: the answer is somebody
// standing in the region, matched by the same rules the cache is matched
// by, and a shell that lists a person under a name has to accept that
// name back from the next command typed.
//
// Searching the GRID is a third step and is not here.  It reaches people
// who are nowhere near, whom nothing has mentioned, and whose names may
// merely resemble what was typed -- which is fine for reading a public
// profile and is not fine for im, offer or give, where a name guessed at
// wrong delivers something to a stranger.  See whoOrSearch, which is the
// commands that only look.
//
// The cost is the reason the region is asked second and not first: it is
// a round trip to the daemon and a name resolution, so the case that
// already works must not pay for it.
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
	if len(hits) == 0 {
		// Asking who is in the region puts their names in the cache, so
		// the second look is the same look: one set of matching rules,
		// and two people whose names differ only in case are still two
		// people rather than a pick.
		//
		// A region that will not answer is left as a region with nobody
		// in it.  The refusal below is what this would have said a
		// moment ago anyway, and turning a mistyped name into a report
		// about the daemon would explain the wrong thing.
		if _, err := sh.s.Nearby(ctx); err == nil {
			hits = sh.s.Find(want)
		}
	}
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
