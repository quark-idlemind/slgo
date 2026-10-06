package sl

// The sounds objects play: SoundTrigger, AttachedSound,
// AttachedSoundGainChange and PreloadSound, kept as they are heard, and
// the loops they leave in force.
// Why: doc/sounds.md#what-is-heard

import (
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

// Invented ids, made with tools/new-id; the two sounds are built-ins.
var (
	idSpeaker  = msg.MustParseUUID("31527e57-7e57-c0de-bf0b-8d39e9d2e111")
	idSpeaker2 = msg.MustParseUUID("4eaf7e57-7e57-c0de-63d2-21a6e01348c9")
	idOwnerOf  = msg.MustParseUUID("52217e57-7e57-c0de-527f-8aa6debd894e")
	idTowerOf  = msg.MustParseUUID("58447e57-7e57-c0de-a13b-99b5b34e5663")
	idOwn2Of   = msg.MustParseUUID("6b487e57-7e57-c0de-103f-aaa2421951e2")

	// UISndClick and UISndAlert, which tools/known-uuids lists.
	idClick = msg.MustParseUUID("4c8c3c77-de8d-bde2-b9b8-32635e0fd4a6")
	idAlert = msg.MustParseUUID("ed124764-705d-d497-167a-182cd9fa2e6c")
)

func attachedMsg(sound, object, owner msg.UUID, gain float32, flags uint8) *msg.AttachedSound {
	m := &msg.AttachedSound{}
	m.DataBlock = msg.AttachedSound_DataBlock{SoundID: sound, ObjectID: object, OwnerID: owner, Gain: gain, Flags: flags}
	return m
}

func TestNothingIsHeardOfSoundsUntilTheRegionSays(t *testing.T) {
	w, _ := newFakeSession(t)
	if got := w.Sounds(); len(got) != 0 {
		t.Errorf("before any message: %v", got)
	}
	if st := w.SoundsNow(); len(st.Loops) != 0 || st.Seq != 0 {
		t.Errorf("before any message: %+v", st)
	}
}

// TestASoundTriggerIsKeptWithWhereAndHowLoud: a one-shot at a place has
// the object, its owner and parent, the place, and a gain the viewer
// would clamp.
func TestASoundTriggerIsKeptWithWhereAndHowLoud(t *testing.T) {
	w, f := newFakeSession(t)
	m := &msg.SoundTrigger{}
	m.SoundData = msg.SoundTrigger_SoundData{
		SoundID: idClick, OwnerID: idOwnerOf, ObjectID: idSpeaker, ParentID: idTowerOf,
		Handle: 0xAA80AA80_00000000, Position: msg.Vector3{X: 1, Y: 2, Z: 3}, Gain: 0.5,
	}
	f.Relay(t, m)
	got := w.Sounds()
	if len(got) != 1 {
		t.Fatalf("heard %d, want 1: %v", len(got), got)
	}
	h := got[0]
	if h.Kind != SoundTriggered || !h.Played() || h.ID != idClick || h.Object != idSpeaker ||
		h.Owner != idOwnerOf || h.Parent != idTowerOf || h.Gain != 0.5 ||
		h.Position != (msg.Vector3{X: 1, Y: 2, Z: 3}) || h.Handle != 0xAA80AA80_00000000 ||
		h.Seq != 1 || h.At.IsZero() || h.Looping {
		t.Errorf("trigger: %+v", h)
	}
	if st := w.SoundsNow(); len(st.Loops) != 0 || st.Seq != 1 {
		t.Errorf("a trigger is no loop: %+v", st)
	}

	// The viewer clamps the gain to 0..1 (llclampf).
	m.SoundData.Gain = 7
	f.Relay(t, m)
	m.SoundData.Gain = -2
	f.Relay(t, m)
	got = w.Sounds()
	if got[1].Gain != 1 || got[2].Gain != 0 {
		t.Errorf("clamped gains: %v and %v, want 1 and 0", got[1].Gain, got[2].Gain)
	}
}

// TestAnAttachedSoundIsKeptWithItsFlags: a one-shot on a prim plays and
// leaves no loop.
func TestAnAttachedSoundIsKeptWithItsFlags(t *testing.T) {
	w, f := newFakeSession(t)
	f.Relay(t, attachedMsg(idAlert, idSpeaker, idOwnerOf, 0.25, SoundFlagSyncMaster))
	got := w.Sounds()
	if len(got) != 1 {
		t.Fatalf("heard %d: %v", len(got), got)
	}
	h := got[0]
	if h.Kind != SoundAttached || !h.Played() || h.ID != idAlert || h.Object != idSpeaker ||
		h.Owner != idOwnerOf || h.Gain != 0.25 || h.Flags != SoundFlagSyncMaster || h.Looping {
		t.Errorf("attached: %+v", h)
	}
	if st := w.SoundsNow(); len(st.Loops) != 0 {
		t.Errorf("a one-shot left a loop: %+v", st)
	}
}

// TestALoopStartedAndStopped: the loop is in force from its message to a
// null sound, and the stop names the loop it ended.
func TestALoopStartedAndStopped(t *testing.T) {
	w, f := newFakeSession(t)
	f.Relay(t, attachedMsg(idAlert, idSpeaker, idOwnerOf, 1, SoundFlagLoop))
	st := w.SoundsNow()
	if len(st.Loops) != 1 || st.Loops[0].ID != idAlert || st.Loops[0].Object != idSpeaker ||
		st.Loops[0].Owner != idOwnerOf || st.Loops[0].Gain != 1 || st.Loops[0].Since.IsZero() {
		t.Fatalf("after the loop starts: %+v", st)
	}
	if h := w.Sounds()[0]; !h.Looping || !h.Replaced.IsZero() {
		t.Errorf("the start: %+v", h)
	}

	f.Relay(t, attachedMsg(msg.UUID{}, idSpeaker, idOwnerOf, 0, SoundFlagStop))
	if st := w.SoundsNow(); len(st.Loops) != 0 {
		t.Fatalf("after the stop: %+v", st)
	}
	got := w.Sounds()
	h := got[1]
	if h.Kind != SoundStopped || h.Played() || !h.ID.IsZero() || h.Replaced != idAlert || h.Looping ||
		h.Flags != SoundFlagStop {
		t.Errorf("the stop: %+v", h)
	}

	// A stop of a prim with no loop is heard, and ends nothing.
	f.Relay(t, attachedMsg(msg.UUID{}, idSpeaker, idOwnerOf, 0, SoundFlagStop))
	if h := w.Sounds()[2]; h.Kind != SoundStopped || !h.Replaced.IsZero() {
		t.Errorf("a stop with nothing to stop: %+v", h)
	}
}

// TestALoopAskedForAgainIsNotAStart: setAttachedSound ignores a loop that
// is already playing, so nothing was heard to start; a different sound
// replaces it, and a one-shot ends it.
func TestALoopAskedForAgainIsNotAStart(t *testing.T) {
	w, f := newFakeSession(t)
	f.Relay(t, attachedMsg(idAlert, idSpeaker, idOwnerOf, 1, SoundFlagLoop))
	f.Relay(t, attachedMsg(idAlert, idSpeaker, idOwnerOf, 1, SoundFlagLoop))
	if n := len(w.Sounds()); n != 1 {
		t.Errorf("the same loop twice is %d sounds, want 1", n)
	}

	f.Relay(t, attachedMsg(idClick, idSpeaker, idOwnerOf, 1, SoundFlagLoop))
	got := w.Sounds()
	if n := len(got); n != 2 || got[1].Replaced != idAlert || !got[1].Looping {
		t.Fatalf("a different loop: %+v", got)
	}
	if st := w.SoundsNow(); len(st.Loops) != 1 || st.Loops[0].ID != idClick {
		t.Errorf("in force: %+v", st)
	}

	f.Relay(t, attachedMsg(idAlert, idSpeaker, idOwnerOf, 1, 0))
	got = w.Sounds()
	if h := got[2]; h.Replaced != idClick || h.Looping {
		t.Errorf("a one-shot over a loop: %+v", h)
	}
	if st := w.SoundsNow(); len(st.Loops) != 0 {
		t.Errorf("in force: %+v", st)
	}
}

// TestAGainChangeIsHeardAndDoesNotStopALoop: a loop at gain 0 is still
// a loop.
func TestAGainChangeIsHeardAndDoesNotStopALoop(t *testing.T) {
	w, f := newFakeSession(t)
	f.Relay(t, attachedMsg(idAlert, idSpeaker, idOwnerOf, 1, SoundFlagLoop))
	g := &msg.AttachedSoundGainChange{}
	g.DataBlock = msg.AttachedSoundGainChange_DataBlock{ObjectID: idSpeaker, Gain: 0}
	f.Relay(t, g)
	st := w.SoundsNow()
	if len(st.Loops) != 1 || st.Loops[0].Gain != 0 {
		t.Errorf("after gain 0: %+v", st)
	}
	h := w.Sounds()[1]
	if h.Kind != SoundGain || h.Object != idSpeaker || h.Gain != 0 || h.Played() || !h.ID.IsZero() {
		t.Errorf("the gain change: %+v", h)
	}

	// For a prim that plays nothing it is heard, and nothing changes.
	g.DataBlock.ObjectID = idSpeaker2
	g.DataBlock.Gain = 0.5
	f.Relay(t, g)
	if n := len(w.Sounds()); n != 3 || len(w.SoundsNow().Loops) != 1 {
		t.Errorf("a gain change for a silent prim: %d heard", n)
	}
}

// TestAPreloadIsHeardOnePerSoundAndPlaysNothing.
func TestAPreloadIsHeardOnePerSoundAndPlaysNothing(t *testing.T) {
	w, f := newFakeSession(t)
	m := &msg.PreloadSound{DataBlock: []msg.PreloadSound_DataBlock{
		{ObjectID: idSpeaker, OwnerID: idOwnerOf, SoundID: idClick},
		{ObjectID: idSpeaker, OwnerID: idOwnerOf, SoundID: idAlert},
	}}
	f.Relay(t, m)
	got := w.Sounds()
	if len(got) != 2 || got[0].Kind != SoundPreloaded || got[0].ID != idClick ||
		got[1].ID != idAlert || got[1].Object != idSpeaker || got[1].Owner != idOwnerOf {
		t.Fatalf("preload: %+v", got)
	}
	if got[0].Played() || got[1].Played() {
		t.Error("a preload is no play")
	}
	if got[0].Seq != 1 || got[1].Seq != 2 {
		t.Errorf("numbers: %d, %d", got[0].Seq, got[1].Seq)
	}
}

// TestAnotherObjectsSoundIsAnotherLoop: each prim has its own, and a stop
// of one leaves the other.
func TestAnotherObjectsSoundIsAnotherLoop(t *testing.T) {
	w, f := newFakeSession(t)
	f.Relay(t, attachedMsg(idAlert, idSpeaker, idOwnerOf, 1, SoundFlagLoop))
	f.Relay(t, attachedMsg(idAlert, idSpeaker2, idOwn2Of, 1, SoundFlagLoop))
	if n := len(w.SoundsNow().Loops); n != 2 {
		t.Fatalf("%d loops, want 2", n)
	}
	f.Relay(t, attachedMsg(msg.UUID{}, idSpeaker, idOwnerOf, 0, SoundFlagStop))
	st := w.SoundsNow()
	if len(st.Loops) != 1 || st.Loops[0].Object != idSpeaker2 || st.Loops[0].Owner != idOwn2Of {
		t.Errorf("after the first stops: %+v", st)
	}
}

func TestSoundsSinceIsWhatCameAfter(t *testing.T) {
	w, f := newFakeSession(t)
	f.Relay(t, attachedMsg(idClick, idSpeaker, idOwnerOf, 1, 0))
	f.Relay(t, attachedMsg(idAlert, idSpeaker, idOwnerOf, 1, 0))
	st := w.SoundsNow()
	f.Relay(t, attachedMsg(idClick, idSpeaker2, idOwnerOf, 1, 0))
	got := w.SoundsSince(st.Seq)
	if len(got) != 1 || got[0].Object != idSpeaker2 || got[0].Seq != 3 {
		t.Errorf("since %d: %+v", st.Seq, got)
	}
	if got := w.SoundsSince(3); len(got) != 0 {
		t.Errorf("since the last: %+v", got)
	}
}

func TestTheLogIsCutToItsLast(t *testing.T) {
	w, f := newFakeSession(t)
	for i := 0; i < MaxHeardSounds+10; i++ {
		f.Relay(t, attachedMsg(idClick, idSpeaker, idOwnerOf, 1, 0))
	}
	got := w.Sounds()
	if len(got) != MaxHeardSounds || got[0].Seq != 11 || got[len(got)-1].Seq != MaxHeardSounds+10 {
		t.Errorf("kept %d, from %d to %d", len(got), got[0].Seq, got[len(got)-1].Seq)
	}
}

// TestAKilledObjectTakesItsLoopWithIt: the viewer destroys the sound with
// the object.  Inferred from the viewer, not measured.
func TestAKilledObjectTakesItsLoopWithIt(t *testing.T) {
	w, f := newFakeSession(t)
	f.Relay(t, anUpdate(msg.ObjectUpdate_ObjectData{FullID: idSpeaker, ID: 41}))
	f.Relay(t, anUpdate(msg.ObjectUpdate_ObjectData{FullID: idSpeaker2, ID: 42}))
	f.Relay(t, attachedMsg(idAlert, idSpeaker, idOwnerOf, 1, SoundFlagLoop))
	f.Relay(t, attachedMsg(idAlert, idSpeaker2, idOwnerOf, 1, SoundFlagLoop))
	f.Relay(t, &msg.KillObject{ObjectData: []msg.KillObject_ObjectData{{ID: 41}}})
	st := w.SoundsNow()
	if len(st.Loops) != 1 || st.Loops[0].Object != idSpeaker2 {
		t.Fatalf("after the kill: %+v", st)
	}
	h := w.Sounds()[2]
	if h.Kind != SoundEnded || h.Object != idSpeaker || h.Replaced != idAlert || h.Played() {
		t.Errorf("the end: %+v", h)
	}
}

// TestLeavingTheRegionEndsItsLoops.
func TestLeavingTheRegionEndsItsLoops(t *testing.T) {
	w, f := newFakeSession(t)
	f.Relay(t, attachedMsg(idAlert, idSpeaker, idOwnerOf, 1, SoundFlagLoop))
	f.RelayRegion(t, goguenName, goguen)
	if st := w.SoundsNow(); len(st.Loops) != 0 {
		t.Errorf("loops kept from the region left: %+v", st)
	}
	if got := w.Sounds(); len(got) != 2 || got[1].Kind != SoundEnded || got[1].Replaced != idAlert {
		t.Errorf("log: %+v", got)
	}
}

// TestAFullObjectUpdateTellsOfALoopAlreadyPlaying: how a loop that began
// before this session listened is known, and how one ends without a
// message.  A one-shot in an update is not a play.
func TestAFullObjectUpdateTellsOfALoopAlreadyPlaying(t *testing.T) {
	w, f := newFakeSession(t)
	d := msg.ObjectUpdate_ObjectData{FullID: idSpeaker, ID: 41, Sound: idAlert, OwnerID: idOwnerOf, Gain: 0.75, Flags: SoundFlagLoop}
	f.Relay(t, anUpdate(d))
	st := w.SoundsNow()
	if len(st.Loops) != 1 || st.Loops[0].ID != idAlert || st.Loops[0].Gain != 0.75 || st.Loops[0].Owner != idOwnerOf {
		t.Fatalf("loop from an update: %+v", st)
	}
	n := len(w.Sounds())

	// The same again says nothing new.
	f.Relay(t, anUpdate(d))
	if len(w.Sounds()) != n {
		t.Error("the same loop in a second update was heard again")
	}

	// A sound the prim last played once is no loop and no play.
	d.Sound, d.Flags = idClick, 0
	f.Relay(t, anUpdate(d))
	if st := w.SoundsNow(); len(st.Loops) != 0 {
		t.Errorf("a one-shot in an update left a loop: %+v", st)
	}
	for _, h := range w.Sounds() {
		if h.Played() && h.ID == idClick {
			t.Error("a one-shot in an update was heard to play")
		}
	}

	// An update with no sound at all, for an object that has none, is silent.
	f.Relay(t, anUpdate(msg.ObjectUpdate_ObjectData{FullID: idSpeaker2, ID: 42}))
	if got := w.Sounds(); len(got) != n+1 {
		t.Errorf("an update with no sound was heard: %+v", got[n+1:])
	}
}

// TestListeningForSoundsIsTheBorrowedSubscription: the four messages,
// taken together, shared by overlapping callers and given back by the
// last.
func TestListeningForSoundsIsTheBorrowedSubscription(t *testing.T) {
	w, f := newFakeSession(t)
	a, err := w.ListenForSounds()
	if err != nil {
		t.Fatalf("ListenForSounds: %v", err)
	}
	b, err := w.ListenForSounds()
	if err != nil {
		t.Fatalf("ListenForSounds: %v", err)
	}
	for _, n := range soundRelays {
		if !f.Watching(n) {
			t.Errorf("not subscribed to %s", n)
		}
	}
	a()
	if !f.Watching("AttachedSound") {
		t.Error("the first caller's stop took the subscription from the second")
	}
	b()
	for _, n := range soundRelays {
		if f.Watching(n) {
			t.Errorf("%s kept after the last stop", n)
		}
	}
}

func TestSoundsAreNotInTheStandingSubscriptions(t *testing.T) {
	for _, n := range Subscriptions {
		for _, s := range soundRelays {
			if n == s {
				t.Errorf("%s is relayed for ever; ask for it with ListenForSounds", n)
			}
		}
	}
}
