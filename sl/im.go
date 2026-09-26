package sl

// Instant messages, and everything that rides on them.
//
// ImprovedInstantMessage is not one message but a dozen: a private
// remark, a group notice, an inventory offer, a teleport lure, a
// friendship offer.  Which it is depends on the Dialog field, so this
// is the one place that has to know the numbers, and callers see the
// kinds they asked for.
//
// Friendship lives here for that reason: an offer is an instant message
// with dialog 38, and accepting it is a message of its own that must
// carry the id the offer arrived with.

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/msg"
)

// Instant message dialogs, from the viewer's EInstantMessage.  These
// are the ones this package acts on; the rest arrive with their number
// intact and nothing happens.
const (
	DialogMessage           = 0
	DialogMessageBox        = 1
	DialogGroupInvitation   = 3
	DialogInventoryOffered  = 4
	DialogInventoryAccepted = 5
	DialogInventoryDeclined = 6
	DialogSessionSend       = 17

	// DialogFromTask is a script's llInstantMessage.  From is the
	// object's OWNER, ID is the object, and FromName is whatever the
	// object is called, which can be anybody's name.
	// See doc/im-senders.md.
	DialogFromTask = 19

	// DialogDoNotDisturbAutoResponse is the reply a viewer sends by
	// itself to a message that reached somebody set to do not disturb.
	DialogDoNotDisturbAutoResponse = 20

	DialogTeleportLure       = 22
	DialogLureAccepted       = 23
	DialogGodlikeLure        = 25
	DialogTeleportRequest    = 26
	DialogFriendshipOffered  = 38
	DialogFriendshipAccepted = 39
	DialogFriendshipDeclined = 40
	DialogTypingStart        = 41
	DialogTypingStop         = 42
)

// IM is one instant message, of whatever kind.
type IM struct {
	At time.Time

	From     msg.UUID
	FromName string
	Text     string

	// To is who it was addressed to.  For a message that arrived it is
	// this avatar; for one this avatar SENT -- see Mine -- it is the
	// other person, and is the only thing saying which conversation the
	// line belongs to.
	To msg.UUID

	// Dialog says what kind it is.  A caller that only wants
	// conversation should check for DialogMessage; the rest range
	// from group notices to teleport lures.
	Dialog uint8

	// ID is what the message carried in its id field, which means
	// different things per dialog: the conversation for a private
	// message, the transaction for a friendship offer, the folder
	// for an inventory offer, the object for DialogFromTask.
	ID msg.UUID

	// Group is set when it came from a group rather than a person.
	Group bool

	// Bucket is the message's binary bucket, undecoded, which is
	// where an inventory offer puts the asset type and id.
	Bucket []byte

	// Mine says this avatar sent it, from another client of the same
	// session, and Via names that client.
	//
	// The grid does not echo what an avatar says: a viewer shows your
	// own remarks because it composed them.  So two clients on one
	// session each saw everything the grid sent and nothing the other
	// said, and somebody watching a conversation through slsh while a
	// daemon answered for the same avatar saw only the half they did
	// not write.  slgod now relays what a client sends to the others,
	// and this is how one arrives.
	//
	// Conversation is FALSE for these, deliberately.  It means
	// "somebody is talking to this avatar", and a line this avatar
	// sent is not that however much it looks like one -- the whole
	// point of the field is that From is us.  Anything that answers
	// conversation would otherwise answer itself, which for a daemon
	// with a model behind it is not a display fault but a loop.  A
	// caller that wants both directions asks for Spoken and looks at
	// this.
	Mine bool
	Via  string
}

// Conversation reports whether this is somebody talking, as opposed to
// the system or a script using the same message to carry something
// else.
func (m *IM) Conversation() bool { return m.Spoken() && !m.Mine }

// Spoken reports whether this is conversation in either direction:
// somebody talking to this avatar, or this avatar talking to somebody
// through another client.
//
// This is what a program showing a conversation wants -- a transcript
// with one side missing is what this whole arrangement exists to fix --
// and Conversation is what a program ANSWERING one wants.  The two are
// deliberately different questions with different names, because the
// cost of confusing them falls entirely on the second: something that
// answers its own remarks talks to itself for ever.
//
// A script's message is not spoken: its name is the object's and its
// From is the owner, who did not write it.  Nor is a do-not-disturb
// auto response, which the far viewer sent by itself.
func (m *IM) Spoken() bool {
	switch m.Dialog {
	case DialogMessage, DialogMessageBox:
		return !m.Group && !m.From.IsZero()
	}
	return false
}

// imSub is a subscription to instant messages.
type imSub struct {
	ch      chan *IM
	dropped atomic.Uint64
}

// DefaultIMDepth is the buffer a subscription gets when none is asked
// for.
const DefaultIMDepth = 64

// IMs returns a channel of instant messages, closed when StopIMs is
// called or the session ends.
//
// Every kind arrives, because deciding which are interesting is the
// caller's business: a chat program wants conversation, a probe may
// want to see a teleport lure land.
func (w *Session) IMs(depth int) <-chan *IM {
	if depth <= 0 {
		depth = DefaultIMDepth
	}
	sub := &imSub{ch: make(chan *IM, depth)}
	if !w.addSub(func() { w.imSubs[sub.ch] = sub }) {
		close(sub.ch)
	}
	return sub.ch
}

// StopIMs closes a subscription, and returns once it is closed.
func (w *Session) StopIMs(ch <-chan *IM) {
	w.onReader(func() {
		if s := w.imSubs[ch]; s != nil {
			delete(w.imSubs, ch)
			close(s.ch)
		}
	})
}

// IMsDropped is how many a subscription missed because its buffer was
// full.
func (w *Session) IMsDropped(ch <-chan *IM) uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	if s := w.imSubs[ch]; s != nil {
		return s.dropped.Load()
	}
	return 0
}

// SendIM sends a private message to one person.
func (w *Session) SendIM(ctx context.Context, to msg.UUID, text string) error {
	m := w.im(to, DialogMessage, text)
	m.MessageBlock.ID = imSessionID(w.me, to)
	return w.Send(ctx, m)
}

// Typing tells somebody this avatar is writing to them, or has
// stopped.
//
// It is the same message an ordinary remark travels on, with a dialog
// that carries no text.  A viewer sends one when the first key is
// pressed and the other when the message goes or the field is cleared,
// and the far end shows "typing..." in between.
//
// Nothing is obliged to send these and nothing breaks if they are
// missed: the far end times its own indicator out.  What they are for
// is the pause before an answer reading as somebody writing rather than
// as nobody there.
func (w *Session) Typing(ctx context.Context, to msg.UUID, on bool) error {
	dialog := uint8(DialogTypingStop)
	if on {
		dialog = DialogTypingStart
	}
	m := w.im(to, dialog, "typing")
	m.MessageBlock.ID = imSessionID(w.me, to)
	return w.Send(ctx, m)
}

// imSessionID is the id a message between two people carries.
//
// The viewer computes it rather than inventing one, so that both ends
// agree without being told: the two agent ids exclusive ored together.
// Getting it wrong does not stop the message arriving, but it lands in
// a conversation the other end thinks is new.
func imSessionID(a, b msg.UUID) msg.UUID {
	var out msg.UUID
	for i := range out {
		out[i] = a[i] ^ b[i]
	}
	return out
}

// im builds a message with the fields every kind needs.
//
// Position and region are what lets the far end offer a teleport to
// where it was sent from.  Neither is required and neither is worth
// failing over, so a session that does not know them sends zeros.
func (w *Session) im(to msg.UUID, dialog uint8, text string) *msg.ImprovedInstantMessage {
	m := &msg.ImprovedInstantMessage{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.MessageBlock.ToAgentID = to
	m.MessageBlock.Dialog = dialog
	m.MessageBlock.Offline = 0 // IM_ONLINE
	m.MessageBlock.FromAgentName = append([]byte(w.Info().AvatarName), 0)
	m.MessageBlock.Message = append([]byte(text), 0)
	m.MessageBlock.ID = randomUUID()

	if where, err := w.Where(context.Background()); err == nil {
		m.MessageBlock.Position = where.Position
	}
	if r, known, err := w.b.Region(context.Background()); err == nil && known {
		m.MessageBlock.RegionID = r.ID
	}
	return m
}

// ------------------------------------------------------------ friendship

// Offer is somebody asking to be a friend.
//
// The transaction is the id the offer arrived with, and answering means
// sending it back: the simulator has no other way to tell which offer
// is being answered, and it is not derived from anything, so an offer
// nobody kept the id of can never be accepted.
type Offer struct {
	At          time.Time
	From        msg.UUID
	Name        string
	Transaction msg.UUID

	// Recorded says it arrived before this session attached, and is
	// known only because slgod kept it.  See offers.go.
	Recorded bool

	// key is slgod's name for it, which is empty for an offer that
	// did not come through a daemon keeping a record.
	key string

	w *Session
}

func (o *Offer) String() string { return o.Name + " offers friendship" }

// Accept forms the friendship.
//
// The grid tells the side that offered -- an instant message and then
// an OnlineNotification -- and tells the side that accepted nothing
// whatsoever, on the grounds that it was the one that did it.  So the
// friend is recorded here as well, or the friend list stays wrong until
// the next login.  A viewer has the same hole and fills it the same
// way.
//
// The folder is where the calling card would go.  A viewer sends its
// Calling Cards folder; this sends zero, which the simulator accepts --
// the friendship forms either way and the calling card is the part that
// does not happen.
func (o *Offer) Accept(ctx context.Context) error {
	undo, err := o.w.answering(ctx, o.key, "accepted")
	if err != nil {
		return err
	}
	m := &msg.AcceptFriendship{}
	m.AgentData.AgentID, m.AgentData.SessionID = o.w.agentBlock()
	m.TransactionBlock.TransactionID = o.Transaction
	m.FolderData = []msg.AcceptFriendship_FolderData{{}}
	if err := o.w.Send(ctx, m); err != nil {
		undo()
		return err
	}
	o.w.forget(o.From)
	return o.w.b.NoteFriend(ctx, o.From, true)
}

// Decline refuses it.  The simulator has to be told, or the offer stays
// pending on the other side.
func (o *Offer) Decline(ctx context.Context) error {
	undo, err := o.w.answering(ctx, o.key, "declined")
	if err != nil {
		return err
	}
	m := &msg.DeclineFriendship{}
	m.AgentData.AgentID, m.AgentData.SessionID = o.w.agentBlock()
	m.TransactionBlock.TransactionID = o.Transaction
	if err := o.w.Send(ctx, m); err != nil {
		undo()
		return err
	}
	o.w.forget(o.From)
	return nil
}

// ------------------------------------------------------------ inventory

// InventoryOffer is somebody offering an item or a folder.
//
// GiveToAvatar makes one of these on the other side.  Nothing arrives
// in inventory until it is accepted: the offer is an instant message
// and the answer is another one, quoting the same transaction.
type InventoryOffer struct {
	At       time.Time
	From     msg.UUID
	FromName string

	// Name is what the giver called it, which is the message text.
	Name string

	// Asset is the kind of thing, and Item its id, both read out of
	// the binary bucket.  A FOLDER is offered as AssetCategory, and
	// then Item is the folder.
	Asset AssetType
	Item  msg.UUID

	// Transaction is the offer's id, which the answer has to quote or
	// the simulator will not match it to anything.
	Transaction msg.UUID

	// Recorded says it arrived before this session attached, and is
	// known only because slgod kept it.  See offers.go.
	Recorded bool

	// key is slgod's name for it; see Offer.
	key string

	w *Session
}

func (o *InventoryOffer) String() string {
	return fmt.Sprintf("%s offers %q", o.FromName, o.Name)
}

// InventoryOfferFrom reads an offer out of an instant message, and says
// whether it was one.
func InventoryOfferFrom(im *IM) (*InventoryOffer, bool) {
	if im.Dialog != DialogInventoryOffered || len(im.Bucket) < 17 {
		return nil, false
	}
	o := &InventoryOffer{
		At:          im.At,
		From:        im.From,
		FromName:    im.FromName,
		Name:        im.Text,
		Asset:       AssetType(int8(im.Bucket[0])),
		Transaction: im.ID,
	}
	copy(o.Item[:], im.Bucket[1:17])
	return o, true
}

// InventoryOffers are the offers waiting for an answer, oldest first.
//
// Kept rather than only delivered, for the same reason friendship
// offers are: whoever is listening may not be ready to answer, and the
// transaction id cannot be recovered afterwards.  An offer nobody kept
// the id of can never be accepted.
func (w *Session) InventoryOffers() []*InventoryOffer {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]*InventoryOffer, 0, len(w.invOffers))
	for _, o := range w.invOffers {
		out = append(out, o)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].At.Before(out[j-1].At); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// InventoryOffersFor returns the waiting offers a name could mean,
// oldest first.  An empty name means all of them.
//
// The whole of a name -- the item's or the giver's -- wins outright
// over part of one, because somebody who typed all of it has already
// said which they mean, and an item whose name is the beginning of
// another's would otherwise never be nameable on its own.
//
// The item's whole name is matched exactly, in the case it has, as
// PickNamed matches an inventory name, and the giver's ignoring case,
// as a person's name is.  Part of a name is a search and ignores case,
// but an item whose name is the whole of this one in another case is
// left out of it: that is another name, and taking it here would be
// taking it as if it had been typed.  Why: doc/names.md
//
// Nothing here picks between the matches, and that is the point: this
// used to hand back the first hit, so two offers under the one name
// answered the older of them and said nothing whatever about the other.
// Whoever is asking decides what more than one means.
func (w *Session) InventoryOffersFor(name string) []*InventoryOffer {
	os := w.InventoryOffers()
	if name == "" {
		return os
	}
	lower := strings.ToLower(name)
	var whole, part []*InventoryOffer
	for _, o := range os {
		switch {
		case o.Name == name || strings.EqualFold(o.FromName, name):
			whole = append(whole, o)
		case strings.EqualFold(o.Name, name):
			// Another spelling of the whole name: neither.
		case strings.Contains(strings.ToLower(o.Name), lower):
			part = append(part, o)
		}
	}
	if len(whole) > 0 {
		return whole
	}
	return part
}

// forgetOffer drops one that has been answered.
func (w *Session) forgetOffer(t msg.UUID) {
	w.mu.Lock()
	delete(w.invOffers, t)
	w.mu.Unlock()
}

// Accept takes the offer up, putting what arrives in a folder of our
// choosing.  A zero folder means the default one for that kind of
// thing, which is what a viewer does when the person clicks Accept
// rather than dragging it somewhere.
//
// Another client of the same avatar may have answered it already, and
// then nothing is sent and the error is an *AnsweredError saying who;
// see offers.go.
func (o *InventoryOffer) Accept(ctx context.Context, into msg.UUID) error {
	undo, err := o.w.answering(ctx, o.key, "accepted")
	if err != nil {
		return err
	}
	if err := o.w.AcceptInventoryOffer(ctx, o, into); err != nil {
		undo()
		return err
	}
	o.w.forgetOffer(o.Transaction)
	return nil
}

// Decline refuses it, unless another client got there first, as for
// Accept.
func (o *InventoryOffer) Decline(ctx context.Context) error {
	undo, err := o.w.answering(ctx, o.key, "declined")
	if err != nil {
		return err
	}
	if err := o.w.DeclineInventoryOffer(ctx, o); err != nil {
		undo()
		return err
	}
	o.w.forgetOffer(o.Transaction)
	return nil
}

// AcceptInventoryOffer takes an offer up, putting what arrives in a
// folder of our choosing.
//
// The answer has to quote the offer's transaction id, which is the only
// thing tying it to the offer; an answer with a fresh id is ignored and
// the offer stays open for ever.  A zero folder means the default one
// for that kind of thing, which is what a viewer does when the person
// clicks Accept rather than dragging it somewhere.
//
// Whether the item actually arrives is a separate question -- the
// simulator does the moving, and says nothing about it -- so a caller
// that needs to know looks in inventory afterwards.
//
// The offer's own Accept is what a caller normally wants.  This one
// sends the answer and leaves the offer where it was, so anything
// listing what is still waiting goes on offering to answer it: that is
// how "answer N" in slsh came to send a second acceptance of an offer
// already taken.
func (w *Session) AcceptInventoryOffer(ctx context.Context, o *InventoryOffer, into msg.UUID) error {
	dialog := uint8(DialogInventoryAccepted)
	m := w.im(o.From, dialog, "")
	// The transaction is the offer's, not a new one.
	m.MessageBlock.ID = o.Transaction
	if !into.IsZero() {
		m.MessageBlock.BinaryBucket = into[:]
	}
	return w.Send(ctx, m)
}

// DeclineInventoryOffer refuses one.  Saying so matters: an offer left
// unanswered stays pending, and the giver is told nothing either way.
//
// Decline is the one to reach for; this leaves the offer waiting, the
// same trap AcceptInventoryOffer describes.
func (w *Session) DeclineInventoryOffer(ctx context.Context, o *InventoryOffer) error {
	m := w.im(o.From, DialogInventoryDeclined, "")
	m.MessageBlock.ID = o.Transaction
	return w.Send(ctx, m)
}

// ------------------------------------------------------------ friendship

// OfferFriendship asks somebody to be a friend.
func (w *Session) OfferFriendship(ctx context.Context, to msg.UUID, text string) error {
	if text == "" {
		text = "Would you be my friend?"
	}
	return w.Send(ctx, w.im(to, DialogFriendshipOffered, text))
}

// Offers returns the friendship offers waiting for an answer, oldest
// first.
func (w *Session) Offers() []*Offer {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]*Offer, 0, len(w.offers))
	for _, o := range w.offers {
		out = append(out, o)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].At.Before(out[j-1].At); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// OfferFrom returns the offer somebody made, if one is waiting.
func (w *Session) OfferFrom(id msg.UUID) (*Offer, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	o, ok := w.offers[id]
	return o, ok
}

func (w *Session) forget(from msg.UUID) {
	w.mu.Lock()
	delete(w.offers, from)
	w.mu.Unlock()
}

// FriendIDs is the friend list as ids, and nothing else.
//
// This is the whole of the list as the grid keeps it: a friendship is
// an id, and the names are something asked for afterwards.  FriendList
// asks, and waits up to three seconds for the answers, which is the
// right price for a listing that prints the names and quite the wrong
// one for a caller that only has to decide whether an id it already
// holds is a friend.  slsh's "map" picks its friends out of the
// picture that way, on every picture, and a map that stopped for three
// seconds to look up names it will never print would be a worse
// command than one with no colour in it.
func (w *Session) FriendIDs(ctx context.Context) ([]msg.UUID, error) {
	fs, err := w.b.Friends(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]msg.UUID, 0, len(fs))
	for _, f := range fs {
		ids = append(ids, f.ID)
	}
	return ids, nil
}

// FriendList is the friend list with names filled in.
func (w *Session) FriendList(ctx context.Context) ([]Person, error) {
	ids, err := w.FriendIDs(ctx)
	if err != nil {
		return nil, err
	}
	w.Names(ctx, ids, 3*time.Second)

	out := make([]Person, 0, len(ids))
	for _, id := range ids {
		out = append(out, Person{ID: id, Name: w.NameOr(id)})
	}
	return out, nil
}

// OnlineFriends is FriendList, kept to those logged in.
func (w *Session) OnlineFriends(ctx context.Context) ([]Person, error) {
	fs, err := w.b.Friends(ctx)
	if err != nil {
		return nil, err
	}
	var ids []msg.UUID
	for _, f := range fs {
		if f.Online {
			ids = append(ids, f.ID)
		}
	}
	w.Names(ctx, ids, 3*time.Second)

	out := make([]Person, 0, len(ids))
	for _, id := range ids {
		out = append(out, Person{ID: id, Name: w.NameOr(id)})
	}
	return out, nil
}

// instantMessage routes one arriving message.
func (w *Session) instantMessage(raw *client.Message, m *msg.ImprovedInstantMessage) {
	b := m.MessageBlock
	// When it arrived, which for one out of slgod's record was some
	// while ago and is worth saying: the listing is in arrival order,
	// and an offer kept overnight is not the newest thing waiting.
	at := time.Now()
	if raw != nil && raw.Recorded && !raw.At.IsZero() {
		at = raw.At
	}
	im := &IM{
		At:       at,
		From:     m.AgentData.AgentID,
		FromName: trimNul(b.FromAgentName),
		Text:     trimNul(b.Message),
		To:       b.ToAgentID,
		Dialog:   b.Dialog,
		ID:       b.ID,
		Group:    b.FromGroup,
		Bucket:   b.BinaryBucket,
	}
	if raw != nil && raw.FromClient != "" {
		im.Mine, im.Via = true, raw.FromClient
	}
	// slgod's name for it when it is an offer the daemon is keeping,
	// and whether it came out of the record rather than off the wire.
	var key string
	var recorded bool
	if raw != nil {
		key, recorded = raw.Offer, raw.Recorded
	}
	w.mu.Lock()
	keep := w.keepableLocked(key, recorded)
	w.mu.Unlock()

	// Everything below this line is about a message that ARRIVED: a
	// name to learn, an offer to keep, a friendship to record.  None of
	// it applies to one this avatar sent.  Learning from an echo would
	// file our own name under whoever we wrote to, and keeping an offer
	// from one would have a session hold, and be able to accept, an
	// offer it had itself made to somebody else.
	if im.Mine {
		w.deliverIM(im)
		return
	}
	// Two kinds carry a name that is not the sender id's.  A group
	// invitation's id is the group and its name whoever invited (see
	// invite.go); a script's message has its owner's id and the
	// object's name.  Learning either pair files a wrong name under
	// that id, and every name printed for it afterwards is wrong.
	if b.Dialog != DialogGroupInvitation && b.Dialog != DialogFromTask {
		w.learn(im.From, im.FromName)
	}

	// A friendship offer is worth keeping rather than only
	// delivering: whoever is listening may not be ready to answer,
	// and the transaction id cannot be recovered afterwards.
	if keep && b.Dialog == DialogFriendshipOffered {
		name := im.FromName
		if name == "" {
			name = w.NameOr(im.From)
		}
		w.mu.Lock()
		if w.offers == nil {
			w.offers = map[msg.UUID]*Offer{}
		}
		w.offers[im.From] = &Offer{
			At: im.At, From: im.From, Name: name, Transaction: b.ID,
			Recorded: recorded, key: key, w: w,
		}
		w.mu.Unlock()
	}

	// A teleport offer, kept for the same reason again: the lure id
	// answers it and nothing else does.  See lure.go.
	if keep && b.Dialog == DialogTeleportLure {
		w.noteLure(im, key, recorded)
	}

	// Somebody asking to be sent one, which is the other direction and
	// is answered by OFFERING rather than by accepting.  Kept because
	// it is a question waiting on a person, like the rest of these; it
	// carries no id of its own, so who asked is the whole of it.
	if keep && b.Dialog == DialogTeleportRequest {
		w.noteTeleportRequest(im, key, recorded)
	}

	// A group invitation, kept for the same reason again: the transaction
	// answers it and nothing else does, and a group with enrolment
	// closed cannot be joined any other way.  See invite.go.
	if keep && b.Dialog == DialogGroupInvitation {
		w.noteInvitation(im, key, recorded)
	}

	// An inventory offer is kept for the same reason: the transaction
	// id is the only thing that can answer it, and it is not derivable.
	if o, ok := InventoryOfferFrom(im); keep && ok {
		o.w = w
		o.Recorded, o.key = recorded, key
		if o.FromName == "" {
			o.FromName = w.NameOr(o.From)
		}
		w.mu.Lock()
		if w.invOffers == nil {
			w.invOffers = map[msg.UUID]*InventoryOffer{}
		}
		w.invOffers[o.Transaction] = o
		w.mu.Unlock()
	}

	// Out of the record, it is not news: it is kept above and goes no
	// further.  A subscriber that was handed it would announce as
	// arriving now an offer made while nobody was listening, and one
	// that acts on offers would act on it as though it had.  A program
	// that wants to know what was waiting when it attached asks for the
	// lists, where these are marked Recorded.
	if recorded {
		return
	}

	// Being told an offer of ours was taken up is the only notice
	// that side gets in time to be useful.
	if b.Dialog == DialogFriendshipAccepted {
		w.b.NoteFriend(context.Background(), im.From, true)
	}

	w.deliverIM(im)
}

// deliverIM hands one to everybody subscribed.
func (w *Session) deliverIM(im *IM) {
	w.mu.Lock()
	subs := make([]*imSub, 0, len(w.imSubs))
	for _, s := range w.imSubs {
		subs = append(subs, s)
	}
	w.mu.Unlock()

	for _, s := range subs {
		select {
		case s.ch <- im:
		default:
			s.dropped.Add(1)
		}
	}
}

// DialogName names a dialog number, for saying what arrived.
func DialogName(d uint8) string {
	switch d {
	case DialogMessage:
		return "message"
	case DialogMessageBox:
		return "message box"
	case DialogGroupInvitation:
		return "group invitation"
	case DialogInventoryOffered:
		return "inventory offer"
	case DialogInventoryAccepted:
		return "inventory accepted"
	case DialogInventoryDeclined:
		return "inventory declined"
	case DialogSessionSend:
		return "group message"
	case DialogFromTask:
		return "object message"
	case DialogDoNotDisturbAutoResponse:
		return "do not disturb auto response"
	case DialogTeleportLure:
		return "teleport lure"
	case DialogLureAccepted:
		return "teleport offer accepted"
	case DialogLureDeclined:
		return "teleport offer declined"
	case DialogGodlikeLure:
		return "godlike teleport"
	case DialogTeleportRequest:
		return "teleport request"
	case DialogFriendshipOffered:
		return "friendship offer"
	case DialogFriendshipAccepted:
		return "friendship accepted"
	case DialogFriendshipDeclined:
		return "friendship declined"
	case DialogTypingStart:
		return "typing"
	case DialogTypingStop:
		return "stopped typing"
	}
	return fmt.Sprintf("dialog %d", d)
}
