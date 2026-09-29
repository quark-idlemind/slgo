package agent

// What the session has been told that says what a purchase costs and
// who it pays, for the rules a profile puts on buying.  Nothing here is
// asked for on the caller's behalf but a name: a price the session has
// not heard is not known, and a purchase priced by it is refused.
// Why: doc/money.md#what-else-spends-l

import (
	"context"
	"encoding/binary"
	"strings"
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// NameLookup bounds asking the grid for a name.  A refusal waits for
// it, so it is short: a refusal without a name is still a refusal.
const NameLookup = 3 * time.Second

// inviteBucketLen is a group invitation's bucket: an S32 fee in network
// byte order and a role id (llimprocessing.cpp:1502-1518).
const inviteBucketLen = 4 + 16

// dialogGroupInvitation is IM_GROUP_INVITATION.
const dialogGroupInvitation = 3

// keptInvitations bounds the invitations remembered.
const keptInvitations = 64

type invitation struct {
	group msg.UUID
	txn   msg.UUID
	fee   int32
}

type prices struct {
	mu       sync.Mutex
	groupFee map[msg.UUID]int32
	invites  []invitation // oldest first
	names    map[msg.UUID]string
	asking   map[msg.UUID]chan struct{}
}

// keepPrices registers the handlers that keep what prices are told.
func (a *Agent) keepPrices() {
	a.Disp.MustHandle("GroupProfileReply", func(p *msg.Packet) {
		m := p.Message.(*msg.GroupProfileReply)
		a.prices.mu.Lock()
		if a.prices.groupFee == nil {
			a.prices.groupFee = map[msg.UUID]int32{}
		}
		a.prices.groupFee[m.GroupData.GroupID] = m.GroupData.MembershipFee
		a.prices.mu.Unlock()
	}, msg.Inline())
	a.Disp.MustHandle("UUIDNameReply", func(p *msg.Packet) {
		for _, b := range p.Message.(*msg.UUIDNameReply).UUIDNameBlock {
			a.prices.learn(b.ID, strings.TrimSpace(trimNul(b.FirstName)+" "+trimNul(b.LastName)))
		}
	}, msg.Inline())
}

func (pr *prices) learn(id msg.UUID, name string) {
	if name == "" {
		return
	}
	pr.mu.Lock()
	if pr.names == nil {
		pr.names = map[msg.UUID]string{}
	}
	pr.names[id] = name
	ch := pr.asking[id]
	delete(pr.asking, id)
	pr.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

// noteInvitation keeps what a group invitation says joining costs.  A
// bucket of any other shape is not kept: a fee guessed at is money.
func (pr *prices) noteInvitation(im *msg.ImprovedInstantMessage) {
	b := &im.MessageBlock
	if b.Dialog != dialogGroupInvitation || len(b.BinaryBucket) != inviteBucketLen {
		return
	}
	inv := invitation{group: im.AgentData.AgentID, txn: b.ID, fee: int32(binary.BigEndian.Uint32(b.BinaryBucket[:4]))}
	pr.mu.Lock()
	pr.invites = append(pr.invites, inv)
	if n := len(pr.invites) - keptInvitations; n > 0 {
		pr.invites = pr.invites[n:]
	}
	pr.mu.Unlock()
}

// ObjectSale is the owner of the object with this local id, and what
// the region last said it is sold for, -1 if it never did.  False when
// no such object is known here or nothing has said who owns it.
func (a *Agent) ObjectSale(local uint32) (owner msg.UUID, price int, ok bool) {
	o, found := a.Objects().ByLocal(local)
	if !found || o.Owner.IsZero() {
		return msg.UUID{}, 0, false
	}
	price = -1
	if o.Priced {
		price = int(o.SalePrice)
	}
	return o.Owner, price, true
}

// ParcelSale is the owner of the parcel this avatar stands on and what
// it asks for sale and for a pass, if that parcel has this local id.
// The region's other parcels are not described to a session unasked, so
// for those it says false.
func (a *Agent) ParcelSale(local int32) (owner msg.UUID, sale, pass int, ok bool) {
	p := a.Parcel()
	if p == nil || p.LocalID != local || p.Owner.IsZero() {
		return msg.UUID{}, 0, 0, false
	}
	return p.Owner, int(p.SalePrice), int(p.PassPrice), true
}

// GroupFee is what the group's profile last said joining costs, if the
// session has been sent that profile.
func (a *Agent) GroupFee(group msg.UUID) (int, bool) {
	a.prices.mu.Lock()
	defer a.prices.mu.Unlock()
	fee, ok := a.prices.groupFee[group]
	return int(fee), ok
}

// InvitationFee is what an invitation into the group said joining costs:
// the one with this transaction id, or the latest into the group when
// txn is zero.
func (a *Agent) InvitationFee(group, txn msg.UUID) (int, bool) {
	a.prices.mu.Lock()
	defer a.prices.mu.Unlock()
	for i := len(a.prices.invites) - 1; i >= 0; i-- {
		inv := a.prices.invites[i]
		if inv.group == group && (txn.IsZero() || inv.txn == txn) {
			return int(inv.fee), true
		}
	}
	return 0, false
}

// NameOf is an avatar's name, from what the session has heard or, for
// NameLookup, from the grid; "" when neither says.
func (a *Agent) NameOf(ctx context.Context, id msg.UUID) string {
	pr := &a.prices
	pr.mu.Lock()
	if n, ok := pr.names[id]; ok {
		pr.mu.Unlock()
		return n
	}
	if pr.asking == nil {
		pr.asking = map[msg.UUID]chan struct{}{}
	}
	ch, already := pr.asking[id]
	if !already {
		ch = make(chan struct{})
		pr.asking[id] = ch
	}
	pr.mu.Unlock()

	if !already {
		m := &msg.UUIDNameRequest{UUIDNameBlock: []msg.UUIDNameRequest_UUIDNameBlock{{ID: id}}}
		if err := a.Send.SendReliable(ctx, m); err != nil {
			return ""
		}
	}
	ctx, cancel := context.WithTimeout(ctx, NameLookup)
	defer cancel()
	select {
	case <-ch:
	case <-ctx.Done():
	}
	pr.mu.Lock()
	defer pr.mu.Unlock()
	if pr.asking[id] == ch {
		delete(pr.asking, id) // unanswered: the next one asks again
	}
	return pr.names[id]
}

// CapOf is the name of the capability a request goes to: the one it
// names, or the one whose address a request by URL begins with, and ""
// when neither is one this session was offered.
func (a *Agent) CapOf(r CapRequest) string {
	if r.Cap != "" || r.URL == "" {
		return r.Cap
	}
	for name, u := range a.Caps() {
		if u != "" && strings.HasPrefix(r.URL, u) {
			return name
		}
	}
	return ""
}
