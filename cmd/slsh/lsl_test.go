package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/sl"
)

// syntax is a small stand-in for what the simulator publishes.
func testSyntax() *sl.Syntax {
	return &sl.Syntax{
		Version: 2,
		Functions: map[string]sl.Function{
			"llGetPos": {Name: "llGetPos", Return: "vector", Energy: 10},
			"llSay": {Name: "llSay", Energy: 10, Sleep: 0.1, Arguments: []sl.Argument{
				{Name: "channel", Type: "integer"}, {Name: "msg", Type: "string"},
			}},
			"llCloud": {Name: "llCloud", Return: "float", Energy: 10, Deprecated: true,
				Arguments: []sl.Argument{{Name: "offset", Type: "vector"}}},
		},
		Constants: map[string]sl.Constant{
			"ACTIVE": {Name: "ACTIVE", Type: "integer", Value: "0x2"},
		},
		Events: map[string]sl.Event{
			"touch_start": {Name: "touch_start", Arguments: []sl.Argument{{Name: "n", Type: "integer"}}},
			"state_entry": {Name: "state_entry"},
		},
		Types: map[string]string{"integer": "", "key": ""},
	}
}

// TestMachineFormat pins the layout eLSL parses.  It is a promise to
// another program, so the fields and their order are not free to drift:
// a reader splits on tabs and takes them by position.
func TestMachineFormat(t *testing.T) {
	var b bytes.Buffer
	if err := lslMachine(&b, testSyntax(), "", true, true, true, true); err != nil {
		t.Fatal(err)
	}

	want := []string{
		// kind      name         return  energy sleep flags       args
		"function\tllCloud\tfloat\t10\t0\tdeprecated\tvector",
		"function\tllGetPos\tvector\t10\t0\t\t",
		"function\tllSay\tvoid\t10\t0.1\t\tinteger,string",
		"constant\tACTIVE\tinteger\t0x2",
		"event\tstate_entry\t",
		"event\ttouch_start\tinteger",
		"type\tinteger",
		"type\tkey",
	}
	got := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%s", len(got), len(want), b.String())
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d:\n got %q\nwant %q", i, got[i], want[i])
		}
	}
}

// TestMachineFieldCounts: a reader splits on tabs and indexes, so a
// record of the wrong width is worse than a missing one -- it parses.
func TestMachineFieldCounts(t *testing.T) {
	var b bytes.Buffer
	if err := lslMachine(&b, testSyntax(), "", true, true, true, true); err != nil {
		t.Fatal(err)
	}
	widths := map[string]int{"function": 7, "constant": 4, "event": 3, "type": 2}
	for _, line := range strings.Split(strings.TrimRight(b.String(), "\n"), "\n") {
		f := strings.Split(line, "\t")
		want, ok := widths[f[0]]
		if !ok {
			t.Errorf("unknown record kind %q", f[0])
			continue
		}
		if len(f) != want {
			t.Errorf("%s record has %d fields, want %d: %q", f[0], len(f), want, line)
		}
	}
}

// TestMachineSelects: each flag limits the output to its own kind, so
// "lsl -m -f" is functions and nothing else.
func TestMachineSelects(t *testing.T) {
	cases := []struct {
		name                     string
		fns, consts, evts, types bool
		wantKinds                []string
	}{
		{"functions", true, false, false, false, []string{"function"}},
		{"constants", false, true, false, false, []string{"constant"}},
		{"events", false, false, true, false, []string{"event"}},
		{"types", false, false, false, true, []string{"type"}},
		{"all", true, true, true, true, []string{"function", "constant", "event", "type"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var b bytes.Buffer
			if err := lslMachine(&b, testSyntax(), "", c.fns, c.consts, c.evts, c.types); err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			for _, line := range strings.Split(strings.TrimRight(b.String(), "\n"), "\n") {
				if line == "" {
					continue
				}
				seen[strings.SplitN(line, "\t", 2)[0]] = true
			}
			if len(seen) != len(c.wantKinds) {
				t.Errorf("kinds %v, want %v", seen, c.wantKinds)
			}
			for _, k := range c.wantKinds {
				if !seen[k] {
					t.Errorf("no %s records", k)
				}
			}
		})
	}
}

// TestMachineFiltersByName: a word still narrows the output, so
// "lsl -m -f llSay" is one line.
func TestMachineFiltersByName(t *testing.T) {
	var b bytes.Buffer
	if err := lslMachine(&b, testSyntax(), "llsay", true, true, true, true); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimRight(b.String(), "\n")
	if !strings.HasPrefix(got, "function\tllSay\t") || strings.Contains(got, "\n") {
		t.Errorf("expected the one llSay line, got:\n%s", got)
	}
}

// TestVoidReturn: the simulator leaves a void function's return type
// empty, and an empty field where a type belongs would shift nothing
// but would read as a missing value.
func TestVoidReturn(t *testing.T) {
	if got := orVoid(""); got != "void" {
		t.Errorf("orVoid(\"\") = %q, want \"void\"", got)
	}
	if got := orVoid("vector"); got != "vector" {
		t.Errorf("orVoid(\"vector\") = %q", got)
	}
}
