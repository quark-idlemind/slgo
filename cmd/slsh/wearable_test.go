package main

import (
	"context"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// wearableAt puts a system wearable in Objects, in a given slot.
//
// The slot lives in the low byte of the flags and nowhere else, which
// is the whole reason these tests exist: a skin and a shape are both
// "bodypart", and everything about replacing turns on telling them
// apart.
func wearableAt(x *testShell, id msg.UUID, name string, kind sl.AssetType, slot sl.WearableType) {
	x.grid.mu.Lock()
	defer x.grid.mu.Unlock()
	objs := x.grid.inv.Dirs[0]
	objs.Items = append(objs.Items, &invItem{
		ID: id, Name: name, Type: int(kind), InvType: 18,
		Flags: uint32(slot),
	})
}

// outfitNames is what the Current Outfit folder holds, by the name on
// each link.
func outfitNames(t *testing.T, x *testShell) []string {
	t.Helper()
	x.grid.mu.Lock()
	defer x.grid.mu.Unlock()
	cof := findDirOfType(x.grid.inv, sl.FolderCurrentOutfit)
	if cof == nil {
		t.Fatal("the fake has no Current Outfit folder")
	}
	var out []string
	for _, it := range cof.Items {
		out = append(out, it.Name)
	}
	return out
}

// TestWearingClothingLinksItIntoTheOutfitAndRebakes.
//
// Wearing something that is not an object is two steps and neither is
// optional: a link into the Current Outfit folder, which IS the
// wearing, and a rebake, which is what makes it visible.  The folder is
// the grid's own record of what an avatar has on and the baking service
// reads it; nothing else here does either job.
//
// Measured against Agni on the way to this: the link went in, the
// immediate rebake came back 500 with an asset error, and the same
// request a few seconds later came back success with the whole texture
// set.  Hence the retry, and hence the wearing and the rebake being
// separable in the first place.
func TestWearingClothingLinksItIntoTheOutfitAndRebakes(t *testing.T) {
	shirt := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000d1")

	x := newTestShell(t)
	wearableAt(x, shirt, "a shirt", sl.AssetClothing, sl.WearableShirt)

	got := x.do(t, "wear Objects/a shirt")
	if want := "a shirt is worn as shirt\n"; got != want {
		t.Errorf("wearing clothing printed %q, want %q", got, want)
	}
	// The slot and not an attachment point, because there is no point:
	// a wearable is not attached to anything.
	if strings.Contains(got, "worn on") {
		t.Errorf("a wearable was reported as worn ON something: %q", got)
	}
	if names := outfitNames(t, x); len(names) != 1 || names[0] != "a shirt" {
		t.Errorf("the Current Outfit folder holds %q, want the one link", names)
	}
	if n := x.grid.Baked(); n != 1 {
		t.Errorf("%d rebakes were asked for, want the one", n)
	}
	// And nothing was sent to the attachment machinery.
	if _, ok := lastAttach(x); ok {
		t.Error("a wearable went out as an attach request")
	}
}

// TestWearingABodyPartReplacesWhateverIsInItsSlot.
//
// A body part always replaces, whether or not --replace was given:
// there is no such thing as an avatar wearing two skins, so a bare wear
// of one is a replace however it is worded.  Saying what came off is
// the honest way to do that rather than the quiet way -- which is the
// complaint the object side of this command was rewritten over.
func TestWearingABodyPartReplacesWhateverIsInItsSlot(t *testing.T) {
	first := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000d2")
	second := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000d3")
	other := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000d4")

	x := newTestShell(t)
	wearableAt(x, first, "blue eyes", sl.AssetBodypart, sl.WearableEyes)
	wearableAt(x, second, "green eyes", sl.AssetBodypart, sl.WearableEyes)
	// A different slot, which must be left alone.
	wearableAt(x, other, "a shape", sl.AssetBodypart, sl.WearableShape)

	x.do(t, "wear Objects/blue eyes")
	x.do(t, "wear Objects/a shape")

	got := x.do(t, "wear Objects/green eyes")
	if want := "green eyes is worn as eyes; blue eyes came off\n"; got != want {
		t.Errorf("replacing a body part printed %q, want %q", got, want)
	}
	names := outfitNames(t, x)
	if len(names) != 2 {
		t.Fatalf("the outfit holds %q, want the shape and the new eyes", names)
	}
	for _, n := range names {
		if n == "blue eyes" {
			t.Error("the body part that was replaced is still in the outfit")
		}
	}
	// The shape is in another slot and is not touched by any of this.
	if !strings.Contains(strings.Join(names, ","), "a shape") {
		t.Errorf("replacing the eyes took the shape off too: %q", names)
	}
}

// TestWearingClothingAddsUnlessReplaceIsAskedFor.
//
// Clothing layers, so it adds -- the same default, and for the same
// reason, as wearing an object: a wrong add is visible and costs a
// detach, a wrong replace is invisible and costs whatever was there.
func TestWearingClothingAddsUnlessReplaceIsAskedFor(t *testing.T) {
	first := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000d5")
	second := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000d6")

	x := newTestShell(t)
	wearableAt(x, first, "a red shirt", sl.AssetClothing, sl.WearableShirt)
	wearableAt(x, second, "a blue shirt", sl.AssetClothing, sl.WearableShirt)

	x.do(t, "wear Objects/a red shirt")
	if got := x.do(t, "wear Objects/a blue shirt"); strings.Contains(got, "came off") {
		t.Errorf("a bare wear of clothing replaced something: %q", got)
	}
	if names := outfitNames(t, x); len(names) != 2 {
		t.Errorf("the outfit holds %q, want both shirts", names)
	}

	y := newTestShell(t)
	wearableAt(y, first, "a red shirt", sl.AssetClothing, sl.WearableShirt)
	wearableAt(y, second, "a blue shirt", sl.AssetClothing, sl.WearableShirt)
	y.do(t, "wear Objects/a red shirt")
	if got := y.do(t, "wear --replace Objects/a blue shirt"); !strings.Contains(got, "a red shirt came off") {
		t.Errorf("--replace on clothing printed %q, and did not name what it displaced", got)
	}
	if names := outfitNames(t, y); len(names) != 1 {
		t.Errorf("after --replace the outfit holds %q, want the one shirt", names)
	}
}

// TestWearingTheSameWearableTwiceIsRefused.
//
// Two links to one item agree in every field a person could name one
// by, so it is a state this shell can create and could not then unpick
// -- which is the reason the object side refuses the same thing.
func TestWearingTheSameWearableTwiceIsRefused(t *testing.T) {
	shirt := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000d7")

	x := newTestShell(t)
	wearableAt(x, shirt, "a shirt", sl.AssetClothing, sl.WearableShirt)
	x.do(t, "wear Objects/a shirt")

	err := x.Do(context.Background(), "wear Objects/a shirt")
	if err == nil {
		t.Fatal("one item was worn twice")
	}
	for _, want := range []string{"already worn as shirt", "detach"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal was %q, which does not say %q", err, want)
		}
	}
	if names := outfitNames(t, x); len(names) != 1 {
		t.Errorf("the outfit holds %q, want the one link", names)
	}
}

// TestDetachTakesOffClothingButNotABodyPart.
//
// An avatar is never without a shape, a skin, hair or eyes: there is
// nothing to fall back to, so the way out of a body part is another
// one.  The viewer draws the same line, silently -- its menu offers
// "Take Off" for clothing and lets a body part fall through without it.
func TestDetachTakesOffClothingButNotABodyPart(t *testing.T) {
	shirt := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000d8")
	eyes := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000d9")

	x := newTestShell(t)
	wearableAt(x, shirt, "a shirt", sl.AssetClothing, sl.WearableShirt)
	wearableAt(x, eyes, "blue eyes", sl.AssetBodypart, sl.WearableEyes)
	x.do(t, "wear Objects/a shirt")
	x.do(t, "wear Objects/blue eyes")

	if got, want := x.do(t, "detach a shirt"), "a shirt is no longer worn as shirt\n"; got != want {
		t.Errorf("detaching clothing printed %q, want %q", got, want)
	}
	if names := outfitNames(t, x); len(names) != 1 || names[0] != "blue eyes" {
		t.Errorf("the outfit holds %q, want the eyes alone", names)
	}

	err := x.Do(context.Background(), "detach blue eyes")
	if err == nil {
		t.Fatal("a body part was taken off, leaving the avatar without one")
	}
	for _, want := range []string{"body part", "no eyes", "Wearing another one"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal was %q, which does not say %q", err, want)
		}
	}
	if names := outfitNames(t, x); len(names) != 1 {
		t.Errorf("the refused detach changed the outfit: %q", names)
	}
}

// TestSlotOfReadsTheLowByteOfTheFlags.
//
// The slot is in the flags and nowhere else, and only for the two asset
// types that have one: an object's flags mean something quite different
// and must not be read as a slot.
func TestSlotOfReadsTheLowByteOfTheFlags(t *testing.T) {
	for _, c := range []struct {
		kind  sl.AssetType
		flags uint32
		slot  sl.WearableType
		is    bool
	}{
		{sl.AssetBodypart, 0, sl.WearableShape, true},
		{sl.AssetBodypart, 3, sl.WearableEyes, true},
		{sl.AssetClothing, 4, sl.WearableShirt, true},
		{sl.AssetClothing, 14, sl.WearableTattoo, true},
		// Only the low byte is the slot; the rest of the word is not.
		{sl.AssetClothing, 0xabcd00 | 5, sl.WearablePants, true},
		// An object has flags too, and none of them is a slot.
		{sl.AssetObject, 4, 0, false},
		{sl.AssetNotecard, 0, 0, false},
	} {
		slot, is := sl.SlotOf(c.kind, c.flags)
		if is != c.is {
			t.Errorf("SlotOf(%v, %#x) said wearable=%v, want %v", c.kind, c.flags, is, c.is)
		}
		if is && slot != c.slot {
			t.Errorf("SlotOf(%v, %#x) = %v, want %v", c.kind, c.flags, slot, c.slot)
		}
	}
	// The names are the grid's own and are what the reports say.
	if got := sl.WearableUniversal.String(); got != "universal" {
		t.Errorf("slot 16 is %q, want universal", got)
	}
	if got := sl.WearablePhysics.String(); got != "physics" {
		t.Errorf("slot 15 is %q, want physics", got)
	}
}

// TestWornListsBothRecordsAndSaysWhereTheyDisagree.
//
// The report this was written for: "worn no longer shows everything
// that is worn".  It never had -- a skin, a shape and four clothing
// layers showed as nothing at all, because what it listed was the
// region's description of objects and a shirt is not an object.  And
// an attachment the region has not described to this session is not in
// that list either, although it is plainly on the avatar, which is the
// ordinary state after a reconnect.
//
// So both records are listed.  The Current Outfit folder is what
// SHOULD be on and the region is what IS, they disagree in both
// directions, and a line that came from only one of them says so.
func TestWornListsBothRecordsAndSaysWhereTheyDisagree(t *testing.T) {
	shirt := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000e1")
	hat := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000e2")
	hatWorn := msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-0000000000e2")

	x := newTestShell(t)
	wearableAt(x, shirt, "a shirt", sl.AssetClothing, sl.WearableShirt)
	addObjectItem(x, hat, "a hat")
	x.grid.AnswerAttach(t, hatWorn, 11, 1)

	// A wearable, which has no object and never appears in the
	// region's listing.
	x.do(t, "wear Objects/a shirt")
	// An object in the folder that the region has not described: what
	// a thing that failed to rez at login looks like, and what
	// everything looks like to a session that attached afterwards.
	x.do(t, "wear Objects/a hat")
	x.grid.forget(hat)
	// And an object the region describes that the folder does not
	// hold.
	wearThings(x, &sl.Seen{Object: sl.Object{ID: testSomebody, Local: 10}, PCode: 9,
		AttachItem: testLamp, AttachPoint: 1})

	got := x.do(t, "worn")
	for _, want := range []string{
		"shirt              a shirt",
		"a hat",
		"in the outfit, not described",
		"a lamp",
		"not in the outfit",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("worn should have said %q:\n%s", want, got)
		}
	}
	// The wearable is not claimed to be on an attachment point, and
	// the disagreements are not claimed to be agreement.
	if strings.Contains(got, "a shirt") && strings.Contains(got, "chest              a shirt") {
		t.Errorf("a wearable was given an attachment point:\n%s", got)
	}
	// Wearables first, so the clothing does not scatter through the
	// attachment points.
	if i, j := strings.Index(got, "a shirt"), strings.Index(got, "a lamp"); i > j {
		t.Errorf("wearables should sort before attachments:\n%s", got)
	}
}

// TestDetachFindsWhatTheRegionNeverDescribed.
//
// The other half of the same report: an object visible in world, named
// in the Current Outfit folder, and refused by detach as not worn --
// because detach searched only what the region had described, and the
// region had described nothing since the reconnect.
//
// Taking one off needs no knowledge of the object at all.
// DetachAttachmentIntoInv carries the INVENTORY item id, which the
// folder has.
func TestDetachFindsWhatTheRegionNeverDescribed(t *testing.T) {
	hat := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000e3")

	hatWorn := msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-0000000000e3")

	x := newTestShell(t)
	addObjectItem(x, hat, "a hat")
	x.grid.AnswerAttach(t, hatWorn, 11, 1)
	x.do(t, "wear Objects/a hat")
	// The region forgets it, which is what a reconnect does.
	x.grid.forget(hat)
	// The session still has the attachment it was told of, and the
	// simulator kills it when it is detached.
	x.grid.mu.Lock()
	x.grid.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.DetachAttachmentIntoInv); ok {
			x.grid.Relay(t, &msg.KillObject{ObjectData: []msg.KillObject_ObjectData{{ID: 11}}})
		}
	}
	x.grid.mu.Unlock()

	if got := x.do(t, "worn"); !strings.Contains(got, "in the outfit, not described") {
		t.Fatalf("the hat should be in the folder and unseen:\n%s", got)
	}

	// By the item id, which is what "worn -l" prints and what somebody
	// would copy out of it.
	baked := x.grid.Baked()
	got := x.do(t, "detach "+hat.String())
	if !strings.Contains(got, "asked to come off") {
		t.Errorf("detaching by item id printed %q", got)
	}
	// Baked after the folder changed, as a viewer bakes and as a detach
	// of something the region described does.
	if n := x.grid.Baked() - baked; n != 1 {
		t.Errorf("%d bakes asked for after the link came out, want 1", n)
	}
	d, ok := lastDetach(x)
	if !ok {
		t.Fatal("nothing was sent to take it off")
	}
	if d.ObjectData.ItemID != hat {
		t.Errorf("the detach named %v, want the item %v", d.ObjectData.ItemID, hat)
	}
	// And out of the folder, or it would come back at the next login.
	if names := outfitNames(t, x); len(names) != 0 {
		t.Errorf("the outfit still holds %q", names)
	}
}

// TestWearingAnObjectRecordsItInTheOutfit.
//
// An attachment put on without this is on the avatar until the session
// ends and then gone: the simulator rezzes what it is told to rez and
// remembers none of it.  The folder is the record, and writing it is
// the client's job -- which is why an avatar dressed from this shell
// used to come back undressed.
func TestWearingAnObjectRecordsItInTheOutfit(t *testing.T) {
	hat := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000e4")
	hatWorn := msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-0000000000e4")

	x := newTestShell(t)
	addObjectItem(x, hat, "a hat")
	x.grid.AnswerAttach(t, hatWorn, 11, 1)

	if got := x.do(t, "wear Objects/a hat"); !strings.Contains(got, "is worn on chest") {
		t.Fatalf("wear printed %q", got)
	}
	if names := outfitNames(t, x); len(names) != 1 || names[0] != "a hat" {
		t.Errorf("the outfit holds %q, want the one link", names)
	}
	// Taking it off takes the record out again, or it would be put
	// back on at the next login.
	x.grid.AnswerDetach(0)
	x.do(t, "detach a hat")
	if names := outfitNames(t, x); len(names) != 0 {
		t.Errorf("after detach the outfit still holds %q", names)
	}
}

// TestDressPutsBackOnWhatTheOutfitNamesAndIsNotOn.
//
// The fault: the simulator puts most of an avatar's attachments back at
// login but not reliably all of them, and a viewer puts on whatever the
// Current Outfit folder names that is still missing.  Nothing here did,
// so an avatar dressed from this shell could come back from a login
// missing part of its outfit and stay that way.
func TestDressPutsBackOnWhatTheOutfitNamesAndIsNotOn(t *testing.T) {
	hat := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000f1")
	hatWorn := msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-0000000000f1")
	shirt := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000f2")

	x := newTestShell(t)
	addObjectItem(x, hat, "a hat")
	wearableAt(x, shirt, "a shirt", sl.AssetClothing, sl.WearableShirt)
	x.grid.AnswerAttach(t, hatWorn, 11, 1)

	// Dressed from this shell, which writes the folder.
	x.do(t, "wear Objects/a hat")
	x.do(t, "wear Objects/a shirt")
	// And logged in again: the folder still names them and the region
	// describes none of them.
	x.grid.takeOff(hat)

	if got := x.do(t, "worn"); !strings.Contains(got, "in the outfit, not described") {
		t.Fatalf("the hat should be in the folder and unseen:\n%s", got)
	}

	if got := x.do(t, "dress"); !strings.Contains(got, "put on a hat") {
		t.Errorf("dress printed %q", got)
	}
	m, ok := lastAttach(x)
	if !ok {
		t.Fatal("dress sent no attach request")
	}
	if m.ObjectData.ItemID != hat {
		t.Errorf("dress asked for %v, want the hat %v", m.ObjectData.ItemID, hat)
	}
	// The point the object carries, WITH the add bit.  A point holds
	// more than one attachment and an ordinary outfit uses that -- a
	// body, a dress and a pair of arms all sit on the chest -- so
	// restoring with replace puts them on one after another and each
	// knocks the last one off.  Seen on an avatar restored that way:
	// she came back in her boots and her hair and nothing else.
	if got, want := m.ObjectData.AttachmentPt, uint8(sl.AttachAdd); got != want {
		t.Errorf("dress asked for point byte %d, want %d: the object's own point, added", got, want)
	}

	// The shirt is not an attachment and is not asked for: clothing is
	// not rezzed and does not go missing at a login.
	for _, sent := range x.grid.Sent() {
		if r, ok := sent.(*msg.RezSingleAttachmentFromInv); ok && r.ObjectData.ItemID == shirt {
			t.Error("dress tried to attach a system wearable")
		}
	}
}

// TestDressOnADressedAvatarSaysSo, rather than printing nothing, which
// is what a command that did nothing looks like.
func TestDressOnADressedAvatarSaysSo(t *testing.T) {
	hat := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000f3")
	hatWorn := msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-0000000000f3")

	x := newTestShell(t)
	addObjectItem(x, hat, "a hat")
	x.grid.AnswerAttach(t, hatWorn, 11, 1)
	x.do(t, "wear Objects/a hat")

	if got, want := x.do(t, "dress"), "already wearing all 1 of them\n"; got != want {
		t.Errorf("dress on a dressed avatar printed %q, want %q", got, want)
	}
}

// TestWearingAndDetachingAnObjectRebakes: a viewer asks for a bake after
// every change to the Current Outfit folder, attachments included, and
// that bake is what brings the simulator's own list of what is worn up
// to date.  Without it the list describes the outfit as it was before,
// and worn and dress can no longer check against it.
func TestWearingAndDetachingAnObjectRebakes(t *testing.T) {
	hat := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000e5")
	hatWorn := msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-0000000000e5")

	x := newTestShell(t)
	x.grid.simOnBake = true
	addObjectItem(x, hat, "a hat")
	x.grid.AnswerAttach(t, hatWorn, 11, 1)

	x.do(t, "wear Objects/a hat")
	if n := x.grid.Baked(); n != 1 {
		t.Fatalf("%d rebakes after wearing an object, want 1", n)
	}
	list, _ := x.grid.SimAttachments(context.Background(), msg.UUID{})
	if list == nil || len(list.Objects) != 1 || list.Objects[0].Object != hatWorn {
		t.Errorf("after the bake the simulator lists %+v, want the hat", list)
	}

	x.grid.AnswerDetach(0)
	x.do(t, "detach a hat")
	if n := x.grid.Baked(); n != 2 {
		t.Errorf("%d rebakes after taking it off again, want 2", n)
	}
}

// attachRequests counts the attach requests sent so far.
func attachRequests(x *testShell) int {
	n := 0
	for _, m := range x.grid.Sent() {
		if _, ok := m.(*msg.RezSingleAttachmentFromInv); ok {
			n++
		}
	}
	return n
}

// TestDressWaitsForWhatTheSimulatorSaysIsOn.
//
// The simulator puts most of an outfit back by itself at login, and the
// region describes each piece as it arrives -- so for a while after a
// login a thing can be on and not yet described.  Its own attachment
// list says so: it names more objects than the region has described.  A
// viewer keeps the avatar a cloud until the two agree; dress waits, and
// does not ask for what is on already.
func TestDressWaitsForWhatTheSimulatorSaysIsOn(t *testing.T) {
	hat := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000e6")
	hatWorn := msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-0000000000e6")
	hatAgain := msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-0000000001e6")

	x := newTestShell(t)
	addObjectItem(x, hat, "a hat")
	x.grid.AnswerAttach(t, hatWorn, 11, 1)
	x.do(t, "wear Objects/a hat")

	// Logged in again.  The simulator has put the hat back, as a new
	// object, and lists it; the region describes it a moment later.
	x.grid.takeOff(hat)
	x.grid.simLists(false, sl.SimAttachment{Object: hatAgain, Point: 1})
	x.grid.describeLater(hat, hatAgain, 12, 1, 3)
	asked := attachRequests(x)

	if got, want := x.do(t, "dress"), "already wearing all 1 of them\n"; got != want {
		t.Errorf("dress printed %q, want %q", got, want)
	}
	if n := attachRequests(x) - asked; n != 0 {
		t.Errorf("dress asked for %d attachments the simulator had already put on", n)
	}
}

// TestDressSaysWhenTheSimulatorListsWhatIsNeverDescribed: a description
// that was lost is not sent again, so the waiting is bounded, and what
// is still unaccounted for afterwards is put on -- which is what a
// viewer does.  The report says the simulator listed something nothing
// described, since that is the one case where "not described" may mean
// "on".
func TestDressSaysWhenTheSimulatorListsWhatIsNeverDescribed(t *testing.T) {
	hat := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000e7")
	hatWorn := msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-0000000000e7")
	somethingElse := msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-0000000001e7")

	x := newTestShell(t)
	addObjectItem(x, hat, "a hat")
	x.grid.AnswerAttach(t, hatWorn, 11, 1)
	x.do(t, "wear Objects/a hat")
	x.grid.takeOff(hat)
	x.grid.simLists(false, sl.SimAttachment{Object: somethingElse, Point: 2})

	got := x.do(t, "dress --wait 1")
	if !strings.Contains(got, "put on a hat") {
		t.Errorf("dress printed %q; the hat should have gone on after the wait", got)
	}
	if !strings.Contains(got, "the simulator lists 1 attachment that nothing here has described") {
		t.Errorf("dress printed %q; it should say what the simulator listed", got)
	}
}

// TestWornSettlesWhatIsNotDescribedFromTheSimulatorsList: the line
// "in the outfit, not described" is ambiguous -- off, or on and not
// described -- and the simulator's list is what settles it, when it is
// current.  Nothing is said when there is nothing to settle.
func TestWornSettlesWhatIsNotDescribedFromTheSimulatorsList(t *testing.T) {
	hat := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000e8")
	hatWorn := msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-0000000000e8")
	onSomewhere := msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-0000000001e8")

	x := newTestShell(t)
	addObjectItem(x, hat, "a hat")
	x.grid.AnswerAttach(t, hatWorn, 11, 1)
	x.do(t, "wear Objects/a hat")

	// Described, and the list agrees: nothing to say.
	x.grid.simLists(false, sl.SimAttachment{Object: hatWorn, Point: 1})
	if got := x.do(t, "worn"); strings.Contains(got, "simulator") {
		t.Errorf("worn said something about the simulator when all was well:\n%s", got)
	}

	// Not described, and the simulator lists something nothing has
	// described: named by point, and by object with -l.
	x.grid.takeOff(hat)
	x.grid.simLists(false, sl.SimAttachment{Object: onSomewhere, Point: 2})
	got := x.do(t, "worn -l")
	if !strings.Contains(got, "the simulator lists 1 attachment that nothing here has described") ||
		!strings.Contains(got, onSomewhere.String()) || !strings.Contains(got, "head") {
		t.Errorf("worn -l should name what the simulator lists undescribed:\n%s", got)
	}

	// Not described, and the simulator lists nothing undescribed: off,
	// with the one exception it cannot speak to.
	x.grid.simLists(false)
	if got := x.do(t, "worn"); !strings.Contains(got, "is off -- unless it is a HUD") {
		t.Errorf("worn should say the hat is off:\n%s", got)
	}

	// A list from before the last change to the outfit settles nothing.
	x.grid.simLists(true)
	if got := x.do(t, "worn"); !strings.Contains(got, "from before the last change to the outfit") {
		t.Errorf("worn should say the list is out of date:\n%s", got)
	}
}

// TestDressAsksForABakeWhenItHasNoListAndWaitsForWhatItNames.
//
// At a login there is no list to go by: the simulator sends an avatar
// its own appearance when a bake is asked for, and nothing has asked.
// A viewer asks as soon as the outfit folder has loaded, and so does
// dress -- and the list that comes back names what is attached, the
// undescribed included, which is what there was to wait for.
func TestDressAsksForABakeWhenItHasNoListAndWaitsForWhatItNames(t *testing.T) {
	hat := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000e9")
	hatWorn := msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-0000000000e9")
	hatAgain := msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-0000000001e9")

	x := newTestShell(t)
	addObjectItem(x, hat, "a hat")
	x.grid.AnswerAttach(t, hatWorn, 11, 1)
	x.do(t, "wear Objects/a hat")

	// Logged in again: no list, and the hat on but not yet described.
	x.grid.mu.Lock()
	x.grid.sim, x.grid.simOnBake = nil, true
	x.grid.mu.Unlock()
	x.grid.takeOff(hat)
	x.grid.describeLater(hat, hatAgain, 12, 1, 3)
	asked, baked := attachRequests(x), x.grid.Baked()

	if got, want := x.do(t, "dress"), "already wearing all 1 of them\n"; got != want {
		t.Errorf("dress printed %q, want %q", got, want)
	}
	if n := x.grid.Baked() - baked; n != 1 {
		t.Errorf("%d bakes asked for, want the one that gets a list", n)
	}
	if n := attachRequests(x) - asked; n != 0 {
		t.Errorf("dress asked for %d attachments that were on already", n)
	}
}
