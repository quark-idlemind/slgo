package agent

// Whether the session can tell what its avatar is doing.
//
// The three transitions below are the ones measured on Agni and written
// up in doc/history/sit.md, and each one is reproduced here as the
// messages that carried it: an ObjectUpdate for this avatar with a
// parent in it, an AvatarAnimation and nothing else, and an
// ObjectUpdate with the parent back to zero.  They go through the
// encoder and the decoder on the way in, like everything else fed to a
// dispatcher here, so what the handlers see is what a simulator would
// have sent.

import (
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

// aSeat is the box that was sat on, and theSeat its local id in the
// region -- the numbering is the region's own, so the number itself
// means nothing beyond being the one the simulator used.
var aSeat = msg.MustParseUUID("8eb77e57-7e57-c0de-f6db-de5d4b8d2189")

const (
	theSeat   uint32 = 83600601
	meLocally uint32 = 2001
)

// avatarPlacement is the seventy-six byte placement blob an avatar's
// ObjectUpdate carries: a collision plane first, then the same sixty
// bytes a prim's has.  The width is the only thing that says which form
// it is, so it is written out in full rather than borrowed from a prim.
func avatarPlacement(pos msg.Vector3) []byte {
	w := &blob{}
	w.vec(msg.Vector3{Z: 1}) // collision plane, four floats
	w.f32(0)
	w.raw(placement(pos, msg.Quaternion{}))
	return w.b
}

// ownUpdate is this avatar's own ObjectUpdate, as the simulator sends it
// when it seats the avatar and again when it stands: the same block,
// with the parent set to the seat's local id or back to zero.
func ownUpdate(t *testing.T, a *Agent, parent uint32) *msg.ObjectUpdate {
	t.Helper()
	return arriving(t, msg.ObjectUpdate_ObjectData{
		ID:         meLocally,
		FullID:     a.Account.AgentID,
		PCode:      pcodeAvatar,
		ParentID:   parent,
		ObjectData: avatarPlacement(msg.Vector3{X: 29, Y: 72, Z: 2001}),
	})
}

// seatUpdate is the box itself, which the region describes like any
// other prim and which is what gives the seat a full id.
func seatUpdate(t *testing.T) *msg.ObjectUpdate {
	t.Helper()
	return arriving(t, msg.ObjectUpdate_ObjectData{
		ID:         theSeat,
		FullID:     aSeat,
		PCode:      9,
		Scale:      msg.Vector3{X: 1, Y: 1, Z: 1},
		ObjectData: placement(msg.Vector3{X: 29, Y: 72, Z: 2001}, msg.Quaternion{}),
	})
}

// animating builds the AvatarAnimation that says these are playing on
// this avatar.  The whole set arrives every time, so this is the whole
// set.
func animating(who msg.UUID, ids ...msg.UUID) *msg.AvatarAnimation {
	m := &msg.AvatarAnimation{}
	m.Sender.ID = who
	for i, id := range ids {
		m.AnimationList = append(m.AnimationList, msg.AvatarAnimation_AnimationList{
			AnimID:         id,
			AnimSequenceID: int32(i + 1),
		})
	}
	return m
}

// standingSession is a session that has heard where its avatar is and
// nothing yet about it sitting.
func standingSession(t *testing.T) *Agent {
	t.Helper()
	a, _ := offlineSession(t)
	a.SetLook(Look{Center: msg.Vector3{X: 29, Y: 72, Z: 2001}, Far: 128})
	return a
}

// TestSittingOnAnObjectIsTheReparenting: the simulator answers a sit
// request by moving the avatar and sending its ObjectUpdate back with
// the seat's local id as the parent.  That message is the whole answer,
// and everything a caller needs to name what it is sitting on is in it
// and in what the cache already knows about the object.
func TestSittingOnAnObjectIsTheReparenting(t *testing.T) {
	t.Parallel()

	a := standingSession(t)
	feed(t, a, seatUpdate(t), ownUpdate(t, a, theSeat),
		animating(a.Account.AgentID, AnimSit))

	p, seat := a.Posture()
	if p != SittingOnObject {
		t.Errorf("posture = %v, want sitting on an object", p)
	}
	if seat.Local != theSeat {
		t.Errorf("seat local id = %d, want %d", seat.Local, theSeat)
	}
	if seat.ID != aSeat {
		t.Errorf("seat id = %v, want %v", seat.ID, aSeat)
	}

	got, seated := a.Seat()
	if !seated || got != seat {
		t.Errorf("Seat = %+v, %v; want %+v, true", got, seated, seat)
	}
}

// TestSittingOnTheGroundIsOnlyTheAnimation: there is no message for a
// ground sit and no reply to it.  The animation list is the only thing
// that arrives, so a session that did not keep it could not tell a
// ground sit from standing there.
func TestSittingOnTheGroundIsOnlyTheAnimation(t *testing.T) {
	t.Parallel()

	a := standingSession(t)
	feed(t, a, ownUpdate(t, a, 0),
		animating(a.Account.AgentID, AnimSitGroundConstrained))

	p, seat := a.Posture()
	if p != SittingOnGround {
		t.Errorf("posture = %v, want sitting on the ground", p)
	}
	if seat != (Seat{}) {
		t.Errorf("seat = %+v, want nothing: a ground sit is seated on nothing", seat)
	}

	// The pair is the whole point: seated on nothing has to read
	// differently from not seated.
	got, seated := a.Seat()
	if !seated {
		t.Error("Seat says not seated, so a ground sit is indistinguishable from standing")
	}
	if got != (Seat{}) {
		t.Errorf("Seat = %+v, want the zero seat", got)
	}
}

// TestStandingClearsBoth: standing up sends the parent back to zero and
// drops the sit from the animation list.  Either half left behind would
// leave the session reporting a sit that has ended.
func TestStandingClearsBoth(t *testing.T) {
	t.Parallel()

	a := standingSession(t)
	feed(t, a, seatUpdate(t), ownUpdate(t, a, theSeat),
		animating(a.Account.AgentID, AnimSit))
	if p, _ := a.Posture(); p != SittingOnObject {
		t.Fatalf("posture before standing = %v, want sitting on an object", p)
	}

	feed(t, a, ownUpdate(t, a, 0), animating(a.Account.AgentID, AnimStand))

	p, seat := a.Posture()
	if p != Standing {
		t.Errorf("posture = %v, want standing", p)
	}
	if seat != (Seat{}) {
		t.Errorf("seat = %+v, want nothing", seat)
	}
	if _, seated := a.Seat(); seated {
		t.Error("still seated after standing up")
	}
	if p.Seated() {
		t.Error("Standing.Seated() is true")
	}
}

// TestStandingFromTheGroundClearsTheAnimation: a stand from a ground sit
// has only the animation to show for it, so the list replacing rather
// than accumulating is the whole of how it is noticed.
func TestStandingFromTheGroundClearsTheAnimation(t *testing.T) {
	t.Parallel()

	a := standingSession(t)
	feed(t, a, animating(a.Account.AgentID, AnimSitGround))
	if p, _ := a.Posture(); p != SittingOnGround {
		t.Fatalf("posture before standing = %v, want sitting on the ground", p)
	}

	feed(t, a, animating(a.Account.AgentID, AnimStand))
	if p, _ := a.Posture(); p != Standing {
		t.Errorf("posture = %v, want standing", p)
	}
	if got := a.Animations(); len(got) != 1 || got[0] != AnimStand {
		t.Errorf("animations = %v; the list should have been replaced, not added to", got)
	}
}

// TestTheParentWinsOverTheAnimation: scripted furniture stops the
// standard sit and plays its own pose, so an avatar plainly sitting on a
// chair can be running nothing recognisable.  Deciding on the animation
// would call it standing.
func TestTheParentWinsOverTheAnimation(t *testing.T) {
	t.Parallel()

	somethingScripted := msg.MustParseUUID("55e27e57-7e57-c0de-c8c8-d1c85f983b8e")

	a := standingSession(t)
	feed(t, a, seatUpdate(t), ownUpdate(t, a, theSeat),
		animating(a.Account.AgentID, somethingScripted))

	if p, _ := a.Posture(); p != SittingOnObject {
		t.Errorf("posture = %v, want sitting on an object", p)
	}
}

// TestAParentedAvatarIsNeverAGroundSitter: the disagreement the other
// way round.  A ground animation on a parented avatar is the object's
// doing -- a script may play whatever it likes -- and the parent is
// still what the simulator believes.
func TestAParentedAvatarIsNeverAGroundSitter(t *testing.T) {
	t.Parallel()

	a := standingSession(t)
	feed(t, a, seatUpdate(t), ownUpdate(t, a, theSeat),
		animating(a.Account.AgentID, AnimSitGroundConstrained))

	p, seat := a.Posture()
	if p != SittingOnObject {
		t.Errorf("posture = %v, want sitting on an object", p)
	}
	if seat.Local != theSeat {
		t.Errorf("seat local id = %d, want %d", seat.Local, theSeat)
	}
}

// TestASitBeforeTheAnimationArrivesIsStillASit: the reparenting and the
// animation are two messages and nothing promises which lands first.
// The parent alone is enough.
func TestASitBeforeTheAnimationArrivesIsStillASit(t *testing.T) {
	t.Parallel()

	a := standingSession(t)
	feed(t, a, seatUpdate(t), ownUpdate(t, a, theSeat))

	if p, _ := a.Posture(); p != SittingOnObject {
		t.Errorf("posture = %v, want sitting on an object", p)
	}
	if len(a.Animations()) != 0 {
		t.Errorf("animations = %v, want none heard yet", a.Animations())
	}
}

// TestAnUndescribedSeatIsStillNamedByItsLocalId: the reparenting can
// arrive before the object it names has been described, and a seat
// beyond the draw distance is never described at all.  The local id is
// what the simulator said and is worth handing back on its own.
func TestAnUndescribedSeatIsStillNamedByItsLocalId(t *testing.T) {
	t.Parallel()

	a := standingSession(t)
	feed(t, a, ownUpdate(t, a, theSeat))

	seat, seated := a.Seat()
	if !seated {
		t.Fatal("not seated, though the simulator reparented the avatar")
	}
	if seat.Local != theSeat {
		t.Errorf("seat local id = %d, want %d", seat.Local, theSeat)
	}
	if !seat.ID.IsZero() {
		t.Errorf("seat id = %v, want nothing: the object was never described", seat.ID)
	}
}

// TestOtherAvatarsAnimationsAreNotKept: this message arrives for every
// avatar in range, in full, about every three seconds.  Keeping the
// crowd's is a list that grows with the region, and somebody else
// sitting down must not make this avatar sit.
func TestOtherAvatarsAnimationsAreNotKept(t *testing.T) {
	t.Parallel()

	a := standingSession(t)
	feed(t, a, animating(someone, AnimSit), animating(avatarID(7), AnimSitGround))

	if got := a.Animations(); len(got) != 0 {
		t.Errorf("animations = %v, want none: those were somebody else's", got)
	}
	if p, _ := a.Posture(); p != Standing {
		t.Errorf("posture = %v; another avatar sitting down seated this one", p)
	}
}

// TestNothingHeardYetIsStanding: neither signal has said otherwise, and
// that is the honest answer rather than a default.
func TestNothingHeardYetIsStanding(t *testing.T) {
	t.Parallel()

	a := standingSession(t)
	p, seat := a.Posture()
	if p != Standing || seat != (Seat{}) {
		t.Errorf("posture = %v, seat = %+v; want standing on nothing", p, seat)
	}
	if _, seated := a.Seat(); seated {
		t.Error("seated, before anything at all had arrived")
	}
}

// TestGettingUpIsNotSitting: sit_to_stand plays on the way out of a sit,
// so counting it would go on reporting a sit for the length of the
// transition.
func TestGettingUpIsNotSitting(t *testing.T) {
	t.Parallel()

	if SittingAnimation(AnimSitToStand) {
		t.Error("sit_to_stand counts as sitting")
	}
	for _, id := range []msg.UUID{
		AnimSit, AnimSitFemale, AnimSitGeneric, AnimSitGround, AnimSitGroundConstrained,
	} {
		if !SittingAnimation(id) {
			t.Errorf("%v does not count as sitting", id)
		}
	}
	if SittingAnimation(AnimStand) {
		t.Error("the stand counts as sitting")
	}
}

// TestTheAnimationListIsCopiedOut: a caller that edited what it was
// given would be editing the set the next message is compared against.
func TestTheAnimationListIsCopiedOut(t *testing.T) {
	t.Parallel()

	a := standingSession(t)
	feed(t, a, animating(a.Account.AgentID, AnimSitGround))

	got := a.Animations()
	if len(got) != 1 {
		t.Fatalf("animations = %v, want the one", got)
	}
	got[0] = msg.UUID{}
	if p, _ := a.Posture(); p != SittingOnGround {
		t.Errorf("posture = %v after a caller edited the list it was handed", p)
	}
}

// TestASeatedAvatarIsWhereItsSeatIs: sitting moves an avatar several
// metres and the coarse update saying so is seconds behind -- measured
// on Agni, about ten of them.  The exact answer is in hand the whole
// time, because the update that seats an avatar gives its position as an
// offset from the seat rather than as a place in the region.
func TestASeatedAvatarIsWhereItsSeatIs(t *testing.T) {
	t.Parallel()

	a := standingSession(t)
	seat := arriving(t, msg.ObjectUpdate_ObjectData{
		ID:         theSeat,
		FullID:     aSeat,
		PCode:      9,
		Scale:      msg.Vector3{X: 1, Y: 1, Z: 1},
		ObjectData: placement(msg.Vector3{X: 30, Y: 72, Z: 2000}, msg.Quaternion{}),
	})
	// The offset a measured sit produced, on a seat a couple of metres
	// from where the avatar had been standing.
	on := arriving(t, msg.ObjectUpdate_ObjectData{
		ID:         meLocally,
		FullID:     a.Account.AgentID,
		PCode:      pcodeAvatar,
		ParentID:   theSeat,
		ObjectData: avatarPlacement(msg.Vector3{X: -0.59, Y: 0, Z: 0.88}),
	})
	feed(t, a, seat, on)

	at := a.Position()
	want := msg.Vector3{X: 30 - 0.59, Y: 72, Z: 2000.88}
	if at != want {
		t.Errorf("a seated avatar is at %v, want %v: the seat's place plus the offset", at, want)
	}
}

// TestAnAvatarOnNothingIsWhereTheSimulatorSaidItWas: the composition
// above is for a seat and nothing else.  An avatar with no parent has a
// position of its own, and nothing about it is an offset from anything.
func TestAnAvatarOnNothingIsWhereTheSimulatorSaidItWas(t *testing.T) {
	t.Parallel()

	a := standingSession(t)
	feed(t, a, ownUpdate(t, a, 0))

	if at := a.Position(); at == (msg.Vector3{X: 29.41, Y: 72, Z: 2001.88}) {
		t.Errorf("a standing avatar was placed as though it were seated: %v", at)
	}
}
