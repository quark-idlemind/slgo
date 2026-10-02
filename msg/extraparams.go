package msg

import "fmt"

// ExtraParams is the optional part of an object: the things most prims
// do not have.
//
// It is a count, then that many blocks of a type, a length and that
// many bytes.  A prim with none of it sends a single zero byte, which
// is why the field is usually one byte long and looks like nothing.
//
// The point of the encoding is that a reader can skip what it does not
// understand: the length is there whether or not the type is known.
// So an unrecognised block is kept as bytes rather than being an error
// -- Linden Lab has added several of these over the years and will add
// more.

// Extra parameter types.
const (
	ExtraFlexible     = 0x10
	ExtraLight        = 0x20
	ExtraSculpt       = 0x30
	ExtraLightImage   = 0x40
	ExtraMesh         = 0x60
	ExtraExtendedMesh = 0x70
	ExtraRenderMat    = 0x80
)

// ExtraParam is one optional block.  Data is kept whether or not the
// type is one this package knows.
type ExtraParam struct {
	Type uint16
	Data []byte
}

// Name says what the type is, for anything that has to print one.
func (e ExtraParam) Name() string {
	switch e.Type {
	case ExtraFlexible:
		return "flexible"
	case ExtraLight:
		return "light"
	case ExtraSculpt:
		return "sculpt"
	case ExtraLightImage:
		return "light image"
	case ExtraMesh:
		return "mesh"
	case ExtraExtendedMesh:
		return "extended mesh"
	case ExtraRenderMat:
		return "render material"
	}
	return fmt.Sprintf("type %#x", e.Type)
}

// Sculpt is a sculpted or mesh prim: the texture holding the shape,
// and what kind of shape it is.
type Sculpt struct {
	Texture UUID
	Type    uint8
}

// SculptKind is the kind of a sculpt block, the low bits of its type
// byte (LL_SCULPT_TYPE_*, llvolume.h:189-199); the top two bits are the
// invert and mirror flags and are not part of the kind.  A mesh is a
// sculpt block of kind SculptMesh.
type SculptKind uint8

const (
	SculptNone     SculptKind = 0
	SculptSphere   SculptKind = 1
	SculptTorus    SculptKind = 2
	SculptPlane    SculptKind = 3
	SculptCylinder SculptKind = 4
	SculptMesh     SculptKind = 5
	SculptGLTF     SculptKind = 6

	sculptKindMask = 7
)

// SculptMark says whether a prim is a sculpt or a mesh, and what holds
// its shape: the texture of a sculpt or the asset of a mesh.  Its zero
// value is a prim that is neither.
type SculptMark struct {
	Kind SculptKind
	ID   UUID
}

// SculptMarkOf reads the mark out of a prim's extra parameters.  A
// block of kind none is no block, as in the viewer's isSculpt
// (llvolume.cpp:3300).
func SculptMarkOf(params []ExtraParam) SculptMark {
	s, ok := SculptOf(params)
	if !ok {
		return SculptMark{}
	}
	k := SculptKind(s.Type & sculptKindMask)
	if k == SculptNone {
		return SculptMark{}
	}
	return SculptMark{Kind: k, ID: s.Texture}
}

// Light is a prim that gives off light.
type Light struct {
	// Colour is the first three of four colour bytes on the wire; the
	// fourth carries Intensity, which is split out.
	Colour    [3]uint8
	Intensity float32
	Radius    float32
	Cutoff    float32
	Falloff   float32
}

// DecodeExtraParams reads the optional blocks from a standalone
// field, as ObjectUpdate carries them.
func DecodeExtraParams(b []byte) ([]ExtraParam, error) {
	if len(b) == 0 {
		return nil, nil
	}
	r := &blobReader{b: b}
	out := r.extraParams()
	return out, r.err
}

// SculptOf returns the sculpt block if there is one.
func SculptOf(params []ExtraParam) (Sculpt, bool) {
	for _, p := range params {
		if p.Type == ExtraSculpt && len(p.Data) >= 17 {
			var s Sculpt
			copy(s.Texture[:], p.Data[:16])
			s.Type = p.Data[16]
			return s, true
		}
	}
	return Sculpt{}, false
}

// LightOf returns the light block if there is one.
//
// Colour and intensity share four bytes: three for the colour and one
// for how bright it is, scaled so that 255 is full.
func LightOf(params []ExtraParam) (Light, bool) {
	for _, p := range params {
		if p.Type == ExtraLight && len(p.Data) >= 16 {
			var l Light
			l.Colour = [3]uint8{p.Data[0], p.Data[1], p.Data[2]}
			l.Intensity = float32(p.Data[3]) / 255
			l.Radius = f32at(p.Data, 4)
			l.Cutoff = f32at(p.Data, 8)
			l.Falloff = f32at(p.Data, 12)
			return l, true
		}
	}
	return Light{}, false
}
