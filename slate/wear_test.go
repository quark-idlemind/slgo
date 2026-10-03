package slate

import (
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

const wearHdr = hdr + "item hat is \"Example Hat\" in \"Objects\"\n"

func TestWearBindsANameThatTheSameStepUses(t *testing.T) {
	f := newGrid(t)
	f.withWearing(t)
	res := play(t, f, wearHdr+`wear hat on "HUD centre 2" as hud
expect attached hud on "HUD centre 2" within 2s
`)
	wantExit(t, res, 0)
	mustHave(t, res, "attached hud HUD centre 2", "pass step 1")
	w := sentOf[*msg.RezSingleAttachmentFromInv](f)
	if len(w) != 1 || w[0].ObjectData.ItemID != idWornHat || w[0].ObjectData.AttachmentPt != sl.HUDCenter2|sl.AttachAdd {
		t.Errorf("sent %+v", w)
	}
}

func TestTheProductsAttachTimeChatAfterWearReturnsIsMatchedInTheSameStep(t *testing.T) {
	f := newGrid(t)
	g := f.withWearing(t)
	g.says = "ready"
	res := play(t, f, wearHdr+`wear hat on "HUD top" as hud
expect say "ready" on public from object hud within 2s
expect attached hud on "HUD top"
`)
	wantExit(t, res, 0)
}

func TestWearingAnItemThatIsAlreadyWornFailsBeforeAnythingIsSent(t *testing.T) {
	f := newGrid(t)
	g := f.withWearing(t)
	g.wornAlready(sl.HUDTop)
	res := play(t, f, wearHdr+"wear hat on \"HUD centre 2\" as hud\nexpect attached hud on \"HUD centre 2\" within 500ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, `slate: step 1: "Example Hat" is already worn on HUD top; take it off first`)
	if n := len(sentOf[*msg.RezSingleAttachmentFromInv](f)); n != 0 {
		t.Errorf("%d wears sent", n)
	}
}

func TestTakeOffThenAttachedOff(t *testing.T) {
	f := newGrid(t)
	f.withWearing(t)
	res := play(t, f, wearHdr+`wear hat on "chest" as hud
expect attached hud on "chest" within 2s
take off hud
expect attached hud off within 2s
`)
	wantExit(t, res, 0)
	mustHave(t, res, "attached hud chest", "attached hud off")
	d := sentOf[*msg.DetachAttachmentIntoInv](f)
	if len(d) != 1 || d[0].ObjectData.ItemID != idWornHat {
		t.Errorf("sent %+v", d)
	}
	if f.listed("Example Hat") || f.listed("Example Hat Band") {
		t.Error("the store still lists the hat")
	}
}

func TestBeforeEachWearsAndAfterEachTakesOffAcrossTwoTests(t *testing.T) {
	f := newGrid(t)
	f.withWearing(t)
	res := play(t, f, wearHdr+`before each {
  wear hat on "HUD centre 2" as hud
}
test "one" {
  expect attached hud on "HUD centre 2" within 2s
}
test "two" {
  expect attached hud on "HUD centre 2" within 2s
}
after each {
  take off hud
  expect attached hud off within 2s
}
`)
	wantExit(t, res, 0)
	if n := len(sentOf[*msg.RezSingleAttachmentFromInv](f)); n != 2 {
		t.Errorf("%d wears, want 2", n)
	}
	if n := len(sentOf[*msg.DetachAttachmentIntoInv](f)); n != 2 {
		t.Errorf("%d take offs, want 2", n)
	}
	if f.listed("Example Hat") {
		t.Error("the hat is still worn")
	}
}

func TestAnItemThatIsNotFoundIsASetupFailure(t *testing.T) {
	f := newGrid(t)
	f.withWearing(t)
	res, err := tryPlay(t, f, hdr+"item hat is \"Example Nothing\" in \"Objects\"\nwear hat on \"chest\" as hud\n", Options{}, testCfg())
	if err == nil {
		t.Fatal("no error")
	}
	wantExit(t, res, 3)
	var got string
	for _, l := range lines(res) {
		if strings.HasPrefix(l, `slate: setup: item "Example Nothing" in "Objects": `) {
			got = l
		}
	}
	if got == "" {
		t.Errorf("no setup failure line:\n%s", res.Transcript)
	}
	if n := len(sentOf[*msg.RezSingleAttachmentFromInv](f)); n != 0 {
		t.Errorf("%d wears sent", n)
	}
	res, _ = tryPlay(t, f, hdr+"item hat is \"Example Hat\" in \"Nowhere\"\nwear hat on \"chest\" as hud\n", Options{}, testCfg())
	wantExit(t, res, 3)
	mustHave(t, res, `slate: setup: item "Example Hat" in "Nowhere": `)
}

func TestANegativeAttachedOffHoldsWhileItIsWornAndFailsOnceItIsNot(t *testing.T) {
	f := newGrid(t)
	f.withWearing(t)
	res := play(t, f, wearHdr+`wear hat on "chest" as hud
expect attached hud on "chest" within 2s
then expect no attached hud off within 300ms
`)
	wantExit(t, res, 0)

	f = newGrid(t)
	f.withWearing(t)
	res = play(t, f, wearHdr+`wear hat on "chest" as hud
take off hud
expect no attached hud off within 300ms
`)
	wantExit(t, res, 1)
	mustHave(t, res, "forbidden")
}

func TestTheStimulusDetailsSayWhatWasWornAndTakenOff(t *testing.T) {
	f := newGrid(t)
	f.withWearing(t)
	res := play(t, f, wearHdr+`wear hat on "HUD centre 2" as hud
expect attached hud on "chest" within 300ms
`)
	wantExit(t, res, 1)
	mustHave(t, res, `wore "Example Hat" on HUD centre 2 as hud`, "last reading: attached hud HUD centre 2")

	f = newGrid(t)
	f.withWearing(t)
	res = play(t, f, wearHdr+`wear hat on "chest" as hud
take off hud
expect attached hud on "head" within 300ms
`)
	wantExit(t, res, 1)
	mustHave(t, res, `took off "Example Hat"`)
}
