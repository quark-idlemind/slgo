package msg

import (
	"encoding/binary"
	"fmt"
	"math"
)

// DecodePlacement reads the position and rotation out of the packed
// blob in an ObjectUpdate.
//
// The blob comes in several widths and the width is how you tell which
// it is.  The wide ones are plain floats; the narrow ones are integers
// quantised over the region, which is why the ranges below are in
// terms of the region's size.  An avatar's blob is sixteen bytes
// longer than a prim's because it starts with a collision plane.
func DecodePlacement(b []byte) (Vector3, Quaternion, bool) {
	const region = 256.0

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
		// rotation 8, angular velocity 6.
		p := Vector3{
			X: u16f(b[0:2], -0.5*region, 1.5*region),
			Y: u16f(b[2:4], -0.5*region, 1.5*region),
			Z: u16f(b[4:6], -0.5*region, 1.5*region),
		}
		q := Quaternion{
			X: u16f(b[18:20], -1, 1),
			Y: u16f(b[20:22], -1, 1),
			Z: u16f(b[22:24], -1, 1),
		}
		return p, q, true

	case 16:
		// Eight bit, the coarsest form.
		p := Vector3{
			X: u8f(b[0], -0.5*region, 1.5*region),
			Y: u8f(b[1], -0.5*region, 1.5*region),
			Z: u8f(b[2], -0.5*region, 1.5*region),
		}
		q := Quaternion{
			X: u8f(b[9], -1, 1),
			Y: u8f(b[10], -1, 1),
			Z: u8f(b[11], -1, 1),
		}
		return p, q, true
	}
	return Vector3{}, Quaternion{}, false
}

func lef32(b []byte) float32 {
	return math.Float32frombits(binary.LittleEndian.Uint32(b))
}

func placeVec(b []byte) Vector3 {
	return Vector3{X: lef32(b[0:4]), Y: lef32(b[4:8]), Z: lef32(b[8:12])}
}

func placeQuat(b []byte) Quaternion {
	return Quaternion{X: lef32(b[0:4]), Y: lef32(b[4:8]), Z: lef32(b[8:12])}
}

func u16f(b []byte, lo, hi float32) float32 {
	v := float32(binary.LittleEndian.Uint16(b)) / 65535
	return lo + (hi-lo)*v
}

func u8f(b uint8, lo, hi float32) float32 {
	return lo + (hi-lo)*(float32(b)/255)
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

	// The rest are sixteen bit fractions of a range.  Rotation runs
	// from minus one to one and all four components are sent here,
	// unlike the three float form elsewhere.  The span used for
	// velocity and acceleration has only ever been seen holding the
	// midpoint, so the value below is the conventional one rather than
	// something this package has confirmed.
	rd3 := func(lo, hi float32) Vector3 {
		v := Vector3{
			X: u16f(b[i:], lo, hi),
			Y: u16f(b[i+2:], lo, hi),
			Z: u16f(b[i+4:], lo, hi),
		}
		i += 6
		return v
	}
	const velMax = 128.0
	t.Velocity = rd3(-velMax, velMax)
	t.Acceleration = rd3(-velMax, velMax)
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
	t.AngularVel = rd3(-velMax, velMax)
	return t, nil
}
