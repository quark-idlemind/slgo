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
	if o == nil || (o.Local == 0 && o.ID.IsZero()) {
		return fmt.Errorf("sl: nothing to texture")
	}
	te, err := EncodeTextureEntry(faces)
	if err != nil {
		return err
	}
	local, err := w.local(ctx, o)
	if err != nil {
		return err
	}
	m := &msg.ObjectImage{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.ObjectData = []msg.ObjectImage_ObjectData{{
		ObjectLocalID: local,
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
// A prim nothing has described the appearance of is asked about before
// it is answered for, because that is the state a change leaves
// behind: the session forgets an appearance it has just replaced,
// since the region will not mention the replacement.  See
// agent.Objects.sent.
//
// One the region does not describe when asked reads as plain white
// rather than as an error: an object can be known to be there before
// anything has said what it looks like, and a prim with no appearance
// recorded against it is a plain white prim, so PlainFaces is the
// truth about it rather than a guess.
func (w *Session) Faces(ctx context.Context, o *Object) ([]Face, error) {
	faces, _, err := w.faces(ctx, o)
	return faces, err
}

// faces is Faces, saying as well whether anything actually described
// the appearance or the answer is the plain white stand-in.
//
// The difference does not matter to a report and matters entirely to a
// change: a change sends every face, so building one on the stand-in
// would blank whatever the object really looks like.  See SetFace.
func (w *Session) faces(ctx context.Context, o *Object) ([]Face, bool, error) {
	if o == nil {
		return nil, false, fmt.Errorf("sl: nothing to look at")
	}
	seen, err := w.ObjectByID(ctx, o.ID, 30*time.Second)
	if err != nil {
		return nil, false, err
	}
	if len(seen.TextureEntry) == 0 {
		again, err := w.describeAgain(ctx, seen)
		if err != nil {
			return nil, false, err
		}
		if again != nil {
			seen = again
		}
	}
	n := facesOf(seen)
	faces, err := seen.Faces(n)
	if err != nil {
		if len(seen.TextureEntry) > 0 {
			return nil, false, fmt.Errorf("sl: reading what %s looks like: %w", o, err)
		}
		return PlainFaces(n), false, nil
	}
	return faces, true, nil
}

// describeAgainFor is how long to wait for the region to say what an
// object looks like, having been asked.
//
// Measured on a live region: a full update carrying the appearance came
// back 100 to 200ms after the request, every time it was tried.  Three
// seconds is that with room for a busy simulator, and running out is
// not an error -- an object nothing will describe reads as plain white,
// which is what it did before anything asked.
const describeAgainFor = 3 * time.Second

// describeAgain asks the region to describe an object it has already
// described, and waits for the answer.
//
// The request is the one a viewer sends for a cache miss, which is
// exactly what this is: the session is missing an appearance it once
// had.  What comes back is an ordinary full update, so it lands in the
// object store the same way everything else does and this reads it from
// there rather than watching for the packet.
//
// A nil answer means nothing arrived in time and the caller should make
// do with what it had.
func (w *Session) describeAgain(ctx context.Context, seen *Seen) (*Seen, error) {
	if seen.Local == 0 {
		return nil, nil
	}
	local, err := w.local(ctx, &seen.Object)
	if err != nil {
		return nil, err
	}
	m := &msg.RequestMultipleObjects{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.ObjectData = []msg.RequestMultipleObjects_ObjectData{
		// Miss type 0 is "I have nothing at all", which is the truth
		// once the appearance has been forgotten.
		{CacheMissType: 0, ID: local},
	}
	if err := w.Send(ctx, m); err != nil {
		return nil, err
	}

	deadline := time.Now().Add(describeAgainFor)
	for time.Now().Before(deadline) {
		if err := w.Settle(ctx, 100*time.Millisecond); err != nil {
			return nil, err
		}
		found, err := w.fetch(ctx, "", seen.ID.String())
		if err != nil {
			return nil, err
		}
		if len(found) > 0 && len(found[0].TextureEntry) > 0 {
			return found[0], nil
		}
	}
	return nil, nil
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
// # What it reads, and what that costs
//
// The region does not describe an appearance when it is changed, so
// the session forgets the one it held as the change went out -- see
// agent.Objects.sent -- and the read here asks the region for it
// again.  Measured on a live region: the answer came back in 100 to
// 200ms, and two changes in a row then composed, every face red
// followed by face 2 green leaving five red faces and a green one.
// Without the asking they did not: the red was gone from all six.
// The composing was watched with a build that asked on every read
// rather than only on a forgotten one; the request that goes out and
// the answer that comes back are the same either way.
//
// A caller changing several faces should still send them together with
// SetFaces, which is the whole appearance in one message and asks the
// region nothing.
//
// If the region will not say what the object looks like, this refuses
// rather than working from the plain white a report settles for.  The
// message replaces every face, so a change built on that stand-in would
// wipe whatever the object really wore -- which is the thing this whole
// arrangement exists to stop, and refusing costs a retry.
func (w *Session) SetFace(ctx context.Context, o *Object, face int, change func(*Face)) error {
	faces, known, err := w.faces(ctx, o)
	if err != nil {
		return err
	}
	if !known {
		return fmt.Errorf("sl: nothing has said what %s looks like, and a change would replace every face of it", o)
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
