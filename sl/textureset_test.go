package sl

import (
	"context"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

// TestATextureEntryRoundTrips through the decoder that already existed,
// which is the check that matters: the encoder is only right if what it
// writes is what the reader -- and the simulator -- takes it for.
func TestATextureEntryRoundTrips(t *testing.T) {
	want := make([]Face, 6)
	for i := range want {
		want[i] = Face{ScaleS: 1, ScaleT: 1, Colour: [4]uint8{255, 255, 255, 255}}
	}
	// One odd face, which is the case the format exists for.
	want[2].Texture = thePrim
	want[2].SetColour(255, 0, 0)
	want[2].SetAlpha(128)
	want[2].SetRepeats(4, 2)
	want[2].SetOffsets(0.25, -0.5)
	want[2].SetFullbright(true)
	want[2].SetShiny(ShinyHigh)
	want[2].Glow = 77
	// And two faces sharing a value, which should cost one exception.
	want[4].SetRepeats(3, 3)
	want[5].SetRepeats(3, 3)

	b, err := EncodeTextureEntry(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeTextureEntry(b, 6)
	if err != nil {
		t.Fatalf("what we encoded does not decode: %v", err)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("face %d came back\n %+v\nwant\n %+v", i, got[i], want[i])
		}
	}
}

// TestTheCommonestValueIsTheDefault: a prim with one odd face should
// cost one exception, not five.
func TestTheCommonestValueIsTheDefault(t *testing.T) {
	odd := make([]Face, 6)
	for i := range odd {
		odd[i].ScaleS, odd[i].ScaleT = 1, 1
	}
	odd[3].SetRepeats(4, 1)

	same := make([]Face, 6)
	for i := range same {
		same[i].ScaleS, same[i].ScaleT = 1, 1
	}

	a, err := EncodeTextureEntry(odd)
	if err != nil {
		t.Fatal(err)
	}
	b, err := EncodeTextureEntry(same)
	if err != nil {
		t.Fatal(err)
	}
	// One exception is a face set byte plus a four byte value.
	if d := len(a) - len(b); d != 5 {
		t.Errorf("one odd face cost %d bytes, want 5", d)
	}
}

// TestTheBumpByteHoldsThreeThings, and setting one must not disturb
// the others -- fullbright, shininess and bumpiness share a byte.
func TestTheBumpByteHoldsThreeThings(t *testing.T) {
	var f Face
	f.SetBumpiness(7)
	f.SetShiny(ShinyMedium)
	f.SetFullbright(true)

	switch {
	case f.Bumpiness() != 7:
		t.Errorf("bumpiness %d", f.Bumpiness())
	case f.Shiny() != ShinyMedium:
		t.Errorf("shiny %d", f.Shiny())
	case !f.Fullbright():
		t.Error("fullbright was lost")
	}

	f.SetFullbright(false)
	if f.Bumpiness() != 7 || f.Shiny() != ShinyMedium {
		t.Errorf("turning fullbright off disturbed the rest: %08b", f.Bump)
	}
}

// TestAnOpaqueWhiteFaceIsFourZeros.  The colour travels inverted, so
// the commonest face in Second Life is the one that costs nothing --
// and an encoder that forgot the inversion would make every untouched
// prim black and transparent.
func TestAnOpaqueWhiteFaceIsFourZeros(t *testing.T) {
	f := Face{Colour: [4]uint8{255, 255, 255, 255}}
	b, err := EncodeTextureEntry([]Face{f})
	if err != nil {
		t.Fatal(err)
	}
	colour := b[17:21] // 16 bytes of texture, a zero, then the colour
	for i, c := range colour {
		if c != 0 {
			t.Errorf("opaque white encoded as %v, byte %d is %d", colour, i, c)
		}
	}
}

// TestSetFacesSendsOneMessageWithEverythingInIt: the appearance is not
// a per face message, so a caller changing one face still sends all of
// them and must be told when it has not.
func TestSetFacesSendsOneMessageWithEverythingInIt(t *testing.T) {
	w, f := newFakeSession(t)
	faces := make([]Face, 6)
	for i := range faces {
		faces[i].ScaleS, faces[i].ScaleT = 1, 1
	}
	if err := w.SetFaces(context.Background(), aThing(), faces); err != nil {
		t.Fatalf("SetFaces: %v", err)
	}
	m := onlySent[*msg.ObjectImage](t, f)
	if m.ObjectData[0].ObjectLocalID != 4242 {
		t.Errorf("textured local id %d", m.ObjectData[0].ObjectLocalID)
	}
	back, err := DecodeTextureEntry(m.ObjectData[0].TextureEntry, 6)
	if err != nil {
		t.Fatalf("the blob we sent does not decode: %v", err)
	}
	if len(back) != 6 {
		t.Errorf("%d faces went out", len(back))
	}

	if err := w.SetFaces(context.Background(), aThing(), nil); err == nil {
		t.Error("an object with no faces was textured")
	}
}

// TestOffsetsAndRotationAreQuantised, which is the format's doing and
// worth a test so that nobody reads a returned 0.2499 as a bug.
func TestOffsetsAndRotationAreQuantised(t *testing.T) {
	var f Face
	f.SetOffsets(0.25, -0.5)
	s, tt := f.OffsetsF()
	if d := s - 0.25; d > 1e-4 || d < -1e-4 {
		t.Errorf("offset s came back %v", s)
	}
	if d := tt + 0.5; d > 1e-4 || d < -1e-4 {
		t.Errorf("offset t came back %v", tt)
	}
}
