package main

// What LSL this simulator implements.
//
// The simulator publishes its own syntax -- every function, constant,
// event and type it will accept -- which makes it the authority on what
// a script may say, rather than documentation that drifts.
//
// # Why there is a machine-readable form
//
// A compiler that targets this grid needs to know two things it cannot
// work out for itself: which of the functions it knows about will fail
// if called here, and which ones exist here that it has never heard of.
// Both are set differences against this list.  So the output has a form
// meant for a program as well as one meant for a person:
//
//	lsl -m -f      one function a line, tab-separated fields
//
// The machine form is deliberately dull.  One record a line, tab
// separated, no alignment, no totals, no colour, and the first field
// always says what kind of record it is -- so `lsl -m -a` can be read
// by the same loop as `lsl -m -f`, and a reader that meets a kind it
// does not know can skip the line rather than misparse it.
//
// Nothing is quoted or escaped, because nothing in the source contains
// a tab: names are identifiers and values are literals as written.  A
// tooltip could contain anything, so tooltips are not in it.

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/quark-idlemind/slgo/sl"
)

// lslOptions is what lsl was asked for.
type lslOptions struct {
	Functions bool `getopt:"--functions -f         list the functions"`
	Constants bool `getopt:"--constants -c         list the constants"`
	Events    bool `getopt:"--events -e            list the events"`
	Types     bool `getopt:"--types -t             list the types and keywords"`
	All       bool `getopt:"--all -a               list everything"`
	Machine   bool `getopt:"--machine-readable -m  one record a line, tab separated, for a program"`
	Help      bool `getopt:"--help -h              show what this command takes"`
}

// wanted says which kinds to print, and whether any flag chose.
//
// With none given the answer depends on whether there is a word to
// match: a bare "lsl" is the summary, and "lsl llSay" searches
// everything, which is what it did before these flags existed.
func (o *lslOptions) wanted() (functions, constants, events, types, chosen bool) {
	if o.All {
		return true, true, true, true, true
	}
	chosen = o.Functions || o.Constants || o.Events || o.Types
	return o.Functions, o.Constants, o.Events, o.Types, chosen
}

func cmdLSL(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o lslOptions
	rest, done, err := subOptions("lsl", "[TEXT]", &o, out, args)
	if err != nil || done {
		return err
	}

	s, err := sh.s.LSLSyntax(ctx)
	if err != nil {
		return err
	}

	fns, consts, evts, types, chosen := o.wanted()
	want := ""
	if len(rest) > 0 {
		want = strings.ToLower(strings.Join(rest, " "))
	}

	// No flags and no word: the summary, as before.  A word with no
	// flags searches everything, also as before.
	if !chosen {
		if want == "" && !o.Machine {
			fmt.Fprintf(out, "version %d: %d functions, %d constants, %d events, %d types\n",
				s.Version, len(s.Functions), len(s.Constants), len(s.Events), len(s.Types))
			return nil
		}
		fns, consts, evts, types = true, true, true, true
	}

	if o.Machine {
		return lslMachine(out, s, want, fns, consts, evts, types)
	}
	return lslReadable(out, s, want, fns, consts, evts, types)
}

// matches is whether this name is what was asked for.  An empty want
// matches everything, which is what -f alone means.
func matches(name, want string) bool {
	return want == "" || strings.Contains(strings.ToLower(name), want)
}

// lslReadable prints for a person.
func lslReadable(out io.Writer, s *sl.Syntax, want string, fns, consts, evts, types bool) error {
	if fns {
		for _, n := range s.FunctionNames() {
			if !matches(n, want) {
				continue
			}
			f := s.Functions[n]
			line := f.Signature()
			if f.Energy != 0 || f.Sleep != 0 {
				line += fmt.Sprintf("   energy %g, sleep %g", f.Energy, f.Sleep)
			}
			if f.Deprecated {
				line += "   [deprecated]"
			}
			if f.GodMode {
				line += "   [god mode]"
			}
			fmt.Fprintln(out, line)
		}
	}
	if consts {
		for _, n := range s.ConstantNames() {
			if !matches(n, want) {
				continue
			}
			c := s.Constants[n]
			fmt.Fprintf(out, "%s %s = %s\n", c.Type, c.Name, c.Value)
		}
	}
	if evts {
		for _, n := range s.EventNames() {
			if !matches(n, want) {
				continue
			}
			e := s.Events[n]
			var as []string
			for _, a := range e.Arguments {
				as = append(as, a.Type+" "+a.Name)
			}
			fmt.Fprintf(out, "%s(%s)\n", e.Name, strings.Join(as, ", "))
		}
	}
	if types {
		for _, n := range sortedKeys(s.Types) {
			if !matches(n, want) {
				continue
			}
			fmt.Fprintf(out, "type %s\n", n)
		}
	}
	return nil
}

// lslMachine prints for a program.
//
// One record a line, fields separated by tabs, the first field naming
// the kind.  The layouts, which are what a reader depends on and so are
// worth writing down exactly:
//
//	function  NAME  RETURN  ENERGY  SLEEP  FLAGS  ARGTYPE,...
//	constant  NAME  TYPE    VALUE
//	event     NAME  ARGTYPE,...
//	type      NAME
//
// RETURN is "void" when the function returns nothing.  FLAGS is a
// comma-separated set that may be empty, and today holds "deprecated"
// and "godmode".  A list of argument types is comma-separated and may
// be empty, so a function of no arguments has an empty last field.
//
// Fields are never reordered and never removed; anything new goes on
// the end, so a reader that splits and takes the first few fields keeps
// working.
func lslMachine(out io.Writer, s *sl.Syntax, want string, fns, consts, evts, types bool) error {
	if fns {
		for _, n := range s.FunctionNames() {
			if !matches(n, want) {
				continue
			}
			f := s.Functions[n]
			var flags []string
			if f.Deprecated {
				flags = append(flags, "deprecated")
			}
			if f.GodMode {
				flags = append(flags, "godmode")
			}
			fmt.Fprintf(out, "function\t%s\t%s\t%g\t%g\t%s\t%s\n",
				f.Name, orVoid(f.Return), f.Energy, f.Sleep,
				strings.Join(flags, ","), argTypes(f.Arguments))
		}
	}
	if consts {
		for _, n := range s.ConstantNames() {
			if !matches(n, want) {
				continue
			}
			c := s.Constants[n]
			fmt.Fprintf(out, "constant\t%s\t%s\t%s\n", c.Name, c.Type, c.Value)
		}
	}
	if evts {
		for _, n := range s.EventNames() {
			if !matches(n, want) {
				continue
			}
			e := s.Events[n]
			fmt.Fprintf(out, "event\t%s\t%s\n", e.Name, argTypes(e.Arguments))
		}
	}
	if types {
		for _, n := range sortedKeys(s.Types) {
			if !matches(n, want) {
				continue
			}
			fmt.Fprintf(out, "type\t%s\n", n)
		}
	}
	return nil
}

// orVoid is the return type, which the simulator leaves empty for a
// function that returns nothing.  Spelled out, so that a reader
// splitting on tabs never meets an empty field where a type belongs.
func orVoid(s string) string {
	if s == "" {
		return "void"
	}
	return s
}

// argTypes is the argument list as types alone, comma separated.  The
// names are not in it: a caller checking whether a call will compile
// cares what may be passed, not what the parameter was called.
func argTypes(as []sl.Argument) string {
	if len(as) == 0 {
		return ""
	}
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.Type)
	}
	return strings.Join(out, ",")
}

// sortedKeys is the map's keys in order, so that output does not move
// about between runs.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
