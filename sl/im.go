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
	w.onReader(func() { w.imSubs[sub.ch] = sub })
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

// FriendList is the friend list with names filled in.
func (w *Session) FriendList(ctx context.Context) ([]Person, error) {
	fs, err := w.b.Friends(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]msg.UUID, 0, len(fs))
	for _, f := range fs {
		ids = append(ids, f.ID)
	}
	w.Names(ctx, ids, 3*time.Second)

	out := make([]Person, 0, len(fs))
	for _, f := range fs {
		out = append(out, Person{ID: f.ID, Name: w.NameOr(f.ID)})
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
	w.learn(im.From, im.FromName)

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
