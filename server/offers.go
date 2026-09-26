package server

// Offers that arrive while nobody is attached.
//
// An offer -- a teleport, an item, friendship, a place in a group -- is
// an instant message carrying the one id that answers it, and it arrives
// once.  So the daemon keeps them, per avatar, until they are dealt
// with:
//
//   - The record is filled from the relay, before the fan-out and before
//     the early return for a session with nobody attached, like the
//     teleport and seat answers in home.go and seat.go.  It is the
//     session's business whether or not anybody is listening.
//   - A client is handed what is still waiting when it attaches, in the
//     Attached frame itself; see Stream and Hosted.attach for why there
//     rather than by a call of its own.
//   - A client that deals with one says so first, through Handled, and
//     every other client is told.  Saying so first is what stops two
//     clients on one avatar both answering the same offer: the second to
//     ask is told who got there first and sends nothing.
//   - An answer that goes out without being announced -- a client too
//     old to call Handled, a viewer on the login endpoint -- is noticed
//     on its way to the circuit and does the same.  See noteSent.
//
// # What is kept
//
// The five kinds that arrive as instant messages and wait on a person:
// a teleport offered (dialog 22), a request to be offered one (26), an
// item handed over (4), an offer of friendship (38) and an invitation
// into a group (3).  Script dialogs and permission requests are not
// kept.
//
// # What makes one unanswerable, and so drops it
//
// Being answered, first: by Handled, or by noteSent seeing the answer go.
//
// Being replaced, second.  A second teleport offer from one person
// supersedes the first, as does a second friendship offer or request,
// and a second invitation into one group; the client keeps one of each
// the same way (see sl/lure.go and sl/invite.go), and the record agrees
// with it.
//
// Room, third.  offerLimit of them are kept and the oldest goes first;
// the record says how many have gone, so a client can say its listing
// is not the whole of it.
//
// Nothing else drops one: not a timer, not a change of region, and not
// the daemon re-establishing the session.
//
// The record lives in memory and goes with the process.  An offer made
// while the daemon was down is not in it, and Since says where it starts.
// Why: doc/daemon.md#offers-kept-while-nobody-is-attached

import (
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// offerLimit is how many unanswered offers are kept for one avatar.
//
// More than anybody works through in one sitting, and small enough that
// the whole record can go in the frame every attach begins with: at a
// kilobyte or so an instant message, it is a tenth of a megabyte at
// worst.  The viewer keeps two and a half times as many persistent
// notifications, of a much broader set (MaxPersistentNotifications).
const offerLimit = 100

// answeredMemory is how many dealt-with offers are remembered, so that a
// second client asking to deal with one can be told who got there first.
// Only the most recent matter: the question comes within moments of the
// answer, from somebody looking at a list the notice has not yet reached.
const answeredMemory = 64

// The kinds kept, which are also the front of each key.
const (
	offerLure      = "lure"
	offerTPRequest = "tprequest"
	offerInventory = "inventory"
	offerFriend    = "friendship"
	offerGroup     = "group"
)

// The instant message dialogs this reads, from the viewer's
// llinstantmessage.h.  Numbers rather than sl's names because the server
// does not import sl, and should not: sl is a client.
const (
	imGroupInvitation        = 3
	imInventoryOffered       = 4
	imInventoryAccepted      = 5
	imInventoryDeclined      = 6
	imLureUser               = 22
	imLureDeclined           = 24
	imTeleportRequest        = 26
	imGroupInvitationAccept  = 35
	imGroupInvitationDecline = 36
	imFriendshipOffered      = 38
)

var imID = msg.IDOf(&msg.ImprovedInstantMessage{})

// offer is one kept offer, as it arrived.
type offer struct {
	// key is the server's name for it, which clients quote back.  It is
	// made from the offer's own contents -- the kind and the id that
	// answers it -- so that the same offer relayed twice is named the
	// same both times, and an answer seen on its way out can be matched
	// to it without anybody having said which it was.
	key  string
	kind string

	// from is who made it, which is what a later offer of the same kind
	// replaces it by.
	from msg.UUID

	// seq is the sequence number it arrived under, which is how a
	// retransmission is told from somebody asking twice.  The relay
	// runs ahead of duplicate suppression, so it sees both.
	seq uint32

	at   time.Time
	body []byte
}

// answered is an offer somebody dealt with, and who and how.
type answered struct {
	offer   *offer
	how, by string
	at      time.Time
}

func (a *answered) pb() *pb.OfferHandled {
	return &pb.OfferHandled{Offer: a.offer.key, How: a.how, By: a.by, At: a.at.UnixMicro()}
}

// offerLog is one avatar's kept offers.
type offerLog struct {
	mu       sync.Mutex
	since    time.Time
	kept     []*offer // oldest first
	answered []*answered
	evicted  uint32
}

func newOfferLog(since time.Time) *offerLog { return &offerLog{since: since} }

// offerLog is this avatar's record, made on first use for a Hosted that
// was built by hand -- which the tests do -- and at creation otherwise,
// so that Since is when the session started rather than when somebody
// first asked.
func (h *Hosted) offerLog() *offerLog {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.offers == nil {
		h.offers = newOfferLog(time.Now())
	}
	return h.offers
}

// offerIn reads whether a message is an offer this server keeps.
//
// The conditions are the client's own for keeping one (sl/im.go), so
// that the record never holds something no client would list: an item
// offer whose bucket is too short to name the item, or an invitation
// with nobody to answer.
func offerIn(p *msg.Packet) (*offer, bool) {
	if p.ID != imID {
		return nil, false
	}
	body := packetBody(p)
	if body == nil {
		return nil, false
	}
	var m msg.ImprovedInstantMessage
	if err := m.Decode(body); err != nil {
		return nil, false
	}
	b := m.MessageBlock
	o := &offer{from: m.AgentData.AgentID, seq: p.Header.Sequence, at: p.At, body: body}
	if o.at.IsZero() {
		o.at = time.Now()
	}
	switch b.Dialog {
	case imLureUser:
		o.kind, o.key = offerLure, offerKey(offerLure, b.ID)
	case imTeleportRequest:
		// A request carries a null id (llavataractions.cpp), so who
		// asked is the whole of it: asking twice is asking.
		o.kind, o.key = offerTPRequest, offerKey(offerTPRequest, o.from)
	case imInventoryOffered:
		if len(b.BinaryBucket) < 17 {
			return nil, false
		}
		o.kind, o.key = offerInventory, offerKey(offerInventory, b.ID)
	case imFriendshipOffered:
		o.kind, o.key = offerFriend, offerKey(offerFriend, b.ID)
	case imGroupInvitation:
		// The sender IS the group, and an invitation from nobody has
		// nowhere to send an answer (sl/invite.go).
		if o.from.IsZero() {
			return nil, false
		}
		o.kind, o.key = offerGroup, offerKey(offerGroup, b.ID)
	default:
		return nil, false
	}
	return o, true
}

func offerKey(kind string, id msg.UUID) string { return kind + ":" + id.String() }

// answersIn reads whether a message this avatar is sending answers
// offers, which ones by key, and how.
//
// Each is the answer the viewer sends and the sl package sends after it:
// an instant message back quoting the offer's id for an item, a lure
// refused and a group invitation, a message of its own for friendship
// and for taking a lure.  A teleport request is answered by offering a
// teleport, which names the people it goes to rather than any id.
func answersIn(p *msg.Packet) (keys []string, how string) {
	body := packetBody(p)
	if body == nil {
		return nil, ""
	}
	switch p.ID {
	case imID:
		var m msg.ImprovedInstantMessage
		if m.Decode(body) != nil {
			return nil, ""
		}
		id := m.MessageBlock.ID
		switch m.MessageBlock.Dialog {
		case imInventoryAccepted:
			return []string{offerKey(offerInventory, id)}, "accepted"
		case imInventoryDeclined:
			return []string{offerKey(offerInventory, id)}, "declined"
		case imLureDeclined:
			return []string{offerKey(offerLure, id)}, "declined"
		case imGroupInvitationAccept:
			return []string{offerKey(offerGroup, id)}, "accepted"
		case imGroupInvitationDecline:
			return []string{offerKey(offerGroup, id)}, "declined"
		}
	case msg.IDOf(&msg.AcceptFriendship{}):
		var m msg.AcceptFriendship
		if m.Decode(body) == nil {
			return []string{offerKey(offerFriend, m.TransactionBlock.TransactionID)}, "accepted"
		}
	case msg.IDOf(&msg.DeclineFriendship{}):
		var m msg.DeclineFriendship
		if m.Decode(body) == nil {
			return []string{offerKey(offerFriend, m.TransactionBlock.TransactionID)}, "declined"
		}
	case msg.IDOf(&msg.TeleportLureRequest{}):
		var m msg.TeleportLureRequest
		if m.Decode(body) == nil {
			return []string{offerKey(offerLure, m.Info.LureID)}, "accepted"
		}
	case msg.IDOf(&msg.StartLure{}):
		var m msg.StartLure
		if m.Decode(body) == nil {
			for _, t := range m.TargetData {
				keys = append(keys, offerKey(offerTPRequest, t.TargetID))
			}
			return keys, "answered with a teleport offer"
		}
	}
	return nil, ""
}

// packetBody is a packet's bytes, encoding them if the session kept only
// the decoded message.  Nil when there are none to be had.
func packetBody(p *msg.Packet) []byte {
	if p.Body != nil {
		return p.Body
	}
	if p.Message == nil {
		return nil
	}
	b, err := p.Message.Encode()
	if err != nil {
		return nil
	}
	return b
}

// note keeps one that has arrived, and says what it is called.
func (l *offerLog) note(o *offer) string {
	l.mu.Lock()
	defer l.mu.Unlock()

	for _, k := range l.kept {
		if k.key == o.key {
			// The same offer again.  A retransmission changes nothing;
			// somebody asking a second time is the newer question, so
			// it is what is kept.
			if k.seq != o.seq {
				k.seq, k.at, k.body = o.seq, o.at, o.body
			}
			return k.key
		}
	}
	for i, a := range l.answered {
		if a.offer.key != o.key {
			continue
		}
		// A retransmission of something already dealt with is not a
		// new offer, and keeping it would list an answered offer as
		// waiting.  A second request from somebody whose first was
		// refused is, and it no longer has an answer.
		if a.offer.seq == o.seq {
			return o.key
		}
		l.answered = append(l.answered[:i], l.answered[i+1:]...)
		break
	}

	// A newer offer of the same kind from the same person replaces the
	// older, except for items: two things handed over are two offers.
	if o.kind != offerInventory {
		kept := l.kept[:0]
		for _, k := range l.kept {
			if k.kind == o.kind && k.from == o.from {
				continue
			}
			kept = append(kept, k)
		}
		clear(l.kept[len(kept):])
		l.kept = kept
	}

	l.kept = append(l.kept, o)
	if over := len(l.kept) - offerLimit; over > 0 {
		clear(l.kept[:over])
		l.kept = l.kept[over:]
		l.evicted += uint32(over)
	}
	return o.key
}

// claim takes one out of the record on a client's word that it is
// dealing with it.
//
// claimed is true for the call that took it out.  Otherwise earlier is
// whoever did, when that is remembered, and nil when the record never
// had it or has forgotten -- which says nothing about whether anybody
// answered it, and is why a client goes ahead on nil.
func (l *offerLog) claim(key, how, by string, at time.Time) (claimed bool, earlier *answered) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, k := range l.kept {
		if k.key != key {
			continue
		}
		l.kept = append(l.kept[:i], l.kept[i+1:]...)
		l.answered = append(l.answered, &answered{offer: k, how: how, by: by, at: at})
		if over := len(l.answered) - answeredMemory; over > 0 {
			clear(l.answered[:over])
			l.answered = l.answered[over:]
		}
		return true, nil
	}
	for i := len(l.answered) - 1; i >= 0; i-- {
		if l.answered[i].offer.key == key {
			return false, l.answered[i]
		}
	}
	return false, nil
}

// restore puts back one a client claimed and then could not answer.
//
// It goes back where it was in arrival order, since the order is what a
// listing shows and an offer that had moved to the end would look newer
// than it is.  A later offer of the same kind from the same person that
// arrived meanwhile has already replaced it, and then it stays gone.
func (l *offerLog) restore(key string) (*offer, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := len(l.answered) - 1; i >= 0; i-- {
		o := l.answered[i].offer
		if o.key != key {
			continue
		}
		l.answered = append(l.answered[:i], l.answered[i+1:]...)
		for _, k := range l.kept {
			if k.key == key || (o.kind != offerInventory && k.kind == o.kind && k.from == o.from) {
				return nil, false
			}
		}
		at := len(l.kept)
		for j, k := range l.kept {
			if k.at.After(o.at) {
				at = j
				break
			}
		}
		l.kept = append(l.kept, nil)
		copy(l.kept[at+1:], l.kept[at:])
		l.kept[at] = o
		return o, true
	}
	return nil, false
}

// snapshot is the record as a client is handed it.
func (l *offerLog) snapshot() *pb.OfferRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := &pb.OfferRecord{
		Since:   l.since.UnixMicro(),
		Evicted: l.evicted,
		Limit:   offerLimit,
	}
	for _, o := range l.kept {
		out.Messages = append(out.Messages, o.inbound())
	}
	return out
}

// inbound is a kept offer as it is handed to a client out of the record.
func (o *offer) inbound() *pb.InboundMessage {
	return &pb.InboundMessage{
		Id:         uint32(imID),
		Name:       "ImprovedInstantMessage",
		Body:       o.body,
		Sequence:   o.seq,
		ReceivedAt: o.at.UnixMicro(),
		Offer:      o.key,
		Recorded:   true,
	}
}

// noteOffer keeps what arrived if it is an offer, and says what it is
// called so that the relay can say so too.  Empty for anything else.
func (h *Hosted) noteOffer(p *msg.Packet) string {
	o, ok := offerIn(p)
	if !ok {
		return ""
	}
	return h.offerLog().note(o)
}

// noteSent takes out of the record any offer a message this avatar is
// sending answers, and tells the clients.
//
// This is the backstop, and for a client that called Handled first it
// finds nothing, because Handled took the offer out already.  What it is
// for is an answer that was never announced: a client built before
// Handled existed, a program using the client package directly, or a
// viewer on the login endpoint, all of which answer through this
// session's circuit and none of which said so.  Without it the record
// would go on listing, to everybody who attached afterwards, an offer
// that had been answered.
//
// It runs on the sender's goroutine for every message sent, so it looks
// at the number before anything else.
func (h *Hosted) noteSent(p *msg.Packet) {
	switch p.ID {
	case imID, msg.IDOf(&msg.AcceptFriendship{}), msg.IDOf(&msg.DeclineFriendship{}),
		msg.IDOf(&msg.TeleportLureRequest{}), msg.IDOf(&msg.StartLure{}):
	default:
		return
	}
	keys, how := answersIn(p)
	if len(keys) == 0 {
		return
	}
	log := h.offerLog()
	now := time.Now()
	for _, k := range keys {
		// Nobody to name: the circuit does not say which program a
		// message came from, and guessing would be worse than saying so.
		if claimed, _ := log.claim(k, how, "", now); claimed {
			h.tellHandled(&pb.OfferHandled{Offer: k, How: how, At: now.UnixMicro()})
		}
	}
}

// tellHandled tells every client that could be holding an offer that it
// is no longer waiting.
//
// The same clients an offer is relayed to: those that asked for instant
// messages, since those are the only ones that can have it.  Dropped for
// a client that is behind, like everything else sent to one.
func (h *Hosted) tellHandled(n *pb.OfferHandled) {
	p := &pb.ServerPacket{Body: &pb.ServerPacket_Handled{Handled: n}}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		if c.wants(imID) {
			c.send(p)
		}
	}
}

// relayRestored hands an offer that was put back to every client that
// could hold it, marked as out of the record: it is not news, and a
// client that announced it would be announcing an offer for the second
// time.
func (h *Hosted) relayRestored(o *offer) {
	p := &pb.ServerPacket{Body: &pb.ServerPacket_Message{Message: o.inbound()}}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		if c.wants(imID) {
			c.send(p)
		}
	}
}
