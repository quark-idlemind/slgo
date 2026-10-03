package sl

import (
	"math"

	"github.com/quark-idlemind/slgo/msg"
)

// face is one face of a unit box or cylinder in the prim's own frame,
// and how S,T lie on it.
//
// A flat face is the point o, where S and T are both 0, and the
// directions s and t that take each of them to 1 across the face.  The
// cylinder's ends are flat faces with a round edge.  Its side is round
// the Z axis, S going round from sideFrom in the direction sideTurn and
// T going up.
// Why: doc/hud-screen.md#where-s-and-t-lie
type face struct {
	n       int
	o, s, t vec
	normalV vec
	round   bool // a flat face with a round edge: a cylinder's end
	side    bool // a cylinder's side
}

// The sides of a box run round it as its profile does, counter-clockwise
// seen from above starting at -Y, so that each one's S runs left to
// right seen from outside and T runs up.  The top's S runs along +X and
// T along +Y, and the bottom's S along +X and T along -Y, as
// LLVolumeFace::createUnCutCubeCap lays them out.  All six were
// measured, each turned to face a viewer.
var boxFaces = []face{
	{n: 0, o: vec{-0.5, -0.5, 0.5}, s: vec{1, 0, 0}, t: vec{0, 1, 0}, normalV: vec{0, 0, 1}},
	{n: 1, o: vec{-0.5, -0.5, -0.5}, s: vec{1, 0, 0}, t: vec{0, 0, 1}, normalV: vec{0, -1, 0}},
	{n: 2, o: vec{0.5, -0.5, -0.5}, s: vec{0, 1, 0}, t: vec{0, 0, 1}, normalV: vec{1, 0, 0}},
	{n: 3, o: vec{0.5, 0.5, -0.5}, s: vec{-1, 0, 0}, t: vec{0, 0, 1}, normalV: vec{0, 1, 0}},
	{n: 4, o: vec{-0.5, 0.5, -0.5}, s: vec{0, -1, 0}, t: vec{0, 0, 1}, normalV: vec{-1, 0, 0}},
	{n: 5, o: vec{-0.5, 0.5, -0.5}, s: vec{1, 0, 0}, t: vec{0, -1, 0}, normalV: vec{0, 0, -1}},
}

// sideFrom and sideTurn are where S starts round a cylinder's side, as
// an angle from +X, and which way it goes: measured, S is 0.5 on the
// side facing -X and grows to the right seen from outside, so it is 0
// at +X and goes round counter-clockwise seen from above.  A cylinder's
// ends are laid out as a box's top and bottom, also measured.
const (
	sideFrom = 0
	sideTurn = 1
)

var cylinderSide = face{n: 1, side: true}

var cylinderFaces = []face{
	{n: 0, o: vec{-0.5, -0.5, 0.5}, s: vec{1, 0, 0}, t: vec{0, 1, 0}, normalV: vec{0, 0, 1}, round: true},
	cylinderSide,
	{n: 2, o: vec{-0.5, 0.5, -0.5}, s: vec{1, 0, 0}, t: vec{0, -1, 0}, normalV: vec{0, 0, -1}, round: true},
}

var shapeFaces = map[string][]face{"box": boxFaces, "cylinder": cylinderFaces}

func faceOf(kind string, n int) (face, bool) {
	fs := shapeFaces[kind]
	if n < 0 || n >= len(fs) {
		return face{}, false
	}
	return fs[n], true
}

// boxFaceOn is the face of the unit box on the plane where axis is side.
func boxFaceOn(axis int, side float64) face {
	n := [3][2]int{{4, 2}, {1, 3}, {5, 0}}[axis]
	if side > 0 {
		return boxFaces[n[1]]
	}
	return boxFaces[n[0]]
}

// at is the point at S,T.
func (f face) at(s, t float64) vec {
	if f.side {
		a := sideFrom + sideTurn*2*math.Pi*s
		return vec{0.5 * math.Cos(a), 0.5 * math.Sin(a), t - 0.5}
	}
	return f.o.add(f.s.scaled(s)).add(f.t.scaled(t))
}

// st undoes at for a point on the face.
func (f face) st(p vec) (float64, float64) {
	if f.side {
		s := (math.Atan2(p.y, p.x) - sideFrom) * sideTurn / (2 * math.Pi)
		return s - math.Floor(s), p.z + 0.5
	}
	d := p.sub(f.o)
	return d.dot(f.s), d.dot(f.t)
}

func (f face) normal(p vec) vec {
	if f.side {
		return vec{p.x, p.y, 0}
	}
	return f.normalV
}

// tDir is the way T grows at p, which is what a viewer sends as the
// binormal.
func (f face) tDir(p vec) vec {
	if f.side {
		return vec{0, 0, 1}
	}
	return f.t
}

// visible is the face's edge in the prim's own frame, when any of it is
// turned toward the viewer, and nothing when none is.
func (f face) visible(p hudPrim) []vec {
	toward := func(n vec) bool { return p.normalToHUD(n).x < -1e-9 }
	switch {
	case f.side:
		return sideEdge(toward)
	case !toward(f.normalV):
		return nil
	case f.round:
		const n = 48
		edge := make([]vec, n)
		for i := range edge {
			a := 2 * math.Pi * float64(i) / n
			edge[i] = f.at(0.5+0.5*math.Cos(a), 0.5+0.5*math.Sin(a))
		}
		return edge
	default:
		return []vec{f.o, f.o.add(f.s), f.o.add(f.s).add(f.t), f.o.add(f.t)}
	}
}

// sideEdge is the edge of the part of a cylinder's side turned toward
// the viewer: along the bottom one way and back along the top.
func sideEdge(toward func(vec) bool) []vec {
	const n = 96
	seen := make([]bool, n)
	any := false
	for i := range seen {
		a := 2 * math.Pi * float64(i) / n
		seen[i] = toward(vec{math.Cos(a), math.Sin(a), 0})
		any = any || seen[i]
	}
	if !any {
		return nil
	}
	// Start just after a step that is turned away, so that the run
	// turned toward the viewer is in one piece.
	start := 0
	for i := range seen {
		if !seen[(i+n-1)%n] && seen[i] {
			start = i
			break
		}
	}
	var arc []float64
	for k := 0; k < n; k++ {
		i := (start + k) % n
		if !seen[i] {
			break
		}
		arc = append(arc, 2*math.Pi*float64(i)/n)
	}
	edge := make([]vec, 0, 2*len(arc))
	for _, a := range arc {
		edge = append(edge, vec{0.5 * math.Cos(a), 0.5 * math.Sin(a), -0.5})
	}
	for i := len(arc) - 1; i >= 0; i-- {
		a := arc[i]
		edge = append(edge, vec{0.5 * math.Cos(a), 0.5 * math.Sin(a), 0.5})
	}
	return edge
}

// vec is a point or direction, in float64 so that turning and
// projecting a HUD several times over does not lose pixels.
type vec struct{ x, y, z float64 }

func vecOf(v msg.Vector3) vec { return vec{float64(v.X), float64(v.Y), float64(v.Z)} }

func (a vec) msg() msg.Vector3 {
	return msg.Vector3{X: float32(a.x), Y: float32(a.y), Z: float32(a.z)}
}

func (a vec) add(b vec) vec        { return vec{a.x + b.x, a.y + b.y, a.z + b.z} }
func (a vec) sub(b vec) vec        { return vec{a.x - b.x, a.y - b.y, a.z - b.z} }
func (a vec) scaled(k float64) vec { return vec{a.x * k, a.y * k, a.z * k} }
func (a vec) times(b vec) vec      { return vec{a.x * b.x, a.y * b.y, a.z * b.z} }
func (a vec) dot(b vec) float64    { return a.x*b.x + a.y*b.y + a.z*b.z }

// over divides by a scale, taking a side of no size as a very small one
// rather than dividing by zero.
func (a vec) over(b vec) vec {
	d := func(x float64) float64 {
		if math.Abs(x) < 1e-6 {
			return 1e-6
		}
		return x
	}
	return vec{a.x / d(b.x), a.y / d(b.y), a.z / d(b.z)}
}

func (a vec) unit() vec {
	l := math.Sqrt(a.dot(a))
	if l == 0 {
		return a
	}
	return a.scaled(1 / l)
}

func (a vec) at(i int) float64 { return [3]float64{a.x, a.y, a.z}[i] }

// rot3 is a rotation, as msg.Quaternion but with W kept.
type rot3 struct{ x, y, z, w float64 }

func quatOf(q msg.Quaternion) rot3 {
	return rot3{float64(q.X), float64(q.Y), float64(q.Z), float64(q.W())}
}

func (q rot3) rotate(v vec) vec {
	u := vec{q.x, q.y, q.z}
	t := cross(u, v).scaled(2)
	return v.add(t.scaled(q.w)).add(cross(u, t))
}

// mul is r turned first, then q, as msg.Quaternion.Mul.
func (q rot3) mul(r rot3) rot3 {
	return rot3{
		x: q.w*r.x + q.x*r.w + q.y*r.z - q.z*r.y,
		y: q.w*r.y - q.x*r.z + q.y*r.w + q.z*r.x,
		z: q.w*r.z + q.x*r.y - q.y*r.x + q.z*r.w,
		w: q.w*r.w - q.x*r.x - q.y*r.y - q.z*r.z,
	}
}

func (q rot3) conj() rot3 { return rot3{-q.x, -q.y, -q.z, q.w} }

func cross(a, b vec) vec {
	return vec{a.y*b.z - a.z*b.y, a.z*b.x - a.x*b.z, a.x*b.y - a.y*b.x}
}
