package slate

import (
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

// folderIM is an object's give of a folder: as offerIM, with the asset
// type of a category, 8, as the one byte of the bucket.
func folderIM(txn msg.UUID, name string) *msg.ImprovedInstantMessage {
	return offerIM(idStranger, "Example Tip Jar", txn, name, 8)
}

// folderOffering makes the vendor offer the folder Example Starter Folder,
// holding two items, when the tester says buy.
func folderOffering(t *testing.T, f *fakeGrid) *fakeInv {
	t.Helper()
	inv := f.withInventory(t)
	f.offerFolder(idOffer, idNewFolder, "Example Starter Folder",
		[]string{"Example Red Swatch", "Example Blue Swatch"}, []msg.UUID{idKitItemA, idKitItemB})
	f.whenSaid("buy", func() { f.relay(folderIM(idOffer, "Example Starter Folder")) })
	return inv
}

const folderSrc = hdr + "say \"buy\" on 0\nexpect give folder \"Example Starter Folder\" from vendor within 1s\n"

func TestAFolderGivenIsAcceptedIntoTheRootAndPassesOnANewFolder(t *testing.T) {
	f := newGrid(t)
	inv := folderOffering(t, f)
	res := play(t, f, folderSrc)
	wantExit(t, res, 0)
	acks := f.accepts()
	if len(acks) != 1 {
		t.Fatalf("%d accepts sent, want 1\n%s", len(acks), res.Transcript)
	}
	m := acks[0].MessageBlock
	if m.Dialog != 10 || m.ToAgentID != idStranger || m.ID != idOffer || string(m.BinaryBucket) != string(testInvRoot[:]) {
		t.Errorf("accept = dialog %d to %s id %s bucket %x", m.Dialog, m.ToAgentID, m.ID, m.BinaryBucket)
	}
	if got := inv.parentOf(idNewFolder); got != testInvRoot {
		t.Errorf("the folder landed in %s, want the root", got)
	}
	mustHave(t, res,
		`give from vendor: folder "Example Starter Folder"`,
		"give accept sent to "+idStranger.String()+" transaction "+idOffer.String()+" into My Inventory",
		"slate: pass step 1")
}

func TestAFolderAcceptedIsNotLeftWaitingForTheEndOfTestDecline(t *testing.T) {
	// The end of a test declines what the session still holds as an offer
	// (leave.go); an accept makes the session forget it.
	f := newGrid(t)
	folderOffering(t, f)
	res := play(t, f, folderSrc)
	wantExit(t, res, 0)
	for _, m := range sentOf[*msg.ImprovedInstantMessage](f) {
		if m.MessageBlock.Dialog == 11 {
			t.Errorf("the accepted folder was declined: %+v", m.MessageBlock)
		}
	}
	mustNotHave(t, res, "declined offer")
}

func TestHoldingPassesWhenEveryNamedItemIsInTheFolder(t *testing.T) {
	f := newGrid(t)
	folderOffering(t, f)
	res := play(t, f, hdr+"say \"buy\" on 0\n"+
		"expect give folder \"Example Starter Folder\" from vendor holding \"Example Red Swatch\" matching \"Blue\" within 1s\n")
	wantExit(t, res, 0)
}

func TestHoldingFailsAndSaysWhatTheFolderHeld(t *testing.T) {
	f := newGrid(t)
	folderOffering(t, f)
	res := play(t, f, hdr+"say \"buy\" on 0\n"+
		"expect give folder \"Example Starter Folder\" from vendor holding \"Example Red Swatch\" \"Example Welcome Note\" within 600ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, `unmatched give folder "Example Starter Folder" from vendor holding "Example Red Swatch" "Example Welcome Note" within 600ms`,
		`; the new folder holds "Example Blue Swatch", "Example Red Swatch"`)
}

func TestAFolderOfAnotherNameDoesNotPass(t *testing.T) {
	f := newGrid(t)
	folderOffering(t, f)
	res := play(t, f, hdr+"say \"buy\" on 0\nexpect give folder \"Example Other Folder\" from vendor within 400ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, `unmatched give folder "Example Other Folder" from vendor within 400ms`)
	if len(f.accepts()) != 0 {
		t.Error("an offer of another name was accepted")
	}
}

func TestAFolderThatWasThereAtTheArmPointIsNotNew(t *testing.T) {
	f := newGrid(t)
	inv := f.withInventory(t)
	inv.addFolder(testInvRoot, idOldFolder, "Example Starter Folder")
	f.whenSaid("buy", func() { f.relay(folderIM(idOffer, "Example Starter Folder")) })
	res := play(t, f, hdr+"say \"buy\" on 0\nexpect give folder \"Example Starter Folder\" from vendor within 400ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "accept was sent and inventory still has 1 folder of that name")
}

func TestAnItemOfferedWhereAFolderIsExpectedIsRefused(t *testing.T) {
	f := newGrid(t)
	offering(t, f, idNewCopy)
	res := play(t, f, hdr+"say \"buy\" on 0\nexpect give folder \"Example Thank You\" from vendor within 400ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "; the offer of that name is an item, and expect give without folder takes it")
	if len(f.accepts()) != 0 {
		t.Error("an item was accepted for a folder expectation")
	}
}

func TestAFolderOfferedWhereAnItemIsExpectedIsRefused(t *testing.T) {
	f := newGrid(t)
	folderOffering(t, f)
	res := play(t, f, hdr+"say \"buy\" on 0\nexpect give \"Example Starter Folder\" from vendor within 400ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, "; the offer of that name is a folder, and expect give folder takes it")
	if len(f.accepts()) != 0 {
		t.Error("a folder was accepted for an item expectation")
	}
}

func TestAFolderGiveBindsTheFoldersNameAndTakesAPattern(t *testing.T) {
	f := newGrid(t)
	folderOffering(t, f)
	res := play(t, f, hdr+"say \"buy\" on 0\nexpect give folder matching \"^Example (?P<kind>\\\\w+) Folder$\" from vendor within 1s as $name\n")
	wantExit(t, res, 0)
	mustHave(t, res, `capture $name = "Example Starter Folder"`, `capture $kind = "Starter"`)
}

func TestTheFolderFormTakesNoToAndNoNamedGroupInHolding(t *testing.T) {
	parseErr(t, secondHdr+"expect give folder \"F\" from vendor to visitor\n", "expect give folder takes no to")
	checkErr(t, hdr+"expect give folder \"F\" from vendor holding matching \"(?P<n>a)\"\n", "a holding pattern binds nothing")
	// Holding belongs to the folder form.
	parseErr(t, hdr+"expect give \"F\" from vendor holding \"a\"\n", "holding")
}

func TestFolderAndHoldingAreStillNames(t *testing.T) {
	for _, w := range []string{"folder", "holding"} {
		mustCheck(t, "slate 1\nobject "+w+" is \"O\"\ntest \"t\" {\n  expect give \"I\" from "+w+"\n  expect give folder \"I\" from "+w+" holding \"x\"\n}\n")
	}
}

func TestTheWorkedExampleOfAFolderGivenChecks(t *testing.T) {
	mustCheck(t, `
slate 1

object vendor is "Example Tip Jar"

say "kit" on 0
expect give folder "Example Starter Folder" from vendor holding "Example Red Swatch" matching "Blue Swatch$" within 15s as $kit
`)
}
