package sl

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// aThing is the prim the touches here are aimed at, found in the region
// the session is in.
func aThing(w *Session) *Object {
	return foundHere(w, &Object{ID: thePrim, Local: 4242, Name: "a thing"})
}

// TestAClickIsAGrabAndADeGrab, in that order and nothing else: that is
// what a viewer sends and what a script counting touch_start against
// touch_end is entitled to see.
func TestAClickIsAGrabAndADeGrab(t *testing.T) {
	w, f := newFakeSession(t)
	if err := w.Touch(context.Background(), aThing(w), Touch{}); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	sent := f.Sent()
	if len(sent) != 2 {
		t.Fatalf("a click sent %d messages", len(sent))
	}
	grab, ok := sent[0].Msg.(*msg.ObjectGrab)
	if !ok {
		t.Fatalf("first message is %T", sent[0].Msg)
	}
	if _, ok := sent[1].Msg.(*msg.ObjectDeGrab); !ok {
		t.Fatalf("second message is %T", sent[1].Msg)
	}
	if grab.ObjectData.LocalID != 4242 {
		t.Errorf("grabbed local id %d", grab.ObjectData.LocalID)
	}
}

// TestAnEmptyTouchIsTheMiddleOfFaceZero.  Zero is a real value for
// every one of these fields and the wrong default for most: a touch
// with no normal points nowhere, and a script reading
// llDetectedTouchNormal gets a zero vector no viewer would ever send.
func TestAnEmptyTouchIsTheMiddleOfFaceZero(t *testing.T) {
	w, f := newFakeSession(t)
	if err := w.TouchStart(context.Background(), aThing(w), Touch{}); err != nil {
		t.Fatal(err)
	}
	grab := onlySent[*msg.ObjectGrab](t, f)
	s := grab.SurfaceInfo[0]
	if s.UVCoord != (msg.Vector3{X: 0.5, Y: 0.5}) || s.STCoord != (msg.Vector3{X: 0.5, Y: 0.5}) {
		t.Errorf("uv %v st %v, want the middle", s.UVCoord, s.STCoord)
	}
	if s.Normal != (msg.Vector3{Z: 1}) {
		t.Errorf("normal %v, want straight up", s.Normal)
	}
	if s.FaceIndex != 0 {
		t.Errorf("face %d", s.FaceIndex)
	}
}

// TestEveryFieldReachesTheWire: the whole point is naming a point on a
// face, so a field that is dropped is a test that silently touches
// somewhere else.
func TestEveryFieldReachesTheWire(t *testing.T) {
	w, f := newFakeSession(t)
	want := Touch{
		Face:     3,
		UV:       msg.Vector3{X: 0.9, Y: 0.25},
		ST:       msg.Vector3{X: 0.1, Y: 0.2},
		Position: msg.Vector3{X: 254, Y: 186, Z: 22},
		Normal:   msg.Vector3{X: 1},
		Binormal: msg.Vector3{Z: 1},
		Offset:   msg.Vector3{X: 0.1, Y: 0.2, Z: 0.3},
	}
	if err := w.TouchStart(context.Background(), aThing(w), want); err != nil {
		t.Fatal(err)
	}
	grab := onlySent[*msg.ObjectGrab](t, f)
	s := grab.SurfaceInfo[0]
	switch {
	case s.FaceIndex != 3:
		t.Errorf("face %d", s.FaceIndex)
	case s.UVCoord != want.UV || s.STCoord != want.ST:
		t.Errorf("uv %v st %v", s.UVCoord, s.STCoord)
	case s.Position != want.Position:
		t.Errorf("position %v", s.Position)
	case s.Normal != want.Normal || s.Binormal != want.Binormal:
		t.Errorf("normal %v binormal %v", s.Normal, s.Binormal)
	case grab.ObjectData.GrabOffset != want.Offset:
		t.Errorf("offset %v", grab.ObjectData.GrabOffset)
	}
}

// TestAHeldTouchKeepsSendingUpdates, and ends even when it is cut
// short: a grab left open makes the next touch of that object look
// like a continuation of this one.
func TestAHeldTouchKeepsSendingUpdates(t *testing.T) {
	w, f := newFakeSession(t)
	err := w.TouchHold(context.Background(), aThing(w), Touch{}, 350*time.Millisecond)
	if err != nil {
		t.Fatalf("TouchHold: %v", err)
	}
	if n := len(sentOf[*msg.ObjectGrabUpdate](f)); n < 2 {
		t.Errorf("a third of a second held sent %d updates, want at least 2", n)
	}
	if n := len(sentOf[*msg.ObjectDeGrab](f)); n != 1 {
		t.Errorf("%d degrabs", n)
	}

	// The update names the object by id, which is the one message of
	// the three that does.
	up := sentOf[*msg.ObjectGrabUpdate](f)[0]
	if up.ObjectData.ObjectID != thePrim {
		t.Errorf("the update named %s", up.ObjectData.ObjectID)
	}
}

// TestACancelledHoldStillLetsGo.
func TestACancelledHoldStillLetsGo(t *testing.T) {
	w, f := newFakeSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	if err := w.TouchHold(ctx, aThing(w), Touch{}, time.Minute); err == nil {
		t.Error("a cancelled hold returned no error")
	}
	if n := len(sentOf[*msg.ObjectDeGrab](f)); n != 1 {
		t.Errorf("%d degrabs after a cancelled hold, want 1", n)
	}
}

// TestTouchingNothing: a local id of zero is an attachment or an
// object nothing has described, and grabbing it grabs whatever happens
// to hold that id.
func TestTouchingNothing(t *testing.T) {
	w, _ := newFakeSession(t)
	ctx := context.Background()
	if err := w.Touch(ctx, &Object{ID: thePrim}, Touch{}); err == nil {
		t.Error("touched an object with no local id")
	}
	if err := w.TouchMove(ctx, &Object{Local: 1}, Touch{}, 0); err == nil {
		t.Error("moved a touch on an object with no id")
	}
}

// TestADragVisitsEveryPointItIsGiven, whatever the rate.  A path is a
// description of where the touch went, and skipping a corner because
// the clock did not land on it would be a drag that took a shortcut.
func TestADragVisitsEveryPointItIsGiven(t *testing.T) {
	w, f := newFakeSession(t)
	at := func(x float32) Touch { return Touch{Position: msg.Vector3{X: x}} }

	err := w.Drag(context.Background(), aThing(w), Drag{
		Points: []Touch{at(1), at(2), at(3)},
		Move:   120 * time.Millisecond,
		Rate:   2, // one update every 500ms: slower than the whole drag
	})
	if err != nil {
		t.Fatalf("Drag: %v", err)
	}

	var xs []float32
	for _, u := range sentOf[*msg.ObjectGrabUpdate](f) {
		xs = append(xs, u.SurfaceInfo[0].Position.X)
	}
	if len(xs) < 2 {
		t.Fatalf("a three point drag sent %d updates", len(xs))
	}
	if xs[len(xs)-1] != 3 {
		t.Errorf("the drag ended at %v, want the last point", xs[len(xs)-1])
	}
	var sawMiddle bool
	for _, x := range xs {
		if x == 2 {
			sawMiddle = true
		}
	}
	if !sawMiddle {
		t.Errorf("the middle point was skipped: %v", xs)
	}
}

// TestADragInterpolatesBetweenPoints, so that a script following one
// sees it move rather than jump.
func TestADragInterpolatesBetweenPoints(t *testing.T) {
	w, f := newFakeSession(t)
	err := w.Drag(context.Background(), aThing(w), Drag{
		Points: []Touch{{UV: msg.Vector3{X: 0}}, {UV: msg.Vector3{X: 1}}},
		Move:   200 * time.Millisecond,
		Rate:   50,
	})
	if err != nil {
		t.Fatalf("Drag: %v", err)
	}
	var between int
	for _, u := range sentOf[*msg.ObjectGrabUpdate](f) {
		if x := u.SurfaceInfo[0].UVCoord.X; x > 0 && x < 1 {
			between++
		}
	}
	if between < 3 {
		t.Errorf("only %d updates fell between the two points", between)
	}
}

// TestTheFaceDoesNotInterpolate: there is nothing between face 2 and
// face 3, so a drag from one to the other is on the first until it
// arrives.
func TestTheFaceDoesNotInterpolate(t *testing.T) {
	w, f := newFakeSession(t)
	err := w.Drag(context.Background(), aThing(w), Drag{
		Points: []Touch{{Face: 2}, {Face: 3}},
		Move:   150 * time.Millisecond,
		Rate:   50,
	})
	if err != nil {
		t.Fatalf("Drag: %v", err)
	}
	ups := sentOf[*msg.ObjectGrabUpdate](f)
	for i, u := range ups {
		got := u.SurfaceInfo[0].FaceIndex
		if got != 2 && got != 3 {
			t.Fatalf("update %d is on face %d", i, got)
		}
		if i < len(ups)-1 && got == 3 {
			t.Errorf("update %d arrived at face 3 early", i)
		}
	}
	if ups[len(ups)-1].SurfaceInfo[0].FaceIndex != 3 {
		t.Error("the drag never reached face 3")
	}
}

// TestThePressAndTheDwellAreSeparate: a script can tell a long press
// from a long rest at the end, so the two are not one duration.
func TestThePressAndTheDwellAreSeparate(t *testing.T) {
	w, f := newFakeSession(t)
	err := w.Drag(context.Background(), aThing(w), Drag{
		Points: []Touch{{UV: msg.Vector3{X: 0.1}}, {UV: msg.Vector3{X: 0.9}}},
		Press:  150 * time.Millisecond,
		Move:   100 * time.Millisecond,
		Dwell:  150 * time.Millisecond,
		Rate:   40,
	})
	if err != nil {
		t.Fatalf("Drag: %v", err)
	}
	ups := sentOf[*msg.ObjectGrabUpdate](f)
	var atStart, atEnd int
	for _, u := range ups {
		switch u.SurfaceInfo[0].UVCoord.X {
		case 0.1:
			atStart++
		case 0.9:
			atEnd++
		}
	}
	if atStart < 3 {
		t.Errorf("%d updates at the first point, want the press to hold there", atStart)
	}
	if atEnd < 3 {
		t.Errorf("%d updates at the last point, want the dwell to rest there", atEnd)
	}
}

// TestADragNeedsSomewhereToStart.
func TestADragNeedsSomewhereToStart(t *testing.T) {
	w, _ := newFakeSession(t)
	if err := w.Drag(context.Background(), aThing(w), Drag{}); err == nil {
		t.Error("a drag with no points was accepted")
	}
}

// turned is a face whose texture is turned a quarter, repeated twice
// across and half down, and slid a quarter one way and a tenth the
// other, so that one coordinate taken for the other shows.  The wire's
// numbers are the viewer's: offsets over 0x7fff and rotation over
// 0x8000 of a turn (llprimitive.cpp:1464-1466).
func turned() Face {
	f := PlainFaces(1)[0]
	f.ScaleS, f.ScaleT = 2, 0.5
	f.OffsetS, f.OffsetT = 8192, -3277 // 0.25, -0.1
	f.Rotation = 8192                  // a quarter turn
	return f
}

// near is whether two coordinates are the same to a hundred-thousandth,
// which is as near as the wire's sixteen-bit offsets allow.
func near(a, b msg.Vector3) bool {
	return math.Abs(float64(a.X-b.X)) < 1e-4 && math.Abs(float64(a.Y-b.Y)) < 1e-4 && a.Z == b.Z
}

// TestTheSurfaceBecomesTheTexture as the viewer's xform turns it
// (llface.cpp:760-785): about the middle, turned, repeated, slid.  The
// values are worked by hand.
//
// A quarter turn of <0.75, 0.6>, a quarter and a tenth from the middle,
// is <0.1, -0.25>; repeated 2 and 0.5 it is <0.2, -0.125>; slid 0.25 and
// -0.1 and put back about the middle it is <0.95, 0.275>.
//
// An eighth turn of <0.2, 0.9>, which is <-0.3, 0.4> from the middle,
// is <0.0707107, 0.4949747>; repeated 3 and -1 it is <0.2121320,
// -0.4949747>; slid 0 and 0.5000153 and put back it is <0.7121320,
// 0.5050406>.
func TestTheSurfaceBecomesTheTexture(t *testing.T) {
	eighth := PlainFaces(1)[0]
	eighth.ScaleS, eighth.ScaleT = 3, -1
	eighth.OffsetT = 16384
	eighth.Rotation = 4096

	for _, c := range []struct {
		what   string
		f      Face
		st, uv msg.Vector3
	}{
		{"a quarter turn", turned(), msg.Vector3{X: 0.75, Y: 0.6}, msg.Vector3{X: 0.95, Y: 0.275}},
		{"an eighth turn", eighth, msg.Vector3{X: 0.2, Y: 0.9}, msg.Vector3{X: 0.7121320, Y: 0.5050406}},
		{"nothing done to it", PlainFaces(1)[0], msg.Vector3{X: 0.3, Y: 0.8}, msg.Vector3{X: 0.3, Y: 0.8}},
	} {
		if got := c.f.SurfaceToTexture(c.st); !near(got, c.uv) {
			t.Errorf("%s: %v on the face is %v in the texture, want %v", c.what, c.st, got, c.uv)
		}
		got, err := c.f.TextureToSurface(c.uv)
		if err != nil || !near(got, c.st) {
			t.Errorf("%s: %v in the texture is %v, %v on the face, want %v", c.what, c.uv, got, err, c.st)
		}
	}

	flat := turned()
	flat.ScaleT = 0
	if _, err := flat.TextureToSurface(msg.Vector3{X: 0.5, Y: 0.5}); err == nil {
		t.Error("a texture repeated zero times was undone to a point")
	}
}

// aTurnedThing stands a prim in the region whose faces are: 0 plain,
// 1 turned, 2 turned with planar mapping, 3 turned under a running
// texture animation, 4 turned and repeated zero times down.
func aTurnedThing(t *testing.T, w *Session, f *fakeBackend) *Object {
	t.Helper()
	faces := PlainFaces(5)
	for i := 1; i < 5; i++ {
		faces[i] = turned()
	}
	faces[2].Media = 0x02
	faces[4].ScaleT = 0
	te, err := EncodeTextureEntry(faces)
	if err != nil {
		t.Fatal(err)
	}
	anim := make([]byte, 16)
	anim[0], anim[1], anim[2], anim[3] = 0x01, 3, 1, 1 // on, face 3
	f.mu.Lock()
	f.objects = []*Seen{{Object: Object{ID: thePrim, Local: 4242, Name: "a thing"},
		TextureEntry: te, TextureAnim: anim}}
	f.mu.Unlock()
	return aThing(w)
}

// TestATouchGivenOneCoordinateSendsBoth, the other worked out as the
// viewer works it (llviewerwindow.cpp:7686-7697): a script choosing a
// button by llDetectedTouchST saw the middle of the face whatever --uv
// said, because ST went as 0.5, 0.5.  Where the viewer uses more than
// the texture entry -- planar mapping, a texture animation -- and where
// there is no face, the one given goes for both.
func TestATouchGivenOneCoordinateSendsBoth(t *testing.T) {
	st := msg.Vector3{X: 0.75, Y: 0.6}
	uv := msg.Vector3{X: 0.95, Y: 0.275}
	for _, c := range []struct {
		what           string
		touch          Touch
		wantST, wantUV msg.Vector3
	}{
		{"st on a turned face", Touch{Face: 1, ST: st}, st, uv},
		{"uv on a turned face", Touch{Face: 1, UV: uv}, st, uv},
		{"neither on a turned face", Touch{Face: 1}, middle, msg.Vector3{X: 0.75, Y: 0.4}},
		{"st on a plain face", Touch{Face: 0, ST: st}, st, st},
		{"st on a planar face", Touch{Face: 2, ST: st}, st, st},
		{"uv on an animated face", Touch{Face: 3, UV: uv}, uv, uv},
		{"st on a face repeated zero times", Touch{Face: 4, ST: st}, st, msg.Vector3{X: 0.95, Y: 0.4}},
		{"uv on no face", Touch{Face: -1, UV: uv}, uv, uv},
		{"uv on a face it does not have", Touch{Face: 9, UV: uv}, uv, uv},
		{"both, anywhere", Touch{Face: 1, ST: st, UV: st}, st, st},
	} {
		w, f := newFakeSession(t)
		if err := w.TouchStart(context.Background(), aTurnedThing(t, w, f), c.touch); err != nil {
			t.Errorf("%s: %v", c.what, err)
			continue
		}
		s := onlySent[*msg.ObjectGrab](t, f).SurfaceInfo[0]
		if !near(s.STCoord, c.wantST) || !near(s.UVCoord, c.wantUV) {
			t.Errorf("%s: sent st %v uv %v, want st %v uv %v", c.what, s.STCoord, s.UVCoord, c.wantST, c.wantUV)
		}
	}

	// A texture coordinate on a face that repeats zero times is no one
	// point on it, and nothing is sent.
	w, f := newFakeSession(t)
	if err := w.Touch(context.Background(), aTurnedThing(t, w, f), Touch{Face: 4, UV: uv}); err == nil {
		t.Error("a uv on a face repeated zero times was touched")
	}
	if n := len(sentOf[*msg.ObjectGrab](f)); n != 0 {
		t.Errorf("%d grabs went out for it", n)
	}
}

// TestADragAlongAFaceSendsTheTextureToo: every point of a drag is placed
// once, before it starts, and every update carries both coordinates.
func TestADragAlongAFaceSendsTheTextureToo(t *testing.T) {
	w, f := newFakeSession(t)
	o := aTurnedThing(t, w, f)
	err := w.Drag(context.Background(), o, Drag{
		Points: []Touch{{Face: 1, ST: msg.Vector3{X: 0.75, Y: 0.6}}, {Face: 1, ST: msg.Vector3{X: 0.25, Y: 0.6}}},
		Move:   60 * time.Millisecond,
		Rate:   50,
	})
	if err != nil {
		t.Fatalf("Drag: %v", err)
	}
	ups := sentOf[*msg.ObjectGrabUpdate](f)
	if len(ups) == 0 {
		t.Fatal("no updates")
	}
	for _, u := range ups {
		s := u.SurfaceInfo[0]
		if want := turned().SurfaceToTexture(s.STCoord); !near(s.UVCoord, want) {
			t.Errorf("an update at st %v sent uv %v, want %v", s.STCoord, s.UVCoord, want)
		}
	}
	last := ups[len(ups)-1].SurfaceInfo[0]
	if !near(last.STCoord, msg.Vector3{X: 0.25, Y: 0.6}) {
		t.Errorf("the drag ended at st %v", last.STCoord)
	}
	if n := len(sentOf[*msg.RequestMultipleObjects](f)); n != 0 {
		t.Errorf("the appearance was asked for %d times though it was known", n)
	}
}
