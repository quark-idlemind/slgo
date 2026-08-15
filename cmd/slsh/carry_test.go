package main

import (
	"testing"

	"github.com/quark-idlemind/slgo/sl"
)

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
