package main

import (
	"io"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// TestRmOptions: the flag is spelled out in full on purpose, and rm
// deletes permanently, so an abbreviation of it must not be accepted.
func TestRmOptions(t *testing.T) {
	var o rmOptions
	rest, done, err := subOptions("rm", &o, io.Discard,
		[]string{"--remove-all-copies", "/Scripts/slrun-bench"})
	if err != nil || done {
		t.Fatalf("done=%v err=%v", done, err)
	}
	if !o.AllCopies {
		t.Error("--remove-all-copies did not take")
	}
	if len(rest) != 1 || rest[0] != "/Scripts/slrun-bench" {
		t.Errorf("the path should survive the flag, got %q", rest)
	}

	// Without it, rm is its careful self.
	o = rmOptions{}
	if _, _, err := subOptions("rm", &o, io.Discard, []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if o.AllCopies || o.Newest || o.Oldest {
		t.Error("rm chose among duplicates without being asked")
	}

	// The two that pick one end of the pile.
	for _, c := range []struct {
		flag           string
		newest, oldest bool
	}{
		{"--newest", true, false},
		{"--oldest", false, true},
	} {
		o = rmOptions{}
		rest, _, err := subOptions("rm", &o, io.Discard, []string{c.flag, "dup"})
		if err != nil {
			t.Fatalf("%s: %v", c.flag, err)
		}
		if o.Newest != c.newest || o.Oldest != c.oldest {
			t.Errorf("%s: got newest=%v oldest=%v", c.flag, o.Newest, o.Oldest)
		}
		if len(rest) != 1 || rest[0] != "dup" {
			t.Errorf("%s: the path should survive the flag, got %q", c.flag, rest)
		}
	}

	if _, _, err := subOptions("rm", &rmOptions{}, io.Discard,
		[]string{"--remove-all"}); err == nil {
		t.Error("a half-typed --remove-all should be refused, not guessed at")
	}
}

// TestWhichOne lists what the line said about choosing among several,
// which is what the refusal for saying two of them prints.
func TestWhichOne(t *testing.T) {
	for _, c := range []struct {
		o    rmOptions
		want string
	}{
		{rmOptions{}, ""},
		{rmOptions{Newest: true}, "--newest"},
		{rmOptions{Oldest: true}, "--oldest"},
		{rmOptions{AllCopies: true}, "--remove-all-copies"},
		{rmOptions{Newest: true, Oldest: true}, "--newest,--oldest"},
		{rmOptions{Oldest: true, AllCopies: true}, "--oldest,--remove-all-copies"},
	} {
		if got := strings.Join(c.o.whichOne(), ","); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.o, got, c.want)
		}
	}
}

// TestPickByAge is the rule --newest and --oldest turn on, and the two
// cases where the dates do not settle it and it must refuse rather than
// guess: a tie at the end being chosen, and a thing with no date.
func TestPickByAge(t *testing.T) {
	id := func(n byte) msg.UUID { var u msg.UUID; u[15] = n; return u }
	es := []sl.Entry{
		{Name: "dup", Created: 200, ID: id(1)},
		{Name: "dup", Created: 400, ID: id(2)},
		{Name: "dup", Created: 300, ID: id(3)},
	}

	if got, err := pickByAge(es, true); err != nil || got.ID != id(2) {
		t.Errorf("newest: got %v, %v", got.ID, err)
	}
	if got, err := pickByAge(es, false); err != nil || got.ID != id(1) {
		t.Errorf("oldest: got %v, %v", got.ID, err)
	}
	// One of the name is that one, whichever end was asked for.
	for _, newest := range []bool{true, false} {
		if got, err := pickByAge(es[:1], newest); err != nil || got.ID != id(1) {
			t.Errorf("newest=%v of one: got %v, %v", newest, got.ID, err)
		}
	}

	// A tie at the end being chosen: the other end is still an answer.
	tied := append(append([]sl.Entry{}, es...), sl.Entry{Name: "dup", Created: 400, ID: id(4)})
	if _, err := pickByAge(tied, true); err == nil {
		t.Error("two at 400 and it picked one of them as the newest")
	} else if !strings.Contains(err.Error(), "id") {
		t.Errorf("the refusal should point at the id, got %v", err)
	}
	if got, err := pickByAge(tied, false); err != nil || got.ID != id(1) {
		t.Errorf("the oldest is still unambiguous: got %v, %v", got.ID, err)
	}

	// Undated is not old.  A folder has no date at all, and it must not
	// come out as the oldest thing there is.
	undated := append(append([]sl.Entry{}, es...), sl.Entry{Name: "dup", Folder: true, ID: id(5)})
	for _, newest := range []bool{true, false} {
		if _, err := pickByAge(undated, newest); err == nil {
			t.Errorf("newest=%v: something with no date was ordered anyway", newest)
		}
	}
}

// TestMatchName is what a name means: everything of it, in listing
// order -- which is why a plain rm of a name that means three things
// refuses, and why --remove-all-copies has three to delete.
func TestMatchName(t *testing.T) {
	id := func(n byte) msg.UUID { var u msg.UUID; u[15] = n; return u }
	es := []sl.Entry{
		{Name: "slrun-bench", ID: id(1)},
		{Name: "other", ID: id(2)},
		{Name: "SLRUN-BENCH", ID: id(3)}, // the grid keeps case; we ignore it
		{Name: "slrun-bench", ID: id(4)},
	}

	got := matchName(es, "slrun-bench")
	if len(got) != 3 {
		t.Fatalf("got %d matches, want 3", len(got))
	}
	if got[0].ID != id(1) {
		t.Errorf("listing order should put this one first, got %v", got[0].ID)
	}
	var names []string
	for _, e := range got {
		names = append(names, e.Name)
	}
	if want := "slrun-bench,SLRUN-BENCH,slrun-bench"; strings.Join(names, ",") != want {
		t.Errorf("listing order not kept: %v", names)
	}

	if n := len(matchName(es, "nothing of the sort")); n != 0 {
		t.Errorf("got %d matches for a name that is not there", n)
	}
}
