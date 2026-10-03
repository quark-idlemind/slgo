package sl

import (
	"context"
	"errors"
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
	f.mu.Lock()
	f.objects = []*Seen{
		{Object: Object{ID: theOther, Local: 10}, PCode: pcodeAvatar},
		box,
	}
	f.mu.Unlock()
	return box
}

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
