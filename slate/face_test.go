package slate

// Face properties (fullbright, glow, colour, alpha) and face all, on the
// fake grid. The entries are encoded as the store encodes them, so the
// glow byte, the inverted colour and the bump byte take the route they
// take on the wire.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

func withBright(face int, on bool) func(*sl.Seen) {
	return withFace(face, func(f *sl.Face) { f.SetFullbright(on) })
}

func withGlow(face int, g uint8) func(*sl.Seen) {
	return withFace(face, func(f *sl.Face) { f.Glow = g })
}

// onAllFaces applies a change to each of the six faces at once.
func onAllFaces(fn func(*sl.Face)) func(*sl.Seen) {
	return func(o *sl.Seen) {
		for i := range 6 {
			withFace(i, fn)(o)
		}
	}
}

func TestFullbrightBecomesOnThenOff(t *testing.T) {
	f := newGrid(t)
	f.whenSaid("go", func() {
		f.changeAfter(t, 40*time.Millisecond, signLocal, withBright(1, true))
		f.changeAfter(t, 200*time.Millisecond, signLocal, withBright(1, false))
	})
	res := play(t, f, hdr+`say "go" on 0
expect fullbright sign face 1 becomes on within 1s
expect fullbright sign face 1 becomes off within 1s
`)
	wantExit(t, res, 0)
	mustHave(t, res, "fullbright sign face 1 off", "fullbright sign face 1 on")
	// Another face does not satisfy it, and the bump byte's other bits do not count.
	f = newGrid(t)
	f.whenSaid("go", func() {
		f.changeAfter(t, 30*time.Millisecond, signLocal, withBright(2, true))
		f.changeAfter(t, 30*time.Millisecond, signLocal, withFace(1, func(fc *sl.Face) { fc.SetBumpiness(5) }))
	})
	res = play(t, f, hdr+"say \"go\" on 0\nexpect fullbright sign face 1 becomes on within 300ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "unmatched fullbright sign face 1 becomes on")
}

func TestGlowAndTheTolerance(t *testing.T) {
	f := newGrid(t)
	f.whenSaid("go", func() { f.changeAfter(t, 30*time.Millisecond, signLocal, withGlow(0, 128)) })
	res := play(t, f, hdr+"say \"go\" on 0\nexpect glow sign face 0 becomes 0.5 within 1s\n")
	wantExit(t, res, 0)
	mustHave(t, res, "glow sign face 0 0.502")
	// 0.5 is within one step of 128/255 and 0.6 is not.
	f = newGrid(t)
	f.change(signLocal, withGlow(0, 128))
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect glow sign face 0 is 0.5 within 300ms\n"), 0)
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect glow sign face 0 is 0.5059 within 150ms\n"), 1)
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect glow sign face 0 is 0.6 within 150ms\n"), 1)
	// A change of one step is not a change; three are.
	f = newGrid(t)
	f.whenSaid("go", func() { f.changeAfter(t, 30*time.Millisecond, signLocal, withGlow(0, 1)) })
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect glow sign face 0 changes within 250ms\n"), 1)
	f = newGrid(t)
	f.whenSaid("go", func() { f.changeAfter(t, 30*time.Millisecond, signLocal, withGlow(0, 3)) })
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect glow sign face 0 changes within 1s\n"), 0)
}

func TestColourAndAlpha(t *testing.T) {
	f := newGrid(t)
	f.whenSaid("go", func() {
		f.changeAfter(t, 30*time.Millisecond, signLocal, withFace(0, func(fc *sl.Face) {
			fc.SetColour(255, 0, 128)
			fc.SetAlpha(64)
		}))
	})
	res := play(t, f, hdr+`say "go" on 0
expect colour sign face 0 becomes 1 0 0.5 within 1s
expect alpha sign face 0 becomes 0.25 within 1s
`)
	wantExit(t, res, 0)
	mustHave(t, res, "colour sign face 0 1 0 0.502", "alpha sign face 0 0.251")
	// One channel off by more than a step fails the whole.
	f = newGrid(t)
	f.change(signLocal, withFace(0, func(fc *sl.Face) { fc.SetColour(255, 0, 128) }))
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect colour sign face 0 is 1 0 0.5 within 300ms\n"), 0)
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect colour sign face 0 is 1 0.1 0.5 within 150ms\n"), 1)
	// Colour changes when any channel moves, and alpha is not colour.
	f = newGrid(t)
	f.whenSaid("go", func() {
		f.changeAfter(t, 30*time.Millisecond, signLocal, withFace(0, func(fc *sl.Face) { fc.SetAlpha(10) }))
	})
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect no colour sign face 0 changes within 250ms\n"), 0)
	f = newGrid(t)
	f.whenSaid("go", func() {
		f.changeAfter(t, 30*time.Millisecond, signLocal, withFace(0, func(fc *sl.Face) { fc.SetColour(0, 9, 0) }))
	})
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect colour sign face 0 changes within 1s\n"), 0)
}

func TestOriginalAndNegativeFormsOfAProperty(t *testing.T) {
	f := newGrid(t)
	f.change(signLocal, withGlow(0, 51))
	f.whenSaid("go", func() {
		f.changeAfter(t, 40*time.Millisecond, signLocal, withGlow(0, 200))
		f.changeAfter(t, 250*time.Millisecond, signLocal, withGlow(0, 51))
	})
	res := play(t, f, hdr+`say "go" on 0
expect glow sign face 0 changes within 1s
expect glow sign face 0 becomes original within 1s
`)
	wantExit(t, res, 0)
	// A negative that holds, and one that is broken.
	f = newGrid(t)
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect no fullbright sign face 0 becomes on within 150ms\nexpect no glow sign face 0 changes within 150ms\n"), 0)
	f = newGrid(t)
	f.whenSaid("go", func() { f.changeAfter(t, 30*time.Millisecond, signLocal, withBright(0, true)) })
	res = play(t, f, hdr+"say \"go\" on 0\nexpect no fullbright sign face 0 is on within 2s\n")
	wantExit(t, res, 1)
	mustHave(t, res, "forbidden no fullbright sign face 0 is on")
}

func TestAPropertyOnALinkReadsThatPrim(t *testing.T) {
	f, o := world(t)
	o.extraSay = func(m msg.Message) {
		if g, ok := m.(*msg.ObjectGrab); ok && g.ObjectData.LocalID == 201 {
			f.change(201, withBright(0, true))
		}
	}
	res := o.play(t, probeHdr+"touch vendor link 2\nexpect fullbright vendor link 2 face 0 becomes on within 1s\n")
	wantExit(t, res, 0)
	mustHave(t, res, "fullbright vendor link 2 face 0 on")
	f, o = world(t)
	res = o.play(t, probeHdr+"touch vendor link 2\nexpect fullbright vendor link 3 face 0 is on within 200ms\n")
	wantExit(t, res, 1)
}

func TestIsAnyCapturesAProperty(t *testing.T) {
	f := newGrid(t)
	f.change(signLocal, withGlow(2, 255))
	f.change(signLocal, withFace(2, func(fc *sl.Face) { fc.SetColour(0, 255, 0); fc.SetAlpha(0) }))
	f.change(signLocal, withBright(2, true))
	f.whenSaid("go", func() {
		f.change(signLocal, withGlow(0, 255))
		f.change(signLocal, withFace(0, func(fc *sl.Face) { fc.SetColour(0, 255, 0); fc.SetAlpha(0) }))
		f.change(signLocal, withBright(0, true))
	})
	res := play(t, f, hdr+`expect glow sign face 2 is any within 500ms as $g
expect colour sign face 2 is any within 500ms as $c
expect alpha sign face 2 is any within 500ms as $a
expect fullbright sign face 2 is any within 500ms as $b
say "go" on 0
expect glow sign face 0 becomes $g within 1s
expect colour sign face 0 becomes $c within 1s
expect alpha sign face 0 becomes $a within 1s
expect fullbright sign face 0 becomes $b within 1s
`)
	wantExit(t, res, 0)
	mustHave(t, res, "capture $g = 1 (step 1)", "capture $c = 0 1 0 (step 1)", "capture $a = 0 (step 1)", "capture $b = on (step 1)")
}

func TestFaceAllFullbright(t *testing.T) {
	// becomes: every face on, and an earlier reading in the window was not.
	f := newGrid(t)
	f.whenSaid("go", func() {
		f.changeAfter(t, 30*time.Millisecond, signLocal, withBright(0, true))
		f.changeAfter(t, 130*time.Millisecond, signLocal, onAllFaces(func(fc *sl.Face) { fc.SetFullbright(true) }))
	})
	res := play(t, f, hdr+"say \"go\" on 0\nexpect fullbright sign face all becomes on within 2s\n")
	wantExit(t, res, 0)
	mustHave(t, res, "fullbright sign face all off", "fullbright sign face all on")
	// A face that differs blocks is.
	f = newGrid(t)
	f.change(signLocal, onAllFaces(func(fc *sl.Face) { fc.SetFullbright(true) }))
	f.change(signLocal, withBright(3, false))
	res = play(t, f, hdr+"say \"go\" on 0\nexpect fullbright sign face all is on within 300ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "unmatched fullbright sign face all is on")
	f.change(signLocal, withBright(3, true))
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect fullbright sign face all is on within 500ms\n"), 0)
	// A box with one face on, the rest as they were: not all.
	f = newGrid(t)
	f.change(signLocal, withBright(0, true))
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect fullbright sign face all is on within 200ms\n"), 1)
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect fullbright sign face 0 is on within 200ms\n"), 0)
	// An all that is already true does not satisfy becomes.
	f = newGrid(t)
	f.change(signLocal, onAllFaces(func(fc *sl.Face) { fc.SetFullbright(true) }))
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect fullbright sign face all becomes on within 200ms\n"), 1)
}

func TestFaceAllChangesAndOriginal(t *testing.T) {
	f := newGrid(t)
	f.whenSaid("go", func() {
		f.changeAfter(t, 40*time.Millisecond, signLocal, withBright(4, true))
		f.changeAfter(t, 250*time.Millisecond, signLocal, withBright(4, false))
	})
	res := play(t, f, hdr+`say "go" on 0
expect fullbright sign face all changes within 1s
expect fullbright sign face all becomes original within 1s
`)
	wantExit(t, res, 0)
	// One face of the tuple moving is a change, and a quiet one is not.
	f = newGrid(t)
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect no fullbright sign face all changes within 200ms\n"), 0)
	// Glow, with its tolerance, across the tuple.
	f = newGrid(t)
	f.whenSaid("go", func() { f.changeAfter(t, 30*time.Millisecond, signLocal, withGlow(5, 1)) })
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect glow sign face all changes within 250ms\n"), 1)
	f = newGrid(t)
	f.whenSaid("go", func() { f.changeAfter(t, 30*time.Millisecond, signLocal, withGlow(5, 40)) })
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect glow sign face all changes within 1s\n"), 0)
}

func TestFaceAllTexture(t *testing.T) {
	f := newGrid(t)
	f.whenSaid("go", func() {
		f.changeAfter(t, 30*time.Millisecond, signLocal, withTexture(0, idTexA))
		f.changeAfter(t, 130*time.Millisecond, signLocal, onAllFaces(func(fc *sl.Face) { fc.Texture = idTexA }))
	})
	res := play(t, f, hdr+fmt.Sprintf("say \"go\" on 0\nexpect texture sign face all becomes %s within 2s\n", idTexA))
	wantExit(t, res, 0)
	mustHave(t, res, "texture sign face all "+idTexA.String())
	// One face apart.
	f = newGrid(t)
	f.change(signLocal, onAllFaces(func(fc *sl.Face) { fc.Texture = idTexA }))
	f.change(signLocal, withTexture(2, idTexB))
	wantExit(t, play(t, f, hdr+fmt.Sprintf("say \"go\" on 0\nexpect texture sign face all is %s within 300ms\n", idTexA)), 1)
	// Offset, repeats and rotation take it too.
	f = newGrid(t)
	f.change(signLocal, onAllFaces(func(fc *sl.Face) { fc.Rotation = 8192; fc.OffsetS = 16384 }))
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect rotation sign face all is 0.25 within 300ms\nexpect offset sign face all is 0.5 0 within 300ms\n"), 0)
	f.change(signLocal, withFace(1, func(fc *sl.Face) { fc.Rotation = 0 }))
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect rotation sign face all is 0.25 within 150ms\n"), 1)
}

func TestFaceAllCapturesATuple(t *testing.T) {
	f := newGrid(t)
	f.change(signLocal, onAllFaces(func(fc *sl.Face) { fc.SetFullbright(true) }))
	f.whenSaid("go", func() { f.change(signLocal, withBright(2, false)) })
	res := play(t, f, hdr+`expect fullbright sign face all is any within 500ms as $all
say "go" on 0
expect fullbright sign face all changes within 1s
then expect fullbright sign face all is $all within 300ms
`)
	wantExit(t, res, 1) // the face went off and stayed off, so the tuple is not the capture
	mustHave(t, res, "capture $all = face all on")

	f = newGrid(t)
	f.change(signLocal, onAllFaces(func(fc *sl.Face) { fc.SetFullbright(true) }))
	f.whenSaid("go", func() {
		f.change(signLocal, withBright(2, false))
		f.changeAfter(t, 150*time.Millisecond, signLocal, withBright(2, true))
	})
	res = play(t, f, hdr+`expect fullbright sign face all is any within 500ms as $all
say "go" on 0
expect fullbright sign face all becomes $all within 1s
`)
	wantExit(t, res, 0)

	// A tuple is used by a face all alone, and a single value likewise;
	// Check refuses the others before anything runs.
	checkErr(t, hdr+`expect fullbright sign face all is any within 500ms as $all
then expect fullbright sign face 0 is $all within 200ms
`, "$all holds every face")
	checkErr(t, hdr+`expect glow sign face 0 is any within 500ms as $g
then expect glow sign face all is $g within 200ms
`, "$g holds one face")
}

func TestACaptureOfFaceAllIsRefusedWhereAFaceCannotBe(t *testing.T) {
	// A key takes one id, so a capture of every face is refused by Check.
	checkErr(t, probeHdr+`expect texture sign face all is any within 300ms as $t
then expect link on vendor from link 1 num 7 text "x" key $t within 300ms
`, "$t holds every face")
}

// faceX and faceY are two faces that differ in one thing the tests read.
func faceX(id msg.UUID) sl.Face { f := sl.PlainFaces(1)[0]; f.Texture = id; return f }

func repeatFace(f sl.Face, n int) []sl.Face {
	out := make([]sl.Face, n)
	for i := range out {
		out[i] = f
	}
	return out
}

// noteOf is the transcript line of a face count that is not known.
func noteOf(name string) string {
	return fmt.Sprintf("slate: step 1: the face count of %q is not known (sculpt, mesh or not described); face all reads the faces its texture entry names", name)
}

func TestFaceAllHasExactlyThePrimsFaces(t *testing.T) {
	// A box of three faces of A and three of B: not all A, not all B, and
	// the tuple has six elements, not a phantom seventh.
	x, y := faceX(idTexA), faceX(idTexB)
	f := newGrid(t)
	f.change(signLocal, withFaces(x, x, x, y, y, y))
	res := play(t, f, hdr+fmt.Sprintf("say \"go\" on 0\nexpect texture sign face all is %s within 300ms\n", idTexB))
	wantExit(t, res, 1)
	mustHave(t, res, fmt.Sprintf("texture sign face all %s, %s, %s, %s, %s, %s", idTexA, idTexA, idTexA, idTexB, idTexB, idTexB))
	mustNotHave(t, res, fmt.Sprintf("%s, %s, %s, %s, %s, %s, ", idTexA, idTexA, idTexA, idTexB, idTexB, idTexB))
	mustNotHave(t, res, "is not known")

	// becomes works across all six, the last face being the one that moves.
	f = newGrid(t)
	f.change(signLocal, withFaces(x, x, x, x, x, y))
	f.whenSaid("go", func() { f.changeAfter(t, 50*time.Millisecond, signLocal, withTexture(5, idTexA)) })
	wantExit(t, play(t, f, hdr+fmt.Sprintf("say \"go\" on 0\nexpect texture sign face all becomes %s within 2s\n", idTexA)), 0)
}

func TestFaceAllReadsAFaceTheDefaultWouldHide(t *testing.T) {
	// A hollow cut box has nine faces, and a texture on the last is seen.
	f := newGrid(t)
	shape := sl.DefaultShape()
	shape.CutBegin, shape.CutEnd, shape.Hollow = 0.2, 0.8, 0.5
	f.change(signLocal, withShape(shape))
	b := faceX(idTexB)
	f.change(signLocal, withFaces(append(repeatFace(b, 8), faceX(idTexA))...))
	res := play(t, f, hdr+fmt.Sprintf("say \"go\" on 0\nexpect texture sign face all is %s within 200ms\n", idTexB))
	wantExit(t, res, 1)
	want := strings.TrimSuffix(strings.Repeat(idTexB.String()+", ", 8), "") + idTexA.String()
	mustHave(t, res, "texture sign face all "+want)
}

func TestFaceAllOfACylinderIsThreeFaces(t *testing.T) {
	// The entry's default is B; faces 0 to 2 are A and are all the prim has.
	x, y := faceX(idTexA), faceX(idTexB)
	f := newGrid(t)
	cyl := sl.DefaultShape()
	cyl.Type = "cylinder"
	f.change(signLocal, withShape(cyl))
	f.change(signLocal, withFaces(x, x, x, y, y, y, y, y))
	wantExit(t, play(t, f, hdr+fmt.Sprintf("say \"go\" on 0\nexpect texture sign face all is %s within 500ms\n", idTexA)), 0)
}

func TestFaceAllOfASculptKeepsTheOldReadingAndSaysSo(t *testing.T) {
	x, y := faceX(idTexA), faceX(idTexB)
	f := newGrid(t)
	f.change(signLocal, asSculpt)
	f.change(signLocal, withFaces(x, x, x, y, y, y, y, y))
	res := play(t, f, hdr+`say "go" on 0
expect texture sign face all is any within 300ms as $t
expect texture sign face all is any within 300ms as $u
`)
	wantExit(t, res, 0)
	if n := strings.Count(res.Transcript, noteOf("sign")); n != 1 {
		t.Errorf("the note was printed %d times, want 1\n%s", n, res.Transcript)
	}
	// The tuple runs to the last face that differs, plus one.
	mustHave(t, res, fmt.Sprintf("texture sign face all %s, %s, %s, %s", idTexA, idTexA, idTexA, idTexB))
}
