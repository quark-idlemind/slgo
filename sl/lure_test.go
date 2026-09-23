package sl

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// offered puts a teleport offer in front of the session, the way one
// arrives: as an instant message of dialog 22 carrying a lure id.
func offered(w *Session) *Lure {
	w.noteLure(&IM{
		At:       time.Now(),
		From:     msg.MustParseUUID("74347e57-7e57-c0de-66d4-4c6b2d882438"),
		FromName: "Jorr Starlit",
		Text:     "Join me in Sandbox Goguen!",
		Dialog:   22,
		ID:       msg.MustParseUUID("0d817e57-7e57-c0de-7df5-71b55084a53f"),
	}, "", false)
	return w.Lures()[0]
}

// accepted runs AcceptLure on its own goroutine and gives up long before
// the timeout it would otherwise wait out, so that a call which hangs is
// a failure in seconds rather than a test that takes a minute and a
// half.
func accepted(t *testing.T, w *Session, l *Lure) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- w.AcceptLure(context.Background(), l) }()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("AcceptLure is still waiting for something the grid never sends")
		return nil
	}
}

// TestALureInsideTheRegionDoesNotWaitForAFinishThatNeverComes: somebody
// standing nearby is the ordinary offer, and the grid answers one of
// those with a TeleportLocal on the circuit and no finish at all.  A
// wait for the finish would turn the common case into a minute and a
// half of nothing.
func TestALureInsideTheRegionDoesNotWaitForAFinishThatNeverComes(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	l := offered(w)

	f.mu.Lock()
	f.presence.RegionHandle = 1099511628032
	f.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.TeleportLureRequest); ok {
			f.Relay(t, &msg.TeleportLocal{})
		}
	}
	f.mu.Unlock()

	if err := accepted(t, w, l); err != nil {
		t.Fatalf("AcceptLure: %v", err)
	}
	if m := onlySent[*msg.TeleportLureRequest](t, f); m.Info.LureID != l.ID {
		t.Errorf("the request answered lure %s, not the offer", m.Info.LureID)
	}
}

// TestALureToAnotherRegionIsFollowed: an offer names no region -- the
// destination is in the TeleportFinish and nowhere else -- so this waits
// for whichever region turns up rather than for one it asked about.
func TestALureToAnotherRegionIsFollowed(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	l := offered(w)

	f.mu.Lock()
	f.presence.RegionHandle = 1099511628032
	f.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.TeleportLureRequest); !ok {
			return
		}
		f.RelayEvent(t, "TeleportFinish", agniFinish)
		f.mu.Lock()
		f.presence.RegionHandle = goguen
		f.presence.Region = "Sandbox Goguen"
		f.mu.Unlock()
	}
	f.mu.Unlock()

	if err := accepted(t, w, l); err != nil {
		t.Fatalf("AcceptLure: %v", err)
	}
}

// TestAnAcceptedLureIsSpentWhetherOrNotItWorked: the id answers one
// offer once.  It is forgotten as soon as the request has gone rather
// than when the teleport succeeds, because keeping it through the wait
// leaves it there to be accepted a second time -- which sends the person
// who offered a second answer to a dialog they have stopped watching.
func TestAnAcceptedLureIsSpentWhetherOrNotItWorked(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	l := offered(w)

	f.mu.Lock()
	f.presence.RegionHandle = 1099511628032
	f.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.TeleportLureRequest); ok {
			f.RelayEvent(t, "TeleportFailed", agniRefused)
		}
	}
	f.mu.Unlock()

	if err := accepted(t, w, l); !errors.Is(err, ErrTeleportRefused) {
		t.Fatalf("AcceptLure = %v, want the grid's refusal", err)
	}
	if got := w.Lures(); len(got) != 0 {
		t.Errorf("%d offers still waiting after one was accepted", len(got))
	}
}

// TestALureThatCouldNotBeSentIsStillWaiting: nothing was answered and
// nothing was spent, so the offer is still there to accept or decline.
func TestALureThatCouldNotBeSentIsStillWaiting(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	l := offered(w)
	f.FailSends(errors.New("the circuit is gone"))

	if err := accepted(t, w, l); err == nil {
		t.Fatal("AcceptLure answered for a request that never went out")
	}
	if got := w.Lures(); len(got) != 1 {
		t.Errorf("%d offers waiting, want the one that was never sent", len(got))
	}
}
