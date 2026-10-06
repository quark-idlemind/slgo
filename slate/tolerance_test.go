package slate

// near: a tolerance a state expectation states, wider than the one the
// reading's quantisation gives it.
// Why: doc/slate-language.md#tolerances

import (
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/sl"
)

func TestNearParsesAndChecks(t *testing.T) {
	const h = "slate 1\nobject a is \"A\"\n"
	for _, body := range []string{
		"position a becomes original near 0.005 within 10s",
		"position a is 1 2 3 near 0.01",
		"size a link 3 is 0.8 0.4 0.016 near 2 percent within 5s",
		"size a changes near 1 percent",
		"offset a face 0 is 0.25 0 near 0.01",
		"repeats a face all becomes original near 5 percent",
		"rotation a face 0 is 0.25 near 0.002",
		"glow a face 0 is 0.5 near 0.1",
		"colour a face 1 is 1 0 0 near 0.05",
		"alpha a face 0 is $v near 0.1 percent",
		"no position a is 1 2 3 near 0.01 within 2s",
		"position a is 100 100 100 near 100 percent",
	} {
		src := h + "expect " + body + "\n"
		if strings.Contains(body, "$v") {
			src = h + "expect alpha a face 0 is any within 1s as $v\nthen expect " + body + "\n"
		}
		mustCheck(t, src)
	}
	// near comes before within and as, and is read as one clause.
	s := mustCheck(t, h+"expect position a is 1 2 3 near 0.5 percent within 4s as $p\n")
	e := s.Tests[0].Steps[0].Expect[0]
	if e.Near == nil || e.Near.Amount.Value != 0.5 || !e.Near.Percent || e.Within == nil || e.As == nil {
		t.Fatalf("near, within and as were read as %+v", e)
	}
	if e := mustParse(t, h+"expect position a is 1 2 3 near 2\n").Tests[0].Steps[0].Expect[0]; e.Near == nil || e.Near.Percent {
		t.Fatalf("near 2 was read as %+v", e.Near)
	}
	parseErr(t, h+"expect position a is 1 2 3 within 4s near 0.5\n", "near")
	parseErr(t, h+"expect position a is 1 2 3 near\n", "expected a number")
	parseErr(t, h+"expect position a is 1 2 3 near x\n", "expected a number")
	// The word is not reserved: it is still a name, and a string.
	mustCheck(t, "slate 1\nobject near is \"A\"\nobject percent is \"B\"\nexpect position near is 1 2 3 near 2 percent\nexpect size percent changes near 1\n")
	mustCheck(t, h+"expect text a is \"near\"\n")
}

func TestNearIsRefusedWhereNothingIsNumeric(t *testing.T) {
	const h = "slate 1\nobject a is \"A\"\n"
	for _, c := range []struct{ body, word string }{
		{"say \"x\" on 0 from tester near 1", "say"},
		{"dialog from a text \"x\" near 1", "dialog"},
		{"textbox from a text \"x\" near 1", "textbox"},
		{"give \"x\" from a near 1", "give"},
		{"texture a face 0 is 6b5e7e57-7e57-c0de-5117-87121399d48f near 1", "texture"},
		{"texture a face 0 changes near 1", "texture"},
		{"click a is touch near 1", "click"},
		{"text a is \"x\" near 1", "text"},
		{"fullbright a face 0 is on near 1", "fullbright"},
		{"alphamode a face 0 is blend near 1", "alphamode"},
		{"attached a off near 1", "attached"},
	} {
		checkErr(t, h+"expect "+c.body+"\n", c.word+" takes no near")
	}
	checkErr(t, h+"expect button a text \"x\" box is shown near 1\n", "button takes no near")
	checkErr(t, h+"expect position a is any near 1 within 1s as $p\n", "is any takes no near")
	checkErr(t, h+"expect position a is 1 2 3 near 0\n", "near 0 is not above 0")
	checkErr(t, h+"expect position a is 1 2 3 near -0.5\n", "near -0.5 is not above 0")
	checkErr(t, h+"expect position a is 1 2 3 near 0 percent\n", "near 0 is not above 0")
	checkErr(t, h+"expect position a is 1 2 3 near 100.5 percent\n", "near 100.5 percent is above 100")
	mustCheck(t, h+"expect position a is 1 2 3 near 150\n") // 150 m is a number of metres, not a percentage
}

// The edge of each numeric kind, compared directly: a component at the
// tolerance matches, and one past it does not.
func TestNearTheEdgeOfEachKind(t *testing.T) {
	abs := &tolerance{amount: 0.01}
	pct := &tolerance{amount: 10, percent: true}
	for _, c := range []struct {
		name string
		k    stateKind
		a, b *reading
		tol  *tolerance
		want bool
	}{
		{"position inside", kPosition, &reading{vec: [3]float64{1.009, 2, 3}}, &reading{vec: [3]float64{1, 2, 3}}, abs, true},
		{"position past", kPosition, &reading{vec: [3]float64{1, 2, 3.011}}, &reading{vec: [3]float64{1, 2, 3}}, abs, false},
		{"size per component, not summed", kSize, &reading{vec: [3]float64{1.009, 2.009, 3.009}}, &reading{vec: [3]float64{1, 2, 3}}, abs, true},
		{"size percent of the wanted", kSize, &reading{vec: [3]float64{0.8, 0.4, 0.016}}, &reading{vec: [3]float64{0.88, 0.44, 0.0176}}, pct, true},
		{"size percent past", kSize, &reading{vec: [3]float64{0.8, 0.4, 0.016}}, &reading{vec: [3]float64{0.896, 0.44, 0.0176}}, pct, false},
		{"offset", kOffset, &reading{off: [2]float64{0.3, 0}}, &reading{off: [2]float64{0.29, 0}}, abs, true},
		{"offset past", kOffset, &reading{off: [2]float64{0.3, 0.02}}, &reading{off: [2]float64{0.3, 0}}, abs, false},
		{"repeats", kRepeats, &reading{rep: [2]float64{2.009, 1}}, &reading{rep: [2]float64{2, 1}}, abs, true},
		{"repeats past", kRepeats, &reading{rep: [2]float64{2, 1.02}}, &reading{rep: [2]float64{2, 1}}, abs, false},
		{"rotation", kRotation, &reading{turns: 0.259}, &reading{turns: 0.25}, abs, true},
		{"rotation past", kRotation, &reading{turns: 0.261}, &reading{turns: 0.25}, abs, false},
		// One angle read a whole turn apart: set 359, -1, 1 and 0 degrees.
		{"rotation 359 against -1", kRotation, &reading{turns: 0.997222900390625}, &reading{turns: -0.002777099609375}, abs, true},
		{"rotation -1 against 359", kRotation, &reading{turns: -0.002777099609375}, &reading{turns: 0.997222900390625}, abs, true},
		{"rotation 1 against -1 is two degrees", kRotation, &reading{turns: 0.002777099609375}, &reading{turns: -0.002777099609375}, nil, false},
		{"rotation 1 against -1 near", kRotation, &reading{turns: 0.002777099609375}, &reading{turns: -0.002777099609375}, &tolerance{amount: 0.006}, true},
		{"rotation 359 against 0", kRotation, &reading{turns: 0.997222900390625}, &reading{turns: 0}, nil, false},
		{"rotation 1 against 359 and 0", kRotation, &reading{turns: 0.002777099609375}, &reading{turns: 0.997222900390625}, &tolerance{amount: 0.006}, true},
		{"rotation half a turn either way", kRotation, &reading{turns: -0.5}, &reading{turns: 0.5}, abs, true},
		{"rotation face all", kRotation, &reading{all: []*reading{{turns: 0.9972}, {turns: -0.0028}}}, &reading{turns: 0}, &tolerance{amount: 0.01}, true},
		{"glow", kGlow, &reading{glow: 0.509}, &reading{glow: 0.5}, abs, true},
		{"glow past", kGlow, &reading{glow: 0.511}, &reading{glow: 0.5}, abs, false},
		{"colour", kColour, &reading{col: [3]float64{1, 0.009, 0}}, &reading{col: [3]float64{1, 0, 0}}, abs, true},
		{"colour past", kColour, &reading{col: [3]float64{1, 0, 0.011}}, &reading{col: [3]float64{1, 0, 0}}, abs, false},
		{"alpha percent", kAlpha, &reading{alpha: 0.45}, &reading{alpha: 0.5}, pct, true},
		{"alpha percent past", kAlpha, &reading{alpha: 0.44}, &reading{alpha: 0.5}, pct, false},
		// A face all tuple is judged face by face.
		{"face all past", kAlpha, &reading{all: []*reading{{alpha: 0.5}, {alpha: 0.7}}}, &reading{alpha: 0.5}, abs, false},
		{"face all inside", kAlpha, &reading{all: []*reading{{alpha: 0.5}, {alpha: 0.505}}}, &reading{alpha: 0.5}, abs, true},
	} {
		if got := c.k.equalTol(c.a, c.b, c.tol); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	// Never tighter than the reading is quantised: 1e-6 on a position still
	// allows a millimetre, and a percentage of a zero component leaves the floor.
	tiny := &tolerance{amount: 1e-6}
	if !kPosition.equalTol(&reading{vec: [3]float64{0.0009, 0, 0}}, &reading{}, tiny) {
		t.Error("a tolerance below a millimetre made a position tighter than it is read")
	}
	if kPosition.equalTol(&reading{vec: [3]float64{0.0011, 0, 0}}, &reading{}, pct) {
		t.Error("a percentage of zero widened a position past the floor")
	}
	if !kGlow.equalTol(&reading{glow: 129.0 / 255}, &reading{glow: 128.0 / 255}, tiny) {
		t.Error("a glow was compared tighter than a byte")
	}
	// A kind with no number is not touched by one.
	if kTexture.equalTol(&reading{}, &reading{tex: [16]byte{1}}, abs) {
		t.Error("a texture matched within a tolerance")
	}
}

func TestPositionNearMatchesAndFailsAtTheEdge(t *testing.T) {
	f := newGrid(t)
	f.change(signLocal, withPos(10, 20, 30))
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect position sign is 10.004 20 30 near 0.005 within 300ms\n"), 0)
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect position sign is 10 20 30.004 near 0.005 within 300ms\n"), 0)
	res := play(t, f, hdr+"say \"go\" on 0\nexpect position sign is 10.006 20 30 near 0.005 within 150ms\n")
	wantExit(t, res, 1)
	mustHave(t, res,
		"unmatched position sign is 10.006 20 30 near 0.005 within 150ms (tolerance 0.005)",
		"; the nearest reading was 0.006 off, and at most 0.005 is allowed: position sign 10 20 30")
	// Without it the same expectation is the millimetre one.
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect position sign is 10.004 20 30 within 150ms\n"), 1)
	// A matching reading says how far off it was.
	res = play(t, f, hdr+"say \"go\" on 0\nexpect position sign is 10.004 20 30 near 0.005 within 300ms\nexpect position sign is 10 20 30 near 0.005 within 300ms\n")
	wantExit(t, res, 0)
	mustHave(t, res, "position sign 10 20 30 (0.004 off, at most 0.005 allowed)")
}

// The motivating case: dragged on the screen and dragged back lands a few
// millimetres from where it was, and resized and resized back a little off.
func TestADraggedAndDraggedBackObjectComesBackWithinNear(t *testing.T) {
	f := newGrid(t)
	wornSign(f)
	f.change(signLocal, withPos(0.8, 0.4, 0))
	f.change(signLocal, withScale(0.8, 0.4, 0.016))
	moves(t, f, map[string]func(*sl.Seen){
		"go":     withPos(0.7, 0.35, 0),
		"back":   withPos(0.803, 0.4, 0),
		"grow":   withScale(1.2, 0.6, 0.016),
		"shrink": withScale(0.7904, 0.3952, 0.016),
	})
	src := hdr + `expect position sign is any within 500ms as $p
expect size sign is any within 500ms as $s
say "go" on 0
expect position sign changes near 0.005 within 1s
say "back" on 0
expect position sign becomes original near 0.005 within 1s
expect position sign becomes $p near 0.005 within 1s
say "grow" on 0
expect size sign changes near 2 percent within 1s
say "shrink" on 0
expect size sign becomes original near 2 percent within 1s
`
	res := play(t, f, src)
	wantExit(t, res, 0)
	mustHave(t, res,
		"(tolerance 0.005)", "(tolerance 2 percent of each wanted component, and at least 0.001)",
		"position sign 0.803 0.4 0 (0.003 off, at most 0.005 allowed)",
		"size sign 0.7904 0.3952 0.016 (0.0096 off, at most 0.016 allowed)")
	// The same file without near is what the language could not say.
	f = newGrid(t)
	wornSign(f)
	f.change(signLocal, withPos(0.8, 0.4, 0))
	moves(t, f, map[string]func(*sl.Seen){"go": withPos(0.7, 0.35, 0), "back": withPos(0.803, 0.4, 0)})
	res = play(t, f, hdr+`expect position sign is any within 500ms as $p
say "go" on 0
expect position sign changes within 1s
say "back" on 0
expect position sign becomes original within 300ms
`)
	wantExit(t, res, 1)
}

func TestSizeNearAPercentageIsOfTheWantedValue(t *testing.T) {
	f := newGrid(t)
	f.change(signLocal, withScale(0.7904, 0.3952, 0.01584))
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect size sign is 0.8 0.4 0.016 near 2 percent within 300ms\n"), 0)
	res := play(t, f, hdr+"say \"go\" on 0\nexpect size sign is 0.8 0.4 0.016 near 1 percent within 150ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "(tolerance 1 percent of each wanted component, and at least 0.001)",
		"; the nearest reading was 0.0096 off, and at most 0.008 is allowed: size sign 0.7904 0.3952 0.01584")
}

func TestNearChangesNeedsAReadingOutsideTheTolerance(t *testing.T) {
	f := newGrid(t)
	f.change(signLocal, withPos(1, 1, 1))
	f.whenSaid("go", func() { f.changeAfter(t, 30*time.Millisecond, signLocal, withPos(1.004, 1, 1)) })
	res := play(t, f, hdr+"say \"go\" on 0\nexpect position sign changes near 0.005 within 300ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "; no reading was more than 0.004 from the baseline, and a change needs more than 0.005: the furthest was 0.004: position sign 1.004 1 1")
	f = newGrid(t)
	f.change(signLocal, withPos(1, 1, 1))
	f.whenSaid("go", func() { f.changeAfter(t, 30*time.Millisecond, signLocal, withPos(1.006, 1, 1)) })
	res = play(t, f, hdr+"say \"go\" on 0\nexpect position sign changes near 0.005 within 1s\n")
	wantExit(t, res, 0)
	mustHave(t, res, "position sign 1.006 1 1")
}

func TestNearIsOnTheNegativeFormsToo(t *testing.T) {
	// 4 mm is no change within 5 mm, and is one within the default millimetre.
	moved := func() *fakeGrid {
		f := newGrid(t)
		f.change(signLocal, withPos(1, 1, 1))
		f.whenSaid("go", func() { f.changeAfter(t, 30*time.Millisecond, signLocal, withPos(1.004, 1, 1)) })
		return f
	}
	wantExit(t, play(t, moved(), hdr+"say \"go\" on 0\nexpect no position sign changes near 0.005 within 300ms\n"), 0)
	wantExit(t, play(t, moved(), hdr+"say \"go\" on 0\nexpect no position sign changes within 300ms\n"), 1)
	// A negative becomes: 1.004 is 6 mm from 1.01, so 7 mm sees it and 5 does not.
	res := play(t, moved(), hdr+"say \"go\" on 0\nexpect no position sign becomes 1.01 1 1 near 0.007 within 300ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "forbidden no position sign becomes 1.01 1 1 near 0.007 within 300ms (tolerance 0.007)")
	wantExit(t, play(t, moved(), hdr+"say \"go\" on 0\nexpect no position sign becomes 1.01 1 1 near 0.005 within 300ms\n"), 0)
}

func TestNearAskedForLessThanTheReadingIsReadToIsRaised(t *testing.T) {
	f := newGrid(t)
	f.change(signLocal, withPos(1, 1, 1))
	f.whenSaid("go", func() { f.changeAfter(t, 30*time.Millisecond, signLocal, withPos(1.0009, 1, 1)) })
	res := play(t, f, hdr+"say \"go\" on 0\nexpect position sign is 1 1 1 near 0.0001 within 300ms\nexpect no position sign changes near 0.0001 within 300ms\n")
	wantExit(t, res, 0)
	mustHave(t, res, "(tolerance 0.001, raised from 0.0001: the least position is read to)")
}

func TestNearOnLevelsAndFaces(t *testing.T) {
	f := newGrid(t)
	f.change(signLocal, withGlow(0, 77)) // 0.302
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect glow sign face 0 is 0.3 near 0.05 within 300ms\n"), 0)
	res := play(t, f, hdr+"say \"go\" on 0\nexpect glow sign face 0 is 0.5 near 0.05 within 150ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "(tolerance 0.05)", "; the nearest reading was 0.198 off, and at most 0.05 is allowed: glow sign face 0 0.302")
	f = newGrid(t)
	f.change(signLocal, withFace(0, func(fc *sl.Face) { fc.SetColour(255, 0, 0); fc.SetAlpha(204) }))
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect colour sign face 0 is 0.95 0.02 0 near 0.06 within 300ms\nexpect alpha sign face 0 is 0.75 near 10 percent within 300ms\n"), 0)
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect alpha sign face 0 is 0.5 near 10 percent within 150ms\n"), 1)
}

func TestNearOnATextureFaceAllOffsetRepeatsAndRotation(t *testing.T) {
	f := newGrid(t)
	f.change(signLocal, onAllFaces(func(fc *sl.Face) { fc.ScaleS, fc.ScaleT = 2.03, 1 }))
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect repeats sign face 0 is 2 1 near 0.05 within 300ms\n"), 0)
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect repeats sign face 0 is 2 1 near 0.01 within 150ms\n"), 1)
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect repeats sign face 0 is 2 1 near 2 percent within 300ms\n"), 0)
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect repeats sign face 0 is 2 1 near 1 percent within 150ms\n"), 1)
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect repeats sign face all is 2 1 near 2 percent within 300ms\n"), 0)
}

func TestRotationGapIsTheShortWayRound(t *testing.T) {
	// Read 0.9972 and -0.0028 are 0.0056 apart round the wrap, not 0.9999.
	g, ok := kRotation.worst(&reading{turns: 0.9972}, &reading{turns: -0.0028}, nil, true)
	if !ok || g.diff > 1e-9 {
		t.Errorf("0.9972 against -0.0028 is %v off, want none", g.diff)
	}
	g, _ = kRotation.worst(&reading{turns: 0.0028}, &reading{turns: -0.0028}, nil, true)
	if d := g.diff - 0.0056; d > 1e-9 || d < -1e-9 {
		t.Errorf("0.0028 against -0.0028 is %v off, want 0.0056", g.diff)
	}
	// A change from one spelling of an angle to another is no change; one
	// across the wrap that is a real step still is.
	if !kRotation.equalTol(&reading{turns: -0.0028}, &reading{turns: 0.9972}, nil) {
		t.Error("0.9972 to -0.0028 was called a change")
	}
	if kRotation.equalTol(&reading{turns: 0.01}, &reading{turns: 0.99}, nil) {
		t.Error("a fiftieth of a turn across the wrap was called no change")
	}
}

func TestRotationTakesNoPercent(t *testing.T) {
	const h = "slate 1\nobject a is \"A\"\n"
	for _, body := range []string{
		"rotation a face 0 is 0.25 near 1 percent",
		"rotation a face 0 changes near 1 percent",
		"rotation a face all becomes original near 5 percent",
	} {
		checkErr(t, h+"expect "+body+"\n", "a rotation takes near in turns, not percent: an angle has no size to be a share of")
	}
	mustCheck(t, h+"expect rotation a face 0 is 0.25 near 0.01\n")
}
