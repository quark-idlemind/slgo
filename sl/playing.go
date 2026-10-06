package sl

// What this avatar is playing, as the simulator last said.
//
// The simulator tells a viewer what an avatar is playing in
// AvatarAnimation: every animation, with a sequence number, and beside
// that list the object that started each one.  The viewer replaces what
// it holds with each message, and keeps the sources for its own avatar
// only (Firestorm 885631b93a, newview/llviewermessage.cpp):
//
//   - process_avatar_animation, :5235, reads the avatar from the
//     sender's id;
//   - :5265 clears the avatar's signaled animations, so the message is
//     the whole list and an animation that stopped is absent from the
//     next one;
//   - :5267 is the branch for the viewer's own avatar, which reads each
//     AnimID and AnimSequenceID (:5273-5274), keeps them (:5286) and,
//     only while the index is below the number of AnimationSourceList
//     blocks (:5299), the ObjectID of the same index (:5301);
//   - the branch for any other avatar (:5347) reads no sources
//     (:5349-5354).
//
// So a source is paired with an animation by position, and the source
// list may be shorter than the animation list.  That the simulator
// sends the sources in the same order as the animations, and names no
// object, or the null id, for an animation the avatar plays by itself,
// is what the viewer's code expects and is not measured here.
// Why: doc/animations.md#what-is-playing

import (
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// A PlayingAnimation is one animation this avatar is playing.
type PlayingAnimation struct {
	// ID is the animation's asset id: a built-in's constant, or the
	// asset of an animation in an inventory or an object.
	ID msg.UUID

	// Sequence is the simulator's number for this play of it, which
	// changes when the animation is started again.
	Sequence int32

	// Source is the object that started it, or the zero id when the
	// simulator named none for it.
	Source msg.UUID
}

// playingOf is a message's list, each animation paired with the source
// at its own position.
func playingOf(m *msg.AvatarAnimation) []PlayingAnimation {
	out := make([]PlayingAnimation, 0, len(m.AnimationList))
	for i, an := range m.AnimationList {
		p := PlayingAnimation{ID: an.AnimID, Sequence: an.AnimSequenceID}
		if i < len(m.AnimationSourceList) {
			p.Source = m.AnimationSourceList[i].ObjectID
		}
		out = append(out, p)
	}
	return out
}

// Animations is what the simulator last said this avatar is playing,
// and when it said it.  The time is zero, and the list nil, until a
// message has been heard, which is not the same as an avatar playing
// nothing: an empty list that was heard is a list the simulator sent.
//
// The message arrives for every avatar in range and only a session that
// asked for it is sent it, so a session that has not (ListenForAnimations,
// or a sit or a stand under way) has nothing here, or a list that is old.
// The list is as old as the time says.
func (w *Session) Animations() ([]PlayingAnimation, time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]PlayingAnimation, len(w.playing))
	copy(out, w.playing)
	return out, w.playingAt
}

// ListenForAnimations asks for AvatarAnimation to be relayed to this
// session until the returned func is called, and Animations is kept
// from it meanwhile.  Calls overlap safely and share the one
// subscription, as the sit and the stand do (borrowAnimations).  A
// session that is not behind a daemon already hears everything.
func (w *Session) ListenForAnimations() (stop func(), err error) {
	return w.borrowAnimations()
}
