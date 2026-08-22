package session

// Choosing where to run, when there are several avatars and several
// groups of objects on each.
//
// Two halves, and they need different fakes.  takeSlots and wearSlots
// are decisions about ONE session and are driven through the fake
// backend in session_test.go.  UseAutoAnywhere is the other half -- it
// dials once per avatar it considers, and closes the sessions it does
// not use -- so it needs a daemon holding several, which is the gRPC
// server on loopback that session_test.go builds.
//
// What is worth holding on to here is the ORDER of things: a queue is
// the last resort, a named avatar is honoured exactly including its
// wait, and objects are taken all together or not at all.  None of that
// is visible in a single run; it only shows up as two benchmarks quietly
// sharing an object, which is a wrong number rather than a failure, or
// as two callers waiting on each other for ever.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/sl"
)

// fourSlots is a benchmark's worth of the pool, for the tests that are
// about wearing objects rather than about which places they came from.
var fourSlots = []int{0, 1, 2, 3}

// allBusy makes every place taken by somebody else, which is one avatar
// with nothing to spare.
func allBusy(f *fakeGrid, by string) {
	busy(f, by, 0, AutoPool())
}

// busy makes the places from lo up to hi somebody else's.
func busy(f *fakeGrid, by string, lo, hi int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := lo; i < hi; i++ {
		f.busy[AutoSlotLock(i)] = by
	}
}

// snapshot is every lock the fake says is held, by name.
func snapshot(f *fakeGrid) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for name, by := range f.busy {
		if by != "" {
			out[name] = by
		}
	}
	return out
}

// atWait runs fn and answers with what was held the first time the
// caller waited for a place.  The allocation lock is queued on every
// time and is not a wait for a place, so it does not count as one.
func atWait(f *fakeGrid, fn func()) map[string]string {
	var during map[string]string
	f.mu.Lock()
	f.onLock = func(name string) {
		if name == AutoAllocLock || during != nil {
			return
		}
		during = snapshot(f)
	}
	f.mu.Unlock()
	fn()
	return during
}

// held is which places the fake says are taken, and by whom.
func held(f *fakeGrid) map[int]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[int]string{}
	for i := 0; i < AutoPool(); i++ {
		if by := f.busy[AutoSlotLock(i)]; by != "" {
			out[i] = by
		}
	}
	return out
}

// slotsWaitedFor is the places that were queued on rather than tried.
// The allocation lock is always queued on -- that is what it is for --
// and is not what these tests are about.
func slotsWaitedFor(f *fakeGrid) []string {
	var out []string
	for _, name := range f.Waited() {
		if name != AutoAllocLock {
			out = append(out, name)
		}
	}
	return out
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

// TestFreeObjectsAreTakenWithoutQueueing: with a pool the wait should be
// the LAST resort -- the old arrangement was one lock over one set of
// objects, and a second benchmark waited for the first even when objects
// were going spare.
func TestFreeObjectsAreTakenWithoutQueueing(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.stock(len(AutoPoints))
	busy(f, "somebody else", 0, AutoGroupSize)

	a, err := useAutoOn(context.Background(), s, AutoGroupSize, true)
	if err != nil {
		t.Fatalf("useAutoOn: %v", err)
	}
	if len(a.Objects) != AutoGroupSize {
		t.Errorf("took %d objects, want the %d asked for", len(a.Objects), AutoGroupSize)
	}
	for _, slot := range a.Slots {
		if slot < AutoGroupSize {
			t.Errorf("took place %d, which somebody else has", slot)
		}
	}
	if a.Agent != "quark" {
		t.Errorf("the objects belong to %q", a.Agent)
	}
	if a.Session != s {
		t.Error("the session that came back is not the one the objects are on")
	}
	if got := slotsWaitedFor(f); len(got) != 0 {
		t.Errorf("queued on %v with objects going spare", got)
	}

	a.Release()
	for slot, by := range held(f) {
		if by == "us" {
			t.Errorf("releasing did not give place %d back", slot)
		}
	}
}

// TestObjectsAreTakenOneAtATimeAndAcrossWhateverIsFree: the pool hands
// out a COUNT and not a block.  Six objects out of a pool whose first
// five are busy is six of the seven that are left, which the old fixed
// groups of four could not express at all.
func TestObjectsAreTakenOneAtATimeAndAcrossWhateverIsFree(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.stock(len(AutoPoints))
	busy(f, "somebody else", 0, 5)

	a, err := useAutoOn(context.Background(), s, 6, true)
	if err != nil {
		t.Fatalf("useAutoOn: %v", err)
	}
	if len(a.Objects) != 6 {
		t.Errorf("took %d objects, want the 6 asked for", len(a.Objects))
	}
	if got := slotsWaitedFor(f); len(got) != 0 {
		t.Errorf("queued on %v with six objects going spare", got)
	}
	for _, slot := range a.Slots {
		if slot < 5 {
			t.Errorf("took place %d, which somebody else has", slot)
		}
	}
}

// TestEverythingBusyIsAWaitOrARefusal: waiting is right when this is the
// avatar that was asked for and wrong when there are others to try, so
// the choice is the caller's -- and the wait forms on one of the places
// somebody else has, because that is what "something was given back"
// looks like with only locks to say it.
func TestEverythingBusyIsAWaitOrARefusal(t *testing.T) {
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
	if len(a.Objects) != 1 {
		t.Errorf("took %d objects after waiting for one", len(a.Objects))
	}
	if got := slotsWaitedFor(f); len(got) != 1 || got[0] != AutoSlotLock(0) {
		t.Errorf("waited on %v, want one of the places somebody else had", got)
	}
}

// TestNothingIsHeldWhileWaiting: this is the whole of why there is an
// allocation lock.  A caller that took what it could and then waited for
// the rest would sit holding objects nobody else can use, waiting for a
// caller that is doing the same thing -- and neither would ever finish.
// So everything taken goes back BEFORE anything is waited for.
func TestNothingIsHeldWhileWaiting(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.stock(len(AutoPoints))
	// Half the pool is somebody else's, so a caller wanting the whole
	// of it cannot be served and has to wait.  It could take six on the
	// way past, and that is exactly what it must not do.
	busy(f, "a benchmark", 0, AutoPool()/2)

	during := atWait(f, func() {
		useAutoOn(context.Background(), s, AutoPool(), true)
	})
	if during == nil {
		t.Fatal("a caller that could not be served did not wait for anything")
	}
	for name, by := range during {
		if by == "us" {
			t.Errorf("%s was still ours at the moment of waiting; "+
				"holding one object while waiting for another is the deadlock",
				name)
		}
	}
}

// TestTheAllocationLockIsGivenBackBeforeWaiting: it is one lock over the
// whole pool, so a caller that waited while holding it would stop
// everybody else from being served -- including the caller whose objects
// it is waiting for, if that caller wants more of them.
func TestTheAllocationLockIsGivenBackBeforeWaiting(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.stock(len(AutoPoints))
	allBusy(f, "somebody else")

	during := atWait(f, func() {
		useAutoOn(context.Background(), s, 1, true)
	})
	if during == nil {
		t.Fatal("a caller with nothing free did not wait for anything")
	}
	if by := during[AutoAllocLock]; by != "" {
		t.Errorf("the allocation lock was held by %q while waiting, "+
			"which stops everybody else being served -- including whoever "+
			"holds the objects being waited for", by)
	}
}

// TestPartOfWhatWasWantedIsGivenBackRatherThanKept: four of the eight
// asked for is not a smaller answer, it is objects taken out of the pool
// that the caller cannot use -- and the next caller, who wanted four,
// finds nothing.
func TestPartOfWhatWasWantedIsGivenBackRatherThanKept(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.stock(len(AutoPoints))
	busy(f, "a benchmark", 0, AutoPool()-3)

	if _, err := useAutoOn(context.Background(), s, 8, false); err == nil {
		t.Fatal("useAutoOn took eight objects out of the three that were free")
	}
	for slot, by := range held(f) {
		if by == "us" {
			t.Errorf("place %d was kept out of an allocation that failed", slot)
		}
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

// TestPlacesTakenAndThenUnusableAreGivenBack: holding a lock over
// objects that could not be worn would leave them unusable to
// everybody, including the next run of this same program.
func TestPlacesTakenAndThenUnusableAreGivenBack(t *testing.T) {
	t.Parallel()

	t.Run("taken without waiting", func(t *testing.T) {
		t.Parallel()
		s, f := newFakeSession(t)
		f.mu.Lock()
		f.capErr = fmt.Errorf("the capability is not answering")
		f.mu.Unlock()

		if _, err := useAutoOn(context.Background(), s, 1, false); err == nil {
			t.Fatal("useAutoOn used objects it could not find")
		}
		for slot, by := range held(f) {
			if by == "us" {
				t.Errorf("place %d is still held after the objects could not be worn", slot)
			}
		}
	})

	t.Run("waited for", func(t *testing.T) {
		t.Parallel()
		s, f := newFakeSession(t)
		allBusy(f, "somebody else")
		f.mu.Lock()
		f.capErr = fmt.Errorf("the capability is not answering")
		f.mu.Unlock()

		if _, err := useAutoOn(context.Background(), s, 1, true); err == nil {
			t.Fatal("useAutoOn used objects it could not find")
		}
		if got := slotsWaitedFor(f); len(got) != 1 {
			t.Errorf("waited on %v", got)
		}
		for slot, by := range held(f) {
			if by == "us" {
				t.Errorf("place %d is still held after the objects could not be worn", slot)
			}
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

// TestWearingMakesTheItemsFirst: the items have to exist before any of
// them can be worn, and the highest place asked for is how far that has
// to reach -- a place at the end of the pool needs every item below it
// to exist as well, because they are all copies of the first.
func TestWearingMakesTheItemsFirst(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.stock(1)
	answerCopies(f)

	a, err := wearSlots(context.Background(), s, []int{4, 5, 6, 7})
	if err != nil {
		t.Fatalf("wearSlots: %v", err)
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

// TestWearingNothingIsRefused: nowhere to run is not somewhere to run
// with no objects in it, and a caller handed an empty Auto would divide
// by the length of it.
func TestWearingNothingIsRefused(t *testing.T) {
	t.Parallel()
	s, _ := newFakeSession(t)

	_, err := wearSlots(context.Background(), s, nil)
	if err == nil || !strings.Contains(err.Error(), "no objects") {
		t.Errorf("wearSlots = %v, want it to say there is nowhere to run", err)
	}
}

// TestMoreObjectsThanTheAvatarHasIsRefusedRatherThanWaitedFor: the
// allocation is all or nothing, so asking for thirteen out of twelve is
// not slow -- it never comes back at all.  The refusal is what turns
// that into a message.
func TestMoreObjectsThanTheAvatarHasIsRefusedRatherThanWaitedFor(t *testing.T) {
	t.Parallel()
	// A port with nothing on it, so that a refusal which stopped being
	// one would fail rather than quietly find the daemon on this
	// machine and take objects on a live avatar.
	_, err := UseAutoAnywhere(context.Background(),
		Options{Addr: "127.0.0.1:1"}, AutoPool()+1)
	if err == nil || !strings.Contains(err.Error(), "for ever") {
		t.Errorf("UseAutoAnywhere = %v, want it to say why it cannot wait", err)
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

		if _, err := wearSlots(context.Background(), s, fourSlots); err == nil {
			t.Error("wearSlots reported success without its first object")
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

		a, err := wearSlots(context.Background(), s, fourSlots)
		if err != nil {
			t.Fatalf("wearSlots: %v", err)
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

		if _, err := wearSlots(context.Background(), s, []int{0}); err == nil {
			t.Error("wearSlots found objects in an inventory it could not read")
		}
	})

	t.Run("nothing to make them from", func(t *testing.T) {
		t.Parallel()
		s, f := newFakeSession(t)
		f.mu.Lock()
		f.presenceErr = fmt.Errorf("the parcel will not have it")
		f.mu.Unlock()

		if _, err := wearSlots(context.Background(), s, fourSlots); err == nil {
			t.Error("wearSlots wore objects that were never made")
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

// ----------------------------------------------- more than a group

// TestAnyCountUpToThePoolCanBeAskedFor: four was the size of a group
// because four is what a benchmark's search uses.  It was never a
// statement about how many objects a program may hold, and a program
// with twelve scripts to run at once wants twelve.
func TestAnyCountUpToThePoolCanBeAskedFor(t *testing.T) {
	t.Parallel()
	for _, n := range []int{1, 2, AutoGroupSize, AutoGroupSize + 2, AutoPool()} {
		s, f := newFakeSession(t)
		f.stock(len(AutoPoints))

		a, err := useAutoOn(context.Background(), s, n, false)
		if err != nil {
			t.Errorf("%d objects: %v", n, err)
			continue
		}
		if len(a.Objects) != n {
			t.Errorf("asked for %d objects and got %d", n, len(a.Objects))
		}
		if len(a.Slots) != n {
			t.Errorf("asked for %d places and got %v", n, a.Slots)
		}
	}
}

// TestNoPlaceIsHandedOutTwice: the daemon's lock is RE-ENTRANT -- a
// client that asks for one it already holds is given it, because asking
// twice and holding once is the sensible answer to a client that asks
// twice.  For anything gathering objects that is a trap, and the same
// object handed out twice is two scripts in one place, which is the one
// thing running several at once must not do.
func TestNoPlaceIsHandedOutTwice(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.stock(len(AutoPoints))

	// The lock does say yes to a place we are already holding, which is
	// what makes this worth a test at all.
	if got, _, err := s.TryLock(context.Background(), AutoSlotLock(0)); err != nil || !got {
		t.Fatalf("the daemon refused a lock its own client holds: %v, %v", got, err)
	}
	s.Unlock(AutoSlotLock(0))

	a, err := useAutoOn(context.Background(), s, AutoPool(), false)
	if err != nil {
		t.Fatalf("useAutoOn: %v", err)
	}
	seen := map[int]bool{}
	for _, slot := range a.Slots {
		if seen[slot] {
			t.Errorf("place %d was handed out twice", slot)
		}
		seen[slot] = true
	}
	ids := map[string]bool{}
	for _, o := range a.Objects {
		if ids[o.ID.String()] {
			t.Errorf("object %s was handed out twice", o.ID)
		}
		ids[o.ID.String()] = true
	}
}

// TestTakingWhatIsAskedForAndNoMore: a run of four scripts that took the
// whole pool would leave eight objects idle and a benchmark queueing
// behind them.
func TestTakingWhatIsAskedForAndNoMore(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.stock(len(AutoPoints))

	if _, err := useAutoOn(context.Background(), s, AutoGroupSize, false); err != nil {
		t.Fatalf("useAutoOn: %v", err)
	}
	if got := len(held(f)); got != AutoGroupSize {
		t.Errorf("%d places are held for a run that asked for %d", got, AutoGroupSize)
	}
}

// TestABenchmarkAndAScriptRunShareThePool: the point of counting
// objects rather than groups is that what is left over is usable.  Eight
// held for scripts leaves four for a benchmark -- which under fixed
// groups of four was true only because eight happens to divide by four.
func TestABenchmarkAndAScriptRunShareThePool(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.stock(len(AutoPoints))
	// Somebody else's script run, holding eight.
	busy(f, "automate --jobs 8", 0, AutoPool()-AutoGroupSize)

	a, err := useAutoOn(context.Background(), s, AutoGroupSize, false)
	if err != nil {
		t.Fatalf("the benchmark: %v", err)
	}
	if len(a.Objects) != AutoGroupSize {
		t.Errorf("the benchmark got %d objects", len(a.Objects))
	}
	for _, slot := range a.Slots {
		if slot < AutoPool()-AutoGroupSize {
			t.Errorf("the benchmark took place %d, which the scripts have", slot)
		}
	}
	if got := slotsWaitedFor(f); len(got) != 0 {
		t.Errorf("the benchmark queued on %v with four objects going spare", got)
	}
}

// TestOneObjectShortIsARefusalAndNotEleven: all or nothing is the whole
// contract.  A caller that wanted twelve and can be given eleven has
// nothing it can do with them -- and eleven taken out of the pool is
// eleven nobody else can have either.
func TestOneObjectShortIsARefusalAndNotEleven(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.stock(len(AutoPoints))
	busy(f, "a benchmark", AutoPool()-1, AutoPool())

	if _, err := useAutoOn(context.Background(), s, AutoPool(), false); err == nil {
		t.Fatal("useAutoOn came back with fewer objects than the caller can use")
	}
	if got := len(held(f)); got != 1 {
		t.Errorf("%d places are held, want only the one somebody else had", got)
	}
}
