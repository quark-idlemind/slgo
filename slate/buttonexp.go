package slate

// expect button: the count of the tuples a button search finds on a prim,
// read from the faces' texture entries and kept as a reading like any
// other state. The search is the touch's (button.go); what is new is when it
// runs: when a searched face's entry is not one already searched.
// Why: doc/slate-runner.md#button-observations

import (
	"context"
	"errors"
	"fmt"
	"image"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/imgfind"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// btnFaceBase is the face of the key of the first button reading; the
// i-th is btnFaceBase-i, below click and all.
const btnFaceBase = -3

// btnWatch is one button reading: the prim, the parts, and the face.
type btnWatch struct {
	name  string // the binding's key
	label string // "button hud text "Close" box", as the transcript says it
	parts []Part
	face  int    // -1: every face
	why   string // what the last poll refused, "" when it read
}

// picKey is what a face's picture is a function of: the entry of the face
// and the texture it names, with the animation block, which can refuse it.
type picKey struct {
	idx  int
	face sl.Face
	anim string
}

type picEntry struct {
	pic     image.Image
	err     error
	retryAt time.Time // for an error that may pass
}

type resKey struct {
	pic   picKey
	parts string
}

type resEntry struct {
	centres []imgfind.Point
	err     error
	retryAt time.Time
}

// btnState is what the watcher keeps for button readings.
type btnState struct {
	by    map[readKey]*btnWatch
	ids   map[string]readKey
	pics  map[picKey]*picEntry
	res   map[resKey]*resEntry
	count map[msg.UUID]countEntry
}

type countEntry struct {
	entry string
	n     int
}

// cacheMax bounds each cache, which a texture that animates by changing its
// entry would otherwise grow without end.
const cacheMax = 64

// addButton registers a button reading, once for its prim, parts and face,
// and says its key.
func (w *watcher) addButton(name, label string, parts []Part, face int) readKey {
	b := &w.btn
	if b.by == nil {
		b.by, b.ids = map[readKey]*btnWatch{}, map[string]readKey{}
		b.pics, b.res = map[picKey]*picEntry{}, map[resKey]*resEntry{}
		b.count = map[msg.UUID]countEntry{}
	}
	id := fmt.Sprintf("%s\x00%d\x00%s", name, face, partsKey(parts))
	if k, ok := b.ids[id]; ok {
		return k
	}
	k := readKey{name, btnFaceBase - len(b.by), kButton}
	b.by[k], b.ids[id] = &btnWatch{name: name, label: label, parts: parts, face: face}, k
	w.keys = append(w.keys, k)
	w.on = true
	return k
}

func partsKey(parts []Part) string {
	s := make([]string, len(parts))
	for i, p := range parts {
		s[i] = partText(p)
	}
	return strings.Join(s, " ")
}

// buttonLabel is the transcript's words for a button reading.
func buttonLabel(sc *Script, x *ButtonExp) string {
	obj := x.Name.Text
	if x.Link != nil {
		obj += fmt.Sprintf(" link %d", x.Link.Value)
	}
	return "button " + obj + " " + partsSource(sc, x.Button.Parts)
}

func buttonFace(b *Button) int {
	if b.Face != nil {
		return int(b.Face.Value)
	}
	return -1
}

// registerButtons registers the readings of the test's button expectations
// whose parts are literal, so that they are read from the test's start. One
// with a capture is registered when its step is made.
func (w *watcher) registerButton(e *Expect) {
	x := e.Button
	for _, p := range x.Button.Parts {
		if p.Capture != nil {
			return
		}
	}
	w.addButton(keyName(x.Name, x.Link), buttonLabel(w.t.r.s, x), x.Button.Parts, buttonFace(x.Button))
}

// buttonExpect makes expect button: a state expectation over the count.
func (s *stepRun) buttonExpect(x *expState) error {
	bx := x.e.Button
	name := keyName(bx.Name, bx.Link)
	if err := s.linkKnown(bx.Name, bx.Link, name); err != nil {
		return err
	}
	bt, err := s.buttonUses(bx.Button)
	if err != nil {
		return err
	}
	w := s.t.watch
	k := w.addButton(name, buttonLabel(s.r.s, bx), bt.Parts, buttonFace(bt))
	bw := w.btn.by[k]
	se := &stateExp{kind: kButton, key: k, word: bx.State.Kind, orig: bx.State.Original}
	if !se.orig && se.word != StateChanges {
		switch bx.Val {
		case ButtonShown:
			se.want = reading{count: 1, atLeast: true}
		case ButtonGone:
			se.want = reading{}
		default:
			se.want = reading{count: int(bx.Count.Value)}
		}
	}
	n := s.n
	se.why = func() string {
		if bw.why == "" {
			return ""
		}
		return fmt.Sprintf("; slate: step %d: %s", n, bw.why)
	}
	x.match = never
	x.noteFn = se.why
	x.eval = func(ctx context.Context) error { return s.evalState(ctx, x, se) }
	return nil
}

// readButton is the reading of a button key at a poll, or nil when there is
// none: a refused face, a failed fetch or a finder error is never a count of
// zero. The reason is kept for the step's failure.
func (w *watcher) readButton(ctx context.Context, k readKey, o *sl.Seen, at time.Time) *reading {
	bw := w.btn.by[k]
	b := w.t.r.lookup(k.name)
	if b == nil {
		return nil
	}
	name := b.seen.Name
	fail := func(format string, args ...any) *reading {
		bw.why = fmt.Sprintf(format, args...)
		return nil
	}
	if sent := partsRefusal(bw.parts); sent != "" {
		return fail("%s", sent)
	}
	n, ok := o.FaceCount()
	if !ok || n <= 0 {
		var err error
		if n, err = w.countFaces(ctx, o); err != nil {
			return fail("reading the faces: %v", err)
		}
	}
	if bw.face >= n {
		return fail("%q has no face %d; it has %d", name, bw.face, n)
	}
	faces, err := o.Faces(n)
	if err != nil {
		return fail("the faces of %q are not known: %v", name, err)
	}
	total := 0
	var list []string
	for i, f := range faces {
		pk := picKey{i, f, string(o.TextureAnim)}
		pe := w.picture(ctx, o, pk)
		switch {
		case pe.err == nil:
		case errors.Is(pe.err, sl.ErrNoTexture):
			continue
		case faceRefusal(pe.err, i, name) != "":
			return fail("%s", faceRefusal(pe.err, i, name))
		default:
			return fail("reading face %d of %q: %v", i, name, pe.err)
		}
		if bw.face >= 0 && i != bw.face {
			continue
		}
		re := w.search(pk, pe, bw.parts)
		if re.err != nil {
			return fail("the finder failed: %v", re.err)
		}
		if len(re.centres) > 0 {
			total += len(re.centres)
			list = append(list, fmt.Sprint(i))
		}
	}
	bw.why = ""
	faceList := "none"
	if len(list) > 0 {
		faceList = strings.Join(list, ", ")
	}
	return &reading{at: at, count: total, label: fmt.Sprintf("%s %d (faces %s)", bw.label, total, faceList)}
}

// countFaces is the face count of a prim whose shape does not say, from
// the session, asked again only when the entry changes.
func (w *watcher) countFaces(ctx context.Context, o *sl.Seen) (int, error) {
	c := w.btn.count[o.ID]
	if c.n > 0 && c.entry == string(o.TextureEntry) {
		return c.n, nil
	}
	faces, err := w.t.r.sess.Faces(ctx, &o.Object)
	if err != nil {
		return 0, err
	}
	w.btn.count[o.ID] = countEntry{string(o.TextureEntry), len(faces)}
	return len(faces), nil
}

// picture is the picture of a face, fetched when its entry was not seen
// before. A refusal is final for the entry; a fetch that failed is tried
// again after the cadence of a texture read.
func (w *watcher) picture(ctx context.Context, o *sl.Seen, pk picKey) *picEntry {
	b := &w.btn
	pe := b.pics[pk]
	if pe != nil && (pe.err == nil || errors.Is(pe.err, sl.ErrNoTexture) || faceRefusal(pe.err, 0, "") != "" ||
		time.Now().Before(pe.retryAt)) {
		return pe
	}
	if len(b.pics) >= cacheMax {
		b.pics = map[picKey]*picEntry{}
	}
	pic, err := w.t.r.sess.FacePicture(ctx, &o.Object, pk.idx)
	pe = &picEntry{err: err}
	if err == nil {
		pe.pic = pic
	} else {
		pe.retryAt = time.Now().Add(w.t.r.cfg.redescribeEvery())
	}
	b.pics[pk] = pe
	return pe
}

// search is the finder's answer for one picture and the parts, cached by
// the picture's key: the same entry and texture give the same picture.
func (w *watcher) search(pk picKey, pe *picEntry, parts []Part) *resEntry {
	b := &w.btn
	rk := resKey{pk, partsKey(parts)}
	re := b.res[rk]
	if re != nil && (re.err == nil || time.Now().Before(re.retryAt)) {
		return re
	}
	if len(b.res) >= cacheMax {
		b.res = map[resKey]*resEntry{}
	}
	centres, _, err := searchPicture(w.t.r.cfg.finder(), pe.pic, parts)
	re = &resEntry{centres: centres, err: err, retryAt: time.Now().Add(w.t.r.cfg.redescribeEvery())}
	b.res[rk] = re
	return re
}
