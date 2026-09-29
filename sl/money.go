package sl

// L$: the balance, what is paid and received, and paying.
//
// Every answer is a MoneyBalanceReply.  A plain one answers a
// MoneyBalanceRequest; one for a payment comes to both sides of it, with
// the same transaction id on each.  Nothing in a request comes back in
// its reply that the sender chose, so a payment's reply is found by what
// it describes: the type, who paid whom, how much, and the reason, which
// the grid echoes.
//
// A payment is never sent twice.  One whose reply does not come is
// settled by reading the balance and comparing it with the balance read
// before sending.
// Why: doc/money.md

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/internal/pay"
	"github.com/quark-idlemind/slgo/msg"
)

// The transaction types Pay and PayObject send, and a Money may carry
// (lltransactiontypes.h).
const (
	TransactionGift      = pay.TransGift
	TransactionPayObject = pay.TransPayObject
)

// MaxPayReason is the longest reason a payment may carry, in bytes, as
// the viewer's pay dialog has it (floater_pay.xml:109).
const MaxPayReason = pay.MaxReason

// MoneyKind is which way a Money went.
type MoneyKind uint8

const (
	// MoneyBalance says the balance and no payment of this avatar's: the
	// answer to Balance, for one.
	MoneyBalance MoneyKind = iota

	// MoneyPaid is a payment this avatar made, or tried to.
	MoneyPaid

	// MoneyReceived is a payment made to this avatar.
	MoneyReceived
)

// Money is one answer about this avatar's L$.
type Money struct {
	At   time.Time
	Kind MoneyKind

	// Transaction is the grid's id for a payment, the same on both
	// sides of it; zero on a plain balance and on a refusal RefusedBy
	// made.
	Transaction msg.UUID

	// Type is the transaction type, 0 on a plain balance.
	Type int

	// From paid To.  Either may be a group, which the flags say.
	From, To           msg.UUID
	FromGroup, ToGroup bool

	// Amount is -1 on a plain balance.
	Amount int

	// Balance is this avatar's after the payment, as the grid says.
	Balance int

	// Success is false for a refusal, and Text is the grid's sentence:
	// "You paid Example Resident L$1.", or why not.
	Success bool
	Text    string

	// Description is the reason the payer gave, as the grid echoes it.
	Description string

	// RefusedBy is who refused the payment before it reached the grid:
	// pay.RefusedBy, which is slgod, and empty for what the grid said.
	// Its Balance is only the last the refuser knew of, and -1 when it
	// knew none.
	RefusedBy string

	// seq numbers the grid's answers as they arrived, and claimed says
	// a payment took this one as its answer; both under the session's
	// lock.  See movedBetween.
	seq     uint64
	claimed bool
}

// Sender says what From names: a group, the grid when nobody paid, and
// otherwise a person.
func (m *Money) Sender() Sender {
	switch {
	case m.FromGroup:
		return SenderGroup
	case m.From.IsZero():
		return SenderGrid
	}
	return SenderPerson
}

// moneyKept is how many of the grid's answers the session remembers, for
// a payment whose own answer did not come to be settled against.
const moneyKept = 64

// DefaultMoneyDepth is the buffer a subscription gets when none is asked
// for.
const DefaultMoneyDepth = 16

// moneySub is a subscription to money.
type moneySub struct {
	ch      chan *Money
	dropped atomic.Uint64
}

// Money returns a channel of every answer about this avatar's L$ --
// payments made, payments received, refusals and plain balances -- and it
// is closed when StopMoney is called or the session ends.  Kind says
// which way each went.
func (w *Session) Money(depth int) <-chan *Money {
	if depth <= 0 {
		depth = DefaultMoneyDepth
	}
	sub := &moneySub{ch: make(chan *Money, depth)}
	if !w.addSub(func() { w.moneySubs[sub.ch] = sub }) {
		close(sub.ch)
	}
	return sub.ch
}

// StopMoney closes a subscription, and returns once it is closed.
func (w *Session) StopMoney(ch <-chan *Money) {
	w.onReader(func() {
		if s := w.moneySubs[ch]; s != nil {
			delete(w.moneySubs, ch)
			close(s.ch)
		}
	})
}

// MoneyDropped is how many a subscription missed because its buffer was
// full.
func (w *Session) MoneyDropped(ch <-chan *Money) uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	if s := w.moneySubs[ch]; s != nil {
		return s.dropped.Load()
	}
	return 0
}

// moneyReply takes one MoneyBalanceReply: it is numbered and kept, given
// to the payment waiting for it if there is one, and delivered.
func (w *Session) moneyReply(raw *client.Message, t *msg.MoneyBalanceReply) {
	a := agent.ReadMoney(t)
	m := &Money{
		At: time.Now(), Transaction: a.Transaction, Type: a.Type,
		From: a.Source, To: a.Dest, FromGroup: a.SourceGroup, ToGroup: a.DestGroup,
		Amount: a.Amount, Balance: a.Balance, Success: a.Success,
		Text: a.Description, Description: a.ItemDescription,
	}
	switch {
	case a.Source == w.me && a.Transfer():
		m.Kind = MoneyPaid
	case a.Dest == w.me:
		m.Kind = MoneyReceived
	}
	if raw != nil && raw.FromClient != "" {
		// A client's own MoneyBalanceReply, echoed, is nobody's word.
		if raw.FromClient != pay.RefusedBy {
			return
		}
		m.RefusedBy = raw.FromClient
	}

	w.mu.Lock()
	if m.RefusedBy == "" {
		w.moneySeq++
		m.seq = w.moneySeq
		w.moneyLog = append(w.moneyLog, m)
		if n := len(w.moneyLog); n > moneyKept {
			w.moneyLog = append([]*Money(nil), w.moneyLog[n-moneyKept:]...)
		}
	}
	// The oldest payment waiting that this answers takes it, so that two
	// alike are answered one each.
	for _, p := range w.payWaits {
		if p.reply == nil && p.answeredBy(m, w.me) {
			p.reply, m.claimed = m, true
			break
		}
	}
	subs := make([]*moneySub, 0, len(w.moneySubs))
	for _, s := range w.moneySubs {
		subs = append(subs, s)
	}
	w.mu.Unlock()

	for _, s := range subs {
		select {
		case s.ch <- m:
		default:
			s.dropped.Add(1)
		}
	}
}

// Balance asks the grid for this avatar's L$ balance and waits for the
// answer, for as long as Options.MoneyTimeout.
func (w *Session) Balance(ctx context.Context) (int, error) {
	b, _, err := w.balance(ctx)
	return b, err
}

// balance is Balance, with the number of the answer it read.  Any answer
// of the grid's after the request says the balance as it is now.
func (w *Session) balance(ctx context.Context) (int, uint64, error) {
	w.mu.Lock()
	mark := w.moneySeq
	w.mu.Unlock()

	m := &msg.MoneyBalanceRequest{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	if err := w.Send(ctx, m); err != nil {
		return 0, 0, err
	}
	var got *Money
	err := w.await(ctx, w.moneyWait(), "the L$ balance", func() bool {
		if w.moneySeq > mark && len(w.moneyLog) > 0 {
			got = w.moneyLog[len(w.moneyLog)-1]
			return true
		}
		return false
	})
	if err != nil {
		return 0, 0, err
	}
	return got.Balance, got.seq, nil
}

// payment is what one Pay or PayObject asked for.
type payment struct {
	typ    int
	dest   msg.UUID
	amount int
	reason string

	// payees is what the grid may name as the destination in its
	// answer: the avatar, or for an object the object or its owner.
	payees []msg.UUID
}

// payWait is a payment waiting for its answer, which the reader puts in
// reply under the session's lock.
type payWait struct {
	payment
	reply *Money
}

// answeredBy is whether m describes this payment.
func (p *payWait) answeredBy(m *Money, me msg.UUID) bool {
	if m.Type != p.typ || m.From != me || m.Amount != p.amount {
		return false
	}
	// The grid names an empty reason "Payment", as the viewer's source
	// has it (llviewermessage.cpp:5846-5847); not measured.
	if m.Description != p.reason && !(p.reason == "" && m.Description == "Payment") {
		return false
	}
	for _, id := range p.payees {
		if m.To == id {
			return true
		}
	}
	return false
}

// PayRefused is a payment or a purchase refused: by the grid, or before
// it reached the grid by slgod or by the profile's rules.  A session
// held here returns it from Send and DoCap; one held through slgod is
// told by a MoneyBalanceReply signed pay.RefusedBy instead, which Pay
// finds and the calls that buy do not wait for.
type PayRefused struct {
	// By is "the grid", pay.RefusedBy, or "this profile's rules" for a
	// session held here.
	By string

	// Reason is the grid's sentence, or the rule that refused it.
	Reason string
}

func (e *PayRefused) Error() string {
	return fmt.Sprintf("sl: %s refused it: %s", e.By, e.Reason)
}

// PayOutcome is what a payment whose answer never came turned out to be,
// by the balance.
type PayOutcome uint8

const (
	// PayUnknown is a balance that moved by something other than the
	// amount, or that could not be read.
	PayUnknown PayOutcome = iota

	// PaidUnconfirmed is a balance down by exactly the amount: it was
	// paid, and the grid's answer was lost.
	PaidUnconfirmed

	// NotPaid is a balance that has not moved.
	NotPaid
)

// PayUnconfirmed is a payment the grid did not answer.  It was not sent
// again; the balance says what became of it.
type PayUnconfirmed struct {
	Outcome PayOutcome
	Amount  int

	// Before is the balance read before sending, and After the one read
	// once the answer was given up on.  Received is what others were
	// seen to pay this avatar in between, and Paid what this avatar's
	// other payments came to, both already allowed for.
	Before, After  int
	Received, Paid int

	// Wait is why the answer was given up on: ErrTimeout, or the
	// caller's context.  BalanceErr is why After could not be read.
	Wait       error
	BalanceErr error
}

func (e *PayUnconfirmed) Error() string {
	head := fmt.Sprintf("sl: the grid did not answer the payment of L$%d (%v)", e.Amount, e.Wait)
	if e.BalanceErr != nil {
		return fmt.Sprintf("%s, and the balance could not be read (%v): whether it was paid is not known", head, e.BalanceErr)
	}
	moved := fmt.Sprintf("the balance went from L$%d to L$%d", e.Before, e.After)
	if e.Received != 0 || e.Paid != 0 {
		moved += fmt.Sprintf(", with L$%d received and L$%d paid otherwise meanwhile", e.Received, e.Paid)
	}
	switch e.Outcome {
	case PaidUnconfirmed:
		return fmt.Sprintf("%s; %s: it was paid, and the grid's answer was lost", head, moved)
	case NotPaid:
		return fmt.Sprintf("%s; %s: it was not paid", head, moved)
	}
	return fmt.Sprintf("%s; %s: whether it was paid is not known", head, moved)
}

// Unwrap is why the answer was given up on.
func (e *PayUnconfirmed) Unwrap() error { return e.Wait }

// Pay gives an avatar L$ -- the viewer's pay dialog's gift -- and waits
// for the grid to answer, handing back the transaction and the balance
// after.
//
// A refusal is a *PayRefused carrying the grid's sentence, or slgod's
// reason.  An amount below 1, a reason longer than MaxPayReason, or a
// balance read beforehand that is short of the amount is refused before
// anything is sent; the viewer sends nothing it cannot afford either.
//
// It is never sent twice.  An answer that does not come within
// Options.MoneyTimeout is a *PayUnconfirmed, which says from the balance
// whether it was paid.
// Why: doc/money.md#never-twice
func (w *Session) Pay(ctx context.Context, to msg.UUID, amount int, reason string) (msg.UUID, int, error) {
	if to.IsZero() {
		return msg.UUID{}, 0, fmt.Errorf("sl: nobody to pay")
	}
	return w.pay(ctx, payment{typ: TransactionGift, dest: to, amount: amount, reason: reason, payees: []msg.UUID{to}})
}

// PayObject pays an object in the region, as the viewer's pay dialog
// does, which is what a script's money event hears.  The object's owner
// is read first, since slgod lets an object be paid only when its owner
// may be.  An empty reason sends the object's name, as the viewer does
// (llfloaterpay.cpp:615-624).  Otherwise it is Pay.
func (w *Session) PayObject(ctx context.Context, o *Object, amount int, reason string) (msg.UUID, int, error) {
	if err := payable(amount, reason); err != nil {
		return msg.UUID{}, 0, err
	}
	w.mu.Lock()
	delete(w.owners, o.ID)
	w.mu.Unlock()
	if err := w.Send(ctx, w.familyRequest(o.ID)); err != nil {
		return msg.UUID{}, 0, err
	}
	var owner msg.UUID
	var name string
	err := w.await(ctx, w.moneyWait(), "the owner of "+o.ID.String(), func() bool {
		owner, name = w.owners[o.ID], w.objectNames[o.ID]
		return !owner.IsZero()
	})
	if err != nil {
		return msg.UUID{}, 0, fmt.Errorf("sl: the owner of %s is not known, so it was not paid: %w", o.ID, err)
	}
	if reason == "" {
		reason = name
	}
	return w.pay(ctx, payment{typ: TransactionPayObject, dest: o.ID, amount: amount, reason: reason,
		payees: []msg.UUID{o.ID, owner}})
}

// payable refuses what is refused before anything is sent.
func payable(amount int, reason string) error {
	if amount < 1 {
		return fmt.Errorf("sl: L$%d cannot be paid; a payment is L$1 or more", amount)
	}
	if len(reason) > MaxPayReason {
		return fmt.Errorf("sl: the reason is %d bytes and a payment carries %d", len(reason), MaxPayReason)
	}
	return nil
}

func (w *Session) pay(ctx context.Context, p payment) (msg.UUID, int, error) {
	if err := payable(p.amount, p.reason); err != nil {
		return msg.UUID{}, 0, err
	}
	before, beforeSeq, err := w.balance(ctx)
	if err != nil {
		return msg.UUID{}, 0, fmt.Errorf("sl: the balance could not be read, so nothing was paid: %w", err)
	}
	if before < p.amount {
		return msg.UUID{}, before, fmt.Errorf("sl: the balance is L$%d, short of L$%d; nothing was paid", before, p.amount)
	}

	pw := &payWait{payment: p}
	w.mu.Lock()
	w.payWaits = append(w.payWaits, pw)
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		for i, q := range w.payWaits {
			if q == pw {
				w.payWaits = append(w.payWaits[:i:i], w.payWaits[i+1:]...)
				break
			}
		}
		w.mu.Unlock()
	}()

	// Every field as the viewer's give_money fills it
	// (llviewermessage.cpp:458-494): no group either side, and no
	// aggregate permissions.
	m := &msg.MoneyTransferRequest{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.MoneyData.SourceID = w.me
	m.MoneyData.DestID = p.dest
	m.MoneyData.Amount = int32(p.amount)
	m.MoneyData.TransactionType = int32(p.typ)
	m.MoneyData.Description = append([]byte(p.reason), 0)
	if err := w.Send(ctx, m); err != nil {
		return msg.UUID{}, before, err
	}

	waited := w.await(ctx, w.moneyWait(), "the grid's answer to the payment", func() bool { return pw.reply != nil })
	if waited == nil {
		return paid(pw.reply)
	}

	// No answer.  Never sent again: read the balance and say which.  A
	// caller that gave up is still owed that.
	look := ctx
	if ctx.Err() != nil {
		look = context.WithoutCancel(ctx)
	}
	after, afterSeq, berr := w.balance(look)
	w.mu.Lock()
	reply := pw.reply
	received, paidElse, unsure := w.movedBetween(beforeSeq, afterSeq)
	w.mu.Unlock()
	if reply != nil {
		// It came while the balance was being read.
		return paid(reply)
	}
	u := &PayUnconfirmed{Amount: p.amount, Before: before, Wait: waited}
	if berr != nil {
		u.BalanceErr = berr
		return msg.UUID{}, before, u
	}
	u.After, u.Received, u.Paid = after, received, paidElse
	switch delta := after - before - received + paidElse; {
	case delta == -p.amount:
		u.Outcome = PaidUnconfirmed
	case delta == 0 && !unsure:
		u.Outcome = NotPaid
	}
	return msg.UUID{}, after, u
}

// paid is what a payment's answer means for its caller.
func paid(r *Money) (msg.UUID, int, error) {
	switch {
	case r.RefusedBy != "":
		return msg.UUID{}, r.Balance, &PayRefused{By: r.RefusedBy, Reason: r.Text}
	case !r.Success:
		return r.Transaction, r.Balance, &PayRefused{By: "the grid", Reason: r.Text}
	}
	return r.Transaction, r.Balance, nil
}

// movedBetween is what the grid's answers after from and up to to say
// moved the balance, as far as they can be told apart from a payment
// whose answer was not recognised: what others paid this avatar, and
// what payments another call took as its answer came to.  unsure is a
// payment of this avatar's that no call took, or answers no longer kept.
// Called with mu held.
func (w *Session) movedBetween(from, to uint64) (received, paid int, unsure bool) {
	if len(w.moneyLog) > 0 && w.moneyLog[0].seq > from+1 {
		unsure = true
	}
	for _, m := range w.moneyLog {
		if m.seq <= from || m.seq > to || !m.Success {
			continue
		}
		switch {
		case m.Kind == MoneyReceived:
			received += m.Amount
		case m.Kind == MoneyPaid && m.claimed:
			paid += m.Amount
		case m.Kind == MoneyPaid:
			unsure = true
		}
	}
	return received, paid, unsure
}
