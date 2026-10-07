package sl

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// wornHUD puts a half-metre box on Center 2 in the fake region, worn by
// an avatar, and returns it.
func wornHUD(t *testing.T, f *fakeBackend) *Seen {
	t.Helper()
	box := prim(t, "box", 0.5, front, msg.Quaternion{})
	box.Object = Object{ID: thePrim, Local: 77}
	box.PCode = pcodePrim
	box.Parent = 10
	box.LinkKnown = true // a lone worn prim: nothing to put in order
	f.mu.Lock()
	f.objects = []*Seen{
		{Object: Object{ID: theOther, Local: 10}, PCode: pcodeAvatar},
		box,
	}
	f.mu.Unlock()
	return box
}

// rescale gives the box a new description with the scale, as the store
// does, after a pause.
func rescale(f *fakeBackend, after time.Duration, scale msg.Vector3) {
	go func() {
		time.Sleep(after)
		f.mu.Lock()
		defer f.mu.Unlock()
		next := *f.objects[1]
		next.Scale = scale
		f.objects[1] = &next
	}()
}

var grownGlass = msg.Vector3{X: 0.01, Y: 4, Z: 2}

// The middle of the world view, where the box is, and a corner of it,
// where it is not.
var (
	onTheBox  = ScreenPoint{1920, 1025}
	offTheBox = ScreenPoint{100, 100}
)

// TestADragOnTheScreenHoldsThePrimPressed: pressed on the box and moved
// off it, the touch stays on the box, as a viewer's does, and off it is
// sent as face -1 at S,T -1,-1.  It lets go of the box.
func TestADragOnTheScreenHoldsThePrimPressed(t *testing.T) {
	w, f := newFakeSession(t)
	box := wornHUD(t, f)

	err := w.DragOnScreen(context.Background(), &box.Object, ScreenDrag{
		View: measured, Points: []ScreenPoint{onTheBox, offTheBox},
		Move: 60 * time.Millisecond, Rate: 100,
	})
	if err != nil {
		t.Fatalf("DragOnScreen: %v", err)
	}

	grabs := sentOf[*msg.ObjectGrab](f)
	if len(grabs) != 1 || grabs[0].ObjectData.LocalID != box.Local {
		t.Fatalf("grabs: %+v, want one of the box", grabs)
	}
	if s := grabs[0].SurfaceInfo[0]; s.FaceIndex != 4 || s.STCoord != (msg.Vector3{X: 0.5, Y: 0.5}) {
		t.Errorf("pressed at face %d S,T %v, want face 4 at 0.5,0.5", s.FaceIndex, s.STCoord)
	}
	updates := sentOf[*msg.ObjectGrabUpdate](f)
	if len(updates) == 0 {
		t.Fatal("no updates were sent")
	}
	for _, u := range updates {
		if u.ObjectData.ObjectID != box.ID {
			t.Errorf("an update went to %s, not the box held", u.ObjectData.ObjectID)
		}
	}
	end := updates[len(updates)-1].SurfaceInfo[0]
	if end.FaceIndex != -1 || end.STCoord != (msg.Vector3{X: -1, Y: -1}) {
		t.Errorf("off the box the touch was face %d at %v, want -1 at -1,-1", end.FaceIndex, end.STCoord)
	}
	if n := len(sentOf[*msg.ObjectDeGrab](f)); n != 1 {
		t.Errorf("let go %d times", n)
	}
}

// TestADragOnTheScreenFollowsAHUDThatGrows: a HUD that grows its prim
// over the screen when pressed is waited for, and then the far point is
// on it.
func TestADragOnTheScreenFollowsAHUDThatGrows(t *testing.T) {
	w, f := newFakeSession(t)
	box := wornHUD(t, f)
	f.mu.Lock()
	f.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.ObjectDeGrab); ok {
			rescale(f, 100*time.Millisecond, box.Scale)
		}
		if _, ok := m.(*msg.ObjectGrab); ok {
			go func() {
				time.Sleep(100 * time.Millisecond)
				// A new description rather than a change to the one
				// already handed out, as the store gives them.
				f.mu.Lock()
				grown := *f.objects[1]
				grown.Scale = msg.Vector3{X: 0.01, Y: 4, Z: 2}
				f.objects[1] = &grown
				f.mu.Unlock()
			}()
		}
	}
	f.mu.Unlock()

	err := w.DragOnScreen(context.Background(), &box.Object, ScreenDrag{
		View: measured, Points: []ScreenPoint{onTheBox, offTheBox},
		Move: 60 * time.Millisecond, Rate: 100, Settle: true,
	})
	if err != nil {
		t.Fatalf("DragOnScreen: %v", err)
	}
	updates := sentOf[*msg.ObjectGrabUpdate](f)
	if len(updates) == 0 {
		t.Fatal("no updates were sent")
	}
	for _, u := range updates {
		if u.SurfaceInfo[0].FaceIndex != 4 {
			t.Fatalf("after the box grew an update was on face %d, want 4", u.SurfaceInfo[0].FaceIndex)
		}
	}
}

// TestADragOnTheScreenSaysWhenTheHUDDoesNotChange: asked to settle and
// nothing changes, it says so, and still lets go.
func TestADragOnTheScreenSaysWhenTheHUDDoesNotChange(t *testing.T) {
	w, f := newFakeSession(t)
	box := wornHUD(t, f)
	w.SetOptions(Options{HUDChangeTimeout: 200 * time.Millisecond})

	err := w.DragOnScreen(context.Background(), &box.Object, ScreenDrag{
		View: measured, Points: []ScreenPoint{onTheBox, offTheBox}, Settle: true,
	})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("DragOnScreen: %v, want ErrTimeout", err)
	}
	if n := len(sentOf[*msg.ObjectDeGrab](f)); n != 1 {
		t.Errorf("let go %d times", n)
	}
}

// TestADragOnTheScreenMustStartOnSomething.
func TestADragOnTheScreenMustStartOnSomething(t *testing.T) {
	w, f := newFakeSession(t)
	box := wornHUD(t, f)
	err := w.DragOnScreen(context.Background(), &box.Object, ScreenDrag{
		View: measured, Points: []ScreenPoint{offTheBox},
	})
	if err == nil {
		t.Fatal("a drag started off the HUD was sent")
	}
	if n := len(sentOf[*msg.ObjectGrab](f)); n != 0 {
		t.Errorf("%d grabs were sent", n)
	}
}

// TestADragOnTheScreenSeesAHUDThatGrowsOnceItMoves: a HUD that grows its
// prim only when the drag starts moving, not when it is pressed, is read
// again as the drag goes, and the points after it grew are on it.
func TestADragOnTheScreenSeesAHUDThatGrowsOnceItMoves(t *testing.T) {
	w, f := newFakeSession(t)
	box := wornHUD(t, f)
	var once bool
	f.mu.Lock()
	f.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.ObjectDeGrab); ok {
			rescale(f, 100*time.Millisecond, box.Scale)
		}
		if _, ok := m.(*msg.ObjectGrabUpdate); ok && !once {
			once = true
			go func() {
				time.Sleep(100 * time.Millisecond)
				f.mu.Lock()
				grown := *f.objects[1]
				grown.Scale = msg.Vector3{X: 0.01, Y: 4, Z: 2}
				f.objects[1] = &grown
				f.mu.Unlock()
			}()
		}
	}
	f.mu.Unlock()

	err := w.DragOnScreen(context.Background(), &box.Object, ScreenDrag{
		View: measured, Points: []ScreenPoint{onTheBox, offTheBox},
		Move: 600 * time.Millisecond, Rate: 50,
	})
	if err != nil {
		t.Fatalf("DragOnScreen: %v", err)
	}
	updates := sentOf[*msg.ObjectGrabUpdate](f)
	if len(updates) < 10 {
		t.Fatalf("%d updates", len(updates))
	}
	// The cursor leaves the box as it was within the first few steps;
	// once the region has the box grown, every later point is on it.
	last := updates[len(updates)-1].SurfaceInfo[0]
	if last.FaceIndex != 4 {
		t.Errorf("the last point of the drag was face %d, want 4 on the grown box", last.FaceIndex)
	}
	off := 0
	for _, u := range updates[len(updates)/2:] {
		if u.SurfaceInfo[0].FaceIndex != 4 {
			off++
		}
	}
	if off > 0 {
		t.Errorf("%d points in the second half of the drag were off the grown box", off)
	}
}

// TestADragOnTheScreenIsRefusedOffTheView: a point of the drag off the
// world view, the start, the end or one between, is refused before
// anything is sent.
func TestADragOnTheScreenIsRefusedOffTheView(t *testing.T) {
	for _, c := range []struct {
		name string
		pts  []ScreenPoint
	}{
		{"start below", []ScreenPoint{{390, 3745}, onTheBox}},
		{"start left", []ScreenPoint{{-4776, 1000}, onTheBox}},
		{"start at the width", []ScreenPoint{{3840, 1000}, onTheBox}},
		{"end", []ScreenPoint{onTheBox, {1920, 2050}}},
		{"between", []ScreenPoint{onTheBox, {-1, 10}, onTheBox}},
	} {
		w, f := newFakeSession(t)
		box := wornHUD(t, f)
		err := w.DragOnScreen(context.Background(), &box.Object, ScreenDrag{View: measured, Points: c.pts, Move: 50 * time.Millisecond})
		if !errors.Is(err, ErrOffView) {
			t.Errorf("%s: %v, want ErrOffView", c.name, err)
		}
		if n := len(sentOf[*msg.ObjectGrab](f)) + len(sentOf[*msg.ObjectDeGrab](f)); n != 0 {
			t.Errorf("%s: %d messages were sent", c.name, n)
		}
	}
	// The last pixel of the view is on it.
	w, f := newFakeSession(t)
	box := wornHUD(t, f)
	err := w.DragOnScreen(context.Background(), &box.Object, ScreenDrag{
		View: measured, Points: []ScreenPoint{onTheBox, {3839, 2049}},
	})
	if err != nil {
		t.Errorf("the last pixel: %v", err)
	}
}

// TestADragOnTheScreenWaitsForAGlassToShrinkAfterTheRelease: a prim that
// grows once the drag moves and goes back 400 ms after the release is
// back at its size when the drag returns, and a HUD whose prim never
// changed returns at once.
func TestADragOnTheScreenWaitsForAGlassToShrinkAfterTheRelease(t *testing.T) {
	w, f := newFakeSession(t)
	box := wornHUD(t, f)
	pressed := box.Scale
	var once bool
	f.mu.Lock()
	f.onSend = func(m msg.Message) {
		switch m.(type) {
		case *msg.ObjectGrabUpdate:
			if !once {
				once = true
				rescale(f, 50*time.Millisecond, grownGlass)
			}
		case *msg.ObjectDeGrab:
			rescale(f, 400*time.Millisecond, pressed)
		}
	}
	f.mu.Unlock()

	t0 := time.Now()
	err := w.DragOnScreen(context.Background(), &box.Object, ScreenDrag{
		View: measured, Points: []ScreenPoint{onTheBox, offTheBox},
		Move: 400 * time.Millisecond, Rate: 50,
	})
	if err != nil {
		t.Fatalf("DragOnScreen: %v", err)
	}
	if took := time.Since(t0); took < 800*time.Millisecond {
		t.Errorf("returned %s after the press, before the glass was back", took)
	}
	f.mu.Lock()
	got := f.objects[1].Scale
	f.mu.Unlock()
	if got != pressed {
		t.Errorf("the prim is %v on return, want %v", got, pressed)
	}

	// A prim that never changed: no wait.
	w, f = newFakeSession(t)
	box = wornHUD(t, f)
	t0 = time.Now()
	err = w.DragOnScreen(context.Background(), &box.Object, ScreenDrag{
		View: measured, Points: []ScreenPoint{onTheBox, offTheBox}, Move: 50 * time.Millisecond,
	})
	if err != nil || time.Since(t0) > 300*time.Millisecond {
		t.Errorf("an unchanged prim: %v after %s", err, time.Since(t0))
	}
}

// TestADragOnTheScreenAcceptsAGlassThatSettlesAtANewSize: a resize
// leaves the prim at a new size 400 ms after the release; the drag
// returns after that without an error.
func TestADragOnTheScreenAcceptsAGlassThatSettlesAtANewSize(t *testing.T) {
	w, f := newFakeSession(t)
	box := wornHUD(t, f)
	resized := msg.Vector3{X: box.Scale.X, Y: box.Scale.Y * 1.16, Z: box.Scale.Z * 1.16}
	var once bool
	f.mu.Lock()
	f.onSend = func(m msg.Message) {
		switch m.(type) {
		case *msg.ObjectGrabUpdate:
			if !once {
				once = true
				rescale(f, 50*time.Millisecond, grownGlass)
			}
		case *msg.ObjectDeGrab:
			rescale(f, 400*time.Millisecond, resized)
		}
	}
	f.mu.Unlock()
	t0 := time.Now()
	err := w.DragOnScreen(context.Background(), &box.Object, ScreenDrag{
		View: measured, Points: []ScreenPoint{onTheBox, offTheBox}, Move: 400 * time.Millisecond, Rate: 50,
	})
	if err != nil {
		t.Fatalf("DragOnScreen: %v", err)
	}
	if took := time.Since(t0); took < 1500*time.Millisecond {
		t.Errorf("returned %s after the press, before the prim had kept its new size for a second", took)
	}
	f.mu.Lock()
	got := f.objects[1].Scale
	f.mu.Unlock()
	if got != resized {
		t.Errorf("the prim is %v on return, want %v", got, resized)
	}
}

// TestADragOnTheScreenSaysWhenTheGlassKeepsChanging: a prim still
// changing when the timeout runs out is reported.
func TestADragOnTheScreenSaysWhenTheGlassKeepsChanging(t *testing.T) {
	w, f := newFakeSession(t)
	box := wornHUD(t, f)
	w.SetOptions(Options{HUDChangeTimeout: 1500 * time.Millisecond})
	var once bool
	f.mu.Lock()
	f.onSend = func(m msg.Message) {
		switch m.(type) {
		case *msg.ObjectGrabUpdate:
			if !once {
				once = true
				rescale(f, 20*time.Millisecond, grownGlass)
			}
		case *msg.ObjectDeGrab:
			go func() {
				for i := 1; i < 40; i++ {
					time.Sleep(100 * time.Millisecond)
					rescale(f, 0, msg.Vector3{X: 0.01, Y: 4 + float32(i)*0.01, Z: 2})
				}
			}()
		}
	}
	f.mu.Unlock()
	t0 := time.Now()
	err := w.DragOnScreen(context.Background(), &box.Object, ScreenDrag{
		View: measured, Points: []ScreenPoint{onTheBox, offTheBox}, Move: 200 * time.Millisecond, Rate: 50,
	})
	if !errors.Is(err, ErrTimeout) || !strings.Contains(err.Error(), "still changing") {
		t.Fatalf("DragOnScreen: %v, want a timeout saying the HUD was still changing", err)
	}
	if time.Since(t0) < 1500*time.Millisecond {
		t.Errorf("gave up after %s", time.Since(t0))
	}
	if n := len(sentOf[*msg.ObjectDeGrab](f)); n != 1 {
		t.Errorf("let go %d times", n)
	}
}
