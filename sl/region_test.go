package sl

// Being told the avatar is somewhere else.
//
// Two halves, and a test that checks only the first would pass on a
// clear that took everything: what belongs to the region has to go, and
// what belongs to the avatar and to other people has to stay.

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// goguenName goes with the goguen handle in teleport_test.go, which is
// the one Agni answered with when stage 0 teleported there.  A region a
// session has really been to is worth more here than a plausible one.
const goguenName = "Sandbox Goguen"

// wornObject is the attachment this avatar is wearing, and wornItem the
// inventory item it was worn from.
var (
	wornObject = msg.MustParseUUID("53b27e57-7e57-c0de-b946-959fa3f4b335")
	wornItem   = msg.MustParseUUID("cf8e7e57-7e57-c0de-1cf7-c6fcd1e07e1e")
)

// TestARegionChangeDropsWhatTheRegionSaidAndKeepsWhatItDidNot is the
// heart of it.  Almost everything a session remembers came from the
// region, in the region's own numbering, and the next region hands the
// same numbers out again for other things -- so a local id kept across
// a teleport does not go vague, it goes wrong.
func TestARegionChangeDropsWhatTheRegionSaidAndKeepsWhatItDidNot(t *testing.T) {
	w, f := newFakeSession(t)

	// A region that has described itself: a prim with a child, an
	// attachment on this avatar, a kill, and an object whose contents
	// were asked for.
	f.Relay(t, anUpdate(msg.ObjectUpdate_ObjectData{FullID: thePrim, ID: 77}))
	f.Relay(t, anUpdate(msg.ObjectUpdate_ObjectData{FullID: theChild, ID: 78, ParentID: 77}))
	f.Relay(t, anUpdate(msg.ObjectUpdate_ObjectData{
		FullID:    wornObject,
		ID:        79,
		NameValue: []byte("AttachItemID STRING RW DS " + wornItem.String() + "\n"),
	}))
	f.Relay(t, familyReply(thePrim, nemo, "a market stall"))
	f.Relay(t, &msg.KillObject{ObjectData: []msg.KillObject_ObjectData{{ID: 78}}})

	inv := &msg.ReplyTaskInventory{}
	inv.InventoryData.TaskID = thePrim
	inv.InventoryData.Filename = append([]byte("inventory_88fa7e57"), 0)
	f.Relay(t, inv)

	// And what the GRID said, which no region owns: a name learned, a
	// name still waited for, something the simulator said out loud, and
	// somebody's offer of a teleport.
	f.Relay(t, namesReply(map[msg.UUID]string{nemo: "Jorr Starlit"}))
	if err := w.AskNames(context.Background(), []msg.UUID{unnamed}); err != nil {
		t.Fatalf("AskNames: %v", err)
	}
	alert := &msg.AlertMessage{}
	alert.AlertData.Message = append([]byte("this parcel will not have you"), 0)
	f.Relay(t, alert)
	lure := offered(w)

	// The state is only worth clearing if it is there.  Without this a
	// clear that worked and a session that never heard anything look
	// exactly alike.
	w.mu.Lock()
	filled := len(w.locals) == 3 && len(w.parents) == 3 && len(w.killed) == 1 &&
		len(w.attach) == 1 && len(w.owners) == 1 && len(w.objectNames) == 1 &&
		len(w.taskInv) == 1 && len(w.taskSeen) == 1 && len(w.asking) == 1
	w.mu.Unlock()
	if !filled {
		t.Fatal("the session did not take in what the region said, so " +
			"clearing it would prove nothing")
	}

	f.RelayRegion(t, goguenName, goguen)

	w.mu.Lock()
	defer w.mu.Unlock()

	// Gone: the region's numbering, and its answers about things that
	// are somewhere else now.
	for _, kept := range []struct {
		what string
		n    int
	}{
		{"object ids to local ids", len(w.locals)},
		{"local ids to their parents", len(w.parents)},
		{"the objects reported killed", len(w.killed)},
		{"the attachments as the last region described them", len(w.attach)},
		{"who owns what", len(w.owners)},
		{"what things are called", len(w.objectNames)},
		{"the task inventory filenames", len(w.taskInv)},
		{"which objects answered about their contents", len(w.taskSeen)},
		{"the names still being waited for", len(w.asking)},
	} {
		if kept.n != 0 {
			t.Errorf("%s: %d kept from the region the avatar left", kept.what, kept.n)
		}
	}

	// Kept: what the grid answered, what a person offered, and what was
	// said out loud while we were listening.
	if got := w.names[nemo]; got != "Jorr Starlit" {
		t.Errorf("who somebody is was forgotten on the way: %q", got)
	}
	if len(w.lures) != 1 || w.lures[lure.From] == nil {
		t.Errorf("%d teleport offers survived, want the one somebody made", len(w.lures))
	}
	if len(w.alerts) != 1 {
		t.Errorf("%d alerts survived, want the one the simulator said", len(w.alerts))
	}
	// And the subscription machinery, which is not state at all: these
	// are callers waiting, and the teleport that caused this is very
	// probably one of them.
	if w.chatSubs == nil || w.imSubs == nil || w.permSubs == nil || w.regionSubs == nil {
		t.Error("a region change took the subscriptions with it")
	}
}

// TestASubscriberIsToldWhichRegionTheAvatarIsInNow: a caller that had
// to poll Where could not tell two teleports apart, and would learn
// about the first one whenever it next happened to look.
func TestASubscriberIsToldWhichRegionTheAvatarIsInNow(t *testing.T) {
	w, f := newFakeSession(t)

	ch := w.RegionChanges(0)
	defer w.StopRegionChanges(ch)

	f.RelayRegion(t, goguenName, goguen)

	select {
	case c := <-ch:
		if c.Region != goguenName || c.Handle != goguen {
			t.Errorf("told %q handle %d, want %q handle %d",
				c.Region, c.Handle, goguenName, goguen)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a subscriber was not told the avatar had moved")
	}
}

// TestARegionSubscriptionIsClosedWhenTheSessionEnds: every kind of
// subscription is, and missing one is not a smaller version of the same
// bug -- a caller ranging over the one that was missed waits for ever.
func TestARegionSubscriptionIsClosedWhenTheSessionEnds(t *testing.T) {
	w, f := newFakeSession(t)

	ch := w.RegionChanges(0)
	f.Close()

	select {
	case _, ok := <-ch:
		if ok {
			t.Error("something was delivered to a subscription after the session ended")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a region subscription outlived the session it belonged to")
	}
}

// TestARegionSubscriptionAskedForTooLateIsHandedBackClosed: a caller
// ranging over one is owed an end to range to, and a session that has
// already stopped has nothing to deliver.
func TestARegionSubscriptionAskedForTooLateIsHandedBackClosed(t *testing.T) {
	w, f := newFakeSession(t)
	f.Close()
	select {
	case <-w.readDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the reader did not stop")
	}

	ch := w.RegionChanges(0)
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("a subscription taken out after the end delivered something")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a subscription taken out after the end was left open")
	}
}

// TestBothBackendsSayTheSameThingAboutARegionChange: the whole point of
// Backend is that nothing above it can tell which one it has.  The two
// hear about a move in entirely different ways -- one off a notice on
// the daemon's stream, the other from the agent in this process -- and
// what comes out of the relay has to be the same either way.
func TestBothBackendsSayTheSameThingAboutARegionChange(t *testing.T) {
	h, d := newFakeDaemon(t)
	d.relay <- &pb.ServerPacket{Body: &pb.ServerPacket_Notice{Notice: &pb.AgentEvent{
		Kind:         pb.AgentEvent_REGION_CHANGED,
		Detail:       "the avatar is now in " + goguenName,
		Region:       goguenName,
		RegionHandle: goguen,
	}}}

	var hosted *RegionChange
	select {
	case hosted = <-h.RegionChanges():
	case <-time.After(5 * time.Second):
		t.Fatal("the hosted backend never passed the notice on")
	}

	direct := aDirectSession(t)
	direct.regions = make(chan *RegionChange, 1)
	direct.regionChanged(goguenName, goguen)

	var got *RegionChange
	select {
	case got = <-direct.RegionChanges():
	case <-time.After(5 * time.Second):
		t.Fatal("the direct backend never passed the change on")
	}

	if !reflect.DeepEqual(hosted, got) {
		t.Errorf("hosted said %+v and direct said %+v", hosted, got)
	}
}

// TestAnythingOtherThanARegionChangeIsNotOne: the notice stream carries
// disconnections too, and a session that took one of those for a move
// would throw away everything it holds every time the circuit blinked.
func TestAnythingOtherThanARegionChangeIsNotOne(t *testing.T) {
	h, d := newFakeDaemon(t)
	d.relay <- &pb.ServerPacket{Body: &pb.ServerPacket_Notice{Notice: &pb.AgentEvent{
		Kind: pb.AgentEvent_DISCONNECTED, Detail: "the simulator went quiet",
	}}}

	// The notice itself arrives, which is what says the relay ran at
	// all rather than the packet being lost.
	select {
	case ev := <-h.Conn().Notices():
		if ev.Kind != pb.AgentEvent_DISCONNECTED {
			t.Fatalf("the notice came back as %v", ev.Kind)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the notice never arrived")
	}
	select {
	case c := <-h.RegionChanges():
		t.Errorf("a disconnection was passed on as a region change: %+v", c)
	default:
	}
}
