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
	item := msg.MustParseUUID("75f27e57-7e57-c0de-b61a-dcef76421b96")

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
	item := msg.MustParseUUID("75f27e57-7e57-c0de-b61a-dcef76421b96")

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
	item := msg.MustParseUUID("75f27e57-7e57-c0de-b61a-dcef76421b96")
	f.Relay(t, wornUpdate(thePrim, 4, item, HUDTop))

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
	f.FailSends(errors.New("the circuit is gone"))
	if err := w.TakeOff(context.Background(), item); err == nil {
		t.Error("TakeOff reported success though nothing was sent")
	}
	if _, ok := w.WornFrom(item); !ok {
		t.Error("a detach that was never sent was recorded as having happened")
	}
}

// TestWornObjectsAsksWhoeverWasConnected: what THIS session has been
// told is nothing at all for a program that started after the avatar
// logged in, so what is worn has to come from the backend, which was
// listening at the time.
func TestWornObjectsAsksWhoeverWasConnected(t *testing.T) {
	w, f := newFakeSession(t)
	item := msg.MustParseUUID("75f27e57-7e57-c0de-b61a-dcef76421b96")
	f.objects = []*Seen{
		{Object: Object{ID: thePrim, Local: 5}, AttachItem: item, AttachPoint: HUDCenter1},
		// A prim standing in the region is not worn, whatever else is
		// known about it.
		{Object: Object{ID: theOther, Local: 6}, PCode: pcodePrim},
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
	item := msg.MustParseUUID("75f27e57-7e57-c0de-b61a-dcef76421b96")
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
	waitSent[*msg.ObjectAdd](t, f)
	f.Relay(t, anUpdate(msg.ObjectUpdate_ObjectData{FullID: id, ID: local}))
	waitSentN[*msg.RequestObjectPropertiesFamily](t, f, 1)
	f.Relay(t, familyReply(id, testAgentID, "Object"))
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
		waitSent[*msg.ObjectAdd](t, f)
		f.Relay(t, anUpdate(msg.ObjectUpdate_ObjectData{FullID: thePrim, ID: 77}))
		waitSent[*msg.RequestObjectPropertiesFamily](t, f)
		f.Relay(t, familyReply(thePrim, testAgentID, "Object"))
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
