package agent

// The camera, and the sweep that keeps the object cache honest.
//
// A draw distance of nothing is not a request to see nothing: it is a
// caller who did not say, and a session that took it literally would put
// itself in nobody's interest list and then wonder why the simulator had
// stopped describing the world.  So Look fills one in, everywhere it can.
//
// The trim runs on a timer rather than being asked for, because the
// thing that makes the cache stale -- the avatar moving -- is not
// something a client is told about, and because the camera and the cache
// are both on this side.

import (
	"context"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// TestACameraAlwaysHasSomethingToSee: zero is what a Look built field by
// field starts at, so this is the ordinary case rather than the odd one.
func TestACameraAlwaysHasSomethingToSee(t *testing.T) {
	t.Parallel()

	a, _ := offlineSession(t)
	for _, far := range []float32{0, -1} {
		a.SetLook(Look{Center: msg.Vector3{X: 5}, Far: far})
		if got := a.Look().Far; got != DefaultDrawDistance {
			t.Errorf("SetLook with Far %v gave %v, want the default", far, got)
		}
	}

	a.SetLook(Look{Center: msg.Vector3{X: 5}, Far: 64})
	if got := a.Look().Far; got != 64 {
		t.Errorf("a draw distance that was asked for became %v", got)
	}
}

// TestTheCacheIsSweptOnATimer: a stale entry is wrong about where
// something is rather than about whether it exists, so this is not
// urgent work -- but nothing else ever removes an object that was merely
// left behind.
func TestTheCacheIsSweptOnATimer(t *testing.T) {
	t.Parallel()

	a, _ := offlineSession(t)
	a.SetLook(Look{Center: msg.Vector3{X: 128, Y: 128, Z: 25}, Far: 64})

	// One object in view and one a region away, put in with the range
	// check turned off so the sweep is the only thing that can remove it.
	a.Objects().update(&msg.ObjectUpdate_ObjectData{ID: 1, FullID: aPrim,
		ObjectData: placement(msg.Vector3{X: 130, Y: 128, Z: 25}, msg.Quaternion{})},
		msg.Vector3{}, 0)
	a.Objects().update(&msg.ObjectUpdate_ObjectData{ID: 2, FullID: aChild,
		ObjectData: placement(msg.Vector3{X: 900, Y: 128, Z: 25}, msg.Quaternion{})},
		msg.Vector3{}, 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.trimObjects(ctx, 5*time.Millisecond)

	deadline := time.Now().Add(2 * time.Second)
	for a.Objects().Count() > 1 {
		if time.Now().After(deadline) {
			t.Fatalf("the sweep left %d objects", a.Objects().Count())
		}
		time.Sleep(time.Millisecond)
	}
	if _, ok := a.Objects().Get(aPrim); !ok {
		t.Error("the sweep took something that was in view")
	}
}

// TestPresenceIsHandedOverAndComesBack: while a viewer holds the camera
// this session must not also send AgentUpdate, and when the viewer stops
// talking the session must start again on its own.
//
// The coming back is the part worth testing.  A viewer that crashes says
// nothing, and a session left permanently silent falls out of the
// simulator's interest list and receives nothing further -- a failure
// that looks exactly like the relay having broken.
func TestPresenceIsHandedOverAndComesBack(t *testing.T) {
	a := &Agent{}

	if a.presenceDeferred() {
		t.Error("a session with no viewer is already deferring")
	}

	// A short lease, because what is under test is it running out
	// rather than it being cancelled: the deadline only ever moves
	// forward, so a past time cannot cut a live lease short.
	a.DeferPresence(time.Now().Add(30 * time.Millisecond))
	if !a.presenceDeferred() {
		t.Error("the camera was not handed over")
	}

	// Nobody asks for it back; it simply lapses.
	time.Sleep(60 * time.Millisecond)
	if a.presenceDeferred() {
		t.Error("a lapsed lease still holds the camera")
	}
}

// TestALapsedLeaseIsNotExtendedByAnOlderOne: the deadline only ever
// moves forward, so a late update carrying an earlier time cannot cut a
// live lease short.
func TestALapsedLeaseIsNotExtendedByAnOlderOne(t *testing.T) {
	a := &Agent{}
	far := time.Now().Add(time.Minute)
	a.DeferPresence(far)
	a.DeferPresence(time.Now().Add(time.Millisecond))
	if !a.presenceDeferred() {
		t.Error("an older deadline shortened a live lease")
	}
}

// TestResumeIsImmediate: a viewer that detaches in an orderly way should
// not leave the session mute for the rest of the lease.
func TestResumeIsImmediate(t *testing.T) {
	a := &Agent{}
	a.DeferPresence(time.Now().Add(time.Minute))
	a.ResumePresence()
	if a.presenceDeferred() {
		t.Error("resuming did not take the camera back")
	}
}
