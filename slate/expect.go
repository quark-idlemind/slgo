package slate

// State expectations (texture, offset, repeats, rotation, click, fullbright, glow, colour, alpha, position, size) and what
// reads them: the object poll, the readings it logs, the baseline and
// `original` they are judged against, the click describe, and observe,
// the one hook the step loop calls for everything that is not a heard
// event.
// Why: doc/slate-runner.md#baselines-and-original

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// The kinds of event this file and its neighbours add. They are numbered
// well above the heard kinds so that two of them added side by side cannot
// share a value.
const (
	evReading eventKind = 100 + iota // a transcript line made from a poll
	evRez                            // a new root, first observed by a poll
	evAccept                         // a give's accept, sent
)

// What the runner paces itself by, with the production value when a
// config leaves it zero. Tests shorten them in runCfg.
const (
	defaultObjectPoll = 250 * time.Millisecond // the object and face polls
	defaultRedescribe = time.Second            // between RequestMultipleObjects for one prim
	defaultClickWait  = 30 * time.Second       // a click describe
	defaultSettle     = 250 * time.Millisecond // the rez settle: one object poll
	defaultInvPoll    = time.Second            // between inventory fetches while a give is awaited
)

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

func (c runCfg) objectEvery() time.Duration { return orDefault(c.objects, defaultObjectPoll) }
func (c runCfg) redescribeEvery() time.Duration {
	return orDefault(c.redescribe, defaultRedescribe)
}
func (c runCfg) clickWait() time.Duration   { return orDefault(c.click, defaultClickWait) }
func (c runCfg) settleFor() time.Duration   { return orDefault(c.settle, defaultSettle) }
func (c runCfg) invEvery() time.Duration    { return orDefault(c.inv, defaultInvPoll) }
func never(*event) bool                     { return false }
func (c runCfg) describeFor() time.Duration { return c.clickWait() }

// Tolerances of a comparison. The offset one is the quantisation of the
// setter, the rotation one is two steps of 1/32768 of a turn.
// Why: doc/slate-runner.md#offset-repeats-rotation
const (
	offsetTol  = 2.0 / 32767
	repeatsTol = 1e-4
	rotTol     = 2.0 / 32768
)

// vecTol is the tolerance of a position and a size, in metres on each
// axis, and the 1e-9 is for the float arithmetic.
// Why: doc/slate-runner.md#position-and-size
const vecTol = 0.001 + 1e-9

type stateKind int

const (
	kTexture stateKind = iota
	kOffset
	kRepeats
	kRotation
	kClick
	kFullbright
	kGlow
	kColour
	kAlpha
	kButton // the count of a button search (buttonexp.go)
	kPosition
	kSize
)

// clickFace is the face of the key that reads a prim's click byte, and
// allFace the face of a key that reads every face of the prim at once.
const (
	clickFace = -1
	allFace   = -2
	// primFace is the face of a key that reads the prim itself: its
	// position or its size.
	primFace = -3
)

// levelTol is the tolerance of glow, colour and alpha: one step of the
// byte they travel as, round(value * 255).
// Why: doc/slate-runner.md#face-properties
const levelTol = 1.0/255 + 1e-9

// allMax is how many faces a tuple reading decodes: the most a texture
// entry can name. The tuple is cut after the first face that is only the
// default (see tupleOf).
const allMax = 32

// readKey is what a reading is of: a face of a bound prim, or its click
// byte. The name is the script's, so an `as` name is read from when it is
// bound.
type readKey struct {
	name string
	face int
	kind stateKind
}

// reading is one observation of a key, stamped when the poll made it. A
// face reading carries every property of the face; the expectation takes
// the one it is about.
type reading struct {
	at    time.Time
	tex   msg.UUID
	off   [2]float64
	rep   [2]float64
	turns float64
	click uint8
	vec   [3]float64 // a position or a size, each a float32 held whole

	bright bool
	glow   float64    // Glow / 255
	col    [3]float64 // red, green, blue, each / 255
	alpha  float64    // Colour[3] / 255

	// A button reading: the tuples found, and the line it prints. A wanted
	// value with atLeast is any count from count up.
	count   int
	atLeast bool
	label   string

	// all, when set, is the reading of face all: the reading of each
	// face of the prim, as many as the prim has when its shape says
	// (faceCount), else from face 0 to the last that is not just the
	// default and the first one after it. Past its end every face is its
	// last element.
	all []*reading
}

func faceReading(f sl.Face, at time.Time) *reading {
	s, t := f.OffsetsF()
	return &reading{
		at: at, tex: f.Texture,
		off:   [2]float64{float64(s), float64(t)},
		rep:   [2]float64{float64(f.ScaleS), float64(f.ScaleT)},
		turns: float64(f.Rotation) / 32768.0, // a float64 division: Rotation is an int16

		bright: f.Fullbright(),
		glow:   float64(f.Glow) / 255,
		col:    [3]float64{float64(f.Colour[0]) / 255, float64(f.Colour[1]) / 255, float64(f.Colour[2]) / 255},
		alpha:  float64(f.Colour[3]) / 255,
	}
}

// tupleOf is the reading of face all. With exact set the faces are
// exactly the prim's, as many as its shape gives, and every one is an
// element. Otherwise nothing says how many faces the prim has, so the tuple
// runs to the last face that differs from the default (a face past what the
// entry names reads as the default) and takes one more, which is the
// default itself. A prim whose faces are all alike reads as one element.
// Why: doc/slate-runner.md#face-all
func tupleOf(faces []sl.Face, at time.Time, exact bool) *reading {
	n := 1
	if exact {
		n = len(faces)
	} else {
		for i := len(faces) - 2; i >= 0; i-- {
			if faces[i] != faces[len(faces)-1] {
				n = i + 2
				break
			}
		}
	}
	r := &reading{at: at}
	for _, f := range faces[:n] {
		r.all = append(r.all, faceReading(f, at))
	}
	return r
}

// faceCount is how many faces a prim of the binding has, from its shape
// (Seen.FaceCount). When that cannot be said (a sculpt, a mesh, a prim
// nothing has described) it is false, and the transcript says so once for
// the binding in the test.
// Why: doc/slate-runner.md#face-all
func (t *testRun) faceCount(name string, step int, o *sl.Seen) (int, bool) {
	if n, ok := o.FaceCount(); ok && n > 0 {
		return n, true
	}
	if !t.uncounted[name] {
		if t.uncounted == nil {
			t.uncounted = map[string]bool{}
		}
		t.uncounted[name] = true
		t.r.printf("slate: step %d: the face count of %q is not known (sculpt, mesh or not described); face all reads the faces its texture entry names", step, name)
	}
	return 0, false
}

// vec3 is a vector as a reading holds it: float32 values, kept whole.
func vec3(v msg.Vector3) [3]float64 {
	return [3]float64{float64(v.X), float64(v.Y), float64(v.Z)}
}

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

// equal is the comparison of a kind: exact for a texture, a click and
// fullbright, within the tolerance for the rest. Two tuples are compared
// face by face, the shorter continued by its last element.
func (k stateKind) equal(a, b *reading) bool {
	if a.all != nil || b.all != nil {
		xs, ys := a.faces(), b.faces()
		for i := range max(len(xs), len(ys)) {
			if !k.equal(xs[min(i, len(xs)-1)], ys[min(i, len(ys)-1)]) {
				return false
			}
		}
		return true
	}
	switch k {
	case kPosition, kSize:
		return near(a.vec[0], b.vec[0], vecTol) && near(a.vec[1], b.vec[1], vecTol) && near(a.vec[2], b.vec[2], vecTol)
	case kButton:
		if b.atLeast {
			return a.count >= b.count
		}
		return a.count == b.count
	case kTexture:
		return a.tex == b.tex
	case kOffset:
		return near(a.off[0], b.off[0], offsetTol) && near(a.off[1], b.off[1], offsetTol)
	case kRepeats:
		return near(a.rep[0], b.rep[0], repeatsTol) && near(a.rep[1], b.rep[1], repeatsTol)
	case kRotation:
		return near(a.turns, b.turns, rotTol)
	case kFullbright:
		return a.bright == b.bright
	case kGlow:
		return near(a.glow, b.glow, levelTol)
	case kColour:
		return near(a.col[0], b.col[0], levelTol) && near(a.col[1], b.col[1], levelTol) && near(a.col[2], b.col[2], levelTol)
	case kAlpha:
		return near(a.alpha, b.alpha, levelTol)
	}
	return a.click == b.click
}

// faces is the elements of a reading: itself when it is one face.
func (r *reading) faces() []*reading {
	if r.all != nil {
		return r.all
	}
	return []*reading{r}
}

// matches is whether a reading is the wanted value: a tuple is every face
// equal to a single value, or equal to a tuple.
func (k stateKind) matches(r, want *reading) bool {
	if r.all == nil || want.all != nil {
		return k.equal(r, want)
	}
	for _, f := range r.all {
		if !k.equal(f, want) {
			return false
		}
	}
	return true
}

// capType is the type of the capture of a kind.
func (k stateKind) capType() CaptureType {
	switch k {
	case kTexture:
		return CapUUID
	case kOffset, kRepeats:
		return CapPair
	case kRotation, kGlow, kAlpha, kButton:
		return CapNumber
	case kFullbright:
		return CapOnOff
	case kColour:
		return CapTriple
	case kPosition, kSize:
		return CapVector
	}
	return CapClick
}

// printKind is the kind whose line a key's readings print. The offset,
// repeats and rotation expectations have always printed the texture line,
// and go on doing so; the other kinds print their own.
func (k stateKind) printKind() stateKind {
	switch k {
	case kOffset, kRepeats, kRotation:
		return kTexture
	}
	return k
}

// r4 rounds a level to four places, which is finer than the byte it came
// from and keeps the transcript short.
func r4(v float64) float64 { return math.Round(v*1e4) / 1e4 }

// text is one face's value as the transcript says it.
func (k stateKind) text(r *reading) string {
	switch k {
	case kButton:
		return r.label
	case kPosition, kSize:
		return g3(r.vec)
	case kTexture:
		return r.tex.String()
	case kOffset:
		return fmt.Sprintf("%g %g", r.off[0], r.off[1])
	case kRepeats:
		return fmt.Sprintf("%g %g", r.rep[0], r.rep[1])
	case kRotation:
		return fmt.Sprintf("%g", r.turns)
	case kFullbright:
		if r.bright {
			return "on"
		}
		return "off"
	case kGlow:
		return fmt.Sprintf("%g", r4(r.glow))
	case kColour:
		return fmt.Sprintf("%g %g %g", r4(r.col[0]), r4(r.col[1]), r4(r.col[2]))
	case kAlpha:
		return fmt.Sprintf("%g", r4(r.alpha))
	}
	return fmt.Sprintf("%d", r.click)
}

// show is a reading as the transcript says it. A face all reading says
// the value of each face from the first, separated by commas.
func (k stateKind) show(key readKey, r *reading) string {
	if k == kClick {
		return fmt.Sprintf("click %s %d", key.name, r.click)
	}
	if k == kButton {
		return r.label
	}
	if k == kPosition || k == kSize {
		return fmt.Sprintf("%s %s %s", map[stateKind]string{kPosition: "position", kSize: "size"}[k], key.name, g3(r.vec))
	}
	word := [...]string{kTexture: "texture", kOffset: "offset", kRepeats: "repeats", kRotation: "rotation",
		kFullbright: "fullbright", kGlow: "glow", kColour: "colour", kAlpha: "alpha"}[k]
	face := fmt.Sprintf("%d", key.face)
	if key.face == allFace {
		face = "all"
	}
	parts := make([]string, 0, len(r.all)+1)
	for _, f := range r.faces() {
		parts = append(parts, k.text(f))
	}
	return fmt.Sprintf("%s %s face %s %s", word, key.name, face, strings.Join(parts, ", "))
}

// value is a reading as the capture of its kind; a tuple is a capture that
// only a face all expectation of the kind can use.
func (k stateKind) value(r *reading) capValue {
	if k == kButton {
		return capValue{typ: CapNumber, num: float64(r.count)}
	}
	if k == kPosition || k == kSize {
		return capValue{typ: CapVector, vec: r.vec}
	}
	if r.all != nil {
		v := capValue{typ: k.capType(), all: true}
		for _, f := range r.all {
			v.tuple = append(v.tuple, k.value(f))
		}
		return v
	}
	switch k {
	case kTexture:
		return capValue{typ: CapUUID, id: r.tex}
	case kOffset:
		return capValue{typ: CapPair, pair: r.off}
	case kRepeats:
		return capValue{typ: CapPair, pair: r.rep}
	case kRotation:
		return capValue{typ: CapNumber, num: r.turns}
	case kFullbright:
		return capValue{typ: CapOnOff, on: r.bright}
	case kGlow:
		return capValue{typ: CapNumber, num: r4(r.glow)}
	case kColour:
		return capValue{typ: CapTriple, triple: [3]float64{r4(r.col[0]), r4(r.col[1]), r4(r.col[2])}}
	case kAlpha:
		return capValue{typ: CapNumber, num: r4(r.alpha)}
	}
	return capValue{typ: CapClick, click: r.click}
}

// want is a capture as the reading a state is compared with.
func (k stateKind) want(v capValue) *reading {
	if v.all {
		r := &reading{}
		for _, f := range v.tuple {
			r.all = append(r.all, k.want(f))
		}
		return r
	}
	r := &reading{}
	switch k {
	case kPosition, kSize:
		r.vec = v.vec
	case kTexture:
		r.tex = v.id
	case kOffset:
		r.off = v.pair
	case kRepeats:
		r.rep = v.pair
	case kRotation:
		r.turns = v.num
	case kFullbright:
		r.bright = v.on
	case kGlow:
		r.glow = v.num
	case kColour:
		r.col = v.triple
	case kAlpha:
		r.alpha = v.num
	default:
		r.click = v.click
	}
	return r
}

// stateExp is a state expectation: what it reads, the word and the value.
type stateExp struct {
	kind  stateKind
	key   readKey
	word  StateKind
	orig  bool
	want  reading
	any   bool          // is any: a real reading, whatever it is
	why   func() string // a button's refusal, said after "unmatched"; nil for the rest
	noted bool          // the baseline note was printed
	final bool          // a negative's window was judged
}

// keyName is the name a reading is keyed by: the binding's, or for a
// link N the name that link is bound under (bridge.go).
func keyName(name Ident, link *Int) string {
	if link == nil {
		return name.Text
	}
	return linkName(name.Text, int32(link.Value))
}

// stateKeyOf is the key and kind of a state expectation, and false for
// any other.
func stateKeyOf(e *Expect) (readKey, stateKind, bool) {
	face := func(all bool, f Int) int {
		if all {
			return allFace
		}
		return int(f.Value)
	}
	switch {
	case e.Texture != nil:
		x := e.Texture
		return readKey{keyName(x.Name, x.Link), face(x.FaceAll, x.Face), kTexture}, kTexture, true
	case e.Offset != nil:
		x := e.Offset
		return readKey{keyName(x.Name, x.Link), face(x.FaceAll, x.Face), kOffset}, kOffset, true
	case e.Repeats != nil:
		x := e.Repeats
		return readKey{keyName(x.Name, x.Link), face(x.FaceAll, x.Face), kRepeats}, kRepeats, true
	case e.Rot != nil:
		x := e.Rot
		return readKey{keyName(x.Name, x.Link), face(x.FaceAll, x.Face), kRotation}, kRotation, true
	case e.Fullbright != nil:
		x := e.Fullbright
		return readKey{keyName(x.Name, x.Link), face(x.FaceAll, x.Face), kFullbright}, kFullbright, true
	case e.Glow != nil:
		x := e.Glow
		return readKey{keyName(x.Name, x.Link), face(x.FaceAll, x.Face), kGlow}, kGlow, true
	case e.Colour != nil:
		x := e.Colour
		return readKey{keyName(x.Name, x.Link), face(x.FaceAll, x.Face), kColour}, kColour, true
	case e.Alpha != nil:
		x := e.Alpha
		return readKey{keyName(x.Name, x.Link), face(x.FaceAll, x.Face), kAlpha}, kAlpha, true
	case e.Click != nil:
		return readKey{keyName(e.Click.Name, e.Click.Link), clickFace, kClick}, kClick, true
	case e.Position != nil:
		return readKey{keyName(e.Position.Name, e.Position.Link), primFace, kPosition}, kPosition, true
	case e.Size != nil:
		return readKey{keyName(e.Size.Name, e.Size.Link), primFace, kSize}, kSize, true
	}
	return readKey{}, 0, false
}

// watcher is what a test reads: the prims and faces its state
// expectations name, the roots that appear in it, and the log of both.
// One poll serves them all. Only the runner's goroutine touches it.
type watcher struct {
	t    *testRun
	on   bool // the test has a state expectation or a rez: poll at all
	rez  bool // the test has a rez: note new roots
	keys []readKey
	nfac map[string]int // faces to decode for a name: the highest named, plus one
	// allStep is the first step with a face all of a name, which is the
	// step a note about its face count is printed under.
	allStep map[string]int
	btn     btnState          // button readings (buttonexp.go)
	linkWhy map[string]string // a link name -> why its last poll found no prim

	initial  bool
	lastPoll time.Time
	all      []*sl.Seen // the latest poll
	reads    map[readKey][]*reading
	printed  map[readKey]string
	orig     map[readKey]*reading
	first    map[msg.UUID]time.Time // when each object was first observed
	roots    []*rezRoot
	lastReq  map[msg.UUID]time.Time

	att      []string                 // names an attached expectation reads (wear.go)
	areads   map[string][]*attReading // their readings
	aprinted map[string]string
}

// newWatcher reads the test's expanded steps, before each and after each
// included, for what has to be read from the start.
func newWatcher(t *testRun) *watcher {
	w := &watcher{
		t: t, linkWhy: map[string]string{}, nfac: map[string]int{}, allStep: map[string]int{},
		reads: map[readKey][]*reading{}, printed: map[readKey]string{},
		orig: map[readKey]*reading{}, first: map[msg.UUID]time.Time{},
		lastReq: map[msg.UUID]time.Time{},
		areads:  map[string][]*attReading{}, aprinted: map[string]string{},
	}
	seen := map[readKey]bool{}
	for n, x := range t.et.Steps {
		for i := range x.Step.Expect {
			e := &x.Step.Expect[i]
			if e.Button != nil {
				w.on = true
				w.registerButton(e)
			}
			if e.Rez != nil {
				w.on, w.rez = true, true
			}
			if e.Attached != nil {
				w.on = true
				if n := e.Attached.Name.Text; !slices.Contains(w.att, n) {
					w.att = append(w.att, n)
				}
			}
			k, _, ok := stateKeyOf(e)
			if !ok {
				continue
			}
			w.on = true
			if !seen[k] {
				seen[k] = true
				w.keys = append(w.keys, k)
			}
			w.nfac[k.name] = max(w.nfac[k.name], k.face+1)
			if k.face == allFace && w.allStep[k.name] == 0 {
				w.allStep[k.name] = n + 1
			}
		}
	}
	return w
}

// start takes the first reading, which is the baseline of the first step
// and is stamped before the test's start. Everything it sees is old.
func (w *watcher) start(ctx context.Context) error {
	w.initial = true
	err := w.poll(ctx, true)
	w.initial = false
	return err
}

func byID(all []*sl.Seen, id msg.UUID) *sl.Seen {
	for _, o := range all {
		if o.ID == id {
			return o
		}
	}
	return nil
}

// poll reads the region once, when one is due: the readings of every key,
// the first sighting of every object, and the linkset of each `as`
// binding, which can gain children after its root. It is one call that
// copies nothing the store holds.
func (w *watcher) poll(ctx context.Context, force bool) error {
	if !w.on {
		return nil
	}
	r := w.t.r
	if !force && time.Since(w.lastPoll) < r.cfg.objectEvery() {
		return nil
	}
	all, err := r.sess.Backend().Objects(ctx, "", "")
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	}
	at := time.Now()
	w.lastPoll = at
	w.all = all

	var fresh []*rezRoot
	for _, o := range all {
		if _, ok := w.first[o.ID]; ok {
			continue
		}
		w.first[o.ID] = at
		if w.initial || !w.rez || o.Parent != 0 || o.PCode != pcodePrim {
			continue
		}
		c := *o
		fresh = append(fresh, &rezRoot{seen: &c, ev: &event{kind: evRez, at: at}})
	}
	// The order of one poll is the order the session sorts its objects.
	sort.Slice(fresh, func(i, j int) bool { return fresh[i].seen.Local < fresh[j].seen.Local })
	w.roots = append(w.roots, fresh...)
	for _, root := range w.roots {
		if o := byID(all, root.seen.ID); o != nil {
			c := *o
			root.seen = &c
		}
	}

	for _, b := range w.t.as {
		if b.seen.ID.IsZero() {
			continue // a wear that has not returned
		}
		if members, err := linksetOf(all, b.seen); err == nil {
			b.members = members
		}
	}
	w.readAttached(all, at)
	for _, k := range w.keys {
		b := r.lookup(k.name)
		if base, n, ok := splitLinkName(k.name); ok {
			// A link of a binding with no probe is the prim the store
			// numbers n now, which may not be the one it was.
			if bb := r.lookup(base); bb != nil && !r.probed(bb) {
				lb, err := r.linkBinding(ctx, all, bb, n)
				if err != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					w.linkFailed(k, err)
					continue
				}
				delete(w.linkWhy, k.name)
				b = lb
			}
		}
		if b == nil {
			continue
		}
		o := byID(all, b.seen.ID)
		if o == nil {
			continue
		}
		var rd *reading
		show := k.kind.printKind()
		switch {
		case k.kind == kButton:
			if rd = w.readButton(ctx, k, o, at); rd == nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				continue
			}
		case k.kind == kPosition:
			rd = &reading{at: at, vec: vec3(o.Position)}
		case k.kind == kSize:
			rd = &reading{at: at, vec: vec3(o.Scale)}
		case k.face == clickFace:
			if !o.ClickKnown {
				continue
			}
			rd = &reading{at: at, click: o.Click}
		case k.face == allFace:
			n, exact := w.t.faceCount(k.name, w.allStep[k.name], o)
			faces, err := o.Faces(n)
			if !exact {
				faces, err = o.Faces(allMax)
			}
			if err != nil {
				continue
			}
			rd = tupleOf(faces, at, exact)
		default:
			faces, err := o.Faces(w.nfac[k.name])
			if err != nil || k.face >= len(faces) {
				continue
			}
			rd = faceReading(faces[k.face], at)
		}
		line := show.show(k, rd)
		w.reads[k] = append(w.reads[k], rd)
		// A line is printed when the reading differs from the last one
		// printed, a stale one included. Each line of a name and face is
		// kept apart, as a fullbright line is not a texture line.
		pk := k
		pk.kind = show
		if w.printed[pk] != line {
			w.printed[pk] = line
			ev := &event{kind: evReading, at: at, consumed: true, text: line}
			r.log.add(ev)
			r.printEvent(ev)
		}
	}
	return nil
}

// linkFailed keeps why a link has no prim, for the step that waits on it:
// a button reading carries it as its own reason.
func (w *watcher) linkFailed(k readKey, err error) {
	if bw := w.btn.by[k]; k.kind == kButton && bw != nil {
		bw.why = err.Error()
		return
	}
	w.linkWhy[k.name] = err.Error()
}

// original is the reading of a key when the test's own steps begin, which
// is after before each, else the first reading after that. It is fixed
// once the steps have begun.
func (w *watcher) original(k readKey) *reading {
	if r := w.orig[k]; r != nil {
		return r
	}
	at := w.t.ownStart
	begun := !at.IsZero()
	if !begun {
		at = w.t.start
	}
	rs := w.reads[k]
	var pick *reading
	for i := len(rs) - 1; i >= 0; i-- {
		if !rs[i].at.After(at) {
			pick = rs[i]
			break
		}
	}
	if pick == nil && len(rs) > 0 {
		pick = rs[0]
	}
	if pick != nil && begun {
		w.orig[k] = pick
	}
	return pick
}

// requestObject asks the region to describe a prim again: the message a
// viewer sends for a cache miss, with the miss type for having nothing.
// The session's own describeAgain sends it and then waits up to 3 s, which
// a step cannot spend in a loop.
// Why: doc/slate-runner.md#texture
func (r *runner) requestObject(ctx context.Context, local uint32) error {
	m := &msg.RequestMultipleObjects{}
	m.AgentData.AgentID, m.AgentData.SessionID = r.sess.Me(), r.sess.Session()
	m.ObjectData = []msg.RequestMultipleObjects_ObjectData{{CacheMissType: 0, ID: local}}
	return r.sess.Send(ctx, m)
}

// redescribe asks again for a prim that an unmet expectation reads, when
// the last request was long enough ago.
func (w *watcher) redescribe(ctx context.Context, id msg.UUID) error {
	o := byID(w.all, id)
	if o == nil || o.Local == 0 {
		return nil
	}
	if last, ok := w.lastReq[id]; ok && time.Since(last) < w.t.r.cfg.redescribeEvery() {
		return nil
	}
	w.lastReq[id] = time.Now()
	if err := w.t.r.requestObject(ctx, o.Local); err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

// linkKnown refuses a name that is not an object. A link N with a probe
// is refused now if the probe has no such link; without one it is read
// from the prim the store numbers N at each poll (watcher.poll), so the
// first poll is what can refuse it.
func (s *stepRun) linkKnown(base Ident, link *Int, key string) error {
	b := s.r.lookup(base.Text)
	if b == nil {
		return fmt.Errorf("%s is not an object", base.Text)
	}
	if link == nil {
		return nil
	}
	if !s.r.probed(b) {
		return nil
	}
	if _, err := s.r.linkBinding(s.r.ctx, nil, b, link.Value); err != nil {
		return err
	}
	if s.r.lookup(key) == nil {
		return fmt.Errorf("%s is not an object", key)
	}
	return nil
}

// linkWhy is a reading's note after "unmatched" when its link could not
// be resolved at the last poll.
func (s *stepRun) linkWhy(key string) func() string {
	w := s.t.watch
	return func() string {
		why := w.linkWhy[key]
		if why == "" {
			return ""
		}
		return fmt.Sprintf("; slate: step %d: %s", s.n, why)
	}
}

// stateExpect makes a state expectation. A link N is read from the prim
// the probe's link map names, or else the store's link numbers.
func (s *stepRun) stateExpect(x *expState) error {
	e := x.e
	var link *Int
	var base Ident
	var st State
	se := &stateExp{}
	k, kind, _ := stateKeyOf(e)
	se.key, se.kind = k, kind
	switch {
	case e.Texture != nil:
		base, link, st = e.Texture.Name, e.Texture.Link, e.Texture.State
	case e.Offset != nil:
		base, link, st = e.Offset.Name, e.Offset.Link, e.Offset.State
	case e.Repeats != nil:
		base, link, st = e.Repeats.Name, e.Repeats.Link, e.Repeats.State
	case e.Rot != nil:
		base, link, st = e.Rot.Name, e.Rot.Link, e.Rot.State
	case e.Fullbright != nil:
		base, link, st = e.Fullbright.Name, e.Fullbright.Link, e.Fullbright.State
	case e.Glow != nil:
		base, link, st = e.Glow.Name, e.Glow.Link, e.Glow.State
	case e.Colour != nil:
		base, link, st = e.Colour.Name, e.Colour.Link, e.Colour.State
	case e.Alpha != nil:
		base, link, st = e.Alpha.Name, e.Alpha.Link, e.Alpha.State
	case e.Click != nil:
		base, link, st = e.Click.Name, e.Click.Link, e.Click.State
	case e.Position != nil:
		base, link, st = e.Position.Name, e.Position.Link, e.Position.State
	case e.Size != nil:
		base, link, st = e.Size.Name, e.Size.Link, e.Size.State
	}
	if err := s.linkKnown(base, link, k.name); err != nil {
		return err
	}
	if link != nil && !s.r.probed(s.r.lookup(base.Text)) {
		se.why = s.linkWhy(k.name)
		x.noteFn = se.why
	}
	if !st.Original && st.Kind != StateChanges {
		if err := s.stateWant(e, se); err != nil {
			return err
		}
	}
	se.word, se.orig = st.Kind, st.Original
	x.match = never
	x.eval = func(ctx context.Context) error { return s.evalState(ctx, x, se) }
	return nil
}

// lit3 is the literal of a position or size as float32, which is what the
// store holds.
func lit3(v *VecExp3) [3]float64 {
	return [3]float64{float64(float32(v.X.Value)), float64(float32(v.Y.Value)), float64(float32(v.Z.Value))}
}

// stateWant makes what a state expectation with a value compares with.
func (s *stepRun) stateWant(e *Expect, se *stateExp) error {
	var use *Capture
	switch {
	case e.Texture != nil:
		use = e.Texture.Use
	case e.Offset != nil:
		use = e.Offset.Use
	case e.Repeats != nil:
		use = e.Repeats.Use
	case e.Rot != nil:
		use = e.Rot.Use
	case e.Fullbright != nil:
		use = e.Fullbright.Use
	case e.Glow != nil:
		use = e.Glow.Use
	case e.Colour != nil:
		use = e.Colour.Use
	case e.Alpha != nil:
		use = e.Alpha.Use
	case e.Click != nil:
		use = e.Click.Use
	case e.Position != nil:
		use = e.Position.Use
	case e.Size != nil:
		use = e.Size.Use
	}
	switch {
	case e.Texture != nil && e.Texture.Any, e.Offset != nil && e.Offset.Any, e.Repeats != nil && e.Repeats.Any,
		e.Rot != nil && e.Rot.Any, e.Click != nil && e.Click.Any, e.Fullbright != nil && e.Fullbright.Any,
		e.Glow != nil && e.Glow.Any, e.Colour != nil && e.Colour.Any, e.Alpha != nil && e.Alpha.Any,
		e.Position != nil && e.Position.Any, e.Size != nil && e.Size.Any:
		se.any = true
	case use != nil:
		v, err := s.captureOrTuple(use, se.kind.capType())
		if err != nil {
			return err
		}
		if v.all != (se.key.face == allFace) {
			if v.all {
				return fmt.Errorf("%s holds every face of an object and only a face all expectation can use it", use)
			}
			return fmt.Errorf("%s holds one face and only a single face can use it; a face all expectation takes a capture of a face all", use)
		}
		se.want = *se.kind.want(v)
	case e.Texture != nil:
		id, err := msg.ParseUUID(e.Texture.ID)
		if err != nil {
			return fmt.Errorf("%q is not a texture id", e.Texture.ID)
		}
		se.want.tex = id
	case e.Offset != nil:
		se.want.off = [2]float64{e.Offset.S.Value, e.Offset.T.Value}
	case e.Repeats != nil:
		// As float32, which is what the face holds.
		se.want.rep = [2]float64{float64(float32(e.Repeats.S.Value)), float64(float32(e.Repeats.T.Value))}
	case e.Rot != nil:
		se.want.turns = e.Rot.Turns.Value
	case e.Click != nil:
		se.want.click = ClickBytes[e.Click.Action]
	case e.Position != nil:
		se.want.vec = lit3(e.Position)
	case e.Size != nil:
		se.want.vec = lit3(e.Size)
	case e.Fullbright != nil:
		se.want.bright = e.Fullbright.On
	case e.Glow != nil:
		se.want.glow = e.Glow.Value.Value
	case e.Colour != nil:
		se.want.col = [3]float64{e.Colour.R.Value, e.Colour.G.Value, e.Colour.B.Value}
	case e.Alpha != nil:
		se.want.alpha = e.Alpha.Value.Value
	}
	return nil
}

// evalState judges a state expectation against the readings: the baseline,
// the latest reading at or before the arm point or else the first after
// it, and every reading up to the expectation's limit. A reading that
// already matched at the arm point passes an `is`, and `becomes` has to
// see a reading that did not match before one that does.
// Why: doc/slate-runner.md#state-words
func (s *stepRun) evalState(ctx context.Context, x *expState, se *stateExp) error {
	if x.neg && se.final {
		return nil
	}
	w := s.t.watch
	now := time.Now()
	rs := w.reads[se.key]
	var base *reading
	for i := len(rs) - 1; i >= 0; i-- {
		if !rs[i].at.After(s.arm) {
			base = rs[i]
			break
		}
	}
	var after []*reading
	for _, r := range rs {
		if r.at.After(s.arm) && !r.at.After(x.limit) {
			after = append(after, r)
		}
	}
	late := false
	if base == nil && len(after) > 0 {
		base, after, late = after[0], after[1:], true
	}
	var seq []*reading
	if base != nil {
		seq = append([]*reading{base}, after...)
	}
	if late && se.word != StateIs && !se.noted {
		se.noted = true
		what := fmt.Sprintf("face %d", se.key.face)
		switch se.key.face {
		case clickFace:
			what = "click"
		case allFace:
			what = "face all"
		case primFace:
			what = "position"
			if se.kind == kSize {
				what = "size"
			}
		}
		if se.kind == kButton {
			what = "button"
		}
		ev := &event{kind: evReading, at: now, consumed: true,
			text: fmt.Sprintf("baseline for %s %s taken after the arm point", se.key.name, what)}
		s.r.log.add(ev)
		s.r.printEvent(ev)
	}

	want := &se.want
	if se.orig {
		want = w.original(se.key)
	}
	var hit *reading
	switch {
	case se.word == StateChanges:
		for _, r := range seq[min(1, len(seq)):] {
			if !se.kind.equal(base, r) {
				hit = r
				break
			}
		}
	case se.any:
		if len(seq) > 0 {
			hit = seq[0]
		}
	case want == nil:
	case se.word == StateIs:
		for _, r := range seq {
			if se.kind.matches(r, want) {
				hit = r
				break
			}
		}
	default: // becomes: a reading that is not X has to come first
		left := false
		for _, r := range seq {
			if se.kind.matches(r, want) {
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
		x.ev = &event{kind: evReading, at: hit.at, text: se.kind.show(se.key, hit)}
		if x.neg {
			x.forbidden = true
		} else {
			x.matched = true
			v := se.kind.value(hit)
			s.bindMatched(x, &v)
		}
	case x.neg && !now.Before(x.limit):
		// The window is over and nothing was read: the store never having
		// said is not proof of anything.
		se.final = true
		if len(seq) == 0 {
			x.note = "; no reading was taken, and a negative needs one"
			if se.why != nil {
				x.note += se.why()
			}
			s.why = "a negative texture, click or state needs a real reading, and none was taken"
			s.fail(1, "no reading")
		}
	case now.Before(x.limit):
		if b := s.r.lookup(se.key.name); b != nil {
			return w.redescribe(ctx, b.seen.ID)
		}
	}
	return nil
}

// ---------------------------------------------------------- the step hook

// stepObs is what a step keeps beside its expectations.
type stepObs struct {
	snap    map[msg.UUID]string // the inventory's item ids and names at the arm point
	desc    []*clickDescribe    // click describes started by a rez, open
	rez     *rezStep
	gives   map[*expState]*giveRun
	owners  map[string]msg.UUID
	noOwner map[string]bool
}

// observe is everything a step looks at besides the heard events: the
// object poll, the state readings and what they decide, the rez roots, the
// gives, the click describes. It is called at each turn of Matching and of
// Hold, and it may fail the step.
func (s *stepRun) observe(ctx context.Context) error {
	w := s.t.watch
	if err := w.poll(ctx, false); err != nil {
		return err
	}
	for _, x := range s.exps {
		if x.eval == nil || x.matched || x.forbidden {
			continue
		}
		if err := x.eval(ctx); err != nil {
			return err
		}
		if s.state == stFailed {
			return nil
		}
	}
	s.badScan()
	if s.state == stFailed {
		return nil
	}
	if s.obs.rez != nil {
		if err := s.rezScan(ctx); err != nil || s.state == stFailed {
			return err
		}
	}
	return s.describeScan(ctx)
}

// ------------------------------------------------------ the click describe

// clickDescribe is a prim bound by a rez that a later click expectation
// names: asked for a full update, the one that carries the click byte,
// until the byte is known or its own clock ends.
// Why: doc/slate-runner.md#rules
type clickDescribe struct {
	b        *binding
	deadline time.Time
	last     time.Time
}

// clickSentence is what a click byte that never came is reported as.
func clickSentence(inWorld string) string {
	return fmt.Sprintf("click action was not on any update of %q; this slate requires the slgod that stores click and click_known", inWorld)
}

// clickNamed reports whether a step after the n-th of the test has a click
// expectation on the name.
func (t *testRun) clickNamed(name string, after int) bool {
	for i, x := range t.et.Steps {
		if i < after {
			continue
		}
		for j := range x.Step.Expect {
			if c := x.Step.Expect[j].Click; c != nil && c.Name.Text == name {
				return true
			}
		}
	}
	return false
}

// startDescribe begins the describe of a name just bound, if a later step
// reads its click byte and the store does not know it yet.
func (s *stepRun) startDescribe(ctx context.Context, b *binding) {
	if !s.t.clickNamed(b.name, s.n) {
		return
	}
	w := s.t.watch
	if o := byID(w.all, b.seen.ID); o != nil && o.ClickKnown {
		return
	}
	d := &clickDescribe{b: b, deadline: time.Now().Add(s.r.cfg.describeFor())}
	s.obs.desc = append(s.obs.desc, d)
	s.askClick(ctx, d)
}

func (s *stepRun) askClick(ctx context.Context, d *clickDescribe) {
	d.last = time.Now()
	if o := byID(s.t.watch.all, d.b.seen.ID); o != nil && o.Local != 0 {
		s.r.requestObject(ctx, o.Local)
	}
}

// describeScan stops the describes whose byte is known, asks again for the
// ones still open on the cadence of a texture read, and fails the step,
// exit 3, for the first whose clock ended. The failure is printed here
// with its sentence and the block is not.
func (s *stepRun) describeScan(ctx context.Context) error {
	now := time.Now()
	open := s.obs.desc[:0]
	for _, d := range s.obs.desc {
		if o := byID(s.t.watch.all, d.b.seen.ID); o != nil && o.ClickKnown {
			continue
		}
		if !now.Before(d.deadline) {
			sentence := clickSentence(d.b.seen.Name)
			s.r.printf("slate: setup: %s", sentence)
			s.quiet = true
			s.obs.desc = nil
			s.fail(3, "%s", sentence)
			return nil
		}
		if now.Sub(d.last) >= s.r.cfg.redescribeEvery() {
			s.askClick(ctx, d)
		}
		open = append(open, d)
	}
	s.obs.desc = open
	return nil
}

// describeWake is when the soonest open describe ends.
func (s *stepRun) describeWake() time.Time {
	var wake time.Time
	for _, d := range s.obs.desc {
		if wake.IsZero() || d.deadline.Before(wake) {
			wake = d.deadline
		}
	}
	return wake
}

// setupClick describes every header binding that a click expectation of a
// selected test names, at setup, on the 30 s of the describe: the same
// request and the same wait as a rez's, and the same failure, exit 3.
// Why: doc/slate-runner.md#rules
func (r *runner) setupClick(ctx context.Context) error {
	tests, err := r.s.Expand()
	if err != nil {
		return err
	}
	named := map[string]bool{}
	for _, et := range tests {
		if r.opt.Run != nil && !r.opt.Run.MatchString(et.Test.Name) {
			continue
		}
		for _, x := range et.Steps {
			for i := range x.Step.Expect {
				if c := x.Step.Expect[i].Click; c != nil {
					named[keyName(c.Name, c.Link)] = true
				}
			}
		}
	}
	// A click on link N reads that prim, which the probe's hellos bound,
	// so this runs after the probe step. With no probe the prim is found at
	// each poll, and is described when its expectation waits (redescribe).
	keys := make([]string, 0, len(named))
	for k := range named {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b := r.bind[k]
		if b == nil {
			continue
		}
		if err := r.describeClick(ctx, b); err != nil {
			return err
		}
	}
	return nil
}

func (r *runner) describeClick(ctx context.Context, b *binding) error {
	deadline := time.Now().Add(r.cfg.describeFor())
	var last time.Time
	for {
		found, err := r.sess.Backend().Objects(ctx, "", b.seen.ID.String())
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &setupError{fmt.Sprintf("reading %q: %v", b.seen.Name, err)}
		}
		if len(found) > 0 && found[0].ClickKnown {
			return nil
		}
		if !time.Now().Before(deadline) {
			return &setupError{clickSentence(b.seen.Name)}
		}
		if len(found) > 0 && found[0].Local != 0 && time.Since(last) >= r.cfg.redescribeEvery() {
			last = time.Now()
			if err := r.requestObject(ctx, found[0].Local); err != nil && ctx.Err() != nil {
				return ctx.Err()
			}
		}
		if err := r.sess.Settle(ctx, r.cfg.poll); err != nil {
			return err
		}
	}
}
