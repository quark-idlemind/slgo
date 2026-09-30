package agent

import (
	"context"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// controlChange is a ScriptControlChange of one block.
func controlChange(take bool, controls uint32, passOn bool) *msg.ScriptControlChange {
	return &msg.ScriptControlChange{Data: []msg.ScriptControlChange_Data{
		{TakeControls: take, Controls: controls, PassToAgent: passOn}}}
}

func wantControls(t *testing.T, a *Agent, taken, passedOn uint32, when string) {
	t.Helper()
	gt, gp := a.ScriptControls()
	if gt != taken || gp != passedOn {
		t.Errorf("%s: taken %#x, passed on %#x; want %#x and %#x", when, gt, gp, taken, passedOn)
	}
}

// TestControlsAreCountedAsTheViewerCountsThem: each take adds one and
// each release takes one off, so two scripts taking a control and one
// letting go leave it taken; a release with nothing to release stops at
// zero rather than owing; and what is passed on is counted apart from
// what is not.
func TestControlsAreCountedAsTheViewerCountsThem(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	wantControls(t, a, 0, 0, "at the start")

	feed(t, a, controlChange(true, ControlAtPos|ControlAtNeg, false))
	feed(t, a, controlChange(true, ControlAtPos, false))
	wantControls(t, a, ControlAtPos|ControlAtNeg, 0, "two takes of forward, one of back")

	feed(t, a, controlChange(false, ControlAtPos, false))
	wantControls(t, a, ControlAtPos|ControlAtNeg, 0, "forward taken twice and released once")

	feed(t, a, controlChange(false, ControlAtPos|ControlAtNeg, false))
	wantControls(t, a, 0, 0, "everything released")

	// The floor: a release of what was never taken is nothing, and the
	// next take is one take and not a take that cancels a debt.
	feed(t, a, controlChange(false, ControlUpPos, false))
	feed(t, a, controlChange(false, ControlUpPos, false))
	feed(t, a, controlChange(true, ControlUpPos, false))
	wantControls(t, a, ControlUpPos, 0, "a take after releases of nothing")
	feed(t, a, controlChange(false, ControlUpPos, false))
	wantControls(t, a, 0, 0, "that one taken and released")

	// Passed on is its own count: releasing it does not touch the
	// other, and the two masks say so.
	feed(t, a, controlChange(true, ControlLeftPos, true))
	feed(t, a, controlChange(true, ControlLeftPos, false))
	wantControls(t, a, ControlLeftPos, ControlLeftPos, "taken both ways")
	feed(t, a, controlChange(false, ControlLeftPos, false))
	wantControls(t, a, 0, ControlLeftPos, "the kept one released, the passed one not")
	feed(t, a, controlChange(false, ControlLeftPos, true))
	wantControls(t, a, 0, 0, "both released")

	// Every block of a message counts, and all thirty-two bits do.
	feed(t, a, &msg.ScriptControlChange{Data: []msg.ScriptControlChange_Data{
		{TakeControls: true, Controls: ControlYawPos | ControlMLLButtonDown, PassToAgent: false},
		{TakeControls: true, Controls: 0x80000000, PassToAgent: true},
	}})
	wantControls(t, a, ControlYawPos|ControlMLLButtonDown, 0x80000000, "two blocks in one message")
}

func TestControlsAreNamedInWords(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		mask uint32
		want string
	}{
		{0, ""},
		{ControlAtPos, "forward"},
		{ControlAtPos | ControlAtNeg, "forward, back"},
		{ControlLeftPos | ControlLeftNeg | ControlUpPos | ControlUpNeg, "left, right, up, down"},
		{ControlYawPos | ControlYawNeg, "turn left, turn right"},
		{ControlLButtonDown | ControlMLLButtonDown, "click, mouselook click"},
		// What a script's CONTROL_FWD | CONTROL_BACK was measured
		// arriving as.
		{0x00180003, "forward, back, nudge forward, nudge back"},
		{ControlNudgeLeftPos | ControlNudgeLeftNeg | ControlNudgeUpPos | ControlNudgeUpNeg, "nudge left, nudge right, nudge up, nudge down"},
		{ControlAtPos | 0x00000040, "forward, 0x00000040"},
		// What an attachment's CONTROL_LBUTTON was seen arriving as.
		{ControlLButtonDown | ControlLButtonUp, "click, click release"},
		{ControlMLLButtonUp, "mouselook click release"},
		{0x00008000, "0x00008000"},
	} {
		if got := ControlWords(c.mask); got != c.want {
			t.Errorf("ControlWords(%#x) = %q, want %q", c.mask, got, c.want)
		}
	}
}

// TestAWalkIsRefusedWhileAScriptKeepsTheForwardControl: the AT_POS a
// walk holds would go to the script, so nothing is sent and the reason
// is the error's own words.  A control the script passes on still moves
// the avatar, and so does not refuse.
func TestAWalkIsRefusedWhileAScriptKeepsTheForwardControl(t *testing.T) {
	t.Parallel()
	a, w := offlineSession(t)
	walkable(t, a, msg.Vector3{X: 100, Y: 100, Z: 25})

	feed(t, a, controlChange(true, ControlAtPos, false))
	end, err := a.Move(context.Background(), MoveRequest{Target: msg.Vector3{X: 104, Y: 100}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if end.State != Refused || end.Reason != ErrControlsTaken.Error() {
		t.Fatalf("the walk ended %v %q, want Refused with %q", end.State, end.Reason, ErrControlsTaken)
	}
	if a.Walking() {
		t.Error("a refused walk is walking")
	}
	if us := updates(t, w); len(us) != 0 {
		t.Errorf("a refused walk sent %d updates", len(us))
	}

	// Passed on: the avatar moves as well, so the walk goes.
	feed(t, a, controlChange(false, ControlAtPos, false), controlChange(true, ControlAtPos, true))
	end, err = a.Move(context.Background(), MoveRequest{Target: msg.Vector3{X: 104, Y: 100}}, nil)
	if err != nil || end.State != Arrived {
		t.Errorf("a walk with forward passed on ended %v %q, %v", end.State, end.Reason, err)
	}
}

// TestAWalkEndsWhenAScriptTakesTheForwardControl: the walk notices on
// its next look, lets go of the flag, and says why.
func TestAWalkEndsWhenAScriptTakesTheForwardControl(t *testing.T) {
	t.Parallel()
	a, w := offlineSession(t)
	walkable(t, a, msg.Vector3{X: 100, Y: 100, Z: 25})

	done := make(chan MoveProgress, 1)
	go func() {
		p, _ := a.Move(context.Background(), MoveRequest{Target: msg.Vector3{X: 160, Y: 100}}, nil)
		done <- p
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !a.Walking() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	feed(t, a, controlChange(true, ControlAtPos|ControlAtNeg, false))

	select {
	case end := <-done:
		if end.State != Cancelled || end.Reason != CancelControlsTaken {
			t.Errorf("the walk ended %v %q, want Cancelled %q", end.State, end.Reason, CancelControlsTaken)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the walk went on after a script took the forward control")
	}
	us := updates(t, w)
	if n := len(us); n < 2 || us[n-2].AgentData.ControlFlags != ControlStop || us[n-1].AgentData.ControlFlags != 0 {
		t.Errorf("the walk did not let go of the flag")
	}
	if flags, _ := a.drive(); flags != 0 {
		t.Errorf("the session still holds %#x", flags)
	}
}
