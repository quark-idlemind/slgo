package slate

// A plain touch sends the UV a viewer would: the face's texture mapping
// applied to ST.
// Why: doc/slate-runner.md#stimuli

import (
	"testing"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// turnedFace is face 0 turned a quarter, repeated twice across and half
// down, and slid a quarter and a tenth, so that ST taken for UV shows.
func turnedFace(t *testing.T, s *sl.Seen) {
	t.Helper()
	faces := sl.PlainFaces(6)
	faces[0].ScaleS, faces[0].ScaleT = 2, 0.5
	faces[0].OffsetS, faces[0].OffsetT = 8192, -3277
	faces[0].Rotation = 8192
	te, err := sl.EncodeTextureEntry(faces)
	if err != nil {
		t.Fatal(err)
	}
	s.TextureEntry = te
}

func TestAPlainTouchSendsTheUVTheFaceGives(t *testing.T) {
	for _, step := range []string{
		"touch vendor link 2 face 0 at 0.75 0.6",
		"touch vendor link 1 face 0 at 0.75 0.6",
	} {
		f, vendor, lid, _ := storeWorld(t)
		turnedFace(t, lid)
		turnedFace(t, vendor)
		wantExit(t, play(t, f, hdr+step+"\n"), 0)
		grabs := sentOf[*msg.ObjectGrab](f)
		if len(grabs) != 1 {
			t.Fatalf("%s: %d grabs", step, len(grabs))
		}
		si := grabs[0].SurfaceInfo[0]
		want := msg.Vector3{X: 0.95, Y: 0.275}
		if si.STCoord != (msg.Vector3{X: 0.75, Y: 0.6}) || si.UVCoord.X < want.X-1e-4 || si.UVCoord.X > want.X+1e-4 ||
			si.UVCoord.Y < want.Y-1e-4 || si.UVCoord.Y > want.Y+1e-4 {
			t.Errorf("%s: st %v uv %v, want uv %v", step, si.STCoord, si.UVCoord, want)
		}
	}
}
