package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

// dialogFrom builds a script dialog with these buttons.
func dialogFrom(name, message string, channel int32, buttons ...string) *msg.ScriptDialog {
	d := &msg.ScriptDialog{}
	d.Data.ObjectID = testLamp
	d.Data.ObjectName = append([]byte(name), 0)
	d.Data.Message = append([]byte(message), 0)
	d.Data.ChatChannel = channel
	for _, b := range buttons {
		d.Buttons = append(d.Buttons, msg.ScriptDialog_Buttons{
			ButtonLabel: append([]byte(b), 0),
		})
	}
	return d
}

// TestANumberMeansTheSameThingAfterAnAnswer is the bug the first live
// run of this found.
//
// Numbering by position looks right until something is answered: with
// two waiting, answering the first made the second become 1, so the
// "answer 2" already typed out was an error -- and would have been a
// wrong answer to the wrong thing if the list had been longer.
func TestANumberMeansTheSameThingAfterAnAnswer(t *testing.T) {
	x := newTestShell(t)
	watching(t, x)

	x.grid.Relay(t, dialogFrom("a lamp", "on or off?", -101, "on", "off"))
	waits(t, x, "a lamp asks")
	x.grid.Relay(t, dialogFrom("a door", "open?", -102, "yes", "no"))
	waits(t, x, "a door asks")

	got := x.do(t, "waiting")
	if !strings.Contains(got, "1  dialog") || !strings.Contains(got, "2  dialog") {
		t.Fatalf("two dialogs should be 1 and 2:\n%s", got)
	}

	if got := x.do(t, "answer 1 on"); !strings.Contains(got, "pressed") {
		t.Fatalf("answering the first: %s", got)
	}

	// The door was 2 before and is 2 now.
	got = x.do(t, "waiting")
	if !strings.Contains(got, "2  dialog") {
		t.Errorf("the door should still be 2:\n%s", got)
	}
	if strings.Contains(got, "1  dialog") {
		t.Errorf("nothing should have taken the answered one's number:\n%s", got)
	}
	if got := x.do(t, "answer 2 yes"); !strings.Contains(got, "pressed") {
		t.Errorf("answering by the number on the screen: %s", got)
	}
	// And with nothing left, the numbering starts again rather than
	// climbing for the rest of the session.
	x.grid.Relay(t, dialogFrom("a gate", "through?", -103, "yes"))
	waits(t, x, "a gate asks")
	if got := x.do(t, "waiting"); !strings.Contains(got, "1  dialog") {
		t.Errorf("an empty list should start again at 1:\n%s", got)
	}
}

// TestATextBoxIsNotAButton: llTextBox arrives as an ordinary dialog
// carrying a sentinel where its buttons would be, and a person should
// be shown a request for text rather than the plumbing.
func TestATextBoxIsNotAButton(t *testing.T) {
	x := newTestShell(t)
	watching(t, x)

	x.grid.Relay(t, dialogFrom("a sign", "your name?", -104, "!!llTextBox!!"))
	notice := waits(t, x, "a sign asks")
	if strings.Contains(notice, "llTextBox") {
		t.Errorf("the sentinel should not be shown to a person:\n%s", notice)
	}

	got := x.do(t, "waiting")
	if !strings.Contains(got, "text box") || !strings.Contains(got, "type an answer") {
		t.Errorf("a text box should say what it wants:\n%s", got)
	}
	if got := x.do(t, "answer 1"); !strings.Contains(got, "a line with one . ends it") {
		t.Errorf("answering a text box with no text should start collecting: %s", got)
	}
	x.abandon()
	if got := x.do(t, "answer 1 Quark"); !strings.Contains(got, "told") {
		t.Errorf("answering with text: %s", got)
	}
}

// TestATextBoxTakesMoreThanOneLine.
//
// The viewer's text box is a text editor rather than a field, so an
// answer there can have newlines in it, and a shell that reads one line
// at a time cannot type one.  Naming a file is the way in.
//
// Measured on Agni: three lines went out and the script received them
// with the newlines intact -- 33 characters, three lines when parsed on
// "\n" -- and 254 bytes with five newlines arrived whole.
func TestATextBoxTakesMoreThanOneLine(t *testing.T) {
	x := newTestShell(t)
	watching(t, x)

	path := filepath.Join(t.TempDir(), "answer.txt")
	if err := os.WriteFile(path, []byte("first line\nsecond line\nthird"), 0o600); err != nil {
		t.Fatal(err)
	}

	x.grid.Relay(t, dialogFrom("a sign", "your name?", -106, "!!llTextBox!!"))
	waits(t, x, "a sign asks")

	if got := x.do(t, "answer --file "+path+" 1"); !strings.Contains(got, "told") {
		t.Fatalf("answering from a file: %s", got)
	}
	var sent string
	for _, m := range x.grid.Sent() {
		if r, ok := m.(*msg.ScriptDialogReply); ok {
			sent = strings.TrimRight(string(r.Data.ButtonLabel), "\x00")
		}
	}
	if want := "first line\nsecond line\nthird"; sent != want {
		t.Errorf("the reply carried %q, want %q", sent, want)
	}
}

// TestATextBoxHasALimit: the label the answer travels in is one byte of
// length and 254 of text, and a person is better told before it goes
// than left wondering which half arrived.
func TestATextBoxHasALimit(t *testing.T) {
	x := newTestShell(t)
	watching(t, x)

	path := filepath.Join(t.TempDir(), "long.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("y", 255)), 0o600); err != nil {
		t.Fatal(err)
	}

	x.grid.Relay(t, dialogFrom("a sign", "your name?", -107, "!!llTextBox!!"))
	waits(t, x, "a sign asks")

	got := x.do(t, "answer --file "+path+" 1")
	if !strings.Contains(got, "too long") || !strings.Contains(got, "254") {
		t.Errorf("an over-long answer should say so and give the limit: %s", got)
	}
	for _, m := range x.grid.Sent() {
		if _, ok := m.(*msg.ScriptDialogReply); ok {
			t.Error("it was sent anyway")
		}
	}
}

// TestIgnoringKeepsItWaiting: the point of ignoring is to stop being
// counted, not to answer or to lose it.
func TestIgnoringKeepsItWaiting(t *testing.T) {
	x := newTestShell(t)
	watching(t, x)

	x.grid.Relay(t, dialogFrom("a lamp", "on or off?", -105, "on", "off"))
	waits(t, x, "a lamp asks")

	if got := x.do(t, "ignore 1"); !strings.Contains(got, "still waiting") {
		t.Fatalf("ignore said: %s", got)
	}
	if n := x.waitingCount(); n != 0 {
		t.Errorf("an ignored thing is still counted: %d", n)
	}
	if got := x.do(t, "waiting"); !strings.Contains(got, "nothing waiting") {
		t.Errorf("an ignored thing should be out of the plain listing:\n%s", got)
	}
	got := x.do(t, "waiting -a")
	if !strings.Contains(got, "a lamp") || !strings.Contains(got, "(ignored)") {
		t.Errorf("-a should show it, marked:\n%s", got)
	}
	// And it can still be answered by the number it kept.
	if got := x.do(t, "answer 1 off"); !strings.Contains(got, "pressed") {
		t.Errorf("answering an ignored thing: %s", got)
	}
}

// TestNothingWaitingSaysSo rather than printing nothing, which reads
// like a command that failed quietly.
func TestNothingWaitingSaysSo(t *testing.T) {
	x := newTestShell(t)
	if got := x.do(t, "waiting"); !strings.Contains(got, "nothing waiting") {
		t.Errorf("waiting printed %q", got)
	}
	if got := x.do(t, "answer 1"); !strings.Contains(got, "nothing is waiting") {
		t.Errorf("answering nothing printed %q", got)
	}
}
