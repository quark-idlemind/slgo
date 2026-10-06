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
// llDetectedTouchFace, llDetectedTouchPos, llDetectedTouchST,
// llDetectedTouchUV and the rest.  See send_ObjectGrab_message in the
// viewer's lltoolgrab.cpp, which packs pick.mIntersection straight into
// the message.
//
// That is worth knowing because it makes this MORE precise than a
// viewer, not less.  A test that has to touch the third face of a prim
// nine tenths of the way along one edge does not have to place a camera
// and aim: it says face 3, st <0.9, 0.5>.
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
	"math"
	"slices"
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

	// ST is where on the face, each 0 to 1 across it, which is what
	// llDetectedTouchST reports.  UV is the same point in the texture's
	// coordinates -- after the face's repeats, offset and rotation --
	// which is what llDetectedTouchUV reports.  A viewer's raycast finds
	// the first and works the second out from it (LLPickInfo::
	// getSurfaceInfo, llviewerwindow.cpp:7686-7697), and sends both
	// (lltoolgrab.cpp:1200-1202).
	//
	// Give both and both go as given.  Give one and the other is worked
	// out from the face's texture entry as the viewer does -- see
	// Face.SurfaceToTexture -- and give neither and the touch is in the
	// middle of the face.  Zero means not given.
	//
	// The entry is not all the viewer uses for a face with planar
	// mapping or a running texture animation, and nothing here follows
	// it there: such a face, a face that is not the object's, no face,
	// and an object whose appearance cannot be read are touched with
	// the given coordinate sent for both, which is what the viewer
	// sends for a face with no entry (llface.cpp:910-914).  The entry
	// is read as Faces reads it, and can be as out of date as Faces's.
	ST msg.Vector3
	UV msg.Vector3

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

// fill supplies the defaults for the surface directions, so that a
// zero Touch points up rather than nowhere.  The coordinates are
// placeTouches's.
func (t Touch) fill() Touch {
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

// middle is the centre of a face, where a touch that names neither
// coordinate lands.
var middle = msg.Vector3{X: 0.5, Y: 0.5}

// SurfaceToTexture is the texture coordinate of a point on this face, as
// the viewer works it out from the texture entry: about the middle of
// the face, turned by the rotation, stretched by the repeats and moved
// by the offset (LLFace::surfaceToTexture and xform, llface.cpp:760-785
// and 904-960).
func (f Face) SurfaceToTexture(st msg.Vector3) msg.Vector3 {
	sin, cos := math.Sincos(float64(f.RotationRad()))
	offS, offT := f.OffsetsF()
	s, t := float64(st.X)-0.5, float64(st.Y)-0.5
	s, t = s*cos+t*sin, -s*sin+t*cos
	return msg.Vector3{
		X: float32(s*float64(f.ScaleS) + float64(offS) + 0.5),
		Y: float32(t*float64(f.ScaleT) + float64(offT) + 0.5),
	}
}

// TextureToSurface is SurfaceToTexture undone: the point on this face
// that a texture coordinate is at.  A face whose texture repeats zero
// times either way has no one such point, and is refused.
func (f Face) TextureToSurface(uv msg.Vector3) (msg.Vector3, error) {
	if f.ScaleS == 0 || f.ScaleT == 0 {
		return msg.Vector3{}, fmt.Errorf("the face repeats its texture %gx%g times, "+
			"so a texture coordinate is no one point on it", f.ScaleS, f.ScaleT)
	}
	sin, cos := math.Sincos(float64(f.RotationRad()))
	offS, offT := f.OffsetsF()
	a := (float64(uv.X) - 0.5 - float64(offS)) / float64(f.ScaleS)
	b := (float64(uv.Y) - 0.5 - float64(offT)) / float64(f.ScaleT)
	return msg.Vector3{
		X: float32(a*cos - b*sin + 0.5),
		Y: float32(a*sin + b*cos + 0.5),
	}, nil
}

// Planar reports whether the face maps its texture by planar projection
// (TEX_GEN_PLANAR in the media byte, lltextureentry.h:64-79), where the
// viewer's texture coordinate is not the entry's transform alone.
func (f Face) Planar() bool { return f.Media&0x06 == 0x02 }

// animated reports whether a texture animation runs on this face: a
// block of sixteen whose mode has ON set, for the face it names, or for
// every face when it names none of the object's
// (llvovolume.cpp:740-744, llviewertextureanim.cpp:83).
func animated(anim []byte, face, faces int) bool {
	if len(anim) != 16 || anim[0]&0x01 == 0 {
		return false
	}
	on := int(int8(anim[1]))
	return on < 0 || on >= faces || on == face
}

// placeTouches works out, for each touch, whichever of ST and UV it was
// not given; see Touch.  The object's appearance is read once, and only
// if a touch needs it.
func (w *Session) placeTouches(ctx context.Context, o *Object, ts ...Touch) ([]Touch, error) {
	out := slices.Clone(ts)
	var faces []Face
	var anim []byte
	var n int
	read := false
	for i, t := range out {
		haveST, haveUV := t.ST != (msg.Vector3{}), t.UV != (msg.Vector3{})
		if haveST && haveUV {
			continue
		}
		if !haveST && !haveUV {
			t.ST, haveST = middle, true
		}
		if !read && t.Face >= 0 {
			faces, anim, n = w.looksOf(ctx, o)
			read = true
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		mapping, entry := mappingOf(faces, anim, n, t.Face)
		switch {
		case !entry && haveST:
			t.UV = t.ST
		case !entry:
			t.ST = t.UV
		case haveST:
			t.UV = mapping.SurfaceToTexture(t.ST)
		default:
			st, err := mapping.TextureToSurface(t.UV)
			if err != nil {
				return nil, fmt.Errorf("sl: touching face %d of %s at uv %g,%g: %w", t.Face, o, t.UV.X, t.UV.Y, err)
			}
			t.ST = st
		}
		out[i] = t
	}
	return out, nil
}

// looksOf is what o looks like, face by face, its texture animation, and
// how many faces it has as the viewer counts them (the count the
// animation is checked against), read as Faces reads them.  Nothing comes
// back when they cannot be read, which a touch takes as a face with no
// entry.
func (w *Session) looksOf(ctx context.Context, o *Object) ([]Face, []byte, int) {
	if o == nil || o.ID.IsZero() {
		return nil, nil, 0
	}
	found, err := w.fetch(ctx, "", o.ID.String())
	if err != nil || len(found) == 0 {
		return nil, nil, 0
	}
	faces, seen, _, err := w.appearance(ctx, o, found[0])
	if err != nil {
		return nil, nil, 0
	}
	n, ok := seen.FaceCount()
	if !ok {
		n = len(faces)
	}
	return faces, seen.TextureAnim, n
}

// mappingOf is the texture mapping the viewer applies to a touch on a
// face, from what the object looks like: the face's own entry, unless the
// face is not the object's, is mapped by planar projection or runs a
// texture animation, where the viewer uses more than the entry and a
// touch sends ST for UV.  n is the object's number of faces.  Every place
// a UV is worked out from an ST -- a touch, a drag on the screen -- asks
// here, and nowhere else calls Face.SurfaceToTexture
// (TestUVIsWorkedOutInOnePlace).
// Why: doc/slate-runner.md#stimuli
func mappingOf(faces []Face, anim []byte, n, face int) (Face, bool) {
	if face < 0 || face >= len(faces) || faces[face].Planar() || animated(anim, face, n) {
		return Face{}, false
	}
	return faces[face], true
}

// TouchStart begins a touch: the script's touch_start.
//
// Nothing waits for anything.  A touch is not acknowledged, the script
// may not be listening, and it may take a second to do whatever it does
// -- so what a caller waits for is the script's own output, not this.
func (w *Session) TouchStart(ctx context.Context, o *Object, t Touch) error {
	ts, err := w.placeTouches(ctx, o, t)
	if err != nil {
		return err
	}
	return w.touchStart(ctx, o, ts[0])
}

func (w *Session) touchStart(ctx context.Context, o *Object, t Touch) error {
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
	ts, err := w.placeTouches(ctx, o, t)
	if err != nil {
		return err
	}
	return w.touchMove(ctx, o, ts[0], held)
}

func (w *Session) touchMove(ctx context.Context, o *Object, t Touch, held time.Duration) error {
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
	ts, err := w.placeTouches(ctx, o, t)
	if err != nil {
		return err
	}
	return w.touchEnd(ctx, o, ts[0])
}

func (w *Session) touchEnd(ctx context.Context, o *Object, t Touch) error {
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
	ts, err := w.placeTouches(ctx, o, t)
	if err != nil {
		return err
	}
	if err := w.touchStart(ctx, o, ts[0]); err != nil {
		return err
	}
	return w.touchEnd(ctx, o, ts[0])
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
// Measured on Aditi, the beta grid, holding a touch on a counting script
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
// something it can do and this cannot: the points may be on different
// faces, where distance means nothing.
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

	// Every point placed at once, so the appearance is read once and
	// not with every update.
	points, err := w.placeTouches(ctx, o, d.Points...)
	if err != nil {
		return err
	}

	if err := w.touchStart(ctx, o, points[0]); err != nil {
		return err
	}
	defer func() {
		end, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = w.touchEnd(end, o, points[len(points)-1])
	}()

	last := time.Now()
	tick := func(at Touch) error {
		now := time.Now()
		err := w.touchMove(ctx, o, at, now.Sub(last))
		last = now
		return err
	}

	// Held still at the start.
	if err := w.hold(ctx, points[0], d.Press, every, tick); err != nil {
		return err
	}

	// Along the path.  Each segment gets an equal share of the time and
	// however many updates fit in it, and the point itself is always
	// sent, so a path is never skipped over by a rate too slow for it.
	if n := len(points) - 1; n > 0 && d.Move > 0 {
		per := d.Move / time.Duration(n)
		for i := 0; i < n; i++ {
			from, to := points[i], points[i+1]
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
	return w.hold(ctx, points[len(points)-1], d.Dwell, every, tick)
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
