package main

// Offers slgod kept from before this daemon attached.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// keptGrid is the fake grid with a daemon's record of offers behind it.
type keptGrid struct {
	*fakeGrid

	record *sl.OfferRecord
	kept   []*sl.Message

	mu     sync.Mutex
	asked  []string
	answer func(key string) (bool, *sl.Handled)
}

var _ sl.OfferKeeper = (*keptGrid)(nil)

func (k *keptGrid) KeptOffers() (*sl.OfferRecord, []*sl.Message, bool) {
	return k.record, k.kept, k.record != nil
}

func (k *keptGrid) Handled(_ context.Context, key, how string) (bool, *sl.Handled, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.asked = append(k.asked, key+" "+how)
	if k.answer == nil {
		return true, nil, nil
	}
	claimed, earlier := k.answer(key)
	return claimed, earlier, nil
}

func (k *keptGrid) Unhandled(context.Context, string) error { return nil }
func (k *keptGrid) HandledOffers() <-chan *sl.Handled       { return nil }

func (k *keptGrid) Asked() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.asked...)
}

// keptOffer is an item offer as slgod hands it over at attach.
func keptOffer(t *testing.T, key string, from msg.UUID, name string, transaction msg.UUID) *sl.Message {
	t.Helper()
	m := offering(from, name, "a notecard", transaction)
	body, err := msg.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return &sl.Message{ID: msg.IDOf(m), Name: "ImprovedInstantMessage", Body: body,
		At: time.Now().Add(-time.Hour), Offer: key, Recorded: true}
}

// keptDaemon is newTestDaemon over a daemon that had kept these.
func keptDaemon(t *testing.T, kept ...*sl.Message) (*bot, *keptGrid) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Path = "(test)"
	cfg.Avatars = []string{"example"}
	cfg.trustedIDs[testSender] = true
	d := newDaemon(cfg, "127.0.0.1:0", func(string, ...any) {})
	b := d.bots["example"]

	k := &keptGrid{fakeGrid: newFakeGrid(), record: &sl.OfferRecord{Since: time.Now().Add(-2 * time.Hour)},
		kept: kept}
	s, err := sl.New(k)
	if err != nil {
		t.Fatalf("sl.New: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	b.setSession(s)
	b.setState(stateAttached, s.Info().AvatarName)
	return b, k
}

// TestAGiftMadeWhileTheDaemonWasAwayIsTaken: a trusted avatar hands over
// a notecard while this daemon is restarting.  slgod kept it, and it is
// never delivered as an arriving message -- it is history, not news -- so
// the rule that takes a trusted avatar's gifts has to be applied to what
// was kept as well, or the gift waits for ever for somebody at a
// keyboard.  A stranger's is left waiting, as it would be live.
func TestAGiftMadeWhileTheDaemonWasAwayIsTaken(t *testing.T) {
	trusted := msg.MustParseUUID("a3bd7e57-7e57-c0de-3638-772c3272c5e7")
	stranger := msg.MustParseUUID("a4297e57-7e57-c0de-713e-13935049227a")
	b, k := keptDaemon(t,
		keptOffer(t, "inventory:1", testSender, "Trusted Resident", trusted),
		keptOffer(t, "inventory:2", testStranger, "Some Body", stranger),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); b.serve(ctx, b.Session()) }()
	defer func() { cancel(); <-done }()

	ims := waitIMs(t, k.fakeGrid, 1)
	quiet(k.fakeGrid)
	if len(ims) != 1 || ims[0].MessageBlock.Dialog != sl.DialogInventoryAccepted ||
		ims[0].MessageBlock.ID != trusted {
		t.Fatalf("sent %d answers, want one acceptance of the trusted gift", len(k.IMsSent()))
	}
	if got := k.Asked(); len(got) != 1 || got[0] != "inventory:1 accepted" {
		t.Errorf("slgod was told %q", got)
	}
	if n := len(b.Session().InventoryOffers()); n != 1 {
		t.Errorf("%d offers waiting, want the stranger's", n)
	}
}

// TestAGiftAnotherClientTookIsNotTakenAgain: a person in slsh accepted
// the gift a moment before this daemon got to it.  slgod says so, and
// nothing goes: a second acceptance of one transaction is an answer to
// something already answered.
func TestAGiftAnotherClientTookIsNotTakenAgain(t *testing.T) {
	trusted := msg.MustParseUUID("a46d7e57-7e57-c0de-a415-0d0d8a16a97d")
	b, k := keptDaemon(t, keptOffer(t, "inventory:3", testSender, "Trusted Resident", trusted))
	k.answer = func(key string) (bool, *sl.Handled) {
		return false, &sl.Handled{Key: key, How: "accepted", By: "slsh", At: time.Now()}
	}

	b.takeKept(context.Background(), b.Session())
	if ims := k.IMsSent(); len(ims) != 0 {
		t.Errorf("%d answers went out for a gift another client had taken", len(ims))
	}
	if n := len(b.Session().InventoryOffers()); n != 0 {
		t.Errorf("%d offers still waiting after slgod said it was taken", n)
	}
}
