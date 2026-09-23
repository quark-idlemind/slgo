package auth

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Throttle slows the answer to a caller that keeps getting the proof
// wrong.
//
// The secret is 256 bits when made as the guide says, and no rate of
// guessing reaches that.  But one typed by hand need not be, and
// without this a wrong proof cost a guesser one round trip and nothing
// else: Answer said no, and the caller could Begin again at once.
//
// # Who is counted
//
// Failures are counted by the address a connection came from -- the
// server's view of it, never anything the caller said -- and not by
// connection.  A count per connection is defeated by hanging up and
// dialling again, which costs nothing.  An address is not free to
// change in the same way, but a caller with many of them (an IPv6
// block is millions) can still spread its guesses out, so every
// failure is also counted daemon-wide, at a tenth of the weight: the
// daemon as a whole starts slowing down after thirty failures, where
// one address does after three.  Nobody who has the secret is slowed
// by either count unless it shares an address, or a daemon, with
// somebody who is guessing.
//
// # What is slowed
//
// After FreeFailures wrong answers, EVERY answer from that address is
// slowed, the right one too.  Slowing only the wrong answers would tell
// a guesser which was which by how soon each came back: it would stop
// waiting after a few milliseconds and take silence for "wrong".  The
// wait comes before the proof is checked, so that a caller that leaves
// during it has had no guess, and is not charged with one.
//
// The delay starts at half a second and doubles with each failure to a
// cap of four seconds.
//
// # What it costs to hold
//
// A delayed answer is a goroutine asleep, and a guesser opens as many
// as it likes.  So at most maxDelayed answers are waited on at once,
// daemon-wide, and at most maxDelayedPerPeer for any one address;
// beyond that a caller that would have been delayed is refused at once
// with ErrBusy, without its proof being looked at.  That is also what
// puts a bound on the rate of guessing, since a delay alone does not:
// a thousand callers waiting four seconds each is still two hundred and
// fifty guesses a second.  With the bounds, and the delay at its cap,
// it is one guess every two seconds from one address and four a second
// from everywhere together.
//
// The price is that somebody with the secret, arriving while the
// daemon-wide count is high and every place is taken, is refused too
// and must try again.  That is a guesser denying service, which anybody
// who can reach the port can do in other ways; what it cannot do is
// guess faster.
//
// No lock is held while waiting, so nothing about this slows a caller
// that is not being throttled.
//
// # When it forgets
//
// An address that proves it knows the secret is forgiven at once.  One
// that stops failing is forgotten after forgetAfter.  The daemon-wide
// count is not reset by anybody's success -- a guesser spreading its
// attempts over many addresses would otherwise be let off whenever a
// real client logged in -- and is forgotten the same way, after a
// quiet spell.
type Throttle struct {
	// Now and Sleep are the clock, for a test that should not have to
	// wait seconds to see a delay.  Nil means the real one.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error

	mu      sync.Mutex
	peers   map[string]*failures
	all     failures
	delayed int
}

type failures struct {
	count    int
	last     time.Time
	inflight int // attempts between Admit and done
	delayed  int // of those, how many were made to wait
}

const (
	// FreeFailures is how many wrong answers an address gets before
	// its answers are slowed.  A client whose secret was changed under
	// it fails once each time it is started, and a person trying it
	// again a couple of times should not be made to wait.
	FreeFailures = 3

	firstDelay = 500 * time.Millisecond
	maxDelay   = 4 * time.Second

	// allWeight is how many failures daemon-wide count as one from a
	// single address.
	allWeight = 10

	maxDelayed        = 16
	maxDelayedPerPeer = 2

	// maxPeers bounds how many addresses are remembered.  Past it a
	// new address is not recorded, which lets it off only its own
	// count: getting there takes thousands of failures, and the
	// daemon-wide count has long since slowed everybody down.
	maxPeers = 4096

	forgetAfter = 15 * time.Minute
)

// ErrBusy is a login refused because too many are already being slowed
// down.  The proof was not looked at.
var ErrBusy = errors.New("too many failed logins lately; try again in a few minutes")

// NewThrottle returns a throttle that has seen nothing.
func NewThrottle() *Throttle {
	return &Throttle{peers: map[string]*failures{}}
}

// Admit is called with a proof in hand and before checking it.  It
// returns once the caller has waited whatever it has earned, and done
// must then be called exactly once with whether the proof was good.
//
// It returns ErrBusy, or the context's error if the caller left while
// waiting, and in either case no proof may be checked.
//
// The attempt is counted as a failure here, before it is checked, and
// taken back if it turns out not to be one.  Counted afterwards, a
// thousand wrong answers sent at once would all arrive to find the
// count at nought and all be answered at once.
func (t *Throttle) Admit(ctx context.Context, peer string) (done func(ok bool), err error) {
	t.mu.Lock()
	now := t.now()
	p := t.peerLocked(peer, now)
	t.forgetLocked(&t.all, now)
	d := delayFor(t.all.count / allWeight)
	if p != nil {
		d = max(d, delayFor(p.count))
	} else {
		p = t.addPeerLocked(peer, now)
	}
	if d > 0 && (t.delayed >= maxDelayed || (p != nil && p.delayed >= maxDelayedPerPeer)) {
		t.dropIfIdleLocked(peer, p)
		t.mu.Unlock()
		return nil, ErrBusy
	}
	t.all.count++
	t.all.last = now
	if p != nil {
		p.count++
		p.last = now
		p.inflight++
	}
	if d > 0 {
		t.delayed++
		if p != nil {
			p.delayed++
		}
	}
	t.mu.Unlock()

	if d > 0 {
		if err := t.sleep(ctx, d); err != nil {
			// Gone before its proof was looked at, so it was not a
			// guess and is not held against anybody.
			t.finish(peer, p, true, refunded)
			return nil, err
		}
	}
	var once sync.Once
	return func(ok bool) {
		once.Do(func() {
			if ok {
				t.finish(peer, p, d > 0, proved)
			} else {
				t.finish(peer, p, d > 0, failed)
			}
		})
	}, nil
}

// How an attempt Admit let through ended.
type outcome int

const (
	failed   outcome = iota // a wrong proof: the charge stands
	refunded                // no proof was checked: the charge is taken back
	proved                  // the right one: the address is forgiven
)

// finish ends an attempt Admit let through: gives back its place among
// the delayed, and settles the failure it was charged with.
//
// The record is the one Admit counted against, carried rather than
// looked up again, and it cannot have been dropped in between: a record
// with an attempt in flight is never idle.
func (t *Throttle) finish(peer string, p *failures, slot bool, how outcome) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if slot {
		t.delayed--
	}
	if how != failed {
		// Daemon-wide, only the charge for this attempt is taken
		// back, even on a success; the failures before it stand.
		// See Throttle.
		t.all.count = max(t.all.count-1, 0)
	}
	if p == nil {
		return
	}
	p.inflight--
	if slot {
		p.delayed--
	}
	switch how {
	case refunded:
		p.count = max(p.count-1, 0)
	case proved:
		p.count = 0
	}
	t.dropIfIdleLocked(peer, p)
}

// addPeerLocked starts a record for an address, or returns nil if
// there is no room for one even after forgetting what is old enough.
func (t *Throttle) addPeerLocked(peer string, now time.Time) *failures {
	if len(t.peers) >= maxPeers {
		t.sweepLocked(now)
	}
	if len(t.peers) >= maxPeers {
		return nil
	}
	p := &failures{}
	t.peers[peer] = p
	return p
}

// peerLocked is what is known about an address, with anything old
// enough forgotten first.  Nil if nothing is.
func (t *Throttle) peerLocked(peer string, now time.Time) *failures {
	p := t.peers[peer]
	if p == nil {
		return nil
	}
	t.forgetLocked(p, now)
	return p
}

func (t *Throttle) forgetLocked(f *failures, now time.Time) {
	if f.count > 0 && now.Sub(f.last) >= forgetAfter {
		f.count = 0
	}
}

func (t *Throttle) dropIfIdleLocked(peer string, p *failures) {
	if p != nil && p.count == 0 && p.inflight == 0 {
		delete(t.peers, peer)
	}
}

func (t *Throttle) sweepLocked(now time.Time) {
	for peer, p := range t.peers {
		t.forgetLocked(p, now)
		t.dropIfIdleLocked(peer, p)
	}
}

// delayFor is the wait earned by a count of failures.
func delayFor(n int) time.Duration {
	if n < FreeFailures {
		return 0
	}
	d := firstDelay
	for i := FreeFailures; i < n && d < maxDelay; i++ {
		d *= 2
	}
	return min(d, maxDelay)
}

func (t *Throttle) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t *Throttle) sleep(ctx context.Context, d time.Duration) error {
	if t.Sleep != nil {
		return t.Sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
