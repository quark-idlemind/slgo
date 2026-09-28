package main

// landmark, driven over a grid that is not there.
//
// The whole of what this command has to get right is which uuid it
// hands over.  An item has two of them, the grid takes one, and it
// answers the other with perfect silence -- so a test that only checked
// that a teleport went out would pass for the command that never
// arrives anywhere.  Nearly every test here is really about that.

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// The landmarks the fake inventory holds, and the assets behind them.
//
// The item ids and the asset ids are deliberately different uuids and
// deliberately unmistakable: half the assertions here are about which
// of the two ended up in a message.
var (
	testLandmarksDir    = msg.MustParseUUID("e2a17e57-7e57-c0de-b864-09116b98f7c6")
	testThrushmoor      = msg.MustParseUUID("e3097e57-7e57-c0de-4f84-d4083f0603ed")
	testThrushmoorAsset = msg.MustParseUUID("f3597e57-7e57-c0de-5e4f-feef856d5803")
	testWorkshop        = msg.MustParseUUID("e85b7e57-7e57-c0de-b2dc-2d09f8c3e809")
	testWorkshopAsset   = msg.MustParseUUID("ee9b7e57-7e57-c0de-141e-585a61c8fbbf")
	testStray           = msg.MustParseUUID("d9e17e57-7e57-c0de-f694-e55564e9af71")
	testStrayAsset      = msg.MustParseUUID("ed6a7e57-7e57-c0de-8ef8-5e9b0b475b19")
	testDeleted         = msg.MustParseUUID("dbf67e57-7e57-c0de-42de-c917df1521a6")
	testDeletedAsset    = msg.MustParseUUID("ec737e57-7e57-c0de-8b1c-9ee8f88cf80b")
)

// thrushmoorAsset is a landmark asset in the form Agni really served:
// a version line, a region id and three numbers, and nothing else.
const thrushmoorAsset = "Landmark version 2\n" +
	"region_id a8377e57-7e57-c0de-9d9c-088e2efc057b\n" +
	"local_pos 32.00 70.00 1000.09\n"

const workshopAsset = "Landmark version 2\n" +
	"region_id b8eb7e57-7e57-c0de-dd61-4991f332c5d1\n" +
	"local_pos 28.00 71.95 2001.20\n"

// withLandmarks puts landmarks in the fake inventory: two where the
// simulator files them, and one somewhere else.
//
// The stray one is the point of the arrangement.  A command that only
// looked in /Landmarks would answer "there is no such landmark" about
// an item that ls shows plainly, and nothing stops one being dragged
// into a folder of its own.
func withLandmarks(x *testShell) {
	x.grid.mu.Lock()
	defer x.grid.mu.Unlock()
	x.grid.inv.Dirs = append(x.grid.inv.Dirs, &invDir{
		ID: testLandmarksDir, Name: "Landmarks", Type: 3,
		Items: []*invItem{
			{ID: testThrushmoor, Name: "Thrushmoor", Type: int(sl.AssetLandmark),
				InvType: 3, Asset: testThrushmoorAsset, Created: 1754000300},
			{ID: testWorkshop, Name: "Pelmar Reach Workshop", Type: int(sl.AssetLandmark),
				InvType: 3, Asset: testWorkshopAsset, Created: 1754000400},
		},
	})
	x.grid.inv.Dirs[0].Items = append(x.grid.inv.Dirs[0].Items,
		&invItem{ID: testStray, Name: "A Sandbox", Type: int(sl.AssetLandmark),
			InvType: 3, Asset: testStrayAsset, Created: 1754000500})
}

// twiceOver adds a second landmark of a name that is already in
// /Landmarks, which is what making one twice does.
//
// Stage 3 of doc/history/landmark.md made this pair on purpose against
// Agni and it is still in qi's inventory: one name, one folder, two
// items, and nothing but the ids to choose between them.
func twiceOver(x *testShell, name string, id, asset msg.UUID) {
	x.grid.mu.Lock()
	defer x.grid.mu.Unlock()
	for _, d := range x.grid.inv.Dirs {
		if d.ID == testLandmarksDir {
			d.Items = append(d.Items, &invItem{
				ID: id, Name: name, Type: int(sl.AssetLandmark),
				InvType: 3, Asset: asset, Created: 1754001000,
			})
			return
		}
	}
	panic("the fake inventory has no Landmarks folder")
}

// intoTheTrash puts a landmark in the fake inventory's trash.
//
// The trash is a folder like any other and is the trash because of its
// preferred type, which the fake gives it -- so this is what a landmark
// deleted with "rm" looks like a moment later, and it is the ordinary
// state of an inventory that has been used.
func intoTheTrash(x *testShell, it *invItem) {
	x.grid.mu.Lock()
	defer x.grid.mu.Unlock()
	for _, d := range x.grid.inv.Dirs {
		if d.Type == sl.FolderTrash {
			d.Items = append(d.Items, it)
			return
		}
	}
	panic("the fake inventory has no trash")
}

// serveLandmarkAssets answers the asset capability with these bodies,
// by the asset id in the query.
//
// By the id rather than with one body whatever was asked, because the
// mistake worth catching is a command that fetches the wrong landmark's
// asset -- which a fake that answered everything identically would let
// through.
func serveLandmarkAssets(t *testing.T, x *testShell, bodies map[msg.UUID]string) {
	t.Helper()
	x.grid.ServeCap(t, sl.AssetCap, func(w http.ResponseWriter, r *http.Request) {
		id, err := msg.ParseUUID(r.URL.Query().Get("landmark_id"))
		if err != nil {
			http.Error(w, "not a landmark_id", http.StatusBadRequest)
			return
		}
		body, ok := bodies[id]
		if !ok {
			http.Error(w, "no such asset", http.StatusNotFound)
			return
		}
		io.WriteString(w, body)
	})
}

// answerLandmarkTeleport has the fake move the avatar when it is asked
// to go to a landmark.
//
// A TeleportLocal is the answer, which is the one the simulator gives
// when the destination turns out to be in the region already occupied
// -- and a landmark may well point there, since the caller cannot know
// where it points without fetching the asset.  Whatever the fake was
// already answering is answered too, since --make and --go both need a
// hook and there is one between them.
func answerLandmarkTeleport(t *testing.T, x *testShell) {
	t.Helper()
	x.grid.mu.Lock()
	defer x.grid.mu.Unlock()
	before := x.grid.onSend
	x.grid.onSend = func(m msg.Message) {
		if before != nil {
			before(m)
		}
		if _, ok := m.(*msg.TeleportLandmarkRequest); ok {
			x.grid.Relay(t, &msg.TeleportLocal{})
		}
	}
}

// sentLandmark is the landmark teleport that went out, or nothing.
func sentLandmark(x *testShell) *msg.TeleportLandmarkRequest {
	var found *msg.TeleportLandmarkRequest
	for _, m := range x.grid.Sent() {
		if r, ok := m.(*msg.TeleportLandmarkRequest); ok {
			found = r
		}
	}
	return found
}

// TestLandmarkListsWhatInventoryHolds, wherever it is filed.
//
// Paths and not names, so that two landmarks of one name can be told
// apart at all -- and nothing is fetched, because where each one goes
// is inside an asset and a listing that read them all would be a
// request apiece.
func TestLandmarkListsWhatInventoryHolds(t *testing.T) {
	x := newTestShell(t)
	withLandmarks(x)

	got := x.do(t, "landmark")
	for _, want := range []string{
		"3 landmarks",
		"/Landmarks/Thrushmoor",
		"/Landmarks/Pelmar Reach Workshop",
		"/Objects/A Sandbox",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the listing should say %q:\n%s", want, got)
		}
	}
	// The notecard and the script in the fake inventory are not
	// landmarks and must not be listed as any.
	if strings.Contains(got, "readme") || strings.Contains(got, "probe") {
		t.Errorf("the listing has something that is not a landmark in it:\n%s", got)
	}
}

// TestLandmarkWithNothingToListSaysHowToMakeOne, because an empty
// answer to a command somebody has just heard of reads as the command
// being broken.
func TestLandmarkWithNothingToListSaysHowToMakeOne(t *testing.T) {
	x := newTestShell(t)
	got := x.do(t, "landmark")
	if !strings.Contains(got, "no landmarks in inventory") || !strings.Contains(got, "--make") {
		t.Errorf("an empty listing said %q", got)
	}
}

// TestLandmarkSaysWhereOneGoesWithoutGoing.
//
// The reading form is the whole reason --go is spelled out: it prints
// the destination and moves nothing, so a person can look before they
// leap.  What it prints is what the asset holds -- a region ID and a
// position -- and not a region name, which a landmark does not carry
// and nothing here could look up.
func TestLandmarkSaysWhereOneGoesWithoutGoing(t *testing.T) {
	x := newTestShell(t)
	withLandmarks(x)
	serveLandmarkAssets(t, x, map[msg.UUID]string{
		testThrushmoorAsset: thrushmoorAsset,
		testWorkshopAsset:   workshopAsset,
	})

	got := x.do(t, "landmark Thrushmoor")
	for _, want := range []string{
		"Thrushmoor",
		"in       /Landmarks",
		"region   a8377e57-7e57-c0de-9d9c-088e2efc057b",
		"at       32.00, 70.00, 1000.09",
		"item     " + testThrushmoor.String(),
		"asset    " + testThrushmoorAsset.String(),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("reading a landmark should say %q:\n%s", want, got)
		}
	}
	if m := sentLandmark(x); m != nil {
		t.Errorf("reading a landmark moved the avatar: %v", m.Info.LandmarkID)
	}
}

// TestLandmarkReadsTheOneItWasAskedAbout: a name with spaces in it is
// joined back together, as tp joins a region name, and the asset
// fetched is that landmark's and not the first one listed.
func TestLandmarkReadsTheOneItWasAskedAbout(t *testing.T) {
	x := newTestShell(t)
	withLandmarks(x)
	serveLandmarkAssets(t, x, map[msg.UUID]string{
		testThrushmoorAsset: thrushmoorAsset,
		testWorkshopAsset:   workshopAsset,
	})

	got := x.do(t, "landmark Pelmar Reach Workshop")
	if !strings.Contains(got, "at       28.00, 71.95, 2001.20") {
		t.Errorf("the wrong landmark's asset was read:\n%s", got)
	}
	// The path names it too, with or without the leading separator.
	for _, path := range []string{"/Landmarks/Pelmar Reach Workshop", "Landmarks/Pelmar Reach Workshop"} {
		if got := x.do(t, "landmark "+path); !strings.Contains(got, "at       28.00, 71.95, 2001.20") {
			t.Errorf("landmark %s should read that landmark:\n%s", path, got)
		}
	}
}

// TestLandmarkTakesEitherOfAnItemsTwoIds.
//
// A person who has just run "ls -l" has the ITEM id in front of them
// and the grid will not take it, so both are looked up here -- and the
// one that goes out is always the asset id.
func TestLandmarkTakesEitherOfAnItemsTwoIds(t *testing.T) {
	x := newTestShell(t)
	withLandmarks(x)
	serveLandmarkAssets(t, x, map[msg.UUID]string{testThrushmoorAsset: thrushmoorAsset})

	for _, id := range []msg.UUID{testThrushmoor, testThrushmoorAsset} {
		if got := x.do(t, "landmark "+id.String()); !strings.Contains(got, "1000.09") {
			t.Errorf("landmark %s said %q", id, got)
		}
	}
}

// TestLandmarkRefusesAUuidItDoesNotHold.
//
// Measured on Agni: a uuid that names nothing is answered with perfect
// silence, out to twelve seconds, and so is an item id.  There is
// nothing to catch and no error to report, so the only place it can be
// said is before the message goes out.
func TestLandmarkRefusesAUuidItDoesNotHold(t *testing.T) {
	x := newTestShell(t)
	withLandmarks(x)

	got := x.do(t, "landmark --go 8ac37e57-7e57-c0de-5f0e-1118be2e7a2c")
	if !strings.Contains(got, "no landmark in inventory has the id") {
		t.Errorf("a uuid nobody holds should be refused, got %q", got)
	}
	if !strings.Contains(got, "silence") {
		t.Errorf("the refusal should say why it is refused rather than sent: %q", got)
	}
	if m := sentLandmark(x); m != nil {
		t.Errorf("it was sent anyway: %v", m.Info.LandmarkID)
	}
}

// TestLandmarkRefusesANameThatMeansSeveral.
//
// tp's reason, and it is stronger here: the guess is not a listing to
// read again, it is an avatar somewhere it was not sent.  Two landmarks
// in one folder can share a name outright, so the id is printed beside
// the path -- there is nothing else left to tell them apart by.
func TestLandmarkRefusesANameThatMeansSeveral(t *testing.T) {
	x := newTestShell(t)
	withLandmarks(x)
	x.grid.mu.Lock()
	x.grid.inv.Dirs[len(x.grid.inv.Dirs)-1].Items = append(
		x.grid.inv.Dirs[len(x.grid.inv.Dirs)-1].Items,
		&invItem{ID: msg.MustParseUUID("e85d7e57-7e57-c0de-dfe1-663fa7f1bf24"),
			Name: "Thrushmoor", Type: int(sl.AssetLandmark), InvType: 3,
			Asset:   msg.MustParseUUID("ecc57e57-7e57-c0de-3520-6ebad7ae73fc"),
			Created: 1754000600})
	x.grid.mu.Unlock()

	// Refused for reading as well as for going: the two forms differ by
	// five characters and a rule learnt from the harmless one is the
	// rule somebody will expect from the other.
	for _, line := range []string{"landmark Thrushmoor", "landmark --go Thrushmoor"} {
		got := x.do(t, line)
		if !strings.Contains(got, "2 landmarks answer to") {
			t.Errorf("%s should refuse an ambiguous name:\n%s", line, got)
		}
		if !strings.Contains(got, testThrushmoor.String()) {
			t.Errorf("%s should print the id of each, since a shared path cannot tell them apart:\n%s",
				line, got)
		}
	}
	if m := sentLandmark(x); m != nil {
		t.Errorf("an ambiguous name moved the avatar: %v", m.Info.LandmarkID)
	}
}

// TestLandmarkSaysWhatSomethingIsWhenItIsNotOne, which is nearly always
// the useful half of the answer.
func TestLandmarkSaysWhatSomethingIsWhenItIsNotOne(t *testing.T) {
	x := newTestShell(t)
	withLandmarks(x)

	if got := x.do(t, "landmark readme"); !strings.Contains(got, "not a landmark") {
		t.Errorf("a notecard named as a landmark said %q", got)
	}
	if got := x.do(t, "landmark readme"); !strings.Contains(got, "notecard") {
		t.Errorf("it should say what the thing really is: %q", got)
	}
	if got := x.do(t, "landmark nowhere"); !strings.Contains(got, `no landmark called "nowhere"`) {
		t.Errorf("a name that is nothing at all said %q", got)
	}
}

// TestALinkIsNotALandmark.
//
// A link's asset id is the ITEM id of the thing it points at, and an
// item id sent to the grid as a landmark is answered with silence -- so
// following one would produce exactly the failure this command is
// arranged to make impossible.  The fake's AIS answer has no links in
// it, so the refusal is exercised where it is written.
func TestALinkIsNotALandmark(t *testing.T) {
	err := noSuchLandmark([]sl.Entry{
		{Name: "Thrushmoor", Type: int(sl.AssetLandmark), IsLink: true},
	}, nil, nil, "Thrushmoor")
	if err == nil {
		t.Fatal("a link was accepted as a landmark")
	}
	for _, want := range []string{"link", "ITEM id", "silence"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not mention %q", err, want)
		}
	}
}

// TestLandmarkGoSendsTheAssetIdAndNotTheItemId.
//
// The one thing this command exists to get right.  The item id is what
// a listing puts in front of a person and it is answered with nothing
// whatever, so a command that sent it would wait out its whole timeout
// and never say why.
func TestLandmarkGoSendsTheAssetIdAndNotTheItemId(t *testing.T) {
	x := newTestShell(t)
	withLandmarks(x)
	answerLandmarkTeleport(t, x)

	got := x.do(t, "landmark --go Thrushmoor")
	if !strings.Contains(got, "going to Thrushmoor") {
		t.Errorf("the name should be said before the waiting starts:\n%s", got)
	}
	if !strings.Contains(got, "Test Region at") {
		t.Errorf("where the avatar ended up should be read back:\n%s", got)
	}
	m := sentLandmark(x)
	if m == nil {
		t.Fatal("nothing was sent")
	}
	if m.Info.LandmarkID != testThrushmoorAsset {
		t.Errorf("went to landmark %v, want the asset id %v", m.Info.LandmarkID, testThrushmoorAsset)
	}
	if m.Info.LandmarkID == testThrushmoor {
		t.Error("the ITEM id went out, which the grid answers with silence")
	}
}

// TestLandmarkGoDoesNotFetchTheAsset: the id came out of a listing that
// said the item is a landmark, so it is the right kind of id already,
// and a fetch in front of the journey would be a request spent on
// nothing.  No asset capability is served here, so a command that
// fetched would fail rather than travel.
func TestLandmarkGoDoesNotFetchTheAsset(t *testing.T) {
	x := newTestShell(t)
	withLandmarks(x)
	answerLandmarkTeleport(t, x)

	if got := x.do(t, "landmark --go Thrushmoor"); !strings.Contains(got, "going to Thrushmoor") {
		t.Errorf("--go should not need the asset read first:\n%s", got)
	}
}

// TestLandmarkGoCarriesTheGridsRefusalThrough.
//
// A refusal is the grid's and not this shell's, and it arrives in two
// voices: a key a program could act on and a sentence meant for a
// person.  Both belong on the screen.
func TestLandmarkGoCarriesTheGridsRefusalThrough(t *testing.T) {
	x := newTestShell(t)
	withLandmarks(x)
	x.grid.mu.Lock()
	x.grid.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.TeleportLandmarkRequest); ok {
			x.grid.RelayEvent(t, "TeleportFailed", agniRefusedTeleport)
		}
	}
	x.grid.mu.Unlock()

	got := x.do(t, "landmark --go Thrushmoor")
	for _, want := range []string{"MustHaveVIPStatus", "premium or vip subscriber"} {
		if !strings.Contains(got, want) {
			t.Errorf("the refusal should carry %q:\n%s", want, got)
		}
	}
}

// TestLandmarkGoBlamesTheItemIdWhenNothingAnswers.
//
// Silence is the ordinary answer to a landmark the grid cannot find,
// and the timeout is the only place anything can ever say so.  The
// message comes from sl.GoTo and is passed through rather than
// swallowed, because there is nothing else to report.
func TestLandmarkGoBlamesTheItemIdWhenNothingAnswers(t *testing.T) {
	x := newTestShell(t)
	withLandmarks(x)

	got := x.do(t, "landmark --wait 1 --go Thrushmoor")
	if !strings.Contains(got, "ITEM id") {
		t.Errorf("the timeout should name the likeliest cause:\n%s", got)
	}
}

// TestLandmarkHomeSendsTheNullId.
//
// Measured on Agni: the grid reads the null landmark id as HOME and
// obeys it in about a second, so home is the one landmark nobody has to
// own and it costs no inventory lookup at all.
func TestLandmarkHomeSendsTheNullId(t *testing.T) {
	x := newTestShell(t)
	answerLandmarkTeleport(t, x)

	got := x.do(t, "landmark --home")
	if !strings.Contains(got, "going home") {
		t.Errorf("--home said %q", got)
	}
	m := sentLandmark(x)
	if m == nil {
		t.Fatal("nothing was sent")
	}
	if !m.Info.LandmarkID.IsZero() {
		t.Errorf("--home sent landmark %v rather than the null id", m.Info.LandmarkID)
	}
}

// TestLandmarkMakeReadsBackWhatTheSimulatorWrote.
//
// Nothing says where the landmark is of and nothing can: the position
// is the simulator's to write, out of where it believes the avatar is
// at the instant the message lands.  So the answer is read back rather
// than assumed, and both type numbers go out with no folder named --
// the simulator files it under Landmarks itself.
func TestLandmarkMakeReadsBackWhatTheSimulatorWrote(t *testing.T) {
	x := newTestShell(t)
	serveLandmarkAssets(t, x, map[msg.UUID]string{testWorkshopAsset: workshopAsset})

	made := make(chan *msg.CreateInventoryItem, 1)
	x.grid.mu.Lock()
	x.grid.onSend = func(m msg.Message) {
		c, ok := m.(*msg.CreateInventoryItem)
		if !ok {
			return
		}
		select {
		case made <- c:
		default:
		}
		x.grid.Relay(t, &msg.UpdateCreateInventoryItem{
			InventoryData: []msg.UpdateCreateInventoryItem_InventoryData{{
				CallbackID: c.InventoryBlock.CallbackID,
				ItemID:     testWorkshop,
				FolderID:   testLandmarksDir,
				AssetID:    testWorkshopAsset,
				Name:       []byte("Pelmar Reach Workshop\x00"),
				Type:       3, InvType: 3,
			}},
		})
	}
	x.grid.mu.Unlock()

	got := x.do(t, "landmark --make Pelmar Reach Workshop")
	for _, want := range []string{
		"made Pelmar Reach Workshop",
		"region   b8eb7e57-7e57-c0de-dd61-4991f332c5d1",
		// Read back out of the asset, not out of where the shell
		// thought the avatar was: the fake stands at 128, 128, 25.
		"at       28.00, 71.95, 2001.20",
		"item     " + testWorkshop.String(),
		"asset    " + testWorkshopAsset.String(),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("--make should say %q:\n%s", want, got)
		}
	}

	c := <-made
	b := c.InventoryBlock
	if b.Type != 3 || b.InvType != 3 {
		t.Errorf("asked for type %d/%d, want 3/3", b.Type, b.InvType)
	}
	if !b.FolderID.IsZero() {
		t.Errorf("a folder was named (%v); the simulator files a landmark itself", b.FolderID)
	}
	if got := strings.TrimRight(string(b.Name), "\x00"); got != "Pelmar Reach Workshop" {
		t.Errorf("made a landmark called %q", got)
	}
}

// TestLandmarkMakeSaysTheItemIsThereWhenTheAssetIsNot.
//
// The asset was readable 200ms after the item arrived when it was
// measured, so a read that finds nothing gets one retry and then gives
// up -- but the item is made either way, and a report that looked like
// a failure would have somebody make a second one.
func TestLandmarkMakeSaysTheItemIsThereWhenTheAssetIsNot(t *testing.T) {
	x := newTestShell(t)
	serveLandmarkAssets(t, x, nil)

	x.grid.mu.Lock()
	x.grid.onSend = func(m msg.Message) {
		c, ok := m.(*msg.CreateInventoryItem)
		if !ok {
			return
		}
		x.grid.Relay(t, &msg.UpdateCreateInventoryItem{
			InventoryData: []msg.UpdateCreateInventoryItem_InventoryData{{
				CallbackID: c.InventoryBlock.CallbackID,
				ItemID:     testWorkshop,
				FolderID:   testLandmarksDir,
				AssetID:    testWorkshopAsset,
				Name:       []byte("Workshop\x00"),
				Type:       3, InvType: 3,
			}},
		})
	}
	x.grid.mu.Unlock()

	got := x.do(t, "landmark --make Workshop")
	if !strings.Contains(got, "made Workshop") {
		t.Errorf("--make should say the item was made:\n%s", got)
	}
	if !strings.Contains(got, "the item is made") {
		t.Errorf("--make should not report an unreadable asset as a failure:\n%s", got)
	}
	if !strings.Contains(got, testWorkshopAsset.String()) {
		t.Errorf("--make should still print the asset id to go by:\n%s", got)
	}
}

// TestALandmarkWithNoAssetIsRefusedRatherThanSentAsHome.
//
// A zero asset id is not a missing landmark, it is the null id, and the
// null id is the one wrong id the grid is not silent about: it reads it
// as home and obeys.  Quietly going home instead of failing is the
// worst answer available.
func TestALandmarkWithNoAssetIsRefusedRatherThanSentAsHome(t *testing.T) {
	x := newTestShell(t)
	x.grid.mu.Lock()
	x.grid.inv.Items = append(x.grid.inv.Items, &invItem{
		ID: testStray, Name: "Nowhere", Type: int(sl.AssetLandmark), InvType: 3,
		Created: 1754000700,
	})
	x.grid.mu.Unlock()

	got := x.do(t, "landmark --go Nowhere")
	if !strings.Contains(got, "no asset id") {
		t.Errorf("a landmark with no asset should be refused, got %q", got)
	}
	if m := sentLandmark(x); m != nil {
		t.Errorf("a trip home went out anyway: %v", m.Info.LandmarkID)
	}
}

// TestLandmarkRefusesTwoVerbsAtOnce, and the other ways of asking for
// something that is not one of the six forms.
func TestLandmarkRefusesTwoVerbsAtOnce(t *testing.T) {
	x := newTestShell(t)

	for _, c := range []struct{ line, want string }{
		// The flags together, because getopt stops reading options at
		// the first operand: "--make X --go X" is a landmark called
		// "X --go X" and not two verbs at all.
		{"landmark --make --go X", "ask for one"},
		{"landmark --go --home X", "ask for one"},
		{"landmark --home Thrushmoor", "nobody has to name"},
		{"landmark --go", "which landmark"},
		{"landmark --make", "which landmark"},
		{"landmark --wait 5 Thrushmoor", "--wait is the waiting"},
		// Setting home is the fourth verb and the one that changes
		// something a teleport cannot put back, so it is refused
		// beside the others rather than left to do half of two things.
		{"landmark --set-home --home", "ask for one"},
		{"landmark --set-home Thrushmoor", "nobody has to name"},
		{"landmark --wait 5 --set-home", "--wait is the waiting"},
	} {
		if got := x.do(t, c.line); !strings.Contains(got, c.want) {
			t.Errorf("%q should say %q, said %q", c.line, c.want, got)
		}
	}
	if m := sentLandmark(x); m != nil {
		t.Errorf("a refused line moved the avatar: %v", m.Info.LandmarkID)
	}
	if m := sentSetHome(x); m != nil {
		t.Error("a refused line set this account's home")
	}
}

// TestATrashedLandmarkIsNotOne.
//
// A viewer's delete moves things to the trash, so a used inventory has
// deleted landmarks in it -- qi's had two of seven, and one of them
// made a live landmark ambiguous with its own corpse.  A deleted
// landmark still has a name, an asset and a working teleport, so
// nothing but leaving it out keeps --go from travelling to one.
func TestATrashedLandmarkIsNotOne(t *testing.T) {
	x := newTestShell(t)
	withLandmarks(x)
	intoTheTrash(x, &invItem{
		ID: testDeleted, Name: "Thrushmoor", Type: int(sl.AssetLandmark),
		InvType: 3, Asset: testDeletedAsset, Created: 1754000800,
	})
	serveLandmarkAssets(t, x, map[msg.UUID]string{testThrushmoorAsset: thrushmoorAsset})
	answerLandmarkTeleport(t, x)

	got := x.do(t, "landmark")
	if strings.Contains(got, "/Trash/") {
		t.Errorf("the listing has a deleted landmark in it:\n%s", got)
	}
	if !strings.Contains(got, "3 landmarks") {
		t.Errorf("the kept landmarks should still be listed:\n%s", got)
	}

	// The live one, and no ambiguity with the deleted one of its name.
	got = x.do(t, "landmark Thrushmoor")
	if strings.Contains(got, "answer to") {
		t.Errorf("a deleted landmark made a kept one ambiguous:\n%s", got)
	}
	if !strings.Contains(got, "in       /Landmarks") {
		t.Errorf("the kept landmark should be the one found:\n%s", got)
	}

	x.do(t, "landmark --go Thrushmoor")
	m := sentLandmark(x)
	if m == nil {
		t.Fatal("nothing was sent")
	}
	if m.Info.LandmarkID != testThrushmoorAsset {
		t.Errorf("went to %v, want the kept landmark %v", m.Info.LandmarkID, testThrushmoorAsset)
	}
}

// TestANameThatOnlyAnswersInTheTrashSaysSo.
//
// Left out of the listing and refused by name, but not silently: a
// person who deleted a landmark an hour ago and has forgotten is owed
// the sentence, in the spirit of the refusal that says a thing is a
// notecard.  The path is in it because the path is what "mv" needs.
func TestANameThatOnlyAnswersInTheTrashSaysSo(t *testing.T) {
	x := newTestShell(t)
	intoTheTrash(x, &invItem{
		ID: testDeleted, Name: "Sandbox", Type: int(sl.AssetLandmark),
		InvType: 3, Asset: testDeletedAsset, Created: 1754000800,
	})

	// By its name, by its whole path, and by either of its two ids: a
	// form that reached into the trash would mean --go could still
	// take the avatar to a landmark somebody deleted.
	for _, line := range []string{
		"landmark Sandbox",
		"landmark --go Sandbox",
		"landmark /Trash/Sandbox",
		"landmark --go " + testDeleted.String(),
		"landmark --go " + testDeletedAsset.String(),
	} {
		got := x.do(t, line)
		if !strings.Contains(got, "trash") {
			t.Errorf("%s should say the landmark is in the trash, said %q", line, got)
		}
		if !strings.Contains(got, "mv") {
			t.Errorf("%s should say how to get it back, said %q", line, got)
		}
	}
	if m := sentLandmark(x); m != nil {
		t.Errorf("a deleted landmark was travelled to: %v", m.Info.LandmarkID)
	}

	// And the listing does not simply lose it either.
	got := x.do(t, "landmark")
	if !strings.Contains(got, "no landmarks in inventory") {
		t.Errorf("nothing kept should list as nothing:\n%s", got)
	}
	if !strings.Contains(got, "1 landmark is in the trash") {
		t.Errorf("the listing should account for the deleted one:\n%s", got)
	}
}

// TestTheTrashIsFoundByItsTypeAndNotItsName: a folder is the trash
// because of its preferred type, it can be renamed, and an account made
// through a viewer in another language never called it "Trash" at all.
// A folder called "Trashcan" beside it is not the trash and must not be
// swallowed by a prefix that forgot the separator.
func TestTheTrashIsFoundByItsTypeAndNotItsName(t *testing.T) {
	es := []sl.Entry{
		{Folder: true, Name: "Papierkorb", Path: "Papierkorb", Type: sl.FolderTrash},
		{Folder: true, Name: "Trash", Path: "Trash", Type: 6},
		{Folder: true, Name: "Trashcan", Path: "Papierkorbular", Type: 6},
	}
	bins := trashPaths(es)
	if len(bins) != 1 || bins[0] != "Papierkorb" {
		t.Fatalf("the trash was found as %v", bins)
	}
	for _, c := range []struct {
		path string
		want bool
	}{
		{"Papierkorb", true},
		{"Papierkorb/Sandbox", true},
		{"Papierkorb/Old/Sandbox", true},
		{"Papierkorbular/Sandbox", false},
		{"Trash/Sandbox", false},
		{"Landmarks/Thrushmoor", false},
	} {
		if got := inTrash(c.path, bins); got != c.want {
			t.Errorf("inTrash(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

// TestOneNameInOneFolderIsToldApartByItsIdAndNothingElse.
//
// Stage 3 of doc/history/landmark.md typed the whole path of a
// duplicated landmark and was told to say which by its whole path, which
// is what it had just typed.  When the matching paths are equal the path
// is not an answer and the advice must not pretend it is.
func TestOneNameInOneFolderIsToldApartByItsIdAndNothingElse(t *testing.T) {
	x := newTestShell(t)
	withLandmarks(x)
	twiceOver(x, "Thrushmoor", testDeleted, testDeletedAsset)

	for _, line := range []string{"landmark Thrushmoor", "landmark /Landmarks/Thrushmoor"} {
		got := x.do(t, line)
		if !strings.Contains(got, "the id beside each is the only thing that can") {
			t.Errorf("%s should offer the id and not the path:\n%s", line, got)
		}
		if strings.Contains(got, "say which by its whole path") {
			t.Errorf("%s sent the person round the same loop:\n%s", line, got)
		}
		for _, id := range []msg.UUID{testThrushmoor, testDeleted} {
			if !strings.Contains(got, id.String()) {
				t.Errorf("%s should print %s, which is the only way to choose:\n%s",
					line, id, got)
			}
		}
	}

	// A name in two DIFFERENT folders is the other case, and there the
	// path is a real answer, so it is still offered.
	y := newTestShell(t)
	withLandmarks(y)
	twiceOver(y, "A Sandbox", testDeleted, testDeletedAsset)
	got := y.do(t, "landmark A Sandbox")
	if !strings.Contains(got, "say which by its whole path") {
		t.Errorf("two folders apart, the path is the way to choose:\n%s", got)
	}
	if !strings.Contains(got, "/Objects/A Sandbox") || !strings.Contains(got, "/Landmarks/A Sandbox") {
		t.Errorf("both paths should be listed:\n%s", got)
	}
}

// TestTheListingTellsTwoOfOneNameApart: two identical lines with
// nothing to choose between them is not a listing, and the id is put on
// those lines only -- a column of ids on every row would pay for one
// collision with noise on every inventory that has none.
func TestTheListingTellsTwoOfOneNameApart(t *testing.T) {
	x := newTestShell(t)
	withLandmarks(x)
	twiceOver(x, "Thrushmoor", testDeleted, testDeletedAsset)

	got := x.do(t, "landmark")
	for _, id := range []msg.UUID{testThrushmoor, testDeleted} {
		if !strings.Contains(got, id.String()) {
			t.Errorf("the listing should tell the two Thrushmoors apart by %s:\n%s", id, got)
		}
	}
	// The landmarks that are not duplicated keep their plain line.
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "A Sandbox") && strings.Contains(line, "-") &&
			strings.Contains(line, testStray.String()) {
			t.Errorf("an id was printed for a landmark that needed none:\n%s", got)
		}
	}
}

// TestMakingASecondLandmarkOfANameSaysSo.
//
// Nothing stops it and the consequence is silent and late: from then on
// the name is refused as ambiguous and only an id will do.  The command
// that created that is the one that should mention it.
func TestMakingASecondLandmarkOfANameSaysSo(t *testing.T) {
	x := newTestShell(t)
	withLandmarks(x)
	serveLandmarkAssets(t, x, map[msg.UUID]string{testDeletedAsset: workshopAsset})

	// The simulator files the new one under Landmarks, beside the
	// "Thrushmoor" that is already there.
	x.grid.mu.Lock()
	x.grid.onSend = func(m msg.Message) {
		c, ok := m.(*msg.CreateInventoryItem)
		if !ok {
			return
		}
		twiceOver(x, "Thrushmoor", testDeleted, testDeletedAsset)
		x.grid.Relay(t, &msg.UpdateCreateInventoryItem{
			InventoryData: []msg.UpdateCreateInventoryItem_InventoryData{{
				CallbackID: c.InventoryBlock.CallbackID,
				ItemID:     testDeleted,
				FolderID:   testLandmarksDir,
				AssetID:    testDeletedAsset,
				Name:       []byte("Thrushmoor\x00"),
				Type:       3, InvType: 3,
			}},
		})
	}
	x.grid.mu.Unlock()

	got := x.do(t, "landmark --make Thrushmoor")
	if !strings.Contains(got, "made Thrushmoor") {
		t.Errorf("--make should say what it made:\n%s", got)
	}
	if !strings.Contains(got, `there are now 2 landmarks called "Thrushmoor"`) {
		t.Errorf("--make should say the name is now taken twice:\n%s", got)
	}
	if !strings.Contains(got, "mv") {
		t.Errorf("--make should say what to do about it:\n%s", got)
	}

	// A name nobody else has says nothing extra.
	y := newTestShell(t)
	withLandmarks(y)
	serveLandmarkAssets(t, y, map[msg.UUID]string{testWorkshopAsset: workshopAsset})
	y.grid.mu.Lock()
	y.grid.onSend = func(m msg.Message) {
		c, ok := m.(*msg.CreateInventoryItem)
		if !ok {
			return
		}
		y.grid.Relay(t, &msg.UpdateCreateInventoryItem{
			InventoryData: []msg.UpdateCreateInventoryItem_InventoryData{{
				CallbackID: c.InventoryBlock.CallbackID,
				ItemID:     testDeleted, FolderID: testLandmarksDir,
				AssetID: testWorkshopAsset, Name: []byte("Somewhere New\x00"),
				Type: 3, InvType: 3,
			}},
		})
	}
	y.grid.mu.Unlock()
	if got := y.do(t, "landmark --make Somewhere New"); strings.Contains(got, "there are now") {
		t.Errorf("a name nobody else has should say nothing extra:\n%s", got)
	}
}

// TestARefusalNamesWhatWasTypedAndNotAUuid.
//
// The commonest failure there is: going to a landmark the avatar is
// already standing on.  Stage 3 of doc/history/landmark.md saw it
// identify the destination by an asset id nobody typed, on the line
// under one that had just said the name.
func TestARefusalNamesWhatWasTypedAndNotAUuid(t *testing.T) {
	x := newTestShell(t)
	withLandmarks(x)
	x.grid.mu.Lock()
	x.grid.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.TeleportLandmarkRequest); ok {
			x.grid.RelayEvent(t, "TeleportFailed", agniCouldNotGoCloser)
		}
	}
	x.grid.mu.Unlock()

	got := x.do(t, "landmark --go Thrushmoor")
	if !strings.Contains(got, `"Thrushmoor"`) {
		t.Errorf("the refusal should name what was typed:\n%s", got)
	}
	if strings.Contains(got, testThrushmoorAsset.String()) {
		t.Errorf("the refusal names a uuid nobody typed:\n%s", got)
	}
	if n := strings.Count(got, "the teleport"); n != 1 {
		t.Errorf("the refusal says \"the teleport\" %d times:\n%s", n, got)
	}
	if !strings.Contains(got, "already standing there") {
		t.Errorf("the refusal should say what this one usually means:\n%s", got)
	}
}

// agniCouldNotGoCloser is what Agni answers a landmark the avatar is
// standing on, sent 2026-08-19.
const agniCouldNotGoCloser = `<llsd><map>` +
	`<key>AlertInfo</key><array><map>` +
	`<key>ExtraParams</key><string></string>` +
	`<key>Message</key><string>CouldntTPCloser</string></map></array>` +
	`<key>Info</key><array><map>` +
	`<key>Reason</key><string>Could not teleport closer to destination` +
	`</string></map></array></map></llsd>`

// TestParentPathIsTheFolderAnItemWasFoundIn, including the root, which
// has no name and prints as nothing after the separator.
func TestParentPathIsTheFolderAnItemWasFoundIn(t *testing.T) {
	for _, c := range []struct{ path, want string }{
		{"Landmarks/Thrushmoor", "Landmarks"},
		{"Landmarks/Old/Thrushmoor", "Landmarks/Old"},
		{"Thrushmoor", ""},
		{"", ""},
	} {
		if got := parentPath(c.path); got != c.want {
			t.Errorf("parentPath(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

// Setting home, which is the one thing this command does that moves
// nobody and cannot be undone by moving.
//
// The sentences below are Agni's own, measured on 2026-09-01: the
// answer to this request is an AlertMessage and there is no reply and
// no field anywhere that says home moved, so what the shell prints is
// what the grid said and a test of anything else would be a test of a
// sentence this program invented.
const (
	agniHomeSet     = "Home position set."
	agniHomeRefused = "You can only set your 'Home Location' on your land " +
		"or at a mainland Infohub."
)

// answerSetHome makes the fake say what a simulator says about a home
// position, as an alert and not as a reply.
func answerSetHome(t *testing.T, x *testShell, said string) {
	t.Helper()
	x.grid.mu.Lock()
	defer x.grid.mu.Unlock()
	before := x.grid.onSend
	x.grid.onSend = func(m msg.Message) {
		if before != nil {
			before(m)
		}
		if _, ok := m.(*msg.SetStartLocationRequest); !ok {
			return
		}
		r := &msg.AlertMessage{}
		r.AlertData.Message = append([]byte(said), 0)
		x.grid.Relay(t, r)
	}
}

// sentSetHome is the home request that went out, or nothing.
func sentSetHome(x *testShell) *msg.SetStartLocationRequest {
	var found *msg.SetStartLocationRequest
	for _, m := range x.grid.Sent() {
		if r, ok := m.(*msg.SetStartLocationRequest); ok {
			found = r
		}
	}
	return found
}

// TestLandmarkSetHomeSaysWhereAndThenWhatTheGridSaid.
//
// Where first, because the place is the half a person can check: the
// grid's sentence is about a position it does not name, so a line
// carrying only that would say home moved without saying where to.
func TestLandmarkSetHomeSaysWhereAndThenWhatTheGridSaid(t *testing.T) {
	x := newTestShell(t)
	answerSetHome(t, x, agniHomeSet)

	got := x.do(t, "landmark --set-home")
	if !strings.Contains(got, "setting home to Test Region at 128, 128, 25") {
		t.Errorf("--set-home said %q, and did not say where", got)
	}
	if !strings.Contains(got, agniHomeSet) {
		t.Errorf("--set-home said %q, and not what the grid said", got)
	}

	m := sentSetHome(x)
	if m == nil {
		t.Fatal("nothing was sent")
	}
	if m.StartLocationData.LocationID != sl.StartLocationHome {
		t.Errorf("location id = %d, want home", m.StartLocationData.LocationID)
	}
	if m.StartLocationData.LocationPos != (msg.Vector3{X: 128, Y: 128, Z: 25}) {
		t.Errorf("home was asked for at %v, and the avatar is at 128, 128, 25",
			m.StartLocationData.LocationPos)
	}
	if m := sentLandmark(x); m != nil {
		t.Error("setting home moved the avatar")
	}
}

// TestLandmarkSetHomePrintsTheRefusalTheGridWrote.
//
// Home may be set on land the account controls and at a mainland
// infohub, and nowhere else.  The refusal is a sentence rather than a
// code, and it is the only thing that says which of the two happened,
// so it is printed as it arrived.
func TestLandmarkSetHomePrintsTheRefusalTheGridWrote(t *testing.T) {
	x := newTestShell(t)
	answerSetHome(t, x, agniHomeRefused)

	got := x.do(t, "landmark --set-home")
	if !strings.Contains(got, "mainland Infohub") {
		t.Errorf("--set-home said %q, and not what the grid refused with", got)
	}
	if strings.Contains(got, agniHomeSet) {
		t.Errorf("a refusal was reported as a home that moved: %q", got)
	}
}

// TestLandmarkSetHomeHasNoWaitOfItsOwn: --wait is how long to believe
// in an ARRIVAL, and nothing arrives here.  A flag that was accepted
// and then ignored would be worse than one that is refused, so the
// refusal says which waiting it is.  What silence from the grid comes
// to is sl's to answer, and sl.TestSetHomeCallsSilenceSilence is where
// it is checked -- the wait there is a fraction of a second and this
// one would be ten of them.
func TestLandmarkSetHomeHasNoWaitOfItsOwn(t *testing.T) {
	x := newTestShell(t)

	got := x.do(t, "landmark --set-home --wait 1")
	if !strings.Contains(got, "--wait is the waiting") {
		t.Errorf("--set-home --wait said %q", got)
	}
	if m := sentSetHome(x); m != nil {
		t.Error("a refused line set home anyway")
	}
}

// TestLandmarkSetHomePrintsThePositionThatWentOut.
//
// The line and the message have to agree.  A shell that read the
// position a second time to print it would print whatever the avatar
// had drifted to since, and an avatar that has just teleported is still
// settling: eight metres separated two reads a second apart when this
// was met on Agni.  So the fake moves the avatar the moment the request
// goes out, and the line still says where home was set.
func TestLandmarkSetHomePrintsThePositionThatWentOut(t *testing.T) {
	x := newTestShell(t)
	x.grid.mu.Lock()
	x.grid.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.SetStartLocationRequest); !ok {
			return
		}
		x.grid.mu.Lock()
		x.grid.presence.Position = msg.Vector3{X: 128, Y: 128, Z: 17}
		x.grid.mu.Unlock()
		r := &msg.AlertMessage{}
		r.AlertData.Message = append([]byte(agniHomeSet), 0)
		x.grid.Relay(t, r)
	}
	x.grid.mu.Unlock()

	got := x.do(t, "landmark --set-home")
	if !strings.Contains(got, "at 128, 128, 25") {
		t.Errorf("--set-home said %q, and home was set at 128, 128, 25", got)
	}
	if strings.Contains(got, "at 128, 128, 17") {
		t.Errorf("--set-home printed where the avatar had fallen to: %q", got)
	}
	m := sentSetHome(x)
	if m == nil {
		t.Fatal("nothing was sent")
	}
	if m.StartLocationData.LocationPos != (msg.Vector3{X: 128, Y: 128, Z: 25}) {
		t.Errorf("the message carried %v", m.StartLocationData.LocationPos)
	}
}
