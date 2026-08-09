package sl

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// TestParseObjectsTakesEveryShapeOfFile.  The simulator has written
// three forms over its life and a person hand-writing one file for one
// object writes a fourth; all four name the same thing.
func TestParseObjectsTakesEveryShapeOfFile(t *testing.T) {
	one := `{"prims":[{"type":"box"}]}`
	for _, c := range []struct {
		what string
		in   string
		want int
	}{
		{"a bare object", one, 1},
		{"a list", "[" + one + "," + one + "]", 2},
		{"a world", `{"objects":[` + one + `],"agents":[]}`, 1},
		{"a world with whitespace", "\n\t " + `{"objects":[` + one + `]}`, 1},
	} {
		got, err := ParseObjects([]byte(c.in))
		if err != nil {
			t.Errorf("%s: %v", c.what, err)
			continue
		}
		if len(got) != c.want {
			t.Errorf("%s: %d objects, want %d", c.what, len(got), c.want)
		}
	}

	if _, err := ParseObjects([]byte(`{"agents":[]}`)); err == nil {
		t.Error("a file with neither objects nor prims parsed")
	}
}

// TestAnOmittedShapeIsAPlainOne: {"type":"sphere"} is a sphere, not a
// sphere with every dimension cut away.  Zero is a real value for most
// of these fields and the wrong default for all of them.
func TestAnOmittedShapeIsAPlainOne(t *testing.T) {
	for _, name := range ShapeNames {
		s, err := PrimJSON{Type: name}.Shape()
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		want := DefaultShape()
		want.Type = name
		if s != want {
			t.Errorf("a plain %s reads as %+v", name, s)
		}
	}
	if _, err := (PrimJSON{Type: "blancmange"}).Shape(); err == nil {
		t.Error("a shape nobody has heard of was accepted")
	}
}

// TestASpheresCutIsCalledADimple, which is LSL's spelling and the
// simulator's.  Reading it from the wrong field cuts nothing and looks
// like the file was ignored.
func TestASpheresCutIsCalledADimple(t *testing.T) {
	s, err := PrimJSON{Type: "sphere", Dimple: []float32{0.25, 0.75, 0}}.Shape()
	if err != nil {
		t.Fatal(err)
	}
	if s.CutBegin != 0.25 || s.CutEnd != 0.75 {
		t.Errorf("a dimple read as cut %v to %v", s.CutBegin, s.CutEnd)
	}

	// And it is written back the same way round.
	seen := &Seen{}
	p, _ := s.Pack()
	seen.Shape = p
	out := describePrim(seen, nil)
	if len(out.Dimple) == 0 || len(out.Cut) != 0 {
		t.Errorf("a sphere described with cut %v and dimple %v", out.Cut, out.Dimple)
	}
}

// TestDescribingAPlainPrimSaysLittle.  A file listing every field of
// every prim is one nobody will read, so a field that is what a plain
// prim of that type has is left out.
func TestDescribingAPlainPrimSaysLittle(t *testing.T) {
	p, _ := DefaultShape().Pack()
	out := describePrim(&Seen{Shape: p}, nil)

	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{"hollow", "twist", "topsize", "cut", "taper", "skew"} {
		if strings.Contains(string(b), `"`+unwanted+`"`) {
			t.Errorf("a plain box was described with %q:\n%s", unwanted, b)
		}
	}
	if out.Type != "box" {
		t.Errorf("a plain box is a %q", out.Type)
	}
}

// TestABuiltListPutsChildrenWhereTheRootIs.  A file may give the root a
// position and leave the children to follow, which is what a linkset
// described relative to nothing has to mean.
func TestABuiltListPutsChildrenWhereTheRootIs(t *testing.T) {
	o := ObjectJSON{Prims: []PrimJSON{
		{Type: "box", Pos: []float32{10, 20, 30}},
		{Type: "sphere"},
		{Type: "torus", Pos: []float32{11, 20, 30}},
	}}
	prims, err := o.BuildList()
	if err != nil {
		t.Fatal(err)
	}
	if prims[1].Position != prims[0].Position {
		t.Errorf("a child with no position went to %v, root is at %v", prims[1].Position, prims[0].Position)
	}
	if prims[2].Position.X != 11 {
		t.Errorf("a child with a position was moved to %v", prims[2].Position)
	}
	if prims[1].Shape.Type != "sphere" || prims[2].Shape.Type != "torus" {
		t.Errorf("shapes came out as %q and %q", prims[1].Shape.Type, prims[2].Shape.Type)
	}
	if _, err := (ObjectJSON{}).BuildList(); err == nil {
		t.Error("an object with no prims built")
	}
}

// TestANegativeWIsTheSameRotation.  The wire form carries three of the
// four components and recovers the fourth by normalising, which only
// works if W is not negative -- so a file that writes one has to be
// read as the same rotation with every component negated.
func TestANegativeWIsTheSameRotation(t *testing.T) {
	q := quat([]float32{0.5, 0, 0, -0.8660254})
	if q.X != -0.5 {
		t.Errorf("a rotation with a negative w read as %v", q)
	}
	if w := q.W(); math.Abs(float64(w)-0.8660254) > 0.0001 {
		t.Errorf("w came back as %v", w)
	}
}
