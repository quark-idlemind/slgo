package server

// Paying and buying, which slgod checks for every client.
//
// A client's message is a body this daemon frames and forwards, so any
// client with the secret could put its own MoneyTransferRequest, or an
// ObjectBuy, on the circuit.  So sendMessage reads the messages that
// spend L$ (pay.Spends) on their way out and checks each against the
// profile's rules (package pay) before it goes.  A refusal goes back to
// the client that sent it -- a MoneyBalanceReply signed pay.RefusedBy on
// a stream, the error of a one-shot Send -- and every refusal and every
// payment or purchase let through is logged.
//
// A viewer's messages do not come this way and are not checked: that is
// a person using the viewer's own pay dialog, and the rules are about
// programs.
// Why: doc/money.md#where-the-rules-are-checked

import (
	"context"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/internal/pay"
	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// PayLedger says where the record of what a profile has paid is kept,
// which is what its daily limit is counted from.  Empty keeps it in
// memory, for as long as the session is hosted.
type PayLedger func(profile string) string

// SetPayLedger tells the server where to keep what each profile has
// paid.  Nil keeps it in memory.  Set before any session comes up: a
// Hosted takes its gate when it is made, as it takes its seats.
func (s *Server) SetPayLedger(where PayLedger) {
	s.mu.Lock()
	s.payLedger = where
	s.mu.Unlock()
}

// payGateFor is the gate a new session of this profile checks with.
func (s *Server) payGateFor(profile string, rules pay.Rules) *pay.Gate {
	s.mu.RLock()
	where := s.payLedger
	s.mu.RUnlock()
	path := ""
	if where != nil {
		path = where(profile)
	}
	return pay.NewGate(rules, path)
}

// payGate is this session's gate.  One built by hand, as a test builds
// one, gets its profile's rules and a record in memory.
func (h *Hosted) payGate() *pay.Gate {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pay == nil {
		h.pay = pay.NewGate(h.login.Pay, "")
	}
	return h.pay
}

// checkSpend decides whether a client's message goes out, when it is one
// that spends L$: a payment, or one of the purchases package pay reads.
// It answers "" when it may, and otherwise why not, having told a client
// on a stream; a one-shot sender, c nil, is told by the error
// sendMessage returns.
func (h *Hosted) checkSpend(ctx context.Context, c *Client, sentBy string, id msg.ID, body []byte) string {
	if !pay.Spends(id) {
		return ""
	}
	name := msg.Lookup(id).Name
	m := msg.New(id)
	a := h.Agent()
	var v pay.Verdict
	spends := true
	switch {
	case m.Decode(body) != nil:
		v = pay.Unread(nil, name, "the "+name+" could not be read, so it was not checked")
	case a == nil:
		v = pay.Unread(m, name, "the session is not up")
	default:
		v, spends = h.payGate().CheckMessage(ctx, a, a.Account.AgentID, m)
	}
	if !spends {
		return ""
	}
	if sentBy == "" {
		sentBy = ElsewhereClient
	}
	h.logf("pay: %s", v.Describe(sentBy))
	if v.Refused == "" {
		return ""
	}
	if c != nil {
		balance := -1
		if a != nil {
			if b, known := a.Balance(); known {
				balance = b
			}
		}
		c.refusePayment(v.Refusal(balance))
	}
	return v.Refused
}

// checkCap decides whether a capability request a client makes may go:
// the one capability that spends L$, AcceptGroupInvite, is checked as
// accepting the invitation is.  It answers "" when it may, and otherwise
// why not.  Nobody is told but the caller, whose error it is.
func (h *Hosted) checkCap(ctx context.Context, a *agent.Agent, r agent.CapRequest) string {
	p := pay.ReadCapPurchase(a, a.Account.AgentID, a.CapOf(r), r.Body)
	if p == nil {
		return ""
	}
	v := h.payGate().CheckPurchase(ctx, a, p)
	h.logf("pay: %s", v.Describe(ElsewhereClient))
	return v.Refused
}

// refusePayment hands a client the reply saying its payment was refused.
// On the answers queue, whatever it subscribed to: it is the answer to
// something it sent, and a client waiting for one is never left without.
func (c *Client) refusePayment(r *msg.MoneyBalanceReply) {
	body, err := r.Encode()
	if err != nil {
		return
	}
	c.answer(&pb.ServerPacket{Body: &pb.ServerPacket_Message{Message: &pb.InboundMessage{
		Id:         uint32(msg.IDOf(r)),
		Name:       r.MsgInfo().Name,
		Body:       body,
		ReceivedAt: time.Now().UnixMicro(),
		FromClient: pay.RefusedBy,
	}}})
}

// noteMoney logs what the grid says of a payment this avatar made,
// whoever asked for it.
func (h *Hosted) noteMoney(m *agent.Money) {
	me := h.self
	if me.IsZero() { // built by hand, as a test builds one
		if a := h.Agent(); a != nil {
			me = a.Account.AgentID
		}
	}
	if me.IsZero() || !m.Transfer() || m.Source != me {
		return
	}
	if m.Success {
		h.logf("pay: the grid says %q; L$%d left", m.Description, m.Balance)
		return
	}
	h.logf("pay: the grid refused L$%d to %s: %q", m.Amount, m.Dest, m.Description)
}
