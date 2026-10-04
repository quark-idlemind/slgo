package agent

// Link numbers: a viewer numbers a child 2 plus its place in its
// parent's list of children, and that list is only what the updates
// built.  These feed the store updates by hand and read the numbers off.
// Why: doc/objects.md#link-numbers

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

var (
	linkRoot  = msg.MustParseUUID("17027e57-7e57-c0de-f461-1f6f90fc1244")
	linkA     = msg.MustParseUUID("37bb7e57-7e57-c0de-c152-3b6a187a8e10")
	linkB     = msg.MustParseUUID("397f7e57-7e57-c0de-28a6-d4e7227ab996")
	linkC     = msg.MustParseUUID("78f87e57-7e57-c0de-007e-f1c57cef9e52")
	linkRoot2 = msg.MustParseUUID("a64f7e57-7e57-c0de-3f0b-452fd491b630")
	linkD     = msg.MustParseUUID("15537e57-7e57-c0de-17fe-2831a17b6f61")
)

// told is one full update: this prim, with this local id, under this
// parent.
func told(o *Objects, id msg.UUID, local, parent uint32) {
	o.update(&msg.ObjectUpdate_ObjectData{ID: local, ParentID: parent, FullID: id, PCode: 9},
		msg.Vector3{}, 0)
}

// numbered is the link numbers of these prims, in the order given.
func numbered(t *testing.T, o *Objects, ids ...msg.UUID) []int {
	t.Helper()
	out := make([]int, len(ids))
	for i, id := range ids {
		v, ok := o.Get(id)
		if !ok {
			t.Fatalf("%s is not in the store", id)
		}
		out[i] = v.LinkNumber
	}
	return out
}

func expectNumbers(t *testing.T, o *Objects, want []int, ids ...msg.UUID) {
	t.Helper()
	if got := numbered(t, o, ids...); !reflect.DeepEqual(got, want) {
		t.Errorf("link numbers %v, want %v", got, want)
	}
}

func TestLinkNumbersFollowTheOrderTheChildrenArrive(t *testing.T) {
	t.Parallel()

	o := newObjects()
	told(o, linkRoot, 1, 0)
	told(o, linkB, 3, 1)
	told(o, linkA, 2, 1)
	told(o, linkC, 4, 1)

	// Local ids say nothing: B came first.
	expectNumbers(t, o, []int{1, 2, 3, 4}, linkRoot, linkB, linkA, linkC)
	if got, _ := o.byLocal(3); got.LinkNumber != 2 {
		t.Errorf("by local id, B is %d, want 2", got.LinkNumber)
	}
	for _, v := range o.All() {
		want := map[msg.UUID]int{linkRoot: 1, linkB: 2, linkA: 3, linkC: 4}[v.ID]
		if v.LinkNumber != want {
			t.Errorf("All numbers %s %d, want %d", v.ID, v.LinkNumber, want)
		}
	}

	set := o.Linkset(1)
	var ids []msg.UUID
	for _, v := range set {
		ids = append(ids, v.ID)
	}
	if want := []msg.UUID{linkRoot, linkB, linkA, linkC}; !reflect.DeepEqual(ids, want) {
		t.Errorf("Linkset is %v, want %v", ids, want)
	}
	if o.Linkset(99) != nil {
		t.Error("Linkset of a local id nothing has is not nil")
	}
}

func TestAPrimWithNothingLinkedIsNumberedZero(t *testing.T) {
	t.Parallel()

	o := newObjects()
	told(o, linkRoot, 1, 0)
	expectNumbers(t, o, []int{0}, linkRoot)
}

func TestChildrenThatArriveBeforeTheirRootWaitInOrder(t *testing.T) {
	t.Parallel()

	o := newObjects()
	told(o, linkC, 4, 1)
	told(o, linkA, 2, 1)
	// A viewer has no number for an orphan.
	expectNumbers(t, o, []int{0, 0}, linkC, linkA)

	told(o, linkRoot, 1, 0)
	told(o, linkB, 3, 1)
	expectNumbers(t, o, []int{1, 2, 3, 4}, linkRoot, linkC, linkA, linkB)
}

func TestAKillInTheMiddleClosesTheNumbersUp(t *testing.T) {
	t.Parallel()

	o := newObjects()
	told(o, linkRoot, 1, 0)
	told(o, linkA, 2, 1)
	told(o, linkB, 3, 1)
	told(o, linkC, 4, 1)

	o.kill(3)
	expectNumbers(t, o, []int{1, 2, 3}, linkRoot, linkA, linkC)

	// A prim that comes back is a new arrival, at the end.
	told(o, linkB, 3, 1)
	expectNumbers(t, o, []int{1, 2, 3, 4}, linkRoot, linkA, linkC, linkB)
}

func TestKillingARootForgetsItsChildList(t *testing.T) {
	t.Parallel()

	o := newObjects()
	told(o, linkRoot, 1, 0)
	told(o, linkA, 2, 1)
	o.kill(1)

	// The local id comes back for another prim, and A is told again.
	told(o, linkRoot2, 1, 0)
	told(o, linkA, 2, 1)
	told(o, linkB, 3, 1)
	expectNumbers(t, o, []int{1, 2, 3}, linkRoot2, linkA, linkB)
}

func TestARepeatedUpdateDoesNotMoveAPrim(t *testing.T) {
	t.Parallel()

	o := newObjects()
	told(o, linkRoot, 1, 0)
	told(o, linkA, 2, 1)
	told(o, linkB, 3, 1)
	told(o, linkC, 4, 1)

	// The same update twice in a row, and interleaved as two agents in
	// one region would deliver the region's descriptions.
	told(o, linkA, 2, 1)
	told(o, linkA, 2, 1)
	told(o, linkB, 3, 1)
	told(o, linkA, 2, 1)
	told(o, linkC, 4, 1)
	told(o, linkB, 3, 1)
	told(o, linkRoot, 1, 0)
	expectNumbers(t, o, []int{1, 2, 3, 4}, linkRoot, linkA, linkB, linkC)
}

func TestTwoAgentsInOneRegionDoNotReorderALinkset(t *testing.T) {
	t.Parallel()

	one, _ := offlineSession(t)
	two, _ := offlineSession(t)
	shared := newObjects()
	one.objects.Store(shared)
	two.objects.Store(shared)

	set := arriving(t,
		msg.ObjectUpdate_ObjectData{ID: 1, FullID: linkRoot, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 3, ParentID: 1, FullID: linkB, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 2, ParentID: 1, FullID: linkA, PCode: 9},
	)
	feed(t, one, set)
	feed(t, two, set)
	feed(t, one, set)
	expectNumbers(t, shared, []int{1, 2, 3}, linkRoot, linkB, linkA)
}

func TestAWornLinksetIsNumberedUnderTheAvatar(t *testing.T) {
	t.Parallel()

	o := newObjects()
	told(o, linkRoot, 1, 0) // the avatar
	told(o, linkA, 2, 1)    // the attachment's root
	told(o, linkB, 3, 2)
	told(o, linkC, 4, 2)

	expectNumbers(t, o, []int{1, 2, 2, 3}, linkRoot, linkA, linkB, linkC)
}

func TestFlushForgetsTheLinkOrder(t *testing.T) {
	t.Parallel()

	o := newObjects()
	told(o, linkRoot, 1, 0)
	told(o, linkA, 2, 1)
	told(o, linkB, 3, 1)
	o.Flush()

	// The region describes the set again, in its own order.
	told(o, linkRoot, 1, 0)
	told(o, linkB, 3, 1)
	told(o, linkA, 2, 1)
	expectNumbers(t, o, []int{1, 2, 3}, linkRoot, linkB, linkA)
}

// known is whether the store believes the link number of each prim.
func known(t *testing.T, o *Objects, ids ...msg.UUID) []bool {
	t.Helper()
	out := make([]bool, len(ids))
	for i, id := range ids {
		v, ok := o.Get(id)
		if !ok {
			t.Fatalf("%s is not in the store", id)
		}
		out[i] = v.LinkKnown
	}
	return out
}

func expectKnown(t *testing.T, o *Objects, want []bool, ids ...msg.UUID) {
	t.Helper()
	if got := known(t, o, ids...); !reflect.DeepEqual(got, want) {
		t.Errorf("link known %v, want %v", got, want)
	}
}

func TestALinksetDescribedFreshIsKnown(t *testing.T) {
	t.Parallel()

	o := newObjects()
	told(o, linkRoot, 1, 0)
	told(o, linkB, 3, 1)
	told(o, linkA, 2, 1)
	expectKnown(t, o, []bool{true, true, true}, linkRoot, linkB, linkA)
	for _, v := range o.Linkset(1) {
		if !v.LinkKnown {
			t.Errorf("Linkset gives %s as unknown", v.ID)
		}
	}
	for _, v := range o.All() {
		if !v.LinkKnown {
			t.Errorf("All gives %s as unknown", v.ID)
		}
	}
}

func TestAnUnlinkedPrimIsKnownAsZero(t *testing.T) {
	t.Parallel()

	o := newObjects()
	told(o, linkRoot, 1, 0)
	v, _ := o.Get(linkRoot)
	if v.LinkNumber != 0 || !v.LinkKnown {
		t.Errorf("unlinked prim is %d, known %v; want 0, true", v.LinkNumber, v.LinkKnown)
	}
}

// The region was watched linking prims on 2026-10-01: each link of one,
// by the client or by llCreateLink, made the new child link 2, so a prim
// the store already held joins at the front; a link of several came as
// one update in the order the prims were made, not the link order.
// Why: doc/objects.md#link-numbers

func TestALinkOfSeveralInOneUpdateIsNotKnown(t *testing.T) {
	t.Parallel()

	a, _ := offlineSession(t)
	o := newObjects()
	a.objects.Store(o)
	feed(t, a, arriving(t,
		msg.ObjectUpdate_ObjectData{ID: 1, FullID: linkRoot, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 2, FullID: linkA, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 3, FullID: linkB, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 4, FullID: linkC, PCode: 9},
	))

	// One update names all three: its order is the order the prims were
	// made in, not the link order, so nothing is guessed.
	feed(t, a, arriving(t,
		msg.ObjectUpdate_ObjectData{ID: 4, ParentID: 1, FullID: linkC, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 3, ParentID: 1, FullID: linkB, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 2, ParentID: 1, FullID: linkA, PCode: 9},
	))
	expectKnown(t, o, []bool{false, false, false, false}, linkRoot, linkA, linkB, linkC)
}

func TestLinksOfOneInSeparateUpdatesAreKnown(t *testing.T) {
	t.Parallel()

	a, _ := offlineSession(t)
	o := newObjects()
	stepping(o)
	a.objects.Store(o)
	feed(t, a, arriving(t,
		msg.ObjectUpdate_ObjectData{ID: 1, FullID: linkRoot, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 2, FullID: linkA, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 3, FullID: linkB, PCode: 9},
	))
	feed(t, a, arriving(t, msg.ObjectUpdate_ObjectData{ID: 2, ParentID: 1, FullID: linkA, PCode: 9}))
	feed(t, a, arriving(t, msg.ObjectUpdate_ObjectData{ID: 3, ParentID: 1, FullID: linkB, PCode: 9}))
	expectNumbers(t, o, []int{1, 2, 3}, linkRoot, linkB, linkA)
	expectKnown(t, o, []bool{true, true, true}, linkRoot, linkA, linkB)
}

// stepping makes the store's clock move on a second each time it is
// read, so that links fed one after another are as far apart as links
// of one are: llCreateLink sleeps its script for a second.
func stepping(o *Objects) {
	var n atomic.Int64
	base := time.Now()
	o.now = func() time.Time { return base.Add(time.Duration(n.Add(1)) * time.Second) }
}

func TestALinkOfTwoSplitOverTwoPacketsIsNotKnown(t *testing.T) {
	t.Parallel()

	// A watching session gets a link of several in ascending local id,
	// sometimes in two packets: one child each, a moment apart.
	a, _, o, _ := linkRig(t)
	feed(t, a, blocks(t, 2))
	feed(t, a, blocks(t, 3))
	expectKnown(t, o, []bool{false, false, false}, linkRoot, linkA, linkB)

	// And a flush and a fresh description of it are not taken as known.
	o.Flush()
	feed(t, a, arriving(t,
		msg.ObjectUpdate_ObjectData{ID: 1, FullID: linkRoot, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 2, ParentID: 1, FullID: linkA, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 3, ParentID: 1, FullID: linkB, PCode: 9},
	))
	expectKnown(t, o, []bool{false, false, false}, linkRoot, linkA, linkB)
}

func TestTwoAgentsEachHearingADifferentChildFirstLeaveTheSetUnknown(t *testing.T) {
	t.Parallel()

	// One store, two agents. A link of two reaches each agent whole, but
	// A's handler applies A's block first and B's handler B's, so each
	// sees one live join and then the other's copy as a repeat.
	one, _, o, _ := linkRig(t)
	two, _ := offlineSession(t)
	two.objects.Store(o)
	feed(t, one, blocks(t, 2))
	feed(t, two, blocks(t, 3))
	feed(t, one, blocks(t, 3))
	feed(t, two, blocks(t, 2))
	expectKnown(t, o, []bool{false, false, false}, linkRoot, linkA, linkB)
}

func TestTwoAgentsHearingALinkAtOnceNeverReportAGuess(t *testing.T) {
	t.Parallel()

	// The same, with the agents' handlers really running at once: a set
	// may come out known only in its true order, and here no session sent
	// the link, so it must never come out known.
	for i := 0; i < 200; i++ {
		one, _, o, _ := linkRig(t)
		two, _ := offlineSession(t)
		two.objects.Store(o)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); feed(t, one, blocks(t, 3, 2)) }()
		go func() { defer wg.Done(); feed(t, two, blocks(t, 2, 3)) }()
		wg.Wait()
		for _, id := range []msg.UUID{linkRoot, linkA, linkB} {
			if v, _ := o.Get(id); v.LinkKnown {
				t.Fatalf("run %d: %s is known after two agents heard a link of two", i, id)
			}
		}
	}
}
func TestLinksOfOneAtATimePutTheNewestFirst(t *testing.T) {
	t.Parallel()

	o := newObjects()
	stepping(o)
	told(o, linkRoot, 1, 0)
	told(o, linkA, 2, 0)
	told(o, linkB, 3, 0)
	told(o, linkC, 4, 0)

	told(o, linkA, 2, 1)
	told(o, linkB, 3, 1)
	told(o, linkC, 4, 1)
	expectNumbers(t, o, []int{1, 2, 3, 4}, linkRoot, linkC, linkB, linkA)
	expectKnown(t, o, []bool{true, true, true, true}, linkRoot, linkA, linkB, linkC)
}

func TestARelinkedPrimBecomesLinkTwoOfItsNewParent(t *testing.T) {
	t.Parallel()

	o := newObjects()
	told(o, linkRoot, 1, 0)
	told(o, linkRoot2, 5, 0)
	told(o, linkA, 2, 1)
	told(o, linkB, 3, 1)
	told(o, linkC, 4, 5)

	told(o, linkA, 2, 5)
	expectNumbers(t, o, []int{1, 2, 1, 2, 3}, linkRoot, linkB, linkRoot2, linkA, linkC)
	expectKnown(t, o, []bool{true, true, true, true, true}, linkRoot, linkB, linkRoot2, linkA, linkC)

	// Unlinked, B leaves, and the root has no children left.
	told(o, linkB, 3, 0)
	expectNumbers(t, o, []int{0, 0}, linkRoot, linkB)
	expectKnown(t, o, []bool{true, true}, linkRoot, linkB)
}

func TestARepeatedUpdateAfterALiveLinkDoesNotMoveAPrim(t *testing.T) {
	t.Parallel()

	o := newObjects()
	told(o, linkRoot, 1, 0)
	told(o, linkA, 2, 1)
	told(o, linkB, 3, 1)
	told(o, linkB, 3, 0)
	told(o, linkB, 3, 1)
	expectNumbers(t, o, []int{1, 2, 3}, linkRoot, linkB, linkA)

	// The same updates again, as a second agent in the region hears them.
	told(o, linkB, 3, 1)
	told(o, linkA, 2, 1)
	told(o, linkB, 3, 1)
	expectNumbers(t, o, []int{1, 2, 3}, linkRoot, linkB, linkA)
}

func TestATakeAndRezDescribesASetAfresh(t *testing.T) {
	t.Parallel()

	o := newObjects()
	told(o, linkRoot, 1, 0)
	told(o, linkA, 2, 1)
	told(o, linkB, 3, 1)

	// Taken and rezzed: everything is killed and described again, in
	// link order.
	o.kill(1)
	o.kill(2)
	o.kill(3)
	told(o, linkRoot, 1, 0)
	told(o, linkB, 3, 1)
	told(o, linkA, 2, 1)
	expectNumbers(t, o, []int{1, 2, 3}, linkRoot, linkB, linkA)
	expectKnown(t, o, []bool{true, true, true}, linkRoot, linkB, linkA)
}

func TestAStoreTakenFromAnotherAgentIsUnknownUntilDescribedAfresh(t *testing.T) {
	t.Parallel()

	from := newObjects()
	told(from, linkRoot, 1, 0)
	told(from, linkA, 2, 1)
	told(from, linkB, 3, 1)
	o := newObjects()
	if n := o.absorb(from); n != 3 {
		t.Fatalf("absorbed %d, want 3", n)
	}
	expectKnown(t, o, []bool{false, false, false}, linkRoot, linkA, linkB)

	// Described again, after a flush, the set is known.
	o.Flush()
	told(o, linkRoot, 1, 0)
	told(o, linkA, 2, 1)
	told(o, linkB, 3, 1)
	expectKnown(t, o, []bool{true, true, true}, linkRoot, linkA, linkB)
}

func TestASetLinkedSeveralAtOnceStaysUnknownAfterAFlush(t *testing.T) {
	t.Parallel()

	a, _ := offlineSession(t)
	o := newObjects()
	a.objects.Store(o)
	loose := arriving(t,
		msg.ObjectUpdate_ObjectData{ID: 1, FullID: linkRoot, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 2, FullID: linkA, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 3, FullID: linkB, PCode: 9},
	)
	feed(t, a, loose)
	feed(t, a, arriving(t,
		msg.ObjectUpdate_ObjectData{ID: 3, ParentID: 1, FullID: linkB, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 2, ParentID: 1, FullID: linkA, PCode: 9},
	))
	expectKnown(t, o, []bool{false, false, false}, linkRoot, linkA, linkB)

	// The region describes it again after a flush, as if fresh, in the
	// order it misorders it in: still unknown, children first or root
	// first.
	o.Flush()
	feed(t, a, arriving(t,
		msg.ObjectUpdate_ObjectData{ID: 3, ParentID: 1, FullID: linkB, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 2, ParentID: 1, FullID: linkA, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 1, FullID: linkRoot, PCode: 9},
	))
	expectKnown(t, o, []bool{false, false, false}, linkRoot, linkA, linkB)
	o.Flush()
	feed(t, a, arriving(t,
		msg.ObjectUpdate_ObjectData{ID: 1, FullID: linkRoot, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 3, ParentID: 1, FullID: linkB, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 2, ParentID: 1, FullID: linkA, PCode: 9},
	))
	expectKnown(t, o, []bool{false, false, false}, linkRoot, linkA, linkB)

	// Deleted or taken, the root is killed and forgotten: a set with
	// that root id described later is known.
	o.kill(1)
	o.kill(2)
	o.kill(3)
	feed(t, a, arriving(t,
		msg.ObjectUpdate_ObjectData{ID: 1, FullID: linkRoot, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 2, ParentID: 1, FullID: linkA, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 3, ParentID: 1, FullID: linkB, PCode: 9},
	))
	expectKnown(t, o, []bool{true, true, true}, linkRoot, linkA, linkB)
}

func TestTrimmingARootDoesNotForgetThatItsSetIsMisordered(t *testing.T) {
	t.Parallel()

	o := newObjects()
	told(o, linkRoot, 1, 0)
	told(o, linkA, 2, 0)
	told(o, linkB, 3, 0)
	told(o, linkB, 3, 1)
	told(o, linkA, 2, 1)
	o.joinedTogether(1, []msg.UUID{linkB, linkA})

	// Out of range is not gone: only a kill drops the memory.
	all := o.All()
	o.mu.Lock()
	for _, v := range all {
		o.forgetLocked(o.byID[v.ID])
	}
	o.mu.Unlock()
	told(o, linkRoot, 1, 0)
	told(o, linkA, 2, 1)
	told(o, linkB, 3, 1)
	expectKnown(t, o, []bool{false, false, false}, linkRoot, linkA, linkB)
}

// A link this session sent names its children's order, which the
// region's update for the set does not carry.
// Why: doc/objects.md#link-numbers

// linkRig is a session with a store whose clock a test sets: the root is
// local 1 and the loose prims A, B and C are 2, 3 and 4.  advance moves
// the clock on.
func linkRig(t *testing.T) (*Agent, *sentPackets, *Objects, func(time.Duration)) {
	t.Helper()
	a, w := offlineSession(t)
	o := newObjects()
	base := time.Now()
	var offset atomic.Int64
	o.now = func() time.Time { return base.Add(time.Duration(offset.Load())) }
	a.objects.Store(o)
	feed(t, a, arriving(t,
		msg.ObjectUpdate_ObjectData{ID: 1, FullID: linkRoot, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 2, FullID: linkA, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 3, FullID: linkB, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 4, FullID: linkC, PCode: 9},
	))
	return a, w, o, func(d time.Duration) { offset.Add(int64(d)) }
}

// sendLink sends an ObjectLink naming these local ids, root first,
// through the session's sender, and waits for the store to have heard
// it go: the tap runs after the datagram is written.
func sendLink(t *testing.T, a *Agent, o *Objects, locals ...uint32) {
	t.Helper()
	m := &msg.ObjectLink{}
	for _, l := range locals {
		m.ObjectData = append(m.ObjectData, msg.ObjectLink_ObjectData{ObjectLocalID: l})
	}
	if err := a.Send.Send(context.Background(), m); err != nil {
		t.Fatalf("sending the link: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		o.mu.RLock()
		_, ok := o.links[locals[0]]
		o.mu.RUnlock()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the store never recorded the link of %v", locals)
		}
		time.Sleep(time.Millisecond)
	}
}

func recorded(o *Objects, root uint32) bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	_, ok := o.links[root]
	return ok
}

// blocks is an update giving these prims, by local id, the root as
// their parent.
func blocks(t *testing.T, locals ...uint32) *msg.ObjectUpdate {
	t.Helper()
	ids := map[uint32]msg.UUID{1: linkRoot, 2: linkA, 3: linkB, 4: linkC, 5: linkD}
	var ds []msg.ObjectUpdate_ObjectData
	for _, l := range locals {
		ds = append(ds, msg.ObjectUpdate_ObjectData{ID: l, ParentID: 1, FullID: ids[l], PCode: 9})
	}
	return arriving(t, ds...)
}

func TestALinkThisSessionSentGivesTheSetItsNamedOrder(t *testing.T) {
	t.Parallel()

	a, _, o, _ := linkRig(t)
	// Root, then B, C, A: a mixed order.
	sendLink(t, a, o, 1, 3, 4, 2)

	// The region sends the children from the last made to the first.
	feed(t, a, blocks(t, 4, 3, 2))
	expectKnown(t, o, []bool{true, true, true, true}, linkRoot, linkA, linkB, linkC)
	expectNumbers(t, o, []int{1, 4, 2, 3}, linkRoot, linkA, linkB, linkC)
	if recorded(o, 1) {
		t.Error("the record was kept after it was used")
	}
}

func TestALinkSplitOverTwoPacketsIsKnownWhenTheLastArrives(t *testing.T) {
	t.Parallel()

	for _, split := range [][][]uint32{{{4, 3}, {2}}, {{4}, {3, 2}}} {
		a, _, o, _ := linkRig(t)
		sendLink(t, a, o, 1, 3, 4, 2)

		feed(t, a, blocks(t, split[0]...))
		// What has not arrived is still loose, and known as 0.
		ids := map[uint32]msg.UUID{4: linkC, 3: linkB, 2: linkA}
		got := []msg.UUID{linkRoot}
		for _, l := range split[0] {
			got = append(got, ids[l])
		}
		expectKnown(t, o, make([]bool, len(got)), got...)
		if !recorded(o, 1) {
			t.Errorf("split %v: the record was dropped for children that had not arrived", split)
		}

		feed(t, a, blocks(t, split[1]...))
		expectKnown(t, o, []bool{true, true, true, true}, linkRoot, linkA, linkB, linkC)
		expectNumbers(t, o, []int{1, 4, 2, 3}, linkRoot, linkA, linkB, linkC)
	}
}

func TestALinkWithAnExtraChildInTheSetIsNotUsed(t *testing.T) {
	t.Parallel()

	a, _, o, _ := linkRig(t)
	feed(t, a, arriving(t,
		msg.ObjectUpdate_ObjectData{ID: 5, ParentID: 1, FullID: linkD, PCode: 9}))
	sendLink(t, a, o, 1, 3, 4, 2)
	feed(t, a, blocks(t, 4, 3, 2))

	expectKnown(t, o, []bool{false, false, false, false, false}, linkRoot, linkA, linkB, linkC, linkD)
	if recorded(o, 1) {
		t.Error("the record of a set that is not the one named was kept")
	}
}

func TestALinkWithAChildUnderAnotherParentIsNotUsed(t *testing.T) {
	t.Parallel()

	a, _, o, _ := linkRig(t)
	feed(t, a, arriving(t,
		msg.ObjectUpdate_ObjectData{ID: 5, FullID: linkD, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 2, ParentID: 5, FullID: linkA, PCode: 9}))
	sendLink(t, a, o, 1, 3, 4, 2)
	feed(t, a, blocks(t, 4, 3))

	expectKnown(t, o, []bool{false, false, false}, linkRoot, linkB, linkC)
	if recorded(o, 1) {
		t.Error("the record was kept for a child that is under another parent")
	}
	// What arrives afterwards finds no record to complete.
	feed(t, a, blocks(t, 2))
	expectKnown(t, o, []bool{false, false, false, false}, linkRoot, linkA, linkB, linkC)
}

func TestAMissingChildAfterTheWindowLeavesTheSetUnknown(t *testing.T) {
	t.Parallel()

	a, _, o, advance := linkRig(t)
	sendLink(t, a, o, 1, 3, 4, 2)
	feed(t, a, blocks(t, 4, 3))
	advance(linkWindow + time.Second)
	feed(t, a, blocks(t, 2))

	expectKnown(t, o, []bool{false, false, false, false}, linkRoot, linkA, linkB, linkC)
	if recorded(o, 1) {
		t.Error("an expired record was kept")
	}
}

func TestALinkTheUpdateOutlastsIsNotUsed(t *testing.T) {
	t.Parallel()

	a, _, o, advance := linkRig(t)
	sendLink(t, a, o, 1, 3, 4, 2)
	advance(linkWindow + time.Second)
	feed(t, a, blocks(t, 4, 3, 2))
	expectKnown(t, o, []bool{false, false, false, false}, linkRoot, linkA, linkB, linkC)

	// Just inside the window it is used.
	a, _, o, advance = linkRig(t)
	sendLink(t, a, o, 1, 3, 4, 2)
	advance(linkWindow - time.Second)
	feed(t, a, blocks(t, 4, 3, 2))
	expectKnown(t, o, []bool{true, true, true, true}, linkRoot, linkA, linkB, linkC)
}

func TestALinkUsedStillLeavesTheSetMisorderedAfterAFlush(t *testing.T) {
	t.Parallel()

	a, _, o, _ := linkRig(t)
	sendLink(t, a, o, 1, 3, 4, 2)
	feed(t, a, blocks(t, 4, 3, 2))
	expectKnown(t, o, []bool{true, true, true, true}, linkRoot, linkA, linkB, linkC)

	// A repeat of the root's update changes nothing.
	feed(t, a, arriving(t, msg.ObjectUpdate_ObjectData{ID: 1, FullID: linkRoot, PCode: 9}))
	expectKnown(t, o, []bool{true, true, true, true}, linkRoot, linkA, linkB, linkC)

	// Described afresh after a flush, in the region's own order, which
	// is not the link order: unknown.
	o.Flush()
	feed(t, a, arriving(t,
		msg.ObjectUpdate_ObjectData{ID: 1, FullID: linkRoot, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 2, ParentID: 1, FullID: linkA, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 3, ParentID: 1, FullID: linkB, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 4, ParentID: 1, FullID: linkC, PCode: 9},
	))
	expectKnown(t, o, []bool{false, false, false, false}, linkRoot, linkA, linkB, linkC)
}

func TestALiveLinkOfOneToAMisorderedSetIsKnown(t *testing.T) {
	t.Parallel()

	a, w, o, advance := linkRig(t)
	sendLink(t, a, o, 1, 3, 2)
	feed(t, a, blocks(t, 3, 2))
	expectNumbers(t, o, []int{1, 2, 3}, linkRoot, linkB, linkA)

	// Another prim joins it alone, a second later: link 2, and the set
	// stays known.
	advance(time.Second)
	sendLinkNotRecorded(t, a, w, o, 1, 4)
	feed(t, a, blocks(t, 4))
	expectKnown(t, o, []bool{true, true, true, true}, linkRoot, linkA, linkB, linkC)
	expectNumbers(t, o, []int{1, 2, 3, 4}, linkRoot, linkC, linkB, linkA)
}

func TestALinkOfOneDoesNotUseTheRecord(t *testing.T) {
	t.Parallel()

	a, w, o, advance := linkRig(t)
	// A link of one is not recorded; a link of several for another root
	// goes out after it, so that the tap has run for both.
	sendLinkNotRecorded(t, a, w, o, 1, 2)
	if recorded(o, 1) {
		t.Error("a link of one was recorded")
	}
	feed(t, a, blocks(t, 2))
	expectKnown(t, o, []bool{true, true}, linkRoot, linkA)
	expectNumbers(t, o, []int{1, 2}, linkRoot, linkA)

	// The next of one, a second later, goes to the front, as before.
	advance(time.Second)
	sendLinkNotRecorded(t, a, w, o, 1, 3)
	feed(t, a, blocks(t, 3))
	expectKnown(t, o, []bool{true, true, true}, linkRoot, linkA, linkB)
	expectNumbers(t, o, []int{1, 3, 2}, linkRoot, linkA, linkB)
}

func TestALinkOfOneAfterALinkOfSeveralLeavesNoStaleRecord(t *testing.T) {
	t.Parallel()

	a, w, o, _ := linkRig(t)
	// A link of several whose update never comes, and a link of one for
	// the same root: the one is an extra child, and the record goes.
	sendLink(t, a, o, 1, 3, 4)
	sendLinkNotRecorded(t, a, w, o, 1, 2)
	feed(t, a, blocks(t, 2))
	expectKnown(t, o, []bool{true, true}, linkRoot, linkA)
	if recorded(o, 1) {
		t.Error("a link of one left the record of an earlier link in place")
	}
	// The earlier link's update arriving now is not given an order.
	feed(t, a, blocks(t, 4, 3))
	expectKnown(t, o, []bool{false, false, false, false}, linkRoot, linkA, linkB, linkC)
}

// sendLinkNotRecorded sends a link of one, and waits until the store
// has heard it by sending a link of several for a root that is not in
// the set and seeing that one recorded.  The tap handles them in order.
func sendLinkNotRecorded(t *testing.T, a *Agent, w *sentPackets, o *Objects, locals ...uint32) {
	t.Helper()
	if err := a.Send.Send(context.Background(), &msg.ObjectLink{
		ObjectData: []msg.ObjectLink_ObjectData{{ObjectLocalID: locals[0]}, {ObjectLocalID: locals[1]}},
	}); err != nil {
		t.Fatalf("sending the link: %v", err)
	}
	sendLink(t, a, o, 900, 901, 902)
	o.mu.Lock()
	delete(o.links, 900)
	o.mu.Unlock()
}

func TestALinkSentByOneAgentIsUsedWhenAnotherHearsTheUpdateFirst(t *testing.T) {
	t.Parallel()

	// Two agents in one region share a store. One sends the link; the
	// region's update reaches the other first, split and in its own
	// order, and only then reaches the one that linked.
	linker, _, o, _ := linkRig(t)
	watcher, _ := offlineSession(t)
	watcher.objects.Store(o)
	sendLink(t, linker, o, 1, 3, 4, 2)

	feed(t, watcher, blocks(t, 2, 3))
	// C has not joined yet, so it is a loose prim, known as 0.
	expectKnown(t, o, []bool{false, false, false, true}, linkRoot, linkA, linkB, linkC)
	feed(t, watcher, blocks(t, 4))
	expectKnown(t, o, []bool{true, true, true, true}, linkRoot, linkA, linkB, linkC)
	expectNumbers(t, o, []int{1, 2, 3, 4}, linkRoot, linkB, linkC, linkA)

	// The linker's own copy of the update is a repeat, and changes nothing.
	feed(t, linker, blocks(t, 4, 3, 2))
	expectKnown(t, o, []bool{true, true, true, true}, linkRoot, linkA, linkB, linkC)
	expectNumbers(t, o, []int{1, 2, 3, 4}, linkRoot, linkB, linkC, linkA)
}

// Two sets of known order, A (root 1, child A at 2) and B (root 5,
// children C at 4 and D at 6, in that order), as the region describes
// them afresh.
func twoSets(t *testing.T) (*Agent, *Objects) {
	t.Helper()
	a, _ := offlineSession(t)
	o := newObjects()
	a.objects.Store(o)
	feed(t, a, arriving(t,
		msg.ObjectUpdate_ObjectData{ID: 1, FullID: linkRoot, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 2, ParentID: 1, FullID: linkA, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 5, FullID: linkRoot2, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 4, ParentID: 5, FullID: linkC, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 6, ParentID: 5, FullID: linkD, PCode: 9},
	))
	expectNumbers(t, o, []int{1, 2, 1, 2, 3}, linkRoot, linkA, linkRoot2, linkC, linkD)
	return a, o
}

func TestASetLinkedWholeToAnotherFollowsTheRootThatStays(t *testing.T) {
	t.Parallel()

	a, o := twoSets(t)
	// B joins A, as llCreateLink(B's root, TRUE) or an ObjectLink naming
	// A's root and then B's does; the update lists B's prims by local id.
	feed(t, a, arriving(t,
		msg.ObjectUpdate_ObjectData{ID: 6, ParentID: 1, FullID: linkD, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 5, ParentID: 1, FullID: linkRoot2, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 4, ParentID: 1, FullID: linkC, PCode: 9},
	))
	expectNumbers(t, o, []int{1, 2, 3, 4, 5}, linkRoot, linkRoot2, linkC, linkD, linkA)
	expectKnown(t, o, []bool{true, true, true, true, true}, linkRoot, linkRoot2, linkC, linkD, linkA)
}

func TestASetWhoseRootBecomesAChildFollowsTheOtherRoot(t *testing.T) {
	t.Parallel()

	a, o := twoSets(t)
	// A joins B, as llCreateLink(B's root, FALSE) does.
	feed(t, a, arriving(t,
		msg.ObjectUpdate_ObjectData{ID: 2, ParentID: 5, FullID: linkA, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 1, ParentID: 5, FullID: linkRoot, PCode: 9},
	))
	expectNumbers(t, o, []int{1, 2, 3, 4, 5}, linkRoot2, linkRoot, linkA, linkC, linkD)
	expectKnown(t, o, []bool{true, true, true, true, true}, linkRoot2, linkRoot, linkA, linkC, linkD)
}

func TestPartOfASetJoiningIsNotKnown(t *testing.T) {
	t.Parallel()

	a, o := twoSets(t)
	// B's root and C move, and D does not: not one whole set.
	feed(t, a, arriving(t,
		msg.ObjectUpdate_ObjectData{ID: 5, ParentID: 1, FullID: linkRoot2, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 4, ParentID: 1, FullID: linkC, PCode: 9},
	))
	expectKnown(t, o, []bool{false, false, false, false}, linkRoot, linkRoot2, linkC, linkA)
}

func TestASetOfUnknownOrderJoiningIsNotKnown(t *testing.T) {
	t.Parallel()

	a, o := twoSets(t)
	// B's order is first made unknown: D and C relinked to it in one update.
	feed(t, a, arriving(t,
		msg.ObjectUpdate_ObjectData{ID: 4, FullID: linkC, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 6, FullID: linkD, PCode: 9},
	))
	feed(t, a, arriving(t,
		msg.ObjectUpdate_ObjectData{ID: 6, ParentID: 5, FullID: linkD, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 4, ParentID: 5, FullID: linkC, PCode: 9},
	))
	expectKnown(t, o, []bool{false, false, false}, linkRoot2, linkC, linkD)
	feed(t, a, arriving(t,
		msg.ObjectUpdate_ObjectData{ID: 6, ParentID: 1, FullID: linkD, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 5, ParentID: 1, FullID: linkRoot2, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 4, ParentID: 1, FullID: linkC, PCode: 9},
	))
	expectKnown(t, o, []bool{false, false, false, false, false}, linkRoot, linkRoot2, linkC, linkD, linkA)
}

// Sitters: a viewer appends every child, avatar or not, so an avatar
// sitting on a set is numbered after all of its prims, in the order the
// avatars sat.
// Why: doc/objects.md#link-numbers
var (
	sitterOne = msg.MustParseUUID("48f07e57-7e57-c0de-7b78-68dc421e249b")
	sitterTwo = msg.MustParseUUID("99a17e57-7e57-c0de-3c9c-78eb61efe352")
	linkSpare = msg.MustParseUUID("af667e57-7e57-c0de-7202-f78602af6e09")
)

// sat is one full update: an avatar with this local id under this parent.
func sat(o *Objects, id msg.UUID, local, parent uint32) {
	o.update(&msg.ObjectUpdate_ObjectData{ID: local, ParentID: parent, FullID: id, PCode: pcodeAvatar},
		msg.Vector3{}, 0)
}

func TestSittersAreNumberedAfterThePrimsInTheOrderTheySat(t *testing.T) {
	t.Parallel()

	o := newObjects()
	told(o, linkRoot, 1, 0)
	told(o, linkA, 2, 1)
	told(o, linkB, 3, 1)
	expectNumbers(t, o, []int{1, 2, 3}, linkRoot, linkA, linkB)

	// Both avatars are in the region, standing, before they sit.
	sat(o, sitterOne, 10, 0)
	sat(o, sitterTwo, 11, 0)
	sat(o, sitterOne, 10, 1)
	expectNumbers(t, o, []int{1, 2, 3, 4}, linkRoot, linkA, linkB, sitterOne)
	sat(o, sitterTwo, 11, 1)
	expectNumbers(t, o, []int{1, 2, 3, 4, 5}, linkRoot, linkA, linkB, sitterOne, sitterTwo)
	expectKnown(t, o, []bool{true, true, true, true, true}, linkRoot, linkA, linkB, sitterOne, sitterTwo)

	// All and Linkset agree with Get, the prims first.
	var ids []msg.UUID
	var nums []int
	for _, v := range o.Linkset(1) {
		ids, nums = append(ids, v.ID), append(nums, v.LinkNumber)
	}
	if want := []msg.UUID{linkRoot, linkA, linkB, sitterOne, sitterTwo}; !reflect.DeepEqual(ids, want) {
		t.Errorf("Linkset is %v, want %v", ids, want)
	}
	if want := []int{1, 2, 3, 4, 5}; !reflect.DeepEqual(nums, want) {
		t.Errorf("Linkset numbers %v, want %v", nums, want)
	}
	for _, v := range o.All() {
		if want := map[msg.UUID]int{linkRoot: 1, linkA: 2, linkB: 3, sitterOne: 4, sitterTwo: 5}[v.ID]; v.LinkNumber != want {
			t.Errorf("All numbers %s %d, want %d", v.ID, v.LinkNumber, want)
		}
	}

	// The first stands and the second closes up.
	sat(o, sitterOne, 10, 0)
	expectNumbers(t, o, []int{1, 2, 3, 0, 4}, linkRoot, linkA, linkB, sitterOne, sitterTwo)

	// Both stand, and the prims are as they were built.
	sat(o, sitterTwo, 11, 0)
	expectNumbers(t, o, []int{1, 2, 3, 0, 0}, linkRoot, linkA, linkB, sitterOne, sitterTwo)
	expectKnown(t, o, []bool{true, true, true, true, true}, linkRoot, linkA, linkB, sitterOne, sitterTwo)
}

func TestASitterOnASinglePrimIsLinkTwoAndThePrimLinkOne(t *testing.T) {
	t.Parallel()

	o := newObjects()
	told(o, linkRoot, 1, 0)
	expectNumbers(t, o, []int{0}, linkRoot)
	sat(o, sitterOne, 10, 1)
	expectNumbers(t, o, []int{1, 2}, linkRoot, sitterOne)
	sat(o, sitterOne, 10, 0)
	expectNumbers(t, o, []int{0, 0}, linkRoot, sitterOne)
}

func TestAKilledSitterClosesTheOthersUp(t *testing.T) {
	t.Parallel()

	o := newObjects()
	told(o, linkRoot, 1, 0)
	told(o, linkA, 2, 1)
	sat(o, sitterOne, 10, 0)
	sat(o, sitterTwo, 11, 0)
	sat(o, sitterOne, 10, 1)
	sat(o, sitterTwo, 11, 1)
	o.kill(10)
	expectNumbers(t, o, []int{1, 2, 3}, linkRoot, linkA, sitterTwo)
	o.kill(1)
	if len(o.sitters) != 0 {
		t.Error("a killed root kept its sitters' list")
	}
}

func TestAPrimLinkedWhileSomebodySitsGoesInAmongThePrims(t *testing.T) {
	t.Parallel()

	o := newObjects()
	told(o, linkRoot, 1, 0)
	told(o, linkA, 2, 1)
	told(o, linkB, 3, 1)
	sat(o, sitterOne, 10, 0)
	sat(o, sitterOne, 10, 1)
	told(o, linkSpare, 4, 0)

	// A live link of one is link 2, the old prims move up, and the sitter
	// is still after every prim: 5 of 5.
	told(o, linkSpare, 4, 1)
	expectNumbers(t, o, []int{1, 2, 3, 4, 5}, linkRoot, linkSpare, linkA, linkB, sitterOne)
	expectKnown(t, o, []bool{true, true, true, true, true}, linkRoot, linkSpare, linkA, linkB, sitterOne)
}

func TestASitFollowedByALinkIsNotTakenForALinkOfSeveral(t *testing.T) {
	t.Parallel()

	o := newObjects()
	base := time.Unix(1700000000, 0)
	o.now = func() time.Time { return base }
	told(o, linkRoot, 1, 0)
	told(o, linkA, 2, 1)
	told(o, linkSpare, 4, 0)
	sat(o, sitterOne, 10, 0)

	// Both at the same instant, well inside joinWindow.
	sat(o, sitterOne, 10, 1)
	told(o, linkSpare, 4, 1)
	expectNumbers(t, o, []int{1, 2, 3, 4}, linkRoot, linkSpare, linkA, sitterOne)
	expectKnown(t, o, []bool{true, true, true, true}, linkRoot, linkSpare, linkA, sitterOne)
	if o.misordered[linkRoot] {
		t.Error("a sit marked the set as one the region misorders")
	}
}

func TestAnAvatarDescribedAlreadySeatedGoesAfterThePrims(t *testing.T) {
	t.Parallel()

	// The prims first, then the avatar.
	o := newObjects()
	told(o, linkRoot, 1, 0)
	told(o, linkA, 2, 1)
	sat(o, sitterOne, 10, 1)
	told(o, linkB, 3, 1)
	expectNumbers(t, o, []int{1, 2, 3, 4}, linkRoot, linkA, linkB, sitterOne)

	// The avatar first, then the root, then the prims.
	o = newObjects()
	sat(o, sitterOne, 10, 1)
	told(o, linkB, 3, 1)
	told(o, linkRoot, 1, 0)
	told(o, linkA, 2, 1)
	expectNumbers(t, o, []int{1, 2, 3, 4}, linkRoot, linkB, linkA, sitterOne)
	expectKnown(t, o, []bool{true, true, true, true}, linkRoot, linkB, linkA, sitterOne)

	// Two described seated are numbered after the prims, but their order
	// is not known.
	sat(o, sitterTwo, 11, 1)
	expectNumbers(t, o, []int{4, 5}, sitterOne, sitterTwo)
	expectKnown(t, o, []bool{false, false}, sitterOne, sitterTwo)
	expectKnown(t, o, []bool{true, true, true}, linkRoot, linkB, linkA)
	sat(o, sitterOne, 10, 0)
	expectKnown(t, o, []bool{true}, sitterTwo)

	// A flush forgets the sitters with the prims.
	o.Flush()
	if len(o.sitters) != 0 || len(o.sitUnordered) != 0 {
		t.Error("a flush left sitters behind")
	}
}
