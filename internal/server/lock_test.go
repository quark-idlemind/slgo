package server

import (
	"context"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
)

// TestLockIsExclusive: one client at a time, and the one that cannot
// have it is told who has.
func TestLockIsExclusive(t *testing.T) {
	r := newRig(t, agent.Caps{})
	a, b := r.dial(t), r.dial(t)
	defer a.Close()
	defer b.Close()

	ctx := context.Background()
	if err := a.Lock(ctx, "auto"); err != nil {
		t.Fatalf("the first client should get it: %v", err)
	}

	held, holder, err := b.TryLock(ctx, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if held {
		t.Error("two clients hold one lock")
	}
	if holder == "" {
		t.Error("a refused lock should say who has it")
	}

	// A different name is a different lock.
	if held, _, err := b.TryLock(ctx, "something else"); err != nil || !held {
		t.Errorf("another name should be free: held=%v err=%v", held, err)
	}
}

// TestLockWaitsItsTurn: waiting is the default, and the wait ends when
// the holder gives it back.
func TestLockWaitsItsTurn(t *testing.T) {
	r := newRig(t, agent.Caps{})
	a, b := r.dial(t), r.dial(t)
	defer a.Close()
	defer b.Close()

	ctx := context.Background()
	if err := a.Lock(ctx, "auto"); err != nil {
		t.Fatal(err)
	}

	got := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		got <- b.Lock(ctx, "auto")
	}()

	// It must NOT be granted while the first client holds it.
	select {
	case err := <-got:
		t.Fatalf("the second client got the lock while the first held it (err %v)", err)
	case <-time.After(300 * time.Millisecond):
	}

	if err := a.Unlock("auto"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-got:
		if err != nil {
			t.Errorf("waiting for the lock: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("the lock was given back but nobody was handed it")
	}
}

// TestLockGoesWithTheClient is the point of hanging the lease on the
// stream: a client that dies without giving the lock back must not keep
// it.  Nothing here says "I am still alive"; the connection ending is
// the whole mechanism.
func TestLockGoesWithTheClient(t *testing.T) {
	r := newRig(t, agent.Caps{})
	a, b := r.dial(t), r.dial(t)
	defer a.Close()
	defer b.Close()

	ctx := context.Background()
	if err := a.Lock(ctx, "auto"); err != nil {
		t.Fatal(err)
	}
	if held, _, _ := b.TryLock(ctx, "auto"); held {
		t.Fatal("the lock was not held to begin with")
	}

	// a goes away without unlocking, as a crash would.
	a.Close()

	deadline := time.Now().Add(5 * time.Second)
	for {
		held, _, err := b.TryLock(ctx, "auto")
		if err != nil {
			t.Fatal(err)
		}
		if held {
			return // freed, which is what should happen
		}
		if time.Now().After(deadline) {
			t.Fatal("the lock outlived the client that held it")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestLockIsGivenUpWhenTheWaiterLeaves: a client that stops waiting
// must not be handed the lock, or it goes to somebody who is not
// listening and everyone behind them waits forever.
func TestLockIsGivenUpWhenTheWaiterLeaves(t *testing.T) {
	r := newRig(t, agent.Caps{})
	a, b, c := r.dial(t), r.dial(t), r.dial(t)
	defer a.Close()
	defer b.Close()
	defer c.Close()

	if err := a.Lock(context.Background(), "auto"); err != nil {
		t.Fatal(err)
	}

	// b queues, then gives up.
	gaveUp := make(chan struct{})
	go func() {
		defer close(gaveUp)
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		b.Lock(ctx, "auto")
	}()
	<-gaveUp

	// c queues and should be the one that gets it.
	got := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		got <- c.Lock(ctx, "auto")
	}()
	time.Sleep(200 * time.Millisecond)

	if err := a.Unlock("auto"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-got:
		if err != nil {
			t.Errorf("the lock should have gone to the client still waiting: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("the lock went to the client that had given up")
	}
}

// TestALockAskedForAsItsStreamEndsIsNotHeld: a request can be handled
// just after the end of its stream gave back every lock the client
// held.  Taken then, the lock would be held by nobody, for good.  The
// order is made here by asking for a client whose stream has already
// ended, which is what the late request sees.
func TestALockAskedForAsItsStreamEndsIsNotHeld(t *testing.T) {
	t.Parallel()
	l := newLocks()
	gone := &Client{}
	gone.closed.Store(true)
	l.releaseAll(gone)

	if ok, _ := l.acquire("the workbench", gone); ok {
		t.Error("a client whose stream had ended was given a lock")
	}
	select {
	case <-l.queue("the bench", gone):
		t.Error("a client whose stream had ended was handed a free lock by waiting")
	default:
	}

	// Behind a holder that then lets go.
	holder := &Client{}
	if ok, _ := l.acquire("the shelf", holder); !ok {
		t.Fatal("a free lock was refused")
	}
	l.queue("the shelf", gone)
	l.giveUp("the shelf", holder)

	next := &Client{}
	for _, name := range []string{"the workbench", "the bench", "the shelf"} {
		if ok, by := l.acquire(name, next); !ok {
			t.Errorf("%s is held (by the client that had gone: %v)", name, by == gone)
		}
	}
}
