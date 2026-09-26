package sl

// What a prim is shaped like, in the terms a person uses rather than
// the ones the wire uses.
//
// Second Life has no "sphere" on the wire.  It has a profile curve, a
// path curve, and a dozen numbers packed into bytes -- and a sphere is
// a half-circle profile swept round a circular path.  The names people
// know, and the names the eLSL simulator's JSON uses, are combinations
// of those two curves, so this file is the translation both ways.
//
// # Where the packing came from
//
// The viewer's LLVolumeMessage::packPathParams and its profile
// counterpart, read out of indra/llprimitive/llvolumemessage.cpp
// rather than guessed:
//
//	begin        round(f / 0.00002)
//	end          50000 - round(f / 0.00002)
//	scale x, y   200 - round(f / 0.01)
//	shear x, y   round(f / 0.01)
//	twist        round(f / 0.01), signed
//	taper        round(f / 0.01), signed
//	revolutions  round((f - 1) / 0.015)
//	hollow       round(f / 0.00002)
//
// The asymmetry in end and scale is not a mistake in the transcription:
// both are stored as the distance from the far end, so a zero byte
// means "all of it".  Getting either backwards produces a prim that is
// inside out rather than one that fails, which is why they are written
// out here.

import (
	"context"
	"fmt"
	"math"

	"github.com/quark-idlemind/slgo/msg"
)

// The quanta each packed field is counted in.
const (
	cutQuantum    = 0.00002
	scaleQuantum  = 0.01
	shearQuantum  = 0.01
	taperQuantum  = 0.01
	revQuantum    = 0.015
	hollowQuantum = 0.00002
)

// Profile curves: the cross-section swept along the path.
const (
	profileCircle     = 0x00
	profileSquare     = 0x01
	profileIsoTri     = 0x02
	profileEqualTri   = 0x03
	profileRightTri   = 0x04
	profileCircleHalf = 0x05

	profileMask = 0x0f
	holeMask    = 0xf0
)

// Path curves: what the cross-section is swept along.
const (
	pathLine   = 0x10
	pathCircle = 0x20
)

// Hole shapes, in the high nibble of the profile curve.
const (
	HoleSame     = 0x00
	HoleCircle   = 0x10
	HoleSquare   = 0x20
	HoleTriangle = 0x30
)

// Shape is a prim's form: its kind, and the numbers that bend it.
//
// The fields are named for what the viewer's build floater and LSL's
// PRIM_TYPE call them, and hold the values LSL would report -- Cut
// and Hollow are fractions, Twist is in turns -- not the bytes the
// protocol packs them into.
type Shape struct {
	// Type is one of box, cylinder, prism, sphere, torus, tube, ring.
	// It is a pair of curves on the wire and one word here.
	Type string

	// HoleShape is what a hollowed prim's hole looks like: HoleSame,
	// HoleCircle, HoleSquare or HoleTriangle.
	HoleShape int

	// Cut is where the profile starts and stops, each 0 to 1.  On a
	// sphere it is called dimple and cuts the other way about.
	CutBegin, CutEnd float32

	// Hollow is the fraction taken out of the middle, 0 to 0.95.
	Hollow float32

	// Twist is the turn from bottom to top, in turns, each -1 to 1.
	TwistBegin, TwistEnd float32

	// TopSize and TopShear are the top face's size and offset.
	TopSizeX, TopSizeY   float32
	TopShearX, TopShearY float32

	// The rest apply to the ring shapes -- torus, tube, ring -- where
	// the path is a circle and there is a profile to cut as well.
	AdvancedCutBegin, AdvancedCutEnd float32
	TaperX, TaperY                   float32
	Revolutions                      float32
	RadiusOffset                     float32
	Skew                             float32
}

// DefaultShape is a plain box, which is what rezzing without saying
// anything gives.
func DefaultShape() Shape {
	return Shape{
		Type: "box", CutEnd: 1, TopSizeX: 1, TopSizeY: 1,
		AdvancedCutEnd: 1, Revolutions: 1,
	}
}

// curves are the profile and path a named shape is made of.
//
// A box and a tube share a square profile and differ only in the path;
// a sphere is the odd one, a half-circle swept round a circle.
var curves = map[string][2]uint8{
	"box":      {profileSquare, pathLine},
	"cylinder": {profileCircle, pathLine},
	"prism":    {profileEqualTri, pathLine},
	"sphere":   {profileCircleHalf, pathCircle},
	"torus":    {profileCircle, pathCircle},
	"tube":     {profileSquare, pathCircle},
	"ring":     {profileEqualTri, pathCircle},
}

// ShapeNames are the shapes that can be named, in a fixed order.
var ShapeNames = []string{"box", "cylinder", "prism", "sphere", "torus", "tube", "ring"}

// shapeName is the reverse of curves.
func shapeName(profile, path uint8) string {
	profile &= profileMask
	for _, name := range ShapeNames {
		c := curves[name]
		if c[0] == profile && c[1] == path {
			return name
		}
	}
	return ""
}

// Pack turns a shape into the bytes the protocol wants.
//
// Which of the two curves carries the cut is the thing to know: for a
// prim swept along a line the profile is cut by Cut and the path is
// whole, and for one swept round a circle it is the other way about,
// with the profile cut by AdvancedCut instead.  LSL's PRIM_TYPE does
// the same swap, which is why a sphere's cut is called a dimple.
func (s Shape) Pack() (msg.PrimShape, error) {
	c, ok := curves[s.Type]
	if !ok {
		return msg.PrimShape{}, fmt.Errorf("sl: no prim shape called %q; there is %v", s.Type, ShapeNames)
	}
	p := msg.PrimShape{
		ProfileCurve:     c[0] | uint8(s.HoleShape&holeMask),
		PathCurve:        c[1],
		ProfileHollow:    packCut(s.Hollow, hollowQuantum),
		PathScaleX:       packScale(s.TopSizeX),
		PathScaleY:       packScale(s.TopSizeY),
		PathShearX:       packSigned8(s.TopShearX, shearQuantum),
		PathShearY:       packSigned8(s.TopShearY, shearQuantum),
		PathTwistBegin:   int8(packSigned8(s.TwistBegin, scaleQuantum)),
		PathTwist:        int8(packSigned8(s.TwistEnd, scaleQuantum)),
		PathTaperX:       int8(packSigned8(s.TaperX, taperQuantum)),
		PathTaperY:       int8(packSigned8(s.TaperY, taperQuantum)),
		PathRadiusOffset: int8(packSigned8(s.RadiusOffset, scaleQuantum)),
		PathSkew:         int8(packSigned8(s.Skew, scaleQuantum)),
		PathRevolutions:  uint8(clamp(math.Round(float64(s.Revolutions-1)/revQuantum), 0, 255)),
	}

	// The cut goes on whichever curve is the one being swept.
	if c[1] == pathCircle {
		p.PathBegin, p.PathEnd = packBegin(s.CutBegin), packEnd(s.CutEnd)
		p.ProfileBegin, p.ProfileEnd = packBegin(s.AdvancedCutBegin), packEnd(s.AdvancedCutEnd)
	} else {
		p.ProfileBegin, p.ProfileEnd = packBegin(s.CutBegin), packEnd(s.CutEnd)
		p.PathBegin, p.PathEnd = packBegin(0), packEnd(1)
	}
	return p, nil
}

// UnpackShape is the inverse of Pack.
//
// An unrecognised pair of curves is reported as a shape with an empty
// Type rather than as an error: a mesh or a sculpt is described by
// these fields too, and refusing to describe one at all would be worse
// than describing it as something without a name.
func UnpackShape(p msg.PrimShape) Shape {
	s := Shape{
		Type:         shapeName(p.ProfileCurve, p.PathCurve),
		HoleShape:    int(p.ProfileCurve & holeMask),
		Hollow:       float32(p.ProfileHollow) * hollowQuantum,
		TopSizeX:     unpackScale(p.PathScaleX),
		TopSizeY:     unpackScale(p.PathScaleY),
		TopShearX:    unpackSigned8(p.PathShearX, shearQuantum),
		TopShearY:    unpackSigned8(p.PathShearY, shearQuantum),
		TwistBegin:   float32(p.PathTwistBegin) * scaleQuantum,
		TwistEnd:     float32(p.PathTwist) * scaleQuantum,
		TaperX:       float32(p.PathTaperX) * taperQuantum,
		TaperY:       float32(p.PathTaperY) * taperQuantum,
		RadiusOffset: float32(p.PathRadiusOffset) * scaleQuantum,
		Skew:         float32(p.PathSkew) * scaleQuantum,
		Revolutions:  1 + float32(p.PathRevolutions)*revQuantum,
	}
	if p.PathCurve == pathCircle {
		s.CutBegin, s.CutEnd = unpackBegin(p.PathBegin), unpackEnd(p.PathEnd)
		s.AdvancedCutBegin, s.AdvancedCutEnd = unpackBegin(p.ProfileBegin), unpackEnd(p.ProfileEnd)
	} else {
		s.CutBegin, s.CutEnd = unpackBegin(p.ProfileBegin), unpackEnd(p.ProfileEnd)
		s.AdvancedCutEnd = 1
	}
	return s
}

// SetShape changes what an existing prim is shaped like.
//
// It is one message and the simulator answers it with an object update,
// so a caller that wants to be sure can read the object back.  Nothing
// here waits: a shape change is not a request that can be refused so
// much as one that can be ignored, and reading back is the only proof
// either way.
func (w *Session) SetShape(ctx context.Context, o *Object, s Shape) error {
	p, err := s.Pack()
	if err != nil {
		return err
	}
	local, err := w.local(ctx, o)
	if err != nil {
		return err
	}
	m := &msg.ObjectShape{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	d := msg.ObjectShape_ObjectData{ObjectLocalID: local}
	p.FillShape(&d)
	m.ObjectData = []msg.ObjectShape_ObjectData{d}
	return w.Send(ctx, m)
}

// -- the packing itself -------------------------------------------------

func packBegin(f float32) uint16 {
	return packCut(f, cutQuantum)
}

// packEnd stores the distance from the far end, so that a zero means
// the whole of it.
func packEnd(f float32) uint16 {
	return 50000 - packCut(f, cutQuantum)
}

func packCut(f float32, quantum float64) uint16 {
	return uint16(clamp(math.Round(float64(f)/quantum), 0, 50000))
}

func unpackBegin(v uint16) float32 { return float32(float64(v) * cutQuantum) }
func unpackEnd(v uint16) float32   { return float32(float64(50000-int(v)) * cutQuantum) }

// packScale stores the distance from the full size, the same way the
// end of a cut does.
func packScale(f float32) uint8 {
	return uint8(clamp(200-math.Round(float64(f)/scaleQuantum), 0, 255))
}

func unpackScale(v uint8) float32 {
	return float32(float64(200-int(v)) * scaleQuantum)
}

// packSigned8 rounds a value into a byte that is read as signed.  The
// protocol declares some of these U8 and some S8 while the viewer
// packs all of them the same way, so the conversion is done once here
// and the field's declared type is where it lands.
func packSigned8(f float32, quantum float64) uint8 {
	return uint8(int8(clamp(math.Round(float64(f)/quantum), -128, 127)))
}

func unpackSigned8(v uint8, quantum float64) float32 {
	return float32(float64(int8(v)) * quantum)
}

func clamp(f, lo, hi float64) float64 {
	if math.IsNaN(f) {
		return lo
	}
	return math.Max(lo, math.Min(hi, f))
}
