package msg

import (
	"encoding/binary"
	"fmt"
	"math"
)

// ObjectUpdateCompressed is how the simulator describes most objects
// most of the time.
//
// It carries one opaque blob per object rather than a block of typed
// fields, and the blob is laid out by what the object actually has:
// a fixed part, then a flags word, then only the parts the flags name.
// An object with no text, no sound, no particles and no parent -- the
// ordinary case -- skips all of it.
//
// Not decoding this leaves a session blind to nearly everything.  A
// full ObjectUpdate arrives when an object is first created near us;
// after that, changes come this way, which is why a texture set on a
// prim never showed up in the ObjectUpdate that had been captured for
// it.

// Flags in the compressed blob, from the viewer's llviewerobject.h.
const (
	compScratchpad      = 0x01
	compTree            = 0x02
	compText            = 0x04
	compParticles       = 0x08
	compSound           = 0x10
	compParentID        = 0x20
	compTextureAnim     = 0x40
	compAngularVelocity = 0x80
	compNameValues      = 0x100
	compMediaURL        = 0x200
)

// Compressed is one object out of an ObjectUpdateCompressed.
//
// The fields that are only there when a flag says so are pointers or
// slices, so "absent" and "zero" are different: an object with no text
// is not an object whose text is empty.
type Compressed struct {
	FullID   UUID
	LocalID  uint32
	PCode    uint8
	State    uint8
	CRC      uint32
	Material uint8
	Click    uint8

	Scale    Vector3
	Position Vector3
	Rotation Quaternion

	Flags uint32
	Owner UUID

	AngularVelocity *Vector3
	ParentID        *uint32

	// Tree is the species of a linden tree or grass, when the object
	// is one.
	Tree *uint8

	// Text is the floating text above an object, and TextColor its
	// colour as the four bytes arrived: red, green, blue, and the alpha
	// subtracted from 255, which the simulator sends that way so that
	// opaque text, the usual kind, zero encodes
	// (llviewerobject.cpp:1532-1533 and 1872).  TextRGBA is the colour
	// with the alpha the right way up.
	Text      string
	TextColor [4]uint8

	MediaURL string

	// Particles and TextureAnim are kept as bytes: they are settings
	// for a renderer this has none of, and nothing here would do
	// anything with them.
	Particles   []byte
	TextureAnim []byte

	Sound       *CompressedSound
	NameValues  string
	ExtraParams []ExtraParam

	// Shape is the prim's profile and path, the numbers that make it a
	// box or a cylinder or a torus.
	Shape PrimShape

	TextureEntry []byte
}

// CompressedSound is a sound an object is playing.
type CompressedSound struct {
	Sound  UUID
	Gain   float32
	Flags  uint8
	Radius float32
}

// PrimShape is what gives a prim its form.
type PrimShape struct {
	PathCurve        uint8
	ProfileCurve     uint8
	PathBegin        uint16
	PathEnd          uint16
	PathScaleX       uint8
	PathScaleY       uint8
	PathShearX       uint8
	PathShearY       uint8
	PathTwist        int8
	PathTwistBegin   int8
	PathRadiusOffset int8
	PathTaperX       int8
	PathTaperY       int8
	PathRevolutions  uint8
	PathSkew         int8
	ProfileBegin     uint16
	ProfileEnd       uint16
	ProfileHollow    uint16
}

// TextRGBA is the floating text's colour with the alpha the right way
// up.  TextColor holds it as sent, with the alpha flipped.
func (c *Compressed) TextRGBA() [4]byte { return textRGBA(c.TextColor) }

// TextRGBA is the floating text's colour with the alpha the right way
// up.  TextColor holds the bytes as sent, and the simulator sends the
// alpha subtracted from 255, in this message as in the compressed one
// (llviewerobject.cpp:1532-1533).
func (d *ObjectUpdate_ObjectData) TextRGBA() [4]byte { return textRGBA(d.TextColor) }

func textRGBA(sent [4]byte) [4]byte {
	sent[3] = 255 - sent[3]
	return sent
}

// IsAttachment reports whether this object is worn.
func (c *Compressed) IsAttachment() bool { return c.State != 0 && c.ParentID != nil }

// DecodeCompressed reads one object out of an ObjectUpdateCompressed
// block.
func DecodeCompressed(b []byte) (*Compressed, error) {
	r := &blobReader{b: b}
	c := &Compressed{}

	c.FullID = r.uuid()
	c.LocalID = r.u32()
	c.PCode = r.u8()
	c.State = r.u8()
	c.CRC = r.u32()
	c.Material = r.u8()
	c.Click = r.u8()
	c.Scale = r.vec3()
	c.Position = r.vec3()
	c.Rotation = r.quat()
	c.Flags = r.u32()
	c.Owner = r.uuid()
	if r.err != nil {
		return nil, fmt.Errorf("msg: compressed object header: %w", r.err)
	}

	if c.Flags&compAngularVelocity != 0 {
		v := r.vec3()
		c.AngularVelocity = &v
	}
	if c.Flags&compParentID != 0 {
		p := r.u32()
		c.ParentID = &p
	}
	if c.Flags&compTree != 0 {
		t := r.u8()
		c.Tree = &t
	} else if c.Flags&compScratchpad != 0 {
		// A scratchpad is a length and that many bytes, and is not
		// used by anything current.  Skipping it correctly still
		// matters: everything after it would be misread otherwise.
		n := r.u32()
		r.skip(int(n))
	}
	if c.Flags&compText != 0 {
		c.Text = r.cstring()
		c.TextColor = [4]uint8{r.u8(), r.u8(), r.u8(), r.u8()}
	}
	if c.Flags&compMediaURL != 0 {
		c.MediaURL = r.cstring()
	}
	if c.Flags&compParticles != 0 {
		c.Particles = r.take(86)
	}

	c.ExtraParams = r.extraParams()

	if c.Flags&compSound != 0 {
		s := CompressedSound{Sound: r.uuid(), Gain: r.f32(), Flags: r.u8(), Radius: r.f32()}
		c.Sound = &s
	}
	if c.Flags&compNameValues != 0 {
		c.NameValues = r.cstring()
	}

	// The shape, in this message's own order, which is not the order
	// the other three use: here the profile curve comes after all the
	// path fields rather than beside the path curve.
	//
	// Reading it in the familiar order costs a byte's alignment on
	// everything from PathBegin on, and the result still looks like a
	// prim: a plain box came back as a cylinder with a skew, which is
	// how this was found.  So the fields are listed one to a line and
	// in the order the bytes arrive, rather than in the order the
	// struct happens to declare them.
	c.Shape.PathCurve = r.u8()
	c.Shape.PathBegin, c.Shape.PathEnd = r.u16(), r.u16()
	c.Shape.PathScaleX, c.Shape.PathScaleY = r.u8(), r.u8()
	c.Shape.PathShearX, c.Shape.PathShearY = r.u8(), r.u8()
	c.Shape.PathTwist, c.Shape.PathTwistBegin = r.i8(), r.i8()
	c.Shape.PathRadiusOffset = r.i8()
	c.Shape.PathTaperX, c.Shape.PathTaperY = r.i8(), r.i8()
	c.Shape.PathRevolutions, c.Shape.PathSkew = r.u8(), r.i8()
	c.Shape.ProfileCurve = r.u8()
	c.Shape.ProfileBegin, c.Shape.ProfileEnd = r.u16(), r.u16()
	c.Shape.ProfileHollow = r.u16()

	// The texture entry is length prefixed here, where in ObjectUpdate
	// it is a field of the message and the framing does it.
	n := r.u32()
	c.TextureEntry = r.take(int(n))

	if c.Flags&compTextureAnim != 0 {
		n := r.u32()
		c.TextureAnim = r.take(int(n))
	}

	if r.err != nil {
		return c, fmt.Errorf("msg: compressed object %s: %w", c.FullID, r.err)
	}
	return c, nil
}

// blobReader walks a packed blob, remembering the first thing that
// went wrong.
//
// Reading past the end returns a zero rather than panicking, and every
// later read is a zero too, so a truncated blob produces a partial
// object and one error instead of a crash.  These arrive from the
// network and a malformed one must not take the session down.
type blobReader struct {
	b   []byte
	i   int
	err error
}

func (r *blobReader) need(n int) bool {
	if r.err != nil {
		return false
	}
	if r.i+n > len(r.b) {
		r.err = fmt.Errorf("wanted %d bytes at offset %d, only %d left", n, r.i, len(r.b)-r.i)
		return false
	}
	return true
}

func (r *blobReader) u8() uint8 {
	if !r.need(1) {
		return 0
	}
	v := r.b[r.i]
	r.i++
	return v
}

func (r *blobReader) i8() int8 { return int8(r.u8()) }

func (r *blobReader) u16() uint16 {
	if !r.need(2) {
		return 0
	}
	v := binary.LittleEndian.Uint16(r.b[r.i:])
	r.i += 2
	return v
}

func (r *blobReader) u32() uint32 {
	if !r.need(4) {
		return 0
	}
	v := binary.LittleEndian.Uint32(r.b[r.i:])
	r.i += 4
	return v
}

func (r *blobReader) f32() float32 {
	return math.Float32frombits(r.u32())
}

func (r *blobReader) uuid() UUID {
	var u UUID
	if !r.need(16) {
		return u
	}
	copy(u[:], r.b[r.i:])
	r.i += 16
	return u
}

func (r *blobReader) vec3() Vector3 {
	return Vector3{X: r.f32(), Y: r.f32(), Z: r.f32()}
}

// quat reads the three float wire form; the fourth component is
// recovered by normalising and is not sent.
func (r *blobReader) quat() Quaternion {
	return Quaternion{X: r.f32(), Y: r.f32(), Z: r.f32()}
}

func (r *blobReader) take(n int) []byte {
	if n < 0 || !r.need(n) {
		return nil
	}
	out := make([]byte, n)
	copy(out, r.b[r.i:r.i+n])
	r.i += n
	return out
}

func (r *blobReader) skip(n int) {
	if n < 0 || !r.need(n) {
		return
	}
	r.i += n
}

// cstring reads up to and including a zero byte.
func (r *blobReader) cstring() string {
	if r.err != nil {
		return ""
	}
	for j := r.i; j < len(r.b); j++ {
		if r.b[j] == 0 {
			s := string(r.b[r.i:j])
			r.i = j + 1
			return s
		}
	}
	r.err = fmt.Errorf("unterminated string at offset %d", r.i)
	return ""
}

// extraParams reads the optional blocks in place.
func (r *blobReader) extraParams() []ExtraParam {
	if r.err != nil {
		return nil
	}
	n := int(r.u8())
	out := make([]ExtraParam, 0, n)
	for range n {
		t := r.u16()
		size := r.u32()
		d := r.take(int(size))
		if r.err != nil {
			return out
		}
		out = append(out, ExtraParam{Type: t, Data: d})
	}
	return out
}

func f32at(b []byte, i int) float32 {
	if i+4 > len(b) {
		return 0
	}
	return math.Float32frombits(binary.LittleEndian.Uint32(b[i:]))
}
