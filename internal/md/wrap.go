package md

import (
	"strings"
	"unicode/utf8"
)

// columns is how many terminal cells a string occupies, treating each
// rune as one cell.  SGR is not in the string we count here -- spans
// hold plain text -- so a combining mark still costs a cell.  That is
// the same rule wrapText in slsh uses, and it is why a uuid is never
// broken: we wrap on spaces, not on width of a single word.
func columns(s string) int {
	return utf8.RuneCountInString(s)
}

type atom struct {
	s     string
	w     int
	space bool
	br    bool
	style style
}

type unit struct {
	atoms []atom
	w     int
	space bool
	br    bool
}

func unitsFrom(spans []span) []unit {
	var atoms []atom
	for _, sp := range spans {
		text := sp.text
		for len(text) > 0 {
			switch {
			case text[0] == '\n':
				atoms = append(atoms, atom{br: true, style: sp.style})
				text = text[1:]
			case text[0] == ' ':
				j := 1
				for j < len(text) && text[j] == ' ' {
					j++
				}
				atoms = append(atoms, atom{s: text[:j], w: j, space: true, style: sp.style})
				text = text[j:]
			default:
				j := 0
				for j < len(text) && text[j] != ' ' && text[j] != '\n' {
					_, n := utf8.DecodeRuneInString(text[j:])
					j += n
				}
				word := text[:j]
				atoms = append(atoms, atom{s: word, w: utf8.RuneCountInString(word), style: sp.style})
				text = text[j:]
			}
		}
	}

	var units []unit
	for i := 0; i < len(atoms); {
		switch {
		case atoms[i].br:
			units = append(units, unit{br: true})
			i++
		case atoms[i].space:
			u := unit{space: true}
			for i < len(atoms) && atoms[i].space {
				u.atoms = append(u.atoms, atoms[i])
				u.w += atoms[i].w
				i++
			}
			units = append(units, u)
		default:
			u := unit{}
			for i < len(atoms) && !atoms[i].space && !atoms[i].br {
				u.atoms = append(u.atoms, atoms[i])
				u.w += atoms[i].w
				i++
			}
			units = append(units, u)
		}
	}
	return units
}

// wrap fills spans to width.  first is written before the first line
// and next before every line after that; both count toward width, so a
// hanging list item wraps under its text rather than under its bullet.
//
// Style is reset at each line break and re-applied on the next line.
// A terminal does not reset at a newline, so a bold word that wraps
// would otherwise paint the rest of the paragraph -- and the bullet of
// the next item -- until a reset happened to arrive.
func wrap(spans []span, width int, first, next string) string {
	if width < 1 {
		width = 1
	}
	units := unitsFrom(spans)
	if len(units) == 0 {
		return ""
	}

	var b strings.Builder
	line := 0
	col := 0
	cur := style{}
	pfxWidth := 0

	open := func(st style) {
		if seq := styleSeq(cur, st); seq != "" {
			b.WriteString(seq)
			cur = st
		}
	}
	prefix := func() {
		if line == 0 {
			b.WriteString(first)
			pfxWidth = columns(first)
		} else {
			b.WriteString(next)
			pfxWidth = columns(next)
		}
		col = pfxWidth
	}
	brk := func() {
		open(style{})
		b.WriteByte('\n')
		line++
		prefix()
	}

	writeUnit := func(u unit) {
		for _, a := range u.atoms {
			open(a.style)
			b.WriteString(a.s)
		}
		col += u.w
	}

	prefix()
	var pending *unit
	for i := range units {
		u := units[i]
		switch {
		case u.br:
			pending = nil
			brk()
		case u.space:
			pending = &units[i]
		default:
			gap := 0
			if pending != nil && col > pfxWidth {
				gap = pending.w
			}
			if col+gap+u.w > width && col > pfxWidth {
				// The space that would have sat at the end of
				// the line is the one we wrap at; keeping it
				// would indent nothing and look like a ragged
				// right margin made of blanks.
				pending = nil
				brk()
				gap = 0
			}
			if pending != nil && col > pfxWidth {
				writeUnit(*pending)
			}
			pending = nil
			writeUnit(u)
		}
	}
	open(style{})
	return b.String()
}

// visibleColumns is columns ignoring CSI sequences.  Table cells and
// tests measure what a person sees, not what was written to produce it.
func visibleColumns(s string) int {
	n := 0
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
		if s[i] == '\n' || s[i] == '\r' {
			i++
			continue
		}
		_, w := utf8.DecodeRuneInString(s[i:])
		n++
		i += w
	}
	return n
}

func longestLine(s string) int {
	max := 0
	for _, line := range strings.Split(s, "\n") {
		if w := visibleColumns(line); w > max {
			max = w
		}
	}
	return max
}
