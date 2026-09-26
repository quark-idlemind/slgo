package server

// Locks: exclusive use of something named, for as long as a client
// holds its stream.
//
// # Why the stream is the lease
//
// A lock has to be given back when the client goes away, and a client
// goes away in more ways than it returns: it exits, it panics, its
// machine is unplugged.  Asking it to say so is the one mechanism that
// does not cover the cases that matter.
//
// A client's stream already answers all of them.  gRPC cancels the
// stream's context when the connection ends, and the Stream handler
// already detaches the client in a defer -- so a lease owned by the
// client is released by code that was there before locks existed and
// is exercised by every disconnection.  What remains is the dead
// machine that never closes the connection, which no protocol can
// notice by itself; keepalive pings settle that, and they are the
// transport's business rather than something to reinvent here.
//
// # What it is for
//
// slrun and slbench run their scripts in one attached object, and
// a benchmark carries its base reading in that object's LINKSET DATA,
// which belongs to the object and not to the script.  Two runs at once
// would each divide by the other's numbers.  Labelling the output would
// not help: the clash is over the data, not over who said what.

import (
	"fmt"
	"sync"
)

// locks is a set of named locks, each held by at most one client.
type locks struct {
	mu   sync.Mutex
	held map[string]*Client
	// waiting is who wants each lock, in the order they asked.
	waiting map[string][]*waiter
}

// waiter is one client's place in the queue.
//
// The client is kept, not just the channel to wake: a client that gives
// up, or that goes away, has to be taken OUT of the queue, and a queue
// of anonymous channels cannot say which entries were its.  Handing the
// lock to a client that is no longer listening loses it until that
// client disconnects, and stalls everybody behind it.
type waiter struct {
	c     *Client
	ready chan struct{}
}

// lockSet returns this agent's locks, making the set on first use.
//
// On demand rather than in a constructor because Hosted is built in
// three places, one of them a test, and a lock set that has to be
// remembered at each of them is a nil pointer waiting for the fourth.
func (h *Hosted) lockSet() *locks {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.locks == nil {
		h.locks = newLocks()
	}
	return h.locks
}

func newLocks() *locks {
	return &locks{
		held:    map[string]*Client{},
		waiting: map[string][]*waiter{},
	}
}

// acquire takes a lock if it is free and reports whether it got it.
// The second return is who has it when it did not.
//
// A client whose stream has ended gets nothing, here or in queue: its
// request can be handled just after releaseAll ran for it, and what it
// took then would be held for good.  closed is set before releaseAll
// takes mu, so one of the two always sees the other.
func (l *locks) acquire(name string, c *Client) (bool, *Client) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if c.closed.Load() {
		return false, nil
	}
	if h, ok := l.held[name]; ok && h != c {
		return false, h
	}
	// Taking one already held is not an error: a client that asks
	// twice still holds it once, and one release still frees it.
	l.held[name] = c
	return true, nil
}

// queue returns a channel closed when the named lock is this client's,
// having put it at the back of the queue if somebody else has it.
func (l *locks) queue(name string, c *Client) chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()

	ready := make(chan struct{})
	if c.closed.Load() {
		return ready // never ready: its waiter ends with the stream
	}
	if h, ok := l.held[name]; !ok || h == c {
		l.held[name] = c
		close(ready)
		return ready
	}
	l.waiting[name] = append(l.waiting[name], &waiter{c: c, ready: ready})
	return ready
}

// giveUp is a client saying it no longer wants a lock: it releases the
// one it holds and abandons its place in the queue.
//
// Both, because a client that asked to wait and changed its mind is not
// the holder, and one that holds it is not in the queue -- and Unlock
// is the same word for both from where the client is standing.
func (l *locks) giveUp(name string, c *Client) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.dropWaiterLocked(name, c)
	if h, ok := l.held[name]; ok && h == c {
		delete(l.held, name)
		l.wake(name)
	}
}

// wake hands the lock to the next waiter, if there is one.
//
// The holder is recorded here, under the same lock that takes the
// waiter off the queue: between the two, nothing else may take it, or
// the queue would be a suggestion rather than an order.
func (l *locks) wake(name string) {
	q := l.waiting[name]
	if len(q) == 0 {
		delete(l.waiting, name)
		return
	}
	w := q[0]
	l.waiting[name] = q[1:]
	l.held[name] = w.c
	close(w.ready)
}

// dropWaiterLocked takes a client out of one queue.
func (l *locks) dropWaiterLocked(name string, c *Client) {
	q := l.waiting[name]
	for i, w := range q {
		if w.c == c {
			l.waiting[name] = append(q[:i:i], q[i+1:]...)
			return
		}
	}
}

// releaseAll gives back everything a client holds and forgets anything
// it was waiting for, which is what happens when its stream ends
// however it ended.
func (l *locks) releaseAll(c *Client) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for name := range l.waiting {
		l.dropWaiterLocked(name, c)
	}
	for name, h := range l.held {
		if h == c {
			delete(l.held, name)
			l.wake(name)
		}
	}
}

// holderName describes who holds a lock, for a message to a person.
func holderName(c *Client) string {
	if c == nil {
		return ""
	}
	return fmt.Sprintf("another client of %s", c.host.Name)
}
