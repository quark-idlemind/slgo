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
	if o == nil || o.Local == 0 {
		return fmt.Errorf("sl: nothing to touch")
	}
	t = t.fill()
	m := &msg.ObjectGrab{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.ObjectData.LocalID = o.Local
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
	if o == nil || o.Local == 0 {
		return fmt.Errorf("sl: nothing to touch")
	}
	t = t.fill()
	m := &msg.ObjectDeGrab{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.ObjectData.LocalID = o.Local
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

// TouchInterval is how often a held touch sends an update.
//
// A viewer sends one per frame while the mouse is down, which is more
// often than anything needs; ten a second is enough for a script
// counting touch events to see a steady stream and cheap enough not to
// matter.
const TouchInterval = 100 * time.Millisecond

// TouchHold touches an object and keeps touching it.
//
// The script sees touch_start, then a touch every TouchInterval for as
// long as asked, then touch_end -- which is what holding the mouse down
// on something does.  A test for a "press and hold" script needs this
// rather than Touch, since a click produces one touch and proves
// nothing about the counting.
//
// It always ends the touch, including when the context is cancelled:
// leaving a grab open makes the next thing to touch that object look
// like it is continuing this one.
func (w *Session) TouchHold(ctx context.Context, o *Object, t Touch, d time.Duration) error {
	if err := w.TouchStart(ctx, o, t); err != nil {
		return err
	}
	defer func() {
		// A cancelled context cannot send the degrab, so the end goes
		// out on one that is still alive.
		end, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = w.TouchEnd(end, o, t)
	}()

	deadline := time.Now().Add(d)
	last := time.Now()
	for {
		wait := TouchInterval
		if left := time.Until(deadline); left < wait {
			wait = left
		}
		if wait <= 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		now := time.Now()
		if err := w.TouchMove(ctx, o, t, now.Sub(last)); err != nil {
			return err
		}
		last = now
	}
}
