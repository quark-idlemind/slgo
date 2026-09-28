package main

// Where a touch lands: the face, the point on it, and the same point
// in the texture's coordinates, which sl works out from the other.

import (
	"math"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// aTurnedSign stands a prim whose face 1 has its texture turned a
// quarter, repeated 2 and 0.5 and slid 0.25 and -0.1, so that a point
// on the face and the same point in the texture are different numbers:
// <0.75, 0.6> on the face is <0.95, 0.275> in the texture.
func aTurnedSign(t *testing.T, x *testShell) {
	t.Helper()
	faces := sl.PlainFaces(2)
	faces[1].ScaleS, faces[1].ScaleT = 2, 0.5
	faces[1].OffsetS, faces[1].OffsetT = 8192, -3277
	faces[1].Rotation = 8192
	te, err := sl.EncodeTextureEntry(faces)
	if err != nil {
		t.Fatal(err)
	}
	x.grid.mu.Lock()
	x.grid.objects = []*sl.Seen{{
		Object:       sl.Object{ID: msg.UUID{0: 0x51, 15: 0x61}, Local: 5161, Name: "a sign"},
		TextureEntry: te,
	}}
	x.grid.mu.Unlock()
}

func nearly(a, b msg.Vector3) bool {
	return math.Abs(float64(a.X-b.X)) < 1e-4 && math.Abs(float64(a.Y-b.Y)) < 1e-4
}

// TestTouchSendsThePointAndItsTextureCoordinate: --st is where on the
// face and --uv the same point in the texture, and either one given is
// the other worked out, as the viewer works it.  --uv alone used to send
// ST as the middle of the face, which is all a script reading
// llDetectedTouchST then saw.
func TestTouchSendsThePointAndItsTextureCoordinate(t *testing.T) {
	st, uv := msg.Vector3{X: 0.75, Y: 0.6}, msg.Vector3{X: 0.95, Y: 0.275}
	for _, line := range []string{
		`touch -f 1 --st 0.75,0.6 "a sign"`,
		`touch -f 1 --uv 0.95,0.275 "a sign"`,
	} {
		x := newTestShell(t)
		aTurnedSign(t, x)
		x.do(t, line)
		var grab *msg.ObjectGrab
		for _, m := range x.grid.Sent() {
			if g, ok := m.(*msg.ObjectGrab); ok {
				grab = g
			}
		}
		if grab == nil {
			t.Fatalf("%s sent no grab", line)
		}
		s := grab.SurfaceInfo[0]
		if s.FaceIndex != 1 || !nearly(s.STCoord, st) || !nearly(s.UVCoord, uv) {
			t.Errorf("%s sent face %d st %v uv %v, want st %v uv %v", line, s.FaceIndex, s.STCoord, s.UVCoord, st, uv)
		}
	}
}

// TestADragPointIsAPlaceOnTheFace: two numbers after the name are a
// point on the face, its ST, and the texture coordinate is worked out
// from it rather than carried over from the point before.
func TestADragPointIsAPlaceOnTheFace(t *testing.T) {
	x := newTestShell(t)
	aTurnedSign(t, x)
	x.do(t, `touch -f 1 --uv 0.95,0.275 --move 0.05 "a sign" 0.25,0.6`)
	var last *msg.ObjectDeGrab
	for _, m := range x.grid.Sent() {
		if d, ok := m.(*msg.ObjectDeGrab); ok {
			last = d
		}
	}
	if last == nil {
		t.Fatal("the drag never let go")
	}
	// <0.25, 0.6> is <-0.25, 0.1> from the middle; a quarter turn makes
	// it <0.1, 0.25>, the repeats <0.2, 0.125>, and the slide and the
	// middle <0.95, 0.525>.
	s := last.SurfaceInfo[0]
	if want := (msg.Vector3{X: 0.25, Y: 0.6}); !nearly(s.STCoord, want) {
		t.Errorf("let go at st %v, want %v", s.STCoord, want)
	}
	if want := (msg.Vector3{X: 0.95, Y: 0.525}); !nearly(s.UVCoord, want) {
		t.Errorf("let go at uv %v, want %v", s.UVCoord, want)
	}
}
