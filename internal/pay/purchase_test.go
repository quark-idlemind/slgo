package pay

import (
	"context"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

var (
	someGroup = msg.MustParseUUID("39fd7e57-7e57-c0de-badc-3904de59fac6")
	someTxn   = msg.MustParseUUID("97037e57-7e57-c0de-40d5-3d3c41520645")
	someItem  = msg.MustParseUUID("6c867e57-7e57-c0de-c699-65760e589f71")
)

// A purchase of each kind, priced at amount where the message or the
// session can say so, and the world that knows it.  The seller is
// example.
type buying struct {
	name  string
	price bool // the price is known, so caps and pay_to apply
	grid  bool // paid to the grid, so pay_to does not apply
	make  func(amount int) (msg.Message, *world)
}

func objectBuy(local uint32, saleType uint8, price int) *msg.ObjectBuy {
	m := &msg.ObjectBuy{}
	m.AgentData.AgentID = me
	m.ObjectData = []msg.ObjectBuy_ObjectData{{ObjectLocalID: local, SaleType: saleType, SalePrice: int32(price)}}
	return m
}

func groupAccept(group msg.UUID) *msg.ImprovedInstantMessage {
	m := &msg.ImprovedInstantMessage{}
	m.AgentData.AgentID = me
	m.MessageBlock.ToAgentID = group
	m.MessageBlock.Dialog = dialogGroupAccept
	m.MessageBlock.ID = someTxn
	return m
}

func buyings() []buying {
	return []buying{
		{"ObjectBuy", true, false, func(n int) (msg.Message, *world) {
			return objectBuy(12, saleCopy, n), &world{objects: map[uint32]sale{12: {example, n}}}
		}},
		{"ParcelBuy", true, false, func(n int) (msg.Message, *world) {
			m := &msg.ParcelBuy{}
			m.Data.LocalID = 7
			m.ParcelData.Price, m.ParcelData.Area = int32(n), 512
			return m, &world{parcels: map[int32]land{7: {owner: example, sale: n}}}
		}},
		{"ParcelBuyPass", true, false, func(n int) (msg.Message, *world) {
			m := &msg.ParcelBuyPass{}
			m.ParcelData.LocalID = 7
			return m, &world{parcels: map[int32]land{7: {owner: example, pass: n}}}
		}},
		{"JoinGroupRequest", true, false, func(n int) (msg.Message, *world) {
			m := &msg.JoinGroupRequest{}
			m.GroupData.GroupID = someGroup
			return m, &world{groupFees: map[msg.UUID]int{someGroup: n}}
		}},
		{"an accepted invitation", true, false, func(n int) (msg.Message, *world) {
			return groupAccept(someGroup), &world{inviteFees: map[msg.UUID]int{someGroup: n}}
		}},
		{"ClassifiedInfoUpdate", true, true, func(n int) (msg.Message, *world) {
			m := &msg.ClassifiedInfoUpdate{}
			m.Data.PriceForListing = int32(n)
			return m, &world{}
		}},
	}
}

// Nothing is bought unless the profile says pay = on, and a refusal
// leaves no mark in the record.
func TestEveryPurchaseIsRefusedWhenPayingIsOff(t *testing.T) {
	for _, b := range buyings() {
		m, w := b.make(1)
		g := NewGate(rules(t, "pay = off", "pay_to = *"), "")
		v, spends := g.CheckMessage(context.Background(), w, me, m)
		if !spends || !strings.Contains(v.Refused, "paying is off") {
			t.Errorf("%s with pay = off: spends %v, refused %q", b.name, spends, v.Refused)
		}
		if got, _ := g.read(); len(got) != 0 {
			t.Errorf("%s: a refusal was recorded: %v", b.name, got)
		}
		// The default profile says nothing at all.
		if v, _ := NewGate(Rules{}, "").CheckMessage(context.Background(), w, me, m); v.Refused == "" {
			t.Errorf("%s went out for a profile that says nothing", b.name)
		}
	}
}

// What a purchase costs is held to pay_max one at a time and to pay_daily
// all together, with payments, and is written to the same record.
func TestAPurchaseCountsAgainstTheCaps(t *testing.T) {
	ctx := context.Background()
	for _, b := range buyings() {
		// Over pay_max.
		m, w := b.make(8)
		g := NewGate(rules(t, "pay = on", "pay_max = 5", "pay_to = *"), "")
		if v, _ := g.CheckMessage(ctx, w, me, m); !strings.Contains(v.Refused, "pay_max of L$5") {
			t.Errorf("%s over pay_max: %q", b.name, v.Refused)
		}

		// Two of L$6 under a pay_daily of L$10.
		m, w = b.make(6)
		g = NewGate(rules(t, "pay = on", "pay_max = 10", "pay_daily = 10", "pay_to = *"), "")
		v, _ := g.CheckMessage(ctx, w, me, m)
		if v.Refused != "" {
			t.Errorf("%s within the caps: %q", b.name, v.Refused)
			continue
		}
		if got, _ := g.read(); len(got) != 1 || got[0].Amount != 6 {
			t.Errorf("%s: the record holds %v, want the L$6 spent", b.name, got)
		}
		if line := v.Describe("a client"); !strings.Contains(line, "L$6") || !strings.Contains(line, "passed by") {
			t.Errorf("%s: logged as %q", b.name, line)
		}
		if v, _ := g.CheckMessage(ctx, w, me, m); !strings.Contains(v.Refused, "pay_daily of L$10") {
			t.Errorf("%s twice: %q", b.name, v.Refused)
		}
		// The record is one: a payment counts after a purchase.
		if d := g.Check(ctx, w, gift(example, 6)); !strings.Contains(d.Refused, "pay_daily of L$10") {
			t.Errorf("%s then a payment: %q", b.name, d.Refused)
		}
	}
}

// The message's figure is what the buyer says; the session's own last
// word on it counts too, and the higher of the two is what is held to
// the caps.
func TestAPurchaseIsHeldToTheHigherOfTheTwoPrices(t *testing.T) {
	ctx := context.Background()
	g := func() *Gate { return NewGate(rules(t, "pay = on", "pay_max = 5", "pay_to = *"), "") }

	w := &world{objects: map[uint32]sale{12: {example, 9}}}
	if v, _ := g().CheckMessage(ctx, w, me, objectBuy(12, saleCopy, 1)); !strings.Contains(v.Refused, "pay_max of L$5") {
		t.Errorf("an object priced 9 by the region and 1 by the message: %q", v.Refused)
	}
	// An object the region never priced is held to the message's.
	w = &world{objects: map[uint32]sale{12: {example, -1}}}
	if v, _ := g().CheckMessage(ctx, w, me, objectBuy(12, saleCopy, 4)); v.Refused != "" {
		t.Errorf("an unpriced object at L$4: %q", v.Refused)
	}

	m := &msg.ParcelBuy{}
	m.Data.LocalID, m.ParcelData.Price = 7, 1
	w = &world{parcels: map[int32]land{7: {owner: example, sale: 9}}}
	if v, _ := g().CheckMessage(ctx, w, me, m); !strings.Contains(v.Refused, "pay_max of L$5") {
		t.Errorf("a parcel priced 9 by the region and 1 by the message: %q", v.Refused)
	}

	// The invitation says nothing to pay and the group's profile says 9.
	w = &world{inviteFees: map[msg.UUID]int{someGroup: 0}, groupFees: map[msg.UUID]int{someGroup: 9}}
	if v, _ := g().CheckMessage(ctx, w, me, groupAccept(someGroup)); !strings.Contains(v.Refused, "pay_max of L$5") {
		t.Errorf("a group asking 9 in its profile and 0 in its invitation: %q", v.Refused)
	}
	if got := w.inviteAsked; len(got) != 1 || got[0] != someTxn {
		t.Errorf("the invitation was looked up by %v, want its transaction id", got)
	}
}

// Whoever is paid must be in pay_to, and a fee the grid takes has nobody
// to be in it.
func TestAPurchaseIsMadeOnlyFromWhoMayBePaid(t *testing.T) {
	ctx := context.Background()
	for _, b := range buyings() {
		m, w := b.make(3)
		w.who = map[msg.UUID]string{example: "Example Resident", someGroup: ""}

		g := NewGate(rules(t, "pay = on", "pay_to = "+other.String()), "")
		v, _ := g.CheckMessage(ctx, w, me, m)
		switch {
		case b.grid:
			if v.Refused != "" {
				t.Errorf("%s, a fee to the grid, with pay_to naming somebody else: %q", b.name, v.Refused)
			}
			// pay = on alone pays nobody, and the grid is not anybody.
			if v, _ := NewGate(rules(t, "pay = on"), "").CheckMessage(ctx, w, me, m); v.Refused != "" {
				t.Errorf("%s, a fee to the grid, with no pay_to: %q", b.name, v.Refused)
			}
		default:
			if !strings.Contains(v.Refused, "not in this profile's pay_to") {
				t.Errorf("%s to somebody not in pay_to: %q", b.name, v.Refused)
			}
			if v, _ := NewGate(rules(t, "pay = on"), "").CheckMessage(ctx, w, me, m); !strings.Contains(v.Refused, "add pay_to") {
				t.Errorf("%s with no pay_to: %q", b.name, v.Refused)
			}
			seller := example
			if strings.Contains(b.name, "roup") || strings.Contains(b.name, "invitation") {
				seller = someGroup
			}
			if v, _ := NewGate(rules(t, "pay = on", "pay_to = "+seller.String()), "").CheckMessage(ctx, w, me, m); v.Refused != "" {
				t.Errorf("%s to somebody named by key: %q", b.name, v.Refused)
			}
		}
	}
}

// Where the price cannot be known the purchase is refused, and says why.
func TestAPurchaseWhosePriceIsNotKnownIsRefused(t *testing.T) {
	ctx := context.Background()
	buyDebit := &msg.ScriptAnswerYes{}
	buyDebit.Data.TaskID, buyDebit.Data.ItemID, buyDebit.Data.Questions = lamp, someItem, 0x2|0x10
	claim := &msg.ParcelClaim{}
	inv := &msg.BuyObjectInventory{}
	inv.Data.ObjectID, inv.Data.ItemID = lamp, someItem
	pass := &msg.ParcelBuyPass{}
	pass.ParcelData.LocalID = 3
	parcel := &msg.ParcelBuy{}
	parcel.Data.LocalID = 3
	join := &msg.JoinGroupRequest{}
	join.GroupData.GroupID = someGroup

	for _, c := range []struct {
		name string
		m    msg.Message
		why  string
	}{
		{"an object nobody has described", objectBuy(99, saleCopy, 1), "not one this session has been told of"},
		{"a sale type that is none", objectBuy(12, 0, 1), "not a sale type"},
		{"an item in an object", inv, "what an item in an object costs"},
		{"a parcel that is not underfoot", parcel, "not the parcel this avatar stands on"},
		{"a pass to one", pass, "not the parcel this avatar stands on"},
		{"a claim", claim, "what claiming land costs"},
		{"a group nobody has said the fee of", join, "neither its profile nor an invitation"},
		{"an invitation nobody heard", groupAccept(someGroup), "neither its profile nor an invitation"},
		{"a new group", &msg.CreateGroupRequest{}, "what creating a group costs"},
		{"permission to debit", buyDebit, "can take any amount"},
	} {
		w := &world{objects: map[uint32]sale{12: {example, 1}}}
		g := NewGate(rules(t, "pay = on", "pay_max = 1000", "pay_daily = 1000", "pay_to = *"), "")
		v, spends := g.CheckMessage(ctx, w, me, c.m)
		if !spends || !strings.Contains(v.Refused, c.why) {
			t.Errorf("%s: spends %v, refused %q, want it to say %q", c.name, spends, v.Refused, c.why)
		}
		if got, _ := g.read(); len(got) != 0 {
			t.Errorf("%s: recorded %v", c.name, got)
		}
		// Refused before the rules are looked at: pay = off says so first.
		if v, _ := NewGate(Rules{}, "").CheckMessage(ctx, w, me, c.m); !strings.Contains(v.Refused, "paying is off") {
			t.Errorf("%s with pay off: %q", c.name, v.Refused)
		}
	}
}

// A message that spends nothing is not looked at, and its body is not
// read at all when its number is not one that could.
func TestAMessageThatSpendsNothingIsNotChecked(t *testing.T) {
	ctx := context.Background()
	g := NewGate(Rules{}, "")

	harmless := &msg.ScriptAnswerYes{}
	harmless.Data.Questions = 0x10
	decline := groupAccept(someGroup)
	decline.MessageBlock.Dialog = 36
	chat := groupAccept(someGroup)
	chat.MessageBlock.Dialog = 0
	for name, m := range map[string]msg.Message{
		"a permission that is not debit": harmless,
		"declining an invitation":        decline,
		"an instant message":             chat,
		"a chat":                         &msg.ChatFromViewer{},
	} {
		if v, spends := g.CheckMessage(ctx, &world{}, me, m); spends || v.Refused != "" {
			t.Errorf("%s was checked: %+v", name, v)
		}
	}
	if Spends(msg.IDOf(&msg.ChatFromViewer{})) || Spends(msg.IDOf(&msg.AgentUpdate{})) {
		t.Error("a message that cannot spend is said to")
	}
	for _, m := range []msg.Message{&msg.MoneyTransferRequest{}, &msg.ObjectBuy{}, &msg.BuyObjectInventory{},
		&msg.ParcelBuy{}, &msg.ParcelClaim{}, &msg.ParcelBuyPass{}, &msg.JoinGroupRequest{},
		&msg.CreateGroupRequest{}, &msg.ClassifiedInfoUpdate{}, &msg.ImprovedInstantMessage{}, &msg.ScriptAnswerYes{}} {
		if !Spends(msg.IDOf(m)) {
			t.Errorf("%s is not looked at", m.MsgInfo().Name)
		}
	}
}

// A message sent as a framed body is read before it is checked, and one
// that cannot be read is refused rather than let past unread.
func TestAMessageSentRawIsCheckedAsTheMessage(t *testing.T) {
	ctx := context.Background()
	m := objectBuy(12, saleCopy, 4)
	body, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	w := &world{objects: map[uint32]sale{12: {example, 4}}}

	v, spends := NewGate(Rules{}, "").CheckMessage(ctx, w, me, msg.NewRaw(msg.IDOf(m), body))
	if !spends || !strings.Contains(v.Refused, "paying is off") {
		t.Errorf("a raw ObjectBuy with pay off: spends %v, %q", spends, v.Refused)
	}
	g := NewGate(rules(t, "pay = on", "pay_to = *"), "")
	if v, _ := g.CheckMessage(ctx, w, me, msg.NewRaw(msg.IDOf(m), body)); v.Refused != "" {
		t.Errorf("a raw ObjectBuy the rules allow: %q", v.Refused)
	}
	// Cut short: not a zerocoded message, whose missing zeros are read as
	// zeros, so a body that ends early is a fault.
	pass := &msg.ParcelBuyPass{}
	pb, _ := pass.Encode()
	v, spends = g.CheckMessage(ctx, w, me, msg.NewRaw(msg.IDOf(pass), pb[:3]))
	if !spends || !strings.Contains(v.Refused, "could not be read") {
		t.Errorf("a raw ParcelBuyPass cut short: spends %v, %q", spends, v.Refused)
	}
}

// A refusal describes the purchase asked for, as the grid describes its
// own, so that a client waiting on it finds it.
func TestARefusedPurchaseIsAnsweredWithAMoneyBalanceReply(t *testing.T) {
	w := &world{objects: map[uint32]sale{12: {example, 5}}}
	v, _ := NewGate(Rules{}, "").CheckMessage(context.Background(), w, me, objectBuy(12, saleCopy, 5))
	r := v.Refusal(42)
	ti := r.TransactionInfo
	if r.MoneyData.TransactionSuccess || r.MoneyData.MoneyBalance != 42 ||
		!strings.Contains(string(r.MoneyData.Description), "paying is off") {
		t.Errorf("the reply says %+v", r.MoneyData)
	}
	if ti.TransactionType != TransObjectSale || ti.SourceID != me || ti.DestID != example || ti.Amount != 5 {
		t.Errorf("the transaction is %+v", ti)
	}
	if _, err := r.Encode(); err != nil {
		t.Errorf("it does not encode: %v", err)
	}

	// A message with no figure of its own says so.
	v, _ = NewGate(Rules{}, "").CheckMessage(context.Background(), w, me, &msg.ParcelClaim{})
	if a := v.Refusal(0).TransactionInfo.Amount; a != -1 {
		t.Errorf("an unpriced purchase reports L$%d", a)
	}
}

// The one capability that buys is read as accepting the invitation.
func TestAcceptingAnInvitationByCapabilityIsAPurchase(t *testing.T) {
	ctx := context.Background()
	body := func(group string) []byte {
		b, err := llsd.Encode(map[string]any{"group": group})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	w := &world{inviteFees: map[msg.UUID]int{someGroup: 9}}

	if ReadCapPurchase(w, me, "NewFileAgentInventory", body(someGroup.String())) != nil {
		t.Error("an upload was read as a purchase")
	}
	p := ReadCapPurchase(w, me, "AcceptGroupInvite", body(someGroup.String()))
	if p == nil {
		t.Fatal("AcceptGroupInvite was not read as a purchase")
	}
	g := NewGate(rules(t, "pay = on", "pay_max = 5", "pay_to = *"), "")
	if v := g.CheckPurchase(ctx, w, p); !strings.Contains(v.Refused, "pay_max of L$5") {
		t.Errorf("accepting by capability, L$9: %q", v.Refused)
	}
	if v := NewGate(Rules{}, "").CheckPurchase(ctx, w, p); !strings.Contains(v.Refused, "paying is off") {
		t.Errorf("accepting by capability with pay off: %q", v.Refused)
	}
	// A request that names no group could be any.
	p = ReadCapPurchase(w, me, "AcceptGroupInvite", []byte("<llsd><map/></llsd>"))
	if v := NewGate(rules(t, "pay = on", "pay_to = *"), "").CheckPurchase(ctx, w, p); !strings.Contains(v.Refused, "names no group") {
		t.Errorf("no group: %q", v.Refused)
	}
}

// A payment refused for its payee names the avatar when the name is
// known, and gives the key alone when it is not.
func TestARefusalNamesWhoWasRefused(t *testing.T) {
	w := &world{who: map[msg.UUID]string{example: "Example Resident"}}
	g := NewGate(rules(t, "pay = on", "pay_to = "+other.String()), "")

	d := g.Check(context.Background(), w, gift(example, 1))
	if want := "Example Resident (" + example.String() + ") is not in this profile's pay_to"; d.Refused != want {
		t.Errorf("refused %q, want %q", d.Refused, want)
	}
	d = g.Check(context.Background(), &world{}, gift(example, 1))
	if want := example.String() + " is not in this profile's pay_to"; d.Refused != want {
		t.Errorf("refused %q, want %q", d.Refused, want)
	}
}
