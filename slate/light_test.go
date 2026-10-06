package slate

// The light and projector readings: a prim's point light and the texture
// its projector throws, read from the extra parameters of its updates.
// Why: doc/slate-runner.md#light-and-projector

import (
	"fmt"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// withLight puts a point light on a prim, the way a region's update says
// it: colour as bytes (what LSL's 0 to 1 becomes), an intensity byte over
// 255, a radius, a cutoff and a falloff.
func withLight(r, g, b uint8, intensity uint8, radius, falloff float32) func(*sl.Seen) {
	return func(o *sl.Seen) {
		o.Light = &msg.Light{Colour: [3]uint8{r, g, b}, Intensity: float32(intensity) / 255, Radius: radius, Falloff: falloff}
	}
}

// withProjector puts a projector on a prim.
func withProjector(tex msg.UUID, fov, focus, ambiance float32) func(*sl.Seen) {
	return func(o *sl.Seen) {
		o.Projector = &msg.LightImage{Texture: tex, FOV: fov, Focus: focus, Ambiance: ambiance}
	}
}

// lightOff and projectorOff are what an update without the block does.
func lightOff(o *sl.Seen)     { o.Light = nil }
func projectorOff(o *sl.Seen) { o.Projector = nil }

func TestLightAndProjectorParseAndCheck(t *testing.T) {
	const h = "slate 1\nobject a is \"A\"\n"
	for _, ok := range []string{
		"expect light a is on\n",
		"expect light a is off within 2s\n",
		"expect light a link 2 becomes on within 2s\n",
		"expect light a changes\n",
		"expect light a becomes original\n",
		"expect no light a is on within 1s\n",
		"expect light a is any within 1s as $v\nthen expect light a is $v\n",
		"expect light a colour is 1 0.5 0\n",
		"expect light a colour becomes 0 0 1 near 0.01 within 2s\n",
		"expect light a intensity is 0.8\n",
		"expect light a radius is 10 near 10 percent\n",
		"expect light a link 3 falloff becomes 0.5\n",
		"expect light a radius changes near 0.5 within 1s\n",
		"expect light a colour is any within 1s as $c\nthen expect light a colour is $c\n",
		"expect light a radius is any within 1s as $n\nthen expect light a falloff is $n\n",
		"expect projector a is off\n",
		"expect projector a is 6b5e7e57-7e57-c0de-5117-87121399d48f\n",
		"expect projector a link 2 becomes off within 2s\n",
		"expect projector a is any within 1s as $t\nthen expect projector a is $t\n",
		"expect projector a fov is 1.5\n",
		"expect projector a focus is -0.25 near 0.01\n",
		"expect projector a ambiance becomes 0.125\n",
	} {
		mustCheck(t, h+ok)
	}
	mustCheck(t, "slate 1\nobject light is \"A\"\nobject colour is \"B\"\nexpect light light is on\nexpect light colour radius is 5\n")
	parseErr(t, h+"expect light a is maybe\n", "expected on, off, original, or a capture")
	parseErr(t, h+"expect light a is 1 0 0\n", "expected on, off, original, or a capture")
	parseErr(t, h+"expect light a colour is 1 0\n", "expected a number")
	parseErr(t, h+"expect light a face 0 is on\n", "expected is, becomes, or changes")
	parseErr(t, h+"expect light a fov is 1\n", "expected is, becomes, or changes")
	parseErr(t, h+"expect projector a radius is 1\n", "expected is, becomes, or changes")
	parseErr(t, h+"expect projector a is on\n", "expected a UUID, off, original, or a capture")
	parseErr(t, h+"expect projector a fov is off\n", "expected a number")
	checkErr(t, h+"expect light a is any\n", "is any needs as")
	checkErr(t, h+"expect light a colour is 1.5 0 0\n", "light colour 1.5 is outside 0 to 1")
	checkErr(t, h+"expect light a colour is 0 0 -0.1\n", "light colour -0.1 is outside 0 to 1")
	checkErr(t, h+"expect light a intensity is 2\n", "light intensity 2 is outside 0 to 1")
	checkErr(t, h+"expect light a is on near 1\n", "light takes no near")
	checkErr(t, h+"expect projector a is off near 1\n", "projector takes no near")
	checkErr(t, h+"expect light a radius is any near 1 within 1s as $n\n", "is any takes no near")
	checkErr(t, h+"expect light a radius is 5 near 0\n", "near 0 is not above 0")
	checkErr(t, h+"expect light a radius is any within 1s as $n\nthen expect light a is $n\n", "capture type mismatch")
	checkErr(t, h+"expect light a colour is any within 1s as $n\nthen expect light a radius is $n\n", "capture type mismatch")
	// A projector's texture is a key, as a face's is.
	mustCheck(t, h+"expect projector a is any within 1s as $t\nthen expect texture a face 0 is $t\n")
}

func TestALightIsOnWhileItsBlockIsThere(t *testing.T) {
	f := newGrid(t)
	f.change(signLocal, withLight(255, 128, 0, 255, 10, 0.75))
	moves(t, f, map[string]func(*sl.Seen){"off": lightOff, "on": withLight(255, 128, 0, 255, 10, 0.75)})
	res := play(t, f, hdr+`expect light sign is on within 500ms
say "off" on 0
expect light sign becomes off within 1s
say "on" on 0
expect light sign becomes on within 1s
expect no light sign becomes off within 200ms
`)
	wantExit(t, res, 0)
	mustHave(t, res, "light sign on", "light sign off")
	// A prim with no light is off from the start, and a wrong claim fails
	// with what was read.
	f = newGrid(t)
	wantExit(t, play(t, f, hdr+"expect light sign is off within 300ms\n"), 0)
	res = play(t, f, hdr+"expect light sign is on within 150ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "unmatched light sign is on within 150ms", "light sign off")
	wantExit(t, play(t, f, hdr+"expect no light sign is on within 200ms\n"), 0)
}

func TestALightsNumbersAreReadWithTheirPacking(t *testing.T) {
	f := newGrid(t)
	// 0.5 in LSL is the byte 128, and 128/255 is 0.0020 over: inside one
	// step of the byte.
	f.change(signLocal, withLight(255, 128, 0, 204, 10, 0.75))
	res := play(t, f, hdr+`expect light sign colour is 1 0.5 0 within 300ms
expect light sign intensity is 0.8 within 300ms
expect light sign radius is 10 within 300ms
expect light sign falloff is 0.75 within 300ms
`)
	wantExit(t, res, 0)
	mustHave(t, res, "light colour sign 1 0.502 0", "light intensity sign 0.8", "light radius sign 10", "light falloff sign 0.75")
	// More than a step of the byte off is not the colour; near widens it.
	res = play(t, f, hdr+"expect light sign colour is 1 0.49 0 within 150ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "light colour sign 1 0.502 0")
	wantExit(t, play(t, f, hdr+"expect light sign colour is 1 0.49 0 near 0.02 within 300ms\n"), 0)
	wantExit(t, play(t, f, hdr+"expect light sign intensity is 0.7 within 150ms\n"), 1)
	wantExit(t, play(t, f, hdr+"expect light sign intensity is 0.7 near 0.15 within 300ms\n"), 0)
	// A float is read whole, and a share is of the wanted value.
	wantExit(t, play(t, f, hdr+"expect light sign radius is 10.01 within 150ms\n"), 1)
	wantExit(t, play(t, f, hdr+"expect light sign radius is 10.5 near 5 percent within 300ms\n"), 0)
	res = play(t, f, hdr+"expect light sign radius is 12 near 5 percent within 150ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "(tolerance 5 percent of each wanted component, and at least 0)",
		"the nearest reading was 2 off, and at most 0.6 is allowed: light radius sign 10")
	wantExit(t, play(t, f, hdr+"expect light sign falloff is 0.1 within 150ms\n"), 1)
}

func TestTheNumbersOfALightThatIsOffHaveNoReading(t *testing.T) {
	f := newGrid(t)
	res := play(t, f, hdr+"expect light sign radius is 10 within 200ms\n")
	wantExit(t, res, 1)
	mustNotHave(t, res, "light radius sign")
	// A negative needs a reading too, and none was taken.
	res = play(t, f, hdr+"expect no light sign radius is 10 within 200ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "no reading")
	// Switched on, the number is there.
	moves(t, f, map[string]func(*sl.Seen){"on": withLight(1, 2, 3, 255, 7, 1)})
	wantExit(t, play(t, f, hdr+"say \"on\" on 0\nexpect light sign radius is 7 within 1s\n"), 0)
}

func TestALightChangesAndItsNumbersCaptureAndAreOriginal(t *testing.T) {
	f := newGrid(t)
	f.change(signLocal, withLight(255, 255, 255, 255, 5, 1))
	moves(t, f, map[string]func(*sl.Seen){
		"red":   withLight(255, 0, 0, 255, 5, 1),
		"wide":  withLight(255, 255, 255, 255, 12, 1),
		"dim":   withLight(255, 255, 255, 127, 5, 1),
		"hair":  withLight(254, 255, 255, 255, 5, 1),
		"reset": withLight(255, 255, 255, 255, 5, 1),
	})
	res := play(t, f, hdr+`expect light sign radius is any within 500ms as $r
expect light sign colour is any within 500ms as $c
say "wide" on 0
expect light sign radius changes within 1s
expect no light sign radius becomes $r within 150ms
expect no light sign colour changes within 150ms
say "reset" on 0
expect light sign radius becomes $r within 1s
expect light sign radius becomes original within 1s
say "red" on 0
expect light sign colour changes within 1s
expect light sign colour becomes 1 0 0 within 1s
say "reset" on 0
expect light sign colour becomes $c within 1s
say "dim" on 0
expect light sign intensity becomes 0.5 within 1s
`)
	wantExit(t, res, 0)
	mustHave(t, res, "capture $r = 5 (step 1)", "capture $c = 1 1 1 (step 1)")
	// A byte is not a change; two are, when near allows one.
	wantExit(t, play(t, f, hdr+"say \"hair\" on 0\nexpect light sign colour changes within 300ms\n"), 1)
	wantExit(t, play(t, f, hdr+"say \"red\" on 0\nexpect light sign colour changes near 1.5 within 300ms\n"), 1)
}

func TestAProjectorIsItsTextureAndNullIsOff(t *testing.T) {
	f := newGrid(t)
	f.change(signLocal, withProjector(idTexA, 1.5, -0.25, 0.125))
	moves(t, f, map[string]func(*sl.Seen){
		"off":   projectorOff,
		"other": withProjector(idTexB, 1.5, -0.25, 0.125),
		"null":  withProjector(msg.UUID{}, 1.5, -0.25, 0.125),
		"wide":  withProjector(idTexA, 2.5, 0, 1),
	})
	res := play(t, f, hdr+fmt.Sprintf(`expect projector sign is %[1]s within 500ms
expect projector sign is any within 500ms as $t
expect projector sign fov is 1.5 within 300ms
expect projector sign focus is -0.25 within 300ms
expect projector sign ambiance is 0.125 within 300ms
say "off" on 0
expect projector sign becomes off within 1s
say "other" on 0
expect projector sign becomes %[2]s within 1s
expect projector sign changes within 300ms
`, idTexA, idTexB))
	wantExit(t, res, 0)
	mustHave(t, res, "projector sign "+idTexA.String(), "projector sign off", "projector fov sign 1.5",
		"projector focus sign -0.25", "projector ambiance sign 0.125", "capture $t = "+idTexA.String()+" (step 1)")
	// A block with the null key projects nothing, and reads as off.
	wantExit(t, play(t, f, hdr+"say \"null\" on 0\nexpect projector sign is off within 1s\n"), 0)
	wantExit(t, play(t, f, hdr+"expect projector sign fov is 1.6 within 150ms\n"), 1)
	wantExit(t, play(t, f, hdr+"expect projector sign fov is 1.6 near 0.2 within 300ms\n"), 0)
	// Its numbers read with the block, and move with it.
	wantExit(t, play(t, f, hdr+"say \"wide\" on 0\nexpect projector sign fov becomes 2.5 within 1s\nexpect projector sign ambiance is 1 within 300ms\n"), 0)
	f = newGrid(t)
	wantExit(t, play(t, f, hdr+"expect projector sign is off within 300ms\n"), 0)
	wantExit(t, play(t, f, hdr+"expect projector sign fov is 1 within 200ms\n"), 1)
}

func TestALightAndAProjectorAreReadOnAChildByLinkNumber(t *testing.T) {
	f, _, lid, _ := storeWorld(t)
	withLight(10, 20, 30, 255, 3, 0.5)(lid)
	f.whenSaid("go", func() { f.changeAfter(t, 30*time.Millisecond, 201, withProjector(idTexA, 1, 2, 3)) })
	res := play(t, f, hdr+`expect light vendor link 2 is on within 500ms
expect light vendor is off within 300ms
say "go" on 0
expect projector vendor link 2 becomes `+idTexA.String()+` within 1s
expect projector vendor link 3 is off within 300ms
`)
	wantExit(t, res, 0)
	mustHave(t, res, "light vendor link 2 on", "light vendor off")
}
