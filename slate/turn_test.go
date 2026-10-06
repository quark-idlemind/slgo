package slate

// The turn reading: a prim's own rotation, said as Euler degrees and
// compared as a rotation.
// Why: doc/slate-runner.md#turn

import (
	"math"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// withTurn sets a prim's rotation from Euler degrees, as llEuler2Rot
// does, packed as the region sends it.
func withTurn(x, y, z float64) func(*sl.Seen) {
	return func(o *sl.Seen) {
		q := quatOfEuler([3]float64{x, y, z})
		o.Rotation = msg.PackQuaternion(float32(q.x), float32(q.y), float32(q.z), float32(q.w))
	}
}

func TestTurnParsesAndChecks(t *testing.T) {
	const h = "slate 1\nobject a is \"A\"\n"
	mustCheck(t, h+"expect turn a is 0 90 -45.5\n")
	mustCheck(t, h+"expect turn a becomes 0 0 180 within 2s\n")
	mustCheck(t, h+"expect turn a changes\n")
	mustCheck(t, h+"expect turn a link 2 becomes 0 0 90 near 0.5 within 2s\n")
	mustCheck(t, h+"expect turn a becomes original near 1\n")
	mustCheck(t, h+"expect no turn a changes within 1s\n")
	mustCheck(t, h+"expect turn a is any within 1s as $v\nthen expect turn a becomes $v\n")
	parseErr(t, h+"expect turn a is 1 2\n", "expected a number")
	parseErr(t, h+"expect turn a face 0 is 1 2 3\n", "expected is, becomes, or changes")
	checkErr(t, h+"expect turn a is any\n", "is any needs as")
	checkErr(t, h+"expect turn a is 1 2 3 near 5 percent\n", "a turn takes near in degrees")
	checkErr(t, h+"expect turn a is any near 1 within 1s as $v\n", "is any takes no near")
	checkErr(t, h+"expect turn a is 1 2 3 near 0\n", "near 0 is not above 0")
	checkErr(t, h+"expect turn a is any within 1s as $v\nthen expect colour a face 0 is $v\n", "capture type mismatch")
	// An angle is not a size: 0 and negatives are fine.
	mustCheck(t, h+"expect turn a is 0 0 0\n")
	// Not reserved.
	mustCheck(t, "slate 1\nobject turn is \"A\"\nexpect turn turn is 0 0 90\n")
}

// The Euler formulas are the viewer's: llEuler2Rot(<0,0,90>*DEG_TO_RAD)
// is <0,0,sin45,cos45>, and what the build tool shows comes back.
func TestEulerDegreesRoundTripAndAreTheViewers(t *testing.T) {
	q := quatOfEuler([3]float64{0, 0, 90})
	if math.Abs(q.z-math.Sqrt2/2) > 1e-12 || math.Abs(q.w-math.Sqrt2/2) > 1e-12 || q.x != 0 || q.y != 0 {
		t.Errorf("<0,0,90> is %+v", q)
	}
	// A quarter about X then, in the viewer's order, the others: the
	// composition is qx * qy * qz of the viewer's, which this pins down.
	for _, e := range [][3]float64{{30, 40, 50}, {-170, 20, 100}, {0, 0, 0}, {12.5, -80, 179.5}, {90, 0, 0}} {
		if got := eulerOf(quatOfEuler(e)); got != e {
			t.Errorf("%v came back as %v", e, got)
		}
	}
	// 0 -0 and a hair under a degree say plainly.
	if got := g3(eulerOf(quatOfEuler([3]float64{-0.0001, 0, 0}))); got != "0 0 0" {
		t.Errorf("a ten-thousandth of a degree says %q", got)
	}
}

func TestTwoSpellingsOfOneRotationAreOne(t *testing.T) {
	a, b := quatOfEuler([3]float64{180, 0, 0}), quatOfEuler([3]float64{0, 180, 180})
	if d := turnAngle(a, b); d > 1e-9 {
		t.Errorf("180 0 0 and 0 180 180 are %g degrees apart", d)
	}
	// q and -q are one rotation too.
	n := quat4{-a.x, -a.y, -a.z, -a.w}
	if d := turnAngle(a, n); d > 1e-9 {
		t.Errorf("q and -q are %g degrees apart", d)
	}
	if d := turnAngle(quatOfEuler([3]float64{0, 0, 10}), quatOfEuler([3]float64{0, 0, 350})); math.Abs(d-20) > 1e-9 {
		t.Errorf("10 and 350 about Z are %g degrees apart, not 20", d)
	}
	f, _, _, _ := storeWorld(t)
	f.change(201, withTurn(180, 0, 0))
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect turn vendor link 2 is 180 0 0 within 300ms\n"), 0)
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect turn vendor link 2 is 0 180 180 within 300ms\n"), 0)
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect turn vendor link 2 is 0 180 0 within 150ms\n"), 1)
}

func TestTurnIsWithinTheTersePackingAndNearWidensIt(t *testing.T) {
	f := newGrid(t)
	f.change(signLocal, withTurn(0, 0, 90))
	// Under the least a region can say, written with a hair of rounding.
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect turn sign is 0 0 90.005 within 300ms\n"), 0)
	res := play(t, f, hdr+"say \"go\" on 0\nexpect turn sign is 0 0 90.02 within 150ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "unmatched turn sign is 0 0 90.02 within 150ms", "turn sign 0 0 90")
	// near is degrees of the angle between, whichever axis it is about.
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect turn sign is 0 0 90.9 near 1 within 300ms\n"), 0)
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect turn sign is 0.5 0.5 90 near 1 within 300ms\n"), 0)
	res = play(t, f, hdr+"say \"go\" on 0\nexpect turn sign is 0 0 91.1 near 1 within 150ms\n")
	wantExit(t, res, 1)
	mustHave(t, res,
		"unmatched turn sign is 0 0 91.1 near 1 within 150ms (tolerance 1)",
		"; the nearest reading was 1.1 off, and at most 1 is allowed: turn sign 0 0 90")
	res = play(t, f, hdr+"say \"go\" on 0\nexpect turn sign is 0 0 90.5 near 1 within 300ms\n")
	wantExit(t, res, 0)
	mustHave(t, res, "turn sign 0 0 90 (0.5 off, at most 1 allowed)")
}

func TestAChildsTurnBecomesAndIsRelativeToItsRoot(t *testing.T) {
	f, vendor, lid, _ := storeWorld(t)
	vendor.Rotation = msg.PackQuaternion(0, 0, 0.5, 0.5)
	withTurn(0, 0, 0)(lid)
	f.whenSaid("go", func() { f.changeAfter(t, 30*time.Millisecond, 201, withTurn(0, 45, 0)) })
	res := play(t, f, hdr+`expect turn vendor link 2 is 0 0 0 within 500ms
say "go" on 0
expect turn vendor link 2 becomes 0 45 0 within 1s
expect no turn vendor link 3 changes within 200ms
`)
	wantExit(t, res, 0)
	mustHave(t, res, "turn vendor link 2 0 0 0", "turn vendor link 2 0 45 0")
	// The root's own rotation is not the child's: the lid reads as it is
	// set, not composed with the vendor's.
	wantExit(t, play(t, f, hdr+"expect turn vendor link 2 is 0 45 0 within 300ms\n"), 0)
	wantExit(t, play(t, f, hdr+"expect turn vendor link 2 is 0 45 90 within 150ms\n"), 1)
	wantExit(t, play(t, f, hdr+"expect turn vendor is 0 0 90 within 300ms\n"), 0)
}

func TestTurnChangesCapturesAndOriginal(t *testing.T) {
	f := newGrid(t)
	f.change(signLocal, withTurn(10, 20, 30))
	moves(t, f, map[string]func(*sl.Seen){"go": withTurn(10, 20, 60), "back": withTurn(10, 20, 30), "hair": withTurn(10, 20, 30.004)})
	res := play(t, f, hdr+`expect turn sign is any within 500ms as $r
say "go" on 0
expect turn sign changes within 1s
expect no turn sign becomes $r within 150ms
say "back" on 0
expect turn sign becomes $r within 1s
expect turn sign becomes original within 1s
expect turn sign is 10 20 30 within 500ms
`)
	wantExit(t, res, 0)
	mustHave(t, res, "capture $r = 10 20 30 (step 1)")
	// A hair is not a change, and a degree is, when near says so.
	f = newGrid(t)
	f.change(signLocal, withTurn(10, 20, 30))
	moves(t, f, map[string]func(*sl.Seen){"hair": withTurn(10, 20, 30.004), "deg": withTurn(10, 20, 31)})
	wantExit(t, play(t, f, hdr+"say \"hair\" on 0\nexpect turn sign changes within 300ms\n"), 1)
	wantExit(t, play(t, f, hdr+"say \"deg\" on 0\nexpect turn sign changes within 1s\n"), 0)
	wantExit(t, play(t, f, hdr+"say \"deg\" on 0\nexpect turn sign changes near 2 within 300ms\n"), 1)
	// A capture of a rotation spelled the other way is still the rotation.
	f = newGrid(t)
	f.change(signLocal, withTurn(0, 180, 180))
	wantExit(t, play(t, f, hdr+"expect turn sign is any within 500ms as $r\nexpect turn sign is 180 0 0 within 300ms\nthen expect turn sign is $r within 300ms\n"), 0)
}
