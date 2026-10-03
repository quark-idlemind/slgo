package slate

import (
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

// openDialog plays one expectation against a vendor dialog with these buttons.
func openDialog(t *testing.T, message string, buttons []string, expect string) *Result {
	t.Helper()
	f := newGrid(t)
	f.menu(-4242, message, buttons...)
	return play(t, f, hdr+"say \"menu\" on 0\nexpect dialog from vendor "+expect+" within 150ms\n")
}

func TestOpenDialogClauses(t *testing.T) {
	ab := []string{"a", "b"}
	for _, c := range []struct {
		name    string
		buttons []string
		expect  string
		want    bool
		detail  string
	}{
		{"no text clause", ab, `button "a"`, true, ""},
		{"no text, no button", ab, ``, true, ""},
		{"matching", []string{"Red 1", "Blue"}, `text "m" button matching "^Red"`, false, ""},
		{"pinned", []string{"x", "y", "z"}, `button 2 "y"`, true, ""},
		{"pinned wrong", []string{"x", "y", "z"}, `button 2 "z"`, false, `clauses could not all be assigned: button 2 "z" has no button`},
		{"pinned past end", ab, `button 3 matching "."`, false, `button 3 matching "." has no button`},
		{"count exact", ab, `count 2`, true, ""},
		{"count wrong", ab, `count 3`, false, "count 3 but the dialog has 2 buttons"},
		{"bipartite", ab, `button matching "a|b" button "a" only`, true, ""},
		{"bipartite not greedy", ab, `button matching "a|b" button "a"`, true, ""},
		{"two identical literals need two", ab, `button "a" button "a"`, false, `button "a" has no button of its own`},
		{"two identical literals, two buttons", []string{"a", " A "}, `button "a" button "a" only`, true, ""},
		{"only claims all", []string{"a", "b", "Cancel"}, `button "a" button "b" only`, false, `only: "Cancel" is not claimed by a clause`},
		{"only fewer buttons", []string{"a"}, `button "a" button "b" only`, false, "has no button"},
	} {
		text := `text "m" `
		if strings.Contains(c.name, "no text") {
			text = ""
		}
		if c.name == "matching" {
			text = ""
		}
		res := openDialog(t, "m", c.buttons, text+c.expect)
		if c.name == "matching" {
			c.want = true
		}
		if got := res.Exit == 0; got != c.want {
			t.Errorf("%s: passed = %v, want %v\n%s", c.name, got, c.want, res.Transcript)
		}
		if c.detail != "" {
			mustHave(t, res, c.detail)
		}
	}
	// A message of any text matches without a text clause.
	res := openDialog(t, "anything at all", ab, `button "b"`)
	wantExit(t, res, 0)
	// A text box never matches expect dialog, with or without text.
	f := newGrid(t)
	f.on("menu", dialogMsg(idVendor, "Example Tip Jar", "Name?", -99, "!!llTextBox!!"))
	wantExit(t, play(t, f, hdr+"say \"menu\" on 0\nexpect dialog from vendor within 100ms\n"), 1)
}

func TestOpenDialogCaptureAndChoose(t *testing.T) {
	f := newGrid(t)
	f.menu(-4242, "Pick", "Cancel", "Item 7", "Item 8")
	res := play(t, f, hdr+`
say "menu" on 0
expect dialog from vendor button matching "^Item (?P<n>[0-9])$" button "Cancel" within 200ms

expect no say "x" on public from anyone within 100ms
`)
	wantExit(t, res, 0)
	mustHave(t, res, `capture $n = "7"`)

	// The label a capture holds is pressed under the literal rules.
	f = newGrid(t)
	f.menu(-4242, "Pick", "Cancel", "Item 7", "Item 8")
	res = play(t, f, hdr+`
say "menu" on 0
expect dialog from vendor button matching "^(?P<x>Item 8)$" within 200ms

choose $x on vendor
expect no say "x" on public from anyone within 100ms
`)
	wantExit(t, res, 0)
	if r := f.replies(); len(r) != 1 || r[0].Data.ButtonIndex != 2 {
		t.Errorf("replies = %+v", r)
	}

	// The capture is also an exact literal clause of a later dialog.
	f = newGrid(t)
	f.replyTo(func(m msg.Message) {
		switch text, _, _ := says(m); text {
		case "menu":
			f.relay(dialogMsg(idVendor, "Example Tip Jar", "Pick", -4242, "Cancel", "Item 7"))
		case "again":
			f.relay(dialogMsg(idVendor, "Example Tip Jar", "Again", -4242, "item 7"))
		}
	})
	wantExit(t, play(t, f, hdr+`
say "menu" on 0
expect dialog from vendor button matching "^(?P<x>Item 7)$" within 200ms

say "again" on 0
expect dialog from vendor text "Again" button $x only within 200ms
`), 0)
}

func chooseRun(t *testing.T, buttons []string, choose string) (*Result, *fakeGrid) {
	t.Helper()
	f := newGrid(t)
	f.menu(-4242, "Pick", buttons...)
	res := play(t, f, hdr+"say \"menu\" on 0\nexpect dialog from vendor within 200ms\n\n"+choose+"\nexpect no say \"x\" on public from anyone within 100ms\n")
	return res, f
}

func TestChooseButtonAndMatching(t *testing.T) {
	bs := []string{"a", "b", "c", "d"}
	res, f := chooseRun(t, bs, "choose button 3 on vendor")
	wantExit(t, res, 0)
	if r := f.replies(); len(r) != 1 || r[0].Data.ButtonIndex != 2 || strings.TrimRight(string(r[0].Data.ButtonLabel), "\x00") != "c" {
		t.Errorf("replies = %+v", r)
	}

	res, f = chooseRun(t, bs, "choose button 5 on vendor")
	wantExit(t, res, 1)
	mustHave(t, res, `there is no button 5`, `"a" "b" "c" "d"`)
	if len(f.replies()) != 0 {
		t.Error("a reply was sent")
	}

	res, f = chooseRun(t, []string{"Yes", "No"}, `choose matching "^N" on vendor`)
	wantExit(t, res, 0)
	if r := f.replies(); len(r) != 1 || r[0].Data.ButtonIndex != 1 {
		t.Errorf("replies = %+v", r)
	}

	res, f = chooseRun(t, []string{"Yes", "No"}, `choose matching "^Z" on vendor`)
	wantExit(t, res, 1)
	mustHave(t, res, `no button matches "^Z"`, `"Yes" "No"`)
	if len(f.replies()) != 0 {
		t.Error("a reply was sent")
	}

	res, f = chooseRun(t, []string{"Yes", "No", "Nope"}, `choose matching "^N" on vendor`)
	wantExit(t, res, 1)
	mustHave(t, res, `2 buttons match "^N"`)
	if len(f.replies()) != 0 {
		t.Error("a reply was sent")
	}
}

func TestChooseCaptureFoldingToTwoButtonsFails(t *testing.T) {
	f := newGrid(t)
	f.menu(-4242, "Pick", "Go", "go ")
	res := play(t, f, hdr+`
say "menu" on 0
expect dialog from vendor button matching "^(?P<x>Go)$" within 200ms

choose $x on vendor
`)
	wantExit(t, res, 1)
	mustHave(t, res, `2 buttons are called "Go"`)
	if len(f.replies()) != 0 {
		t.Error("a reply was sent")
	}
}
