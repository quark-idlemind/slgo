package agent

import (
	"fmt"
	"sync"

	"github.com/quark-idlemind/slgo/msg"
)

// What the avatar is doing with itself, which is two facts arriving by
// two different roads.
//
// A sit on an object is a reparenting.  The simulator answers
// AgentRequestSit by moving the avatar -- up to about ten metres, over
// whatever is in the way -- and sending this avatar's own ObjectUpdate
// back with ParentID set to the seat's local id: measured on Agni, 123
// milliseconds after the request, with the position in it now an offset
// from the object rather than a place in the region.  Standing sends
// another with the parent back to zero.  All of that is in the object
// cache already, so half of this costs nothing new.
//
// A ground sit is not a message at all.  It is a control flag on
// AgentUpdate, the simulator answers nothing and reparents nothing, and
// the only evidence anywhere on the wire that it happened is the
// animation list: AvatarAnimation, 144 milliseconds later, carrying the
// constrained ground sit.  So the animations are kept here too, and
// without them a seated avatar and a standing one are the same silence.
//
// See doc/history/sit.md, where those measurements are written down.

// The well known animations a sit and a stand play.
//
// Named here rather than spelled at the point of use because they are
// the viewer's own constants and the viewer is the authority for them:
// every id below was read out of indra/llcharacter/llanimationstates.cpp
// in a Firestorm checkout, where they are ANIM_AGENT_SIT and its
// neighbours.
//
// Three of them were also watched arriving on Agni, and the two accounts
// agree exactly: AnimSit was running while the avatar sat on a box and
// was gone after it stood, AnimSitGroundConstrained arrived for a ground
// sit, and AnimStand arrived after standing.  The rest are the viewer's
// word alone -- which of them a particular sit plays depends on the
// avatar's shape and on what the object it is sitting on asks for, and
// no attempt has been made here to provoke each one.
var (
	// AnimSit is the ordinary sit on an object.
	AnimSit = msg.MustParseUUID("1a5fe8ac-a804-8a5d-7cbd-56bd83184568")

	// AnimSitFemale and AnimSitGeneric are the viewer's other two names
	// for sitting on something.  Neither was seen on Agni, and what
	// decides which of the three a given sit plays has not been looked
	// into: they are here so that a sit playing one of them is not read
	// as a stand.
	AnimSitFemale  = msg.MustParseUUID("b1709c8d-ecd3-54a1-4f28-d55ac0840782")
	AnimSitGeneric = msg.MustParseUUID("245f3c54-f1c0-bf2e-811f-46d8eeb386e7")

	// AnimSitGround and AnimSitGroundConstrained are the ground sit.
	// The constrained one is what a measured ground sit played; the
	// other has only its name to say when it is used instead.
	AnimSitGround            = msg.MustParseUUID("1c7600d6-661f-b87b-efe2-d7421eb93c86")
	AnimSitGroundConstrained = msg.MustParseUUID("1a2bd58e-87ff-0df8-0b4c-53e047b0bb6e")

	// AnimSitToStand is getting up, and is deliberately not one of the
	// sitting animations below: it plays while the avatar is on its way
	// out of a sit, so counting it would have Posture go on reporting a
	// sit for as long as the transition lasts.
	AnimSitToStand = msg.MustParseUUID("a8dee56f-2eae-9e7a-05a2-6fb92b97e21e")

	// AnimStand is the idle a standing avatar plays, and is what
	// arrived after standing up from both kinds of sit.  There are four
	// more numbered stands the viewer cycles through; none of them is
	// needed here, because standing is what is left when no sit is
	// running rather than something positively asserted.
	AnimStand = msg.MustParseUUID("2408fe9e-df1d-1d7d-f4ff-1384fa7b350f")
)

// SittingAnimation says whether an animation is one a seated avatar
// plays.  Any of them means seated.
//
// Which one it is would say something about how the avatar is seated --
// the two ground ones are only ever a ground sit -- but nothing here
// leans on that, because for an object sit the parent is a better answer
// than the animation could be.  See Posture.
func SittingAnimation(id msg.UUID) bool {
	switch id {
	case AnimSit, AnimSitFemale, AnimSitGeneric, AnimSitGround, AnimSitGroundConstrained:
		return true
	}
	return false
}

// animations is what the simulator says is playing on this avatar.
//
// This avatar's only, and that is a decision rather than an oversight.
// AvatarAnimation arrives for every avatar in range, in full, about
// every three seconds, so keeping the crowd's would be keeping a list
// that grows with the region and is rewritten several times a minute by
// people this session has no business tracking.
//
// It is worth having one day all the same -- "who is sitting" is
// answerable for everybody at no extra cost on the wire, and
// doc/history/sit.md wants it before anything here throws it away --
// which is why the handler hands over the sender's id rather than
// filtering on it.  Whose animations are kept is decided here, in the
// storage: keeping the crowd means a map keyed by avatar and a bound on
// it, exactly the shape Appearances already has, and not a line of the
// handler changes.
type animations struct {
	// self is whose animations are kept.  It is written once, in
	// trackPosture, before the dispatcher is running and before the
	// account can be handed to anything else, so it is read without the
	// lock that guards what arrives.
	self msg.UUID

	mu      sync.Mutex
	running []msg.UUID
}

// note keeps one avatar's animation list, replacing what was known.
//
// Replacing rather than merging, because the message is the whole list
// every time: an animation that has stopped is simply absent from the
// next one, and that absence is the only way standing up from a ground
// sit is ever heard about.
func (s *animations) note(who msg.UUID, list []msg.AvatarAnimation_AnimationList) {
	if who != s.self {
		return
	}
	// The ids alone, in the order they arrived, which is the order the
	// viewer plays them in.  The sequence numbers and the objects that
	// triggered each one are dropped: nothing here needs either, and a
	// viewer on the other end of Options.Relay is handed the message
	// itself rather than this.
	ids := make([]msg.UUID, 0, len(list))
	for _, an := range list {
		ids = append(ids, an.AnimID)
	}

	s.mu.Lock()
	s.running = ids
	s.mu.Unlock()
}

// all is what is playing, copied so that a caller cannot edit the set
// the next message will be compared against.
func (s *animations) all() []msg.UUID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]msg.UUID(nil), s.running...)
}

// sitting reports whether any of what is playing means seated.
func (s *animations) sitting() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.running {
		if SittingAnimation(id) {
			return true
		}
	}
	return false
}

// trackPosture keeps this avatar's animation list current.
//
// Inline, like the other handlers that only write down what arrived: it
// is a copy of a dozen ids, and running it in order means anything
// dispatched after it sees the posture the simulator has just described.
func (a *Agent) trackPosture() {
	a.anims.self = a.Account.AgentID

	a.Disp.MustHandle("AvatarAnimation", func(p *msg.Packet) {
		if m, ok := p.Message.(*msg.AvatarAnimation); ok {
			a.anims.note(m.Sender.ID, m.AnimationList)
		}
	}, msg.Inline())
}

// Animations is what the simulator says is playing on this avatar, in
// the order it sent them.
//
// Empty means nothing has been heard yet as much as it means nothing is
// playing, and the two are not worth separating: the list is resent in
// full about every three seconds, so the ambiguity lasts a moment after
// the handshake and never again.
func (a *Agent) Animations() []msg.UUID { return a.anims.all() }

// A Posture is what the avatar is doing: standing, sitting on the
// ground, or sitting on something.
type Posture int

const (
	// Standing is also what is reported when nothing has been heard yet,
	// which is the honest answer: neither signal has said otherwise.
	Standing Posture = iota
	SittingOnGround
	SittingOnObject
)

// Seated says whether this is any kind of sit.
func (p Posture) Seated() bool { return p != Standing }

func (p Posture) String() string {
	switch p {
	case Standing:
		return "standing"
	case SittingOnGround:
		return "sitting on the ground"
	case SittingOnObject:
		return "sitting on an object"
	}
	return fmt.Sprintf("posture(%d)", int(p))
}

// A Seat is what an avatar is sitting on.
//
// Local is the seat's local id, which is what the simulator puts in the
// ObjectUpdate that seats the avatar and is the region's own numbering
// -- it means nothing outside the region, and means something else
// inside the next one.  ID is the object's full id, which is the name
// that travels, and is zero when the object cache has never been told
// what that local id belongs to.  A zero ID is ordinary rather than
// alarming: the reparenting can arrive before the object it names has
// been described, and a seat further off than the draw distance is never
// described at all.
//
// A ground sit has no seat, so the zero Seat is what a ground sit
// reports.  Whether that means the ground or means standing is not this
// type's to say; ask Posture, or take the second return from Agent.Seat.
type Seat struct {
	Local uint32
	ID    msg.UUID
}

// Posture answers "am I seated, and how", and names the seat when there
// is one.  The Seat is zero for anything but SittingOnObject.
//
// # Which signal wins
//
// The parent does, wherever it is set.  It is what the simulator itself
// believes -- it decides where the avatar is and what it moves with --
// it arrives whether or not any animation ever does, and it is not
// something a script can dress up.  The animation can be: furniture that
// plays its own pose stops the standard sit, so an avatar plainly seated
// on a chair may be running nothing this package would recognise, and
// deciding on the animation would call it standing.
//
// The animation decides the ground, because for the ground it is all
// there is.  A ground sit produces no reply, no reparenting and nothing
// else on the wire: the animation list is the whole of the evidence.
//
// So the two can disagree, and both directions of disagreement are
// answered without lying about either.  A sit that has been reparented
// but whose animation has not yet arrived -- nothing promises an order,
// and the reparenting was inside a seventh of a second of the request
// when it was measured -- reads as SittingOnObject with no sitting
// animation running, which is what it is.  A ground sit in the same
// window reads as Standing, which is not a lie either: nothing has
// arrived to say otherwise, and the
// caller that wants to know when it has should wait for the animation
// rather than ask twice.  What is never done is guessing: an avatar with
// a parent is never called a ground sitter on the strength of a ground
// animation, and an avatar with a sitting animation and no parent is
// never said to be sitting on something this package cannot name.
func (a *Agent) Posture() (Posture, Seat) {
	store := a.Objects()

	// This avatar's own entry, which the region describes like any other
	// object and which every sit and stand updates.  Not being in the
	// cache at all is the same answer as not being parented: it happens
	// in the seconds before the region has described this avatar to
	// itself, and there is nothing seated about it.
	if own, ok := store.Get(a.Account.AgentID); ok && own.Parent != 0 {
		seat := Seat{Local: own.Parent}
		if o, ok := store.byLocal(own.Parent); ok {
			seat.ID = o.ID
		}
		return SittingOnObject, seat
	}

	if a.anims.sitting() {
		return SittingOnGround, Seat{}
	}
	return Standing, Seat{}
}

// Seat is what this avatar is sitting on, and whether it is sitting at
// all.
//
// The pair is what makes a ground sit distinguishable from standing:
// sitting on the ground is seated on nothing, so it answers a zero Seat
// with true, where standing answers the same zero Seat with false.  A
// caller that reads only the Seat cannot tell the two apart, which is
// why the bool is not a courtesy.
func (a *Agent) Seat() (Seat, bool) {
	p, seat := a.Posture()
	return seat, p.Seated()
}
