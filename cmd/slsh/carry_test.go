package main

import (
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/sl"
)

// TestPlaceRefusesTheLineThatUsedToMoveAnObject.
//
// "place NAME X Y Z" repositioned a rezzed object until this name was
// given to the other half of take, and a file of commands written
// before that still says so.  Obeyed as far as its first word it would
// name an inventory item instead -- an object and the item it came from
// share a name -- and rez a second copy beside the avatar, report
// success, and leave the object the script meant to move where it was.
// So the argument count is part of what the command means, and the
// refusal says which word does the old job.
func TestPlaceRefusesTheLineThatUsedToMoveAnObject(t *testing.T) {
	x := newTestShell(t)

	if got := x.do(t, "place probe 10 20 30"); !strings.Contains(got, "move NAME X Y Z") {
		t.Errorf("the old four-argument place printed %q, which does not name move", got)
	}
	// The form place does take is not caught by the count: it gets as
	// far as looking the path up, and fails there or not at all.
	if got := x.do(t, "place nosuchthing"); strings.Contains(got, "move NAME X Y Z") {
		t.Errorf("a one-argument place was refused for its arity: %q", got)
	}
}

// TestPermMask: the letters are slinv's, because they are what is
// written in the notes of every experiment run so far.
func TestPermMask(t *testing.T) {
	for _, c := range []struct {
		text string
		want uint32
	}{
		{"all", sl.PermAll},
		{"none", 0},
		{"", 0},
		{"c", sl.PermCopy},
		{"cmt", sl.PermCopy | sl.PermModify | sl.PermTransfer},
		{"CMTV", sl.PermAll},
		{" cm ", sl.PermCopy | sl.PermModify},
	} {
		got, err := permMask(c.text)
		if err != nil {
			t.Errorf("permMask(%q) = %v", c.text, err)
			continue
		}
		if got != c.want {
			t.Errorf("permMask(%q) = %#x, want %#x", c.text, got, c.want)
		}
	}
}

// TestPermMaskRefusesNonsense: a typo in a permission is not a thing to
// guess at -- "cmz" meaning copy and modify would quietly drop whatever
// z was meant to be.
func TestPermMaskRefusesNonsense(t *testing.T) {
	if _, err := permMask("cmz"); err == nil {
		t.Error("permMask(\"cmz\") was accepted")
	}
}

// TestPermsSaysOnlyWhatTheRegionConfirmed.
//
// Nothing answers a permission change, so perms reads the masks back
// and prints a line for each one that reads as asked.  The permission
// rules never give everyone modify, so that request comes back as an
// error saying what everyone may do, and no line claims otherwise.
func TestPermsSaysOnlyWhatTheRegionConfirmed(t *testing.T) {
	t.Parallel()
	x := newTestShell(t)
	standing(x, aPrim(aChair, 11, "lantern", 0))
	x.grid.AnswerPermissions(t, func(who uint8, mask uint32) uint32 {
		if who == sl.WhoEveryone {
			mask &^= sl.PermModify
		}
		return mask
	})

	got := x.do(t, "perms --group cm --everyone cm lantern")
	if !strings.Contains(got, "the group may now copy, modify") {
		t.Errorf("perms did not say what it confirmed:\n%s", got)
	}
	if strings.Contains(got, "everyone may now") {
		t.Errorf("perms claimed a change the region did not make:\n%s", got)
	}
	if !strings.Contains(got, "it allows copy") {
		t.Errorf("perms did not say what everyone may do instead:\n%s", got)
	}
}
