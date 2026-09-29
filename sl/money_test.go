package sl

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/internal/pay"
	"github.com/quark-idlemind/slgo/msg"
)

var (
	examplePayee = msg.MustParseUUID("92f67e57-7e57-c0de-24de-53f27a898992")
	anotherPayer = msg.MustParseUUID("cd957e57-7e57-c0de-26e8-543033dda432")
	aPaidLamp    = msg.MustParseUUID("1b737e57-7e57-c0de-de35-3390805ca1b0")
	payTx        = msg.MustParseUUID("c14e7e57-7e57-c0de-824e-a88b9b0cd5f8")
	otherTx      = msg.MustParseUUID("c1ad7e57-7e57-c0de-3272-2be82b0bbc49")
)

// moneyReplyOf is a MoneyBalanceReply as the grid sends one.
func moneyReplyOf(tx msg.UUID, ok bool, balance int, said string, typ int, from, to msg.UUID, amount int, echo string) *msg.MoneyBalanceReply {
	m := &msg.MoneyBalanceReply{}
	m.MoneyData.TransactionID = tx
	m.MoneyData.TransactionSuccess = ok
	m.MoneyData.MoneyBalance = int32(balance)
	m.MoneyData.Description = append([]byte(said), 0)
	m.TransactionInfo.TransactionType = int32(typ)
	m.TransactionInfo.SourceID = from
	m.TransactionInfo.DestID = to
	m.TransactionInfo.Amount = int32(amount)
	m.TransactionInfo.ItemDescription = append([]byte(echo), 0)
	return m
}

// plainBalance is the answer to a MoneyBalanceRequest, as measured: no
// transaction, zero ids, and an amount of -1.
func plainBalance(balance int) *msg.MoneyBalanceReply {
	return moneyReplyOf(msg.UUID{}, true, balance, "", 0, msg.UUID{}, msg.UUID{}, -1, "")
}

// answerBalances has the fake answer each MoneyBalanceRequest with the
// next of these balances, the last repeating.
func answerBalances(t *testing.T, f *fakeBackend, balances ...int) {
	t.Helper()
	var mu sync.Mutex
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.MoneyBalanceRequest); !ok {
			return
		}
		mu.Lock()
		b := balances[0]
		if len(balances) > 1 {
			balances = balances[1:]
		}
		mu.Unlock()
		f.Relay(t, plainBalance(b))
	}
}

func TestBalanceAsksAndReadsTheAnswer(t *testing.T) {
	w, f := newFakeSession(t)
	wait := aside(t, func() (int, error) { return w.Balance(context.Background()) })
	q := waitSent[*msg.MoneyBalanceRequest](t, f)
	if q.AgentData.AgentID != testAgentID || q.AgentData.SessionID != testSessionID || !q.MoneyData.TransactionID.IsZero() {
		t.Errorf("asked with %+v", q)
	}
	f.Relay(t, plainBalance(120))
	if b, err := wait(); err != nil || b != 120 {
		t.Errorf("balance %d, %v", b, err)
	}
}

func TestABalanceNobodyAnswersRunsOut(t *testing.T) {
	w, _ := newFakeSession(t)
	w.SetOptions(Options{MoneyTimeout: 50 * time.Millisecond})
	if _, err := w.Balance(context.Background()); !errors.Is(err, ErrTimeout) {
		t.Errorf("err = %v, want a timeout", err)
	}
}

func TestPayFillsTheRequestAsTheViewerDoes(t *testing.T) {
	w, f := newFakeSession(t)
	answerBalances(t, f, 100)
	wait := aside(t, func() (msg.UUID, error) {
		tx, b, err := w.Pay(context.Background(), examplePayee, 5, "for the lamp")
		if b != 95 && err == nil {
			t.Errorf("balance after %d", b)
		}
		return tx, err
	})
	m := waitSent[*msg.MoneyTransferRequest](t, f)
	d := m.MoneyData
	if m.AgentData.AgentID != testAgentID || m.AgentData.SessionID != testSessionID ||
		d.SourceID != testAgentID || d.DestID != examplePayee || d.Flags != 0 || d.Amount != 5 ||
		d.AggregatePermNextOwner != 0 || d.AggregatePermInventory != 0 ||
		d.TransactionType != 5001 || string(d.Description) != "for the lamp\x00" {
		t.Errorf("sent %+v", m)
	}
	f.Relay(t, moneyReplyOf(payTx, true, 95, "You paid Example Resident L$5.", 5001, testAgentID, examplePayee, 5, "for the lamp"))
	if tx, err := wait(); err != nil || tx != payTx {
		t.Errorf("tx %v, %v", tx, err)
	}
}

// Nothing a payment sends comes back in its answer but what it
// describes, so a payment received meanwhile, and a plain balance, are
// told apart from it by that.
func TestPayTakesItsOwnAnswerAndNotAPaymentReceived(t *testing.T) {
	w, f := newFakeSession(t)
	heard := w.Money(0)
	answerBalances(t, f, 100)
	wait := aside(t, func() (msg.UUID, error) {
		tx, _, err := w.Pay(context.Background(), examplePayee, 5, "for the lamp")
		return tx, err
	})
	waitSent[*msg.MoneyTransferRequest](t, f)

	f.Relay(t, moneyReplyOf(otherTx, true, 105, "Example Resident paid you L$5.", 5001, examplePayee, testAgentID, 5, "for the lamp"))
	f.Relay(t, plainBalance(105))
	f.Relay(t, moneyReplyOf(payTx, true, 100, "You paid Example Resident L$5.", 5001, testAgentID, examplePayee, 5, "for the lamp"))
	if tx, err := wait(); err != nil || tx != payTx {
		t.Fatalf("tx %v, %v; want the payment's own", tx, err)
	}

	var kinds []MoneyKind
	for len(kinds) < 4 {
		select {
		case m := <-heard:
			kinds = append(kinds, m.Kind)
		case <-time.After(5 * time.Second):
			t.Fatalf("heard %v", kinds)
		}
	}
	// The balance read before paying, the payment received, a balance,
	// and the payment made.
	want := []MoneyKind{MoneyBalance, MoneyReceived, MoneyBalance, MoneyPaid}
	for i := range want {
		if kinds[i] != want[i] {
			t.Errorf("heard %v, want %v", kinds, want)
			break
		}
	}
}

func TestARefusalCarriesTheGridsSentence(t *testing.T) {
	w, f := newFakeSession(t)
	answerBalances(t, f, 100)
	wait := asideErr(t, func() error {
		_, _, err := w.Pay(context.Background(), examplePayee, 50, "")
		return err
	})
	waitSent[*msg.MoneyTransferRequest](t, f)
	// Measured: a refusal is filled in as a success is, the amount that
	// was asked for included.
	f.Relay(t, moneyReplyOf(payTx, false, 100, "Insufficient funds.", 5001, testAgentID, examplePayee, 50, ""))
	var r *PayRefused
	if err := wait(); !errors.As(err, &r) || r.By != "the grid" || r.Reason != "Insufficient funds." {
		t.Errorf("err = %v", err)
	}
}

func TestSlgodsRefusalIsARefusal(t *testing.T) {
	w, f := newFakeSession(t)
	answerBalances(t, f, 100)
	wait := asideErr(t, func() error {
		_, _, err := w.Pay(context.Background(), examplePayee, 5, "for the lamp")
		return err
	})
	m := waitSent[*msg.MoneyTransferRequest](t, f)
	r := pay.Refusal(m, 100, "paying is off for this profile")
	body, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	f.RelayRaw(t, &Message{ID: msg.IDOf(r), Name: "MoneyBalanceReply", Body: body, At: time.Now(), FromClient: pay.RefusedBy})
	var refused *PayRefused
	if err := wait(); !errors.As(err, &refused) || refused.By != pay.RefusedBy || refused.Reason != "paying is off for this profile" {
		t.Errorf("err = %v", err)
	}
}

// A MoneyBalanceReply another client put on the circuit comes back as an
// echo, and it is nobody's word about anything.
func TestAnEchoedMoneyReplyIsNotBelieved(t *testing.T) {
	w, f := newFakeSession(t)
	heard := w.Money(0)
	r := moneyReplyOf(payTx, true, 1000000, "You paid Example Resident L$5.", 5001, testAgentID, examplePayee, 5, "")
	body, _ := r.Encode()
	f.RelayRaw(t, &Message{ID: msg.IDOf(r), Name: "MoneyBalanceReply", Body: body, At: time.Now(), FromClient: "another client"})
	select {
	case m := <-heard:
		t.Errorf("an echo was delivered: %+v", m)
	default:
	}
}

// losing is a payment whose answer never comes, the balance read before
// it and after it as given, and what was relayed while it waited.
func losing(t *testing.T, before, after int, meanwhile ...*msg.MoneyBalanceReply) (int, error) {
	t.Helper()
	w, f := newFakeSession(t)
	w.SetOptions(Options{MoneyTimeout: 300 * time.Millisecond})
	answerBalances(t, f, before, after)
	wait := aside(t, func() (int, error) {
		_, b, err := w.Pay(context.Background(), examplePayee, 5, "for the lamp")
		return b, err
	})
	waitSent[*msg.MoneyTransferRequest](t, f)
	for _, m := range meanwhile {
		f.Relay(t, m)
	}
	b, err := wait()
	if n := len(sentOf[*msg.MoneyTransferRequest](f)); n != 1 {
		t.Errorf("the payment was sent %d times", n)
	}
	return b, err
}

func outcome(t *testing.T, err error) PayOutcome {
	t.Helper()
	var u *PayUnconfirmed
	if !errors.As(err, &u) {
		t.Fatalf("err = %v, want a *PayUnconfirmed", err)
	}
	if !errors.Is(err, ErrTimeout) {
		t.Errorf("%v does not say it timed out", err)
	}
	return u.Outcome
}

func TestALostAnswerIsSettledByTheBalance(t *testing.T) {
	if b, err := losing(t, 100, 95); outcome(t, err) != PaidUnconfirmed || b != 95 {
		t.Errorf("down by the amount: %v, balance %d", err, b)
	}
	if _, err := losing(t, 100, 100); outcome(t, err) != NotPaid {
		t.Errorf("unchanged: %v", err)
	}
	if _, err := losing(t, 100, 90); outcome(t, err) != PayUnknown {
		t.Errorf("down by more: %v", err)
	}
}

// A payment received while the answer was awaited moves the balance too,
// and is counted as what it is.
func TestAPaymentReceivedMeanwhileIsNotTakenForTheOneSent(t *testing.T) {
	received := moneyReplyOf(otherTx, true, 102, "Another Resident paid you L$2.", 5001, anotherPayer, testAgentID, 2, "")
	_, err := losing(t, 100, 97, received)
	if outcome(t, err) != PaidUnconfirmed {
		t.Errorf("paid 5 and received 2: %v", err)
	}
	_, err = losing(t, 100, 102, received)
	if outcome(t, err) != NotPaid {
		t.Errorf("received 2 and paid nothing: %v", err)
	}
}

func TestWhatIsRefusedBeforeAnythingIsSent(t *testing.T) {
	w, f := newFakeSession(t)
	answerBalances(t, f, 3)
	for _, c := range []struct {
		amount int
		reason string
	}{
		{0, ""}, {-4, ""}, {1, string(make([]byte, MaxPayReason+1))}, {5, ""},
	} {
		if _, _, err := w.Pay(context.Background(), examplePayee, c.amount, c.reason); err == nil {
			t.Errorf("L$%d with a reason of %d bytes was paid", c.amount, len(c.reason))
		}
	}
	if n := len(sentOf[*msg.MoneyTransferRequest](f)); n != 0 {
		t.Errorf("%d payments went out", n)
	}
}

func TestTwoAlikePaymentsAreAnsweredOneEach(t *testing.T) {
	w, f := newFakeSession(t)
	answerBalances(t, f, 100)
	pay := func() (msg.UUID, error) {
		tx, _, err := w.Pay(context.Background(), examplePayee, 5, "")
		return tx, err
	}
	first := aside(t, pay)
	waitSentN[*msg.MoneyTransferRequest](t, f, 1)
	second := aside(t, pay)
	waitSentN[*msg.MoneyTransferRequest](t, f, 2)

	f.Relay(t, moneyReplyOf(payTx, true, 95, "You paid Example Resident L$5.", 5001, testAgentID, examplePayee, 5, "Payment"))
	f.Relay(t, moneyReplyOf(otherTx, true, 90, "You paid Example Resident L$5.", 5001, testAgentID, examplePayee, 5, "Payment"))
	a, errA := first()
	b, errB := second()
	if errA != nil || errB != nil || a != payTx || b != otherTx {
		t.Errorf("first %v %v, second %v %v", a, errA, b, errB)
	}
}

func TestPayObjectPaysTheObjectAsTheViewerDoes(t *testing.T) {
	w, f := newFakeSession(t)
	answerBalances(t, f, 100)
	wait := aside(t, func() (msg.UUID, error) {
		tx, _, err := w.PayObject(context.Background(), &Object{ID: aPaidLamp}, 3, "")
		return tx, err
	})
	q := waitSent[*msg.RequestObjectPropertiesFamily](t, f)
	if q.ObjectData.ObjectID != aPaidLamp {
		t.Fatalf("asked about %v", q.ObjectData.ObjectID)
	}
	fam := &msg.ObjectPropertiesFamily{}
	fam.ObjectData.ObjectID = aPaidLamp
	fam.ObjectData.OwnerID = examplePayee
	fam.ObjectData.Name = []byte("a lamp\x00")
	fam.ObjectData.Description = []byte("\x00")
	f.Relay(t, fam)

	m := waitSent[*msg.MoneyTransferRequest](t, f)
	if d := m.MoneyData; d.DestID != aPaidLamp || d.TransactionType != 5008 || d.Flags != 0 ||
		string(d.Description) != "a lamp\x00" || d.Amount != 3 {
		t.Errorf("sent %+v", d)
	}
	// Which of the two the grid names in its answer has not been
	// measured; the owner is taken as well as the object.
	f.Relay(t, moneyReplyOf(payTx, true, 97, "You paid Example Resident L$3.", 5008, testAgentID, examplePayee, 3, "a lamp"))
	if tx, err := wait(); err != nil || tx != payTx {
		t.Errorf("tx %v, %v", tx, err)
	}
}

func TestTheMoneySubscriptionEndsWithTheSession(t *testing.T) {
	w, f := newFakeSession(t)
	ch := w.Money(0)
	f.Close()
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("something was delivered")
		}
	case <-time.After(5 * time.Second):
		t.Error("the subscription was left open")
	}
}

// A session held here has no daemon to check what it pays, so its own
// backend does, against the rules it logged in with.
func TestADirectSessionChecksAPaymentAgainstItsRules(t *testing.T) {
	d := aDirectSession(t)
	rw := &recordingWriter{}
	d.a.Send = msg.NewSender(rw)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.a.Send.Run(ctx)

	m := &msg.MoneyTransferRequest{}
	m.MoneyData.SourceID = testAgentID
	m.MoneyData.DestID = examplePayee
	m.MoneyData.Amount = 5
	m.MoneyData.TransactionType = pay.TransGift
	m.MoneyData.Description = []byte("\x00")

	var r *PayRefused
	if err := d.Send(ctx, m, true); !errors.As(err, &r) || r.By != "this profile's rules" {
		t.Fatalf("a direct session with no rules: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(rw.datagrams()); n != 0 {
		t.Fatalf("%d datagrams went out", n)
	}

	d.pay = pay.NewGate(pay.Rules{On: true, To: []string{examplePayee.String()}}, "")
	if err := d.Send(ctx, m, true); err != nil {
		t.Fatalf("a payment the rules allow: %v", err)
	}
	waitFor(t, "the payment to go out", func() bool { return len(rw.datagrams()) == 1 })
}

// A purchase is checked the same way, and so is a message sent as it is
// framed.  Nothing goes out for one refused.
func TestADirectSessionChecksAPurchaseAgainstItsRules(t *testing.T) {
	d := aDirectSession(t)
	rw := &recordingWriter{}
	d.a.Send = msg.NewSender(rw)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.a.Send.Run(ctx)

	buy := &msg.ObjectBuy{}
	buy.ObjectData = []msg.ObjectBuy_ObjectData{{ObjectLocalID: 9, SaleType: 2, SalePrice: 5}}
	raw := func() msg.Message {
		body, err := buy.Encode()
		if err != nil {
			t.Fatal(err)
		}
		return msg.NewRaw(msg.IDOf(buy), body)
	}

	for name, m := range map[string]msg.Message{"typed": buy, "framed": raw()} {
		var r *PayRefused
		if err := d.Send(ctx, m, true); !errors.As(err, &r) || r.By != "this profile's rules" ||
			!strings.Contains(r.Reason, "paying is off") {
			t.Fatalf("a %s purchase under no rules: %v", name, err)
		}
	}

	// Paying allowed, and the price not known: refused all the same.
	d.pay = pay.NewGate(pay.Rules{On: true, To: []string{"*"}}, "")
	var r *PayRefused
	if err := d.Send(ctx, buy, true); !errors.As(err, &r) || !strings.Contains(r.Reason, "not one this session has been told of") {
		t.Fatalf("a purchase of an object nobody described: %v", err)
	}
	// Answering an invitation by capability is one too.
	d.pay = nil
	body := []byte("<llsd><map><key>group</key><uuid>" + examplePayee.String() + "</uuid></map></llsd>")
	if _, err := d.DoCap(ctx, agent.CapRequest{Cap: "AcceptGroupInvite", Method: "POST", Body: body}); !errors.As(err, &r) ||
		!strings.Contains(r.Reason, "paying is off") {
		t.Fatalf("accepting by capability under no rules: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(rw.datagrams()); n != 0 {
		t.Fatalf("%d datagrams went out", n)
	}

	// What spends nothing goes as it did.
	say := &msg.ChatFromViewer{}
	say.ChatData.Message = []byte("hi\x00")
	if err := d.Send(ctx, say, true); err != nil {
		t.Fatalf("a chat: %v", err)
	}
	waitFor(t, "the chat to go out", func() bool { return len(rw.datagrams()) == 1 })
}
