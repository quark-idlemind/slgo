package sl

import (
	"context"
	"sync"
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

// ------------------------------------------------ reading it back again

// aRegionThatKeepsTheAppearance is a fake grid that behaves the way a
// measured one does: an ObjectImage changes what the object really
// looks like and the region says nothing about it afterwards, so the
// only way to see the change is to ask for the object to be described
// again.
//
// The session's own store is modelled too, since that is where the lag
// lives: the appearance held there is dropped when the change goes out
// -- agent.Objects.sent does that on the daemon -- and filled in again
// when the region answers a request.
func aRegionThatKeepsTheAppearance(t *testing.T, f *fakeBackend, faces []Face) func() []Face {
	t.Helper()
	truth := append([]Face(nil), faces...)
	var mu sync.Mutex

	te, err := EncodeTextureEntry(truth)
	if err != nil {
		t.Fatalf("EncodeTextureEntry: %v", err)
	}
	f.mu.Lock()
	f.objects = []*Seen{{Object: Object{ID: thePrim, Local: 4242, Name: "a thing"}, TextureEntry: te}}
	f.onSend = func(m msg.Message) {
		switch v := m.(type) {
		case *msg.ObjectImage:
			got, err := DecodeTextureEntry(v.ObjectData[0].TextureEntry, len(truth))
			if err != nil {
				t.Errorf("the blob that went out does not decode: %v", err)
				return
			}
			mu.Lock()
			truth = got
			mu.Unlock()
			// What the session held is now wrong and nothing will
			// correct it.
			f.mu.Lock()
			f.objects[0].TextureEntry = nil
			f.mu.Unlock()
		case *msg.RequestMultipleObjects:
			mu.Lock()
			b, err := EncodeTextureEntry(truth)
			mu.Unlock()
			if err != nil {
				t.Errorf("EncodeTextureEntry: %v", err)
				return
			}
			f.mu.Lock()
			f.objects[0].TextureEntry = b
			f.mu.Unlock()
		}
	}
	f.mu.Unlock()

	return func() []Face {
		mu.Lock()
		defer mu.Unlock()
		return append([]Face(nil), truth...)
	}
}

// TestFacesAsksTheRegionWhenNothingHasDescribedTheAppearance.
//
// An object with no appearance held against it is the state a change
// leaves behind, so reading one has to be an asking rather than a
// shrug: answering plain white there is answering with the wrong
// object.
func TestFacesAsksTheRegionWhenNothingHasDescribedTheAppearance(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	red := PlainFaces(8)
	for i := range red {
		red[i].SetColour(255, 0, 0)
	}
	aRegionThatKeepsTheAppearance(t, f, red)

	// Nothing here has described it: the appearance is only had by
	// asking.
	f.mu.Lock()
	f.objects[0].TextureEntry = nil
	f.mu.Unlock()

	got, err := w.Faces(context.Background(), aThing())
	if err != nil {
		t.Fatalf("Faces: %v", err)
	}
	if len(sentOf[*msg.RequestMultipleObjects](f)) != 1 {
		t.Fatalf("the region was asked %d times to describe it, want 1; all of them: %s",
			len(sentOf[*msg.RequestMultipleObjects](f)), f.describe())
	}
	if got[0].Colour != [4]uint8{255, 0, 0, 255} {
		t.Errorf("face 0 read back as %v, want the red the region holds", got[0].Colour)
	}
}

// TestFacesAsksNothingWhenItAlreadyKnows: the asking is what a change
// costs, and a read of a settled object must not pay it.
func TestFacesAsksNothingWhenItAlreadyKnows(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	aRegionThatKeepsTheAppearance(t, f, PlainFaces(8))

	if _, err := w.Faces(context.Background(), aThing()); err != nil {
		t.Fatalf("Faces: %v", err)
	}
	if n := len(sentOf[*msg.RequestMultipleObjects](f)); n != 0 {
		t.Errorf("%d requests went out for an appearance already known", n)
	}
}

// TestTwoChangesInARowCompose, which is the whole point of forgetting
// an appearance that has been replaced.
//
// Measured on a live region before any of this: colouring every face
// and then one face alone left the other five as they had been before
// the first change, because the second read the appearance from before
// it.
func TestTwoChangesInARowCompose(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	truth := aRegionThatKeepsTheAppearance(t, f, PlainFaces(8))

	ctx := context.Background()
	if err := w.SetFace(ctx, aThing(), AllFaces, func(fc *Face) { fc.SetColour(255, 0, 0) }); err != nil {
		t.Fatalf("colouring every face: %v", err)
	}
	if err := w.SetFace(ctx, aThing(), 2, func(fc *Face) { fc.SetColour(0, 255, 0) }); err != nil {
		t.Fatalf("colouring face 2: %v", err)
	}

	for i, fc := range truth() {
		want := [4]uint8{255, 0, 0, 255}
		if i == 2 {
			want = [4]uint8{0, 255, 0, 255}
		}
		if fc.Colour != want {
			t.Errorf("face %d ended up %v, want %v", i, fc.Colour, want)
		}
	}
}

// TestAChangeRefusesAnAppearanceNothingHasDescribed.
//
// A report can settle for plain white, since a prim nothing has
// described is a plain white prim.  A change cannot: it sends every
// face, so the stand-in would go out as the object's real appearance
// and wipe whatever it wore.  Refusing costs a retry; the other way
// costs the object.
func TestAChangeRefusesAnAppearanceNothingHasDescribed(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	f.mu.Lock()
	// Described, but never described a look -- and nothing answers the
	// asking either.
	f.objects = []*Seen{{Object: Object{ID: thePrim, Local: 4242, Name: "a thing"}}}
	f.mu.Unlock()

	err := w.SetFace(context.Background(), aThing(), 0, func(fc *Face) { fc.SetColour(255, 0, 0) })
	if err == nil {
		t.Fatal("a change went out against an appearance nothing had described")
	}
	if n := len(sentOf[*msg.ObjectImage](f)); n != 0 {
		t.Errorf("%d changes went out anyway", n)
	}

	// The report is still answered, because white is the truth about
	// such a prim.
	faces, err := w.Faces(context.Background(), aThing())
	if err != nil {
		t.Fatalf("Faces: %v", err)
	}
	if faces[0].Colour != [4]uint8{255, 255, 255, 255} {
		t.Errorf("the report read %v, want plain white", faces[0].Colour)
	}
}
