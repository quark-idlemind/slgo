package main

// A line editor that keeps the prompt on the bottom line while messages
// arrive above it.
//
// Everything printed goes through Printf, which erases the prompt,
// writes the line, and draws the prompt again underneath.  Anything
// that writes to the terminal without doing that leaves the display
// wrong until the next keystroke, which is why nothing else here writes
// to stdout.
//
// Keys are decoded in one place and handed over as runes: a printable
// character is itself, a control character is the rune it is (ESC is
// 27, tab 9, Ctrl-G 7), and the keys that arrive as escape sequences --
// the arrows, home, end -- are negative constants.  That is what lets
// the prefix key be configured as any key at all: it is compared as a
// rune like everything else.

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/term"
)

// Keys that arrive as escape sequences rather than as themselves.
const (
	keyLeft = -(iota + 1)
	keyRight
	keyUp
	keyDown
	keyHome
	keyEnd
	keyDelete
)

// escapeWait is how long a lone ESC waits for the rest of a sequence
// before it is taken to be the ESC key itself.
//
// This is the price of ESC being both a key and the first byte of every
// arrow key.  A terminal sends the whole sequence in one write, so the
// rest is already in the buffer if it is coming; the wait only has to
// outlast the scheduler, not the network.
const escapeWait = 40 * time.Millisecond

// Term is the terminal, in raw mode, with a line being edited on the
// bottom line.
type Term struct {
	in  *os.File
	out io.Writer

	keys chan rune
	done chan struct{}

	mu      sync.Mutex
	prompt  string
	line    []rune
	pos     int // cursor, as an index into line
	width   int
	height  int
	restore func() error
	closed  bool

	// plain is a terminal that is not one: a pipe, in a test or a
	// script.  Nothing is redrawn and no key is special, because
	// there is nobody watching and no raw mode to read them in.
	plain bool
}

// NewTerm puts the terminal in raw mode and starts reading it.
//
// Input that is not a terminal -- a pipe from a test, a script -- is
// read a line at a time instead, with none of the editing and no
// redrawing.  The rest of the program does not know the difference.
func NewTerm(in *os.File, out io.Writer) (*Term, error) {
	t := &Term{
		in:     in,
		out:    out,
		keys:   make(chan rune, 64),
		done:   make(chan struct{}),
		width:  80,
		height: 24,
	}

	fd := int(in.Fd())
	if !term.IsTerminal(fd) {
		t.plain = true
		go t.readPlain()
		return t, nil
	}

	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, fmt.Errorf("slsh: cannot put the terminal in raw mode: %w", err)
	}
	t.restore = func() error { return term.Restore(fd, state) }
	if w, h, err := term.GetSize(fd); err == nil && w > 0 {
		t.width, t.height = w, h
	}
	go t.watchSize(fd)

	raw := make(chan byte, 256)
	go t.readBytes(raw)
	go t.decode(raw)
	return t, nil
}

// Plain reports whether this is a pipe rather than a terminal.
func (t *Term) Plain() bool { return t.plain }

// Rows is how many lines the terminal has, which is what decides how
// much of a long listing fits on one page.
func (t *Term) Rows() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.height
}

// Keys is the decoded keystrokes.  It is closed when input ends.
func (t *Term) Keys() <-chan rune { return t.keys }

// Close restores the terminal.  It is safe to call twice, since the
// usual path is a deferred call plus an explicit one on the way out.
func (t *Term) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.closed = true
	if !t.plain {
		fmt.Fprint(t.out, "\r\x1b[K")
	}
	if t.restore != nil {
		t.restore()
	}
	close(t.done)
}

func (t *Term) readBytes(out chan<- byte) {
	defer close(out)
	buf := make([]byte, 256)
	for {
		n, err := t.in.Read(buf)
		for i := 0; i < n; i++ {
			select {
			case out <- buf[i]:
			case <-t.done:
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// decode turns bytes into keys.
//
// It holds at most one byte back, for the case of an ESC followed by
// something that is not an escape sequence: the ESC is delivered and
// the byte after it is then decoded as though it had arrived on its
// own.
func (t *Term) decode(raw <-chan byte) {
	defer close(t.keys)

	var pending byte
	var havePending bool

	next := func() (byte, bool) {
		if havePending {
			havePending = false
			return pending, true
		}
		b, ok := <-raw
		return b, ok
	}
	// nextSoon is next with a deadline, for telling ESC apart from
	// the start of an escape sequence.
	nextSoon := func() (byte, bool) {
		if havePending {
			havePending = false
			return pending, true
		}
		timer := time.NewTimer(escapeWait)
		defer timer.Stop()
		select {
		case b, ok := <-raw:
			return b, ok
		case <-timer.C:
			return 0, false
		}
	}
	emit := func(r rune) bool {
		select {
		case t.keys <- r:
			return true
		case <-t.done:
			return false
		}
	}

	for {
		b, ok := next()
		if !ok {
			return
		}

		switch {
		case b == 27:
			b2, ok := nextSoon()
			if !ok {
				// Nothing followed: this is the ESC key.
				if !emit(27) {
					return
				}
				continue
			}
			if b2 != '[' && b2 != 'O' {
				// ESC then something unrelated.
				if !emit(27) {
					return
				}
				pending, havePending = b2, true
				continue
			}
			b3, ok := next()
			if !ok {
				return
			}
			var r rune
			switch b3 {
			case 'A':
				r = keyUp
			case 'B':
				r = keyDown
			case 'C':
				r = keyRight
			case 'D':
				r = keyLeft
			case 'H':
				r = keyHome
			case 'F':
				r = keyEnd
			case '1', '3', '4', '7', '8':
				// A numbered sequence, ending in '~'.
				for {
					b4, ok := next()
					if !ok {
						return
					}
					if b4 == '~' {
						break
					}
				}
				switch b3 {
				case '1', '7':
					r = keyHome
				case '3':
					r = keyDelete
				case '4', '8':
					r = keyEnd
				}
			default:
				continue // a sequence nobody here knows
			}
			if r != 0 && !emit(r) {
				return
			}

		case b < 0x80:
			if !emit(rune(b)) {
				return
			}

		default:
			// The start of a UTF-8 rune; gather the rest.
			buf := []byte{b}
			for !utf8.FullRune(buf) && len(buf) < 4 {
				b2, ok := next()
				if !ok {
					return
				}
				buf = append(buf, b2)
			}
			r, _ := utf8.DecodeRune(buf)
			if r != utf8.RuneError && !emit(r) {
				return
			}
		}
	}
}

// readPlain feeds whole lines through the same channel, one rune at a
// time, so a pipe drives exactly the same code a keyboard does.
func (t *Term) readPlain() {
	defer close(t.keys)
	sc := bufio.NewScanner(t.in)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		for _, r := range sc.Text() {
			select {
			case t.keys <- r:
			case <-t.done:
				return
			}
		}
		select {
		case t.keys <- '\r':
		case <-t.done:
			return
		}
	}
}

func (t *Term) watchSize(fd int) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	defer signal.Stop(ch)
	for {
		select {
		case <-ch:
			if w, h, err := term.GetSize(fd); err == nil && w > 0 {
				t.mu.Lock()
				t.width, t.height = w, h
				t.redrawLocked()
				t.mu.Unlock()
			}
		case <-t.done:
			return
		}
	}
}

// Printf writes a line above the prompt.
func (t *Term) Printf(format string, a ...any) {
	t.Print(fmt.Sprintf(format, a...))
}

// Print writes text above the prompt, one terminal line per line of it.
func (t *Term) Print(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	if t.plain {
		fmt.Fprintln(t.out, s)
		return
	}
	var b strings.Builder
	b.WriteString("\r\x1b[K")
	for _, line := range strings.Split(s, "\n") {
		b.WriteString(line)
		b.WriteString("\r\n")
	}
	fmt.Fprint(t.out, b.String())
	t.redrawLocked()
}

// SetPrompt changes the prompt and redraws it.
func (t *Term) SetPrompt(p string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prompt = p
	t.redrawLocked()
}

// Line is what is being typed.
func (t *Term) Line() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.line)
}

// SetLine replaces what is being typed, putting the cursor at the end.
func (t *Term) SetLine(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.line = []rune(s)
	t.pos = len(t.line)
	t.redrawLocked()
}

// Key applies one keystroke to the line being edited and reports
// whether it was consumed.  A key this does not handle -- Enter, tab,
// the prefix key -- is left to the caller, which is what decides what
// they mean.
func (t *Term) Key(r rune) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	switch r {
	case keyLeft, 2: // Ctrl-B
		if t.pos > 0 {
			t.pos--
		}
	case keyRight, 6: // Ctrl-F
		if t.pos < len(t.line) {
			t.pos++
		}
	case keyHome, 1: // Ctrl-A
		t.pos = 0
	case keyEnd, 5: // Ctrl-E
		t.pos = len(t.line)
	case 127, 8: // backspace
		if t.pos > 0 {
			t.line = append(t.line[:t.pos-1], t.line[t.pos:]...)
			t.pos--
		}
	case keyDelete, 4: // Ctrl-D deletes forward when there is something to delete
		if t.pos < len(t.line) {
			t.line = append(t.line[:t.pos], t.line[t.pos+1:]...)
		} else if r == 4 {
			return false // Ctrl-D on an empty tail means end of input
		}
	case 21: // Ctrl-U, kill to start
		t.line = append([]rune{}, t.line[t.pos:]...)
		t.pos = 0
	case 11: // Ctrl-K, kill to end
		t.line = t.line[:t.pos]
	case 23: // Ctrl-W, kill the word behind
		i := t.pos
		for i > 0 && t.line[i-1] == ' ' {
			i--
		}
		for i > 0 && t.line[i-1] != ' ' {
			i--
		}
		t.line = append(append([]rune{}, t.line[:i]...), t.line[t.pos:]...)
		t.pos = i
	case 12: // Ctrl-L, redraw
	default:
		if r < 32 || r == keyUp || r == keyDown {
			return false
		}
		t.line = append(t.line, 0)
		copy(t.line[t.pos+1:], t.line[t.pos:])
		t.line[t.pos] = r
		t.pos++
	}
	t.redrawLocked()
	return true
}

// Take returns the line being edited and clears it.
func (t *Term) Take() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := string(t.line)
	t.line = t.line[:0]
	t.pos = 0
	t.redrawLocked()
	return s
}

// Echo writes the prompt and the line as it stands and starts a fresh
// one, the way a terminal does when Enter is pressed, so that what
// follows reads as a transcript: the command, then its output, then the
// next command.
//
// It writes the text out rather than leaving what is already drawn.  A
// line wider than the terminal is drawn as a window on to it, scrolled
// sideways to keep the cursor in view, so keeping the drawn version
// would record a fragment of the command instead of the command.
//
// A pipe is not a terminal and nothing was drawn on it, so there is
// nothing to leave behind: echoing there would put the commands in the
// middle of output that is on its way to a file.
func (t *Term) Echo() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.plain || t.closed {
		return
	}
	fmt.Fprint(t.out, "\r\x1b[K"+t.prompt+string(t.line)+"\r\n")
}

// redrawLocked paints the prompt and the line, scrolling sideways when
// the two are wider than the terminal so that the cursor is always on
// screen.  A wrapped line would leave the display a mess after the next
// message arrives above it.
func (t *Term) redrawLocked() {
	if t.plain || t.closed {
		return
	}
	avail := t.width - len([]rune(t.prompt)) - 1
	if avail < 8 {
		avail = 8
	}
	start := 0
	if t.pos > avail {
		start = t.pos - avail
	}
	end := len(t.line)
	if end > start+avail {
		end = start + avail
	}
	shown := string(t.line[start:end])
	fmt.Fprintf(t.out, "\r\x1b[K%s%s", t.prompt, shown)
	// Put the cursor where it belongs, counting from the left edge.
	// Column zero is a bare carriage return: a cursor-forward of zero
	// moves one column in terminals that read it as the default.
	if col := len([]rune(t.prompt)) + (t.pos - start); col > 0 {
		fmt.Fprintf(t.out, "\r\x1b[%dC", col)
	} else {
		fmt.Fprint(t.out, "\r")
	}
}
