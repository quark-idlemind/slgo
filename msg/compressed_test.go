package msg

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

// A compressed object is a fixed header, a flags word, and then only
// the parts the flags name.  Every optional part is therefore a chance
// to lose the offset, and losing it does not fail loudly -- it reads
// the next field out of the middle of the last one and carries on.
//
// So these tests put the texture entry, which is last, at the end of
// increasingly furnished objects and check it still comes out.  If any
// optional part is skipped by the wrong number of bytes, that is where
// it shows.

type blobWriter struct{ b []byte }

func (w *blobWriter) u8(v uint8)   { w.b = append(w.b, v) }
func (w *blobWriter) i8(v int8)    { w.b = append(w.b, byte(v)) }
func (w *blobWriter) u16(v uint16) { w.b = binary.LittleEndian.AppendUint16(w.b, v) }
func (w *blobWriter) u32(v uint32) { w.b = binary.LittleEndian.AppendUint32(w.b, v) }
func (w *blobWriter) f32(v float32) {
	w.b = binary.LittleEndian.AppendUint32(w.b, math.Float32bits(v))
}
func (w *blobWriter) uuid(u UUID)   { w.b = append(w.b, u[:]...) }
func (w *blobWriter) vec(v Vector3) { w.f32(v.X); w.f32(v.Y); w.f32(v.Z) }
func (w *blobWriter) cstr(s string) { w.b = append(w.b, s...); w.b = append(w.b, 0) }
func (w *blobWriter) raw(b []byte)  { w.b = append(w.b, b...) }

// compressedHead writes the fixed part every blob begins with, up to
// and including the flags word that says what follows it.  It is
// separate from build because a test that wants a blob to STOP
// somewhere cannot have the shape and texture entry appended to it.
func compressedHead(flags uint32) *blobWriter {
	w := &blobWriter{}
	w.uuid(UUID{1, 2, 3})      // FullID
	w.u32(4242)                // LocalID
	w.u8(9)                    // PCode, a prim
	w.u8(0)                    // State
	w.u32(0xdeadbeef)          // CRC
	w.u8(3)                    // Material
	w.u8(0)                    // ClickAction
	w.vec(Vector3{1, 2, 3})    // Scale
	w.vec(Vector3{10, 20, 30}) // Position
	w.f32(0)                   // Rotation, three floats
	w.f32(0)
	w.f32(0)
	w.u32(flags)
	w.uuid(UUID{9, 9, 9}) // Owner
	return w
}

// build makes a compressed blob with whatever the flags ask for.
func build(flags uint32, fill func(w *blobWriter), te []byte) []byte {
	w := compressedHead(flags)

	if fill != nil {
		fill(w)
	}

	// Shape: eighteen fields, whose sizes are as easy to get wrong as
	// anything optional -- and whose ORDER is this message's own.  The
	// profile curve comes last of the curves here, after every path
	// field, where the other three messages put it second.
	//
	// Every field gets a value of its own so that reading them in the
	// wrong order fails here rather than in a region.  Zeros hid the
	// real thing: a stream of them reads the same however it is
	// misaligned, and a plain box came back from Second Life as a
	// cylinder before this fixture could say so.
	w.u8(16)      // PathCurve
	w.u16(0x0102) // PathBegin
	w.u16(0x0304) // PathEnd
	w.u8(100)     // PathScaleX
	w.u8(99)      // PathScaleY
	w.u8(98)      // PathShearX
	w.u8(97)      // PathShearY
	w.i8(-5)      // PathTwist
	w.i8(-6)      // PathTwistBegin
	w.i8(-7)      // PathRadiusOffset
	w.i8(-8)      // PathTaperX
	w.i8(-9)      // PathTaperY
	w.u8(96)      // PathRevolutions
	w.i8(-10)     // PathSkew
	w.u8(1)       // ProfileCurve
	w.u16(0x0506) // ProfileBegin
	w.u16(0x0708) // ProfileEnd
	w.u16(0x090a) // ProfileHollow

	w.u32(uint32(len(te)))
	w.raw(te)
	return w.b
}

// marker is a recognisable texture entry: if the reader has lost its
// place, what comes back will not be this.
var marker = []byte("TEXTURE-ENTRY-MARKER")

// wantShape is what build wrote, field by field.
var wantShape = PrimShape{
	PathCurve: 16,
	PathBegin: 0x0102, PathEnd: 0x0304,
	PathScaleX: 100, PathScaleY: 99,
	PathShearX: 98, PathShearY: 97,
	PathTwist: -5, PathTwistBegin: -6, PathRadiusOffset: -7,
	PathTaperX: -8, PathTaperY: -9,
	PathRevolutions: 96, PathSkew: -10,
	ProfileCurve: 1,
	ProfileBegin: 0x0506, ProfileEnd: 0x0708, ProfileHollow: 0x090a,
}

func checkTail(t *testing.T, c *Compressed, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if c.LocalID != 4242 {
		t.Errorf("LocalID = %d, want 4242", c.LocalID)
	}
	if c.Position != (Vector3{10, 20, 30}) {
		t.Errorf("Position = %v, want {10 20 30}", c.Position)
	}
	if c.Shape != wantShape {
		t.Errorf("shape misread:\n\t%+v\nwant\n\t%+v", c.Shape, wantShape)
	}
	if !bytes.Equal(c.TextureEntry, marker) {
		t.Errorf("TextureEntry = %q, want %q -- the reader lost its place",
			c.TextureEntry, marker)
	}
}

func TestDecodeCompressedPlain(t *testing.T) {
	c, err := DecodeCompressed(build(0, func(w *blobWriter) {
		w.u8(0) // no extra params
	}, marker))
	checkTail(t, c, err)
	if c.ParentID != nil || c.Sound != nil || c.Text != "" {
		t.Error("a plain object came back with optional parts set")
	}
}

func TestDecodeCompressedEverything(t *testing.T) {
	flags := uint32(compAngularVelocity | compParentID | compText |
		compMediaURL | compParticles | compSound | compNameValues | compTextureAnim)

	// The texture animation trails the texture entry, so it is
	// appended after build has finished rather than inside it.
	b := build(flags, func(w *blobWriter) {
		w.vec(Vector3{0.5, 0, 0})
		w.u32(777)
		w.cstr("hover text")
		w.u8(1)
		w.u8(2)
		w.u8(3)
		w.u8(4)
		w.cstr("http://example.invalid/")
		w.raw(make([]byte, 86))
		w.u8(1)
		w.u16(ExtraSculpt)
		w.u32(17)
		w.raw(make([]byte, 17))
		w.uuid(UUID{7})
		w.f32(0.75)
		w.u8(2)
		w.f32(20)
		w.cstr("AttachItemID STRING RW DS 00000000-0000-0000-0000-000000000000")
	}, marker)
	b = binary.LittleEndian.AppendUint32(b, 4)
	b = append(b, 1, 2, 3, 4)

	c, err := DecodeCompressed(b)
	checkTail(t, c, err)

	if c.AngularVelocity == nil || c.AngularVelocity.X != 0.5 {
		t.Errorf("angular velocity = %v", c.AngularVelocity)
	}
	if c.ParentID == nil || *c.ParentID != 777 {
		t.Errorf("parent = %v, want 777", c.ParentID)
	}
	if c.Text != "hover text" {
		t.Errorf("text = %q", c.Text)
	}
	if c.TextColor != [4]uint8{1, 2, 3, 4} {
		t.Errorf("text colour = %v", c.TextColor)
	}
	if c.MediaURL != "http://example.invalid/" {
		t.Errorf("media url = %q", c.MediaURL)
	}
	if len(c.Particles) != 86 {
		t.Errorf("particles = %d bytes, want 86", len(c.Particles))
	}
	if len(c.ExtraParams) != 1 || c.ExtraParams[0].Type != ExtraSculpt {
		t.Errorf("extra params = %+v", c.ExtraParams)
	}
	if c.Sound == nil || c.Sound.Gain != 0.75 || c.Sound.Radius != 20 {
		t.Errorf("sound = %+v", c.Sound)
	}
	if c.NameValues == "" {
		t.Error("name values missing")
	}
	if len(c.TextureAnim) != 4 {
		t.Errorf("texture animation = %d bytes, want 4", len(c.TextureAnim))
	}
}

// A tree takes a byte where a scratchpad takes a length and a payload,
// and the flags for them are different bits that share a branch.
func TestDecodeCompressedTreeAndScratchpad(t *testing.T) {
	c, err := DecodeCompressed(build(compTree, func(w *blobWriter) {
		w.u8(5) // species
		w.u8(0) // no extra params
	}, marker))
	checkTail(t, c, err)
	if c.Tree == nil || *c.Tree != 5 {
		t.Errorf("tree = %v, want 5", c.Tree)
	}

	c, err = DecodeCompressed(build(compScratchpad, func(w *blobWriter) {
		w.u32(6)
		w.raw([]byte("ignore"))
		w.u8(0)
	}, marker))
	checkTail(t, c, err)
	if c.Tree != nil {
		t.Error("a scratchpad was read as a tree")
	}
}

// A blob cut short must produce an error and whatever was read, not a
// panic: these arrive from the network.
func TestDecodeCompressedTruncated(t *testing.T) {
	full := build(0, func(w *blobWriter) { w.u8(0) }, marker)
	for n := range len(full) {
		c, err := DecodeCompressed(full[:n])
		if err == nil {
			t.Fatalf("%d bytes of a %d byte object decoded without complaint", n, len(full))
		}
		_ = c
	}
}

// TestUnterminatedTextIsAnError: the hover text is a C string, and a
// blob whose text runs to the end of the buffer with no terminator has
// to stop the read rather than take everything after it as text.
func TestUnterminatedTextIsAnError(t *testing.T) {
	// Nothing follows the text, so there is no zero byte anywhere for
	// the scan to stop on -- which is the case a blob cut short in
	// transit presents.
	w := compressedHead(compText | compMediaURL)
	w.raw([]byte("text with no terminator"))

	c, err := DecodeCompressed(w.b)
	if err == nil {
		t.Fatalf("an unterminated string decoded to %+v", c)
	}
	// Everything after the failure reads as absent rather than as
	// whatever happened to be in the buffer.
	if c.MediaURL != "" || len(c.ExtraParams) != 0 {
		t.Errorf("reads after the failure produced values: %q %+v", c.MediaURL, c.ExtraParams)
	}
}

// TestScratchpadLengthIsChecked: the scratchpad is skipped rather than
// read, and a length longer than the blob would otherwise move the
// cursor past the end and misread everything after it.
func TestScratchpadLengthIsChecked(t *testing.T) {
	b := build(compScratchpad, func(w *blobWriter) {
		w.u32(1 << 20) // a megabyte that is not there
		w.u8(0)
	}, marker)

	if c, err := DecodeCompressed(b); err == nil {
		t.Fatalf("a scratchpad longer than the blob decoded to %+v", c)
	}
}

// TestExtraParamsLengthIsChecked, for the same reason: the block says
// how long it is, and that length is the reader's only way past a type
// it does not understand.
func TestExtraParamsLengthIsChecked(t *testing.T) {
	b := build(0, func(w *blobWriter) {
		w.u8(1)        // one block
		w.u16(0xabcd)  // of a type nobody knows
		w.u32(1 << 20) // and a length that is not there
	}, marker)

	if c, err := DecodeCompressed(b); err == nil {
		t.Fatalf("decoded to %+v", c)
	}
}

// TestIsAttachment is what tells a worn object from one lying on the
// ground: both have a parent, and only a worn one has a State saying
// which attachment point it is on.
func TestIsAttachment(t *testing.T) {
	parent := uint32(4242)
	cases := []struct {
		what string
		c    Compressed
		want bool
	}{
		{"worn on the right hand", Compressed{State: 6, ParentID: &parent}, true},
		{"sitting on a prim", Compressed{State: 0, ParentID: &parent}, false},
		{"loose in the world", Compressed{State: 6}, false},
	}
	for _, c := range cases {
		if got := c.c.IsAttachment(); got != c.want {
			t.Errorf("%s: IsAttachment = %v, want %v", c.what, got, c.want)
		}
	}
}

// TestF32AtStopsAtTheEnd.  Nothing in the light block can reach this --
// LightOf checks the length first -- but f32at is the only float reader
// in this file that does not go through the cursor, and a caller added
// later would find out the hard way.
func TestF32AtStopsAtTheEnd(t *testing.T) {
	b := []byte{0, 0, 0x80, 0x3f, 1, 2, 3}
	if got := f32at(b, 0); got != 1 {
		t.Errorf("f32at(0) = %v, want 1", got)
	}
	if got := f32at(b, 4); got != 0 {
		t.Errorf("f32at past the end = %v, want 0", got)
	}
}
