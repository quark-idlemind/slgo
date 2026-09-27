package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
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

// TestAPlaceGivenUpOnDeletesWhatItRezzed: a place cancelled while it
// waits, whose object turns up all the same, deletes that object into
// the trash rather than leaving it standing with nobody holding its id.
func TestAPlaceGivenUpOnDeletesWhatItRezzed(t *testing.T) {
	x := newTestShell(t)
	placed := msg.MustParseUUID("f3a47e57-7e57-c0de-be8b-000000000031")
	x.grid.AnswerDeletes(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	x.grid.mu.Lock()
	answer := x.grid.onSend
	x.grid.onSend = func(m msg.Message) {
		answer(m)
		if _, ok := m.(*msg.RezObject); !ok {
			return
		}
		// Cancelled as the rez goes out, and described too late for
		// the look that follows, so only the last look finds it.
		cancel()
		x.grid.mu.Lock()
		x.grid.later = []*sl.Seen{{
			Object: sl.Object{ID: placed, Local: 31, Name: "a lamp"},
			PCode:  9, Owner: testMe, Position: msg.Vector3{X: 129, Y: 128, Z: 25},
		}}
		x.grid.laterAt = x.grid.objectsCalls + 2
		x.grid.mu.Unlock()
	}
	x.grid.mu.Unlock()

	if err := x.Do(ctx, `place "Objects/a lamp"`); !errors.Is(err, context.Canceled) {
		t.Fatalf("a place given up on returned %v", err)
	}
	var deleted []msg.DeRezObject
	for _, d := range sentOfShell[*msg.DeRezObject](x) {
		deleted = append(deleted, *d)
	}
	if len(deleted) != 1 || deleted[0].ObjectData[0].ObjectLocalID != 31 ||
		deleted[0].AgentBlock.DestinationID != testTrash {
		t.Errorf("deleted %+v, want the placed prim, 31, into the trash", deleted)
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

// TestPermsSaysWhatTheMaskNowAllows.
//
// Nothing answers a permission change, so perms reads the mask back and
// prints what it now allows.  The permission rules adjust a request
// rather than refuse it -- a next owner who may not copy may always
// transfer, and everyone is never given modify -- so that is what is
// printed, and not the letters that were typed.
func TestPermsSaysWhatTheMaskNowAllows(t *testing.T) {
	x := newTestShell(t)
	standing(x, aPrim(aChair, 11, "lantern", 0))
	x.grid.AnswerPermissions(t, func(who uint8, mask uint32) uint32 {
		switch who {
		case sl.WhoEveryone:
			mask &^= sl.PermModify
		case sl.WhoNextOwner:
			if mask&sl.PermCopy == 0 {
				mask |= sl.PermTransfer
			}
		}
		return mask
	})

	if got := x.do(t, "perms --next m lantern"); got != "the next owner may now modify, transfer\n" {
		t.Errorf("perms --next m printed %q", got)
	}
	if got := x.do(t, "perms --everyone cm lantern"); got != "everyone may now copy\n" {
		t.Errorf("perms --everyone cm printed %q", got)
	}
}

// TestPermsSaysWhenAChangeDidNotLand: a mask that never reads as the
// rules make what was sent is an error saying what it allows, and no
// line claims otherwise.  What was confirmed before it is still said.
func TestPermsSaysWhenAChangeDidNotLand(t *testing.T) {
	t.Parallel()
	x := newTestShell(t)
	shortReadBacks(x)
	standing(x, aPrim(aChair, 11, "lantern", 0))
	x.grid.AnswerPermissions(t, func(who uint8, mask uint32) uint32 {
		if who == sl.WhoGroup {
			mask &^= sl.PermCopy
		}
		return mask
	})

	got := x.do(t, "perms --owner all --group cm lantern")
	if !strings.Contains(got, "the owner may now copy, modify, transfer, move") {
		t.Errorf("perms did not say what it confirmed:\n%s", got)
	}
	if strings.Contains(got, "the group may now") {
		t.Errorf("perms claimed a change that did not land:\n%s", got)
	}
	if !strings.Contains(got, "it allows modify") {
		t.Errorf("perms did not say what the group may do instead:\n%s", got)
	}
}
