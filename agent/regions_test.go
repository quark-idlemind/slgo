package agent

// One store per region, shared by the agents in it.
//
// What matters here is not that sharing works but that it does not lose
// things: two agents keeping one store must not evict each other's
// view, and the store must go when the last of them leaves, since
// nothing tells an empty region's cache that an object was destroyed.

import (
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

var (
	regionOne = msg.MustParseUUID("127d7e57-7e57-c0de-dda3-423b673dda89")
	regionTwo = msg.MustParseUUID("1bf77e57-7e57-c0de-ae21-4da7fb671a0e")
)

// TestOneStorePerRegion: two agents in a region get the same store, and
// an agent in another region does not.
func TestOneStorePerRegion(t *testing.T) {
	c := NewCache()
	a := c.Attach(regionOne)
	b := c.Attach(regionOne)
	if a != b {
		t.Error("two agents in one region were given different stores")
	}
	if elsewhere := c.Attach(regionTwo); elsewhere == a {
		t.Error("two regions were given the same store")
	}
	if c.Regions() != 2 {
		t.Errorf("%d stores held, want 2", c.Regions())
	}
}

// TestTheLastOneOutDropsTheStore.
//
// A region goes on changing with nobody in it, and the only notice of
// an object being destroyed is a KillObject to the agents present.  A
// store kept past the last agent would fill with things that no longer
// exist and would look exactly like a store that was right.
func TestTheLastOneOutDropsTheStore(t *testing.T) {
	c := NewCache()
	first := c.Attach(regionOne)
	c.Attach(regionOne)

	c.Detach(regionOne)
	if c.Regions() != 1 {
		t.Fatal("the store went while an agent was still in the region")
	}
	if again := c.Attach(regionOne); again != first {
		t.Error("the agent still there had its store swapped underneath it")
	}
	c.Detach(regionOne)
	c.Detach(regionOne)
	if c.Regions() != 0 {
		t.Errorf("%d stores held after everyone left, want 0", c.Regions())
	}

	// And the region afterwards is a fresh store rather than the old
	// one with whatever it had accumulated.
	if after := c.Attach(regionOne); after == first {
		t.Error("a region nobody was in kept its old store")
	}
}

// TestAnUnidentifiedRegionIsNotShared: a region with no id cannot be
// told from the next one, and sharing on that basis would merge two
// regions into one.
func TestAnUnidentifiedRegionIsNotShared(t *testing.T) {
	c := NewCache()
	a := c.Attach(msg.UUID{})
	b := c.Attach(msg.UUID{})
	if a == b {
		t.Error("two unidentified regions were given the same store")
	}
	if c.Regions() != 0 {
		t.Errorf("an unidentified region was cached: %d held", c.Regions())
	}
	c.Detach(msg.UUID{}) // must not panic or disturb the cache
}

// TestEveryViewpointGetsASayInWhatIsKept.
//
// The trim used to answer to one camera.  Pointed at a shared store,
// one avatar walking away would throw out what another was standing in
// front of.
func TestEveryViewpointGetsASayInWhatIsKept(t *testing.T) {
	o := newObjects()
	here, far := msg.Vector3{X: 0, Y: 0, Z: 0}, msg.Vector3{X: 500, Y: 500, Z: 0}
	o.update(&msg.ObjectUpdate_ObjectData{
		ID: 1, FullID: msg.UUID{15: 1}, ObjectData: placement(far, msg.Quaternion{}),
	}, far, 64)

	// The second avatar is standing next to it; the first is not.
	o.Watch("b", far, 64)
	if n := o.Trim(here, 64); n != 0 {
		t.Errorf("trimmed %d objects that another avatar can see", n)
	}
	if o.Count() != 1 {
		t.Fatalf("%d objects kept, want 1", o.Count())
	}

	// Once that avatar goes, nothing is keeping it.
	o.Unwatch("b")
	if n := o.Trim(here, 64); n != 1 {
		t.Errorf("trimmed %d after the only avatar who could see it left, want 1", n)
	}
}

// TestAnUpdateIsKeptForWhoeverCanSeeIt: the range check on the way in
// has the same problem as the trim -- the agent that HEARD the update
// need not be the one it is near.
func TestAnUpdateIsKeptForWhoeverCanSeeIt(t *testing.T) {
	o := newObjects()
	here, far := msg.Vector3{}, msg.Vector3{X: 500, Y: 500}
	o.Watch("b", far, 64)

	// Heard by the agent at the origin, about something 700m away.
	o.update(&msg.ObjectUpdate_ObjectData{
		ID: 1, FullID: msg.UUID{15: 1}, ObjectData: placement(far, msg.Quaternion{}),
	}, here, 64)
	if o.Count() != 1 {
		t.Error("an update was dropped although another avatar was standing next to it")
	}
}

// TestJoiningAStoreKeepsWhatWasHeardFirst: an object can be described
// before the handshake says which region this is, and nothing will
// describe it again.
func TestJoiningAStoreKeepsWhatWasHeardFirst(t *testing.T) {
	private := newObjects()
	private.update(&msg.ObjectUpdate_ObjectData{
		ID: 7, FullID: msg.UUID{15: 7}, ObjectData: placement(msg.Vector3{X: 1}, msg.Quaternion{}),
	}, msg.Vector3{}, 0)

	shared := newObjects()
	if n := shared.absorb(private); n != 1 {
		t.Fatalf("absorbed %d, want 1", n)
	}
	if _, ok := shared.Get(msg.UUID{15: 7}); !ok {
		t.Error("what was heard before the region named itself was lost")
	}

	// What the store already has is what the agents already there
	// heard, and is not worth replacing with a newcomer's copy.
	older := newObjects()
	older.update(&msg.ObjectUpdate_ObjectData{
		ID: 7, FullID: msg.UUID{15: 7}, Scale: msg.Vector3{X: 9},
		ObjectData: placement(msg.Vector3{X: 1}, msg.Quaternion{}),
	}, msg.Vector3{}, 0)
	if n := older.absorb(shared); n != 0 {
		t.Errorf("absorbed %d objects it already had", n)
	}
	if v, _ := older.Get(msg.UUID{15: 7}); v.Scale.X != 9 {
		t.Error("a newcomer's copy overwrote what was already known")
	}
}

// TestANameDoesNotConjureAnObject.
//
// A name arrives because something asked, and an answer can outlive its
// object: a reply about something already trimmed would otherwise make
// an entry with a name and nothing else -- no position, no shape, no
// parent -- which lists as a root prim at the origin and never goes
// away, since nothing will describe it again.  Measured on a live
// region before this: eight of sixty-six objects were exactly that.
func TestANameDoesNotConjureAnObject(t *testing.T) {
	o := newObjects()
	gone := msg.UUID{15: 3}

	o.named(gone, "LH Med Fire Grate", msg.UUID{15: 9})
	if _, ok := o.Get(gone); ok {
		t.Error("a name for something nobody has described made an object out of nothing")
	}

	// And a name for something that IS here still lands.
	o.update(&msg.ObjectUpdate_ObjectData{
		ID: 3, FullID: gone, PCode: 9,
		ObjectData: placement(msg.Vector3{X: 5}, msg.Quaternion{}),
	}, msg.Vector3{}, 0)
	o.named(gone, "LH Med Fire Grate", msg.UUID{15: 9})
	if v, ok := o.Get(gone); !ok || v.Name != "LH Med Fire Grate" {
		t.Errorf("naming something that is here gave %+v", v)
	}
}

// TestAWornPrimIsJudgedByItsWearer.
//
// An attachment's position is an offset from the avatar, and a prim of
// a linked attachment is an offset from that attachment's root -- whose
// own parent is the avatar rather than nothing.  Looking only for a
// parent that has no parent found neither, so every prim of a linked
// hud was an orphan and went a minute later.
func TestAWornPrimIsJudgedByItsWearer(t *testing.T) {
	o := newObjects()
	here := msg.Vector3{X: 128, Y: 128, Z: 30}

	// The avatar, standing here.
	o.update(&msg.ObjectUpdate_ObjectData{
		ID: 1, FullID: msg.UUID{15: 1}, PCode: 47,
		ObjectData: placement(here, msg.Quaternion{}),
	}, here, 128)
	// Its attachment, and a prim of that attachment.  Both carry
	// offsets, which are nowhere near the camera as coordinates.
	o.update(&msg.ObjectUpdate_ObjectData{
		ID: 2, FullID: msg.UUID{15: 2}, PCode: 9, ParentID: 1,
		ObjectData: placement(msg.Vector3{X: 0.2}, msg.Quaternion{}),
	}, here, 128)
	o.update(&msg.ObjectUpdate_ObjectData{
		ID: 3, FullID: msg.UUID{15: 3}, PCode: 9, ParentID: 2,
		ObjectData: placement(msg.Vector3{X: 0.1}, msg.Quaternion{}),
	}, here, 128)

	if n := o.Trim(here, 128); n != 0 {
		t.Errorf("trimmed %d of an avatar standing in front of the camera", n)
	}
	if o.Count() != 3 {
		t.Fatalf("%d objects kept, want the avatar and both prims", o.Count())
	}

	// And when the avatar is far away its prims go, because they are
	// where their wearer is standing rather than at their offsets.
	//
	// The person stays.  A region names each avatar once and a standing
	// one says nothing afterwards, so an avatar dropped for distance is
	// dropped for good -- see pcodeAvatar.
	far := msg.Vector3{X: 900, Y: 900}
	if n := o.Trim(far, 128); n != 2 {
		t.Errorf("trimmed %d when the wearer walked off, want the two prims", n)
	}
	if o.Count() != 1 {
		t.Errorf("%d kept, want the avatar", o.Count())
	}
}

// TestAnOrphanTheRegionKeepsMentioningIsKept.
//
// The grace period is measured from the last word about a prim, not the
// first.  A region goes on describing prims whose roots it never
// describes to us; those updates cannot be judged for distance, so they
// are taken in -- and dropping them on a timer only means taking them
// straight back.  Measured on a live region: one prim was deleted and
// re-created every minute for hours, losing its name each time and
// costing a name lookup to get it back.
func TestAnOrphanTheRegionKeepsMentioningIsKept(t *testing.T) {
	o := newObjects()
	here := msg.Vector3{X: 128, Y: 128, Z: 30}
	orphan := msg.UUID{15: 5}

	describe := func() {
		o.update(&msg.ObjectUpdate_ObjectData{
			ID: 5, FullID: orphan, PCode: 9, ParentID: 4242,
			ObjectData: placement(msg.Vector3{X: 1}, msg.Quaternion{}),
		}, here, 128)
	}
	describe()
	o.named(orphan, "HearthEmbers", msg.UUID{15: 9})

	// Long past the grace period by the clock that used to be used.
	o.mu.Lock()
	o.byID[orphan].First = time.Now().Add(-10 * orphanGrace)
	o.mu.Unlock()

	describe() // and the region mentions it again
	if n := o.Trim(here, 128); n != 0 {
		t.Errorf("trimmed %d that the region is still describing", n)
	}
	if v, ok := o.Get(orphan); !ok || v.Name != "HearthEmbers" {
		t.Error("a prim the region keeps describing was dropped, name and all")
	}

	// Once it stops being mentioned, it goes.
	o.mu.Lock()
	o.byID[orphan].Last = time.Now().Add(-2 * orphanGrace)
	o.mu.Unlock()
	if n := o.Trim(here, 128); n != 1 {
		t.Errorf("trimmed %d after the region went quiet about it, want 1", n)
	}
}

// TestWhatASeatedPersonWearsOutlastsAnUndescribedSeat.
//
// A seated avatar's parent is its seat.  When nothing here had
// described the seat, every attachment the avatar wore walked up
// through the avatar into nothing, counted as an orphan, and went once
// the region had been quiet about it for a minute -- which for
// something worn is a minute after it went on.  The avatar itself is
// never dropped, so neither is what it is wearing.
func TestWhatASeatedPersonWearsOutlastsAnUndescribedSeat(t *testing.T) {
	o := newObjects()
	here := msg.Vector3{X: 128, Y: 128, Z: 30}

	// Sitting on local 900, which nothing has described.
	o.update(&msg.ObjectUpdate_ObjectData{
		ID: 1, FullID: msg.UUID{15: 1}, PCode: 47, ParentID: 900,
		ObjectData: placement(msg.Vector3{Z: 0.6}, msg.Quaternion{}),
	}, here, 128)
	o.update(&msg.ObjectUpdate_ObjectData{
		ID: 2, FullID: msg.UUID{15: 2}, PCode: 9, ParentID: 1,
		ObjectData: placement(msg.Vector3{X: 0.2}, msg.Quaternion{}),
	}, here, 128)
	o.update(&msg.ObjectUpdate_ObjectData{
		ID: 3, FullID: msg.UUID{15: 3}, PCode: 9, ParentID: 2,
		ObjectData: placement(msg.Vector3{X: 0.1}, msg.Quaternion{}),
	}, here, 128)

	// Long quiet, as anything worn is.
	o.mu.Lock()
	for _, v := range o.byID {
		v.Last = time.Now().Add(-10 * orphanGrace)
	}
	o.mu.Unlock()

	if n := o.Trim(here, 128); n != 0 {
		t.Errorf("trimmed %d from a person sitting on something undescribed", n)
	}
	if o.Count() != 3 {
		t.Errorf("%d kept, want the avatar and both prims", o.Count())
	}

	// A prim with no person above it is still an orphan, and still goes.
	o.update(&msg.ObjectUpdate_ObjectData{
		ID: 4, FullID: msg.UUID{15: 4}, PCode: 9, ParentID: 901,
		ObjectData: placement(msg.Vector3{X: 1}, msg.Quaternion{}),
	}, here, 128)
	o.mu.Lock()
	o.byID[msg.UUID{15: 4}].Last = time.Now().Add(-2 * orphanGrace)
	o.mu.Unlock()
	if n := o.Trim(here, 128); n != 1 {
		t.Errorf("trimmed %d, want the one orphan that is on nobody", n)
	}
}

// TestANewcomerHasASayFromTheMomentItArrives.
//
// An agent used to register where it was looking only on its own trim
// tick.  Joining a store the others were already trimming, it had no
// say until then, and one of them trimming first -- from a camera far
// away -- threw out everything around the newcomer for good.  Measured
// on a live region: the last of four avatars to log in, the other three
// high above it, lost every attachment the simulator had put back on.
func TestANewcomerHasASayFromTheMomentItArrives(t *testing.T) {
	cache := NewCache()
	region := msg.UUID{15: 0x42}
	aloft, ground := msg.Vector3{X: 88, Y: 172, Z: 4004}, msg.Vector3{X: 90, Y: 169, Z: 42}

	above := &Agent{regions: cache, Account: &Account{SessionID: msg.UUID{15: 1}}}
	above.enterRegion(region)
	above.SetLook(defaultLook(aloft))

	// The newcomer arrives on the ground and looks from where it is --
	// and has not yet reached its first trim tick.
	newcomer := &Agent{regions: cache, Account: &Account{SessionID: msg.UUID{15: 2}}}
	newcomer.enterRegion(region)
	newcomer.SetLook(defaultLook(ground))

	store := newcomer.Objects()
	if store != above.Objects() {
		t.Fatal("the two agents are not sharing one store")
	}
	store.update(&msg.ObjectUpdate_ObjectData{
		ID: 7, FullID: msg.UUID{15: 7}, ObjectData: placement(ground, msg.Quaternion{}),
	}, ground, 128)

	// The one above trims first.
	l := above.Look()
	if n := store.Trim(l.Center, l.Far); n != 0 {
		t.Errorf("trimmed %d objects in front of an agent that had just arrived", n)
	}

	// And a camera that moves takes its say with it.
	newcomer.setCenter(aloft)
	if n := store.Trim(l.Center, l.Far); n != 1 {
		t.Errorf("trimmed %d once nobody was looking at it, want 1", n)
	}
}
