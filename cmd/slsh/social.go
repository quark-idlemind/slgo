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
	"errors"
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
		params:   "[WHO]",
		flags:    func() any { return new(helpOnly) },
		brief:    "enter chat mode; with WHO, in an instant message session",
		keywords: "talk conversation chat mode typing messages local nearby private switch keyboard",
		man:      "chat",
		run:      cmdChat,
	},
	"say": {
		params:   "TEXT ...",
		flags:    func() any { return new(sayOptions) },
		brief:    "say one line without leaving command mode",
		keywords: "speak chat out loud local chat channel message nearby",
		man:      "say",
		run:      cmdSay,
	},
	"im": {
		params:   "WHO [TEXT]",
		flags:    func() any { return new(helpOnly) },
		brief:    "open a conversation, or send one message to it",
		keywords: "private message instant message direct message send tell person somebody",
		man:      "im",
		run:      cmdIM,
	},
	"friends": {
		flags:    func() any { return new(friendsOptions) },
		brief:    "friends who are online, or -a for all of them",
		keywords: "online friend list buddies contacts who is on logged in",
		man:      "friends",
		run:      cmdFriends,
	},
	"lookup": {
		params:   "TEXT",
		flags:    func() any { return new(lookupOptions) },
		brief:    "search the grid for people by part of a name; -l for the keys",
		keywords: "search find person people avatar name username key uuid resident",
		man:      "lookup",
		run:      cmdLookup,
	},
	"profile": {
		params:   "WHO",
		flags:    func() any { return new(profileOptions) },
		brief:    "what somebody's profile says: born, payment, partner, about, groups",
		keywords: "profile about bio born age partner groups picks information person avatar",
		man:      "profile",
		run:      cmdProfile,
	},
	"offer": {
		params:   "WHO [TEXT]",
		flags:    func() any { return new(helpOnly) },
		brief:    "offer friendship",
		keywords: "friendship add friend befriend request friend ask to be friends",
		man:      "offer",
		run:      cmdOffer,
	},
	"lure": {
		params:   "WHO [TEXT]",
		flags:    func() any { return new(lureFlags) },
		brief:    "offer somebody a teleport here, or --ask to be brought to them",
		keywords: "offer teleport bring summon invite somebody here to me teleport request come over",
		man:      "lure",
		run:      cmdLure,
	},
	"offers": {
		flags:    func() any { return new(helpOnly) },
		brief:    "friendship and inventory offers waiting for an answer",
		keywords: "pending incoming friendship requests items offered gifts received list",
		man:      "offers",
		run:      cmdOffers,
	},
	"accept": {
		params:   "[WHO|NAME]",
		flags:    func() any { return new(helpOnly) },
		brief:    "accept an offer of friendship, or of an inventory item",
		keywords: "yes agree receive incoming friendship request friend item gift inventory offer received",
		man:      "accept",
		run:      cmdAccept,
	},
	"decline": {
		params:   "[WHO|NAME]",
		flags:    func() any { return new(helpOnly) },
		brief:    "refuse one",
		keywords: "refuse reject deny no friendship request item offer gift",
		man:      "decline",
		run:      cmdDecline,
	},
	"talk": {
		flags:    func() any { return new(helpOnly) },
		brief:    "the conversations chat mode cycles between",
		keywords: "conversations list chats open sessions switch",
		man:      "talk",
		run:      cmdTalk,
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
	// The avatar arriving somewhere, which is news whoever asked for it:
	// a lure accepted from another client attached to the same daemon, a
	// session re-established after the circuit was lost, and an avatar
	// that walked over a border and was told about it.  tp asks for its
	// own and prints its own line as well -- see cmdTP for why both are
	// wanted.
	regions := sh.s.RegionChanges(0)
	// Money: a payment made to this avatar is said, as an instant
	// message is.
	money := sh.s.Money(0)
	// What is said in the group chats this avatar is in, with the
	// invitations to the ones it is not.
	gchats := sh.s.GroupChats(0)
	// The group names a notice is announced with, asked for now so that
	// the first notice need not wait for them.  See heardNotice.
	sh.groups.kick()
	// An offer another client of this avatar has dealt with, or a dialog
	// or a permission request dropped unanswered, for its age or to make
	// room, which is gone from waiting as of now.  Said, because a
	// number a person was about to type has just stopped meaning
	// anything, and "there is no 3" a moment later would be the first
	// they heard of it.
	//
	// Every notice that changes what is waiting redraws the prompt, whose
	// count would otherwise be stale until the next command.  A prompt
	// set while a command runs is not drawn until it returns.
	sh.s.OnHandled = func(h sl.Handled) {
		sh.noticef("%s -- it is no longer waiting", h)
		sh.prompt()
	}
	sh.s.OnDialog = func(d sl.Dialog) {
		// A text box carries a sentinel where its buttons would be, and
		// printing that at somebody is showing them the plumbing.
		if d.IsTextBox() {
			sh.noticef("%s asks: %q -- waiting lists it, answer N TEXT replies",
				sl.SenderObject.Label(d.ObjectName), d.Message)
		} else {
			sh.noticef("%s asks: %q %v -- waiting lists it, answer N picks one",
				sl.SenderObject.Label(d.ObjectName), d.Message, d.Buttons)
		}
		sh.prompt()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case l, ok := <-lines:
			if !ok {
				return
			}
			if l.Source == sh.s.Me() && !l.Mine {
				continue // our own words, already shown when sent
			}
			if l.Mine {
				// This avatar, speaking through another client of the
				// same session -- a daemon, or a second shell.  Shown
				// the way this shell shows its own words, because that
				// is what it is: one avatar said one thing, and which
				// keyboard it came from is not what a transcript is
				// about.
				if l.Channel != 0 {
					sh.printf("%s > [Local %d] %s", stamp(), l.Channel, l.Text)
				} else {
					sh.printf("%s > [Local] %s", stamp(), l.Text)
				}
				continue
			}
			sh.printf("%s < [Local] %s: %s", stamp(), l.Sender().Label(l.From), l.Text)
		case m, ok := <-ims:
			if !ok {
				return
			}
			sh.heard(m)
			// It may have been an offer, which is one more thing
			// waiting.
			sh.prompt()
		case q, ok := <-perms:
			if !ok {
				return
			}
			sh.noticef("%s wants %s -- waiting lists it, answer N grants it",
				sl.SenderObject.Label(q.ObjectName), q.Wants)
			sh.prompt()
		case c, ok := <-regions:
			if !ok {
				return
			}
			// What was described in the region left behind is gone, and
			// saying so is the useful half of the line: a script or a
			// person holding an object from a moment ago is holding
			// something the new region has never heard of.
			sh.noticef("the avatar is now in %s -- what the last region "+
				"described is gone", c.Region)
		case m, ok := <-money:
			if !ok {
				return
			}
			sh.heardMoney(ctx, m)
		case g, ok := <-gchats:
			if !ok {
				return
			}
			sh.heardGroupChat(g)
		}
	}
}

// heard prints one instant message.  Whatever sent it is named through
// Sender.Label, so an object, a group or the grid is never printed as
// though a person had said it.
// Why: doc/im-senders.md#labelling-a-sender
func (sh *Shell) heard(m *sl.IM) {
	name := m.FromName
	switch {
	case name != "":
	case m.Dialog == sl.DialogFromTask:
		// The object's key: never its owner's name, which is somebody
		// else.
		name = sh.s.NameOr(m.ID)
	default:
		name = sh.s.NameOr(m.From)
	}
	who := m.Sender().Label(name)
	switch {
	case m.Mine:
		// This avatar wrote it, through another client of the same
		// session.  Without this the transcript here is every reply and
		// none of the questions, which is how it read while a daemon
		// answered for an avatar somebody was watching through slsh.
		//
		// Shown exactly as this shell shows what it sends itself: one
		// avatar said one thing to one person, and which of its clients
		// composed it is not what the conversation is about.
		if !m.Spoken() {
			return // an offer answered, a lure taken: not something said
		}
		to := sh.s.NameOr(m.To)
		if to == "" {
			to = m.To.String()
		}
		c, made := sh.talk.Open(m.To, to)
		if made {
			sh.noticef("new conversation with %s", to)
		}
		sh.printf("%s > [IM %s] %s", stamp(), c.Label(), m.Text)
	case m.Conversation():
		c, made := sh.talk.Open(m.From, name)
		if made {
			sh.noticef("new conversation with %s", name)
		}
		sh.printf("%s < [IM %s] %s", stamp(), c.Label(), m.Text)
	case m.Dialog == sl.DialogFromTask:
		// A script's message.  The name is the object's and From its
		// owner, who did not write it, so it opens no conversation.
		sh.printf("%s < %s: %s", stamp(), who, m.Text)
	case m.Dialog == sl.DialogFriendshipOffered:
		sh.noticef("%s offers friendship -- accept %s, or decline %s",
			who, firstWord(name), firstWord(name))
	case m.Dialog == sl.DialogGroupInvitation:
		// Deliberately not "answer N joins": whether a bare answer
		// joins depends on the fee, which the listing has and a line
		// of notice has no room for.
		sh.noticef("%s invites you into a group -- waiting lists it, and what joining costs", who)
	case m.Dialog == sl.DialogGroupNotice:
		sh.heardNotice(m)
	case m.Dialog == sl.DialogTypingStart, m.Dialog == sl.DialogTypingStop:
		// A line per keystroke is not worth showing.
	case m.Dialog == sl.DialogSessionSend:
		// A group's chat, which arrives again as a group chat event that
		// says which group; see heardGroupChat.
	default:
		sh.noticef("%s from %s: %s", sl.DialogName(m.Dialog), who, m.Text)
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
		sh.printf("%s > [Local] %s", stamp(), text)
		return
	}
	if err := sh.s.SendIM(ctx, c.Target, text); err != nil {
		sh.errorf("%v", err)
		return
	}
	sh.printf("%s > [IM %s] %s", stamp(), c.Label(), text)
}

// ---------------------------------------------------------------- commands

func cmdChat(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	args, done, err := subOptions("chat", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) > 0 {
		id, name, err := sh.who(ctx, out, strings.Join(args, " "))
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
	args, done, err := subOptions("say", &o, out, args)
	if err != nil || done {
		return err
	}
	channel := int32(o.Channel)
	if len(args) == 0 {
		return usageError("say")
	}
	text := strings.Join(args, " ")
	if err := sh.s.Say(ctx, text, channel); err != nil {
		return err
	}
	where := "Local"
	if channel != 0 {
		where = fmt.Sprintf("channel %d", channel)
	}
	sh.printf("%s > [%s] %s", stamp(), where, text)
	return nil
}

func cmdIM(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var flags helpOnly
	args, done, err := subOptions("im", &flags, out, args)
	if err != nil || done {
		return err
	}
	if len(args) == 0 {
		return usageError("im")
	}
	id, name, rest, err := sh.whoAndRest(ctx, out, args)
	if err != nil {
		return err
	}
	c, made := sh.talk.Open(id, name)
	if len(rest) == 0 {
		sh.talk.Switch(c)
		sh.setMode(modeChat)
		return nil
	}
	text := strings.Join(rest, " ")
	if err := sh.s.SendIM(ctx, id, text); err != nil {
		return err
	}
	if made {
		sh.noticef("new conversation with %s", name)
	}
	sh.printf("%s > [IM %s] %s", stamp(), c.Label(), text)
	return nil
}

// friendsOptions is what friends was asked for.
type friendsOptions struct {
	All  bool `getopt:"-a          everyone, not only whoever is online"`
	Help bool `getopt:"--help -h   show what this command takes"`
}

func cmdFriends(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o friendsOptions
	if _, done, err := subOptions("friends", &o, out, args); err != nil || done {
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

// lookupOptions is what lookup was asked for.
//
// -l is the flag it is everywhere else here: the ids as well as the
// names.  A resident's key is the one handle that does not change --
// a name can be a display name today and another tomorrow, and several
// people answer to part of one -- and every command that takes a
// person takes a key instead, so a search that could not print one
// sent people to "profile" for a line they had already asked for.
type lookupOptions struct {
	Long bool `getopt:"-l          the key as well as the name"`
	Help bool `getopt:"--help -h   show what this command takes"`
}

func cmdLookup(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o lookupOptions
	args, done, err := subOptions("lookup", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) == 0 {
		return usageError("lookup")
	}
	found, err := sh.s.Lookup(ctx, strings.Join(args, " "))
	if err != nil {
		return err
	}
	if len(found) == 0 {
		fmt.Fprintln(out, "nobody found")
		return nil
	}
	sh.printFound(out, found, o.Long)
	return nil
}

// printFound prints what a name search turned up and makes it the last
// listing, so that a number typed afterwards means one of these lines.
//
// This is lookup's listing, shared rather than copied: whoOrSearch and
// onTheGrid show the same rows for the same reason -- somebody has to
// pick one of them -- and two spellings of the same list would be two
// lists as far as anybody reading the screen is concerned.  The display
// name is shown only where it differs from the name, since a second
// column repeating the first is a column of noise.
//
// A full page says so.  The search answers with at most sl.LookupLimit
// rows and nothing in the reply says how many it left behind, so one
// letter typed into it comes back looking exactly like the whole of the
// grid -- and a person scrolling a hundred names for one that is not
// there deserves to know the search stopped counting rather than that
// their friend has left.
func (sh *Shell) printFound(out io.Writer, found []sl.Found, long bool) {
	listed := make([]person, 0, len(found))
	for i, f := range found {
		line := fmt.Sprintf("%2d  %-32s", i+1, f.Name)
		// The key after the name and before the display name, which is
		// where worn -l puts its ids: the columns before it are fixed
		// width, so the key lands in the same place on every row and
		// can be cut out of a listing.  The display name is last
		// because it is the one field with no width at all.
		if long {
			line += "  " + f.ID.String()
		}
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
//	  partner   dc047e57-7e57-c0de-1117-911169260e8b  Somebody Resident
//	  about     I build things.
//	  groups    1 listed, which is not every group they are in
//	  4cac7e57-7e57-c0de-07c7-d7093839ea77  Officer   Lorn Rangers
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
	args, done, err := subOptions("profile", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) == 0 {
		return usageError("profile")
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
// search when nothing here has heard the name.  It is for profile,
// which is asked about people who are often not here, and which reaches
// nobody, so it takes more from the search than onTheGrid does for the
// commands that reach somebody: one row, or a username typed alone.
//
// One hit is the answer, and so is a name or username that matches one
// row exactly among several.  Otherwise several are printed as lookup's
// numbered listing, through printFound, and refused.  A key or a number
// is never searched for: a key needs no search, and a number that was
// not in the last listing means the listing, not somebody called "3".
// Why: doc/slsh.md#the-search-profile-makes
func (sh *Shell) whoOrSearch(ctx context.Context, out io.Writer, want string) (msg.UUID, string, error) {
	id, name, err := sh.whoNear(ctx, want)
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

	// Not the long form: this is a refusal asking somebody to pick a
	// number, and a column of keys in front of that is answering a
	// question they did not ask.  "lookup -l" is where the keys are.
	sh.printFound(out, found, false)
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
	args, done, err := subOptions("offer", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) == 0 {
		return usageError("offer")
	}
	id, name, rest, err := sh.whoAndRest(ctx, out, args)
	if err != nil {
		return err
	}
	if err := sh.s.OfferFriendship(ctx, id, strings.Join(rest, " ")); err != nil {
		return err
	}
	fmt.Fprintf(out, "offered friendship to %s\n", name)
	return nil
}

// lureFlags is which direction the teleport goes.
type lureFlags struct {
	Ask  bool `getopt:"--ask -a   ask to be brought to them, rather than offering"`
	Help bool `getopt:"--help -h  show what this command takes"`
}

// cmdLure offers somebody a teleport.
//
// Named for the grid's own word, which is what the rest of this tree
// already says -- a teleport offer arriving here is announced as a
// "teleport lure".  A viewer's menu calls it Offer Teleport; `offer`
// here was already friendship, and two things called offer that differ
// by a flag would be worse than one word of the protocol's jargon with
// a page explaining it.
func cmdLure(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o lureFlags
	args, done, err := subOptions("lure", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) == 0 {
		return usageError("lure")
	}
	id, name, rest, err := sh.whoAndRest(ctx, out, args)
	if err != nil {
		return err
	}
	if o.Ask {
		if err := sh.s.RequestTeleport(ctx, id, strings.Join(rest, " ")); err != nil {
			return err
		}
		fmt.Fprintf(out, "asked %s to teleport this avatar there\n", name)
		return nil
	}
	if err := sh.s.OfferTeleport(ctx, id, strings.Join(rest, " ")); err != nil {
		return err
	}

	// What was sent and not what will happen.  Whether they come is
	// theirs to decide and there is no notice either way: an offer
	// accepted arrives as dialog 23 if it arrives at all, and one
	// ignored arrives as nothing.
	fmt.Fprintf(out, "offered %s a teleport here\n", name)
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
	args, done, err := subOptions("offers", &o, out, args)
	if err != nil || done {
		return err
	}
	friends := sh.s.Offers()
	items := sh.s.InventoryOffers()
	if len(friends) == 0 && len(items) == 0 {
		fmt.Fprintf(out, "no offers waiting -- %s\n", sh.heardFrom(false))
		return nil
	}
	for _, o := range friends {
		fmt.Fprintf(out, "friendship  %-28s %s ago%s\n",
			o.Name, time.Since(o.At).Round(time.Second), beforeShell(o.Recorded))
	}
	for _, o := range items {
		fmt.Fprintf(out, "%-11s %-28s %s ago   from %s%s\n",
			o.Asset.String(), o.Name,
			time.Since(o.At).Round(time.Second), o.FromName, beforeShell(o.Recorded))
	}
	return nil
}

// beforeShell marks an offer that arrived before this shell attached,
// and is listed only because slgod kept it.
func beforeShell(recorded bool) string {
	if recorded {
		return "   (from before this shell)"
	}
	return ""
}

func cmdAccept(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var flags helpOnly
	args, done, err := subOptions("accept", &flags, out, args)
	if err != nil || done {
		return err
	}
	// An inventory offer is looked for first, so "accept" with a
	// friendship offer waiting still means what it always did.
	o, ok, err := sh.inventoryOffer(args)
	if err != nil {
		return err
	}
	if ok {
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

	f, err := sh.offer(args)
	if err != nil {
		return err
	}
	if err := f.Accept(ctx); err != nil {
		return err
	}
	fmt.Fprintf(out, "accepted %s\n", f.Name)
	return nil
}

func cmdDecline(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var flags helpOnly
	args, done, err := subOptions("decline", &flags, out, args)
	if err != nil || done {
		return err
	}
	o, ok, err := sh.inventoryOffer(args)
	if err != nil {
		return err
	}
	if ok {
		if err := o.Decline(ctx); err != nil {
			return err
		}
		fmt.Fprintf(out, "declined %q from %s\n", o.Name, o.FromName)
		return nil
	}

	f, err := sh.offer(args)
	if err != nil {
		return err
	}
	if err := f.Decline(ctx); err != nil {
		return err
	}
	fmt.Fprintf(out, "declined %s\n", f.Name)
	return nil
}

// inventoryOffer finds the inventory offer a command means.
//
// With no argument it answers only when an inventory offer is the ONLY
// kind waiting, so that "accept" with a friendship offer pending keeps
// meaning the friendship offer, which is the older rule and the one
// people have in their fingers.
//
// The error is what tells "not this command's business" apart from
// "this command's business and I will not guess", and both callers
// have to know the difference.  A word that names no item at all is
// not an error: it goes on to be tried against the friendship offers,
// which is how accept has always read its argument -- unless an item
// offered has that name in another case, which is refused with the
// name it has.  Anything else that cannot be narrowed to one is an
// error too.
//
// Before this there was no error and both cases fell through to the
// friendship offers -- so two items waiting and no friendship offer
// answered a bare "accept" with "no offers waiting" while "offers"
// listed both of them.  Measured, on both accept and decline.
func (sh *Shell) inventoryOffer(args []string) (*sl.InventoryOffer, bool, error) {
	items := sh.s.InventoryOffers()
	if len(items) == 0 {
		return nil, false, nil
	}
	want := strings.TrimSpace(strings.Join(args, " "))
	if want == "" {
		if len(sh.s.Offers()) > 0 {
			return nil, false, nil // a friendship offer is waiting too; say which
		}
		if len(items) == 1 {
			return items[0], true, nil
		}
		return nil, false, fmt.Errorf("%d items offered; name one, or pick it by number in waiting",
			len(items))
	}
	hits := sh.s.InventoryOffersFor(want)
	switch len(hits) {
	case 1:
		return hits[0], true, nil
	case 0:
		// An item offered under this name in another case is not a
		// match, but it is the likely typo, and sl.AllNamedFunc's hint
		// says more than the friendship offers' refusal would.
		_, err := sl.AllNamedFunc(items, want, "offered item", "",
			func(o *sl.InventoryOffer) (string, msg.UUID) { return o.Name, o.Item })
		if ne := (*sl.NameError)(nil); errors.As(err, &ne) && len(ne.Near) > 0 {
			return nil, false, err
		}
		// Not ours; the friendship offers get the same word next.
		return nil, false, nil
	}
	// Two offers can carry the same name from the same person, so
	// naming harder is not always a way out -- but the numbered listing
	// always is, and answering the oldest without saying so is not.
	return nil, false, fmt.Errorf("%q matches %d offered items; name one exactly, "+
		"or pick it by number in waiting", want, len(hits))
}

func cmdTalk(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	args, done, err := subOptions("talk", &o, out, args)
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

// unknownName is a name nothing here has heard of.
//
// It is a type rather than a sentence because its callers have to tell
// it from the other refusals.  whoNear asks the region about this one
// and about nothing else: a uuid and a number have been answered
// definitively either way, and asking the region about "9" would turn
// "there is no 9 in the last listing" into a report about somebody
// called nine.  whoAndRest treats it as the signal to try a shorter run
// of words -- see there -- and onTheGrid searches the grid for this
// one and hands any other refusal back as it was.
//
// unsearched is why the grid's search, which is the last place a name
// is looked for, could not be asked.  It is carried rather than
// returned in place of the refusal because a search that failed has
// found nobody, and nobody found is the refusal this always was; but a
// person reading it is owed the difference between "the grid has
// nobody by that name" and "the grid was not asked", since only the
// second is worth trying again.
type unknownName struct {
	want       string
	unsearched error
}

func (e *unknownName) Error() string {
	if e.unsearched != nil {
		return fmt.Sprintf("nobody called %q is known here, and the grid's search "+
			"could not be made (%v); try who, friends or lookup", e.want, e.unsearched)
	}
	return fmt.Sprintf("nobody called %q is known; try who, friends or lookup", e.want)
}

// ambiguousName is a name that answers for more than one person.
//
// The opposite signal to unknownName, and the reason they are two types
// rather than one: a name nobody has is a name to keep looking for, and
// a name several people have is the answer.
type ambiguousName struct {
	want  string
	names []string
}

func (e *ambiguousName) Error() string {
	return fmt.Sprintf("%q could be any of: %s", e.want, strings.Join(e.names, ", "))
}

// maxNameWords is how many words at the front of a command line can be
// the name of a person.
//
// Two, because a Second Life name is a first name and a last name and
// every place a name is resolved from here holds it in that form: the
// session's cache is filled from UUIDNameReply, which is two fields,
// and so is the region listing and so is a search result.  A run of
// three words could therefore only ever be a name with a word of the
// message stuck to the end of it, and trying it would be one more
// chance to resolve something that was never a name.
const maxNameWords = 2

// whoAndRest is the person named at the front of a command line and
// whatever is left of it, for the commands that take somebody AND
// something else -- im, offer, lure, give, invite.  A name has two words
// in it and sh.who matches either half of one, so reading the first
// word as the name would leave the last name at the front of the rest.
//
// So the longest leading run that names somebody wins: "im a b c" tries
// "a b" and then "a".  A run that names nobody is a run to try shorter,
// which is what leaves "im Example hello there" saying "hello there" --
// "Example hello" is nobody.
//
// An ambiguous run is not.  It is returned as it stands rather than
// retried shorter, and it has to be: Find matches on prefixes, so
// everybody "a b" could be "a" could be too, and every shorter run is
// ambiguous in at least as many ways.  Falling back would replace a
// refusal naming two people with a refusal naming five.
//
// The region is asked once, after every run has failed against what is
// already known, for the reason whoNear asks it at all -- and once
// rather than per run, since one answer fills the cache for all of
// them.  The grid is asked last, run by run in the same order, and only
// once the region has had its turn: see onTheGrid for why a search
// answers these commands at all, and what it is allowed to answer.
// Why: doc/slsh.md#a-name-at-the-front-of-a-line
func (sh *Shell) whoAndRest(ctx context.Context, out io.Writer, args []string) (msg.UUID, string, []string, error) {
	if len(args) == 0 {
		return msg.UUID{}, "", nil, fmt.Errorf("nobody was named")
	}
	n := min(maxNameWords, len(args))

	var last error
	for pass := range 2 {
		for k := n; k >= 1; k-- {
			id, name, err := sh.whoKnown(ctx, strings.Join(args[:k], " "))
			if err == nil {
				return id, name, args[k:], nil
			}
			var unknown *unknownName
			if !errors.As(err, &unknown) {
				return msg.UUID{}, "", nil, err
			}
			// The shortest run is the one a person meant as a name, so
			// its refusal is the one worth printing if it comes to that.
			last = err
		}
		if pass == 0 {
			if _, err := sh.s.Nearby(ctx); err != nil {
				break
			}
		}
	}

	runs := make([]string, 0, n)
	for k := n; k >= 1; k-- {
		runs = append(runs, strings.Join(args[:k], " "))
	}
	i, id, name, err := sh.onTheGrid(ctx, out, runs, last)
	if err != nil {
		return msg.UUID{}, "", nil, err
	}
	return id, name, args[n-i:], nil
}

// who turns what was typed into somebody: a uuid, the number from the
// last listing, a name the session has heard, somebody standing in the
// region, or -- by the whole of their name and nothing less -- somebody
// the grid's search knows.  It is whoNear and then onTheGrid, which say
// why each of those is asked.
func (sh *Shell) who(ctx context.Context, out io.Writer, want string) (msg.UUID, string, error) {
	id, name, err := sh.whoNear(ctx, want)
	var unknown *unknownName
	if !errors.As(err, &unknown) {
		return id, name, err
	}
	_, id, name, err = sh.onTheGrid(ctx, out, []string{want}, err)
	return id, name, err
}

// whoNear is who without the grid: a uuid, the number from the last
// listing, a name the session has heard, or somebody standing in the
// region.
//
// The region is asked second, and only when what the session has heard
// names nobody: a shell that has just started has heard of nobody, and
// the answer is somebody standing there, matched by the same rules, so
// it is not a guess.  Asking first would make the case that already
// works pay a round trip.  The grid is not asked here, because its two
// callers take different answers from it: see whoOrSearch and
// onTheGrid.
// Why: doc/slsh.md#asking-the-region-for-a-name
func (sh *Shell) whoNear(ctx context.Context, want string) (msg.UUID, string, error) {
	id, name, err := sh.whoKnown(ctx, want)
	var unknown *unknownName
	if !errors.As(err, &unknown) {
		return id, name, err
	}

	// Asking who is in the region puts their names in the cache, so the
	// second look is the same look: one set of matching rules, and two
	// people whose names differ only in case are still two people rather
	// than a pick.
	//
	// A region that will not answer is left as a region with nobody in
	// it.  The refusal already in hand is what this would have said a
	// moment ago anyway, and turning a mistyped name into a report about
	// the daemon would explain the wrong thing.
	if _, e := sh.s.Nearby(ctx); e != nil {
		return msg.UUID{}, "", err
	}
	return sh.whoKnown(ctx, want)
}

// onTheGrid is the last place a name is looked for by the commands that
// reach somebody -- im, chat, offer, lure, give, invite.  It searches
// for each of runs in turn, longest first, and says which of them named
// somebody.  refusal is what the nearer places said, and what comes
// back, with the reason added, when the grid cannot be asked.
//
// These commands hand something to whoever the name resolves to, so a
// row is taken only when its name IS what was typed: "First Last" in
// any case, or with a dot for the space.  Anything short of that is
// listed, numbered and refused, even when it is the only row, and a
// bare word is never a whole name.  A search that could not be made has found
// nobody, and there is no falling back to lookup's older message, which
// has only a fifteen-second deadline to say that nobody answered.
// Why: doc/slsh.md#searching-the-grid-for-somebody-to-reach
func (sh *Shell) onTheGrid(ctx context.Context, out io.Writer, runs []string, refusal error) (int, msg.UUID, string, error) {
	var unknown *unknownName
	if !errors.As(refusal, &unknown) {
		return 0, msg.UUID{}, "", refusal
	}
	unsearched := func(why error) error {
		return &unknownName{want: unknown.want, unsearched: why}
	}
	if !sh.s.Backend().HasCap(sl.PickerCap) {
		return 0, msg.UUID{}, "", unsearched(
			fmt.Errorf("this session was not given %s", sl.PickerCap))
	}

	// The first run that turned anybody up is the one listed: it is the
	// most of what was typed that resembles somebody, and people whose
	// names are like "Lorn Harbour" are more use than every Lorn there is.
	var near []sl.Found
	var nearFor string
	for i, run := range runs {
		found, err := sh.s.Lookup(ctx, run)
		if err != nil {
			return 0, msg.UUID{}, "", unsearched(err)
		}
		exact := namedExactly(found, run)
		switch {
		case len(exact) == 1:
			return i, exact[0].ID, exact[0].Name, nil
		case len(exact) > 1:
			sh.printFound(out, exact, false)
			return 0, msg.UUID{}, "", fmt.Errorf("%d people on the grid are called %q; "+
				"a number picks one", len(exact), run)
		case near == nil && len(found) > 0:
			near, nearFor = found, run
		}
	}
	if near == nil {
		return 0, msg.UUID{}, "", fmt.Errorf("nobody here or on the grid is called %q; "+
			"try less of the name to see who comes close, or give the key", unknown.want)
	}
	sh.printFound(out, near, false)
	return 0, msg.UUID{}, "", fmt.Errorf("nobody on the grid is called %q, and %s; "+
		"a number picks one", nearFor, resembling(len(near)))
}

// resembling is how many people a search turned up that are not the one
// asked for, as the half of a sentence the refusal under them needs.
func resembling(n int) string {
	if n == 1 {
		return "the one listed has a name like it"
	}
	return fmt.Sprintf("the %d listed have names like it", n)
}

// namedExactly is the rows of a search whose name is what was typed,
// with a dot read as the space it stands for in a username: "Example
// Resident", "example resident" and "example.resident" are all one
// person's whole name, where "example" alone is not.  See onTheGrid.
func namedExactly(found []sl.Found, want string) []sl.Found {
	whole := strings.Join(strings.Fields(strings.ReplaceAll(want, ".", " ")), " ")
	if !strings.Contains(whole, " ") {
		return nil
	}
	var out []sl.Found
	for _, f := range found {
		if strings.EqualFold(f.Name, whole) {
			out = append(out, f)
		}
	}
	return out
}

// whoKnown is who a name means among what this shell already has: a
// uuid, a number from the last listing, or a name the session has
// heard.  It asks the region nothing, which is what lets whoNear ask
// it once and whoAndRest ask it once for a whole line of words.
func (sh *Shell) whoKnown(ctx context.Context, want string) (msg.UUID, string, error) {
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
		return msg.UUID{}, "", &unknownName{want: want}
	default:
		names := make([]string, 0, len(hits))
		for _, id := range hits {
			names = append(names, sh.s.NameOr(id))
		}
		return msg.UUID{}, "", &ambiguousName{want: want, names: names}
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
