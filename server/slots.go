package server

// The shared objects, handed out a number at a time.
//
// # What is being shared
//
// Every hosted avatar wears a set of objects that programs run scripts
// in -- slrun one per script it runs at once, slbench four for its
// search.  Two runs in one object overwrite each other's script and each
// other's data, and both report success, so the objects have to be
// handed out.
//
// # Why the daemon and not the clients
//
// The clients did it themselves until now, with a lock per object and an
// allocation lock over them, and an invariant every one of them had to
// keep for the scheme not to deadlock.  That works and it is delicate,
// and it cannot answer the question a caller actually asks: give me
// twelve, from wherever they are.  Here there is one pool for the whole
// daemon, so a request for twelve is answered out of three avatars if
// that is where the free ones are, atomically, by the only thing that
// can see all of them at once.
//
// # What a slot is, and is not
//
// A PLACE: an avatar and a number, nothing more.  Not an object.  The
// daemon deliberately does not know which object stands in which place,
// because knowing would mean reading inventory to learn which worn items
// are pool objects -- the item's name is the only thing that says so,
// and names live in AIS rather than on the wire.  The client already
// knows all of that: it is the one that wears a missing object and makes
// a missing item.
//
// So the daemon hands out exclusion and the client hands out objects,
// which is the division that was there before and is worth keeping.
//
// # Leases
//
// A grant belongs to the stream that asked for it and is given back when
// that stream ends, however it ended -- the same lease a lock has, for
// the same reason.  The clock on top of that is for the client that
// wedges without dying: it is connected, so its stream says it is alive,
// and it is never going to give anything back.

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/internal/slots"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// SlotsPerAgent is how many places each hosted avatar has.
//
// It has to agree with what the clients believe, since they are the ones
// that turn a place into an object: a place the client cannot wear is a
// grant nobody can use.  cmd/slgod sets it from the same list the
// clients read.
const SlotsPerAgent = 12

// where is what one slot stands for.  Only the daemon looks at it.
type where struct {
	agent string
	slot  int

	// dirty is set when a grant ended without its holder saying it had
	// left the object fit to use -- which includes every grant that ran
	// out, since a caller that wedged is exactly the caller whose script
	// is still talking.  It is the next holder's job to clear it, and it
	// is told so.  Read and written under slotPool.mu.
	dirty bool
}

// grant is what one client was given, kept so that it can be given back
// when the stream ends and so that a client cannot renew or release
// somebody else's.
type grant struct {
	id    slots.ID
	token string
	who   *Client
	held  []slots.Slot
}

// slotPool is the pool and what the daemon keeps beside it.
type slotPool struct {
	srv  *Server
	pool *slots.Pool

	mu     sync.Mutex
	next   uint64
	grants map[string]*grant

	// placing is held while an avatar's places go into the pool or come
	// out, so that the two cannot cross: a removal between an add's
	// deciding and its doing would leave the places in.  It guards added
	// and leaving.  It is held across calls to the pool, whose Present
	// takes the server's lock, so it is never taken with that lock held.
	placing sync.Mutex
	added   map[string][]*slots.Slot // each hosted avatar's places
	leaving map[string][]*slots.Slot // places taken out with their avatar, maybe still granted
}

// slotsOf returns the daemon's pool, starting it on first use.
//
// On demand rather than in a constructor because Server is built in more
// than one place, one of them a test, and a pool that has to be
// remembered at each of them is a nil pointer waiting for the next one.
func (s *Server) slotsOf() *slotPool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.slots == nil {
		s.slots = newSlotPool(s)
	}
	return s.slots
}

func newSlotPool(s *Server) *slotPool {
	sp := &slotPool{
		srv:     s,
		pool:    slots.New(),
		grants:  map[string]*grant{},
		added:   map[string][]*slots.Slot{},
		leaving: map[string][]*slots.Slot{},
	}
	// A place is only worth handing out while its avatar is here and up:
	// one that is not is a grant the client cannot use, and it would find
	// that out in the middle of a run.  A place whose avatar is logged
	// out is passed over and left in the pool; one whose avatar has left
	// the daemon, as a forced Host makes it before logging in again, is
	// taken out by removeAgent.
	// Why: doc/slots.md#when-an-avatar-is-logged-out-or-leaves
	sp.pool.Present = func(data any) bool {
		w, ok := data.(*where)
		if !ok {
			return false
		}
		h, err := s.lookup(w.agent)
		return err == nil && h != nil && !h.Stopped() && h.Agent() != nil
	}
	go sp.pool.Run()
	go sp.expire()
	return sp
}

// expire is the clock the pool does not own.
//
// The pool says when a grant could next be taken back and this arms a
// timer for it; nothing ticks when nothing is out.  A check that turns
// out to be early costs a pass over the grants, which is why the pool is
// allowed to be approximate about when it asks.
func (sp *slotPool) expire() {
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	for {
		select {
		case at, ok := <-sp.pool.Wake():
			if !ok {
				return
			}
			timer.Stop()
			d := time.Until(at)
			if d < 0 {
				d = 0
			}
			timer.Reset(d)
		case <-timer.C:
			if err := sp.pool.ExpiryCheck(); err != nil {
				return // the pool has stopped
			}
		}
	}
}

// sync makes sure every avatar the daemon holds has its places in the
// pool.
//
// Asked on every request rather than only told, because a Hosted comes
// into being in more than one way -- StartAgent, and a test that builds
// one directly -- and a pool that had to be notified at each of them is
// a pool with no places in it the day somebody adds a third way.  It is
// a map lookup per avatar, on a path that is about to go to the grid.
// Hosting an avatar tells it as well, so that a request already waiting
// for that avatar hears of its places.
func (sp *slotPool) sync() {
	if sp.srv == nil {
		return
	}
	sp.placing.Lock()
	defer sp.placing.Unlock()
	// The old places of an avatar that left, once no grant has them,
	// are forgotten, and the name can be given new ones.
	for name, old := range sp.leaving {
		still, err := sp.pool.Held(old...)
		switch {
		case err != nil:
		case len(still) == 0:
			delete(sp.leaving, name)
		default:
			sp.leaving[name] = still
		}
	}
	// Listed under placing, so that an avatar removed meanwhile is not
	// given its places back.
	for _, name := range sp.srv.hosted() {
		sp.addAgent(name)
	}
}

// addAgent puts an avatar's places into the pool.  placing is held.
//
// Once while it is hosted: a session that drops and comes back is the
// same avatar wearing the same things, and whether they can be used is
// Present's to answer.  A name hosted again after it left is given new
// places, but not while any of the old ones is still granted, since the
// two would stand for the same objects.
func (sp *slotPool) addAgent(name string) {
	if sp.added[name] != nil || len(sp.leaving[name]) > 0 {
		return
	}
	list := make([]*slots.Slot, 0, SlotsPerAgent)
	for i := 0; i < SlotsPerAgent; i++ {
		// Dirty to begin with: whatever was in these objects when the
		// daemon started is not this daemon's doing, and a script left
		// running by the last one is exactly what dirty means.
		list = append(list, &slots.Slot{Data: &where{agent: name, slot: i, dirty: true}})
	}
	sp.added[name] = list
	sp.pool.Add(list...)
}

// removeAgent takes an avatar's places out of the pool, when it has left
// the daemon.
//
// A place granted at the time stays with its holder until it is given
// back or runs out, and the pool drops it then.  The holder renews and
// gives back as ever, and giving back puts nothing in.  Until the last
// has gone, the name is given no new places; see addAgent.
func (sp *slotPool) removeAgent(name string) {
	sp.placing.Lock()
	defer sp.placing.Unlock()
	list := sp.added[name]
	if list == nil {
		return
	}
	delete(sp.added, name)
	sp.pool.Remove(list...)
	sp.leaving[name] = append(sp.leaving[name], list...)
}

// ask answers a client asking for places.
func (sp *slotPool) ask(ctx context.Context, c *Client, req *pb.Slots) {
	want := int(req.GetWant())
	if want < 1 {
		c.answer(sp.granted(req.GetRequest(), "", nil, time.Time{}, "a request for no objects"))
		return
	}

	timeout := time.Duration(req.GetSeconds()) * time.Second
	var match func(any) bool
	if only := req.GetAgent(); only != "" {
		// One avatar, because these places have to have something in
		// common with each other: a benchmark compares its objects, so
		// four spread over three avatars is four readings that cannot
		// be compared.
		match = func(data any) bool {
			w, ok := data.(*where)
			return ok && w.agent == only
		}
	}
	// A bounded wait gives up with an answer saying so.  A try never
	// reaches the select, so it needs no clock.
	var lapsed <-chan time.Time
	wait := time.Duration(req.GetWaitSeconds()) * time.Second
	if wait > 0 && !req.GetTry() {
		t := time.NewTimer(wait)
		defer t.Stop()
		lapsed = t.C
	}
	for {
		// Whatever the daemon is holding now, which may be more than it
		// was holding when this client attached.
		sp.sync()

		// An avatar the daemon does not host has no places and none are
		// coming, so it is refused rather than waited for.  One that is
		// logged out, or still being logged in, is waited for: hosting
		// it gives it places.
		only, most := req.GetAgent(), sp.most(req.GetAgent())
		if only != "" && most == 0 {
			c.answer(sp.granted(req.GetRequest(), "", nil, time.Time{},
				fmt.Sprintf("no avatar called %q is hosted here", only)))
			return
		}

		// More than there could ever be is refused rather than waited
		// for: waiting for objects that do not exist is waiting for
		// ever, and the daemon is the only thing that knows how many
		// there are.  The pool cannot say -- a place away being tidied
		// is not in it at all -- and would simply never fill.
		if want > most {
			c.answer(sp.granted(req.GetRequest(), "", nil, time.Time{}, fmt.Sprintf(
				"%d objects were asked for and %s %d",
				want, hasOrHave(req.GetAgent()), most)))
			return
		}

		r, err := sp.pool.GetWhere(want, timeout, match)
		if err != nil {
			c.answer(sp.granted(req.GetRequest(), "", nil, time.Time{}, err.Error()))
			return
		}
		if r.Filled() {
			token := sp.keep(c, r)
			if token == "" {
				// The stream ended while this was settled, and what
				// it held has already been given back.
				sp.pool.Return(r.ID)
				return
			}
			c.answer(sp.granted(req.GetRequest(), token, r.Slots, r.Expire, ""))
			return
		}
		if req.GetTry() {
			c.answer(sp.granted(req.GetRequest(), "", nil, time.Time{},
				fmt.Sprintf("%d objects are not free", want)))
			return
		}
		// Waiting.  Nothing is held while this waits -- the pool gave
		// back what it could not fill the request with before it
		// answered -- so a client queued here is holding nothing and
		// blocking nobody.
		select {
		case <-r.Wait:
		case <-lapsed:
			c.answer(sp.granted(req.GetRequest(), "", nil, time.Time{},
				fmt.Sprintf("%d objects were still not free after %v", want, wait)))
			return
		case <-ctx.Done():
			return
		}
	}
}

// most is how many places there could ever be: one avatar's worth when
// one was named and the daemon holds it -- logged out, and still being
// logged in, count -- none when it does not, and every hosted avatar's
// otherwise.
func (sp *slotPool) most(agent string) int {
	if sp.srv == nil {
		return SlotsPerAgent
	}
	if agent != "" {
		if !sp.srv.holds(agent) {
			return 0
		}
		return SlotsPerAgent
	}
	return SlotsPerAgent * len(sp.srv.hosted())
}

// hasOrHave keeps the refusal readable whichever way it was asked.
func hasOrHave(agent string) string {
	if agent != "" {
		return agent + " has"
	}
	return "this daemon's avatars have"
}

// keep files a grant against the client that asked and names it, so
// that the client can talk about it afterwards and so that the end of
// its stream gives it back.
//
// It files nothing, and returns "", for a client whose stream has ended:
// releaseAll has been or is about to be, and a grant filed after it
// would hold its places until it ran out.  closed is set before
// releaseAll takes mu, so one of the two always sees the other.
func (sp *slotPool) keep(c *Client, r slots.Response) string {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if c.closed.Load() {
		return ""
	}
	sp.next++
	g := &grant{id: r.ID, token: fmt.Sprintf("g%d", sp.next), who: c, held: r.Slots}
	sp.grants[g.token] = g
	return g.token
}

// mine finds a grant, and only for the client it belongs to: a client
// cannot renew or release somebody else's work.
func (sp *slotPool) mine(c *Client, token string) *grant {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	g := sp.grants[token]
	if g == nil || g.who != c {
		return nil
	}
	return g
}

// renew puts a grant's clock back.
func (sp *slotPool) renew(c *Client, req *pb.RenewSlots) {
	g := sp.mine(c, req.GetGrant())
	if g == nil {
		c.answer(sp.granted(req.GetRequest(), "", nil, time.Time{}, "no such grant"))
		return
	}
	until, err := sp.pool.Renew(g.id, time.Duration(req.GetSeconds())*time.Second)
	if err != nil {
		sp.forget(g.token)
		c.answer(sp.granted(req.GetRequest(), "", nil, time.Time{}, err.Error()))
		return
	}
	c.answer(sp.granted(req.GetRequest(), g.token, g.held, until, ""))
}

// release gives a grant back.
func (sp *slotPool) release(c *Client, token string, clean bool) {
	g := sp.mine(c, token)
	if g == nil {
		return
	}
	sp.markDirty(g, !clean)
	sp.forget(token)
	sp.pool.Return(g.id)
}

// releaseAll gives back everything a client holds, which is what the end
// of its stream means.  A stream that ended said nothing about having
// left anything fit to use, so none of it is clean.
func (sp *slotPool) releaseAll(c *Client) {
	sp.mu.Lock()
	var mine []*grant
	for _, g := range sp.grants {
		if g.who == c {
			mine = append(mine, g)
		}
	}
	for _, g := range mine {
		delete(sp.grants, g.token)
	}
	sp.mu.Unlock()

	for _, g := range mine {
		sp.markDirty(g, true)
		sp.pool.Return(g.id)
	}
}

// markDirty says whether the objects behind a grant were left fit to
// use.  The flag is on the slot's own data, so it survives the grant.
// Under mu, because an answer being built may be reading it.
func (sp *slotPool) markDirty(g *grant, dirty bool) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	for i := range g.held {
		if w, ok := g.held[i].Data.(*where); ok {
			w.dirty = dirty
		}
	}
}

func (sp *slotPool) forget(token string) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	delete(sp.grants, token)
}

// granted builds the answer to request.
func (sp *slotPool) granted(request uint64, token string, held []slots.Slot, expires time.Time, why string) *pb.ServerPacket {
	g := &pb.SlotsGranted{Request: request, Grant: token, Why: why}
	if !expires.IsZero() {
		g.Expires = expires.Unix()
	}
	sp.mu.Lock()
	defer sp.mu.Unlock()
	for _, s := range held {
		w, ok := s.Data.(*where)
		if !ok {
			continue
		}
		g.Held = append(g.Held, &pb.SlotHeld{
			Agent: w.agent, Slot: uint32(w.slot), Dirty: w.dirty,
		})
	}
	return &pb.ServerPacket{Body: &pb.ServerPacket_Granted{Granted: g}}
}
