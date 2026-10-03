package slate

import (
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

func TestOfferItemNameIsReadOutOfTheOfferText(t *testing.T) {
	for _, c := range []struct {
		text, want string
		ok         bool
	}{
		{"'Example Thank You'  ( http://example.invalid/Test/128/64/22 )", "Example Thank You", true},
		// A name with a quote in it is read whole.
		{"'Example 'Quoted' Name'  ( http://example.invalid/Test/1/2/3 )", "Example 'Quoted' Name", true},
		{"'Example Thank You'", "", false},
		{"no quotes at all", "", false},
	} {
		got, ok := offerItemName(c.text)
		if got != c.want || ok != c.ok {
			t.Errorf("offerItemName(%q) = %q, %v; want %q, %v", c.text, got, ok, c.want, c.ok)
		}
	}
}

// giveSrc asks the vendor for the item and expects it.
const giveSrc = hdr + "say \"buy\" on 0\nexpect give \"Example Thank You\" from vendor within 1s\n"

// offering makes the vendor offer an item when the tester says buy, and
// accepting it deliver the item, as the grid does.
func offering(t *testing.T, f *fakeGrid, item msg.UUID) *fakeInv {
	t.Helper()
	inv := f.withInventory(t)
	f.offer(idOffer, item, "Example Thank You", 10)
	f.whenSaid("buy", func() {
		f.relay(offerIM(idStranger, "Example Tip Jar", idOffer, "Example Thank You", 10))
	})
	return inv
}

func TestAGiveIsAcceptedIntoTheScriptsFolderAndPassesOnANewIdWithAnOlderCopyHeld(t *testing.T) {
	f := newGrid(t)
	inv := offering(t, f, idNewCopy)
	inv.add(idScriptsFolder, idOldCopy, "Example Thank You", 10)
	res := play(t, f, giveSrc)
	wantExit(t, res, 0)
	acks := f.accepts()
	if len(acks) != 1 {
		t.Fatalf("%d accepts sent, want 1\n%s", len(acks), res.Transcript)
	}
	m := acks[0].MessageBlock
	if m.Dialog != 10 || m.ToAgentID != idStranger || m.ID != idOffer || string(m.BinaryBucket) != string(idScriptsFolder[:]) {
		t.Errorf("accept = dialog %d to %s id %s bucket %x", m.Dialog, m.ToAgentID, m.ID, m.BinaryBucket)
	}
	if got := inv.folderOf(idNewCopy); got != idScriptsFolder {
		t.Errorf("the item landed in %s, want the Scripts folder", got)
	}
	mustHave(t, res,
		`give from vendor: "Example Thank You"`,
		"give accept sent to "+idStranger.String()+" transaction "+idOffer.String()+" into Scripts",
		"slate: pass step 1")
}

func TestAnAcceptThatDeliveredNothingIsTheUnmatchedLineWithTheCount(t *testing.T) {
	// An older copy is held and the accept brings nothing new.
	f := newGrid(t)
	inv := f.withInventory(t)
	inv.add(idScriptsFolder, idOldCopy, "Example Thank You", 10)
	f.whenSaid("buy", func() {
		f.relay(offerIM(idStranger, "Example Tip Jar", idOffer, "Example Thank You", 10))
	})
	res := playWith(t, f, hdr+"say \"buy\" on 0\nexpect give \"Example Thank You\" from vendor within 300ms\n", Options{}, testCfg())
	wantExit(t, res, 1)
	mustHave(t, res, `unmatched give "Example Thank You" from vendor within 300ms; accept was sent and inventory still has 1 item of that name`)

	// None held, none came.
	f = newGrid(t)
	f.withInventory(t)
	f.whenSaid("buy", func() {
		f.relay(offerIM(idStranger, "Example Tip Jar", idOffer, "Example Thank You", 10))
	})
	res = play(t, f, hdr+"say \"buy\" on 0\nexpect give \"Example Thank You\" from vendor within 300ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "accept was sent and inventory still has 0 items of that name")

	// No offer, no accept, and no clause.
	f = newGrid(t)
	f.withInventory(t)
	res = play(t, f, hdr+"say \"buy\" on 0\nexpect give \"Example Thank You\" from vendor within 200ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, `unmatched give "Example Thank You" from vendor within 200ms`)
	mustNotHave(t, res, "accept was sent")
	if len(f.accepts()) != 0 {
		t.Error("an accept was sent with no offer")
	}
}

func TestTwoMatchingOffersAreAmbiguousAndNeitherIsAccepted(t *testing.T) {
	f := newGrid(t)
	f.withInventory(t)
	f.whenSaid("buy", func() {
		f.relay(offerIM(idStranger, "Example Tip Jar", idOffer, "Example Thank You", 10))
		f.relay(offerIM(idStranger, "Example Tip Jar", idOffer2, "Example Thank You", 10))
	})
	res := play(t, f, giveSrc)
	wantExit(t, res, 1)
	mustHave(t, res, "ambiguous", idOffer.String(), idOffer2.String())
	if len(f.accepts()) != 0 {
		t.Errorf("%d accepts sent for an ambiguous pair", len(f.accepts()))
	}
}

func TestTwoNewItemsOfTheNameAreAmbiguousEvenAfterTheAccept(t *testing.T) {
	f := newGrid(t)
	inv := f.withInventory(t)
	f.whenSaid("buy", func() {
		f.relay(offerIM(idStranger, "Example Tip Jar", idOffer, "Example Thank You", 10))
		inv.add(idScriptsFolder, idNewCopy, "Example Thank You", 10)
		inv.add(idObjectsFolder, idNewCopy2, "Example Thank You", 10)
	})
	res := play(t, f, giveSrc)
	wantExit(t, res, 1)
	mustHave(t, res, "ambiguous", idNewCopy.String(), idNewCopy2.String())
	if len(f.accepts()) != 1 {
		t.Errorf("%d accepts sent, want the one that was sent before the second item showed", len(f.accepts()))
	}
}

func TestAGiveMatchingAPatternAndFromTheLinksetOnly(t *testing.T) {
	f := newGrid(t)
	offering(t, f, idNewCopy)
	res := play(t, f, hdr+"say \"buy\" on 0\nexpect give matching \"Thank\\\\sYou$\" from vendor within 1s\n")
	wantExit(t, res, 0)

	// An offer from an object that is not in the linkset is not this give.
	f = newGrid(t)
	f.withInventory(t)
	f.whenSaid("buy", func() {
		f.relay(offerIM(idStranger, "Example Other Object", idOffer, "Example Thank You", 10))
	})
	res = play(t, f, hdr+"say \"buy\" on 0\nexpect give \"Example Thank You\" from vendor within 200ms\n")
	wantExit(t, res, 1)
	if len(f.accepts()) != 0 {
		t.Error("an offer from another object was accepted")
	}
}

func TestAnOfferOfATypeWhoseFolderIsNotKnownFailsClearly(t *testing.T) {
	f := newGrid(t)
	f.withInventory(t)
	f.whenSaid("buy", func() {
		f.relay(offerIM(idStranger, "Example Tip Jar", idOffer, "Example Thank You", 99))
	})
	res := play(t, f, giveSrc)
	wantExit(t, res, 1)
	mustHave(t, res, "asset type 99", "only a script's has been measured")
	if len(f.accepts()) != 0 {
		t.Error("an accept was sent for a type with no known folder")
	}
}

func TestANegativeGive(t *testing.T) {
	f := newGrid(t)
	f.withInventory(t)
	wantExit(t, play(t, f, hdr+"say \"x\" on 0\nexpect no give \"Example Thank You\" from vendor within 100ms\n"), 0)

	f = newGrid(t)
	f.withInventory(t)
	f.whenSaid("buy", func() {
		f.relay(offerIM(idStranger, "Example Tip Jar", idOffer, "Example Thank You", 10))
	})
	res := play(t, f, hdr+"say \"buy\" on 0\nexpect no give \"Example Thank You\" from vendor within 1s\n")
	wantExit(t, res, 1)
	mustHave(t, res, "forbidden no give")
	if len(f.accepts()) != 0 {
		t.Error("a negative give accepted the offer")
	}
}
