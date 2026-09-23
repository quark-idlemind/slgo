package askindex

// Writing an index out and reading it back.
//
// # Why text
//
// The encoding is committed to the repository and embedded in the
// binary, so it is read by three things besides Decode: git, which
// shows a change to it as a diff only if it is lines of text; a person
// reviewing that diff, who can see that a keyword went in or a page
// grew a section; and tools/check-identities, which greps the tree for
// real names and skips anything that looks binary.  The index holds
// every word of every man page, so a name that got into a page would
// get into the index too, and an index the checker could not read would
// be the one place it went unnoticed.
//
// It is ASCII whatever the pages hold: anything outside printable ASCII
// is escaped, so the file is safe to grep, to diff and to paste.
//
// # Why the same input gives the same bytes
//
// A test regenerates the encoding from the command table and the pages
// and compares it with the embedded copy, byte for byte, and fails when
// they differ; that is what stops the index going stale.  So nothing in
// the encoding may depend on anything but the documents: the words are
// written sorted, each word's documents in index order, and no score or
// other float is stored at all -- the numbers are counts, and Decode
// works out the rest exactly as Build does.
//
// # The layout
//
//	askindex 1
//	docs N
//	ID <tab> Command <tab> Kind <tab> Heading <tab> Text      N lines
//	words M
//	word <tab> postings                                       M lines
//
// A word's postings are the documents it is in, as the gap from the
// previous one (the first as its own number), with "*COUNT" after any
// document that has the word more than once: "3,1,5*2" is documents 3,
// 4 and 9, with the word twice in 9.  The gaps are what keep this
// compact; most words are in a few documents close together, and most
// of them once.

import (
	"bufio"
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// magic is the first line.  The number changes when the layout does,
// so that an index in an old layout is refused rather than misread.
const magic = "askindex 1"

// Encode writes the index out.  The same documents in the same order
// always encode to the same bytes.
func (ix *Index) Encode() []byte {
	var b bytes.Buffer
	b.WriteString(magic + "\n")
	fmt.Fprintf(&b, "docs %d\n", len(ix.docs))
	for _, d := range ix.docs {
		b.WriteString(escape(d.ID))
		b.WriteByte('\t')
		b.WriteString(escape(d.Command))
		b.WriteByte('\t')
		b.WriteString(escape(d.Kind))
		b.WriteByte('\t')
		b.WriteString(escape(d.Heading))
		b.WriteByte('\t')
		b.WriteString(escape(d.Text))
		b.WriteByte('\n')
	}

	words := make([]string, 0, len(ix.postings))
	for w := range ix.postings {
		words = append(words, w)
	}
	sort.Strings(words)
	fmt.Fprintf(&b, "words %d\n", len(words))
	for _, w := range words {
		b.WriteString(escape(w))
		b.WriteByte('\t')
		prev := 0
		for i, p := range ix.postings[w] {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(strconv.Itoa(p.doc - prev))
			prev = p.doc
			if p.tf > 1 {
				b.WriteByte('*')
				b.WriteString(strconv.Itoa(p.tf))
			}
		}
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// Decode reads back what Encode wrote.  Anything else is an error that
// says which line it could not read, since the one way to get here with
// a bad index is a file edited by hand.
func Decode(data []byte) (*Index, error) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), len(data)+1)
	line := 0
	next := func() (string, bool) {
		if !sc.Scan() {
			return "", false
		}
		line++
		return sc.Text(), true
	}
	fail := func(format string, args ...any) (*Index, error) {
		return nil, fmt.Errorf("askindex: line %d: %s", line, fmt.Sprintf(format, args...))
	}

	if s, ok := next(); !ok || s != magic {
		return fail("not an index in this layout; the first line should be %q", magic)
	}
	s, _ := next()
	nDocs, err := count(s, "docs")
	if err != nil {
		return fail("%v", err)
	}
	ix := &Index{docs: make([]Doc, 0, nDocs), postings: map[string][]posting{}}
	for i := 0; i < nDocs; i++ {
		s, ok := next()
		if !ok {
			return fail("the index ends after %d of %d documents", i, nDocs)
		}
		f := strings.Split(s, "\t")
		if len(f) != 5 {
			return fail("a document has %d fields, not 5", len(f))
		}
		var d Doc
		for k, dst := range []*string{&d.ID, &d.Command, &d.Kind, &d.Heading, &d.Text} {
			if *dst, err = unescape(f[k]); err != nil {
				return fail("%v", err)
			}
		}
		ix.docs = append(ix.docs, d)
	}

	s, _ = next()
	nWords, err := count(s, "words")
	if err != nil {
		return fail("%v", err)
	}
	for i := 0; i < nWords; i++ {
		s, ok := next()
		if !ok {
			return fail("the index ends after %d of %d words", i, nWords)
		}
		w, list, ok := strings.Cut(s, "\t")
		if !ok || list == "" {
			return fail("a word with no documents")
		}
		if w, err = unescape(w); err != nil {
			return fail("%v", err)
		}
		if _, dup := ix.postings[w]; dup {
			return fail("%q is listed twice", w)
		}
		doc := 0
		ps := make([]posting, 0, strings.Count(list, ",")+1)
		for k, item := range strings.Split(list, ",") {
			gap, tf := item, "1"
			if g, t, ok := strings.Cut(item, "*"); ok {
				gap, tf = g, t
			}
			g, err1 := strconv.Atoi(gap)
			n, err2 := strconv.Atoi(tf)
			if err1 != nil || err2 != nil || g < 0 || (k > 0 && g == 0) || n < 1 {
				return fail("%q is not a posting", item)
			}
			doc += g
			if doc >= nDocs {
				return fail("%q names document %d of %d", w, doc, nDocs)
			}
			ps = append(ps, posting{doc: doc, tf: n})
		}
		ix.postings[w] = ps
	}
	if s, ok := next(); ok {
		return fail("more after the last word: %.40q", s)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("askindex: %v", err)
	}
	ix.finish()
	return ix, nil
}

// count reads a "docs N" or "words N" line.
func count(s, what string) (int, error) {
	rest, ok := strings.CutPrefix(s, what+" ")
	if !ok {
		return 0, fmt.Errorf("expected %q and a count, found %.40q", what, s)
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%q is not a count of %s", rest, what)
	}
	return n, nil
}

// escape makes s one line of printable ASCII with no tab in it.  A
// backslash, a newline and a tab have short forms, since every page is
// full of newlines; anything else outside printable ASCII is written as
// its code point in braces, \u{e9}.
func escape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r >= 0x7f:
			fmt.Fprintf(&b, `\u{%x}`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// unescape is escape undone.
func unescape(s string) (string, error) {
	if !strings.Contains(s, `\`) {
		return s, nil
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		if i+1 >= len(s) {
			return "", fmt.Errorf("a backslash at the end of %.40q", s)
		}
		i++
		switch s[i] {
		case '\\':
			b.WriteByte('\\')
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'u':
			end := strings.IndexByte(s[i:], '}')
			if i+1 >= len(s) || s[i+1] != '{' || end < 0 {
				return "", fmt.Errorf("a bad \\u escape in %.40q", s)
			}
			r, err := strconv.ParseUint(s[i+2:i+end], 16, 32)
			if err != nil {
				return "", fmt.Errorf("a bad \\u escape in %.40q", s)
			}
			b.WriteRune(rune(r))
			i += end
		default:
			return "", fmt.Errorf("an unknown escape \\%c in %.40q", s[i], s)
		}
	}
	return b.String(), nil
}
