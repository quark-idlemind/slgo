package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/quark-idlemind/slgo/sl"
)

// Keeping an avatar attended to is a small state machine with two rules
// worth being exact about: a session that drops is asked for again, and
// a session somebody stopped on purpose is not.  Getting the second one
// wrong means a daemon fighting a person for their own avatar, which is
// the fault the deliberate logout exists to prevent.

// fakeSlgod stands in for the daemon's two calls to slgod.
type fakeSlgod struct {
	mu sync.Mutex

	hosted  []string // every name asked for, in order
	forced  []bool   // whether each ask said force
	hostErr error
	deliber bool

	attached int
	attachFn func() (*sl.Session, error)
}

func withFakeSlgod(t *testing.T) (*daemon, *bot, *fakeSlgod) {
	t.Helper()
	d, b, _ := newTestDaemon(t)
	b.setSession(nil)
	b.setState(stateStarting, "")

	f := &fakeSlgod{}
	d.host = func(ctx context.Context, name string, force bool) (bool, error) {
		f.mu.Lock()
		f.hosted = append(f.hosted, name)
		f.forced = append(f.forced, force)
		err, deliberate := f.hostErr, f.deliber
		f.mu.Unlock()
		return deliberate, err
	}
	d.attach = func(ctx context.Context, name string) (*sl.Session, error) {
		f.mu.Lock()
		f.attached++
		fn := f.attachFn
		f.mu.Unlock()
		if fn != nil {
			return fn()
		}
		return nil, fmt.Errorf("nothing to attach to")
	}
	return d, b, f
}

func (f *fakeSlgod) asks() ([]string, []bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.hosted...), append([]bool(nil), f.forced...)
}

// A session that came up and then ended is asked for again: that is
// what an attendant is for.
func TestASessionThatEndsIsAskedForAgain(t *testing.T) {
	d, b, f := withFakeSlgod(t)
	f.attachFn = func() (*sl.Session, error) {
		g := newFakeGrid()
		s, err := sl.New(g)
		if err != nil {
			return nil, err
		}
		// It ends the moment it is served, which is the shape of a
		// session dropping.
		go func() {
			time.Sleep(10 * time.Millisecond)
			g.Close()
		}()
		return s, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); b.run(ctx) }()

	deadline := time.Now().Add(15 * time.Second)
	for {
		names, _ := f.asks()
		if len(names) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("asked for the session %d times, wanted it asked for again", len(names))
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	_ = d
}

// A deliberate logout stops the asking.  Without this the attendant
// would ask every minute for ever, and would undo the one thing the
// logout was for.
func TestADeliberateLogoutStopsTheAsking(t *testing.T) {
	_, b, f := withFakeSlgod(t)
	f.deliber = true
	f.hostErr = status.Error(codes.FailedPrecondition,
		"example was stopped deliberately (logged out on request)")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); b.run(ctx) }()

	waitState(t, b, stateHeldBack)

	// And it stays stopped: nothing asks again on its own.
	asked, _ := f.asks()
	time.Sleep(500 * time.Millisecond)
	again, _ := f.asks()
	if len(again) != len(asked) {
		t.Errorf("asked %d times and then %d; it should have stopped asking", len(asked), len(again))
	}

	// Until somebody says to force it, which is what ":host --force"
	// does.  The force travels with the next ask and is used once.
	f.mu.Lock()
	f.deliber, f.hostErr = false, nil
	f.mu.Unlock()
	b.Wake(true)

	deadline := time.Now().Add(10 * time.Second)
	for {
		_, forced := f.asks()
		if len(forced) > len(asked) {
			if !forced[len(asked)] {
				t.Error("the ask after a forced wake did not say force")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a forced wake did not make it ask again")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
}

// The force is spent on one attempt.  A flag that stayed set would turn
// every later retry into one that overrules a person, which is exactly
// what it was added not to do.
func TestForceIsUsedOnce(t *testing.T) {
	_, b, _ := withFakeSlgod(t)
	b.Wake(true)
	if !b.takeForce() {
		t.Fatal("the force was not carried")
	}
	if b.takeForce() {
		t.Error("the force survived being used")
	}
}

// An avatar with no session answers a command by saying so and saying
// why, since "nothing happened" is the least useful thing a daemon can
// send back.
func TestAnAttendantSaysWhyItHasNoSession(t *testing.T) {
	_, b, _ := withFakeSlgod(t)
	b.setState(stateHeldBack, "stopped deliberately")
	_, err := b.Need()
	if err == nil {
		t.Fatal("a bot with no session handed one over")
	}
	if got := err.Error(); !strings.Contains(got, "example") ||
		!strings.Contains(got, "stopped deliberately") {
		t.Errorf("err = %q", got)
	}
}

func waitState(t *testing.T, b *bot, want state) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if got, _ := b.State(); got == want {
			return
		}
		if time.Now().After(deadline) {
			got, detail := b.State()
			t.Fatalf("state is %q (%s), waited for %q", got, detail, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
