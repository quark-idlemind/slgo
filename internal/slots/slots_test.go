package slots

// What a pool decides is invisible from outside except as who got what,
// which is why every one of these is about two callers rather than one.
// A pool that hands the same slot to both, or holds slots for a caller
// that was never served, or leaves a waiter asleep through the arrival
// of what it wanted, looks from a single caller exactly like a pool that
// works.
//
// Nothing here sleeps.  Time passes when ExpiryCheck says it does, which
// is the point of the pool not owning a clock: a lease can be run out
// deliberately rather than waited out, and a test that took a second per
// expiry would be a test nobody runs.

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// clock is time as the pool sees it, which a test moves by hand.
//
// The pool reads a clock but owns no timer, which is what makes a whole
// lease -- granted, run out, taken back -- something a test can do in no
// time at all rather than something it has to sit through.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *clock) pass(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// running is a pool with n slots in it, already going, numbered from 1
// so that a test can say which it expected.
func running(t *testing.T, n int) (*Pool, *clock) {
	t.Helper()
	p, c := stopped(t)
	go p.Run()

	slots := make([]*Slot, 0, n)
	for i := 1; i <= n; i++ {
		slots = append(slots, &Slot{Data: i})
	}
	if err := p.Add(slots...); err != nil {
		t.Fatalf("Add: %v", err)
	}
	return p, c
}

// stopped is a pool that has not been started, for the tests that want
// to set something on it first.
func stopped(t *testing.T) (*Pool, *clock) {
	t.Helper()
	// A time of its own, so that a lease reaching its end is something
	// this test did rather than something that happened to it.
	c := &clock{at: time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)}
	p := New()
	p.now = c.now
	t.Cleanup(func() { p.Close() })
	return p, c
}

// which is the Data of what was granted, as a set.
func which(r Response) map[int]bool {
	out := map[int]bool{}
	for _, s := range r.Slots {
		out[s.Data.(int)] = true
	}
	return out
}

// get asks and fails the test if the pool would not answer at all.
func get(t *testing.T, p *Pool, n int) Response {
	t.Helper()
	r, err := p.Get(n, time.Minute)
	if err != nil {
		t.Fatalf("Get(%d): %v", n, err)
	}
	return r
}

// TestAllOfThemOrNoneOfThem: the whole contract.  A caller given four of
// the eight it asked for can only hold them while waiting for the rest,
// which is the deadlock, or give them back -- so it is never given them.
func TestAllOfThemOrNoneOfThem(t *testing.T) {
	p, _ := running(t, 8)

	first := get(t, p, 5)
	if !first.Filled() || len(first.Slots) != 5 {
		t.Fatalf("a request for 5 from 8 got %d slots, filled=%v",
			len(first.Slots), first.Filled())
	}

	second := get(t, p, 5)
	if second.Filled() {
		t.Errorf("5 more were granted out of the 3 that were left: %v", which(second))
	}
	if len(second.Slots) != 0 {
		t.Errorf("a request that was not filled came back holding %d slots",
			len(second.Slots))
	}
	if second.Wait == nil {
		t.Error("a request that was not filled has nothing to wait on")
	}

	// And the three it did not take are still there for somebody who can
	// use them: a refusal must not quietly hold what it could not use.
	third := get(t, p, 3)
	if !third.Filled() {
		t.Error("the three left over went nowhere after a refusal")
	}
}

// TestNoSlotIsGrantedTwice: the failure this exists to prevent, and the
// one that does not look like a failure -- two runs in one object
// overwrite each other's script and each other's data, and both report
// success.
func TestNoSlotIsGrantedTwice(t *testing.T) {
	p, _ := running(t, 6)

	a := get(t, p, 3)
	b := get(t, p, 3)
	if !a.Filled() || !b.Filled() {
		t.Fatal("six slots would not go to two callers wanting three each")
	}
	for slot := range which(a) {
		if which(b)[slot] {
			t.Errorf("slot %d was granted to both callers", slot)
		}
	}
	if len(which(a))+len(which(b)) != 6 {
		t.Errorf("%d distinct slots between two grants of three",
			len(which(a))+len(which(b)))
	}
}

// TestWhatIsGivenBackCanBeHadAgain: Return is the ordinary way slots
// come round again, and a pool that files them somewhere other than
// where it looks for them drains until it is empty and never says so.
func TestWhatIsGivenBackCanBeHadAgain(t *testing.T) {
	p, _ := running(t, 4)

	first := get(t, p, 4)
	if !first.Filled() {
		t.Fatal("four slots would not go to one caller wanting four")
	}
	if err := p.Return(first.ID); err != nil {
		t.Fatalf("Return: %v", err)
	}

	second := get(t, p, 4)
	if !second.Filled() {
		t.Fatal("the four that were given back could not be had again")
	}
	if len(which(second)) != 4 {
		t.Errorf("%d distinct slots came back out of four returned", len(which(second)))
	}

	// Twice is not an error and does not put anything back a second
	// time: a caller that returns from a defer and again by hand is
	// ordinary.
	if err := p.Return(first.ID); err != nil {
		t.Errorf("returning twice: %v", err)
	}
	if r := get(t, p, 1); r.Filled() {
		t.Error("returning a grant twice put its slots back twice")
	}
}

// TestAWaiterIsWokenByAReturn: what the waiting channel is for.  Without
// it a caller that could not be served has to poll, and a poll is either
// too slow or a busy loop.
func TestAWaiterIsWokenByAReturn(t *testing.T) {
	p, _ := running(t, 4)

	held := get(t, p, 4)
	short := get(t, p, 4)
	if short.Filled() {
		t.Fatal("four slots went to two callers at once")
	}

	select {
	case <-short.Wait:
		t.Fatal("the waiter was woken before anything had changed")
	default:
	}

	if err := p.Return(held.ID); err != nil {
		t.Fatalf("Return: %v", err)
	}
	select {
	case <-short.Wait:
	case <-time.After(time.Second):
		t.Fatal("a return did not wake the caller waiting for it")
	}
}

// TestAWaiterIsWokenByAnArrival: slots arrive as well as come back -- an
// avatar logs in, or somebody sets up more objects -- and a waiter that
// slept through the arrival of exactly what it wanted would wait for the
// next return instead, which may be a benchmark away.
func TestAWaiterIsWokenByAnArrival(t *testing.T) {
	p, _ := running(t, 2)

	short := get(t, p, 4)
	if short.Filled() {
		t.Fatal("four slots came out of a pool of two")
	}

	if err := p.Add(&Slot{Data: 3}, &Slot{Data: 4}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	select {
	case <-short.Wait:
	case <-time.After(time.Second):
		t.Fatal("slots arriving did not wake the caller waiting for them")
	}

	if r := get(t, p, 4); !r.Filled() {
		t.Error("the four that were then there could not be had")
	}
}

// TestEverybodyWaitingIsWokenAtOnce: one channel serves every waiter,
// because what it says is "something has changed" rather than "this is
// yours".  Waking one and leaving the other asleep would mean a caller
// waiting for a change that has already happened.
func TestEverybodyWaitingIsWokenAtOnce(t *testing.T) {
	p, _ := running(t, 2)

	held := get(t, p, 2)
	one := get(t, p, 2)
	two := get(t, p, 2)
	if one.Filled() || two.Filled() {
		t.Fatal("two slots went to three callers")
	}
	if one.Wait != two.Wait {
		t.Error("two waiters were given different channels to wait on")
	}

	p.Return(held.ID)
	for i, r := range []Response{one, two} {
		select {
		case <-r.Wait:
		case <-time.After(time.Second):
			t.Errorf("waiter %d was not woken", i)
		}
	}
}

// TestAGrantThatRanOutIsTakenBackWhenSomebodyAsks: the backstop for a
// caller that wedges without dying.  A request looks at what has run out
// before it gives up, so slots held by nobody are not left out of use
// waiting for whoever drives the clock to get round to it.
func TestAGrantThatRanOutIsTakenBackWhenSomebodyAsks(t *testing.T) {
	p, c := running(t, 4)

	if held := get(t, p, 4); !held.Filled() {
		t.Fatal("four slots would not go to one caller")
	}
	if r := get(t, p, 1); r.Filled() {
		t.Fatal("a slot was handed out from under a grant that was still good")
	}

	// Past the deadline the caller was given, and past the grace on top
	// of it.
	c.pass(time.Minute + grace + time.Second)

	if r := get(t, p, 4); !r.Filled() {
		t.Error("a grant that had run out was not taken back for somebody who asked")
	}
}

// TestAnExpiryCheckWakesWhoeverIsWaiting: the reason this is a request
// at all rather than something a Get does on the way past.  A caller
// already waiting is not asking, so without something to say time has
// passed it sleeps through the end of the very grant it is waiting for
// -- and the pool holds no timer of its own to tell it.
func TestAnExpiryCheckWakesWhoeverIsWaiting(t *testing.T) {
	p, c := running(t, 4)

	if held := get(t, p, 4); !held.Filled() {
		t.Fatal("four slots would not go to one caller")
	}
	short := get(t, p, 4)
	if short.Filled() {
		t.Fatal("four slots went to two callers at once")
	}

	c.pass(time.Minute + grace + time.Second)
	select {
	case <-short.Wait:
		t.Fatal("the waiter was woken by time passing, which nothing had noticed yet")
	default:
	}

	if err := p.ExpiryCheck(); err != nil {
		t.Fatalf("ExpiryCheck: %v", err)
	}
	select {
	case <-short.Wait:
	case <-time.After(time.Second):
		t.Fatal("a grant running out did not wake the caller waiting for it")
	}
	if r := get(t, p, 4); !r.Filled() {
		t.Error("the grant that ran out was woken about but not taken back")
	}
}

// TestTheGraceIsOnTopOfWhatTheCallerWasTold: the caller is told when its
// grant runs out and must have FINISHED by then; the pool waits a little
// longer before re-letting, to cover the gap between a caller looking at
// its deadline and its write arriving.  A pool that re-let at the moment
// it named would make that check useless.
func TestTheGraceIsOnTopOfWhatTheCallerWasTold(t *testing.T) {
	p, c := running(t, 2)

	held := get(t, p, 2)
	if !held.Filled() {
		t.Fatal("two slots would not go to one caller")
	}

	// Past the deadline the caller was told, and not past the grace.
	c.pass(time.Minute + time.Second)
	if !held.Expire.Before(c.now()) {
		t.Fatalf("Expire = %v, which has not passed at %v", held.Expire, c.now())
	}

	p.ExpiryCheck()
	if r := get(t, p, 1); r.Filled() {
		t.Error("a grant was re-let the moment the caller's deadline passed, " +
			"leaving no room between a caller checking it and writing")
	}
}

// TestRenewIsForWorkThatCannotSayHowLongItWillTake: a benchmark's search
// finishes when it converges.  Without renewal every caller has to guess
// a duration and guess high, and callers guessing high is what makes
// expiry useless as a way of getting objects back.
func TestRenewIsForWorkThatCannotSayHowLongItWillTake(t *testing.T) {
	p, c := running(t, 2)

	held := get(t, p, 2)
	c.pass(time.Minute + time.Second) // its deadline has passed
	until, err := p.Renew(held.ID, time.Hour)
	if err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if !until.After(held.Expire) {
		t.Errorf("Renew = %v, which is no later than the deadline it replaced (%v)",
			until, held.Expire)
	}

	p.ExpiryCheck()
	if r := get(t, p, 1); r.Filled() {
		t.Error("a renewed grant was taken back anyway")
	}
}

// TestRenewingWhatIsGoneSaysSo: a grant that ran out is not an error the
// pool made -- the caller was told when it would -- but the caller has
// to find out, because carrying on would mean writing into an object
// somebody else now has.
func TestRenewingWhatIsGoneSaysSo(t *testing.T) {
	p, c := running(t, 1)

	held := get(t, p, 1)
	c.pass(time.Minute + grace + time.Second)
	p.ExpiryCheck()

	if _, err := p.Renew(held.ID, time.Hour); !errors.Is(err, ErrNoLease) {
		t.Errorf("Renew of a grant that ran out = %v, want ErrNoLease", err)
	}
	if _, err := p.Renew(ID{}, time.Hour); !errors.Is(err, ErrNoLease) {
		t.Errorf("Renew of nothing = %v, want ErrNoLease", err)
	}
}

// TestWakeSaysWhenAnExpiryCheckWouldBeWorthMaking: the pool owns no
// clock, so somebody else has to arm the timer -- and a fixed tick is
// either wasted wakeups or slots left out of use.  The earliest deadline
// is the only time worth knowing.
func TestWakeSaysWhenAnExpiryCheckWouldBeWorthMaking(t *testing.T) {
	p, c := running(t, 4)

	if _, err := p.Get(2, time.Hour); err != nil {
		t.Fatalf("Get: %v", err)
	}
	select {
	case at := <-p.Wake():
		if !at.After(c.now().Add(time.Minute)) {
			t.Errorf("Wake = %v for a grant of an hour made at %v", at, c.now())
		}
	case <-time.After(time.Second):
		t.Fatal("nothing was said about when to check a grant that was made")
	}

	// A shorter grant replaces it: what is wanted is the FIRST one that
	// could be taken back, not the last one granted.
	if _, err := p.Get(2, time.Minute); err != nil {
		t.Fatalf("Get: %v", err)
	}
	select {
	case at := <-p.Wake():
		if at.After(c.now().Add(time.Hour)) {
			t.Errorf("Wake = %v, want the earlier of the two deadlines", at)
		}
	case <-time.After(time.Second):
		t.Fatal("a nearer deadline was not offered")
	}
}

// TestASlotThatWentAwayIsNotHandedOut: the pool cannot know whether an
// object is still worn -- it can be taken off, deleted, or left behind
// by an avatar logging out -- so it asks, and what it is told is the
// difference between a run that fails at once and a run that fails in
// the middle with half its scripts installed.
func TestASlotThatWentAwayIsNotHandedOut(t *testing.T) {
	p, _ := stopped(t)
	gone := map[int]bool{2: true, 3: true}
	p.Present = func(data any) bool { return !gone[data.(int)] }
	go p.Run()

	if err := p.Add(&Slot{Data: 1}, &Slot{Data: 2}, &Slot{Data: 3}, &Slot{Data: 4}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if r := get(t, p, 3); r.Filled() {
		t.Errorf("three slots came out of a pool with two of them gone: %v", which(r))
	}
	r := get(t, p, 2)
	if !r.Filled() {
		t.Fatal("the two that were still there could not be had")
	}
	for slot := range which(r) {
		if gone[slot] {
			t.Errorf("slot %d was handed out after it had gone away", slot)
		}
	}
}

// TestASlotTakenOutWhileInUseGoesWhenItComesBack: an avatar logging out
// takes its objects with it, and the run holding them will find that out
// from its own session.  Pulling them from under it here would not help
// it and would let somebody else have an object that is not there.
func TestASlotTakenOutWhileInUseGoesWhenItComesBack(t *testing.T) {
	p, _ := stopped(t)
	go p.Run()

	one, two := &Slot{Data: 1}, &Slot{Data: 2}
	if err := p.Add(one, two); err != nil {
		t.Fatalf("Add: %v", err)
	}

	held := get(t, p, 2)
	if !held.Filled() {
		t.Fatal("two slots would not go to one caller")
	}
	if err := p.Remove(one); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := p.Return(held.ID); err != nil {
		t.Fatalf("Return: %v", err)
	}

	r := get(t, p, 2)
	if r.Filled() {
		t.Errorf("a slot that was taken out came back into the pool: %v", which(r))
	}
	r = get(t, p, 1)
	if !r.Filled() || !which(r)[2] {
		t.Errorf("the slot that was NOT taken out did not come back: %v", which(r))
	}
}

// TestSlotsAreHandedOutOldestFirst: the pool knows nothing about
// avatars, but the daemon adds one avatar's objects before the next -- so
// oldest-first is what keeps a run that fits on one avatar on one
// avatar, without this package having to hear the word.
func TestSlotsAreHandedOutOldestFirst(t *testing.T) {
	p, _ := running(t, 8)

	for want := 1; want <= 8; want += 2 {
		r := get(t, p, 2)
		if !r.Filled() {
			t.Fatalf("a pair could not be had with %d slots left", 9-want)
		}
		got := which(r)
		if !got[want] || !got[want+1] {
			t.Errorf("got %v, want %d and %d -- the oldest that were free",
				got, want, want+1)
		}
	}
}

// TestARemovedSlotAddedAgainIsANewSlot: an avatar that logged out and
// back in wears the same items, and the objects they made are new ones
// with new keys.  Treating that as the same slot would mean a Return
// from before the logout putting an object back that no longer exists.
func TestARemovedSlotAddedAgainIsANewSlot(t *testing.T) {
	p, _ := stopped(t)
	go p.Run()

	s := &Slot{Data: 1}
	p.Add(s)
	first := get(t, p, 1)
	was := first.Slots[0].ID()

	p.Remove(s)
	p.Return(first.ID)
	p.Add(s)

	again := get(t, p, 1)
	if !again.Filled() {
		t.Fatal("a slot added again could not be had")
	}
	if again.Slots[0].ID() == was {
		t.Errorf("the slot came back as %v, the ID it had before it went away", was)
	}
}

// TestAskingForNothing: a request for no slots is a caller's mistake and
// not a grant of nothing, which would be a grant that cannot be
// returned and holds nothing.
func TestAskingForNothing(t *testing.T) {
	p, _ := running(t, 2)
	for _, n := range []int{0, -1} {
		if _, err := p.Get(n, time.Minute); err == nil {
			t.Errorf("Get(%d) was answered rather than refused", n)
		}
	}
}

// TestAStoppedPoolAnswersRatherThanHangs: Run returning leaves a channel
// nobody reads, and every caller after it would wait for an answer that
// is not coming.  A program shutting down is exactly when that would be
// hardest to see.
func TestAStoppedPoolAnswersRatherThanHangs(t *testing.T) {
	p, _ := running(t, 2)
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := p.Get(1, time.Minute); !errors.Is(err, ErrClosed) {
			t.Errorf("Get after Close = %v, want ErrClosed", err)
		}
		if err := p.Add(&Slot{}); !errors.Is(err, ErrClosed) {
			t.Errorf("Add after Close = %v, want ErrClosed", err)
		}
		if err := p.Return(ID{}); !errors.Is(err, ErrClosed) {
			t.Errorf("Return after Close = %v, want ErrClosed", err)
		}
		if err := p.ExpiryCheck(); !errors.Is(err, ErrClosed) {
			t.Errorf("ExpiryCheck after Close = %v, want ErrClosed", err)
		}
		if _, err := p.Renew(ID{}, time.Minute); !errors.Is(err, ErrClosed) {
			t.Errorf("Renew after Close = %v, want ErrClosed", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a call to a stopped pool hung")
	}
}

// TestClosingWakesWhoeverIsWaiting: a caller waiting for slots that are
// never coming has to be let go, or a program shutting down waits for
// itself.
func TestClosingWakesWhoeverIsWaiting(t *testing.T) {
	p, _ := running(t, 1)

	get(t, p, 1)
	short := get(t, p, 1)
	if short.Filled() {
		t.Fatal("one slot went to two callers")
	}

	p.Close()
	select {
	case <-short.Wait:
	case <-time.After(time.Second):
		t.Error("closing the pool left a caller waiting for slots that are not coming")
	}
}

// TestTheDefaultDeadlineIsUsedWhenNobodySays: a caller that does not
// care still gets a deadline, because the point of the lease is the
// caller that wedged and cannot be asked.
func TestTheDefaultDeadlineIsUsedWhenNobodySays(t *testing.T) {
	p, c := running(t, 2)

	r, err := p.Get(2, 0)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if want := c.now().Add(DefaultTimeout); !r.Expire.Equal(want) {
		t.Errorf("Expire = %v with no timeout asked for, want %v", r.Expire, want)
	}

	until, err := p.Renew(r.ID, 0)
	if err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if want := c.now().Add(DefaultTimeout); !until.Equal(want) {
		t.Errorf("Renew = %v with no timeout asked for, want %v", until, want)
	}
}

// TestASlotTakenOutWhileInUseGoesWhenItsGrantRunsOut: the same as
// returning it, by the other road.  An avatar logs out and the run
// holding its objects wedges rather than tidying up, and the slot must
// not come back into a pool where the object it stands for is gone.
func TestASlotTakenOutWhileInUseGoesWhenItsGrantRunsOut(t *testing.T) {
	p, c := stopped(t)
	go p.Run()

	one, two := &Slot{Data: 1}, &Slot{Data: 2}
	if err := p.Add(one, two); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if held := get(t, p, 2); !held.Filled() {
		t.Fatal("two slots would not go to one caller")
	}
	if err := p.Remove(one); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	c.pass(time.Minute + grace + time.Second)
	if err := p.ExpiryCheck(); err != nil {
		t.Fatalf("ExpiryCheck: %v", err)
	}

	if r := get(t, p, 2); r.Filled() {
		t.Errorf("a slot that was taken out came back when its grant ran out: %v",
			which(r))
	}
	if r := get(t, p, 1); !r.Filled() || !which(r)[2] {
		t.Errorf("the slot that was NOT taken out did not come back: %v", which(r))
	}
}

// TestAnIDSaysWhichSlotAndWhetherItIsOne: IDs are printed in logs and
// compared against the zero one, which is what a caller has before it
// has been given anything.
func TestAnIDSaysWhichSlotAndWhetherItIsOne(t *testing.T) {
	p, _ := running(t, 1)

	var none ID
	if !none.IsZero() {
		t.Error("the ID nothing has does not say so")
	}
	r := get(t, p, 1)
	if r.ID.IsZero() {
		t.Error("a grant was given the ID nothing has")
	}
	if r.Slots[0].ID().IsZero() {
		t.Error("a slot was given the ID nothing has")
	}
	if got := r.ID.String(); got == "" || got == none.String() {
		t.Errorf("ID.String() = %q, which does not tell two apart", got)
	}
}
