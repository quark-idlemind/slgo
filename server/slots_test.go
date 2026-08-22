package server

// Handing out the shared objects, over the wire that hands them out.
//
// Every one of these is about two clients, because what a pool decides
// is invisible from one: a daemon that gives the same place to both
// looks, from either of them, exactly like one that works.  What that
// costs on a grid is two scripts in one object, which is not a failure
// but a pair of plausible answers.

import (
	"context"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/client"
)

// TestSlotsAreExclusive: the whole of it.  Twelve places on one avatar,
// and what one client has the other cannot.
func TestSlotsAreExclusive(t *testing.T) {
	r := newRig(t, agent.Caps{})
	a, b := r.dial(t), r.dial(t)
	defer a.Close()
	defer b.Close()

	ctx := context.Background()
	first, err := a.Slots(ctx, SlotsPerAgent, time.Minute, "")
	if err != nil {
		t.Fatalf("Slots: %v", err)
	}
	if !first.Held() {
		t.Fatalf("a whole avatar's places would not go to one client: %s", first.Why)
	}
	if len(first.Places) != SlotsPerAgent {
		t.Errorf("got %d places, want %d", len(first.Places), SlotsPerAgent)
	}

	second, err := b.TrySlots(ctx, 1, time.Minute, "")
	if err != nil {
		t.Fatalf("TrySlots: %v", err)
	}
	if second.Held() {
		t.Errorf("a place was given to two clients at once: %v", second.Places)
	}
	if second.Why == "" {
		t.Error("a request that got nothing did not say why")
	}
}

// TestAllOfThemOrNoneOverTheWire: a client given four of the eight it
// asked for could only hold them while waiting for the rest, which is
// how two clients deadlock against each other.  So it is given none --
// and what it did not take is still there for somebody who can use it.
func TestAllOfThemOrNoneOverTheWire(t *testing.T) {
	r := newRig(t, agent.Caps{})
	a, b := r.dial(t), r.dial(t)
	defer a.Close()
	defer b.Close()

	ctx := context.Background()
	held, err := a.Slots(ctx, SlotsPerAgent-3, time.Minute, "")
	if err != nil || !held.Held() {
		t.Fatalf("Slots: %v %v", held, err)
	}

	short, err := b.TrySlots(ctx, 8, time.Minute, "")
	if err != nil {
		t.Fatal(err)
	}
	if short.Held() {
		t.Fatalf("eight places came out of the three that were left: %v", short.Places)
	}
	if len(short.Places) != 0 {
		t.Errorf("a request that was refused came back holding %d places", len(short.Places))
	}

	fits, err := b.TrySlots(ctx, 3, time.Minute, "")
	if err != nil {
		t.Fatal(err)
	}
	if !fits.Held() {
		t.Errorf("the three that were left went nowhere: %s", fits.Why)
	}
}

// TestSlotsWaitTheirTurn: waiting is the default, and the wait ends when
// the holder gives them back -- without the waiter holding anything
// meanwhile, which is what makes waiting safe.
func TestSlotsWaitTheirTurn(t *testing.T) {
	r := newRig(t, agent.Caps{})
	a, b := r.dial(t), r.dial(t)
	defer a.Close()
	defer b.Close()

	ctx := context.Background()
	held, err := a.Slots(ctx, SlotsPerAgent, time.Minute, "")
	if err != nil || !held.Held() {
		t.Fatalf("Slots: %v %v", held, err)
	}

	got := make(chan *client.Grant, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		g, err := b.Slots(ctx, 4, time.Minute, "")
		if err != nil {
			t.Errorf("the waiting client: %v", err)
		}
		got <- g
	}()

	select {
	case g := <-got:
		t.Fatalf("a client was given places somebody else holds: %v", g.Places)
	case <-time.After(200 * time.Millisecond):
	}

	if err := a.ReleaseSlots(held.ID, true); err != nil {
		t.Fatalf("ReleaseSlots: %v", err)
	}
	select {
	case g := <-got:
		if !g.Held() || len(g.Places) != 4 {
			t.Errorf("the wait ended with %v (%s)", g.Places, g.Why)
		}
	case <-time.After(10 * time.Second):
		t.Error("giving the places back did not end the wait")
	}
}

// TestAStreamThatEndsGivesItsPlacesBack: the lease is the stream, which
// is the reason any of this lives on the stream rather than in an RPC.
// A client that exited, crashed or was unplugged said nothing about it,
// and there is nothing to clean up by hand.
func TestAStreamThatEndsGivesItsPlacesBack(t *testing.T) {
	r := newRig(t, agent.Caps{})
	a, b := r.dial(t), r.dial(t)
	defer b.Close()

	ctx := context.Background()
	if g, err := a.Slots(ctx, SlotsPerAgent, time.Minute, ""); err != nil || !g.Held() {
		t.Fatalf("Slots: %v %v", g, err)
	}
	a.Close()

	deadline := time.Now().Add(10 * time.Second)
	for {
		g, err := b.TrySlots(ctx, SlotsPerAgent, time.Minute, "")
		if err != nil {
			t.Fatalf("TrySlots: %v", err)
		}
		if g.Held() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("a stream that ended did not give its places back: %s", g.Why)
		}
	}
}

// TestPlacesComeBackDirtyUnlessTheHolderSaysOtherwise: a script left
// running in an object is a line the next holder reads as its own, so
// the next holder has to be told.  Saying nothing -- which includes
// every stream that just ended -- means it was not left fit to use.
func TestPlacesComeBackDirtyUnlessTheHolderSaysOtherwise(t *testing.T) {
	r := newRig(t, agent.Caps{})
	a := r.dial(t)
	defer a.Close()

	ctx := context.Background()

	// The first time out they are dirty, because whatever is in them is
	// not this daemon's doing.
	first, err := a.Slots(ctx, 1, time.Minute, "")
	if err != nil || !first.Held() {
		t.Fatalf("Slots: %v %v", first, err)
	}
	if !first.Places[0].Dirty {
		t.Error("a place nobody has ever cleared came out clean")
	}

	// Given back as cleared, it comes out clean.
	if err := a.ReleaseSlots(first.ID, true); err != nil {
		t.Fatal(err)
	}
	second, err := a.Slots(ctx, 1, time.Minute, "")
	if err != nil || !second.Held() {
		t.Fatalf("Slots: %v %v", second, err)
	}
	if second.Places[0].Dirty {
		t.Error("a place given back as cleared came out dirty")
	}

	// Given back without saying, it is dirty again.
	if err := a.ReleaseSlots(second.ID, false); err != nil {
		t.Fatal(err)
	}
	third, err := a.Slots(ctx, 1, time.Minute, "")
	if err != nil || !third.Held() {
		t.Fatalf("Slots: %v %v", third, err)
	}
	if !third.Places[0].Dirty {
		t.Error("a place given back without a word came out clean")
	}
}

// TestAGrantIsOnlyItsOwnClientsToGiveBack: a client that could release
// somebody else's grant could take their objects out from under them,
// which is the same collision by another road.
func TestAGrantIsOnlyItsOwnClientsToGiveBack(t *testing.T) {
	r := newRig(t, agent.Caps{})
	a, b := r.dial(t), r.dial(t)
	defer a.Close()
	defer b.Close()

	ctx := context.Background()
	held, err := a.Slots(ctx, SlotsPerAgent, time.Minute, "")
	if err != nil || !held.Held() {
		t.Fatalf("Slots: %v %v", held, err)
	}

	// b tries to give back what a holds, and to put its clock back.
	if err := b.ReleaseSlots(held.ID, true); err != nil {
		t.Fatalf("ReleaseSlots: %v", err)
	}
	if g, err := b.RenewSlots(ctx, held.ID, time.Hour); err != nil {
		t.Fatalf("RenewSlots: %v", err)
	} else if g.Held() {
		t.Error("a client renewed somebody else's grant")
	}

	// Which changed nothing: they are still a's.
	got, err := b.TrySlots(ctx, 1, time.Minute, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Held() {
		t.Error("one client gave back another's places")
	}
}

// TestRenewingSaysTheNewDeadline: work that cannot say in advance how
// long it will take renews rather than guessing high, and guessing high
// is what makes a lease useless as a way of getting objects back.
func TestRenewingSaysTheNewDeadline(t *testing.T) {
	r := newRig(t, agent.Caps{})
	a := r.dial(t)
	defer a.Close()

	ctx := context.Background()
	held, err := a.Slots(ctx, 1, time.Minute, "")
	if err != nil || !held.Held() {
		t.Fatalf("Slots: %v %v", held, err)
	}

	got, err := a.RenewSlots(ctx, held.ID, time.Hour)
	if err != nil {
		t.Fatalf("RenewSlots: %v", err)
	}
	if !got.Held() {
		t.Fatalf("a grant that exists would not renew: %s", got.Why)
	}
	if !got.Expires.After(held.Expires) {
		t.Errorf("renewed to %v, which is no later than %v", got.Expires, held.Expires)
	}
	if got.ID != held.ID {
		t.Errorf("renewing changed the grant from %q to %q", held.ID, got.ID)
	}
}

// TestMoreThanTheDaemonHasIsRefusedRatherThanWaitedFor: waiting for
// objects that do not exist is waiting for ever, and a client that asked
// for more than there is deserves to be told rather than hung.
func TestMoreThanTheDaemonHasIsRefusedRatherThanWaitedFor(t *testing.T) {
	r := newRig(t, agent.Caps{})
	a := r.dial(t)
	defer a.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	g, err := a.Slots(ctx, SlotsPerAgent*10, time.Minute, "")
	if err != nil {
		t.Fatalf("Slots: %v", err)
	}
	if g.Held() {
		t.Errorf("the daemon granted %d places it does not have", len(g.Places))
	}
	if g.Why == "" {
		t.Error("asking for more than there is did not say why it could not be done")
	}
}

// TestAskingForNothing: a request for no places is a caller's mistake,
// and a grant of nothing is a grant that holds nothing and cannot be
// given back.
func TestAskingForNothingIsRefused(t *testing.T) {
	r := newRig(t, agent.Caps{})
	a := r.dial(t)
	defer a.Close()

	if _, err := a.Slots(context.Background(), 0, time.Minute, ""); err == nil {
		t.Error("a request for no places was sent rather than refused")
	}
}
