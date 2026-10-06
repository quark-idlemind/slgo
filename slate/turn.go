package slate

import (
	"math"

	"github.com/quark-idlemind/slgo/msg"
)

// turnTol is the tolerance of a turn, in degrees. A terse update sends a
// rotation as four sixteen bit fractions of -1 to 1, a step of 2/65535 a
// component; a quaternion off by one step in each component is turned
// 4/65535 rad from the one sent, and the tolerance is two of those, as
// rotTol is two steps. A full or compressed update carries three float32
// and is exact beside it.
// Why: doc/slate-runner.md#turn
const turnTol = 8.0/65535*180/math.Pi + 1e-9

// A turn is read as a quaternion and said as Euler degrees X Y Z, which is
// what the build tool shows and what llRot2Euler gives times RAD_TO_DEG:
// the formulas are LLQuaternion::getEulerAngles and LLQuaternion::setQuat,
// Firestorm 885631b93a, indra/llmath/llquaternion.cpp:295-311
// (setQuat(roll, pitch, yaw)) and 888-916 (getEulerAngles), so that the
// numbers written beside a script's llEuler2Rot are the ones the grid means.
type quat4 struct{ x, y, z, w float64 }

// quatOfWire is a rotation as the store holds it: three floats and a W that
// is recovered.
func quatOfWire(q msg.Quaternion) quat4 {
	return quat4{float64(q.X), float64(q.Y), float64(q.Z), float64(q.W())}
}

// eulerOf is the Euler degrees of a rotation, rounded to a thousandth of a
// degree, which is as many places as the transcript says and more than a
// build tool shows. -0 is 0.
func eulerOf(q quat4) [3]float64 {
	sx := 2 * (q.x*q.w - q.y*q.z)
	sy := 2 * (q.y*q.w + q.x*q.z)
	ys := q.w*q.w - q.y*q.y
	xz := q.x*q.x - q.z*q.z
	cx := ys - xz
	cy := math.Sqrt(sx*sx + cx*cx)
	var roll, pitch, yaw float64
	if cy > 0.000436 { // GIMBAL_THRESHOLD
		roll = math.Atan2(sx, cx)
		pitch = math.Atan2(sy, cy)
		yaw = math.Atan2(2*(q.z*q.w-q.x*q.y), ys+xz)
	} else {
		if sy > 0 {
			pitch = math.Pi / 2
			yaw = 2 * math.Atan2(q.z+q.x, q.w+q.y)
		} else {
			pitch = -math.Pi / 2
			yaw = 2 * math.Atan2(q.z-q.x, q.w-q.y)
		}
	}
	out := [3]float64{roll, pitch, yaw}
	for i, r := range out {
		d := math.Round(r*180/math.Pi*1e3) / 1e3
		if d == 0 {
			d = 0 // not -0
		}
		out[i] = d
	}
	return out
}

// quatOfEuler is the rotation of Euler degrees X Y Z, as llEuler2Rot makes
// it from <x, y, z> * DEG_TO_RAD.
func quatOfEuler(deg [3]float64) quat4 {
	sx, cx := math.Sincos(deg[0] * math.Pi / 360)
	sy, cy := math.Sincos(deg[1] * math.Pi / 360)
	sz, cz := math.Sincos(deg[2] * math.Pi / 360)
	return quat4{
		x: sx*cy*cz + cx*sy*sz,
		y: cx*sy*cz - sx*cy*sz,
		z: cx*cy*sz + sx*sy*cz,
		w: cx*cy*cz - sx*sy*sz,
	}
}

// turnAngle is the angle in degrees of the rotation that takes a to b,
// however each is signed (q and -q are one rotation) and however the
// Euler triples that wrote them differ.
func turnAngle(a, b quat4) float64 {
	// a * conj(b), as a Hamilton product.
	x := a.w*-b.x + a.x*b.w + a.y*-b.z - a.z*-b.y
	y := a.w*-b.y - a.x*-b.z + a.y*b.w + a.z*-b.x
	z := a.w*-b.z + a.x*-b.y - a.y*-b.x + a.z*b.w
	w := a.w*b.w + a.x*b.x + a.y*b.y + a.z*b.z
	return 2 * math.Atan2(math.Sqrt(x*x+y*y+z*z), math.Abs(w)) * 180 / math.Pi
}

// rot is the rotation a reading of a turn holds: the one read, or for a
// wanted value the one its Euler degrees write.
func (r *reading) rot() quat4 {
	if r.hasQuat {
		return r.quat
	}
	return quatOfEuler(r.vec)
}
