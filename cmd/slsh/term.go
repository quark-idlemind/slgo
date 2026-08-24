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
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
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

// The escape sequences the shell writes, one constant per sequence.
// The block above is the ones that arrive; these are the ones that go
// out, and every place that writes to a terminal writes one of these
// rather than spelling it out again.
//
// A caller that wants two of them writes them added together, so that
// each name still stands for a whole sequence and not for half of one:
// the erase after a carriage return is "\r"+eraseLine.
// Nothing here erases the screen or moves the cursor to a row of its
// choosing.  This shell writes under what it has already written and
// rewrites the line the cursor is on, which is what leaves the
// terminal's scrollback intact; see the pager, which is the part that
// would otherwise have wanted a clear screen.
const (
	eraseLine     = "\x1b[K"   // from the cursor to the end of the line
	cursorForward = "\x1b[%dC" // right by the number of columns given
	reverseOn     = "\x1b[7m"  // swap foreground and background
	attrsOff      = "\x1b[0m"  // back to the terminal's own colours
	askCellSize   = "\x1b[16t" // how big is a character cell?  See CellSize
)

// escapeWait is how long a lone ESC waits for the rest of a sequence
// before it is taken to be the ESC key itself.
//
// This is the price of ESC being both a key and the first byte of every
// arrow key.  A terminal sends the whole sequence in one write, so the
// rest is already in the buffer if it is coming; the wait only has to
// outlast the scheduler, not the network.
const escapeWait = 40 * time.Millisecond

// cellSizeWait is how long CellSize waits for the terminal to say how
// big a character cell is.
//
// A terminal that does not know the question answers nothing at all --
// there is no refusal to receive -- so the only way to stop waiting is
// to stop waiting.  The whole of this wait is therefore paid by exactly
// the people who are about to be told their terminal will not say,
// which is what decides the length: a second is far longer than the
// round trip to a terminal at the far end of an ssh link, and short
// enough that it reads as an answer rather than as a shell that has
// hung.
const cellSizeWait = time.Second

// maxCSIParams bounds how much of an escape sequence is swallowed while
// looking for the byte that ends it.  Nothing sends a sequence a
// quarter this long; the bound is there so that a stray ESC [ in a
// paste cannot eat the keyboard until a bracket happens to arrive.
const maxCSIParams = 64

// cellSize is how big one character cell is in pixels, as the terminal
// reports it: height first, the order a cell is written in everywhere
// here.  See CellRatio, which is the same order for the same reason.
type cellSize struct{ tall, wide int }

// Term is the terminal, in raw mode, with a line being edited on the
// bottom line.
type Term struct {
	in  *os.File
	out io.Writer

	keys chan rune
	done chan struct{}

	// cells carries the terminal's answer about its cell size back from
	// the decoder, which is where it arrives: the reply comes in
	// through the keyboard, being the only way back a terminal has.
	//
	// One deep, and never blocked on.  A report nobody is waiting for
	// is a real thing to receive -- somebody can type the query by hand
	// at the prompt, and a terminal may answer one this gave up on --
	// and the goroutine that would be left holding it is the one
	// reading the keyboard.
	cells chan cellSize

	// asking is held for the length of one question to the terminal, so
	// that two of them cannot be outstanding at once.  Nothing here
	// needs two, and two answers arriving on one channel could not be
	// told apart if it did.
	asking sync.Mutex

	mu      sync.Mutex
	prompt  string
	line    []rune
	pos     int // cursor, as an index into line
	width   int
	height  int
	restore func() error
	closed  bool

	// busy says a command is running.  While it is, the prompt is not
	// drawn: a prompt means the shell is ready for the next line, and
	// it is not -- keys typed at it wait in the queue until the command
	// returns.  Output still prints; only the prompt waits.
	busy bool

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
		cells:  make(chan cellSize, 1),
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

// Cols is how wide the terminal is, which is what decides where prose
// is wrapped.  A pipe has no width and answers 80, the width a terminal
// is assumed to have until one says otherwise.
func (t *Term) Cols() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.width
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
		fmt.Fprint(t.out, "\r"+eraseLine)
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
			// The whole of the sequence is read, however little of it
			// is understood.  ESC O is three bytes and the third ends
			// it; a CSI sequence runs until a byte in the range 0x40 to
			// 0x7E, with its parameters -- the digits and semicolons of
			// "6;18;10" -- in front of that.
			//
			// Reading to the end matters as much for the sequences
			// nothing here answers to as for the ones it does.  This
			// used to stop at the third byte and give up on anything it
			// did not recognise, which left the REST of the sequence in
			// the stream to be decoded as ordinary keys: a report of
			// the cell size, ESC [ 6 ; 18 ; 10 t, typed ";18;10t" at
			// the prompt, and a bracketed paste typed "00~".
			var params []byte
			var final byte
			if b2 == 'O' {
				// One byte, and that is the whole of it.
				if final, ok = next(); !ok {
					return
				}
			} else {
				var ended bool
				if params, final, ended = readCSI(next); ended {
					return
				}
				if final == 0 {
					// A sequence that ran on past being one.  What was
					// read is dropped and what follows is ordinary keys
					// again.
					continue
				}
			}

			// The parameters are ignored for the letters, because a
			// terminal saying Ctrl-Right -- ESC [ 1 ; 5 C -- is saying
			// right, and a line editor has nothing else to do with the
			// Ctrl.
			var r rune
			switch final {
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
			case '~':
				// The numbered spellings of the same keys, which is
				// what some terminals send instead.
				switch string(params) {
				case "1", "7":
					r = keyHome
				case "3":
					r = keyDelete
				case "4", "8":
					r = keyEnd
				}
			case 't':
				// The terminal answering a question about its window,
				// which is it talking back rather than anything anybody
				// typed.  See CellSize.
				if c, ok := parseCellSize(params); ok {
					select {
					case t.cells <- c:
					default:
						// Nobody is waiting for it, or the one who was
						// has given up.  Dropped rather than held on
						// to: this goroutine is the keyboard.
					}
				}
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

// readCSI reads the rest of an escape sequence that began ESC [ : the
// parameter bytes, and the byte in the range 0x40 to 0x7E that ends it.
//
// ended says the input ran out half way, which is the caller's cue to
// stop altogether.  A final byte of zero with ended false is a sequence
// that ran on past maxCSIParams and so is not a sequence at all: what
// was read is thrown away and decoding carries on with the next byte.
func readCSI(next func() (byte, bool)) (params []byte, final byte, ended bool) {
	for {
		c, ok := next()
		if !ok {
			return nil, 0, true
		}
		if c >= 0x40 && c <= 0x7e {
			return params, c, false
		}
		if len(params) >= maxCSIParams {
			return nil, 0, false
		}
		params = append(params, c)
	}
}

// parseCellSize reads the parameters of the answer to \033[16t, which
// arrives whole as ESC [ 6 ; HEIGHT ; WIDTH t.
//
// The leading 6 is the terminal saying which question it is answering,
// and it is checked rather than skipped: \033[14t and \033[18t come
// back through the same final letter with a 4 and an 8 in front of
// them, and neither of those is the size of a cell.
func parseCellSize(params []byte) (cellSize, bool) {
	f := strings.Split(string(params), ";")
	if len(f) != 3 || f[0] != "6" {
		return cellSize{}, false
	}
	tall, err := strconv.Atoi(f[1])
	if err != nil || tall < 1 {
		return cellSize{}, false
	}
	wide, err := strconv.Atoi(f[2])
	if err != nil || wide < 1 {
		return cellSize{}, false
	}
	return cellSize{tall: tall, wide: wide}, true
}

// CellSize asks the terminal how big one character cell is and answers
// with its height and its width, in pixels.
//
// \033[16t is the question, and the answer comes back through the
// KEYBOARD, that being the only way a terminal has of replying: the
// decoder picks ESC [ 6 ; HEIGHT ; WIDTH t out of what it is reading
// and hands it over here, rather than delivering ";18;10t" as though
// somebody had typed it.
//
// Anything actually typed while this waits is untouched.  It goes down
// the same channel it always does and is read when whatever called this
// returns, which is the same queue a key pressed during any other
// command waits in.
//
// A terminal that does not know the question says nothing at all, so
// the wait is bounded and a refusal is what a shell gets rather than a
// hang; see cellSizeWait, which the caller passes.
func (t *Term) CellSize(timeout time.Duration) (tall, wide int, err error) {
	if t.plain {
		return 0, 0, errors.New("this is a pipe, not a terminal, so there is nothing to ask")
	}

	// One question at a time.  See asking.
	t.asking.Lock()
	defer t.asking.Unlock()

	// Anything already waiting belongs to a question that is over -- one
	// that timed out, or one nobody asked -- and would otherwise be
	// handed back as the answer to this one.
	select {
	case <-t.cells:
	default:
	}

	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return 0, 0, errors.New("the terminal has been closed")
	}
	// Nothing is drawn by this and nothing needs drawing again: the
	// question is not displayed, so the prompt under it is untouched.
	_, err = fmt.Fprint(t.out, askCellSize)
	t.mu.Unlock()
	if err != nil {
		return 0, 0, err
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case c := <-t.cells:
		return c.tall, c.wide, nil
	case <-t.done:
		return 0, 0, errors.New("the terminal has been closed")
	case <-timer.C:
		// Written as it would be typed, since somebody who reads this is
		// being sent to try it by hand.  An ESC in a message would go to
		// the terminal as an ESC.
		return 0, 0, errors.New("this terminal did not answer \\033[16t")
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
	b.WriteString("\r" + eraseLine)
	for _, line := range strings.Split(s, "\n") {
		b.WriteString(line)
		b.WriteString("\r\n")
	}
	fmt.Fprint(t.out, b.String())
	t.redrawLocked()
}

// Paint writes s to the terminal as-is, without the prompt.
//
// It is for a command that has taken the display over while busy --
// the man pager -- and would leave the prompt in the wrong place if it
// went through Print.  A pipe has no display to take over.
func (t *Term) Paint(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.plain || t.closed {
		return
	}
	fmt.Fprint(t.out, s)
}

// Status writes a line in place, over whatever Status wrote last.
//
// It is for progress: something worth watching while it happens and not
// worth keeping afterwards, so it is overwritten rather than scrolled
// and it never reaches a file.  A command's real output goes to its
// writer, which may be a redirect; this goes to the terminal or
// nowhere.  Status("") clears the line.
func (t *Term) Status(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.plain || t.closed {
		return
	}
	fmt.Fprint(t.out, "\r"+eraseLine+s)
}

// SetBusy says whether a command is running, and so whether a prompt
// would be telling the truth.
func (t *Term) SetBusy(busy bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.busy = busy
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
	fmt.Fprint(t.out, "\r"+eraseLine+t.prompt+string(t.line)+"\r\n")
}

// redrawLocked paints the prompt and the line, scrolling sideways when
// the two are wider than the terminal so that the cursor is always on
// screen.  A wrapped line would leave the display a mess after the next
// message arrives above it.
func (t *Term) redrawLocked() {
	if t.plain || t.closed || t.busy {
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
	fmt.Fprintf(t.out, "\r"+eraseLine+"%s%s", t.prompt, shown)
	// Put the cursor where it belongs, counting from the left edge.
	// Column zero is a bare carriage return: a cursor-forward of zero
	// moves one column in terminals that read it as the default.
	if col := len([]rune(t.prompt)) + (t.pos - start); col > 0 {
		fmt.Fprintf(t.out, "\r"+cursorForward, col)
	} else {
		fmt.Fprint(t.out, "\r")
	}
}
