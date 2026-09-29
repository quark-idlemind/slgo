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

	// NameOf is the name of an avatar, when the session has it or the
	// grid says it soon enough, and "" when not.  A refusal names who
	// was refused with it.
	NameOf(ctx context.Context, id msg.UUID) string

	// What a purchase is worth, from what the session has been told.
	// See ReadPurchase.
	Prices
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
	by, spent, why := g.admit(ctx, w, []charge{{amount: d.Amount, payee: d.Payee}})
	d.Spent = spent
	if why != "" {
		return refuse("%s", why)
	}
	d.AllowedBy = by[0]
	return d
}

// charge is one amount to let through: to whom, or to the grid itself.
type charge struct {
	amount int
	payee  msg.UUID
	grid   bool // a fee the grid takes, which no pay_to line covers
}

// admit is what a payment and a purchase share once the amounts are
// found to be ones that may be spent: who may be paid, the daily total,
// and the record.  It answers the pay_to line that let each charge
// through, what the last day held before them, and why not when they
// may not go.  g.mu is held.
func (g *Gate) admit(ctx context.Context, w World, cs []charge) (by []string, spent int, why string) {
	by = make([]string, len(cs))
	sum := 0
	for i, c := range cs {
		sum += c.amount
		if c.grid || c.amount == 0 {
			continue // nobody to be in pay_to
		}
		var why string
		if by[i], why = g.allows(ctx, w, c.payee); why != "" {
			return nil, 0, why
		}
	}

	past, err := g.read()
	if err != nil {
		return nil, 0, fmt.Sprintf("the record of what this profile has paid cannot be read: %v", err)
	}
	now := g.now()
	past = recent(past, now)
	spent = total(past)
	if limit := g.rules.DailyLimit(); spent+sum > limit {
		return nil, spent, fmt.Sprintf("L$%d more would make L$%d paid in the last 24 hours, over this profile's pay_daily of L$%d",
			sum, spent+sum, limit)
	}
	for _, c := range cs {
		if c.amount > 0 {
			past = append(past, Entry{At: now, Amount: c.amount, To: c.payee})
		}
	}
	if err := g.write(past); err != nil {
		return nil, spent, fmt.Sprintf("the payment could not be recorded, so it is not made: %v", err)
	}
	return by, spent, ""
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
	return "", fmt.Sprintf("%s is not in this profile's pay_to", who(ctx, w, payee))
}

// who is an avatar as a refusal names it: the name when it is known,
// then the key.
func who(ctx context.Context, w World, id msg.UUID) string {
	if id.IsZero() {
		return id.String()
	}
	if name := w.NameOf(ctx, id); name != "" {
		return fmt.Sprintf("%s (%s)", name, id)
	}
	return id.String()
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
