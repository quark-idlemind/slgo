package sl

// What is worn, and how a program finds it again.
//
// An attachment is described when it goes on and at login, and never
// again.  A program that connects afterwards has therefore been told
// nothing about what the avatar is wearing, and the object it is
// looking for was rezzed afresh with an id nobody has ever seen before
// -- so the only durable handle on a worn thing is the inventory item
// it came from, carried in a NameValue line on the update that
// described it.  Everything here is about not losing that thread:
// reading the item id out of the update, keeping it, asking whoever was
// connected at the time, and taking something off and putting it back
// on when nobody heard.
//
// EnsureAttached is the reason the rest exists.  It costs a rez, a
// rename, a take and a wear once in the life of an account and a single
// lookup on every run after that, and the test for it drives the whole
// of that path against a fake region and a fake inventory server.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// attachState packs an attachment point the way an ObjectUpdate carries
// it, with the nibbles swapped: point 35 goes on the wire as 0x32.
func attachState(point int) uint8 {
	return uint8((point&0x0f)<<4 | (point>>4)&0x0f)
}

// wornUpdate is the update that describes a worn object: an id nobody
// has seen before, the point it is on, and the inventory item it came
// from in a NameValue line.
func wornUpdate(id msg.UUID, local uint32, item msg.UUID, point int) *msg.ObjectUpdate {
	return anUpdate(msg.ObjectUpdate_ObjectData{
		FullID:    id,
		ID:        local,
		State:     attachState(point),
		NameValue: []byte("AttachItemID STRING RW DS " + item.String() + "\n"),
	})
}

// TestAttachPointsAreNamed: a point is a number on the wire and nothing
// a person can read, and a program reporting "point 31" instead of "HUD
// centre 2" is one nobody can follow.
func TestAttachPointsAreNamed(t *testing.T) {
	cases := map[int]string{
		1:              "chest",
		2:              "head",
		30:             "right pec",
		HUDCenter2:     "HUD centre 2",
		HUDBottomLeft:  "HUD bottom left",
		HUDBottomRight: "HUD bottom right",
		55:             "right hind foot",
	}
	for point, want := range cases {
		if got := AttachPointName(point); got != want {
			t.Errorf("AttachPointName(%d) = %q, want %q", point, got, want)
		}
	}
	// The points go on being added to, so one this package has never
	// heard of is given as its number rather than as a name invented
	// here.
	if got := AttachPointName(200); got != "point 200" {
		t.Errorf("an unknown point = %q", got)
	}
}

// TestAPointCanBeFoundByTheNameItIsPrintedUnder: a person choosing
// where to put something has the name and not the number, and the two
// tables are one table -- a name that AttachPointName prints has to be
// one AttachPointNamed accepts, or the shell can print a point it will
// not then take back.
func TestAPointCanBeFoundByTheNameItIsPrintedUnder(t *testing.T) {
	for point, n := range attachPointNames {
		for _, name := range []string{n.name, n.viewer} {
			got, ok := AttachPointNamed(name)
			if !ok || got != point {
				t.Errorf("AttachPointNamed(%q) = %d, %v, want %d", name, got, ok, point)
			}
		}
	}

	// Typed by a person, so case and space are not the question being
	// asked; and the viewer spells the HUD points the American way.
	for _, name := range []string{"LEFT HAND", "  left hand  ", "Left Hand"} {
		if got, ok := AttachPointNamed(name); !ok || got != 5 {
			t.Errorf("AttachPointNamed(%q) = %d, %v", name, got, ok)
		}
	}
	if got, ok := AttachPointNamed("HUD center 1"); !ok || got != HUDCenter1 {
		t.Errorf("the American spelling of a HUD point = %d, %v", got, ok)
	}

	// A name nothing is called is not guessed at.  An attachment put on
	// the wrong point has to be hunted down and taken off again, which
	// is a worse answer than being told the name was not understood.
	if got, ok := AttachPointNamed("elbow"); ok {
		t.Errorf("AttachPointNamed(\"elbow\") = %d, want nothing", got)
	}
	if _, ok := AttachPointNamed(""); ok {
		t.Error("the empty name found a point")
	}
}

// TestTheViewersOwnNamesAreUnderstood: the names this package prints
// are not the viewer's -- they spell out its abbreviations, they put
// "HUD" in front of its bare "Top" and "Center", and three of them are
// different words altogether -- and a person choosing a point is
// reading it off the viewer's menu, not off this table.  So every name
// the viewer has for a point is accepted for that point, and typing
// "Skull" is not answered with there being no such thing.
//
// The names below were read out of the viewer's own table --
// indra/newview/character/avatar_lad.xml, one
// <attachment_point id="N" ... name="..."/> per point -- from a
// Phoenix-Firestorm checkout on 2026-08-23.  They are copied here as
// data on purpose: that file is not part of this repository, and a test
// that went looking for it would pass by not running on any machine
// without a viewer checked out beside this one.
func TestTheViewersOwnNamesAreUnderstood(t *testing.T) {
	viewer := map[int]string{
		1:  "Chest",
		2:  "Skull",
		3:  "Left Shoulder",
		4:  "Right Shoulder",
		5:  "Left Hand",
		6:  "Right Hand",
		7:  "Left Foot",
		8:  "Right Foot",
		9:  "Spine",
		10: "Pelvis",
		11: "Mouth",
		12: "Chin",
		13: "Left Ear",
		14: "Right Ear",
		15: "Left Eyeball",
		16: "Right Eyeball",
		17: "Nose",
		18: "R Upper Arm",
		19: "R Forearm",
		20: "L Upper Arm",
		21: "L Forearm",
		22: "Right Hip",
		23: "R Upper Leg",
		24: "R Lower Leg",
		25: "Left Hip",
		26: "L Upper Leg",
		27: "L Lower Leg",
		28: "Stomach",
		29: "Left Pec",
		30: "Right Pec",
		31: "Center 2",
		32: "Top Right",
		33: "Top",
		34: "Top Left",
		35: "Center",
		36: "Bottom Left",
		37: "Bottom",
		38: "Bottom Right",
		39: "Neck",
		40: "Avatar Center",
		41: "Left Ring Finger",
		42: "Right Ring Finger",
		43: "Tail Base",
		44: "Tail Tip",
		45: "Left Wing",
		46: "Right Wing",
		47: "Jaw",
		48: "Alt Left Ear",
		49: "Alt Right Ear",
		50: "Alt Left Eye",
		51: "Alt Right Eye",
		52: "Tongue",
		53: "Groin",
		54: "Left Hind Foot",
		55: "Right Hind Foot",
	}

	for point, name := range viewer {
		got, ok := AttachPointNamed(name)
		if !ok {
			t.Errorf("the viewer calls point %d %q and we do not know the name", point, name)
			continue
		}
		if got != point {
			t.Errorf("AttachPointNamed(%q) = %d, but the viewer means %d", name, got, point)
		}
	}

	// The printed name and the name taken back are the same name, for
	// every point the viewer has.  This is the property the shell leans
	// on: what worn prints is what wear --at will take.
	for point := range viewer {
		name := AttachPointName(point)
		if got, ok := AttachPointNamed(name); !ok || got != point {
			t.Errorf("point %d prints as %q, which comes back as %d, %v", point, name, got, ok)
		}
	}
}

// TestTheAttachPointArrivesWithItsNibblesSwapped: the State byte holds
// the point in swapped halves, and reading it straight gives a point
// number that names something else entirely.
func TestTheAttachPointArrivesWithItsNibblesSwapped(t *testing.T) {
	cases := []struct {
		state uint8
		point int
	}{
		{0x00, 0},
		{0x10, 1},  // chest
		{0x20, 2},  // head
		{0x32, 35}, // HUD centre 1, measured against the live grid
		{0xf0, 15},
		{0x0f, 240},
	}
	for _, c := range cases {
		if got := attachPoint(c.state); got != c.point {
			t.Errorf("attachPoint(%#02x) = %d, want %d", c.state, got, c.point)
		}
		if c.point < 256 && c.point > 0 {
			if got := attachState(c.point); attachPoint(got) != c.point {
				t.Errorf("packing point %d gave %#02x", c.point, got)
			}
		}
	}
}

// TestTheItemAnAttachmentCameFromIsInItsNameValues: this line is the
// only thing tying a worn object back to inventory, and the object gets
// a fresh id every time it goes on, so anything that misreads it loses
// the attachment for good.
func TestTheItemAnAttachmentCameFromIsInItsNameValues(t *testing.T) {
	item := msg.MustParseUUID("75f27e57-7e57-c0de-3e6e-cebd8a8d72d0")

	cases := []struct {
		name string
		nv   string
		want msg.UUID
	}{
		{"the line by itself", "AttachItemID STRING RW DS " + item.String(), item},
		{
			"among the others an avatar carries",
			"FirstName STRING RW SV Quark\nAttachItemID STRING RW DS " + item.String() +
				"\nAttachOffset VEC3 RW DS <0,0,0>",
			item,
		},
		{"nothing at all", "", msg.UUID{}},
		{"no such line", "FirstName STRING RW SV Quark", msg.UUID{}},
		{"the line with nothing after it", "AttachItemID", msg.UUID{}},
		{"an id that is not one", "AttachItemID STRING RW DS not-a-uuid", msg.UUID{}},
		// A zero id is the same as no id: it names nothing.
		{"a zero id", "AttachItemID STRING RW DS " + msg.UUID{}.String(), msg.UUID{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := attachItem(append([]byte(c.nv), 0))
			if ok != !c.want.IsZero() {
				t.Fatalf("attachItem = %s, %v", got, ok)
			}
			if got != c.want {
				t.Errorf("attachItem = %s, want %s", got, c.want)
			}
		})
	}
}

// TestAnAttachmentIsRememberedWhenItIsDescribed: the update that
// describes a worn object is the only time this session is told about
// it, so what is not taken from that update is not had at all.
func TestAnAttachmentIsRememberedWhenItIsDescribed(t *testing.T) {
	w, f := newFakeSession(t)
	item := msg.MustParseUUID("75f27e57-7e57-c0de-3e6e-cebd8a8d72d0")

	if len(w.Attachments()) != 0 {
		t.Error("a fresh session already knew what was worn")
	}
	if _, ok := w.WornFrom(item); ok {
		t.Error("something was worn before anything said so")
	}

	f.Relay(t, wornUpdate(thePrim, 0, item, HUDCenter1))
	// An object with no item id in it is not an attachment, and must
	// not turn up in the list as one.
	f.Relay(t, anUpdate(msg.ObjectUpdate_ObjectData{FullID: theOther, ID: 12}))

	all := w.Attachments()
	if len(all) != 1 {
		t.Fatalf("%d attachments, want 1", len(all))
	}
	a := all[0]
	if a.Object.ID != thePrim || a.Item != item || a.Point != HUDCenter1 {
		t.Errorf("attachment = %+v", a)
	}
	got, ok := w.WornFrom(item)
	if !ok || got != a {
		t.Errorf("WornFrom = %+v, %v", got, ok)
	}
}

// TestWearWaitsForTheSimulatorToSayItIsOn: attaching makes a brand new
// object, and other objects arrive the whole time, so the new one is
// found by the item it came from rather than by being new.
func TestWearWaitsForTheSimulatorToSayItIsOn(t *testing.T) {
	w, f := newFakeSession(t)
	it := anItem(theChild, "workbench")
	it.Desc = "a place to run scripts"
	it.Flags, it.GroupMask, it.EveryoneMask, it.NextOwnerMask = 1, 2, 3, 4

	// Something worn from this item before, which is a stale answer
	// once it is being put on again: the object id changes every time.
	f.Relay(t, wornUpdate(theOther, 3, it.ID, HUDTop))

	// A timeout of zero asks for the default, which is not reached
	// here.
	wait := aside(t, func() (*Attached, error) {
		return w.Wear(context.Background(), it, HUDCenter1, 0)
	})

	m := waitSent[*msg.RezSingleAttachmentFromInv](t, f)
	d := m.ObjectData
	if d.ItemID != it.ID || d.OwnerID != testAgentID {
		t.Errorf("wore %s for %s", d.ItemID, d.OwnerID)
	}
	if d.AttachmentPt != HUDCenter1 {
		t.Errorf("wore it on point %d", d.AttachmentPt)
	}
	if trimNul(d.Name) != "workbench" || trimNul(d.Description) != "a place to run scripts" {
		t.Errorf("wore %q %q", trimNul(d.Name), trimNul(d.Description))
	}
	if d.ItemFlags != 1 || d.GroupMask != 2 || d.EveryoneMask != 3 || d.NextOwnerMask != 4 {
		t.Errorf("object data = %+v", d)
	}

	f.Relay(t, wornUpdate(thePrim, 55, it.ID, HUDCenter1))

	a, err := wait()
	if err != nil {
		t.Fatalf("Wear: %v", err)
	}
	if a.Object.ID != thePrim || a.Object.Local != 55 || a.Point != HUDCenter1 {
		t.Errorf("wore %+v, want the object described after the request", a)
	}
}

// TestWearSaysWhenNothingWentOn: an attachment that did not go on and
// one nobody was told about look the same from here, and both have to
// be a failure -- a caller handed an object that is not worn puts
// scripts in it and hears nothing back.
func TestWearSaysWhenNothingWentOn(t *testing.T) {
	it := anItem(theChild, "workbench")

	t.Run("the request never went", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.FailSends(errors.New("the circuit is gone"))
		if _, err := w.Wear(context.Background(), it, HUDCenter1, time.Second); err == nil {
			t.Error("Wear reported something worn though nothing was sent")
		}
	})

	t.Run("nothing described it", func(t *testing.T) {
		w, _ := newFakeSession(t)
		_, err := w.Wear(context.Background(), it, HUDCenter1, 50*time.Millisecond)
		if !errors.Is(err, ErrTimeout) {
			t.Errorf("Wear = %v, want a timeout", err)
		}
	})
}

// TestTakeOffForgetsWhatWasWorn: the session's record of an attachment
// is what Worn answers from, so leaving it there after taking the thing
// off hands out a local id that no longer names anything.
func TestTakeOffForgetsWhatWasWorn(t *testing.T) {
	w, f := newFakeSession(t)
	item := msg.MustParseUUID("75f27e57-7e57-c0de-3e6e-cebd8a8d72d0")
	f.Relay(t, wornUpdate(thePrim, 4, item, HUDTop))
	relayKillOnDetach(t, f, 4)

	if err := w.TakeOff(context.Background(), item); err != nil {
		t.Fatalf("TakeOff: %v", err)
	}
	m := onlySent[*msg.DetachAttachmentIntoInv](t, f)
	if m.ObjectData.ItemID != item || m.ObjectData.AgentID != testAgentID {
		t.Errorf("detached %+v", m.ObjectData)
	}
	if _, ok := w.WornFrom(item); ok {
		t.Error("something taken off is still recorded as worn")
	}

	// A detach that never went leaves it on, and saying otherwise
	// would have the caller waiting to be told about a wear that
	// cannot happen while it is still attached.
	f.Relay(t, wornUpdate(thePrim, 4, item, HUDTop))
	f.onSend = nil
	f.FailSends(errors.New("the circuit is gone"))
	if err := w.TakeOff(context.Background(), item); err == nil {
		t.Error("TakeOff reported success though nothing was sent")
	}
	if _, ok := w.WornFrom(item); !ok {
		t.Error("a detach that was never sent was recorded as having happened")
	}
}

// relayKillOnDetach makes the simulator kill the attachment with this
// local id when it is told to detach, as the grid does.
func relayKillOnDetach(t *testing.T, f *fakeBackend, local uint32) {
	t.Helper()
	f.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.DetachAttachmentIntoInv); ok {
			f.Relay(t, &msg.KillObject{ObjectData: []msg.KillObject_ObjectData{{ID: local}}})
		}
	}
}

// TestTakeOffWaitsForTheKill: a delete sent while the grid is still
// carrying out the detach is sometimes refused, so TakeOff returns only
// once the attachment is killed.
// Why: doc/slsh.md#deleting-straight-after-a-take-off
func TestTakeOffWaitsForTheKill(t *testing.T) {
	item := msg.MustParseUUID("75f27e57-7e57-c0de-3e6e-cebd8a8d72d0")

	t.Run("not before the kill", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.Relay(t, wornUpdate(thePrim, 4, item, HUDTop))
		done := make(chan error, 1)
		go func() { done <- w.TakeOff(context.Background(), item) }()
		waitSent[*msg.DetachAttachmentIntoInv](t, f)
		select {
		case err := <-done:
			t.Fatalf("TakeOff returned (%v) before the attachment was killed", err)
		case <-time.After(250 * time.Millisecond): // more than one poll of await
		}
		if _, ok := w.WornFrom(item); ok {
			t.Error("still recorded as worn after the request went")
		}
		f.Relay(t, &msg.KillObject{ObjectData: []msg.KillObject_ObjectData{{ID: 4}}})
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("TakeOff: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("TakeOff did not return after the kill")
		}
	})

	t.Run("a kill from before it was described does not count", func(t *testing.T) {
		w, f := newFakeSession(t)
		w.SetOptions(Options{TakeOffTimeout: 300 * time.Millisecond})
		// An earlier object with local id 4 was killed; the update that
		// describes the attachment under the same number clears that.
		f.Relay(t, &msg.KillObject{ObjectData: []msg.KillObject_ObjectData{{ID: 4}}})
		f.Relay(t, wornUpdate(thePrim, 4, item, HUDTop))
		err := w.TakeOff(context.Background(), item)
		if !errors.Is(err, ErrTimeout) {
			t.Errorf("TakeOff = %v, want a timeout: the old kill was taken for this one", err)
		}
	})

	t.Run("already taken off by another client", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.Relay(t, wornUpdate(thePrim, 4, item, HUDTop))
		f.Relay(t, &msg.KillObject{ObjectData: []msg.KillObject_ObjectData{{ID: 4}}})
		start := time.Now()
		if err := w.TakeOff(context.Background(), item); err != nil {
			t.Fatalf("TakeOff of an attachment already gone = %v", err)
		}
		if took := time.Since(start); took > time.Second {
			t.Errorf("TakeOff waited %v for an attachment already gone", took)
		}
		if _, ok := w.WornFrom(item); ok {
			t.Error("it is still recorded as worn")
		}
	})

	t.Run("no kill", func(t *testing.T) {
		w, f := newFakeSession(t)
		w.SetOptions(Options{TakeOffTimeout: 300 * time.Millisecond})
		f.Relay(t, wornUpdate(thePrim, 4, item, HUDTop))
		err := w.TakeOff(context.Background(), item)
		if !errors.Is(err, ErrTimeout) {
			t.Errorf("TakeOff = %v, want a timeout", err)
		}
		onlySent[*msg.DetachAttachmentIntoInv](t, f)
		if _, ok := w.WornFrom(item); ok {
			t.Error("a detach that was sent is still recorded as worn")
		}
	})

	t.Run("never seen worn", func(t *testing.T) {
		w, f := newFakeSession(t)
		start := time.Now()
		if err := w.TakeOff(context.Background(), item); err != nil {
			t.Fatalf("TakeOff: %v", err)
		}
		if d := time.Since(start); d > 50*time.Millisecond {
			t.Errorf("TakeOff of an unknown item took %s", d)
		}
		onlySent[*msg.DetachAttachmentIntoInv](t, f)
	})
}

// TestWornObjectsAsksWhoeverWasConnected: what THIS session has been
// told is nothing at all for a program that started after the avatar
// logged in, so what is worn has to come from the backend, which was
// listening at the time.
func TestWornObjectsAsksWhoeverWasConnected(t *testing.T) {
	w, f := newFakeSession(t)
	item := msg.MustParseUUID("75f27e57-7e57-c0de-3e6e-cebd8a8d72d0")
	elsewhere := msg.MustParseUUID("87e97e57-7e57-c0de-0cfe-880b205c342b")
	f.objects = []*Seen{
		// This avatar, which is what says whose the attachment is: it
		// hangs off the wearer.
		{Object: Object{ID: testAgentID, Local: 4}, PCode: pcodeAvatar},
		{Object: Object{ID: thePrim, Local: 5}, Parent: 4, AttachItem: item, AttachPoint: HUDCenter1},
		// A prim standing in the region is not worn, whatever else is
		// known about it.
		{Object: Object{ID: theOther, Local: 6}, PCode: pcodePrim},
		// Somebody else's attachment.  The store is the region's and
		// may be shared with the avatars in it, so being worn is not
		// enough to be worn by us.
		{Object: Object{ID: elsewhere, Local: 8}, PCode: pcodeAvatar},
		{Object: Object{ID: msg.MustParseUUID("93907e57-7e57-c0de-de98-7237d586ade7"), Local: 9},
			Parent: 8, AttachItem: msg.MustParseUUID("94c37e57-7e57-c0de-f905-ec5fc2ffeaf7")},
	}

	worn, err := w.WornObjects(context.Background())
	if err != nil {
		t.Fatalf("WornObjects: %v", err)
	}
	if len(worn) != 1 {
		t.Fatalf("%d worn, want 1: %+v", len(worn), worn)
	}
	if worn[0].Object.ID != thePrim || worn[0].Item != item || worn[0].Point != HUDCenter1 {
		t.Errorf("worn = %+v", worn[0])
	}
	// The objects come back unnamed on purpose: a worn object does not
	// answer a request for its properties, so the name a person knows
	// it by is the inventory item's.
	if worn[0].Object.Name != "" {
		t.Errorf("a worn object came back named %q", worn[0].Object.Name)
	}

	f.objectsErr = errors.New("the daemon is not answering")
	if _, err := w.WornObjects(context.Background()); err == nil {
		t.Error("WornObjects reported nothing worn rather than a failure")
	}
}

// TestWornFromItemIsTheOnlyWayToFindTheSameAttachmentTwice: the object
// id changes every time it goes on and at every login, so the item is
// the only handle that survives between sessions.
func TestWornFromItemIsTheOnlyWayToFindTheSameAttachmentTwice(t *testing.T) {
	w, f := newFakeSession(t)
	item := msg.MustParseUUID("75f27e57-7e57-c0de-3e6e-cebd8a8d72d0")
	f.objects = []*Seen{
		{Object: Object{ID: theOther, Local: 6}},
		{Object: Object{ID: thePrim, Local: 5}, AttachItem: item, AttachPoint: HUDCenter1},
	}

	a, ok := w.WornFromItem(context.Background(), item)
	if !ok {
		t.Fatal("the worn object was not found by the item it came from")
	}
	if a.Object.ID != thePrim || a.Point != HUDCenter1 {
		t.Errorf("found %+v", a)
	}

	if _, ok := w.WornFromItem(context.Background(), theChild); ok {
		t.Error("an item nothing was worn from found something")
	}
	// Nobody is asked about nothing: the zero item is not a question.
	if _, ok := w.WornFromItem(context.Background(), msg.UUID{}); ok {
		t.Error("the zero item found something")
	}

	// A backend that cannot answer means not found, not a failure:
	// every caller of this is deciding whether to put something on,
	// and putting it on again is the safe answer.
	f.objectsErr = errors.New("the daemon is not answering")
	if _, ok := w.WornFromItem(context.Background(), item); ok {
		t.Error("a backend that answered nothing found something")
	}
}

// TestWornTakesItOffToBeToldAboutIt: an attachment put on before this
// program started has never been described to it, so its local id is
// unknown and nothing can be done with it.  Taking it off and putting
// it back on is the only way to be told.
func TestWornTakesItOffToBeToldAboutIt(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	it := anItem(theChild, "workbench")
	f.ServeInventory(t, func(msg.UUID) []*Item { return []*Item{it} })

	wait := aside(t, func() (*Attached, error) {
		return w.Worn(context.Background(), aFolder, "workbench", HUDCenter1)
	})

	off := waitSent[*msg.DetachAttachmentIntoInv](t, f)
	if off.ObjectData.ItemID != it.ID {
		t.Errorf("took off %s", off.ObjectData.ItemID)
	}
	// The wear comes ten seconds later, after the detach has settled:
	// attaching something already worn is not reliably an event, and
	// the event is the whole point.
	waitSent[*msg.RezSingleAttachmentFromInv](t, f)
	f.Relay(t, wornUpdate(thePrim, 55, it.ID, HUDCenter1))

	a, err := wait()
	if err != nil {
		t.Fatalf("Worn: %v", err)
	}
	if a.Object.ID != thePrim || a.Item != it.ID {
		t.Errorf("worn = %+v", a)
	}
}

// TestWornAnswersFromWhatItWasToldWhenItCan: the taking off and putting
// back on costs ten seconds, so it happens only when this session has
// never been told about the attachment.
func TestWornAnswersFromWhatItWasToldWhenItCan(t *testing.T) {
	w, f := newFakeSession(t)
	it := anItem(theChild, "workbench")
	f.ServeInventory(t, func(msg.UUID) []*Item { return []*Item{it} })
	f.Relay(t, wornUpdate(thePrim, 55, it.ID, HUDCenter1))

	a, err := w.Worn(context.Background(), aFolder, "workbench", HUDCenter1)
	if err != nil {
		t.Fatalf("Worn: %v", err)
	}
	if a.Object.ID != thePrim {
		t.Errorf("worn = %+v", a)
	}
	if got := f.Sent(); len(got) != 0 {
		t.Errorf("an attachment already known about was disturbed: %s", f.describe())
	}
}

// TestWornSaysWhatWentWrong: it is a folder read, a detach, a wait and
// a wear, and each of them fails differently.
func TestWornSaysWhatWentWrong(t *testing.T) {
	t.Run("no such item", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.ServeInventory(t, func(msg.UUID) []*Item { return nil })
		if _, err := w.Worn(context.Background(), aFolder, "workbench", HUDCenter1); err == nil {
			t.Error("Worn found an item that is not in the folder")
		}
	})

	t.Run("the detach never went", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.ServeInventory(t, func(msg.UUID) []*Item { return []*Item{anItem(theChild, "workbench")} })
		f.FailSends(errors.New("the circuit is gone"))
		if _, err := w.Worn(context.Background(), aFolder, "workbench", HUDCenter1); err == nil {
			t.Error("Worn went on to the wear though the detach was not sent")
		}
	})

	t.Run("the caller gave up while the detach settled", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.ServeInventory(t, func(msg.UUID) []*Item { return []*Item{anItem(theChild, "workbench")} })
		ctx, cancel := context.WithCancel(context.Background())
		wait := aside(t, func() (*Attached, error) {
			return w.Worn(ctx, aFolder, "workbench", HUDCenter1)
		})
		waitSent[*msg.DetachAttachmentIntoInv](t, f)
		cancel()
		if _, err := wait(); !errors.Is(err, context.Canceled) {
			t.Errorf("Worn = %v, want the context's reason", err)
		}
	})

	t.Run("nothing ever said it went back on", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		f.ServeInventory(t, func(msg.UUID) []*Item { return []*Item{anItem(theChild, "workbench")} })
		wait := aside(t, func() (*Attached, error) {
			return w.Worn(context.Background(), aFolder, "workbench", HUDCenter1)
		})
		// The wear itself is what fails: once the detach has settled,
		// nothing goes out at all.
		waitSent[*msg.DetachAttachmentIntoInv](t, f)
		f.FailSends(errors.New("the circuit is gone"))
		if _, err := wait(); err == nil {
			t.Error("Worn reported something worn though the wear was not sent")
		}
	})
}

// rezAndName answers a call that is making a prim and naming it: the
// region describing the new object, whose it is, and what it is called
// once the rename has gone out.
//
// The second question about the name is the one that matters.  SetName
// drops what it thinks the name is and asks again, so answering its
// first question -- the one the rez asked, to find out whose the prim
// is -- would have it read the old name back as the new one.
func rezAndName(t *testing.T, f *fakeBackend, id msg.UUID, local uint32, name string) {
	t.Helper()
	confirmRez(t, f, id, local, 1)
	waitSent[*msg.ObjectName](t, f)
	waitSentN[*msg.RequestObjectPropertiesFamily](t, f, 2)
	f.Relay(t, familyReply(id, testAgentID, name))
}

// TestEnsureAttachedMakesOneWhenThereIsNone: this is the once in the
// life of an account path -- rez a prim, name it, take it, put it on --
// and it is the one that has to be right, because everything after it
// depends on there being an item of that name to find.
func TestEnsureAttachedMakesOneWhenThereIsNone(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	// The folder is not empty, and holds nothing of the name asked
	// for, which is what makes the search for it worth anything.
	other := anItem(theOther, "something else")
	made := anItem(theChild, "workbench")
	var mu sync.Mutex
	taken := false
	f.ServeInventory(t, func(msg.UUID) []*Item {
		mu.Lock()
		defer mu.Unlock()
		if taken {
			return []*Item{other, made}
		}
		return []*Item{other}
	})

	wait := aside(t, func() (*Attached, error) {
		return w.EnsureAttached(context.Background(), aFolder, "workbench", HUDCenter1)
	})

	rezAndName(t, f, thePrim, 77, "workbench")

	// The prim is rezzed a metre and a half away, which is well within
	// anything that would be described.
	add := waitSent[*msg.ObjectAdd](t, f)
	if got := add.ObjectData.RayStart; got.X != 129.5 || got.Z != 25.5 {
		t.Errorf("rezzed at %+v, want it beside the avatar", got)
	}

	waitSent[*msg.DeRezObject](t, f)
	mu.Lock()
	taken = true
	mu.Unlock()

	waitSent[*msg.RezSingleAttachmentFromInv](t, f)
	f.Relay(t, wornUpdate(theChild, 88, made.ID, HUDCenter1))

	a, err := wait()
	if err != nil {
		t.Fatalf("EnsureAttached: %v", err)
	}
	if a.Item != made.ID || a.Object.ID != theChild || a.Point != HUDCenter1 {
		t.Errorf("attached %+v", a)
	}
}

// TestEnsureAttachedCostsOneLookupWhenItIsAlreadyOn: the ordinary case
// on every run after the first, and the reason the call exists: nothing
// is rezzed, nothing is taken off, and nothing waits.
func TestEnsureAttachedCostsOneLookupWhenItIsAlreadyOn(t *testing.T) {
	w, f := newFakeSession(t)
	it := anItem(theChild, "workbench")
	f.ServeInventory(t, func(msg.UUID) []*Item { return []*Item{it} })
	f.objects = []*Seen{
		{Object: Object{ID: thePrim, Local: 5}, AttachItem: it.ID, AttachPoint: HUDCenter1},
	}

	a, err := w.EnsureAttached(context.Background(), aFolder, "workbench", HUDCenter1)
	if err != nil {
		t.Fatalf("EnsureAttached: %v", err)
	}
	if a.Object.ID != thePrim || a.Item != it.ID {
		t.Errorf("found %+v", a)
	}
	if got := f.Sent(); len(got) != 0 {
		t.Errorf("an attachment already on was disturbed: %s", f.describe())
	}
}

// TestEnsureAttachedPutsOnWhatIsInInventory: in the folder but not on
// is the middle case, and it goes through Worn -- which here can answer
// from what this session was told, since it heard the attachment
// described even though the backend's picture of the region has not
// caught up.
func TestEnsureAttachedPutsOnWhatIsInInventory(t *testing.T) {
	w, f := newFakeSession(t)
	it := anItem(theChild, "workbench")
	f.ServeInventory(t, func(msg.UUID) []*Item { return []*Item{it} })
	// The backend knows of no attachment, so EnsureAttached goes to
	// Worn rather than answering from WornFromItem.
	f.Relay(t, wornUpdate(thePrim, 55, it.ID, HUDCenter1))

	a, err := w.EnsureAttached(context.Background(), aFolder, "workbench", HUDCenter1)
	if err != nil {
		t.Fatalf("EnsureAttached: %v", err)
	}
	if a.Object.ID != thePrim {
		t.Errorf("attached %+v", a)
	}
}

// TestEnsureAttachedClearsAwayARezGivenUpOn: a caller that gives up
// while the prim is being looked for may have one standing by then, and
// Rez hands it back.  Nobody else holds its id, so it is deleted into
// the trash before the cancel is returned.
func TestEnsureAttachedClearsAwayARezGivenUpOn(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	trash := msg.MustParseUUID("1ad37e57-7e57-c0de-4b44-9217348fe328")
	f.ServeInventoryTree(t, func(id msg.UUID) ([]*Folder, []*Item) {
		if id == testInvRoot {
			return []*Folder{{ID: trash, ParentID: testInvRoot, Name: "Trash", Type: FolderTrash}}, nil
		}
		return nil, nil
	})
	answerDeletes(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The prim appears as the first look after the rez is answered, so
	// only the last look sees it.
	looked := false
	f.mu.Lock()
	f.afterObjects = func() {
		if looked || !sentLocked[*msg.ObjectAdd](f) {
			return
		}
		looked = true
		f.objects = append(f.objects, ours(thePrim, 81, msg.Vector3{X: 129.5, Y: 128, Z: 25.75}))
		cancel()
	}
	f.mu.Unlock()

	_, err := whenCancelled(t, ctx, func(ctx context.Context) (*Attached, error) {
		return w.EnsureAttached(ctx, aFolder, "workbench", HUDCenter1)
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("EnsureAttached = %v, want the caller's cancel", err)
	}
	d := sentOf[*msg.DeRezObject](f)
	if len(d) != 1 || d[0].ObjectData[0].ObjectLocalID != 81 || d[0].AgentBlock.DestinationID != trash {
		t.Errorf("sent %s; want the prim that was made deleted into the trash", f.describe())
	}
}

// TestEnsureAttachedSaysWhichStepFailed: it is four calls deep by the
// end, and "sl: timed out" on its own would say nothing about which of
// them the caller has to look at.
func TestEnsureAttachedSaysWhichStepFailed(t *testing.T) {
	t.Parallel()

	t.Run("the folder cannot be read", func(t *testing.T) {
		t.Parallel()
		w, _ := newFakeSession(t)
		if _, err := w.EnsureAttached(context.Background(), aFolder, "workbench", HUDCenter1); err == nil {
			t.Error("EnsureAttached read a folder that is not there")
		}
	})

	t.Run("nothing knows where we are", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		f.ServeInventory(t, func(msg.UUID) []*Item { return nil })
		f.presenceErr = errors.New("no presence")
		if _, err := w.EnsureAttached(context.Background(), aFolder, "workbench", HUDCenter1); err == nil {
			t.Error("EnsureAttached rezzed a prim without knowing where it would go")
		}
	})

	t.Run("the prim could not be made", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		f.ServeInventory(t, func(msg.UUID) []*Item { return nil })
		f.FailSends(errors.New("the circuit is gone"))
		_, err := w.EnsureAttached(context.Background(), aFolder, "workbench", HUDCenter1)
		if err == nil || !strings.Contains(err.Error(), "making") {
			t.Errorf("EnsureAttached = %v, want it to say the rez failed", err)
		}
	})

	t.Run("the prim could not be named", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		f.ServeInventory(t, func(msg.UUID) []*Item { return nil })
		wait := aside(t, func() (*Attached, error) {
			return w.EnsureAttached(context.Background(), aFolder, "workbench", HUDCenter1)
		})

		// The rez goes through and then the circuit dies, so the
		// rename is the step that fails.
		confirmRez(t, f, thePrim, 77, 1)
		f.FailSends(errors.New("the circuit is gone"))

		_, err := wait()
		if err == nil || !strings.Contains(err.Error(), "naming") {
			t.Errorf("EnsureAttached = %v, want it to say the rename failed", err)
		}
	})

	t.Run("the prim could not be taken", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		f.ServeInventory(t, func(msg.UUID) []*Item { return nil })
		wait := aside(t, func() (*Attached, error) {
			return w.EnsureAttached(context.Background(), aFolder, "workbench", HUDCenter1)
		})

		rezAndName(t, f, thePrim, 77, "workbench")
		// Inventory stops answering, which is where a take begins.
		f.mu.Lock()
		f.capErr = errors.New("the inventory server is not answering")
		f.mu.Unlock()

		_, err := wait()
		if err == nil || !strings.Contains(err.Error(), "taking") {
			t.Errorf("EnsureAttached = %v, want it to say the take failed", err)
		}
	})

	t.Run("the prim could not be put on", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		made := anItem(theChild, "workbench")
		var mu sync.Mutex
		taken := false
		f.ServeInventory(t, func(msg.UUID) []*Item {
			mu.Lock()
			defer mu.Unlock()
			if taken {
				return []*Item{made}
			}
			return nil
		})
		wait := aside(t, func() (*Attached, error) {
			return w.EnsureAttached(context.Background(), aFolder, "workbench", HUDCenter1)
		})

		rezAndName(t, f, thePrim, 77, "workbench")
		waitSent[*msg.DeRezObject](t, f)
		mu.Lock()
		taken = true
		mu.Unlock()

		// The take lands and then nothing more goes out, so the wear
		// is the step that fails.
		f.FailSends(errors.New("the circuit is gone"))

		_, err := wait()
		if err == nil || !strings.Contains(err.Error(), "putting") {
			t.Errorf("EnsureAttached = %v, want it to say the wear failed", err)
		}
	})
}

// TestAnAttachmentTheRegionKillsIsNoLongerWorn: a script's detach ends
// in a KillObject for the attachment, and the record of what is worn
// has to follow it. A teleport sends no kill and describes the
// attachment again under a new local id, which is still worn.
// Why: doc/outfit.md#an-attachment-that-takes-itself-off
func TestAnAttachmentTheRegionKillsIsNoLongerWorn(t *testing.T) {
	item := msg.MustParseUUID("75f27e57-7e57-c0de-3e6e-cebd8a8d72d0")

	t.Run("killed", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.Relay(t, wornUpdate(thePrim, 4, item, HUDTop))
		f.Relay(t, &msg.KillObject{ObjectData: []msg.KillObject_ObjectData{{ID: 9}}})
		if _, ok := w.WornFrom(item); !ok {
			t.Fatal("the kill of another local id took the attachment off")
		}
		f.Relay(t, &msg.KillObject{ObjectData: []msg.KillObject_ObjectData{{ID: 4}}})
		if _, ok := w.WornFrom(item); ok {
			t.Error("a killed attachment is still reported worn")
		}
		if n := len(w.Attachments()); n != 0 {
			t.Errorf("%d attachments after the kill", n)
		}
	})

	t.Run("described again", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.Relay(t, wornUpdate(thePrim, 4, item, HUDTop))
		f.Relay(t, wornUpdate(thePrim, 11, item, HUDTop))
		got, ok := w.WornFrom(item)
		if !ok || got.Object.Local != 11 {
			t.Errorf("WornFrom = %+v, %v; want worn under local id 11", got, ok)
		}
		// The old local id is gone from the region; killing it must not
		// take the new description with it.
		f.Relay(t, &msg.KillObject{ObjectData: []msg.KillObject_ObjectData{{ID: 4}}})
		if _, ok := w.WornFrom(item); !ok {
			t.Error("the kill of the old local id took the redescribed attachment off")
		}
	})
}

// TestAShortenedPointNameSaysWhichItCouldBe: a name that is the first
// words of several points' names is refused with all of them, in point
// order, and never resolved to one; the whole names and the numbers
// they stand for are as they were.
func TestAShortenedPointNameSaysWhichItCouldBe(t *testing.T) {
	for _, c := range []struct {
		name   string
		points []int
	}{
		{"HUD centre", []int{HUDCenter1, HUDCenter2}},
		{"hud center", []int{HUDCenter1, HUDCenter2}},
		{"  HUD   Centre ", []int{HUDCenter1, HUDCenter2}},
		{"left", []int{13, 15, 7, 5, 54, 25, 21, 27, 29, 41, 3, 20, 26, 45}},
		{"left ring", []int{41}},
		{"avatar", []int{40}},
	} {
		got, err := ParseAttachPoint(c.name)
		var start *AttachPointStartError
		if !errors.As(err, &start) {
			t.Errorf("ParseAttachPoint(%q) = %d, %v, want the points it begins", c.name, got, err)
			continue
		}
		if !slices.Equal(start.Points, c.points) {
			t.Errorf("ParseAttachPoint(%q) begins %v, want %v", c.name, start.Points, c.points)
		}
		if start.Ambiguous() != (len(c.points) > 1) {
			t.Errorf("ParseAttachPoint(%q).Ambiguous() = %v for %v", c.name, start.Ambiguous(), c.points)
		}
	}

	_, err := ParseAttachPoint("HUD centre")
	if want := `"HUD centre" is HUD centre 1 or HUD centre 2`; err == nil || err.Error() != want {
		t.Errorf("the message is %v, want %q", err, want)
	}
	_, err = ParseAttachPoint("left")
	if err == nil || !strings.Contains(err.Error(), "left ear, left eye, left foot") ||
		!strings.Contains(err.Error(), " or left wing") {
		t.Errorf("the message for left is %v, want every candidate, the last after \"or\"", err)
	}
	_, err = ParseAttachPoint("left ring")
	if want := `"left ring" is not a whole attachment point name; did you mean "left ring finger"`; err == nil || err.Error() != want {
		t.Errorf("the message is %v, want %q", err, want)
	}

	// A whole name wins over being the start of others: "HUD top" begins
	// "HUD top left" and is a point.  A number is not this function's.
	for name, want := range map[string]int{"HUD top": HUDTop, "hud centre 1": HUDCenter1, "HUD Center 2": HUDCenter2, "left hand": 5} {
		if got, err := ParseAttachPoint(name); err != nil || got != want {
			t.Errorf("ParseAttachPoint(%q) = %d, %v, want %d", name, got, err, want)
		}
	}

	// Not the start of any name: a part of a word, nothing, or nothing known.
	for _, name := range []string{"ch", "elbow", "", "  ", "hud centre 3", "left hand 2"} {
		if _, err := ParseAttachPoint(name); !errors.Is(err, ErrNoAttachPoint) {
			t.Errorf("ParseAttachPoint(%q) = %v, want ErrNoAttachPoint", name, err)
		}
	}
}
