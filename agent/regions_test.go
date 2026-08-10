package agent

// One store per region, shared by the agents in it.
//
// What matters here is not that sharing works but that it does not lose
// things: two agents keeping one store must not evict each other's
// view, and the store must go when the last of them leaves, since
// nothing tells an empty region's cache that an object was destroyed.

import (
	"testing"

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
