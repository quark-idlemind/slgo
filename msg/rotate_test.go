package msg

import (
	"math"
	"testing"
)

func close3(a, b Vector3, tol float32) bool {
	return abs32(a.X-b.X) < tol && abs32(a.Y-b.Y) < tol && abs32(a.Z-b.Z) < tol
}

func abs32(f float32) float32 {
	if f < 0 {
		return -f
	}
	return f
}

// TestRotatingAVector: a quarter turn about z takes x to y, which is
// the one case anybody can check by eye.
func TestRotatingAVector(t *testing.T) {
	q := Quaternion{Z: float32(math.Sin(math.Pi / 4))} // 90 degrees about z
	got := q.Rotate(Vector3{X: 1})
	if !close3(got, Vector3{Y: 1}, 1e-5) {
		t.Errorf("x turned a quarter about z = %+v, want {0 1 0}", got)
	}

	// And undoing it comes back.
	if back := q.Conjugate().Rotate(got); !close3(back, Vector3{X: 1}, 1e-5) {
		t.Errorf("turning back gave %+v", back)
	}
}

// TestComposingRotations: two quarter turns about z are a half turn,
// and a half turn takes x to -x.
//
// The tolerance is a thousandth rather than the millionth the single
// rotation above manages, and that is the representation rather than
// the arithmetic: W is not stored, it is recovered as
// sqrt(1 - x^2 - y^2 - z^2) in float32, so every composition
// reconstructs it and the error compounds.  Measured here at 7e-4 for
// two quarter turns.  It is a millimetre at metre scale, which is
// under the resolution prim positions are sent at anyway.
func TestComposingRotations(t *testing.T) {
	q := Quaternion{Z: float32(math.Sin(math.Pi / 4))}
	half := q.Mul(q)
	if got := half.Rotate(Vector3{X: 1}); !close3(got, Vector3{X: -1}, 1e-3) {
		t.Errorf("two quarter turns took x to %+v, want {-1 0 0}", got)
	}
	if none := q.Mul(q.Conjugate()); !close3(none.Rotate(Vector3{X: 1}), Vector3{X: 1}, 1e-3) {
		t.Error("a rotation composed with its conjugate is not the identity")
	}
}

// TestTheIdentityRotatesNothing, since the zero value is what a prim
// with no rotation has and it must not move anything.
func TestTheIdentityRotatesNothing(t *testing.T) {
	v := Vector3{X: 1, Y: 2, Z: 3}
	if got := (Quaternion{}).Rotate(v); !close3(got, v, 1e-6) {
		t.Errorf("the identity moved %+v to %+v", v, got)
	}
}
