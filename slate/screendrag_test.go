package slate

import (
	"strings"
	"testing"
	"time"
)

func TestScreenDragParses(t *testing.T) {
	for _, c := range []struct {
		src           string
		face, by, set bool
		link          int
		from, to      [2]float64
		over          time.Duration
	}{
		{"drag hud on screen from 100 200 to 300.5 400\n", false, false, false, -1, [2]float64{100, 200}, [2]float64{300.5, 400}, 0},
		{"drag hud on screen from -10 0.5 by 300 -200 over 800ms settle\n", false, true, true, -1, [2]float64{-10, 0.5}, [2]float64{300, -200}, 800 * time.Millisecond},
		{"drag hud on screen from face 0 at 0.5 0.9 by 300 200 over 2s\n", true, true, false, -1, [2]float64{0.5, 0.9}, [2]float64{300, 200}, 2 * time.Second},
		{"drag hud on screen from link 2 face 1 at 0 0 to 5 6 settle\n", true, false, true, 2, [2]float64{0, 0}, [2]float64{5, 6}, 0},
	} {
		s := mustParse(t, "slate 1\nobject hud is \"H\"\n"+c.src)
		d := findDrag(t, s)
		sd := d.Screen
		if sd == nil || (sd.Face != nil) != c.face || sd.By != c.by || sd.Settle != c.set {
			t.Fatalf("%q: %+v", c.src, sd)
		}
		from := [2]float64{sd.FromPixels.S.Value, sd.FromPixels.T.Value}
		if c.face {
			from = [2]float64{sd.At.S.Value, sd.At.T.Value}
		}
		if from != c.from || [2]float64{sd.To.S.Value, sd.To.T.Value} != c.to {
			t.Errorf("%q: from %v to %v", c.src, from, sd.To)
		}
		if c.link >= 0 && (sd.Link == nil || sd.Link.Value != int64(c.link)) {
			t.Errorf("%q: link %v", c.src, sd.Link)
		}
		if c.link < 0 && sd.Link != nil {
			t.Errorf("%q: link %v", c.src, sd.Link)
		}
		if (d.Over == nil) != (c.over == 0) || (d.Over != nil && d.Over.Value != c.over) {
			t.Errorf("%q: over %v", c.src, d.Over)
		}
	}
	// The words are ordinary ones elsewhere.
	mustCheck(t, "slate 1\nobject screen is \"S\"\nobject settle is \"T\"\ndrag screen on screen from 1 1 by 5 5 settle\ntouch settle anywhere\n")
}

func TestScreenDragParseErrors(t *testing.T) {
	const h = "slate 1\nobject hud is \"H\"\n"
	parseErr(t, h+"drag hud from 1 1 to 2 2\n", "expected face")
	parseErr(t, h+"drag hud on from 1 1 to 2 2\n", "expected screen")
	parseErr(t, h+"drag hud on screen 1 1 to 2 2\n", "expected from")
	parseErr(t, h+"drag hud on screen from 1 to 2 2\n", "expected a number")
	parseErr(t, h+"drag hud on screen from a b to 2 2\n", "expected a number")
	parseErr(t, h+"drag hud on screen from 1 1\n", "expected to or by")
	parseErr(t, h+"drag hud on screen from 1 1 to 2 2 by 3 3\n", "")
	parseErr(t, h+"drag hud on screen from face 0 1 1 to 2 2\n", "expected at")
	parseErr(t, h+"drag hud on screen from face at 1 1 to 2 2\n", "expected an integer")
	parseErr(t, h+"drag hud on screen from 1 1 to 2\n", "expected a number")
}

func TestScreenDragIsCheckedAgainstItsBudget(t *testing.T) {
	const h = "slate 1\nobject hud is \"H\"\n"
	// 2s of move and up to 5s of settling.
	mustCheck(t, h+"drag hud on screen from 1 1 by 5 5 over 2s settle\nexpect say \"x\" on public from object hud within 8s\n")
	mustCheck(t, h+"drag hud on screen from 1 1 by 5 5 over 6s\nexpect say \"x\" on public from object hud within 8s\n")
	checkErr(t, h+"drag hud on screen from 1 1 by 5 5 over 6s settle\nexpect say \"x\" on public from object hud within 8s\n", "drag over 6s and settle (up to 5s) can take 11s, which is longer than the step's budget of 8s")
	checkErr(t, h+"drag hud on screen from 1 1 by 5 5 over 6s\nexpect say \"x\" on public from object hud within 5s\n", "can take 6s, which is longer than the step's budget of 5s")
	// Settle with no over is the default move and the wait: 5.5s.
	checkErr(t, h+"timeout 5s\ndrag hud on screen from 1 1 by 5 5 settle\n", "can take 5.5s")
	mustCheck(t, h+"timeout 6s\ndrag hud on screen from 1 1 by 5 5 settle\n")
	checkErr(t, h+"drag hud on screen from 1 1 by 5 5 over 50ms\n", "outside 100ms to 120s")
}

func TestScreenDragNeedsAnObjectKnownToBeOnAHUDPoint(t *testing.T) {
	const w = wearHdr + "wear hat on \"%s\" as h\n"
	for _, c := range []struct{ point, want string }{
		{"chest", "h is worn on chest; drag on screen needs an object worn on a HUD point"},
		{"HUD top", ""},
	} {
		src := strings.Replace(w, "%s", c.point, 1) + "drag h on screen from 1 1 by 5 5\n"
		if c.want == "" {
			mustCheck(t, src)
			continue
		}
		checkErr(t, src, c.want)
	}
	// A name a rez made is in the world.
	checkErr(t, hdr+"say \"go\" on 0\nexpect rez name \"R\" from sign as made within 1s\ndrag made on screen from 1 1 by 5 5\n", "made is rezzed in the world; drag on screen needs an object worn on a HUD point")
	// A header name is not known either way.
	mustCheck(t, hdr+"drag sign on screen from 1 1 by 5 5\n")
}

// The worked example of doc/slate-language.md, "Move and resize a HUD by
// its glass", is a file that parses and checks.
func TestTheWorkedExampleOfAScreenDragChecks(t *testing.T) {
	mustCheck(t, `slate 1
timeout 20s

item hud_item is "ExampleHUD" in "Objects"

before each {
  wear hud_item on "HUD centre 2" as hud
  expect attached hud on "HUD centre 2" within 10s
}

after each {
  take off hud
  expect attached hud off within 5s
}

test "moves and resizes" {
  # Grab the background low in the middle and take it 300 pixels right and
  # 200 down; the glass grows when pressed, so wait for it.
  drag hud on screen from face 0 at 0.5 0.9 by 300 200 over 800ms settle
  expect position hud changes within 10s

  # The resize corner is a point on the screen, here in the default
  # 1920x1025 view.
  drag hud on screen from 1480 640 by -150 100 over 800ms settle
  expect size hud becomes 0.8146 0.4073 0.1629 within 10s
}
`)
}
