package sl

import (
	"math"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

// TestShapeNamesAreTwoCurves: every named shape has to pack to a pair
// the grid knows and unpack back to the name it started as, or a prim
// rezzed from JSON is silently the wrong kind.
func TestShapeNamesAreTwoCurves(t *testing.T) {
	for _, name := range ShapeNames {
		s := DefaultShape()
		s.Type = name
		p, err := s.Pack()
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got := UnpackShape(p).Type; got != name {
			t.Errorf("%s packed and unpacked as %q", name, got)
		}
	}
	if _, err := (Shape{Type: "dodecahedron"}).Pack(); err == nil {
		t.Error("a shape nobody has heard of packed anyway")
	}
}

// TestTheDefaultBoxPacksAsTheViewerWouldPackIt.  These are the numbers
// from LLVolumeMessage, and getting the two inverted fields backwards
// makes a prim that is inside out rather than one that fails.
func TestTheDefaultBoxPacksAsTheViewerWouldPackIt(t *testing.T) {
	p, err := DefaultShape().Pack()
	if err != nil {
		t.Fatal(err)
	}
	want := msg.PrimShape{
		ProfileCurve: 0x01, // square
		PathCurve:    0x10, // line
		ProfileEnd:   0,    // 50000 - 50000: a whole profile
		PathEnd:      0,    // likewise
		PathScaleX:   100,  // 200 - 100: full size
		PathScaleY:   100,
	}
	if p != want {
		t.Errorf("a default box packs as\n %+v\nwant\n %+v", p, want)
	}
}

// TestEveryFieldSurvivesTheRoundTrip, to within the quantum it is
// stored in.  A field that packs but does not unpack is a prim that
// changes shape when it is described and rebuilt.
func TestEveryFieldSurvivesTheRoundTrip(t *testing.T) {
	s := Shape{
		Type: "torus", HoleShape: HoleSquare,
		CutBegin: 0.125, CutEnd: 0.75,
		Hollow:     0.5,
		TwistBegin: -0.25, TwistEnd: 0.5,
		TopSizeX: 0.4, TopSizeY: 1.6,
		TopShearX: -0.3, TopShearY: 0.2,
		AdvancedCutBegin: 0.1, AdvancedCutEnd: 0.9,
		TaperX: -0.5, TaperY: 0.25,
		Revolutions: 3.0, RadiusOffset: 0.6, Skew: -0.4,
	}
	p, err := s.Pack()
	if err != nil {
		t.Fatal(err)
	}
	got := UnpackShape(p)

	for _, c := range []struct {
		what     string
		want, is float32
		tol      float64
	}{
		{"cut begin", s.CutBegin, got.CutBegin, cutQuantum},
		{"cut end", s.CutEnd, got.CutEnd, cutQuantum},
		{"hollow", s.Hollow, got.Hollow, hollowQuantum},
		{"twist begin", s.TwistBegin, got.TwistBegin, scaleQuantum},
		{"twist end", s.TwistEnd, got.TwistEnd, scaleQuantum},
		{"top size x", s.TopSizeX, got.TopSizeX, scaleQuantum},
		{"top size y", s.TopSizeY, got.TopSizeY, scaleQuantum},
		{"top shear x", s.TopShearX, got.TopShearX, shearQuantum},
		{"top shear y", s.TopShearY, got.TopShearY, shearQuantum},
		{"advanced cut begin", s.AdvancedCutBegin, got.AdvancedCutBegin, cutQuantum},
		{"advanced cut end", s.AdvancedCutEnd, got.AdvancedCutEnd, cutQuantum},
		{"taper x", s.TaperX, got.TaperX, taperQuantum},
		{"taper y", s.TaperY, got.TaperY, taperQuantum},
		{"revolutions", s.Revolutions, got.Revolutions, revQuantum},
		{"radius offset", s.RadiusOffset, got.RadiusOffset, scaleQuantum},
		{"skew", s.Skew, got.Skew, scaleQuantum},
	} {
		if d := math.Abs(float64(c.want - c.is)); d > c.tol {
			t.Errorf("%s: %v came back %v, %v off and the quantum is %v",
				c.what, c.want, c.is, d, c.tol)
		}
	}
	if got.Type != "torus" || got.HoleShape != HoleSquare {
		t.Errorf("torus with a square hole came back as %q with hole %#x", got.Type, got.HoleShape)
	}
}

// TestTheCutSwapsWithThePath.  A prim swept along a line is cut by its
// profile and one swept round a circle by its path, which is why LSL
// calls a sphere's cut a dimple.  Getting it wrong cuts the wrong axis
// and looks like the numbers were ignored.
func TestTheCutSwapsWithThePath(t *testing.T) {
	line := Shape{Type: "box", CutBegin: 0.25, CutEnd: 0.75, TopSizeX: 1, TopSizeY: 1, Revolutions: 1}
	p, err := line.Pack()
	if err != nil {
		t.Fatal(err)
	}
	if p.ProfileBegin == 0 {
		t.Error("a box's cut did not reach the profile")
	}
	if p.PathBegin != 0 || p.PathEnd != 0 {
		t.Errorf("a box's path was cut: begin %d end %d", p.PathBegin, p.PathEnd)
	}

	round := Shape{Type: "sphere", CutBegin: 0.25, CutEnd: 0.75, TopSizeX: 1, TopSizeY: 1, Revolutions: 1}
	p, err = round.Pack()
	if err != nil {
		t.Fatal(err)
	}
	if p.PathBegin == 0 {
		t.Error("a sphere's cut did not reach the path")
	}
}
