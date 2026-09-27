package main

// Sitting down and getting up, over a grid that is not there.
//
// What is worth pinning here is the pair of things a sit answers with.
// The seat is one of them and the position is the other, because a sit
// carries the avatar to whatever it sat on -- so the fake moves it,
// and every picture below shows the move.  The rest is the difference
// between the three answers a sit can have: seated, refused in the
// grid's own words, and nothing at all, which is not a refusal and
// must not read like one.

import (
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// myLocal is the local id the fake region has given this avatar.  It is
// what a seat is hung off, and it arrives in the same update that says
// what the seat is.
const myLocal = 42

// onTheBox is where the avatar ends up once it has been seated, which
// is not where the fake stands it to begin with.  A sit really does
// carry the avatar to the seat: see doc/history/sit.md.
var onTheBox = msg.Vector3{X: 135, Y: 72, Z: 2001}

// sittingShell is a shell over a region with one thing in it to sit on,
// with the fake answering sits and stands the way a simulator does.
func sittingShell(t *testing.T) *testShell {
	t.Helper()
	x := newTestShell(t)
	x.grid.objects = []*sl.Seen{
		{Object: sl.Object{ID: testLamp, Local: 1, Name: "box"}, PCode: 9, Position: onTheBox},
	}

	// The sit MOVES the avatar, which is the whole reason these
	// commands print a position at all, so the fake carries it over to
	// the box rather than leaving it where it was standing.  This hook
	// is set BEFORE AnswerPosture, which chains onto whatever it finds
	// instead of replacing it.
	x.grid.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.AgentRequestSit); !ok {
			return
		}
		x.grid.mu.Lock()
		defer x.grid.mu.Unlock()
		x.grid.presence.Position = onTheBox
	}
	x.grid.AnswerPosture(t, myLocal)
	return x
}

// TestSittingOnTheGroundNamesNothingAndPutsNoMessageOnTheWire.
//
// The bare command is the ground because the two sits are two
// mechanisms and the argument is the only thing that tells them apart.
// There is no message for a ground sit at all: it is a control flag,
// which is why the wire stays empty and the flag is what to look at.
func TestSittingOnTheGroundNamesNothingAndPutsNoMessageOnTheWire(t *testing.T) {
	x := sittingShell(t)

	want := "sat on the ground\nTest Region at 128, 128, 25\n"
	if got := x.do(t, "sit"); got != want {
		t.Errorf("sit printed %q, want %q", got, want)
	}
	if sent := x.grid.Sent(); len(sent) != 0 {
		t.Errorf("a ground sit put %d messages on the wire; there is no message for one", len(sent))
	}
	flags := x.grid.Controls()
	if len(flags) == 0 || flags[0]&agent.ControlSitOnGround == 0 {
		t.Errorf("the flags asked for were %v, want the ground sit among them", flags)
	}
}

// TestSittingOnAnObjectSaysWhatItSatOnAndWhereThatLeftTheAvatar.
//
// Both halves are the answer.  A sit carries the avatar up to about ten
// metres, so a command that named the seat and stopped would leave
// somebody holding a position that is now several metres wrong -- and
// the position printed is read back from the session, which is what
// makes "where" afterwards agree with it rather than merely come close.
func TestSittingOnAnObjectSaysWhatItSatOnAndWhereThatLeftTheAvatar(t *testing.T) {
	x := sittingShell(t)

	want := "sat on \"box\" " + testLamp.String() + " (local 1)\nTest Region at 135, 72, 2001\n"
	if got := x.do(t, "sit box"); got != want {
		t.Errorf("sit printed %q, want %q", got, want)
	}
	// And "where" afterwards agrees with both halves.  The position is
	// the one sit printed, and the seat is named in the same shape,
	// because a seated avatar's coordinates are the seat's doing and a
	// line saying so is what explains them.
	got := x.do(t, "where")
	if !strings.HasPrefix(got, "Test Region at 135, 72, 2001\n") {
		t.Errorf("where after a sit printed %q, want the position sit printed", got)
	}
	if want := "sitting on \"box\" " + testLamp.String() + " (local 1)\n"; !strings.Contains(got, want) {
		t.Errorf("where after a sit printed %q, want a line %q", got, want)
	}

	// One message, and it is the request itself.  The pair a viewer
	// sends is AgentRequestSit and then AgentSit; the second does
	// nothing, and the avatar was seated six seconds before it in the
	// run that measured this, so nothing here sends it.
	sent := x.grid.Sent()
	if len(sent) != 1 {
		t.Fatalf("%d messages went out, want the one request: %v", len(sent), sent)
	}
	r, ok := sent[0].(*msg.AgentRequestSit)
	if !ok {
		t.Fatalf("the sit went out as %T", sent[0])
	}
	if r.TargetObject.TargetID != testLamp {
		t.Errorf("the request names %s, want the box %s", r.TargetObject.TargetID, testLamp)
	}
	if flags := x.grid.Controls(); len(flags) != 0 {
		t.Errorf("an object sit asked for the control flags %v; it is a message, not a flag", flags)
	}
}

// TestSittingOnAKeyNamesTheSameThingAsSittingOnItsName, since the
// object is resolved the way touch and take resolve one and a key is
// taken as itself.
func TestSittingOnAKeyNamesTheSameThingAsSittingOnItsName(t *testing.T) {
	x := sittingShell(t)

	want := "sat on \"box\" " + testLamp.String() + " (local 1)\nTest Region at 135, 72, 2001\n"
	if got := x.do(t, "sit "+testLamp.String()); got != want {
		t.Errorf("sit by key printed %q, want %q", got, want)
	}
}

// TestAWordThatNamesTwoObjectsIsRefusedRatherThanGuessedAt.
//
// The same refusal touch and take give, and it matters more here: the
// wrong guess does not click the wrong thing, it moves the avatar to
// it.  Both keys are printed, since naming one by key is what somebody
// has to do next, and nothing goes out.
func TestAWordThatNamesTwoObjectsIsRefusedRatherThanGuessedAt(t *testing.T) {
	x := sittingShell(t)
	x.grid.objects = append(x.grid.objects, &sl.Seen{
		Object: sl.Object{ID: testNote, Local: 2, Name: "box"}, PCode: 9,
	})

	got := x.do(t, "sit box")
	for _, want := range []string{
		"2 objects are called \"box\"", testLamp.String(), testNote.String(),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the refusal should mention %q:\n%s", want, got)
		}
	}
	if sent := x.grid.Sent(); len(sent) != 0 {
		t.Errorf("%d messages went out for a sit that was never resolved", len(sent))
	}
}

// TestARefusedSitPrintsTheGridsOwnWords.
//
// The words are the only part of a refusal anybody can act on and they
// are not always accurate -- a key that is no object at all is refused
// with "not in the same region as you" -- so they are repeated rather
// than interpreted.  The alert used here is the one Agni sent for an
// object eleven metres off.
func TestARefusedSitPrintsTheGridsOwnWords(t *testing.T) {
	x := newTestShell(t)
	x.grid.objects = []*sl.Seen{
		{Object: sl.Object{ID: testLamp, Local: 1, Name: "box"}, PCode: 9},
	}
	x.grid.AnswerSitRefused(t, "No room to sit here, try another spot.")

	want := "slsh: sit: sl: the simulator refused the sit: " +
		"\"No room to sit here, try another spot.\"\n"
	if got := x.do(t, "sit box"); got != want {
		t.Errorf("a refused sit printed %q, want %q", got, want)
	}
}

// TestASitNothingAnsweredReadsAsSilenceAndNotAsARefusal.
//
// A sit is answered one way or the other in about a tenth of a second,
// so nothing arriving means the request may have taken effect unseen.
// That is a third outcome and not a no, and a shell that reported it in
// the words of a refusal would be inventing a decision the grid never
// made.
func TestASitNothingAnsweredReadsAsSilenceAndNotAsARefusal(t *testing.T) {
	x := newTestShell(t)
	x.grid.objects = []*sl.Seen{
		{Object: sl.Object{ID: testLamp, Local: 1, Name: "box"}, PCode: 9},
	}

	got := x.do(t, "sit --wait 1 box")
	for _, want := range []string{
		"timed out", "neither seated this avatar nor refused",
		"this is not a refusal and the sit may yet have taken",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("silence should say %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "refused the sit") {
		t.Errorf("silence must not read as the simulator refusing:\n%s", got)
	}
}

// TestStandingUpSaysWhereThatLeftTheAvatar.
//
// Which is not where it was before it sat down: standing up does not
// undo the journey the sit made, and the measured case left the avatar
// six metres from where it started.  So the position is printed for the
// same reason sit prints it.
//
// unsit is the same command under the other spelling, and asking twice
// is not an error: a stand with nothing to stand up from sends the flag
// anyway, because what this session believes may be out of date.
func TestStandingUpSaysWhereThatLeftTheAvatar(t *testing.T) {
	x := sittingShell(t)
	x.do(t, "sit box")

	want := "stood up\nTest Region at 135, 72, 2001\n"
	if got := x.do(t, "stand"); got != want {
		t.Errorf("stand printed %q, want %q", got, want)
	}
	flags := x.grid.Controls()
	if len(flags) == 0 || flags[0]&agent.ControlStandUp == 0 {
		t.Errorf("the flags asked for were %v, want the stand among them", flags)
	}

	if got := x.do(t, "unsit"); got != want {
		t.Errorf("unsit printed %q, want the same as stand, %q", got, want)
	}
}

// TestStandTakesNothingAndSitTakesOneThing, since the shell quotes a
// name with a space in it and a second word is somebody meaning
// something else.
func TestStandTakesNothingAndSitTakesOneThing(t *testing.T) {
	x := sittingShell(t)

	if got := x.do(t, "sit box and the other one"); !strings.Contains(got, "usage: sit") {
		t.Errorf("sit with several names printed %q", got)
	}
	if got := x.do(t, "stand up"); !strings.Contains(got, "usage: stand") {
		t.Errorf("stand with an argument printed %q", got)
	}
	// The alias's refusal names stand, the name usageError is given;
	// its --help names what was typed, as exit's does.
	if got := x.do(t, "unsit now"); !strings.Contains(got, "usage: stand") {
		t.Errorf("unsit with an argument printed %q", got)
	}
	if sent := x.grid.Sent(); len(sent) != 0 {
		t.Errorf("%d messages went out for commands that were refused", len(sent))
	}
}
