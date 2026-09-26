package main

// Building from a file, and taking a failed build away again.

import (
	"context"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// TestAFailedBuildIsTakenAwayByItsRoots.
//
// A prim linked to the root goes when the root does, so only roots are
// sent, as the viewer sends a delete.  Sent after its root had gone, a
// child would be waited for and reported as not confirmed gone, which
// it is.
func TestAFailedBuildIsTakenAwayByItsRoots(t *testing.T) {
	x := newTestShell(t)
	standing(x, aPrim(aChair, 11, "chair", 0), aPrim(aLeg, 12, "leg", 11), aPrim(aSeat, 13, "seat", 0))
	x.grid.Relay(t, &msg.ObjectUpdate{ObjectData: []msg.ObjectUpdate_ObjectData{
		{FullID: aLeg, ID: 12, ParentID: 11},
	}})
	x.grid.AnswerDeletes(t)

	chair := &sl.Object{ID: aChair, Local: 11}
	b := &sl.Built{Root: chair, Parts: []*sl.Object{
		chair, {ID: aLeg, Local: 12}, {ID: aSeat, Local: 13},
	}}
	if left := removeBuilt(context.Background(), x.s, b); left != "" {
		t.Errorf("removeBuilt left %s", left)
	}
	var sent []uint32
	for _, d := range sentOfShell[*msg.DeRezObject](x) {
		sent = append(sent, d.ObjectData[0].ObjectLocalID)
	}
	if len(sent) != 2 || sent[0] != 11 || sent[1] != 13 {
		t.Errorf("deleted %v, want the root 11 and the prim on its own, 13", sent)
	}
}

// TestAFailedBuildSaysWhatItCouldNotConfirmGone: a region that never
// says the prim has gone leaves it perhaps still standing, and the
// command that built it has to say so.
func TestAFailedBuildSaysWhatItCouldNotConfirmGone(t *testing.T) {
	t.Parallel()
	x := newTestShell(t)
	standing(x, aPrim(aChair, 11, "chair", 0))

	chair := &sl.Object{ID: aChair, Local: 11}
	left := removeBuilt(context.Background(), x.s, &sl.Built{Root: chair, Parts: []*sl.Object{chair}})
	if !strings.Contains(left, "not confirmed") {
		t.Errorf("removeBuilt = %q, want it to say the delete was not confirmed", left)
	}
}
