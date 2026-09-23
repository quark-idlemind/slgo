package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// clock is a throttle's time, made to stand still and move on command,
// and its sleeps, recorded rather than slept.  A test of a four-second
// delay should not take four seconds, and one that did would still not
// say how long the wait was.
type clock struct {
	mu     sync.Mutex
	now    time.Time
	slept  []time.Duration
	block  chan struct{} // when set, a sleep waits for it to close
	asleep chan struct{} // told as each sleep begins, when set
}

func throttled(c *clock) *Throttle {
	t := NewThrottle()
	c.now = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	t.Now = func() time.Time {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.now
	}
	t.Sleep = func(ctx context.Context, d time.Duration) error {
		c.mu.Lock()
		c.slept = append(c.slept, d)
		block, asleep := c.block, c.asleep
		c.mu.Unlock()
		if asleep != nil {
			asleep <- struct{}{}
		}
		if block == nil {
			return ctx.Err()
		}
		select {
		case <-block:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// sleeps is what has been slept since it was last asked.
func (c *clock) sleeps() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.slept
	c.slept = nil
	return out
}

// attempt is one proof from an address, and how it turned out.
func attempt(t *testing.T, th *Throttle, peer string, ok bool) {
	t.Helper()
	done, err := th.Admit(context.Background(), peer)
	if err != nil {
		t.Fatalf("an attempt from %s was refused: %v", peer, err)
	}
	done(ok)
}

// TestAWrongAnswerIsSlowedOnlyAfterThree: three failures cost nothing,
// so a client whose secret changed under it and a person trying it
// again are not made to wait; the fourth waits half a second, and each
// after that twice as long as the last, to a cap.
func TestAWrongAnswerIsSlowedOnlyAfterThree(t *testing.T) {
	c := &clock{}
	th := throttled(c)

	for i := 0; i < FreeFailures; i++ {
		attempt(t, th, "198.51.100.7", false)
	}
	if got := c.sleeps(); len(got) != 0 {
		t.Fatalf("the first %d failures waited %v; want no wait", FreeFailures, got)
	}

	want := []time.Duration{
		500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second,
	}
	for range want {
		attempt(t, th, "198.51.100.7", false)
	}
	if got := c.sleeps(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("failures after the third waited %v, want %v", got, want)
	}
}

// TestTheRightAnswerIsSlowedTooAndThenForgiven: once an address is
// being slowed, its right answer waits as long as a wrong one would --
// or how soon an answer came back would say which it was -- and after
// it the address starts again from nothing.
func TestTheRightAnswerIsSlowedTooAndThenForgiven(t *testing.T) {
	c := &clock{}
	th := throttled(c)
	for i := 0; i < FreeFailures+1; i++ {
		attempt(t, th, "198.51.100.7", false)
	}
	c.sleeps()

	attempt(t, th, "198.51.100.7", true)
	if got := c.sleeps(); len(got) != 1 || got[0] != time.Second {
		t.Errorf("the right answer after four wrong ones waited %v; want the same second a wrong one would", got)
	}
	attempt(t, th, "198.51.100.7", false)
	if got := c.sleeps(); len(got) != 0 {
		t.Errorf("an address that proved itself is still slowed: %v", got)
	}
}

// TestAddressesAreCountedApart: one address guessing does not slow a
// client somewhere else that has the secret.
func TestAddressesAreCountedApart(t *testing.T) {
	c := &clock{}
	th := throttled(c)
	for i := 0; i < FreeFailures+2; i++ {
		attempt(t, th, "198.51.100.7", false)
	}
	c.sleeps()

	attempt(t, th, "127.0.0.1", true)
	if got := c.sleeps(); len(got) != 0 {
		t.Errorf("a client at another address waited %v for somebody else's failures", got)
	}
}

// TestManyAddressesTogetherSlowEveryone: a guesser with an address per
// guess gets none of the per-address count's attention, so the daemon
// counts too, at a tenth of the weight.
func TestManyAddressesTogetherSlowEveryone(t *testing.T) {
	c := &clock{}
	th := throttled(c)
	for i := 0; i < FreeFailures*allWeight; i++ {
		attempt(t, th, fmt.Sprintf("2001:db8::%x", i), false)
	}
	if got := c.sleeps(); len(got) != 0 {
		t.Fatalf("one failure each from %d addresses was slowed before the daemon-wide count said so: %v",
			FreeFailures*allWeight, got)
	}

	attempt(t, th, "2001:db8::ffff", false)
	if got := c.sleeps(); len(got) != 1 || got[0] != firstDelay {
		t.Errorf("a new address after %d failures elsewhere waited %v, want %v",
			FreeFailures*allWeight, got, firstDelay)
	}

	// And nobody's success lets the rest off: a guesser spread over
	// many addresses would otherwise be forgiven whenever a real client
	// happened to log in.
	attempt(t, th, "127.0.0.1", true)
	c.sleeps()
	attempt(t, th, "2001:db8::fffe", false)
	if got := c.sleeps(); len(got) != 1 {
		t.Errorf("after one client's success, a new address waited %v; the daemon-wide count should stand", got)
	}
}

// TestFailuresAreForgottenAfterAQuietSpell: an address that stops
// failing is not held to it for ever, and nor is the daemon.
func TestFailuresAreForgottenAfterAQuietSpell(t *testing.T) {
	c := &clock{}
	th := throttled(c)
	for i := 0; i < FreeFailures*allWeight; i++ {
		attempt(t, th, "198.51.100.7", false)
	}
	c.sleeps()

	c.advance(forgetAfter)
	attempt(t, th, "198.51.100.7", false)
	if got := c.sleeps(); len(got) != 0 {
		t.Errorf("after %v with nothing, a failure still waited %v", forgetAfter, got)
	}
}

// TestConcurrentWrongAnswersDoNotAllArriveFree: an attempt is counted
// when it arrives, not when it has been checked.  Counted afterwards, a
// guesser sending a thousand at once finds the count at nought for all
// of them.
func TestConcurrentWrongAnswersDoNotAllArriveFree(t *testing.T) {
	c := &clock{}
	th := throttled(c)

	var pending []func(bool)
	for i := 0; i < FreeFailures; i++ {
		done, err := th.Admit(context.Background(), "198.51.100.7")
		if err != nil {
			t.Fatal(err)
		}
		pending = append(pending, done)
	}
	done, err := th.Admit(context.Background(), "198.51.100.7")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.sleeps(); len(got) != 1 {
		t.Errorf("a fourth attempt while three were unanswered waited %v; want it slowed", got)
	}
	done(false)
	for _, d := range pending {
		d(false)
	}
}

// TestACallerThatLeavesWhileWaitingIsNotCharged: no proof is checked
// until the wait is over, so a caller that leaves during it has had no
// guess -- and it is not counted as one.
func TestACallerThatLeavesWhileWaitingIsNotCharged(t *testing.T) {
	c := &clock{}
	th := throttled(c)
	for i := 0; i < FreeFailures; i++ {
		attempt(t, th, "198.51.100.7", false)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 5; i++ {
		if _, err := th.Admit(ctx, "198.51.100.7"); !errors.Is(err, context.Canceled) {
			t.Fatalf("an attempt whose caller left = %v, want the context's error", err)
		}
	}
	c.sleeps()

	attempt(t, th, "198.51.100.7", false)
	if got := c.sleeps(); len(got) != 1 || got[0] != firstDelay {
		t.Errorf("after five abandoned attempts the next waited %v, want %v as if they had not happened",
			got, firstDelay)
	}
}

// TestDelayedAnswersAreBounded: each delayed answer is a goroutine
// asleep, and a guesser can open as many as it likes.  Past the bound,
// a caller that would have waited is refused at once without its proof
// being looked at -- which is also what bounds the rate of guessing,
// since a delay alone does not.  A caller not being slowed is not
// affected by any of it, and no lock is held while anybody waits.
func TestDelayedAnswersAreBounded(t *testing.T) {
	c := &clock{block: make(chan struct{}), asleep: make(chan struct{}, maxDelayed)}
	th := throttled(c)

	// Enough addresses, each already slowed, to fill every place.
	peers := make([]string, maxDelayed)
	for i := range peers {
		peers[i] = fmt.Sprintf("198.51.100.%d", i+1)
		for j := 0; j < FreeFailures; j++ {
			done, err := th.Admit(context.Background(), peers[i])
			if err != nil {
				t.Fatal(err)
			}
			done(false)
		}
		// The daemon-wide count is cleared each time, or these
		// failures together would slow every caller -- the ones
		// setting this up, and the one at the end that is to show a
		// caller not being slowed is not held up.
		th.mu.Lock()
		th.all.count = 0
		th.mu.Unlock()
	}

	var wg sync.WaitGroup
	admit := func(peer string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			done, err := th.Admit(context.Background(), peer)
			if err != nil {
				t.Errorf("a waiting attempt from %s: %v", peer, err)
				return
			}
			done(false)
		}()
		<-c.asleep
	}

	// One address may hold only so many.
	for i := 0; i < maxDelayedPerPeer; i++ {
		admit(peers[0])
	}
	if _, err := over(th, peers[0]); !errors.Is(err, ErrBusy) {
		t.Errorf("attempt %d from one address while %d wait = %v, want ErrBusy",
			maxDelayedPerPeer+1, maxDelayedPerPeer, err)
	}

	// And the daemon only so many altogether.
	for i := 1; i <= maxDelayed-maxDelayedPerPeer; i++ {
		admit(peers[i])
	}
	if _, err := over(th, peers[maxDelayed-1]); !errors.Is(err, ErrBusy) {
		t.Errorf("an attempt with %d already waiting = %v, want ErrBusy", maxDelayed, err)
	}

	// While every place is taken, a caller that is not being slowed
	// goes straight through.
	fresh := make(chan error, 1)
	go func() {
		done, err := th.Admit(context.Background(), "127.0.0.1")
		if err == nil {
			done(true)
		}
		fresh <- err
	}()
	select {
	case err := <-fresh:
		if err != nil {
			t.Errorf("a caller with no failures, while others wait: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a caller with no failures was held up behind those being slowed")
	}

	close(c.block)
	wg.Wait()

	// Every place is given back.
	th.mu.Lock()
	delayed := th.delayed
	th.mu.Unlock()
	if delayed != 0 {
		t.Errorf("%d places still taken after every wait ended", delayed)
	}
}

// over is an attempt that should be refused at once.  If it is let wait
// instead, it gives up after a while rather than waiting for ever, so
// that a missing bound fails the test instead of hanging it.
func over(th *Throttle, peer string) (func(bool), error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return th.Admit(ctx, peer)
}

// TestTheAddressesRememberedAreBounded: a guesser with an address per
// guess must not be able to grow the record without limit.
func TestTheAddressesRememberedAreBounded(t *testing.T) {
	c := &clock{}
	th := throttled(c)
	for i := 0; i < maxPeers+100; i++ {
		// The daemon-wide count is cleared each time, or it would
		// slow all this down; it has nothing to do with the record.
		attempt(t, th, fmt.Sprintf("2001:db8::%x", i), false)
		th.mu.Lock()
		th.all.count = 0
		th.mu.Unlock()
	}
	th.mu.Lock()
	n := len(th.peers)
	th.mu.Unlock()
	if n > maxPeers {
		t.Errorf("remembering %d addresses, bound is %d", n, maxPeers)
	}
}
