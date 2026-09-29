package main

import (
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/quark-idlemind/slgo/internal/pay"
	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// TestAViewersPaymentIsNotChecked: the rules are about programs.  A
// viewer handed the session is a person at its own pay dialog, and what
// it sends goes down its own circuit, past the check a client's payment
// goes through.  The profile says nothing about paying, so the same
// payment from a client is refused.
func TestAViewersPaymentIsNotChecked(t *testing.T) {
	here := msg.RegionHandle(3, 5)
	r := newHomingRig(t, here)
	h, said := r.host(t)
	vh, _ := viewerLoginTo(t, r.ctx, r.srv, "viewer-secret")
	a := h.Agent()
	inRegion(t, a, here)

	m := &msg.MoneyTransferRequest{}
	m.AgentData.AgentID, m.AgentData.SessionID = a.Account.AgentID, a.Account.SessionID
	m.MoneyData.SourceID = a.Account.AgentID
	m.MoneyData.DestID = msg.MustParseUUID("92f67e57-7e57-c0de-24de-53f27a898992")
	m.MoneyData.Amount = 25
	m.MoneyData.TransactionType = pay.TransGift
	m.MoneyData.Description = []byte("\x00")

	body, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.srv.Send(r.ctx, &pb.SendRequest{Agent: "example",
		Message: &pb.OutboundMessage{Name: "MoneyTransferRequest", Body: body, Reliable: true}})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("a client's payment under a profile that says nothing about paying: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if r.sim.saw("MoneyTransferRequest") {
		t.Fatal("the client's payment reached the grid")
	}

	c, err := vh.circuitFor("example")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialUDP("udp", nil, c.Addr())
	if err != nil {
		t.Skipf("no loopback UDP: %v", err)
	}
	defer conn.Close()
	viewerSends(t, conn, a, 1, m)

	waitFor(t, 5*time.Second, "the viewer's payment to reach the grid", func() bool {
		return r.sim.saw("MoneyTransferRequest")
	})
	if n := strings.Count(said.String(), "asked to pay"); n != 1 {
		t.Errorf("%d payments were checked, want the client's alone; the log:\n%s", n, said)
	}
}

// TestAViewersPurchaseIsNotChecked: as a payment is not.  The profile
// says nothing about paying, so a client's ObjectBuy is refused, and the
// same message from a viewer's own circuit reaches the grid.
func TestAViewersPurchaseIsNotChecked(t *testing.T) {
	here := msg.RegionHandle(3, 5)
	r := newHomingRig(t, here)
	h, said := r.host(t)
	vh, _ := viewerLoginTo(t, r.ctx, r.srv, "viewer-secret")
	a := h.Agent()
	inRegion(t, a, here)

	m := &msg.ObjectBuy{}
	m.AgentData.AgentID, m.AgentData.SessionID = a.Account.AgentID, a.Account.SessionID
	m.ObjectData = []msg.ObjectBuy_ObjectData{{ObjectLocalID: 9, SaleType: 2, SalePrice: 25}}
	body, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.srv.Send(r.ctx, &pb.SendRequest{Agent: "example",
		Message: &pb.OutboundMessage{Name: "ObjectBuy", Body: body, Reliable: true}})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("a client's purchase under a profile that says nothing about paying: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if r.sim.saw("ObjectBuy") {
		t.Fatal("the client's purchase reached the grid")
	}

	c, err := vh.circuitFor("example")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialUDP("udp", nil, c.Addr())
	if err != nil {
		t.Skipf("no loopback UDP: %v", err)
	}
	defer conn.Close()
	viewerSends(t, conn, a, 1, m)

	waitFor(t, 5*time.Second, "the viewer's purchase to reach the grid", func() bool {
		return r.sim.saw("ObjectBuy")
	})
	if n := strings.Count(said.String(), "asked to buy"); n != 1 {
		t.Errorf("%d purchases were checked, want the client's alone; the log:\n%s", n, said)
	}
}

// TestWhatIsPaidOutlivesTheDaemon: the daily limit is counted from a
// record in slgod's own directory, so a daemon started again counts what
// the one before it paid.
func TestWhatIsPaidOutlivesTheDaemon(t *testing.T) {
	dir := t.TempDir()
	payOnce := func(amount int) error {
		r := newHomingRig(t, 0)
		r.srv.SetPayLedger(func(profile string) string { return pay.LedgerPath(dir, profile) })
		r.login.Pay = pay.Rules{On: true, To: []string{"*"}}
		h, _ := r.host(t)
		a := h.Agent()

		m := &msg.MoneyTransferRequest{}
		m.AgentData.AgentID, m.AgentData.SessionID = a.Account.AgentID, a.Account.SessionID
		m.MoneyData.SourceID = a.Account.AgentID
		m.MoneyData.DestID = msg.MustParseUUID("92f67e57-7e57-c0de-24de-53f27a898992")
		m.MoneyData.Amount = int32(amount)
		m.MoneyData.TransactionType = pay.TransGift
		m.MoneyData.Description = []byte("\x00")
		body, err := m.Encode()
		if err != nil {
			t.Fatal(err)
		}
		_, err = r.srv.Send(r.ctx, &pb.SendRequest{Agent: "example",
			Message: &pb.OutboundMessage{Name: "MoneyTransferRequest", Body: body, Reliable: true}})
		return err
	}

	if err := payOnce(8); err != nil {
		t.Fatalf("the first daemon's payment: %v", err)
	}
	if _, err := os.Stat(pay.LedgerPath(dir, "example")); err != nil {
		t.Fatalf("nothing was recorded: %v", err)
	}
	err := payOnce(3)
	if status.Code(err) != codes.PermissionDenied || !strings.Contains(err.Error(), "pay_daily") {
		t.Errorf("the second daemon's payment, L$11 in a day: %v", err)
	}
}
