package slate

import (
	"fmt"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var (
	idShowA = msg.MustParseUUID("2af17e57-7e57-c0de-d7e3-d90bee2591fa")
	idShowB = msg.MustParseUUID("8d887e57-7e57-c0de-fc96-86df7d38f4fc")
	idShowC = msg.MustParseUUID("9d717e57-7e57-c0de-0108-423b1b457593")
	idShowD = msg.MustParseUUID("b2f37e57-7e57-c0de-b1e4-d786a36fbb76")
	idPanel = msg.MustParseUUID("c24c7e57-7e57-c0de-3b3a-f0f5a5bc7825")
)

// showGrid is a sign of two prims: the root (local 101) and a panel child
// (local 103). The root shows A on face 1 and B on face 3, the panel C on
// face 2.
func showGrid(t *testing.T) *fakeGrid {
	t.Helper()
	f := newGrid(t)
	f.appear(child(at(prim(idPanel, 103, "Example Panel", testMe), 130, 128, 26), f.objects[0]))
	f.change(101, withTexture(1, idShowA))
	f.change(101, withTexture(3, idShowB))
	f.change(103, withTexture(2, idShowC))
	return f
}

func touchOf(t *testing.T, f *fakeGrid) (local uint32, face int32, st msg.Vector3) {
	t.Helper()
	grabs := sentOf[*msg.ObjectGrab](f)
	if len(grabs) != 1 || len(sentOf[*msg.ObjectDeGrab](f)) != 1 || len(grabs[0].SurfaceInfo) != 1 {
		t.Fatalf("%d grabs", len(grabs))
	}
	si := grabs[0].SurfaceInfo[0]
	return grabs[0].ObjectData.LocalID, si.FaceIndex, si.STCoord
}

func noTouch(t *testing.T, f *fakeGrid) {
	t.Helper()
	if n := len(sentOf[*msg.ObjectGrab](f)); n != 0 {
		t.Errorf("%d grabs sent", n)
	}
}

func TestShowingTouchesTheOneFaceThatShowsTheTexture(t *testing.T) {
	for _, c := range []struct {
		id    msg.UUID
		local uint32
		face  int32
		prim  string
	}{
		{idShowA, 101, 1, "Example Sign"},
		{idShowB, 101, 3, "Example Sign"},
		{idShowC, 103, 2, "Example Panel"},
	} {
		f := showGrid(t)
		res := play(t, f, hdr+"touch sign showing "+c.id.String()+"\n")
		wantExit(t, res, 0)
		local, face, st := touchOf(t, f)
		if local != c.local || face != c.face || st != (msg.Vector3{X: 0.5, Y: 0.5}) {
			t.Errorf("%s: local %d face %d st %v", c.id, local, face, st)
		}
		if n := len(f.requests(103)); n == 0 {
			t.Errorf("the linkset was not described again")
		}
	}
}

func TestShowingAtSendsTheCoordinates(t *testing.T) {
	f := showGrid(t)
	wantExit(t, play(t, f, hdr+"touch sign showing "+idShowC.String()+" at 0.25 0.75\n"), 0)
	local, face, st := touchOf(t, f)
	if local != 103 || face != 2 || st != (msg.Vector3{X: 0.25, Y: 0.75}) {
		t.Errorf("local %d face %d st %v", local, face, st)
	}
}

func TestShowingWithNoMatchFailsBeforeAnythingIsSent(t *testing.T) {
	f := showGrid(t)
	res := play(t, f, hdr+"touch sign showing "+idShowD.String()+"\n")
	wantExit(t, res, 1)
	want := fmt.Sprintf(`slate: step 1: no face of "Example Sign"'s linkset shows %s`, idShowD)
	n := 0
	for _, l := range lines(res) {
		if strings.TrimSpace(l) == want {
			n++
		}
	}
	if n != 2 {
		t.Errorf("the sentence is on %d lines, want 2:\n%s", n, res.Transcript)
	}
	noTouch(t, f)
}

func TestShowingWithTwoMatchesListsEachAndSendsNothing(t *testing.T) {
	// Two prims.
	f := showGrid(t)
	f.change(103, withTexture(0, idShowA))
	res := play(t, f, hdr+"touch sign showing "+idShowA.String()+"\n")
	wantExit(t, res, 1)
	mustHave(t, res, fmt.Sprintf(`slate: step 1: 2 faces of "Example Sign"'s linkset show %s: "Example Sign" face 1; "Example Panel" face 0`, idShowA))
	noTouch(t, f)

	// Two faces of one prim.
	f = showGrid(t)
	f.change(101, withTexture(4, idShowA))
	res = play(t, f, hdr+"touch sign showing "+idShowA.String()+"\n")
	wantExit(t, res, 1)
	mustHave(t, res, fmt.Sprintf(`slate: step 1: 2 faces of "Example Sign"'s linkset show %s: "Example Sign" face 1; "Example Sign" face 4`, idShowA))
	noTouch(t, f)
}

func TestShowingReadsFreshFacesNotTheStaleStore(t *testing.T) {
	// The store says A is on face 1; the region has moved it to face 5.
	f := showGrid(t)
	f.changeOnRequest(101, func(o *sl.Seen) {
		withTexture(1, idPanel)(o)
		withTexture(5, idShowA)(o)
	})
	wantExit(t, play(t, f, hdr+"touch sign showing "+idShowA.String()+"\n"), 0)
	if _, face, _ := touchOf(t, f); face != 5 {
		t.Errorf("touched face %d, want 5", face)
	}

	// A texture the store still shows and the region has taken away.
	f = showGrid(t)
	f.changeOnRequest(101, withTexture(1, idPanel))
	wantExit(t, play(t, f, hdr+"touch sign showing "+idShowA.String()+"\n"), 1)
	noTouch(t, f)
}

func TestShowingUsesAUUIDCapture(t *testing.T) {
	f := showGrid(t)
	res := play(t, f, hdr+"expect texture sign face 3 is any as $tile\ntouch sign showing $tile at 0.1 0.9\n")
	wantExit(t, res, 0)
	local, face, st := touchOf(t, f)
	if local != 101 || face != 3 || st != (msg.Vector3{X: 0.1, Y: 0.9}) {
		t.Errorf("local %d face %d st %v", local, face, st)
	}
}

func TestShowingReportsWhatItTouched(t *testing.T) {
	f := showGrid(t)
	res := play(t, f, hdr+"touch sign showing "+idShowC.String()+"\nexpect say \"never\" on public from anyone within 100ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, fmt.Sprintf(`touched "Example Panel" face 2 showing %s`, idShowC))
}

func TestShowingSearchesExactlyThePrimsFaces(t *testing.T) {
	// A hollow cut box has nine faces, and a texture on the ninth is found.
	f := showGrid(t)
	shape := sl.DefaultShape()
	shape.CutBegin, shape.CutEnd, shape.Hollow = 0.2, 0.8, 0.5
	f.change(101, withShape(shape))
	f.change(101, withTexture(8, idShowD))
	res := play(t, f, hdr+"touch sign showing "+idShowD.String()+"\n")
	wantExit(t, res, 0)
	if local, face, _ := touchOf(t, f); local != 101 || face != 8 {
		t.Errorf("local %d face %d, want 101 face 8", local, face)
	}
	mustNotHave(t, res, "is not known")

	// A texture on the default of a six-face box is not on a seventh face.
	f = newGrid(t)
	f.change(101, withFaces(repeatFace(faceX(idShowA), 6)...))
	wantExit(t, play(t, f, hdr+"touch sign showing "+idShowA.String()+"\n"), 1)
}

func TestShowingOfASculptKeepsTheOldSearchAndSaysSo(t *testing.T) {
	f := showGrid(t)
	f.change(101, asSculpt)
	res := play(t, f, hdr+"touch sign showing "+idShowA.String()+"\ntouch sign showing "+idShowB.String()+"\n")
	wantExit(t, res, 0)
	if n := strings.Count(res.Transcript, noteOf("sign")); n != 1 {
		t.Errorf("the note was printed %d times, want 1\n%s", n, res.Transcript)
	}
}
