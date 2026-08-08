package main

import (
	"bytes"
	"io"
	"net/http"
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

// ------------------------------------------------- the command itself

// serveSyntax puts a small language behind the LSLSyntax capability,
// which is where the real one comes from: the simulator publishes what
// it implements, so it rather than documentation is the authority.
func serveSyntax(t *testing.T, x *testShell) {
	t.Helper()
	x.grid.ServeCap(t, "LSLSyntax", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/llsd+xml")
		io.WriteString(w, `<?xml version="1.0" ?><llsd><map>
		  <key>llsd-lsl-syntax-version</key><integer>2</integer>
		  <key>functions</key><map>
		    <key>llSay</key><map>
		      <key>energy</key><real>10</real><key>sleep</key><real>0.1</real>
		      <key>arguments</key><array>
		        <map><key>channel</key><map><key>type</key><string>integer</string></map></map>
		        <map><key>text</key><map><key>type</key><string>string</string></map></map>
		      </array>
		    </map>
		    <key>llGetPos</key><map>
		      <key>return</key><string>vector</string><key>energy</key><real>10</real>
		    </map>
		    <key>llCloud</key><map>
		      <key>return</key><string>float</string><key>deprecated</key><boolean>1</boolean>
		    </map>
		    <key>llGodLikeRezObject</key><map><key>god-mode</key><boolean>1</boolean></map>
		  </map>
		  <key>constants</key><map>
		    <key>ACTIVE</key><map>
		      <key>type</key><string>integer</string><key>value</key><string>0x2</string>
		    </map>
		  </map>
		  <key>events</key><map>
		    <key>state_entry</key><map/>
		    <key>touch_start</key><map><key>arguments</key><array>
		      <map><key>total_number</key><map><key>type</key><string>integer</string></map></map>
		    </array></map>
		  </map>
		  <key>types</key><map><key>integer</key><map/><key>key</key><map/></map>
		</map></llsd>`)
	})
}

// TestWantedIsWhatTheFlagsChose, and whether any of them did -- which
// is what decides between the summary and a search of everything.
func TestWantedIsWhatTheFlagsChose(t *testing.T) {
	for _, c := range []struct {
		o                        lslOptions
		fns, consts, evts, types bool
		chosen                   bool
	}{
		{o: lslOptions{}},
		{o: lslOptions{All: true}, fns: true, consts: true, evts: true, types: true, chosen: true},
		{o: lslOptions{Functions: true}, fns: true, chosen: true},
		{o: lslOptions{Constants: true}, consts: true, chosen: true},
		{o: lslOptions{Events: true}, evts: true, chosen: true},
		{o: lslOptions{Types: true}, types: true, chosen: true},
		{o: lslOptions{Functions: true, Types: true}, fns: true, types: true, chosen: true},
	} {
		fns, consts, evts, types, chosen := c.o.wanted()
		if fns != c.fns || consts != c.consts || evts != c.evts || types != c.types || chosen != c.chosen {
			t.Errorf("%+v: got %v %v %v %v %v", c.o, fns, consts, evts, types, chosen)
		}
	}
}

// TestBareLSLIsTheSummary, and a word with no flags searches
// everything, which is what it did before the flags existed.
func TestBareLSLIsTheSummary(t *testing.T) {
	x := newTestShell(t)
	serveSyntax(t, x)

	got := x.do(t, "lsl")
	if want := "version 2: 4 functions, 1 constants, 2 events, 2 types\n"; got != want {
		t.Errorf("lsl printed %q, want %q", got, want)
	}

	// A word alone searches every kind, and matches the NAME rather
	// than anything else about the entry.
	got = x.do(t, "lsl e")
	for _, want := range []string{
		"vector llGetPos()", "integer ACTIVE = 0x2", "state_entry()", "type integer",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("lsl e should find %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "touch_start") {
		t.Errorf("lsl e should not find a name with no e in it:\n%s", got)
	}
}

// TestLSLReadableSaysWhatCostsAndWhatIsGone.
//
// Energy and sleep are what a script pays, and deprecated and god mode
// are what a script cannot rely on -- all three are the reason to ask
// the simulator rather than read a manual.
func TestLSLReadableSaysWhatCostsAndWhatIsGone(t *testing.T) {
	x := newTestShell(t)
	serveSyntax(t, x)

	got := x.do(t, "lsl -f")
	for _, want := range []string{
		" llSay(integer channel, string text)   energy 10, sleep 0.1",
		"vector llGetPos()   energy 10, sleep 0",
		"float llCloud()   [deprecated]",
		" llGodLikeRezObject()   [god mode]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("lsl -f should print %q:\n%s", want, got)
		}
	}

	if got := x.do(t, "lsl -c"); got != "integer ACTIVE = 0x2\n" {
		t.Errorf("lsl -c printed %q", got)
	}
	if got, want := x.do(t, "lsl -e"), "state_entry()\ntouch_start(integer total_number)\n"; got != want {
		t.Errorf("lsl -e printed %q, want %q", got, want)
	}
	if got, want := x.do(t, "lsl -t"), "type integer\ntype key\n"; got != want {
		t.Errorf("lsl -t printed %q, want %q", got, want)
	}

	// A word narrows whichever kinds were asked for.
	if got, want := x.do(t, "lsl -f llsay"), " llSay(integer channel, string text)   energy 10, sleep 0.1\n"; got != want {
		t.Errorf("lsl -f llsay printed %q, want %q", got, want)
	}
	if got := x.do(t, "lsl -a"); !strings.Contains(got, "llSay") || !strings.Contains(got, "type key") {
		t.Errorf("lsl -a should print everything:\n%s", got)
	}
	// -a with a word is everything that matches, which for a function
	// name is the function and nothing else.
	if got, want := x.do(t, "lsl -a llsay"), " llSay(integer channel, string text)   energy 10, sleep 0.1\n"; got != want {
		t.Errorf("lsl -a llsay printed %q, want %q", got, want)
	}
}

// TestLSLMachineFormIsWhatACompilerReads: the same selection, in the
// dull form, and -m with no other flag means everything -- otherwise a
// program asking for the lot would get the summary meant for a person.
func TestLSLMachineFormIsWhatACompilerReads(t *testing.T) {
	x := newTestShell(t)
	serveSyntax(t, x)

	got := x.do(t, "lsl -m")
	for _, want := range []string{"function\tllSay\t", "constant\tACTIVE\t", "event\t", "type\tinteger"} {
		if !strings.Contains(got, want) {
			t.Errorf("lsl -m should print %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "version 2:") {
		t.Errorf("lsl -m should not print the summary meant for a person:\n%s", got)
	}

	if got := x.do(t, "lsl -m -f llgetpos"); got != "function\tllGetPos\tvector\t10\t0\t\t\n" {
		t.Errorf("lsl -m -f printed %q", got)
	}
}

// TestLSLSaysWhenTheSimulatorWillNotTellIt: a session whose simulator
// never offered the capability cannot answer, and an empty list would
// read as a grid with no functions in it.
func TestLSLSaysWhenTheSimulatorWillNotTellIt(t *testing.T) {
	x := newTestShell(t)
	if got := x.do(t, "lsl"); !strings.Contains(got, "LSLSyntax") {
		t.Errorf("lsl should name the capability it is missing, got %q", got)
	}
	if got := x.do(t, "lsl --help"); !strings.Contains(got, "-m") {
		t.Errorf("lsl --help printed %q", got)
	}
}
