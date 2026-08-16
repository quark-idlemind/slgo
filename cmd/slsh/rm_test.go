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
	if o.AllCopies {
		t.Error("all copies without being asked")
	}

	if _, _, err := subOptions("rm", &rmOptions{}, io.Discard,
		[]string{"--remove-all"}); err == nil {
		t.Error("a half-typed --remove-all should be refused, not guessed at")
	}
}

// TestMatchName is the rule --remove-all-copies turns on: everything of
// the name, in listing order, so the first is what a plain rm means.
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
		t.Errorf("the first match should be the one a plain rm takes, got %v", got[0].ID)
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
