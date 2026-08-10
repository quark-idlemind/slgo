package sl

import (
	"context"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

func aThing() *Object { return &Object{ID: thePrim, Local: 4242, Name: "a thing"} }

// TestAClickIsAGrabAndADeGrab, in that order and nothing else: that is
// what a viewer sends and what a script counting touch_start against
// touch_end is entitled to see.
func TestAClickIsAGrabAndADeGrab(t *testing.T) {
	w, f := newFakeSession(t)
	if err := w.Touch(context.Background(), aThing(), Touch{}); err != nil {
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
	if err := w.TouchStart(context.Background(), aThing(), Touch{}); err != nil {
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
	if err := w.TouchStart(context.Background(), aThing(), want); err != nil {
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
	err := w.TouchHold(context.Background(), aThing(), Touch{}, 350*time.Millisecond)
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
	if err := w.TouchHold(ctx, aThing(), Touch{}, time.Minute); err == nil {
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

	err := w.Drag(context.Background(), aThing(), Drag{
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
	err := w.Drag(context.Background(), aThing(), Drag{
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
	err := w.Drag(context.Background(), aThing(), Drag{
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
	err := w.Drag(context.Background(), aThing(), Drag{
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
	if err := w.Drag(context.Background(), aThing(), Drag{}); err == nil {
		t.Error("a drag with no points was accepted")
	}
}
