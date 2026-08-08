package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
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

// TestDecodeTheNumberedSequences: home, delete and end arrive as ESC [
// N ~ on some terminals and as ESC [ letter on others, and both have to
// mean the same key.
func TestDecodeTheNumberedSequences(t *testing.T) {
	got := feed(t, []byte("\x1b[1~\x1b[3~\x1b[4~\x1b[7~\x1b[8~"))
	want := []rune{keyHome, keyDelete, keyEnd, keyHome, keyEnd}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("key %d = %d, want %d", i, got[i], want[i])
		}
	}
}

// TestDecodeTheApplicationCursorKeys: ESC O A is the same up arrow, and
// a terminal in application mode sends it instead.
func TestDecodeTheApplicationCursorKeys(t *testing.T) {
	got := feed(t, []byte("\x1bOA\x1bOD"))
	if len(got) != 2 || got[0] != keyUp || got[1] != keyLeft {
		t.Fatalf("got %v, want up and left", got)
	}
}

// TestDecodeSkipsWhatItDoesNotKnow rather than delivering the letters
// of an escape sequence as though they had been typed, which would run
// a command nobody asked for.
func TestDecodeSkipsWhatItDoesNotKnow(t *testing.T) {
	if got := feed(t, []byte("\x1b[Za")); len(got) != 1 || got[0] != 'a' {
		t.Fatalf("got %v, want the a alone", got)
	}
	// A sequence cut off by the input ending delivers nothing, in each
	// of the three places it can be cut.
	for _, in := range []string{"\x1b[", "\x1b[3", "\xc3"} {
		if got := feed(t, []byte(in)); len(got) != 0 {
			t.Errorf("%q gave %v, want nothing", in, got)
		}
	}
	// A byte that is not UTF-8 at all is dropped rather than delivered
	// as the replacement character.
	if got := feed(t, []byte{0xff}); len(got) != 0 {
		t.Errorf("an invalid byte gave %v", got)
	}
}

// TestDecodeStopsWhenTheTerminalCloses.
//
// Every key the decoder produces is handed over on a channel, and
// nothing is reading it once the shell has gone.  Without the done case
// on each of those sends the goroutine would be there until the process
// ended, holding the terminal it was reading.
func TestDecodeStopsWhenTheTerminalCloses(t *testing.T) {
	for _, in := range []string{"a", "\x1b", "\x1bx", "\x1b[A", "é"} {
		done := make(chan struct{})
		close(done)
		term := &Term{keys: make(chan rune), done: done}

		raw := make(chan byte, len(in))
		for _, b := range []byte(in) {
			raw <- b
		}
		close(raw)

		stopped := make(chan struct{})
		go func() { term.decode(raw); close(stopped) }()
		select {
		case <-stopped:
		case <-time.After(2 * time.Second):
			t.Fatalf("%q: the decoder did not give up on a closed terminal", in)
		}
	}
}

// TestTheEditorsOtherSpellings: the arrows and the control characters
// are the same keys, because a terminal may send either.
func TestTheEditorsOtherSpellings(t *testing.T) {
	tm := &Term{plain: true}
	for _, r := range "abc" {
		tm.Key(r)
	}

	tm.Key(2) // Ctrl-B
	tm.Key(keyLeft)
	tm.Key('X')
	if got := tm.Line(); got != "aXbc" {
		t.Errorf("after two lefts and an X: %q", got)
	}
	tm.Key(6) // Ctrl-F
	tm.Key(keyRight)
	tm.Key('Y')
	if got := tm.Line(); got != "aXbcY" {
		t.Errorf("after two rights and a Y: %q", got)
	}

	tm.Key(keyHome)
	tm.Key('1')
	tm.Key(keyEnd)
	tm.Key('2')
	if got := tm.Line(); got != "1aXbcY2" {
		t.Errorf("after home and end: %q", got)
	}

	// The bounds hold: left at the start and right at the end are not
	// errors and do not move.
	tm.Key(keyHome)
	tm.Key(keyLeft)
	tm.Key('|')
	tm.Key(keyEnd)
	tm.Key(keyRight)
	tm.Key('|')
	if got := tm.Line(); got != "|1aXbcY2|" {
		t.Errorf("the cursor left the line: %q", got)
	}

	// Ctrl-L redraws and changes nothing, and backspace at the start
	// has nothing to take.
	tm.Key(12)
	tm.Key(keyHome)
	tm.Key(127)
	if got := tm.Line(); got != "|1aXbcY2|" {
		t.Errorf("after Ctrl-L and a backspace at the start: %q", got)
	}
	// Ctrl-D with something in front of the cursor deletes it, which is
	// what makes it end-of-input only on an empty tail.
	if !tm.Key(4) {
		t.Error("Ctrl-D with something to delete should be consumed")
	}
	if got := tm.Line(); got != "1aXbcY2|" {
		t.Errorf("after Ctrl-D: %q", got)
	}
}

// TestNothingIsDrawnWhileACommandRuns.
//
// A prompt means the shell is ready for the next line.  Drawing one
// under a command that takes ten seconds said the shell was ready when
// it was not, and the only way to tell that from a finished command was
// to try typing at it.
func TestNothingIsDrawnWhileACommandRuns(t *testing.T) {
	var out bytes.Buffer
	tm := &Term{out: &out, width: 80, done: make(chan struct{})}
	tm.SetPrompt("/$ ")

	tm.SetBusy(true)
	out.Reset()
	tm.Key('a')
	tm.SetPrompt("/Objects$ ")
	if got := out.String(); got != "" {
		t.Errorf("something was drawn during a command: %q", got)
	}

	// What was typed is still there when the prompt comes back.
	tm.SetBusy(false)
	tm.SetPrompt("/Objects$ ")
	if got := out.String(); !strings.Contains(got, "/Objects$ a") {
		t.Errorf("the line was lost while the command ran: %q", got)
	}
}

// TestDrawingLeavesRoomOnATerminalTooNarrowForThePrompt.
//
// A prompt can be longer than the window -- a deep inventory path in a
// narrow pane -- and the space left for the line would then be negative,
// which is a slice out of range rather than a display fault.
func TestDrawingLeavesRoomOnATerminalTooNarrowForThePrompt(t *testing.T) {
	var out bytes.Buffer
	tm := &Term{out: &out, width: 10, done: make(chan struct{})}
	tm.SetPrompt("/a/very/deep/path$ ")
	for _, r := range "and something typed" {
		tm.Key(r)
	}
	if got := tm.Line(); got != "and something typed" {
		t.Errorf("the line itself should be unharmed: %q", got)
	}
}

// TestDrawingWithNoPromptAtAllPutsTheCursorAtColumnZero.
//
// A cursor-forward of zero moves one column in terminals that read it
// as the default, so column zero has to be a bare carriage return.
func TestDrawingWithNoPromptAtAllPutsTheCursorAtColumnZero(t *testing.T) {
	var out bytes.Buffer
	tm := &Term{out: &out, width: 80, done: make(chan struct{})}
	tm.SetPrompt("")
	if got := out.String(); !strings.HasSuffix(got, "\r\x1b[K\r") {
		t.Errorf("an empty prompt drew %q", got)
	}
}

// TestEchoWritesTheLineOutRatherThanLeavingWhatWasDrawn.
//
// A line wider than the terminal is drawn as a window on to it,
// scrolled sideways, so keeping the drawn version would record a
// fragment of the command instead of the command.
func TestEchoWritesTheLineOutRatherThanLeavingWhatWasDrawn(t *testing.T) {
	var out bytes.Buffer
	tm := &Term{out: &out, width: 20, done: make(chan struct{})}
	tm.SetPrompt("/$ ")
	for _, r := range strings.Repeat("x", 40) {
		tm.Key(r)
	}

	out.Reset()
	tm.Echo()
	got := out.String()
	if !strings.HasSuffix(got, "\r\n") {
		t.Errorf("Echo should end the line: %q", got)
	}
	if !strings.Contains(got, "/$ "+strings.Repeat("x", 40)) {
		t.Errorf("Echo should write the whole command, not the visible part: %q", got)
	}
}

// TestAClosedTerminalIsWrittenToNoFurther.
//
// Close restores the terminal, and anything written after that lands on
// a screen the shell no longer owns -- over whatever the shell it
// returned to has printed since.
func TestAClosedTerminalIsWrittenToNoFurther(t *testing.T) {
	var out bytes.Buffer
	tm := &Term{out: &out, width: 80, done: make(chan struct{})}
	tm.SetPrompt("/$ ")

	tm.Close()
	out.Reset()
	tm.Print("a line")
	tm.Status("working")
	tm.Echo()
	tm.Key('a')
	if got := out.String(); got != "" {
		t.Errorf("a closed terminal wrote %q", got)
	}

	// And closing twice is not an error, since the usual path is a
	// deferred call plus an explicit one on the way out.
	tm.Close()
}

// TestStatusIsOverwrittenRatherThanScrolled.
//
// It is for progress: something worth watching while it happens and not
// worth keeping afterwards.  On a pipe there is nobody watching, so it
// goes nowhere rather than into the output on its way to a file.
func TestStatusIsOverwrittenRatherThanScrolled(t *testing.T) {
	var out bytes.Buffer
	tm := &Term{out: &out, width: 80, done: make(chan struct{})}
	tm.Status("14 of 600")
	if got := out.String(); got != "\r\x1b[K14 of 600" {
		t.Errorf("Status wrote %q", got)
	}

	out.Reset()
	plain := &Term{out: &out, plain: true, done: make(chan struct{})}
	plain.Status("14 of 600")
	if got := out.String(); got != "" {
		t.Errorf("Status on a pipe wrote %q", got)
	}
	// Nor does a pipe get the prompt drawn on it or the line echoed.
	plain.SetPrompt("/$ ")
	plain.Key('a')
	plain.Echo()
	if got := out.String(); got != "" {
		t.Errorf("a pipe was drawn on: %q", got)
	}
	// Printing still works there, since that is the output.
	plain.Print("a line")
	if got := out.String(); got != "a line\n" {
		t.Errorf("a pipe should still be printed to, got %q", got)
	}
}

// TestATerminalThatIsAPipeReadsLines.
//
// Input that is not a terminal -- a pipe from a test, a script -- is
// read a line at a time and fed through the same channel a keyboard
// uses, so a piped session runs the same code an interactive one does.
func TestATerminalThatIsAPipeReadsLines(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	var out bytes.Buffer
	tm, err := NewTerm(r, &out)
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close()

	if !tm.Plain() {
		t.Error("a pipe is not a terminal")
	}
	if got := tm.Rows(); got != 24 {
		t.Errorf("a pipe has no size, so it should be assumed: %d rows", got)
	}

	io.WriteString(w, "pwd\n")
	w.Close()

	var got []rune
	for r := range tm.Keys() {
		got = append(got, r)
	}
	if string(got) != "pwd\r" {
		t.Errorf("the line arrived as %q, want the keys and an Enter", string(got))
	}
}

// TestARealTerminalIsPutInRawMode.
//
// This is the half of NewTerm a pipe never reaches: raw mode, the size,
// the byte reader and the decoder.  A pty is a terminal in every way
// that matters here and needs no window on the screen.
func TestARealTerminalIsPutInRawMode(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	defer ptmx.Close()
	defer tty.Close()
	if err := pty.Setsize(tty, &pty.Winsize{Rows: 30, Cols: 100}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	tm, err := NewTerm(tty, &out)
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close()

	if tm.Plain() {
		t.Error("a pty is a terminal")
	}
	if got := tm.Rows(); got != 30 {
		t.Errorf("the terminal is 30 rows, got %d", got)
	}

	// Typing at it arrives as keys, through the reader and the decoder.
	if _, err := ptmx.WriteString("hi\x1b[A"); err != nil {
		t.Fatal(err)
	}
	want := []rune{'h', 'i', keyUp}
	for i, w := range want {
		select {
		case got := <-tm.Keys():
			if got != w {
				t.Errorf("key %d = %d, want %d", i, got, w)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("key %d never arrived", i)
		}
	}

	// A window that changes size is noticed, which is what keeps a
	// long line drawn as a window on to the right number of columns.
	if err := pty.Setsize(tty, &pty.Winsize{Rows: 40, Cols: 120}); err != nil {
		t.Fatal(err)
	}
	syscall.Kill(os.Getpid(), syscall.SIGWINCH)
	deadline := time.Now().Add(5 * time.Second)
	for tm.Rows() != 40 {
		if time.Now().After(deadline) {
			t.Fatalf("the resize was not noticed; still %d rows", tm.Rows())
		}
		time.Sleep(2 * time.Millisecond)
	}

	// Closing restores the terminal and stops both goroutines.
	tm.Close()
	select {
	case <-tm.done:
	case <-time.After(5 * time.Second):
		t.Error("Close did not finish")
	}
}

// TestALoneEscapeIsTheEscapeKeyAfterAWait.
//
// This is the price of ESC being both a key and the first byte of every
// arrow: nothing can be decided until either the rest of the sequence
// arrives or enough time passes that it is not coming.  The input here
// stays open and silent, which is a person pressing escape and then
// nothing -- the case a closed input does not reach, since a closed one
// answers at once.
func TestALoneEscapeIsTheEscapeKeyAfterAWait(t *testing.T) {
	t.Parallel()
	term := &Term{keys: make(chan rune, 4), done: make(chan struct{})}
	raw := make(chan byte, 4)
	raw <- 27
	go term.decode(raw)

	select {
	case got := <-term.keys:
		if got != 27 {
			t.Errorf("got key %d, want ESC", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a lone escape never arrived")
	}
	close(raw)
}

// TestKillingAWordCrossesTheSpacesBehindIt, so that Ctrl-W at the end
// of "cd Objects " takes the word and not the gap.
func TestKillingAWordCrossesTheSpacesBehindIt(t *testing.T) {
	tm := &Term{plain: true}
	for _, r := range "cd Objects  " {
		tm.Key(r)
	}
	tm.Key(23) // Ctrl-W
	if got := tm.Line(); got != "cd " {
		t.Errorf("after Ctrl-W over the spaces: %q", got)
	}
}

// TestALongLineIsAWindowOnToItselfFromWhereverTheCursorIs.
//
// The window follows the cursor: at the end it shows the end, and at
// the start it shows the start.  Both have to stop at the width, or the
// line wraps and the next message printed above it lands in the middle
// of the wrapped part.
func TestALongLineIsAWindowOnToItselfFromWhereverTheCursorIs(t *testing.T) {
	var out bytes.Buffer
	tm := &Term{out: &out, width: 20, done: make(chan struct{})}
	tm.SetPrompt("/$ ")
	for _, r := range strings.Repeat("x", 40) {
		tm.Key(r)
	}

	out.Reset()
	tm.Key(keyHome)
	drawn := out.String()
	drawn = strings.TrimPrefix(drawn, "\r\x1b[K")
	if i := strings.Index(drawn, "\x1b"); i >= 0 {
		drawn = drawn[:i]
	}
	drawn = strings.TrimSuffix(drawn, "\r")
	if len([]rune(drawn)) > 20 {
		t.Errorf("drew %d columns into a 20 column terminal: %q", len([]rune(drawn)), drawn)
	}
	if !strings.HasPrefix(drawn, "/$ ") {
		t.Errorf("the prompt was scrolled away: %q", drawn)
	}
}

// TestTheReadersGiveUpWhenTheTerminalCloses.
//
// Both of them hand what they read to a channel nobody is reading once
// the shell has gone.  Without the done case they would sit there until
// the process ended, holding the terminal open.
func TestTheReadersGiveUpWhenTheTerminalCloses(t *testing.T) {
	closed := func() chan struct{} {
		c := make(chan struct{})
		close(c)
		return c
	}

	// readBytes, blocked handing a byte over.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	io.WriteString(w, "a")

	tm := &Term{in: r, done: closed()}
	stopped := make(chan struct{})
	go func() { tm.readBytes(make(chan byte)); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Error("readBytes did not give up on a closed terminal")
	}
	w.Close()

	// readPlain, blocked on the line's first key.
	r, w, err = os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(w, "a\n")
	w.Close()

	tm = &Term{in: r, keys: make(chan rune), done: closed()}
	stopped = make(chan struct{})
	go func() { tm.readPlain(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Error("readPlain did not give up on the key it was handing over")
	}
	r.Close()

	// And readPlain blocked on the Enter that ends the line, which is
	// a second send and gives up on its own.  The terminal is closed
	// only once the key before it has been taken, so the line's keys
	// are not the ones being refused.
	r, w, err = os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(w, "a\n")
	w.Close()

	tm = &Term{in: r, keys: make(chan rune), done: make(chan struct{})}
	stopped = make(chan struct{})
	go func() { tm.readPlain(); close(stopped) }()
	if got := <-tm.keys; got != 'a' {
		t.Fatalf("the first key was %d", got)
	}
	close(tm.done)
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Error("readPlain did not give up on the Enter it was handing over")
	}
	r.Close()
}
