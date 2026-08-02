package agent

import (
	"context"
	"time"

	"slgo/msg"
)

// AgentUpdate is how the simulator learns where the avatar is looking
// and how far it can see, and it is not optional in the way it looks.
//
// A session that never sends one is in nobody's interest list: the
// simulator streams no ObjectUpdate at all, so nothing can be seen,
// selected, or built on.  Rezzing a prim and then waiting for it to
// appear is how that was found -- the object was created and the
// simulator simply never mentioned it.
//
// It lives here rather than in a client for the reason the circuit
// does: it has to keep being sent, and a client that stopped would
// silently take object streaming with it.  A client that wants to move
// the camera sets it through Agent.Look; the default is a fixed view
// from wherever the avatar arrived.

// DefaultDrawDistance is the Far value sent when none is set.
const DefaultDrawDistance = 128

// Look is where the avatar's camera is and what it can see.
type Look struct {
	Center msg.Vector3
	At     msg.Vector3
	Left   msg.Vector3
	Up     msg.Vector3
	Far    float32

	// ControlFlags and State go out unchanged; movement lives in
	// them, and this package has no opinion about movement.
	ControlFlags uint32
	State        uint8
}

func defaultLook(at msg.Vector3) Look {
	return Look{
		Center: at,
		At:     msg.Vector3{X: 1},
		Left:   msg.Vector3{Y: 1},
		Up:     msg.Vector3{Z: 1},
		Far:    DefaultDrawDistance,
	}
}

// Look returns what is currently being sent.
func (a *Agent) Look() Look {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.look
}

// setCenter moves the camera without disturbing the rest of the view.
//
// This follows the avatar, and it is the server's job rather than a
// client's: a client that exits leaves the camera wherever it was, and
// a camera in the wrong place quietly empties the interest list for
// every client that connects afterwards.
func (a *Agent) setCenter(at msg.Vector3) {
	a.mu.Lock()
	a.look.Center = at
	if a.look.Far <= 0 {
		a.look.Far = DefaultDrawDistance
	}
	a.mu.Unlock()
}

// SetLook changes it.  The next update carries it.
func (a *Agent) SetLook(l Look) {
	if l.Far <= 0 {
		l.Far = DefaultDrawDistance
	}
	a.mu.Lock()
	a.look = l
	a.mu.Unlock()
}

// sendPresence keeps AgentUpdate flowing.
func (a *Agent) sendPresence(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()

	send := func() {
		l := a.Look()
		m := &msg.AgentUpdate{}
		d := &m.AgentData
		d.AgentID = a.Account.AgentID
		d.SessionID = a.Account.SessionID
		d.CameraCenter = l.Center
		d.CameraAtAxis = l.At
		d.CameraLeftAxis = l.Left
		d.CameraUpAxis = l.Up
		d.Far = l.Far
		d.ControlFlags = l.ControlFlags
		d.State = l.State
		// Unreliable on purpose: the next one is along shortly
		// and a lost update is not worth retransmitting.
		_ = a.Send.Send(ctx, m)
	}

	send()
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.done:
			return
		case <-t.C:
			send()
		}
	}
}

// TrimInterval is how often the object cache is trimmed to the draw
// distance.
//
// It is not urgent work.  A stale entry is wrong about where something
// is, not about whether it exists, and the cost of holding one is a
// map entry.
const TrimInterval = 15 * time.Second

// trimObjects keeps the cache to what is within the draw distance.
//
// It runs here rather than being asked for because the thing that
// makes the cache stale -- the avatar moving -- is not something a
// client is told about, and because the camera and the cache are both
// on this side.
func (a *Agent) trimObjects(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.done:
			return
		case <-t.C:
			l := a.Look()
			a.Objects.Trim(l.Center, l.Far)
		}
	}
}
