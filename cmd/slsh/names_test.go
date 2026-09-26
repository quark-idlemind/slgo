package main

// A name in inventory, over a grid that is not there: matched exactly,
// in the case it has, and meaning one thing or refused.  The rule is
// sl.PickNamed's and is tested there; this is that every command that
// takes a path keeps to it, reads included.

import (
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// TestEveryCommandRefusesANameThatMeansSeveral.
//
// Nothing that takes a path picks the first of two things of one name,
// whether it deletes, hands over, overwrites or only reads.  A read is
// not let off, because a person is not always there to look again:
// "cat notes > file" in a script would write the wrong notecard to disk
// and say nothing.  The refusal lists the ids, which name one each.
func TestEveryCommandRefusesANameThatMeansSeveral(t *testing.T) {
	x := aBoxHolding(t)
	x.setListed([]person{{ID: testSomebody, Name: "Some Body"}})
	stock(t, x, "dup", 2)
	note := serveItemWrite(t, x, "UpdateNotecardAgentInventory", savedOK)
	file := aFile(t, "notes.txt", "one\n")
	ids := []string{
		msg.UUID{0: 1, 15: 0xdd}.String(),
		msg.UUID{0: 2, 15: 0xdd}.String(),
	}

	for _, line := range []string{
		"cat dup",
		"get dup",
		"save " + file + " dup",
		"give 1 dup",
		"mv dup elsewhere",
		"cp dup another",
		"wear dup",
		"place dup",
		"drop Box1 dup",
	} {
		got := x.do(t, line)
		if !strings.Contains(got, `2 things here are called "dup"`) {
			t.Errorf("%q printed %q, want a refusal naming the two", line, got)
		}
		for _, id := range ids {
			if !strings.Contains(got, id) {
				t.Errorf("%q printed %q, without the id %s", line, got, id)
			}
		}
	}

	if _, wrote := note.seen(); wrote != "" {
		t.Errorf("a refused save wrote %q", wrote)
	}
	if got := sentOfShell[*msg.ImprovedInstantMessage](x); len(got) != 0 {
		t.Errorf("a refused give still offered something: %+v", got)
	}
	if got := strings.Count(x.do(t, "ls"), "/dup\n"); got != 2 {
		t.Errorf("after the refusals %d things are called dup, want the 2 there were", got)
	}

	// Either one by its id is one thing, and goes ahead.
	if got := x.do(t, "save "+file+" "+ids[1]); !strings.Contains(got, "dup: 4 bytes written") {
		t.Errorf("save by id printed %q", got)
	}
	if asked, _ := note.seen(); !strings.Contains(asked, ids[1]) {
		t.Errorf("save by id wrote to %q, want %s", asked, ids[1])
	}
}

// TestAnExactNameIsFoundBesideACaseVariant.
//
// The grid keeps "readme" and "README" as two names, so each is the one
// spelt that way: neither is a second meaning of the other, and neither
// is refused for the other being there.  A name spelt a third way is
// nothing, and the refusal offers what is there.
func TestAnExactNameIsFoundBesideACaseVariant(t *testing.T) {
	x := newTestShell(t)
	upper := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000aa")
	x.grid.mu.Lock()
	x.grid.inv.Items = append(x.grid.inv.Items, &invItem{
		ID: upper, Name: "README", Type: int(sl.AssetNotecard), Created: 1754000400,
	})
	x.grid.mu.Unlock()

	for name, id := range map[string]msg.UUID{"readme": testNote, "README": upper} {
		got := x.do(t, "ls -l "+name)
		if strings.Count(got, "\n") != 1 || !strings.Contains(got, id.String()) {
			t.Errorf("ls -l %s printed %q, want the one line for %s", name, got, id)
		}
	}

	// A write goes to the one spelt that way.
	note := serveItemWrite(t, x, "UpdateNotecardAgentInventory", savedOK)
	if got := x.do(t, "save "+aFile(t, "notes.txt", "one\n")+" README"); !strings.Contains(got, "README: 4 bytes written") {
		t.Errorf("save to README printed %q", got)
	}
	if asked, _ := note.seen(); !strings.Contains(asked, upper.String()) || strings.Contains(asked, testNote.String()) {
		t.Errorf("save to README wrote to %q, want %s", asked, upper)
	}

	got := x.do(t, "cat Readme")
	if !strings.Contains(got, `nothing called "Readme" here; did you mean "README" or "readme"?`) {
		t.Errorf("cat of a third spelling printed %q", got)
	}

	// So does a delete, which takes the one and leaves the other.
	if got := x.do(t, "rm README"); got != "" {
		t.Errorf("rm README printed %q", got)
	}
	if got := x.do(t, "ls"); !strings.Contains(got, "/readme\n") || strings.Contains(got, "/README\n") {
		t.Errorf("after rm README the root lists:\n%s", got)
	}

	// And a folder, which is walked by the same rule.
	if got := x.do(t, "cd objects"); !strings.Contains(got, `no folder "objects" here; did you mean "Objects"?`) {
		t.Errorf("cd objects printed %q", got)
	}
	if got := x.do(t, `ls "/objects/a lamp"`); !strings.Contains(got, `did you mean "Objects"?`) {
		t.Errorf("ls through a case variant of a folder printed %q", got)
	}
}

// TestTwoFoldersOfOneNameAreNeitherOfThem: a folder may be made twice
// under one name, and a path through that name cannot say which.  cd is
// refused with the ids, and ls lists the two as themselves, which is
// where the ids come from.
func TestTwoFoldersOfOneNameAreNeitherOfThem(t *testing.T) {
	x := newTestShell(t)
	first := msg.MustParseUUID("b6577e57-7e57-c0de-e464-a5adbba4c37c")
	second := msg.MustParseUUID("b68a7e57-7e57-c0de-7e5e-912ed2b463d5")
	x.grid.mu.Lock()
	x.grid.inv.Dirs = append(x.grid.inv.Dirs,
		&invDir{ID: first, Name: "Probe", Type: -1},
		&invDir{ID: second, Name: "Probe", Type: -1},
	)
	x.grid.mu.Unlock()

	got := x.do(t, "cd Probe")
	if !strings.Contains(got, `2 folders here are called "Probe"`) ||
		!strings.Contains(got, first.String()) || !strings.Contains(got, second.String()) {
		t.Errorf("cd into two folders of one name printed %q", got)
	}
	if got := x.do(t, "pwd"); strings.TrimSpace(got) != "/" {
		t.Errorf("a refused cd went somewhere: pwd says %q", got)
	}

	got = x.do(t, "ls -l Probe")
	if strings.Count(got, "\n") != 2 || !strings.Contains(got, first.String()) || !strings.Contains(got, second.String()) {
		t.Errorf("ls -l of two folders of one name printed %q", got)
	}
}
