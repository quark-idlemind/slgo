package agent

// The controls a script has taken.
//
// A script holding PERMISSION_TAKE_CONTROLS calls llTakeControls, and
// the simulator tells the avatar's viewer with ScriptControlChange: a
// list of blocks, each a take or a release of some control bits, and
// whether the avatar is to have them as well.  A control taken and not
// passed on goes to the script instead of moving the avatar; one passed
// on does both.  The viewer keeps a count per bit for each of the two,
// adds one on a take, takes one off on a release and stops at zero, and
// never clears them once the agent exists (LLAgent::processScriptControlChange,
// llagent.cpp:4532; the counts are zeroed only in the constructor, at
// llagent.cpp:519-522).  This does the same, so what it says is what a
// viewer would have.
//
// It matters here because a walk is AT_POS held in every AgentUpdate,
// and a script that has taken the forward control and keeps it gets the
// flag instead of the simulator: the walk would send ten updates a
// second and the avatar would stand still until it was called Blocked.
// Why: doc/avatar-state.md#scriptcontrolchange

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/quark-idlemind/slgo/msg"
)

// totalControls is how many bits a count is kept for, the viewer's
// TOTAL_CONTROLS (indra_constants.h:321).
const totalControls = 32

// scriptControls is the two sets of counts.
type scriptControls struct {
	mu       sync.Mutex
	taken    [totalControls]int
	passedOn [totalControls]int
}

// apply takes or releases each bit of mask in the set the block names.
func (c *scriptControls) apply(take bool, mask uint32, passOn bool) {
	counts := &c.taken
	if passOn {
		counts = &c.passedOn
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := 0; i < totalControls; i++ {
		if mask&(1<<i) == 0 {
			continue
		}
		if take {
			counts[i]++
		} else if counts[i] > 0 {
			counts[i]--
		}
	}
}

// ScriptControls says which controls scripts hold: taken is the bits a
// script has taken and does not pass on, which go to the script and
// not to the avatar, and passedOn the bits taken and passed on, which
// go to both.  A bit is set while its count is above zero, so two
// scripts taking a control and one letting go of it leave it taken.
//
// Both are empty until the simulator has said otherwise, and what it
// has said is only what this session has been told since it began.
func (a *Agent) ScriptControls() (taken, passedOn uint32) {
	c := &a.controls
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := 0; i < totalControls; i++ {
		if c.taken[i] > 0 {
			taken |= 1 << i
		}
		if c.passedOn[i] > 0 {
			passedOn |= 1 << i
		}
	}
	return taken, passedOn
}

// keepScriptControls registers the handler that keeps the counts.
func (a *Agent) keepScriptControls() {
	a.Disp.MustHandle("ScriptControlChange", func(p *msg.Packet) {
		m := p.Message.(*msg.ScriptControlChange)
		for _, b := range m.Data {
			a.controls.apply(b.TakeControls, b.Controls, b.PassToAgent)
		}
	}, msg.Inline())
}

// ErrControlsTaken is what a walk is refused with while a script has
// taken the forward control and does not pass it on: the AT_POS a walk
// holds would go to the script, and the avatar would not move.
var ErrControlsTaken = errors.New("a script has taken the forward control and does not pass it on")

// walkHeld says whether the walk's own control is held by a script that
// keeps it.
func (a *Agent) walkHeld() bool {
	taken, _ := a.ScriptControls()
	return taken&ControlAtPos != 0
}

// controlNames is the word for each bit a script is likely to take, in
// bit order.  The LSL constants of the same value are CONTROL_FWD,
// CONTROL_BACK, CONTROL_LEFT, CONTROL_RIGHT, CONTROL_UP, CONTROL_DOWN,
// CONTROL_ROT_LEFT, CONTROL_ROT_RIGHT, CONTROL_LBUTTON and
// CONTROL_ML_LBUTTON.  The nudges have no LSL constant: the simulator
// adds them to what a script takes, CONTROL_FWD bringing the forward
// nudge with it.
// Why: doc/avatar-state.md#measured
var controlNames = []struct {
	bit  uint32
	name string
}{
	{ControlAtPos, "forward"},
	{ControlAtNeg, "back"},
	{ControlLeftPos, "left"},
	{ControlLeftNeg, "right"},
	{ControlUpPos, "up"},
	{ControlUpNeg, "down"},
	{ControlYawPos, "turn left"},
	{ControlYawNeg, "turn right"},
	{ControlNudgeAtPos, "nudge forward"},
	{ControlNudgeAtNeg, "nudge back"},
	{ControlNudgeLeftPos, "nudge left"},
	{ControlNudgeLeftNeg, "nudge right"},
	{ControlNudgeUpPos, "nudge up"},
	{ControlNudgeUpNeg, "nudge down"},
	{ControlLButtonDown, "click"},
	{ControlMLLButtonDown, "mouselook click"},
}

// ControlWords names the bits of a control mask in words, separated by
// commas, lowest first; a bit with no name is its own value in
// hex, after the named ones.  An empty mask is an empty string.
func ControlWords(mask uint32) string {
	var out []string
	for _, n := range controlNames {
		if mask&n.bit != 0 {
			out = append(out, n.name)
			mask &^= n.bit
		}
	}
	for i := 0; i < totalControls; i++ {
		if mask&(1<<i) != 0 {
			out = append(out, fmt.Sprintf("0x%08x", uint32(1)<<i))
		}
	}
	return strings.Join(out, ", ")
}
