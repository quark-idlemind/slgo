package slate

// The floating text reading: a prim's Seen.Text, compared exactly, as a
// literal, a pattern, a capture or original.
// Why: doc/slate-runner.md#floating-text

import (
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/sl"
)

func withText(s string) func(*sl.Seen) { return func(o *sl.Seen) { o.Text = s } }

func TestFloatingTextParseAndCheck(t *testing.T) {
	const h = "slate 1\nobject a is \"Example Sign\"\n"
	mustCheck(t, h+"expect text a changes\n")
	mustCheck(t, h+"expect text a is \"Ready\"\n")
	mustCheck(t, h+"expect text a becomes \"Controlling a\" within 2s\n")
	mustCheck(t, h+"expect text a link 2 becomes \"x\"\n")
	mustCheck(t, h+"expect text a becomes matching \"^Controlling (?P<dev>.+)$\" within 2s\nthen expect text a is $dev\n")
	mustCheck(t, h+"expect text a becomes original\n")
	mustCheck(t, h+"expect no text a changes within 1s\n")
	mustCheck(t, h+"expect text a is any within 1s as $t\nthen expect text a becomes $t\n")
	// The text of a reading is text, and is usable where text is.
	mustCheck(t, h+"expect text a is any within 1s as $t\nthen expect say $t on public from object a within 1s\n")
	parseErr(t, h+"expect text a is 3\n", "expected a string")
	parseErr(t, h+"expect text a face 0 is \"x\"\n", "expected is, becomes, or changes")
	parseErr(t, h+"expect text a is matching $x\n", "a capture is not a pattern")
	checkErr(t, h+"expect text a is matching \"(\"\n", "pattern")
	checkErr(t, h+"expect text a is any\n", "is any needs as")
	checkErr(t, h+"expect text a becomes any as $t\n", "any is a reading of is")
	checkErr(t, h+"expect text a is $nothing\n", "is not bound")
	checkErr(t, h+"expect text b is \"x\"\n", "b is not an object")
	checkErr(t, h+"expect text a link -1 is \"x\"\n", "link")
	// A reading of another type is no text, and text is no other type.
	checkErr(t, h+"expect position a is any within 1s as $v\nthen expect text a is $v\n", "capture type mismatch")
	checkErr(t, h+"expect text a is any within 1s as $t\nthen expect position a is $t\n", "capture type mismatch")
	checkErr(t, h+"expect text a is any within 1s as $t\nthen expect texture a face 0 is $t\n", "capture type mismatch")
	// A group of a pattern binds text, once.
	checkErr(t, h+"expect text a is matching \"(?P<d>x)\" within 1s as $d\n", "already bound")
	// The word is not reserved.
	mustCheck(t, "slate 1\nobject text is \"A\"\nexpect text text changes\n")
}

func TestFloatingTextBecomesAndMatching(t *testing.T) {
	f := newGrid(t)
	f.whenSaid("go", func() { f.changeAfter(t, 40*time.Millisecond, signLocal, withText("Controlling Example Chair")) })
	res := play(t, f, hdr+`say "go" on 0
expect text sign becomes matching "^Controlling (?P<dev>.+)$" within 1s as $all
then expect text sign is $all within 300ms
expect text sign becomes "Controlling Example Chair" within 300ms
`)
	// The second becomes cannot see a change after the first: it fails.
	wantExit(t, res, 1)
	mustHave(t, res, `text sign ""`, `text sign "Controlling Example Chair"`,
		`capture $dev = "Example Chair"`, `capture $all = "Controlling Example Chair"`)

	f = newGrid(t)
	f.whenSaid("go", func() { f.changeAfter(t, 40*time.Millisecond, signLocal, withText("Controlling Example Chair")) })
	res = play(t, f, hdr+`say "go" on 0
expect text sign becomes "Controlling Example Chair" within 1s
then expect text sign is "Controlling Example Chair" within 300ms
expect text sign is matching "Chair$" within 300ms
`)
	wantExit(t, res, 0)
}

func TestFloatingTextChangesAndCapture(t *testing.T) {
	f := newGrid(t)
	f.change(signLocal, withText("Idle"))
	moves(t, f, map[string]func(*sl.Seen){"go": withText("Busy"), "back": withText("Idle")})
	res := play(t, f, hdr+`expect text sign is any within 500ms as $t
say "go" on 0
expect text sign changes within 1s
expect no text sign becomes $t within 150ms
say "back" on 0
expect text sign becomes $t within 1s
expect text sign becomes original within 500ms
`)
	wantExit(t, res, 0)
	mustHave(t, res, `text sign "Idle"`, `text sign "Busy"`, `capture $t = "Idle" (step 1)`)

	f = newGrid(t)
	f.change(signLocal, withText("Idle"))
	moves(t, f, map[string]func(*sl.Seen){"go": withText("Busy"), "back": withText("Idle")})
	res = play(t, f, hdr+`say "go" on 0
expect text sign changes within 1s
say "back" on 0
expect text sign becomes original within 1s
`)
	wantExit(t, res, 0)
}

func TestFloatingTextOfALink(t *testing.T) {
	f, _, _, _ := storeWorld(t)
	f.whenSaid("go", func() { f.changeAfter(t, 30*time.Millisecond, 201, withText("Lid open")) })
	res := play(t, f, hdr+`say "go" on 0
expect text vendor link 2 becomes "Lid open" within 1s
expect no text vendor link 3 changes within 200ms
`)
	wantExit(t, res, 0)
	mustHave(t, res, `text vendor link 2 "Lid open"`)
}

func TestFloatingTextThatNeverComesFailsWithTheReading(t *testing.T) {
	f := newGrid(t)
	f.change(signLocal, withText("Idle"))
	res := play(t, f, hdr+"say \"go\" on 0\nexpect text sign becomes \"Busy\" within 200ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, `unmatched text sign becomes "Busy" within 200ms`, `text sign "Idle"`)
	// A pattern that never matches fails the same way.
	res = play(t, f, hdr+"say \"go\" on 0\nexpect text sign is matching \"^Busy\" within 150ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, `text sign "Idle"`)
}

func TestFloatingTextIsQuotedInTheTranscript(t *testing.T) {
	f := newGrid(t)
	text := "Said \"hi\"\nsigned-off"
	f.whenSaid("go", func() { f.changeAfter(t, 30*time.Millisecond, signLocal, withText(text)) })
	res := play(t, f, hdr+"say \"go\" on 0\nexpect text sign becomes matching \"hi\" within 1s\n")
	wantExit(t, res, 0)
	mustHave(t, res, `text sign "Said \"hi\"\nsigned-off"`)
	// The newline cannot start a line of its own.
	for _, l := range strings.Split(res.Transcript, "\n") {
		if strings.HasPrefix(l, "signed-off") {
			t.Errorf("the newline made a line of its own:\n%s", res.Transcript)
		}
	}
}
