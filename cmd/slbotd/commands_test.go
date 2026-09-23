package main

import (
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
		{ID: testStranger, Name: "A Lamp"},
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

	// One is one, whatever its case.
	e, err := pickEntry(kids, "SOMETHING ELSE", "Objects")
	if err != nil || e.ID != testSender {
		t.Errorf("pickEntry = %v, %v", e, err)
	}

	// And nothing says where it looked, since a path that resolved to
	// the wrong folder looks exactly like a missing item.
	_, err = pickEntry(kids, "a hat", "")
	if err == nil || !strings.Contains(err.Error(), "the inventory root") {
		t.Errorf("err = %v", err)
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
