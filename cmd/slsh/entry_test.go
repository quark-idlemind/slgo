package main

import "testing"

// TestEntryLineDots: a line of nothing but full stops loses one and is
// kept, so a paragraph can end with a full stop of its own.  One full
// stop ends the answer, which is ed's rule and the reason the escape is
// needed at all.
func TestEntryLineDots(t *testing.T) {
	for _, c := range []struct {
		line string
		text string
		done bool
	}{
		{".", "", true},
		{"..", ".", false},
		{"...", "..", false},
		{"....", "...", false},

		// Not all dots, so nothing is stripped: a line ending in a
		// full stop is the ordinary case and must survive untouched.
		{"That is all.", "That is all.", false},
		{". ", ". ", false},
		{" .", " .", false},
		{".x.", ".x.", false},
		{"", "", false},
	} {
		text, done := entryLine(c.line)
		if text != c.text || done != c.done {
			t.Errorf("entryLine(%q) = %q, %v; want %q, %v", c.line, text, done, c.text, c.done)
		}
	}
}

// TestAnEmptyLineIsKept: a blank line between paragraphs is text, not a
// terminator, and losing it would quietly reflow what somebody typed.
func TestAnEmptyLineIsKept(t *testing.T) {
	text, done := entryLine("")
	if done {
		t.Error("an empty line ended the answer")
	}
	if text != "" {
		t.Errorf("an empty line became %q", text)
	}
}
