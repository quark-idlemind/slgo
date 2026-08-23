package md

import (
	"strconv"
	"strings"
)

type block any

type para struct{ spans []span }

type heading struct {
	level int
	spans []span
}

type codeBlock struct{ text string }

type quote struct{ inner []block }

type listBlock struct {
	ordered bool
	start   int
	loose   bool
	items   [][]block
}

type rule struct{}

type align int

const (
	alignLeft align = iota
	alignCenter
	alignRight
)

type table struct {
	align []align
	rows  [][][]span // rows[0] is the header
}

type parser struct {
	lines []string
	i     int
}

func parse(src string) []block {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	src = strings.ReplaceAll(src, "\r", "\n")
	src = strings.TrimRight(src, "\n")
	if src == "" {
		return nil
	}
	p := &parser{lines: strings.Split(src, "\n")}
	return p.blocks()
}

func (p *parser) blocks() []block {
	var out []block
	for p.i < len(p.lines) {
		if isBlank(p.lines[p.i]) {
			p.i++
			continue
		}
		out = append(out, p.one())
	}
	return out
}

func (p *parser) one() block {
	line := p.lines[p.i]
	if _, _, _, _, ok := fenceOpen(line); ok {
		return p.fence()
	}
	if level, text, ok := atx(line); ok {
		p.i++
		return &heading{level: level, spans: parseInline(text)}
	}
	if isRule(line) {
		p.i++
		return &rule{}
	}
	if _, ok := stripQuote(line); ok {
		return p.quote()
	}
	if _, ok := parseMarker(line); ok {
		return p.list()
	}
	if p.i+1 < len(p.lines) && looksLikeTableRow(line) && isTableSep(p.lines[p.i+1]) {
		return p.table()
	}
	if indentOf(line) >= 4 {
		return p.indentedCode()
	}
	return p.paragraph()
}

func (p *parser) fence() block {
	indent, ch, n, _, _ := fenceOpen(p.lines[p.i])
	p.i++
	var body []string
	for p.i < len(p.lines) {
		if closeFence(p.lines[p.i], ch, n) {
			p.i++
			break
		}
		body = append(body, stripIndent(p.lines[p.i], indent))
		p.i++
	}
	return &codeBlock{text: strings.Join(body, "\n")}
}

func (p *parser) indentedCode() block {
	var lines []string
	for p.i < len(p.lines) {
		line := p.lines[p.i]
		if isBlank(line) {
			if !p.moreIndentedCode() {
				break
			}
			lines = append(lines, "")
			p.i++
			continue
		}
		if indentOf(line) < 4 {
			break
		}
		lines = append(lines, stripIndent(line, 4))
		p.i++
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return &codeBlock{text: strings.Join(lines, "\n")}
}

func (p *parser) moreIndentedCode() bool {
	for j := p.i + 1; j < len(p.lines); j++ {
		if isBlank(p.lines[j]) {
			continue
		}
		return indentOf(p.lines[j]) >= 4
	}
	return false
}

func (p *parser) quote() block {
	var inner []string
	for p.i < len(p.lines) {
		rest, ok := stripQuote(p.lines[p.i])
		if !ok {
			break
		}
		inner = append(inner, rest)
		p.i++
	}
	return &quote{inner: parse(strings.Join(inner, "\n"))}
}

func (p *parser) list() block {
	m, ok := parseMarker(p.lines[p.i])
	if !ok {
		return p.paragraph()
	}
	indent := m.indent
	ordered := m.ordered
	start := m.start
	var items [][]block
	loose := false
	for p.i < len(p.lines) {
		if isBlank(p.lines[p.i]) {
			p.i++
			for p.i < len(p.lines) && isBlank(p.lines[p.i]) {
				p.i++
			}
			if p.i >= len(p.lines) {
				break
			}
			m2, ok := parseMarker(p.lines[p.i])
			if !ok || m2.indent != indent || m2.ordered != ordered {
				break
			}
			loose = true
		}
		m, ok = parseMarker(p.lines[p.i])
		if !ok || m.indent != indent || m.ordered != ordered {
			break
		}
		items = append(items, p.listItem(m))
	}
	return &listBlock{ordered: ordered, start: start, loose: loose, items: items}
}

func (p *parser) listItem(m marker) []block {
	contentCol := m.width
	p.i++
	lines := []string{m.rest}
	for p.i < len(p.lines) {
		line := p.lines[p.i]
		if isBlank(line) {
			if p.i+1 < len(p.lines) && !isBlank(p.lines[p.i+1]) && indentOf(p.lines[p.i+1]) >= contentCol {
				lines = append(lines, "")
				p.i++
				continue
			}
			break
		}
		if m2, ok := parseMarker(line); ok && m2.indent <= m.indent {
			break
		}
		if indentOf(line) >= contentCol {
			lines = append(lines, stripIndent(line, contentCol))
			p.i++
			continue
		}
		break
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return parse(strings.Join(lines, "\n"))
}

func (p *parser) paragraph() block {
	var lines []string
	lines = append(lines, p.lines[p.i])
	p.i++
	for p.i < len(p.lines) {
		if isSetext(p.lines[p.i], '=') {
			p.i++
			return &heading{level: 1, spans: parseInline(joinPara(lines))}
		}
		if isSetext(p.lines[p.i], '-') {
			p.i++
			return &heading{level: 2, spans: parseInline(joinPara(lines))}
		}
		if p.startsBlock() {
			break
		}
		lines = append(lines, p.lines[p.i])
		p.i++
	}
	return &para{spans: parseInline(joinPara(lines))}
}

func (p *parser) startsBlock() bool {
	if p.i >= len(p.lines) {
		return true
	}
	line := p.lines[p.i]
	if isBlank(line) {
		return true
	}
	if _, _, _, _, ok := fenceOpen(line); ok {
		return true
	}
	if _, _, ok := atx(line); ok {
		return true
	}
	if isRule(line) {
		return true
	}
	if _, ok := stripQuote(line); ok {
		return true
	}
	if _, ok := parseMarker(line); ok {
		return true
	}
	if p.i+1 < len(p.lines) && looksLikeTableRow(line) && isTableSep(p.lines[p.i+1]) {
		return true
	}
	return false
}

func (p *parser) table() block {
	header := splitRow(p.lines[p.i])
	sep := splitRow(p.lines[p.i+1])
	p.i += 2
	al := make([]align, len(sep))
	for i, c := range sep {
		c = strings.TrimSpace(c)
		left := strings.HasPrefix(c, ":")
		right := strings.HasSuffix(c, ":")
		switch {
		case left && right:
			al[i] = alignCenter
		case right:
			al[i] = alignRight
		default:
			al[i] = alignLeft
		}
	}
	cols := len(header)
	if len(sep) > cols {
		cols = len(sep)
	}
	var rows [][][]span
	rows = append(rows, cellsToSpans(header, cols))
	for p.i < len(p.lines) {
		line := p.lines[p.i]
		if isBlank(line) || !strings.Contains(line, "|") {
			break
		}
		if p.startsBlockExceptTable() {
			break
		}
		rows = append(rows, cellsToSpans(splitRow(line), cols))
		p.i++
	}
	if len(al) < cols {
		extra := make([]align, cols-len(al))
		al = append(al, extra...)
	} else if len(al) > cols {
		al = al[:cols]
	}
	return &table{align: al, rows: rows}
}

func (p *parser) startsBlockExceptTable() bool {
	if p.i >= len(p.lines) {
		return true
	}
	line := p.lines[p.i]
	if isBlank(line) {
		return true
	}
	if _, _, _, _, ok := fenceOpen(line); ok {
		return true
	}
	if _, _, ok := atx(line); ok {
		return true
	}
	if isRule(line) {
		return true
	}
	if _, ok := stripQuote(line); ok {
		return true
	}
	if _, ok := parseMarker(line); ok {
		return true
	}
	return false
}

func cellsToSpans(cells []string, cols int) [][]span {
	out := make([][]span, cols)
	for i := 0; i < cols; i++ {
		if i < len(cells) {
			out[i] = parseInline(cells[i])
		}
	}
	return out
}

func joinPara(lines []string) string {
	var b strings.Builder
	prevHard := false
	for i, line := range lines {
		s := strings.TrimLeft(line, " \t")
		hard := false
		if strings.HasSuffix(s, `\`) {
			// A trailing backslash is a hard break, unless it is
			// itself escaped.  Counting backslashes from the end
			// is the usual "is this one escaped" test.
			n := 0
			for n < len(s) && s[len(s)-1-n] == '\\' {
				n++
			}
			if n%2 == 1 {
				s = strings.TrimRight(s[:len(s)-1], " \t")
				hard = true
			}
		}
		if !hard {
			trimmed := strings.TrimRight(s, " \t")
			if len(s) >= len(trimmed)+2 {
				s = strings.TrimSpace(s)
				hard = true
			} else {
				s = strings.TrimSpace(s)
			}
		}
		if i > 0 {
			if prevHard {
				b.WriteByte('\n')
			} else {
				b.WriteByte(' ')
			}
		}
		b.WriteString(s)
		prevHard = hard
	}
	return b.String()
}

func isBlank(line string) bool {
	return strings.TrimSpace(line) == ""
}

func indentOf(line string) int {
	n := 0
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case ' ':
			n++
		case '\t':
			n += 4 - n%4
		default:
			return n
		}
	}
	return n
}

func stripIndent(line string, cols int) string {
	n := 0
	i := 0
	for i < len(line) && n < cols {
		switch line[i] {
		case ' ':
			n++
			i++
		case '\t':
			n += 4 - n%4
			i++
		default:
			return line[i:]
		}
	}
	return line[i:]
}

func fenceOpen(line string) (indent int, ch byte, n int, info string, ok bool) {
	indent = indentOf(line)
	if indent > 3 {
		return 0, 0, 0, "", false
	}
	s := stripIndent(line, indent)
	if len(s) < 3 {
		return 0, 0, 0, "", false
	}
	ch = s[0]
	if ch != '`' && ch != '~' {
		return 0, 0, 0, "", false
	}
	for n < len(s) && s[n] == ch {
		n++
	}
	if n < 3 {
		return 0, 0, 0, "", false
	}
	info = strings.TrimSpace(s[n:])
	if ch == '`' && strings.Contains(info, "`") {
		return 0, 0, 0, "", false
	}
	return indent, ch, n, info, true
}

func closeFence(line string, ch byte, n int) bool {
	if indentOf(line) > 3 {
		return false
	}
	s := strings.TrimSpace(line)
	if len(s) < n {
		return false
	}
	i := 0
	for i < len(s) && s[i] == ch {
		i++
	}
	if i < n {
		return false
	}
	return strings.TrimSpace(s[i:]) == ""
}

func atx(line string) (level int, text string, ok bool) {
	if indentOf(line) > 3 {
		return 0, "", false
	}
	s := stripIndent(line, indentOf(line))
	n := 0
	for n < len(s) && s[n] == '#' {
		n++
	}
	if n < 1 || n > 6 {
		return 0, "", false
	}
	if n < len(s) && s[n] != ' ' && s[n] != '\t' {
		return 0, "", false
	}
	text = strings.TrimSpace(s[n:])
	for len(text) > 0 && text[len(text)-1] == '#' {
		i := len(text)
		for i > 0 && text[i-1] == '#' {
			i--
		}
		if i == 0 {
			text = ""
			break
		}
		if text[i-1] == ' ' || text[i-1] == '\t' {
			text = strings.TrimRight(text[:i], " \t")
		}
		break
	}
	return n, text, true
}

func isRule(line string) bool {
	if indentOf(line) > 3 {
		return false
	}
	s := strings.TrimSpace(line)
	if s == "" {
		return false
	}
	var ch byte
	n := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t':
			continue
		case '-', '*', '_':
			if ch == 0 {
				ch = s[i]
			} else if s[i] != ch {
				return false
			}
			n++
		default:
			return false
		}
	}
	return n >= 3
}

func isSetext(line string, ch byte) bool {
	if indentOf(line) > 3 {
		return false
	}
	s := strings.TrimSpace(line)
	if len(s) == 0 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] != ch {
			return false
		}
	}
	return true
}

func stripQuote(line string) (string, bool) {
	if indentOf(line) > 3 {
		return "", false
	}
	s := stripIndent(line, indentOf(line))
	if len(s) == 0 || s[0] != '>' {
		return "", false
	}
	s = s[1:]
	if len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	return s, true
}

type marker struct {
	indent  int
	ordered bool
	start   int
	width   int
	rest    string
}

func parseMarker(line string) (marker, bool) {
	if isRule(line) {
		return marker{}, false
	}
	indent := indentOf(line)
	if indent > 3 {
		return marker{}, false
	}
	s := stripIndent(line, indent)
	if len(s) == 0 {
		return marker{}, false
	}

	switch s[0] {
	case '-', '+', '*':
		if len(s) == 1 {
			return marker{indent: indent, width: indent + 2}, true
		}
		if s[1] != ' ' && s[1] != '\t' {
			return marker{}, false
		}
		return marker{
			indent: indent,
			width:  indent + 2,
			rest:   strings.TrimLeft(s[1:], " \t"),
		}, true
	}

	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 || i > 9 {
		return marker{}, false
	}
	if i >= len(s) || (s[i] != '.' && s[i] != ')') {
		return marker{}, false
	}
	if i+1 < len(s) && s[i+1] != ' ' && s[i+1] != '\t' {
		return marker{}, false
	}
	start, err := strconv.Atoi(s[:i])
	if err != nil {
		return marker{}, false
	}
	rest := ""
	if i+1 < len(s) {
		rest = strings.TrimLeft(s[i+1:], " \t")
	}
	return marker{
		indent:  indent,
		ordered: true,
		start:   start,
		width:   indent + i + 2,
		rest:    rest,
	}, true
}

func looksLikeTableRow(line string) bool {
	return strings.Contains(line, "|")
}

func isTableSep(line string) bool {
	cells := splitRow(line)
	if len(cells) == 0 {
		return false
	}
	for _, c := range cells {
		c = strings.TrimSpace(c)
		if c == "" {
			return false
		}
		if strings.HasPrefix(c, ":") {
			c = c[1:]
		}
		if strings.HasSuffix(c, ":") {
			c = c[:len(c)-1]
		}
		if len(c) == 0 {
			return false
		}
		for _, ch := range c {
			if ch != '-' {
				return false
			}
		}
	}
	return true
}

func splitRow(line string) []string {
	s := strings.TrimSpace(line)
	if s == "" {
		return nil
	}
	if s[0] == '|' {
		s = s[1:]
	}
	if len(s) > 0 && s[len(s)-1] == '|' && !oddBackslash(s, len(s)-1) {
		s = s[:len(s)-1]
	}
	var cells []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '|' && !oddBackslash(s, i) {
			cells = append(cells, unescapePipes(strings.TrimSpace(s[start:i])))
			start = i + 1
		}
	}
	cells = append(cells, unescapePipes(strings.TrimSpace(s[start:])))
	return cells
}

func oddBackslash(s string, i int) bool {
	n := 0
	for i > 0 && s[i-1] == '\\' {
		n++
		i--
	}
	return n%2 == 1
}

func unescapePipes(s string) string {
	return strings.ReplaceAll(s, `\|`, "|")
}
