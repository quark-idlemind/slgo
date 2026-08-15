package main

// Linking and unlinking over a grid that is not there.
//
// The two halves are not symmetrical and that is what these are mostly
// about: a link names the root first and a delink names the prims being
// freed, so the message each command puts on the wire is a different
// shape from the other, and getting either backwards would take apart
// something nobody asked about.

import (
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// The prims these tests build with: a chair of three, or three things
// standing on their own, depending on what each one sets up.
var (
	aChair = msg.MustParseUUID("24f67e57-7e57-c0de-57e0-6fb484e1a15e")
	aLeg   = msg.MustParseUUID("6ab17e57-7e57-c0de-6176-dacb69eb6ae7")
	aSeat  = msg.MustParseUUID("83857e57-7e57-c0de-08f7-d2cb69fa7777")
)

// aPrim is one object the region has described, named and with a parent
// or without one.  PCode 9 is a prim: an avatar would be skipped by
// everything that walks a linkset.
func aPrim(id msg.UUID, local uint32, name string, parent uint32) *sl.Seen {
	return &sl.Seen{
		Object: sl.Object{ID: id, Local: local, Name: name},
		PCode:  9,
		Parent: parent,
	}
}

// standing puts objects in the region, replacing whatever was there.
func standing(x *testShell, objs ...*sl.Seen) {
	x.grid.mu.Lock()
	defer x.grid.mu.Unlock()
	x.grid.objects = objs
}

// TestLinkingJoinsWhatItIsGivenWithTheFirstAsRoot.
//
// The order is the whole of the command's meaning: the root is what the
// object becomes, keeps its name and is where it stands, so a link that
// sent the arguments in any other order would build the same set of
// prims into a different object.
func TestLinkingJoinsWhatItIsGivenWithTheFirstAsRoot(t *testing.T) {
	x := newTestShell(t)
	standing(x,
		aPrim(aChair, 11, "chair", 0),
		aPrim(aLeg, 12, "leg", 0),
		aPrim(aSeat, 13, "seat", 0),
	)
	x.grid.AnswerLinking(t)

	got := x.do(t, "link chair leg seat")
	if !strings.Contains(got, "is one object of 3 prims") {
		t.Errorf("link printed %q", got)
	}
	if !strings.Contains(got, `"chair"`) {
		t.Errorf("link should report the object under the root's name, got %q", got)
	}

	m, ok := lastOf[*msg.ObjectLink](x)
	if !ok {
		t.Fatal("link sent no ObjectLink")
	}
	if len(m.ObjectData) != 3 {
		t.Fatalf("linked %+v, want three prims", m.ObjectData)
	}
	if m.ObjectData[0].ObjectLocalID != 11 {
		t.Errorf("linked with %d as the root, want the first named", m.ObjectData[0].ObjectLocalID)
	}
	if m.ObjectData[1].ObjectLocalID != 12 || m.ObjectData[2].ObjectLocalID != 13 {
		t.Errorf("linked %+v, want the children in the order they were named", m.ObjectData)
	}

	// The count is read back from the region rather than taken from how
	// many names were typed: putting one prim onto an object that is
	// already two makes an object of three, and "2 prims" would be the
	// wrong thing to tell somebody who is looking at it.
	x = newTestShell(t)
	standing(x,
		aPrim(aChair, 11, "chair", 0),
		aPrim(aSeat, 13, "seat", 11),
		aPrim(aLeg, 12, "leg", 0),
	)
	x.grid.AnswerLinking(t)
	if got := x.do(t, "link chair leg"); !strings.Contains(got, "is one object of 3 prims") {
		t.Errorf("link onto an object that was already two printed %q", got)
	}
}

// TestLinkNeedsARootAndSomethingToPutUnderIt.
//
// One name is the mistake worth catching: it is what somebody types
// meaning "link this to what I have selected", which is a viewer's idea
// and not one the shell has, and a command that took it would have to
// invent the other half.
func TestLinkNeedsARootAndSomethingToPutUnderIt(t *testing.T) {
	x := newTestShell(t)
	standing(x, aPrim(aChair, 11, "chair", 0), aPrim(aLeg, 12, "leg", 0))

	for _, line := range []string{"link", "link chair"} {
		got := x.do(t, line)
		if !strings.Contains(got, "name the root and then at least one thing") {
			t.Errorf("%q printed %q, want a refusal saying what it takes", line, got)
		}
	}

	// The same prim twice is a typo the simulator would take in
	// silence, since a prim cannot be linked to itself.
	if got := x.do(t, "link chair chair"); !strings.Contains(got, "named twice") {
		t.Errorf("link of one prim to itself printed %q", got)
	}
	if got := sentOfShell[*msg.ObjectLink](x); len(got) != 0 {
		t.Errorf("a refused link still sent %d link messages", len(got))
	}
	if got := x.do(t, "link --help"); !strings.Contains(got, "Usage:") {
		t.Errorf("link --help printed %q", got)
	}
}

// TestUnlinkingARootTakesTheWholeThingApart.
//
// A delink names the prims being FREED and not the root they are
// leaving (llselectmgr.cpp:5477), so the root's local id has no business
// in the message: what frees the set is naming every child of it.  The
// listing is the other half -- the pieces keep their own names, so the
// keys are what a person needs to go on naming them afterwards.
func TestUnlinkingARootTakesTheWholeThingApart(t *testing.T) {
	x := newTestShell(t)
	standing(x,
		aPrim(aChair, 11, "chair", 0),
		aPrim(aLeg, 12, "leg", 11),
		aPrim(aSeat, 13, "seat", 11),
	)
	x.grid.AnswerLinking(t)

	got := x.do(t, "unlink chair")
	if !strings.Contains(got, "came apart into 3 objects") {
		t.Errorf("unlink printed %q", got)
	}
	for _, want := range []string{aChair.String(), aLeg.String(), aSeat.String(), "leg", "seat"} {
		if !strings.Contains(got, want) {
			t.Errorf("unlink should list %s among the pieces, printed %q", want, got)
		}
	}

	m, ok := lastOf[*msg.ObjectDelink](x)
	if !ok {
		t.Fatal("unlink sent no ObjectDelink")
	}
	if len(m.ObjectData) != 2 {
		t.Fatalf("delinked %+v, want the two children", m.ObjectData)
	}
	for _, d := range m.ObjectData {
		if d.ObjectLocalID == 11 {
			t.Error("the delink named the root, which has no parent to lose")
		}
	}

	// Nothing else goes out.  The viewer promotes a PHYSICS_SHAPE_NONE
	// prim to a convex hull on the way past (llselectmgr.cpp:5458-5472)
	// and this deliberately does not; see sl.Unlink for why.  A message
	// appearing here is that decision being reversed without the
	// reasoning being revisited.
	var names []string
	for _, s := range x.grid.Sent() {
		names = append(names, s.MsgInfo().Name)
	}
	for _, n := range names {
		if n != "ObjectSelect" && n != "ObjectDelink" {
			t.Errorf("unlink sent %s as well; the wire was %s", n, strings.Join(names, ", "))
		}
	}
}

// TestUnlinkingOnePrimLeavesTheRestLinked: naming a child is the
// viewer's Edit Linked Parts, and the prim leaves on its own.  What is
// left is worth saying, because taking one prim out of a pair leaves
// something that is no longer a linkset at all.
func TestUnlinkingOnePrimLeavesTheRestLinked(t *testing.T) {
	x := newTestShell(t)
	standing(x,
		aPrim(aChair, 11, "chair", 0),
		aPrim(aLeg, 12, "leg", 11),
		aPrim(aSeat, 13, "seat", 11),
	)
	x.grid.AnswerLinking(t)

	got := x.do(t, "unlink leg")
	if !strings.Contains(got, "is out of") || !strings.Contains(got, "2 prims now") {
		t.Errorf("unlink of one prim printed %q, want what left and what is left", got)
	}

	m, ok := lastOf[*msg.ObjectDelink](x)
	if !ok {
		t.Fatal("unlink sent no ObjectDelink")
	}
	if len(m.ObjectData) != 1 || m.ObjectData[0].ObjectLocalID != 12 {
		t.Errorf("delinked %+v, want the one prim named", m.ObjectData)
	}

	// Taking the last child out is not one prim leaving a linkset, it
	// is the linkset ceasing to be one, and is reported as that: the
	// pieces are listed, because both of them are now things in their
	// own right that somebody may want to name.
	if got := x.do(t, "unlink seat"); !strings.Contains(got, "came apart into 2 objects") {
		t.Errorf("unlink of the last child printed %q", got)
	}
}

// TestUnlinkingSomethingThatIsNotLinkedSaysSo.
//
// "done" would be a lie and a bare error would be a puzzle: a person who
// typed this believes the thing is a linkset, and what they need to hear
// is that it is one prim.  Sending a delink for it would be worse -- the
// message would go out, nothing would answer, and the command would wait
// twenty seconds to report a timeout about a prim that was never linked.
func TestUnlinkingSomethingThatIsNotLinkedSaysSo(t *testing.T) {
	x := newTestShell(t)
	standing(x, aPrim(aChair, 11, "chair", 0))

	got := x.do(t, "unlink chair")
	if !strings.Contains(got, "not linked to anything") {
		t.Errorf("unlink of a single prim printed %q", got)
	}
	if got := sentOfShell[*msg.ObjectDelink](x); len(got) != 0 {
		t.Errorf("unlink of a single prim still sent %d delinks", len(got))
	}

	// A name for nothing is the region's answer rather than this
	// command's, and comes back as it stands.
	if got := x.do(t, "unlink footstool"); !strings.Contains(got, "nothing called \"footstool\"") {
		t.Errorf("unlink of a name that is not there printed %q", got)
	}
	if got := x.do(t, "unlink"); !strings.Contains(got, "usage: unlink") {
		t.Errorf("unlink with no argument printed %q", got)
	}
}

// TestUnlinkSaysWhenThePiecesCannotBeToldApartByName.
//
// A linkset answers to its root's name and the prims inside keep their
// own, which for anything built prim by prim is "Object" for all of
// them.  So an unlink can leave three things in the region answering to
// one word, and the next command that names one is refused as
// ambiguous.  The keys are in the listing for that reason, and the line
// saying so is printed only when there is a clash to warn about.
func TestUnlinkSaysWhenThePiecesCannotBeToldApartByName(t *testing.T) {
	x := newTestShell(t)
	standing(x,
		aPrim(aChair, 11, "chair", 0),
		aPrim(aLeg, 12, "Object", 11),
		aPrim(aSeat, 13, "Object", 11),
	)
	x.grid.AnswerLinking(t)

	got := x.do(t, "unlink chair")
	if !strings.Contains(got, `more than one of these is called "Object"`) {
		t.Errorf("unlink of a set whose pieces share a name printed %q", got)
	}
	if !strings.Contains(got, "name one by its key") {
		t.Errorf("the warning should say what to do instead, got %q", got)
	}

	// Nothing to warn about is nothing to say: the pieces of the chair
	// in the other tests are all named differently.
	x = newTestShell(t)
	standing(x,
		aPrim(aChair, 11, "chair", 0),
		aPrim(aLeg, 12, "leg", 11),
	)
	x.grid.AnswerLinking(t)
	if got := x.do(t, "unlink chair"); strings.Contains(got, "more than one") {
		t.Errorf("unlink warned about names that are all different: %q", got)
	}
}

// lastOf and sentOfShell are the messages a command sent, out of
// everything on the wire: resolving a name and taking a selection both
// go out on the way past, and only one of the messages is the request
// under test.
func lastOf[T msg.Message](x *testShell) (T, bool) {
	var got T
	var found bool
	for _, m := range x.grid.Sent() {
		if r, ok := m.(T); ok {
			got, found = r, true
		}
	}
	return got, found
}

func sentOfShell[T msg.Message](x *testShell) []T {
	var out []T
	for _, m := range x.grid.Sent() {
		if r, ok := m.(T); ok {
			out = append(out, r)
		}
	}
	return out
}
