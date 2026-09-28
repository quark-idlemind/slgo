package sl

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/quark-idlemind/slgo/msg"
)

// TextureEntry is how an object's appearance travels: one blob in
// ObjectUpdate carrying every face's texture, tint, mapping and
// material.
//
// It is packed by exception rather than per face.  Each property is a
// section holding a default value, then pairs of (which faces, value)
// for the faces that differ, ended by a zero.  A prim whose faces all
// match is a short blob; a prim with one odd face costs one more entry
// in one section rather than a whole extra face.
//
// The sections always appear in this order, and each is read the same
// way, only the width of the value changing:
//
//	texture     16 bytes, a UUID
//	colour       4 bytes, RGBA, stored inverted
//	scale S      4 bytes, float
//	scale T      4 bytes, float
//	offset S     2 bytes, signed
//	offset T     2 bytes, signed
//	rotation     2 bytes, signed
//	bump         1 byte, bump, shininess and fullbright packed together
//	media        1 byte
//	glow         1 byte
//	material    16 bytes, a UUID
//
// The face set is a variable length integer, seven bits to a byte with
// the top bit meaning another follows, so faces past the seventh cost
// a second byte.

// Face is the appearance of one face of an object.
type Face struct {
	Texture msg.UUID

	// Colour is RGBA as it is meant, not as it travels: the wire form
	// is stored inverted and is undone here.
	Colour [4]uint8

	ScaleS, ScaleT float32

	// OffsetS, OffsetT and Rotation are as they appear on the wire.
	// The conventional readings are the helpers below.
	OffsetS, OffsetT int16
	Rotation         int16

	// Bump holds bumpiness, shininess and the fullbright flag
	// together; Media and Glow are a byte each.
	Bump  uint8
	Media uint8
	Glow  uint8

	Material msg.UUID
}

// Offsets and rotation are signed fractions of their range, read as the
// viewer reads them: offsets over 0x7fff, and rotation over 0x8000 of a
// whole turn (llprimitive.cpp:1464-1466).  The sections they come from
// have only been seen holding zero from a simulator.
func (f Face) OffsetsF() (float32, float32) {
	return float32(f.OffsetS) / 32767, float32(f.OffsetT) / 32767
}

// RotationRad reads the rotation as radians.
func (f Face) RotationRad() float32 {
	return float32(f.Rotation) / 32768 * 2 * math.Pi
}

// Fullbright reports whether the face ignores lighting.
func (f Face) Fullbright() bool { return f.Bump&0x20 != 0 }

// Shiny and Bumpiness are the other two things packed into that byte.
func (f Face) Shiny() uint8     { return (f.Bump >> 6) & 0x03 }
func (f Face) Bumpiness() uint8 { return f.Bump & 0x1f }

// DecodeTextureEntry unpacks the appearance of each face.
//
// Faces says how many the object has -- six for a box, and there is
// nothing in the blob itself that says.  A count of zero takes the
// highest face mentioned, which is right only if the last face is not
// one of the ones left on the default.
func DecodeTextureEntry(b []byte, faces int) ([]Face, error) {
	if len(b) == 0 {
		return nil, fmt.Errorf("sl: empty TextureEntry")
	}
	if faces <= 0 {
		n, err := textureEntryFaces(b)
		if err != nil {
			return nil, err
		}
		faces = n
	}

	out := make([]Face, faces)
	r := &teReader{b: b}

	if err := r.section(16, faces, func(i int, v []byte) {
		copy(out[i].Texture[:], v)
	}); err != nil {
		return nil, fmt.Errorf("sl: TextureEntry texture: %w", err)
	}
	if err := r.section(4, faces, func(i int, v []byte) {
		// Stored inverted: an opaque white face travels as four zero
		// bytes.
		out[i].Colour = [4]uint8{255 - v[0], 255 - v[1], 255 - v[2], 255 - v[3]}
	}); err != nil {
		return nil, fmt.Errorf("sl: TextureEntry colour: %w", err)
	}
	if err := r.section(4, faces, func(i int, v []byte) {
		out[i].ScaleS = f32le(v)
	}); err != nil {
		return nil, fmt.Errorf("sl: TextureEntry scale S: %w", err)
	}
	if err := r.section(4, faces, func(i int, v []byte) {
		out[i].ScaleT = f32le(v)
	}); err != nil {
		return nil, fmt.Errorf("sl: TextureEntry scale T: %w", err)
	}
	if err := r.section(2, faces, func(i int, v []byte) {
		out[i].OffsetS = int16(binary.LittleEndian.Uint16(v))
	}); err != nil {
		return nil, fmt.Errorf("sl: TextureEntry offset S: %w", err)
	}
	if err := r.section(2, faces, func(i int, v []byte) {
		out[i].OffsetT = int16(binary.LittleEndian.Uint16(v))
	}); err != nil {
		return nil, fmt.Errorf("sl: TextureEntry offset T: %w", err)
	}
	if err := r.section(2, faces, func(i int, v []byte) {
		out[i].Rotation = int16(binary.LittleEndian.Uint16(v))
	}); err != nil {
		return nil, fmt.Errorf("sl: TextureEntry rotation: %w", err)
	}
	if err := r.section(1, faces, func(i int, v []byte) {
		out[i].Bump = v[0]
	}); err != nil {
		return nil, fmt.Errorf("sl: TextureEntry bump: %w", err)
	}
	// What follows may be absent on an older simulator, so running out
	// of bytes here is the end rather than a fault.
	_ = r.section(1, faces, func(i int, v []byte) { out[i].Media = v[0] })
	_ = r.section(1, faces, func(i int, v []byte) { out[i].Glow = v[0] })
	_ = r.section(16, faces, func(i int, v []byte) { copy(out[i].Material[:], v) })

	return out, nil
}

// textureEntryFaces guesses the face count from the sets mentioned.
//
// Only the exception sets count.  The default at the head of a section
// stands for every face there could be, so a guess that let the default
// speak answered thirty-two every time -- which it did, and which gave
// every caller passing a count of zero twenty-six faces the object does
// not have.
func textureEntryFaces(b []byte) (int, error) {
	r := &teReader{b: b}
	high := 0
	err := r.exceptions(16, func(bits uint64, _ []byte) {
		for i := range 32 {
			if bits&(1<<uint(i)) != 0 && i+1 > high {
				high = i + 1
			}
		}
	})
	if err != nil {
		return 0, err
	}
	if high == 0 {
		// Every face is the default, so nothing in the blob says how
		// many there are.  One is the smallest answer that is not
		// nonsense, and it is the right appearance for any of them.
		high = 1
	}
	return high, nil
}

type teReader struct {
	b []byte
	i int
}

// section reads one property: a default, then exceptions, then a zero.
func (r *teReader) section(width, faces int, set func(i int, v []byte)) error {
	if r.i+width > len(r.b) {
		return fmt.Errorf("want %d bytes for the default, %d left", width, len(r.b)-r.i)
	}
	// Taken before exceptions walks past it.  It stays valid: the slice
	// is a view of b and nothing here writes to it.
	def := r.b[r.i : r.i+width]
	for i := range faces {
		set(i, def)
	}

	return r.exceptions(width, func(bits uint64, v []byte) {
		for i := range faces {
			if bits&(1<<uint(i)) != 0 {
				set(i, v)
			}
		}
	})
}

// exceptions walks one property's face sets, from the default it skips
// past to the zero that ends them, and hands each set to each.
//
// It is separate from section because counting the faces an object has
// must not let the default speak: the default covers every face there
// could be, and a count taken from it is a count of the maximum rather
// than of the object.
func (r *teReader) exceptions(width int, each func(bits uint64, v []byte)) error {
	if r.i+width > len(r.b) {
		return fmt.Errorf("want %d bytes for the default, %d left", width, len(r.b)-r.i)
	}
	r.i += width

	for {
		bits, n, ok := r.varint()
		if !ok {
			return fmt.Errorf("truncated face set")
		}
		r.i += n
		if bits == 0 {
			return nil
		}
		if r.i+width > len(r.b) {
			return fmt.Errorf("want %d bytes for an exception, %d left", width, len(r.b)-r.i)
		}
		v := r.b[r.i : r.i+width]
		r.i += width
		each(bits, v)
	}
}

// varint reads the face set: seven bits a byte, top bit meaning
// another follows.
func (r *teReader) varint() (uint64, int, bool) {
	var v uint64
	var shift uint
	for n := 0; r.i+n < len(r.b); n++ {
		c := r.b[r.i+n]
		v |= uint64(c&0x7f) << shift
		shift += 7
		if c&0x80 == 0 {
			return v, n + 1, true
		}
	}
	return 0, 0, false
}

func f32le(b []byte) float32 {
	return math.Float32frombits(binary.LittleEndian.Uint32(b))
}
