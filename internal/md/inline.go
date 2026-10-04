package md

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// style is the SGR attributes of one run of text.  Comparable, so a
// change of style is a != and not a list of flags to walk.
type style struct {
	bold, dim, italic, under, strike bool
}

func (st style) seq() string {
	var codes []byte
	if st.bold {
		codes = append(codes, '1')
	}
	if st.dim {
		if len(codes) > 0 {
			codes = append(codes, ';')
		}
		codes = append(codes, '2')
	}
	if st.italic {
		if len(codes) > 0 {
			codes = append(codes, ';')
		}
		codes = append(codes, '3')
	}
	if st.under {
		if len(codes) > 0 {
			codes = append(codes, ';')
		}
		codes = append(codes, '4')
	}
	if st.strike {
		if len(codes) > 0 {
			codes = append(codes, ';')
		}
		codes = append(codes, '9')
	}
	if len(codes) == 0 {
		return ""
	}
	return "\x1b[" + string(codes) + "m"
}

const sgrReset = "\x1b[0m"

// styleSeq is the sequences that take the terminal from one style to
// another.  Turning anything off is a reset and a re-apply: the
// alternative is a table of "not italic" codes that still have to
// leave bold alone, and a missed combination leaks into the next
// paragraph.
func styleSeq(from, to style) string {
	if from == to {
		return ""
	}
	if to == (style{}) {
		return sgrReset
	}
	if from == (style{}) {
		return to.seq()
	}
	return sgrReset + to.seq()
}

// span is a run of text in one style, with no SGR in the text itself.
type span struct {
	text  string
	style style
}

func parseInline(s string) []span {
	var out []span
	var buf strings.Builder
	st := style{}
	flush := func() {
		if buf.Len() == 0 {
			return
		}
		out = append(out, span{text: buf.String(), style: st})
		buf.Reset()
	}

	i := 0
	for i < len(s) {
		// An escape eats the next punctuation character, so a
		// starred word can be written \*like this\* and survive.
		if s[i] == '\\' && i+1 < len(s) && isASCIIPunct(s[i+1]) {
			buf.WriteByte(s[i+1])
			i += 2
			continue
		}

		if s[i] == '`' {
			if n, content, ok := codeSpan(s, i); ok {
				flush()
				out = append(out, span{text: content, style: style{dim: true}})
				i += n
				continue
			}
		}

		if strings.HasPrefix(s[i:], "![") {
			if alt, url, n, ok := parseLink(s[i+1:]); ok {
				flush()
				label := alt
				if label == "" {
					label = url
				}
				out = append(out, span{text: label, style: st})
				i += 1 + n
				continue
			}
		}

		if s[i] == '[' {
			if text, url, n, ok := parseLink(s[i:]); ok {
				flush()
				inner := parseInline(text)
				for j := range inner {
					inner[j].style.under = true
				}
				out = append(out, inner...)
				if url != "" && url != text {
					out = append(out, span{text: " (" + url + ")", style: style{dim: true}})
				}
				i += n
				continue
			}
		}

		if s[i] == '<' {
			if n, url, ok := autoLink(s, i); ok {
				flush()
				u := st
				u.under = true
				out = append(out, span{text: url, style: u})
				i += n
				continue
			}
		}

		// Opening looks ahead for a closer so a lone ** is left as
		// punctuation.  Closing does not: the closer we looked for
		// when we opened is this pair, and asking for another one
		// after it would leave the rest of the paragraph painted.
		if strings.HasPrefix(s[i:], "~~") {
			if st.strike || closer(s, i, "~~") > i+2 {
				flush()
				st.strike = !st.strike
				i += 2
				continue
			}
		}

		if strings.HasPrefix(s[i:], "**") {
			if st.bold || closer(s, i, "**") > i+2 {
				flush()
				st.bold = !st.bold
				i += 2
				continue
			}
		}

		if strings.HasPrefix(s[i:], "__") && (st.bold || canOpenUnderscore(s, i)) {
			if st.bold || closer(s, i, "__") > i+2 {
				flush()
				st.bold = !st.bold
				i += 2
				continue
			}
		}

		if s[i] == '*' && (i+1 >= len(s) || s[i+1] != '*') {
			if st.italic || closerStar(s, i) > i+1 {
				flush()
				st.italic = !st.italic
				i++
				continue
			}
		}

		if s[i] == '_' && (i+1 >= len(s) || s[i+1] != '_') {
			if st.italic {
				flush()
				st.italic = false
				i++
				continue
			}
			if canOpenUnderscore(s, i) && closerUnderscore(s, i) > i+1 {
				flush()
				st.italic = true
				i++
				continue
			}
		}

		r, n := utf8.DecodeRuneInString(s[i:])
		buf.WriteRune(r)
		i += n
	}
	flush()
	return mergeSpans(out)
}

func mergeSpans(in []span) []span {
	if len(in) == 0 {
		return in
	}
	out := make([]span, 0, len(in))
	for _, s := range in {
		if s.text == "" {
			continue
		}
		if len(out) > 0 && out[len(out)-1].style == s.style {
			out[len(out)-1].text += s.text
			continue
		}
		out = append(out, s)
	}
	return out
}

func isASCIIPunct(b byte) bool {
	return b >= 0x21 && b <= 0x2f ||
		b >= 0x3a && b <= 0x40 ||
		b >= 0x5b && b <= 0x60 ||
		b >= 0x7b && b <= 0x7e
}

func canOpenUnderscore(s string, i int) bool {
	if i > 0 {
		r, _ := utf8.DecodeLastRuneInString(s[:i])
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
	}
	if i+1 >= len(s) {
		return false
	}
	return s[i+1] != ' ' && s[i+1] != '\t'
}

// closer finds delim after i that leaves at least one character of
// content.  An empty pair (** immediately followed by **) is left as
// punctuation; treating it as emphasis would swallow the stars a
// person wrote to talk about the stars.
func closer(s string, i int, delim string) int {
	d := len(delim)
	for j := i + d; j+d <= len(s); j++ {
		if !strings.HasPrefix(s[j:], delim) {
			continue
		}
		if j == i+d {
			continue
		}
		return j
	}
	return -1
}

func closerStar(s string, i int) int {
	for j := i + 1; j < len(s); j++ {
		if s[j] != '*' {
			continue
		}
		// A ** is bold, not the end of italic.
		if j+1 < len(s) && s[j+1] == '*' {
			j++
			continue
		}
		if j == i+1 {
			continue
		}
		return j
	}
	return -1
}

func closerUnderscore(s string, i int) int {
	for j := i + 1; j < len(s); j++ {
		if s[j] != '_' {
			continue
		}
		if j+1 < len(s) && s[j+1] == '_' {
			j++
			continue
		}
		if j == i+1 {
			continue
		}
		if j+1 < len(s) {
			r, _ := utf8.DecodeRuneInString(s[j+1:])
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				continue
			}
		}
		return j
	}
	return -1
}

func codeSpan(s string, i int) (n int, content string, ok bool) {
	if s[i] != '`' {
		return 0, "", false
	}
	open := 0
	for i+open < len(s) && s[i+open] == '`' {
		open++
	}
	j := i + open
	for j < len(s) {
		if s[j] != '`' {
			j++
			continue
		}
		k := 0
		for j+k < len(s) && s[j+k] == '`' {
			k++
		}
		if k == open {
			content = s[i+open : j]
			content = strings.ReplaceAll(content, "\n", " ")
			if len(content) >= 2 && content[0] == ' ' && content[len(content)-1] == ' ' {
				content = content[1 : len(content)-1]
			}
			return j + k - i, content, true
		}
		j += k
	}
	return 0, "", false
}

func parseLink(s string) (text, url string, n int, ok bool) {
	if len(s) == 0 || s[0] != '[' {
		return "", "", 0, false
	}
	depth := 1
	i := 1
	for i < len(s) && depth > 0 {
		switch s[i] {
		case '\\':
			i++
			if i < len(s) {
				i++
			}
		case '[':
			depth++
			i++
		case ']':
			depth--
			i++
		default:
			i++
		}
	}
	if depth != 0 {
		return "", "", 0, false
	}
	text = s[1 : i-1]
	if i >= len(s) || s[i] != '(' {
		return "", "", 0, false
	}
	i++
	start := i
	for i < len(s) && s[i] != ')' {
		if s[i] == '\\' && i+1 < len(s) {
			i += 2
			continue
		}
		i++
	}
	if i >= len(s) {
		return "", "", 0, false
	}
	url = strings.TrimSpace(s[start:i])
	if len(url) >= 2 && url[0] == '<' && url[len(url)-1] == '>' {
		url = url[1 : len(url)-1]
	}
	if k := strings.IndexAny(url, " \t"); k >= 0 {
		url = strings.TrimSpace(url[:k])
	}
	return text, url, i + 1, true
}

func autoLink(s string, i int) (n int, url string, ok bool) {
	if s[i] != '<' {
		return 0, "", false
	}
	j := strings.IndexByte(s[i:], '>')
	if j < 2 {
		return 0, "", false
	}
	inner := s[i+1 : i+j]
	if !looksLikeURL(inner) {
		return 0, "", false
	}
	return j + 1, inner, true
}

func looksLikeURL(s string) bool {
	return strings.HasPrefix(s, "http://") ||
		strings.HasPrefix(s, "https://") ||
		strings.HasPrefix(s, "mailto:") ||
		strings.HasPrefix(s, "ftp://")
}
