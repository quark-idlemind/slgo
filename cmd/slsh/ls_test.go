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
		args       []string
		long, deep bool
		path       string
		byTime     bool
	}{
		{args: []string{}},
		{args: []string{"-l"}, long: true},
		{args: []string{"-r"}, deep: true},
		{args: []string{"-lr"}, long: true, deep: true},
		{args: []string{"-rl"}, long: true, deep: true},
		{args: []string{"-l", "-r"}, long: true, deep: true},
		{args: []string{"-lt"}, long: true, byTime: true},
		{args: []string{"-l", "-t"}, long: true, byTime: true},
		{args: []string{"-lrt"}, long: true, deep: true, byTime: true},
		{args: []string{"-l", "/Objects"}, long: true, path: "/Objects"},
		{args: []string{"/Objects"}, path: "/Objects"},
		{args: []string{"-"}, path: "-"},
	} {
		o, err := readLsOptions(io.Discard, c.args)
		if err != nil {
			t.Errorf("%v: %v", c.args, err)
			continue
		}
		if o.Long != c.long || o.Deep != c.deep ||
			o.ByTime != c.byTime || o.path != c.path {
			t.Errorf("%v: got long=%v deep=%v byTime=%v path=%q, want %v %v %v %q",
				c.args, o.Long, o.Deep, o.ByTime, o.path,
				c.long, c.deep, c.byTime, c.path)
		}
	}

	// -T used to add the time of day, which -l now always prints.  It
	// is gone rather than kept as a flag that does nothing, so that a
	// line that asks for it is answered rather than quietly obeyed.
	if _, err := readLsOptions(io.Discard, []string{"-lT"}); err == nil {
		t.Error("-T should be refused now that -l prints the time")
	}

	if _, err := readLsOptions(io.Discard, []string{"-lq"}); err == nil {
		t.Error(`-lq should be refused: "q" is not a flag`)
	} else if !strings.Contains(err.Error(), "q") {
		t.Errorf("the refusal should name the letter, got %v", err)
	}
}

// TestLsWhen: the whole date, down to the second, in ONE field -- the
// listing has four columns, so anything reading the id out of the third
// goes on working, and the seconds are what rm --newest chooses by.
func TestLsWhen(t *testing.T) {
	when := time.Date(2026, 8, 3, 21, 26, 43, 0, time.Local).Unix()

	if got, want := lsWhen(when), "2026-08-03T21:26:43"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := lsWhen(when); strings.ContainsAny(got, " \t") {
		t.Errorf("%q has whitespace in it, so it is two columns", got)
	}
	// A folder has no date and must not collapse the column.
	if got := lsWhen(0); got != "-" {
		t.Errorf("a folder should be %q, got %q", "-", got)
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

// TestAttachPointNames: worn prints where a thing is worn, so the
// points have to be named -- and an unknown one has to say so rather
// than be invented.
func TestAttachPointNames(t *testing.T) {
	for point, want := range map[int]string{
		1:                "chest",
		sl.HUDBottomLeft: "HUD bottom left",
		sl.HUDTop:        "HUD top",
		40:               "avatar centre",
		250:              "point 250", // no such point; say the number
	} {
		if got := sl.AttachPointName(point); got != want {
			t.Errorf("AttachPointName(%d) = %q, want %q", point, got, want)
		}
	}
}
