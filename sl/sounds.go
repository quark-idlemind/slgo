package sl

// The sounds objects play, as the region tells this avatar of them.
//
// Four messages carry them, and the viewer reads each in
// newview/llviewermessage.cpp (Firestorm 885631b93a):
//
//   - SoundTrigger, process_sound_trigger :4875: a sound played once at a
//     place (llTriggerSound, llPlaySound's one-shot).  Sound, owner and
//     object ids are read at :4893-4895, the parent, region handle,
//     position and gain at :4927-4930 and the gain is clamped to 0..1 at
//     :4931.  It has no flags, so it never loops.
//   - AttachedSound, process_attached_sound :5053: a sound that belongs
//     to a prim (llPlaySound, llLoopSound).  Gain and Flags are read at
//     :5084-5085, gain clamped at :5086, and the prim is told at :5091 by
//     set_attached_sound (:4493) to LLViewerObject::setAttachedSound
//     (llviewerobject.cpp :6607).
//   - AttachedSoundGainChange, process_attached_sound_gain_change :5110:
//     a new gain for the sound a prim has (llAdjustSoundVolume), applied
//     by adjustAudioGain (llviewerobject.cpp :6723).  Nothing starts or
//     stops.
//   - PreloadSound, process_preload_sound :4992: the object is told to
//     fetch a sound before it plays it (llPreloadSound).  Nothing plays.
//
// What ends a loop, from setAttachedSound: a null sound id, when the
// current sound is a loop (:6614-6629), or when it is not and the STOP
// flag is set the sound is stopped (:6630-6634); and a sound that is not
// a loop (:6685 sets the loop from the flags, :6689-6692 stops what was
// playing unless QUEUE is set), so a one-shot started on a prim that
// loops ends the loop.  A loop asked for again with the same id is
// ignored (:6656-6662).  A gain change does not end one, and a loop at
// gain 0 is still a loop.  An object that is killed takes its sound with
// it (llviewerobject.cpp :514-521).  The full ObjectUpdate carries the
// prim's sound too (Sound, Gain and Flags; llviewerobject.cpp :1358-1376),
// which is how the viewer learns of a loop already playing when it sees
// the object, and is read here for loops only.
//
// The flags are LL_SOUND_FLAG_* in llcommon/lldefs.h :128-135.
//
// What the viewer does not do and this does: keep what it hears.  The
// viewer plays it and forgets it.  Every one of them is kept here, with
// the time it was heard, in a log that is cut to its last
// MaxHeardSounds, and the loops in force are kept apart from it.
// Why: doc/sounds.md#what-is-heard

import (
	"math"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// The sound flags of AttachedSound, as the viewer names them.
const (
	SoundFlagLoop        uint8 = 1 << 0 // LL_SOUND_FLAG_LOOP
	SoundFlagSyncMaster  uint8 = 1 << 1 // LL_SOUND_FLAG_SYNC_MASTER
	SoundFlagSyncSlave   uint8 = 1 << 2 // LL_SOUND_FLAG_SYNC_SLAVE
	SoundFlagSyncPending uint8 = 1 << 3 // LL_SOUND_FLAG_SYNC_PENDING
	SoundFlagQueue       uint8 = 1 << 4 // LL_SOUND_FLAG_QUEUE
	SoundFlagStop        uint8 = 1 << 5 // LL_SOUND_FLAG_STOP
)

// MaxHeardSounds is how many heard sounds the session keeps; the oldest
// go first.  A caller that reads often enough (SoundsSince) loses none.
const MaxHeardSounds = 1024

// SoundKind is which of the messages a HeardSound came from, or what was
// worked out from them.
type SoundKind int

const (
	// SoundTriggered is a SoundTrigger: played once, at a place.
	SoundTriggered SoundKind = iota
	// SoundAttached is an AttachedSound with a sound: it plays on the
	// prim, once or, with Looping, until something ends it.
	SoundAttached
	// SoundStopped is an AttachedSound with no sound: the prim's sound
	// is stopped.  ID is the null id; Replaced is the loop it ended, if
	// one was running.
	SoundStopped
	// SoundGain is an AttachedSoundGainChange.
	SoundGain
	// SoundPreloaded is a PreloadSound, one for each sound it names.
	SoundPreloaded
	// SoundEnded is not a message: the object that held a loop was
	// killed, or the avatar left the region it was in, and the viewer
	// ends the sound with the object.  Inferred, not measured.
	SoundEnded
)

func (k SoundKind) String() string {
	switch k {
	case SoundTriggered:
		return "triggered"
	case SoundAttached:
		return "attached"
	case SoundStopped:
		return "stopped"
	case SoundGain:
		return "gain change"
	case SoundPreloaded:
		return "preloaded"
	case SoundEnded:
		return "ended"
	}
	return "unknown"
}

// A HeardSound is one thing the region said about a sound.
type HeardSound struct {
	// Seq numbers them in the order heard, from 1.
	Seq uint64
	// At is when the session heard it.
	At   time.Time
	Kind SoundKind

	// ID is the sound's asset id: a built-in's, or an uploaded sound.
	// Null for SoundStopped and SoundGain, which name none.
	ID msg.UUID
	// Object is the prim that plays it (a trigger's source, the prim an
	// attached sound belongs to) and Owner its owner.  A worn HUD's
	// owner is the wearer.  Owner is null where the message gives none
	// (SoundGain, SoundEnded).
	Object, Owner msg.UUID
	// Parent is a trigger's parent object, as the message gives it.
	Parent msg.UUID

	// Gain is 0 to 1, clamped as the viewer clamps it.  For SoundGain it
	// is the new gain.
	Gain float32
	// Flags are an AttachedSound's, SoundFlag*.  Zero for the rest.
	Flags uint8
	// Position and Handle say where a SoundTrigger was played, in the
	// region the handle names.
	Position msg.Vector3
	Handle   uint64

	// Looping says that after this message a loop of ID is running on
	// Object.
	Looping bool
	// Replaced is the sound whose loop on Object this message ended, or
	// null when it ended none.
	Replaced msg.UUID
}

// Played says the sound was heard to play: a trigger, or an attached
// sound with an id.  A stop, a gain change, a preload and an end are not.
func (h HeardSound) Played() bool {
	return h.Kind == SoundTriggered || h.Kind == SoundAttached
}

// A LoopingSound is a loop in force on a prim.
type LoopingSound struct {
	ID, Object, Owner msg.UUID
	Gain              float32
	// Since is when the session heard it start.
	Since time.Time
}

// SoundState is what the session holds of sounds at one moment: the loops
// in force and the number of the last sound heard, so that SoundsSince(Seq)
// is what comes after.
type SoundState struct {
	Loops []LoopingSound
	Seq   uint64
}

// Sounds is every sound the session still holds, oldest first.
func (w *Session) Sounds() []HeardSound { return w.SoundsSince(0) }

// SoundsSince is what was heard after the sound numbered seq.  The log
// is cut to MaxHeardSounds, so a caller that waits long enough is missing
// the oldest.
func (w *Session) SoundsSince(seq uint64) []HeardSound {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []HeardSound
	for _, h := range w.heard {
		if h.Seq > seq {
			out = append(out, h)
		}
	}
	return out
}

// SoundsNow is the loops in force and the number of the last sound heard,
// taken together.
func (w *Session) SoundsNow() SoundState {
	w.mu.Lock()
	defer w.mu.Unlock()
	st := SoundState{Seq: w.heardSeq}
	for _, l := range w.loops {
		st.Loops = append(st.Loops, *l)
	}
	return st
}

// ListenForSounds asks for SoundTrigger, AttachedSound,
// AttachedSoundGainChange and PreloadSound to be relayed to this session
// until the returned func is called.  Calls overlap safely and share the
// one subscription.  A session that is not behind a daemon already hears
// everything.
//
// None is in Subscriptions: a session is sent them only when it asks, as
// with AvatarAnimation, because a region sends them for every sound near
// the avatar and a program that reads none would pay for them.  A loop
// that was already playing when the session began to listen is known from
// the full ObjectUpdate, which is always relayed, and a loop started
// while nothing listened is known only so far as that says.
// Why: doc/sounds.md#what-is-heard
func (w *Session) ListenForSounds() (stop func(), err error) {
	return w.borrow(&w.soundWatch, soundRelays,
		"sl: cannot listen for sounds")
}

// soundRelays are the messages ListenForSounds asks for.
var soundRelays = []string{"SoundTrigger", "AttachedSound", "AttachedSoundGainChange", "PreloadSound"}

// clampGain is the viewer's llclampf, which also leaves a NaN at 0.
func clampGain(g float32) float32 {
	if g != g {
		return 0
	}
	return float32(math.Max(0, math.Min(1, float64(g))))
}

// logSound adds to the log.  The caller holds w.mu.
func (w *Session) logSound(h HeardSound) {
	w.heardSeq++
	h.Seq = w.heardSeq
	h.At = time.Now()
	w.heard = append(w.heard, h)
	if n := len(w.heard) - MaxHeardSounds; n > 0 {
		w.heard = append(w.heard[:0:0], w.heard[n:]...)
	}
}

// endLoop ends the loop an object has, if it has one, and says which.
// The caller holds w.mu.
func (w *Session) endLoop(object msg.UUID) msg.UUID {
	l, ok := w.loops[object]
	if !ok {
		return msg.UUID{}
	}
	delete(w.loops, object)
	return l.ID
}

func (w *Session) soundTrigger(t *msg.SoundTrigger) {
	d := &t.SoundData
	w.mu.Lock()
	defer w.mu.Unlock()
	w.logSound(HeardSound{
		Kind: SoundTriggered, ID: d.SoundID, Object: d.ObjectID, Owner: d.OwnerID,
		Parent: d.ParentID, Gain: clampGain(d.Gain), Position: d.Position, Handle: d.Handle,
	})
}

func (w *Session) attachedSound(t *msg.AttachedSound) {
	d := &t.DataBlock
	w.mu.Lock()
	defer w.mu.Unlock()
	h := HeardSound{
		Kind: SoundAttached, ID: d.SoundID, Object: d.ObjectID, Owner: d.OwnerID,
		Gain: clampGain(d.Gain), Flags: d.Flags,
	}
	if d.SoundID.IsZero() {
		h.Kind = SoundStopped
		h.Replaced = w.endLoop(d.ObjectID)
		w.logSound(h)
		return
	}
	if l := w.loops[d.ObjectID]; d.Flags&SoundFlagLoop != 0 && l != nil && l.ID == d.SoundID {
		// setAttachedSound ignores a loop already playing, so nothing
		// was heard to start.
		return
	}
	h.Replaced = w.endLoop(d.ObjectID)
	if d.Flags&SoundFlagLoop != 0 {
		h.Looping = true
		w.startLoop(h)
	}
	w.logSound(h)
}

// startLoop records a loop in force.  The caller holds w.mu.
func (w *Session) startLoop(h HeardSound) {
	if w.loops == nil {
		w.loops = map[msg.UUID]*LoopingSound{}
	}
	w.loops[h.Object] = &LoopingSound{ID: h.ID, Object: h.Object, Owner: h.Owner, Gain: h.Gain, Since: time.Now()}
}

func (w *Session) attachedSoundGain(t *msg.AttachedSoundGainChange) {
	d := &t.DataBlock
	w.mu.Lock()
	defer w.mu.Unlock()
	g := clampGain(d.Gain)
	if l := w.loops[d.ObjectID]; l != nil {
		l.Gain = g
	}
	w.logSound(HeardSound{Kind: SoundGain, Object: d.ObjectID, Gain: g})
}

func (w *Session) preloadSound(t *msg.PreloadSound) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, d := range t.DataBlock {
		w.logSound(HeardSound{Kind: SoundPreloaded, ID: d.SoundID, Object: d.ObjectID, Owner: d.OwnerID})
	}
}

// updateSound reads the sound a full ObjectUpdate carries, for loops
// only: a looping sound that is not the one the prim already loops
// starts a loop, and a prim whose update carries none, or one that is not
// a loop, ends the loop it had.  A one-shot named in an update is not
// heard to play: the viewer does play it, but the field is the last sound
// the prim was given, not an event, and reading it as one would hear the
// same sound again at every update.  The caller holds w.mu.
func (w *Session) updateSound(o *msg.ObjectUpdate_ObjectData) {
	l := w.loops[o.FullID]
	if o.Sound.IsZero() || o.Flags&SoundFlagLoop == 0 {
		if l != nil {
			w.logSound(HeardSound{Kind: SoundStopped, Object: o.FullID, Replaced: w.endLoop(o.FullID)})
		}
		return
	}
	if l != nil && l.ID == o.Sound {
		l.Gain = clampGain(o.Gain)
		return
	}
	h := HeardSound{
		Kind: SoundAttached, ID: o.Sound, Object: o.FullID, Owner: o.OwnerID,
		Gain: clampGain(o.Gain), Flags: o.Flags, Looping: true,
		Replaced: w.endLoop(o.FullID),
	}
	w.startLoop(h)
	w.logSound(h)
}

// objectGone ends the loop of an object the region has killed.  The caller
// holds w.mu.
func (w *Session) objectGone(local uint32) {
	for id, l := range w.loops {
		if w.locals[id] == local {
			delete(w.loops, id)
			w.logSound(HeardSound{Kind: SoundEnded, Object: id, Owner: l.Owner, Replaced: l.ID})
		}
	}
}

// loopsGone ends every loop, for a region left behind.  The caller holds
// w.mu.
func (w *Session) loopsGone() {
	for id, l := range w.loops {
		delete(w.loops, id)
		w.logSound(HeardSound{Kind: SoundEnded, Object: id, Owner: l.Owner, Replaced: l.ID})
	}
}
