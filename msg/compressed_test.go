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
func (w *blobWriter) u16(v uint16) { w.b = binary.LittleEndian.AppendUint16(w.b, v) }
func (w *blobWriter) u32(v uint32) { w.b = binary.LittleEndian.AppendUint32(w.b, v) }
func (w *blobWriter) f32(v float32) {
	w.b = binary.LittleEndian.AppendUint32(w.b, math.Float32bits(v))
}
func (w *blobWriter) uuid(u UUID)   { w.b = append(w.b, u[:]...) }
func (w *blobWriter) vec(v Vector3) { w.f32(v.X); w.f32(v.Y); w.f32(v.Z) }
func (w *blobWriter) cstr(s string) { w.b = append(w.b, s...); w.b = append(w.b, 0) }
func (w *blobWriter) raw(b []byte)  { w.b = append(w.b, b...) }

// build makes a compressed blob with whatever the flags ask for.
func build(flags uint32, fill func(w *blobWriter), te []byte) []byte {
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

	if fill != nil {
		fill(w)
	}

	// Shape: eighteen fields, whose sizes are as easy to get wrong as
	// anything optional.
	w.u8(16)  // PathCurve
	w.u8(1)   // ProfileCurve
	w.u16(0)  // PathBegin
	w.u16(0)  // PathEnd
	w.u8(100) // PathScaleX
	w.u8(100) // PathScaleY
	w.u8(0)   // PathShearX
	w.u8(0)   // PathShearY
	w.u8(0)   // PathTwist
	w.u8(0)   // PathTwistBegin
	w.u8(0)   // PathRadiusOffset
	w.u8(0)   // PathTaperX
	w.u8(0)   // PathTaperY
	w.u8(0)   // PathRevolutions
	w.u8(0)   // PathSkew
	w.u16(0)  // ProfileBegin
	w.u16(0)  // ProfileEnd
	w.u16(0)  // ProfileHollow

	w.u32(uint32(len(te)))
	w.raw(te)
	return w.b
}

// marker is a recognisable texture entry: if the reader has lost its
// place, what comes back will not be this.
var marker = []byte("TEXTURE-ENTRY-MARKER")

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
	if c.Shape.PathCurve != 16 || c.Shape.ProfileCurve != 1 || c.Shape.PathScaleX != 100 {
		t.Errorf("shape misread: %+v", c.Shape)
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
