package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// TestWearingSomethingReportsWhereItLanded.
//
// With no --at the request carries point 0, which means "wherever the
// object itself says", and nothing here knows where that is until the
// region answers.  So the line printed has to quote the point that came
// back rather than the one that was asked for: a wear that reported the
// request would say "point 0" for every attachment on the avatar.
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
	// The value under test: 0 is what the viewer sends for "wherever the
	// object says", and anything else here would put the thing somewhere
	// its maker did not choose.
	if m.ObjectData.AttachmentPt != 0 {
		t.Errorf("wear with no --at asked for point %d, want 0", m.ObjectData.AttachmentPt)
	}
}

// TestWearingOnANamedPointSendsThatPoint: a person reading a point off
// the viewer's menu has a name and not a number, and the name is the
// whole of what they can type.
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
		if m.ObjectData.AttachmentPt != c.want {
			t.Errorf("wear %s asked for point %d, want %d", c.at, m.ObjectData.AttachmentPt, c.want)
		}
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
// something else entirely.
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
	if got := x.do(t, "wear --help"); !strings.Contains(got, "--at") {
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

// TestDetachingANameWornTwiceAsksWhichOne: two items of one name can
// both be on, and taking off whichever came back first would be a coin
// toss the person cannot see being flipped.
func TestDetachingANameWornTwiceAsksWhichOne(t *testing.T) {
	x := newTestShell(t)
	other := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000a1")
	x.grid.mu.Lock()
	objs := x.grid.inv.Dirs[0]
	objs.Items = append(objs.Items, &invItem{ID: other, Name: "a lamp", Type: int(sl.AssetObject)})
	x.grid.mu.Unlock()

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
