package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// The commands themselves, against a grid that is not there.  What is
// under test is what this package adds -- how an answer is worded, what
// is refused, and what is never guessed at -- rather than the
// operations underneath, which sl has its own tests for.

func TestWhereSaysTheRegionAndThePosition(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	if got := send(t, d, b, "where"); got != "Nowhere at 128, 64, 25\n" {
		t.Errorf("got %q", got)
	}
}

func TestLookSaysWhatTheSimulatorSaid(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	got := send(t, d, b, "look")
	for _, want := range []string{"Nowhere", "general", "water at 20.0m"} {
		if !strings.Contains(got, want) {
			t.Errorf("look did not mention %q:\n%s", want, got)
		}
	}
}

func TestWhoListsThePeopleInRange(t *testing.T) {
	d, b, f := newTestDaemon(t)
	f.objects = []*sl.Seen{
		{Object: sl.Object{ID: testSender, Name: "Trusted Resident"}, PCode: 47,
			Position: msg.Vector3{X: 138, Y: 64, Z: 25}},
		{Object: sl.Object{ID: testStranger, Name: "Some Body"}, PCode: 47,
			Position: msg.Vector3{X: 130, Y: 64, Z: 25}},
		{Object: sl.Object{ID: testLamp, Name: "a lamp"}, PCode: 9,
			Position: msg.Vector3{X: 129, Y: 64, Z: 25}},
	}
	got := send(t, d, b, "who")
	if strings.Contains(got, "a lamp") {
		t.Errorf("who listed an object:\n%s", got)
	}
	// Nearest first, and a prim is not a person.
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines:\n%s", len(lines), got)
	}
	if !strings.Contains(lines[0], "2m") || !strings.Contains(lines[1], "10m") {
		t.Errorf("not nearest first:\n%s", got)
	}
}

func TestWhoSaysSoWhenThereIsNobody(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	if got := send(t, d, b, "who"); !strings.Contains(got, "nobody else") {
		t.Errorf("got %q", got)
	}
}

// A listing of every prim in every linkset is a thousand lines of the
// same object, and the root is the thing anybody names.
func TestObjectsListsRootsOnly(t *testing.T) {
	d, b, f := newTestDaemon(t)
	f.objects = []*sl.Seen{
		{Object: sl.Object{ID: testLamp, Name: "a lamp", Local: 10}},
		{Object: sl.Object{ID: testStranger, Name: "a lamp shade", Local: 11}, Parent: 10},
	}
	got := send(t, d, b, "objects")
	if strings.Contains(got, "shade") {
		t.Errorf("objects listed a child prim:\n%s", got)
	}
	if !strings.Contains(got, "a lamp") || !strings.Contains(got, "1 objects") {
		t.Errorf("got %q", got)
	}
}

func TestSayPutsChatOnTheWire(t *testing.T) {
	d, b, f := newTestDaemon(t)
	if got := send(t, d, b, "say hello there"); !strings.Contains(got, "said: hello there") {
		t.Errorf("got %q", got)
	}
	var chats []*msg.ChatFromViewer
	for _, m := range f.Sent() {
		if c, ok := m.(*msg.ChatFromViewer); ok {
			chats = append(chats, c)
		}
	}
	if len(chats) != 1 {
		t.Fatalf("%d chat messages sent", len(chats))
	}
	if got := strings.TrimRight(string(chats[0].ChatData.Message), "\x00"); got != "hello there" {
		t.Errorf("said %q", got)
	}
	if chats[0].ChatData.Channel != 0 {
		t.Errorf("channel = %d", chats[0].ChatData.Channel)
	}
}

func TestSayCanChooseAChannelAndAVolume(t *testing.T) {
	d, b, f := newTestDaemon(t)
	if got := send(t, d, b, "say --channel 42 --shout ping"); !strings.Contains(got, "channel 42") {
		t.Errorf("got %q", got)
	}
	for _, m := range f.Sent() {
		if c, ok := m.(*msg.ChatFromViewer); ok {
			if c.ChatData.Channel != 42 || c.ChatData.Type != chatShout {
				t.Errorf("channel %d, type %d", c.ChatData.Channel, c.ChatData.Type)
			}
		}
	}
}

func TestSayWillNotShoutAndWhisperAtOnce(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	if got := send(t, d, b, "say --shout --whisper hello"); !strings.Contains(got, "pick one") {
		t.Errorf("got %q", got)
	}
}

func TestIMGoesToTheAvatarNamed(t *testing.T) {
	d, b, f := newTestDaemon(t)
	got := send(t, d, b, "im "+testStranger.String()+" hello there")
	if !strings.Contains(got, "hello there") {
		t.Errorf("got %q", got)
	}
	ims := f.IMsSent()
	if len(ims) != 1 {
		t.Fatalf("%d messages sent", len(ims))
	}
	if ims[0].MessageBlock.ToAgentID != testStranger {
		t.Errorf("sent to %s", ims[0].MessageBlock.ToAgentID)
	}
}

// ------------------------------------------------------------- offers

// offerTo puts an offer into the session by the one route that produces
// a real one: the message the grid sends.
func offerTo(t *testing.T, f *fakeGrid, s *sl.Session, what string, transaction msg.UUID) {
	t.Helper()
	ims := s.IMs(4)
	defer s.StopIMs(ims)
	f.deliver(t, offering(testSender, "Trusted Resident", what, transaction))
	<-ims
}

func TestOffersListsWhatIsWaiting(t *testing.T) {
	d, b, f := newTestDaemon(t)
	s := b.Session()
	if got := send(t, d, b, "offers"); !strings.Contains(got, "nothing is waiting") {
		t.Errorf("got %q", got)
	}

	first := msg.MustParseUUID("9c847e57-7e57-c0de-8f99-11049d4de96d")
	second := msg.MustParseUUID("9ddb7e57-7e57-c0de-5ed1-76ac971c1969")
	offerTo(t, f, s, "a notecard", first)
	offerTo(t, f, s, "a script", second)

	got := send(t, d, b, "offers")
	if !strings.Contains(got, "1  item") || !strings.Contains(got, "2  item") {
		t.Errorf("got %q", got)
	}
	if !strings.Contains(got, "a notecard") || !strings.Contains(got, "a script") {
		t.Errorf("got %q", got)
	}
}

// The number is a position in the listing somebody has just read, and
// answering the wrong one of two cannot be undone: the transaction is
// spent and the other offer is still sitting there looking identical.
func TestAcceptAnswersTheOfferByNumber(t *testing.T) {
	d, b, f := newTestDaemon(t)
	s := b.Session()
	first := msg.MustParseUUID("9c847e57-7e57-c0de-8f99-11049d4de96d")
	second := msg.MustParseUUID("9ddb7e57-7e57-c0de-5ed1-76ac971c1969")
	offerTo(t, f, s, "a notecard", first)
	offerTo(t, f, s, "a script", second)

	before := len(f.IMsSent())
	if got := send(t, d, b, "accept 2"); !strings.Contains(got, "a script") {
		t.Errorf("got %q", got)
	}
	ims := f.IMsSent()[before:]
	if len(ims) != 1 {
		t.Fatalf("%d messages sent", len(ims))
	}
	if ims[0].MessageBlock.Dialog != sl.DialogInventoryAccepted {
		t.Errorf("dialog = %d", ims[0].MessageBlock.Dialog)
	}
	if ims[0].MessageBlock.ID != second {
		t.Errorf("answered %s, want the second offer %s", ims[0].MessageBlock.ID, second)
	}
	// The one that was answered is gone and the other is still there.
	if got := send(t, d, b, "offers"); !strings.Contains(got, "a notecard") || strings.Contains(got, "a script") {
		t.Errorf("after accepting, offers said %q", got)
	}
}

func TestDeclineRefusesOne(t *testing.T) {
	d, b, f := newTestDaemon(t)
	s := b.Session()
	transaction := msg.MustParseUUID("9e5b7e57-7e57-c0de-59ce-577f7cc1e0ed")
	offerTo(t, f, s, "a notecard", transaction)

	before := len(f.IMsSent())
	if got := send(t, d, b, "decline 1"); !strings.Contains(got, "declined") {
		t.Errorf("got %q", got)
	}
	ims := f.IMsSent()[before:]
	if len(ims) != 1 || ims[0].MessageBlock.Dialog != sl.DialogInventoryDeclined {
		t.Fatalf("sent %d messages, first dialog %d", len(ims), ims[0].MessageBlock.Dialog)
	}
}

func TestANumberThatIsNotThereIsRefused(t *testing.T) {
	d, b, f := newTestDaemon(t)
	transaction := msg.MustParseUUID("9e6b7e57-7e57-c0de-d391-d8690171ccdc")
	offerTo(t, f, b.Session(), "a notecard", transaction)

	if got := send(t, d, b, "accept 7"); !strings.Contains(got, "there are 1 waiting") {
		t.Errorf("got %q", got)
	}
	if got := send(t, d, b, "accept soon"); !strings.Contains(got, "not a number") {
		t.Errorf("got %q", got)
	}
	if got := send(t, d, b, "accept"); !strings.Contains(got, "usage: accept") {
		t.Errorf("got %q", got)
	}
}

func TestAcceptingNothingSaysThereIsNothing(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	if got := send(t, d, b, "accept 1"); !strings.Contains(got, "nothing is waiting") {
		t.Errorf("got %q", got)
	}
}

// ------------------------------------------------------------- refusals

// The map searches by prefix, so a name typed in full comes back beside
// every longer name that begins with it.  Choosing between them would
// put the avatar somewhere nobody asked for, minutes away from whoever
// is watching.
func TestARegionThatMeansSeveralIsRefused(t *testing.T) {
	found := []sl.MapRegion{{Name: "Example Bay"}, {Name: "Example Bayside"}}
	if _, err := pickRegion(found, "Example Bay"); err != nil {
		t.Errorf("an exact name was refused: %v", err)
	}
	_, err := pickRegion(found, "Example")
	if err == nil || !strings.Contains(err.Error(), "Example Bayside") {
		t.Fatalf("err = %v, want both names listed", err)
	}
}

func TestTPNeedsSomewhereToGo(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	if got := send(t, d, b, "tp"); !strings.Contains(got, "usage: tp") {
		t.Errorf("got %q", got)
	}
}

// Three numbers at the end are a position and anything in front of them
// is a region, which is the whole of the rule.
func TestATeleportTargetIsReadFromWhatWasTyped(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		region string
		x      float32
	}{
		{[]string{"128", "64", "25"}, "", 128},
		{[]string{"Example", "128", "64", "25"}, "Example", 128},
		{[]string{"Example", "Bay", "10", "20", "30"}, "Example Bay", 10},
		{[]string{"Example"}, "Example", 128},
		{[]string{"Example", "Bay"}, "Example Bay", 128},
	} {
		region, at, err := teleportTarget(tc.args)
		if err != nil {
			t.Errorf("teleportTarget(%q): %v", tc.args, err)
			continue
		}
		if region != tc.region || at.X != tc.x {
			t.Errorf("teleportTarget(%q) = %q, %v, want %q, x=%v",
				tc.args, region, at, tc.region, tc.x)
		}
	}
}

// A position typed is a target whatever it is, and the middle of the
// region at 25 m -- the documented example -- is a position like any
// other rather than the absence of one.
func TestAPositionTypedIsAlwaysATarget(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want msg.Vector3
	}{
		{[]string{"128", "128", "25"}, msg.Vector3{X: 128, Y: 128, Z: 25}},
		// As copied off a line that separates them with commas.
		{[]string{"128,", "64,", "25"}, msg.Vector3{X: 128, Y: 64, Z: 25}},
	} {
		region, at, err := teleportTarget(tc.args)
		if err != nil {
			t.Errorf("teleportTarget(%q): %v", tc.args, err)
			continue
		}
		if region != "" || at != tc.want {
			t.Errorf("teleportTarget(%q) = %q, %v, want a move in this region to %v",
				tc.args, region, at, tc.want)
		}
	}
	if _, _, err := teleportTarget(nil); err == nil {
		t.Error("nothing typed was taken as somewhere to go")
	}
}

// The same, through the command: tp X Y Z is asked of the simulator
// rather than refused before anything is sent.  The fake never answers,
// so the command times out; what matters is what went.
func TestTPToTheMiddleOfThisRegionIsSent(t *testing.T) {
	d, b, grid := newTestDaemon(t)
	grid.presence.RegionHandle = msg.RegionHandle(3, 5)
	got := send(t, d, b, "tp --wait 1 128 128 25")
	if strings.Contains(got, "nothing to teleport to") {
		t.Fatalf("the documented example was refused: %q", got)
	}
	for _, m := range grid.Sent() {
		if tp, ok := m.(*msg.TeleportLocationRequest); ok {
			if want := (msg.Vector3{X: 128, Y: 128, Z: 25}); tp.Info.Position != want {
				t.Errorf("teleported to %v, want %v", tp.Info.Position, want)
			}
			return
		}
	}
	t.Errorf("no teleport was asked for; the command said %q", got)
}

func TestStandTakesNothing(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	if got := send(t, d, b, "stand up"); !strings.Contains(got, "takes nothing") {
		t.Errorf("got %q", got)
	}
}

// Which of two identical names somebody meant is not something to
// guess at when the answer is a delete.
func TestANameThatMeansTwoThingsIsRefused(t *testing.T) {
	kids := []sl.Entry{
		{ID: testLamp, Name: "a lamp"},
		{ID: testStranger, Name: "a lamp"},
		{ID: testSender, Name: "something else"},
	}
	_, err := pickEntry(kids, "a lamp", "Objects")
	if err == nil {
		t.Fatal("two things of one name were resolved to one")
	}
	for _, want := range []string{testLamp.String(), testStranger.String(), "Objects"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}

	// And nothing says where it looked, since a path that resolved to
	// the wrong folder looks exactly like a missing item.
	_, err = pickEntry(kids, "a hat", "")
	if err == nil || !strings.Contains(err.Error(), "the inventory root") {
		t.Errorf("err = %v", err)
	}
}

// The grid keeps "A Lamp" and "a lamp" as two names, so a name is the
// one spelt that way and a case variant beside it is neither a second
// meaning nor a stand-in.
func TestPickEntryMatchesTheCaseItHas(t *testing.T) {
	kids := []sl.Entry{
		{ID: testLamp, Name: "a lamp"},
		{ID: testStranger, Name: "A Lamp"},
		{ID: testSender, Name: "something else"},
	}
	for want, id := range map[string]msg.UUID{"a lamp": testLamp, "A Lamp": testStranger} {
		if e, err := pickEntry(kids, want, "Objects"); err != nil || e.ID != id {
			t.Errorf("pickEntry(%q) = %v, %v; want %s", want, e.ID, err, id)
		}
	}

	// Another case is not a match, and the refusal says what is there.
	_, err := pickEntry(kids, "SOMETHING ELSE", "Objects")
	if err == nil {
		t.Fatal(`"SOMETHING ELSE" was taken for "something else"`)
	}
	if !strings.Contains(err.Error(), `did you mean "something else"?`) {
		t.Errorf("the refusal does not offer the near miss: %v", err)
	}
}

// A rez sends every permission mask the item carries, so an item that
// lost them on the way through would be rezzed with permissions nobody
// asked for.
func TestAListingEntryBecomesAWholeItem(t *testing.T) {
	e := sl.Entry{
		ID: testLamp, Parent: testRoot, Asset: testStranger, Name: "a lamp",
		Type: int(sl.AssetObject), InvType: 6,
		BaseMask: 0x7ffffff0, OwnerMask: 0x7ffffff1, GroupMask: 2,
		EveryoneMask: 3, NextOwnerMask: 4, SalePrice: 10, SaleType: 1,
	}
	it := itemOf(e)
	if it.ID != e.ID || it.ParentID != e.Parent || it.AssetID != e.Asset {
		t.Errorf("the identifiers did not survive: %+v", it)
	}
	if it.BaseMask != e.BaseMask || it.OwnerMask != e.OwnerMask ||
		it.GroupMask != e.GroupMask || it.EveryoneMask != e.EveryoneMask ||
		it.NextOwnerMask != e.NextOwnerMask {
		t.Errorf("a permission mask was lost: %+v", it)
	}
	if it.SalePrice != e.SalePrice || it.SaleType != e.SaleType {
		t.Errorf("the sale terms were lost: %+v", it)
	}
}

// Every field of an Item comes out of the Entry set.  Each Entry field
// is given a value of its own, so a field left behind -- the flags were,
// and they carry the slot a wearable goes in, and so was the group --
// comes out zero.
func TestAListingEntryLeavesNothingOfItselfBehind(t *testing.T) {
	var e sl.Entry
	v := reflect.ValueOf(&e).Elem()
	for i := range v.NumField() {
		f := v.Field(i)
		switch f.Kind() {
		case reflect.Bool:
			f.SetBool(true)
		case reflect.Int, reflect.Int64:
			f.SetInt(int64(i + 1))
		case reflect.Uint32:
			f.SetUint(uint64(i + 1))
		case reflect.String:
			f.SetString(v.Type().Field(i).Name)
		case reflect.Array:
			f.Index(0).SetUint(uint64(i + 1))
		default:
			t.Fatalf("Entry.%s is a %s, which this test does not fill", v.Type().Field(i).Name, f.Kind())
		}
	}

	it := reflect.ValueOf(itemOf(e)).Elem()
	for i := range it.NumField() {
		if it.Field(i).IsZero() {
			t.Errorf("Item.%s came out zero from an Entry with every field set", it.Type().Field(i).Name)
		}
	}
	if got := itemOf(e).Flags; got != e.Flags {
		t.Errorf("Flags = %#x, want %#x", got, e.Flags)
	}
}

func TestTrustedSaysWhoIsObeyed(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	got := send(t, d, b, "trusted")
	if !strings.Contains(got, testSender.String()) {
		t.Errorf("the trusted id is not listed:\n%s", got)
	}
	if !strings.Contains(got, "Trusted Resident") {
		t.Errorf("the trusted name is not listed:\n%s", got)
	}
	if !strings.Contains(got, "inventory offers: trusted") {
		t.Errorf("what happens to offers is not said:\n%s", got)
	}
}

func TestStatusSaysWhatTheAttendantIsDoing(t *testing.T) {
	d, b, _ := newTestDaemon(t)
	got := send(t, d, b, "status")
	for _, want := range []string{"example: attached", "Example Resident", "in Nowhere at 128", "0 of 4 commands"} {
		if !strings.Contains(got, want) {
			t.Errorf("status did not say %q:\n%s", want, got)
		}
	}
}

// A landmark's whole name is matched as it is spelt, and several of it
// are refused with their ids.  A name only another spelling has is
// refused with that spelling as the hint rather than found by the
// search that follows, which would take it as if it had been typed.
// The search, for part of a name, still ignores case.
func TestPickLandmarkMatchesTheCaseItHas(t *testing.T) {
	id := func(n byte) msg.UUID { return msg.UUID{0: 0xdd, 15: n} }
	kept := []sl.Entry{
		{ID: id(1), Name: "Thrushmoor", Path: "Landmarks/Thrushmoor"},
		{ID: id(2), Name: "thrushmoor", Path: "Landmarks/thrushmoor"},
		{ID: id(3), Name: "Pelmar Reach Workshop", Path: "Landmarks/Pelmar Reach Workshop"},
		{ID: id(4), Name: "Twice", Path: "Landmarks/Twice"},
		{ID: id(5), Name: "Twice", Path: "Twice"},
	}
	for want, n := range map[string]byte{"Thrushmoor": 1, "thrushmoor": 2, "WORKSHOP": 3} {
		if e, err := pickLandmark(kept, want); err != nil || e.ID != id(n) {
			t.Errorf("pickLandmark(%q) = %v, %v; want %s", want, e.ID, err, id(n))
		}
	}

	_, err := pickLandmark(kept, "THRUSHMOOR")
	if err == nil || !strings.Contains(err.Error(), `did you mean "Thrushmoor" or "thrushmoor"?`) {
		t.Errorf("pickLandmark(THRUSHMOOR) = %v; want the two spellings offered", err)
	}
	// One other spelling is not taken for the name either.
	if e, err := pickLandmark(kept[1:], "THRUSHMOOR"); err == nil {
		t.Errorf("pickLandmark took %q for THRUSHMOOR", e.Name)
	}

	_, err = pickLandmark(kept, "Twice")
	if err == nil || !strings.Contains(err.Error(), id(4).String()) || !strings.Contains(err.Error(), id(5).String()) {
		t.Errorf("pickLandmark(Twice) = %v; want both ids", err)
	}
	if _, err := pickLandmark(kept, "nowhere"); err == nil || !strings.Contains(err.Error(), `no landmark called "nowhere"`) {
		t.Errorf("pickLandmark(nowhere) = %v", err)
	}
}

// A worn object is named as it is spelt, and one worn under another
// spelling of the name is offered rather than taken off.
func TestDetachMatchesTheCaseAWornObjectHas(t *testing.T) {
	d, b, f := newTestDaemon(t)
	upper := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-000000000002")
	f.objects = []*sl.Seen{
		{Object: sl.Object{ID: testMe, Local: 1}, PCode: 47},
		{Object: sl.Object{ID: msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-000000000001"),
			Name: "a lamp", Local: 10}, Parent: 1, PCode: 9, AttachItem: testLamp, AttachPoint: 1},
		{Object: sl.Object{ID: msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-000000000002"),
			Name: "A Lamp", Local: 11}, Parent: 1, PCode: 9, AttachItem: upper, AttachPoint: 2},
	}
	detached := func() []msg.UUID {
		var out []msg.UUID
		for _, m := range f.Sent() {
			if d, ok := m.(*msg.DetachAttachmentIntoInv); ok {
				out = append(out, d.ObjectData.ItemID)
			}
		}
		return out
	}

	got := send(t, d, b, "detach A LAMP")
	if !strings.Contains(got, `no worn object "A LAMP"; did you mean "a lamp" or "A Lamp"?`) {
		t.Errorf("detach of a third spelling printed %q", got)
	}
	if got := detached(); len(got) != 0 {
		t.Fatalf("a name in another case took off %v", got)
	}

	if got := send(t, d, b, "detach A Lamp"); !strings.Contains(got, "took off A Lamp") {
		t.Errorf("detach A Lamp printed %q", got)
	}
	if got := detached(); len(got) != 1 || got[0] != upper {
		t.Errorf("detach A Lamp took off %v, want %s", got, upper)
	}
}
