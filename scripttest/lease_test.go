package scripttest_test

// What a lease has to do, and what goes wrong when it does not.

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/quark-idlemind/slgo/proto/scriptv1"
	"github.com/quark-idlemind/slgo/scripttest"
)

// TestALeaseSaysWhichAvatarAndGroupItLandedOn covers the thing a caller
// that did not name an avatar is supposed to say out loud: a reading is
// only comparable with another from the same avatar, so a Granted that
// did not name one would leave a benchmark unable to label its own
// numbers.
func TestALeaseSaysWhichAvatarAndGroupItLandedOn(t *testing.T) {
	bothWays(t, func(t *testing.T, r reach) {
		_, c := serve(t, r, scripttest.Options{Agents: []string{"qi", "example"}, Groups: 2})

		g, done := lease(t, c, &scriptv1.LeaseRequest{Targets: 3, Who: "slbench string-concat"})
		defer done()

		if g.GetAgent() != "qi" {
			t.Errorf("granted on %q, want the first avatar with a free group", g.GetAgent())
		}
		if len(g.GetTargets()) != 3 {
			t.Fatalf("granted %d targets, want the 3 that were asked for", len(g.GetTargets()))
		}
		for _, x := range g.GetTargets() {
			if x.GetId() == "" || x.GetName() == "" {
				t.Errorf("target %v has no id or no name; a caller passes the id back and shows the name to a person", x)
			}
		}

		// And the pool says who has it, which is the whole reason Who is on
		// the request: a person wondering what is holding the objects is
		// better served by a benchmark's name than by a client address.
		p, err := c.Pool(context.Background(), &scriptv1.PoolRequest{})
		if err != nil {
			t.Fatalf("Pool: %v", err)
		}
		var by string
		for _, a := range p.GetAgents() {
			for _, grp := range a.GetGroups() {
				if grp.GetHeldBy() != "" {
					by = grp.GetHeldBy()
				}
			}
		}
		if by != "slbench string-concat" {
			t.Errorf("the pool says the group is held by %q, want the name the caller gave", by)
		}
	})
}

// TestTheLeaseIsGivenBackWhenTheStreamEnds is the ordinary case: a
// caller finishes and stops asking.  If it did not hold, a pool would
// empty over a day of benchmarks and the only cure would be restarting
// the backend.
func TestTheLeaseIsGivenBackWhenTheStreamEnds(t *testing.T) {
	bothWays(t, func(t *testing.T, r reach) {
		_, c := serve(t, r, scripttest.Options{})

		_, done := lease(t, c, &scriptv1.LeaseRequest{Who: "first"})
		if held(t, c) != 1 {
			t.Fatalf("%d groups held, want the one just leased", held(t, c))
		}
		done()
		eventually(t, "the lease coming back", func() bool { return held(t, c) == 0 })
	})
}

// TestTheLeaseIsGivenBackWhenTheCallerGoesAwayWithoutSayingAnything is
// why the lease is a stream at all.
//
// A caller that dies mid-benchmark says nothing -- no Release, no
// cancellation, nothing on the wire but a connection that stops being
// there.  The objects have to come back anyway, because the alternatives
// are both wrong: a lease timer short enough to catch a crash loses the
// objects of a long run, and one long enough for a long run blocks the
// pool after a crash.
//
// On the wire only, and this is the test that says why there is a wire.
// Dying means losing a connection, and the in-process client has none to
// lose: every way a Direct caller can stop is a way it CHOSE, which is
// the easy half of the problem.
func TestTheLeaseIsGivenBackWhenTheCallerGoesAwayWithoutSayingAnything(t *testing.T) {
	s, watcher := serve(t, overAPipe, scripttest.Options{})

	// A connection of its own, so that closing it is the caller dying
	// and not the test tearing everything down.
	conn, err := s.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	doomed := scriptv1.NewRunnerClient(conn)

	stream, err := doomed.Lease(context.Background(), &scriptv1.LeaseRequest{Who: "about to die"})
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("Lease: %v", err)
	}
	if held(t, watcher) != 1 {
		t.Fatal("the lease was not granted")
	}

	conn.Close()
	eventually(t, "the lease coming back after the caller died", func() bool { return held(t, watcher) == 0 })
}

// TestTwoCallersQueueRatherThanShareAGroup is the one that a benchmark
// depends on for its numbers to mean anything: a second caller in the
// same object reads somebody else's base reading and divides against it,
// which is a wrong answer rather than a failure.
func TestTwoCallersQueueRatherThanShareAGroup(t *testing.T) {
	bothWays(t, func(t *testing.T, r reach) {
		_, c := serve(t, r, scripttest.Options{})

		first, done := lease(t, c, &scriptv1.LeaseRequest{Who: "first"})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		second, err := c.Lease(ctx, &scriptv1.LeaseRequest{Who: "second"})
		if err != nil {
			t.Fatalf("Lease: %v", err)
		}
		ev, err := second.Recv()
		if err != nil {
			t.Fatalf("Lease: %v", err)
		}
		if ev.GetQueued() == nil {
			t.Fatalf("the second caller was sent %T with the only group busy, want a Queued", ev.GetEvent())
		}

		// And the pool says somebody is waiting, which is the answer to "why
		// is my benchmark not starting".
		p, err := c.Pool(context.Background(), &scriptv1.PoolRequest{})
		if err != nil {
			t.Fatalf("Pool: %v", err)
		}
		if p.GetWaiting() != 1 {
			t.Errorf("the pool says %d waiting, want the one that is", p.GetWaiting())
		}

		done()

		ev, err = second.Recv()
		if err != nil {
			t.Fatalf("waiting for a turn: %v", err)
		}
		g := ev.GetGranted()
		if g == nil {
			t.Fatalf("after the first lease ended the second was sent %T, want a Granted", ev.GetEvent())
		}
		// The same objects, under different ids: the id is minted per grant,
		// so what the first caller was holding names nothing now.
		if g.GetTargets()[0].GetName() != first.GetTargets()[0].GetName() {
			t.Errorf("the queue handed over %q, want the group that came free (%q)",
				g.GetTargets()[0].GetName(), first.GetTargets()[0].GetName())
		}
		if g.GetTargets()[0].GetId() == first.GetTargets()[0].GetId() {
			t.Error("the second caller was given the first caller's target id; an id from a lease that ended must name nothing")
		}
	})
}

// TestANamedAvatarIsWaitedForRatherThanSubstituted holds a caller to
// what it asked for.  Being given another avatar's objects would be
// worse than being slow: readings from two avatars are not comparable,
// and nothing downstream would show that they had been mixed.
func TestANamedAvatarIsWaitedForRatherThanSubstituted(t *testing.T) {
	bothWays(t, func(t *testing.T, r reach) {
		_, c := serve(t, r, scripttest.Options{Agents: []string{"qi", "example"}})

		_, done := lease(t, c, &scriptv1.LeaseRequest{Agent: "qi", Who: "first"})
		defer done()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		stream, err := c.Lease(ctx, &scriptv1.LeaseRequest{Agent: "qi", Who: "second"})
		if err != nil {
			t.Fatalf("Lease: %v", err)
		}
		ev, err := stream.Recv()
		if err != nil {
			t.Fatalf("Lease: %v", err)
		}
		if g := ev.GetGranted(); g != nil {
			t.Fatalf("a caller that named qi was given %s; naming one is honoured exactly, including the waiting",
				g.GetAgent())
		}

		// While a caller that does not mind takes the other avatar at once.
		other, done2 := lease(t, c, &scriptv1.LeaseRequest{Who: "anywhere"})
		defer done2()
		if other.GetAgent() != "example" {
			t.Errorf("a caller that named nobody landed on %q, want the avatar with a free group", other.GetAgent())
		}
	})
}

// TestAskingForMoreObjectsThanAGroupHoldsFailsRatherThanWaits: a group
// is the unit of exclusion, so a request bigger than one can never be
// met.  Queueing for it would be a wait that never ends, and a caller
// with a bad number would look like a caller with bad luck.
func TestAskingForMoreObjectsThanAGroupHoldsFailsRatherThanWaits(t *testing.T) {
	bothWays(t, func(t *testing.T, r reach) {
		_, c := serve(t, r, scripttest.Options{GroupSize: 4})

		stream, err := c.Lease(context.Background(), &scriptv1.LeaseRequest{Targets: 5})
		if err != nil {
			t.Fatalf("Lease: %v", err)
		}
		_, err = stream.Recv()
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("asking for 5 of a group of 4 = %v, want InvalidArgument", err)
		}
	})
}

// TestABoundedWaitGivesUpInsteadOfQueueingForever is the caller that
// would rather fail than be slow -- a script run from a Makefile, say.
// Zero waits as long as the stream lives, so a bound has to be a bound.
func TestABoundedWaitGivesUpInsteadOfQueueingForever(t *testing.T) {
	bothWays(t, func(t *testing.T, r reach) {
		_, c := serve(t, r, scripttest.Options{})

		_, done := lease(t, c, &scriptv1.LeaseRequest{Who: "first"})
		defer done()

		start := time.Now()
		stream, err := c.Lease(context.Background(), &scriptv1.LeaseRequest{WaitSeconds: 1, Who: "impatient"})
		if err != nil {
			t.Fatalf("Lease: %v", err)
		}
		ev, err := stream.Recv()
		if err != nil {
			t.Fatalf("Lease: %v", err)
		}
		if ev.GetQueued() == nil {
			t.Fatalf("first event is %T, want a Queued", ev.GetEvent())
		}
		if _, err = stream.Recv(); status.Code(err) != codes.DeadlineExceeded {
			t.Errorf("a bounded wait ended with %v, want DeadlineExceeded", err)
		}
		if took := time.Since(start); took < testSecond {
			t.Errorf("the wait was over in %v, want at least the second it asked for (%v)", took, testSecond)
		}
		// And giving up leaves nothing behind: a waiter that stayed in the
		// queue would be handed the group when it came free, and the group
		// would then be held by nobody.
		eventually(t, "the queue emptying", func() bool {
			p, err := c.Pool(context.Background(), &scriptv1.PoolRequest{})
			return err == nil && p.GetWaiting() == 0
		})
	})
}
