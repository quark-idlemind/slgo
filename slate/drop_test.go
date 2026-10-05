package slate

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

const dropHdr = hdr + "item swatch is \"Example Red Swatch\" in \"Objects\"\n"

func TestDropParsesBothForms(t *testing.T) {
	s := mustCheck(t, dropHdr+"drop swatch into sign\ndrop swatch into vendor link 2\ndrop swatch onto sign face 3\ndrop swatch onto vendor link 2 face 0\n")
	st := s.Tests[0].Steps
	a, b, c, d := st[0].Stimulus.Drop, st[1].Stimulus.Drop, st[2].Stimulus.Drop, st[3].Stimulus.Drop
	if a == nil || a.Onto || a.Item.Text != "swatch" || a.Name.Text != "sign" || a.Link != nil {
		t.Errorf("into: %+v", a)
	}
	if b == nil || b.Onto || b.Link == nil || b.Link.Value != 2 {
		t.Errorf("into link: %+v", b)
	}
	if c == nil || !c.Onto || c.Face.Value != 3 || c.Link != nil {
		t.Errorf("onto: %+v", c)
	}
	if d == nil || !d.Onto || d.Link == nil || d.Link.Value != 2 || d.Face.Value != 0 {
		t.Errorf("onto link: %+v", d)
	}
	// Neither word is reserved as a name.
	mustCheck(t, "slate 1\nobject drop is \"Example Sign\"\nitem into is \"Example Red Swatch\" in \"Objects\"\ndrop into into drop\n")
	// With an expectation after it.
	mustCheck(t, dropHdr+"drop swatch onto sign face 3\nexpect texture sign face 3 is any as $t\n")
}

func TestDropParseErrors(t *testing.T) {
	parseErr(t, dropHdr+"drop swatch sign\n", "expected into or onto")
	parseErr(t, dropHdr+"drop swatch into\n", "expected a name")
	parseErr(t, dropHdr+"drop swatch onto sign\n", "expected face")
	parseErr(t, dropHdr+"drop swatch onto sign face\n", "expected an integer")
	parseErr(t, dropHdr+"drop swatch into sign link\n", "expected an integer")
	parseErr(t, dropHdr+"drop\n", "expected a name")
}

func TestDropChecks(t *testing.T) {
	refuses(t, dropHdr+"drop sign into vendor\n", "sign into", "sign is an object; drop takes an item")
	refuses(t, dropHdr+"drop nothing into sign\n", "nothing", "nothing is not an item")
	refuses(t, dropHdr+"drop swatch into nothing\n", "nothing", "nothing is not an object")
	refuses(t, dropHdr+"drop swatch into swatch\n", "swatch\n", "swatch is an item; only wear, rez and drop use an item")
	refuses(t, dropHdr+"avatar visitor\ndrop swatch into visitor\n", "last:visitor", "visitor is an avatar, not an object")
	refuses(t, dropHdr+"drop swatch into sign link 99999999999\n", "99999999999", "is outside -2147483648 to 2147483647")
	refuses(t, dropHdr+"drop swatch onto sign face 99999999999\n", "99999999999", "is outside -2147483648 to 2147483647")
	// A name that is gone, or not yet bound, is as for any stimulus.
	refuses(t, dropHdr+"test \"t\" {\n  drop swatch into made\n  rez swatch at 1 2 3 as made\n}\n", "made\n", "made is not an object")
}

func TestDropIsTheTestersAlone(t *testing.T) {
	src := dropHdr + "avatar visitor\n"
	checkErr(t, src+"drop swatch into sign as visitor\n", "drop stays the tester's")
	checkErr(t, src+"drop swatch onto sign face 1 as visitor\n", "drop stays the tester's")
}

func TestDropIntoPutsACopyInAndRemovesItAtTheEnd(t *testing.T) {
	f := newGrid(t)
	g := f.withDropping(t)
	g.holds(signLocal, heldItem{id: idHeldItem, name: "Example Box", asset: idBoxItem, typ: "object"})
	res := play(t, f, dropHdr+"drop swatch into sign\n")
	wantExit(t, res, 0)
	mustHave(t, res, "pass step 1",
		`slate: removed "Example Red Swatch" from sign`, `slate: pass test "t"`)
	u := sentOf[*msg.UpdateTaskInventory](f)
	if len(u) != 1 || u[0].UpdateData.LocalID != signLocal || u[0].UpdateData.Key != 0 ||
		u[0].InventoryData.ItemID != idSwatchItem || u[0].InventoryData.FolderID != idSign ||
		strings.TrimRight(string(u[0].InventoryData.Name), "\x00") != "Example Red Swatch" {
		t.Errorf("sent %+v", u)
	}
	r := sentOf[*msg.RemoveTaskInventory](f)
	if len(r) != 1 || r[0].InventoryData.LocalID != signLocal || r[0].InventoryData.ItemID == idSwatchItem || r[0].InventoryData.ItemID == idHeldItem {
		t.Errorf("removed %+v", r)
	}
	if got := g.contents(signLocal); !reflect.DeepEqual(got, []string{"Example Box"}) {
		t.Errorf("the prim holds %v afterwards, want only what it held", got)
	}
	// Removed before the verdict.
	if iRm, iPass := strings.Index(res.Transcript, "slate: removed"), strings.Index(res.Transcript, "slate: pass test"); iRm < 0 || iRm > iPass {
		t.Errorf("the removal came after the pass line:\n%s", res.Transcript)
	}
}

func TestDropIntoRemovesOnlyTheCopyWhenTheNameWasTaken(t *testing.T) {
	f := newGrid(t)
	g := f.withDropping(t)
	g.holds(signLocal, heldItem{id: idHeldItem, name: "Example Red Swatch", asset: idSwatchAsset, typ: "texture"})
	res := play(t, f, dropHdr+"drop swatch into sign\ndrop swatch into sign\n")
	wantExit(t, res, 0)
	mustHave(t, res, `slate: removed "Example Red Swatch 1" from sign`)
	if got := g.contents(signLocal); !reflect.DeepEqual(got, []string{"Example Red Swatch"}) {
		t.Errorf("the prim holds %v afterwards", got)
	}
	r := sentOf[*msg.RemoveTaskInventory](f)
	if len(r) != 2 {
		t.Fatalf("%d removals, want 2", len(r))
	}
	for _, x := range r {
		if x.InventoryData.ItemID == idHeldItem {
			t.Error("the item the prim held before was removed")
		}
	}
}

func TestDropIntoWaitsForTheCopyToShow(t *testing.T) {
	f := newGrid(t)
	g := f.withDropping(t)
	g.delay = 150 * time.Millisecond
	res := play(t, f, dropHdr+"drop swatch into sign\n")
	wantExit(t, res, 0)
	mustHave(t, res, `slate: removed "Example Red Swatch" from sign`)
	if len(g.contents(signLocal)) != 0 {
		t.Errorf("the prim still holds %v", g.contents(signLocal))
	}
}

func TestDropSaysWhatItDidWhenTheStepFails(t *testing.T) {
	miss := "\nexpect texture sign face 5 is " + idTexB.String() + " within 200ms\n"
	f := newGrid(t)
	f.withDropping(t)
	res := play(t, f, dropHdr+"drop swatch into sign"+miss)
	wantExit(t, res, 1)
	mustHave(t, res, `dropped "Example Red Swatch" into sign`)
	f = newGrid(t)
	g := f.withDropping(t)
	res = play(t, f, dropHdr+"drop swatch onto sign face 2"+miss)
	mustHave(t, res, `dropped "Example Red Swatch" onto sign face 2; nothing went into the prim`)
	f = newGrid(t)
	g = f.withDropping(t)
	g.inv.setOwnerMask(idSwatchItem, permNoTrans)
	res = play(t, f, dropHdr+"drop swatch onto sign face 2"+miss)
	mustHave(t, res, "a copy went into the prim first")
	f = newGrid(t)
	g = f.withDropping(t)
	g.holds(signLocal, heldItem{id: idHeldItem, name: "Example Box", asset: idSwatchAsset, typ: "texture"})
	res = play(t, f, dropHdr+"drop swatch onto sign face 2"+miss)
	mustHave(t, res, "the prim already held it")
}

func TestDropIntoANoCopyItemIsRefusedBeforeAnythingIsSent(t *testing.T) {
	f := newGrid(t)
	g := f.withDropping(t)
	g.inv.setOwnerMask(idSwatchItem, permNoCopy)
	res := play(t, f, dropHdr+"drop swatch into sign\n")
	wantExit(t, res, 1)
	mustHave(t, res, `slate: step 1: "Example Red Swatch" may not be copied`, "the drop was not sent")
	if n := len(sentOf[*msg.UpdateTaskInventory](f)); n != 0 {
		t.Errorf("%d UpdateTaskInventory sent for a no-copy item", n)
	}
	mustNotHave(t, res, "slate: removed")
}

func TestDropIntoAPrimThatNeverShowsTheCopyFailsAndRemovesNothing(t *testing.T) {
	f := newGrid(t)
	g := f.withDropping(t)
	g.refuse = true
	res := play(t, f, dropHdr+"drop swatch into sign\nexpect texture sign face 5 is "+idTexA.String()+" within 300ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, `did not show in the contents of sign`)
	mustNotHave(t, res, "slate: removed")
	if n := len(sentOf[*msg.RemoveTaskInventory](f)); n != 0 {
		t.Errorf("%d removals for a copy that never showed", n)
	}
}

// TestDropIntoLeavesTheProductsOwnNearNameItem: a product that puts in an
// item of its own as the drop arrives, named like the drop with more after
// it, keeps it.  Only the name, or the name, a space and a number, of the
// item's type and asset, is the copy.
func TestDropIntoLeavesTheProductsOwnNearNameItem(t *testing.T) {
	f := newGrid(t)
	g := f.withDropping(t)
	g.product = []heldItem{
		{id: idHeldItem, name: "Example Red Swatch backup", asset: idSwatchAsset, typ: "texture"},
		{id: idBoxItem, name: "Example Red Swatch 2", asset: idSwatchAsset, typ: "object"},
	}
	res := play(t, f, dropHdr+"drop swatch into sign\n")
	wantExit(t, res, 0)
	mustHave(t, res, `slate: removed "Example Red Swatch" from sign`)
	if got := g.contents(signLocal); !reflect.DeepEqual(got, []string{"Example Red Swatch backup", "Example Red Swatch 2"}) {
		t.Errorf("the prim holds %v afterwards, want the product's two items", got)
	}
}

// TestDropIntoACopyThatComesLateIsRemovedAtTheEnd: the step gives up, the
// copy shows afterwards, and the end of the test finds and removes it.
func TestDropIntoACopyThatComesLateIsRemovedAtTheEnd(t *testing.T) {
	f := newGrid(t)
	g := f.withDropping(t)
	g.delay = 1300 * time.Millisecond
	res := play(t, f, dropHdr+"timeout 1s\nafter each {\n  wait 900ms\n}\ntest \"t\" {\n  drop swatch into sign\n}\n")
	wantExit(t, res, 1)
	mustHave(t, res, `did not show in the contents of sign`, `slate: removed "Example Red Swatch" from sign`)
	if got := g.contents(signLocal); len(got) != 0 {
		t.Errorf("the prim still holds %v", got)
	}
}

// TestDropIntoACopyThatNeverComesIsSaidToHaveNeverShown: the end of the
// test looks once more, finds nothing, and says so.
func TestDropIntoACopyThatNeverComesIsSaidToHaveNeverShown(t *testing.T) {
	f := newGrid(t)
	g := f.withDropping(t)
	g.refuse = true
	res := play(t, f, dropHdr+"timeout 1s\ndrop swatch into sign\n")
	wantExit(t, res, 1)
	mustHave(t, res, `slate: the copy of "Example Red Swatch" never showed in sign; nothing removed`)
	if n := len(sentOf[*msg.RemoveTaskInventory](f)); n != 0 {
		t.Errorf("%d removals for a copy that never showed", n)
	}
}

func TestDropIntoIsRemovedWhenTheTestFails(t *testing.T) {
	f := newGrid(t)
	g := f.withDropping(t)
	res := play(t, f, dropHdr+"drop swatch into sign\nexpect texture sign face 5 is "+idTexA.String()+" within 200ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, `slate: removed "Example Red Swatch" from sign`)
	if len(g.contents(signLocal)) != 0 {
		t.Errorf("the prim still holds %v", g.contents(signLocal))
	}
}

func TestDropIntoIsRemovedAfterAfterEach(t *testing.T) {
	f := newGrid(t)
	g := f.withDropping(t)
	res := play(t, f, dropHdr+"test \"t\" {\n  drop swatch into sign\n}\nafter each {\n  drop swatch into vendor\n}\n")
	wantExit(t, res, 0)
	mustHave(t, res, `slate: removed "Example Red Swatch" from sign`, `slate: removed "Example Red Swatch" from vendor`)
	if len(g.contents(signLocal)) != 0 || len(g.contents(vendorLocal)) != 0 {
		t.Errorf("sign %v, vendor %v", g.contents(signLocal), g.contents(vendorLocal))
	}
	// Each test removes its own, before the next one begins.
	f = newGrid(t)
	g = f.withDropping(t)
	res = play(t, f, dropHdr+"test \"a\" { drop swatch into sign }\ntest \"b\" { drop swatch into sign }\n")
	wantExit(t, res, 0)
	if i, j := strings.Index(res.Transcript, "slate: removed"), strings.Index(res.Transcript, `slate: test "b"`); i < 0 || i > j {
		t.Errorf("a's copy was not removed before b began:\n%s", res.Transcript)
	}
	if n := len(sentOf[*msg.RemoveTaskInventory](f)); n != 2 {
		t.Errorf("%d removals, want 2", n)
	}
}

func TestDropIntoALinkIsSentToThatPrim(t *testing.T) {
	f, _, _, _ := storeWorld(t)
	g := f.withDropping(t)
	res := play(t, f, dropHdr+"drop swatch into vendor link 2\n")
	wantExit(t, res, 0)
	u := sentOf[*msg.UpdateTaskInventory](f)
	if len(u) != 1 || u[0].UpdateData.LocalID != 201 {
		t.Errorf("sent %+v", u)
	}
	mustHave(t, res, `slate: removed "Example Red Swatch" from vendor link 2`)
	if len(g.contents(201)) != 0 {
		t.Errorf("the child still holds %v", g.contents(201))
	}
}

func TestDropIntoThatCannotBeRemovedFailsTheTest(t *testing.T) {
	f := newGrid(t)
	g := f.withDropping(t)
	g.stuck = true
	cfg := testCfg()
	// The removal is read back for as long as it is given; shorten it.
	res, err := tryPlayShort(t, f, dropHdr+"drop swatch into sign\n", cfg)
	if err != nil {
		t.Fatal(err)
	}
	wantExit(t, res, 1)
	mustHave(t, res, `slate: remove failed: "Example Red Swatch" from sign`, `slate: fail test "t": could not put back sign`)
	mustNotHave(t, res, `slate: pass test`)
}

// tryPlayShort is play with the undo bound shortened by a context of the
// test's own: the stuck removal is read back until the bound.
func tryPlayShort(t *testing.T, f *fakeGrid, src string, cfg runCfg) (*Result, error) {
	t.Helper()
	old := dropUndoFor
	dropUndoFor = 400 * time.Millisecond
	t.Cleanup(func() { dropUndoFor = old })
	return tryPlay(t, f, src, Options{}, cfg)
}

func swatchOnSign(f *fakeGrid) {
	f.change(signLocal, withTexture(2, idTexA))
	f.change(signLocal, withFace(2, func(fc *sl.Face) { fc.Colour = [4]uint8{200, 10, 10, 255}; fc.Glow = 40 }))
}

func TestDropOntoSetsTheFaceAndPutsItBack(t *testing.T) {
	f := newGrid(t)
	g := f.withDropping(t)
	swatchOnSign(f)
	res := play(t, f, dropHdr+"drop swatch onto sign face 2\nexpect texture sign face 2 is "+idSwatchAsset.String()+" within 1s\n")
	wantExit(t, res, 0)
	mustHave(t, res, "slate: restored sign face 2 to texture "+idTexA.String())
	// Copy and transfer: nothing goes into the prim.
	if n := len(sentOf[*msg.UpdateTaskInventory](f)) + len(sentOf[*msg.RemoveTaskInventory](f)); n != 0 {
		t.Errorf("%d inventory messages for a texture that may be copied and transferred", n)
	}
	img := sentOf[*msg.ObjectImage](f)
	if len(img) != 2 {
		t.Fatalf("%d ObjectImage messages, want the drop and the restore", len(img))
	}
	if got := f.faceTexture(signLocal, 2); got != idTexA {
		t.Errorf("face 2 is %s afterwards, want %s", got, idTexA)
	}
	// The rest of the face is kept, in the drop and in the restore.
	f.mu.Lock()
	var faces []sl.Face
	for _, o := range f.objects {
		if o.Local == signLocal {
			faces, _ = o.Faces(6)
		}
	}
	f.mu.Unlock()
	if faces[2].Colour != [4]uint8{200, 10, 10, 255} || faces[2].Glow != 40 {
		t.Errorf("the face's colour and glow were not kept: %+v", faces[2])
	}
	if len(g.contents(signLocal)) != 0 {
		t.Errorf("the prim holds %v", g.contents(signLocal))
	}
}

func TestDropOntoAFaceTexturedAlreadyChangesNothingToPutBack(t *testing.T) {
	f := newGrid(t)
	f.withDropping(t)
	f.change(signLocal, withTexture(2, idSwatchAsset))
	res := play(t, f, dropHdr+"drop swatch onto sign face 2\n")
	wantExit(t, res, 0)
	mustNotHave(t, res, "slate: restored")
	if n := len(sentOf[*msg.ObjectImage](f)); n != 1 {
		t.Errorf("%d ObjectImage messages, want the one of the drop", n)
	}
}

func TestDropOntoACopyOnlyTexturePutsACopyInFirst(t *testing.T) {
	f := newGrid(t)
	g := f.withDropping(t)
	g.inv.setOwnerMask(idSwatchItem, permNoTrans)
	swatchOnSign(f)
	res := play(t, f, dropHdr+"drop swatch onto sign face 2\n")
	wantExit(t, res, 0)
	mustHave(t, res, "slate: restored sign face 2", `slate: removed "Example Red Swatch" from sign`)
	// In order: the copy, the texture; then the texture back, the copy out.
	var order []string
	f.mu.Lock()
	for _, m := range f.sent {
		switch m.(type) {
		case *msg.UpdateTaskInventory:
			order = append(order, "put")
		case *msg.ObjectImage:
			order = append(order, "image")
		case *msg.RemoveTaskInventory:
			order = append(order, "remove")
		}
	}
	f.mu.Unlock()
	if want := []string{"put", "image", "image", "remove"}; !reflect.DeepEqual(order, want) {
		t.Errorf("sent %v, want %v", order, want)
	}
	if got := f.faceTexture(signLocal, 2); got != idTexA {
		t.Errorf("face 2 is %s afterwards", got)
	}
	if len(g.contents(signLocal)) != 0 {
		t.Errorf("the prim holds %v", g.contents(signLocal))
	}
}

func TestDropOntoANoCopyTextureIsRefusedAndTheFaceIsKept(t *testing.T) {
	f := newGrid(t)
	g := f.withDropping(t)
	g.inv.setOwnerMask(idSwatchItem, permNoCopy)
	swatchOnSign(f)
	res := play(t, f, dropHdr+"drop swatch onto sign face 2\n")
	wantExit(t, res, 1)
	mustHave(t, res, `"Example Red Swatch" may not be copied`, "the face was not changed")
	if n := len(sentOf[*msg.UpdateTaskInventory](f)) + len(sentOf[*msg.ObjectImage](f)); n != 0 {
		t.Errorf("%d messages sent for a refused drop", n)
	}
	mustNotHave(t, res, "slate: restored")
}

func TestDropOntoATextureThePrimHoldsNeedsNoCopy(t *testing.T) {
	f := newGrid(t)
	g := f.withDropping(t)
	g.inv.setOwnerMask(idSwatchItem, permNoCopy)
	g.holds(signLocal, heldItem{id: idHeldItem, name: "Example Box", asset: idSwatchAsset, typ: "texture"})
	swatchOnSign(f)
	res := play(t, f, dropHdr+"drop swatch onto sign face 2\n")
	wantExit(t, res, 0)
	mustHave(t, res, "slate: restored sign face 2")
	if n := len(sentOf[*msg.UpdateTaskInventory](f)) + len(sentOf[*msg.RemoveTaskInventory](f)); n != 0 {
		t.Errorf("%d inventory messages for a texture the prim held", n)
	}
	if got := g.contents(signLocal); !reflect.DeepEqual(got, []string{"Example Box"}) {
		t.Errorf("the prim holds %v", got)
	}
}

func TestDropOntoANonTextureFailsTheStep(t *testing.T) {
	f := newGrid(t)
	g := f.withDropping(t)
	g.inv.add(idObjectsFolder, idBoxItem, "Example Box", 6)
	res := play(t, f, hdr+"item box is \"Example Box\" in \"Objects\"\ndrop box onto sign face 1\n")
	wantExit(t, res, 1)
	mustHave(t, res, `slate: step 1: "Example Box" is not a texture`, "the drop was not sent")
	if n := len(sentOf[*msg.UpdateTaskInventory](f)) + len(sentOf[*msg.ObjectImage](f)); n != 0 {
		t.Errorf("%d messages sent", n)
	}
	// Into takes any item that may be copied.
	f = newGrid(t)
	g = f.withDropping(t)
	g.inv.add(idObjectsFolder, idBoxItem, "Example Box", 6)
	g.inv.setOwnerMask(idBoxItem, permAll)
	wantExit(t, play(t, f, hdr+"item box is \"Example Box\" in \"Objects\"\ndrop box into sign\n"), 0)
}

func TestDropOntoAFaceThePrimDoesNotHaveFailsTheStep(t *testing.T) {
	f := newGrid(t)
	f.withDropping(t)
	res := play(t, f, dropHdr+"drop swatch onto sign face 7\n")
	wantExit(t, res, 1)
	mustHave(t, res, "sign has 6 faces, so there is no face 7")
	if n := len(sentOf[*msg.ObjectImage](f)); n != 0 {
		t.Errorf("%d ObjectImage sent", n)
	}
}

func TestDropOntoALinkTexturesThatPrim(t *testing.T) {
	f, _, _, _ := storeWorld(t)
	f.withDropping(t)
	res := play(t, f, dropHdr+"drop swatch onto vendor link 2 face 1\n")
	wantExit(t, res, 0)
	img := sentOf[*msg.ObjectImage](f)
	if len(img) != 2 || img[0].ObjectData[0].ObjectLocalID != 201 {
		t.Errorf("sent %+v", img)
	}
	mustHave(t, res, "slate: restored vendor link 2 face 1")
}

// The two worked examples of doc/slate-language.md.
func TestTheDropAndGroupWorkedExamplesCheck(t *testing.T) {
	mustCheck(t, `slate 1

object box is "Example Box"
object panel is "Example Panel"
item swatch is "Example Red Swatch" in "Objects"

test "an item dropped in is noticed" {
  drop swatch into box
  expect say "got it" on public from object box within 5s
}

test "a texture dropped on a face is noticed" {
  drop swatch onto panel face 2
  expect say "painted" on public from object panel within 5s
}
`)
	mustCheck(t, `slate 1

object panel is "Example Panel"
avatar visitor

test "a member is let in" {
  group visitor "Example Group"
  touch panel anywhere as visitor
  expect say "welcome" on public from object panel within 5s
}

test "an avatar with no group is turned away" {
  group visitor none
  touch panel anywhere as visitor
  expect say "members only" on public from object panel within 5s
}
`)
}
