package sl

// Touching things.
//
// # The raycast is not on the wire
//
// A viewer works out what was clicked by casting a ray from the camera
// through the mouse, and then sends the ANSWER: an object, a face
// index, the point of intersection, the surface normal, the texture
// coordinates.  The simulator does no geometry of its own -- it takes
// the numbers it is given and hands them to the script as
// llDetectedTouchFace, llDetectedTouchPos, llDetectedTouchUV and the
// rest.  See send_ObjectGrab_message in the viewer's lltoolgrab.cpp,
// which packs pick.mIntersection straight into the message.
//
// That is worth knowing because it makes this MORE precise than a
// viewer, not less.  A test that has to touch the third face of a prim
// nine tenths of the way along one edge does not have to place a camera
// and aim: it says face 3, uv <0.9, 0.5>.
//
// # Three messages, three events
//
//	ObjectGrab        touch_start
//	ObjectGrabUpdate  touch, once per update, while held
//	ObjectDeGrab      touch_end
//
// A click is a grab and an immediate degrab, which is what Touch sends.
// A script that counts touch events sees exactly what a viewer's click
// produces.

import (
	"context"
	"fmt"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// Touch says where on an object a touch lands.
//
// The zero value touches face 0 in the middle, pointing up, which is
// what "click it" means when nothing more is said.  Every field is
// what a viewer would have worked out by casting a ray, and nothing
// here checks that the numbers describe a point that is really on the
// object: the simulator does not either, and a test that wants to
// touch a face edge-on is not making a mistake.
type Touch struct {
	// Face is which face was touched, counting as LSL does.  Negative
	// means none, which is what a viewer sends when it hit the object
	// but not a face of it.
	Face int

	// UV is where on the face, each 0 to 1, and ST is the same point in
	// the texture's own coordinates.  Zero means the middle.
	UV msg.Vector3
	ST msg.Vector3

	// Position is the point touched, in region coordinates.  It is
	// what llDetectedTouchPos reports.
	Position msg.Vector3

	// Normal and Binormal are the surface directions there.  Zero
	// means straight up and along y, which is a flat top face.
	Normal   msg.Vector3
	Binormal msg.Vector3

	// Offset is the grab offset, from the object's centre.  It is what
	// llDetectedGrab reports during a drag, and it is the only field
	// that means anything different while the touch is held.
	Offset msg.Vector3
}

// fill supplies the defaults, so that a zero Touch is a click in the
// middle of face 0 rather than a click on a corner pointing nowhere.
func (t Touch) fill() Touch {
	if t.UV == (msg.Vector3{}) {
		t.UV = msg.Vector3{X: 0.5, Y: 0.5}
	}
	if t.ST == (msg.Vector3{}) {
		t.ST = msg.Vector3{X: 0.5, Y: 0.5}
	}
	if t.Normal == (msg.Vector3{}) {
		t.Normal = msg.Vector3{Z: 1}
	}
	if t.Binormal == (msg.Vector3{}) {
		t.Binormal = msg.Vector3{Y: 1}
	}
	return t
}

func (t Touch) grabSurface() msg.ObjectGrab_SurfaceInfo {
	return msg.ObjectGrab_SurfaceInfo{
		UVCoord: t.UV, STCoord: t.ST, FaceIndex: int32(t.Face),
		Position: t.Position, Normal: t.Normal, Binormal: t.Binormal,
	}
}

func (t Touch) degrabSurface() msg.ObjectDeGrab_SurfaceInfo {
	return msg.ObjectDeGrab_SurfaceInfo{
		UVCoord: t.UV, STCoord: t.ST, FaceIndex: int32(t.Face),
		Position: t.Position, Normal: t.Normal, Binormal: t.Binormal,
	}
}

func (t Touch) updateSurface() msg.ObjectGrabUpdate_SurfaceInfo {
	return msg.ObjectGrabUpdate_SurfaceInfo{
		UVCoord: t.UV, STCoord: t.ST, FaceIndex: int32(t.Face),
		Position: t.Position, Normal: t.Normal, Binormal: t.Binormal,
	}
}

// TouchStart begins a touch: the script's touch_start.
//
// Nothing waits for anything.  A touch is not acknowledged, the script
// may not be listening, and it may take a second to do whatever it does
// -- so what a caller waits for is the script's own output, not this.
func (w *Session) TouchStart(ctx context.Context, o *Object, t Touch) error {
	if o == nil || (o.Local == 0 && o.ID.IsZero()) {
		return fmt.Errorf("sl: nothing to touch")
	}
	local, err := w.local(ctx, o)
	if err != nil {
		return err
	}
	t = t.fill()
	m := &msg.ObjectGrab{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.ObjectData.LocalID = local
	m.ObjectData.GrabOffset = t.Offset
	m.SurfaceInfo = []msg.ObjectGrab_SurfaceInfo{t.grabSurface()}
	return w.Send(ctx, m)
}

// TouchMove is one more touch event while the touch is held.
//
// held is how long since the last one, which the simulator is told and
// a script can read as the drag's timing.  This is the one message of
// the three that names the object by id rather than by local id.
func (w *Session) TouchMove(ctx context.Context, o *Object, t Touch, held time.Duration) error {
	if o == nil || o.ID.IsZero() {
		return fmt.Errorf("sl: nothing to touch")
	}
	t = t.fill()
	m := &msg.ObjectGrabUpdate{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.ObjectData.ObjectID = o.ID
	m.ObjectData.GrabOffsetInitial = t.Offset
	m.ObjectData.GrabPosition = t.Position
	m.ObjectData.TimeSinceLast = uint32(held.Milliseconds())
	m.SurfaceInfo = []msg.ObjectGrabUpdate_SurfaceInfo{t.updateSurface()}
	return w.Send(ctx, m)
}

// TouchEnd finishes a touch: the script's touch_end.
func (w *Session) TouchEnd(ctx context.Context, o *Object, t Touch) error {
	if o == nil || (o.Local == 0 && o.ID.IsZero()) {
		return fmt.Errorf("sl: nothing to touch")
	}
	local, err := w.local(ctx, o)
	if err != nil {
		return err
	}
	t = t.fill()
	m := &msg.ObjectDeGrab{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.ObjectData.LocalID = local
	m.SurfaceInfo = []msg.ObjectDeGrab_SurfaceInfo{t.degrabSurface()}
	return w.Send(ctx, m)
}

// Touch is a click: a start and an end, with nothing in between.
//
// It is what a viewer sends for a click that is not a drag, and a
// script sees touch_start and touch_end -- and one touch, since the
// simulator fires that for the frame the grab was live in.
func (w *Session) Touch(ctx context.Context, o *Object, t Touch) error {
	if err := w.TouchStart(ctx, o, t); err != nil {
		return err
	}
	return w.TouchEnd(ctx, o, t)
}

// TouchRate is how many updates a second a moving touch sends, which
// is what a viewer sends: one a frame.
//
// It is NOT how many touch events the script sees.  That is the
// simulator's business and nothing a client does changes it; see
// TouchEventRate for the measurement.
const TouchRate = 45

// TouchEventRate is how often a script's touch event actually fires
// while a touch is held: 22.5 times a second, half the simulator's
// 45 fps.
//
// Measured on Agni's beta grid, holding a touch on a counting script
// while varying how fast updates were sent:
//
//	updates/s    1     2     5    15    30    45    90
//	2s hold     --    --    45    45    45    45    45
//	4s hold     90    90    --    --    --    --    90
//
// The count depends on the duration and on nothing else.  One update a
// second and ninety produce the same number of events, so a caller
// cannot make a script see more touches by sending more, and does not
// lose any by sending fewer.
const TouchEventRate = 22.5

// Drag is a touch that moves.
//
// It is the general case and Touch is the degenerate one: a single
// point with no durations is a click.  What it describes is what a
// hand does -- press somewhere, hold still a moment, move through some
// points, rest at the end, let go -- because that is what a script
// being tested has to survive.
type Drag struct {
	// Points are where the touch is, in order.  The first is where it
	// starts and the last is where it is released.  One point is a
	// touch that does not move.
	Points []Touch

	// Press is how long to hold still at the first point before
	// moving, Move is how long the whole path takes, and Dwell is how
	// long to rest at the last point before letting go.
	//
	// They are separate because a script can tell them apart: a menu
	// that opens on a long press and a slider that follows a drag are
	// looking at different halves of the same gesture.
	Press time.Duration
	Move  time.Duration
	Dwell time.Duration

	// Rate is how many updates a second to send.  Zero means TouchRate.
	//
	// It controls how finely a MOVING touch is sampled -- how often the
	// script is told the point has changed -- and nothing else.  It
	// does not control how many touch events fire: the simulator fires
	// those at TouchEventRate whatever a client does, which is measured
	// there.  So a still hold needs almost no updates, and a drag that
	// must be followed closely needs many.
	Rate int
}

// Drag touches an object, moves the touch through the points, and lets
// go.
//
// Time is divided equally between the segments rather than by distance.
// A caller that wants an even speed spaces its points evenly, which is
// something it can do and this cannot: the points may be texture
// coordinates on different faces, where distance means nothing.
//
// It always lets go, cancellation included.
func (w *Session) Drag(ctx context.Context, o *Object, d Drag) error {
	if len(d.Points) == 0 {
		return fmt.Errorf("sl: a drag needs somewhere to start")
	}
	rate := d.Rate
	if rate <= 0 {
		rate = TouchRate
	}
	every := time.Second / time.Duration(rate)

	if err := w.TouchStart(ctx, o, d.Points[0]); err != nil {
		return err
	}
	defer func() {
		end, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = w.TouchEnd(end, o, d.Points[len(d.Points)-1])
	}()

	last := time.Now()
	tick := func(at Touch) error {
		now := time.Now()
		err := w.TouchMove(ctx, o, at, now.Sub(last))
		last = now
		return err
	}

	// Held still at the start.
	if err := w.hold(ctx, d.Points[0], d.Press, every, tick); err != nil {
		return err
	}

	// Along the path.  Each segment gets an equal share of the time and
	// however many updates fit in it, and the point itself is always
	// sent, so a path is never skipped over by a rate too slow for it.
	if n := len(d.Points) - 1; n > 0 && d.Move > 0 {
		per := d.Move / time.Duration(n)
		for i := 0; i < n; i++ {
			from, to := d.Points[i], d.Points[i+1]
			steps := int(per / every)
			for s := 1; s <= steps; s++ {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(every):
				}
				if err := tick(between(from, to, float32(s)/float32(steps))); err != nil {
					return err
				}
			}
			if err := tick(to); err != nil {
				return err
			}
		}
	}

	// Resting at the end.
	return w.hold(ctx, d.Points[len(d.Points)-1], d.Dwell, every, tick)
}

// hold sends updates at one point for a while.
func (w *Session) hold(ctx context.Context, at Touch, d, every time.Duration, tick func(Touch) error) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		wait := every
		if left := time.Until(deadline); left < wait {
			wait = left
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		if err := tick(at); err != nil {
			return err
		}
	}
	return nil
}

// between is one point part of the way to another.
//
// Everything continuous is interpolated; the face is not, since there
// is nothing between face 2 and face 3.  It takes the face it is
// travelling FROM until it arrives, which is what a finger sliding off
// one face onto another does.
func between(a, b Touch, t float32) Touch {
	return Touch{
		Face:     a.Face,
		UV:       lerp3(a.UV, b.UV, t),
		ST:       lerp3(a.ST, b.ST, t),
		Position: lerp3(a.Position, b.Position, t),
		Normal:   lerp3(a.Normal, b.Normal, t),
		Binormal: lerp3(a.Binormal, b.Binormal, t),
		Offset:   lerp3(a.Offset, b.Offset, t),
	}
}

func lerp3(a, b msg.Vector3, t float32) msg.Vector3 {
	return msg.Vector3{
		X: a.X + (b.X-a.X)*t,
		Y: a.Y + (b.Y-a.Y)*t,
		Z: a.Z + (b.Z-a.Z)*t,
	}
}

// TouchHold touches an object and keeps touching it, without moving.
//
// The script sees touch_start, a stream of touch, then touch_end, which
// is what holding the mouse down on something does.  A test for a
// "press and hold" script needs this rather than Touch, since a click
// produces one touch and proves nothing about the counting.
func (w *Session) TouchHold(ctx context.Context, o *Object, t Touch, d time.Duration) error {
	return w.Drag(ctx, o, Drag{Points: []Touch{t}, Dwell: d})
}
