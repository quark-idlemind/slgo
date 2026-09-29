package pay

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// World is what a check asks of the session a payment would go out on.
// *agent.Agent is one.
type World interface {
	// ObjectOwner is who owns an object the region has described, and
	// whether that is known.
	ObjectOwner(id msg.UUID) (msg.UUID, bool)

	// FindAvatar is the avatar whose whole name this is, or the zero id
	// when nobody has it.
	FindAvatar(ctx context.Context, name string) (msg.UUID, error)
}

// lookupFor bounds the asking of the grid who a pay_to name is.
const lookupFor = 15 * time.Second

// Gate checks payments against one profile's rules, and records the
// ones it lets through.
type Gate struct {
	rules  Rules
	ledger string // "" keeps the record in memory
	now    func() time.Time

	mu    sync.Mutex
	mem   []Entry
	names map[string]msg.UUID // pay_to names the grid has answered for
}

// NewGate checks against rules and keeps the record at ledger, or in
// memory when ledger is empty.
func NewGate(rules Rules, ledger string) *Gate {
	return &Gate{rules: rules, ledger: ledger, now: time.Now, names: map[string]msg.UUID{}}
}

// Decision is what Check made of a payment.
type Decision struct {
	Transfer

	// Payee is who the money reaches: the destination, or the owner of
	// the object paid.  Zero when that is not known.
	Payee msg.UUID

	// AllowedBy is the pay_to line that let it through.
	AllowedBy string

	// Spent is what the ledger held for the last day before this one.
	Spent int

	// Refused says why not, and is empty when the payment may go.
	Refused string
}

// Check decides whether a MoneyTransferRequest may go out, and records
// it against the daily total when it may.  It is serialised, so that two
// payments at once cannot both fit under a limit only one of them fits
// under.
func (g *Gate) Check(ctx context.Context, w World, m *msg.MoneyTransferRequest) Decision {
	g.mu.Lock()
	defer g.mu.Unlock()

	d := Decision{Transfer: ReadTransfer(m)}
	r := g.rules
	refuse := func(format string, v ...any) Decision {
		d.Refused = fmt.Sprintf(format, v...)
		return d
	}

	if !r.On {
		return refuse("paying is off for this profile; pay = on in the profile turns it on")
	}
	if len(r.To) == 0 {
		return refuse("this profile's pay_to names nobody; add pay_to = NAME for each avatar it may pay, or pay_to = * for anyone")
	}
	if d.Amount < 1 {
		return refuse("L$%d is not an amount that can be paid", d.Amount)
	}
	if max := r.MaxPayment(); d.Amount > max {
		return refuse("L$%d is more than this profile's pay_max of L$%d", d.Amount, max)
	}

	d.Payee = d.Dest
	if d.Type == TransPayObject {
		owner, ok := w.ObjectOwner(d.Dest)
		if !ok {
			return refuse("the owner of object %s is not known, and an object is paid only when its owner may be", d.Dest)
		}
		d.Payee = owner
	}
	by, why := g.allows(ctx, w, d.Payee)
	if why != "" {
		return refuse("%s", why)
	}
	d.AllowedBy = by

	past, err := g.read()
	if err != nil {
		return refuse("the record of what this profile has paid cannot be read: %v", err)
	}
	now := g.now()
	past = recent(past, now)
	d.Spent = total(past)
	if limit := r.DailyLimit(); d.Spent+d.Amount > limit {
		return refuse("L$%d more would make L$%d paid in the last 24 hours, over this profile's pay_daily of L$%d",
			d.Amount, d.Spent+d.Amount, limit)
	}
	if err := g.write(append(past, Entry{At: now, Amount: d.Amount, To: d.Payee})); err != nil {
		return refuse("the payment could not be recorded, so it is not made: %v", err)
	}
	return d
}

// allows says which pay_to line lets payee be paid, or why none does.
func (g *Gate) allows(ctx context.Context, w World, payee msg.UUID) (string, string) {
	if g.rules.anyone() {
		return "*", ""
	}
	var names []string
	for _, to := range g.rules.To {
		if id, err := msg.ParseUUID(to); err == nil {
			if id == payee {
				return to, ""
			}
			continue
		}
		names = append(names, to)
	}
	for _, name := range names {
		id, ok := g.names[name]
		if !ok {
			look, cancel := context.WithTimeout(ctx, lookupFor)
			found, err := w.FindAvatar(look, WholeName(name))
			cancel()
			if err != nil {
				return "", fmt.Sprintf("the grid could not be asked who pay_to %q is: %v", name, err)
			}
			// Nobody by that name is remembered too: a name does not
			// come to belong to somebody later.
			g.names[name] = found
			id = found
		}
		if !id.IsZero() && id == payee {
			return name, ""
		}
	}
	return "", fmt.Sprintf("%s is not in this profile's pay_to", payee)
}

func (g *Gate) read() ([]Entry, error) {
	if g.ledger == "" {
		return append([]Entry(nil), g.mem...), nil
	}
	return readLedger(g.ledger)
}

func (g *Gate) write(es []Entry) error {
	if g.ledger == "" {
		g.mem = es
		return nil
	}
	return writeLedger(g.ledger, es)
}

// Refusal is a MoneyBalanceReply saying a payment was refused before it
// reached the grid: the transaction filled in as the grid fills one in,
// so that it is matched the way the grid's answer would be, success
// false, and why in the grid's sentence.  balance is the last the
// refuser knew of.
// Why: doc/money.md#how-a-refusal-reaches-the-client
func Refusal(m *msg.MoneyTransferRequest, balance int, why string) *msg.MoneyBalanceReply {
	r := &msg.MoneyBalanceReply{}
	r.MoneyData.AgentID = m.AgentData.AgentID
	r.MoneyData.MoneyBalance = int32(balance)
	r.MoneyData.Description = CString(why, 255)
	r.TransactionInfo.TransactionType = m.MoneyData.TransactionType
	r.TransactionInfo.SourceID = m.MoneyData.SourceID
	r.TransactionInfo.DestID = m.MoneyData.DestID
	r.TransactionInfo.IsDestGroup = m.MoneyData.Flags&FlagDestGroup != 0
	r.TransactionInfo.Amount = m.MoneyData.Amount
	r.TransactionInfo.ItemDescription = append([]byte(nil), m.MoneyData.Description...)
	return r
}

// Describe is a Decision as a line of slgod's log: who asked, whom it
// was for, how much, and what became of it.
func (d Decision) Describe(who string) string {
	to := d.Dest.String()
	if d.Type == TransPayObject {
		to = fmt.Sprintf("object %s owned by %s", d.Dest, d.Payee)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s asked to pay %s L$%d", who, to, d.Amount)
	if d.Refused != "" {
		fmt.Fprintf(&b, "; refused: %s", d.Refused)
		return b.String()
	}
	fmt.Fprintf(&b, "; passed by pay_to %s, L$%d paid in the last 24 hours before it", d.AllowedBy, d.Spent)
	return b.String()
}
