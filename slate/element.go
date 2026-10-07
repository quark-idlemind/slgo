package slate

// touch OBJ [link N] [face F] element "NAME": the record says where the
// area is on the texture; the face's repeats, offset and rotation say
// where that is on the face; one click goes there.
// Why: doc/slate-runner.md#stimuli

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// elementHit is an area that shows on a face and where, in the face's own
// coordinates.
type elementHit struct {
	face int
	s, t float64
}

// elementStimulus finds the area in prepare, so that every refusal comes
// before anything is sent, and sends one touch.
func (s *stepRun) elementStimulus(t *Touch, b *binding) (*stimulus, error) {
	name := t.Element.Name
	var hit elementHit
	return &stimulus{
		prepare: func(ctx context.Context) error {
			h, err := s.findElement(ctx, b, t, name)
			hit = h
			return err
		},
		send: func(ctx context.Context, _ time.Duration) (string, error) {
			st := msg.Vector3{X: float32(hit.s), Y: float32(hit.t)}
			if err := s.r.actor(t.AsAvatar).Touch(ctx, &b.seen.Object, sl.Touch{Face: hit.face, ST: st}); err != nil {
				return "", err
			}
			return fmt.Sprintf("touched %s face %d element %q at %v %v%s", b.name, hit.face, name, st.X, st.Y, asText(t.AsAvatar)), nil
		},
	}, nil
}

// windowOf says what a face does to its texture, for a sentence.
func windowOf(f sl.Face) string {
	offS, offT := f.OffsetsF()
	return fmt.Sprintf("repeats %g by %g, offset %.4g %.4g, rotation %.4g degrees",
		f.ScaleS, f.ScaleT, offS, offT, float64(f.RotationRad())*180/math.Pi)
}

// maxRepeat is the largest repeat in size a face may have for an element:
// the face then shows the texture or a part of it, once.
const maxRepeat = 1 + 1e-6

// findElement is the whole of an element step before the touch: the faces
// of the prim whose texture has a record, the refusals, the areas of that
// name whose centres are inside the face, and the one to click.
func (s *stepRun) findElement(ctx context.Context, b *binding, t *Touch, name string) (elementHit, error) {
	var none elementHit
	o, oname := &b.seen.Object, b.seen.Name
	faces, refused, err := s.r.sess.FaceRefusals(ctx, o)
	if err != nil {
		return none, s.buttonError(ctx, err)
	}
	var cands []int
	for i, f := range faces {
		if t.Face != nil && int(t.Face.Value) != i {
			continue
		}
		if len(s.r.elems.areas[f.Texture]) > 0 && f.Texture != (msg.UUID{}) {
			cands = append(cands, i)
		}
	}
	if t.Face != nil {
		face := int(t.Face.Value)
		if face >= len(faces) {
			return none, s.sentence("%q has no face %d; it has %d", oname, face, len(faces))
		}
		if len(cands) == 0 {
			return none, s.sentence("face %d of %q does not show a texture with an element record", face, oname)
		}
	} else if len(cands) == 0 {
		return none, s.sentence("no face of %q shows a texture with an element record", oname)
	}
	for _, i := range cands {
		if err := refused[i]; err != nil {
			if sent := faceRefusal(err, i, oname); sent != "" {
				return none, s.sentence("%s", sent)
			}
			return none, s.sentence("%v", err)
		}
		if f := faces[i]; math.Abs(float64(f.ScaleS)) > maxRepeat || math.Abs(float64(f.ScaleT)) > maxRepeat {
			return none, s.sentence("face %d of %q repeats its texture %g by %g, and an element is found in the part of the texture a face shows once; a repeat larger than 1 in size shows it more than once, so set the repeats to 1 or less, or touch with at S T", i, oname, f.ScaleS, f.ScaleT)
		}
	}
	var hits []elementHit
	var listed, recorded []int // faces whose record has the name, and every face with a record
	for _, i := range cands {
		f := faces[i]
		found := false
		for _, a := range s.r.elems.areas[f.Texture] {
			if a.name != name {
				continue
			}
			found = true
			if st, ok := visibleAt(f, a); ok {
				hits = append(hits, elementHit{i, st[0], st[1]})
			}
		}
		if found {
			listed = append(listed, i)
		}
		recorded = append(recorded, i)
	}
	switch {
	case len(hits) == 1:
		return hits[0], nil
	case len(hits) > 1:
		return none, s.sentence("%s", ambiguous(oname, name, hits))
	case len(listed) == 0:
		// Each texture once: one texture is often on several faces.
		var ids []string
		seen := map[string]bool{}
		for _, i := range recorded {
			if id := faces[i].Texture.String(); !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		texture := "the texture"
		if len(ids) > 1 {
			texture = "the textures"
		}
		return none, s.sentence("element %q is not in the record of %s, %s shown on %q%s", name, strings.Join(ids, " or "), texture, oname, onFaces(recorded))
	}
	var how []string
	for _, i := range listed {
		how = append(how, fmt.Sprintf("face %d: %s", i, windowOf(faces[i])))
	}
	return none, s.sentence("element %q is not showing on %q%s (%s)", name, oname, onFaces(listed), strings.Join(how, "; "))
}

// onFaces is " face 2" or " faces 1 and 3" after a prim's name.
func onFaces(faces []int) string {
	if len(faces) == 1 {
		return fmt.Sprintf(" face %d", faces[0])
	}
	var w []string
	for _, i := range faces {
		w = append(w, fmt.Sprint(i))
	}
	return " faces " + strings.Join(w, ", ")
}

// ambiguous is the sentence for an area that shows more than once.
func ambiguous(prim, name string, hits []elementHit) string {
	var faces []int
	seen := map[int]bool{}
	for _, h := range hits {
		if !seen[h.face] {
			seen[h.face] = true
			faces = append(faces, h.face)
		}
	}
	if len(faces) > 1 {
		return fmt.Sprintf("element %q is showing in %d places on %q,%s; write face F to choose one", name, len(hits), prim, onFaces(faces))
	}
	return fmt.Sprintf("element %q is showing in %d places on %q face %d; the record has the name more than once inside the face's window", name, len(hits), prim, faces[0])
}

// visibleAt is where an area's centre is on a face, when it is on the
// face. The record's U and V are mapped to the face's S and T by
// Face.TextureToSurface, the viewer's mapping undone as a touch's is. A
// texture repeats beyond 0 to 1, so a window that hangs over an edge shows
// the centre one texture over too, and the shifts -1, 0 and 1 are tried.
func visibleAt(f sl.Face, a area) ([2]float64, bool) {
	cu, cv := a.centre()
	for _, du := range []float64{0, -1, 1} {
		for _, dv := range []float64{0, -1, 1} {
			st, err := f.TextureToSurface(msg.Vector3{X: float32(cu + du), Y: float32(cv + dv)})
			if err != nil {
				return [2]float64{}, false
			}
			x, y := float64(st.X), float64(st.Y)
			if x >= 0 && x <= 1 && y >= 0 && y <= 1 {
				return [2]float64{x, y}, true
			}
		}
	}
	return [2]float64{}, false
}
