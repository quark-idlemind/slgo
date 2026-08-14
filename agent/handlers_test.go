package agent

// The state a session owns rather than relays.
//
// Everything here arrives on one message, early, and is never mentioned
// again: the active group, the group memberships, the region's own
// description, where the avatar is standing.  A client that attaches
// afterwards cannot ask for any of it, which is why the session keeps
// it -- and why a handler that drops a field leaves a hole nothing later
// can fill.
//
// The camera is the one with teeth.  AgentUpdate is what puts a session
// in the simulator's interest list, and the interest list is worked out
// from the camera rather than from where the avatar actually is.  Leave
// the camera behind and the simulator stops describing everything around
// the avatar -- including its own attachments -- while cheerfully
// reporting that the teleport succeeded.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

var (
	aGroup      = msg.MustParseUUID("18017e57-7e57-c0de-bc69-01ec1f4dacc2")
	anotherOne  = msg.MustParseUUID("19ba7e57-7e57-c0de-40ef-323fb37693ea")
	aRegion     = msg.MustParseUUID("4e587e57-7e57-c0de-d7c1-83a802086f89")
	otherRegion = msg.MustParseUUID("4fb77e57-7e57-c0de-2998-152c89ddb903")
)

// TestTheActiveGroupIsWhatDecidesWhetherWeMayBuild: a login starts with
// no group active, and a parcel usually grants building to a group
// rather than to individuals.  AgentDataUpdate is where the current one
// arrives, at login and whenever it changes.
func TestTheActiveGroupIsWhatDecidesWhetherWeMayBuild(t *testing.T) {
	t.Parallel()

	a, _ := offlineSession(t)
	if !a.ActiveGroup().IsZero() {
		t.Error("a session starts acting as nobody")
	}

	m := &msg.AgentDataUpdate{}
	m.AgentData.AgentID = a.Account.AgentID
	m.AgentData.ActiveGroupID = aGroup
	m.AgentData.GroupName = []byte("Builders\x00")
	feed(t, a, m)

	if a.ActiveGroup() != aGroup {
		t.Errorf("ActiveGroup = %v, want %v", a.ActiveGroup(), aGroup)
	}
}

// TestGroupMembershipComesFromTheSimulatorAndNotTheLogin: the login
// server answers only what its options ask for, and "groups" is not one
// it honours -- so asking there gets a missing field, which reads exactly
// like belonging to none.  The simulator volunteers the real list moments
// after the handshake.
func TestGroupMembershipComesFromTheSimulatorAndNotTheLogin(t *testing.T) {
	t.Parallel()

	a, _ := offlineSession(t)
	if gs := a.Groups(); len(gs) != 0 {
		t.Errorf("Groups = %v before being told", gs)
	}

	m := &msg.AgentGroupDataUpdate{}
	m.AgentData.AgentID = a.Account.AgentID
	m.GroupData = []msg.AgentGroupDataUpdate_GroupData{
		{GroupID: aGroup, GroupName: []byte("Builders\x00"), GroupPowers: 0x101},
		{GroupID: anotherOne, GroupName: []byte("Everyone Else\x00")},
	}
	feed(t, a, m)

	gs := a.Groups()
	if len(gs) != 2 {
		t.Fatalf("Groups = %+v, want two", gs)
	}
	if gs[0].ID != aGroup || gs[0].Name != "Builders" || gs[0].Powers != 0x101 {
		t.Errorf("first group = %+v", gs[0])
	}
	if gs[1].Name != "Everyone Else" {
		t.Errorf("second group = %+v", gs[1])
	}

	// The slice handed out is a copy: a caller that sorted it in place
	// would be rearranging the session's own list.
	gs[0].Name = "not this"
	if again := a.Groups(); again[0].Name != "Builders" {
		t.Errorf("the list was edited from outside: %q", again[0].Name)
	}
}

// TestWaitGroupsGivesUpRatherThanInsisting: belonging to no groups is a
// perfectly ordinary state and is indistinguishable from not having been
// told yet, so refusing to proceed on account of it would be wrong.
func TestWaitGroupsGivesUpRatherThanInsisting(t *testing.T) {
	t.Parallel()

	a, _ := offlineSession(t)

	// Already known: it answers at once.
	m := &msg.AgentGroupDataUpdate{}
	m.AgentData.AgentID = a.Account.AgentID
	m.GroupData = []msg.AgentGroupDataUpdate_GroupData{
		{GroupID: aGroup, GroupName: []byte("Builders\x00")},
	}
	feed(t, a, m)
	if gs := a.WaitGroups(context.Background(), time.Second); len(gs) != 1 {
		t.Errorf("WaitGroups = %+v with the list already in", gs)
	}

	// Never told: it gives up and says nothing rather than failing.
	b, _ := offlineSession(t)
	start := time.Now()
	if gs := b.WaitGroups(context.Background(), 10*time.Millisecond); gs != nil {
		t.Errorf("WaitGroups = %+v, want nothing", gs)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("WaitGroups waited %s past its deadline", d)
	}

	// Cancelled while waiting: it hands back whatever it has, which is
	// the same answer a moment earlier.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if gs := b.WaitGroups(ctx, time.Minute); len(gs) != 0 {
		t.Errorf("WaitGroups after cancellation = %+v", gs)
	}
}

// TestTheRegionIntroducesItselfOnce: RegionHandshake is the only time
// the simulator describes where we are, and the reply to it is what the
// simulator waits for before saying anything else.
func TestTheRegionIntroducesItselfOnce(t *testing.T) {
	t.Parallel()

	a, sent := offlineSession(t)
	if _, known := a.Region(); known {
		t.Error("a region is known before the handshake")
	}

	feed(t, a, handshakeFor(aRegion, "the test region"))

	r, known := a.Region()
	if !known {
		t.Fatal("the handshake did not register")
	}
	if r.ID != aRegion || r.Name != "the test region" {
		t.Errorf("region = %+v", r)
	}
	if r.Flags != 0x1234 || r.Extended != 0x5678 || r.Protocols != 9 {
		t.Errorf("flags %#x extended %#x protocols %d", r.Flags, r.Extended, r.Protocols)
	}
	if r.ProductName != "Estate / Full Region" || r.ProductSKU != "023" || r.ColoName != "Dallas" {
		t.Errorf("product = %+v", r)
	}
	if !r.EstateManager || r.WaterHeight != 20 || r.Access != 13 || r.CPURatio != 4 {
		t.Errorf("region = %+v", r)
	}
	if a.RegionName() != "the test region" {
		t.Errorf("RegionName = %q", a.RegionName())
	}

	// The simulator is answered, and the wait is released.
	sent.waitFor(t, 1)
	if out := sent.messages(t); out[0].MsgInfo().Name != "RegionHandshakeReply" {
		t.Errorf("answered the handshake with %s", out[0].MsgInfo().Name)
	}
	if err := a.WaitForRegionHandshake(context.Background(), time.Second); err != nil {
		t.Errorf("WaitForRegionHandshake: %v", err)
	}
}

// TestCrossingToAnotherRegionForgetsTheOldOne: the object cache is only
// correct for the region it was filled in, and nothing on the wire says
// so.  A second handshake with a different region id is the notice.
//
// The comparison is on the id and not the name, because two regions can
// share a name and a region can be renamed without becoming another
// place.
func TestCrossingToAnotherRegionForgetsTheOldOne(t *testing.T) {
	t.Parallel()

	a, _ := offlineSession(t)
	a.SetLook(Look{Far: 128})

	feed(t, a, handshakeFor(aRegion, "the test region"))
	feed(t, a, arriving(t, msg.ObjectUpdate_ObjectData{
		ID: 1, FullID: aPrim, ObjectData: placement(msg.Vector3{}, msg.Quaternion{}),
	}))

	// The same region again, under a new name: nothing is forgotten.
	feed(t, a, handshakeFor(aRegion, "renamed since"))
	if a.Objects().Count() != 1 {
		t.Error("a rename emptied the object cache")
	}

	feed(t, a, handshakeFor(otherRegion, "somewhere else"))
	if a.Objects().Count() != 0 {
		t.Errorf("%d objects from the old region survived the crossing", a.Objects().Count())
	}
}

// handshakeFor builds a RegionHandshake with every field this package
// reads filled in with something distinguishable.
func handshakeFor(id msg.UUID, name string) *msg.RegionHandshake {
	m := &msg.RegionHandshake{}
	m.RegionInfo.SimName = append([]byte(name), 0)
	m.RegionInfo.RegionFlags = 0x1234
	m.RegionInfo.SimAccess = 13
	m.RegionInfo.SimOwner = anOwner
	m.RegionInfo.IsEstateManager = true
	m.RegionInfo.WaterHeight = 20
	m.RegionInfo2.RegionID = id
	m.RegionInfo3.ProductName = []byte("Estate / Full Region\x00")
	m.RegionInfo3.ProductSKU = []byte("023\x00")
	m.RegionInfo3.ColoName = []byte("Dallas\x00")
	m.RegionInfo3.CPUClassID = 12
	m.RegionInfo3.CPURatio = 4
	m.RegionInfo4 = []msg.RegionHandshake_RegionInfo4{
		{RegionFlagsExtended: 0x5678, RegionProtocols: 9},
	}
	return m
}

// TestAnOlderSimulatorSendsNoRegionInfo4: the block is variable, so a
// simulator that omits it must leave the extended flags at nothing
// rather than take the message down.
func TestAnOlderSimulatorSendsNoRegionInfo4(t *testing.T) {
	t.Parallel()

	a, _ := offlineSession(t)
	m := handshakeFor(aRegion, "an older sim")
	m.RegionInfo4 = nil
	feed(t, a, m)

	r, _ := a.Region()
	if r.Extended != 0 || r.Protocols != 0 {
		t.Errorf("extended %#x protocols %d, want nothing", r.Extended, r.Protocols)
	}
	if r.Name != "an older sim" {
		t.Errorf("the rest of the handshake was lost: %+v", r)
	}
}

// TestTheCameraFollowsTheAvatar: this is the session's job and not a
// client's.  A client that exits leaves the camera wherever it was, and
// a camera in the wrong place quietly empties the interest list for
// every client that connects afterwards.
func TestTheCameraFollowsTheAvatar(t *testing.T) {
	t.Parallel()

	a, _ := offlineSession(t)

	arrived := &msg.AgentMovementComplete{}
	arrived.Data.Position = msg.Vector3{X: 188.4, Y: 202.8, Z: 26.3}
	arrived.Data.LookAt = msg.Vector3{X: 1}
	arrived.Data.RegionHandle = 0x0003_f000_0003_e800
	arrived.SimData.ChannelVersion = []byte("Second Life Server 2026-07-10\x00")
	feed(t, a, arrived)

	if a.Position() != (msg.Vector3{X: 188.4, Y: 202.8, Z: 26.3}) {
		t.Errorf("position = %+v", a.Position())
	}
	if a.RegionHandle() == 0 || !strings.HasPrefix(a.ChannelVersion(), "Second Life Server") {
		t.Errorf("handle %d, channel %q", a.RegionHandle(), a.ChannelVersion())
	}
	if a.Look().Center != a.Position() {
		t.Errorf("camera at %+v, avatar at %+v", a.Look().Center, a.Position())
	}
	// setCenter fills in a draw distance for a session that has not been
	// given one, since a camera that can see nothing is no better than a
	// camera in the wrong place.
	if a.Look().Far != DefaultDrawDistance {
		t.Errorf("draw distance = %v", a.Look().Far)
	}

	// A teleport within the region moves both.
	tp := &msg.TeleportLocal{}
	tp.Info.Position = msg.Vector3{X: 10, Y: 20, Z: 30}
	tp.Info.LookAt = msg.Vector3{Y: 1}
	feed(t, a, tp)
	if a.Position() != (msg.Vector3{X: 10, Y: 20, Z: 30}) || a.Look().Center != a.Position() {
		t.Errorf("after a local teleport: avatar %+v, camera %+v", a.Position(), a.Look().Center)
	}
}

// TestWalkingAwayIsOnlyEverReportedCoarsely: CoarseLocationUpdate is the
// only thing that keeps arriving as an avatar walks, so it is what stops
// the camera drifting away from one that moved without teleporting.  It
// is whole metres, and four of them vertically, which is ample for
// deciding what is nearby.
func TestWalkingAwayIsOnlyEverReportedCoarsely(t *testing.T) {
	t.Parallel()

	a, _ := offlineSession(t)

	m := &msg.CoarseLocationUpdate{}
	m.Location = []msg.CoarseLocationUpdate_Location{
		{X: 1, Y: 2, Z: 3},
		{X: 100, Y: 110, Z: 7},
	}
	m.Index.You = 1
	feed(t, a, m)

	if want := (msg.Vector3{X: 100, Y: 110, Z: 28}); a.Position() != want {
		t.Errorf("position = %+v, want %+v", a.Position(), want)
	}
	if a.Look().Center != a.Position() {
		t.Errorf("the camera did not follow: %+v", a.Look().Center)
	}

	// An index outside the block is a message about somebody else's
	// view, and moving the camera to a location we were not given would
	// be worse than ignoring it.
	for _, you := range []int16{-1, 2} {
		m := &msg.CoarseLocationUpdate{}
		m.Location = []msg.CoarseLocationUpdate_Location{{X: 9, Y: 9, Z: 9}}
		m.Index.You = you
		feed(t, a, m)
		if want := (msg.Vector3{X: 100, Y: 110, Z: 28}); a.Position() != want {
			t.Errorf("index %d moved the avatar to %+v", you, a.Position())
		}
	}
}

// TestBeingKickedIsNotSomethingToRecoverFrom: a supervisor that treats
// every ending as a fault will, when you log the avatar into a viewer,
// wait a few seconds and take it back -- and then do it again, for as
// long as you keep trying.  So a kick is a distinct error, and the
// reason is carried along for a person to read.
func TestBeingKickedIsNotSomethingToRecoverFrom(t *testing.T) {
	t.Parallel()

	a, _ := offlineSession(t)
	if reason, ok := a.Kicked(); ok || reason != "" {
		t.Errorf("Kicked = %q, %v on a healthy session", reason, ok)
	}

	m := &msg.KickUser{}
	m.UserInfo.Reason = []byte("logged in elsewhere\x00")
	feed(t, a, m)

	reason, ok := a.Kicked()
	if !ok || reason != "logged in elsewhere" {
		t.Errorf("Kicked = %q, %v", reason, ok)
	}
	select {
	case <-a.Done():
	default:
		t.Error("a kicked session is still running")
	}

	var k *Kicked
	if !errors.As(a.Err(), &k) || k.Reason != "logged in elsewhere" {
		t.Fatalf("Err = %v (%T), wanted a *Kicked", a.Err(), a.Err())
	}
	if Retryable(a.Err()) {
		t.Error("a kick was reported as worth retrying")
	}
	if !strings.Contains(k.Error(), "logged in elsewhere") {
		t.Errorf("Error = %q, should carry the reason", k.Error())
	}

	// The grid does not always say why, and a kick with no reason is
	// still a kick.
	silent := &Kicked{}
	if !strings.Contains(silent.Error(), "no more than that") {
		t.Errorf("Error = %q", silent.Error())
	}
	if Retryable(silent) {
		t.Error("a silent kick was reported as worth retrying")
	}

	// Everything else is worth another go: a fault nobody understands
	// is more likely to be a lost circuit than a decision.
	if !Retryable(errors.New("the network went away")) || !Retryable(nil) {
		t.Error("an ordinary failure was reported as final")
	}
}

// TestWaitingOnASessionThatHasEndedDoesNotHang: every wait in the
// handshake goes through the same place, and a session that ends while
// something is waiting on it has to say so rather than sit out the
// timeout.
func TestWaitingOnASessionThatHasEndedDoesNotHang(t *testing.T) {
	t.Parallel()

	a, _ := offlineSession(t)
	close(a.done)

	err := a.WaitForRegionHandshake(context.Background(), time.Minute)
	if err == nil || !strings.Contains(err.Error(), "RegionHandshake") {
		t.Errorf("err = %v, should name what it was waiting for", err)
	}

	// And a caller that gives up is answered with its own reason.
	b, _ := offlineSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.WaitForRegionHandshake(ctx, time.Minute); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want the context's own error", err)
	}
}

// TestLogoutReplyEndsTheWait: Logout is bounded either way, but a
// simulator that answers should not be waited out.
func TestLogoutReplyEndsTheWait(t *testing.T) {
	t.Parallel()

	a, sent := offlineSession(t)
	go func() {
		// The reply arrives once the request is on its way.
		sent.waitFor(t, 1)
		feed(t, a, &msg.LogoutReply{})
	}()

	// A timeout of nothing takes the default rather than giving up at
	// once, which would make every logout look like a failure.
	if err := a.Logout(context.Background(), 0); err != nil {
		t.Fatalf("logout: %v", err)
	}
	out := sent.messages(t)
	if len(out) == 0 || out[0].MsgInfo().Name != "LogoutRequest" {
		t.Errorf("sent %v", out)
	}
}

// TestWireStringsLoseTheirTerminator: the protocol nul ends its strings
// and a Go string that keeps it compares equal to nothing.
func TestWireStringsLoseTheirTerminator(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		in   []byte
		want string
	}{
		{[]byte("Builders\x00"), "Builders"},
		{[]byte("no terminator"), "no terminator"},
		{[]byte{0}, ""},
		{nil, ""},
	} {
		if got := trimNul(c.in); got != c.want {
			t.Errorf("trimNul(%q) = %q, want %q", c.in, got, c.want)
		}
		if got := trimNulBytes(c.in); got != c.want {
			t.Errorf("trimNulBytes(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestAFriendListThatWasNeverSeededStillTakesNotifications: Connect
// always installs the login response's list, even an empty one, so the
// map is there before anything can arrive -- but nothing about these two
// requires that to have happened, and a nil map would panic rather than
// merely lose a friend.
func TestAFriendListThatWasNeverSeededStillTakesNotifications(t *testing.T) {
	t.Parallel()

	a := &Agent{}
	a.setOnline([]msg.UUID{oneFriend}, true)
	if fs := a.Friends(); len(fs) != 1 || !fs[0].Online {
		t.Errorf("Friends = %+v", fs)
	}

	b := &Agent{}
	b.NoteFriend(otherFriend, true)
	if fs := b.Friends(); len(fs) != 1 || fs[0].ID != otherFriend {
		t.Errorf("Friends = %+v", fs)
	}
}

// TestAnAvatarAboveTheCoarseCeilingKeepsTheHeightItHad.
//
// The height in a coarse location is one byte of four metre steps, so it
// stops at 1020 and 255 means "higher than this can say".  Read as a
// height it puts the camera a kilometre under an avatar on a skybox --
// and then everything the region describes is judged against a place the
// avatar is not: the objects around it, and the other avatars standing
// beside it, arrive already out of range and are dropped.  A region
// describes an object once, so nothing brings them back.
//
// Measured against Second Life before this: three avatars at about 2001m
// were each reported at exactly 1020, and none could see any of the
// others, nor its own avatar.
func TestAnAvatarAboveTheCoarseCeilingKeepsTheHeightItHad(t *testing.T) {
	t.Parallel()

	a, _ := offlineSession(t)

	// Something that carries the height in full puts it up there.
	up := &msg.AgentMovementComplete{}
	up.Data.Position = msg.Vector3{X: 30, Y: 70, Z: 2001}
	feed(t, a, up)
	if got := a.Position().Z; got != 2001 {
		t.Fatalf("the avatar starts at %v, want 2001", got)
	}

	// Now a coarse update with the height at its ceiling.  Sideways
	// movement is still worth having -- it is whole metres and correct
	// -- but the height it reports is not a height.
	m := &msg.CoarseLocationUpdate{}
	m.Location = []msg.CoarseLocationUpdate_Location{{X: 33, Y: 75, Z: 255}}
	m.Index.You = 0
	feed(t, a, m)

	if want := (msg.Vector3{X: 33, Y: 75, Z: 2001}); a.Position() != want {
		t.Errorf("position = %+v, want %+v -- 255 is not 1020", a.Position(), want)
	}
	if a.Look().Center.Z != 2001 {
		t.Errorf("the camera dropped to %v, a kilometre below the avatar", a.Look().Center.Z)
	}

	// Below the ceiling it is used, since there it is an answer.
	m = &msg.CoarseLocationUpdate{}
	m.Location = []msg.CoarseLocationUpdate_Location{{X: 33, Y: 75, Z: 8}}
	m.Index.You = 0
	feed(t, a, m)
	if got := a.Position().Z; got != 32 {
		t.Errorf("a height under the ceiling gave %v, want 32", got)
	}
}
