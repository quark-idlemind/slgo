package main

import (
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

func TestParseKey(t *testing.T) {
	cases := map[string]rune{
		"ESC":    27,
		"esc":    27,
		"escape": 27,
		"^[":     27,
		"TAB":    9,
		"^G":     7,
		"^g":     7,
		"C-g":    7,
		"ctrl-g": 7,
		"^A":     1,
		"!":      '!',
		"0x07":   7,
		"27":     27,
		"space":  ' ',
	}
	for in, want := range cases {
		got, err := ParseKey(in)
		if err != nil {
			t.Errorf("ParseKey(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseKey(%q) = %d, want %d", in, got, want)
		}
	}

	for _, in := range []string{"", "nonsense", "enter", "^!"} {
		if _, err := ParseKey(in); err == nil {
			t.Errorf("ParseKey(%q) should have been refused", in)
		}
	}
}

func TestKeyName(t *testing.T) {
	cases := map[rune]string{27: "ESC", 9: "TAB", 7: "^G", 1: "^A", 'x': "x", ' ': "SPACE"}
	for in, want := range cases {
		if got := KeyName(in); got != want {
			t.Errorf("KeyName(%d) = %q, want %q", in, got, want)
		}
	}
}

// TestSessionID: both ends have to compute the same id from their two
// agent ids without being told, which is what makes it an exclusive or.
func TestSessionID(t *testing.T) {
	a := msg.MustParseUUID("a5707e57-7e57-c0de-65b6-5a7ceceb4b01")
	b := msg.MustParseUUID("e6887e57-7e57-c0de-2a6a-c862daab753b")

	if sessionID(a, b) != sessionID(b, a) {
		t.Error("the two ends would disagree about the session")
	}
	if sessionID(a, b).IsZero() {
		t.Error("two different people should not produce a zero session")
	}
	if !sessionID(a, a).IsZero() {
		t.Error("the same person twice is a zero session")
	}
}

func TestSessionsCycle(t *testing.T) {
	ss := NewSessions()
	if !ss.Current().Local() {
		t.Fatal("the first session should be open chat")
	}

	alice := msg.MustParseUUID("a5707e57-7e57-c0de-65b6-5a7ceceb4b01")
	bob := msg.MustParseUUID("e6887e57-7e57-c0de-2a6a-c862daab753b")
	if _, made := ss.Open(alice, "Alice Resident"); !made {
		t.Error("the first Open should have made a session")
	}
	if _, made := ss.Open(alice, "Alice Resident"); made {
		t.Error("Open made a second session for the same person")
	}
	ss.Open(bob, "Bob Resident")

	// Round the houses and back to the start.
	want := []string{"Alice Resident", "Bob Resident", "Local", "Alice Resident"}
	for i, w := range want {
		if got := ss.Next().Label(); got != w {
			t.Errorf("cycle step %d = %q, want %q", i, got, w)
		}
	}

	// Closing the current one lands somewhere real.
	ss.Switch(ss.Current())
	cur := ss.Close(ss.Current())
	if cur == nil || cur.Label() == "Alice Resident" {
		t.Errorf("after closing Alice, current is %v", cur)
	}

	// Open chat cannot be closed: it is the one place there always is
	// to talk.
	list, _ := ss.All()
	ss.Switch(list[0])
	ss.Close(list[0])
	if list, _ := ss.All(); list[0].Label() != "Local" {
		t.Error("open chat was closed")
	}
}

func TestRosterFind(t *testing.T) {
	r := NewRoster()
	alice := msg.MustParseUUID("a5707e57-7e57-c0de-65b6-5a7ceceb4b01")
	bob := msg.MustParseUUID("e6887e57-7e57-c0de-2a6a-c862daab753b")
	alto := msg.MustParseUUID("e34a7e57-7e57-c0de-432d-e2701ced4688")
	r.Learn(alice, "Alice Resident")
	r.Learn(bob, "Bob Idlemind")
	r.Learn(alto, "Alto Resident")

	if got := r.Find("alice"); len(got) != 1 || got[0] != alice {
		t.Errorf("Find(alice) = %v", got)
	}
	// A full name, in the wrong case.
	if got := r.Find("bob idlemind"); len(got) != 1 || got[0] != bob {
		t.Errorf("Find(bob idlemind) = %v", got)
	}
	// The last name on its own.
	if got := r.Find("idlemind"); len(got) != 1 || got[0] != bob {
		t.Errorf("Find(idlemind) = %v", got)
	}
	// An ambiguous prefix returns both, so the caller can refuse
	// rather than pick.
	if got := r.Find("al"); len(got) != 2 {
		t.Errorf("Find(al) = %v, want both Alice and Alto", got)
	}
	if got := r.Find("nobody"); len(got) != 0 {
		t.Errorf("Find(nobody) = %v", got)
	}
}

func TestRosterUnknownAsksOnce(t *testing.T) {
	r := NewRoster()
	known := msg.MustParseUUID("a5707e57-7e57-c0de-65b6-5a7ceceb4b01")
	stranger := msg.MustParseUUID("e6887e57-7e57-c0de-2a6a-c862daab753b")
	r.Learn(known, "Alice Resident")

	got := r.Unknown([]msg.UUID{known, stranger})
	if len(got) != 1 || got[0] != stranger {
		t.Fatalf("Unknown = %v, want just the stranger", got)
	}
	// Asking twice for the same person is a round trip wasted.
	if got := r.Unknown([]msg.UUID{stranger}); len(got) != 0 {
		t.Errorf("Unknown asked again: %v", got)
	}
	// Until the answer arrives, at which point it is known.
	r.Learn(stranger, "Bob Resident")
	if r.NameOr(stranger) != "Bob Resident" {
		t.Errorf("name = %q", r.NameOr(stranger))
	}
}

// TestNameOrFallsBackToTheId: a listing has to print something for
// somebody the simulator has not named yet.
func TestNameOrFallsBackToTheId(t *testing.T) {
	r := NewRoster()
	id := msg.MustParseUUID("a5707e57-7e57-c0de-65b6-5a7ceceb4b01")
	if got := r.NameOr(id); got != "(3ac37e57)" {
		t.Errorf("NameOr = %q", got)
	}
}

func TestNulTerm(t *testing.T) {
	got := nulTerm("hello")
	if string(got) != "hello\x00" {
		t.Errorf("nulTerm = %q", got)
	}
	if trimNul(got) != "hello" {
		t.Errorf("trimNul did not undo it: %q", trimNul(got))
	}
}

// TestCommandModeRewritesThePrompt: pressing the prefix key has to say
// so, and must not cost whatever was half typed at the time.
func TestCommandModeRewritesThePrompt(t *testing.T) {
	tm := &Term{plain: true}
	a := &App{
		cfg:      Config{Prefix: 27},
		term:     tm,
		sessions: NewSessions(),
		offers:   map[msg.UUID]offer{},
		quit:     make(chan struct{}),
	}

	a.refreshPrompt()
	if tm.prompt != "Local> " {
		t.Errorf("prompt = %q, want the session it will go to", tm.prompt)
	}

	for _, r := range "half a sentence" {
		tm.Key(r)
	}
	a.enterCommand()
	if tm.prompt != "command> " {
		t.Errorf("the prompt did not say a command was being typed: %q", tm.prompt)
	}
	if tm.Line() != "" {
		t.Errorf("the command line started with something in it: %q", tm.Line())
	}

	a.leaveCommand()
	if tm.prompt != "Local> " {
		t.Errorf("prompt after the command: %q", tm.prompt)
	}
	if tm.Line() != "half a sentence" {
		t.Errorf("the half typed line was lost: %q", tm.Line())
	}
}

// TestPromptFollowsTheSession: the prompt is the only thing that says
// where the next line will go, so it has to track tab.
func TestPromptFollowsTheSession(t *testing.T) {
	tm := &Term{plain: true}
	a := &App{
		cfg: Config{Prefix: 27}, term: tm,
		sessions: NewSessions(), offers: map[msg.UUID]offer{},
		quit: make(chan struct{}),
	}
	id := msg.MustParseUUID("e6887e57-7e57-c0de-2a6a-c862daab753b")
	a.sessions.Open(id, "Quark Idlemind")

	a.refreshPrompt()
	if tm.prompt != "Local> " {
		t.Fatalf("prompt = %q", tm.prompt)
	}
	a.tab()
	if tm.prompt != "Quark Idlemind> " {
		t.Errorf("after tab the prompt is %q", tm.prompt)
	}
	a.tab()
	if tm.prompt != "Local> " {
		t.Errorf("tab did not wrap round: %q", tm.prompt)
	}

	// With something typed, tab must not move: sending a half typed
	// line to the wrong person is worse than not cycling.
	for _, r := range "mid sentence" {
		tm.Key(r)
	}
	a.tab()
	if tm.prompt != "Local> " {
		t.Errorf("tab cycled with a line in progress: %q", tm.prompt)
	}
}
