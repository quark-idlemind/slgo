package md

import (
	"strconv"
	"strings"
)

// The drawing characters a terminal already has.  Named as escapes so
// the source of this file stays ASCII, which is the rule for comments
// and .go text here; the output is allowed the rest of Unicode.
const (
	bullet   = "\u2022"
	vbar     = "\u2502"
	hline    = "\u2500"
	hdouble  = "\u2550"
	cornerTL = "\u250c"
	cornerTR = "\u2510"
	cornerBL = "\u2514"
	cornerBR = "\u2518"
	teeL     = "\u251c"
	teeR     = "\u2524"
	teeT     = "\u252c"
	teeB     = "\u2534"
	cross    = "\u253c"
)

func renderBlocks(blocks []block, width int, tight bool) string {
	if width < 1 {
		width = 1
	}
	var b strings.Builder
	for i, bl := range blocks {
		if i > 0 && !tight {
			b.WriteByte('\n')
		}
		writeBlock(&b, bl, width)
	}
	return b.String()
}

func writeBlock(b *strings.Builder, bl block, width int) {
	switch v := bl.(type) {
	case *para:
		b.WriteString(wrap(v.spans, width, "", ""))
		b.WriteByte('\n')
	case *heading:
		writeHeading(b, v, width)
	case *codeBlock:
		writeCode(b, v.text)
	case *quote:
		writeQuote(b, v, width)
	case *listBlock:
		writeList(b, v, width)
	case *rule:
		n := width
		if n < 1 {
			n = 1
		}
		b.WriteString(strings.Repeat(hline, n))
		b.WriteByte('\n')
	case *table:
		writeTable(b, v, width)
	}
}

func writeHeading(b *strings.Builder, h *heading, width int) {
	spans := make([]span, len(h.spans))
	for i, s := range h.spans {
		s.style.bold = true
		spans[i] = s
	}
	text := wrap(spans, width, "", "")
	b.WriteString(text)
	b.WriteByte('\n')
	if h.level > 2 {
		return
	}
	w := longestLine(text)
	if w < 1 {
		w = 1
	}
	ch := hline
	if h.level == 1 {
		ch = hdouble
	}
	b.WriteString(strings.Repeat(ch, w))
	b.WriteByte('\n')
}

func writeCode(b *strings.Builder, text string) {
	if text == "" {
		return
	}
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		b.WriteString("    ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
}

func writeQuote(b *strings.Builder, q *quote, width int) {
	innerW := width - 2
	if innerW < 8 {
		innerW = width
		if innerW < 1 {
			innerW = 1
		}
	}
	inner := renderBlocks(q.inner, innerW, false)
	b.WriteString(prefixLines(inner, vbar+" ", vbar+" "))
}

func writeList(b *strings.Builder, l *listBlock, width int) {
	n := l.start
	if !l.ordered {
		n = 1
	}
	if l.ordered && n < 1 {
		n = 1
	}
	for i, item := range l.items {
		if i > 0 && l.loose {
			b.WriteByte('\n')
		}
		var first string
		if l.ordered {
			first = strconv.Itoa(n) + ". "
		} else {
			first = bullet + " "
		}
		next := strings.Repeat(" ", columns(first))
		innerW := width - columns(first)
		if innerW < 8 {
			innerW = width
			if innerW < 1 {
				innerW = 1
			}
		}
		inner := renderBlocks(item, innerW, !l.loose)
		if inner == "" {
			b.WriteString(first)
			b.WriteByte('\n')
		} else {
			b.WriteString(prefixLines(inner, first, next))
		}
		n++
	}
}

func prefixLines(text, first, next string) string {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return first + "\n"
	}
	lines := strings.Split(text, "\n")
	var b strings.Builder
	for i, line := range lines {
		if i == 0 {
			b.WriteString(first)
		} else {
			b.WriteString(next)
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func writeTable(b *strings.Builder, t *table, width int) {
	if len(t.rows) == 0 {
		return
	}
	cols := len(t.align)
	widths := make([]int, cols)
	cells := make([][][]span, len(t.rows))
	for r, row := range t.rows {
		cells[r] = make([][]span, cols)
		for c := 0; c < cols; c++ {
			var spans []span
			if c < len(row) {
				spans = row[c]
			}
			if r == 0 {
				spans = withBold(spans)
			}
			cells[r][c] = spans
			if w := spanColumns(spans); w > widths[c] {
				widths[c] = w
			}
		}
	}
	for i := range widths {
		if widths[i] < 1 {
			widths[i] = 1
		}
	}
	widths = fitWidths(widths, width)

	rendered := make([][][]string, len(cells))
	for r, row := range cells {
		rendered[r] = make([][]string, cols)
		for c, spans := range row {
			text := wrap(spans, widths[c], "", "")
			if text == "" {
				rendered[r][c] = []string{""}
			} else {
				rendered[r][c] = strings.Split(text, "\n")
			}
		}
	}

	writeRule := func(left, mid, right string) {
		b.WriteString(left)
		for c, w := range widths {
			if c > 0 {
				b.WriteString(mid)
			}
			b.WriteString(strings.Repeat(hline, w+2))
		}
		b.WriteString(right)
		b.WriteByte('\n')
	}

	start := 0
	if len(cells) > 0 && rowEmpty(cells[0]) {
		// GFM wants a header row even when the author had no names
		// for the columns.  Drawing that row is a blank bar under
		// the top rule; skip it and treat every remaining row as
		// body.
		start = 1
	}

	writeRule(cornerTL, teeT, cornerTR)
	for r := start; r < len(rendered); r++ {
		row := rendered[r]
		height := 1
		for _, lines := range row {
			if len(lines) > height {
				height = len(lines)
			}
		}
		for y := 0; y < height; y++ {
			b.WriteString(vbar)
			for c, lines := range row {
				cell := ""
				if y < len(lines) {
					cell = lines[y]
				}
				b.WriteByte(' ')
				b.WriteString(padCell(cell, widths[c], t.align[c]))
				b.WriteByte(' ')
				b.WriteString(vbar)
			}
			b.WriteByte('\n')
		}
		if r == start && start == 0 && len(rendered) > 1 {
			writeRule(teeL, cross, teeR)
		}
	}
	writeRule(cornerBL, teeB, cornerBR)
}

func rowEmpty(row [][]span) bool {
	for _, cells := range row {
		for _, s := range cells {
			if strings.TrimSpace(s.text) != "" {
				return false
			}
		}
	}
	return true
}

func spanColumns(spans []span) int {
	n := 0
	for _, s := range spans {
		n += columns(s.text)
	}
	return n
}

// fitWidths shrinks columns until the boxed table is no wider than
// width.  The widest column gives up a cell first, so a long prose
// column yields before a short label column.  A word longer than the
// room left still overflows -- the same rule as wrap, and for the
// same reason: a path cut in half cannot be copied.
func fitWidths(widths []int, width int) []int {
	out := append([]int(nil), widths...)
	need := 1 + 3*len(out)
	for _, w := range out {
		need += w
	}
	for need > width {
		j := 0
		for i, w := range out {
			if w > out[j] {
				j = i
			}
		}
		if out[j] <= 1 {
			break
		}
		out[j]--
		need--
	}
	return out
}

func withBold(spans []span) []span {
	if len(spans) == 0 {
		return spans
	}
	out := make([]span, len(spans))
	for i, s := range spans {
		s.style.bold = true
		out[i] = s
	}
	return out
}

func padCell(s string, width int, a align) string {
	vis := visibleColumns(s)
	extra := width - vis
	if extra < 0 {
		extra = 0
	}
	pad := strings.Repeat(" ", extra)
	switch a {
	case alignRight:
		return pad + s
	case alignCenter:
		l := extra / 2
		return strings.Repeat(" ", l) + s + strings.Repeat(" ", extra-l)
	default:
		return s + pad
	}
}
