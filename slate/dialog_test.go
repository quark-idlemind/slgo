package slate

import (
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// menu makes the vendor open a dialog when the tester says "menu".
func (f *fakeGrid) menu(channel int32, message string, buttons ...string) {
	f.on("menu", dialogMsg(idVendor, "Example Tip Jar", message, channel, buttons...))
}

func TestDialogOnlyAndExtraButtons(t *testing.T) {
	for _, c := range []struct {
		expect string
		want   bool
	}{
		{`button "Red" button "Blue"`, true},
		{`button "red " button "BLUE"`, true}, // ignoring case and space
		{`button "Red"`, true},                // extras are allowed without only
		{`button "Red" button "Blue" only`, true},
		{`button "Red" only`, false},           // only rejects an extra button
		{`button "Red" button "Green"`, false}, // every listed button must be there
		{`button "Red" button "Red" only`, false},
	} {
		f := newGrid(t)
		f.menu(-4242, "Choose", "Red", "Blue")
		res := play(t, f, hdr+"say \"menu\" on 0\nexpect dialog from vendor text \"Choose\" "+c.expect+" within 150ms\n")
		if got := res.Exit == 0; got != c.want {
			t.Errorf("%s: passed = %v, want %v\n%s", c.expect, got, c.want, res.Transcript)
		}
	}
	f := newGrid(t)
	f.menu(-4242, "Choose a colour", "Red", "Blue")
	res := play(t, f, hdr+"say \"menu\" on 0\nexpect dialog from vendor text matching \"^Choose\" button \"Red\" within 150ms\n")
	wantExit(t, res, 0)
	mustHave(t, res, `dialog from vendor: "Choose a colour" buttons "Red" "Blue"`)
	f = newGrid(t)
	f.menu(-4242, "Choose a colour", "Red", "Blue")
	wantExit(t, play(t, f, hdr+"say \"menu\" on 0\nexpect dialog from vendor text \"Choose\" button \"Red\" within 100ms\n"), 1)
}

func TestADialogIsMatchedByObjectIDOrName(t *testing.T) {
	// The id of the bound prim, or its name when the id is another prim's.
	f := newGrid(t)
	f.on("menu", dialogMsg(idStray, "Example Tip Jar", "Hi", -1, "A"))
	wantExit(t, play(t, f, hdr+"say \"menu\" on 0\nexpect dialog from vendor text \"Hi\" button \"A\" within 100ms\n"), 0)
	// Another object with another name is not the vendor's.
	f = newGrid(t)
	f.on("menu", dialogMsg(idStray, "Example Other", "Hi", -1, "A"))
	wantExit(t, play(t, f, hdr+"say \"menu\" on 0\nexpect dialog from vendor text \"Hi\" button \"A\" within 100ms\n"), 1)
}

func TestATextBoxIsNotADialog(t *testing.T) {
	box := dialogMsg(idVendor, "Example Tip Jar", "Name?", -99, "!!llTextBox!!")

	f := newGrid(t)
	f.on("menu", box)
	res := play(t, f, hdr+"say \"menu\" on 0\nexpect textbox from vendor text \"Name?\" within 150ms\n")
	wantExit(t, res, 0)
	mustHave(t, res, `textbox from vendor: "Name?"`)

	f = newGrid(t)
	f.on("menu", box)
	wantExit(t, play(t, f, hdr+"say \"menu\" on 0\nexpect dialog from vendor text \"Name?\" button \"x\" within 100ms\n"), 1)

	f = newGrid(t)
	f.menu(-4242, "Name?", "Red")
	wantExit(t, play(t, f, hdr+"say \"menu\" on 0\nexpect textbox from vendor text \"Name?\" within 100ms\n"), 1)
}

func TestNegativeDialogAndTextBox(t *testing.T) {
	f := newGrid(t)
	f.menu(-4242, "Choose", "Red")
	res := play(t, f, hdr+"say \"menu\" on 0\nexpect no dialog from vendor text \"Choose\" button \"Red\" within 300ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, `forbidden no dialog from vendor text "Choose" button "Red" within 300ms`)

	f = newGrid(t)
	t0 := time.Now()
	res = play(t, f, hdr+"say \"menu\" on 0\nexpect no textbox from vendor text \"Name?\" within 200ms\n")
	wantExit(t, res, 0)
	if time.Since(t0) < 200*time.Millisecond {
		t.Error("a negative textbox did not hold its window")
	}
}

// D1: the dialog matched in one step is held for the next, which is a
// stimulus and so a step of its own.
func TestChooseOnAHoldFromThePreviousStep(t *testing.T) {
	f := newGrid(t)
	f.menu(-4242, "Choose", "Red", "Blue")
	res := play(t, f, hdr+`
say "menu" on 0
expect dialog from vendor text "Choose" button "Red" button "Blue" within 200ms

choose " blue" on vendor
expect no say "x" on public from anyone within 100ms
`)
	wantExit(t, res, 0)
	r := f.replies()
	if len(r) != 1 {
		t.Fatalf("replies = %d", len(r))
	}
	d := r[0].Data
	if d.ObjectID != idVendor || d.ChatChannel != -4242 || d.ButtonIndex != 1 || strings.TrimRight(string(d.ButtonLabel), "\x00") != "Blue" {
		t.Errorf("reply = %+v", d)
	}
}

func TestChooseWithoutAHoldAfterTheTestEndedOrConsumed(t *testing.T) {
	// The hold of the first test is dropped when it ends, and forgotten.
	f := newGrid(t)
	f.menu(-4242, "Choose", "Red")
	src := hdr + `
test "one" {
  say "menu" on 0
  expect dialog from vendor text "Choose" button "Red" within 200ms
}
test "two" {
  choose "Red" on vendor
}
`
	s := mustCheck(t, src)
	sess := f.session(t)
	res, err := run(t.Context(), sess, s, Options{}, testCfg())
	if err != nil {
		t.Fatal(err)
	}
	wantExit(t, res, 1)
	if !res.Tests[0].Passed || res.Tests[1].Passed {
		t.Fatalf("tests = %+v", res.Tests)
	}
	mustHave(t, res, "no dialog is held for vendor", `slate: fail t.slate test "two" step 1`)
	if len(f.replies()) != 0 {
		t.Error("a choose with nothing held sent a reply")
	}
	if left := sess.Dialogs(); len(left) != 0 {
		t.Errorf("the session still holds %d dialogs after the test ended", len(left))
	}

	// Consumed by a choose, the hold is gone for a second one.
	f = newGrid(t)
	f.menu(-4242, "Choose", "Red")
	res = play(t, f, hdr+"say \"menu\" on 0\nexpect dialog from vendor text \"Choose\" button \"Red\" within 200ms\nchoose \"Red\" on vendor\nchoose \"Red\" on vendor\n")
	wantExit(t, res, 1)
	mustHave(t, res, "slate: pass step 2", "no dialog is held for vendor", "step 3")
	if len(f.replies()) != 1 {
		t.Errorf("replies = %d, want one", len(f.replies()))
	}
}

func TestAnUnconsumedHoldIsReportedOnTheFailedTest(t *testing.T) {
	f := newGrid(t)
	f.menu(-4242, "Choose", "Red", "Blue")
	sess := f.session(t)
	s := mustCheck(t, hdr+"say \"menu\" on 0\nexpect dialog from vendor text \"Choose\" button \"Red\" within 200ms\nsay \"x\" on 0\nexpect say \"never\" on public from anyone within 100ms\n")
	res, err := run(t.Context(), sess, s, Options{}, testCfg())
	if err != nil {
		t.Fatal(err)
	}
	wantExit(t, res, 1)
	mustHave(t, res, `dialog left unanswered: [Object] "Example Tip Jar" "Choose" buttons "Red" "Blue"`)
	if len(sess.Dialogs()) != 0 {
		t.Error("the unanswered dialog was not forgotten")
	}
}

func TestALaterDialogReplacesTheHold(t *testing.T) {
	f := newGrid(t)
	f.on("one", dialogMsg(idVendor, "Example Tip Jar", "First", -1, "A"))
	f.replyTo(func(m msg.Message) {
		text, _, ok := says(m)
		if !ok {
			return
		}
		switch text {
		case "one":
			f.relay(dialogMsg(idVendor, "Example Tip Jar", "First", -11, "A"))
		case "two":
			f.relay(dialogMsg(idVendor, "Example Tip Jar", "Second", -22, "B"))
		}
	})
	res := play(t, f, hdr+`
say "one" on 0
expect dialog from vendor text "First" button "A" within 200ms
say "two" on 0
expect dialog from vendor text "Second" button "B" within 200ms
choose "B" on vendor
`)
	wantExit(t, res, 0)
	if r := f.replies(); len(r) != 1 || r[0].Data.ChatChannel != -22 {
		t.Errorf("replies = %+v", r)
	}
}

func TestAnswerOnATextBox(t *testing.T) {
	f := newGrid(t)
	f.on("menu", dialogMsg(idVendor, "Example Tip Jar", "Name?", -99, "!!llTextBox!!"))
	res := play(t, f, hdr+"say \"menu\" on 0\nexpect textbox from vendor text \"Name?\" within 200ms\nanswer \"Example\" on vendor\n")
	wantExit(t, res, 0)
	r := f.replies()
	if len(r) != 1 || r[0].Data.ChatChannel != -99 || strings.TrimRight(string(r[0].Data.ButtonLabel), "\x00") != "Example" {
		t.Errorf("replies = %+v", r)
	}

	// choose on a text box, and answer on a dialog, fail before sending.
	f = newGrid(t)
	f.on("menu", dialogMsg(idVendor, "Example Tip Jar", "Name?", -99, "!!llTextBox!!"))
	res = play(t, f, hdr+"say \"menu\" on 0\nexpect textbox from vendor text \"Name?\" within 200ms\nchoose \"Red\" on vendor\n")
	wantExit(t, res, 1)
	mustHave(t, res, "is a text box; use answer")

	f = newGrid(t)
	f.menu(-4242, "Choose", "Red", "Blue")
	res = play(t, f, hdr+"say \"menu\" on 0\nexpect dialog from vendor text \"Choose\" button \"Red\" within 200ms\nanswer \"x\" on vendor\n")
	wantExit(t, res, 1)
	mustHave(t, res, `is not a text box; its buttons are "Red" "Blue"`)
	if len(f.replies()) != 0 {
		t.Error("a refused answer was sent")
	}
}

func TestTwoButtonsFoldingToOneLabelFail(t *testing.T) {
	f := newGrid(t)
	f.menu(-4242, "Choose", "Red", "red ", "Blue")
	res := play(t, f, hdr+"say \"menu\" on 0\nexpect dialog from vendor text \"Choose\" button \"Blue\" within 200ms\nchoose \"RED\" on vendor\n")
	wantExit(t, res, 1)
	mustHave(t, res, `2 buttons are called "RED" ("Red" "red ")`)
	if len(f.replies()) != 0 {
		t.Error("an ambiguous choose pressed a button")
	}

	f = newGrid(t)
	f.menu(-4242, "Choose", "Red")
	res = play(t, f, hdr+"say \"menu\" on 0\nexpect dialog from vendor text \"Choose\" button \"Red\" within 200ms\nchoose \"Green\" on vendor\n")
	wantExit(t, res, 1)
	mustHave(t, res, `"Green" is not one of the buttons of the dialog held for vendor: "Red"`)
}

func TestADialogOfAnEarlierStepIsNotEligibleAgain(t *testing.T) {
	// The dialog was observed before the second step's arm point.
	f := newGrid(t)
	f.menu(-4242, "Choose", "Red")
	res := play(t, f, hdr+`
say "menu" on 0
expect dialog from vendor text "Choose" button "Red" within 200ms
say "again" on 0
expect dialog from vendor text "Choose" button "Red" within 100ms
`)
	wantExit(t, res, 1)
	mustHave(t, res, "slate: pass step 1", "step 2")
}
