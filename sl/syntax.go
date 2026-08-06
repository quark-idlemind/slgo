package sl

// The LSL this simulator implements, from the simulator.
//
// The LSLSyntax capability answers with the whole language: every
// function with its arguments, return type, energy and sleep, every
// constant with its type and value, every event with its arguments,
// and the types and control keywords.  It is what the built-in editor
// colours and completes from, and it comes from the machine that will
// run the script rather than from a wiki page somebody edited.
//
// For anything checking a compiler against Second Life, that makes it
// the oracle: a function this does not list does not exist here, a
// constant whose value differs is a bug in the compiler, and the day
// Linden Lab adds something the id in SimulatorFeatures changes and the
// difference can be read off.
//
// It is half a megabyte, so it is fetched once and kept, keyed by that
// id.  Asking again costs one small GET for the id and, when it has not
// moved, nothing else.

import (
	"bytes"
	"context"
	"fmt"
	"sort"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

// Argument is one parameter of a function or event.
type Argument struct {
	Name    string
	Type    string
	Tooltip string
}

// Function is one of the ll... calls.
type Function struct {
	Name      string
	Return    string // "void" when it returns nothing
	Arguments []Argument

	// Energy is what a call costs, and Sleep how long the script is
	// forced to wait afterwards, in seconds.  Both are what makes one
	// implementation of a function differ from another.
	Energy float64
	Sleep  float64

	Deprecated bool
	GodMode    bool
	Tooltip    string
}

// Signature renders the function the way it is written in a script.
func (f Function) Signature() string {
	var b []byte
	b = append(b, f.Return...)
	b = append(b, ' ')
	b = append(b, f.Name...)
	b = append(b, '(')
	for i, a := range f.Arguments {
		if i > 0 {
			b = append(b, ", "...)
		}
		b = append(b, a.Type...)
		b = append(b, ' ')
		b = append(b, a.Name...)
	}
	return string(append(b, ')'))
}

// Constant is one of the named values.
type Constant struct {
	Name    string
	Type    string
	Value   string // as written, so 0x2 stays 0x2
	Tooltip string
}

// Event is one of the handlers a state may declare.
type Event struct {
	Name      string
	Arguments []Argument
	Tooltip   string
}

// Syntax is the whole language, as the simulator describes it.
type Syntax struct {
	// Version is the format's own version, and ID is what
	// SimulatorFeatures called this syntax.
	Version int
	ID      msg.UUID

	Functions map[string]Function
	Constants map[string]Constant
	Events    map[string]Event

	// Types and Controls are the keywords, mapped to their
	// descriptions: integer, float, key ... and if, else, for, jump.
	Types    map[string]string
	Controls map[string]string

	// Raw is the decoded document, for anything not modelled above.
	Raw map[string]any
}

// FunctionNames, ConstantNames and EventNames list what there is,
// sorted, which is the form a comparison wants.
func (s *Syntax) FunctionNames() []string { return sortedKeys(s.Functions) }
func (s *Syntax) ConstantNames() []string { return sortedKeys(s.Constants) }
func (s *Syntax) EventNames() []string    { return sortedKeys(s.Events) }

func sortedKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// LSLSyntax fetches the language, or returns the copy already held when
// the simulator says it has not changed.
func (w *Session) LSLSyntax(ctx context.Context) (*Syntax, error) {
	// The id is cheap and says whether the expensive half is needed.
	var id msg.UUID
	if f, err := w.Features(ctx); err == nil {
		id = f.LSLSyntaxID()
	}

	w.mu.Lock()
	cached := w.syntax
	w.mu.Unlock()
	if cached != nil && (id.IsZero() || cached.ID == id) {
		return cached, nil
	}

	body, err := w.capDo(ctx, agent.CapRequest{Cap: "LSLSyntax", Method: "GET"})
	if err != nil {
		return nil, err
	}
	v, err := llsd.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("sl: LSLSyntax: %w", err)
	}
	m := llsd.Map(v)
	if m == nil {
		return nil, fmt.Errorf("sl: LSLSyntax answered with %T, wanted a map", v)
	}

	s := parseSyntax(m)
	s.ID = id

	w.mu.Lock()
	w.syntax = s
	w.mu.Unlock()
	return s, nil
}

func parseSyntax(m map[string]any) *Syntax {
	s := &Syntax{
		Raw:       m,
		Functions: map[string]Function{},
		Constants: map[string]Constant{},
		Events:    map[string]Event{},
		Types:     map[string]string{},
		Controls:  map[string]string{},
	}
	switch v := m["llsd-lsl-syntax-version"].(type) {
	case int64:
		s.Version = int(v)
	case float64:
		s.Version = int(v)
	}

	for name, e := range asMap(m["functions"]) {
		f := asMap(e)
		s.Functions[name] = Function{
			Name:       name,
			Return:     str(f["return"]),
			Arguments:  parseArguments(f["arguments"]),
			Energy:     num(f["energy"]),
			Sleep:      num(f["sleep"]),
			Deprecated: boolean(f["deprecated"]),
			GodMode:    boolean(f["god-mode"]),
			Tooltip:    str(f["tooltip"]),
		}
	}
	for name, e := range asMap(m["constants"]) {
		c := asMap(e)
		s.Constants[name] = Constant{
			Name:    name,
			Type:    str(c["type"]),
			Value:   str(c["value"]),
			Tooltip: str(c["tooltip"]),
		}
	}
	for name, e := range asMap(m["events"]) {
		v := asMap(e)
		s.Events[name] = Event{
			Name:      name,
			Arguments: parseArguments(v["arguments"]),
			Tooltip:   str(v["tooltip"]),
		}
	}
	for name, e := range asMap(m["types"]) {
		s.Types[name] = str(asMap(e)["tooltip"])
	}
	for name, e := range asMap(m["controls"]) {
		s.Controls[name] = str(asMap(e)["tooltip"])
	}
	return s
}

// parseArguments reads the argument list, which is an array of one-key
// maps: the key is the parameter's name and the value describes it.
// The order is the order they are written in, and it matters.
func parseArguments(v any) []Argument {
	arr, _ := v.([]any)
	var out []Argument
	for _, e := range arr {
		for name, d := range asMap(e) {
			a := asMap(d)
			out = append(out, Argument{
				Name:    name,
				Type:    str(a["type"]),
				Tooltip: str(a["tooltip"]),
			})
		}
	}
	return out
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func boolean(v any) bool {
	b, _ := v.(bool)
	return b
}

func num(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	}
	return 0
}
