package session

// The layout of the auto objects and the limit on how many are worn.
//
// These run over fakeGrid, which now answers a wear and a take-off the
// way a region does: the object appears, or stops being listed, and the
// item stays in inventory.  Its wearLimit is the region's silent refusal.
// What is checked is the decisions made here: what is counted as worn,
// what is asked for, and where each object is sent.
// Why: doc/slots.md#setting-up-and-the-limit

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// setWorn makes exactly these slots worn, each on the point given, and
// every one of the pool's twenty-four items exist.
func (f *fakeGrid) setWorn(points map[int]int) {
	f.stock(AutoPool())
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects = nil
	for slot, point := range points {
		f.objects = append(f.objects, &sl.Seen{
			Object:      sl.Object{ID: msg.UUID(autoItemID(slot)), Local: uint32(100 + slot)},
			AttachItem:  autoItemID(slot),
			AttachPoint: point,
			Scale:       msg.Vector3{X: 0.2, Y: 0.2, Z: 0.2},
		})
	}
}

// wearOwn makes n attachments that are not auto objects worn and
// described: the owner's own.
func (f *fakeGrid) wearOwn(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := 0; i < n; i++ {
		f.objects = append(f.objects, &sl.Seen{
			Object:      sl.Object{ID: ownObjectID(i), Local: uint32(900 + i)},
			AttachItem:  ownItemID(i),
			AttachPoint: 1,
		})
	}
}

// linkOwn makes n more of the owner's own attachments that the region has
// NOT described, as after a login: they are inventory items with a link
// each in the Current Outfit folder, and nothing else.
func (f *fakeGrid) linkOwn(first, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	objects := findDir(f.inv, testObjects)
	cof := findDir(f.inv, testOutfit)
	for i := first; i < first+n; i++ {
		name := fmt.Sprintf("Some HUD %d", i)
		objects.Items = append(objects.Items, &invItem{ID: ownItemID(i), Name: name, Type: int(sl.AssetObject)})
		cof.Items = append(cof.Items, &invItem{
			ID: linkID(i), Name: name, Type: int(sl.AssetLink), IsLink: true, Asset: ownItemID(i),
		})
	}
}

// linkAuto puts a link to an auto item in the Current Outfit folder.
func (f *fakeGrid) linkAuto(slot int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cof := findDir(f.inv, testOutfit)
	cof.Items = append(cof.Items, &invItem{
		ID: linkID(100 + slot), Name: AutoName(slot), Type: int(sl.AssetLink),
		IsLink: true, Asset: autoItemID(slot),
	})
}

// outfitLinks is how many links the Current Outfit folder holds.
func (f *fakeGrid) outfitLinks() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(findDir(f.inv, testOutfit).Items)
}

// serveBake answers the rebake request and counts the asks.
func (f *fakeGrid) serveBake(t *testing.T) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	f.ServeCap(t, "UpdateAvatarAppearance", func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("Content-Type", "application/llsd+xml")
		io.WriteString(w, `<llsd><map><key>success</key><boolean>1</boolean></map></llsd>`)
	})
	return &n
}

// placement is what one MultipleObjectUpdate asked for.
type placement struct {
	Local          uint32
	At, Rot, Scale msg.Vector3
}

// placements are the Place calls that went out, in order.
func placements(t *testing.T, f *fakeGrid) []placement {
	t.Helper()
	var out []placement
	for _, m := range f.Sent() {
		u, ok := m.(*msg.MultipleObjectUpdate)
		if !ok {
			continue
		}
		for _, d := range u.ObjectData {
			if len(d.Data) != 36 {
				t.Fatalf("a Place carried %d bytes, want 36", len(d.Data))
			}
			v := func(i int) msg.Vector3 {
				f := func(j int) float32 {
					return math.Float32frombits(binary.LittleEndian.Uint32(d.Data[i*12+j*4:]))
				}
				return msg.Vector3{X: f(0), Y: f(1), Z: f(2)}
			}
			out = append(out, placement{Local: d.ObjectLocalID, At: v(0), Rot: v(1), Scale: v(2)})
		}
	}
	return out
}

func near32(a, b float32) bool { return math.Abs(float64(a-b)) < 1e-5 }

func sameVector(a, b msg.Vector3) bool {
	return near32(a.X, b.X) && near32(a.Y, b.Y) && near32(a.Z, b.Z)
}

// sentOf counts the messages of one kind that went out.
func sentOf[T msg.Message](f *fakeGrid) []T {
	var out []T
	for _, m := range f.Sent() {
		if v, ok := m.(T); ok {
			out = append(out, v)
		}
	}
	return out
}

// quick makes the waits short for a test that does not mean to wait.
func quick(t *testing.T) {
	t.Helper()
	wear, off, settle := autoWearWait, autoOffWait, autoUndescribedSettle
	autoWearWait, autoOffWait, autoUndescribedSettle = 400*time.Millisecond, 2*time.Second, 0
	t.Cleanup(func() { autoWearWait, autoOffWait, autoUndescribedSettle = wear, off, settle })
}

// wornPoint is the point an item is worn on, or -1 if it is not worn.
func (f *fakeGrid) wornPoint(slot int) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, o := range f.objects {
		if o.AttachItem == autoItemID(slot) {
			return o.AttachPoint
		}
	}
	return -1
}

// wornCount is how many attachments the region lists, whoever's.
func (f *fakeGrid) wornCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, o := range f.objects {
		if !o.AttachItem.IsZero() {
			n++
		}
	}
	return n
}

// captureStderr runs fn and returns what it wrote to standard error.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	was := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	func() {
		defer func() { os.Stderr = was; w.Close() }()
		fn()
	}()
	return <-done
}

var parked = AutoParked
var cube = msg.Vector3{X: AutoSize, Y: AutoSize, Z: AutoSize}

// TestSetupWearsOnBottomLeftParkedAndSized: the first n are put on with
// the add bit over Bottom Left, and each is then sent to the parked spot
// at the size of a cube.  One Place does both, and it is sent for the
// object that went on, not for an id found some other way.
func TestSetupWearsOnBottomLeftParkedAndSized(t *testing.T) {
	quick(t)
	s, f := newFakeSession(t)
	f.setWorn(nil)

	rep, err := SetupAuto(context.Background(), s, 4)
	if err != nil {
		t.Fatalf("SetupAuto: %v", err)
	}
	if len(rep.Placed) != 4 || rep.Note != "" {
		t.Fatalf("placed %d, note %q", len(rep.Placed), rep.Note)
	}
	for _, r := range sentOf[*msg.RezSingleAttachmentFromInv](f) {
		if r.ObjectData.AttachmentPt != uint8(sl.HUDBottomLeft|sl.AttachAdd) {
			t.Errorf("worn on point byte %#x, want Bottom Left with the add bit", r.ObjectData.AttachmentPt)
		}
	}
	ps := placements(t, f)
	if len(ps) != 4 {
		t.Fatalf("%d objects placed, want 4", len(ps))
	}
	for i, p := range ps {
		if !sameVector(p.At, parked) || !sameVector(p.Scale, cube) || !sameVector(p.Rot, msg.Vector3{}) {
			t.Errorf("object %d placed at %v rotated %v scaled %v, want parked at a cube",
				i, p.At, p.Rot, p.Scale)
		}
		if rep.Placed[i].Object.Local != p.Local {
			t.Errorf("slot %d: placed local %d, but it is local %d", i, p.Local, rep.Placed[i].Object.Local)
		}
	}
	for i := 0; i < 4; i++ {
		if got := f.wornPoint(i); got != sl.HUDBottomLeft {
			t.Errorf("slot %d is worn on point %d", i, got)
		}
	}
}

// TestSetupMovesWhatIsOnAnotherPoint: an object from the old spread is
// taken off and worn again on Bottom Left, and one already there is left
// on and only placed.
func TestSetupMovesWhatIsOnAnotherPoint(t *testing.T) {
	quick(t)
	s, f := newFakeSession(t)
	f.serveBake(t)
	f.setWorn(map[int]int{0: sl.HUDBottomLeft, 1: sl.HUDBottom, 2: sl.HUDCenter2})

	rep, err := SetupAuto(context.Background(), s, 3)
	if err != nil {
		t.Fatalf("SetupAuto: %v", err)
	}
	if rep.Moved != 2 || rep.Removed != 0 {
		t.Errorf("moved %d, removed %d, want 2 and 0", rep.Moved, rep.Removed)
	}
	off := sentOf[*msg.DetachAttachmentIntoInv](f)
	if len(off) != 2 {
		t.Fatalf("%d taken off, want the two on other points", len(off))
	}
	for _, d := range off {
		if d.ObjectData.ItemID == autoItemID(0) {
			t.Error("the object already on Bottom Left was taken off")
		}
	}
	if got := len(sentOf[*msg.RezSingleAttachmentFromInv](f)); got != 2 {
		t.Errorf("%d worn, want the two that moved", got)
	}
	for i := 0; i < 3; i++ {
		if got := f.wornPoint(i); got != sl.HUDBottomLeft {
			t.Errorf("slot %d is worn on point %d, want Bottom Left", i, got)
		}
	}
	if got := len(placements(t, f)); got != 3 {
		t.Errorf("%d placed, want all three", got)
	}
}

// TestSetupTakesOffWhatIsBeyondN: the ones beyond n come off with their
// links in the Current Outfit folder, one rebake follows, and an
// attachment that is not an auto object is not touched.
func TestSetupTakesOffWhatIsBeyondN(t *testing.T) {
	quick(t)
	s, f := newFakeSession(t)
	bakes := f.serveBake(t)
	f.setWorn(map[int]int{0: sl.HUDBottomLeft, 1: sl.HUDBottomLeft, 2: sl.HUDBottomLeft, 3: sl.HUDTop})
	f.wearOwn(2)
	f.linkAuto(2)
	f.linkAuto(3)

	rep, err := SetupAuto(context.Background(), s, 2)
	if err != nil {
		t.Fatalf("SetupAuto: %v", err)
	}
	if rep.Removed != 2 || len(rep.Placed) != 2 {
		t.Errorf("removed %d, placed %d, want 2 and 2", rep.Removed, len(rep.Placed))
	}
	if f.wornPoint(2) != -1 || f.wornPoint(3) != -1 {
		t.Error("an object beyond n is still worn")
	}
	if got := f.outfitLinks(); got != 0 {
		t.Errorf("%d links are left in the Current Outfit folder", got)
	}
	if got := bakes.Load(); got != 1 {
		t.Errorf("rebaked %d times, want once at the end", got)
	}
	if got := f.wornCount(); got != 4 {
		t.Errorf("the region lists %d attachments, want the 2 auto and the 2 of the owner's", got)
	}
}

// TestSetupWearsAnObjectOnlyTheOutfitKnows: after a login the region has
// not described a HUD, so it has no local id to move; it counts as worn
// and is taken off and put on again.
func TestSetupWearsAnObjectOnlyTheOutfitKnows(t *testing.T) {
	quick(t)
	s, f := newFakeSession(t)
	f.serveBake(t)
	f.setWorn(nil)
	f.linkAuto(1)

	rep, err := SetupAuto(context.Background(), s, 2)
	if err != nil {
		t.Fatalf("SetupAuto: %v", err)
	}
	if rep.Moved != 1 || len(rep.Placed) != 2 {
		t.Errorf("moved %d, placed %d, want 1 and 2", rep.Moved, len(rep.Placed))
	}
	if got := f.wornPoint(1); got != sl.HUDBottomLeft {
		t.Errorf("auto 2 is on point %d", got)
	}
	if got := f.outfitLinks(); got != 0 {
		t.Errorf("its old link was left in the outfit folder (%d)", got)
	}
}

// TestSetupStopsWhereTheAvatarIsFull: an avatar already wearing 30 is
// asked for 12 and gets 7, which is where 37 worn in all comes to, one
// short of the 38 the region allows.  The report says how many and why,
// and nothing past the room is asked of the region.  Thirty counts a HUD
// that only the outfit folder knows about, as after a login.
func TestSetupStopsWhereTheAvatarIsFull(t *testing.T) {
	quick(t)
	s, f := newFakeSession(t)
	f.setWorn(nil)
	f.wearOwn(25)
	f.linkOwn(25, 5)

	rep, err := SetupAuto(context.Background(), s, 12)
	if err != nil {
		t.Fatalf("SetupAuto: %v", err)
	}
	if len(rep.Placed) != 7 {
		t.Errorf("wore %d, want 7", len(rep.Placed))
	}
	want := "wore 7 of 12: the avatar wears 37, keeping 1 slot free"
	if rep.Note != want {
		t.Errorf("note = %q, want %q", rep.Note, want)
	}
	if got := len(sentOf[*msg.RezSingleAttachmentFromInv](f)); got != 7 {
		t.Errorf("%d wears were asked for, want only the 7 that fit", got)
	}
	if got := f.wornCount(); got != 32 {
		t.Errorf("the region lists %d, want the 25 described and 7 new", got)
	}
}

// TestSetupSaysWhenTheRegionRefusesAtTheLimit: a wear past the limit is
// refused without a word, so it only shows as one that is never
// confirmed.  Setup waits that out once, stops, and says so.  The region
// here refuses at 30 while saying nothing of it, so the count is no use.
func TestSetupSaysWhenTheRegionRefusesAtTheLimit(t *testing.T) {
	quick(t)
	s, f := newFakeSession(t)
	f.setWorn(nil)
	f.wearOwn(28)
	f.mu.Lock()
	f.wearLimit = 30
	f.mu.Unlock()

	rep, err := SetupAuto(context.Background(), s, 6)
	if err != nil {
		t.Fatalf("SetupAuto: %v", err)
	}
	if len(rep.Placed) != 2 {
		t.Errorf("wore %d, want the 2 the region let on", len(rep.Placed))
	}
	for _, want := range []string{"wore 2 of 6", "auto 3", "refuses a wear past its limit", "38"} {
		if !strings.Contains(rep.Note, want) {
			t.Errorf("note %q lacks %q", rep.Note, want)
		}
	}
	if got := len(sentOf[*msg.RezSingleAttachmentFromInv](f)); got != 3 {
		t.Errorf("%d wears were asked for, want 2 that worked and the 1 that was refused", got)
	}
}

// TestSetupRestoresWhatItTookOffWhateverTheCount: an avatar that is
// already past the reserve and has an auto object on another point gets
// that object back on Bottom Left, since it was worn before and the
// avatar is no fuller; it just wears nothing new.
func TestSetupRestoresWhatItTookOffWhateverTheCount(t *testing.T) {
	quick(t)
	s, f := newFakeSession(t)
	f.serveBake(t)
	f.setWorn(map[int]int{0: sl.HUDBottom})
	f.wearOwn(37)

	rep, err := SetupAuto(context.Background(), s, 3)
	if err != nil {
		t.Fatalf("SetupAuto: %v", err)
	}
	if len(rep.Placed) != 1 || f.wornPoint(0) != sl.HUDBottomLeft {
		t.Errorf("placed %d, auto is on point %d, want it moved to Bottom Left", len(rep.Placed), f.wornPoint(0))
	}
	if !strings.HasPrefix(rep.Note, "wore 1 of 3: the avatar wears 38") {
		t.Errorf("note = %q", rep.Note)
	}
}

// TestALeaseKeepsFourSlotsFree: the same avatar wearing 30 is given 4 of
// 12 by a lease, which stops at 34 worn in all, says so, and does not
// fail for it.  Objects already worn are used whatever the count.
func TestALeaseKeepsFourSlotsFree(t *testing.T) {
	quick(t)
	s, f := newFakeSession(t)
	f.setWorn(nil)
	f.wearOwn(25)
	f.linkOwn(25, 5)

	slots := make([]int, 12)
	for i := range slots {
		slots[i] = i
	}
	var a *Auto
	var err error
	said := captureStderr(t, func() { a, err = wearSlots(context.Background(), s, slots, nil) })
	if err != nil {
		t.Fatalf("wearSlots: %v", err)
	}
	if len(a.Objects) != 4 || len(a.Slots) != 4 {
		t.Errorf("a lease of 12 got %d objects, want 4", len(a.Objects))
	}
	if !strings.Contains(said, "wore 4 of 12: keeping 4 slots free") {
		t.Errorf("the run was told %q", said)
	}
	if got := f.wornCount(); got != 29 {
		t.Errorf("the region lists %d attachments, want 25 and the 4 the lease wore", got)
	}

	// Those four are worn now.  Asking again uses them whatever the
	// count, and has nothing to say.
	f.wearOwn(0)
	said = captureStderr(t, func() { a, err = wearSlots(context.Background(), s, slots[:4], nil) })
	if err != nil || len(a.Objects) != 4 || said != "" {
		t.Errorf("a lease of the four already worn: %d objects, %v, said %q", len(a.Objects), err, said)
	}
}

// TestALeaseWithNoRoomAtAllFails: a run given nothing has nowhere to run,
// and is told why instead of being handed an empty set.
func TestALeaseWithNoRoomAtAllFails(t *testing.T) {
	quick(t)
	s, f := newFakeSession(t)
	f.setWorn(nil)
	f.wearOwn(34)

	_, err := wearSlots(context.Background(), s, fourSlots, nil)
	if err == nil || !strings.Contains(err.Error(), "wore 0 of 4: keeping 4 slots free") {
		t.Errorf("wearSlots = %v, want it to say it kept four slots free", err)
	}
}

// TestALeaseParksWhatItPutOnAndLeavesWhatWasWorn: an object the lease had
// to put on is sent to the parked spot at a cube's size; one already worn
// is left exactly where it is, so what `auto show` laid out survives a
// run.
func TestALeaseParksWhatItPutOnAndLeavesWhatWasWorn(t *testing.T) {
	quick(t)
	s, f := newFakeSession(t)
	f.setWorn(map[int]int{0: sl.HUDBottomLeft})

	a, err := wearSlots(context.Background(), s, []int{0, 1, 2}, nil)
	if err != nil {
		t.Fatalf("wearSlots: %v", err)
	}
	ps := placements(t, f)
	if len(ps) != 2 {
		t.Fatalf("%d objects placed, want the two put on", len(ps))
	}
	for _, p := range ps {
		if p.Local == a.Objects[0].Local {
			t.Error("the object that was already worn was moved")
		}
		if !sameVector(p.At, parked) || !sameVector(p.Scale, cube) {
			t.Errorf("placed at %v size %v, want parked at a cube", p.At, p.Scale)
		}
	}
}

// TestArrangingPlacesEveryWornObject: reset parks and sizes, hide parks
// and keeps the size, show tiles along the bottom of the screen and keeps
// the size.  The size is the one the region says the object has.
func TestArrangingPlacesEveryWornObject(t *testing.T) {
	quick(t)
	for _, tc := range []struct {
		name string
		how  AutoLayout
		at   func(slot int) msg.Vector3
		size msg.Vector3
	}{
		{"reset", AutoReset, func(int) msg.Vector3 { return parked }, cube},
		{"hide", AutoHide, func(int) msg.Vector3 { return parked }, msg.Vector3{X: 0.2, Y: 0.2, Z: 0.2}},
		{"show", AutoShow, AutoTile, msg.Vector3{X: 0.2, Y: 0.2, Z: 0.2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newFakeSession(t)
			all := map[int]int{}
			for i := 0; i < AutoPool(); i++ {
				all[i] = sl.HUDBottomLeft
			}
			f.setWorn(all)
			f.wearOwn(3)

			rep, err := ArrangeAuto(context.Background(), s, tc.how)
			if err != nil {
				t.Fatalf("ArrangeAuto: %v", err)
			}
			if len(rep.Placed) != 24 {
				t.Fatalf("placed %d, want 24", len(rep.Placed))
			}
			ps := placements(t, f)
			if len(ps) != 24 {
				t.Fatalf("%d Place calls, want 24 and none for the owner's three", len(ps))
			}
			for _, p := range ps {
				slot := int(p.Local) - 100
				if !sameVector(p.At, tc.at(slot)) || !sameVector(p.Scale, tc.size) {
					t.Errorf("slot %d placed at %v size %v, want %v size %v",
						slot, p.At, p.Scale, tc.at(slot), tc.size)
				}
			}
		})
	}
}

// TestShowTilesAlongTheBottomOfTheScreen: slot i at <0, -0.025 - 0.05 i,
// 0.025>, so that twenty-four make one row 1.2 m long.
func TestShowTilesAlongTheBottomOfTheScreen(t *testing.T) {
	t.Parallel()
	for slot, want := range map[int]msg.Vector3{
		0:  {X: 0, Y: -0.025, Z: 0.025},
		1:  {X: 0, Y: -0.075, Z: 0.025},
		23: {X: 0, Y: -1.175, Z: 0.025},
	} {
		if got := AutoTile(slot); !sameVector(got, want) {
			t.Errorf("AutoTile(%d) = %v, want %v", slot, got, want)
		}
	}
	if end := AutoTile(23).Y - AutoSize/2; !near32(end, -1.2) {
		t.Errorf("twenty-four tiles end at %v, want a row 1.2 m long", end)
	}
	if AutoParked.Y <= 0 || AutoParked.Z >= 0 {
		t.Errorf("parked at %v, want left of and below the corner", AutoParked)
	}
}

// TestArrangingWearsOnBottomLeftWhatIsNot: an object on another point, or
// one the region has not described, cannot be put at a spot relative to
// Bottom Left, so it is taken off and worn there first.
func TestArrangingWearsOnBottomLeftWhatIsNot(t *testing.T) {
	quick(t)
	s, f := newFakeSession(t)
	f.serveBake(t)
	f.setWorn(map[int]int{0: sl.HUDBottomLeft, 1: sl.HUDTop})
	f.linkAuto(2)

	rep, err := ArrangeAuto(context.Background(), s, AutoShow)
	if err != nil {
		t.Fatalf("ArrangeAuto: %v", err)
	}
	if rep.Moved != 2 || len(rep.Placed) != 3 {
		t.Errorf("moved %d, placed %d, want 2 and 3", rep.Moved, len(rep.Placed))
	}
	for i := 0; i < 3; i++ {
		if got := f.wornPoint(i); got != sl.HUDBottomLeft {
			t.Errorf("slot %d is on point %d", i, got)
		}
	}
}

// TestClearTakesOffEveryAutoObjectOnAnyPoint: and its link, and nothing
// else, and rebakes once.
func TestClearTakesOffEveryAutoObjectOnAnyPoint(t *testing.T) {
	quick(t)
	s, f := newFakeSession(t)
	bakes := f.serveBake(t)
	f.setWorn(map[int]int{0: sl.HUDBottomLeft, 1: sl.HUDTop, 5: sl.HUDCenter2})
	f.wearOwn(3)
	f.linkAuto(1)
	f.linkAuto(7) // only the outfit knows this one
	f.linkOwn(10, 1)

	rep, err := ClearAuto(context.Background(), s)
	if err != nil {
		t.Fatalf("ClearAuto: %v", err)
	}
	if rep.Removed != 4 {
		t.Errorf("took off %d, want 4", rep.Removed)
	}
	if got := f.wornCount(); got != 3 {
		t.Errorf("%d are still listed, want the owner's 3", got)
	}
	if got := f.outfitLinks(); got != 1 {
		t.Errorf("%d links are left, want the owner's 1", got)
	}
	if got := bakes.Load(); got != 1 {
		t.Errorf("rebaked %d times, want once", got)
	}
}

// TestDeleteMovesOnlyExactAutoNamesToTheTrash: "auto" and "auto N" go;
// "autobench" and "automate" and whatever else are not touched, and the
// Trash is not emptied.
func TestDeleteMovesOnlyExactAutoNamesToTheTrash(t *testing.T) {
	quick(t)
	s, f := newFakeSession(t)
	f.serveBake(t)
	f.setWorn(map[int]int{0: sl.HUDBottomLeft, 1: sl.HUDBottomLeft})
	f.mu.Lock()
	objects := findDir(f.inv, testObjects)
	objects.Items = objects.Items[:3] // auto, auto 2, auto 3
	for i, name := range []string{"autobench", "automate", "auto x", "an auto", "aut"} {
		objects.Items = append(objects.Items, &invItem{
			ID:   msg.MustParseUUID(fmt.Sprintf("b8cf7e57-7e57-c0de-1c55-%012d", i)),
			Name: name, Type: int(sl.AssetObject),
		})
	}
	trash := findDir(f.inv, testTrash)
	trash.Items = append(trash.Items, &invItem{
		ID: msg.MustParseUUID("d8427e57-7e57-c0de-1ce0-a143a2fb55bc"), Name: "old", Type: int(sl.AssetObject),
	})
	f.mu.Unlock()

	rep, err := DeleteAuto(context.Background(), s)
	if err != nil {
		t.Fatalf("DeleteAuto: %v", err)
	}
	if rep.Removed != 2 || rep.Trashed != 3 {
		t.Errorf("took off %d and trashed %d, want 2 and 3", rep.Removed, rep.Trashed)
	}
	var left, binned []string
	f.mu.Lock()
	for _, it := range findDir(f.inv, testObjects).Items {
		left = append(left, it.Name)
	}
	for _, it := range findDir(f.inv, testTrash).Items {
		binned = append(binned, it.Name)
	}
	f.mu.Unlock()
	if strings.Join(left, ",") != "autobench,automate,auto x,an auto,aut" {
		t.Errorf("Objects holds %v afterwards", left)
	}
	if strings.Join(binned, ",") != "old,auto,auto 2,auto 3" {
		t.Errorf("the Trash holds %v afterwards", binned)
	}
}

// TestEverythingThatMovesThemTakesAllTheSlotsFirst: while any place is
// held by something running, none of -n, reset, show, hide, clear and
// delete does anything, not even read, and each says who has them.
func TestEverythingThatMovesThemTakesAllTheSlotsFirst(t *testing.T) {
	for name, do := range map[string]func(context.Context, *grantingGrid, *sl.Session) error{
		"-n": func(ctx context.Context, g *grantingGrid, s *sl.Session) error {
			_, err := SetupAuto(ctx, s, 3)
			return err
		},
		"reset": func(ctx context.Context, g *grantingGrid, s *sl.Session) error {
			_, err := ArrangeAuto(ctx, s, AutoReset)
			return err
		},
		"show": func(ctx context.Context, g *grantingGrid, s *sl.Session) error {
			_, err := ArrangeAuto(ctx, s, AutoShow)
			return err
		},
		"hide": func(ctx context.Context, g *grantingGrid, s *sl.Session) error {
			_, err := ArrangeAuto(ctx, s, AutoHide)
			return err
		},
		"clear": func(ctx context.Context, g *grantingGrid, s *sl.Session) error {
			_, err := ClearAuto(ctx, s)
			return err
		},
		"delete": func(ctx context.Context, g *grantingGrid, s *sl.Session) error {
			_, err := DeleteAuto(ctx, s)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			quick(t)
			s, g := newGranting(t, "quark")
			g.fakeGrid.setWorn(map[int]int{0: sl.HUDBottomLeft})
			g.free["quark"] = 5 // somebody is running

			err := do(context.Background(), g, s)
			if err == nil || !strings.Contains(err.Error(), "in use") ||
				!strings.Contains(err.Error(), "only 5 free") {
				t.Fatalf("%s while a place was held: %v, want a refusal that says so", name, err)
			}
			if sent := g.fakeGrid.Sent(); len(sent) != 0 {
				t.Errorf("%d messages went out while places were held", len(sent))
			}
			if got := g.gaveBack(); len(got) != 0 {
				t.Errorf("gave back %v, having taken nothing", got)
			}

			// Free, it goes ahead, and gives the places back not clean.
			g.free["quark"] = SlotsPerAgentForTest
			g.fakeGrid.serveBake(t)
			if err := do(context.Background(), g, s); err != nil {
				t.Fatalf("%s with nothing running: %v", name, err)
			}
			if got := g.gaveBack(); len(got) != 1 || !strings.HasSuffix(got[0], ":false") {
				t.Errorf("gave back %v, want the one grant, not clean", got)
			}
		})
	}
}

// TestTheCountIsTheUnionOfTheOutfitAndTheDescribed: an auto object that
// only the outfit folder knows about counts, one that is only described
// counts, and one that is both counts once.
func TestTheCountIsTheUnionOfTheOutfitAndTheDescribed(t *testing.T) {
	s, f := newFakeSession(t)
	f.setWorn(map[int]int{0: sl.HUDBottomLeft, 1: sl.HUDBottomLeft})
	f.linkAuto(1) // both
	f.linkAuto(2) // the outfit alone
	f.wearOwn(2)

	n, err := CountAuto(context.Background(), s)
	if err != nil || n != 3 {
		t.Errorf("CountAuto = %d, %v, want 3", n, err)
	}
	st, err := readWorn(context.Background(), s, testObjects)
	if err != nil {
		t.Fatal(err)
	}
	if st.total() != 5 {
		t.Errorf("the avatar wears %d in all, want 5", st.total())
	}
}

// TestALinkToSomethingKeptDeepStillCounts: Outfit walks only a few
// folders down, so an attachment kept deeper has a link whose target is
// not found.  The link's own inventory type still says object, and it is
// counted; a wearable's link, found or not, is not.
func TestALinkToSomethingKeptDeepStillCounts(t *testing.T) {
	s, f := newFakeSession(t)
	f.setWorn(nil)
	f.mu.Lock()
	cof := findDir(f.inv, testOutfit)
	cof.Items = append(cof.Items,
		&invItem{ID: linkID(200), Name: "Example Box", Type: int(sl.AssetLink),
			IsLink: true, Asset: ownItemID(200), InvType: sl.InvTypeObject},
		&invItem{ID: linkID(201), Name: "Example Red Swatch", Type: int(sl.AssetLink),
			IsLink: true, Asset: ownItemID(201), InvType: 18})
	f.mu.Unlock()

	st, err := readWorn(context.Background(), s, testObjects)
	if err != nil {
		t.Fatal(err)
	}
	if st.total() != 1 {
		t.Errorf("the avatar wears %d in all, want 1: the deep HUD's link and not the shirt's", st.total())
	}
}

// TestSetupMakesItsLimitFromWhatTheRegionSays: 38 when the simulator does
// not say, and what it says when it does.
func TestSetupMakesItsLimitFromWhatTheRegionSays(t *testing.T) {
	quick(t)
	s, f := newFakeSession(t)
	if got := attachmentLimit(context.Background(), s); got != 38 {
		t.Errorf("the limit is %d with nothing said, want 38", got)
	}
	f.ServeCap(t, "SimulatorFeatures", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/llsd+xml")
		io.WriteString(w, `<llsd><map><key>MaxAgentAttachments</key><integer>20</integer></map></llsd>`)
	})
	if got := attachmentLimit(context.Background(), s); got != 20 {
		t.Errorf("the limit is %d, want the 20 the region said", got)
	}
	f.setWorn(nil)
	f.wearOwn(15)
	rep, err := SetupAuto(context.Background(), s, 12)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Placed) != 4 || !strings.Contains(rep.Note, "wore 4 of 12: the avatar wears 19") {
		t.Errorf("wore %d, note %q, want 4 and 19 worn in all", len(rep.Placed), rep.Note)
	}
}
