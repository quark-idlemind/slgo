package msg

import "testing"

// The pairs below were read off Agni rather than computed here: the
// coordinates are what the map answered with and the handles are what
// the session and the teleport requests carried.  A test that worked
// both numbers out of the same expression would agree with itself
// however the expression was wrong.
var handles = []struct {
	name string
	x, y uint32
	want uint64
}{
	{"Pelmar Reach", 43648, 43648, 47991483540340736},
	{"Vortaro", 43584, 43712, 47921114796179456},
	{"Sandbox Goguen", 995, 997, 1094014069892352},
}

// TestAHandleIsTheGridSquareMeasuredInMetres, which is the whole of the
// arithmetic and the only thing about it worth getting wrong.
func TestAHandleIsTheGridSquareMeasuredInMetres(t *testing.T) {
	for _, c := range handles {
		if got := RegionHandle(c.x, c.y); got != c.want {
			t.Errorf("%s is at (%d, %d), so its handle is %d, not %d",
				c.name, c.x, c.y, c.want, got)
		}
	}
}

// TestGridCoordinatesComeBackOutOfAHandle: a handle is opaque, so taking
// one apart is how the packing above is checked -- and how somewhere
// arrived at is said in words.
func TestGridCoordinatesComeBackOutOfAHandle(t *testing.T) {
	for _, c := range handles {
		x, y := GridCoords(c.want)
		if x != c.x || y != c.y {
			t.Errorf("handle %d is (%d, %d), want %s at (%d, %d)",
				c.want, x, y, c.name, c.x, c.y)
		}
	}
}

// TestAHandleIsNotTheCoordinatesUnmultiplied.
//
// The pair and the handle are the same fact in different units, and a
// handle built without the 256 is a plausible number that names a region
// two hundred and fifty six squares away -- which as a teleport
// destination is no region at all.
func TestAHandleIsNotTheCoordinatesUnmultiplied(t *testing.T) {
	h := RegionHandle(1, 1)
	if h == 1<<32|1 {
		t.Fatalf("RegionHandle(1, 1) = %d, which is the coordinates packed without the width", h)
	}
	if x, y := GridCoords(h); x != 1 || y != 1 {
		t.Errorf("the round trip gave (%d, %d), want (1, 1)", x, y)
	}
}
