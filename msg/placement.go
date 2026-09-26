package msg

import (
	"encoding/binary"
	"fmt"
	"math"
)

// DecodePlacement reads the position and rotation out of the packed
// blob in an ObjectUpdate, and reports false for a width it does not
// know.
//
// The width is how you tell which form the blob is.  Sixty bytes are
// plain floats.  Thirty-two are sixteen bit fractions, laid out and
// ranged as the viewer's sixteen bit reader has them
// (llviewerobject.cpp:1636-1695).  An avatar's blob is sixteen bytes
// longer than a prim's, 76 or 48, because it starts with a collision
// plane.  No other width is read.
// Why: doc/placement.md#the-widths
func DecodePlacement(b []byte) (Vector3, Quaternion, bool) {
	// Avatars carry a collision plane first.
	switch len(b) {
	case 76, 48:
		b = b[16:]
	}

	switch len(b) {
	case 60:
		// Floats: position, velocity, acceleration, rotation, angular
		// velocity, twelve bytes each.
		return placeVec(b[0:12]), placeQuat(b[36:48]), true

	case 32:
		// Sixteen bit: position 6, velocity 6, acceleration 6,
		// rotation 8, angular velocity 6.  The rotation's fourth
		// component is kept, as DecodeTerse keeps it: dropping it and
		// recovering W as positive reads a -q as its mirror image.
		p := Vector3{
			X: u16f(b[0:2], -0.5*regionWidth, 1.5*regionWidth),
			Y: u16f(b[2:4], -0.5*regionWidth, 1.5*regionWidth),
			Z: u16f(b[4:6], placeMinZ, placeMaxZ),
		}
		q := PackQuaternion(
			u16f(b[18:20], -1, 1),
			u16f(b[20:22], -1, 1),
			u16f(b[22:24], -1, 1),
			u16f(b[24:26], -1, 1),
		)
		return p, q, true
	}
	return Vector3{}, Quaternion{}, false
}

// The heights a sixteen bit position runs over: the viewer's MIN_HEIGHT
// and MAX_HEIGHT (llviewerobject.cpp:1305-1306), which on Second Life
// are a region's width below zero (llworld.h:125) and SL_MAX_OBJECT_Z
// (llworld.cpp:207, llmath/xform.h:38).
const (
	placeMinZ = -regionWidth
	placeMaxZ = 4096
)

func lef32(b []byte) float32 {
	return math.Float32frombits(binary.LittleEndian.Uint32(b))
}

func placeVec(b []byte) Vector3 {
	return Vector3{X: lef32(b[0:4]), Y: lef32(b[4:8]), Z: lef32(b[8:12])}
}

func placeQuat(b []byte) Quaternion {
	return Quaternion{X: lef32(b[0:4]), Y: lef32(b[4:8]), Z: lef32(b[8:12])}
}

// u16f reads a sixteen bit fraction of lo..hi as the viewer's U16_to_F32
// does (llmath/llquantize.h:72-85), including its one adjustment: a
// value within one step of zero is exactly zero.  Zero falls between two
// steps of a span centred on it, so without that a thing at rest reads
// as moving a few millimetres a second.
func u16f(b []byte, lo, hi float32) float32 {
	span := hi - lo
	v := lo + span*(float32(binary.LittleEndian.Uint16(b))/65535)
	if step := span / 65535; v > -step && v < step {
		return 0
	}
	return v
}

// Terse is one object out of an ImprovedTerseObjectUpdate.
//
// This is the message the simulator sends most: everything that is
// moving, several times a second.  It carries no identity beyond the
// local id and no appearance at all -- only where something is and
// where it is going, quantised to sixteen bits over the region.
type Terse struct {
	LocalID uint32
	State   uint8

	// Avatar says whether the blob began with a collision plane,
	// which is the only thing distinguishing the two layouts.
	Avatar bool

	// CollisionPlane is where an avatar is standing, and is present
	// only for one.
	CollisionPlane [4]float32

	Position     Vector3
	Velocity     Vector3
	Acceleration Vector3
	Rotation     Quaternion
	AngularVel   Vector3
}

// DecodeTerse reads the packed blob in ImprovedTerseObjectUpdate.
//
// The layout was read off real blobs rather than assumed, and the
// assumption was wrong: the position is three plain floats, not the
// quantised pair of bytes per axis that everything after it uses.
// Decoding it as quantised gave positions that looked like positions
// -- in range, stable, changing plausibly -- while being nowhere near
// where the objects actually were, which is the kind of wrong that
// does not announce itself.
//
//	0  local id, four bytes
//	4  state
//	5  whether an avatar, and so whether a collision plane follows
//	6  collision plane, sixteen bytes, avatars only
//	   position, three floats
//	   velocity, three sixteen bit fractions
//	   acceleration, three
//	   rotation, four
//	   angular velocity, three
//
// Forty-four bytes for a prim, sixty for an avatar.
func DecodeTerse(b []byte) (*Terse, error) {
	if len(b) < 6 {
		return nil, fmt.Errorf("msg: terse update is %d bytes, too short for a header", len(b))
	}

	t := &Terse{
		LocalID: binary.LittleEndian.Uint32(b),
		State:   b[4],
		Avatar:  b[5] != 0,
	}
	i := 6
	if t.Avatar {
		if len(b) < i+16 {
			return nil, fmt.Errorf("msg: terse update for an avatar is missing its collision plane")
		}
		for j := range 4 {
			t.CollisionPlane[j] = lef32(b[i+j*4:])
		}
		i += 16
	}

	// position 12, velocity 6, acceleration 6, rotation 8, angular 6
	if len(b) < i+38 {
		return nil, fmt.Errorf("msg: terse update has %d bytes of body, wanted 38", len(b)-i)
	}

	t.Position = Vector3{X: lef32(b[i:]), Y: lef32(b[i+4:]), Z: lef32(b[i+8:])}
	i += 12

	// The rest are sixteen bit fractions of a range, the viewer's
	// ranges (llviewerobject.cpp:1755-1778): velocity over plus or minus
	// 128, acceleration and angular velocity over plus or minus 64, and
	// rotation over plus or minus one, with all four components sent
	// here, unlike the three float form elsewhere.
	rd3 := func(lo, hi float32) Vector3 {
		v := Vector3{
			X: u16f(b[i:], lo, hi),
			Y: u16f(b[i+2:], lo, hi),
			Z: u16f(b[i+4:], lo, hi),
		}
		i += 6
		return v
	}
	t.Velocity = rd3(-128, 128)
	t.Acceleration = rd3(-64, 64)
	// All four components are on the wire here, and the fourth is not
	// only there to be thrown away.  The simulator does not keep W
	// positive, and q and -q are the same rotation, so keeping X, Y and
	// Z as they came and recovering W as positive -- which is what
	// Quaternion does -- turns a -q into the mirror image of q: a yaw
	// of plus a quarter turn read as minus one.  Measured on Agni,
	// 2026-09-24, an avatar walking north read as facing south on some
	// walks and not on others.  PackQuaternion normalises and puts the
	// sign where Quaternion expects it.
	t.Rotation = PackQuaternion(
		u16f(b[i:], -1, 1),
		u16f(b[i+2:], -1, 1),
		u16f(b[i+4:], -1, 1),
		u16f(b[i+6:], -1, 1),
	)
	i += 8
	t.AngularVel = rd3(-64, 64)
	return t, nil
}
