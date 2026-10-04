package server

// A client's purchase, checked on its way out as its payment is.

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/internal/pay"
	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

var (
	theGroup = msg.MustParseUUID("39fd7e57-7e57-c0de-badc-3904de59fac6")
	theTxn   = msg.MustParseUUID("97037e57-7e57-c0de-40d5-3d3c41520645")
	stranger = msg.MustParseUUID("cd957e57-7e57-c0de-26e8-543033dda432")
	lampID   = uint32(77)
)

// describeLamp tells the session there is an object for sale, whose
// owner and price it now knows.
func (r *rig) describeLamp(t *testing.T, owner msg.UUID, price int32) {
	t.Helper()
	h := r.hosted(t)
	r.sim.send(&msg.ObjectUpdate{ObjectData: []msg.ObjectUpdate_ObjectData{{ID: lampID, FullID: paidLamp, PCode: 9}}}, 0)
	waitFor(t, 5*time.Second, "the lamp to be described", func() bool {
		_, ok := h.Agent().Objects().Get(paidLamp)
		return ok
	})
	fam := &msg.ObjectPropertiesFamily{}
	fam.ObjectData.ObjectID = paidLamp
	fam.ObjectData.OwnerID = owner
	fam.ObjectData.SalePrice = price
	fam.ObjectData.Name = []byte("a lamp\x00")
	fam.ObjectData.Description = []byte("\x00")
	r.sim.send(fam, 0)
	waitFor(t, 5*time.Second, "the lamp's owner and price to be known", func() bool {
		_, price, ok := h.Agent().ObjectSale(lampID)
		return ok && price >= 0
	})
}

func lampBuy(price int) *msg.ObjectBuy {
	m := &msg.ObjectBuy{}
	m.ObjectData = []msg.ObjectBuy_ObjectData{{ObjectLocalID: lampID, SaleType: 2, SalePrice: int32(price)}}
	return m
}

func groupAccepted() *msg.ImprovedInstantMessage {
	m := &msg.ImprovedInstantMessage{}
	m.MessageBlock.ToAgentID = theGroup
	m.MessageBlock.Dialog = 35
	m.MessageBlock.ID = theTxn
	return m
}

// every purchase the server looks at, with what it is called on the wire.
func purchases() map[string]msg.Message {
	inv := &msg.BuyObjectInventory{}
	inv.Data.ObjectID = paidLamp
	buyPass := &msg.ParcelBuyPass{}
	buyPass.ParcelData.LocalID = 3
	parcel := &msg.ParcelBuy{}
	parcel.Data.LocalID, parcel.ParcelData.Price = 3, 10
	join := &msg.JoinGroupRequest{}
	join.GroupData.GroupID = theGroup
	classified := &msg.ClassifiedInfoUpdate{}
	classified.Data.PriceForListing = 5
	debit := &msg.ScriptAnswerYes{}
	debit.Data.TaskID, debit.Data.Questions = paidLamp, 0x2
	return map[string]msg.Message{
		"ObjectBuy":              lampBuy(5),
		"BuyObjectInventory":     inv,
		"ParcelBuy":              parcel,
		"ParcelClaim":            &msg.ParcelClaim{},
		"ParcelBuyPass":          buyPass,
		"JoinGroupRequest":       join,
		"CreateGroupRequest":     &msg.CreateGroupRequest{},
		"ClassifiedInfoUpdate":   classified,
		"ImprovedInstantMessage": groupAccepted(),
		"ScriptAnswerYes":        debit,
	}
}

// Nothing is bought by a client under a profile that says nothing about
// paying, and each refusal is the answer that client is waiting for.
func TestAPurchaseIsRefusedWhenTheProfileSaysNothing(t *testing.T) {
	r := newRig(t, nil)
	log := r.logTo(t)
	c := r.dial(t) // subscribed to nothing: a refusal is not a subscription
	defer c.Close()

	for name, m := range purchases() {
		if err := c.Send(context.Background(), m, true); err != nil {
			t.Fatal(err)
		}
		got := waitMsg(t, c, "MoneyBalanceReply", 5*time.Second)
		if got.FromClient != pay.RefusedBy {
			t.Errorf("%s: the refusal is signed %q, want %q", name, got.FromClient, pay.RefusedBy)
		}
		v, err := got.Decode()
		if err != nil {
			t.Fatal(err)
		}
		reply := v.(*msg.MoneyBalanceReply)
		if reply.MoneyData.TransactionSuccess || !strings.Contains(string(reply.MoneyData.Description), "paying is off") {
			t.Errorf("%s: the refusal says %+v", name, reply.MoneyData)
		}
	}
	time.Sleep(200 * time.Millisecond)
	for _, n := range r.sim.got() {
		if _, bought := purchases()[n]; bought && n != "ImprovedInstantMessage" {
			t.Errorf("%s reached the grid", n)
		}
		if n == "ImprovedInstantMessage" {
			t.Error("the acceptance of a group invitation reached the grid")
		}
	}
	if s := log.String(); strings.Count(s, "refused: paying is off") != len(purchases()) {
		t.Errorf("every refusal is logged; the log:\n%s", s)
	}
}

// An allowed purchase goes out, is logged, and counts against the caps
// with what else was paid.
func TestAPurchaseTheRulesAllowGoesOutAndIsCounted(t *testing.T) {
	r := newRig(t, nil)
	log := r.logTo(t)
	r.payRules(t, "pay = on", "pay_to = "+payee.String())
	r.describeLamp(t, payee, 6)
	c := r.dial(t)
	defer c.Close()

	if err := c.Send(context.Background(), lampBuy(6), true); err != nil {
		t.Fatal(err)
	}
	seenSoon(t, r.sim, "ObjectBuy")
	if s := log.String(); !strings.Contains(s, "asked to buy with ObjectBuy a copy of object 77 for L$6 to "+payee.String()+
		"; passed by pay_to "+payee.String()+", L$0 paid") {
		t.Errorf("the purchase was not logged; the log:\n%s", s)
	}

	// L$6 more is over the daily default of ten, and stops here.
	if err := c.Send(context.Background(), lampBuy(6), true); err != nil {
		t.Fatal(err)
	}
	got := waitMsg(t, c, "MoneyBalanceReply", 5*time.Second)
	if v, _ := got.Decode(); !strings.Contains(string(v.(*msg.MoneyBalanceReply).MoneyData.Description), "pay_daily") {
		t.Errorf("the second purchase: %s", v.(*msg.MoneyBalanceReply).MoneyData.Description)
	}
	if n := len(r.sim.sawBody("ObjectBuy")); n != 1 {
		t.Errorf("%d purchases reached the grid, want 1", n)
	}
	// And a payment after them is counted with them.
	if err := c.Send(context.Background(), giving(r.me(t), payee, 6, pay.TransGift), true); err != nil {
		t.Fatal(err)
	}
	got = waitMsg(t, c, "MoneyBalanceReply", 5*time.Second)
	if v, _ := got.Decode(); !strings.Contains(string(v.(*msg.MoneyBalanceReply).MoneyData.Description), "pay_daily") {
		t.Errorf("a payment after the purchase: %s", v.(*msg.MoneyBalanceReply).MoneyData.Description)
	}
}

// Who is paid must be in pay_to, and the refusal names them when the
// grid says who they are.
func TestAPurchaseFromSomebodyNotInPayToIsRefusedByName(t *testing.T) {
	r := newRig(t, nil)
	r.sim.nameOf(stranger, "Some", "Stranger")
	r.payRules(t, "pay = on", "pay_to = "+payee.String())
	r.describeLamp(t, stranger, 3)
	c := r.dial(t)
	defer c.Close()

	if err := c.Send(context.Background(), lampBuy(3), true); err != nil {
		t.Fatal(err)
	}
	got := waitMsg(t, c, "MoneyBalanceReply", 5*time.Second)
	v, _ := got.Decode()
	want := "Some Stranger (" + stranger.String() + ") is not in this profile's pay_to"
	if d := string(v.(*msg.MoneyBalanceReply).MoneyData.Description); !strings.Contains(d, want) {
		t.Errorf("the refusal says %q, want it to say %q", d, want)
	}
	neverSeen(t, r.sim, "ObjectBuy")
}

// A price the session was never told is not guessed.
func TestAPurchaseWhosePriceIsNotKnownIsRefused(t *testing.T) {
	r := newRig(t, nil)
	r.payRules(t, "pay = on", "pay_max = 1000", "pay_daily = 1000", "pay_to = *")
	c := r.dial(t)
	defer c.Close()

	for name, want := range map[string]string{
		"ObjectBuy":          "not one this session has been told of",
		"ParcelBuy":          "not the parcel this avatar stands on",
		"ParcelClaim":        "what claiming land costs",
		"JoinGroupRequest":   "neither its profile nor an invitation",
		"CreateGroupRequest": "what creating a group costs",
		"ScriptAnswerYes":    "can take any amount",
	} {
		if err := c.Send(context.Background(), purchases()[name], true); err != nil {
			t.Fatal(err)
		}
		got := waitMsg(t, c, "MoneyBalanceReply", 5*time.Second)
		v, _ := got.Decode()
		if d := string(v.(*msg.MoneyBalanceReply).MoneyData.Description); !strings.Contains(d, want) {
			t.Errorf("%s: the refusal says %q, want %q", name, d, want)
		}
	}
	time.Sleep(200 * time.Millisecond)
	if n := len(r.sim.got()); n != 0 {
		// The circuit's own traffic is not a purchase.
		for _, name := range r.sim.got() {
			if _, bought := purchases()[name]; bought {
				t.Errorf("%s reached the grid", name)
			}
		}
	}
}

// A group's fee is what its profile said or its invitation said, and the
// group is who is paid.
func TestJoiningAGroupIsCheckedAgainstItsFee(t *testing.T) {
	r := newRig(t, nil)
	r.payRules(t, "pay = on", "pay_max = 10", "pay_daily = 100", "pay_to = "+theGroup.String())
	c := r.dial(t)
	defer c.Close()

	profile := &msg.GroupProfileReply{}
	profile.GroupData.GroupID = theGroup
	profile.GroupData.MembershipFee = 25
	r.sim.send(profile, 0)
	h := r.hosted(t)
	waitFor(t, 5*time.Second, "the group's fee to be known", func() bool {
		_, ok := h.Agent().GroupFee(theGroup)
		return ok
	})
	join := purchases()["JoinGroupRequest"]
	if err := c.Send(context.Background(), join, true); err != nil {
		t.Fatal(err)
	}
	got := waitMsg(t, c, "MoneyBalanceReply", 5*time.Second)
	if v, _ := got.Decode(); !strings.Contains(string(v.(*msg.MoneyBalanceReply).MoneyData.Description), "pay_max of L$10") {
		t.Errorf("a group asking L$25: %s", v.(*msg.MoneyBalanceReply).MoneyData.Description)
	}

	profile.GroupData.MembershipFee = 4
	r.sim.send(profile, 0)
	waitFor(t, 5*time.Second, "the fee to change", func() bool {
		f, _ := h.Agent().GroupFee(theGroup)
		return f == 4
	})
	if err := c.Send(context.Background(), join, true); err != nil {
		t.Fatal(err)
	}
	seenSoon(t, r.sim, "JoinGroupRequest")

	// An invitation quotes its own, and accepting it is checked by that.
	bucket := make([]byte, 20)
	binary.BigEndian.PutUint32(bucket, 50)
	inv := &msg.ImprovedInstantMessage{}
	inv.AgentData.AgentID = theGroup
	inv.MessageBlock.Dialog, inv.MessageBlock.ID, inv.MessageBlock.BinaryBucket = 3, theTxn, bucket
	inv.MessageBlock.FromAgentName, inv.MessageBlock.Message = []byte("example.inviter\x00"), []byte("join\x00")
	r.sim.send(inv, 0)
	waitFor(t, 5*time.Second, "the invitation's fee to be known", func() bool {
		_, ok := h.Agent().InvitationFee(theGroup, theTxn)
		return ok
	})
	if err := c.Send(context.Background(), groupAccepted(), true); err != nil {
		t.Fatal(err)
	}
	got = waitMsg(t, c, "MoneyBalanceReply", 5*time.Second)
	if v, _ := got.Decode(); !strings.Contains(string(v.(*msg.MoneyBalanceReply).MoneyData.Description), "L$50") {
		t.Errorf("accepting an invitation of L$50: %s", v.(*msg.MoneyBalanceReply).MoneyData.Description)
	}
	for _, n := range r.sim.got() {
		if n == "ImprovedInstantMessage" {
			t.Error("the acceptance reached the grid")
		}
	}
}

// A message that spends nothing goes out as it always did.
func TestAMessageThatSpendsNothingIsNotHeld(t *testing.T) {
	r := newRig(t, nil)
	c := r.dial(t)
	defer c.Close()

	chat := &msg.ImprovedInstantMessage{}
	chat.MessageBlock.ToAgentID = payee
	chat.MessageBlock.Message = []byte("hello\x00")
	chat.MessageBlock.FromAgentName = []byte("Example Resident\x00")
	if err := c.Send(context.Background(), chat, true); err != nil {
		t.Fatal(err)
	}
	seenSoon(t, r.sim, "ImprovedInstantMessage")

	ok := &msg.ScriptAnswerYes{}
	ok.Data.Questions = 0x10
	if err := c.Send(context.Background(), ok, true); err != nil {
		t.Fatal(err)
	}
	seenSoon(t, r.sim, "ScriptAnswerYes")
}

// A one-shot purchase is refused with the error of the call.
func TestAOneShotPurchaseIsRefusedWithAnError(t *testing.T) {
	r := newRig(t, nil)
	body, err := lampBuy(1).Encode()
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.srv.Send(context.Background(), &pb.SendRequest{Agent: "example",
		Message: &pb.OutboundMessage{Name: "ObjectBuy", Body: body, Reliable: true}})
	if status.Code(err) != codes.PermissionDenied || !strings.Contains(err.Error(), "paying is off") {
		t.Errorf("a one-shot purchase: %v", err)
	}
	neverSeen(t, r.sim, "ObjectBuy")
}

// The capability that answers an invitation is a purchase too.
func TestAcceptingAnInvitationByCapabilityIsChecked(t *testing.T) {
	r := newRig(t, agent.Caps{"AcceptGroupInvite": "http://127.0.0.1:1/cap/accept"})
	body := []byte("<llsd><map><key>group</key><uuid>" + theGroup.String() + "</uuid></map></llsd>")
	_, err := r.srv.Cap(context.Background(), &pb.CapRequest{Agent: "example", Cap: "AcceptGroupInvite", Method: "POST", Body: body})
	if status.Code(err) != codes.PermissionDenied || !strings.Contains(err.Error(), "paying is off") {
		t.Errorf("accepting by capability under a profile that says nothing: %v", err)
	}
	// By its address, as a client that knows it may.
	_, err = r.srv.Cap(context.Background(), &pb.CapRequest{Agent: "example", Method: "POST", Url: "http://127.0.0.1:1/cap/accept", Body: body})
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("accepting by its address: %v", err)
	}
}
