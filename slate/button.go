package slate

// touch OBJ button: a picture match on every face of the prim, then one
// click at the match's centre.
// Why: doc/slate-runner.md#buttons

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/imgfind"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// The two sentences for a part the finder cannot match, fixed by
// doc/slate-language.md#buttons.
const (
	imageSentence = "button part image is allowed, and the finder does not match one; it matches text, a pattern, a drawing (circle, arrow, and the four directions), and an outlined box"
	ovalSentence  = "button part oval is allowed, and the finder does not match one; a round ring is circle, and a wide outlined control is box"
)

// facePic is one textured face and the picture it shows.
type facePic struct {
	face int
	pic  image.Image
}

// buttonHit is what a button search found: the face to click and where,
// in pixels of that face's picture.
type buttonHit struct {
	face int
	pic  image.Image
	at   imgfind.Point
}

// buttonStimulus searches the faces of the bound prim in prepare, so that
// every refusal comes before anything is sent, and sends one touch.
func (s *stepRun) buttonStimulus(t *Touch, b *binding) (*stimulus, error) {
	bt, err := s.buttonUses(t.Button)
	if err != nil {
		return nil, err
	}
	var hit buttonHit
	skip := false
	return &stimulus{
		prepare: func(ctx context.Context) error {
			if t.Guard != nil {
				h, none, err := s.guardedButton(ctx, b, bt)
				hit, skip = h, none
				return err
			}
			h, err := s.searchButton(ctx, b, bt)
			hit = h
			return err
		},
		send: func(ctx context.Context, _ time.Duration) (string, error) {
			if skip {
				return "not sent: no button to touch", nil
			}
			w, h := hit.pic.Bounds().Dx(), hit.pic.Bounds().Dy()
			st := msg.Vector3{
				X: float32((float64(hit.at.X) + 0.5) / float64(w)),
				Y: float32(1 - (float64(hit.at.Y)+0.5)/float64(h)),
			}
			if err := s.r.actor(t.AsAvatar).Touch(ctx, &b.seen.Object, sl.Touch{Face: hit.face, ST: st}); err != nil {
				return "", err
			}
			return fmt.Sprintf("touched %s face %d button at %d,%d (st %v %v)%s", b.name, hit.face, hit.at.X, hit.at.Y, st.X, st.Y, asText(t.AsAvatar)), nil
		},
	}, nil
}

// buttonUses is the button with each text $x part made the text it holds.
// A literal is checked non-empty by Check; a capture is checked here, so
// the step fails before the touch. The script's button is not changed.
func (s *stepRun) buttonUses(bt *Button) (*Button, error) {
	has := false
	for _, p := range bt.Parts {
		has = has || p.Capture != nil
	}
	if !has {
		return bt, nil
	}
	out := *bt
	out.Parts = make([]Part, len(bt.Parts))
	for i, p := range bt.Parts {
		if p.Capture != nil {
			v, err := s.capture(p.Capture, CapText)
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(v.text) == "" {
				return nil, s.sentence("%s is %q and a button part needs text", p.Capture, v.text)
			}
			p.Text, p.Capture = v.text, nil
		}
		out.Parts[i] = p
	}
	return &out, nil
}

// sentence is a refusal printed as a line of its own.
func (s *stepRun) sentence(format string, args ...any) error {
	return &stepSentence{fmt.Sprintf("slate: step %d: ", s.n) + fmt.Sprintf(format, args...)}
}

// partsRefusal is the fixed sentence for a part the finder cannot match,
// or "".
func partsRefusal(parts []Part) string {
	for _, p := range parts {
		switch p.Kind {
		case PartImage:
			return imageSentence
		case PartOval:
			return ovalSentence
		}
	}
	return ""
}

// faceRefusal is the sentence for a face whose picture is refused, shared
// by a touch and by a button reading, or "" for any other error.
func faceRefusal(err error, face int, name string) string {
	switch {
	case errors.Is(err, sl.ErrPlanarFace):
		return fmt.Sprintf("face %d of %q is planar, so its offset, repeats and rotation are not the picture on it", face, name)
	case errors.Is(err, sl.ErrAnimatedFace):
		return fmt.Sprintf("face %d of %q has a texture animation, so one still picture is not what it shows", face, name)
	}
	return ""
}

// finder is imgfind.Find, which a test replaces to count its calls.
type finder func(image.Image, ...*imgfind.Request) ([]*imgfind.Item, error)

func (c runCfg) finder() finder {
	if c.find != nil {
		return c.find
	}
	return imgfind.Find
}

// searchPicture is the one search both a touch and a button reading make:
// the centre of every tuple of the parts on one picture, and how many items
// each part found.
func searchPicture(find finder, pic image.Image, parts []Part) ([]imgfind.Point, []int, error) {
	reqs := make([]*imgfind.Request, len(parts))
	for i, p := range parts {
		reqs[i] = requestOf(p)
	}
	items, err := find(pic, reqs...)
	if err != nil {
		return nil, nil, err
	}
	found := make([]int, len(reqs))
	groups := make([][]*imgfind.Item, len(reqs))
	for _, it := range items {
		for i, r := range reqs {
			if it.Request == r {
				groups[i] = append(groups[i], it)
				found[i]++
			}
		}
	}
	return intersections(groups), found, nil
}

// searchTuples is a button step's search before the touch: every tuple on
// the faces, in face order, the pictures searched and how many items each
// part found.
func (s *stepRun) searchTuples(ctx context.Context, b *binding, bt *Button) ([]buttonHit, []int, []facePic, error) {
	if sent := partsRefusal(bt.Parts); sent != "" {
		return nil, nil, nil, s.sentence("%s", sent)
	}
	o, name := &b.seen.Object, b.seen.Name
	faces, err := s.r.sess.Faces(ctx, o)
	if err != nil {
		return nil, nil, nil, s.buttonError(ctx, err)
	}
	// Every face is asked before any is searched: one planar or animated
	// face refuses the whole prim, whichever face the button names.
	var pics []facePic
	var refusal error
	for i := range faces {
		pic, err := s.r.sess.FacePicture(ctx, o, i)
		switch {
		case errors.Is(err, sl.ErrNoTexture):
		case faceRefusal(err, i, name) != "":
			if refusal == nil {
				refusal = s.sentence("%s", faceRefusal(err, i, name))
			}
		case err != nil:
			return nil, nil, nil, s.buttonError(ctx, err)
		default:
			pics = append(pics, facePic{i, pic})
		}
	}
	if refusal != nil {
		return nil, nil, nil, refusal
	}
	want := -1
	if bt.Face != nil {
		want = int(bt.Face.Value)
		if want >= len(faces) {
			return nil, nil, nil, s.sentence("%q has no face %d; it has %d", name, want, len(faces))
		}
	}
	var tuples []buttonHit
	found := make([]int, len(bt.Parts)) // items found for each part, on the searched faces
	for _, fp := range pics {
		if want >= 0 && fp.face != want {
			continue
		}
		centres, n, err := searchPicture(s.r.cfg.finder(), fp.pic, bt.Parts)
		if err != nil {
			return nil, nil, nil, s.buttonFailed(pics, "the finder failed: %v", err)
		}
		for i := range found {
			found[i] += n[i]
		}
		for _, at := range centres {
			tuples = append(tuples, buttonHit{face: fp.face, pic: fp.pic, at: at})
		}
	}
	return tuples, found, pics, nil
}

// guardedButton is the search of touch ... if shown: no tuple is no
// button, which prints its line and sends nothing. Anything the search
// refuses fails the step, as a touch's does.
func (s *stepRun) guardedButton(ctx context.Context, b *binding, bt *Button) (buttonHit, bool, error) {
	tuples, _, pics, err := s.searchTuples(ctx, b, bt)
	if err != nil {
		return buttonHit{}, false, err
	}
	switch len(tuples) {
	case 0:
		s.r.printf("slate: step %d: no %s button on %q; the touch was not sent", s.n, s.partsAsWritten(bt), b.seen.Name)
		return buttonHit{}, true, nil
	case 1:
		return tuples[0], false, nil
	}
	return buttonHit{}, false, s.buttonFailed(pics, "button matches %d times on %q%s; write a number before the parts to choose one", len(tuples), b.seen.Name, listHits(tuples))
}

// partsAsWritten is the parts of a button as the file has them, on one
// line.
func (s *stepRun) partsAsWritten(bt *Button) string {
	return partsSource(s.r.s, bt.Parts)
}

func partsSource(sc *Script, parts []Part) string {
	if len(parts) == 0 {
		return ""
	}
	sp := cover(parts[0].Span, parts[len(parts)-1].Span)
	return strings.Join(strings.Fields(sc.Source(sp)), " ")
}

// searchButton is the whole of a button step before the touch.
func (s *stepRun) searchButton(ctx context.Context, b *binding, bt *Button) (buttonHit, error) {
	var none buttonHit
	tuples, found, pics, err := s.searchTuples(ctx, b, bt)
	if err != nil {
		return none, err
	}
	name := b.seen.Name
	switch {
	case bt.Nth != nil:
		n := int(bt.Nth.Value)
		if n > len(tuples) {
			return none, s.buttonFailed(pics, "button %d: the prim %q has %d matches%s", n, name, len(tuples), listHits(tuples))
		}
		return tuples[n-1], nil
	case len(tuples) == 1:
		return tuples[0], nil
	case len(tuples) > 1:
		return none, s.buttonFailed(pics, "button matches %d times on %q%s; write a number before the parts to choose one", len(tuples), name, listHits(tuples))
	}
	return none, s.buttonFailed(pics, "%s", missing(bt.Parts, found, len(pics) > 0, name))
}

// requestOf is a part's finder request.
func requestOf(p Part) *imgfind.Request {
	switch p.Kind {
	case PartText:
		return imgfind.Text(p.Text)
	case PartPattern:
		return imgfind.Pattern(p.Text)
	case PartSymbol:
		return imgfind.Drawing(p.Text)
	case PartCircle:
		return imgfind.Drawing("circle")
	}
	return imgfind.Box()
}

// partText is a part as the script writes it.
func partText(p Part) string {
	switch p.Kind {
	case PartText:
		return fmt.Sprintf("text %q", p.Text)
	case PartPattern:
		return fmt.Sprintf("pattern %q", p.Text)
	case PartSymbol:
		return fmt.Sprintf("symbol %q", p.Text)
	case PartCircle:
		return "circle"
	}
	return "box"
}

// missing says why no tuple was found: the first part nothing matched, or
// that every part matched and no items overlap.
func missing(parts []Part, found []int, textured bool, name string) string {
	if !textured {
		return fmt.Sprintf("no face of %q has a texture, so there is no picture to search", name)
	}
	for i, p := range parts {
		if found[i] == 0 {
			return fmt.Sprintf("button part %s matched nothing on %q", partText(p), name)
		}
	}
	return fmt.Sprintf("every button part matched on %q, and no items of all the parts overlap", name)
}

// listHits is each match's face and centre.
func listHits(hits []buttonHit) string {
	var w strings.Builder
	for i, h := range hits {
		sep := ";"
		if i == 0 {
			sep = ":"
		}
		fmt.Fprintf(&w, "%s face %d at %d,%d", sep, h.face, h.at.X, h.at.Y)
	}
	return w.String()
}

// intersections is the centre of every tuple, one item from each group,
// whose rectangles share an area: the first group's items in the outer
// loop and the later groups' inside it, each in finder order.
func intersections(groups [][]*imgfind.Item) []imgfind.Point {
	var out []imgfind.Point
	var walk func(i int, x0, y0, x1, y1 int)
	walk = func(i int, x0, y0, x1, y1 int) {
		if i == len(groups) {
			out = append(out, imgfind.Point{X: x0 + (x1-x0)/2, Y: y0 + (y1-y0)/2})
			return
		}
		for _, it := range groups[i] {
			ax, ay := it.Location.X, it.Location.Y
			bx, by := ax+it.Width, ay+it.Height
			if i > 0 {
				ax, ay, bx, by = max(ax, x0), max(ay, y0), min(bx, x1), min(by, y1)
			}
			if bx-ax > 0 && by-ay > 0 {
				walk(i+1, ax, ay, bx, by)
			}
		}
	}
	if len(groups) > 0 {
		walk(0, 0, 0, 0, 0)
	}
	return out
}

// buttonError is an error from the session while the faces were read.
func (s *stepRun) buttonError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return s.sentence("reading the faces: %v", err)
}

// buttonFailed fails the step after a search, and leaves the pictures it
// searched where they can be looked at, with their paths in the transcript.
// A refusal that comes before any search keeps no file.
func (s *stepRun) buttonFailed(pics []facePic, format string, args ...any) error {
	if len(pics) > 0 {
		s.keepPictures(pics)
	}
	return s.sentence(format, args...)
}

// keepPictures writes each face's picture as N.png under a fresh
// directory in the system temp directory, and prints the paths.
func (s *stepRun) keepPictures(pics []facePic) {
	dir, err := os.MkdirTemp("", "slate-button-")
	if err != nil {
		s.r.printf("slate: step %d: face pictures not written: %v", s.n, err)
		return
	}
	for _, p := range pics {
		path := filepath.Join(dir, fmt.Sprintf("%d.png", p.face))
		if err := writePNG(path, p.pic); err != nil {
			s.r.printf("slate: step %d: face %d picture not written: %v", s.n, p.face, err)
			continue
		}
		s.r.printf("slate: step %d: face %d picture: %s", s.n, p.face, path)
	}
}

func writePNG(path string, m image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, m); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
