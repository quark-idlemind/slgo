package server

// A client's payment, checked on its way out.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/quark-idlemind/slgo/internal/pay"
	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

var (
	payee    = msg.MustParseUUID("92f67e57-7e57-c0de-24de-53f27a898992")
	paidLamp = msg.MustParseUUID("1b737e57-7e57-c0de-de35-3390805ca1b0")
)

// hosted is the rig's one session.
func (r *rig) hosted(t *testing.T) *Hosted {
	t.Helper()
	h, ok := r.srv.Agent("example")
	if !ok {
		t.Fatal("the rig hosts nothing")
	}
	return h
}

// payRules gives the rig's session these rules, with a record in memory.
func (r *rig) payRules(t *testing.T, lines ...string) {
	t.Helper()
	var rules pay.Rules
	for _, l := range lines {
		k, v, _ := strings.Cut(l, "=")
		if err := rules.Set(strings.TrimSpace(k), v); err != nil {
			t.Fatal(err)
		}
	}
	h := r.hosted(t)
	h.mu.Lock()
	h.pay = pay.NewGate(rules, "")
	h.mu.Unlock()
}

// said is what a Hosted logged.
type said struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *said) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (r *rig) logTo(t *testing.T) *said {
	t.Helper()
	s := &said{}
	r.hosted(t).Log = func(format string, v ...any) {
		s.mu.Lock()
		fmt.Fprintf(&s.b, format+"\n", v...)
		s.mu.Unlock()
	}
	return s
}

func giving(from, to msg.UUID, amount int, typ int32) *msg.MoneyTransferRequest {
	m := &msg.MoneyTransferRequest{}
	m.AgentData.AgentID = from
	m.MoneyData.SourceID = from
	m.MoneyData.DestID = to
	m.MoneyData.Amount = int32(amount)
	m.MoneyData.TransactionType = typ
	m.MoneyData.Description = []byte("for the lamp\x00")
	return m
}

func (r *rig) me(t *testing.T) msg.UUID { return r.hosted(t).Agent().Account.AgentID }

// neverSeen fails if the sim has been sent a message by that name.
func neverSeen(t *testing.T, sim *fakeSim, name string) {
	t.Helper()
	// Long enough for a message sent to have crossed loopback.
	time.Sleep(200 * time.Millisecond)
	for _, n := range sim.got() {
		if n == name {
			t.Fatalf("%s reached the grid", name)
		}
	}
}

func seenSoon(t *testing.T, sim *fakeSim, name string) {
	t.Helper()
	waitFor(t, 5*time.Second, name+" to reach the grid", func() bool {
		for _, n := range sim.got() {
			if n == name {
				return true
			}
		}
		return false
	})
}

func TestAPaymentIsRefusedWhenTheProfileSaysNothing(t *testing.T) {
	r := newRig(t, nil)
	log := r.logTo(t)
	c := r.dial(t, "MoneyBalanceReply")
	defer c.Close()

	if err := c.Send(context.Background(), giving(r.me(t), payee, 5, pay.TransGift), true); err != nil {
		t.Fatal(err)
	}
	got := waitMsg(t, c, "MoneyBalanceReply", 5*time.Second)
	if got.FromClient != pay.RefusedBy {
		t.Errorf("the refusal is signed %q, want %q", got.FromClient, pay.RefusedBy)
	}
	v, err := got.Decode()
	if err != nil {
		t.Fatal(err)
	}
	reply := v.(*msg.MoneyBalanceReply)
	if reply.MoneyData.TransactionSuccess || !strings.Contains(string(reply.MoneyData.Description), "paying is off") {
		t.Errorf("the refusal says %+v", reply.MoneyData)
	}
	if ti := reply.TransactionInfo; ti.DestID != payee || ti.Amount != 5 || ti.TransactionType != pay.TransGift ||
		string(ti.ItemDescription) != "for the lamp\x00" {
		t.Errorf("the refusal describes %+v, not the payment asked for", ti)
	}
	neverSeen(t, r.sim, "MoneyTransferRequest")

	if s := log.String(); !strings.Contains(s, "asked to pay "+payee.String()+" L$5; refused: paying is off") {
		t.Errorf("the refusal was not logged as it happened; the log:\n%s", s)
	}
}

func TestAPaymentTheRulesAllowGoesOut(t *testing.T) {
	r := newRig(t, nil)
	log := r.logTo(t)
	r.payRules(t, "pay = on", "pay_to = "+payee.String())
	c := r.dial(t, "MoneyBalanceReply")
	defer c.Close()

	if err := c.Send(context.Background(), giving(r.me(t), payee, 5, pay.TransGift), true); err != nil {
		t.Fatal(err)
	}
	seenSoon(t, r.sim, "MoneyTransferRequest")
	bodies := r.sim.sawBody("MoneyTransferRequest")
	var sent msg.MoneyTransferRequest
	if err := sent.Decode(bodies[0]); err != nil {
		t.Fatal(err)
	}
	if sent.MoneyData.DestID != payee || sent.MoneyData.Amount != 5 {
		t.Errorf("sent %+v", sent.MoneyData)
	}
	if s := log.String(); !strings.Contains(s, "L$5; passed by pay_to "+payee.String()+", L$0 paid") {
		t.Errorf("the payment was not logged; the log:\n%s", s)
	}

	// The second is over the daily default of ten, and stops here.
	if err := c.Send(context.Background(), giving(r.me(t), payee, 6, pay.TransGift), true); err != nil {
		t.Fatal(err)
	}
	got := waitMsg(t, c, "MoneyBalanceReply", 5*time.Second)
	if v, _ := got.Decode(); !strings.Contains(string(v.(*msg.MoneyBalanceReply).MoneyData.Description), "pay_daily") {
		t.Errorf("the second payment: %s", v.(*msg.MoneyBalanceReply).MoneyData.Description)
	}
	if n := len(r.sim.sawBody("MoneyTransferRequest")); n != 1 {
		t.Errorf("%d payments reached the grid, want 1", n)
	}
}

// A refusal is the answer to what one client sent, and goes to that one
// whatever it subscribed to -- and to nobody else.
func TestARefusalGoesToTheClientThatPaidAndNoOther(t *testing.T) {
	r := newRig(t, nil)
	payer := r.dial(t) // subscribed to nothing
	defer payer.Close()
	other := r.dial(t, "MoneyBalanceReply")
	defer other.Close()
	time.Sleep(100 * time.Millisecond)

	if err := payer.Send(context.Background(), giving(r.me(t), payee, 1, pay.TransGift), true); err != nil {
		t.Fatal(err)
	}
	if got := waitMsg(t, payer, "MoneyBalanceReply", 5*time.Second); got.FromClient != pay.RefusedBy {
		t.Errorf("signed %q", got.FromClient)
	}
	select {
	case m, ok := <-other.Messages():
		if ok {
			t.Fatalf("another client was told of the refusal: %s", m.Name)
		}
	case <-time.After(300 * time.Millisecond):
	}
}

func TestAOneShotPaymentIsRefusedWithAnError(t *testing.T) {
	r := newRig(t, nil)
	body, err := giving(r.me(t), payee, 1, pay.TransGift).Encode()
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.srv.Send(context.Background(), &pb.SendRequest{Agent: "example",
		Message: &pb.OutboundMessage{Name: "MoneyTransferRequest", Body: body, Reliable: true}})
	if status.Code(err) != codes.PermissionDenied || !strings.Contains(err.Error(), "paying is off") {
		t.Errorf("a one-shot payment: %v", err)
	}
	neverSeen(t, r.sim, "MoneyTransferRequest")
}

// An object is paid only when its owner may be, and the owner is what
// the region said.
func TestAnObjectIsPaidOnlyWhenItsOwnerIsKnownAndAllowed(t *testing.T) {
	r := newRig(t, nil)
	r.payRules(t, "pay = on", "pay_to = "+payee.String())
	c := r.dial(t, "MoneyBalanceReply")
	defer c.Close()

	if err := c.Send(context.Background(), giving(r.me(t), paidLamp, 1, pay.TransPayObject), true); err != nil {
		t.Fatal(err)
	}
	got := waitMsg(t, c, "MoneyBalanceReply", 5*time.Second)
	if v, _ := got.Decode(); !strings.Contains(string(v.(*msg.MoneyBalanceReply).MoneyData.Description), "owner of object") {
		t.Errorf("an object nobody is known to own: %s", v.(*msg.MoneyBalanceReply).MoneyData.Description)
	}

	h := r.hosted(t)
	r.sim.send(&msg.ObjectUpdate{ObjectData: []msg.ObjectUpdate_ObjectData{{ID: 77, FullID: paidLamp, PCode: 9}}}, 0)
	waitFor(t, 5*time.Second, "the lamp to be described", func() bool {
		_, ok := h.Agent().Objects().Get(paidLamp)
		return ok
	})
	fam := &msg.ObjectPropertiesFamily{}
	fam.ObjectData.ObjectID = paidLamp
	fam.ObjectData.OwnerID = payee
	fam.ObjectData.Name = []byte("a lamp\x00")
	fam.ObjectData.Description = []byte("\x00")
	r.sim.send(fam, 0)
	waitFor(t, 5*time.Second, "the lamp's owner to be known", func() bool {
		_, ok := h.Agent().ObjectOwner(paidLamp)
		return ok
	})

	if err := c.Send(context.Background(), giving(r.me(t), paidLamp, 1, pay.TransPayObject), true); err != nil {
		t.Fatal(err)
	}
	seenSoon(t, r.sim, "MoneyTransferRequest")
}

// What the grid says of a payment this avatar made is logged, whoever
// asked for it; a payment received is not.
func TestTheGridsAnswerToAPaymentIsLogged(t *testing.T) {
	r := newRig(t, nil)
	h := r.hosted(t)
	log := r.logTo(t)
	me := r.me(t)

	reply := &msg.MoneyBalanceReply{}
	reply.MoneyData.TransactionSuccess = true
	reply.MoneyData.MoneyBalance = 94
	reply.MoneyData.Description = []byte("You paid Example Resident L$5.\x00")
	reply.TransactionInfo.TransactionType = pay.TransGift
	reply.TransactionInfo.SourceID = me
	reply.TransactionInfo.DestID = payee
	reply.TransactionInfo.Amount = 5
	r.sim.send(reply, msg.FlagZerocoded)

	received := *reply
	received.MoneyData.Description = []byte("Example Resident paid you L$2.\x00")
	received.TransactionInfo.SourceID = payee
	received.TransactionInfo.DestID = me
	r.sim.send(&received, msg.FlagZerocoded)

	waitFor(t, 5*time.Second, "the payment to be logged", func() bool {
		return strings.Contains(log.String(), `the grid says "You paid Example Resident L$5."; L$94 left`)
	})
	time.Sleep(100 * time.Millisecond)
	if strings.Contains(log.String(), "paid you") {
		t.Errorf("a payment received was logged:\n%s", log)
	}
	if b, known := h.Agent().Balance(); !known || b != 94 {
		t.Errorf("balance %d, %v", b, known)
	}
}
