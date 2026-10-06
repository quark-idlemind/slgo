package slate

// expect sound: the sounds objects play, read from what the region says
// of them (sl.Session.Sounds) and offered two ways.  A sound heard is an
// event, as a line of chat is: a trigger or an attached play of the id,
// from the object if one is written, at the gain if one is, after the
// step's arm point and within its window.  A loop is a state, looping or
// stopped, judged over the readings of the step's window as an animation
// is.  Nothing here plays a sound.
// Why: doc/slate-runner.md#sounds

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// evSound is a sound the region said was played: an event that a heard
// expectation matches.
const evSound eventKind = evAccept + 1

// soundFloor is how far a heard gain may be from a wanted one with no
// near: a gain is an F32 the script wrote, so only its rounding.
const soundFloor = 0.001

// soundKey is what a loop's reading is of: a sound, and the binding
// whose linkset must be playing it, or "" for any prim.
type soundKey struct {
	id   msg.UUID
	from string
}

func (k soundKey) text() string {
	if k.from == "" {
		return "sound " + k.id.String()
	}
	return "sound " + k.id.String() + " from " + k.from
}

// say is the key with a verb after the sound: "sound ID looping from X".
func (k soundKey) say(verb string) string {
	t := "sound " + k.id.String() + " " + verb
	if k.from != "" {
		t += " from " + k.from
	}
	return t
}

func soundKeyOf(e *SoundExp) soundKey {
	id, _ := msg.ParseUUID(e.ID)
	k := soundKey{id: id}
	if e.From != nil {
		k.from = e.From.Text
	}
	return k
}

// soundExps is every sound expectation of the tests the run selects.
func (r *runner) soundExps() ([]*SoundExp, error) {
	tests, err := r.s.Expand()
	if err != nil {
		return nil, err
	}
	var out []*SoundExp
	for _, et := range tests {
		if r.opt.Run != nil && !r.opt.Run.MatchString(et.Test.Name) {
			continue
		}
		for _, x := range et.Steps {
			for i := range x.Step.Expect {
				if e := x.Step.Expect[i].Sound; e != nil {
					out = append(out, e)
				}
			}
		}
	}
	return out, nil
}

// soundRun is what the runner keeps for sounds: the sounds an expectation
// names, and how much of the session's log it has read.
type soundRun struct {
	named map[msg.UUID]bool // the ids an expectation names, which the transcript says wherever they play
	seq   uint64
}

// setupSounds asks for the four sound messages for the length of the run
// when a test names a sound, and says the loops already playing.  There is
// nothing to wait for: the region sends a sound when it plays, and a loop
// that began earlier is known from the object updates, which are always
// relayed.
// Why: doc/slate-runner.md#sounds
func (r *runner) setupSounds(ctx context.Context) error {
	exps, err := r.soundExps()
	if err != nil || len(exps) == 0 {
		return err
	}
	stop, err := r.sess.ListenForSounds()
	if err != nil {
		return &setupError{fmt.Sprintf("cannot listen for sounds: %v", err)}
	}
	r.stopSounds = stop
	r.snd = &soundRun{named: map[msg.UUID]bool{}}
	for _, e := range exps {
		id, _ := msg.ParseUUID(e.ID)
		r.snd.named[id] = true
	}
	st := r.sess.SoundsNow()
	r.snd.seq = st.Seq
	for _, l := range st.Loops {
		if text := r.soundText(sl.HeardSound{Kind: sl.SoundAttached, ID: l.ID, Object: l.Object, Gain: l.Gain, Looping: true}); text != "" {
			ev := &event{kind: evReading, at: l.Since, consumed: true, text: text}
			r.log.add(ev)
			r.printEvent(ev)
		}
	}
	return nil
}

// drainSounds takes in what the session heard since the last look: a
// sound played is an event for an expectation to match, and the transcript
// says it when a bound object played it or an expectation names its id.
func (r *runner) drainSounds() {
	if r.snd == nil {
		return
	}
	for _, h := range r.sess.SoundsSince(r.snd.seq) {
		r.snd.seq = h.Seq
		if !h.Replaced.IsZero() && h.Kind != sl.SoundStopped && h.Kind != sl.SoundEnded {
			// A loop that another sound took the place of stopped.
			r.sayEvent(h, r.soundText(sl.HeardSound{Kind: sl.SoundStopped, ID: h.Replaced, Replaced: h.Replaced, Object: h.Object}))
		}
		if h.Played() {
			h := h
			ev := &event{kind: evSound, at: arrived(h.At), snd: &h, text: r.soundText(h)}
			r.log.add(ev)
			r.printEvent(ev)
			continue
		}
		r.sayEvent(h, r.soundText(h))
	}
}

// sayEvent puts a line in the transcript at the time the sound was heard.
func (r *runner) sayEvent(h sl.HeardSound, text string) {
	if text == "" {
		return
	}
	ev := &event{kind: evReading, at: arrived(h.At), consumed: true, text: text}
	r.log.add(ev)
	r.printEvent(ev)
}

// soundText is the transcript line for a sound, or "" for one that is
// not said: those no bound object played and no expectation names, and
// the messages that play nothing.
func (r *runner) soundText(h sl.HeardSound) string {
	id := h.ID
	switch h.Kind {
	case sl.SoundStopped, sl.SoundEnded:
		id = h.Replaced
	case sl.SoundGain, sl.SoundPreloaded:
		return ""
	}
	if id.IsZero() {
		return "" // a stop of nothing
	}
	who := r.whoPrim(h.Object, nil)
	if who == "" && !(r.snd != nil && r.snd.named[id]) {
		return ""
	}
	var b strings.Builder
	b.WriteString("sound " + id.String() + " ")
	switch {
	case h.Kind == sl.SoundStopped || h.Kind == sl.SoundEnded:
		b.WriteString("stopped")
	case h.Kind == sl.SoundTriggered:
		b.WriteString("triggered")
	case h.Looping:
		b.WriteString("looping")
	default:
		b.WriteString("played")
	}
	if who != "" {
		b.WriteString(" from " + who)
	}
	if h.Played() {
		b.WriteString(" at gain " + strconv.FormatFloat(float64(h.Gain), 'g', 4, 32))
	}
	return b.String()
}

// soundReads is what a test keeps of the loops: what its expectations
// read and the readings of each, built from the session's log from the
// test's start.
type soundReads struct {
	keys  []soundKey
	seq   uint64
	loops map[msg.UUID]msg.UUID // prim to the sound it loops
	reads map[soundKey][]*onOffReading
}

// newSoundReads reads the test's expanded steps for the loops they name.
func newSoundReads(t *testRun) *soundReads {
	a := &soundReads{loops: map[msg.UUID]msg.UUID{}, reads: map[soundKey][]*onOffReading{}}
	seen := map[soundKey]bool{}
	for _, x := range t.et.Steps {
		for i := range x.Step.Expect {
			e := x.Step.Expect[i].Sound
			if e == nil || e.State == nil {
				continue
			}
			if k := soundKeyOf(e); !seen[k] {
				seen[k] = true
				a.keys = append(a.keys, k)
			}
		}
	}
	return a
}

// start takes the loops in force as the first reading of each key, which
// is stamped before the test's start and is the baseline of its first
// step.
func (w *watcher) startSounds() {
	a := w.snd
	if a == nil || len(a.keys) == 0 {
		return
	}
	st := w.t.r.sess.SoundsNow()
	a.seq = st.Seq
	now := time.Now()
	for _, l := range st.Loops {
		a.loops[l.Object] = l.ID
	}
	a.read(w.t.r, now)
}

// sampleSounds applies what the session heard since the last look to the
// loops, taking a reading of each key after each sound that changed one,
// stamped when it was heard.
func (w *watcher) sampleSounds() {
	a := w.snd
	if a == nil || len(a.keys) == 0 {
		return
	}
	r := w.t.r
	for _, h := range r.sess.SoundsSince(a.seq) {
		a.seq = h.Seq
		if !h.Replaced.IsZero() || h.Looping {
			delete(a.loops, h.Object)
		}
		if h.Looping {
			a.loops[h.Object] = h.ID
		}
		a.read(r, h.At)
	}
}

// read appends a reading of each key whose value differs from its last.
// A key whose binding is not known yet is not read.
func (a *soundReads) read(r *runner, at time.Time) {
	for _, k := range a.keys {
		on := false
		var b *binding
		if k.from != "" {
			if b = r.lookup(k.from); b == nil {
				continue
			}
		}
		for obj, id := range a.loops {
			if id == k.id && (b == nil || b.owns(obj)) {
				on = true
			}
		}
		if rs := a.reads[k]; len(rs) == 0 || rs[len(rs)-1].on != on {
			a.reads[k] = append(rs, &onOffReading{at: at, on: on})
		}
	}
}

// soundExpect makes expect sound UUID, in the form the file wrote.
func (s *stepRun) soundExpect(x *expState) error {
	e := x.e.Sound
	if e.From != nil {
		if err := s.linkKnown(*e.From, nil, e.From.Text); err != nil {
			return err
		}
	}
	if e.State != nil {
		return s.loopExpect(x, e)
	}
	k := soundKeyOf(e)
	var tol *tolerance
	if x.e.Near != nil {
		tol = toleranceOf(x.e.Near)
	}
	var want float64
	if e.Gain != nil {
		want = e.Gain.Value
		if tol != nil {
			x.text += " (tolerance " + describeGain(tol, want) + ")"
		}
	}
	var near []string // what of the id was heard and did not match
	x.match = func(ev *event) bool {
		if ev.kind != evSound || ev.snd.ID != k.id {
			return false
		}
		h := ev.snd
		if k.from != "" {
			if b := s.r.lookup(k.from); b == nil || !b.owns(h.Object) {
				near = append(near, fmt.Sprintf("heard from another object at gain %s", gainText(h.Gain)))
				return false
			}
		}
		if e.Gain != nil && math.Abs(float64(h.Gain)-want) > tol.allowed(soundFloor, want) {
			near = append(near, fmt.Sprintf("heard at gain %s", gainText(h.Gain)))
			return false
		}
		return true
	}
	if !x.neg {
		x.noteFn = func() string {
			if len(near) == 0 {
				return "; no sound of that id was heard"
			}
			return "; last: " + near[len(near)-1]
		}
	}
	return nil
}

func gainText(g float32) string { return strconv.FormatFloat(float64(g), 'g', 4, 32) }

// describeGain is the tolerance as the transcript says it.
func describeGain(t *tolerance, want float64) string {
	return strconv.FormatFloat(t.allowed(soundFloor, want), 'g', 4, 64)
}

// loopExpect makes expect sound UUID is|becomes looping|stopped, or
// changes, over the readings of the test's loops.
func (s *stepRun) loopExpect(x *expState, e *SoundExp) error {
	k := soundKeyOf(e)
	sx := &onOffState{word: e.State.Kind, on: e.Loop}
	x.match = never
	x.eval = func(ctx context.Context) error {
		return s.evalOnOff(x, sx, s.t.watch.snd.reads[k], onOffWords{
			noun:    "sound",
			label:   k.text(),
			reading: func(on bool) string { return k.say(map[bool]string{true: "looping", false: "not looping"}[on]) },
			change: func(on bool) string {
				return k.say(map[bool]string{true: "started looping", false: "stopped"}[on])
			},
		})
	}
	if !x.neg {
		x.noteFn = func() string {
			rs := s.t.watch.snd.reads[k]
			if len(rs) == 0 {
				return "; no reading was taken" + map[bool]string{true: "", false: ": no binding of that name could be read against"}[k.from == ""]
			}
			on := rs[len(rs)-1].on
			return "; last reading: " + k.say(map[bool]string{true: "looping", false: "not looping"}[on])
		}
	}
	return nil
}
