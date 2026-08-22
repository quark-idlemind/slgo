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
// # Leases, and why the caller is told when its own runs out
//
// A grant lasts a while rather than for ever, so that a caller which
// wedges without dying does not take objects out of use permanently.
// That is only safe if the holder knows: a pool that quietly re-lets an
// object somebody is still writing a script into has caused exactly the
// collision it exists to prevent.  So Response.Expire is what the caller
// was given, and it is the caller's business to have FINISHED by then --
// not merely to have started.  Measured against the grid, replacing a
// script in an object costs about 0.9s and creating one about 8.1s, so
// "will this finish in what is left?" is a real question and not a
// formality.  Renew is for work that cannot say in advance how long it
// will take.
//
// The pool holds each grant for a little longer than the caller was told
// -- see grace -- which covers the gap between a caller checking its
// deadline and its write reaching the grid.  It does not cover the work
// itself.
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

// DefaultTimeout is how long a grant lasts when the caller does not say.
const DefaultTimeout = time.Minute

// grace is how much longer than the caller was told the pool holds a
// grant before re-letting it.
//
// It covers the gap between a caller looking at its deadline and its
// write arriving -- scheduling, a round trip, a retry -- and nothing
// else.  A caller that starts an eight second write with a second left
// is outside it, and no grace that is not a second timeout would save
// it; having finished by the deadline is the caller's half of the
// bargain.
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

	// Expire is when the grant runs out.  The caller must be FINISHED
	// with the slots by then -- see the package comment.
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

	// now is the clock, so that a test can run a lease out rather than
	// wait it out.  Set before Run and never after: the pool's goroutine
	// is the only thing that reads it.
	now func() time.Time

	req  chan any
	wake chan time.Time
	done chan struct{}

	id       uint64
	free     map[ID]*Slot
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
		now:      time.Now,
		req:      make(chan any),
		wake:     make(chan time.Time, 1),
		done:     make(chan struct{}),
		free:     map[ID]*Slot{},
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
		now := p.now()
		switch req := r.(type) {
		case getReq:
			req.reply <- p.get(now, req)

		case renewReq:
			req.reply <- p.renew(now, req)

		case returnReq:
			p.give(req.id)
			close(req.done)

		case addReq:
			for _, s := range req.slots {
				s.id = p.nextID()
				s.removed = false
				p.free[s.id] = s
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
				delete(p.free, s.id)
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
		delete(p.free, s.id)
		r.Slots = append(r.Slots, *s)
	}
	p.sayWhenToWake()
	return r
}

// take finds n slots that are free and still there, or nothing.
//
// In ID order, which is the order they were added: the caller of a pool
// holding several avatars' objects gets the first avatar's before the
// second's, so a request that fits on one avatar stays on one avatar
// without this package knowing what an avatar is.
func (p *Pool) take(n int) []*Slot {
	var got []*Slot
	for _, s := range p.inOrder() {
		if p.Present != nil && !p.Present(s.Data) {
			// Gone since it was added -- taken off, deleted, or left
			// behind by a logout.  Dropped rather than handed out.
			s.removed = true
			delete(p.free, s.id)
			continue
		}
		got = append(got, s)
		if len(got) == n {
			return got
		}
	}
	// Not enough.  Nothing was taken out of free on the way, so there is
	// nothing to put back.
	return nil
}

// inOrder is the free slots, oldest first.
func (p *Pool) inOrder() []*Slot {
	out := make([]*Slot, 0, len(p.free))
	for _, s := range p.free {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id.id < out[j].id.id })
	return out
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
	for _, s := range l.slots {
		if s.removed {
			// Taken out of the pool while it was in use.  This is where
			// that finally happens.
			continue
		}
		p.free[s.id] = s
	}
	p.wakeWaiters()
	p.sayWhenToWake()
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
		for _, s := range l.slots {
			if s.removed {
				continue
			}
			p.free[s.id] = s
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
