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

// TestPermWords says a mask back in words, so what was set is legible
// to somebody who does not know the letters.
func TestPermWords(t *testing.T) {
	for _, c := range []struct {
		mask uint32
		want string
	}{
		{0, "nothing"},
		{sl.PermCopy, "copy"},
		{sl.PermCopy | sl.PermTransfer, "copy, transfer"},
		{sl.PermAll, "copy, modify, transfer, move"},
	} {
		if got := permWords(c.mask); got != c.want {
			t.Errorf("permWords(%#x) = %q, want %q", c.mask, got, c.want)
		}
	}
}
