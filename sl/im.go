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

	"github.com/quark-idlemind/slgo/msg"
)

// Instant message dialogs, from the viewer's EInstantMessage.  These
// are the ones this package acts on; the rest arrive with their number
// intact and nothing happens.
const (
	DialogMessage            = 0
	DialogMessageBox         = 1
	DialogGroupInvitation    = 3
	DialogInventoryOffered   = 4
	DialogInventoryAccepted  = 5
	DialogInventoryDeclined  = 6
	DialogSessionSend        = 17
	DialogBusyAutoResponse   = 19
	DialogTeleportLure       = 22
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

	// Dialog says what kind it is.  A caller that only wants
	// conversation should check for DialogMessage; the rest range
	// from group notices to teleport lures.
	Dialog uint8

	// ID is what the message carried in its id field, which means
	// different things per dialog: the conversation for a private
	// message, the transaction for a friendship offer, the folder
	// for an inventory offer.
	ID msg.UUID

	// Group is set when it came from a group rather than a person.
	Group bool

	// Bucket is the message's binary bucket, undecoded, which is
	// where an inventory offer puts the asset type and id.
	Bucket []byte
}

// Conversation reports whether this is somebody talking, as opposed to
// the system or a script using the same message to carry something
// else.
func (m *IM) Conversation() bool {
	switch m.Dialog {
	case DialogMessage, DialogMessageBox, DialogBusyAutoResponse:
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
	m.MessageBlock.FromAgentName = append([]byte(w.info.AvatarName), 0)
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
	m := &msg.AcceptFriendship{}
	m.AgentData.AgentID, m.AgentData.SessionID = o.w.agentBlock()
	m.TransactionBlock.TransactionID = o.Transaction
	m.FolderData = []msg.AcceptFriendship_FolderData{{}}
	if err := o.w.Send(ctx, m); err != nil {
		return err
	}
	o.w.forget(o.From)
	return o.w.b.NoteFriend(ctx, o.From, true)
}

// Decline refuses it.  The simulator has to be told, or the offer stays
// pending on the other side.
func (o *Offer) Decline(ctx context.Context) error {
	m := &msg.DeclineFriendship{}
	m.AgentData.AgentID, m.AgentData.SessionID = o.w.agentBlock()
	m.TransactionBlock.TransactionID = o.Transaction
	if err := o.w.Send(ctx, m); err != nil {
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
		case strings.EqualFold(o.Name, name) || strings.EqualFold(o.FromName, name):
			whole = append(whole, o)
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
func (o *InventoryOffer) Accept(ctx context.Context, into msg.UUID) error {
	if err := o.w.AcceptInventoryOffer(ctx, o, into); err != nil {
		return err
	}
	o.w.forgetOffer(o.Transaction)
	return nil
}

// Decline refuses it.
func (o *InventoryOffer) Decline(ctx context.Context) error {
	if err := o.w.DeclineInventoryOffer(ctx, o); err != nil {
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
func (w *Session) instantMessage(m *msg.ImprovedInstantMessage) {
	b := m.MessageBlock
	im := &IM{
		At:       time.Now(),
		From:     m.AgentData.AgentID,
		FromName: trimNul(b.FromAgentName),
		Text:     trimNul(b.Message),
		Dialog:   b.Dialog,
		ID:       b.ID,
		Group:    b.FromGroup,
		Bucket:   b.BinaryBucket,
	}
	// A group invitation is the one kind whose sender is not a person:
	// the id is the group and the agent-name field is whoever invited
	// (see invite.go), so learning the two together would file a
	// person's name under a group's id and every name printed for that
	// group afterwards would be wrong.
	if b.Dialog != DialogGroupInvitation {
		w.learn(im.From, im.FromName)
	}

	// A friendship offer is worth keeping rather than only
	// delivering: whoever is listening may not be ready to answer,
	// and the transaction id cannot be recovered afterwards.
	if b.Dialog == DialogFriendshipOffered {
		name := im.FromName
		if name == "" {
			name = w.NameOr(im.From)
		}
		w.mu.Lock()
		if w.offers == nil {
			w.offers = map[msg.UUID]*Offer{}
		}
		w.offers[im.From] = &Offer{
			At: im.At, From: im.From, Name: name, Transaction: b.ID, w: w,
		}
		w.mu.Unlock()
	}

	// A teleport offer, kept for the same reason again: the lure id
	// answers it and nothing else does.  See lure.go.
	if b.Dialog == DialogTeleportLure {
		w.noteLure(im)
	}

	// A group invitation, kept for the same reason again: the transaction
	// answers it and nothing else does, and a group with enrolment
	// closed cannot be joined any other way.  See invite.go.
	if b.Dialog == DialogGroupInvitation {
		w.noteInvitation(im)
	}

	// An inventory offer is kept for the same reason: the transaction
	// id is the only thing that can answer it, and it is not derivable.
	if o, ok := InventoryOfferFrom(im); ok {
		o.w = w
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

	// Being told an offer of ours was taken up is the only notice
	// that side gets in time to be useful.
	if b.Dialog == DialogFriendshipAccepted {
		w.b.NoteFriend(context.Background(), im.From, true)
	}

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
	case DialogBusyAutoResponse:
		return "busy auto response"
	case DialogTeleportLure:
		return "teleport lure"
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
