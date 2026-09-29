package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

var (
	moneyTx     = msg.MustParseUUID("c14e7e57-7e57-c0de-824e-a88b9b0cd5f8")
	somebody    = msg.MustParseUUID("e8a87e57-7e57-c0de-b1c0-5e4362d0213d")
	nearlyThem  = msg.MustParseUUID("e8ea7e57-7e57-c0de-833b-8cdf97963357")
	aPaidObject = msg.MustParseUUID("448e7e57-7e57-c0de-41f1-5383eb8af019")
)

// balanceReply is a MoneyBalanceReply as the grid sends one.
func balanceReply(tx msg.UUID, ok bool, balance int, said string, typ int, from, to msg.UUID, amount int, echo string) *msg.MoneyBalanceReply {
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

func TestTheBalanceIsKeptAndEveryReplyHandedOn(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	var mu sync.Mutex
	var heard []*Money
	a.opts.OnMoney = func(m *Money) {
		mu.Lock()
		heard = append(heard, m)
		mu.Unlock()
	}

	if _, known := a.Balance(); known {
		t.Error("a balance is known before the grid has said one")
	}

	me := a.Account.AgentID
	feed(t, a, balanceReply(msg.UUID{}, true, 100, "", 0, msg.UUID{}, msg.UUID{}, -1, ""))
	if b, known := a.Balance(); !known || b != 100 {
		t.Errorf("after a plain balance: %d, %v", b, known)
	}
	feed(t, a, balanceReply(moneyTx, true, 99, "You paid Example Resident L$1.", 5001, me, somebody, 1, "for the lamp"))
	if b, _ := a.Balance(); b != 99 {
		t.Errorf("after paying L$1: %d", b)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(heard) != 2 {
		t.Fatalf("handed on %d replies, want 2", len(heard))
	}
	if heard[0].Transfer() {
		t.Error("a plain balance was taken for a payment")
	}
	p := heard[1]
	if !p.Transfer() || p.Transaction != moneyTx || !p.Success || p.Type != 5001 ||
		p.Source != me || p.Dest != somebody || p.Amount != 1 ||
		p.Description != "You paid Example Resident L$1." || p.ItemDescription != "for the lamp" {
		t.Errorf("read the payment as %+v", p)
	}
}

// answerPicker waits for the search to go out and answers it with rows.
func answerPicker(t *testing.T, a *Agent, w *sentPackets, rows ...msg.AvatarPickerReply_Data) *msg.AvatarPickerRequest {
	t.Helper()
	var q *msg.AvatarPickerRequest
	deadline := time.Now().Add(2 * time.Second)
	for q == nil {
		for _, m := range w.messages(t) {
			if r, ok := m.(*msg.AvatarPickerRequest); ok {
				q = r
			}
		}
		if q == nil {
			if time.Now().After(deadline) {
				t.Fatal("no AvatarPickerRequest went out")
			}
			time.Sleep(time.Millisecond)
		}
	}
	r := &msg.AvatarPickerReply{}
	r.AgentData.AgentID = a.Account.AgentID
	r.AgentData.QueryID = q.AgentData.QueryID
	r.Data = rows
	feed(t, a, r)
	return q
}

func row(id msg.UUID, first, last string) msg.AvatarPickerReply_Data {
	return msg.AvatarPickerReply_Data{AvatarID: id, FirstName: append([]byte(first), 0), LastName: append([]byte(last), 0)}
}

func TestFindingAnAvatarTakesOnlyTheWholeName(t *testing.T) {
	t.Parallel()
	a, w := offlineSession(t)

	got := make(chan msg.UUID, 1)
	go func() {
		id, err := a.FindAvatar(context.Background(), "example  resident")
		if err != nil {
			t.Error(err)
		}
		got <- id
	}()
	q := answerPicker(t, a, w, row(nearlyThem, "Example", "Residents"), row(somebody, "Example", "Resident"))
	if name := trimNul(q.Data.Name); name != "example resident" {
		t.Errorf("searched for %q", name)
	}
	if id := <-got; id != somebody {
		t.Errorf("found %v, want %v", id, somebody)
	}
}

func TestFindingNobodyIsTheZeroID(t *testing.T) {
	t.Parallel()
	a, w := offlineSession(t)

	got := make(chan msg.UUID, 1)
	go func() {
		id, err := a.FindAvatar(context.Background(), "Nobody Resident")
		if err != nil {
			t.Error(err)
		}
		got <- id
	}()
	// The one row a search that matched nothing is answered with.
	answerPicker(t, a, w, row(msg.UUID{}, "", ""))
	if id := <-got; !id.IsZero() {
		t.Errorf("found %v", id)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := a.FindAvatar(ctx, "Example Resident"); err == nil {
		t.Error("a search nobody answered found somebody")
	}
}

func TestAnObjectsOwnerIsKnownOnlyOnceTheRegionHasSaid(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	if _, ok := a.ObjectOwner(aPaidObject); ok {
		t.Error("an object the region never described has an owner")
	}
	o := a.Objects()
	o.mu.Lock()
	o.seen(aPaidObject)
	o.mu.Unlock()
	if _, ok := a.ObjectOwner(aPaidObject); ok {
		t.Error("an object nobody has asked about has an owner")
	}
	f := &msg.ObjectPropertiesFamily{}
	f.ObjectData.ObjectID = aPaidObject
	f.ObjectData.OwnerID = somebody
	f.ObjectData.Name = []byte("a lamp\x00")
	f.ObjectData.Description = []byte("\x00")
	feed(t, a, f)
	if owner, ok := a.ObjectOwner(aPaidObject); !ok || owner != somebody {
		t.Errorf("owner %v, %v", owner, ok)
	}
}
