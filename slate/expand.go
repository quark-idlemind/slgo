package slate

import (
	"fmt"
	"strings"
)

// Phase says which part of a test a step came from.
type Phase int

const (
	PhaseBefore Phase = iota // before each
	PhaseBody                // the test's own steps
	PhaseAfter               // after each
)

func (p Phase) String() string {
	switch p {
	case PhaseBefore:
		return "before each"
	case PhaseAfter:
		return "after each"
	default:
		return "test"
	}
}

// Site is one do call: the step do NAME that inlined a sequence.
type Site struct {
	Name string
	Span Span // the do step; Span.Line is the line to report
}

// ExpandedStep is one step of a test as it runs, with do calls inlined.
// Step is the step in the script, so its Span is where it is written.
// Sequence is the innermost sequence it is written in, or empty when it
// is written directly in its block. Via lists the do calls that reached
// it, outermost first, and is empty for a step written in its block.
type ExpandedStep struct {
	Step     *Step
	Phase    Phase
	Sequence string
	Via      []Site
}

// ExpandedTest is one test's steps in run order: before each, the body,
// after each. Step N of the run is Steps[N-1].
type ExpandedTest struct {
	Test  *Test
	Steps []ExpandedStep
}

// Expand returns every test with its steps expanded. It fails on a do of
// an undefined sequence and on a cycle, the same errors Check reports.
func (s *Script) Expand() ([]ExpandedTest, error) {
	if s == nil {
		return nil, &Error{Msg: "no script"}
	}
	out := make([]ExpandedTest, 0, len(s.Tests))
	for i := range s.Tests {
		et, err := s.expandTest(&s.Tests[i])
		if err != nil {
			return nil, err
		}
		out = append(out, et)
	}
	return out, nil
}

func (s *Script) expandTest(t *Test) (ExpandedTest, error) {
	et := ExpandedTest{Test: t}
	emit := func(x ExpandedStep) { et.Steps = append(et.Steps, x) }
	if b := s.Before(); b != nil {
		if err := s.inline(b.Steps, PhaseBefore, "", nil, nil, emit); err != nil {
			return ExpandedTest{}, err
		}
	}
	if err := s.inline(t.Steps, PhaseBody, "", nil, nil, emit); err != nil {
		return ExpandedTest{}, err
	}
	if b := s.After(); b != nil {
		if err := s.inline(b.Steps, PhaseAfter, "", nil, nil, emit); err != nil {
			return ExpandedTest{}, err
		}
	}
	return et, nil
}

// inline walks steps, calling emit for each plain step and recursing into
// the sequence a do names. stack holds the sequences being expanded, for
// the cycle report. A nil emit only validates the calls.
func (s *Script) inline(steps []Step, ph Phase, seq string, via []Site, stack []string, emit func(ExpandedStep)) error {
	for i := range steps {
		st := &steps[i]
		if st.Do == nil {
			if emit != nil {
				emit(ExpandedStep{Step: st, Phase: ph, Sequence: seq, Via: via})
			}
			continue
		}
		name := st.Do.Name.Text
		target := s.Sequence(name)
		if target == nil {
			return s.errAt(st.Do.Name.Span, "do %s: there is no sequence of that name", name)
		}
		for k, n := range stack {
			if n == name {
				cycle := append(append([]string{}, stack[k:]...), name)
				return s.errAt(st.Do.Span, "sequence cycle: %s", strings.Join(cycle, " -> "))
			}
		}
		next := append(append([]Site{}, via...), Site{Name: name, Span: st.Do.Span})
		inner := append(append([]string{}, stack...), name)
		if err := s.inline(target.Steps, ph, name, next, inner, emit); err != nil {
			return err
		}
	}
	return nil
}

func (s *Script) errAt(sp Span, format string, args ...any) error {
	return &Error{File: s.File, Line: sp.Line, Column: sp.Col, Msg: fmt.Sprintf(format, args...)}
}

// Via formats a call chain the way the failure block prints it, innermost
// call first: "via do inner at line 9 via do outer at line 14".
// Why: doc/slate-language.md#reading-the-result
func Via(chain []Site) string {
	parts := make([]string, len(chain))
	for i, c := range chain {
		parts[len(chain)-1-i] = fmt.Sprintf("via do %s at line %d", c.Name, c.Span.Line)
	}
	return strings.Join(parts, " ")
}
