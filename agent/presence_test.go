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

// TestAControlFlagIsSentOnceAndNotRemembered: the flags are edge
// triggered -- measured, one update carrying SIT_ON_GROUND was enough
// and the presence update a second later carrying none did not undo it
// -- so putting one in Look would be resending it for ever, and the
// avatar would never be able to stand up again.
func TestAControlFlagIsSentOnceAndNotRemembered(t *testing.T) {
	a, w := offlineSession(t)
	a.SetLook(Look{Center: msg.Vector3{X: 128, Y: 128, Z: 25}, Far: 64})

	if err := a.Control(context.Background(), ControlSitOnGround); err != nil {
		t.Fatalf("Control: %v", err)
	}
	w.waitFor(t, 1)

	sent := w.messages(t)
	u, ok := sent[0].(*msg.AgentUpdate)
	if !ok {
		t.Fatalf("a control flag went out as %s, want AgentUpdate", sent[0].MsgInfo().Name)
	}
	if u.AgentData.ControlFlags != ControlSitOnGround {
		t.Errorf("the update carried flags %#x, want %#x",
			u.AgentData.ControlFlags, ControlSitOnGround)
	}
	if got := a.Look().ControlFlags; got != 0 {
		t.Errorf("the flag was remembered in the look as %#x; it would be resent for ever", got)
	}

	// And the rest of the update is the session's own camera rather than
	// anything invented, because the simulator scopes its interest list
	// by exactly those fields.
	if u.AgentData.CameraCenter != (msg.Vector3{X: 128, Y: 128, Z: 25}) || u.AgentData.Far != 64 {
		t.Errorf("the one-shot update described a camera at %v seeing %v metres",
			u.AgentData.CameraCenter, u.AgentData.Far)
	}
}

// TestAControlFlagDoesNotInterruptWhatIsAlreadyHeldDown: the flags a
// session is continuously sending are movement, and an avatar walking
// when it is asked to stand up must not stop walking for one update.
func TestAControlFlagDoesNotInterruptWhatIsAlreadyHeldDown(t *testing.T) {
	a, w := offlineSession(t)
	const walking uint32 = 1 // AGENT_CONTROL_AT_POS
	a.SetLook(Look{Center: msg.Vector3{X: 5}, Far: 64, ControlFlags: walking})

	if err := a.Control(context.Background(), ControlStandUp); err != nil {
		t.Fatalf("Control: %v", err)
	}
	w.waitFor(t, 1)

	u := w.messages(t)[0].(*msg.AgentUpdate)
	if want := walking | ControlStandUp; u.AgentData.ControlFlags != want {
		t.Errorf("the update carried %#x, want %#x", u.AgentData.ControlFlags, want)
	}
	if got := a.Look().ControlFlags; got != walking {
		t.Errorf("the look ended up carrying %#x, want the movement alone", got)
	}
}

// TestAControlFlagGoesEvenWhileAViewerHoldsTheCamera: the deferral stops
// this session ARGUING about where the camera is, which is a thing only
// the viewer can be right about.  It is not a stop on being asked to do
// things, and the viewer will never send a flag nobody told it about --
// so refusing here would mean an avatar could not be sat down while
// somebody was watching through it, which is when it is most wanted.
func TestAControlFlagGoesEvenWhileAViewerHoldsTheCamera(t *testing.T) {
	a, w := offlineSession(t)
	a.SetLook(Look{Center: msg.Vector3{X: 5}, Far: 64})
	a.DeferPresence(time.Now().Add(time.Minute))

	if err := a.Control(context.Background(), ControlStandUp); err != nil {
		t.Fatalf("Control: %v", err)
	}
	w.waitFor(t, 1)

	if got := w.messages(t)[0].(*msg.AgentUpdate).AgentData.ControlFlags; got != ControlStandUp {
		t.Errorf("the update carried %#x, want the stand up flag", got)
	}
	if !a.presenceDeferred() {
		t.Error("sending one update took the camera back from the viewer")
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
