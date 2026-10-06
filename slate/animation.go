package slate

// expect animation: an animation the tester is playing, read from what
// the region says in AvatarAnimation (sl.Session.Animations) and judged
// as a state is, on or off, over the readings of the step's window.
// Why: doc/slate-runner.md#animations

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

const defaultAnimWait = 10 * time.Second // the first AvatarAnimation after the run asks for it

func (c runCfg) animationWait() time.Duration { return orDefault(c.animations, defaultAnimWait) }

// animKey is what a reading is of: an animation, and the binding whose
// linkset must have started it, or "" for any source.
type animKey struct {
	id   msg.UUID
	from string
}

// onOffReading is the state of one key when what said it was heard.  It
// serves animations and sounds, which are judged alike (evalOnOff).
type onOffReading struct {
	at time.Time
	on bool
}

func (k animKey) text() string {
	if k.from == "" {
		return "animation " + k.id.String()
	}
	return "animation " + k.id.String() + " from " + k.from
}

// animWatch is what a test keeps of the tester's animations.
type animWatch struct {
	keys  []animKey // what an expectation of the test reads
	froms []string  // the bindings an expectation names with from
	reads map[animKey][]*onOffReading

	shown map[string]bool // what the transcript has said is playing, by line
	said  bool            // the first list has been said
}

// newAnimWatch reads the test's expanded steps for the animations they
// name.
func newAnimWatch(t *testRun) *animWatch {
	a := &animWatch{reads: map[animKey][]*onOffReading{}, shown: map[string]bool{}}
	seen := map[animKey]bool{}
	for _, x := range t.et.Steps {
		for i := range x.Step.Expect {
			e := x.Step.Expect[i].Animation
			if e == nil {
				continue
			}
			k := animKeyOf(e)
			if !seen[k] {
				seen[k] = true
				a.keys = append(a.keys, k)
			}
			if k.from != "" && !containsStr(a.froms, k.from) {
				a.froms = append(a.froms, k.from)
			}
		}
	}
	return a
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// animKeyOf is the key of an expectation. The id was checked at Check.
func animKeyOf(e *AnimationExp) animKey {
	id, _ := msg.ParseUUID(e.ID)
	k := animKey{id: id}
	if e.From != nil {
		k.from = e.From.Text
	}
	return k
}

// usesAnimations is whether any selected test names an animation, so the
// run asks for AvatarAnimation at all: it arrives for every avatar in
// range and is not relayed to a session that does not ask.
func (r *runner) usesAnimations() (bool, error) {
	tests, err := r.s.Expand()
	if err != nil {
		return false, err
	}
	for _, et := range tests {
		if r.opt.Run != nil && !r.opt.Run.MatchString(et.Test.Name) {
			continue
		}
		for _, x := range et.Steps {
			for i := range x.Step.Expect {
				if x.Step.Expect[i].Animation != nil {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// setupAnimations asks for AvatarAnimation for the length of the run and
// waits for a list heard after the asking: the baseline of a first step
// is what the region said the avatar was playing, and a list from before
// the subscription is of unknown age.
// Why: doc/slate-runner.md#animations
func (r *runner) setupAnimations(ctx context.Context) error {
	uses, err := r.usesAnimations()
	if err != nil || !uses {
		return err
	}
	asked := time.Now()
	stop, err := r.sess.ListenForAnimations()
	if err != nil {
		return &setupError{fmt.Sprintf("cannot listen for animations: %v", err)}
	}
	r.stopAnims = stop
	deadline := asked.Add(r.cfg.animationWait())
	for {
		if _, at := r.sess.Animations(); at.After(asked) {
			return nil
		}
		if !time.Now().Before(deadline) {
			return &setupError{fmt.Sprintf("no AvatarAnimation was heard in %s, and an expectation of an animation needs one to read from; the simulator resends the list about every three seconds", r.cfg.animationWait())}
		}
		if err := r.sess.Settle(ctx, r.cfg.poll); err != nil {
			return err
		}
	}
}

// sample reads the tester's list and keeps what differs from the last
// reading of each key, stamped when the list was heard, so a change is
// never dated after the arm point it came before. It prints the lines of
// what started and stopped.
func (w *watcher) sample() {
	a := w.an
	if a == nil || len(a.keys) == 0 {
		return
	}
	r := w.t.r
	list, heard := r.sess.Animations()
	if heard.IsZero() {
		return
	}
	now := map[string]bool{}
	for _, k := range a.keys {
		on, known := false, true
		if k.from == "" {
			for _, p := range list {
				on = on || p.ID == k.id
			}
			if on {
				now[animKey{id: k.id}.text()] = true
			}
		} else if b := r.lookup(k.from); b == nil {
			known = false
		} else {
			for _, p := range list {
				if p.ID == k.id && b.owns(p.Source) {
					on = true
				}
			}
		}
		if rs := a.reads[k]; known && (len(rs) == 0 || rs[len(rs)-1].on != on) {
			a.reads[k] = append(rs, &onOffReading{at: heard, on: on})
		}
	}
	// Every animation a named binding started, whether or not an
	// expectation names it: that is how a test author learns the id of
	// what a product plays.
	for _, from := range a.froms {
		b := r.lookup(from)
		if b == nil {
			continue
		}
		for _, p := range list {
			if !p.Source.IsZero() && b.owns(p.Source) {
				now[animKey{id: p.ID, from: from}.text()] = true
			}
		}
	}
	// An animation said with its source is not said again without it:
	// the plain line is for one no named binding started.
	for l := range now {
		if id, _, ok := strings.Cut(l, " from "); ok {
			delete(now, id)
		}
	}
	var lines []string
	for l := range now {
		if !a.shown[l] {
			word := "started"
			if !a.said {
				word = "playing"
			}
			lines = append(lines, l+"\x00"+word)
		}
	}
	for l := range a.shown {
		if !now[l] {
			lines = append(lines, l+"\x00stopped")
		}
	}
	sort.Strings(lines)
	for _, ls := range lines {
		l, word, _ := strings.Cut(ls, "\x00")
		// "animation ID from NAME" says "animation ID started from NAME".
		id, from, _ := strings.Cut(strings.TrimPrefix(l, "animation "), " from ")
		text := "animation " + id + " " + word
		if from != "" {
			text += " from " + from
		}
		ev := &event{kind: evReading, at: heard, consumed: true, text: text}
		r.log.add(ev)
		r.printEvent(ev)
	}
	a.shown, a.said = now, true
}

// animationExp is an animation expectation: the key and the state asked.
type animationExp struct {
	key animKey
	onOffState
}

// onOffState is what an on/off expectation judges by: the word, the value
// asked for, and what the judging has said and settled.
type onOffState struct {
	word  StateKind
	on    bool
	noted bool
	final bool // a negative's window was judged
}

// animationExpect makes expect animation UUID is|becomes on|off, or
// changes, with from OBJ for the animations the linkset started.
func (s *stepRun) animationExpect(x *expState) error {
	e := x.e.Animation
	if e.From != nil {
		if err := s.linkKnown(*e.From, nil, e.From.Text); err != nil {
			return err
		}
	}
	ax := &animationExp{key: animKeyOf(e), onOffState: onOffState{word: e.State.Kind, on: e.On}}
	x.match = never
	x.eval = func(ctx context.Context) error { return s.evalAnimation(x, ax) }
	if !x.neg {
		x.noteFn = func() string {
			rs := s.t.watch.an.reads[ax.key]
			if len(rs) == 0 {
				return "; no reading was taken: no list of the tester's animations was heard" +
					map[bool]string{true: "", false: " that a binding of that name could be read against"}[ax.key.from == ""]
			}
			return "; last reading: " + animText(ax.key, rs[len(rs)-1].on)
		}
	}
	return nil
}

// animText is a reading as the transcript says it.
func animText(k animKey, on bool) string {
	if on {
		return k.text() + " playing"
	}
	return k.text() + " not playing"
}

// evalAnimation judges the readings of an animation.
func (s *stepRun) evalAnimation(x *expState, ax *animationExp) error {
	return s.evalOnOff(x, &ax.onOffState, s.t.watch.an.reads[ax.key], onOffWords{
		noun:    "animation",
		label:   ax.key.text(),
		reading: func(on bool) string { return animText(ax.key, on) },
		change:  func(on bool) string { return ax.key.text() + map[bool]string{true: " started", false: " stopped"}[on] },
	})
}

// onOffWords is how an on/off expectation says what it read.
type onOffWords struct {
	noun    string               // what is judged, for a failure's sentence
	label   string               // the key, as a transcript line says it
	reading func(on bool) string // a reading
	change  func(on bool) string // a change from the reading before
}

// evalOnOff judges the readings as evalState does: the baseline is the
// latest reading at or before the arm point, else the first after it, and
// `becomes` needs the other value to come first.
// Why: doc/slate-runner.md#state-words
func (s *stepRun) evalOnOff(x *expState, ax *onOffState, rs []*onOffReading, w onOffWords) error {
	if x.neg && ax.final {
		return nil
	}
	var base *onOffReading
	for i := len(rs) - 1; i >= 0; i-- {
		if !rs[i].at.After(s.arm) {
			base = rs[i]
			break
		}
	}
	var after []*onOffReading
	for _, r := range rs {
		if r.at.After(s.arm) && !r.at.After(x.limit) {
			after = append(after, r)
		}
	}
	late := false
	if base == nil && len(after) > 0 {
		base, after, late = after[0], after[1:], true
	}
	var seq []*onOffReading
	if base != nil {
		seq = append([]*onOffReading{base}, after...)
	}
	now := time.Now()
	if late && ax.word != StateIs && !ax.noted {
		ax.noted = true
		ev := &event{kind: evReading, at: now, consumed: true,
			text: fmt.Sprintf("baseline for %s taken after the arm point", w.label)}
		s.r.log.add(ev)
		s.r.printEvent(ev)
	}

	var hit *onOffReading
	switch {
	case ax.word == StateChanges:
		for _, r := range seq[min(1, len(seq)):] {
			if r.on != base.on {
				hit = r
				break
			}
		}
	case ax.word == StateIs:
		for _, r := range seq {
			if r.on == ax.on {
				hit = r
				break
			}
		}
	default: // becomes
		left := false
		for _, r := range seq {
			if r.on == ax.on {
				if left {
					hit = r
					break
				}
			} else {
				left = true
			}
		}
	}
	switch {
	case hit != nil:
		text := w.reading(hit.on)
		if hit != base {
			text = w.change(hit.on)
		}
		x.ev = &event{kind: evReading, at: hit.at, text: text}
		if x.neg {
			x.forbidden = true
		} else {
			x.matched = true
		}
	case x.neg && !now.Before(x.limit):
		// The window is over and nothing was heard: a region that never
		// said is not proof that nothing played.
		ax.final = true
		if len(seq) == 0 {
			x.note = "; no reading was taken, and a negative needs one"
			s.why = "a negative " + w.noun + " needs a real reading, and none was taken"
			s.fail(1, "no reading")
		}
	}
	return nil
}
