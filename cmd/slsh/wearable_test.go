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
