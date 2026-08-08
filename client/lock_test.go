package client

// Asking a daemon that is not there for exclusive use of something.
//
// A lock lives on the stream rather than in an rpc of its own, which is
// the whole point of it: what makes a lock worth having is that dying
// gives it back, and the stream is what slgod already watches for that.
// So none of this can be tested without a stream, and the fake daemon
// in client_test.go answers lock requests from a function a test
// supplies -- granted, refused with a holder, or not answered at all,
// which is a daemon too old to know what a lock is.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// grants makes the daemon hand over everything asked for.
func grants(l *pb.Lock) *pb.Locked { return &pb.Locked{Name: l.Name, Held: true} }

// TestALockWaitedForComesBackHeld: waiting is the ordinary case, and it
// is answered only when the lock has been given -- so an answer at all
// means it is ours.
func TestALockWaitedForComesBackHeld(t *testing.T) {
	t.Parallel()
	d, conn := dialFake(t)
	d.locked = grants
	if _, err := conn.Attach(context.Background(), "quark"); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	if err := conn.Lock(context.Background(), "the workbench"); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if err := conn.Unlock("the workbench"); err != nil {
		t.Errorf("Unlock: %v", err)
	}
}

// TestTryLockSaysWhoHasItRatherThanWaiting: somebody else holding it is
// an answer rather than a failure, and it is what lets a caller move on
// to the next of a pool instead of queueing on the first.
func TestTryLockSaysWhoHasItRatherThanWaiting(t *testing.T) {
	t.Parallel()
	d, conn := dialFake(t)
	d.locked = func(l *pb.Lock) *pb.Locked {
		if l.Try && l.Name == "taken" {
			return &pb.Locked{Name: l.Name, Held: false, Holder: "somebody else"}
		}
		return grants(l)
	}
	if _, err := conn.Attach(context.Background(), "quark"); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	got, by, err := conn.TryLock(context.Background(), "the workbench")
	if err != nil || !got || by != "" {
		t.Errorf("TryLock = %v, %q, %v", got, by, err)
	}
	if got, by, err = conn.TryLock(context.Background(), "taken"); err != nil || got {
		t.Errorf("TryLock took a lock somebody else holds: %v, %v", got, err)
	}
	if by != "somebody else" {
		t.Errorf("TryLock said it was held by %q", by)
	}
}

// TestAWaitAnsweredWithoutTheLockIsNotSilentlyTaken: only TryLock is
// entitled to come back empty handed.  A wait that did would leave the
// caller believing it holds something it does not, and two benchmarks
// would quietly share one object.
func TestAWaitAnsweredWithoutTheLockIsNotSilentlyTaken(t *testing.T) {
	t.Parallel()
	d, conn := dialFake(t)
	d.locked = func(l *pb.Lock) *pb.Locked { return &pb.Locked{Name: l.Name, Held: false} }
	if _, err := conn.Attach(context.Background(), "quark"); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	err := conn.Lock(context.Background(), "the workbench")
	if err == nil || !strings.Contains(err.Error(), "the workbench") {
		t.Errorf("Lock = %v, want a complaint naming the lock", err)
	}
}

// TestALockNeedsANameAndAStream: both are the caller's mistake rather
// than the daemon's, so neither reaches the wire -- and an unnamed lock
// would otherwise be a lock on "", which every such caller would share.
func TestALockNeedsANameAndAStream(t *testing.T) {
	t.Parallel()
	_, conn := dialFake(t)

	if _, _, err := conn.TryLock(context.Background(), ""); err == nil {
		t.Error("a lock with no name was accepted")
	}
	if err := conn.Lock(context.Background(), ""); err == nil {
		t.Error("a lock with no name was accepted")
	}
	// Not attached: there is no stream to ask on, and no rpc to fall
	// back to, because the lock is the stream's.
	if _, _, err := conn.TryLock(context.Background(), "the workbench"); err == nil {
		t.Error("TryLock asked for a lock with nothing attached")
	}
	if err := conn.Unlock("the workbench"); err == nil {
		t.Error("Unlock gave back a lock with nothing attached")
	}
}

// TestGivingUpOnAWaitTellsTheDaemon: a caller that walks away silently
// is handed the lock later by a daemon that has no idea nobody is
// listening, and the lock is then held by a program that has forgotten
// it asked.
func TestGivingUpOnAWaitTellsTheDaemon(t *testing.T) {
	t.Parallel()
	d, conn := dialFake(t)
	// No answer at all: the daemon has taken the request and is
	// queueing it, which is what waiting looks like from here.
	if _, err := conn.Attach(context.Background(), "quark"); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		waitForLock(t, d)
		cancel()
	}()
	err := conn.Lock(ctx, "the workbench")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Lock = %v, want the cancellation", err)
	}

	// And the unlock that says so went out, so the daemon can drop the
	// queued request rather than granting it to nobody.
	for {
		select {
		case p := <-d.sent:
			if u := p.GetUnlock(); u != nil {
				if u.Name != "the workbench" {
					t.Errorf("gave up on %q", u.Name)
				}
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("giving up on the wait was never mentioned to the daemon")
		}
	}
}

// TestAWaitEndsWhenTheDaemonDoes: a lock nobody will ever answer must
// not outlive the connection it was asked on, or a program waiting its
// turn waits for a daemon that has gone -- and it would wait for as long
// as it was told to, which for a benchmark queueing on an object is a
// quarter of an hour.
func TestAWaitEndsWhenTheDaemonDoes(t *testing.T) {
	t.Parallel()
	d, conn := dialFake(t)
	d.hangUpOnLock = true
	if _, err := conn.Attach(context.Background(), "quark"); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// The ask gets through and the daemon then goes away, so the wait
	// ends with whatever ended the stream rather than with nothing.
	err := conn.Lock(context.Background(), "the workbench")
	if err == nil {
		t.Error("Lock came back holding a lock from a daemon that had gone")
	}
	select {
	case <-conn.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the receive loop never noticed the stream end")
	}
}

// TestAskingForALockOnAStreamThatHasGoneIsReported: the ask is the only
// part of a lock that can fail outright, and it has to be told apart
// from waiting -- one is a connection to reopen, the other is patience.
func TestAskingForALockOnAStreamThatHasGoneIsReported(t *testing.T) {
	t.Parallel()
	_, conn := attachFake(t)
	conn.Close()

	if _, _, err := conn.TryLock(context.Background(), "the workbench"); err == nil {
		t.Error("TryLock asked for a lock on a stream that had been closed")
	}
	if err := conn.Unlock("the workbench"); err == nil {
		t.Error("Unlock gave a lock back on a stream that had been closed")
	}
}

// TestAnAnswerNobodyIsWaitingForIsDropped: the daemon answers a name,
// not a request, so a late answer to a wait already given up on has
// nowhere to go -- and blocking on the delivery would stop the receive
// loop, which is every other message on the connection.
func TestAnAnswerNobodyIsWaitingForIsDropped(t *testing.T) {
	t.Parallel()
	d, conn := attachFake(t)

	d.relay <- &pb.ServerPacket{Body: &pb.ServerPacket_Locked{
		Locked: &pb.Locked{Name: "nobody asked", Held: true},
	}}
	// The connection is still working afterwards, which is the point:
	// the answer was dropped rather than left blocking the loop.
	d.relay <- &pb.ServerPacket{Body: &pb.ServerPacket_Notice{
		Notice: &pb.AgentEvent{Kind: pb.AgentEvent_REGION_CHANGED},
	}}
	select {
	case <-conn.Notices():
	case <-time.After(5 * time.Second):
		t.Fatal("the receive loop stopped on an answer nobody wanted")
	}
}

// TestTwoWaitsOnOneNameAreAnsweredOldestFirst: the answer names the
// lock and nothing else, so which of several waiters it belongs to is
// decided here -- and taking the newest would starve whoever has been
// waiting longest.
func TestTwoWaitsOnOneNameAreAnsweredOldestFirst(t *testing.T) {
	t.Parallel()
	var l locking

	first := make(chan *pb.Locked, 1)
	second := make(chan *pb.Locked, 1)
	l.await("the workbench", first)
	l.await("the workbench", second)

	l.deliver(&pb.Locked{Name: "the workbench", Held: true, Holder: "the first"})
	select {
	case got := <-first:
		if got.Holder != "the first" {
			t.Errorf("the first waiter got %+v", got)
		}
	default:
		t.Fatal("the answer did not reach the waiter that asked first")
	}
	select {
	case got := <-second:
		t.Errorf("the second waiter was given %+v as well", got)
	default:
	}

	// Giving up on a wait that has already been answered finds nothing
	// to remove, and must leave the queue alone rather than dropping
	// whoever is now at the front of it.
	l.stopAwaiting("the workbench", first)
	l.deliver(&pb.Locked{Name: "the workbench", Held: true, Holder: "the second"})
	select {
	case got := <-second:
		if got.Holder != "the second" {
			t.Errorf("the second waiter got %+v", got)
		}
	default:
		t.Fatal("the second waiter was dropped by the first one giving up")
	}

	// An answer for a name nobody is waiting on is not an error, since
	// the wait may have been given up on a moment earlier.
	l.deliver(&pb.Locked{Name: "nobody asked"})
}

// waitForLock reads the next lock request off the stream, so that a
// test can act once the daemon has been asked rather than racing it.
func waitForLock(t *testing.T, d *fakeDaemon) *pb.Lock {
	t.Helper()
	for {
		select {
		case p := <-d.sent:
			if l := p.GetLock(); l != nil {
				return l
			}
		case <-time.After(5 * time.Second):
			t.Error("the daemon was never asked for the lock")
			return nil
		}
	}
}
