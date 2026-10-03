package slate

// touch OBJ showing UUID: find the one face of the binding's linkset that
// shows a texture and touch it.
// Why: doc/slate-language.md#stimuli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// shownFace is one (prim, face) pair that shows the texture.
type shownFace struct {
	prim *sl.Seen
	face int
}

// showingStimulus is a touch on the face that shows a texture. The prim
// and the face are found in prepare, from readings taken after the region
// was asked to describe every member again, and only then is anything
// sent.
func (s *stepRun) showingStimulus(t *Touch) (*stimulus, error) {
	b, err := s.bound(t.Name)
	if err != nil {
		return nil, err
	}
	sh := t.Showing
	var tex msg.UUID
	if sh.Use != nil {
		v, err := s.capture(sh.Use, CapUUID)
		if err != nil {
			return nil, err
		}
		tex = v.id
	} else if tex, err = msg.ParseUUID(sh.ID); err != nil {
		return nil, err
	}
	var hit shownFace
	return &stimulus{
		prepare: func(ctx context.Context) error {
			var err error
			hit, err = s.findShowing(ctx, b, tex)
			return err
		},
		send: func(ctx context.Context, _ time.Duration) (string, error) {
			touch := sl.Touch{Face: hit.face}
			if sh.At != nil {
				touch.ST = stOf(*sh.At)
			}
			if err := s.r.sess.Touch(ctx, &hit.prim.Object, touch); err != nil {
				return "", err
			}
			return fmt.Sprintf("touched %q face %d showing %s", hit.prim.Name, hit.face, tex), nil
		},
	}, nil
}

// findShowing asks the region to describe every prim of the binding's
// linkset again, waits one settle, reads the store and returns the one
// (prim, face) that shows the texture. Zero or several is the step's
// refusal.
func (s *stepRun) findShowing(ctx context.Context, b *binding, tex msg.UUID) (shownFace, error) {
	r := s.r
	for _, m := range b.prims() {
		if m.Local == 0 {
			continue
		}
		if err := r.requestObject(ctx, m.Local); err != nil {
			if ctx.Err() != nil {
				return shownFace{}, ctx.Err()
			}
			return shownFace{}, err
		}
	}
	if err := r.wait(ctx, time.Now().Add(r.cfg.settleFor())); err != nil {
		return shownFace{}, err
	}
	all, err := r.sess.Backend().Objects(ctx, "", "")
	if err != nil {
		if ctx.Err() != nil {
			return shownFace{}, ctx.Err()
		}
		return shownFace{}, fmt.Errorf("reading the objects of the region: %v", err)
	}
	members := b.prims()
	if root := byID(all, b.seen.ID); root != nil {
		if ms, err := linksetOf(all, root); err == nil {
			members = ms
		}
	}
	var hits []shownFace
	for _, m := range members {
		if len(m.TextureEntry) == 0 {
			continue
		}
		// Exactly the faces the prim has, when its shape says; else
		// those the entry names (and the note, once).
		n, _ := s.t.faceCount(b.name, s.n, m)
		faces, err := m.Faces(n)
		if err != nil {
			continue
		}
		for i, f := range faces {
			if f.Texture == tex {
				hits = append(hits, shownFace{m, i})
			}
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return shownFace{}, s.sentence("no face of %q's linkset shows %s", b.seen.Name, tex)
	}
	list := make([]string, len(hits))
	for i, h := range hits {
		list[i] = fmt.Sprintf("%q face %d", h.prim.Name, h.face)
	}
	return shownFace{}, s.sentence("%d faces of %q's linkset show %s: %s", len(hits), b.seen.Name, tex, strings.Join(list, "; "))
}
