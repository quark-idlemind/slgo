package session

// Choosing where to run, when there are several avatars and several
// groups of objects on each.
//
// Two halves, and they need different fakes.  useAutoOn and wearGroup
// are decisions about ONE session and are driven through the fake
// backend in session_test.go.  UseAutoAnywhere is the other half -- it
// dials once per avatar it considers, and closes the sessions it does
// not use -- so it needs a daemon holding several, which is the gRPC
// server on loopback that session_test.go builds.
//
// What is worth holding on to here is the ORDER of things: a queue is
// the last resort, a named avatar is honoured exactly including its
// wait, and a group is taken whole or not at all.  None of that is
// visible in a single run; it only shows up as two benchmarks quietly
// sharing an object, which is a wrong number rather than a failure.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/sl"
)

// allBusy makes every group taken by somebody else, which is one avatar
// with nothing to spare.
func allBusy(f *fakeGrid, by string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for g := 0; g < AutoGroups(); g++ {
		f.busy[AutoGroupLock(g)] = by
	}
}

// TestReleasingTwiceGivesBackOneLock: Release is called from a defer and
// often from the caller as well, and a second Unlock would give back a
// lock that by then belongs to whoever was queued behind us.
func TestReleasingTwiceGivesBackOneLock(t *testing.T) {
	t.Parallel()
	n := 0
	a := &Auto{release: func() { n++ }}
	a.Release()
	a.Release()
	if n != 1 {
		t.Errorf("the objects were given back %d times", n)
	}

	// One that never took anything has nothing to give back and must
	// not say so by panicking.
	(&Auto{}).Release()
}

// TestAFreeGroupIsTakenWithoutQueueing: with a pool the wait should be
// the LAST resort -- the old arrangement was one lock over one set of
// objects, and a second benchmark waited for the first even when objects
// were going spare.
func TestAFreeGroupIsTakenWithoutQueueing(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.stock(len(AutoPoints))
	f.mu.Lock()
	f.busy[AutoGroupLock(0)] = "somebody else"
	f.mu.Unlock()

	a, err := useAutoOn(context.Background(), s, AutoGroupSize, true)
	if err != nil {
		t.Fatalf("useAutoOn: %v", err)
	}
	if a.Group != 1 {
		t.Errorf("took group %d, want the first one that was free", a.Group)
	}
	if len(a.Objects) != AutoGroupSize {
		t.Errorf("took %d objects, want a whole group", len(a.Objects))
	}
	if a.Agent != "quark" {
		t.Errorf("the objects belong to %q", a.Agent)
	}
	if a.Session != s {
		t.Error("the session that came back is not the one the objects are on")
	}
	if got := f.Waited(); len(got) != 0 {
		t.Errorf("queued on %v with a group going spare", got)
	}

	a.Release()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.busy[AutoGroupLock(1)] != "" {
		t.Error("releasing did not give the group back")
	}
}

// TestEveryGroupBusyIsAWaitOrARefusal: waiting is right when this is the
// avatar that was asked for and wrong when there are others to try, so
// the choice is the caller's -- and a wait forms on group 0 because a
// queue has to form somewhere.
func TestEveryGroupBusyIsAWaitOrARefusal(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.stock(len(AutoPoints))
	allBusy(f, "somebody else")

	_, err := useAutoOn(context.Background(), s, 1, false)
	if err == nil {
		t.Fatal("useAutoOn took a group when every one of them was busy")
	}
	if !strings.Contains(err.Error(), "quark") {
		t.Errorf("useAutoOn = %v, want it to name the avatar", err)
	}

	a, err := useAutoOn(context.Background(), s, 1, true)
	if err != nil {
		t.Fatalf("useAutoOn: %v", err)
	}
	if a.Group != 0 {
		t.Errorf("queued on group %d, want the one a queue forms on", a.Group)
	}
	if got := f.Waited(); len(got) != 1 || got[0] != AutoGroupLock(0) {
		t.Errorf("waited on %v", got)
	}
}

// TestADaemonTooOldToLockIsNotWorkedAround: the lock is the whole of
// what makes several runs at once safe, so one that cannot be asked for
// is not something to carry on without -- and the message says what to
// do instead, because an operator with an old daemon can still run.
func TestADaemonTooOldToLockIsNotWorkedAround(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.mu.Lock()
	f.lockErr = fmt.Errorf("unknown method Lock")
	f.mu.Unlock()

	_, err := useAutoOn(context.Background(), s, 1, true)
	if err == nil {
		t.Fatal("useAutoOn used objects it could not lock")
	}
	if !strings.Contains(err.Error(), "--rez") {
		t.Errorf("useAutoOn = %v, want it to say what avoids the lock", err)
	}
}

// TestAGroupTakenAndThenUnusableIsGivenBack: holding a lock over
// objects that could not be worn would leave the group unusable to
// everybody, including the next run of this same program.
func TestAGroupTakenAndThenUnusableIsGivenBack(t *testing.T) {
	t.Parallel()

	t.Run("a group taken without waiting", func(t *testing.T) {
		t.Parallel()
		s, f := newFakeSession(t)
		f.mu.Lock()
		f.capErr = fmt.Errorf("the capability is not answering")
		f.mu.Unlock()

		if _, err := useAutoOn(context.Background(), s, 1, false); err == nil {
			t.Fatal("useAutoOn used objects it could not find")
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.busy[AutoGroupLock(0)] != "" {
			t.Error("the group is still held after the objects could not be worn")
		}
	})

	t.Run("a group waited for", func(t *testing.T) {
		t.Parallel()
		s, f := newFakeSession(t)
		allBusy(f, "somebody else")
		f.mu.Lock()
		f.capErr = fmt.Errorf("the capability is not answering")
		f.mu.Unlock()

		if _, err := useAutoOn(context.Background(), s, 1, true); err == nil {
			t.Fatal("useAutoOn used objects it could not find")
		}
		if got := f.Waited(); len(got) != 1 {
			t.Errorf("waited on %v", got)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.busy[AutoGroupLock(0)] != "" {
			t.Error("the group is still held after the objects could not be worn")
		}
	})
}

// TestAWaitThatEndsWithoutTheLockIsReported: the queue is the last
// resort, so it ending badly is the end of the road -- the daemon went
// away, or the fifteen minutes ran out, and either way there is nowhere
// else to look.
func TestAWaitThatEndsWithoutTheLockIsReported(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	allBusy(f, "somebody else")
	f.mu.Lock()
	f.waitErr = fmt.Errorf("the daemon went away")
	f.mu.Unlock()

	_, err := useAutoOn(context.Background(), s, 1, true)
	if err == nil {
		t.Fatal("useAutoOn came back holding a lock it never got")
	}
	if !strings.Contains(err.Error(), "quark") {
		t.Errorf("useAutoOn = %v, want it to name the avatar waited on", err)
	}
}

// TestWearingAGroupMakesTheItemsFirst: the items have to exist before
// any of them can be worn, and the highest slot in the group is how far
// that has to reach -- a group at the end of the pool needs every item
// below it to exist as well, because they are all copies of the first.
func TestWearingAGroupMakesTheItemsFirst(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.stock(1)
	answerCopies(f)

	a, err := wearGroup(context.Background(), s, 1, AutoGroupSize)
	if err != nil {
		t.Fatalf("wearGroup: %v", err)
	}
	if len(a.Objects) != AutoGroupSize {
		t.Errorf("wore %d objects", len(a.Objects))
	}
	items, err := s.FolderItems(context.Background(), testObjects)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2*AutoGroupSize {
		t.Errorf("%d items exist, want everything up to the top of the group", len(items))
	}
}

// TestAGroupPastTheEndOfThePoolHasNoSlots: the arithmetic is the only
// thing standing between a group number and an index into AutoPoints,
// and a group that is not there has to say so rather than wear nothing
// and report success.
func TestAGroupPastTheEndOfThePoolHasNoSlots(t *testing.T) {
	t.Parallel()
	s, _ := newFakeSession(t)

	_, err := wearGroup(context.Background(), s, AutoGroups()+1, AutoGroupSize)
	if err == nil || !strings.Contains(err.Error(), "no slots") {
		t.Errorf("wearGroup = %v, want it to say the group is not there", err)
	}
}

// TestWearingStopsAtTheFirstObjectAndCarriesOnAfterIt: no objects at all
// is no benchmark; some of them is a slower one, and the caller already
// copes with getting fewer than it asked for.
func TestWearingStopsAtTheFirstObjectAndCarriesOnAfterIt(t *testing.T) {
	t.Parallel()

	t.Run("nothing to wear", func(t *testing.T) {
		t.Parallel()
		s, f := newFakeSession(t)
		f.stock(4)
		f.mu.Lock()
		f.objectsErr = fmt.Errorf("the circuit went away")
		f.sendErr = fmt.Errorf("the circuit is gone")
		f.mu.Unlock()

		if _, err := wearGroup(context.Background(), s, 0, AutoGroupSize); err == nil {
			t.Error("wearGroup reported success without its first object")
		}
	})

	t.Run("some of them", func(t *testing.T) {
		t.Parallel()
		s, f := newFakeSession(t)
		// Two objects exist and are on; nothing can be copied to make
		// the rest, and nothing can be built either.
		f.stock(2)
		f.mu.Lock()
		f.sendErr = fmt.Errorf("the circuit is gone")
		f.presenceErr = fmt.Errorf("the parcel will not have it")
		f.mu.Unlock()

		a, err := wearGroup(context.Background(), s, 0, AutoGroupSize)
		if err != nil {
			t.Fatalf("wearGroup: %v", err)
		}
		if len(a.Objects) != 2 {
			t.Errorf("wore %d objects, want the two that exist", len(a.Objects))
		}
	})

	t.Run("nowhere to look", func(t *testing.T) {
		t.Parallel()
		s, f := newFakeSession(t)
		f.mu.Lock()
		f.capErr = fmt.Errorf("the capability is not answering")
		f.mu.Unlock()

		if _, err := wearGroup(context.Background(), s, 0, 1); err == nil {
			t.Error("wearGroup found objects in an inventory it could not read")
		}
	})

	t.Run("nothing to make them from", func(t *testing.T) {
		t.Parallel()
		s, f := newFakeSession(t)
		f.mu.Lock()
		f.presenceErr = fmt.Errorf("the parcel will not have it")
		f.mu.Unlock()

		if _, err := wearGroup(context.Background(), s, 0, AutoGroupSize); err == nil {
			t.Error("wearGroup wore objects that were never made")
		}
	})
}

// TestOnlyAHostedSessionKnowsWhoElseThereIs: a direct session is the
// only session there is, and mistaking it for a daemon holding one
// avatar would have this looking for others that cannot exist.
func TestOnlyAHostedSessionKnowsWhoElseThereIs(t *testing.T) {
	t.Parallel()
	s, _ := newFakeSession(t)
	if _, err := sessionNames(context.Background(), s); err == nil {
		t.Error("a direct session was asked who else the daemon holds")
	}

	hosted, err := sl.New(&listsSessions{
		fakeGrid: newFakeGrid(t),
		names:    []string{"quark", "example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hosted.Close() })
	got, err := sessionNames(context.Background(), hosted)
	if err != nil || len(got) != 2 {
		t.Errorf("sessionNames = %v, %v", got, err)
	}

	refuses, err := sl.New(&listsSessions{
		fakeGrid: newFakeGrid(t),
		err:      fmt.Errorf("the daemon is going down"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { refuses.Close() })
	if _, err := sessionNames(context.Background(), refuses); err == nil {
		t.Error("sessionNames answered from a daemon that refused")
	}
}

// ------------------------------------------------- choosing an avatar

// TestABenchmarkCannotAskForMoreThanOneGroup: more than a group would
// have to be two locks, and nothing here ever holds one while waiting
// for another -- which is the whole reason this cannot deadlock.  So it
// is refused rather than quietly rounded down.
func TestABenchmarkCannotAskForMoreThanOneGroup(t *testing.T) {
	t.Parallel()
	_, err := UseAutoAnywhere(context.Background(), Options{}, AutoGroupSize+1)
	if err == nil || !strings.Contains(err.Error(), "deadlock") {
		t.Errorf("UseAutoAnywhere = %v, want it to say why the limit is there", err)
	}
}

// TestANamedAvatarIsHonouredExactly: asking for qi and being given
// example would be worse than being slow, so a named avatar is used and
// waited for rather than fallen over from.
func TestANamedAvatarIsHonouredExactly(t *testing.T) {
	d, addr := newFakeDaemon(t)
	d.agents = []string{"quark", "example"}
	d.busy("quark")

	a, err := UseAutoAnywhere(context.Background(), Options{Addr: addr, Agent: "quark"}, 1)
	if err != nil {
		t.Fatalf("UseAutoAnywhere: %v", err)
	}
	defer a.Session.Close()
	if a.Agent != "quark" {
		t.Errorf("asked for quark and was given %q", a.Agent)
	}
	if len(a.Objects) != 1 {
		t.Errorf("took %d objects", len(a.Objects))
	}
	a.Release()
}

// TestAnAvatarThatCannotBeReachedIsReported: with one named there is
// nothing to fall over to, so a daemon that is not there is the answer
// rather than something to work around.
func TestAnAvatarThatCannotBeReachedIsReported(t *testing.T) {
	newFakeDaemon(t)
	if _, err := UseAutoAnywhere(context.Background(),
		Options{Addr: "127.0.0.1:1", Agent: "quark"}, 1); err == nil {
		t.Error("UseAutoAnywhere reached a daemon on a port nothing is listening on")
	}
}

// TestASessionOpenedAndThenUnusableIsClosed: the session it returns
// belongs to the caller, and one it opened and could not use belongs to
// nobody -- leaving it open holds a stream on the daemon for as long as
// the program runs.
func TestASessionOpenedAndThenUnusableIsClosed(t *testing.T) {
	d, addr := newFakeDaemon(t)
	d.capFail = true

	if _, err := UseAutoAnywhere(context.Background(),
		Options{Addr: addr, Agent: "quark"}, 1); err == nil {
		t.Error("UseAutoAnywhere used objects it could not find")
	}
}

// TestOneAvatarIsUsedWithoutBeingChosenBetween: a daemon holding one
// session, or one too old to say what it holds, has nothing to choose
// between -- so the connection already open is the one used.
func TestOneAvatarIsUsedWithoutBeingChosenBetween(t *testing.T) {
	d, addr := newFakeDaemon(t)
	d.agents = []string{"quark"}

	a, err := UseAutoAnywhere(context.Background(), Options{Addr: addr}, AutoGroupSize)
	if err != nil {
		t.Fatalf("UseAutoAnywhere: %v", err)
	}
	defer a.Session.Close()
	if a.Agent != "quark" || len(a.Objects) != AutoGroupSize {
		t.Errorf("ran on %q with %d objects", a.Agent, len(a.Objects))
	}
}

// TestTheOnlyAvatarFailingIsTheEndOfIt: with one session there is
// nothing to fall over to, so the session opened for it is closed and
// the failure is passed on rather than turned into a search.
func TestTheOnlyAvatarFailingIsTheEndOfIt(t *testing.T) {
	d, addr := newFakeDaemon(t)
	d.agents = []string{"quark"}
	d.capFail = true

	if _, err := UseAutoAnywhere(context.Background(), Options{Addr: addr}, 1); err == nil {
		t.Error("UseAutoAnywhere used objects it could not find")
	}
}

// TestTheDefaultAvatarIsTriedWithoutReconnecting: the daemon has already
// attached us to one, and dialling it again to find out whether it is
// free would be a second connection to the same session.
func TestTheDefaultAvatarIsTriedWithoutReconnecting(t *testing.T) {
	d, addr := newFakeDaemon(t)
	d.agents = []string{"quark", "example"}

	a, err := UseAutoAnywhere(context.Background(), Options{Addr: addr}, 1)
	if err != nil {
		t.Fatalf("UseAutoAnywhere: %v", err)
	}
	defer a.Session.Close()
	if a.Agent != "quark" {
		t.Errorf("ran on %q, want the daemon's own first choice", a.Agent)
	}
}

// TestABusyDefaultMovesOnToTheNextAvatar: this is what the pool is for.
// The old arrangement queued on the first avatar however many others
// were idle, and the wait is meant to be the last resort.
func TestABusyDefaultMovesOnToTheNextAvatar(t *testing.T) {
	d, addr := newFakeDaemon(t)
	d.agents = []string{"quark", "example"}
	d.busy("quark")

	a, err := UseAutoAnywhere(context.Background(), Options{Addr: addr}, 1)
	if err != nil {
		t.Fatalf("UseAutoAnywhere: %v", err)
	}
	defer a.Session.Close()
	if a.Agent != "example" {
		t.Errorf("ran on %q, want the avatar that was free", a.Agent)
	}
}

// TestAnAvatarThatCannotBeReachedIsSkippedRatherThanFatal: one of
// several being unreachable is a reason to try the next, not a reason to
// stop -- the daemon may be holding a session that has just gone down.
func TestAnAvatarThatCannotBeReachedIsSkippedRatherThanFatal(t *testing.T) {
	d, addr := newFakeDaemon(t)
	d.agents = []string{"quark", "gone", "example"}
	d.refuseAttach["gone"] = true
	d.busy("quark")

	a, err := UseAutoAnywhere(context.Background(), Options{Addr: addr}, 1)
	if err != nil {
		t.Fatalf("UseAutoAnywhere: %v", err)
	}
	defer a.Session.Close()
	if a.Agent != "example" {
		t.Errorf("ran on %q, want the avatar past the one that was gone", a.Agent)
	}
}

// TestEveryAvatarIsTriedBeforeAnyIsWaitedFor: with everything busy the
// queue forms on the DEFAULT rather than on whichever avatar happened to
// be asked last, so that waiting is predictable from one run to the next.
func TestEveryAvatarIsTriedBeforeAnyIsWaitedFor(t *testing.T) {
	d, addr := newFakeDaemon(t)
	d.agents = []string{"quark", "example"}
	d.busy("quark", "example")

	a, err := UseAutoAnywhere(context.Background(), Options{Addr: addr}, 1)
	if err != nil {
		t.Fatalf("UseAutoAnywhere: %v", err)
	}
	defer a.Session.Close()
	if a.Agent != "quark" {
		t.Errorf("queued on %q, want the default", a.Agent)
	}
}

// TestAWaitOnTheDefaultThatFailsIsReported: the last resort failing is
// the end of the road -- there is nowhere else to look, and the session
// opened for it is closed on the way out.
func TestAWaitOnTheDefaultThatFailsIsReported(t *testing.T) {
	d, addr := newFakeDaemon(t)
	d.agents = []string{"quark", "example"}
	d.busy("quark", "example")
	d.capFail = true

	if _, err := UseAutoAnywhere(context.Background(), Options{Addr: addr}, 1); err == nil {
		t.Error("UseAutoAnywhere used objects it could not find")
	}
}

// TestAskingForNoObjectsAsksForOne: the number comes off a command line
// and nought objects is not a thing a benchmark can run on, so it is
// rounded up rather than refused.
func TestAskingForNoObjectsAsksForOne(t *testing.T) {
	d, addr := newFakeDaemon(t)
	d.agents = []string{"quark"}

	a, err := UseAutoAnywhere(context.Background(), Options{Addr: addr}, 0)
	if err != nil {
		t.Fatalf("UseAutoAnywhere: %v", err)
	}
	defer a.Session.Close()
	if len(a.Objects) != 1 {
		t.Errorf("asking for none took %d objects", len(a.Objects))
	}
}

// TestUseAutoAnywhereNeedsADaemonAtAll: with nobody named there is still
// a first connection to make, and it failing is the end of it.
func TestUseAutoAnywhereNeedsADaemonAtAll(t *testing.T) {
	newFakeDaemon(t)
	if _, err := UseAutoAnywhere(context.Background(), Options{Addr: "127.0.0.1:1"}, 1); err == nil {
		t.Error("UseAutoAnywhere ran somewhere with no daemon to run it on")
	}
}
