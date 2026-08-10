package sl

// Setting what an object's faces look like.
//
// The read half is in texture.go; this is the other direction.  One
// message carries the whole appearance -- ObjectImage, with a
// TextureEntry blob -- so a face cannot be changed on its own: what
// goes out is every face, and anything not being changed has to be
// what it already was.  That is what SetFaces insists on and why
// SetFace reads the object first.

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// The bits packed into a face's Bump byte.
const (
	Bumpiness  = 0x1f // the bump map, 0 to 31
	Fullbright = 0x20 // ignore lighting
	shinyShift = 6    // shininess is the top two bits
)

// Shininess, as the build floater offers it.
const (
	ShinyNone = iota
	ShinyLow
	ShinyMedium
	ShinyHigh
)

// AllFaces is what SetFace takes to mean every face at once.
const AllFaces = -1

// SetFullbright turns lighting off or on for a face, leaving the bump
// and shininess in that byte alone.
func (f *Face) SetFullbright(on bool) {
	if on {
		f.Bump |= Fullbright
		return
	}
	f.Bump &^= Fullbright
}

// SetShiny sets shininess, ShinyNone to ShinyHigh.
func (f *Face) SetShiny(level uint8) {
	f.Bump = f.Bump&^(0x03<<shinyShift) | (level&0x03)<<shinyShift
}

// SetBumpiness sets the bump map.
func (f *Face) SetBumpiness(b uint8) { f.Bump = f.Bump&^Bumpiness | b&Bumpiness }

// SetAlpha sets how opaque a face is, 0 clear and 255 solid.
//
// It is the fourth component of the colour rather than a field of its
// own, which is worth saying because the wire stores the whole colour
// inverted and a face that has never been touched is four zero bytes:
// opaque white.
func (f *Face) SetAlpha(a uint8) { f.Colour[3] = a }

// SetColour tints a face, keeping its alpha.
func (f *Face) SetColour(r, g, b uint8) {
	f.Colour[0], f.Colour[1], f.Colour[2] = r, g, b
}

// SetRepeats is how many times the texture is tiled across a face.
func (f *Face) SetRepeats(s, t float32) { f.ScaleS, f.ScaleT = s, t }

// SetOffsets moves the texture on the face, each -1 to 1.
//
// The wire keeps these as signed fractions of the range, so what goes
// out is quantised to about one part in thirty thousand.
func (f *Face) SetOffsets(s, t float32) {
	f.OffsetS, f.OffsetT = fraction16(s), fraction16(t)
}

// SetRotationRad turns the texture on the face.
func (f *Face) SetRotationRad(rad float32) {
	turns := float64(rad) / (2 * math.Pi)
	f.Rotation = int16(clamp(math.Round(turns*32768), -32768, 32767))
}

func fraction16(f float32) int16 {
	return int16(clamp(math.Round(float64(f)*32767), -32768, 32767))
}

// EncodeTextureEntry packs faces back into the blob an object update
// carries and ObjectImage sets.
//
// Each property is written as the value most faces share, then a face
// set and a value for each group that differs.  Choosing the commonest
// as the default rather than the first face's is what keeps a prim with
// one odd face down to one exception instead of five.
func EncodeTextureEntry(faces []Face) ([]byte, error) {
	if len(faces) == 0 {
		return nil, fmt.Errorf("sl: a TextureEntry needs at least one face")
	}
	if len(faces) > 64 {
		// The face set is a bitfield and everything reading one holds
		// it in 64 bits, this package included.
		return nil, fmt.Errorf("sl: %d faces is more than a TextureEntry can carry", len(faces))
	}

	var out []byte
	section := func(width int, value func(f Face) []byte) {
		vals := make([]string, len(faces))
		for i, f := range faces {
			vals[i] = string(value(f))
		}
		out = append(out, commonest(vals)...)
		for _, group := range groups(vals) {
			out = appendFaceSet(out, group.bits)
			out = append(out, group.value...)
		}
		out = append(out, 0) // the end of this property's exceptions
	}

	section(16, func(f Face) []byte { return f.Texture[:] })
	section(4, func(f Face) []byte {
		// Stored inverted, so an opaque white face is four zeros.
		return []byte{255 - f.Colour[0], 255 - f.Colour[1], 255 - f.Colour[2], 255 - f.Colour[3]}
	})
	section(4, func(f Face) []byte { return f32bytes(f.ScaleS) })
	section(4, func(f Face) []byte { return f32bytes(f.ScaleT) })
	section(2, func(f Face) []byte { return u16bytes(uint16(f.OffsetS)) })
	section(2, func(f Face) []byte { return u16bytes(uint16(f.OffsetT)) })
	section(2, func(f Face) []byte { return u16bytes(uint16(f.Rotation)) })
	section(1, func(f Face) []byte { return []byte{f.Bump} })
	section(1, func(f Face) []byte { return []byte{f.Media} })
	section(1, func(f Face) []byte { return []byte{f.Glow} })
	section(16, func(f Face) []byte { return f.Material[:] })
	return out, nil
}

// commonest is the value to use as a section's default.
func commonest(vals []string) string {
	count := map[string]int{}
	for _, v := range vals {
		count[v]++
	}
	best, most := vals[0], 0
	for _, v := range vals {
		// Ties go to the earliest face, so the same input always packs
		// to the same bytes.
		if count[v] > most {
			best, most = v, count[v]
		}
	}
	return best
}

type faceGroup struct {
	bits  uint64
	value string
}

// groups are the faces that differ from the default, gathered by value
// so that two faces sharing an exception cost one entry.
func groups(vals []string) []faceGroup {
	def := commonest(vals)
	var order []string
	bits := map[string]uint64{}
	for i, v := range vals {
		if v == def {
			continue
		}
		if _, seen := bits[v]; !seen {
			order = append(order, v)
		}
		bits[v] |= 1 << uint(i)
	}
	out := make([]faceGroup, len(order))
	for i, v := range order {
		out[i] = faceGroup{bits: bits[v], value: v}
	}
	return out
}

// appendFaceSet writes a face bitfield, seven bits to a byte with the
// top bit meaning another follows.
func appendFaceSet(b []byte, bits uint64) []byte {
	for {
		c := byte(bits & 0x7f)
		bits >>= 7
		if bits != 0 {
			c |= 0x80
		}
		b = append(b, c)
		if bits == 0 {
			return b
		}
	}
}

func f32bytes(f float32) []byte {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], math.Float32bits(f))
	return b[:]
}

func u16bytes(v uint16) []byte {
	var b [2]byte
	binary.LittleEndian.PutUint16(b[:], v)
	return b[:]
}

// SetFaces gives an object a whole new appearance.
//
// Every face goes out together, because the message carries the whole
// blob: faces omitted from the list are not left alone, they cease to
// have a description.  Read the object's faces, change the ones that
// should change, and send them all back -- which is what SetFace does.
func (w *Session) SetFaces(ctx context.Context, o *Object, faces []Face) error {
	if o == nil || o.Local == 0 {
		return fmt.Errorf("sl: nothing to texture")
	}
	te, err := EncodeTextureEntry(faces)
	if err != nil {
		return err
	}
	m := &msg.ObjectImage{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.ObjectData = []msg.ObjectImage_ObjectData{{
		ObjectLocalID: o.Local,
		MediaURL:      nil,
		TextureEntry:  te,
	}}
	return w.Send(ctx, m)
}

// Faces is what each face of an object looks like now: its texture,
// tint, tiling, and the rest.
//
// The number of faces is what the object update said the prim has,
// since the blob does not say -- a face that never differed from the
// default leaves no trace in it.
//
// A prim nothing has described yet reads as plain white rather than as
// an error: an object can be known to be there before anything has said
// what it looks like, and a prim with no appearance recorded against it
// is a plain white prim, so PlainFaces is the truth about it rather
// than a guess.
//
// What comes back is the last appearance the REGION described, which
// lags a change made a moment ago; see SetFace.
func (w *Session) Faces(ctx context.Context, o *Object) ([]Face, error) {
	if o == nil {
		return nil, fmt.Errorf("sl: nothing to look at")
	}
	seen, err := w.ObjectByID(ctx, o.ID, 30*time.Second)
	if err != nil {
		return nil, err
	}
	n := facesOf(seen)
	faces, err := seen.Faces(n)
	if err != nil {
		if len(seen.TextureEntry) > 0 {
			return nil, fmt.Errorf("sl: reading what %s looks like: %w", o, err)
		}
		return PlainFaces(n), nil
	}
	return faces, nil
}

// SetFace changes one face and leaves the others as they are.
//
// It reads the object's current appearance first, since the message
// replaces all of it.  face is which one, or AllFaces for every face
// at once; change is given the face to modify.
//
// The count of faces is what the object update said, which is the
// number the prim really has -- a client cannot work it out from the
// blob, since faces that never differed from the default leave no
// trace in it.
//
// # It reads a cache, and the cache lags
//
// What it reads back is the last appearance the region described, and
// the region does not describe one the instant it is changed.  Two
// SetFace calls in quick succession therefore both start from the
// appearance BEFORE either -- the second undoes the first everywhere
// it did not touch.  Measured: texturing every face and then face 2
// alone left faces 0 and 1 plain.
//
// So SetFace is for one change at a time against an object that has
// settled.  A caller making several should keep the faces it built and
// send them with SetFaces, which is the whole appearance in one
// message and has nothing to read back.
func (w *Session) SetFace(ctx context.Context, o *Object, face int, change func(*Face)) error {
	faces, err := w.Faces(ctx, o)
	if err != nil {
		return err
	}
	switch {
	case face == AllFaces:
		for i := range faces {
			change(&faces[i])
		}
	case face < 0 || face >= len(faces):
		return fmt.Errorf("sl: %s has %d faces, so there is no face %d", o, len(faces), face)
	default:
		change(&faces[face])
	}
	return w.SetFaces(ctx, o, faces)
}

// facesOf is how many faces a prim has, from its shape.
//
// A box has six, a cylinder three, a sphere one -- and cutting or
// hollowing one adds the new surfaces that makes.  Getting it wrong
// truncates an appearance rather than failing, so when the shape is
// not known the answer is the eight a prim can have at most, which
// costs a little space and loses nothing.
func facesOf(s *Seen) int {
	form, known := s.Form()
	if !known {
		return 8
	}
	n := map[string]int{
		"box": 6, "prism": 5, "cylinder": 3, "sphere": 1,
		"torus": 1, "tube": 4, "ring": 3,
	}[form.Type]
	if n == 0 {
		return 8
	}
	if form.Hollow > 0 {
		n++
	}
	if form.CutBegin != 0 || form.CutEnd != 1 {
		n += 2
	}
	if n > 8 {
		n = 8
	}
	return n
}

// PlainFaces is what an untouched prim looks like: white, opaque, one
// tile of no texture on every face.
//
// It is the starting point for a prim nothing has described yet, and
// it is not the zero value -- a Face of all zeros is black, invisible
// and tiled zero times.
func PlainFaces(n int) []Face {
	out := make([]Face, n)
	for i := range out {
		out[i] = Face{Colour: [4]uint8{255, 255, 255, 255}, ScaleS: 1, ScaleT: 1}
	}
	return out
}
