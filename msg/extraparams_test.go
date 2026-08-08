package msg

import (
	"bytes"
	"encoding/binary"
	"math"
	"strings"
	"testing"
)

// The extra parameters are where Linden Lab puts everything a prim
// usually does not have, and they add to the list.  The encoding exists
// so a reader can skip a block it has never heard of -- the length is
// there whether or not the type is -- so the test that matters is that
// an unknown block does not stop the ones after it being read.

// param lays out one block the way the simulator does: a type, a four
// byte length, then that many bytes.
func param(t uint16, data []byte) []byte {
	b := binary.LittleEndian.AppendUint16(nil, t)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(data)))
	return append(b, data...)
}

// params wraps blocks in the count byte that precedes them.
func params(blocks ...[]byte) []byte {
	out := []byte{byte(len(blocks))}
	for _, b := range blocks {
		out = append(out, b...)
	}
	return out
}

// sculptData is the sculpt block: a texture and a shape type.  0x05 is
// a mesh, which is what most of them are now.
func sculptData(tex UUID, kind uint8) []byte {
	return append(append([]byte(nil), tex[:]...), kind)
}

// lightData is the light block: three colour bytes, an intensity byte
// and three floats.
func lightData(r, g, b, intensity uint8, radius, cutoff, falloff float32) []byte {
	out := []byte{r, g, b, intensity}
	for _, f := range []float32{radius, cutoff, falloff} {
		out = binary.LittleEndian.AppendUint32(out, math.Float32bits(f))
	}
	return out
}

// TestDecodeExtraParamsKeepsWhatItDoesNotUnderstand is the whole point
// of the encoding.  A block of a type this build has never seen must be
// kept as bytes and must not cost the blocks after it.
func TestDecodeExtraParamsKeepsWhatItDoesNotUnderstand(t *testing.T) {
	tex := MustParseUUID("17ac7e57-7e57-c0de-1e5d-e125ee89dc71")
	blob := params(
		param(0xabcd, []byte{9, 9, 9}),
		param(ExtraSculpt, sculptData(tex, 5)),
	)

	got, err := DecodeExtraParams(blob)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("read %d blocks, want 2: %+v", len(got), got)
	}
	if got[0].Type != 0xabcd || !bytes.Equal(got[0].Data, []byte{9, 9, 9}) {
		t.Errorf("unknown block = %+v", got[0])
	}
	s, ok := SculptOf(got)
	if !ok {
		t.Fatal("the sculpt block after the unknown one was lost")
	}
	if s.Texture != tex || s.Type != 5 {
		t.Errorf("sculpt = %+v", s)
	}
}

// TestExtraParamNames: these end up in dumps and in the shell's object
// listing, and a type with no name still has to print as something.
func TestExtraParamNames(t *testing.T) {
	for typ, want := range map[uint16]string{
		ExtraFlexible:     "flexible",
		ExtraLight:        "light",
		ExtraSculpt:       "sculpt",
		ExtraLightImage:   "light image",
		ExtraMesh:         "mesh",
		ExtraExtendedMesh: "extended mesh",
		ExtraRenderMat:    "render material",
	} {
		if got := (ExtraParam{Type: typ}).Name(); got != want {
			t.Errorf("type %#x = %q, want %q", typ, got, want)
		}
	}
	if got := (ExtraParam{Type: 0xabcd}).Name(); !strings.Contains(got, "abcd") {
		t.Errorf("an unknown type printed as %q, which does not say which", got)
	}
}

// TestDecodeExtraParamsEmpty: a prim with none of this sends a single
// zero byte, and the field is often absent altogether.
func TestDecodeExtraParamsEmpty(t *testing.T) {
	for _, b := range [][]byte{nil, {}, {0}} {
		got, err := DecodeExtraParams(b)
		if err != nil {
			t.Errorf("%x: %v", b, err)
		}
		if len(got) != 0 {
			t.Errorf("%x decoded to %+v", b, got)
		}
	}
}

// TestDecodeExtraParamsTruncated: these arrive from the network, so a
// block whose length runs off the end has to be an error with whatever
// was read, not a panic and not a silent zero.
func TestDecodeExtraParamsTruncated(t *testing.T) {
	whole := params(
		param(ExtraLight, lightData(255, 128, 0, 255, 10, 0.5, 0.75)),
		param(ExtraSculpt, sculptData(Zero, 5)),
	)
	for n := 1; n < len(whole); n++ {
		got, err := DecodeExtraParams(whole[:n])
		if err == nil {
			t.Errorf("%d of %d bytes decoded to %+v without complaint", n, len(whole), got)
		}
	}
}

// TestLightOf splits the four bytes that colour and intensity share:
// three for the colour and one for how bright, scaled so 255 is full.
func TestLightOf(t *testing.T) {
	blob := params(param(ExtraLight, lightData(255, 128, 0, 255, 10, 0.5, 0.75)))
	got, err := DecodeExtraParams(blob)
	if err != nil {
		t.Fatal(err)
	}
	l, ok := LightOf(got)
	if !ok {
		t.Fatal("the light block was not found")
	}
	if l.Colour != [3]uint8{255, 128, 0} {
		t.Errorf("colour = %v", l.Colour)
	}
	if l.Intensity != 1 {
		t.Errorf("intensity = %v, want 1 for a full 255", l.Intensity)
	}
	if l.Radius != 10 || l.Cutoff != 0.5 || l.Falloff != 0.75 {
		t.Errorf("light = %+v", l)
	}
}

// TestSculptAndLightAreAbsentWhenTheyAre: a prim with neither must
// report so rather than handing back a zero value that reads like a
// black light of radius nothing.
func TestSculptAndLightAreAbsentWhenTheyAre(t *testing.T) {
	only := []ExtraParam{{Type: ExtraFlexible, Data: bytes.Repeat([]byte{1}, 16)}}
	if _, ok := SculptOf(only); ok {
		t.Error("found a sculpt block in a flexible one")
	}
	if _, ok := LightOf(only); ok {
		t.Error("found a light block in a flexible one")
	}

	// A block of the right type but too short to hold the fields is
	// not one either: reading it would take the fields from whatever
	// followed.
	short := []ExtraParam{
		{Type: ExtraSculpt, Data: make([]byte, 16)},
		{Type: ExtraLight, Data: make([]byte, 15)},
	}
	if _, ok := SculptOf(short); ok {
		t.Error("a 16 byte sculpt block has no shape type in it")
	}
	if _, ok := LightOf(short); ok {
		t.Error("a 15 byte light block is missing its last float")
	}
}
