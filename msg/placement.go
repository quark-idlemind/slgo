package msg

import (
	"encoding/binary"
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
