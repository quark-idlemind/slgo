package main

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// TestReadLsOptions pins the flag letters and how they cluster.
func TestReadLsOptions(t *testing.T) {
	for _, c := range []struct {
		args             []string
		long, deep, xact bool
		path             string
		byTime           bool
	}{
		{args: []string{}},
		{args: []string{"-l"}, long: true},
		{args: []string{"-r"}, deep: true},
		{args: []string{"-lr"}, long: true, deep: true},
		{args: []string{"-rl"}, long: true, deep: true},
		{args: []string{"-l", "-r"}, long: true, deep: true},
		// -T is detail, and -l is what detail is, so it implies it.
		{args: []string{"-T"}, long: true, xact: true},
		{args: []string{"-lT"}, long: true, xact: true},
		{args: []string{"-lrT"}, long: true, deep: true, xact: true},
		{args: []string{"-lt"}, long: true, byTime: true},
		{args: []string{"-l", "-t"}, long: true, byTime: true},
		{args: []string{"-ltT"}, long: true, byTime: true, xact: true},
		{args: []string{"-lT", "/Objects"}, long: true, xact: true, path: "/Objects"},
		{args: []string{"/Objects"}, path: "/Objects"},
		{args: []string{"-"}, path: "-"},
	} {
		o, err := readLsOptions(io.Discard, c.args)
		if err != nil {
			t.Errorf("%v: %v", c.args, err)
			continue
		}
		if o.Long != c.long || o.Deep != c.deep || o.Exact != c.xact ||
			o.ByTime != c.byTime || o.path != c.path {
			t.Errorf("%v: got long=%v deep=%v exact=%v byTime=%v path=%q, want %v %v %v %v %q",
				c.args, o.Long, o.Deep, o.Exact, o.ByTime, o.path,
				c.long, c.deep, c.xact, c.byTime, c.path)
		}
	}

	if _, err := readLsOptions(io.Discard, []string{"-lq"}); err == nil {
		t.Error(`-lq should be refused: "q" is not a flag`)
	} else if !strings.Contains(err.Error(), "q") {
		t.Errorf("the refusal should name the letter, got %v", err)
	}
}

// TestLsWhen: the date column stays ONE field with -T as without it, so
// that a listing has four columns either way and anything reading the
// id out of the third field goes on working.
func TestLsWhen(t *testing.T) {
	when := time.Date(2026, 8, 3, 21, 26, 43, 0, time.Local).Unix()

	if got, want := lsWhen(when, false), "2026-08-03"; got != want {
		t.Errorf("without -T: got %q, want %q", got, want)
	}
	if got, want := lsWhen(when, true), "2026-08-03T21:26:43"; got != want {
		t.Errorf("with -T: got %q, want %q", got, want)
	}
	for _, exact := range []bool{false, true} {
		if got := lsWhen(when, exact); strings.ContainsAny(got, " \t") {
			t.Errorf("exact=%v: %q has whitespace in it, so it is two columns", exact, got)
		}
		// A folder has no date and must not collapse the column.
		if got := lsWhen(0, exact); got != "-" {
			t.Errorf("exact=%v: a folder should be %q, got %q", exact, "-", got)
		}
	}
}

// TestSortByTime: -t is newest first, then by name, and it has to give
// the same answer twice for the same listing.
func TestSortByTime(t *testing.T) {
	id := func(n byte) msg.UUID { var u msg.UUID; u[15] = n; return u }
	es := []sl.Entry{
		{Name: "beta", Created: 100, ID: id(1)},
		{Name: "alpha", Created: 300, ID: id(2)},
		{Name: "Zulu", Created: 200, ID: id(3)},
		{Name: "alpha", Created: 200, ID: id(4)}, // ties with Zulu on time
		{Name: "a folder", Folder: true, ID: id(5)},
	}
	sortByTime(es)

	var got []string
	for _, e := range es {
		got = append(got, e.Name)
	}
	// 300, then the two at 200 by name (alpha before Zulu, ignoring
	// case), then 100, and the undated folder last.
	want := []string{"alpha", "alpha", "Zulu", "beta", "a folder"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}

	// Two entries alike in every way a reader can see still have to
	// come out in a fixed order, or two listings of one folder differ.
	same := []sl.Entry{
		{Name: "same", Created: 5, ID: id(9)},
		{Name: "same", Created: 5, ID: id(2)},
	}
	sortByTime(same)
	if same[0].ID != id(2) {
		t.Errorf("ties should fall out by id, got %v first", same[0].ID)
	}
}
