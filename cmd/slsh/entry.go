package main

// Typing more than one line at a shell prompt.
//
// A text box in the viewer is a text editor: several lines, and the
// whole of it goes when the button is pressed.  A shell reads one line
// at a time, so the two shapes do not meet -- and inventing an escape
// for the newline means inventing an escape for the escape.
//
// So the answer is collected the way ed collects one: lines until a
// line that is a single full stop.  A line of nothing but full stops
// loses one and is kept, so a paragraph can end with a full stop of its
// own -- ".." is how you say ".", and "...." is how you say "...".
// That rule is what makes the terminator escapable without a second
// syntax, and it costs nothing on any line that is not all dots.
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

// endLine is the line that finishes the answer.
const endLine = "."

// entryLine takes one typed line, and says whether that was the end.
//
// The dots are counted rather than trimmed so that a line of spaces and
// dots is left alone: only a line that is nothing but full stops means
// anything special, which is the rule ed has and the reason a person
// can type a paragraph without thinking about it.
func entryLine(line string) (text string, done bool) {
	if line == endLine {
		return "", true
	}
	if line != "" && strings.Trim(line, ".") == "" {
		// All dots and more than one: it stands for itself, one
		// shorter.
		return line[1:], false
	}
	return line, false
}

// typed takes a line while a text box answer is being collected.
func (sh *Shell) typed(ctx context.Context, line string) {
	sh.mu.Lock()
	e := sh.entry
	sh.mu.Unlock()
	if e == nil {
		sh.setMode(modeCommand)
		return
	}

	text, done := entryLine(line)
	if !done {
		e.lines = append(e.lines, text)
		sh.prompt()
		return
	}

	answer := strings.Join(e.lines, "\n")
	sh.mu.Lock()
	sh.entry = nil
	sh.mu.Unlock()
	sh.setMode(modeCommand)

	out := sh.stdout()
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
