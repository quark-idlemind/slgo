package main

// Typing more than one line at a shell prompt.
//
// A text box in the viewer is a text editor: several lines, and the
// whole of it goes when the button is pressed.  A shell reads one line
// at a time, so the two shapes do not meet -- and inventing an escape
// for the newline means inventing an escape for the escape.
//
// So the answer is collected the way ed collects one, and ended the way
// mail does: Ctrl-D, which cannot appear in text and so needs no escape.
//
// It was a full stop on a line of its own first, with ed's rule for
// escaping one -- a line of nothing but full stops losing one, so ".."
// said "." -- and that went as soon as Ctrl-D was bound, because the
// reason to keep it did not survive being looked at.  The argument was
// that a file of commands cannot send Ctrl-D; but a file of commands
// cannot type an answer either, since slsh -f hands every line to the
// command parser and the line after "answer 1" would be run as a
// command.  Scripted answers have --file.  So the escape rule was
// paying for a case that does not exist, and every line now means
// itself.
//
// A line being typed is an ordinary line: every key the prompt's editor
// owns works here, because it IS that editor -- see Term.Key for the
// whole set.  What none of them can reach is a line already entered, so
// Escape starts the whole answer again.
//
// Ctrl-D ends it, and needs no escaping because it cannot appear in
// text.  Ends it at the END of the line, that is: with anything in front
// of the cursor it deletes forward, as it does at the prompt, so a
// person who has moved left to fix a typo does not send the answer by
// pressing it.
//
// Leaving without sending is Ctrl-C, which is what it does everywhere
// else here.

import (
	"context"
	"fmt"
	"strings"

	"github.com/quark-idlemind/slgo/sl"
)

// entry is a multi-line answer being typed.
type entry struct {
	// dialog is the text box it will go to.
	dialog sl.Dialog

	// what names it on the screen, and n is the number it was listed
	// as, so that a person who came from "waiting" recognises it.
	what string
	n    int

	lines []string
}

// typing reports whether a multi-line answer is being collected.
func (sh *Shell) typing() bool {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	return sh.mode == modeText
}

// collect starts taking lines for a text box.
func (sh *Shell) collect(d sl.Dialog, what string, n int) {
	sh.mu.Lock()
	sh.entry = &entry{dialog: d, what: what, n: n}
	sh.mu.Unlock()
	sh.setMode(modeText)
}

// typed takes a line while a text box answer is being collected.
func (sh *Shell) typed(ctx context.Context, line string) {
	// Every line means itself, including one that is a full stop.
	// Appended under the lock, since the prompt counts them and is
	// redrawn from the printer's goroutine too.
	sh.mu.Lock()
	e := sh.entry
	if e != nil {
		e.lines = append(e.lines, line)
	}
	sh.mu.Unlock()
	if e == nil {
		sh.setMode(modeCommand)
		return
	}
	sh.prompt()
}

// finish ends the answer where it stands, which is what Ctrl-D means at
// the end of a line -- in front of anything it deletes forward instead,
// and never reaches here.
//
// Whatever is half typed on the current line counts as a last line, the
// way it does to mail(1): a person who has typed something and pressed
// Ctrl-D meant to send it.
func (sh *Shell) finish(ctx context.Context) {
	line := sh.term.Take()
	sh.term.Echo()
	sh.mu.Lock()
	if e := sh.entry; e != nil && line != "" {
		e.lines = append(e.lines, line)
	}
	sh.mu.Unlock()
	sh.submit(ctx)
}

// startOver throws away what has been typed and stays in the answer,
// because the line editor cannot reach a line already entered and the
// alternative is to abandon the whole thing and find it again.
func (sh *Shell) startOver() {
	sh.mu.Lock()
	e := sh.entry
	if e != nil {
		e.lines = nil
	}
	sh.mu.Unlock()
	sh.term.Take()
	if e != nil {
		fmt.Fprintf(sh.stdout(), "cleared; type the answer again\n")
	}
	sh.prompt()
}

// submit sends what has been collected.
func (sh *Shell) submit(ctx context.Context) {
	sh.mu.Lock()
	e := sh.entry
	sh.mu.Unlock()
	if e == nil {
		sh.setMode(modeCommand)
		return
	}

	answer := strings.Join(e.lines, "\n")
	sh.mu.Lock()
	sh.entry = nil
	sh.mu.Unlock()
	sh.setMode(modeCommand)

	out := sh.stdout()
	if len(e.lines) == 0 {
		// An empty answer is a real answer -- a text box can be
		// submitted blank -- but it is worth saying which happened.
		fmt.Fprintf(out, "sending an empty answer to %s\n", e.what)
	}
	if len(answer) > sl.MaxDialogReply {
		// Said rather than truncated: half an answer arriving is worse
		// than none, and the lines are still on the screen to be
		// shortened and typed again.
		fmt.Fprintf(out, "that is %d bytes and a text box carries %d; nothing was sent\n",
			len(answer), sl.MaxDialogReply)
		return
	}
	if err := sh.s.AnswerText(ctx, e.dialog, answer); err != nil {
		sh.errorf("answer: %v", err)
		return
	}
	fmt.Fprintf(out, "told %s %d %s\n", e.what, len(e.lines), plural(len(e.lines), "line", "lines"))
}

// abandon drops a half-typed answer, leaving the text box waiting.
func (sh *Shell) abandon() {
	sh.mu.Lock()
	e := sh.entry
	sh.entry = nil
	sh.mu.Unlock()
	sh.setMode(modeCommand)
	if e != nil {
		fmt.Fprintf(sh.stdout(), "nothing sent; %d is still waiting\n", e.n)
	}
}
