package llsd

// LLSD notation, the text form the simulator uses where a message carries
// LLSD in a byte string (GenericStreamingMessage's data is one) and the
// viewer reads with LLSDNotationParser.
//
//	map        {'key':value,"key":value,s(3)"key":value}
//	array      [value,value]
//	undef      !
//	boolean    true false t f T F TRUE FALSE 1 0
//	integer    i123
//	real       r0.25
//	uuid       u21217e57-7e57-c0de-347f-fdfaeee3f80a
//	string     "g'day"  'have a "nice" day'  s(5)"raw!!"
//	uri        l"escaped"
//	date       d"2026-10-06T12:00:00.00Z"
//	binary     b16"ff31"  b64"/zE="  b(2)"raw"
//
// The values decode to the Go values the XML form does, so a field is
// read with the same accessors: a uuid, uri and date come out as strings.
// The grammar is the viewer's (Firestorm 885631b93a,
// indra/llcommon/llsdserialize.cpp, LLSDNotationParser::doParse :477,
// parseMap, parseArray, parseBinary, and deserialize_string,
// deserialize_string_delim, deserialize_string_raw and deserialize_boolean
// beside them).
//
// Where this departs: the viewer skips whatever stands between a map's
// entries or an array's elements that is not whitespace, a comma or a
// colon, and has no limit on how deep a value nests; here other text there
// is an error, and nesting is cut at maxNotationDepth, since the data is
// from the network.  Like the viewer's fromNotation, one value is read and
// whatever follows it is left alone.

import (
	"encoding/base64"
	"fmt"
	"strconv"
)

// maxNotationDepth is how deeply maps and arrays may nest.
const maxNotationDepth = 256

// DecodeNotation reads one value in LLSD notation from the start of b,
// after any whitespace.
func DecodeNotation(b []byte) (any, error) {
	p := &notation{b: b}
	v, err := p.value(maxNotationDepth)
	if err != nil {
		return nil, fmt.Errorf("llsd: notation: %w (at byte %d)", err, p.i)
	}
	return v, nil
}

type notation struct {
	b []byte
	i int
}

func (p *notation) skipSpace() {
	for p.i < len(p.b) && isSpace(p.b[p.i]) {
		p.i++
	}
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

func (p *notation) value(depth int) (any, error) {
	p.skipSpace()
	if p.i >= len(p.b) {
		return nil, fmt.Errorf("no value")
	}
	if depth == 0 {
		return nil, fmt.Errorf("nested deeper than %d", maxNotationDepth)
	}
	c := p.b[p.i]
	switch c {
	case '{':
		return p.mapValue(depth - 1)
	case '[':
		return p.arrayValue(depth - 1)
	case '!':
		p.i++
		return nil, nil
	case '0':
		p.i++
		return false, nil
	case '1':
		p.i++
		return true, nil
	case 't', 'T':
		p.i++
		return p.word("true", true)
	case 'f', 'F':
		p.i++
		return p.word("false", false)
	case 'i':
		p.i++
		return p.integer()
	case 'r':
		p.i++
		return p.real()
	case 'u':
		p.i++
		return p.uuid()
	case '"', '\'', 's':
		return p.str()
	case 'l', 'd':
		// A uri and a date are a delimited string after the letter, the
		// delimiter being whatever character follows it.
		p.i++
		if p.i >= len(p.b) {
			return nil, fmt.Errorf("a %q with nothing after it", c)
		}
		d := p.b[p.i]
		p.i++
		return p.delimited(d)
	case 'b':
		return p.binary()
	}
	return nil, fmt.Errorf("unrecognised character %q", c)
}

// word reads the rest of true or false after its first letter: nothing,
// or the remaining letters in any case (deserialize_boolean compares
// each lowercased).  A letter that does not fit is a failure, as it is
// for the viewer, which reads a word only when an alphabetic character
// follows.
func (p *notation) word(full string, v bool) (any, error) {
	if p.i >= len(p.b) || !isAlpha(p.b[p.i]) {
		return v, nil
	}
	for k := 1; k < len(full); k++ {
		if p.i >= len(p.b) || lower(p.b[p.i]) != full[k] {
			return nil, fmt.Errorf("not %q", full)
		}
		p.i++
	}
	return v, nil
}

func isAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func lower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// span reads the longest run of bytes ok accepts.
func (p *notation) span(ok func(byte) bool) string {
	s := p.i
	for p.i < len(p.b) && ok(p.b[p.i]) {
		p.i++
	}
	return string(p.b[s:p.i])
}

func (p *notation) integer() (any, error) {
	p.skipSpace()
	s := p.span(func(c byte) bool { return c >= '0' && c <= '9' || c == '-' || c == '+' })
	n, err := strconv.ParseInt(s, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("integer %q", s)
	}
	return n, nil
}

func (p *notation) real() (any, error) {
	p.skipSpace()
	s := p.span(func(c byte) bool {
		return c >= '0' && c <= '9' || c == '-' || c == '+' || c == '.' || c == 'e' || c == 'E'
	})
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, fmt.Errorf("real %q", s)
	}
	return f, nil
}

// uuid reads the 36 characters of an id.
func (p *notation) uuid() (any, error) {
	if p.i+36 > len(p.b) {
		return nil, fmt.Errorf("uuid is shorter than 36 characters")
	}
	s := string(p.b[p.i : p.i+36])
	for k := 0; k < 36; k++ {
		c := s[k]
		if k == 8 || k == 13 || k == 18 || k == 23 {
			if c != '-' {
				return nil, fmt.Errorf("uuid %q", s)
			}
		} else if !isHex(c) {
			return nil, fmt.Errorf("uuid %q", s)
		}
	}
	p.i += 36
	return s, nil
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func hexVal(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return 0
}

// str reads a string in any of its three forms and returns it as a value.
func (p *notation) str() (any, error) {
	s, err := p.stringForm()
	if err != nil {
		return nil, err
	}
	return s, nil
}

// stringForm is deserialize_string: a quoted string, or s(size) and the
// size's bytes in quotes.
func (p *notation) stringForm() (string, error) {
	if p.i >= len(p.b) {
		return "", fmt.Errorf("no string")
	}
	c := p.b[p.i]
	p.i++
	switch c {
	case '"', '\'':
		return p.delimited(c)
	case 's':
		return p.raw()
	}
	return "", fmt.Errorf("not a string")
}

// delimited is deserialize_string_delim: the characters up to the
// delimiter, a backslash taking the next one, with \a \b \f \n \r \t \v
// and \xHH meaning what they do in C.
func (p *notation) delimited(d byte) (string, error) {
	var out []byte
	for p.i < len(p.b) {
		c := p.b[p.i]
		p.i++
		switch {
		case c == '\\':
			if p.i >= len(p.b) {
				return "", fmt.Errorf("string ends in a backslash")
			}
			e := p.b[p.i]
			p.i++
			switch e {
			case 'a':
				out = append(out, '\a')
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'v':
				out = append(out, '\v')
			case 'x':
				if p.i+2 > len(p.b) {
					return "", fmt.Errorf("string ends inside \\x")
				}
				out = append(out, hexVal(p.b[p.i])<<4|hexVal(p.b[p.i+1]))
				p.i += 2
			default:
				out = append(out, e)
			}
		case c == d:
			return string(out), nil
		default:
			out = append(out, c)
		}
	}
	return "", fmt.Errorf("string is not closed")
}

// raw is deserialize_string_raw after the s: (size), a quote, size bytes,
// a quote.
func (p *notation) raw() (string, error) {
	n, err := p.sizeInParens()
	if err != nil {
		return "", err
	}
	if p.i >= len(p.b) || (p.b[p.i] != '"' && p.b[p.i] != '\'') {
		return "", fmt.Errorf("raw string without its opening quote")
	}
	p.i++
	if n > len(p.b)-p.i {
		return "", fmt.Errorf("raw string of %d bytes runs past the data", n)
	}
	s := string(p.b[p.i : p.i+n])
	p.i += n
	if p.i >= len(p.b) || (p.b[p.i] != '"' && p.b[p.i] != '\'') {
		return "", fmt.Errorf("raw string without its closing quote")
	}
	p.i++
	return s, nil
}

// sizeInParens reads (n).
func (p *notation) sizeInParens() (int, error) {
	if p.i >= len(p.b) || p.b[p.i] != '(' {
		return 0, fmt.Errorf("expected a (size)")
	}
	p.i++
	s := p.span(func(c byte) bool { return c >= '0' && c <= '9' })
	if p.i >= len(p.b) || p.b[p.i] != ')' || s == "" {
		return 0, fmt.Errorf("expected a (size)")
	}
	p.i++
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("size %q", s)
	}
	return n, nil
}

// binary is parseBinary: b16"hex", b64"base64" or b(size)"raw bytes".
func (p *notation) binary() (any, error) {
	p.i++ // the b
	switch {
	case p.i < len(p.b) && p.b[p.i] == '(':
		n, err := p.sizeInParens()
		if err != nil {
			return nil, err
		}
		if p.i >= len(p.b) || p.b[p.i] != '"' {
			return nil, fmt.Errorf("binary without its opening quote")
		}
		p.i++
		if n > len(p.b)-p.i {
			return nil, fmt.Errorf("binary of %d bytes runs past the data", n)
		}
		out := append([]byte{}, p.b[p.i:p.i+n]...)
		p.i += n
		if p.i >= len(p.b) || p.b[p.i] != '"' {
			return nil, fmt.Errorf("binary without its closing quote")
		}
		p.i++
		return out, nil
	case p.hasPrefix("16\""):
		p.i += 3
		s := p.span(func(c byte) bool { return c != '"' })
		if p.i >= len(p.b) {
			return nil, fmt.Errorf("binary is not closed")
		}
		p.i++
		if len(s)%2 != 0 {
			return nil, fmt.Errorf("b16 has an odd number of digits")
		}
		out := make([]byte, len(s)/2)
		for k := range out {
			if !isHex(s[2*k]) || !isHex(s[2*k+1]) {
				return nil, fmt.Errorf("b16 has a character that is not hex")
			}
			out[k] = hexVal(s[2*k])<<4 | hexVal(s[2*k+1])
		}
		return out, nil
	case p.hasPrefix("64\""):
		p.i += 3
		s := p.span(func(c byte) bool { return c != '"' })
		if p.i >= len(p.b) {
			return nil, fmt.Errorf("binary is not closed")
		}
		p.i++
		out, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("b64: %v", err)
		}
		return out, nil
	}
	return nil, fmt.Errorf("binary is not b16, b64 or b(size)")
}

func (p *notation) hasPrefix(s string) bool {
	return len(p.b)-p.i >= len(s) && string(p.b[p.i:p.i+len(s)]) == s
}

func (p *notation) mapValue(depth int) (any, error) {
	p.i++ // the {
	out := map[string]any{}
	for {
		for p.i < len(p.b) && (isSpace(p.b[p.i]) || p.b[p.i] == ',') {
			p.i++
		}
		if p.i >= len(p.b) {
			return nil, fmt.Errorf("map is not closed")
		}
		if p.b[p.i] == '}' {
			p.i++
			return out, nil
		}
		key, err := p.stringForm()
		if err != nil {
			return nil, fmt.Errorf("map key: %w", err)
		}
		for p.i < len(p.b) && (isSpace(p.b[p.i]) || p.b[p.i] == ':') {
			p.i++
		}
		v, err := p.value(depth)
		if err != nil {
			return nil, fmt.Errorf("map value of %q: %w", key, err)
		}
		out[key] = v
	}
}

func (p *notation) arrayValue(depth int) (any, error) {
	p.i++ // the [
	out := []any{}
	for {
		for p.i < len(p.b) && (isSpace(p.b[p.i]) || p.b[p.i] == ',') {
			p.i++
		}
		if p.i >= len(p.b) {
			return nil, fmt.Errorf("array is not closed")
		}
		if p.b[p.i] == ']' {
			p.i++
			return out, nil
		}
		v, err := p.value(depth)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
}
