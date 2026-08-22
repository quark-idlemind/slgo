// Package slots hands out interchangeable things a number at a time,
// all of them or none.
//
// # What it is for
//
// Several programs share a set of objects worn by the daemon's avatars:
// a benchmark takes four because its search compares readings taken at
// once, and a program running scripts takes one per script it runs at
// once.  Two of them in the same object would overwrite each other's
// script and each other's data, so the objects have to be handed out --
// and handed out in a way that cannot leave two callers each holding
// some of what the other needs.
//
// # Why one goroutine owns everything
//
// The alternative, which this replaces, was a lock per object and an
// allocation lock over them, with an invariant -- nothing may block
// while holding the allocation lock -- that every caller had to keep for
// the scheme to be deadlock free.  Here there is one goroutine, it holds
// no locks, and it never waits for anything: a request is decided in one
// pass over what is free.  There is nothing for a caller to get wrong,
// and nothing to reason about beyond this file.
//
// # All of them or none
//
// A caller given four of the eight it asked for has two bad choices:
// hold them while waiting for the rest, which is the deadlock, or give
// them back, which is what this does for it.  A request that cannot be
// served takes nothing and comes back with a channel to wait on, closed
// when something has been given back or added -- so a caller learns when
// it is worth asking again rather than polling.
//
// What that leaves is starvation: a caller wanting twelve can in
// principle be stepped over for ever by a stream of callers wanting one.
// Nothing here prevents that, deliberately.  These are programs run by
// hand or from a script, not a service under load; the requests drain,
// and a scheme that could not be reasoned about would be the worse
// trade.
//
// # Leases, and who makes them safe
//
// A grant lasts a while rather than for ever, so that a caller which
// wedges without dying does not take objects out of use permanently.
// Response.Expire is when it runs out, and Renew puts it back for work
// that cannot say in advance how long it will take -- a search finishes
// when it converges.  A caller that watches its deadline and renews is
// never surprised.
//
// What makes a caller that does NOT watch harmless is not this package.
// The pool hands out names for things it knows nothing about, and it
// cannot stop anybody using one it has taken back.  That belongs to
// whoever owns the things themselves: the daemon that writes a script
// into an object can see which slot the object is, and refuse a write
// from a caller that no longer holds it.  So a caller which overruns is
// refused at the moment it writes, loudly and attributably, rather than
// landing in somebody else's object.  (Intended rather than built: as
// this is written nothing enforces it, and the pool is the only thing
// standing between two callers.)
//
// grace is the other half of that, and it is why the deadline is not a
// cliff.  See it below.
//
// Nothing here reads a clock of its own.  Expiry happens when
// ExpiryCheck says so, and Wake says when that would be worth doing.  A
// pool that owned a timer would be a pool whose tests had to sleep.
package slots

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// timeNow is the clock, so that a test can run a lease out rather than
// wait it out.  A test that replaces it puts it back and does not run in
// parallel with another that does: it is the whole package's clock and
// not one pool's.
var timeNow = time.Now

// DefaultTimeout is how long a grant lasts when the caller does not say.
const DefaultTimeout = time.Minute

// grace is how much longer than the caller was told the pool holds a
// grant before re-letting it.
//
// Without it every deadline is a cliff: a caller that looks at the clock
// a moment before its grant runs out, and whose write arrives a moment
// after, is refused for a race it could not have avoided and nobody else
// wanted the slot anyway.  Holding on a little turns that into a write
// that simply works.
//
// It is not what makes overrunning safe -- see the package comment --
// and it is not sized for the work itself.  Measured against the grid,
// replacing a script in an object costs about 0.9s and creating one
// about 8.1s, so a caller that starts a create with a second left is
// past this and ought to have renewed.
const grace = 10 * time.Second

// Errors a caller can act on.  A lease that is gone is not an error the
// pool made: the caller was told when it would run out.
var (
	ErrClosed  = errors.New("slots: the pool has stopped")
	ErrNoLease = errors.New("slots: no such grant; it was returned or it ran out")
)

// An ID is unique within one pool and is only assigned here.
type ID struct{ id uint64 }

func (id ID) String() string { return fmt.Sprintf("slot#%d", id.id) }

// IsZero is whether this is the ID nothing has.
func (id ID) IsZero() bool { return id.id == 0 }

// A Slot is one of the things being handed out.
//
// Data is the caller's: what the slot stands for, which this package
// never looks at.  For the object pool it is the avatar and the
// inventory item the object was worn from -- the item and not the
// object, because a worn object is rezzed afresh with a new key every
// time it is put on and every time the avatar logs in, where the item it
// came from does not change.
type Slot struct {
	// Clean, when set, is what has to happen to a slot before anybody
	// else can have it, and is set by whoever added it.  For an object
	// that a script was run in it is writing an empty script over what
	// is there, so that the last caller's script stops talking before
	// the next caller starts listening -- chat carries the object a line
	// came from and never the script's name, so a script left running is
	// a line the next caller reads as its own.
	//
	// It is run on a goroutine of its own and NOT on the pool's, because
	// it goes to the grid and everybody else's request would wait behind
	// it.  A slot with tidying to do LEAVES the pool when it is given
	// back, and comes back only when Clean says so by calling Cleaned.
	//
	// So a slot that is really gone -- the object detached, deleted,
	// left behind by a logout -- needs no error and no telling: Clean
	// looks, decides there is nothing to tidy and nothing to hand on,
	// and says nothing.  The slot is out of the pool because it was
	// never put back.  A Clean that hangs has the same effect by
	// accident, which is the reason to put whatever it does on a clock
	// of its own.
	Clean func()

	id      ID
	removed bool
	Data    any
}

// ID is which slot this is.  It survives being copied into a Response,
// which is how a caller says which slot it is talking about.
func (s Slot) ID() ID { return s.id }

// A Response is the answer to a request.
//
// Filled says which of the two it is.  When it was not filled, Wait is
// closed once something has been given back or added, and asking again
// then is worth the trip.
type Response struct {
	// ID names this grant, and is what Return and Renew take.
	ID ID

	// Slots is what was granted, in the order the pool holds them.
	Slots []Slot

	// Expire is when the grant runs out.  A caller with work still to do
	// renews before then; one that does not is liable to be refused when
	// it writes.  See the package comment.
	Expire time.Time

	// Wait is closed when the pool has changed in a way that might make
	// the same request succeed.  Nil when the request was filled.
	Wait <-chan struct{}
}

// Filled is whether the request was granted.
func (r Response) Filled() bool { return r.Wait == nil }

// A Pool hands out slots.  Nothing works until Run is called.
type Pool struct {
	// Present, when set, is asked whether the thing a slot stands for is
	// still there, just before the slot is handed out.  A slot it
	// refuses is dropped from the pool rather than granted.
	//
	// It is here because the pool cannot know: an object is worn by an
	// avatar on a grid, and it can be taken off, deleted, or left behind
	// by a logout between one grant and the next.  It is called from the
	// pool's own goroutine and must not call back into the pool.
	Present func(data any) bool

	req  chan any
	wake chan time.Time
	done chan struct{}

	id uint64

	// free is what nobody has, oldest first.  Sorted rather than a set
	// because the order is the answer to "which of these should go out
	// next", and an order maintained as slots come and go is cheaper and
	// plainer than one worked out afresh on every request.
	free     []*Slot
	assigned map[ID]*lease
	wait     chan struct{}
}

// A lease is what one caller was granted.
type lease struct {
	slots []*Slot

	// until is what the caller was told and expire is when the pool
	// will actually take the slots back: the difference is grace.
	until  time.Time
	expire time.Time
}

// New returns a pool with nothing in it.  Add puts slots in; Run makes
// it answer.
func New() *Pool {
	return &Pool{
		req:      make(chan any),
		wake:     make(chan time.Time, 1),
		done:     make(chan struct{}),
		assigned: map[ID]*lease{},
	}
}

// Wake carries the time at which an ExpiryCheck would be worth making:
// the earliest a grant could be taken back.
//
// It is how the pool stays free of clocks.  Whoever runs the pool arms a
// timer for what arrives here and calls ExpiryCheck when it fires.  Only
// the most useful time is kept, so a reader that is slow misses nothing
// but a wakeup it no longer needs, and an ExpiryCheck that turns out to
// be early costs a pass over the grants.
func (p *Pool) Wake() <-chan time.Time { return p.wake }

// --------------------------------------------------------- the caller

// Get asks for n slots, all of them or none.
//
// timeout is how long the caller wants them for; it defaults to
// DefaultTimeout.  Look at Filled before Slots.
func (p *Pool) Get(n int, timeout time.Duration) (Response, error) {
	if n < 1 {
		return Response{}, fmt.Errorf("slots: a request for %d slots", n)
	}
	r := getReq{n: n, timeout: timeout, reply: make(chan Response, 1)}
	return ask(p, r, r.reply)
}

// Renew puts a grant's deadline back, for work that cannot say in
// advance how long it will take.
//
// It answers with the new deadline.  A grant that has already been taken
// back is ErrNoLease, and is not an error the pool made: the caller was
// told when it would run out.
func (p *Pool) Renew(id ID, timeout time.Duration) (time.Time, error) {
	r := renewReq{id: id, timeout: timeout, reply: make(chan renewed, 1)}
	got, err := ask(p, r, r.reply)
	if err != nil {
		return time.Time{}, err
	}
	return got.until, got.err
}

// Return gives a grant back.  Returning one twice is not an error: the
// second one has nothing to give.
func (p *Pool) Return(id ID) error {
	r := returnReq{id: id, done: make(chan struct{})}
	_, err := ask(p, r, r.done)
	return err
}

// ---------------------------------------------------------- the owner

// Add puts slots into the pool and gives each an ID.
//
// A slot that was removed and is added again is a new slot with a new
// ID, because the thing it stands for went away and came back.
func (p *Pool) Add(slots ...*Slot) error {
	r := addReq{slots: slots, done: make(chan struct{})}
	_, err := ask(p, r, r.done)
	return err
}

// Remove takes slots out of the pool.
//
// One that is granted at the time is marked and taken out when it comes
// back, rather than pulled from under whoever is using it -- which is
// what an avatar logging out looks like: its objects are gone, and the
// run that had them will find that out from its own session.
func (p *Pool) Remove(slots ...*Slot) error {
	r := removeReq{slots: slots, done: make(chan struct{})}
	_, err := ask(p, r, r.done)
	return err
}

// Cleaned puts a slot back after its Clean has made it fit to be used
// again, keeping the ID it already had -- it is the same thing, and its
// place in the order is its age.
//
// This is the only way back for a slot with tidying to do, and not
// calling it is how Clean says the slot is gone: there is nothing to
// hand on, so nothing is.
//
// A slot that is already in the pool, or that was taken out while it was
// away, is ignored rather than added: putting the same one in twice
// would be one object handed to two callers.
func (p *Pool) Cleaned(s *Slot) error {
	r := cleanedReq{slot: s, done: make(chan struct{})}
	_, err := ask(p, r, r.done)
	return err
}

// ExpiryCheck takes back every grant that has run out.
//
// Nothing here reads a clock, so this is how time passes as far as the
// pool is concerned.  Whoever runs the pool calls it when the time Wake
// named arrives; calling it more often is a waste and never wrong.
func (p *Pool) ExpiryCheck() error {
	r := expiryReq{done: make(chan struct{})}
	_, err := ask(p, r, r.done)
	return err
}

// Close stops the pool.  Everything after it is ErrClosed rather than a
// wait for a goroutine that has gone.
func (p *Pool) Close() error {
	select {
	case p.req <- stopReq{}:
		<-p.done
		return nil
	case <-p.done:
		return ErrClosed
	}
}

// ask sends a request and waits for its answer, or for the pool to stop.
//
// Both halves have to watch for the pool stopping: a caller that sent
// its request a moment before Run returned would otherwise wait for an
// answer nobody is going to write.
func ask[T any](p *Pool, req any, reply chan T) (T, error) {
	var zero T
	select {
	case p.req <- req:
	case <-p.done:
		return zero, ErrClosed
	}
	select {
	case got := <-reply:
		return got, nil
	case <-p.done:
		return zero, ErrClosed
	}
}

// The requests, which are the pool's whole vocabulary.  They are not
// exported: a caller that had to build one and supply a channel to
// answer on would be doing by hand what the methods above do for it, and
// could get it wrong in ways that hang.
type (
	getReq struct {
		n       int
		timeout time.Duration
		reply   chan Response
	}
	renewReq struct {
		id      ID
		timeout time.Duration
		reply   chan renewed
	}
	returnReq struct {
		id   ID
		done chan struct{}
	}
	addReq struct {
		slots []*Slot
		done  chan struct{}
	}
	removeReq struct {
		slots []*Slot
		done  chan struct{}
	}
	cleanedReq struct {
		slot *Slot
		done chan struct{}
	}
	expiryReq struct{ done chan struct{} }
	stopReq   struct{}
)

// renewed is what a renewal came to.
type renewed struct {
	until time.Time
	err   error
}

// ------------------------------------------------------------ the pool

// Run answers requests until Close.  It is the only thing that touches
// the pool's state, which is the whole design: there are no locks here
// because there is nothing to lock against.
func (p *Pool) Run() {
	defer close(p.done)
	for r := range p.req {
		now := timeNow()
		switch req := r.(type) {
		case getReq:
			req.reply <- p.get(now, req)

		case renewReq:
			req.reply <- p.renew(now, req)

		case returnReq:
			p.give(req.id)
			close(req.done)

		case cleanedReq:
			if p.insert(req.slot) {
				p.wakeWaiters()
			}
			close(req.done)

		case addReq:
			for _, s := range req.slots {
				s.id = p.nextID()
				s.removed = false
				p.insert(s)
			}

			if len(req.slots) > 0 {
				// Somebody waiting for four may be waiting for exactly
				// what has just arrived.
				p.wakeWaiters()
			}
			close(req.done)

		case removeReq:
			for _, s := range req.slots {
				s.removed = true
				p.drop(s.id)
			}
			close(req.done)

		case expiryReq:
			if p.reclaim(now) {
				p.wakeWaiters()
			}
			p.sayWhenToWake()
			close(req.done)

		case stopReq:
			// Anybody waiting is woken rather than left: their request
			// will fail, which is an answer.
			p.wakeWaiters()
			return
		}
	}
}

// get answers a request for n slots.
func (p *Pool) get(now time.Time, req getReq) Response {
	// Expired grants first: they are slots this request can have, and
	// nothing else is going to notice them until the next ExpiryCheck.
	p.reclaim(now)

	got := p.take(req.n)
	if got == nil {
		return Response{Wait: p.waiter()}
	}

	timeout := req.timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	l := &lease{slots: got, until: now.Add(timeout), expire: now.Add(timeout + grace)}
	id := p.nextID()
	p.assigned[id] = l

	r := Response{ID: id, Slots: make([]Slot, 0, len(got)), Expire: l.until}
	for _, s := range got {
		// Without the hook: it is the pool's to run and the owner's to
		// have written, and a copy of it in somebody else's hands is a
		// way to have the tidying happen twice, or at the wrong moment,
		// or by whoever is holding the slot rather than by whoever
		// knows what tidying it needs.
		c := *s
		c.Clean = nil
		r.Slots = append(r.Slots, c)
	}
	p.sayWhenToWake()
	return r
}

// take finds n slots that are free and still there, or nothing.
//
// Oldest first, which is the order they were added: the caller of a pool
// holding several avatars' objects gets the first avatar's before the
// second's, so a request that fits on one avatar stays on one avatar
// without this package knowing what an avatar is.
//
// It stops as soon as it has enough, so Present is asked about the slots
// that were candidates and not about the whole pool.
func (p *Pool) take(n int) []*Slot {
	var (
		got  []*Slot
		seen int
	)
	for seen = 0; seen < len(p.free) && len(got) < n; seen++ {
		s := p.free[seen]
		if p.Present != nil && !p.Present(s.Data) {
			// Gone since it was added -- taken off, deleted, or left
			// behind by a logout.  Dropped rather than handed out, and
			// not put back below.
			s.removed = true
			continue
		}
		got = append(got, s)
	}

	// Everything up to seen has been decided: granted, or dropped.  The
	// rest is untouched and keeps its order.
	rest := append([]*Slot{}, p.free[seen:]...)
	if len(got) < n {
		// Not enough.  What was collected is still free, and still the
		// oldest, so it goes back in front.
		p.free = append(got, rest...)
		return nil
	}
	p.free = rest
	return got
}

// insert files a slot among the free ones, oldest first, and says
// whether it took it.
//
// By ID, which is by age: they are handed out in the order this package
// assigned them, and it assigns them upwards.
//
// It refuses a slot that is already there, which is the one mistake that
// must not get through: the same slot in the free list twice is one
// object handed to two callers, which is what all of this exists to
// prevent.  Finding out costs nothing, because the place it would go is
// the place to look.  It refuses one that has been taken out of the pool
// for the same sort of reason -- a slot removed while it was away being
// tidied must not come back.
func (p *Pool) insert(s *Slot) bool {
	if s == nil || s.removed || s.id.IsZero() {
		return false
	}
	i := sort.Search(len(p.free), func(i int) bool { return p.free[i].id.id >= s.id.id })
	if i < len(p.free) && p.free[i].id == s.id {
		return false
	}
	p.free = append(p.free, nil)
	copy(p.free[i+1:], p.free[i:])
	p.free[i] = s
	return true
}

// drop takes a slot out of the free ones.  A slot that is not there --
// because somebody has it -- is not an error: Remove marks it, and it
// goes when it comes back.
func (p *Pool) drop(id ID) {
	for i, s := range p.free {
		if s.id == id {
			p.free = append(p.free[:i], p.free[i+1:]...)
			return
		}
	}
}

// renew puts a grant's deadline back.
func (p *Pool) renew(now time.Time, req renewReq) renewed {
	l, ok := p.assigned[req.id]
	if !ok {
		return renewed{err: ErrNoLease}
	}
	timeout := req.timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	l.until = now.Add(timeout)
	l.expire = now.Add(timeout + grace)
	p.sayWhenToWake()
	return renewed{until: l.until}
}

// give puts a grant's slots back in the pool.
func (p *Pool) give(id ID) {
	l, ok := p.assigned[id]
	if !ok {
		return
	}
	delete(p.assigned, id)
	if p.release(l.slots) {
		p.wakeWaiters()
	}
	p.sayWhenToWake()
}

// release lets go of slots nobody has any more, and says whether any of
// them reached the free list.
//
// Some do not: one taken out of the pool while it was in use goes now,
// and one with tidying to do goes to be tidied and arrives later.  What
// this answers is whether there is anything new for a caller to be woken
// about -- waking somebody to look at a pool that has not changed is a
// round trip for nothing.
func (p *Pool) release(slots []*Slot) bool {
	freed := false
	for _, s := range slots {
		switch {
		case s.removed:
			// Taken out of the pool while it was in use.  This is where
			// that finally happens.
		case s.Clean != nil:
			// Out of the pool until its own tidying puts it back.
			go s.Clean()
		default:
			if p.insert(s) {
				freed = true
			}
		}
	}
	return freed
}

// reclaim takes back every grant whose time is up, and says whether it
// took anything.
func (p *Pool) reclaim(now time.Time) bool {
	took := false
	for id, l := range p.assigned {
		if now.Before(l.expire) {
			continue
		}
		delete(p.assigned, id)
		if p.release(l.slots) {
			took = true
		}
	}
	return took
}

// waiter is the channel a request that could not be served waits on.
// One channel serves everybody waiting: what it says is "something has
// changed", and what to do about it is to ask again.
func (p *Pool) waiter() <-chan struct{} {
	if p.wait == nil {
		p.wait = make(chan struct{})
	}
	return p.wait
}

// wakeWaiters tells everybody waiting that it is worth asking again.
func (p *Pool) wakeWaiters() {
	if p.wait != nil {
		close(p.wait)
		p.wait = nil
	}
}

// sayWhenToWake offers the earliest time a grant could be taken back.
//
// A non-blocking send over a channel of one, so the pool never waits for
// whoever is driving it, and only the most useful time is kept: an
// earlier one replaces a later one, and a later one is dropped, because
// a check made too early costs a pass and a check made too late leaves
// slots out of use.
func (p *Pool) sayWhenToWake() {
	var first time.Time
	for _, l := range p.assigned {
		if first.IsZero() || l.expire.Before(first) {
			first = l.expire
		}
	}
	if first.IsZero() {
		return // nothing is out; there is nothing to wake for
	}
	select {
	case had := <-p.wake:
		if had.Before(first) {
			first = had
		}
	default:
	}
	select {
	case p.wake <- first:
	default:
	}
}

// nextID returns the next ID this pool has not used.
func (p *Pool) nextID() ID { p.id++; return ID{id: p.id} }
