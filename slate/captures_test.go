package slate

import (
	"regexp"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// Captured values on the fake grid: what binds them, what uses them and
// what a failure says. The forms of dialog clauses, choose, face
// properties and showing are still not run, and wave1_test.go says so.

// signTalks makes the sign say a line for each trigger the tester says.
// It keeps whatever answers the grid already has, the simulated probes
// of world among them.
func signTalks(f *fakeGrid, lines map[string]string) {
	f.mu.Lock()
	prev := f.onSend
	f.mu.Unlock()
	f.replyTo(func(m msg.Message) {
		if prev != nil {
			prev(m)
		}
		if text, _, ok := says(m); ok {
			if line, ok := lines[text]; ok {
				f.relay(signSays(line))
			}
		}
	})
}

func TestANamedGroupBindsAndALaterSayExpectsThatText(t *testing.T) {
	f := newGrid(t)
	signTalks(f, map[string]string{"go": "Now showing Example Tile", "again": "Example Tile"})
	res := play(t, f, hdr+`say "go" on 0
expect say matching "^Now showing (?P<what>.+)$" on public from object sign within 500ms
say "again" on 0
expect say $what on public from object sign within 500ms
`)
	wantExit(t, res, 0)
	mustHave(t, res, `capture $what = "Example Tile" (step 1)`, "slate: pass step 2")
	if !regexp.MustCompile(`(?m)^\d\d:\d\d:\d\d\.\d{3} capture \$what = `).MatchString(res.Transcript) {
		t.Errorf("the binding line has no time:\n%s", res.Transcript)
	}
	// A capture is compared whole: a line that only contains it is not it.
	f = newGrid(t)
	signTalks(f, map[string]string{"go": "Now showing Example Tile", "again": "an Example Tile here"})
	res = play(t, f, hdr+`say "go" on 0
expect say matching "^Now showing (?P<what>.+)$" on public from object sign within 500ms
say "again" on 0
expect say $what on public from object sign within 100ms
`)
	wantExit(t, res, 1)
}

func TestAsBindsTheReadingOfSayDialogGiveAndTexture(t *testing.T) {
	f := newGrid(t)
	f.withInventory(t)
	f.offer(idOffer, idNewCopy, "Example Thank You", 10)
	f.replyTo(func(m msg.Message) {
		text, _, _ := says(m)
		switch text {
		case "hi":
			f.relay(signSays("hello there"))
		case "menu":
			f.relay(dialogMsg(idVendor, "Example Tip Jar", "Choose a tile", -4242, "Red", "Blue"))
		case "buy":
			f.relay(offerIM(idStranger, "Example Tip Jar", idOffer, "Example Thank You", 10))
		case "again":
			f.relay(signSays("hello there"))
			f.relay(signSays("Choose a tile"))
			f.relay(signSays("Example Thank You"))
		}
	})
	signAt(f, idTexA)
	res := play(t, f, hdr+`say "hi" on 0
expect say matching "^hello" on public from object sign within 500ms as $line
say "menu" on 0
expect dialog from vendor text matching "^Choose" button "Red" within 500ms as $msg
say "buy" on 0
expect give "Example Thank You" from vendor within 1s as $item
expect texture sign face 0 is `+idTexA.String()+` within 500ms as $tex
say "again" on 0
expect say $line on public from object sign within 500ms
expect say $msg on public from object sign within 500ms
expect say $item on public from object sign within 500ms
expect texture sign face 0 is $tex within 500ms
`)
	wantExit(t, res, 0)
	mustHave(t, res,
		`capture $line = "hello there" (step 1)`,
		`capture $msg = "Choose a tile" (step 2)`,
		`capture $item = "Example Thank You" (step 3)`,
		"capture $tex = "+idTexA.String()+" (step 3)")
}

func TestIsAnyCapturesAFaceAndAnotherFaceBecomesIt(t *testing.T) {
	f := newGrid(t)
	f.change(signLocal, withTexture(2, idTexA))
	f.whenSaid("go", func() { f.change(signLocal, withTexture(0, idTexA)) })
	res := play(t, f, hdr+`expect texture sign face 2 is any within 500ms as $tile
say "go" on 0
expect texture sign face 0 becomes $tile within 1s
`)
	wantExit(t, res, 0)
	mustHave(t, res, "capture $tile = "+idTexA.String()+" (step 1)")

	// Offsets, repeats, rotation and click bind their own types.
	f = newGrid(t)
	f.change(signLocal, withFace(1, func(fc *sl.Face) { fc.OffsetS, fc.OffsetT = 8192, 16384 }))
	f.change(signLocal, withClick(0))
	res = play(t, f, hdr+`expect offset sign face 1 is any within 500ms as $o
expect rotation sign face 1 is any within 500ms as $r
expect click sign is any within 500ms as $c
then expect offset sign face 1 is $o within 500ms
expect rotation sign face 1 is $r within 500ms
expect click sign is $c within 500ms
`)
	wantExit(t, res, 0)
	mustHave(t, res, "capture $o = ", "capture $r = 0 (step 1)", "capture $c = 0 (step 1)")
}

func TestAGroupThatDidNotTakePartFailsTheStepWithItsSentence(t *testing.T) {
	f := newGrid(t)
	signTalks(f, map[string]string{"go": "ac"})
	res := play(t, f, hdr+`say "go" on 0
expect say matching "^a(?P<opt>b)?(?P<c>c)$" on public from object sign within 500ms
`)
	wantExit(t, res, 1)
	mustHave(t, res, "  failed: $opt did not take part in the match", "captured: none")
	mustNotHave(t, res, "capture $c =")
}

func TestAnEmptyCaptureAsAButtonPartFailsBeforeTheTouch(t *testing.T) {
	f := newGrid(t)
	signTalks(f, map[string]string{"go": "Name: "})
	res := play(t, f, hdr+`say "go" on 0
expect say matching "^Name: (?P<first>.*)$" on public from object sign within 500ms
touch sign button text $first
`)
	wantExit(t, res, 1)
	mustHave(t, res, `slate: step 2: $first is "" and a button part needs text`, `captured: $first ""`)
	if g := sentOf[*msg.ObjectGrab](f); len(g) != 0 {
		t.Errorf("%d touches sent", len(g))
	}
}

func TestASendTextThatBreaksTheRulesFailsBeforeAnythingIsSpoken(t *testing.T) {
	long := strings.Repeat("a", 1100)
	for _, c := range []struct{ line, want string }{
		{"héllo", `$w is "h` + "é" + `llo" and link text must be bytes 0x20-0x7E, tab or newline`},
		{long, `$w is "` + long + `" and the line to the bridge is `},
	} {
		f, o := world(t)
		signTalks(f, map[string]string{"go": c.line})
		res := o.play(t, probeHdr+`say "go" on 0
expect say matching "^(?P<w>.+)$" on public from object sign within 500ms
send on vendor from link 1 to link 2 num 7 text $w
`)
		wantExit(t, res, 1)
		mustHave(t, res, "slate: step 2: "+c.want)
		for _, s := range f.said() {
			if strings.Contains(s, " send ") {
				t.Errorf("a command was spoken: %.80q", s)
			}
		}
	}
}

func TestASendTextAndKeyCaptureAreSentAsTheyAre(t *testing.T) {
	f, o := world(t)
	signTalks(f, map[string]string{"go": "a \"b\" c"})
	f.change(signLocal, withTexture(0, idTexA))
	res := o.play(t, probeHdr+`say "go" on 0
expect say matching "^(?P<w>.+)$" on public from object sign within 500ms
expect texture sign face 0 is any within 500ms as $k
send on vendor from link 1 to link 2 num 7 text $w key $k
expect link on vendor from link 1 num 7 text $w key $k within 1s
`)
	wantExit(t, res, 0)
	found := false
	for _, s := range f.said() {
		found = found || strings.Contains(s, " send ") && strings.Contains(s, idTexA.String()) && strings.HasSuffix(s, `"a \"b\" c"`)
	}
	if !found {
		t.Errorf("no send line with the capture: %q", f.said())
	}
}

func TestTheFailureBlockListsTheCapturesInBindingOrder(t *testing.T) {
	f := newGrid(t)
	f.change(signLocal, withTexture(0, idTexA))
	signTalks(f, map[string]string{"go": "Example Sign"})
	res := play(t, f, hdr+`say "go" on 0
expect say matching "^(?P<first>.+)$" on public from object sign within 500ms
expect texture sign face 0 is any within 500ms as $tile
expect say "never" on public from object sign within 100ms
`)
	wantExit(t, res, 1)
	mustHave(t, res,
		`capture $first = "Example Sign" (step 1)`,
		"  payment: none",
		`  captured: $first "Example Sign", $tile `+idTexA.String())
	// The line follows payment and ends the block.
	ls := lines(res)
	for i, l := range ls {
		if l == "  payment: none" && ls[i+1] != `  captured: $first "Example Sign", $tile `+idTexA.String() {
			t.Errorf("line after payment: %q", ls[i+1])
		}
	}
}

func TestCapturesDoNotLeakIntoTheNextTest(t *testing.T) {
	f := newGrid(t)
	signTalks(f, map[string]string{"go": "Example Sign"})
	res := play(t, f, hdr+`test "one" {
  say "go" on 0
  expect say matching "^(?P<first>.+)$" on public from object sign within 500ms
}
test "two" {
  expect say "never" on public from object sign within 100ms
}
`)
	wantExit(t, res, 1)
	mustHave(t, res, "captured: none")
	mustNotHave(t, res, "captured: $first")
}

func TestACaptureFromBeforeEachIsUsedInTheBodyAndAfterEach(t *testing.T) {
	f := newGrid(t)
	signTalks(f, map[string]string{"go": "Example Sign", "again": "Example Sign"})
	res := play(t, f, hdr+`before each {
  say "go" on 0
  expect say matching "^(?P<n>.+)$" on public from object sign within 500ms
}
test "t" {
  say "again" on 0
  expect say $n on public from object sign within 500ms
}
after each {
  say "again" on 0
  expect say $n on public from object sign within 500ms
}
`)
	wantExit(t, res, 0)
	mustHave(t, res, `capture $n = "Example Sign" (step 1)`, "slate: pass step 3")
	if n := strings.Count(res.Transcript, "capture $n"); n != 1 {
		t.Errorf("%d binding lines, want 1\n%s", n, res.Transcript)
	}
}

func TestARezNameGroupBindsTheRootsName(t *testing.T) {
	f := newGrid(t)
	f.whenSaid("go", func() { f.appear(balloonAt(idRootA, 201, 130)) })
	res := play(t, f, hdr+`say "go" on 0
expect rez name matching "^(?P<kind>Example) Balloon$" from vendor as balloon within 1s
`)
	wantExit(t, res, 0)
	mustHave(t, res, `capture $kind = "Example" (step 1)`)
}

func TestAnUnboundUseIsAnInternalErrorNotAPanic(t *testing.T) {
	s := mustParse(t, hdr+"expect say $nope on public from anyone within 100ms\n")
	st := &stepRun{t: &testRun{caps: map[string]*capValue{}}, n: 1}
	_, err := st.capture(s.Tests[0].Steps[0].Expect[0].Say.Text.Capture, CapText)
	if err == nil || !strings.Contains(err.Error(), "$nope is not bound at step 1") {
		t.Fatalf("got %v", err)
	}
	st.t.caps["nope"] = &capValue{typ: CapUUID}
	if _, err := st.capture(s.Tests[0].Steps[0].Expect[0].Say.Text.Capture, CapText); err == nil || !strings.Contains(err.Error(), "holds uuid") {
		t.Fatalf("got %v", err)
	}
}
