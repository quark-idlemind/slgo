package main

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// TestWearingSomethingReportsWhereItLanded.
//
// With no --at the request names no point, which means "wherever the
// object itself says", and nothing here knows where that is until the
// region answers.  So the line printed has to quote the point that came
// back rather than the one that was asked for: a wear that reported the
// request would say "point 0" for every attachment on the avatar.
//
// Nothing was worn here, so the line is the bare one -- what a wear says
// when it displaced nothing is the sentence and no more.
func TestWearingSomethingReportsWhereItLanded(t *testing.T) {
	x := newTestShell(t)
	x.grid.AnswerAttach(t, testSomebody, 10, 5)

	if got, want := x.do(t, "wear Objects/a lamp"), "a lamp is worn on left hand\n"; got != want {
		t.Errorf("wear printed %q, want %q", got, want)
	}

	m, ok := lastAttach(x)
	if !ok {
		t.Fatal("wear sent no attach request")
	}
	if m.ObjectData.ItemID != testLamp {
		t.Errorf("wore item %s, want %s", m.ObjectData.ItemID, testLamp)
	}
}

// TestWearingAddsUnlessReplaceIsAskedFor.
//
// This is the byte the whole change is about.  A point and the add bit
// share one field, so what "add, wherever the object itself says" looks
// like on the wire is 0x80 alone and nothing else -- and the old default,
// which sent 0, is what took auto 11 off an occupied HUD point on Agni
// without anybody saying so.  --replace is that behaviour, kept and
// named, and it must still send the point bare or it would not replace.
//
// Asserted here rather than in the test above because the two forms are
// only worth reading side by side: the difference between them is one
// bit and the whole of what a person gets.
func TestWearingAddsUnlessReplaceIsAskedFor(t *testing.T) {
	for _, c := range []struct {
		line string
		want uint8
	}{
		{"wear Objects/a lamp", sl.AttachAdd},
		{"wear --at chest Objects/a lamp", 1 | sl.AttachAdd},
		{"wear --replace Objects/a lamp", 0},
		{"wear --replace --at chest Objects/a lamp", 1},
	} {
		x := newTestShell(t)
		x.grid.AnswerAttach(t, testSomebody, 10, 1)
		if got := x.do(t, c.line); !strings.Contains(got, "is worn on") {
			t.Errorf("%s printed %q", c.line, got)
		}
		m, ok := lastAttach(x)
		if !ok {
			t.Fatalf("%s sent no attach request", c.line)
		}
		if m.ObjectData.AttachmentPt != c.want {
			t.Errorf("%s sent 0x%02x, want 0x%02x", c.line, m.ObjectData.AttachmentPt, c.want)
		}
	}
}

// TestWearingOnANamedPointSendsThatPoint: a person reading a point off
// the viewer's menu has a name and not a number, and the name is the
// whole of what they can type.
//
// The point arrives with the add bit over it, since these all wear the
// default way; what is under test here is the number, which is why the
// bit is taken back off rather than written into every want.
func TestWearingOnANamedPointSendsThatPoint(t *testing.T) {
	for _, c := range []struct {
		at   string
		want uint8
	}{
		{`--at "left hand"`, 5},
		{`--at "LEFT HAND"`, 5},
		{`--at "HUD top right"`, sl.HUDTopRight},
		// The viewer spells the HUD points the American way, so a person
		// copying one off its menu should not be told there is no such
		// point.
		{`--at "HUD centre 1"`, sl.HUDCenter1},
		{`--at "HUD center 1"`, sl.HUDCenter1},
		{"--at 5", 5},
	} {
		x := newTestShell(t)
		x.grid.AnswerAttach(t, testSomebody, 10, int(c.want))
		if got := x.do(t, "wear "+c.at+" Objects/a lamp"); !strings.Contains(got, "is worn on") {
			t.Errorf("wear %s printed %q", c.at, got)
		}
		m, ok := lastAttach(x)
		if !ok {
			t.Fatalf("wear %s sent no attach request", c.at)
		}
		if got := m.ObjectData.AttachmentPt &^ sl.AttachAdd; got != c.want {
			t.Errorf("wear %s asked for point %d, want %d", c.at, got, c.want)
		}
	}
}

// TestZeroIsAPointAndTheRefusalSaysWhichNumbersAre: zero is a value and
// not a missing one -- it is what asks for the point the object itself
// carries, which is what wearing from the viewer's menu asks for -- and
// attachPointArg takes it.  The refusal it prints for a number out of
// range used to say the points "run from 1 to 127", which names a range
// narrower than the one the function enforces and tells somebody who
// typed 0 deliberately that what they typed is not a point at all.
//
// So both halves are checked here against the function rather than
// against the sentence: every number it takes, and the numbers the
// sentence names.
func TestZeroIsAPointAndTheRefusalSaysWhichNumbersAre(t *testing.T) {
	if got, err := attachPointArg("0"); err != nil || got != attachWhereItSays {
		t.Errorf("--at 0 came out as (%d, %v), want %d and no error",
			got, err, attachWhereItSays)
	}
	// An empty --at is the same request, since it is what leaving the
	// flag off asks for.
	if got, err := attachPointArg(""); err != nil || got != attachWhereItSays {
		t.Errorf("--at with nothing in it came out as (%d, %v), want %d and no error",
			got, err, attachWhereItSays)
	}
	// The range the refusal names, taken one number at a time: 0 for
	// what the object says and 1 to 127 for a point.
	for n := 0; n < sl.AttachAdd; n++ {
		if got, err := attachPointArg(strconv.Itoa(n)); err != nil || got != n {
			t.Fatalf("--at %d came out as (%d, %v), want %d and no error", n, got, err, n)
		}
	}

	// The first number that is not a point, and a negative one, which is
	// the other way to name a byte this field cannot carry.
	for _, bad := range []string{"128", "-1"} {
		_, err := attachPointArg(bad)
		if err == nil {
			t.Fatalf("--at %s was taken as a point", bad)
		}
		// The sentence has to name the whole of what is taken, zero
		// included, and say what zero asks for: somebody who typed it
		// on purpose is owed the answer that it worked, and somebody
		// who has just been refused is owed the number to type instead.
		for _, want := range []string{"1 to 127", "0 asks for wherever the object itself says"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("--at %s was refused with %q, which does not say %q", bad, err, want)
			}
		}
	}
}

// TestReplacingSaysWhatCameOff.
//
// The silent loss is what started this: on Agni, a wear onto an occupied
// HUD point took the attachment that was there off, and nothing -- not
// the command, not the region -- mentioned it.  --replace still does
// that, so the line has to say it.
func TestReplacingSaysWhatCameOff(t *testing.T) {
	hat := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000a2")
	hatWorn := msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-000000000002")

	x := newTestShell(t)
	addObjectItem(x, hat, "a hat")
	wearThings(x, &sl.Seen{Object: sl.Object{ID: testSomebody, Local: 10}, PCode: 9,
		AttachItem: testLamp, AttachPoint: 1})
	x.grid.AnswerAttach(t, hatWorn, 11, 1, testLamp)

	if got, want := x.do(t, "wear --replace Objects/a hat"), "a hat is worn on chest; a lamp came off\n"; got != want {
		t.Errorf("a replacing wear printed %q, want %q", got, want)
	}
	if m, ok := lastAttach(x); !ok || m.ObjectData.ItemID != hat {
		t.Errorf("the wrong item was worn: %v", m)
	}

	// An add says nothing about what came off, and the point of saying so
	// here is that the lamp DOES come off while it happens -- a script,
	// or somebody at a viewer.  An add displaces nothing, so whatever
	// else was going on at the time is not this command's to report.
	// Claiming it would be the original complaint told backwards.
	x = newTestShell(t)
	addObjectItem(x, hat, "a hat")
	wearThings(x, &sl.Seen{Object: sl.Object{ID: testSomebody, Local: 10}, PCode: 9,
		AttachItem: testLamp, AttachPoint: 1})
	x.grid.AnswerAttach(t, hatWorn, 11, 1, testLamp)

	if got, want := x.do(t, "wear Objects/a hat"), "a hat is worn on chest\n"; got != want {
		t.Errorf("an adding wear printed %q, want %q", got, want)
	}
}

// TestAReplaceNamesOnlyTheAttachmentThatWentOffThePoint.
//
// The command used to name everything that had been on the point, which
// agreed with the region for as long as a point held one thing -- and
// adding is what made a point hold two.  Measured on Agni, as holt,
// with auto 11 and auto 3 both on HUD bottom right: a --replace of auto
// 4 onto that point printed "auto 11 and auto 3 came off", and the point
// afterwards held auto 3 and auto 4.  A replace displaces one.
//
// So the clause is confirmed against what is worn afterwards, and this
// is the shape that tells the two apart: two attachments on the point,
// the region taking one of them off, and one name in the line.
func TestAReplaceNamesOnlyTheAttachmentThatWentOffThePoint(t *testing.T) {
	clock := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000a3")
	hat := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000a2")
	hatWorn := msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-000000000002")

	x := newTestShell(t)
	addObjectItem(x, clock, "a clock")
	addObjectItem(x, hat, "a hat")
	wearThings(x,
		&sl.Seen{Object: sl.Object{ID: testSomebody, Local: 10}, PCode: 9,
			AttachItem: testLamp, AttachPoint: sl.HUDBottomRight},
		&sl.Seen{Object: sl.Object{ID: testNote, Local: 11}, PCode: 9,
			AttachItem: clock, AttachPoint: sl.HUDBottomRight},
	)
	// The region takes the lamp off and leaves the clock, which is what
	// it was seen to do.
	x.grid.AnswerAttach(t, hatWorn, 12, sl.HUDBottomRight, testLamp)

	got := x.do(t, "wear --replace --at \"HUD bottom right\" Objects/a hat")
	want := "a hat is worn on HUD bottom right; a lamp came off\n"
	if got != want {
		t.Errorf("a replace over two attachments printed %q, want %q", got, want)
	}
	if strings.Contains(got, "a clock") {
		t.Errorf("the clock is still worn and was named as having come off: %q", got)
	}

	// The other half of "off the point": a replace reaches no further
	// than the point it lands on, so an attachment that stops being worn
	// somewhere else while this is going on is somebody else's doing --
	// a script, or a detach from a moment ago still settling -- and
	// naming it here would blame the wear for it.
	badge := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000a4")
	x = newTestShell(t)
	addObjectItem(x, badge, "a badge")
	addObjectItem(x, hat, "a hat")
	wearThings(x,
		&sl.Seen{Object: sl.Object{ID: testSomebody, Local: 10}, PCode: 9,
			AttachItem: testLamp, AttachPoint: sl.HUDBottomRight},
		&sl.Seen{Object: sl.Object{ID: testNote, Local: 11}, PCode: 9,
			AttachItem: badge, AttachPoint: 1},
	)
	x.grid.AnswerAttach(t, hatWorn, 12, sl.HUDBottomRight, testLamp, badge)

	got = x.do(t, "wear --replace --at \"HUD bottom right\" Objects/a hat")
	if got != want {
		t.Errorf("a replace printed %q, want %q -- the badge came off a different point", got, want)
	}
}

// TestAWearThatCannotBeCheckedAfterwardsStillReportsTheWear.
//
// The clause costs a read that happens after the attachment is on, so a
// grid that stops answering between the two has cost the report and not
// the wear.  Failing the command there would say the wear failed, which
// is the one thing that is known not to have happened.
func TestAWearThatCannotBeCheckedAfterwardsStillReportsTheWear(t *testing.T) {
	hat := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000a2")
	hatWorn := msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-000000000002")

	x := newTestShell(t)
	addObjectItem(x, hat, "a hat")
	wearThings(x, &sl.Seen{Object: sl.Object{ID: testSomebody, Local: 10}, PCode: 9,
		AttachItem: testLamp, AttachPoint: 1})
	x.grid.AnswerAttach(t, hatWorn, 11, 1, testLamp)

	// The failure begins as the request goes out, so the read before it
	// answers and the read after it does not.
	x.grid.mu.Lock()
	answer := x.grid.onSend
	x.grid.onSend = func(m msg.Message) {
		answer(m)
		if _, ok := m.(*msg.RezSingleAttachmentFromInv); !ok {
			return
		}
		x.grid.mu.Lock()
		defer x.grid.mu.Unlock()
		x.grid.objectsErr = errors.New("nobody is holding this session")
	}
	x.grid.mu.Unlock()

	if got, want := x.do(t, "wear --replace Objects/a hat"), "a hat is worn on chest\n"; got != want {
		t.Errorf("a wear that could not be checked printed %q, want %q", got, want)
	}
}

// TestWearingTheSameItemTwiceIsRefused.
//
// Adding makes it possible, and nothing downstream could undo it: an
// attachment is known by the item it came from, so two worn from one
// item are indistinguishable to detach, which finds both, refuses as
// ambiguous, and offers the item id as the tie-breaker -- the one field
// they share.  The refusal has to name a way on, or it is just a wall.
func TestWearingTheSameItemTwiceIsRefused(t *testing.T) {
	x := newTestShell(t)
	wearThings(x, &sl.Seen{Object: sl.Object{ID: testSomebody, Local: 10}, PCode: 9,
		AttachItem: testLamp, AttachPoint: 1})
	// The region takes the old copy off, which is what a replace of
	// something already worn does, and it is described as gone before the
	// one that took its place is described as on.
	x.grid.AnswerAttach(t, msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-000000000002"), 11, 1, testLamp)

	got := x.do(t, "wear Objects/a lamp")
	if !strings.Contains(got, "already worn on chest") {
		t.Errorf("wearing a worn item again printed %q, want a refusal saying where it is", got)
	}
	if !strings.Contains(got, "--replace") || !strings.Contains(got, "detach a lamp") {
		t.Errorf("the refusal should say what to do instead, got %q", got)
	}
	if _, ok := lastAttach(x); ok {
		t.Error("a refused wear still sent an attach request")
	}

	// --replace is one of the two ways out it names, so it has to work:
	// the item ends up worn once, which is a state detach can unpick.
	// The line is the bare one and is matched whole, because the thing
	// that stopped being worn here is the item that was just put on --
	// "a lamp is worn on chest; a lamp came off" is a sentence arguing
	// with itself, and no ordering of the region's descriptions should
	// be able to produce it.
	if got, want := x.do(t, "wear --replace Objects/a lamp"), "a lamp is worn on chest\n"; got != want {
		t.Errorf("wear --replace of a worn item printed %q, want %q", got, want)
	}
	if _, ok := lastAttach(x); !ok {
		t.Error("wear --replace of a worn item sent nothing")
	}
}

// TestAnAttachmentPointThatIsNotOneIsRefused.
//
// Guessing would be the worst answer available: an unrecognised name
// falling back to the default would put the thing somewhere the person
// did not ask for and report success, and finding it again means
// hunting through "worn" to take it off.  A number too big for the
// field is the same mistake with a different cause -- 0x80 is the bit
// that means "add rather than replace", so a point above it arrives as
// something else entirely.  That refusal survives the bit becoming the
// default: somebody typing a number means a point by it, and the bit is
// laid over what this returns rather than being something to type.
func TestAnAttachmentPointThatIsNotOneIsRefused(t *testing.T) {
	x := newTestShell(t)
	for _, at := range []string{"--at elbow", "--at 200", "--at 128", `--at "left hands"`} {
		got := x.do(t, "wear "+at+" Objects/a lamp")
		if !strings.Contains(got, "not an attachment point") {
			t.Errorf("wear %s printed %q, want a refusal", at, got)
		}
	}
	if got := x.grid.Sent(); len(got) != 0 {
		t.Errorf("a refused point still sent %d messages", len(got))
	}

	// The other ways of asking for nothing.
	if got := x.do(t, "wear"); !strings.Contains(got, "usage: wear") {
		t.Errorf("wear with no argument printed %q", got)
	}
	if got := x.do(t, "wear Objects"); !strings.Contains(got, "is a folder") {
		t.Errorf("wear of a folder printed %q", got)
	}
	// Both flags in the help, since --replace is now the only way to ask
	// for what this command used to do without being asked.
	got := x.do(t, "wear --help")
	if !strings.Contains(got, "--at") || !strings.Contains(got, "--replace") {
		t.Errorf("wear --help printed %q", got)
	}
}

// TestDetachingSomethingNotWornSaysSo.
//
// "no such thing" would be a lie dressed as an answer.  What is worn is
// what the region has described, and it describes an attachment when it
// goes on and at login and never again, so an avatar dressed before this
// session began can be wearing something nothing here has heard of.  The
// refusal has to say that, or the next thing the person does is conclude
// the attachment is not on.
func TestDetachingSomethingNotWornSaysSo(t *testing.T) {
	x := newTestShell(t)
	got := x.do(t, "detach a lamp")
	if !strings.Contains(got, `nothing worn is called "a lamp"`) {
		t.Errorf("detach of something not worn printed %q", got)
	}
	if !strings.Contains(got, "before this session started") {
		t.Errorf("detach should say why the list may be short, got %q", got)
	}
	if got := x.grid.Sent(); len(got) != 0 {
		t.Errorf("detach of something not worn still sent %d messages", len(got))
	}

	// A grid that cannot say what is worn is reported as itself rather
	// than as a name that was not found.
	x.grid.objectsErr = errors.New("nobody is holding this session")
	if got := x.do(t, "detach a lamp"); !strings.Contains(got, "nobody is holding this session") {
		t.Errorf("detach should report the failure, got %q", got)
	}
}

// TestDetachingSendsTheItemAndNotTheObject.
//
// A worn object is rezzed afresh with a new key every time it goes on
// and again at every login, so its key is worth nothing; the item it was
// worn from does not change, and is what DetachAttachmentIntoInv takes.
// Sending the object's key instead would be accepted and ignored.
func TestDetachingSendsTheItemAndNotTheObject(t *testing.T) {
	x := newTestShell(t)
	wearThings(x, &sl.Seen{Object: sl.Object{ID: testSomebody, Local: 10}, PCode: 9,
		AttachItem: testLamp, AttachPoint: 1})
	x.grid.AnswerDetach(0)

	if got, want := x.do(t, "detach a lamp"), "a lamp is no longer worn on chest\n"; got != want {
		t.Errorf("detach printed %q, want %q", got, want)
	}
	m, ok := lastDetach(x)
	if !ok {
		t.Fatal("detach sent no request")
	}
	if m.ObjectData.ItemID != testLamp {
		t.Errorf("detached %s, want the item %s", m.ObjectData.ItemID, testLamp)
	}
	if m.ObjectData.AgentID != testMe {
		t.Errorf("detached for %s, want this avatar", m.ObjectData.AgentID)
	}
}

// TestDetachingTakesEitherKeyOrAPath.
//
// Both keys are in the answer that had to be fetched anyway, so looking
// for either costs nothing and saves the person having to know which of
// the two columns of "worn -l" they are holding.  A path is allowed
// because that is what wear took, and only its last name is used: what
// is worn says nothing about which folder its item sits in.
func TestDetachingTakesEitherKeyOrAPath(t *testing.T) {
	for _, arg := range []string{
		testLamp.String(),     // the inventory item
		testSomebody.String(), // the worn object
		"Objects/a lamp",      // a path, of which only the leaf can matter
	} {
		x := newTestShell(t)
		wearThings(x, &sl.Seen{Object: sl.Object{ID: testSomebody, Local: 10}, PCode: 9,
			AttachItem: testLamp, AttachPoint: 1})
		x.grid.AnswerDetach(0)

		if got := x.do(t, "detach "+arg); !strings.Contains(got, "no longer worn on chest") {
			t.Errorf("detach %s printed %q", arg, got)
		}
		m, ok := lastDetach(x)
		if !ok {
			t.Fatalf("detach %s sent no request", arg)
		}
		if m.ObjectData.ItemID != testLamp {
			t.Errorf("detach %s detached %s", arg, m.ObjectData.ItemID)
		}
	}
}

// TestDetachingWaitsForTheRegionToAgreeItIsOff.
//
// Nothing replies to a detach, so a command that printed as soon as the
// request went out would be reporting that it had asked -- and on a live
// region the difference shows: a "worn" straight after such a detach
// still listed the attachment.  Saying a thing came off when it has not
// is worse than saying nothing, because there is no way to tell from the
// line which of the two happened.
func TestDetachingWaitsForTheRegionToAgreeItIsOff(t *testing.T) {
	const gone = 300 * time.Millisecond
	x := newTestShell(t)
	wearThings(x, &sl.Seen{Object: sl.Object{ID: testSomebody, Local: 10}, PCode: 9,
		AttachItem: testLamp, AttachPoint: 1})
	x.grid.AnswerDetach(gone)

	start := time.Now()
	got := x.do(t, "detach a lamp")
	took := time.Since(start)

	if !strings.Contains(got, "no longer worn on chest") {
		t.Errorf("detach printed %q", got)
	}
	// The attachment was still worn when the request went out and for a
	// while after, so an answer sooner than that is an answer given
	// before there was anything to answer from.
	if took < gone {
		t.Errorf("detach answered after %s, before the region had stopped listing it: it did not wait", took)
	}
}

// TestDetachingSaysWhenTheRegionNeverAgrees: a detach that is refused or
// lost looks exactly like one that is slow, and the difference cannot be
// had from here -- so the line has to say what IS known, which is that
// the request went out and nothing has confirmed it, rather than pick
// one of the two and sound certain.
func TestDetachingSaysWhenTheRegionNeverAgrees(t *testing.T) {
	x := newTestShell(t)
	wearThings(x, &sl.Seen{Object: sl.Object{ID: testSomebody, Local: 10}, PCode: 9,
		AttachItem: testLamp, AttachPoint: 1})

	// Nothing answers, which is the region ignoring the request.
	got := x.do(t, "detach -w 1 a lamp")
	if !strings.Contains(got, "still lists it as worn") {
		t.Errorf("a detach nothing agreed to printed %q", got)
	}
	if !strings.Contains(got, "the request went out") {
		t.Errorf("the timeout should say what is known, got %q", got)
	}
	if _, ok := lastDetach(x); !ok {
		t.Error("detach gave up without ever sending the request")
	}

	// A region that stops answering DURING the wait is reported as that,
	// rather than as an attachment that would not come off: they are
	// different things to go and look at.  The failure has to begin after
	// the request has gone, or the command never gets as far as sending
	// one.
	x.grid.mu.Lock()
	x.grid.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.DetachAttachmentIntoInv); !ok {
			return
		}
		x.grid.mu.Lock()
		defer x.grid.mu.Unlock()
		x.grid.objectsErr = errors.New("nobody is holding this session")
	}
	x.grid.mu.Unlock()
	if got := x.do(t, "detach -w 1 a lamp"); !strings.Contains(got, "nobody is holding this session") {
		t.Errorf("a detach that could not be checked printed %q", got)
	}
}

// TestADetachNobodyConfirmedStillSaysWhatWasLeftBehind: when the region
// never agrees and the link could not be taken out of the Current Outfit
// folder either, both are said.  The link is what puts the thing back on
// at the next login, and an error that carried only the wait would leave
// that to be found then.  Nor is the thing said to be no longer worn,
// which nothing confirmed.
func TestADetachNobodyConfirmedStillSaysWhatWasLeftBehind(t *testing.T) {
	x := newTestShell(t)
	wearThings(x, &sl.Seen{Object: sl.Object{ID: testSomebody, Local: 10}, PCode: 9,
		AttachItem: testLamp, AttachPoint: 1})

	// The region ignores the request, and inventory has stopped
	// answering by the time the link is to be taken out.
	x.grid.mu.Lock()
	x.grid.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.DetachAttachmentIntoInv); !ok {
			return
		}
		x.grid.mu.Lock()
		defer x.grid.mu.Unlock()
		x.grid.capErr = errors.New("the capability went away")
	}
	x.grid.mu.Unlock()

	got := x.do(t, "detach -w 1 a lamp")
	for _, want := range []string{
		"still lists it as worn",
		"a lamp: its link is still in the Current Outfit folder",
		"the capability went away",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("detach printed %q, want it to say %q", got, want)
		}
	}
	if strings.Contains(got, "no longer worn") {
		t.Errorf("detach printed %q, which says it came off though nothing confirmed it", got)
	}
}

// TestDetachingANameWornTwiceAsksWhichOne: two items of one name can
// both be on, and taking off whichever came back first would be a coin
// toss the person cannot see being flipped.
func TestDetachingANameWornTwiceAsksWhichOne(t *testing.T) {
	x := newTestShell(t)
	other := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000a1")
	addObjectItem(x, other, "a lamp")

	wearThings(x,
		&sl.Seen{Object: sl.Object{ID: testSomebody, Local: 10}, PCode: 9,
			AttachItem: testLamp, AttachPoint: 1},
		&sl.Seen{Object: sl.Object{ID: testNote, Local: 11}, PCode: 9,
			AttachItem: other, AttachPoint: sl.HUDTop},
	)

	got := x.do(t, "detach a lamp")
	if !strings.Contains(got, "chest") || !strings.Contains(got, "HUD top") {
		t.Errorf("detach of an ambiguous name should say where both are, got %q", got)
	}
	if !strings.Contains(got, "worn -l") {
		t.Errorf("detach should say how to tell them apart, got %q", got)
	}
	if got := x.grid.Sent(); len(got) != 0 {
		t.Errorf("an ambiguous detach still sent %d messages", len(got))
	}
}

// addObjectItem puts another object in the fake's Objects folder.
//
// The tests that need one need two things of it: that wear can find it
// by path, and that its name can be looked up from its item id, which is
// how both the displaced attachment and an ambiguous detach are named.
// Both come from the same inventory, so putting it there covers both.
func addObjectItem(x *testShell, id msg.UUID, name string) {
	addItemOfKind(x, id, name, sl.AssetObject)
}

// addItemOfKind is addObjectItem for something that is not an object.
func addItemOfKind(x *testShell, id msg.UUID, name string, kind sl.AssetType) {
	x.grid.mu.Lock()
	defer x.grid.mu.Unlock()
	objs := x.grid.inv.Dirs[0]
	objs.Items = append(objs.Items, &invItem{ID: id, Name: name, Type: int(kind)})
}

// addOutfitFolder puts a folder beside the others holding one link, and
// nothing else, to an item that lives elsewhere.
//
// Which is the whole shape of an outfit folder: it holds no items at
// all.  A link carries the same name as what it points at, so nothing
// in a listing of one distinguishes the two except the word "link" in
// the type column.
func addOutfitFolder(x *testShell, folder, link msg.UUID, name string, to msg.UUID) {
	addLinkFolder(x, folder, link, name, to, sl.AssetObject)
}

// addLinkFolder is addOutfitFolder for a link to something other than
// an object, since a link may point at any kind of item.
func addLinkFolder(x *testShell, folder, link msg.UUID, name string, to msg.UUID, kind sl.AssetType) {
	x.grid.mu.Lock()
	defer x.grid.mu.Unlock()
	x.grid.inv.Dirs = append(x.grid.inv.Dirs, &invDir{
		ID: folder, Name: "An outfit", Items: []*invItem{{
			ID: link, Name: name, Asset: to, IsLink: true,
			Type: int(sl.AssetLink), InvType: int(kind),
		}},
	})
}

// lastAttach and lastDetach are the request a command sent, out of
// everything on the wire: several calls go out on the way past -- the
// region is asked what is worn, and inventory is read -- and only one of
// them is the thing under test.
func lastAttach(x *testShell) (*msg.RezSingleAttachmentFromInv, bool) {
	var got *msg.RezSingleAttachmentFromInv
	for _, m := range x.grid.Sent() {
		if r, ok := m.(*msg.RezSingleAttachmentFromInv); ok {
			got = r
		}
	}
	return got, got != nil
}

func lastDetach(x *testShell) (*msg.DetachAttachmentIntoInv, bool) {
	var got *msg.DetachAttachmentIntoInv
	for _, m := range x.grid.Sent() {
		if d, ok := m.(*msg.DetachAttachmentIntoInv); ok {
			got = d
		}
	}
	return got, got != nil
}

// TestWearFollowsALink.
//
// An outfit folder holds links, so the path a person reads the name off
// is a link's path nearly every time -- and a link's id is not the id of
// the thing it names.  Measured on Agni: wearing an outfit folder's
// entry by its own id sent that id, the simulator said nothing whatever
// about it, and the command waited out its full timeout before
// reporting that the region had never agreed the thing was on.  Every
// clause of that sentence was true and none of it was the reason.
//
// The assertion is on what went out on the wire and not on the line
// printed, because the fake answers whatever id it is asked about and
// would therefore agree to wear a link quite happily.  Only the grid
// draws the distinction, so only the request can be checked here.
func TestWearFollowsALink(t *testing.T) {
	hat := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000a3")
	folder := msg.MustParseUUID("abf67e57-7e57-c0de-631e-0f50d36eee20")
	link := msg.MustParseUUID("e36c7e57-7e57-c0de-fa5c-bb297cefdd98")
	worn := msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-0000000000a3")

	x := newTestShell(t)
	addObjectItem(x, hat, "a hat")
	addOutfitFolder(x, folder, link, "a hat", hat)
	x.grid.AnswerAttach(t, worn, 12, 1)

	if got, want := x.do(t, "wear An outfit/a hat"), "a hat is worn on chest\n"; got != want {
		t.Errorf("wearing through a link printed %q, want %q", got, want)
	}
	m, ok := lastAttach(x)
	if !ok {
		t.Fatal("wearing through a link sent no attach request at all")
	}
	if m.ObjectData.ItemID == link {
		t.Fatal("wear sent the link's own id, which the grid has no object for")
	}
	if m.ObjectData.ItemID != hat {
		t.Errorf("wear sent %v, want the item the link points at, %v", m.ObjectData.ItemID, hat)
	}
}

// TestWearRefusesALinkItCannotFollow.
//
// A link outlives what it pointed at, so an outfit assembled years ago
// may name things that are no longer in inventory.  The refusal has to
// say that it was a link and name the id it could not find: without
// both, somebody is looking at a path that is plainly there in "ls"
// being called missing.
func TestWearRefusesALinkItCannotFollow(t *testing.T) {
	gone := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000a4")
	folder := msg.MustParseUUID("aded7e57-7e57-c0de-0124-2e70a7757394")
	link := msg.MustParseUUID("e4d07e57-7e57-c0de-3032-5424ad0dcf12")

	x := newTestShell(t)
	addOutfitFolder(x, folder, link, "a hat", gone)

	err := x.Do(context.Background(), "wear An outfit/a hat")
	if err == nil {
		t.Fatal("a link to nothing was worn")
	}
	for _, want := range []string{"is a link", gone.String()} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal was %q, which does not say %q", err, want)
		}
	}
	if _, ok := lastAttach(x); ok {
		t.Error("a link that could not be followed was sent to the grid anyway")
	}
}

// TestWearingAWearableThroughALinkWearsTheThing.
//
// Which is how it will actually be met: everything in an outfit folder
// is a link, so the clothing in one is a link to clothing.  The link is
// followed first, so what gets linked into the Current Outfit folder is
// the item at the far end and not the link that was named -- a link to
// a link would be a thing the folder could not resolve.
//
// This replaces a test that asserted a refusal.  Wearing a system
// wearable used to be impossible here and was refused rather than left
// to time out; it is now done, so the refusal is gone and what is
// checked is that the right id went into the folder.
func TestWearingAWearableThroughALinkWearsTheThing(t *testing.T) {
	shape := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000c2")
	folder := msg.MustParseUUID("b64f7e57-7e57-c0de-7b8c-0c3a6deb20ef")
	link := msg.MustParseUUID("e8587e57-7e57-c0de-2c70-2196c4f04635")

	x := newTestShell(t)
	wearableAt(x, shape, "a shape", sl.AssetBodypart, sl.WearableShape)
	addLinkFolder(x, folder, link, "a shape", shape, sl.AssetBodypart)

	if got, want := x.do(t, "wear An outfit/a shape"), "a shape is worn as shape\n"; got != want {
		t.Errorf("wearing a body part through a link printed %q, want %q", got, want)
	}
	// Never as an attachment: the simulator answers a request to
	// attach one of these with silence, so this would have waited out
	// its whole timeout and blamed the region.
	if _, ok := lastAttach(x); ok {
		t.Fatal("a body part went out as an attach request")
	}
	// The link in the outfit points at the item, not at the link that
	// was named.
	x.grid.mu.Lock()
	cof := findDirOfType(x.grid.inv, sl.FolderCurrentOutfit)
	var pointsAt msg.UUID
	if cof != nil && len(cof.Items) == 1 {
		pointsAt = cof.Items[0].Asset
	}
	x.grid.mu.Unlock()
	if pointsAt == link {
		t.Fatal("the outfit holds a link to a link")
	}
	if pointsAt != shape {
		t.Errorf("the outfit links to %v, want the item %v", pointsAt, shape)
	}
}
