package sl

// Moving the avatar.
//
// # What this does and does not do
//
// A teleport WITHIN the region is a small thing: the simulator already
// has the avatar, so it moves it and says TeleportLocal.  Nothing about
// the session changes -- same circuit, same capabilities, same session
// id -- so a client can ask for it and the daemon underneath need not
// know it happened.
//
// A teleport to ANOTHER region is a different act entirely.  The
// destination is a different simulator on a different address: the
// session has to be established there, a new circuit opened, a new set
// of capabilities fetched, and the old circuit closed.  That is the
// daemon's work, not a client's, and slgod does not do it yet.  So this
// file deliberately stops at the region boundary rather than offering
// something that would half-work.
//
// Until then, arriving in another region is a matter of logging in
// there: a profile's start location is honoured, and slgod's -start
// overrides it.  That is a relog and not a teleport -- it costs a login
// and every client attached is told the session was re-established --
// but it does arrive.

import (
	"context"
	"fmt"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// TeleportLocal moves the avatar to a position in the region it is
// already in, and waits for the simulator to agree that it moved.
//
// The confirmation matters.  A refused teleport -- somewhere the parcel
// will not have you, or a position outside the region -- produces no
// error and no reply, so a caller that did not check would carry on
// believing it had moved and every position it computed afterwards
// would be wrong.
func (w *Session) TeleportLocal(ctx context.Context, to msg.Vector3, timeout time.Duration) error {
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	where, err := w.Where(ctx)
	if err != nil {
		return err
	}
	if where.RegionHandle == 0 {
		return fmt.Errorf("sl: this session does not know which region it is in")
	}

	m := &msg.TeleportLocationRequest{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.Info.RegionHandle = where.RegionHandle
	m.Info.Position = to
	// Looking the way we are already looking.  The field is not
	// optional and a zero vector is not a direction.
	m.Info.LookAt = where.LookAt
	if m.Info.LookAt == (msg.Vector3{}) {
		m.Info.LookAt = msg.Vector3{X: 1}
	}
	if err := w.Send(ctx, m); err != nil {
		return err
	}

	// Near enough, rather than exact: the simulator stands the avatar
	// on whatever is under the point, so the height it settles at is
	// its choice and not ours.
	const near = 4.0 // metres
	deadline := time.Now().Add(timeout)
	for {
		p, err := w.Where(ctx)
		if err == nil && dist2(p.Position, to) < near*near {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("sl: the avatar did not arrive at %v; "+
				"a parcel that will not have this avatar refuses silently: %w", to, ErrTimeout)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// dist2 is the SQUARED distance: comparing squares avoids a square
// root, and the name says so, because comparing a squared distance
// against a plain one is a bug that looks right.
func dist2(a, b msg.Vector3) float64 {
	dx, dy, dz := float64(a.X-b.X), float64(a.Y-b.Y), float64(a.Z-b.Z)
	return dx*dx + dy*dy + dz*dz
}
