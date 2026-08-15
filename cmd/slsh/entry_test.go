package main

import (
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

// TestEveryLineMeansItself.
//
// The answer was ended by a full stop on a line of its own first, with
// ed's rule for escaping one.  Ctrl-D ends it now, so a full stop is
// text like anything else -- and a person typing a paragraph that ends
// in one should never find out that this program has opinions about it.
func TestEveryLineMeansItself(t *testing.T) {
	x := newTestShell(t)
	watching(t, x)

	x.grid.Relay(t, dialogFrom("a sign", "your name?", -108, "!!llTextBox!!"))
	waits(t, x, "a sign asks")
	if got := x.do(t, "answer 1"); !strings.Contains(got, "^D ends it") {
		t.Fatalf("starting an answer: %s", got)
	}

	for _, line := range []string{"That is all.", ".", "..", "...."} {
		x.typed(t.Context(), line)
	}
	x.finish(t.Context())

	sent := ""
	for _, m := range x.grid.Sent() {
		if r, ok := m.(*msg.ScriptDialogReply); ok {
			sent = strings.TrimRight(string(r.Data.ButtonLabel), "\x00")
		}
	}
	if want := "That is all.\n.\n..\n...."; sent != want {
		t.Errorf("the reply carried %q, want %q", sent, want)
	}
}

// TestStartingOverKeepsTheQuestion: the line editor reaches the line
// being typed and no further, so Escape is the only way back from a
// line already entered -- and it must not lose the text box itself,
// which would leave a person hunting for it again.
func TestStartingOverKeepsTheQuestion(t *testing.T) {
	x := newTestShell(t)
	watching(t, x)

	x.grid.Relay(t, dialogFrom("a sign", "your name?", -109, "!!llTextBox!!"))
	waits(t, x, "a sign asks")
	x.do(t, "answer 1")

	x.typed(t.Context(), "wrong from the start")
	x.startOver()
	if !x.typing() {
		t.Fatal("starting over left the answer entirely")
	}
	x.typed(t.Context(), "the real answer")
	x.finish(t.Context())

	sent := ""
	for _, m := range x.grid.Sent() {
		if r, ok := m.(*msg.ScriptDialogReply); ok {
			sent = strings.TrimRight(string(r.Data.ButtonLabel), "\x00")
		}
	}
	if want := "the real answer"; sent != want {
		t.Errorf("the reply carried %q, want %q", sent, want)
	}
}

// TestAbandoningLeavesItWaiting: Ctrl-C sends nothing, and the text box
// is still there to be answered.
func TestAbandoningLeavesItWaiting(t *testing.T) {
	x := newTestShell(t)
	watching(t, x)

	x.grid.Relay(t, dialogFrom("a sign", "your name?", -110, "!!llTextBox!!"))
	waits(t, x, "a sign asks")
	x.do(t, "answer 1")
	x.typed(t.Context(), "half an answer")
	x.abandon()

	if x.typing() {
		t.Error("Ctrl-C left it collecting")
	}
	for _, m := range x.grid.Sent() {
		if _, ok := m.(*msg.ScriptDialogReply); ok {
			t.Error("something was sent")
		}
	}
	if got := x.do(t, "waiting"); !strings.Contains(got, "text box") {
		t.Errorf("the text box should still be waiting:\n%s", got)
	}
}
