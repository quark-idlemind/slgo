package slate

import (
	"context"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var (
	idPane  = msg.MustParseUUID("adb37e57-7e57-c0de-ccaf-c554c41e4c8a")
	idGlass = msg.MustParseUUID("c2527e57-7e57-c0de-5007-99424153168c")
)

const paneHdr = "slate 1\ntimeout 20s\nobject hud is \"Example HUD\"\nobject sign is \"Example Sign\"\n"

// withPane wears a half-metre box on Center 2, in front of anything else
// worn, with a child to one side of it.
func (f *fakeGrid) withPane() *sl.Seen {
	root := prim(idPane, 301, "Example HUD", testMe)
	root.Parent, root.AttachPoint = 1, sl.HUDCenter2
	root.Position = msg.Vector3{X: -0.5}
	root.Scale = msg.Vector3{X: 0.5, Y: 0.5, Z: 0.5}
	glass := child(prim(idGlass, 302, "Example Glass", testMe), root)
	glass.Position = msg.Vector3{Y: 0.5}
	glass.Scale = msg.Vector3{X: 0.5, Y: 0.25, Z: 0.5}
	f.mu.Lock()
	f.objects = append(f.objects, root, glass)
	f.mu.Unlock()
	return root
}

func dragSteps(f *fakeGrid) (grab *msg.ObjectGrab, end msg.ObjectDeGrab_SurfaceInfo, n int) {
	grabs, downs := sentOf[*msg.ObjectGrab](f), sentOf[*msg.ObjectDeGrab](f)
	if len(grabs) > 0 {
		grab = grabs[0]
	}
	if len(downs) > 0 {
		end = downs[len(downs)-1].SurfaceInfo[0]
	}
	return grab, end, len(grabs)
}

// TestAScreenDragFromAFaceIsPutOnTheScreenAndMoved: the start is the
// face's point through PointOf, the end is where it is given, or the
// start plus the move, and the drag reaches DragOnScreen: a grab of the
// root where the viewer would press, and a release at the far point.
func TestAScreenDragFromAFaceIsPutOnTheScreenAndMoved(t *testing.T) {
	view := sl.HUDView{Width: DefaultScreenWidth, Height: DefaultScreenHeight}
	for _, c := range []struct {
		name, step string
		dx, dy     float64
	}{
		{"by", "from face 4 at 0.5 0.5 by 100 50 over 150ms", 100, 50},
		{"to", "from face 4 at 0.5 0.5 to 1060 562.5 over 150ms", 100, 50},
		{"pixels", "from 960 512.5 by 100 50 over 150ms", 100, 50},
	} {
		f := newGrid(t)
		root := f.withPane()
		res := play(t, f, paneHdr+"drag hud on screen "+c.step+"\n")
		wantExit(t, res, 0)
		grab, end, n := dragSteps(f)
		if n != 1 || grab.ObjectData.LocalID != root.Local {
			t.Fatalf("%s: %d grabs, %+v", c.name, n, grab)
		}
		// Pressed on the middle of the front face.
		if si := grab.SurfaceInfo[0]; si.FaceIndex != 4 || si.STCoord != (msg.Vector3{X: 0.5, Y: 0.5}) {
			t.Errorf("%s: pressed at %+v", c.name, si)
		}
		// Released where the viewer would, 100 and 50 pixels along.
		hit, ok, err := view.Pick([]*sl.Seen{root}, sl.ScreenPoint{X: 960 + c.dx, Y: 512.5 + c.dy})
		if err != nil || !ok {
			t.Fatalf("%s: pick %v %v", c.name, ok, err)
		}
		if end.FaceIndex != int32(hit.Touch.Face) || end.STCoord != hit.Touch.ST {
			t.Errorf("%s: released at %+v, want face %d at %v", c.name, end, hit.Touch.Face, hit.Touch.ST)
		}
	}
}

func TestAScreenDragFromALinkUsesThatPrim(t *testing.T) {
	f := newGrid(t)
	f.withPane()
	// The child sits half a metre to the side, so face 4 at 0.5 0.5 of
	// link 2 is nowhere near the middle; it is a press on that prim.
	res := play(t, f, paneHdr+"drag hud on screen from link 2 face 4 at 0.5 0.5 by 10 10 over 100ms\n")
	wantExit(t, res, 0)
	grab, _, _ := dragSteps(f)
	if grab.ObjectData.LocalID != 302 {
		t.Errorf("pressed local %d, want the child, 302", grab.ObjectData.LocalID)
	}
}

func TestAScreenDragOnAFaceThePrimCannotShowSaysSo(t *testing.T) {
	f := newGrid(t)
	f.withPane()
	res, err := tryPlay(t, f, paneHdr+"drag hud on screen from face 99 at 0.5 0.5 by 10 10\n", Options{}, testCfg())
	if err != nil {
		t.Fatal(err)
	}
	wantExit(t, res, 1)
	mustHave(t, res, `slate: step 1: "Example HUD" cannot be put on the screen: sl: `, "has no face 99", "nothing was sent")
	if n := len(sentOf[*msg.ObjectGrab](f)); n != 0 {
		t.Errorf("%d grabs", n)
	}
}

func TestAScreenDragOfSomethingNotOnAHUDPointSaysWhy(t *testing.T) {
	f := newGrid(t)
	res := play(t, f, hdr+"drag sign on screen from 100 100 by 5 5\n")
	wantExit(t, res, 1)
	mustHave(t, res, `slate: step 1: "Example Sign" is not worn; a drag on the screen needs an object worn on a HUD point, and nothing was sent`)

	f = newGrid(t)
	root := f.withPane()
	f.mu.Lock()
	root.AttachPoint = 1
	f.mu.Unlock()
	res = play(t, f, paneHdr+"drag hud on screen from 100 100 by 5 5\n")
	wantExit(t, res, 1)
	mustHave(t, res, `"Example HUD" is worn on chest; a drag on the screen needs an object worn on a HUD point`)
	if n := len(sentOf[*msg.ObjectGrab](f)); n != 0 {
		t.Errorf("%d grabs", n)
	}
}

// TestTheRunsScreenAndZoomReachTheView: the same pixels hit the prim or
// miss it as the world view changes.
func TestTheRunsScreenAndZoomReachTheView(t *testing.T) {
	for _, c := range []struct {
		name    string
		view    sl.HUDView
		step    string
		pressed bool
	}{
		{"default", sl.HUDView{}, "from 960 512.5 by 5 5", true},
		{"default misses the middle of a bigger window", sl.HUDView{}, "from 1920 1025 by 5 5", false},
		{"a bigger window", sl.HUDView{Width: 3840, Height: 2050}, "from 1920 1025 by 5 5", true},
		{"zoom 1", sl.HUDView{Zoom: 1}, "from 1360 512.5 by 5 5", false},
		{"zoom 2", sl.HUDView{Zoom: 2}, "from 1360 512.5 by 5 5", true},
	} {
		f := newGrid(t)
		f.withPane()
		res, err := tryPlay(t, f, paneHdr+"drag hud on screen "+c.step+" over 100ms\n", Options{Screen: c.view}, testCfg())
		if err != nil {
			t.Fatal(err)
		}
		_, _, n := dragSteps(f)
		if (n == 1) != c.pressed || (res.Exit == 0) != c.pressed {
			t.Errorf("%s: %d grabs, exit %d\n%s", c.name, n, res.Exit, res.Transcript)
		}
	}
}

// settleWorld has the pane grow when it is pressed, as a HUD dragged by
// a transparent prim does.
func settleWorld(t *testing.T, grow bool) *fakeGrid {
	f := newGrid(t)
	f.withPane()
	if grow {
		f.replyTo(func(m msg.Message) {
			if _, ok := m.(*msg.ObjectDeGrab); ok {
				time.AfterFunc(100*time.Millisecond, func() { f.setPaneScale(msg.Vector3{X: 0.5, Y: 0.5, Z: 0.5}) })
			}
			if _, ok := m.(*msg.ObjectGrab); ok {
				time.AfterFunc(60*time.Millisecond, func() {
					f.mu.Lock()
					defer f.mu.Unlock()
					for i, o := range f.objects {
						if o.Local == 301 {
							grown := *o
							grown.Scale = msg.Vector3{X: 0.01, Y: 4, Z: 2}
							f.objects[i] = &grown
						}
					}
				})
			}
		})
	}
	return f
}

func runScreen(t *testing.T, f *fakeGrid, src string, change time.Duration) *Result {
	t.Helper()
	s := mustCheck(t, src)
	sess := f.session(t)
	sess.SetOptions(sl.Options{HUDChangeTimeout: change})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := run(ctx, sess, s, Options{}, testCfg())
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, res.Transcript)
	}
	return res
}

func TestAScreenDragThatSettlesWaitsForTheHUDToChange(t *testing.T) {
	f := settleWorld(t, true)
	res := runScreen(t, f, paneHdr+"drag hud on screen from 960 512.5 by 5 5 over 150ms settle\n", 3*time.Second)
	wantExit(t, res, 0)
	// After it grew, every move was still on the pane's front face.
	for _, u := range sentOf[*msg.ObjectGrabUpdate](f) {
		if u.SurfaceInfo[0].FaceIndex != 4 {
			t.Fatalf("an update on face %d after the pane grew", u.SurfaceInfo[0].FaceIndex)
		}
	}
	if n := len(sentOf[*msg.ObjectDeGrab](f)); n != 1 {
		t.Errorf("let go %d times", n)
	}
}

func TestAScreenDragThatSettlesAndSeesNoChangeFailsAndLetsGo(t *testing.T) {
	f := settleWorld(t, false)
	t0 := time.Now()
	res := runScreen(t, f, paneHdr+"drag hud on screen from 960 512.5 by 5 5 over 150ms settle\n", 200*time.Millisecond)
	wantExit(t, res, 1)
	mustHave(t, res, "to change when pressed")
	if took := time.Since(t0); took < 200*time.Millisecond {
		t.Errorf("gave up after %s", took)
	}
	if n := len(sentOf[*msg.ObjectDeGrab](f)); n != 1 {
		t.Errorf("let go %d times", n)
	}
}

// Without settle the same HUD is not waited for: the press is followed
// at once by the move.
func TestAScreenDragThatDoesNotSettleDoesNotWait(t *testing.T) {
	f := settleWorld(t, false)
	res := runScreen(t, f, paneHdr+"drag hud on screen from 960 512.5 by 5 5 over 150ms\n", 200*time.Millisecond)
	wantExit(t, res, 0)
}

// setPaneScale gives the pane a new description with the scale.
func (f *fakeGrid) setPaneScale(scale msg.Vector3) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, o := range f.objects {
		if o.Local == 301 {
			next := *o
			next.Scale = scale
			f.objects[i] = &next
		}
	}
}

// growsOnTheMove has the pane grow to a box over the screen once a drag
// moves, and go back after the release once shrink has passed, never if
// shrink is 0, where it keeps changing instead.
func growsOnTheMove(t *testing.T, shrink time.Duration) *fakeGrid {
	f := newGrid(t)
	f.withPane()
	var once bool
	f.replyTo(func(m msg.Message) {
		switch m.(type) {
		case *msg.ObjectGrabUpdate:
			f.mu.Lock()
			first := !once
			once = true
			f.mu.Unlock()
			if first {
				time.AfterFunc(60*time.Millisecond, func() { f.setPaneScale(msg.Vector3{X: 0.01, Y: 10, Z: 10}) })
			}
		case *msg.ObjectDeGrab:
			if shrink == 0 {
				// Never settles: it keeps changing.
				go func() {
					for i := 1; i < 40; i++ {
						time.Sleep(100 * time.Millisecond)
						f.setPaneScale(msg.Vector3{X: 0.01, Y: 10 + float32(i)*0.01, Z: 10})
					}
				}()
			}
			if shrink > 0 {
				time.AfterFunc(shrink, func() { f.setPaneScale(msg.Vector3{X: 0.5, Y: 0.5, Z: 0.5}) })
				f.mu.Lock()
				once = false
				f.mu.Unlock()
			}
		}
	})
	return f
}

const twoDrags = "drag hud on screen from face 4 at 0.5 0.5 by 100 50 over 300ms\n" +
	"drag hud on screen from face 4 at 0.5 0.5 by -100 -50 over 300ms\n"

// TestADragAfterADragStartsOnTheViewWhenTheGlassShrinksLate: the glass
// grows once the drag moves and goes back 400 ms after the release; the
// next drag, placed from a face, starts where meant.
func TestADragAfterADragStartsOnTheViewWhenTheGlassShrinksLate(t *testing.T) {
	f := growsOnTheMove(t, 400*time.Millisecond)
	res := runScreen(t, f, paneHdr+twoDrags, 3*time.Second)
	wantExit(t, res, 0)
	grabs := sentOf[*msg.ObjectGrab](f)
	if len(grabs) != 2 {
		t.Fatalf("%d grabs, want 2", len(grabs))
	}
	for i, g := range grabs {
		if si := g.SurfaceInfo[0]; si.FaceIndex != 4 || si.STCoord != (msg.Vector3{X: 0.5, Y: 0.5}) {
			t.Errorf("grab %d pressed at face %d, %v, want face 4 at 0.5 0.5", i+1, si.FaceIndex, si.STCoord)
		}
	}
}

// A glass that keeps changing: the step says the HUD was still changing.
func TestADragWhoseGlassKeepsChangingSaysSo(t *testing.T) {
	f := growsOnTheMove(t, 0)
	res := runScreen(t, f, paneHdr+twoDrags, 1500*time.Millisecond)
	wantExit(t, res, 1)
	mustHave(t, res, "still changing", "after 1.5s")
	if _, _, n := dragSteps(f); n != 1 {
		t.Errorf("%d grabs, want 1: the second was placed on a glass that never settled", n)
	}
}

// A start or an end off the view fails the step, and nothing is sent.
func TestAScreenDragOffTheViewIsRefusedBeforeAnythingIsSent(t *testing.T) {
	for _, c := range []struct{ step, want string }{
		{"from 390 3745 by 5 5", "the drag would start at 390,3745, off the 1920x1025 view; nothing was sent"},
		{"from 960 512 by 5000 0", "the drag would end at 5960,512, off the 1920x1025 view; nothing was sent"},
		{"from 960 512 to 960 -1", "the drag would end at 960,-1, off the 1920x1025 view; nothing was sent"},
	} {
		f := newGrid(t)
		f.withPane()
		res := play(t, f, paneHdr+"drag hud on screen "+c.step+"\n")
		wantExit(t, res, 1)
		mustHave(t, res, "slate: step 1: "+c.want)
		if _, _, n := dragSteps(f); n != 0 {
			t.Errorf("%s: %d grabs", c.step, n)
		}
	}
}
