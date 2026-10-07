package agent

// A set's order confirmed by a script in the object: ConfirmOrder takes
// the script's keys, by link number.
// Why: doc/objects.md#confirmed-by-the-objects-own-script

import (
	"errors"
	"reflect"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

// aSetOfFour is a root and three children listed in one packet: the store
// numbers them 1 to 4 as the root, A, B and C.
func aSetOfFour() *Objects {
	o := newObjects()
	packet(o, 1, one(linkRoot, 1, 0), one(linkA, 2, 1), one(linkB, 3, 1), one(linkC, 4, 1))
	return o
}

func TestAScriptThatAgreesConfirmsTheSet(t *testing.T) {
	t.Parallel()

	o := aSetOfFour()
	c, err := o.ConfirmOrder(linkRoot, []msg.UUID{linkRoot, linkA, linkB, linkC})
	if err != nil {
		t.Fatal(err)
	}
	if c.Corrected || len(c.Moved) != 0 || c.At.IsZero() {
		t.Errorf("confirmation %+v, want one that changed nothing", c)
	}
	expectNumbers(t, o, []int{1, 2, 3, 4}, linkRoot, linkA, linkB, linkC)
	for _, id := range []msg.UUID{linkRoot, linkA, linkB, linkC} {
		v, _ := o.Get(id)
		if !v.LinkKnown || v.LinkConfirmed == nil || v.LinkConfirmed.Corrected {
			t.Errorf("%s: known %v, confirmed %+v", id, v.LinkKnown, v.LinkConfirmed)
		}
	}
}

// A set the packets could not vouch for (a child came as an answer to a
// request) is known once the script has agreed, and says why it was not.
func TestAScriptMakesASetThePacketsLeftUnknownKnown(t *testing.T) {
	t.Parallel()

	o := newObjects()
	packet(o, 1, one(linkRoot, 1, 0), one(linkA, 2, 1))
	o.noted(DescRequested, 0, 3)
	packet(o, 2, one(linkB, 3, 1))
	expectKnown(t, o, []bool{false, false, false}, linkRoot, linkA, linkB)
	if _, err := o.ConfirmOrder(linkRoot, []msg.UUID{linkRoot, linkA, linkB}); err != nil {
		t.Fatal(err)
	}
	expectKnown(t, o, []bool{true, true, true}, linkRoot, linkA, linkB)
	if b, _ := o.Get(linkB); b.LinkNote != "" {
		t.Errorf("a confirmed set still says %q", b.LinkNote)
	}
}

func TestAScriptThatDisagreesIsTakenAndSaysWhichLinks(t *testing.T) {
	t.Parallel()

	o := aSetOfFour()
	c, err := o.ConfirmOrder(linkRoot, []msg.UUID{linkRoot, linkC, linkB, linkA})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Corrected || !reflect.DeepEqual(c.Moved, []int{2, 4}) {
		t.Errorf("confirmation %+v, want links 2 and 4 moved", c)
	}
	expectNumbers(t, o, []int{1, 4, 3, 2}, linkRoot, linkA, linkB, linkC)
	expectKnown(t, o, []bool{true, true, true, true}, linkRoot, linkA, linkB, linkC)
	set := o.Linkset(1)
	if len(set) != 4 || set[1].ID != linkC || set[1].LinkConfirmed == nil || !set[1].LinkConfirmed.Corrected {
		t.Errorf("Linkset after a correction: %v", set)
	}
	// A repeat of a description the store already has does not undo it.
	packet(o, 9, one(linkA, 2, 1))
	expectNumbers(t, o, []int{1, 4, 3, 2}, linkRoot, linkA, linkB, linkC)
}

func TestAnotherCircuitsKeysDoNotUndoACorrection(t *testing.T) {
	t.Parallel()

	o := aSetOfFour()
	if _, err := o.ConfirmOrder(linkRoot, []msg.UUID{linkRoot, linkB, linkA, linkC}); err != nil {
		t.Fatal(err)
	}
	packetOn(o, 2, 5, one(linkRoot, 1, 0), one(linkA, 2, 1), one(linkB, 3, 1), one(linkC, 4, 1))
	expectNumbers(t, o, []int{1, 3, 2, 4}, linkRoot, linkA, linkB, linkC)
	expectKnown(t, o, []bool{true, true, true, true}, linkRoot, linkA, linkB, linkC)
}

func TestALoneConfirmedPrimIsKnownAndAChildJoiningDropsIt(t *testing.T) {
	t.Parallel()

	o := newObjects()
	told(o, linkRoot, 1, 0)
	if _, err := o.ConfirmOrder(linkRoot, []msg.UUID{linkRoot}); err != nil {
		t.Fatal(err)
	}
	if r, _ := o.Get(linkRoot); r.LinkConfirmed == nil || r.LinkNumber != 0 {
		t.Errorf("lone prim: number %d, confirmed %+v", r.LinkNumber, r.LinkConfirmed)
	}
	packet(o, 3, one(linkA, 2, 1))
	if r, _ := o.Get(linkRoot); r.LinkConfirmed != nil {
		t.Errorf("a prim joined and the confirmation stayed: %+v", r.LinkConfirmed)
	}
}

func TestAPrimJoiningOrLeavingDropsTheConfirmation(t *testing.T) {
	t.Parallel()

	for name, change := range map[string]func(*Objects){
		"a child is described under the root":  func(o *Objects) { packet(o, 7, one(linkD, 5, 1)) },
		"a child is killed":                    func(o *Objects) { o.kill(3) },
		"a child is described under another":   func(o *Objects) { packet(o, 8, one(linkB, 3, 0)) },
		"the root goes, and with it the state": func(o *Objects) { o.kill(1) },
	} {
		o := aSetOfFour()
		if _, err := o.ConfirmOrder(linkRoot, []msg.UUID{linkRoot, linkA, linkB, linkC}); err != nil {
			t.Fatal(err)
		}
		change(o)
		if len(o.confirms) != 0 {
			t.Errorf("%s: confirmations left: %v", name, o.confirms)
		}
	}
}

func TestADroppedCorrectionLeavesTheSetUnknown(t *testing.T) {
	t.Parallel()

	o := aSetOfFour()
	if _, err := o.ConfirmOrder(linkRoot, []msg.UUID{linkRoot, linkC, linkB, linkA}); err != nil {
		t.Fatal(err)
	}
	packet(o, 7, one(linkD, 5, 1))
	if v, _ := o.Get(linkA); v.LinkKnown || v.LinkConfirmed != nil {
		t.Errorf("after a prim joined: known %v, confirmed %+v", v.LinkKnown, v.LinkConfirmed)
	}
}

func TestAnAgreedConfirmationDroppedFallsBackToThePackets(t *testing.T) {
	t.Parallel()

	o := aSetOfFour()
	if _, err := o.ConfirmOrder(linkRoot, []msg.UUID{linkRoot, linkA, linkB, linkC}); err != nil {
		t.Fatal(err)
	}
	o.kill(4)
	expectKnown(t, o, []bool{true, true, true}, linkRoot, linkA, linkB)
	if v, _ := o.Get(linkA); v.LinkConfirmed != nil {
		t.Errorf("confirmation after a kill: %+v", v.LinkConfirmed)
	}
}

func TestFlushForgetsConfirmations(t *testing.T) {
	t.Parallel()

	o := aSetOfFour()
	if _, err := o.ConfirmOrder(linkRoot, []msg.UUID{linkRoot, linkA, linkB, linkC}); err != nil {
		t.Fatal(err)
	}
	o.Flush()
	if len(o.confirms) != 0 {
		t.Errorf("confirmations after a flush: %v", o.confirms)
	}
}

func TestAScriptThatDoesNotNameTheStoresPrimsChangesNothing(t *testing.T) {
	t.Parallel()

	for name, keys := range map[string][]msg.UUID{
		"none":                  nil,
		"the root is not first": {linkA, linkRoot, linkB, linkC},
		"one short":             {linkRoot, linkA, linkB},
		"one too many":          {linkRoot, linkA, linkB, linkC, linkD},
		"a stranger for a prim": {linkRoot, linkA, linkB, linkD},
		"a prim twice":          {linkRoot, linkA, linkA, linkC},
	} {
		o := aSetOfFour()
		_, err := o.ConfirmOrder(linkRoot, keys)
		if !errors.Is(err, ErrSetDiffers) {
			t.Errorf("%s: error %v, want ErrSetDiffers", name, err)
		}
		if len(o.confirms) != 0 || !reflect.DeepEqual(o.kids[1], []msg.UUID{linkA, linkB, linkC}) {
			t.Errorf("%s: the store changed: %v %v", name, o.confirms, o.kids[1])
		}
	}
	if _, err := aSetOfFour().ConfirmOrder(linkD, []msg.UUID{linkD}); !errors.Is(err, ErrNoSuchObject) {
		t.Errorf("an object not here: %v", err)
	}
}

func TestSittersAreNotInAConfirmedOrderAndStayAfterThePrims(t *testing.T) {
	t.Parallel()

	o := aSetOfFour()
	sitter := msg.MustParseUUID("8a3e7e57-7e57-c0de-b2d1-6f40c93e5a17")
	o.update(&msg.ObjectUpdate_ObjectData{ID: 9, ParentID: 1, FullID: sitter, PCode: pcodeAvatar}, msg.Vector3{}, 0)
	if _, err := o.ConfirmOrder(linkRoot, []msg.UUID{linkRoot, linkB, linkA, linkC}); err != nil {
		t.Fatal(err)
	}
	expectNumbers(t, o, []int{1, 3, 2, 4, 5}, linkRoot, linkA, linkB, linkC, sitter)
}
