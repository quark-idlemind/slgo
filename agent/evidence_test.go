package agent

// How each object was described, and when a linkset's order is known:
// the store orders a set's children by the packet that listed them, and
// calls the order known when the root was described and every child was
// listed by an update the region sent of its own accord.
// Why: doc/objects.md#when-a-set-is-known

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// one is a full-update block for a prim.
func one(id msg.UUID, local, parent uint32) *msg.ObjectUpdate_ObjectData {
	return &msg.ObjectUpdate_ObjectData{ID: local, ParentID: parent, FullID: id, PCode: 9}
}

// packet tells the store these blocks as one full message, as the
// handler does for one packet.
func packet(o *Objects, seq uint32, ds ...*msg.ObjectUpdate_ObjectData) {
	in := o.arrive(1, DescFull, seq, len(ds))
	for i, d := range ds {
		o.updateAt(d, msg.Vector3{}, 0, in, i)
	}
}

// packetOn is packet for a block that came on another circuit, which
// numbers its packets by itself.
func packetOn(o *Objects, circ, seq uint32, ds ...*msg.ObjectUpdate_ObjectData) {
	in := o.arrive(circ, DescFull, seq, len(ds))
	for i, d := range ds {
		o.updateAt(d, msg.Vector3{}, 0, in, i)
	}
}

func kinds(v *Object) []DescKind {
	var out []DescKind
	for _, d := range v.Descriptions() {
		out = append(out, d.Kind)
	}
	return out
}

func TestADescriptionRingKeepsOnlyTheLastFew(t *testing.T) {
	t.Parallel()

	o := newObjects()
	packet(o, 1, one(linkRoot, 1, 0), one(linkA, 2, 1))
	for i := 0; i < 20; i++ {
		packet(o, uint32(10+i), one(linkA, 2, 1))
	}
	a, _ := o.Get(linkA)
	got := a.Descriptions()
	if len(got) > ringSize+1 {
		t.Fatalf("%d descriptions kept, at most %d", len(got), ringSize+1)
	}
	// The one that listed the prim stays first, though it has gone out of
	// the last few.
	if !got[0].Listed || got[0].Seq != 1 || got[0].Parent != 1 || got[0].Kind != DescFull {
		t.Errorf("first is %+v, want the listing one of packet 1", got[0])
	}
	if last := got[len(got)-1]; last.Seq != 29 || last.Listed {
		t.Errorf("last is %+v, want packet 29, not listing", last)
	}
	if got[1].Seq != 26 {
		t.Errorf("the ring starts at packet %d, want 26", got[1].Seq)
	}
}

func TestTerseUpdatesAreFoldedIntoOneEntry(t *testing.T) {
	t.Parallel()

	o := newObjects()
	packet(o, 1, one(linkRoot, 1, 0))
	for i := 0; i < 50; i++ {
		in := o.arrive(1, DescTerse, uint32(2+i), 1)
		if !o.movedAt(&msg.Terse{LocalID: 1}, nil, in, 0) {
			t.Fatal("a terse update for a prim held was not applied")
		}
	}
	v, _ := o.Get(linkRoot)
	got := v.Descriptions()
	if len(got) != 2 || got[1].Kind != DescTerse || got[1].Count != 50 || got[1].Seq != 51 {
		t.Errorf("descriptions %+v, want a full and one terse standing for 50", got)
	}
}

func TestDescriptionsGoWithTheObject(t *testing.T) {
	t.Parallel()

	o := newObjects()
	packet(o, 1, one(linkRoot, 1, 0), one(linkA, 2, 1))
	packet(o, 2, one(linkA, 2, 1))
	o.kill(1)
	if _, ok := o.Get(linkRoot); ok {
		t.Fatal("killed root still held")
	}
	// Described again, the object starts a new record: only the kill
	// that preceded it, and the new description.
	packet(o, 3, one(linkRoot, 1, 0))
	v, _ := o.Get(linkRoot)
	if got := kinds(v); !reflect.DeepEqual(got, []DescKind{DescKilled, DescFull}) {
		t.Errorf("kinds %v, want the kill and the new description only", got)
	}
	o.Flush()
	if len(o.notes) != 0 {
		t.Error("a flush kept notes")
	}
}

func TestACachedAndRequestedNoticeShowsAndMakesTheNextUpdateARefill(t *testing.T) {
	t.Parallel()

	o := newObjects()
	packet(o, 1, one(linkRoot, 1, 0))
	o.noted(DescCached, 7, 2, 3)
	o.noted(DescRequested, 0, 2)
	packet(o, 9, one(linkA, 2, 1))
	a, _ := o.Get(linkA)
	if got := kinds(a); !reflect.DeepEqual(got, []DescKind{DescCached, DescRequested, DescFull}) {
		t.Fatalf("kinds %v", got)
	}
	d := a.Descriptions()
	if !d[2].Refill || d[0].Seq != 7 || d[1].Refill {
		t.Errorf("descriptions %+v", d)
	}
	// A notice is spent by the update it preceded.
	packet(o, 10, one(linkA, 2, 1))
	a, _ = o.Get(linkA)
	if d := a.Descriptions(); d[len(d)-1].Refill {
		t.Error("the notice made a second update a refill")
	}
}

func TestNoticesForAnObjectHeldAreOnItsRingAtOnceAndOnlyOnce(t *testing.T) {
	t.Parallel()

	o := newObjects()
	packet(o, 1, one(linkRoot, 1, 0))
	o.noted(DescRequested, 0, 1)
	packet(o, 2, one(linkRoot, 1, 0))
	v, _ := o.Get(linkRoot)
	if got := kinds(v); !reflect.DeepEqual(got, []DescKind{DescFull, DescRequested, DescFull}) {
		t.Errorf("kinds %v", got)
	}
}

func TestTheNoticesAreBounded(t *testing.T) {
	t.Parallel()

	o := newObjects()
	locals := make([]uint32, noteCap+10)
	for i := range locals {
		locals[i] = uint32(i + 100)
	}
	o.noted(DescRequested, 0, locals...)
	if len(o.notes) > noteCap {
		t.Fatalf("%d notices kept, at most %d", len(o.notes), noteCap)
	}
	// Notices past the cap are not kept, and then every description is
	// taken for a refill: the set is not witnessed.
	packet(o, 1, one(linkRoot, 1, 0), one(linkA, 2, 1), one(linkB, 3, 1))
	expectKnown(t, o, []bool{false, false, false}, linkRoot, linkA, linkB)
}

func TestASetDescribedWholeInOneMessageIsKnown(t *testing.T) {
	t.Parallel()

	o := newObjects()
	packet(o, 1, one(linkRoot, 1, 0), one(linkB, 3, 1), one(linkA, 2, 1), one(linkC, 4, 1))
	expectNumbers(t, o, []int{1, 2, 3, 4}, linkRoot, linkB, linkA, linkC)
	expectKnown(t, o, []bool{true, true, true, true}, linkRoot, linkB, linkA, linkC)
	set := o.Linkset(1)
	if len(set) != 4 {
		t.Fatalf("Linkset has %d", len(set))
	}
}

func TestARootInAnEarlierMessageThanItsChildrenIsKnown(t *testing.T) {
	t.Parallel()

	o := newObjects()
	packet(o, 1, one(linkRoot, 1, 0))
	packet(o, 2, one(linkB, 3, 1), one(linkA, 2, 1))
	expectKnown(t, o, []bool{true, true, true}, linkRoot, linkB, linkA)
}

func TestASetOverSeveralPacketsInOrderIsKnown(t *testing.T) {
	t.Parallel()

	o := newObjects()
	packet(o, 100, one(linkRoot, 1, 0), one(linkA, 2, 1))
	expectKnown(t, o, []bool{true, true}, linkRoot, linkA)
	packet(o, 101, one(linkB, 3, 1), one(linkC, 4, 1))
	packet(o, 105, one(linkD, 5, 1))
	expectNumbers(t, o, []int{1, 2, 3, 4, 5}, linkRoot, linkA, linkB, linkC, linkD)
	expectKnown(t, o, []bool{true, true, true, true, true}, linkRoot, linkA, linkB, linkC, linkD)
	for _, v := range o.Linkset(1) {
		if !v.LinkKnown {
			t.Errorf("Linkset gives %s as unknown", v.ID)
		}
	}
}

// The shape caught live: the root and two children in one packet, the
// last child in the next, and the next one delivered first.  Ordered by
// arrival the last child would have been link 2 (as the old arrival rule
// read it); by the packets it is link 4.
func TestALaterPacketDeliveredFirstDoesNotTakeTheEarlierOnesPlaces(t *testing.T) {
	t.Parallel()

	o := newObjects()
	packet(o, 127, one(linkC, 4, 1))
	packet(o, 126, one(linkRoot, 1, 0), one(linkA, 2, 1), one(linkB, 3, 1))
	expectNumbers(t, o, []int{1, 2, 3, 4}, linkRoot, linkA, linkB, linkC)
	expectKnown(t, o, []bool{true, true, true, true}, linkRoot, linkA, linkB, linkC)
	if got := o.kids[1]; !reflect.DeepEqual(got, []msg.UUID{linkA, linkB, linkC}) {
		t.Errorf("children %v, want A, B, C", got)
	}
}

func TestTheCaughtCaseThroughTheHandlers(t *testing.T) {
	t.Parallel()

	a, _ := offlineSession(t)
	root := arriving(t,
		msg.ObjectUpdate_ObjectData{ID: 1, FullID: linkRoot, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 2, ParentID: 1, FullID: linkA, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 3, ParentID: 1, FullID: linkB, PCode: 9})
	last := arriving(t, msg.ObjectUpdate_ObjectData{ID: 4, ParentID: 1, FullID: linkC, PCode: 9})
	feedSequenced(t, a, []uint32{127, 126}, []msg.Message{last, root})
	store := a.Objects()
	expectNumbers(t, store, []int{1, 2, 3, 4}, linkRoot, linkA, linkB, linkC)
	expectKnown(t, store, []bool{true, true, true, true}, linkRoot, linkA, linkB, linkC)
}

func TestAChildSentToTheViewerAsAnAnswerToARequestMakesTheSetUnknown(t *testing.T) {
	t.Parallel()

	o := newObjects()
	packet(o, 1, one(linkRoot, 1, 0), one(linkA, 2, 1))
	o.noted(DescRequested, 0, 3)
	packet(o, 2, one(linkB, 3, 1))
	expectNumbers(t, o, []int{1, 2, 3}, linkRoot, linkA, linkB)
	expectKnown(t, o, []bool{false, false, false}, linkRoot, linkA, linkB)
}

func TestARootThatIsAnAnswerToARequestDoesNotMakeTheSetUnknown(t *testing.T) {
	t.Parallel()

	o := newObjects()
	o.noted(DescRequested, 0, 1)
	packet(o, 1, one(linkRoot, 1, 0), one(linkA, 2, 1), one(linkB, 3, 1))
	expectKnown(t, o, []bool{true, true, true}, linkRoot, linkA, linkB)
}

func TestACompressedChildIsKnownLikeAFullOne(t *testing.T) {
	t.Parallel()

	o := newObjects()
	packet(o, 1, one(linkRoot, 1, 0), one(linkA, 2, 1))
	parent := uint32(1)
	in := o.arrive(1, DescCompressed, 2, 1)
	o.compressedAt(&msg.Compressed{FullID: linkB, LocalID: 3, ParentID: &parent, PCode: 9}, msg.Vector3{}, 0, in, 0)
	expectKnown(t, o, []bool{true, true, true}, linkRoot, linkA, linkB)
	b, _ := o.Get(linkB)
	if d := b.Descriptions(); len(d) != 1 || d[0].Kind != DescCompressed || !d[0].Listed || d[0].Parent != 1 {
		t.Errorf("descriptions %+v", d)
	}
}

func TestTheOrderIsByBlockWithinAPacket(t *testing.T) {
	t.Parallel()

	o := newObjects()
	packet(o, 7, one(linkRoot, 1, 0))
	packet(o, 9, one(linkC, 4, 1), one(linkD, 5, 1))
	packet(o, 8, one(linkA, 2, 1), one(linkB, 3, 1))
	expectNumbers(t, o, []int{1, 2, 3, 4, 5}, linkRoot, linkA, linkB, linkC, linkD)
}

func TestTheSequenceWrapKeepsTheOrder(t *testing.T) {
	t.Parallel()

	// The sequence space is 24 bits: packets 0xfffffe, 0xffffff, then
	// 0 and 1 are in that order, whichever arrives first.
	last := uint32(seqSpace - 1)
	o := newObjects()
	packet(o, last-1, one(linkRoot, 1, 0), one(linkA, 2, 1))
	packet(o, 1, one(linkD, 5, 1))
	packet(o, 0, one(linkC, 4, 1))
	packet(o, last, one(linkB, 3, 1))
	expectNumbers(t, o, []int{1, 2, 3, 4, 5}, linkRoot, linkA, linkB, linkC, linkD)
	expectKnown(t, o, []bool{true, true, true, true, true}, linkRoot, linkA, linkB, linkC, linkD)

	for _, c := range []struct {
		a, b uint32
		want bool
	}{
		{1, 2, true}, {2, 1, false}, {5, 5, false},
		{last, 0, true}, {0, last, false}, {last - 3, 4, true},
	} {
		if got := seqBefore(c.a, c.b); got != c.want {
			t.Errorf("seqBefore(%d, %d) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestARootDescribedAfterItsChildrenIsKnownAndTheyAreInOrder(t *testing.T) {
	t.Parallel()

	// In one packet, the root last.
	o := newObjects()
	packet(o, 1, one(linkA, 2, 1), one(linkB, 3, 1), one(linkRoot, 1, 0))
	expectNumbers(t, o, []int{1, 2, 3}, linkRoot, linkA, linkB)
	expectKnown(t, o, []bool{true, true, true}, linkRoot, linkA, linkB)

	// Not known while the root has not come.
	o = newObjects()
	packet(o, 1, one(linkA, 2, 1))
	packet(o, 3, one(linkB, 3, 1))
	expectKnown(t, o, []bool{false, false}, linkA, linkB)
	packet(o, 2, one(linkRoot, 1, 0))
	expectKnown(t, o, []bool{true, true, true}, linkRoot, linkA, linkB)
}

func TestALiveLinkStaysAtTheFrontAmongSequencedChildren(t *testing.T) {
	t.Parallel()

	o := newObjects()
	packet(o, 1, one(linkRoot, 1, 0), one(linkA, 2, 1))
	packet(o, 5, one(linkB, 3, 0))
	packet(o, 6, one(linkB, 3, 1))
	// A child of the first burst that turns up late goes behind the
	// sequenced ones it follows, not in front of the live link.
	packet(o, 2, one(linkC, 4, 1))
	expectNumbers(t, o, []int{1, 2, 3, 4}, linkRoot, linkB, linkA, linkC)
	expectKnown(t, o, []bool{true, true, true, true}, linkRoot, linkB, linkA, linkC)
}

func TestAChildFilledInAfterACacheMissIsNotKnown(t *testing.T) {
	t.Parallel()

	o := newObjects()
	// The region says the session holds the children, the session asks,
	// and the answer comes as an ordinary update in whatever order.
	o.noted(DescCached, 3, 2, 3)
	o.noted(DescRequested, 0, 2, 3)
	packet(o, 4, one(linkRoot, 1, 0), one(linkA, 2, 1), one(linkB, 3, 1))
	expectNumbers(t, o, []int{1, 2, 3}, linkRoot, linkA, linkB)
	expectKnown(t, o, []bool{false, false, false}, linkRoot, linkA, linkB)
}

func TestAnAnswerToARequestStaysAnAnswerOnlyAWhile(t *testing.T) {
	t.Parallel()

	o := newObjects()
	now := time.Unix(1_000_000, 0)
	o.now = func() time.Time { return now }
	o.noted(DescRequested, 0, 2)
	now = now.Add(noteTTL + time.Second)
	packet(o, 4, one(linkRoot, 1, 0), one(linkA, 2, 1))
	expectKnown(t, o, []bool{true, true}, linkRoot, linkA)
}

func TestASetIsKnownAgainWhenItIsTakenAndDescribedWhole(t *testing.T) {
	t.Parallel()

	o := newObjects()
	o.noted(DescRequested, 0, 3)
	packet(o, 1, one(linkRoot, 1, 0), one(linkA, 2, 1))
	packet(o, 2, one(linkB, 3, 1))
	expectKnown(t, o, []bool{false, false, false}, linkRoot, linkA, linkB)

	o.kill(1)
	packet(o, 3, one(linkRoot2, 1, 0), one(linkC, 2, 1), one(linkD, 3, 1))
	expectKnown(t, o, []bool{true, true, true}, linkRoot2, linkC, linkD)
}

func TestAFlushForgetsWhatWasWitnessed(t *testing.T) {
	t.Parallel()

	o := newObjects()
	o.noted(DescRequested, 0, 3)
	packet(o, 1, one(linkRoot, 1, 0), one(linkA, 2, 1))
	packet(o, 2, one(linkB, 3, 1))
	expectKnown(t, o, []bool{false, false, false}, linkRoot, linkA, linkB)
	o.Flush()
	packet(o, 3, one(linkRoot, 1, 0), one(linkA, 2, 1), one(linkB, 3, 1))
	expectKnown(t, o, []bool{true, true, true}, linkRoot, linkA, linkB)
}

func TestALiveLinkOfOneKeepsASetKnownAndAnUnknownOneUnknown(t *testing.T) {
	t.Parallel()

	o := newObjects()
	packet(o, 1, one(linkRoot, 1, 0), one(linkA, 2, 1))
	packet(o, 2, one(linkB, 3, 0))
	packet(o, 3, one(linkB, 3, 1))
	expectNumbers(t, o, []int{1, 2, 3}, linkRoot, linkB, linkA)
	expectKnown(t, o, []bool{true, true, true}, linkRoot, linkB, linkA)

	// A set with a child that answered a request stays unknown.
	o = newObjects()
	packet(o, 1, one(linkRoot, 1, 0), one(linkA, 2, 1))
	o.noted(DescRequested, 0, 4)
	packet(o, 2, one(linkC, 4, 1))
	packet(o, 3, one(linkB, 3, 0))
	packet(o, 4, one(linkB, 3, 1))
	expectKnown(t, o, []bool{false, false, false, false}, linkRoot, linkB, linkA, linkC)
}

func TestALiveLinkIntoAParentNotHereIsNotKnown(t *testing.T) {
	t.Parallel()

	o := newObjects()
	packet(o, 1, one(linkA, 2, 0))
	packet(o, 2, one(linkA, 2, 9))
	packet(o, 3, one(linkB, 3, 9))
	expectKnown(t, o, []bool{false, false}, linkA, linkB)
}

func TestAWornSetIsKnownWhenTheRootWasDescribedAndNoChildAnAnswer(t *testing.T) {
	t.Parallel()

	avatar := &msg.ObjectUpdate_ObjectData{ID: 10, FullID: sitterOne, PCode: pcodeAvatar}
	o := newObjects()
	packet(o, 1, avatar)
	packet(o, 2, one(linkRoot, 1, 10), one(linkA, 2, 1), one(linkB, 3, 1))
	expectNumbers(t, o, []int{1, 2, 3}, linkRoot, linkA, linkB)
	expectKnown(t, o, []bool{true, true, true}, linkRoot, linkA, linkB)

	// Consecutive packets at a login.
	o = newObjects()
	packet(o, 1, avatar)
	packet(o, 59, one(linkRoot, 1, 10), one(linkA, 2, 1))
	packet(o, 60, one(linkB, 3, 1))
	expectNumbers(t, o, []int{1, 2, 3}, linkRoot, linkA, linkB)
	expectKnown(t, o, []bool{true, true, true}, linkRoot, linkA, linkB)

	// The worn root after its children.
	o = newObjects()
	packet(o, 1, avatar)
	packet(o, 2, one(linkA, 2, 1), one(linkB, 3, 1))
	packet(o, 3, one(linkRoot, 1, 10))
	expectNumbers(t, o, []int{1, 2, 3}, linkRoot, linkA, linkB)
	expectKnown(t, o, []bool{true, true, true}, linkRoot, linkA, linkB)

	// A child that answered a request.
	o = newObjects()
	packet(o, 1, avatar)
	o.noted(DescRequested, 0, 3)
	packet(o, 2, one(linkRoot, 1, 10), one(linkA, 2, 1), one(linkB, 3, 1))
	expectKnown(t, o, []bool{false, false, false}, linkRoot, linkA, linkB)

	// Several worn sets do not take each other's place.
	o = newObjects()
	packet(o, 1, avatar)
	packet(o, 2, one(linkRoot, 1, 10), one(linkA, 2, 1), one(linkRoot2, 4, 10), one(linkB, 3, 4))
	expectKnown(t, o, []bool{true, true, true, true}, linkRoot, linkA, linkRoot2, linkB)
}

func TestTwoAgentsHearingOnePacketKnowTheSet(t *testing.T) {
	t.Parallel()

	one1, _ := offlineSession(t)
	two, _ := offlineSession(t)
	shared := newObjects()
	one1.objects.Store(shared)
	two.objects.Store(shared)
	set := arriving(t,
		msg.ObjectUpdate_ObjectData{ID: 1, FullID: linkRoot, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 3, ParentID: 1, FullID: linkB, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 2, ParentID: 1, FullID: linkA, PCode: 9},
	)
	feed(t, one1, set)
	feed(t, two, set)
	expectKnown(t, shared, []bool{true, true, true}, linkRoot, linkB, linkA)
}

func TestASetOverTwoPacketsThroughTheHandlerIsKnownAndSaysHow(t *testing.T) {
	t.Parallel()

	a, _ := offlineSession(t)
	feed(t, a,
		arriving(t,
			msg.ObjectUpdate_ObjectData{ID: 1, FullID: linkRoot, PCode: 9},
			msg.ObjectUpdate_ObjectData{ID: 2, ParentID: 1, FullID: linkA, PCode: 9}),
		arriving(t,
			msg.ObjectUpdate_ObjectData{ID: 3, ParentID: 1, FullID: linkB, PCode: 9}),
	)
	store := a.Objects()
	expectKnown(t, store, []bool{true, true, true}, linkRoot, linkA, linkB)
	b, _ := store.Get(linkB)
	d := b.Descriptions()
	if len(d) != 1 || d[0].Kind != DescFull || !d[0].Listed || d[0].Parent != 1 || d[0].Seq == 0 || d[0].Blocks != 1 {
		t.Errorf("descriptions of B: %+v", d)
	}
	root, _ := store.Get(linkRoot)
	ra := root.Descriptions()
	av, _ := store.Get(linkA)
	aa := av.Descriptions()
	if ra[0].Message != aa[0].Message || ra[0].Message == d[0].Message || aa[0].Block != 1 || aa[0].Blocks != 2 {
		t.Errorf("messages: root %+v, A %+v, B %+v", ra, aa, d)
	}
}

func TestACachedUpdateThenTheRequestedSetIsRecordedThroughTheHandlers(t *testing.T) {
	t.Parallel()

	a, sent := offlineSession(t)
	_ = sent
	feed(t, a, &msg.ObjectUpdateCached{ObjectData: []msg.ObjectUpdateCached_ObjectData{{ID: 2, CRC: 5}, {ID: 3, CRC: 6}}})
	// The handler asks on its own goroutine; the notice that makes is
	// waited for.
	waitFor(t, "the request's notice", func() bool {
		o := a.Objects()
		o.mu.RLock()
		defer o.mu.RUnlock()
		n := o.notes[3]
		return n != nil && n.n >= 2
	})
	feed(t, a, arriving(t,
		msg.ObjectUpdate_ObjectData{ID: 1, FullID: linkRoot, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 2, ParentID: 1, FullID: linkA, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 3, ParentID: 1, FullID: linkB, PCode: 9}))
	store := a.Objects()
	v, _ := store.Get(linkA)
	if got := kinds(v); !reflect.DeepEqual(got, []DescKind{DescCached, DescRequested, DescFull}) {
		t.Errorf("kinds %v", got)
	}
	expectKnown(t, store, []bool{false, false, false}, linkRoot, linkA, linkB)
}

// feedSequenced delivers the messages in the order given, each under the
// sequence number at the same place in seqs.
func feedSequenced(t *testing.T, a *Agent, seqs []uint32, ms []msg.Message) {
	t.Helper()
	ch := make(chan *msg.Packet, len(ms))
	for i, m := range ms {
		ch <- &msg.Packet{
			At:      time.Now(),
			Header:  msg.Header{Sequence: seqs[i]},
			ID:      msg.IDOf(m),
			Message: onTheWire(t, m),
		}
	}
	close(ch)
	if err := a.Disp.Run(context.Background(), ch); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
}

// Two agents share one store and each circuit numbers its packets by
// itself.  The second agent's packet, with the later children and a
// sequence number far below the first's, arrives first: comparing the two
// numbers means nothing, so the set must read unknown and not known in a
// misordered state.
func TestChildrenListedFromTwoCircuitsAreUnknown(t *testing.T) {
	t.Parallel()

	o := newObjects()
	packetOn(o, 1, 120, one(linkRoot, 1, 0), one(linkA, 2, 1))
	packetOn(o, 2, 40, one(linkC, 4, 1), one(linkD, 5, 1))
	packetOn(o, 1, 121, one(linkB, 3, 1))
	expectKnown(t, o, []bool{false, false, false, false, false}, linkRoot, linkA, linkC, linkD, linkB)
	for _, v := range o.Linkset(1) {
		if v.LinkKnown {
			t.Errorf("Linkset gives %s as known", v.ID)
		}
	}
}

func TestChildrenAllListedFromOneCircuitStayKnownWhateverTheCircuit(t *testing.T) {
	t.Parallel()

	o := newObjects()
	packetOn(o, 2, 40, one(linkRoot, 1, 0), one(linkA, 2, 1))
	packetOn(o, 2, 42, one(linkC, 4, 1))
	packetOn(o, 2, 41, one(linkB, 3, 1))
	expectNumbers(t, o, []int{1, 2, 3, 4}, linkRoot, linkA, linkB, linkC)
	expectKnown(t, o, []bool{true, true, true, true}, linkRoot, linkA, linkB, linkC)
	// Another circuit hearing the same children lists nothing new.
	packetOn(o, 3, 7, one(linkRoot, 1, 0), one(linkA, 2, 1), one(linkB, 3, 1), one(linkC, 4, 1))
	expectKnown(t, o, []bool{true, true, true, true}, linkRoot, linkA, linkB, linkC)
}

// Through the handlers: two agents on one store, each with its own
// sequence numbers.
func TestTwoAgentsWithTheirOwnSequencesAreUnknownThroughTheHandlers(t *testing.T) {
	t.Parallel()

	one1, _ := offlineSession(t)
	two, _ := offlineSession(t)
	shared := newObjects()
	one1.objects.Store(shared)
	two.objects.Store(shared)
	first := arriving(t,
		msg.ObjectUpdate_ObjectData{ID: 1, FullID: linkRoot, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 2, ParentID: 1, FullID: linkA, PCode: 9})
	later := arriving(t,
		msg.ObjectUpdate_ObjectData{ID: 3, ParentID: 1, FullID: linkB, PCode: 9},
		msg.ObjectUpdate_ObjectData{ID: 4, ParentID: 1, FullID: linkC, PCode: 9})
	feedSequenced(t, two, []uint32{40}, []msg.Message{later})
	feedSequenced(t, one1, []uint32{120}, []msg.Message{first})
	expectKnown(t, shared, []bool{false, false, false, false}, linkRoot, linkA, linkB, linkC)
}

func TestThreeCircuitsDescribingASetInAgreeingOrderKnowIt(t *testing.T) {
	t.Parallel()

	o := newObjects()
	// Each circuit numbers its packets by itself, and the packets reach
	// the store interleaved.
	packetOn(o, 1, 100, one(linkRoot, 1, 0), one(linkA, 2, 1))
	packetOn(o, 2, 40, one(linkRoot, 1, 0), one(linkA, 2, 1))
	packetOn(o, 3, 9000, one(linkRoot, 1, 0), one(linkA, 2, 1), one(linkB, 3, 1))
	packetOn(o, 2, 41, one(linkB, 3, 1), one(linkC, 4, 1))
	packetOn(o, 1, 101, one(linkB, 3, 1))
	packetOn(o, 1, 102, one(linkC, 4, 1))
	packetOn(o, 3, 9001, one(linkC, 4, 1))
	expectNumbers(t, o, []int{1, 2, 3, 4}, linkRoot, linkA, linkB, linkC)
	expectKnown(t, o, []bool{true, true, true, true}, linkRoot, linkA, linkB, linkC)
}

func TestASetOnlyOneCircuitSawInFullIsKnownByThatCircuit(t *testing.T) {
	t.Parallel()

	o := newObjects()
	packetOn(o, 3, 700, one(linkC, 4, 1))
	packetOn(o, 1, 100, one(linkRoot, 1, 0), one(linkA, 2, 1), one(linkB, 3, 1))
	packetOn(o, 2, 5, one(linkB, 3, 1), one(linkA, 2, 1))
	packetOn(o, 1, 101, one(linkC, 4, 1))
	// Circuit 2 saw two of three, in an order circuit 1 does not give,
	// and circuit 3 one: neither is complete, so neither is compared.
	expectNumbers(t, o, []int{1, 2, 3, 4}, linkRoot, linkA, linkB, linkC)
	expectKnown(t, o, []bool{true, true, true, true}, linkRoot, linkA, linkB, linkC)
}

func TestTwoCircuitsWithCompleteOrderingsThatDisagreeAreUnknownAndSayWhich(t *testing.T) {
	t.Parallel()

	o := newObjects()
	packetOn(o, 1, 10, one(linkRoot, 1, 0), one(linkA, 2, 1), one(linkB, 3, 1), one(linkC, 4, 1))
	packetOn(o, 2, 40, one(linkC, 4, 1), one(linkA, 2, 1), one(linkB, 3, 1))
	expectKnown(t, o, []bool{false, false, false, false}, linkRoot, linkA, linkB, linkC)
	o.mu.RLock()
	note := o.noteLocked(1)
	o.mu.RUnlock()
	if !strings.Contains(note, "circuits 1 and 2") || !strings.Contains(note, "disagree") {
		t.Errorf("note %q, want the two circuits that disagree", note)
	}
	for _, v := range o.Linkset(1) {
		if v.LinkKnown || (v.ID != linkRoot && v.LinkNote == "") {
			t.Errorf("Linkset gives %s known %v, note %q", v.ID, v.LinkKnown, v.LinkNote)
		}
	}
}

// Agent B's later children arrive before agent A's packets.  Whichever
// circuit has described the whole set orders it, by its own numbers, and
// the numbers of the two are never compared.
func TestALaterCircuitsLaterChildrenArrivingFirstAreNeverMixed(t *testing.T) {
	t.Parallel()

	// B's packet 41 (the last child) first; A's 120 and 121 after; B's
	// own 40 last.  Both end complete and agree.
	o := newObjects()
	packetOn(o, 2, 41, one(linkC, 4, 1))
	packetOn(o, 1, 120, one(linkRoot, 1, 0), one(linkA, 2, 1), one(linkB, 3, 1))
	expectKnown(t, o, []bool{false, false, false}, linkRoot, linkA, linkB)
	packetOn(o, 1, 121, one(linkC, 4, 1))
	expectNumbers(t, o, []int{1, 2, 3, 4}, linkRoot, linkA, linkB, linkC)
	expectKnown(t, o, []bool{true, true, true, true}, linkRoot, linkA, linkB, linkC)
	packetOn(o, 2, 40, one(linkRoot, 1, 0), one(linkA, 2, 1), one(linkB, 3, 1))
	expectNumbers(t, o, []int{1, 2, 3, 4}, linkRoot, linkA, linkB, linkC)
	expectKnown(t, o, []bool{true, true, true, true}, linkRoot, linkA, linkB, linkC)

	// B alone has the whole set; A saw only the last child, with a
	// sequence number that would put it first if the two were compared.
	o = newObjects()
	packetOn(o, 1, 3, one(linkC, 4, 1))
	packetOn(o, 2, 900, one(linkRoot, 1, 0), one(linkA, 2, 1), one(linkB, 3, 1))
	packetOn(o, 2, 901, one(linkC, 4, 1))
	expectNumbers(t, o, []int{1, 2, 3, 4}, linkRoot, linkA, linkB, linkC)
	expectKnown(t, o, []bool{true, true, true, true}, linkRoot, linkA, linkB, linkC)
}

func TestACircuitThatLeavesTakesItsKeysAndItsCompleteOrderingWithIt(t *testing.T) {
	t.Parallel()

	o := newObjects()
	packetOn(o, 1, 100, one(linkRoot, 1, 0), one(linkA, 2, 1), one(linkB, 3, 1))
	packetOn(o, 2, 7, one(linkB, 3, 1))
	expectKnown(t, o, []bool{true, true, true}, linkRoot, linkA, linkB)
	o.Leave("agent one", 1)
	expectKnown(t, o, []bool{false, false, false}, linkRoot, linkA, linkB)
	for _, id := range []msg.UUID{linkRoot, linkA, linkB} {
		o.mu.RLock()
		for _, k := range o.byID[id].keys {
			if k.circ == 1 {
				t.Errorf("%s still holds a key of the circuit that left", id)
			}
		}
		o.mu.RUnlock()
	}
}
