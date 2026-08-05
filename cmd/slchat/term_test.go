package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// feed runs the key decoder over some bytes and returns the keys it
// produced, the way the terminal would deliver them.
func feed(t *testing.T, in []byte) []rune {
	t.Helper()
	term := &Term{keys: make(chan rune, 64), done: make(chan struct{})}
	raw := make(chan byte, len(in))
	for _, b := range in {
		raw <- b
	}
	close(raw)
	go term.decode(raw)

	var out []rune
	for {
		select {
		case r, ok := <-term.keys:
			if !ok {
				return out
			}
			out = append(out, r)
		case <-time.After(2 * time.Second):
			t.Fatal("decoder did not finish")
		}
	}
}

// TestDecodeKeys: the ordinary ones, and the control characters that
// have to survive because a prefix key may be any of them.
func TestDecodeKeys(t *testing.T) {
	got := feed(t, []byte("ab\r\t\x07\x03"))
	want := []rune{'a', 'b', '\r', '\t', 7, 3}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("key %d = %d, want %d", i, got[i], want[i])
		}
	}
}

// TestDecodeArrows: an arrow is three bytes starting with ESC, and must
// not be mistaken for the ESC key.
func TestDecodeArrows(t *testing.T) {
	got := feed(t, []byte("\x1b[A\x1b[B\x1b[C\x1b[D\x1b[H\x1b[F"))
	want := []rune{keyUp, keyDown, keyRight, keyLeft, keyHome, keyEnd}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("key %d = %d, want %d", i, got[i], want[i])
		}
	}
}

// TestDecodeLoneEscape is the case the whole escapeWait exists for: ESC
// pressed on its own is the default prefix key, and nothing follows it.
func TestDecodeLoneEscape(t *testing.T) {
	got := feed(t, []byte{27})
	if len(got) != 1 || got[0] != 27 {
		t.Fatalf("got %v, want a single ESC", got)
	}
}

// TestDecodeEscapeThenLetter: ESC followed by an ordinary key is the
// prefix and then that key, which is how "ESC w" reaches a command.
func TestDecodeEscapeThenLetter(t *testing.T) {
	got := feed(t, []byte("\x1bwho"))
	want := []rune{27, 'w', 'h', 'o'}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("key %d = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestDecodeUTF8(t *testing.T) {
	got := feed(t, []byte("é☃"))
	if len(got) != 2 || got[0] != 'é' || got[1] != '☃' {
		t.Fatalf("got %v, want the two runes", got)
	}
}

// TestEditing drives the line editor the way fingers would.
func TestEditing(t *testing.T) {
	tm := &Term{plain: true} // plain: no escape codes written anywhere

	for _, r := range "hello world" {
		tm.Key(r)
	}
	if tm.Line() != "hello world" {
		t.Fatalf("line = %q", tm.Line())
	}

	tm.Key(23) // Ctrl-W, kill the word behind
	if tm.Line() != "hello " {
		t.Errorf("after Ctrl-W: %q", tm.Line())
	}

	tm.Key(1) // Ctrl-A, to the start
	for _, r := range "oh " {
		tm.Key(r)
	}
	if tm.Line() != "oh hello " {
		t.Errorf("after inserting at the start: %q", tm.Line())
	}

	tm.Key(5)   // Ctrl-E, to the end
	tm.Key(127) // backspace
	if tm.Line() != "oh hello" {
		t.Errorf("after backspace: %q", tm.Line())
	}

	// Two left, then delete forward: "oh hello" loses the second l.
	tm.Key(keyLeft)
	tm.Key(keyLeft)
	tm.Key(keyDelete)
	if tm.Line() != "oh helo" {
		t.Errorf("after delete: %q", tm.Line())
	}

	// Ctrl-U kills to the start and keeps the tail, as it does in a
	// shell; the cursor is still before the last "o".
	tm.Key(21)
	if tm.Line() != "o" {
		t.Errorf("after Ctrl-U: %q", tm.Line())
	}
	tm.Key(11) // Ctrl-K, kill to the end
	if tm.Line() != "" {
		t.Errorf("after Ctrl-K: %q", tm.Line())
	}

	// Keys the editor does not own are refused, so the caller can
	// give them a meaning: Enter, tab, ESC, Ctrl-C.
	for _, r := range []rune{'\r', '\t', 27, 3} {
		if tm.Key(r) {
			t.Errorf("the editor swallowed %d", r)
		}
	}
}

// TestTakeClearsTheLine: what Enter does.
func TestTakeClearsTheLine(t *testing.T) {
	tm := &Term{plain: true}
	for _, r := range "a message" {
		tm.Key(r)
	}
	if got := tm.Take(); got != "a message" {
		t.Errorf("Take = %q", got)
	}
	if tm.Line() != "" {
		t.Errorf("line survived Take: %q", tm.Line())
	}
}

// TestRenderKeepsThePromptBelow is the display rule the whole thing
// rests on: a message that arrives while something is half typed must
// not land on top of it.  The line is erased, the message is written,
// and the prompt is drawn again underneath with what was typed still
// there.
func TestRenderKeepsThePromptBelow(t *testing.T) {
	var out bytes.Buffer
	tm := &Term{out: &out, width: 80, done: make(chan struct{})}

	tm.SetPrompt("Local> ")
	for _, r := range "half a sentence" {
		tm.Key(r)
	}
	out.Reset()

	tm.Print("23:59:59 < [Local] Someone: hello")

	got := out.String()
	if !strings.HasPrefix(got, "\r\x1b[K") {
		t.Errorf("the typed line was not erased first: %q", got)
	}
	if !strings.Contains(got, "hello\r\n") {
		t.Errorf("the message was not written: %q", got)
	}
	// Everything typed is still there, after the message.
	i := strings.Index(got, "hello\r\n")
	after := got[i:]
	if !strings.Contains(after, "Local> half a sentence") {
		t.Errorf("the prompt and line were not restored: %q", after)
	}
	// And the cursor is put back where it was, at the end of what was
	// typed: 7 for the prompt plus 15 typed.
	if !strings.Contains(after, "\x1b[22C") {
		t.Errorf("the cursor was not returned to the typing position: %q", after)
	}
}

// TestRenderScrollsRatherThanWraps: a line wider than the terminal
// would wrap, and the next message printed above it would then land in
// the middle of the wrapped part.  It scrolls sideways instead.
func TestRenderScrollsRatherThanWraps(t *testing.T) {
	var out bytes.Buffer
	tm := &Term{out: &out, width: 20, done: make(chan struct{})}
	tm.SetPrompt("Local> ")
	for _, r := range strings.Repeat("x", 40) {
		tm.Key(r)
	}

	last := out.String()
	if i := strings.LastIndex(last, "\r\x1b[K"); i >= 0 {
		last = last[i:]
	}
	// What is drawn has to fit: prompt plus the visible tail, and no
	// more than the terminal is wide.
	const erase = "\r\x1b[K"
	drawn := strings.TrimPrefix(last, erase)
	if i := strings.Index(drawn, "\x1b"); i >= 0 {
		drawn = drawn[:i]
	}
	drawn = strings.TrimSuffix(drawn, "\r")
	if len([]rune(drawn)) > 20 {
		t.Errorf("drew %d columns into a 20 column terminal: %q", len([]rune(drawn)), drawn)
	}
	if !strings.HasPrefix(drawn, "Local> ") {
		t.Errorf("the prompt was scrolled away: %q", drawn)
	}
}
