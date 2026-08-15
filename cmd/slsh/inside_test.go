package main

import (
	"strings"
	"testing"
)

// The operations themselves -- listing, renaming and deleting inside an
// object -- are not exercised here: they go through RequestTaskInventory
// and the xfer protocol, which this package's fake grid does not speak.
// They were run against Agni instead.  What is checked here is the
// wiring: that --in takes the path it is given to the right place, and
// that a command asked for something impossible says so rather than
// doing something else.

// TestLsInTakesNoPath: an object holds no folders, so a path alongside
// --in is a person expecting something this cannot do.
func TestLsInTakesNoPath(t *testing.T) {
	x := newTestShell(t)
	got := x.do(t, "ls --in Box1 Objects/thing")
	if !strings.Contains(got, "takes no path") {
		t.Errorf("ls --in with a path printed %q", got)
	}
}

// TestDropWantsBoth: an object and an item, since neither can be
// guessed at.
func TestDropWantsBoth(t *testing.T) {
	x := newTestShell(t)
	if got := x.do(t, "drop"); !strings.Contains(got, "usage: drop") {
		t.Errorf("drop with nothing printed %q", got)
	}
	if got := x.do(t, "drop Box1"); !strings.Contains(got, "usage: drop") {
		t.Errorf("drop with only an object printed %q", got)
	}
}

// TestNewRefusesAKindItCannotMake: a texture goes up through the upload
// that charges L$, and quietly making a notecard instead would be a
// surprise of the worst kind.
func TestNewRefusesAKindItCannotMake(t *testing.T) {
	x := newTestShell(t)
	got := x.do(t, "new --kind texture Objects/thing")
	if !strings.Contains(got, "notecard or script") {
		t.Errorf("new --kind texture printed %q", got)
	}
}
