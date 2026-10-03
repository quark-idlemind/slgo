package slate

// Captured values: what a positive expectation binds ($name), what a later
// step uses as a literal, and how both are printed. A capture is data: it is
// never parsed, never compiled as a pattern and never spliced into a string.
// Why: doc/slate-language.md#static-checks

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// capValue is one captured value. typ says which field holds it.
type capValue struct {
	typ    CaptureType
	text   string     // CapText
	id     msg.UUID   // CapUUID
	pair   [2]float64 // CapPair
	num    float64    // CapNumber
	click  uint8      // CapClick
	triple [3]float64 // CapTriple
	on     bool       // CapOnOff
	step   int        // the step that bound it

	// all and tuple are a capture of face all: the value of each face, in
	// the type of typ. Only a face all expectation of the same kind uses
	// one; every other place refuses it (capture).
	all   bool
	tuple []capValue
}

// String is the value as the transcript prints it: text quoted, the rest
// as the reading lines print them.
func (v capValue) String() string {
	if v.all {
		parts := make([]string, len(v.tuple))
		for i, f := range v.tuple {
			parts[i] = f.String()
		}
		return "face all " + strings.Join(parts, ", ")
	}
	switch v.typ {
	case CapText:
		return fmt.Sprintf("%q", v.text)
	case CapUUID:
		return v.id.String()
	case CapPair:
		return fmt.Sprintf("%g %g", v.pair[0], v.pair[1])
	case CapNumber:
		return fmt.Sprintf("%g", v.num)
	case CapClick:
		return fmt.Sprintf("%d", v.click)
	case CapTriple:
		return fmt.Sprintf("%g %g %g", v.triple[0], v.triple[1], v.triple[2])
	case CapOnOff:
		if v.on {
			return "on"
		}
		return "off"
	}
	return "?"
}

// namedVal is a value and the capture it is bound to.
type namedVal struct {
	name string
	v    capValue
}

// capture is the value of a use of a capture, which has to be bound by an
// earlier step and of the type of the place that uses it. Check makes
// both certain, so a failure here is a bug in the runner or in Check.
//
// A capture of face all holds one value per face and Check types it as a
// single one, so the place that uses it has to say so: capture refuses it,
// and captureOrTuple, which a face all expectation calls, does not.
func (s *stepRun) capture(c *Capture, want CaptureType) (capValue, error) {
	v, err := s.captureOrTuple(c, want)
	if err == nil && v.all {
		return capValue{}, fmt.Errorf("%s holds every face of an object and only a face all expectation of the same kind can use it", c)
	}
	return v, err
}

func (s *stepRun) captureOrTuple(c *Capture, want CaptureType) (capValue, error) {
	v := s.t.caps[c.Name]
	switch {
	case v == nil:
		return capValue{}, fmt.Errorf("slate: internal error: %s is not bound at step %d, and Check should have refused the script", c, s.n)
	case v.typ != want:
		return capValue{}, fmt.Errorf("slate: internal error: %s holds %s, and step %d needs %s", c, v.typ, s.n, want)
	}
	return *v, nil
}

// bindCapture binds a name for the rest of the test and prints the line.
func (s *stepRun) bindCapture(name string, v capValue) {
	t := s.t
	if t.caps[name] != nil {
		s.why = fmt.Sprintf("slate: internal error: $%s is bound twice, and Check should have refused the script", name)
		s.fail(1, "%s", s.why)
		return
	}
	v.step = s.n
	t.caps[name] = &v
	t.capNames = append(t.capNames, name)
	s.r.printf("%s capture $%s = %s (step %d)", time.Now().UTC().Format(stampLayout), name, v, s.n)
}

// capturedLine is the failure block's list of the captures so far, in the
// order they were bound.
func (t *testRun) capturedLine() string {
	if len(t.capNames) == 0 {
		return "none"
	}
	parts := make([]string, len(t.capNames))
	for i, n := range t.capNames {
		parts[i] = fmt.Sprintf("$%s %s", n, t.caps[n])
	}
	return strings.Join(parts, ", ")
}

// linkText is the link-text rule: printable ASCII, tab and newline.
func linkText(s string) bool {
	for i := 0; i < len(s); i++ {
		if b := s[i]; (b < 0x20 || b > 0x7e) && b != '\t' && b != '\n' {
			return false
		}
	}
	return true
}

// textMatch is a text of an expectation made ready: ok judges a string,
// and re is the pattern when it has named groups to bind.
type textMatch struct {
	ok func(string) bool
	re *regexp.Regexp
}

func (m textMatch) match(s string) bool { return m.ok(s) }

// groups is the named groups of the pattern in text, as text captures. A
// group that did not take part in the match is an error, and nothing binds.
func (m textMatch) groups(text string) ([]namedVal, error) {
	if m.re == nil {
		return nil, nil
	}
	idx := m.re.FindStringSubmatchIndex(text)
	if idx == nil {
		return nil, nil
	}
	var out []namedVal
	for i, n := range m.re.SubexpNames() {
		if n == "" {
			continue
		}
		if idx[2*i] < 0 {
			return nil, fmt.Errorf("$%s did not take part in the match", n)
		}
		out = append(out, namedVal{n, capValue{typ: CapText, text: text[idx[2*i]:idx[2*i+1]]}})
	}
	return out, nil
}

// textMatch makes a text ready: a literal, a pattern, or a capture, which
// is compared whole and exactly like a literal.
func (s *stepRun) textMatch(t Text) (textMatch, error) {
	if t.Capture == nil {
		return textMatcher(t)
	}
	v, err := s.capture(t.Capture, CapText)
	if err != nil {
		return textMatch{}, err
	}
	want := v.text
	return textMatch{ok: func(got string) bool { return got == want }}, nil
}

// groupSrc is a pattern and the text it matched.
type groupSrc struct {
	m    textMatch
	text string
}

// bindMatched binds what a positive expectation that has just matched
// binds: the named groups of each pattern in the text it matched, then its
// as, which takes as. A group that did not take part fails the step and
// binds nothing.
func (s *stepRun) bindMatched(x *expState, as *capValue, srcs ...groupSrc) {
	var list []namedVal
	for _, g := range srcs {
		vs, err := g.m.groups(g.text)
		if err != nil {
			s.why = err.Error()
			s.fail(1, "%s", s.why)
			return
		}
		list = append(list, vs...)
	}
	if as != nil && x.e.As != nil {
		list = append(list, namedVal{x.e.As.Name, *as})
	}
	for _, nv := range list {
		s.bindCapture(nv.name, nv.v)
	}
}
