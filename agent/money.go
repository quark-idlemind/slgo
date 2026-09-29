package agent

// The avatar's L$, as the grid reports it.
//
// Every MoneyBalanceReply carries the balance, whatever else it says: a
// plain one answers a MoneyBalanceRequest, and one for a transaction
// comes to both sides of a payment, the same transaction id on each.
// The balance is kept, because it is not asked for again until somebody
// wants it, and each reply is handed to Options.OnMoney.
// Why: doc/money.md#what-the-grid-says

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"sync"

	"github.com/quark-idlemind/slgo/msg"
)

// Money is one MoneyBalanceReply.
type Money struct {
	// Transaction is the grid's id for the payment, the same on both
	// sides of it, and zero on a plain balance.
	Transaction msg.UUID

	// Success is false when the grid refused, and Description is its
	// sentence, which is empty on a plain balance.
	Success     bool
	Balance     int
	Description string

	// The transaction: its type (0 on a plain balance), who paid whom,
	// how much (-1 on a plain balance), and the reason the payer gave,
	// echoed.
	Type                   int
	Source, Dest           msg.UUID
	SourceGroup, DestGroup bool
	Amount                 int
	ItemDescription        string
}

// Transfer reports whether this is about a payment rather than only the
// balance: a plain one names nobody (llviewermessage.cpp:5947-5951).
func (m *Money) Transfer() bool { return !m.Source.IsZero() || !m.Dest.IsZero() }

// ReadMoney reads a MoneyBalanceReply.
func ReadMoney(m *msg.MoneyBalanceReply) *Money {
	d, t := &m.MoneyData, &m.TransactionInfo
	return &Money{
		Transaction:     d.TransactionID,
		Success:         d.TransactionSuccess,
		Balance:         int(d.MoneyBalance),
		Description:     trimNul(d.Description),
		Type:            int(t.TransactionType),
		Source:          t.SourceID,
		SourceGroup:     t.IsSourceGroup,
		Dest:            t.DestID,
		DestGroup:       t.IsDestGroup,
		Amount:          int(t.Amount),
		ItemDescription: trimNul(t.ItemDescription),
	}
}

// MoneyHandler is told of each MoneyBalanceReply.  It runs on the
// dispatch goroutine, so it hands over and returns.
type MoneyHandler func(*Money)

// money is the balance last reported, and whether one has been.
type money struct {
	mu      sync.Mutex
	balance int
	known   bool
}

// Balance is the L$ balance the grid last reported, and whether it has
// reported one.  It does not ask; a MoneyBalanceRequest does.
func (a *Agent) Balance() (int, bool) {
	a.money.mu.Lock()
	defer a.money.mu.Unlock()
	return a.money.balance, a.money.known
}

// keepMoney registers the handler that keeps the balance.
func (a *Agent) keepMoney() {
	a.Disp.MustHandle("MoneyBalanceReply", func(p *msg.Packet) {
		m := ReadMoney(p.Message.(*msg.MoneyBalanceReply))
		a.money.mu.Lock()
		a.money.balance, a.money.known = m.Balance, true
		a.money.mu.Unlock()
		if fn := a.opts.OnMoney; fn != nil {
			fn(m)
		}
	}, msg.Inline())
}

// ObjectOwner is who owns an object in the region's store, and whether
// the region has said.
func (a *Agent) ObjectOwner(id msg.UUID) (msg.UUID, bool) {
	o, ok := a.Objects().Get(id)
	if !ok || o.Owner.IsZero() {
		return msg.UUID{}, false
	}
	return o.Owner, true
}

// FindAvatar is the avatar whose whole name this is -- "First Last" in
// any case -- or the zero id when the grid knows nobody by it.
//
// It asks with AvatarPickerRequest, which matches only a whole name
// (see sl.Lookup), so an answer is never somebody whose name merely
// starts the same way.
func (a *Agent) FindAvatar(ctx context.Context, name string) (msg.UUID, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return msg.UUID{}, fmt.Errorf("agent: no name to look for")
	}
	// crypto/rand does not fail as of Go 1.24; a query id that repeated
	// would only share an answer.
	var query msg.UUID
	rand.Read(query[:])
	replies := make(chan []msg.AvatarPickerReply_Data, 1)
	a.pickers.Store(query, replies)
	defer a.pickers.Delete(query)

	m := &msg.AvatarPickerRequest{}
	m.AgentData.AgentID = a.Account.AgentID
	m.AgentData.SessionID = a.Account.SessionID
	m.AgentData.QueryID = query
	m.Data.Name = append([]byte(name), 0)
	if err := a.Send.SendReliable(ctx, m); err != nil {
		return msg.UUID{}, err
	}
	select {
	case rows := <-replies:
		for _, r := range rows {
			whole := strings.TrimSpace(trimNul(r.FirstName) + " " + trimNul(r.LastName))
			if !r.AvatarID.IsZero() && strings.EqualFold(whole, name) {
				return r.AvatarID, nil
			}
		}
		return msg.UUID{}, nil
	case <-ctx.Done():
		return msg.UUID{}, fmt.Errorf("agent: the grid did not say who %q is: %w", name, ctx.Err())
	}
}

// keepPickers registers the handler that hands a search's answer to
// whoever asked it.
func (a *Agent) keepPickers() {
	a.Disp.MustHandle("AvatarPickerReply", func(p *msg.Packet) {
		m := p.Message.(*msg.AvatarPickerReply)
		if ch, ok := a.pickers.Load(m.AgentData.QueryID); ok {
			select {
			case ch.(chan []msg.AvatarPickerReply_Data) <- m.Data:
			default:
			}
		}
	}, msg.Inline())
}
