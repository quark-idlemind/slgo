package main

// A pager for man, in the spirit of more(1): one screenful at a time,
// with a handful of keys to move and to search.  It only runs when the
// page is going to a real terminal; a redirect or a pipe gets the whole
// text, because there is nobody there to press space.

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"
)

// page shows text a screenful at a time.  A page that fits is printed
// in full and does not wait.  q, Ctrl-C, or space at the end leave.
func page(ctx context.Context, t *Term, text string) error {
	if t == nil || t.Plain() {
		return nil
	}
	p := newPager(text, t.Rows())
	if p.fits() {
		t.Print(strings.TrimRight(text, "\n"))
		return nil
	}
	defer p.finish(t)
	for {
		p.view = t.Rows() - 1
		if p.view < 1 {
			p.view = 1
		}
		p.paint(t)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case r, ok := <-t.Keys():
			if !ok {
				return nil
			}
			if p.handle(ctx, t, r) {
				return nil
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
		p.paint(t)
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

func (p *pager) paint(t *Term) {
	var b strings.Builder
	b.WriteString(cursorHome + eraseScreen)
	for i := 0; i < p.view; i++ {
		if j := p.top + i; j < len(p.lines) {
			b.WriteString(p.lines[j])
		}
		b.WriteString("\r\n")
	}
	b.WriteString(reverseOn)
	b.WriteString(p.status())
	b.WriteString(attrsOff + eraseLine)
	t.Paint(b.String())
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
	// Drop the status line and leave the last screen sitting above
	// the prompt that will come back.
	row := t.Rows()
	if row < 1 {
		row = 1
	}
	t.Paint(fmt.Sprintf(cursorRow+eraseLine, row))
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
