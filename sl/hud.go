package sl

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/quark-idlemind/slgo/msg"
)

// HUDView is the part of a viewer's window a worn HUD is drawn over,
// which is what places it: the world view's size in pixels, and the
// zoom the viewer draws HUDs at.
//
// The world view is the window less the viewer's menu bar; the bars
// drawn over it do not shrink it.  Its height is one metre of HUD
// whatever its width, so the pixels can be whatever the caller counts
// in -- a Retina screen's backing pixels or its points -- as long as
// Width and Height are counted the same way.
//
// Zoom is the viewer's own and nothing the region says gives it: 0 is
// taken as 1, the viewer's default.  It scales every HUD's size and
// its distance from the middle of the world view alike.
// Why: doc/hud-screen.md
type HUDView struct {
	Width, Height int
	Zoom          float64
}

// ScreenPoint is a point in the world view, in pixels from its top left
// corner: X to the right and Y down, as imgfind.Point.
type ScreenPoint struct{ X, Y float64 }

// ScreenFace is one face of a worn prim as the viewer shows it.
type ScreenFace struct {
	Object Object
	Link   int // the prim's place in the linkset as Linkset gives it, 1 for the root
	Face   int

	// Outline is the face's edge on the screen, in order round it.  A
	// round edge is given as a polygon of short sides.
	Outline []ScreenPoint

	// Depth is how far the face's middle is along the viewer's line of
	// sight, in metres; the smaller is in front.
	Depth float64
}

// ScreenHit is what a viewer would touch at a point on the screen: the
// prim, and the face, S,T, position and normal it would send.
type ScreenHit struct {
	Object Object
	Link   int
	Touch  Touch
}

// ErrShapeNotPlaced is a prim whose faces this package cannot put on
// the screen: anything but a plain box or cylinder, so far.  Such a prim
// is still in the way of what is behind it: a click where it is in front
// is refused with this, and a click anywhere else is answered.
// Why: doc/hud-screen.md#which-shapes
var ErrShapeNotPlaced = errors.New("sl: only a plain box or cylinder can be put on the screen")

// Faces is every face of a worn linkset that faces the viewer, in
// front first.  linkset is as Linkset returns it: the root, worn on a
// HUD point, then its children in link order.  A linkset with a prim
// whose faces are not known is refused, since every face is the promise.
func (v HUDView) Faces(linkset []*Seen) ([]ScreenFace, error) {
	prims, err := v.place(linkset)
	if err != nil {
		return nil, err
	}
	for _, p := range prims {
		if p.kind == "" {
			return nil, p.unplaced()
		}
	}
	var out []ScreenFace
	for _, p := range prims {
		out = append(out, p.faces()...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Depth < out[j].Depth })
	return out, nil
}

// Pick is what a click at p would touch: the prim in front along the
// viewer's line of sight, as a viewer's pick finds it.  It is false
// over nothing.
//
// A prim is picked whatever it looks like, fully transparent included,
// as a viewer's left click picks.
// Why: doc/hud-screen.md#a-click
func (v HUDView) Pick(linkset []*Seen, p ScreenPoint) (ScreenHit, bool, error) {
	prims, err := v.place(linkset)
	if err != nil {
		return ScreenHit{}, false, err
	}
	var best hit
	found := false
	for _, pr := range prims {
		if h, ok := pr.intersect(v.ray(p)); ok && (!found || h.depth < best.depth) {
			best, found = h, true
		}
	}
	if !found {
		return ScreenHit{}, false, nil
	}
	if best.shaped != nil {
		return ScreenHit{}, false, fmt.Errorf("%w, and %s is in front at %.0f,%.0f",
			best.shaped.unplaced(), best.shaped.seen.Object, p.X, p.Y)
	}
	return best.ScreenHit, true, nil
}

// PickOn is what a point on the screen is on one prim, link, and on
// nothing else: what a viewer sends while a touch is held and the
// cursor moves, since it keeps the prim the touch began on.  Off that
// prim it is false, and the Touch is the one the viewer sends then:
// face -1 and S,T of -1,-1.
// Why: doc/hud-screen.md#a-drag
func (v HUDView) PickOn(linkset []*Seen, link int, p ScreenPoint) (ScreenHit, bool, error) {
	prims, err := v.place(linkset)
	if err != nil {
		return ScreenHit{}, false, err
	}
	pr, err := primAt(prims, link)
	if err != nil {
		return ScreenHit{}, false, err
	}
	if pr.kind == "" {
		return ScreenHit{}, false, pr.unplaced()
	}
	if h, ok := pr.intersect(v.ray(p)); ok {
		return h.ScreenHit, true, nil
	}
	return ScreenHit{Object: pr.seen.Object, Link: pr.link, Touch: missed}, false, nil
}

// PointOf is where on the screen S,T of a face of one prim is.  The face
// need not be turned toward the viewer: a point on one turned away is
// where it would be seen through the prim.
func (v HUDView) PointOf(linkset []*Seen, link, face int, st msg.Vector3) (ScreenPoint, error) {
	prims, err := v.place(linkset)
	if err != nil {
		return ScreenPoint{}, err
	}
	pr, err := primAt(prims, link)
	if err != nil {
		return ScreenPoint{}, err
	}
	if pr.kind == "" {
		return ScreenPoint{}, pr.unplaced()
	}
	f, ok := faceOf(pr.kind, face)
	if !ok {
		return ScreenPoint{}, fmt.Errorf("sl: %s has no face %d", pr.seen.Object, face)
	}
	return v.project(pr.toHUD(f.at(float64(st.X), float64(st.Y)))), nil
}

// missed is the touch a viewer sends for a point off the prim it holds:
// LLPickInfo::getSurfaceInfo leaves these when its ray misses.
var missed = Touch{Face: -1, ST: msg.Vector3{X: -1, Y: -1}, UV: msg.Vector3{X: -1, Y: -1}}

// hudPoints is where each HUD point is, before the world view's aspect
// is applied to Y: avatar_lad.xml, the attachment points on mScreen.
// None of them is turned.
var hudPoints = map[int]vec{
	HUDCenter2:     {0, 0, 0},
	HUDTopRight:    {0, -0.5, 0.5},
	HUDTop:         {0, 0, 0.5},
	HUDTopLeft:     {0, 0.5, 0.5},
	HUDCenter1:     {0, 0, 0},
	HUDBottomLeft:  {0, 0.5, -0.5},
	HUDBottom:      {0, 0, -0.5},
	HUDBottomRight: {0, -0.5, -0.5},
}

func (v HUDView) zoom() float64 {
	if v.Zoom <= 0 {
		return 1
	}
	return v.Zoom
}

// point is where a HUD point is in the HUD's frame: its Y stretched by
// the world view's aspect, as the screen joint is scaled
// (llvoavatarself.cpp, mScreenp).  What is worn on it is not stretched.
func (v HUDView) point(p int) (vec, error) {
	at, ok := hudPoints[p&^AttachAdd]
	if !ok {
		return vec{}, fmt.Errorf("point %d is not a HUD point", p)
	}
	if v.Width <= 0 || v.Height <= 0 {
		return vec{}, fmt.Errorf("a world view of %dx%d pixels has no room for a HUD", v.Width, v.Height)
	}
	at.y *= float64(v.Width) / float64(v.Height)
	return at, nil
}

// project is where a point in the HUD's frame lands on the screen.  The
// view is orthographic and looks along +X, one metre tall, so X is
// dropped; +Y is to the left and +Z up.
func (v HUDView) project(p vec) ScreenPoint {
	h, z := float64(v.Height), v.zoom()
	return ScreenPoint{
		X: float64(v.Width)/2 - p.y*z*h,
		Y: h/2 - p.z*z*h,
	}
}

// ray is the viewer's line of sight through a point on the screen: the
// line along +X in the HUD's frame through this Y and Z.
func (v HUDView) ray(p ScreenPoint) vec {
	h, z := float64(v.Height), v.zoom()
	return vec{0, (float64(v.Width)/2 - p.X) / (z * h), (h/2 - p.Y) / (z * h)}
}

// hudPrim is one prim put in the HUD's frame.  In its own frame a prim
// is a unit box or cylinder about its middle; scale stretches it and
// turn and at put it in the HUD's frame.  A prim of any other shape has
// no kind, and stands for the unit box it fits in: in the way of what is
// behind it, with no faces of its own.
type hudPrim struct {
	view  HUDView
	seen  *Seen
	link  int
	kind  string // box, cylinder, or empty for any other shape
	why   string // what the shape is, when kind is empty
	point vec    // the HUD point it is worn on, which touch positions are from
	at    vec
	turn  rot3
	scale vec
}

// place puts every prim of a worn linkset in the HUD's frame: the root
// at its position from the point it is worn on, and a child at its
// position turned by the root's rotation, as a viewer composes them.
func (v HUDView) place(linkset []*Seen) ([]hudPrim, error) {
	if len(linkset) == 0 || linkset[0] == nil {
		return nil, errors.New("sl: no linkset to place")
	}
	root := linkset[0]
	pt, err := v.point(root.AttachPoint)
	if err != nil {
		return nil, fmt.Errorf("sl: %s is not on the screen: %w", root.Object, err)
	}
	rootAt := pt.add(vecOf(root.Position))
	rootTurn := quatOf(root.Rotation)
	out := make([]hudPrim, 0, len(linkset))
	for i, s := range linkset {
		if s == nil {
			return nil, fmt.Errorf("sl: link %d of %s is missing", i+1, root.Object)
		}
		kind, why, err := plainKind(s)
		if err != nil {
			return nil, err
		}
		p := hudPrim{view: v, seen: s, link: i + 1, kind: kind, why: why, point: pt,
			at: rootAt, turn: rootTurn, scale: vecOf(s.Scale)}
		if i > 0 {
			p.at = rootAt.add(rootTurn.rotate(vecOf(s.Position)))
			p.turn = rootTurn.mul(quatOf(s.Rotation))
		}
		out = append(out, p)
	}
	return out, nil
}

// plainKind is box or cylinder for a prim this package can place, and
// otherwise no kind and what the shape is.  An error is a prim nothing
// has described the shape of.
func plainKind(s *Seen) (kind, why string, err error) {
	shape, ok := s.Form()
	if !ok {
		return "", "", fmt.Errorf("sl: nothing has described the shape of %s", s.Object)
	}
	if s.Sculpt.Kind != msg.SculptNone {
		return "", "a sculpt or a mesh", nil
	}
	if shape.Type != "box" && shape.Type != "cylinder" {
		return "", "a " + shape.Type, nil
	}
	plain := DefaultShape()
	plain.Type = shape.Type
	if !sameShape(shape, plain) {
		return "", "a " + shape.Type + " with its shape changed", nil
	}
	return shape.Type, "", nil
}

// unplaced is the refusal for a prim whose faces are not known.
func (p hudPrim) unplaced() error {
	return fmt.Errorf("%w: %s is %s", ErrShapeNotPlaced, p.seen.Object, p.why)
}

// sameShape compares the parameters a box or cylinder uses, to within
// what the wire's packing keeps.
func sameShape(a, b Shape) bool {
	near := func(x, y float32) bool { return math.Abs(float64(x-y)) < 0.01 }
	return a.Type == b.Type &&
		near(a.CutBegin, b.CutBegin) && near(a.CutEnd, b.CutEnd) &&
		near(a.Hollow, b.Hollow) &&
		near(a.TwistBegin, b.TwistBegin) && near(a.TwistEnd, b.TwistEnd) &&
		near(a.TopSizeX, b.TopSizeX) && near(a.TopSizeY, b.TopSizeY) &&
		near(a.TopShearX, b.TopShearX) && near(a.TopShearY, b.TopShearY)
}

func primAt(prims []hudPrim, link int) (hudPrim, error) {
	if link == 0 && len(prims) == 1 {
		link = 1 // a prim on its own is link 0 to LSL
	}
	if link < 1 || link > len(prims) {
		return hudPrim{}, fmt.Errorf("sl: there is no link %d; the linkset has %d", link, len(prims))
	}
	return prims[link-1], nil
}

// toHUD is a point in the prim's own frame put in the HUD's frame.
func (p hudPrim) toHUD(local vec) vec {
	return p.at.add(p.turn.rotate(local.times(p.scale)))
}

// normalToHUD is a normal in the prim's own frame put in the HUD's.  A
// stretch turns a normal the other way from a point.
func (p hudPrim) normalToHUD(n vec) vec {
	return p.turn.rotate(n.over(p.scale)).unit()
}

// faces is the prim's faces turned toward the viewer.
func (p hudPrim) faces() []ScreenFace {
	var out []ScreenFace
	for _, f := range shapeFaces[p.kind] {
		edge := f.visible(p)
		if len(edge) < 3 {
			continue
		}
		var mid vec
		outline := make([]ScreenPoint, len(edge))
		for i, c := range edge {
			hud := p.toHUD(c)
			outline[i] = p.view.project(hud)
			mid = mid.add(hud)
		}
		out = append(out, ScreenFace{
			Object: p.seen.Object, Link: p.link, Face: f.n,
			Outline: outline, Depth: mid.x / float64(len(edge)),
		})
	}
	return out
}

// hit is where a line of sight meets a prim.  shaped is the prim when
// it is one whose faces are not known, met at the box it fits in.
type hit struct {
	ScreenHit
	depth  float64
	shaped *hudPrim
}

// intersect is where the line of sight through ray first meets the
// prim, if it does.
func (p hudPrim) intersect(ray vec) (hit, bool) {
	// The line in the prim's own frame: from where it crosses X = 0 in
	// the HUD's frame, going one metre along X per unit.
	from := p.turn.conj().rotate(ray.sub(p.at)).over(p.scale)
	along := p.turn.conj().rotate(vec{1, 0, 0}).over(p.scale)
	var (
		t  float64
		f  face
		ok bool
	)
	switch p.kind {
	case "box":
		t, f, ok = enterBox(from, along)
	case "cylinder":
		t, f, ok = enterCylinder(from, along)
	default:
		// Only how far along, which is all that is known.
		if t, _, ok = enterBox(from, along); ok {
			return hit{depth: p.toHUD(from.add(along.scaled(t))).x, shaped: &p}, true
		}
	}
	if !ok {
		return hit{}, false
	}
	local := from.add(along.scaled(t))
	s, tt := f.st(local)
	hud := p.toHUD(local)
	normal := p.normalToHUD(f.normal(local))
	return hit{
		ScreenHit: ScreenHit{
			Object: p.seen.Object, Link: p.link,
			Touch: Touch{
				Face: f.n,
				ST:   msg.Vector3{X: float32(s), Y: float32(tt)},
				// From the HUD point, as llDetectedTouchPos gives it.
				Position: hud.sub(p.point).msg(),
				Normal:   normal.msg(),
				Binormal: p.turn.rotate(f.tDir(local).times(p.scale)).unit().msg(),
			},
		},
		depth: hud.x,
	}, true
}

// enterBox is where a line first enters the unit box: how far along,
// and through which face.
func enterBox(from, along vec) (float64, face, bool) {
	lo, hi := math.Inf(-1), math.Inf(1)
	axis, side := -1, 0.0
	for a := 0; a < 3; a++ {
		o, d := from.at(a), along.at(a)
		if math.Abs(d) < 1e-12 {
			if o < -0.5 || o > 0.5 {
				return 0, face{}, false
			}
			continue
		}
		t1, t2 := (-0.5-o)/d, (0.5-o)/d
		s := -0.5
		if t1 > t2 {
			t1, t2, s = t2, t1, 0.5
		}
		if t1 > lo {
			lo, axis, side = t1, a, s
		}
		hi = math.Min(hi, t2)
	}
	if axis < 0 || lo > hi {
		return 0, face{}, false
	}
	return lo, boxFaceOn(axis, side), true
}

// enterCylinder is where a line first enters the unit cylinder, round
// Z: through the side or through an end.
func enterCylinder(from, along vec) (float64, face, bool) {
	lo, hi := math.Inf(-1), math.Inf(1)
	end := 0
	if math.Abs(along.z) < 1e-12 {
		if from.z < -0.5 || from.z > 0.5 {
			return 0, face{}, false
		}
	} else {
		t1, t2 := (-0.5-from.z)/along.z, (0.5-from.z)/along.z
		e := 2 // the bottom, at -0.5
		if t1 > t2 {
			t1, t2, e = t2, t1, 0
		}
		lo, hi, end = t1, t2, e
	}
	// x² + y² = 0.25 along the line.
	a := along.x*along.x + along.y*along.y
	b := 2 * (from.x*along.x + from.y*along.y)
	c := from.x*from.x + from.y*from.y - 0.25
	side := false
	if a < 1e-12 {
		if c > 0 {
			return 0, face{}, false
		}
	} else {
		disc := b*b - 4*a*c
		if disc < 0 {
			return 0, face{}, false
		}
		r := math.Sqrt(disc)
		t1, t2 := (-b-r)/(2*a), (-b+r)/(2*a)
		if t1 > lo {
			lo, side = t1, true
		}
		hi = math.Min(hi, t2)
	}
	if lo > hi || math.IsInf(lo, -1) {
		return 0, face{}, false
	}
	if side {
		return lo, cylinderSide, true
	}
	return lo, shapeFaces["cylinder"][capIndex(end)], true
}

func capIndex(face int) int {
	if face == 0 {
		return 0
	}
	return 2
}
