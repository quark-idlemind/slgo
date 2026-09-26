package agent

// One object store per region, shared by every agent standing in it.
//
// What a region says about its objects is true for everybody there.
// The ids, the shapes, the appearances, the names -- none of it depends
// on which avatar was told, and the local ids that most messages use
// are the region's own numbering rather than a per-agent one.  So three
// avatars in one region keeping three caches is three chances to give a
// different answer to the same question, three copies of the same few
// thousand objects, and three separate requests for every object's
// name.
//
// Sharing also widens what is known.  A region describes what is near
// each avatar's camera; avatars in different corners are told about
// different things, and the union is more than any one of them heard.
//
// # What is NOT shared
//
// Anything that means "mine".  An attachment is a region object like
// any other and belongs in here, but which avatar is wearing it is
// answered by its parent, not by which store it turned up in -- see
// Session.WornObjects, which had to start filtering when this arrived.
//
// Per-agent permission bits would be the other kind, if they were ever
// kept: an ObjectUpdate's UpdateFlags say what THIS avatar may do with
// the object, and they are deliberately not stored here.

import (
	"sync"

	"github.com/quark-idlemind/slgo/msg"
)

// Cache hands out the store for a region.
//
// It counts holders rather than keeping stores forever.  A region with
// nobody in it goes on changing, and the only notice of an object being
// destroyed is a KillObject sent to the agents present -- so a store
// left behind by the last agent to leave would quietly fill with things
// that no longer exist, and would look exactly like a store that was
// right.
type Cache struct {
	mu sync.Mutex
	by map[msg.UUID]*held
}

type held struct {
	store *Objects
	refs  int
}

// NewCache makes a cache for one process to share among its agents.
func NewCache() *Cache { return &Cache{by: map[msg.UUID]*held{}} }

// Attach returns the store for a region, making one if this is the
// first agent to arrive.
//
// A region with no id gets a store of its own that is in no cache: an
// unidentified region cannot be told apart from the next unidentified
// one, and sharing on that basis would merge two regions into one.
func (c *Cache) Attach(region msg.UUID) *Objects {
	if region.IsZero() {
		return newObjects()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	h := c.by[region]
	if h == nil {
		h = &held{store: newObjects()}
		c.by[region] = h
	}
	h.refs++
	return h.store
}

// Detach gives a region's store back.  The last one out drops it.
func (c *Cache) Detach(region msg.UUID) {
	if region.IsZero() {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	h := c.by[region]
	if h == nil {
		return
	}
	if h.refs--; h.refs <= 0 {
		delete(c.by, region)
	}
}

// Regions is how many stores are being held.
func (c *Cache) Regions() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.by)
}

// Objects is what this agent's region has said about itself.
//
// It is a method rather than a field because the store changes when the
// avatar crosses into another region, and a caller holding the old
// pointer would be reading somewhere else.
//
// An agent that has not registered its handlers yet gets an empty store
// rather than nil: the alternative is that every caller checks, and
// what they would do about it is make one of these.
func (a *Agent) Objects() *Objects {
	if o := a.objects.Load(); o != nil {
		return o
	}
	fresh := newObjects()
	if a.objects.CompareAndSwap(nil, fresh) {
		return fresh
	}
	return a.objects.Load()
}

// viewKey names this agent among the others sharing a store.  The
// session id rather than the agent id, so that the same avatar logged
// in twice counts as two viewpoints, which is what it is.
func (a *Agent) viewKey() string { return a.Account.SessionID.String() }

// enterRegion puts this agent in a region and takes it out of the last
// one.
//
// Without a cache the store is this agent's own, and arriving somewhere
// new means forgetting: what it holds describes a region the avatar has
// left.  With one, leaving is giving the old store back -- the agents
// still there keep it, and it is dropped when the last of them goes.
func (a *Agent) enterRegion(region msg.UUID) {
	a.mu.Lock()
	was := a.attached
	if was == region {
		a.mu.Unlock()
		return
	}
	a.attached = region
	a.mu.Unlock()

	// The first handshake is not a crossing.  Nothing has been left
	// behind, and what the store holds is this region's: an object can
	// be described before the message that says where we are.
	first := was.IsZero()

	// The land of the region just left describes somewhere else.  It
	// is dropped on a crossing and not on the first handshake, where
	// patches may already have arrived ahead of it.  So are the local
	// ids asked about there: each region numbers its own objects.
	if !first {
		a.terrain.forget()
		a.appearance.forget()
		a.parcels.forget()
		a.askedMu.Lock()
		a.asked = nil
		a.askedMu.Unlock()
	}

	if a.regions == nil {
		if !first {
			a.objects.Load().Flush()
		}
		return
	}

	old := a.Objects()
	store := a.regions.Attach(region)
	if first {
		store.absorb(old)
	}
	a.objects.Store(store)
	old.Unwatch(a.viewKey())
	a.regions.Detach(was)
	// Looking from here from the moment it is here: see lookFrom.
	a.lookFrom()
}

// A RegionChangeHandler is told the name and handle of the region the
// avatar is in now.  See Options.OnRegionChange.
type RegionChangeHandler func(name string, handle uint64)

// regionChanged says that the avatar is in a different region from the
// one it was in.
//
// It is called from arrive and not from the RegionHandshake handler,
// which is where the object store and the terrain change and so is the
// obvious place for it.  The obvious place is the wrong one,
// because the handshake does not carry the handle: that comes in
// AgentMovementComplete, and a notice fired from the handshake would
// pair the new region's name with the handle of the region the avatar
// has left -- an answer that is half right, which is worse than none,
// since the two halves would be checked against each other and
// disagree.  Nor is the movement enough on its own, since it can be
// delivered before the handshake; arrive waits for both, so the name
// this is given is the new region's whichever order they came in.
//
// The first arrival of a session is not a change and says nothing.
// That is what a zero handle behind us means: nothing was held that
// could be stale.  It is also what keeps a reconnect quiet -- a
// reconnect is a fresh Agent, so its AgentMovementComplete is a first
// arrival, and whatever hosts the session already says so for itself.
//
// A second AgentMovementComplete naming the region we are already in
// says nothing either.  There is nothing to drop, and a notice for it
// would have a client throw away an object cache it has just filled.
func (a *Agent) regionChanged(was, now uint64, name string) {
	if was == 0 || was == now {
		return
	}
	if fn := a.opts.OnRegionChange; fn != nil {
		fn(name, now)
	}
}

// leaveRegion gives the store back, which is what logging out amounts
// to.  A viewpoint left behind would keep objects alive for an avatar
// that is not there, and a reference left behind would keep a whole
// region's store alive after the last agent had gone.
func (a *Agent) leaveRegion() {
	a.mu.Lock()
	was := a.attached
	a.attached = msg.UUID{}
	a.mu.Unlock()

	if o := a.objects.Load(); o != nil {
		o.Unwatch(a.viewKey())
	}
	if a.regions != nil {
		a.regions.Detach(was)
	}
}
