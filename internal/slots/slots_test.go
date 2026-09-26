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
//
// It is the package's clock rather than one pool's, so nothing here runs
// in parallel and each test puts it back.
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
	was := timeNow
	timeNow = c.now
	p := New()
	t.Cleanup(func() {
		p.Close()
		timeNow = was
	})
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

// TestASlotThatIsNotThereForNowIsKept: an avatar that is logged out has
// not gone for good -- hosting it again brings it back wearing the same
// things -- so a slot Present refuses is passed over and kept, and is
// handed out, in its place in the order, once it is there again.
func TestASlotThatIsNotThereForNowIsKept(t *testing.T) {
	p, _ := stopped(t)
	var (
		mu   sync.Mutex
		away = true
	)
	p.Present = func(data any) bool {
		mu.Lock()
		defer mu.Unlock()
		return data.(int) != 1 || !away
	}
	go p.Run()

	if err := p.Add(&Slot{Data: 1}, &Slot{Data: 2}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if r := get(t, p, 2); r.Filled() {
		t.Errorf("two slots came out while one of them was away: %v", which(r))
	}
	held := get(t, p, 1)
	if !held.Filled() || !which(held)[2] {
		t.Fatalf("the slot that was there could not be had: %v", which(held))
	}
	if err := p.Return(held.ID); err != nil {
		t.Fatalf("Return: %v", err)
	}

	mu.Lock()
	away = false
	mu.Unlock()
	if r := get(t, p, 1); !r.Filled() || !which(r)[1] {
		t.Errorf("got %v once slot 1 was back, want slot 1, the oldest", which(r))
	}
}

// TestHeldSaysWhichSlotsAGrantHas: an owner putting new slots in place of
// ones it removed has to know when the old ones have gone, or the same
// object would be in two callers' hands.  A grant that has run out holds
// nothing, whether or not an ExpiryCheck has noticed yet.
func TestHeldSaysWhichSlotsAGrantHas(t *testing.T) {
	p, c := stopped(t)
	go p.Run()

	one, two, three := &Slot{Data: 1}, &Slot{Data: 2}, &Slot{Data: 3}
	if err := p.Add(one, two, three); err != nil {
		t.Fatalf("Add: %v", err)
	}
	first, second := get(t, p, 1), get(t, p, 1)
	if !first.Filled() || !second.Filled() {
		t.Fatal("two callers could not have one slot each")
	}
	if err := p.Remove(one, two, three); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	held := func() map[int]bool {
		t.Helper()
		got, err := p.Held(one, two, three)
		if err != nil {
			t.Fatalf("Held: %v", err)
		}
		out := map[int]bool{}
		for _, s := range got {
			out[s.Data.(int)] = true
		}
		return out
	}
	if got := held(); len(got) != 2 || !got[1] || !got[2] {
		t.Errorf("Held = %v with slots 1 and 2 granted, want 1 and 2", got)
	}
	if err := p.Return(first.ID); err != nil {
		t.Fatalf("Return: %v", err)
	}
	if got := held(); len(got) != 1 || !got[2] {
		t.Errorf("Held = %v once slot 1 was given back, want 2", got)
	}
	c.pass(time.Minute + grace + time.Second)
	if got := held(); len(got) != 0 {
		t.Errorf("Held = %v once the last grant had run out, want nothing", got)
	}
}

// TestAWaiterIsWokenWhenARemovedSlotHasGone: nothing reaches the free
// list, but the owner may have been waiting for exactly this to put a
// new slot in the old one's place, and a caller waiting for that has to
// ask again to find it.  By either road: given back, or run out.
func TestAWaiterIsWokenWhenARemovedSlotHasGone(t *testing.T) {
	for _, road := range []string{"given back", "run out"} {
		t.Run(road, func(t *testing.T) {
			p, c := stopped(t)
			go p.Run()

			s := &Slot{Data: 1}
			if err := p.Add(s); err != nil {
				t.Fatalf("Add: %v", err)
			}
			held := get(t, p, 1)
			short := get(t, p, 1)
			if !held.Filled() || short.Filled() {
				t.Fatal("one slot did not go to exactly one of two callers")
			}
			if err := p.Remove(s); err != nil {
				t.Fatalf("Remove: %v", err)
			}

			if road == "given back" {
				if err := p.Return(held.ID); err != nil {
					t.Fatalf("Return: %v", err)
				}
			} else {
				c.pass(time.Minute + grace + time.Second)
				if err := p.ExpiryCheck(); err != nil {
					t.Fatalf("ExpiryCheck: %v", err)
				}
			}
			select {
			case <-short.Wait:
			case <-time.After(2 * time.Second):
				t.Fatal("the waiter was not woken when the removed slot finally went")
			}
			if r := get(t, p, 1); r.Filled() {
				t.Errorf("a removed slot came back: %v", which(r))
			}
		})
	}
}

// TestASlotTakenOutWhileInUseGoesWhenItComesBack: an avatar leaving the
// daemon takes its objects with it, and the run holding them will find
// that out from its own session.  Pulling them from under it here would not help
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

// TestARemovedSlotAddedAgainIsANewSlot: an avatar that left the daemon
// and was hosted again wears the same items, and the objects they made
// are new ones with new keys.  Treating that as the same slot would mean
// a Return from before it left putting an object back that no longer
// exists.
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
// returning it, by the other road.  An avatar leaves the daemon and the
// run holding its objects wedges rather than tidying up, and the slot
// must not come back into a pool where the object it stands for is gone.
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

// TestWhatComesBackGoesBackWhereItWas: the free slots are kept oldest
// first, so a grant that is returned has to go back among them rather
// than on the end.  Otherwise the order decays with every run until the
// oldest-first promise means nothing.
func TestWhatComesBackGoesBackWhereItWas(t *testing.T) {
	p, _ := running(t, 4)

	first := get(t, p, 2) // 1 and 2, the oldest
	if !first.Filled() {
		t.Fatal("two slots would not go to one caller")
	}
	if err := p.Return(first.ID); err != nil {
		t.Fatalf("Return: %v", err)
	}

	// 3 and 4 were never taken and are now the younger ones, so the two
	// that came back have to be handed out before them.
	r := get(t, p, 2)
	if !r.Filled() {
		t.Fatal("the returned pair could not be had")
	}
	if got := which(r); !got[1] || !got[2] {
		t.Errorf("got %v after returning 1 and 2, want them back before 3 and 4", got)
	}
}

// TestASlotRemovedWhileNobodyHasItGoesAtOnce: an avatar leaving the
// daemon while its objects are idle.  There is nobody to wait for, so the slot
// goes now -- and the ones around it keep their order.
func TestASlotRemovedWhileNobodyHasItGoesAtOnce(t *testing.T) {
	p, _ := stopped(t)
	go p.Run()

	slots := []*Slot{{Data: 1}, {Data: 2}, {Data: 3}, {Data: 4}}
	if err := p.Add(slots...); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := p.Remove(slots[1]); err != nil { // the second, from the middle
		t.Fatalf("Remove: %v", err)
	}

	if r := get(t, p, 4); r.Filled() {
		t.Errorf("four slots came out of a pool of three: %v", which(r))
	}
	r := get(t, p, 3)
	if !r.Filled() {
		t.Fatal("the three that were left could not be had")
	}
	if got := which(r); got[2] {
		t.Errorf("got %v, and slot 2 was removed", got)
	}
	// Taken from the middle without disturbing what was on either side.
	if r.Slots[0].Data != 1 || r.Slots[1].Data != 3 || r.Slots[2].Data != 4 {
		t.Errorf("got %v, %v, %v, want 1, 3, 4 in that order",
			r.Slots[0].Data, r.Slots[1].Data, r.Slots[2].Data)
	}
}

// ------------------------------------------------------- tidying up

// TestASlotIsNotHandedOnUntilItHasBeenTidied: the whole point of the
// hook.  For an object that a script ran in, tidying is stopping that
// script from talking -- and a slot handed on before it has stopped is a
// caller reading somebody else's output as its own, which is measured
// behaviour and not a worry: chat carries the object a line came from
// and never the script's name.
func TestASlotIsNotHandedOnUntilItHasBeenTidied(t *testing.T) {
	p, _ := stopped(t)
	go p.Run()

	tidying := make(chan struct{})
	release := make(chan struct{})
	s := &Slot{Data: 1, Clean: func(s *Slot) {
		close(tidying)
		<-release
		p.Cleaned(s)
	}}
	if err := p.Add(s); err != nil {
		t.Fatalf("Add: %v", err)
	}

	held := get(t, p, 1)
	if !held.Filled() {
		t.Fatal("the one slot would not go to one caller")
	}
	if err := p.Return(held.ID); err != nil {
		t.Fatalf("Return: %v", err)
	}
	<-tidying

	if r := get(t, p, 1); r.Filled() {
		t.Error("a slot was handed on while it was still being tidied")
	}
	close(release)

	if !waitFor(t, p, 1) {
		t.Error("a slot that had been tidied never came back")
	}
}

// waitFor asks until n slots can be had, and says whether they ever
// could.  A slot comes back from its tidying on a goroutine of its own,
// so there is a moment between the tidying finishing and the pool having
// heard about it.
func waitFor(t *testing.T, p *Pool, n int) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r := get(t, p, n); r.Filled() {
			return true
		}
	}
	return false
}

// TestTidyingDoesNotStopThePoolAnsweringEverybodyElse: tidying goes to
// the grid -- a read, a write, and waiting to hear the object stop -- so
// a pool that ran it on its own goroutine would answer nobody for a
// second or more each time a slot came back, and eight at once would
// stop everything for eight seconds.
func TestTidyingDoesNotStopThePoolAnsweringEverybodyElse(t *testing.T) {
	p, _ := stopped(t)
	go p.Run()

	tidying := make(chan struct{})
	release := make(chan struct{})
	slow := &Slot{Data: 1, Clean: func(s *Slot) {
		close(tidying)
		<-release
		p.Cleaned(s)
	}}
	if err := p.Add(slow, &Slot{Data: 2}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	first := get(t, p, 1) // the oldest, which is the slow one
	if !first.Filled() || !which(first)[1] {
		t.Fatalf("got %v, want the slot that tidies slowly", which(first))
	}

	// Returning does not wait for the tidying either: the caller has
	// finished with the slot and has nothing to do with what happens to
	// it next.
	returned := make(chan error, 1)
	go func() { returned <- p.Return(first.ID) }()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("Return: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Return waited for the tidying it set going")
	}
	<-tidying

	// The pool is mid-tidy.  Everything else still works.
	answered := make(chan bool, 1)
	go func() {
		r, err := p.Get(1, time.Minute)
		answered <- err == nil && r.Filled() && which(r)[2]
	}()
	select {
	case ok := <-answered:
		if !ok {
			t.Error("the other slot could not be had while one was being tidied")
		}
	case <-time.After(2 * time.Second):
		t.Error("the pool answered nobody while a slot was being tidied")
	}
	close(release)
}

// TestASlotCleanNeverSpeaksForIsGone: an object that has gone, or will
// not answer, needs no error and no telling.  Clean looks, finds nothing
// to hand on, and says nothing -- and the slot is out of the pool
// because it was never put back.
func TestASlotCleanNeverSpeaksForIsGone(t *testing.T) {
	p, _ := stopped(t)
	go p.Run()

	looked := make(chan struct{})
	bad := &Slot{Data: 1, Clean: func(*Slot) {
		// Nothing to tidy and nothing to hand on.
		close(looked)
	}}
	good := &Slot{Data: 2, Clean: func(s *Slot) { p.Cleaned(s) }}
	if err := p.Add(bad, good); err != nil {
		t.Fatalf("Add: %v", err)
	}

	held := get(t, p, 2)
	if !held.Filled() {
		t.Fatal("two slots would not go to one caller")
	}
	if err := p.Return(held.ID); err != nil {
		t.Fatalf("Return: %v", err)
	}
	<-looked

	if !waitFor(t, p, 1) {
		t.Fatal("the slot that came clean never came back")
	}
	if r := get(t, p, 1); r.Filled() {
		t.Errorf("the slot nothing spoke for was handed on: %v", which(r))
	}
}

// TestASlotCannotBeHandedBackTwice: the one mistake that must not get
// through.  A Clean with a bug, or one that gave up and then found both
// its attempts had worked, calls Cleaned twice -- and the same slot in
// the free list twice is one object handed to two callers, which is what
// all of this exists to prevent.
func TestASlotCannotBeHandedBackTwice(t *testing.T) {
	p, _ := stopped(t)
	go p.Run()

	s := &Slot{Data: 1}
	if err := p.Add(s); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Twice from the free list, twice from a tidying, and once for a
	// slot the pool never had.
	if err := p.Cleaned(s); err != nil {
		t.Fatalf("Cleaned: %v", err)
	}
	if err := p.Cleaned(&Slot{Data: 99}); err != nil {
		t.Fatalf("Cleaned: %v", err)
	}

	first := get(t, p, 1)
	if !first.Filled() {
		t.Fatal("the one slot would not go to one caller")
	}
	if r := get(t, p, 1); r.Filled() {
		t.Errorf("a second caller was given a slot as well: %v", which(r))
	}
}

// TestTidyingIsNotSomethingTheCallerIsHanded: the hook is the owner's,
// and a copy of it in the hands of whoever holds the slot is a way for
// the tidying to happen twice, or at the wrong moment, or by somebody
// who does not know what tidying this thing needs.
func TestTidyingIsNotSomethingTheCallerIsHanded(t *testing.T) {
	p, _ := stopped(t)
	go p.Run()

	if err := p.Add(&Slot{Data: 1, Clean: func(*Slot) {}}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	r := get(t, p, 1)
	if !r.Filled() {
		t.Fatal("the one slot would not go to one caller")
	}
	if r.Slots[0].Clean != nil {
		t.Error("the slot handed to the caller carries the owner's tidying")
	}
}

// TestAWaiterIsWokenWhenTheTidyingIsDoneAndNotBefore: a slot given back
// is not a slot anybody can have yet, so waking a waiter then would send
// it to look at a pool that has nothing in it.  The wake belongs at the
// end of the tidying.
func TestAWaiterIsWokenWhenTheTidyingIsDoneAndNotBefore(t *testing.T) {
	p, _ := stopped(t)
	go p.Run()

	tidying := make(chan struct{})
	release := make(chan struct{})
	s := &Slot{Data: 1, Clean: func(s *Slot) {
		close(tidying)
		<-release
		p.Cleaned(s)
	}}
	if err := p.Add(s); err != nil {
		t.Fatalf("Add: %v", err)
	}

	held := get(t, p, 1)
	short := get(t, p, 1)
	if short.Filled() {
		t.Fatal("one slot went to two callers")
	}
	if err := p.Return(held.ID); err != nil {
		t.Fatalf("Return: %v", err)
	}
	<-tidying

	select {
	case <-short.Wait:
		t.Fatal("the waiter was woken before the slot was fit to be had")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	select {
	case <-short.Wait:
	case <-time.After(2 * time.Second):
		t.Fatal("the waiter was not woken when the tidying finished")
	}
}

// TestASlotRemovedWhileBeingTidiedDoesNotComeBack: an avatar that left
// the daemon while its object was being tidied.  The tidying finishes and hands
// the slot back, and the pool refuses it: what it stands for is not the
// pool's any more.
func TestASlotRemovedWhileBeingTidiedDoesNotComeBack(t *testing.T) {
	p, _ := stopped(t)
	go p.Run()

	tidying := make(chan struct{})
	release := make(chan struct{})
	s := &Slot{Data: 1, Clean: func(s *Slot) {
		close(tidying)
		<-release
		p.Cleaned(s)
	}}
	if err := p.Add(s); err != nil {
		t.Fatalf("Add: %v", err)
	}

	held := get(t, p, 1)
	p.Return(held.ID)
	<-tidying
	if err := p.Remove(s); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	close(release)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if r := get(t, p, 1); r.Filled() {
			t.Fatal("a slot removed while it was being tidied came back into the pool")
		}
	}
}

// TestASlotIsTidiedWhenItsGrantRunsOutToo: a caller that wedged is
// exactly the caller whose script is still running, so the road back
// through expiry needs the tidying more than the polite one does.
func TestASlotIsTidiedWhenItsGrantRunsOutToo(t *testing.T) {
	p, c := stopped(t)
	go p.Run()

	tidied := make(chan struct{})
	if err := p.Add(&Slot{Data: 1, Clean: func(*Slot) { close(tidied) }}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if held := get(t, p, 1); !held.Filled() {
		t.Fatal("the one slot would not go to one caller")
	}
	c.pass(time.Minute + grace + time.Second)
	if err := p.ExpiryCheck(); err != nil {
		t.Fatalf("ExpiryCheck: %v", err)
	}

	select {
	case <-tidied:
	case <-time.After(2 * time.Second):
		t.Fatal("a slot taken back from a caller that ran out was not tidied")
	}
}

// TestSlotsCanBeAskedForByWhatTheyAre: a caller whose slots have to have
// something in common with each other -- a benchmark compares its
// objects, so four spread over three avatars is four readings that
// cannot be compared -- asks for the ones it can use, and all or nothing
// applies to those.
func TestSlotsCanBeAskedForByWhatTheyAre(t *testing.T) {
	p, _ := stopped(t)
	go p.Run()

	// Two kinds, interleaved, so that taking the second kind means
	// stepping over the first.
	var list []*Slot
	for i := 0; i < 8; i++ {
		list = append(list, &Slot{Data: i % 2}) // 0,1,0,1,...
	}
	if err := p.Add(list...); err != nil {
		t.Fatalf("Add: %v", err)
	}

	ones := func(data any) bool { return data.(int) == 1 }
	r, err := p.GetWhere(4, time.Minute, ones)
	if err != nil {
		t.Fatalf("GetWhere: %v", err)
	}
	if !r.Filled() {
		t.Fatal("the four of that kind could not be had")
	}
	for _, s := range r.Slots {
		if s.Data.(int) != 1 {
			t.Errorf("got a slot of kind %v", s.Data)
		}
	}

	// A fifth of that kind is not there, and the other kind is
	// untouched: what was passed over on the way is somebody else's
	// answer, not this caller's to take or to wait for.
	if more, _ := p.GetWhere(1, time.Minute, ones); more.Filled() {
		t.Error("a fifth slot of a kind there are four of was granted")
	}
	others, err := p.GetWhere(4, time.Minute, func(data any) bool { return data.(int) == 0 })
	if err != nil {
		t.Fatalf("GetWhere: %v", err)
	}
	if !others.Filled() {
		t.Error("the other kind was taken or lost while the first was being chosen")
	}
}

// TestWhatWasPassedOverKeepsItsPlace: the free list is oldest first, and
// a request that steps over slots it cannot use must put them back where
// they were.  Otherwise asking for one kind quietly reorders the other,
// and oldest-first decays into whatever the last caller happened to want.
func TestWhatWasPassedOverKeepsItsPlace(t *testing.T) {
	p, _ := stopped(t)
	go p.Run()

	var list []*Slot
	for i := 1; i <= 6; i++ {
		list = append(list, &Slot{Data: i})
	}
	if err := p.Add(list...); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Take the fourth, stepping over 1, 2 and 3.
	r, err := p.GetWhere(1, time.Minute, func(data any) bool { return data.(int) == 4 })
	if err != nil || !r.Filled() {
		t.Fatalf("GetWhere: %v %v", r, err)
	}

	// The three stepped over are still the oldest, in their old order.
	rest := get(t, p, 3)
	if !rest.Filled() {
		t.Fatal("the three that were stepped over could not be had")
	}
	for i, want := range []int{1, 2, 3} {
		if got := rest.Slots[i].Data.(int); got != want {
			t.Errorf("place %d came back as %d, want %d", i, got, want)
		}
	}
}
