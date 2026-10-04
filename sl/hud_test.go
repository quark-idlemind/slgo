package sl

import (
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

// The measured cases below are doc/hud-screen.md's: a viewer's window of
// 1920x1080 points on a screen at 2x, so a world view of 3840x2050
// backing pixels.  They were read off screenshots in the window's
// pixels, where the world view's top is at y = 108; each expected Y here
// has had that taken off.  The measurements agreed with the rule to
// within about 2 pixels, so that is the margin, with one more for the
// screenshot.
var measured = HUDView{Width: 3840, Height: 2050}

const pixels = 3.0

// prim is a plain box or cylinder worn on Center 2.
func prim(t *testing.T, kind string, size float32, at msg.Vector3, rot msg.Quaternion) *Seen {
	t.Helper()
	shape := DefaultShape()
	shape.Type = kind
	packed, err := shape.Pack()
	if err != nil {
		t.Fatal(err)
	}
	return &Seen{
		Object:      Object{ID: msg.MustParseUUID("27c67e57-7e57-c0de-304a-8fcc3264f296"), Local: 900},
		Position:    at,
		Rotation:    rot,
		Scale:       msg.Vector3{X: size, Y: size, Z: size},
		Shape:       packed,
		AttachPoint: HUDCenter2,
	}
}

// about is a turn of deg degrees about an axis, as llAxisAngle2Rot.
func about(axis msg.Vector3, deg float64) msg.Quaternion {
	h := deg * math.Pi / 360
	s := float32(math.Sin(h))
	return msg.PackQuaternion(axis.X*s, axis.Y*s, axis.Z*s, float32(math.Cos(h)))
}

// then is LSL's a * b: a, and then b.
func then(a, b msg.Quaternion) msg.Quaternion { return b.Mul(a) }

var (
	xAxis = msg.Vector3{X: 1}
	yAxis = msg.Vector3{Y: 1}
	zAxis = msg.Vector3{Z: 1}

	// In front of anything else worn, as the measurements had it.
	front = msg.Vector3{X: -0.5}
)

func shown(t *testing.T, v HUDView, linkset ...*Seen) map[int]ScreenFace {
	t.Helper()
	got, err := v.Faces(linkset)
	if err != nil {
		t.Fatal(err)
	}
	out := map[int]ScreenFace{}
	for _, f := range got {
		out[f.Face] = f
	}
	return out
}

// box is the smallest rectangle round a face's outline.
func box(f ScreenFace) (left, top, right, bottom float64) {
	left, top = math.Inf(1), math.Inf(1)
	right, bottom = math.Inf(-1), math.Inf(-1)
	for _, p := range f.Outline {
		left, right = math.Min(left, p.X), math.Max(right, p.X)
		top, bottom = math.Min(top, p.Y), math.Max(bottom, p.Y)
	}
	return
}

func within(t *testing.T, what string, got, want, margin float64) {
	t.Helper()
	if math.Abs(got-want) > margin {
		t.Errorf("%s = %.1f, want %.1f within %.1f", what, got, want, margin)
	}
}

// TestAHUDBoxIsHalfTheWorldViewTall: a metre is the world view's height,
// whatever its width (test 3: 1024 by 1026 pixels).
func TestAHUDBoxIsHalfTheWorldViewTall(t *testing.T) {
	faces := shown(t, measured, prim(t, "box", 0.5, front, msg.Quaternion{}))
	if len(faces) != 1 {
		t.Fatalf("faces shown: %v, want face 4 alone", faces)
	}
	l, top, r, b := box(faces[4])
	within(t, "width", r-l, 1024, pixels)
	within(t, "height", b-top, 1026, pixels)
	within(t, "middle x", (l+r)/2, 1920, pixels)
	within(t, "middle y", (top+b)/2, 1133-108, pixels)
}

// TestABoxTurnedAboutZShowsTwoFacesSideBySide (test 4): at 30 degrees
// face 4 is 888 wide on the right and face 3 512 on the left, meeting at
// x = 1732; at 45 each is 724 and they meet in the middle.
func TestABoxTurnedAboutZShowsTwoFacesSideBySide(t *testing.T) {
	for _, c := range []struct {
		deg         float64
		four, three float64
		meet        float64
	}{
		{30, 888, 512, 1732},
		{45, 724, 724, 1920},
	} {
		faces := shown(t, measured, prim(t, "box", 0.5, front, about(zAxis, c.deg)))
		if len(faces) != 2 {
			t.Fatalf("at %v degrees faces shown: %v, want 3 and 4", c.deg, faces)
		}
		l4, t4, r4, b4 := box(faces[4])
		l3, _, r3, _ := box(faces[3])
		within(t, "face 4's width", r4-l4, c.four, pixels)
		within(t, "face 3's width", r3-l3, c.three, pixels)
		within(t, "face 4's height", b4-t4, 1026, pixels)
		within(t, "where they meet", l4, c.meet, pixels)
		within(t, "face 3's right edge", r3, c.meet, pixels)
	}
}

// TestABoxTurnedAboutYShowsItsBottom (test 5): face 4 888 tall above
// face 5, 512 tall, their shared edge at y = 1322.
func TestABoxTurnedAboutYShowsItsBottom(t *testing.T) {
	faces := shown(t, measured, prim(t, "box", 0.5, front, about(yAxis, 30)))
	if len(faces) != 2 {
		t.Fatalf("faces shown: %v, want 4 and 5", faces)
	}
	_, t4, _, b4 := box(faces[4])
	_, t5, _, b5 := box(faces[5])
	within(t, "face 4's height", b4-t4, 888, pixels)
	within(t, "face 5's height", b5-t5, 512, pixels)
	within(t, "the shared edge", b4, 1322-108, pixels)
	within(t, "face 5's top", t5, 1322-108, pixels)
}

// TestABoxCornerOnIsAHexagon (test 6): three faces, 3 on the left, 4 on
// the right and 5 at the bottom, together a hexagon whose corners were
// read at (1920,298), (2644,714), (2644,1554), (1918,1970), (1196,1550)
// and (1196,714).
func TestABoxCornerOnIsAHexagon(t *testing.T) {
	r := then(about(zAxis, 45), about(yAxis, 35.26))
	faces := shown(t, measured, prim(t, "box", 0.5, front, r))
	if len(faces) != 3 || faces[3].Outline == nil || faces[4].Outline == nil || faces[5].Outline == nil {
		t.Fatalf("faces shown: %v, want 3, 4 and 5", faces)
	}
	l3, _, _, _ := box(faces[3])
	_, _, r4, _ := box(faces[4])
	_, _, _, b5 := box(faces[5])
	top := math.Inf(1)
	for _, f := range faces {
		_, tp, _, _ := box(f)
		top = math.Min(top, tp)
	}
	within(t, "left", l3, 1196, pixels)
	within(t, "right", r4, 2644, pixels)
	within(t, "top", top, 298-108, pixels)
	within(t, "bottom", b5, 1970-108, pixels)
}

// TestACylinderEndOnIsADisc (test 10): turned -90 about Y its top, face
// 0, is a disc 1024 across; turned +90, its bottom, face 2.  Twenty
// degrees off end-on the top is an ellipse 1024 by 964 centred at
// y = 958, with the side, face 1, below it down to y = 1790.
func TestACylinderEndOnIsADisc(t *testing.T) {
	for _, c := range []struct {
		deg  float64
		face int
	}{{-90, 0}, {90, 2}} {
		faces := shown(t, measured, prim(t, "cylinder", 0.5, front, about(yAxis, c.deg)))
		f, ok := faces[c.face]
		if !ok {
			t.Fatalf("turned %v about Y, faces shown: %v, want %d", c.deg, faces, c.face)
		}
		l, tp, r, b := box(f)
		within(t, "the disc's width", r-l, 1024, pixels)
		within(t, "the disc's height", b-tp, 1024, pixels)
	}

	faces := shown(t, measured, prim(t, "cylinder", 0.5, front, about(yAxis, -70)))
	l, tp, r, b := box(faces[0])
	within(t, "the ellipse's width", r-l, 1024, pixels)
	within(t, "the ellipse's height", b-tp, 964, pixels)
	within(t, "the ellipse's middle", (tp+b)/2, 958-108, pixels)
	side, ok := faces[1]
	if !ok {
		t.Fatalf("faces shown: %v, want the side too", faces)
	}
	_, _, _, sb := box(side)
	within(t, "the side's bottom", sb, 1790-108, pixels)
}

// TestAClickOnFaceFourIsItsST (test 11): on the unrotated box, S grows
// to the right and T upward, and clicks at the pixels for five S,T came
// back as those S,T.  The touch position is from the middle of the
// screen, which on Center 2 is the HUD point: the middle of the face of a
// box half a metre in front is -0.75 along X.
func TestAClickOnFaceFourIsItsST(t *testing.T) {
	linkset := []*Seen{prim(t, "box", 0.5, front, msg.Quaternion{})}
	for _, st := range [][2]float32{{0.5, 0.5}, {0.1, 0.1}, {0.9, 0.1}, {0.1, 0.9}, {0.9, 0.9}} {
		want := msg.Vector3{X: st[0], Y: st[1]}
		at, err := measured.PointOf(linkset, 1, 4, want)
		if err != nil {
			t.Fatal(err)
		}
		within(t, "x", at.X, 1920+(float64(st[0])-0.5)*0.5*2050, 0.5)
		within(t, "y", at.Y, 1025-(float64(st[1])-0.5)*0.5*2050, 0.5)
		h, ok, err := measured.Pick(linkset, at)
		if err != nil || !ok {
			t.Fatalf("Pick at %v: %v, %v", at, ok, err)
		}
		if h.Touch.Face != 4 {
			t.Errorf("at %v the face is %d, want 4", at, h.Touch.Face)
		}
		within(t, "S", float64(h.Touch.ST.X), float64(st[0]), 0.001)
		within(t, "T", float64(h.Touch.ST.Y), float64(st[1]), 0.001)
	}
	h, _, _ := measured.Pick(linkset, ScreenPoint{1920, 1025})
	if p := h.Touch.Position; math.Abs(float64(p.X)+0.75) > 0.001 || math.Abs(float64(p.Y)) > 0.001 {
		t.Errorf("the middle's position is %v, want -0.75, 0, 0", p)
	}
}

// TestAClickOnATurnedFaceIsItsST (test 11 again): turned 30 degrees about
// Z, clicks spaced evenly across face 4's 888 pixels came back S 0.0997,
// 0.4987 and 0.8978, with the normal <-0.866, -0.5, 0>; one in the
// middle of face 3 came back S 0.499.
func TestAClickOnATurnedFaceIsItsST(t *testing.T) {
	linkset := []*Seen{prim(t, "box", 0.5, front, about(zAxis, 30))}
	faces := shown(t, measured, linkset...)
	l4, _, r4, _ := box(faces[4])
	for _, s := range []float64{0.1, 0.5, 0.9} {
		h, ok, err := measured.Pick(linkset, ScreenPoint{l4 + s*(r4-l4), 1025})
		if err != nil || !ok || h.Touch.Face != 4 {
			t.Fatalf("at S %v: %+v, %v, %v", s, h, ok, err)
		}
		within(t, "S", float64(h.Touch.ST.X), s, 0.003)
		n := h.Touch.Normal
		within(t, "the normal's X", float64(n.X), -0.866, 0.001)
		within(t, "the normal's Y", float64(n.Y), -0.5, 0.001)
	}
	l3, _, r3, _ := box(faces[3])
	h, _, _ := measured.Pick(linkset, ScreenPoint{(l3 + r3) / 2, 1025})
	if h.Touch.Face != 3 {
		t.Fatalf("the middle of the left face is face %d, want 3", h.Touch.Face)
	}
	within(t, "face 3's S", float64(h.Touch.ST.X), 0.5, 0.003)
}

// TestSAndTOnEveryFace (test 13): each face of a half-metre box and
// cylinder turned to the viewer and clicked 0.15 of the world view's
// height right of its middle and above it.  The S,T are the ones the
// viewer's clicks came back with; they were 308 pixels off in a world
// view whose metre measured 2048, so each is within a few thousandths
// of what exactly 0.15 m gives.
func TestSAndTOnEveryFace(t *testing.T) {
	right := ScreenPoint{1920 + 0.15*2050, 1025}
	up := ScreenPoint{1920, 1025 - 0.15*2050}
	left := ScreenPoint{1920 - 0.15*2050, 1025}
	type click struct {
		at   ScreenPoint
		s, t float64
	}
	for _, c := range []struct {
		kind   string
		turn   msg.Quaternion
		face   int
		clicks []click
	}{
		{"box", msg.Quaternion{}, 4, []click{{right, 0.80161, 0.50293}, {up, 0.50000, 0.80454}}},
		{"box", about(zAxis, 180), 2, []click{{right, 0.80161, 0.50293}, {up, 0.50000, 0.80454}}},
		{"box", about(zAxis, 90), 3, []click{{right, 0.80160, 0.50293}, {up, 0.49997, 0.80454}}},
		{"box", about(zAxis, -90), 1, []click{{right, 0.80149, 0.50293}, {up, 0.49997, 0.80454}}},
		{"box", about(yAxis, -90), 0, []click{{right, 0.50290, 0.19839}, {up, 0.80445, 0.50000}}},
		{"box", about(yAxis, 90), 5, []click{{right, 0.49710, 0.80161}, {up, 0.19547, 0.50000}}},
		{"cylinder", about(yAxis, -90), 0, []click{{right, 0.50290, 0.19839}, {up, 0.80445, 0.50000}}},
		{"cylinder", about(yAxis, 90), 2, []click{{right, 0.49710, 0.80161}, {up, 0.19547, 0.50000}}},
		{"cylinder", msg.Quaternion{}, 1, []click{{right, 0.60410, 0.50293}, {up, 0.50000, 0.80454}, {left, 0.39590, 0.50293}}},
	} {
		linkset := []*Seen{prim(t, c.kind, 0.5, front, c.turn)}
		for _, k := range c.clicks {
			h, ok, err := measured.Pick(linkset, k.at)
			if err != nil || !ok {
				t.Fatalf("%s face %d: Pick at %v: %v, %v", c.kind, c.face, k.at, ok, err)
			}
			if h.Touch.Face != c.face {
				t.Errorf("%s turned %v: at %v face %d, want %d", c.kind, c.turn, k.at, h.Touch.Face, c.face)
				continue
			}
			what := fmt.Sprintf("%s face %d at %v", c.kind, c.face, k.at)
			within(t, what+" S", float64(h.Touch.ST.X), k.s, 0.006)
			within(t, what+" T", float64(h.Touch.ST.Y), k.t, 0.006)
		}
	}
}

// TestOffThePrimHeldTheTouchIsNone: PickOn a point off the prim gives
// what a viewer sends then, face -1 at S,T -1,-1, and Pick nothing.
func TestOffThePrimHeldTheTouchIsNone(t *testing.T) {
	linkset := []*Seen{prim(t, "box", 0.5, front, msg.Quaternion{})}
	off := ScreenPoint{100, 100}
	if _, ok, _ := measured.Pick(linkset, off); ok {
		t.Error("Pick found something at the corner of the screen")
	}
	h, ok, err := measured.PickOn(linkset, 1, off)
	if err != nil || ok {
		t.Fatalf("PickOn: %v, %v", ok, err)
	}
	if h.Touch.Face != -1 || h.Touch.ST != (msg.Vector3{X: -1, Y: -1}) {
		t.Errorf("the touch off the prim is %+v, want face -1 at -1,-1", h.Touch)
	}
}

// TestOnlyAPlainBoxOrCylinderIsPlaced.
func TestOnlyAPlainBoxOrCylinderIsPlaced(t *testing.T) {
	hollow := prim(t, "box", 0.5, front, msg.Quaternion{})
	shape := DefaultShape()
	shape.Hollow = 0.5
	hollow.Shape, _ = shape.Pack()
	sphere := prim(t, "sphere", 0.5, front, msg.Quaternion{})
	for _, s := range []*Seen{hollow, sphere} {
		if _, err := measured.Faces([]*Seen{s}); !errors.Is(err, ErrShapeNotPlaced) {
			t.Errorf("Faces: %v, want ErrShapeNotPlaced", err)
		}
	}
}

// TestPickAndPointOfAgreeEverywhere: a point inside a face's outline is
// picked as that face, and PointOf its S,T is the same point, on boxes
// and cylinders turned every which way.
func TestPickAndPointOfAgreeEverywhere(t *testing.T) {
	turns := []msg.Quaternion{
		{},
		about(zAxis, 30), about(yAxis, -40), about(xAxis, 70),
		then(about(zAxis, 45), about(yAxis, 35.26)),
		then(about(xAxis, 20), then(about(yAxis, -110), about(zAxis, 15))),
	}
	for _, kind := range []string{"box", "cylinder"} {
		for _, r := range turns {
			s := prim(t, kind, 0.3, front, r)
			s.Scale = msg.Vector3{X: 0.3, Y: 0.2, Z: 0.1}
			linkset := []*Seen{s}
			for _, f := range shown(t, measured, linkset...) {
				var mid ScreenPoint
				for _, p := range f.Outline {
					mid.X += p.X / float64(len(f.Outline))
					mid.Y += p.Y / float64(len(f.Outline))
				}
				// A flat face is convex: its middle, and halfway from
				// that to each corner.  A cylinder's side is a curved
				// band, its outline along the bottom and back along the
				// top, so halfway between the two edges at each step.
				points := []ScreenPoint{mid}
				for _, p := range f.Outline {
					points = append(points, ScreenPoint{(mid.X + p.X) / 2, (mid.Y + p.Y) / 2})
				}
				if kind == "cylinder" && f.Face == 1 {
					points = points[:0]
					n := len(f.Outline)
					for k := 0; k < n/2; k++ {
						a, b := f.Outline[k], f.Outline[n-1-k]
						points = append(points, ScreenPoint{(a.X + b.X) / 2, (a.Y + b.Y) / 2})
					}
				}
				for _, at := range points {
					h, ok, err := measured.Pick(linkset, at)
					if err != nil || !ok {
						t.Fatalf("%s %v face %d: Pick at %v found nothing", kind, r, f.Face, at)
					}
					if h.Touch.Face != f.Face {
						t.Errorf("%s %v: inside face %d's outline at %v Pick found face %d",
							kind, r, f.Face, at, h.Touch.Face)
						continue
					}
					back, err := measured.PointOf(linkset, 1, f.Face, h.Touch.ST)
					if err != nil {
						t.Fatal(err)
					}
					if math.Abs(back.X-at.X) > 0.01 || math.Abs(back.Y-at.Y) > 0.01 {
						t.Errorf("%s %v face %d: Pick at %v gave %v, and PointOf that is %v",
							kind, r, f.Face, at, h.Touch.ST, back)
					}
				}
			}
		}
	}
}

// TestAChildIsPlacedByItsRoot (test 7): a child's position is the root's
// frame's, so a child a quarter metre along the root's +Y is a quarter
// metre to the left (measured 512 pixels to the left of 2048 a metre),
// and with the root turned 90 degrees about X it is a quarter metre up
// (513 above).
func TestAChildIsPlacedByItsRoot(t *testing.T) {
	for _, c := range []struct {
		turn   msg.Quaternion
		dx, dy float64
	}{
		{msg.Quaternion{}, -0.25 * 2050, 0},
		{about(xAxis, 90), 0, -0.25 * 2050},
	} {
		root := prim(t, "box", 0.25, front, c.turn)
		child := prim(t, "box", 0.25, msg.Vector3{Y: 0.25}, msg.Quaternion{})
		child.Object.Local = 901
		got, err := measured.Faces([]*Seen{root, child})
		if err != nil {
			t.Fatal(err)
		}
		var mid [3]ScreenPoint
		for _, f := range got {
			if f.Face == 4 {
				l, tp, r, b := box(f)
				mid[f.Link] = ScreenPoint{(l + r) / 2, (tp + b) / 2}
			}
		}
		within(t, "the child's X from the root's", mid[2].X-mid[1].X, c.dx, 0.5)
		within(t, "the child's Y from the root's", mid[2].Y-mid[1].Y, c.dy, 0.5)
	}
}

// TestAChildTurnedShowsTwoFaces (test 7 again): the child turned 30
// degrees about Z, its root not, shows faces 3 and 4, 256 and 444 pixels
// wide, and its middle stays where it was, 512 left of the root's.
func TestAChildTurnedShowsTwoFaces(t *testing.T) {
	root := prim(t, "box", 0.25, front, msg.Quaternion{})
	child := prim(t, "box", 0.25, msg.Vector3{Y: 0.25}, about(zAxis, 30))
	child.Object.Local = 901
	got, err := measured.Faces([]*Seen{root, child})
	if err != nil {
		t.Fatal(err)
	}
	shown := map[int]ScreenFace{}
	for _, f := range got {
		if f.Link == 2 {
			shown[f.Face] = f
		}
	}
	if len(shown) != 2 {
		t.Fatalf("the child shows %v, want faces 3 and 4", shown)
	}
	l3, _, r3, _ := box(shown[3])
	l4, _, r4, _ := box(shown[4])
	within(t, "face 3's width", r3-l3, 256, pixels)
	within(t, "face 4's width", r4-l4, 444, pixels)
	within(t, "the child's middle", (l3+r4)/2, 1920-0.25*2050, pixels)
}

// TestZoomScalesSizeAndPlaceAlike (test 8): HUDScaleFactor scales a HUD
// about the middle of the world view.  A half-metre box 0.2 m left of and
// 0.1 m above Center 2 was, at zoom 0.5, 514 pixels square, its middle
// 205 left of and 103 above the HUD's; at 0.75, 768 square, 308 left and
// 154 above.
func TestZoomScalesSizeAndPlaceAlike(t *testing.T) {
	off := msg.Vector3{X: -0.5, Y: 0.2, Z: 0.1}
	linkset := []*Seen{prim(t, "box", 0.5, off, msg.Quaternion{})}
	for _, c := range []struct {
		zoom, size, left, above float64
	}{
		{0.5, 514, 205, 103},
		{0.75, 768, 308, 154},
	} {
		v := measured
		v.Zoom = c.zoom
		l, tp, r, b := box(shown(t, v, linkset...)[4])
		within(t, "width", r-l, c.size, pixels)
		within(t, "height", b-tp, c.size, pixels)
		within(t, "middle x", (l+r)/2, 1920-c.left, pixels)
		within(t, "middle y", (tp+b)/2, 1025-c.above, pixels)
	}
}

// TestAShapedPrimIsInTheWayAndNoMore: a prim whose faces are not known --
// here boxes with their profile cut -- stands for the box it fits in.  A click where it is in front is refused;
// a click on the plain prims anywhere else is answered as before, and so
// is a drag held on one of them.
func TestAShapedPrimIsInTheWayAndNoMore(t *testing.T) {
	cut := func(at msg.Vector3, size float32, local uint32) *Seen {
		s := prim(t, "box", size, at, msg.Quaternion{})
		shape := DefaultShape()
		shape.CutEnd = 0.875
		s.Shape, _ = shape.Pack()
		s.Object.Local = local
		return s
	}
	root := prim(t, "box", 0.25, front, msg.Quaternion{})
	beside := cut(msg.Vector3{Y: 0.25}, 0.25, 901)          // to the left of the root
	before := cut(msg.Vector3{X: -0.2, Z: 0.08}, 0.05, 902) // in front of the root's top
	linkset := []*Seen{root, beside, before}

	mid, err := measured.PointOf(linkset, 1, 4, msg.Vector3{X: 0.5, Y: 0.3})
	if err != nil {
		t.Fatal(err)
	}
	h, ok, err := measured.Pick(linkset, mid)
	if err != nil || !ok || h.Link != 1 || h.Touch.Face != 4 {
		t.Fatalf("Pick on the root clear of both = %+v, %v, %v; want link 1 face 4", h, ok, err)
	}
	if h, ok, err := measured.PickOn(linkset, 1, mid); err != nil || !ok || h.Link != 1 {
		t.Errorf("PickOn the root = %+v, %v, %v", h, ok, err)
	}

	top, err := measured.PointOf(linkset, 1, 4, msg.Vector3{X: 0.5, Y: 0.82})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := measured.Pick(linkset, top); !errors.Is(err, ErrShapeNotPlaced) {
		t.Errorf("Pick where a shaped prim is in front: %v, want ErrShapeNotPlaced", err)
	}
	// Held on the root, the drag goes on reading the root, whatever is
	// in front of it there.
	if h, ok, err := measured.PickOn(linkset, 1, top); err != nil || !ok || h.Link != 1 {
		t.Errorf("PickOn the root behind a shaped prim = %+v, %v, %v", h, ok, err)
	}

	if _, err := measured.PointOf(linkset, 2, 4, msg.Vector3{X: 0.5, Y: 0.5}); !errors.Is(err, ErrShapeNotPlaced) {
		t.Errorf("PointOf on a shaped prim: %v, want ErrShapeNotPlaced", err)
	}
	if _, _, err := measured.PickOn(linkset, 2, mid); !errors.Is(err, ErrShapeNotPlaced) {
		t.Errorf("PickOn a shaped prim: %v, want ErrShapeNotPlaced", err)
	}
	if _, err := measured.Faces(linkset); !errors.Is(err, ErrShapeNotPlaced) {
		t.Errorf("Faces of a linkset with a shaped prim: %v, want ErrShapeNotPlaced", err)
	}
}

// TestATouchPositionOnAnotherPointIsFromTheMiddleOfTheScreen: off the
// centre points the viewer's touch position is not from the point the
// prim is worn on but from the middle of the screen.  Measured in
// Firestorm on a world view of 3840x2050: a 0.1 x 0.4 x 0.2 box worn
// unturned on Top Left, clicked on face 4 at S,T 0.97836, 0.02782, read
// llDetectedTouchPos <-0.05, 0.74524, 0.40556> -- the point, half the
// aspect across and half a metre up, plus the place on the box.
func TestATouchPositionOnAnotherPointIsFromTheMiddleOfTheScreen(t *testing.T) {
	box := prim(t, "box", 0.1, msg.Vector3{}, msg.Quaternion{})
	box.Scale = msg.Vector3{X: 0.1, Y: 0.4, Z: 0.2}
	box.AttachPoint = HUDTopLeft
	linkset := []*Seen{box}
	at, err := measured.PointOf(linkset, 1, 4, msg.Vector3{X: 0.97836, Y: 0.02782})
	if err != nil {
		t.Fatal(err)
	}
	h, ok, err := measured.Pick(linkset, at)
	if err != nil || !ok {
		t.Fatalf("Pick: %v %v", ok, err)
	}
	got := h.Touch.Position
	within(t, "x", float64(got.X), -0.05, 0.001)
	within(t, "y", float64(got.Y), 0.74524, 0.001)
	within(t, "z", float64(got.Z), 0.40556, 0.001)
}

// TestADragsChangeOfPositionIsTheSameOnEveryPoint: a script that moves or
// resizes a HUD by the change in the touch position sees the same change
// on any point, since the points differ by a constant.
func TestADragsChangeOfPositionIsTheSameOnEveryPoint(t *testing.T) {
	change := func(point int) msg.Vector3 {
		box := prim(t, "box", 0.2, msg.Vector3{}, msg.Quaternion{})
		box.AttachPoint = point
		linkset := []*Seen{box}
		var at [2]msg.Vector3
		for i, st := range []msg.Vector3{{X: 0.2, Y: 0.3}, {X: 0.7, Y: 0.9}} {
			p, err := measured.PointOf(linkset, 1, 4, st)
			if err != nil {
				t.Fatal(err)
			}
			h, ok, err := measured.Pick(linkset, p)
			if err != nil || !ok {
				t.Fatalf("Pick on %d: %v %v", point, ok, err)
			}
			at[i] = h.Touch.Position
		}
		return msg.Vector3{X: at[1].X - at[0].X, Y: at[1].Y - at[0].Y, Z: at[1].Z - at[0].Z}
	}
	centre, corner := change(HUDCenter2), change(HUDTopLeft)
	within(t, "the change across", float64(corner.Y), float64(centre.Y), 1e-5)
	within(t, "the change up", float64(corner.Z), float64(centre.Z), 1e-5)
}
