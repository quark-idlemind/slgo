package server

// Paying, which slgod checks for every client.
//
// A client's message is a body this daemon frames and forwards, so any
// client with the secret could put its own MoneyTransferRequest on the
// circuit.  So sendMessage reads that one message on its way out and
// checks it against the profile's rules (package pay) before it goes.
// A refusal goes back to the client that sent it -- a MoneyBalanceReply
// signed pay.RefusedBy on a stream, the error of a one-shot Send -- and
// every refusal and every payment let through is logged.
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

var moneyTransferRequest = msg.IDOf(&msg.MoneyTransferRequest{})

// checkPayment decides whether a client's MoneyTransferRequest goes out.
// It answers "" when it may, and otherwise why not, having told a
// client on a stream; a one-shot sender, c nil, is told by the error
// sendMessage returns.
func (h *Hosted) checkPayment(ctx context.Context, c *Client, sentBy string, body []byte) string {
	m := &msg.MoneyTransferRequest{}
	var d pay.Decision
	a := h.Agent()
	switch {
	case m.Decode(body) != nil:
		d.Refused = "the MoneyTransferRequest could not be read, so it was not checked"
	case a == nil:
		d.Transfer = pay.ReadTransfer(m)
		d.Refused = "the session is not up"
	default:
		d = h.payGate().Check(ctx, a, m)
	}
	if sentBy == "" {
		sentBy = ElsewhereClient
	}
	h.logf("pay: %s", d.Describe(sentBy))
	if d.Refused == "" {
		return ""
	}
	if c != nil {
		balance := -1
		if a != nil {
			if b, known := a.Balance(); known {
				balance = b
			}
		}
		c.refusePayment(pay.Refusal(m, balance, d.Refused))
	}
	return d.Refused
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
