package sl

// What this avatar is playing: kept from AvatarAnimation, the whole list
// replaced each time, a source paired with its animation by position.
// Why: doc/animations.md#what-is-playing

import (
	"testing"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
)

// Invented ids, made with tools/new-id.
var (
	idLamp     = msg.MustParseUUID("417f7e57-7e57-c0de-845e-cb70809e0a80")
	idBanner   = msg.MustParseUUID("71a87e57-7e57-c0de-af6e-fc9be8902698")
	idStranger = msg.MustParseUUID("85777e57-7e57-c0de-2d7c-a31caba658f5")
)

// playingMsg is an AvatarAnimation of one avatar: each animation is
// given as id, sequence and, when it has one, a source object.
func playingMsg(avatar msg.UUID, list ...PlayingAnimation) *msg.AvatarAnimation {
	m := &msg.AvatarAnimation{}
	m.Sender.ID = avatar
	for _, p := range list {
		m.AnimationList = append(m.AnimationList,
			msg.AvatarAnimation_AnimationList{AnimID: p.ID, AnimSequenceID: p.Sequence})
	}
	// A source block for each animation up to the last one that has a
	// source, as the viewer reads them, by position.
	last := -1
	for i, p := range list {
		if !p.Source.IsZero() {
			last = i
		}
	}
	for _, p := range list[:last+1] {
		m.AnimationSourceList = append(m.AnimationSourceList,
			msg.AvatarAnimation_AnimationSourceList{ObjectID: p.Source})
	}
	return m
}

func TestNothingIsHeardUntilTheSimulatorSays(t *testing.T) {
	w, _ := newFakeSession(t)
	list, at := w.Animations()
	if len(list) != 0 || !at.IsZero() {
		t.Errorf("before any message: %v at %v, want nothing and no time", list, at)
	}
}

// TestAnAnimationIsAddedAndThenRemoved: the list is the whole of it each
// time, so the animation that is missing from the next one has stopped.
func TestAnAnimationIsAddedAndThenRemoved(t *testing.T) {
	w, f := newFakeSession(t)
	stand := PlayingAnimation{ID: agent.AnimStand, Sequence: 3}
	lamp := PlayingAnimation{ID: agent.AnimSitGround, Sequence: 7, Source: idLamp}

	f.Relay(t, playingMsg(testAgentID, stand))
	got, at := w.Animations()
	if len(got) != 1 || got[0] != stand || at.IsZero() {
		t.Fatalf("after the stand: %v at %v", got, at)
	}

	f.Relay(t, playingMsg(testAgentID, stand, lamp))
	got, _ = w.Animations()
	if len(got) != 2 || got[0] != stand || got[1] != lamp {
		t.Fatalf("after the lamp starts: %v, want the stand and %v", got, lamp)
	}

	f.Relay(t, playingMsg(testAgentID, stand))
	got, _ = w.Animations()
	if len(got) != 1 || got[0] != stand {
		t.Errorf("after the lamp stops: %v, want only the stand", got)
	}

	f.Relay(t, playingMsg(testAgentID))
	got, at = w.Animations()
	if len(got) != 0 || at.IsZero() {
		t.Errorf("an empty list that was heard: %v at %v, want nothing, and a time", got, at)
	}
}

// TestSourcesAreKeptByPosition: the source list may be shorter than the
// animation list, and one animation's source is not another's.
func TestSourcesAreKeptByPosition(t *testing.T) {
	w, f := newFakeSession(t)
	a := PlayingAnimation{ID: agent.AnimStand, Sequence: 1, Source: idLamp}
	b := PlayingAnimation{ID: agent.AnimSitGround, Sequence: 2}
	c := PlayingAnimation{ID: agent.AnimSitGroundConstrained, Sequence: 3, Source: idBanner}

	f.Relay(t, playingMsg(testAgentID, a, b, c))
	got, _ := w.Animations()
	if len(got) != 3 || got[0].Source != idLamp || !got[1].Source.IsZero() || got[2].Source != idBanner {
		t.Errorf("sources by position: %v", got)
	}

	// A list with fewer sources than animations: the later ones have none.
	short := playingMsg(testAgentID, a, b, c)
	short.AnimationSourceList = short.AnimationSourceList[:1]
	f.Relay(t, short)
	got, _ = w.Animations()
	if len(got) != 3 || got[0].Source != idLamp || !got[1].Source.IsZero() || !got[2].Source.IsZero() {
		t.Errorf("a short source list: %v", got)
	}
}

// TestAnotherAvatarsAnimationsAreNotKept: the message is sent for every
// avatar in range, and a session holds this one's only.
func TestAnotherAvatarsAnimationsAreNotKept(t *testing.T) {
	w, f := newFakeSession(t)
	mine := PlayingAnimation{ID: agent.AnimStand, Sequence: 1}
	f.Relay(t, playingMsg(testAgentID, mine))
	f.Relay(t, playingMsg(idStranger, PlayingAnimation{ID: agent.AnimSitGround, Sequence: 9, Source: idLamp}))
	got, _ := w.Animations()
	if len(got) != 1 || got[0] != mine {
		t.Errorf("a stranger's list changed ours: %v", got)
	}
}

// TestListeningForAnimationsIsTheBorrowedSubscription: it is the one
// the sit and the stand share, and it is given back.
func TestListeningForAnimationsIsTheBorrowedSubscription(t *testing.T) {
	w, f := newFakeSession(t)
	stop, err := w.ListenForAnimations()
	if err != nil {
		t.Fatalf("ListenForAnimations: %v", err)
	}
	if !f.Watching("AvatarAnimation") {
		t.Error("nothing subscribed to AvatarAnimation")
	}
	stop()
	if f.Watching("AvatarAnimation") {
		t.Error("the subscription was kept after stop")
	}
}
