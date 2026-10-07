package slate

// The element record: a plain file saying where each named area is on a
// texture, which touch OBJ element "NAME" reads.
// Why: doc/slate-language.md#element-records

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/quark-idlemind/slgo/msg"
)

// area is one named area of a texture: a box in the texture's own
// coordinates, 0 to 1 across the whole texture with the origin at the
// bottom left, as llDetectedTouchUV has them.
type area struct {
	name           string
	u0, v0, u1, v1 float64
	text           string
	file           string
	line           int
}

// centre is the middle of the box, which is inside every shape an area
// can have.
func (a area) centre() (u, v float64) { return (a.u0 + a.u1) / 2, (a.v0 + a.v1) / 2 }

// elementMap is the areas of each texture, in the order the file has them,
// and the file each texture was recorded in.
type elementMap struct {
	areas map[msg.UUID][]area
	from  map[msg.UUID]string
}

func newElementMap() *elementMap {
	return &elementMap{areas: map[msg.UUID][]area{}, from: map[msg.UUID]string{}}
}

// readElements reads what an elements header names: a file, or the *.tsv
// files of a directory in name order. Nothing is fetched and nothing is
// guessed; a texture recorded in two files is an error naming both.
func (m *elementMap) readElements(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return m.readFile(path)
	}
	ents, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".tsv") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names {
		if err := m.readFile(filepath.Join(path, n)); err != nil {
			return err
		}
	}
	return nil
}

func (m *elementMap) readFile(path string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	here, err := parseElements(path, src)
	if err != nil {
		return err
	}
	for id := range here {
		if prev, dup := m.from[id]; dup {
			return fmt.Errorf("texture %s is recorded in both %s and %s; a texture is recorded in one place", id, prev, path)
		}
	}
	for id, as := range here {
		m.areas[id] = as
		m.from[id] = path
	}
	return nil
}

// parseElements reads one record file: a line a texture id, an element
// name, the box u0 v0 u1 v1 and optionally text, in that order, separated
// by spaces or tabs: 6 or 7 fields. A line whose first non-blank character
// is # is a comment, and a blank line is skipped. The name and the text
// are each one field, double-quoted when they hold white space, a quote, a
// backslash or a #, with only \" and \\ inside. Anything else is an error
// that names the file and the line.
func parseElements(file string, src []byte) (map[msg.UUID][]area, error) {
	out := map[msg.UUID][]area{}
	for i, raw := range strings.Split(string(src), "\n") {
		line := strings.TrimRight(raw, "\r")
		if t := strings.TrimSpace(line); t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		id, a, err := parseElementLine(line)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %v", file, i+1, err)
		}
		a.file, a.line = file, i+1
		out[id] = append(out[id], a)
	}
	return out, nil
}

// word reads the next field of a line: a bare word up to white space, or a
// double-quoted string. rest is what follows it.
func word(s string) (w string, rest string, quoted bool, err error) {
	s = strings.TrimLeft(s, " \t")
	if s == "" {
		return "", "", false, nil
	}
	if s[0] != '"' {
		end := strings.IndexAny(s, " \t")
		if end < 0 {
			end = len(s)
		}
		if i := strings.IndexAny(s[:end], `"\`); i >= 0 {
			return "", "", false, fmt.Errorf("%q holds a %c; a field with a quote or a backslash is quoted, with \\\" and \\\\ inside", s[:end], s[i])
		}
		return s[:end], s[end:], false, nil
	}
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		switch c := s[i]; c {
		case '"':
			if rest := s[i+1:]; rest != "" && rest[0] != ' ' && rest[0] != '\t' {
				return "", "", true, fmt.Errorf("a closing quote is followed by %q; a quoted field ends at white space", rest[:1])
			}
			return b.String(), s[i+1:], true, nil
		case '\\':
			i++
			if i >= len(s) || (s[i] != '"' && s[i] != '\\') {
				return "", "", true, fmt.Errorf(`inside quotes a backslash is \" or \\`)
			}
			b.WriteByte(s[i])
		default:
			b.WriteByte(c)
		}
	}
	return "", "", true, fmt.Errorf("a quote is not closed")
}

func parseElementLine(line string) (msg.UUID, area, error) {
	var a area
	rest := line
	next := func(what string) (string, bool, error) {
		w, r, q, err := word(rest)
		if err != nil {
			return "", false, fmt.Errorf("%s: %v", what, err)
		}
		if w == "" && !q {
			return "", false, fmt.Errorf("the line ends before the %s; a line is texture id, element, u0 v0 u1 v1, then optional text", what)
		}
		rest = r
		return w, q, nil
	}
	idText, q, err := next("texture id")
	if err != nil {
		return msg.UUID{}, a, err
	}
	if q {
		return msg.UUID{}, a, fmt.Errorf("the texture id is not quoted")
	}
	id, err := msg.ParseUUID(idText)
	if err != nil {
		return msg.UUID{}, a, fmt.Errorf("texture id %q is not a UUID", idText)
	}
	if id.IsZero() {
		return msg.UUID{}, a, fmt.Errorf("texture id is the null key, which names no texture")
	}
	if a.name, _, err = next("element name"); err != nil {
		return msg.UUID{}, a, err
	}
	if a.name == "" {
		return msg.UUID{}, a, fmt.Errorf("the element name is empty")
	}
	var n [4]float64
	for i, label := range []string{"u0", "v0", "u1", "v1"} {
		w, q, err := next(label)
		if err != nil {
			return msg.UUID{}, a, err
		}
		v, perr := strconv.ParseFloat(w, 64)
		if q || perr != nil || v != v || v < 0 || v > 1 {
			return msg.UUID{}, a, fmt.Errorf("%s is %q; U and V are numbers from 0 to 1 across the whole texture", label, w)
		}
		n[i] = v
	}
	a.u0, a.v0, a.u1, a.v1 = n[0], n[1], n[2], n[3]
	if a.u0 >= a.u1 || a.v0 >= a.v1 {
		return msg.UUID{}, a, fmt.Errorf("the box %v %v %v %v is empty or backwards; u0 is less than u1 and v0 is less than v1", a.u0, a.v0, a.u1, a.v1)
	}
	w, after, q, err := word(rest)
	if err != nil {
		return msg.UUID{}, a, fmt.Errorf("text: %v", err)
	}
	if w != "" || q {
		a.text = w
		if more, _, mq, merr := word(after); more != "" || mq || merr != nil {
			return msg.UUID{}, a, fmt.Errorf("more after the text; a line has 6 or 7 fields, and text with spaces is quoted")
		}
	}
	return id, a, nil
}

// setupElements reads every elements header, in the order the file has
// them. A path is relative to the Slate file.
// Why: doc/slate-language.md#element-records
func (r *runner) setupElements(context.Context) error {
	if len(r.s.Elements) == 0 {
		return nil
	}
	m := newElementMap()
	dir := filepath.Dir(r.s.File)
	for _, h := range r.s.Elements {
		p := h.Path
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		if err := m.readElements(p); err != nil {
			return &setupError{fmt.Sprintf("elements %q: %v", h.Path, err)}
		}
	}
	r.elems = m
	return nil
}
