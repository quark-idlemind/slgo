package main

// Moving things and moving about.
//
// These commands are the ones that change something, so their careful
// halves are the point: give reports an offer rather than a transfer,
// move waits for the object to have moved instead of reading back the
// position it had before, and cp refuses a folder outright.  All of
// that is reachable over a fake grid; what is not is noted in
// coverage-notes/slsh-shell.md.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/internal/session"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"

	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// TestPositionReadsThreeNumbers, and refuses anything else rather than
// putting an object at the origin.
func TestPositionReadsThreeNumbers(t *testing.T) {
	got, err := position([]string{"10", "20.5", "-3"})
	if err != nil {
		t.Fatal(err)
	}
	if got != (msg.Vector3{X: 10, Y: 20.5, Z: -3}) {
		t.Errorf("position = %v", got)
	}

	// A trailing comma survives, because a position copied off the
	// screen comes with them.
	if got, err := position([]string{"10,", " 20, ", "30"}); err != nil || got.Y != 20 {
		t.Errorf("a copied position: %v %v", got, err)
	}

	for _, args := range [][]string{{}, {"1", "2"}, {"1", "2", "3", "4"}, {"1", "over there", "3"}} {
		if _, err := position(args); err == nil {
			t.Errorf("%q should not be a position", args)
		}
	}
}

// TestNearIsLooseEnoughForAFloat32AndTightEnoughToMeanArrived.
func TestNearIsLooseEnoughForAFloat32AndTightEnoughToMeanArrived(t *testing.T) {
	at := msg.Vector3{X: 10, Y: 20, Z: 30}
	if !near(at, at) {
		t.Error("a position is near itself")
	}
	if !near(msg.Vector3{X: 10.1, Y: 19.9, Z: 30.2}, at) {
		t.Error("rounding on the way through a float32 is not a different place")
	}
	for _, off := range []msg.Vector3{{X: 11, Y: 20, Z: 30}, {X: 10, Y: 21, Z: 30}, {X: 10, Y: 20, Z: 31}} {
		if near(off, at) {
			t.Errorf("%v is a metre away, which is not arrived", off)
		}
	}
}

// TestGiveOffersRatherThanGives.
//
// Nothing moves until they accept, and nothing here can tell whether
// they did, so "offered" is the honest report.
func TestGiveOffersRatherThanGives(t *testing.T) {
	x := newTestShell(t)
	x.setListed([]person{{ID: testSomebody, Name: "Some Body"}})

	got := x.do(t, "give 1 readme")
	if got != "offered \"readme\" to Some Body\n" {
		t.Errorf("give printed %q", got)
	}

	// The offer carries the asset type in its bucket, which is what
	// tells the far end what it is being offered.
	sent := x.grid.Sent()
	if len(sent) != 1 {
		t.Fatalf("%d messages went out, want the one offer", len(sent))
	}
	im, ok := sent[0].(*msg.ImprovedInstantMessage)
	if !ok {
		t.Fatalf("the offer went out as %T", sent[0])
	}
	if got, want := im.MessageBlock.BinaryBucket[0], byte(sl.AssetNotecard); got != want {
		t.Errorf("the bucket says asset type %d, want %d", got, want)
	}
}

// TestGivingAFolderGivesItAsAFolder: AssetCategory is how the protocol
// offers a whole folder, and an offer marked as the folder's preferred
// type would arrive as something the viewer cannot open.
func TestGivingAFolderGivesItAsAFolder(t *testing.T) {
	x := newTestShell(t)
	x.setListed([]person{{ID: testSomebody, Name: "Some Body"}})

	if got := x.do(t, "give 1 Objects"); !strings.Contains(got, `offered "Objects"`) {
		t.Fatalf("give printed %q", got)
	}
	im := x.grid.Sent()[0].(*msg.ImprovedInstantMessage)
	if got, want := im.MessageBlock.BinaryBucket[0], byte(sl.AssetCategory); got != want {
		t.Errorf("a folder went as asset type %d, want AssetCategory %d", got, want)
	}
}

// TestGiveNeedsBothHalvesOfTheQuestion.
func TestGiveNeedsBothHalvesOfTheQuestion(t *testing.T) {
	x := newTestShell(t)

	for _, line := range []string{"give", "give 1"} {
		if err := x.Do(context.Background(), line); err == nil {
			t.Errorf("%q should be refused", line)
		}
	}
	if got := x.do(t, "give --help"); !strings.Contains(got, "WHO PATH") {
		t.Errorf("give --help printed %q", got)
	}

	// Somebody who is not in the last listing.
	if got := x.do(t, "give 3 readme"); !strings.Contains(got, "no 3 in the last listing") {
		t.Errorf("give with a number nobody showed printed %q", got)
	}
	// Something that is not in inventory.
	x.setListed([]person{{ID: testSomebody, Name: "Some Body"}})
	if got := x.do(t, "give 1 nothing-of-the-sort"); !strings.Contains(got, "nothing called") {
		t.Errorf("give with a path that is not there printed %q", got)
	}

	// And an offer the circuit would not take is not an offer.
	x.grid.sendErr = errors.New("the circuit is down")
	got := x.do(t, "give 1 readme")
	if !strings.Contains(got, "the circuit is down") {
		t.Errorf("give should report the failure, got %q", got)
	}
	if strings.Contains(got, "offered") {
		t.Errorf("a send that failed is not an offer: %q", got)
	}
}

// TestCopyDuplicatesAnItemUnderANewName.
//
// The point is not tidiness: an avatar that may not rez cannot make an
// object at all, but it can copy one it already owns, because copying
// asks the land nothing.
func TestCopyDuplicatesAnItemUnderANewName(t *testing.T) {
	x := newTestShell(t)
	copied := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000c0")

	// The grid makes the copy when it is asked to, which is what the
	// command watches inventory for.
	x.grid.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.CopyInventoryItem); !ok {
			return
		}
		x.grid.mu.Lock()
		defer x.grid.mu.Unlock()
		x.grid.inv.Items = append(x.grid.inv.Items,
			&invItem{ID: copied, Name: "readme again", Type: int(sl.AssetNotecard)})
	}

	if got, want := x.do(t, "cp readme readme again"), copied.String()+"\n"; got != want {
		t.Errorf("cp printed %q, want the new id %q", got, want)
	}
}

// TestCopyRefusesAFolder: a folder is not an item and the grid does not
// answer, so the refusal has to come from here or the command hangs for
// its whole timeout and then blames the item.
func TestCopyRefusesAFolder(t *testing.T) {
	x := newTestShell(t)
	if got := x.do(t, "cp Objects elsewhere"); !strings.Contains(got, "only items can be copied") {
		t.Errorf("cp of a folder printed %q", got)
	}

	for _, line := range []string{"cp", "cp readme"} {
		if err := x.Do(context.Background(), line); err == nil {
			t.Errorf("%q should be refused", line)
		}
	}
	if got := x.do(t, "cp --help"); !strings.Contains(got, "PATH NAME") {
		t.Errorf("cp --help printed %q", got)
	}
	if got := x.do(t, "cp nothing-of-the-sort elsewhere"); !strings.Contains(got, "nothing called") {
		t.Errorf("cp of nothing printed %q", got)
	}

	// A grid that will not take the request is reported as itself.
	x.grid.sendErr = errors.New("the circuit is down")
	if got := x.do(t, "cp readme elsewhere"); !strings.Contains(got, "the circuit is down") {
		t.Errorf("cp should report the failure, got %q", got)
	}
}

// TestCopyGoesIntoTheFolderTheShellIsIn, which is what cp with a bare
// name means everywhere else -- so a shell standing in a folder that
// has been renamed or removed underneath it has nowhere to put the copy
// and has to say so.
func TestCopyGoesIntoTheFolderTheShellIsIn(t *testing.T) {
	x := newTestShell(t)
	// The item is named by id, so finding it does not depend on where
	// the shell thinks it is standing; the destination does.
	x.cwd = []string{"a folder that went away"}

	got := x.do(t, "cp "+testNote.String()+" a copy")
	if !strings.Contains(got, "no folder") {
		t.Errorf("cp into a folder that is not there printed %q", got)
	}
}

// TestTPSaysHowItIsTypedRatherThanGuessing.
//
// Three numbers is a position and anything else is a region's name, so
// the one line that is neither -- numbers, and not three of them -- is a
// position typed short.  Asking the grid's map about a region called
// "128 128" would answer a question nobody asked and take a round trip
// over it.
func TestTPSaysHowItIsTypedRatherThanGuessing(t *testing.T) {
	x := newTestShell(t)

	for _, line := range []string{"tp", "tp 128 128", "tp 128 128 25 30"} {
		got := x.do(t, line)
		if !strings.Contains(got, "usage: tp") {
			t.Errorf("%q printed %q", line, got)
		}
	}
	if got := x.grid.Sent(); len(got) != 0 {
		t.Errorf("a teleport nobody could read went out anyway: %v", got)
	}
	if got := x.do(t, "tp --help"); !strings.Contains(got, "X Y Z") {
		t.Errorf("tp --help printed %q", got)
	}
}

// TestTPWithThreeNumbersAsksTheMapNothing.
//
// A move inside the region is the cheap thing it has always been: the
// simulator already has the avatar, nothing about the session changes,
// and there is no name to look up.  A version that sent every teleport
// through the map would work and would cost a round trip on the grid for
// every step across a room.
func TestTPWithThreeNumbersAsksTheMapNothing(t *testing.T) {
	x := newTestShell(t)

	if got, want := x.do(t, "tp 130 128 25"), "Test Region at 128, 128, 25\n"; got != want {
		t.Errorf("tp printed %q, want %q", got, want)
	}
	for _, m := range x.grid.Sent() {
		if _, ok := m.(*msg.MapNameRequest); ok {
			t.Error("a position in this region was looked up on the map")
		}
	}
	sent := lastTeleport(t, x)
	if sent.Info.RegionHandle != testRegionHandle {
		t.Errorf("a local teleport named region %d", sent.Info.RegionHandle)
	}
	if sent.Info.Position != (msg.Vector3{X: 130, Y: 128, Z: 25}) {
		t.Errorf("tp asked for %v", sent.Info.Position)
	}
}

// TestTPReportsWhereItEndedUp rather than where it was aimed: the
// simulator stands the avatar on whatever is under the point.
func TestTPReportsWhereItEndedUp(t *testing.T) {
	x := newTestShell(t)

	// The fake is already standing there, so the move is agreed to at
	// once -- which is what a teleport of a few metres looks like.
	if got, want := x.do(t, "tp 128 128 25"), "Test Region at 128, 128, 25\n"; got != want {
		t.Errorf("tp printed %q, want %q", got, want)
	}

	// The read-back afterwards is a second question, and it can fail
	// on its own.
	x.grid.presenceCalls, x.grid.presenceFailAt = 0, 3
	if got := x.do(t, "tp 128 128 25"); !strings.Contains(got, "went away mid-command") {
		t.Errorf("tp should report a failed read-back, got %q", got)
	}

	x.grid.presenceFailAt = 0
	x.grid.sendErr = errors.New("the circuit is down")
	if got := x.do(t, "tp 128 128 25"); !strings.Contains(got, "the circuit is down") {
		t.Errorf("tp should report a refused send, got %q", got)
	}
}

// TestTPByNameGoesToTheHandleTheMapGave, which is the only thing a
// teleport can be addressed to: a name is not in the request at all.
//
// The line saying where it is going comes first and before any of the
// waiting, because a cross-region teleport takes about four hundred
// milliseconds and has been measured at five seconds, and a shell that
// has silently stopped answering is indistinguishable from one that has
// hung.
func TestTPByNameGoesToTheHandleTheMapGave(t *testing.T) {
	x := newTestShell(t)
	x.grid.AnswerMap(t, mapBlock("Sandbox Goguen", 995, 997, 13))
	x.grid.AnswerTeleport(t, "Sandbox Goguen", goguenHandle)

	got := x.do(t, "tp Sandbox Goguen 33 73 2001")
	said := strings.Index(got, "teleporting to Sandbox Goguen")
	arrived := strings.Index(got, "Sandbox Goguen at")
	if said < 0 || arrived < 0 || said > arrived {
		t.Fatalf("tp should say where it is going and then where it arrived:\n%s", got)
	}
	// The name travels to the map as it was typed, spaces and all, with
	// the position taken off the end of it rather than made part of it.
	var asked string
	for _, m := range x.grid.Sent() {
		if q, ok := m.(*msg.MapNameRequest); ok {
			asked = strings.TrimRight(string(q.NameData.Name), "\x00")
		}
	}
	if asked != "Sandbox Goguen" {
		t.Errorf("the map was asked about %q", asked)
	}
	sent := lastTeleport(t, x)
	if sent.Info.RegionHandle != goguenHandle {
		t.Errorf("tp asked for region %d, want the handle the map gave", sent.Info.RegionHandle)
	}
	if sent.Info.Position != (msg.Vector3{X: 33, Y: 73, Z: 2001}) {
		t.Errorf("tp asked to arrive at %v", sent.Info.Position)
	}
}

// TestTPWithNoPositionArrivesInTheMiddleOfTheRegion, which is where a
// viewer puts somebody who typed a name into the world map and said
// nothing about where in it.
//
// The height is asked for as nothing rather than guessed at: the
// simulator stands the avatar on whatever is under the point, and there
// is no cheap way to ask how high the ground is somewhere this session
// has never been.
func TestTPWithNoPositionArrivesInTheMiddleOfTheRegion(t *testing.T) {
	x := newTestShell(t)
	x.grid.AnswerMap(t, mapBlock("Sandbox Goguen", 995, 997, 13))
	x.grid.AnswerTeleport(t, "Sandbox Goguen", goguenHandle)

	x.do(t, "tp Sandbox Goguen")
	if got := lastTeleport(t, x).Info.Position; got != (msg.Vector3{X: 128, Y: 128}) {
		t.Errorf("tp with no position asked to arrive at %v, want the middle of the region", got)
	}
}

// TestTPWillNotChooseBetweenTheRegionsANameMatched.
//
// The map's search is by prefix, so a name that matches several is the
// ordinary case and not a mistake.  Choosing the closest row would be
// this command deciding which of somebody's several possibilities they
// meant -- and a wrong guess here is not a listing to read again, it is
// an avatar somewhere it was not sent.
func TestTPWillNotChooseBetweenTheRegionsANameMatched(t *testing.T) {
	x := newTestShell(t)
	x.grid.AnswerMap(t,
		mapBlock("Sandbox Goguen", 995, 997, 13),
		mapBlock("Sandbox Goguen Margin", 43552, 43552, 13))

	got := x.do(t, "tp Sandbox")
	for _, want := range []string{"Sandbox Goguen", "Sandbox Goguen Margin", "name in full"} {
		if !strings.Contains(got, want) {
			t.Errorf("tp should list what matched and refuse, want %q:\n%s", want, got)
		}
	}
	for _, m := range x.grid.Sent() {
		if _, ok := m.(*msg.TeleportLocationRequest); ok {
			t.Fatal("an ambiguous name teleported the avatar anyway")
		}
	}
}

// TestTPTakesANameTypedInFullOverTheLongerOnesBesideIt.
//
// A name typed in full is not an ambiguous name.  Since the search is by
// prefix, every longer name comes back with it, so a command that
// refused whenever more than one row arrived could never reach a region
// whose name is the beginning of another's.
func TestTPTakesANameTypedInFullOverTheLongerOnesBesideIt(t *testing.T) {
	x := newTestShell(t)
	x.grid.AnswerMap(t,
		mapBlock("Sandbox Goguen", 995, 997, 13),
		mapBlock("Sandbox Goguen Margin", 43552, 43552, 13))
	x.grid.AnswerTeleport(t, "Sandbox Goguen", goguenHandle)

	// And in whatever case it was typed in, because the map's search
	// ignores case and a person typing a name does too.
	if got := x.do(t, "tp sandbox goguen"); !strings.Contains(got, "Sandbox Goguen at") {
		t.Fatalf("tp should take the name typed in full:\n%s", got)
	}
	if got := lastTeleport(t, x).Info.RegionHandle; got != goguenHandle {
		t.Errorf("tp went to region %d, want the one named in full", got)
	}
}

// TestTPReportsBothVoicesOfARefusal.
//
// The refusal is the grid's and it says two things at once: a key a
// program could act on and a sentence meant for a person.  They are not
// always the same string, and neither can be worked out from the other,
// so a shell that printed one of them would leave whoever read it
// without either the reason or the name of it.
func TestTPReportsBothVoicesOfARefusal(t *testing.T) {
	x := newTestShell(t)
	x.grid.AnswerMap(t, mapBlock("Sandbox Bricker", 43554, 43552, 42))
	x.grid.AnswerTeleportWith(t, agniRefusedTeleport)

	got := x.do(t, "tp Sandbox Bricker")
	for _, want := range []string{"MustHaveVIPStatus", "premium or vip subscriber"} {
		if !strings.Contains(got, want) {
			t.Errorf("the refusal should carry %q:\n%s", want, got)
		}
	}
}

// TestTPGivesUpAfterTheWaitItWasGiven.
//
// A teleport answered with nothing at all is a real answer rather than a
// fault to retry: once this avatar has been handed off, the simulator it
// left answers no further teleport request, ever.  So --wait is what
// says how long to believe in one, and this shell has a default of its
// own rather than the ninety seconds sl gives a caller that names none.
func TestTPGivesUpAfterTheWaitItWasGiven(t *testing.T) {
	x := newTestShell(t)
	x.grid.AnswerMap(t, mapBlock("Sandbox Goguen", 995, 997, 13))

	start := time.Now()
	got := x.do(t, "tp --wait 1 Sandbox Goguen")
	if !strings.Contains(got, "nothing at all") {
		t.Errorf("tp should say what silence means:\n%s", got)
	}
	if took := time.Since(start); took > 15*time.Second {
		t.Errorf("tp waited %s, which is not the second it was given", took.Round(time.Second))
	}
}

// lastTeleport is the teleport the shell asked for, and a failure when
// it asked for none.
func lastTeleport(t *testing.T, x *testShell) *msg.TeleportLocationRequest {
	t.Helper()
	var last *msg.TeleportLocationRequest
	for _, m := range x.grid.Sent() {
		if r, ok := m.(*msg.TeleportLocationRequest); ok {
			last = r
		}
	}
	if last == nil {
		t.Fatal("nothing asked to be teleported anywhere")
	}
	return last
}

// goguenHandle is the region the map blocks above stand for: grid square
// (995, 997), which is the destination stage 0 measured a real teleport
// to and back from.
const goguenHandle = 1094014069892352

// agniRefusedTeleport is a region that would not have this avatar, as
// Agni sent it: a key in one block and a sentence for a person in the
// other.
const agniRefusedTeleport = `<llsd><map>` +
	`<key>AlertInfo</key><array><map>` +
	`<key>ExtraParams</key><string></string>` +
	`<key>Message</key><string>MustHaveVIPStatus</string></map></array>` +
	`<key>Info</key><array><map>` +
	`<key>Reason</key><string>You must be a premium or vip subscriber ` +
	`to enter this region.</string></map></array></map></llsd>`

// TestMoveNeedsExactlyOneObjectOfThatName.
//
// Two of a name is not an ambiguity to guess at: move sets a position,
// a rotation and a scale at once, so picking the wrong one reshapes it.
func TestMoveNeedsExactlyOneObjectOfThatName(t *testing.T) {
	x := newTestShell(t)

	if got := x.do(t, "move probe 1 2"); !strings.Contains(got, "NAME X Y Z") {
		t.Errorf("move with too few arguments printed %q", got)
	}
	if got := x.do(t, "move probe 1 2 over-there"); !strings.Contains(got, "is not a number") {
		t.Errorf("move with a bad position printed %q", got)
	}
	if got := x.do(t, "move --help"); !strings.Contains(got, "NAME X Y Z") {
		t.Errorf("move --help printed %q", got)
	}
	if got := x.do(t, "move probe 10 20 30"); !strings.Contains(got, `no object named "probe"`) {
		t.Errorf("move of nothing printed %q", got)
	}

	x.grid.objects = []*sl.Seen{
		{Object: sl.Object{ID: testLamp, Local: 1, Name: "probe"}, PCode: 9},
		{Object: sl.Object{ID: testNote, Local: 2, Name: "probe"}, PCode: 9},
	}
	if got := x.do(t, "move probe 10 20 30"); !strings.Contains(got, "2 objects are called") {
		t.Errorf("move of two of a name printed %q", got)
	}

	x.grid.objectsErr = errors.New("nobody is holding this session")
	if got := x.do(t, "move probe 10 20 30"); !strings.Contains(got, "nobody is holding") {
		t.Errorf("move should report the failure, got %q", got)
	}
}

// TestMoveWaitsForTheObjectToHaveMoved.
//
// sl.Place is fire and forget: the simulator answers with an
// ObjectUpdate whenever it gets round to it, so an immediate re-read
// returns the position the object had BEFORE the move and reports it
// with total confidence.  A stale answer is worse than none, because
// nothing about it looks wrong.
func TestMoveWaitsForTheObjectToHaveMoved(t *testing.T) {
	x := newTestShell(t)
	at := msg.Vector3{X: 10, Y: 20, Z: 30}
	x.grid.objects = []*sl.Seen{
		{Object: sl.Object{ID: testLamp, Local: 1, Name: "probe"}, PCode: 9, Position: at},
	}

	if got, want := x.do(t, "move probe 10 20 30"), "probe is at 10.0, 20.0, 30.0\n"; got != want {
		t.Errorf("move printed %q, want %q", got, want)
	}

	// An object that goes away while it is being moved is a failure
	// rather than a move that never finishes.
	x.grid.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.MultipleObjectUpdate); !ok {
			return
		}
		x.grid.mu.Lock()
		defer x.grid.mu.Unlock()
		x.grid.objects = nil
	}
	if got := x.do(t, "move probe 10 20 30"); !strings.Contains(got, "is not in the region") {
		t.Errorf("move of something that vanished printed %q", got)
	}

	// A move the circuit would not take is reported rather than waited
	// out: there is nothing to wait for.
	x.grid.onSend = nil
	x.grid.objects = []*sl.Seen{
		{Object: sl.Object{ID: testLamp, Local: 1, Name: "probe"}, PCode: 9, Position: at},
	}
	x.grid.sendErr = errors.New("the circuit is down")
	if got := x.do(t, "move probe 10 20 30"); !strings.Contains(got, "the circuit is down") {
		t.Errorf("move should report a refused send, got %q", got)
	}
}

// TestAutoCountsWhatIsWornAndSaysWhatThatBuys.
//
// The number is not the point: what a person wants to know is how many
// benchmarks can run at once, which is that number divided by the size
// of a group.
func TestAutoCountsWhatIsWornAndSaysWhatThatBuys(t *testing.T) {
	x := newTestShell(t)
	if got := x.do(t, "auto"); !strings.Contains(got, "0 auto objects worn") {
		t.Errorf("auto with nothing worn printed %q", got)
	}
	if got := x.do(t, "auto"); !strings.Contains(got, "sets up the rest") {
		t.Errorf("auto should say how to set the rest up, got %q", got)
	}

	// Enough of them worn that it stops offering to set any up.
	x.grid.mu.Lock()
	objs := x.grid.inv.Dirs[0]
	for i := range session.AutoPoints {
		id := msg.UUID{15: byte(i + 1)}
		objs.Items = append(objs.Items, &invItem{ID: id, Name: session.AutoName(i)})
		// Worn by us: an attachment hangs off the avatar wearing it,
		// and what is worn by somebody else does not count.
		x.grid.objects = append(x.grid.objects, &sl.Seen{
			Object: sl.Object{ID: msg.UUID{14: byte(i + 1)}, Local: uint32(i + 1)},
			PCode:  9, Owner: testMe, AttachItem: id, AttachPoint: session.AutoPoints[i],
		})
	}
	x.grid.mu.Unlock()

	got := x.do(t, "auto")
	if !strings.Contains(got, "so 3 runs at once") {
		t.Errorf("auto should say how many benchmarks fit:\n%s", got)
	}
	if strings.Contains(got, "sets up the rest") {
		t.Errorf("a full set has no rest to set up:\n%s", got)
	}

	x.grid.objectsErr = errors.New("nobody is holding this session")
	if got := x.do(t, "auto"); !strings.Contains(got, "nobody is holding") {
		t.Errorf("auto should report the failure, got %q", got)
	}
	if got := x.do(t, "auto --help"); !strings.Contains(got, "-n") {
		t.Errorf("auto --help printed %q", got)
	}
}

// TestAutoWillNotRearrangeObjectsUnderARunningBenchmark.
//
// Setting up moves attachments about, and a benchmark holding a group
// has its base readings in those objects' linkset data.  So -n takes
// every group's lock first and gives up if any of them is busy, rather
// than doing half the work and finding out.
func TestAutoWillNotRearrangeObjectsUnderARunningBenchmark(t *testing.T) {
	x := newTestShell(t)
	x.grid.lockedBy = "autobench"

	got := x.do(t, "auto -n 4")
	if !strings.Contains(got, "in use by autobench") {
		t.Errorf("auto -n should say who has the objects, got %q", got)
	}
	if !strings.Contains(got, "cannot be done under a running benchmark") {
		t.Errorf("auto -n should say why it will not, got %q", got)
	}
}

// --------------------------------------------------------- the daemon

// TestTheDaemonCommandsSayWhenThereIsNoDaemon.
//
// A session logged in directly has no daemon behind it, and these four
// have nothing to ask.  Saying so is the whole of their answer, and it
// has to name what to do instead.
func TestTheDaemonCommandsSayWhenThereIsNoDaemon(t *testing.T) {
	x := newTestShell(t)

	if got := x.do(t, "agents"); !strings.Contains(got, "logged in directly, not through a daemon") {
		t.Errorf("agents printed %q", got)
	}
	for _, line := range []string{"login somebody", "logout somebody", "status"} {
		if got := x.do(t, line); !strings.Contains(got, "logged in directly") {
			t.Errorf("%q printed %q", line, got)
		}
	}
}

// TestAgentsListsInTheDaemonsOrder, which is not alphabetical: oldest
// first, and the first hosted one is what a command that names no agent
// gets.  That order is the information.
func TestAgentsListsInTheDaemonsOrder(t *testing.T) {
	x, d := newDaemonShell(t)
	d.agents = []*pb.AgentInfo{
		{Name: "stopped", AvatarName: "Old Resident",
			State: pb.AgentInfo_STOPPED, Detail: "logged out on request"},
		{Name: "first", AvatarName: "One Resident", Region: "Test Region",
			State: pb.AgentInfo_HOSTED},
		{Name: "fake", AvatarName: "Quark Idlemind", Region: "Test Region",
			State: pb.AgentInfo_HOSTED},
	}

	got := x.do(t, "agents")
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("agents printed %d lines:\n%s", len(lines), got)
	}
	// The mark is the daemon's first choice, which is only meaningful
	// among the ones it is actually holding.
	if strings.HasPrefix(lines[0], "*") {
		t.Errorf("a stopped agent is not the default: %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "* first") {
		t.Errorf("the first hosted agent should be marked: %q", lines[1])
	}
	if !strings.Contains(lines[0], "stopped: logged out on request") {
		t.Errorf("a stopped agent should say so where the region goes: %q", lines[0])
	}
	if !strings.Contains(lines[2], "<- this shell") {
		t.Errorf("the session this shell is attached to should be marked: %q", lines[2])
	}

	d.fail = errors.New("the daemon is going down")
	if got := x.do(t, "agents"); !strings.Contains(got, "the daemon is going down") {
		t.Errorf("agents should report the failure, got %q", got)
	}
	if got := x.do(t, "agents --help"); !strings.Contains(got, "agents") {
		t.Errorf("agents --help printed %q", got)
	}
}

// TestLoginIsSafeToRepeat: one already up comes back as already up
// rather than being logged in a second time, which would kick the
// session it has.
//
// This command was "host" until the pair was made to read login and
// logout.  Nothing answers to the old name, and the first line here is
// what would notice if an alias for it were ever added.
func TestLoginIsSafeToRepeat(t *testing.T) {
	x, d := newDaemonShell(t)
	d.host = &pb.HostResponse{Agent: &pb.AgentInfo{
		AvatarName: "One Resident", Region: "Test Region"}}

	if got := x.do(t, "host first"); !strings.Contains(got, "no such command") {
		t.Errorf("host is not a command any more, and printed %q", got)
	}
	if got := x.do(t, "login first"); got != "first: One Resident in Test Region\n" {
		t.Errorf("login printed %q", got)
	}

	d.host.Already = true
	if got := x.do(t, "login first"); !strings.Contains(got, "was already up") {
		t.Errorf("login of one already up printed %q", got)
	}

	for _, line := range []string{"login", "login one two"} {
		if got := x.do(t, line); !strings.Contains(got, "flags come before the name") {
			t.Errorf("%q printed %q", line, got)
		}
	}
	if got := x.do(t, "login --help"); !strings.Contains(got, "-f") {
		t.Errorf("login --help printed %q", got)
	}

	d.fail = errors.New("no profile of that name")
	if got := x.do(t, "login first"); !strings.Contains(got, "no profile of that name") {
		t.Errorf("login should report the failure, got %q", got)
	}
}

// TestLogoutSaysItWillStayOut, since that is the part a person has to
// know: it does not come back on its own.
func TestLogoutSaysItWillStayOut(t *testing.T) {
	x, d := newDaemonShell(t)
	d.logout = &pb.LogoutResponse{}

	if got := x.do(t, "logout first"); !strings.Contains(got, `until "login first" asks for it`) {
		t.Errorf("logout printed %q", got)
	}

	for _, line := range []string{"logout", "logout one two"} {
		if got := x.do(t, line); !strings.Contains(got, "flags come before the name") {
			t.Errorf("%q printed %q", line, got)
		}
	}
	if got := x.do(t, "logout --help"); !strings.Contains(got, "-f") {
		t.Errorf("logout --help printed %q", got)
	}

	d.fail = errors.New("no agent named \"first\"")
	if got := x.do(t, "logout first"); !strings.Contains(got, "no agent named") {
		t.Errorf("logout should report the failure, got %q", got)
	}
}

// TestLogoutRefusalNamesWhoIsUsingIt: the person deciding whether to
// force it has to be told what they would be interrupting.
//
// It used not to be told.  slgod returned the names on a LogoutResponse
// beside the error, and a gRPC unary call carries a message or a status
// and never both -- so the transport dropped the message and the client
// got (nil, err) every time.  The names go in the status details now,
// which is the one place they survive next to a refusal.
func TestLogoutRefusalNamesWhoIsUsingIt(t *testing.T) {
	x, d := newDaemonShell(t)
	d.logout = &pb.LogoutResponse{Clients: []string{"autobench", "slsh"}}
	d.fail = errors.New("first is in use by autobench, slsh; use force to log out anyway")

	if got := x.do(t, "logout first"); !strings.Contains(got, "attached: autobench, slsh") {
		t.Errorf("the refusal should name who is using it, got %q", got)
	}
}
