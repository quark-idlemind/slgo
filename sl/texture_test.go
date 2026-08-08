package sl

import (
	"encoding/hex"
	"strings"
	"testing"
)

// A TextureEntry captured from the test region: a freshly rezzed box, all six
// faces on the default plywood, nothing else set.  127 bytes.
//
// Keeping the real bytes rather than a constructed example is the
// point of this test.  The layout is not documented anywhere this
// package can appeal to, so what it decodes has to be checked against
// something the simulator actually sent.
const defaultBoxTE = `
89556747 24cb43ed 920b47ca ed15465f
1f
89556747 24cb43ed 920b47ca ed15465f
00
00000000
1f 00000000
00
0000803f
1f 0000803f
00
0000803f
1f 0000803f
00
0000
1f 0000
00
0000
1f 0000
00
0000
1f 0000
00
00
1f 00
00
00
1f 00
00
00
1f 00
00
00000000 00000000 00000000 00000000
1f
00000000 00000000 00000000 00000000
`

func teBytes(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.Join(strings.Fields(s), ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDecodeTextureEntryDefaultBox(t *testing.T) {
	b := teBytes(t, defaultBoxTE)
	if len(b) != 127 {
		t.Fatalf("the capture is %d bytes, the simulator sent 127", len(b))
	}

	faces, err := DecodeTextureEntry(b, 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(faces) != 6 {
		t.Fatalf("got %d faces, want 6", len(faces))
	}

	const plywood = "89556747-24cb-43ed-920b-47caed15465f"
	for i, f := range faces {
		if got := f.Texture.String(); got != plywood {
			t.Errorf("face %d texture = %s, want the plywood a new prim starts with", i, got)
		}
		// White and opaque.  It travels inverted, so all four bytes
		// are zero on the wire; anything other than 255 here would
		// mean the inversion had been missed.
		if f.Colour != [4]uint8{255, 255, 255, 255} {
			t.Errorf("face %d colour = %v, want opaque white", i, f.Colour)
		}
		if f.ScaleS != 1 || f.ScaleT != 1 {
			t.Errorf("face %d scale = %gx%g, want 1x1", i, f.ScaleS, f.ScaleT)
		}
		if f.OffsetS != 0 || f.OffsetT != 0 || f.Rotation != 0 {
			t.Errorf("face %d has an offset or rotation set", i)
		}
		if f.Bump != 0 || f.Media != 0 || f.Glow != 0 {
			t.Errorf("face %d bump/media/glow = %#x/%#x/%#x, want zero",
				i, f.Bump, f.Media, f.Glow)
		}
		if !f.Material.IsZero() {
			t.Errorf("face %d has a material set", i)
		}
	}
}

// The exception encoding is what the format exists for, and the
// capture above cannot exercise it: every face of that prim is the
// same, so the face set could be anything and the result would look
// right.  This builds a blob by hand with one face differing to check
// that the bits select the faces they name and the rest keep the
// default.
func TestDecodeTextureEntryException(t *testing.T) {
	const (
		texA = "12b57e577e57c0deefe3b327af5dfe62"
		texB = "1c117e577e57c0deda64e42aeda52a0a"
	)
	// Texture: default A, then face 2 alone set to B.  0x04 is the
	// third bit, so face 2.
	b := teBytes(t, texA+" 04 "+texB+" 00 "+
		// colour, scales, offsets, rotation, bump, media, glow: a
		// default and nothing else.
		"00000000 00 "+
		"0000803f 00 "+
		"0000803f 00 "+
		"0000 00 "+
		"0000 00 "+
		"0000 00 "+
		"00 00 "+
		"00 00 "+
		"00 00")

	faces, err := DecodeTextureEntry(b, 6)
	if err != nil {
		t.Fatal(err)
	}
	for i, f := range faces {
		want := texA
		if i == 2 {
			want = texB
		}
		got := strings.ReplaceAll(f.Texture.String(), "-", "")
		if got != want {
			t.Errorf("face %d texture = %s, want %s", i, got, want)
		}
	}
}

// A face set past the seventh needs a second byte, seven bits at a
// time with the top bit meaning another follows.  A prim with more
// than seven faces is ordinary -- a sculpt or a mesh -- so this is not
// a corner case.
func TestDecodeTextureEntryWideFaceSet(t *testing.T) {
	const (
		texA = "12b57e577e57c0deefe3b327af5dfe62"
		texB = "1c117e577e57c0deda64e42aeda52a0a"
	)
	// 0x80 0x01: first byte has the top bit set so another follows and
	// contributes nothing, second byte contributes bit 0 shifted seven
	// places, which is face 7.
	b := teBytes(t, texA+" 8001 "+texB+" 00 "+
		"00000000 00 0000803f 00 0000803f 00 0000 00 0000 00 0000 00 00 00 00 00 00 00")

	faces, err := DecodeTextureEntry(b, 9)
	if err != nil {
		t.Fatal(err)
	}
	for i, f := range faces {
		want := texA
		if i == 7 {
			want = texB
		}
		got := strings.ReplaceAll(f.Texture.String(), "-", "")
		if got != want {
			t.Errorf("face %d texture = %s, want %s", i, got, want)
		}
	}
}

func TestDecodeTextureEntryRejectsRubbish(t *testing.T) {
	if _, err := DecodeTextureEntry(nil, 6); err == nil {
		t.Error("an empty TextureEntry decoded without complaint")
	}
	if _, err := DecodeTextureEntry([]byte{1, 2, 3}, 6); err == nil {
		t.Error("a three byte TextureEntry decoded without complaint")
	}
}

// TestTheConventionalReadingsOfAFace: the offsets and the rotation
// travel as signed fractions of their range, and the bump byte holds
// three separate things -- so a caller reading the fields raw would have
// a rotation of 8192 and no idea whether the face is fullbright.
//
// The scaling is not something this package has confirmed against a
// simulator: the sections it comes from have only ever been seen holding
// zero.  It is here as the conventional reading and is documented as
// such in texture.go.
func TestTheConventionalReadingsOfAFace(t *testing.T) {
	f := Face{OffsetS: 32767, OffsetT: -32767, Rotation: 16384}
	s, tt := f.OffsetsF()
	if s != 1 || tt != -1 {
		t.Errorf("OffsetsF = %v, %v, want the ends of the range", s, tt)
	}
	if got := f.RotationRad(); got < 3.14 || got > 3.15 {
		t.Errorf("RotationRad = %v, want half a turn", got)
	}

	// Bumpiness in the bottom five bits, fullbright at 0x20, shininess
	// in the top two.
	f = Face{Bump: 0x20 | 0x03}
	if !f.Fullbright() || f.Shiny() != 0 || f.Bumpiness() != 3 {
		t.Errorf("bump %#x reads as fullbright %v, shiny %d, bumpiness %d",
			f.Bump, f.Fullbright(), f.Shiny(), f.Bumpiness())
	}
	f = Face{Bump: 0xc0}
	if f.Fullbright() || f.Shiny() != 3 || f.Bumpiness() != 0 {
		t.Errorf("bump %#x reads as fullbright %v, shiny %d, bumpiness %d",
			f.Bump, f.Fullbright(), f.Shiny(), f.Bumpiness())
	}
}

// TestAFaceCountOfZeroIsGuessedFromWhatIsMentioned: nothing in the blob
// says how many faces the object has, so a caller that does not know
// either is documented to get the highest face any exception names.
//
// It used not to.  textureEntryFaces asked section to fill thirty-two
// faces with the default before any exception was read, and the fill
// called set for every one of them -- so the highest face "mentioned"
// was always the thirty-second, whatever the blob said, and every
// caller passing a count of zero got twenty-six faces the object does
// not have.
//
// Five is the right answer for the box captured above: its exception
// set names faces 0 to 4 and leaves face 5 on the default, which is
// exactly the case the doc comment warns is unreliable.  A guess is
// still a guess -- the blob does not carry the count -- but it is now a
// guess from the evidence rather than from the ceiling.
func TestAFaceCountOfZeroIsGuessedFromWhatIsMentioned(t *testing.T) {
	faces, err := DecodeTextureEntry(teBytes(t, defaultBoxTE), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(faces) != 5 {
		t.Errorf("guessed %d faces from a six faced box, want the 5 it mentions", len(faces))
	}

	// A blob whose first section has no exceptions at all mentions no
	// face, and one face is the smallest answer that is not nonsense.
	n, err := textureEntryFaces(teBytes(t, `
		89556747 24cb43ed 920b47ca ed15465f
		00`))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("guessed %d faces from a blob mentioning none", n)
	}
}

// TestAFaceCountOfZeroStillDecodes: whatever the guess comes out at, a
// caller that does not know how many faces the object has must get
// something back rather than an error -- and a blob too short to guess
// from is a different answer from a blob that mentions nothing.
func TestAFaceCountOfZeroStillDecodes(t *testing.T) {
	faces, err := DecodeTextureEntry(teBytes(t, defaultBoxTE), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(faces) == 0 {
		t.Fatal("guessing the face count came back with no faces at all")
	}
	const plywood = "89556747-24cb-43ed-920b-47caed15465f"
	if got := faces[0].Texture.String(); got != plywood {
		t.Errorf("face 0 texture = %s", got)
	}
}

// TestDecodingRefusesWhatItCannotFinish: the blob is packed by exception
// and every section can run out in the middle, which is not the same as
// a prim whose faces are all on the default -- and a decoder that
// returned what it had would describe a face nothing said anything
// about.
func TestDecodingRefusesWhatItCannotFinish(t *testing.T) {
	if _, err := DecodeTextureEntry(nil, 6); err == nil {
		t.Error("an empty TextureEntry decoded to something")
	}

	// A count of zero on a blob too short to guess from.
	if _, err := DecodeTextureEntry([]byte{1, 2, 3}, 0); err == nil {
		t.Error("a truncated TextureEntry was guessed at")
	}

	// The default of a later section missing, which is where an older
	// simulator's blob genuinely ends -- but this one stops in the
	// colour, which is not optional.
	short := teBytes(t, `
		89556747 24cb43ed 920b47ca ed15465f
		00
		0000`)
	if _, err := DecodeTextureEntry(short, 6); err == nil {
		t.Error("a TextureEntry that stops inside the colour decoded")
	}

	// A face set that never ends: the top bit says another byte
	// follows, and there is not one.
	unterminated := teBytes(t, `
		89556747 24cb43ed 920b47ca ed15465f
		ff`)
	if _, err := DecodeTextureEntry(unterminated, 6); err == nil {
		t.Error("a TextureEntry with an unterminated face set decoded")
	}

	// An exception naming faces with no value after it.
	dangling := teBytes(t, `
		89556747 24cb43ed 920b47ca ed15465f
		1f 8955`)
	if _, err := DecodeTextureEntry(dangling, 6); err == nil {
		t.Error("a TextureEntry whose exception has no value decoded")
	}
}

// TestEverySectionSaysWhichOneRanOut: the blob is eleven sections read
// the same way, and a decoder that reported them all alike would leave
// somebody guessing which property the simulator's bytes stopped in --
// the first eight are not optional and each has to name itself.
func TestEverySectionSaysWhichOneRanOut(t *testing.T) {
	// One section at a time, each blob stopping in the next: a default
	// and a terminating zero is one whole section.
	const (
		texture = `89556747 24cb43ed 920b47ca ed15465f 00`
		colour  = `00000000 00`
		scale   = `0000803f 00`
		offset  = `0000 00`
		one     = `00 00`
	)
	for _, c := range []struct {
		want string
		blob string
	}{
		{"texture", ``},
		{"colour", texture},
		{"scale S", texture + colour},
		{"scale T", texture + colour + scale},
		{"offset S", texture + colour + scale + scale},
		{"offset T", texture + colour + scale + scale + offset},
		{"rotation", texture + colour + scale + scale + offset + offset},
		{"bump", texture + colour + scale + scale + offset + offset + offset},
	} {
		// A byte of the next section's default, which is not enough of
		// it -- an empty tail would be the blob ending cleanly.
		_, err := DecodeTextureEntry(teBytes(t, c.blob+`00`), 6)
		if err == nil {
			t.Errorf("a blob stopping in the %s decoded", c.want)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("a blob stopping in the %s said %v", c.want, err)
		}
	}
}

// TestWhatComesAfterTheBumpByteMayNotBeThere: media, glow and the
// material were added after the format was, so an older simulator's blob
// ends at the bump -- running out there is the end rather than a fault,
// and a decoder that refused it would report every such prim as corrupt.
func TestWhatComesAfterTheBumpByteMayNotBeThere(t *testing.T) {
	const upToBump = `
		89556747 24cb43ed 920b47ca ed15465f 00
		00000000 00
		0000803f 00
		0000803f 00
		0000 00
		0000 00
		0000 00
		00 00`
	faces, err := DecodeTextureEntry(teBytes(t, upToBump), 6)
	if err != nil {
		t.Fatalf("a blob that ends at the bump byte was refused: %v", err)
	}
	if len(faces) != 6 {
		t.Fatalf("got %d faces", len(faces))
	}
	for i, f := range faces {
		if f.Media != 0 || f.Glow != 0 || !f.Material.IsZero() {
			t.Errorf("face %d has what the blob never said: %+v", i, f)
		}
	}
}
