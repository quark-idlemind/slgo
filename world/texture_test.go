package world

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
