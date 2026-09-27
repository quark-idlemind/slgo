package main

// A pager for man, in the spirit of more(1): one screenful at a time,
// with a handful of keys to move and to search.  It only runs when the
// page is going to a real terminal; a redirect or a pipe gets the whole
// text, because there is nobody there to press space.
//
// Nothing here erases the screen or moves the cursor up it.  A pager
// that clears takes the terminal's scrollback with it -- whatever was
// on screen before the page began is gone, and so is the page itself
// once it has been read.  This one only ever appends: each screenful is
// written under the last, the old screenful scrolls off the top, and
// everything stays where the terminal keeps everything else that has
// been printed.  The one line it does rewrite in place is its own
// status line, which is on the bottom line and is erased on the way
// out.

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"
)

// page shows text a screenful at a time.  A page that fits is printed
// in full and does not wait.  q, Ctrl-C, or space at the end leave.
//
// The text is written as it is, escape sequences and all, whichever
// way it goes out: a man page is this program's own words with bold in
// them, and the bold is escape sequences.  So nothing from the grid is
// to be paged; see Paint and printOwn, which are the two ways out.
func page(ctx context.Context, t *Term, text string) error {
	if t == nil || t.Plain() {
		return nil
	}
	p := newPager(text, t.Rows())
	if p.fits() {
		t.printOwn(strings.TrimRight(text, "\n"))
		return nil
	}
	defer p.finish(t)
	p.resize(t)
	p.paint(t)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case r, ok := <-t.Keys():
			if !ok {
				return nil
			}
			p.resize(t)
			was := p.top
			if p.handle(ctx, t, r) {
				return nil
			}
			// A key that moved nothing -- a stray letter, a search
			// that found no line -- must not print the screenful
			// again, because printing is the only thing this pager
			// can do and a second copy would be there for good.
			if p.top != was {
				p.paint(t)
			} else {
				p.showStatus(t)
			}
		}
	}
}

type pager struct {
	lines []string
	top   int
	view  int
	query string
	dir   int // last search: +1 from '/', -1 from '?'
	msg   string
	mode  rune // 0, or '/' or '?' while typing a search
	buf   []rune
	shown int // how far down has been written; a step on writes none twice
}

func newPager(text string, rows int) *pager {
	text = strings.TrimRight(text, "\n")
	var lines []string
	if text == "" {
		lines = nil
	} else {
		lines = strings.Split(text, "\n")
	}
	view := rows - 1
	if view < 1 {
		view = 1
	}
	return &pager{lines: lines, view: view}
}

func (p *pager) fits() bool {
	return len(p.lines) <= p.view
}

func (p *pager) maxTop() int {
	if len(p.lines) <= p.view {
		return 0
	}
	return len(p.lines) - p.view
}

func (p *pager) home() { p.top = 0 }

func (p *pager) back(n int) {
	if n < 1 {
		n = 1
	}
	p.top -= n
	if p.top < 0 {
		p.top = 0
	}
}

// forward moves down.  It reports whether the move was off the end,
// which is how space leaves once the last screen is on display.
func (p *pager) forward(n int) bool {
	if n < 1 {
		n = 1
	}
	if p.top >= p.maxTop() {
		return true
	}
	p.top += n
	if p.top > p.maxTop() {
		p.top = p.maxTop()
	}
	return false
}

func (p *pager) half() int {
	n := p.view / 2
	if n < 1 {
		n = 1
	}
	return n
}

// search looks from the line after (or before) the top of the window
// for a line containing q.  An empty q repeats the last query.  The
// matching line is brought to the top, or as near as the last screen
// allows.
func (p *pager) search(q string, dir int) bool {
	if q == "" {
		q = p.query
	} else {
		p.query = q
	}
	if q == "" || dir == 0 || len(p.lines) == 0 {
		return false
	}
	for i := p.top + dir; i >= 0 && i < len(p.lines); i += dir {
		if lineHas(p.lines[i], q) {
			p.top = i
			return true
		}
	}
	return false
}

func lineHas(line, q string) bool {
	return strings.Contains(stripANSI(line), q)
}

func (p *pager) handle(ctx context.Context, t *Term, r rune) bool {
	p.msg = ""
	switch r {
	case ' ', 'f':
		return p.forward(p.view)
	case '\r', '\n':
		// One line, for reading down through something slowly.  It
		// leaves at the end of the page for the same reason space
		// does: there is nothing further forward to go to.
		return p.forward(1)
	case 'b':
		p.back(p.view)
	case 'd':
		p.forward(p.half())
	case 'u':
		p.back(p.half())
	case 'p':
		p.home()
	case 'q', 3:
		return true
	case '/':
		p.promptSearch(ctx, t, 1)
	case '?':
		p.promptSearch(ctx, t, -1)
	case 'n':
		if !p.search("", p.dir) {
			p.msg = "not found"
		}
	}
	return false
}

func (p *pager) promptSearch(ctx context.Context, t *Term, dir int) {
	if dir < 0 {
		p.mode = '?'
	} else {
		p.mode = '/'
	}
	p.buf = nil
	defer func() { p.mode = 0; p.buf = nil }()
	for {
		p.showStatus(t)
		select {
		case <-ctx.Done():
			return
		case r, ok := <-t.Keys():
			if !ok {
				return
			}
			switch r {
			case '\r', '\n':
				p.dir = dir
				if !p.search(string(p.buf), dir) {
					p.msg = "not found"
				}
				return
			case 3, 27:
				return
			case 127, 8:
				if len(p.buf) > 0 {
					p.buf = p.buf[:len(p.buf)-1]
				}
			default:
				if r >= 32 && utf8.ValidRune(r) {
					p.buf = append(p.buf, r)
				}
			}
		}
	}
}

// resize takes the size of the window from the terminal, which may
// have been resized since the last key.  One row is held back for the
// status line.
func (p *pager) resize(t *Term) {
	p.view = t.Rows() - 1
	if p.view < 1 {
		p.view = 1
	}
}

// paint writes the lines the window has moved on to, under whatever is
// already on the screen, and puts the status line below them.
//
// A step that carries on from where the last one stopped writes only
// the lines nobody has seen: a half-screen step prints half a screen,
// as more(1) does, rather than printing the half that is still on the
// screen a second time.  A step backwards, or a search that lands
// somewhere else entirely, has no such run to continue and writes the
// screenful whole.  Short lines are not padded out to a screenful --
// the blank lines would be as permanent as the text.
func (p *pager) paint(t *Term) {
	end := p.top + p.view
	if end > len(p.lines) {
		end = len(p.lines)
	}
	start := p.top
	if p.shown > start && p.shown < end {
		start = p.shown
	}
	if end > p.shown {
		p.shown = end
	}
	var b strings.Builder
	b.WriteString("\r" + eraseLine) // the status line of the last screenful
	for _, line := range p.lines[start:end] {
		b.WriteString(line)
		b.WriteString("\r\n")
	}
	b.WriteString(p.statusLine())
	t.Paint(b.String())
}

// showStatus rewrites the status line where it stands, for a keystroke
// that changes what it says and nothing else: every character typed
// into a search, which would otherwise print a screenful apiece.
func (p *pager) showStatus(t *Term) {
	t.Paint(p.statusLine())
}

// statusLine is the bottom line, in reverse video.  The erase comes
// before the video is turned on so that the part of the line with
// nothing on it is erased in the terminal's own colours rather than in
// a bar of ink stretching to the right margin.
func (p *pager) statusLine() string {
	return "\r" + eraseLine + reverseOn + p.status() + attrsOff
}

func (p *pager) status() string {
	if p.mode == '/' || p.mode == '?' {
		return string(p.mode) + string(p.buf)
	}
	if p.msg != "" {
		return p.msg
	}
	if p.top >= p.maxTop() {
		return "(END)"
	}
	return fmt.Sprintf("--More-- %d%%", p.percent())
}

// percent is how far the bottom of the window is through the page.
// The first screen is already some of the document, so it is not 0%.
func (p *pager) percent() int {
	n := len(p.lines)
	if n == 0 {
		return 100
	}
	bot := p.top + p.view
	if bot > n {
		bot = n
	}
	return (100 * bot) / n
}

func (p *pager) finish(t *Term) {
	// Drop the status line and leave the last screenful sitting above
	// the prompt that will come back.  The cursor is already on the
	// status line, so there is nowhere to move it to.
	t.Paint("\r" + eraseLine)
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '\x1b' {
			i++
			if i < len(s) && s[i] == '[' {
				i++
				for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
					i++
				}
				if i < len(s) {
					i++
				}
			}
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
