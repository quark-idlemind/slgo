package slate

// The position and size readings: a prim's Seen.Position and Seen.Scale,
// compared within a millimetre on each axis.
// Why: doc/slate-runner.md#position-and-size

import (
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

func withPos(x, y, z float32) func(*sl.Seen) {
	return func(o *sl.Seen) { o.Position = msg.Vector3{X: x, Y: y, Z: z} }
}

func withScale(x, y, z float32) func(*sl.Seen) {
	return func(o *sl.Seen) { o.Scale = msg.Vector3{X: x, Y: y, Z: z} }
}

// wornSign puts the sign on the avatar, on a HUD point, as a worn root is.
func wornSign(f *fakeGrid) {
	f.objects[0].Parent, f.objects[0].AttachPoint = 1, 31
	f.objects[0].Position = msg.Vector3{}
}

// moves changes the sign 30 ms after the tester says each trigger.
func moves(t *testing.T, f *fakeGrid, by map[string]func(*sl.Seen)) {
	t.Helper()
	f.replyTo(func(m msg.Message) {
		if text, _, ok := says(m); ok && by[text] != nil {
			f.changeAfter(t, 30*time.Millisecond, signLocal, by[text])
		}
	})
}

func TestPositionAndSizeParseAndCheck(t *testing.T) {
	const h = "slate 1\nobject a is \"A\"\n"
	for _, w := range []string{"position", "size"} {
		mustCheck(t, h+"expect "+w+" a is 0.5 1 2\n")
		mustCheck(t, h+"expect "+w+" a becomes 0.5 1 2 within 2s\n")
		mustCheck(t, h+"expect "+w+" a changes\n")
		mustCheck(t, h+"expect "+w+" a link 2 becomes 1 1 1\n")
		mustCheck(t, h+"expect "+w+" a becomes original\n")
		mustCheck(t, h+"expect no "+w+" a changes within 1s\n")
		mustCheck(t, h+"expect "+w+" a is any within 1s as $v\nthen expect "+w+" a becomes $v\n")
		// Both readings bind a vector, which either can use.
		mustCheck(t, h+"expect "+w+" a is any within 1s as $v\nthen expect position a is $v\nexpect size a is $v\n")
		parseErr(t, h+"expect "+w+" a is 1 2\n", "expected a number")
		parseErr(t, h+"expect "+w+" a is 1 2 x\n", "expected a number")
		parseErr(t, h+"expect "+w+" a changes 1 2 3\n", "changes takes no value")
		parseErr(t, h+"expect "+w+" a face 0 is 1 2 3\n", "expected is, becomes, or changes")
		checkErr(t, h+"expect "+w+" a is any\n", "is any needs as")
		checkErr(t, h+"expect "+w+" a becomes any as $v\n", "any is a reading of is")
		checkErr(t, h+"expect "+w+" a is $nothing\n", "is not bound")
		checkErr(t, h+"expect "+w+" b is 1 2 3\n", "b is not an object")
		checkErr(t, h+"expect "+w+" a link -1 is 1 2 3\n", "link")
	}
	// A position may be negative or zero; a size must be above 0 on each axis.
	mustCheck(t, h+"expect position a is -1.5 0 0\n")
	checkErr(t, h+"expect size a is 0 1 1\n", "size 0 is not above 0")
	checkErr(t, h+"expect size a is 1 -0.5 1\n", "size -0.5 is not above 0")
	checkErr(t, h+"expect size a becomes 1 1 0\n", "size 0 is not above 0")
	// A vector is no colour, a pair or a number.
	checkErr(t, h+"expect position a is any within 1s as $v\nthen expect colour a face 0 is $v\n", "capture type mismatch")
	checkErr(t, h+"expect offset a face 0 is any within 1s as $o\nthen expect position a is $o\n", "capture type mismatch")
	checkErr(t, h+"expect position a is any within 1s as $v\nthen expect rotation a face 0 is $v\n", "capture type mismatch")
	// The words are not reserved: a position or a size is still a name.
	mustCheck(t, "slate 1\nobject position is \"A\"\nobject size is \"B\"\nexpect position position is 1 2 3\nexpect size size changes\n")
}

func TestAWornRootsPositionBecomesANewOffset(t *testing.T) {
	f := newGrid(t)
	wornSign(f)
	f.whenSaid("go", func() { f.changeAfter(t, 40*time.Millisecond, signLocal, withPos(0.25, -0.5, 0.125)) })
	res := play(t, f, hdr+`expect position sign is 0 0 0 within 500ms
say "go" on 0
expect position sign becomes 0.25 -0.5 0.125 within 1s
`)
	wantExit(t, res, 0)
	mustHave(t, res, "position sign 0 0 0", "position sign 0.25 -0.5 0.125")
}

func TestPositionIsWithinAMillimetreOnEachAxis(t *testing.T) {
	f := newGrid(t)
	f.change(signLocal, withPos(10, 20, 30))
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect position sign is 10 20 30 within 300ms\n"), 0)
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect position sign is 10.0009 19.9991 30.0009 within 300ms\n"), 0)
	// 2 mm out on one axis fails, whichever axis, and says what it read.
	for _, lit := range []string{"10.002 20 30", "10 20.002 30", "10 20 29.998"} {
		res := play(t, f, hdr+"say \"go\" on 0\nexpect position sign is "+lit+" within 150ms\n")
		wantExit(t, res, 1)
		mustHave(t, res, "unmatched position sign is "+lit+" within 150ms", "position sign 10 20 30")
	}
}

func TestSizeBecomes(t *testing.T) {
	f := newGrid(t)
	f.change(signLocal, withScale(0.5, 0.25, 0.1))
	f.whenSaid("go", func() { f.changeAfter(t, 40*time.Millisecond, signLocal, withScale(0.8, 0.4, 0.16)) })
	res := play(t, f, hdr+`say "go" on 0
expect size sign becomes 0.8 0.4 0.16 within 1s
`)
	wantExit(t, res, 0)
	mustHave(t, res, "size sign 0.5 0.25 0.1", "size sign 0.8 0.4 0.16")
	// A size already X is not a becomes, and is not a position.
	f = newGrid(t)
	f.change(signLocal, withScale(0.8, 0.4, 0.16))
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect size sign becomes 0.8 0.4 0.16 within 250ms\n"), 1)
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect size sign is 0.8 0.4 0.16 within 250ms\n"), 0)
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect position sign is 0.8 0.4 0.16 within 150ms\n"), 1)
}

func TestPositionAndSizeChange(t *testing.T) {
	f := newGrid(t)
	f.whenSaid("go", func() { f.changeAfter(t, 30*time.Millisecond, signLocal, withPos(1, 2, 3)) })
	res := play(t, f, hdr+`say "go" on 0
expect position sign changes within 1s
expect no size sign changes within 300ms
`)
	wantExit(t, res, 0)
	// A move of half a millimetre is not a change; two are.
	f = newGrid(t)
	f.change(signLocal, withScale(1, 1, 1))
	f.whenSaid("go", func() { f.changeAfter(t, 30*time.Millisecond, signLocal, withScale(1.0005, 1, 1)) })
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect size sign changes within 300ms\n"), 1)
	f = newGrid(t)
	f.change(signLocal, withScale(1, 1, 1))
	f.whenSaid("go", func() { f.changeAfter(t, 30*time.Millisecond, signLocal, withScale(1, 1, 1.002)) })
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect size sign changes within 1s\n"), 0)
	// Nothing moved: the step fails at its deadline.
	res = play(t, newGrid(t), hdr+"say \"go\" on 0\nexpect position sign changes within 200ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "unmatched position sign changes within 200ms")
}

func TestAChildsPositionIsRelativeToItsRoot(t *testing.T) {
	f, _, lid, _ := storeWorld(t)
	f.change(201, withPos(0, 0.5, 0.25))
	f.whenSaid("go", func() { f.changeAfter(t, 30*time.Millisecond, 201, withPos(0, 0.5, 0.75)) })
	res := play(t, f, hdr+`expect position vendor link 2 is 0 0.5 0.25 within 500ms
say "go" on 0
expect position vendor link 2 becomes 0 0.5 0.75 within 1s
expect no position vendor link 3 changes within 200ms
`)
	wantExit(t, res, 0)
	mustHave(t, res, "position vendor link 2 0 0.5 0.75")
	if lid.Parent != vendorLocal {
		t.Fatalf("the lid hangs off %d", lid.Parent)
	}
	// The root's own position is not the child's.
	f, vendor, _, _ := storeWorld(t)
	vendor.Position = msg.Vector3{X: 100, Y: 100, Z: 100}
	f.change(201, withPos(1, 2, 3))
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect position vendor link 2 is 1 2 3 within 300ms\n"), 0)
	wantExit(t, play(t, f, hdr+"say \"go\" on 0\nexpect position vendor link 2 is 101 102 103 within 150ms\n"), 1)
}

func TestARezzedObjectsPositionIsItsRegionPosition(t *testing.T) {
	f := newGrid(t)
	f.whenSaid("go", func() { f.appear(balloonAt(idRootA, 201, 130)) })
	res := play(t, f, hdr+rezBalloon+"then\nexpect position balloon is 130 133 25 within 1s\nexpect size balloon is any within 1s as $s\n")
	wantExit(t, res, 0)
	mustHave(t, res, "position balloon 130 133 25", "capture $s = ")
}

func TestPositionAndSizeCapturesAndOriginal(t *testing.T) {
	f := newGrid(t)
	wornSign(f)
	f.change(signLocal, withPos(0.5, 0.25, 0))
	f.change(signLocal, withScale(0.5, 0.25, 0.1))
	moves(t, f, map[string]func(*sl.Seen){"go": withPos(0.75, 0.25, 0), "back": withPos(0.5, 0.25, 0)})
	res := play(t, f, hdr+`expect position sign is any within 500ms as $p
expect size sign is any within 500ms as $s
say "go" on 0
expect position sign changes within 1s
expect no size sign becomes $s within 150ms
say "back" on 0
expect position sign becomes $p within 1s
expect position sign is original within 500ms
`)
	wantExit(t, res, 0)
	mustHave(t, res, "capture $p = 0.5 0.25 0 (step 1)", "capture $s = 0.5 0.25 0.1 (step 1)")
	// original is a becomes too: it has to leave and come back.
	f = newGrid(t)
	f.change(signLocal, withPos(1, 1, 1))
	moves(t, f, map[string]func(*sl.Seen){"go": withPos(2, 2, 2), "back": withPos(1, 1, 1)})
	res = play(t, f, hdr+`say "go" on 0
expect position sign changes within 1s
say "back" on 0
expect position sign becomes original within 1s
`)
	wantExit(t, res, 0)
}

func TestAPositionTheStoreNeverHadForALinkIsAFailure(t *testing.T) {
	f := newGrid(t)
	res := play(t, f, hdr+"say \"go\" on 0\nexpect position sign link 9 is 0 0 0 within 200ms\n")
	wantExit(t, res, 1)
	if !strings.Contains(res.Transcript, "has no link 9") {
		t.Errorf("no sentence for the missing link:\n%s", res.Transcript)
	}
	// A negative needs a reading as every state expectation does.
	res = play(t, f, hdr+"say \"go\" on 0\nexpect no position sign link 9 changes within 200ms\n")
	wantExit(t, res, 1)
}
