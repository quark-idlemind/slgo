package slate

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type kind int

const (
	kEOF kind = iota
	kWord
	kString
	kDuration
	kUUID
	kMoney
	kFloat
	kInt
	kLBrace
	kRBrace
	kCapture // $ and a letter, then letters, digits and underscores; text is the whole token
)

type token struct {
	kind  kind
	text  string
	span  Span
	dur   time.Duration
	f     float64
	intv  int64
	intOK bool
}

func (t token) String() string {
	switch t.kind {
	case kEOF:
		return "end of file"
	case kString:
		return strconv.Quote(t.text)
	case kMoney:
		return "L$"
	case kLBrace:
		return "{"
	case kRBrace:
		return "}"
	default:
		if t.text == "" {
			return "token"
		}
		return t.text
	}
}

type lexer struct {
	src    []byte
	file   string
	i      int
	line   int
	col    int
	buf    token
	bufErr error
	hasBuf bool
}

func newLexer(file string, src []byte) *lexer {
	return &lexer{src: src, file: file, line: 1, col: 1}
}

func (l *lexer) Next() (token, error) {
	if l.hasBuf {
		l.hasBuf = false
		return l.buf, l.bufErr
	}
	return l.scan()
}

func (l *lexer) Peek() (token, error) {
	if l.hasBuf {
		return l.buf, l.bufErr
	}
	t, err := l.scan()
	l.buf, l.bufErr, l.hasBuf = t, err, true
	return t, err
}

func (l *lexer) scan() (token, error) {
	if err := l.skip(); err != nil {
		return token{}, err
	}
	if l.i >= len(l.src) {
		return token{kind: kEOF, span: Span{Start: l.i, End: l.i, Line: l.line, Col: l.col}}, nil
	}
	c := l.src[l.i]
	switch {
	case c == '"':
		return l.scanString()
	case c == '{' || c == '}':
		return l.scanBrace(), nil
	case c == '-':
		return l.scanMinus()
	case c == '$':
		return l.scanCapture()
	case c == '.':
		return token{}, l.badDotOrIllegal()
	case isDigit(c):
		return l.scanFromDigit()
	case isLetter(c):
		return l.scanIdent()
	default:
		return token{}, l.illegal()
	}
}

func (l *lexer) skip() error {
	for l.i < len(l.src) {
		c := l.src[l.i]
		switch c {
		case 0:
			return l.fail(l.line, l.col, "a script cannot contain a NUL")
		case ' ', '\t':
			l.bump()
		case '\r':
			l.i++
			l.line++
			l.col = 1
			if l.i < len(l.src) && l.src[l.i] == '\n' {
				l.i++
			}
		case '\n':
			l.i++
			l.line++
			l.col = 1
		case '#':
			for l.i < len(l.src) && l.src[l.i] != '\n' && l.src[l.i] != '\r' {
				if l.src[l.i] == 0 {
					return l.fail(l.line, l.col, "a script cannot contain a NUL")
				}
				r, size := utf8.DecodeRune(l.src[l.i:])
				if r == utf8.RuneError && size == 1 {
					return l.fail(l.line, l.col, "invalid UTF-8")
				}
				for k := 0; k < size; k++ {
					l.bump()
				}
			}
		default:
			return nil
		}
	}
	return nil
}

func (l *lexer) bump() {
	if l.i < len(l.src) && l.src[l.i] == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
	l.i++
}

func (l *lexer) fail(line, col int, msg string) error {
	return &Error{File: l.file, Line: line, Column: col, Msg: msg}
}

func (l *lexer) spanFrom(start, line, col int) Span {
	return Span{Start: start, End: l.i, Line: line, Col: col}
}

func (l *lexer) scanString() (token, error) {
	line, col := l.line, l.col
	start := l.i
	l.bump()
	var b strings.Builder
	for l.i < len(l.src) {
		c := l.src[l.i]
		switch c {
		case 0:
			return token{}, l.fail(l.line, l.col, "a script cannot contain a NUL")
		case '\n', '\r':
			return token{}, l.fail(l.line, l.col, "a string cannot contain a newline")
		case '"':
			l.bump()
			return token{kind: kString, text: b.String(), span: l.spanFrom(start, line, col)}, nil
		case '\\':
			escLine, escCol := l.line, l.col
			l.bump()
			if l.i >= len(l.src) {
				return token{}, l.fail(escLine, escCol, "truncated string escape")
			}
			e := l.src[l.i]
			var out byte
			switch e {
			case '"', '\\':
				out = e
			case 'n':
				out = '\n'
			case 't':
				out = '\t'
			default:
				return token{}, l.fail(escLine, escCol, "unknown string escape")
			}
			b.WriteByte(out)
			l.bump()
		default:
			r, size := utf8.DecodeRune(l.src[l.i:])
			if r == utf8.RuneError && size == 1 {
				return token{}, l.fail(l.line, l.col, "invalid UTF-8")
			}
			b.Write(l.src[l.i : l.i+size])
			for k := 0; k < size; k++ {
				l.bump()
			}
		}
	}
	return token{}, l.fail(line, col, "unterminated string")
}

func (l *lexer) scanMinus() (token, error) {
	if l.i+1 < len(l.src) && l.src[l.i+1] == '.' {
		return token{}, l.fail(l.line, l.col, "a number needs digits on both sides of the dot")
	}
	if l.i+1 < len(l.src) && isDigit(l.src[l.i+1]) {
		return l.scanNumber()
	}
	return token{}, l.fail(l.line, l.col, "illegal character '-'")
}

func (l *lexer) badDotOrIllegal() error {
	if l.i+1 < len(l.src) && isDigit(l.src[l.i+1]) {
		return l.fail(l.line, l.col, "a number needs digits on both sides of the dot")
	}
	return l.illegal()
}

func (l *lexer) illegal() error {
	if l.src[l.i] == 0 {
		return l.fail(l.line, l.col, "a script cannot contain a NUL")
	}
	r, size := utf8.DecodeRune(l.src[l.i:])
	if r == utf8.RuneError && size == 1 {
		return l.fail(l.line, l.col, "invalid UTF-8")
	}
	return l.fail(l.line, l.col, fmt.Sprintf("illegal character %q", r))
}

func (l *lexer) scanFromDigit() (token, error) {
	if t, ok, err := l.tryUUID(); ok {
		return t, err
	}
	return l.scanNumber()
}

// scanIdent reads a letter-led word. The lexer does not say what a word
// means; the parser compares its text where the grammar expects one.
func (l *lexer) scanIdent() (token, error) {
	if t, ok, err := l.tryUUID(); ok {
		return t, err
	}
	if l.src[l.i] == 'L' && l.i+1 < len(l.src) && l.src[l.i+1] == '$' {
		return l.scanMoney(), nil
	}
	line, col := l.line, l.col
	start := l.i
	l.bump()
	for l.i < len(l.src) && isIdentCont(l.src[l.i]) {
		l.bump()
	}
	text := string(l.src[start:l.i])
	return token{kind: kWord, text: text, span: l.spanFrom(start, line, col)}, nil
}

// scanCapture reads $name. A word that begins with L and is followed by $
// is the money symbol, and scanIdent takes it before this is reached; a
// $ in a string never gets here. The name is what RE2 allows for a group
// and begins with a letter, so a named group can bind it.
// Why: doc/slate-language.md#lexical-grammar
func (l *lexer) scanCapture() (token, error) {
	if l.i+1 >= len(l.src) || !isLetter(l.src[l.i+1]) {
		return token{}, l.illegal()
	}
	line, col := l.line, l.col
	start := l.i
	l.bump()
	for l.i < len(l.src) && isWordByte(l.src[l.i]) {
		l.bump()
	}
	return token{kind: kCapture, text: string(l.src[start:l.i]), span: l.spanFrom(start, line, col)}, nil
}

func (l *lexer) scanBrace() token {
	line, col := l.line, l.col
	start := l.i
	k := kLBrace
	if l.src[l.i] == '}' {
		k = kRBrace
	}
	l.bump()
	return token{kind: k, text: string(l.src[start:l.i]), span: l.spanFrom(start, line, col)}
}

func (l *lexer) scanMoney() token {
	line, col := l.line, l.col
	start := l.i
	l.bump()
	l.bump()
	return token{kind: kMoney, text: "L$", span: l.spanFrom(start, line, col)}
}

// scanNumber reads an integer, a float, or a duration. A duration is
// digits glued to ms, s, or m, with no sign and no dot. A float needs
// digits on both sides of the dot. An integer that does not fit in
// int64 is still a token; the use decides whether that is an error.
func (l *lexer) scanNumber() (token, error) {
	line, col := l.line, l.col
	start := l.i
	signed := l.src[l.i] == '-'
	if signed {
		l.bump()
	}
	digStart := l.i
	for l.i < len(l.src) && isDigit(l.src[l.i]) {
		l.bump()
	}
	digits := string(l.src[digStart:l.i])
	if !signed {
		if unit, ok := l.peekUnit(); ok {
			for k := 0; k < len(unit); k++ {
				l.bump()
			}
			if l.i < len(l.src) && isWordByte(l.src[l.i]) {
				// 10seconds, 5min: the unit is the whole glued word.
				end := l.i
				for end < len(l.src) && isWordByte(l.src[end]) {
					end++
				}
				word := string(l.src[digStart+len(digits) : end])
				return token{}, l.fail(line, col, fmt.Sprintf("unknown duration unit %q; a duration is digits and ms, s or m, with nothing after it", word))
			}
			d, err := durationValue(digits, unit)
			if err != nil {
				return token{}, l.fail(line, col, "duration overflows")
			}
			return token{kind: kDuration, text: string(l.src[start:l.i]), dur: d, span: l.spanFrom(start, line, col)}, nil
		}
	}
	if l.i < len(l.src) && l.src[l.i] == '.' {
		dotLine, dotCol := l.line, l.col
		l.bump()
		frac := l.i
		for l.i < len(l.src) && isDigit(l.src[l.i]) {
			l.bump()
		}
		if l.i == frac {
			return token{}, l.fail(dotLine, dotCol, "a number needs digits on both sides of the dot")
		}
		text := string(l.src[start:l.i])
		f, err := strconv.ParseFloat(text, 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return token{}, l.fail(line, col, "number does not fit in a float")
		}
		return token{kind: kFloat, text: text, f: f, span: l.spanFrom(start, line, col)}, nil
	}
	text := string(l.src[start:l.i])
	v, err := strconv.ParseInt(text, 10, 64)
	return token{kind: kInt, text: text, intv: v, intOK: err == nil, span: l.spanFrom(start, line, col)}, nil
}

func (l *lexer) peekUnit() (string, bool) {
	if l.i >= len(l.src) {
		return "", false
	}
	if l.src[l.i] == 'm' {
		if l.i+1 < len(l.src) && l.src[l.i+1] == 's' {
			return "ms", true
		}
		return "m", true
	}
	if l.src[l.i] == 's' {
		return "s", true
	}
	return "", false
}

func durationValue(digits, unit string) (time.Duration, error) {
	n, err := strconv.ParseUint(digits, 10, 64)
	if err != nil {
		return 0, err
	}
	var mult uint64
	switch unit {
	case "ms":
		mult = uint64(time.Millisecond)
	case "s":
		mult = uint64(time.Second)
	case "m":
		mult = uint64(time.Minute)
	default:
		return 0, strconv.ErrSyntax
	}
	if mult == 0 || n > uint64(math.MaxInt64)/mult {
		return 0, strconv.ErrRange
	}
	return time.Duration(n * mult), nil
}

// tryUUID matches hex8-hex4-hex4-hex4-hex12 at the current byte; a
// match glued to a word byte is an error.
// It is tried before an integer or an identifier, so a leading digit
// does not split the token. Hex is case-insensitive; the text is lowercase.
func (l *lexer) tryUUID() (token, bool, error) {
	const n = 36
	if l.i+n > len(l.src) {
		return token{}, false, nil
	}
	s := l.src[l.i : l.i+n]
	groups := []int{8, 4, 4, 4, 12}
	pos := 0
	for gi, g := range groups {
		if gi > 0 {
			if s[pos] != '-' {
				return token{}, false, nil
			}
			pos++
		}
		for k := 0; k < g; k++ {
			if !isHex(s[pos+k]) {
				return token{}, false, nil
			}
		}
		pos += g
	}
	if l.i+n < len(l.src) && isWordByte(l.src[l.i+n]) {
		return token{}, true, l.fail(l.line, l.col, "a UUID is followed by more characters; a UUID is exactly 36 characters")
	}
	line, col := l.line, l.col
	start := l.i
	for k := 0; k < n; k++ {
		l.bump()
	}
	return token{kind: kUUID, text: strings.ToLower(string(s)), span: l.spanFrom(start, line, col)}, true, nil
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isLetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isHex(c byte) bool {
	return isDigit(c) || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// isWordByte is a letter, digit or underscore: what may not follow a
// duration or a UUID.
func isWordByte(c byte) bool { return isLetter(c) || isDigit(c) || c == '_' }

func isIdentCont(c byte) bool {
	return isLetter(c) || isDigit(c) || c == '_' || c == '-'
}
