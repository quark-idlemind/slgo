package pay

// Buying, which a profile's rules cover as they cover paying.
//
// A viewer spends L$ in more ways than the pay dialog: buying an object,
// a parcel of land or a pass to one, joining a group that charges for it,
// publishing a classified, creating a group.  Each is a message a program
// could send raw, so each is read here and checked through the same Gate,
// and what it costs is counted in the same record.  Uploads are not: a
// small fixed fee that slsh's put relies on.
// Why: doc/money.md#what-else-spends-l

import (
	"context"
	"fmt"
	"strings"

	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

// The transaction types a refusal of a purchase reports, from the
// viewer's lltransactiontypes.h.
const (
	TransLandClaim     = 1001
	TransGroupCreate   = 1002
	TransGroupJoin     = 1004
	TransClassifiedFee = 1103
	TransObjectSale    = 5000
	TransLandSale      = 5002
	TransInventorySale = 5004
	TransLandPassSale  = 5006
)

// What marks a message as spending, out of its own fields.
const (
	permissionDebit      = 0x2 // PERMISSION_DEBIT in a ScriptAnswerYes
	dialogGroupAccept    = 35  // IM_GROUP_INVITATION_ACCEPT
	capAcceptGroupInvite = "AcceptGroupInvite"
)

// The values of SaleType in an ObjectBuy (EForSale, llsaleinfo.h).
const (
	saleOriginal = 1
	saleCopy     = 2
	saleContents = 3
)

// Prices is what a session has been told that says what a purchase
// costs and who it pays.  *agent.Agent is one.  Each answer says whether
// the session knows; a price it does not know is never guessed.
type Prices interface {
	// ObjectSale is the owner of the object with this local id and the
	// price the region last said it is sold for, -1 when it never said,
	// and false when the object is not one the session has heard of or
	// its owner is not known.
	ObjectSale(local uint32) (owner msg.UUID, price int, ok bool)

	// ParcelSale is the owner of the parcel this avatar stands on, if
	// that is the one with this local id, and what it asks for sale and
	// for a pass.
	ParcelSale(local int32) (owner msg.UUID, sale, pass int, ok bool)

	// GroupFee is what the group's own profile said joining costs.
	GroupFee(group msg.UUID) (fee int, ok bool)

	// InvitationFee is what the invitation into the group said joining
	// costs, by the invitation's transaction id, or the latest into
	// that group when txn is zero.
	InvitationFee(group, txn msg.UUID) (fee int, ok bool)
}

// Item is one thing a message buys.
type Item struct {
	// What says which, for the log.
	What string

	// Amount is what it costs, in L$: the message's figure or the
	// session's, whichever is higher.
	Amount int

	// Payee is who is paid, and the zero id is the grid itself, which no
	// pay_to line covers.
	Payee msg.UUID

	// Unknown is why the price or the payee is not known, when it is
	// not.  An item with it is refused.
	Unknown string
}

// Purchase is what a message that spends L$ other than as a payment
// asks for.
type Purchase struct {
	// Message names it, and Type is the transaction type a refusal of
	// it reports.
	Message string
	Type    int32

	Items []Item

	// source and dest are what a refusal names in the transaction.
	source, dest msg.UUID
}

// Verdict is what the gate made of a message that spends L$.
type Verdict struct {
	// Refused says why not, and is empty when it may go.
	Refused string

	line  func(who string) string
	reply func(balance int, why string) *msg.MoneyBalanceReply
}

// Describe is the verdict as a line of slgod's log.
func (v Verdict) Describe(who string) string { return v.line(who) }

// Refusal is the MoneyBalanceReply that tells a client its message was
// refused; see Refusal.
func (v Verdict) Refusal(balance int) *msg.MoneyBalanceReply { return v.reply(balance, v.Refused) }

// Spends says whether a message with this id is one CheckMessage looks
// at.  It is asked first, so that the body of any other is never read.
func Spends(id msg.ID) bool { return spending[id] }

var spending = map[msg.ID]bool{}

func init() {
	for _, m := range []msg.Message{
		&msg.MoneyTransferRequest{}, &msg.ObjectBuy{}, &msg.BuyObjectInventory{},
		&msg.ParcelBuy{}, &msg.ParcelClaim{}, &msg.ParcelBuyPass{},
		&msg.JoinGroupRequest{}, &msg.CreateGroupRequest{}, &msg.ClassifiedInfoUpdate{},
		&msg.ImprovedInstantMessage{}, &msg.ScriptAnswerYes{},
	} {
		spending[msg.IDOf(m)] = true
	}
}

// CheckMessage decides whether a message a program sends may go out.
// It answers false when the message spends nothing and is not looked at
// further, and otherwise the gate's verdict, having recorded what was
// let through.  self is the avatar sending it.
func (g *Gate) CheckMessage(ctx context.Context, w World, self msg.UUID, m msg.Message) (Verdict, bool) {
	// A body sent as it is framed is read before it is checked.
	if r, ok := m.(*msg.Raw); ok {
		name := r.MsgInfo().Name
		if m = msg.New(r.MsgInfo().ID); m == nil || m.Decode(r.Body) != nil {
			return Unread(nil, name, "the "+name+" could not be read, so it was not checked"), true
		}
	}
	if t, ok := m.(*msg.MoneyTransferRequest); ok {
		d := g.Check(ctx, w, t)
		return Verdict{Refused: d.Refused,
			line:  d.Describe,
			reply: func(balance int, why string) *msg.MoneyBalanceReply { return Refusal(t, balance, why) },
		}, true
	}
	p := ReadPurchase(w, self, m)
	if p == nil {
		return Verdict{}, false
	}
	return g.CheckPurchase(ctx, w, p), true
}

// Unread is the verdict on a message that spends and could not be
// checked -- it could not be read, or the session was not up -- and so
// is refused.  m is the message as far as it was read, or nil.
func Unread(m msg.Message, name, why string) Verdict {
	if t, ok := m.(*msg.MoneyTransferRequest); ok {
		d := Decision{Transfer: ReadTransfer(t), Refused: why}
		return Verdict{Refused: why, line: d.Describe,
			reply: func(balance int, why string) *msg.MoneyBalanceReply { return Refusal(t, balance, why) }}
	}
	return Verdict{Refused: why,
		line: func(who string) string { return fmt.Sprintf("%s sent %s; refused: %s", who, name, why) },
		reply: func(balance int, why string) *msg.MoneyBalanceReply {
			r := &msg.MoneyBalanceReply{}
			r.MoneyData.MoneyBalance = int32(balance)
			r.MoneyData.Description = CString(why, 255)
			return r
		},
	}
}

// ReadPurchase is what a message buys, or nil when it buys nothing.
// A price that cannot be known is an Item's Unknown, and refuses the
// message, rather than being let through as if it were nothing.
func ReadPurchase(w Prices, self msg.UUID, m msg.Message) *Purchase {
	p := &Purchase{source: self}
	switch m := m.(type) {
	case *msg.ObjectBuy:
		p.Message, p.Type = "ObjectBuy", TransObjectSale
		for _, o := range m.ObjectData {
			p.Items = append(p.Items, objectItem(w, o))
		}
	case *msg.BuyObjectInventory:
		p.Message, p.Type, p.dest = "BuyObjectInventory", TransInventorySale, m.Data.ObjectID
		p.Items = []Item{{
			What:    fmt.Sprintf("item %s in object %s", m.Data.ItemID, m.Data.ObjectID),
			Unknown: "what an item in an object costs is not in the message, and the session is not told it",
		}}
	case *msg.ParcelBuy:
		p.Message, p.Type = "ParcelBuy", TransLandSale
		it := Item{What: fmt.Sprintf("parcel %d, %d m2", m.Data.LocalID, m.ParcelData.Area), Amount: int(m.ParcelData.Price)}
		owner, sale, _, ok := w.ParcelSale(m.Data.LocalID)
		if ok {
			it.Payee, it.Amount = owner, max(it.Amount, sale)
		} else {
			it.Unknown = fmt.Sprintf("parcel %d is not the parcel this avatar stands on, so its owner is not known", m.Data.LocalID)
		}
		p.Items = []Item{it}
	case *msg.ParcelClaim:
		p.Message, p.Type = "ParcelClaim", TransLandClaim
		p.Items = []Item{{What: "a claim of land",
			Unknown: "what claiming land costs is not in the message, and the session is not told it"}}
	case *msg.ParcelBuyPass:
		p.Message, p.Type = "ParcelBuyPass", TransLandPassSale
		it := Item{What: fmt.Sprintf("a pass to parcel %d", m.ParcelData.LocalID)}
		owner, _, pass, ok := w.ParcelSale(m.ParcelData.LocalID)
		if ok {
			it.Payee, it.Amount = owner, pass
		} else {
			it.Unknown = fmt.Sprintf("parcel %d is not the parcel this avatar stands on, so what its pass costs is not known", m.ParcelData.LocalID)
		}
		p.Items = []Item{it}
	case *msg.JoinGroupRequest:
		p.Items = []Item{joinItem(w, m.GroupData.GroupID, msg.UUID{})}
		p.Message, p.Type, p.dest = "JoinGroupRequest", TransGroupJoin, m.GroupData.GroupID
	case *msg.ImprovedInstantMessage:
		if m.MessageBlock.Dialog != dialogGroupAccept {
			return nil
		}
		group := m.MessageBlock.ToAgentID
		p.Items = []Item{joinItem(w, group, m.MessageBlock.ID)}
		p.Message, p.Type, p.dest = "an accepted group invitation", TransGroupJoin, group
	case *msg.CreateGroupRequest:
		p.Message, p.Type = "CreateGroupRequest", TransGroupCreate
		p.Items = []Item{{What: "a new group",
			Unknown: "what creating a group costs is not in the message, and the session is not told it"}}
	case *msg.ClassifiedInfoUpdate:
		p.Message, p.Type = "ClassifiedInfoUpdate", TransClassifiedFee
		p.Items = []Item{{What: fmt.Sprintf("classified %s", m.Data.ClassifiedID), Amount: int(m.Data.PriceForListing)}}
	case *msg.ScriptAnswerYes:
		if m.Data.Questions&permissionDebit == 0 {
			return nil
		}
		p.Message, p.Type, p.dest = "ScriptAnswerYes", 0, m.Data.TaskID
		p.Items = []Item{{What: fmt.Sprintf("permission to debit for script %s in %s", m.Data.ItemID, m.Data.TaskID),
			Unknown: "a script given PERMISSION_DEBIT can take any amount, to anyone, whenever it likes"}}
	default:
		return nil
	}
	if p.dest.IsZero() && len(p.Items) > 0 {
		p.dest = p.Items[0].Payee
	}
	return p
}

// ReadCapPurchase is what a capability request buys, or nil.  Only one
// capability does: AcceptGroupInvite, which answers an invitation that
// arrived while logged out, and joins the group as accepting it does.
func ReadCapPurchase(w Prices, self msg.UUID, cap string, body []byte) *Purchase {
	if cap != capAcceptGroupInvite {
		return nil
	}
	p := &Purchase{source: self, Message: capAcceptGroupInvite, Type: TransGroupJoin}
	var group msg.UUID
	if v, err := llsd.Decode(strings.NewReader(string(body))); err == nil {
		group, _ = msg.ParseUUID(llsd.String(llsd.Map(v), "group"))
	}
	if group.IsZero() {
		p.Items = []Item{{What: "a group invitation", Unknown: "the request names no group, so what joining costs is not known"}}
		return p
	}
	p.dest = group
	p.Items = []Item{joinItem(w, group, msg.UUID{})}
	return p
}

// objectItem is one ObjectData of an ObjectBuy.  The message carries the
// price and the region compares it with the object's own; the session's
// last word on it counts as well, and the higher is what is counted.
func objectItem(w Prices, o msg.ObjectBuy_ObjectData) Item {
	var kind string
	switch o.SaleType {
	case saleOriginal:
		kind = "the original"
	case saleCopy:
		kind = "a copy"
	case saleContents:
		kind = "the contents"
	}
	it := Item{What: fmt.Sprintf("%s of object %d", kind, o.ObjectLocalID), Amount: int(o.SalePrice)}
	owner, price, ok := w.ObjectSale(o.ObjectLocalID)
	switch {
	case kind == "":
		it.What = fmt.Sprintf("object %d", o.ObjectLocalID)
		it.Unknown = fmt.Sprintf("%d is not a sale type", o.SaleType)
	case !ok:
		it.Unknown = fmt.Sprintf("object %d is not one this session has been told of, so its owner is not known", o.ObjectLocalID)
	default:
		it.Payee, it.Amount = owner, max(it.Amount, price)
	}
	return it
}

// joinItem is joining a group: what the invitation said, and what the
// group's profile said, whichever is higher (txn zero: the latest
// invitation into the group).  The grid charges the
// group's fee as it is when the join arrives, so either is what was
// last said and not a promise.
func joinItem(w Prices, group, txn msg.UUID) Item {
	it := Item{What: fmt.Sprintf("membership of group %s", group), Payee: group}
	fee, known := w.GroupFee(group)
	if f, ok := w.InvitationFee(group, txn); ok {
		fee, known = max(fee, f), true
	}
	if !known {
		it.Unknown = fmt.Sprintf("what joining group %s costs is not known: neither its profile nor an invitation into it has been heard", group)
	}
	it.Amount = fee
	return it
}

// CheckPurchase decides whether a purchase may go out, and records what
// it costs against the daily total when it may.  Nothing is spent
// without pay = on; nothing whose price or payee is not known is let
// through; each item is held to pay_max, every payee to pay_to, and the
// sum to pay_daily.
func (g *Gate) CheckPurchase(ctx context.Context, w World, p *Purchase) Verdict {
	g.mu.Lock()
	defer g.mu.Unlock()

	v := Verdict{
		reply: func(balance int, why string) *msg.MoneyBalanceReply { return p.refusal(balance, why) },
	}
	var by []string
	var spent int
	refuse := func(format string, a ...any) Verdict {
		why := fmt.Sprintf(format, a...)
		v.Refused = why
		v.line = func(who string) string { return p.describe(who, why, nil, spent) }
		return v
	}
	v.line = func(who string) string { return p.describe(who, "", by, spent) }

	r := g.rules
	if !r.On {
		return refuse("paying is off for this profile, and so is buying; pay = on in the profile turns both on")
	}
	var cs []charge
	for _, it := range p.Items {
		if it.Unknown != "" {
			return refuse("%s is refused: %s", it.What, it.Unknown)
		}
		if it.Amount < 0 {
			return refuse("L$%d for %s is not an amount that can be paid", it.Amount, it.What)
		}
		if max := r.MaxPayment(); it.Amount > max {
			return refuse("L$%d for %s is more than this profile's pay_max of L$%d", it.Amount, it.What, max)
		}
		if !it.Payee.IsZero() && it.Amount > 0 && len(r.To) == 0 {
			return refuse("this profile's pay_to names nobody; add pay_to = NAME for each avatar it may pay, or pay_to = * for anyone")
		}
		cs = append(cs, charge{amount: it.Amount, payee: it.Payee, grid: it.Payee.IsZero()})
	}
	var why string
	if by, spent, why = g.admit(ctx, w, cs); why != "" {
		return refuse("%s", why)
	}
	return v
}

// describe is a purchase as a line of slgod's log: who asked, what for,
// how much and to whom, and what became of it.
func (p *Purchase) describe(who, refused string, by []string, spent int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s asked to buy with %s", who, p.Message)
	for i, it := range p.Items {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, " %s for L$%d", it.What, it.Amount)
		if !it.Payee.IsZero() {
			fmt.Fprintf(&b, " to %s", it.Payee)
		}
	}
	if refused != "" {
		fmt.Fprintf(&b, "; refused: %s", refused)
		return b.String()
	}
	var lines []string
	for _, l := range by {
		if l != "" {
			lines = append(lines, "pay_to "+l)
		}
	}
	if len(lines) == 0 {
		lines = []string{"no pay_to line, nobody being paid"}
	}
	fmt.Fprintf(&b, "; passed by %s, L$%d paid in the last 24 hours before it", strings.Join(lines, ", "), spent)
	return b.String()
}

// refusal is the MoneyBalanceReply a refused purchase is answered with,
// filled in as Refusal fills in a payment's: the transaction as it was
// asked for, success false, and why.  A message with no price of its
// own says -1.
func (p *Purchase) refusal(balance int, why string) *msg.MoneyBalanceReply {
	r := &msg.MoneyBalanceReply{}
	r.MoneyData.AgentID = p.source
	r.MoneyData.MoneyBalance = int32(balance)
	r.MoneyData.Description = CString(why, 255)
	r.TransactionInfo.TransactionType = p.Type
	r.TransactionInfo.SourceID = p.source
	r.TransactionInfo.DestID = p.dest
	r.TransactionInfo.Amount = -1
	if len(p.Items) > 0 && p.Items[0].Unknown == "" {
		sum := 0
		for _, it := range p.Items {
			sum += it.Amount
		}
		r.TransactionInfo.Amount = int32(sum)
	}
	r.TransactionInfo.ItemDescription = CString(p.Message, 255)
	return r
}
