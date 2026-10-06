package slate

// drop ITEM into OBJ, and drop ITEM onto OBJ face N: an inventory item put
// into a prim's contents, and a texture put on one face, as a viewer's
// drag does either. What a drop changed is put back when the test ends.
// Why: doc/slate-language.md#stimuli

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// dropUndoFor bounds each step of putting back what one drop changed. A
// variable so that a test of a removal that never lands need not wait it
// out.
var dropUndoFor = 30 * time.Second

// dropped is what one drop step changed, to be put back at the test's
// end: the copy it put into the object, and the face it textured.
type dropped struct {
	name string // the script binding of the object
	obj  sl.Object

	added *sl.TaskItem // the copy in the object's contents; nil when none went in

	face    int      // the face textured; meaningful when faceSet
	faceSet bool     // the face was changed
	was     msg.UUID // the face's texture before
	now     msg.UUID // and what the drop gave it
}

// dropStimulus is drop ITEM into OBJ or drop ITEM onto OBJ face N. Both are
// blocking and the tester's alone.
func (s *stepRun) dropStimulus(d *Drop) (*stimulus, error) {
	it := s.r.itemHdr[d.Item.Text]
	if it == nil {
		return nil, fmt.Errorf("%s is not an item", d.Item.Text)
	}
	b, resolve, err := s.target(d.Name, d.Link)
	if err != nil {
		return nil, err
	}
	st := &stimulus{blocking: true}
	st.prepare = func(ctx context.Context) error {
		switch {
		case d.Onto && it.Type != int(sl.AssetTexture):
			return &stepSentence{fmt.Sprintf("slate: step %d: %q is not a texture; drop onto a face needs one, and the drop was not sent", s.n, it.Name)}
		case !d.Onto && it.OwnerMask&sl.PermCopy == 0:
			return &stepSentence{fmt.Sprintf("slate: step %d: %q may not be copied, so dropping it would move it out of the tester's inventory and the run could not give it back; the drop was not sent", s.n, it.Name)}
		}
		return nil
	}
	if d.Onto {
		st.send = func(ctx context.Context, budget time.Duration) (string, error) {
			return s.dropOnto(ctx, budget, d, it, b)
		}
	} else {
		st.send = func(ctx context.Context, budget time.Duration) (string, error) {
			return s.dropInto(ctx, budget, it, b)
		}
	}
	return withLink(st, resolve), nil
}

// dropInto puts a copy of the item into the prim, and waits to see it.
func (s *stepRun) dropInto(ctx context.Context, budget time.Duration, it *sl.Item, b *binding) (string, error) {
	u := &dropped{name: b.name, obj: b.seen.Object}
	// Kept as soon as the copy is in, even beside an error, so that the
	// end of the test removes it.
	if err := s.putIn(ctx, budget, it, u); err != nil {
		return "", err
	}
	if err := s.t.watch.poll(ctx, true); err != nil {
		return "", err
	}
	if sl.IsScript(it) {
		// PutInObject sends a script as the viewer drops one, asking it to run.
		// Why: doc/scripts.md#dropping-a-script-into-an-object
		return fmt.Sprintf("dropped %q into %s, asked to run", it.Name, b.name), nil
	}
	return fmt.Sprintf("dropped %q into %s", it.Name, b.name), nil
}

// dropOnto does what the viewer does for a texture dropped on a face
// (dropTextureOneFace, handleDropMaterialProtections): when the prim holds
// an item of that asset the face is set and nothing else; a texture the
// tester may copy and transfer is set on the face; one it may copy and not
// transfer goes in as a copy first; one it may not copy is refused.
// Why: doc/slate-language.md#stimuli
func (s *stepRun) dropOnto(ctx context.Context, budget time.Duration, d *Drop, it *sl.Item, b *binding) (string, error) {
	sess := s.r.sess
	obj := b.seen.Object
	face := int(d.Face.Value)
	faces, err := sess.Faces(ctx, &obj)
	if err != nil {
		return "", err
	}
	if face >= len(faces) {
		return "", fmt.Errorf("%s has %d faces, so there is no face %d", b.name, len(faces), face)
	}
	held, err := sess.TaskInventory(ctx, &obj)
	if err != nil {
		return "", err
	}
	u := &dropped{name: b.name, obj: obj, face: face, was: faces[face].Texture, now: it.AssetID}
	how := "; nothing went into the prim"
	switch {
	case holdsTexture(held, it.AssetID):
		how = "; the prim already held it"
	case it.OwnerMask&sl.PermCopy == 0:
		return "", fmt.Errorf("%q may not be copied, so putting it into %s would move it out of the tester's inventory and the run could not give it back; the face was not changed", it.Name, b.name)
	case it.OwnerMask&sl.PermTransfer == 0:
		if err := s.putIn(ctx, budget, it, u); err != nil {
			return "", err
		}
		how = "; a copy went into the prim first"
	}
	if err := sess.SetFace(ctx, &obj, face, func(f *sl.Face) { f.Texture = it.AssetID }); err != nil {
		return "", err
	}
	u.faceSet = true
	if u.added == nil { // putIn kept it already when a copy went in
		s.t.drops = append(s.t.drops, u)
	}
	if err := s.t.watch.poll(ctx, true); err != nil {
		return "", err
	}
	return fmt.Sprintf("dropped %q onto %s face %d%s", it.Name, b.name, face, how), nil
}

// copyNumbered is what follows the item's name in the name a prim gives a
// copy whose name it already holds: a space and a number, and nothing else.
var copyNumbered = regexp.MustCompile(`^ [0-9]+$`)

// ourCopy is the entry of now that is the copy a drop of it put in, or nil.
// It is only what a prim makes of a drop: an entry that was not in had,
// named exactly as the item or as the item, a space and a number, of the
// item's type, and of its asset where the contents show one.  A product
// that puts in or renames an item of its own as the drop arrives -- the
// item's name with something after it -- is not taken for the copy, so the
// end of the test does not remove the product's own item.
// Why: doc/slate-language.md#stimuli
func ourCopy(now []sl.TaskItem, had map[msg.UUID]bool, it *sl.Item) *sl.TaskItem {
	for i := range now {
		e := &now[i]
		if had[e.ID] {
			continue
		}
		named := e.Name == it.Name || copyNumbered.MatchString(strings.TrimPrefix(e.Name, it.Name))
		if !named || e.Type != sl.AssetType(it.Type).String() {
			continue
		}
		if !e.Asset.IsZero() && !it.AssetID.IsZero() && e.Asset != it.AssetID {
			continue
		}
		return e
	}
	return nil
}

// lateDrop is a drop whose copy had not shown when its step gave up: what
// the prim held before it, so that the end of the test can tell a copy that
// came late from anything else.
type lateDrop struct {
	u    *dropped
	item *sl.Item
	had  map[msg.UUID]bool
}

// holdsTexture says whether a prim's contents hold a texture of the asset.
func holdsTexture(held []sl.TaskItem, asset msg.UUID) bool {
	for _, h := range held {
		if h.Type == "texture" && h.Asset == asset {
			return true
		}
	}
	return false
}

// putIn sends PutInObject (RezScript for a script, UpdateTaskInventory for the rest) and reads the prim's contents until the copy
// shows: the message has no reply. The copy is the item with the name the
// prim gave it (a duplicate is renamed "NAME 1") that was not there
// before. u keeps it from the moment it is seen.
// Why: doc/slate-language.md#stimuli
func (s *stepRun) putIn(ctx context.Context, budget time.Duration, it *sl.Item, u *dropped) error {
	sess := s.r.sess
	before, err := sess.TaskInventory(ctx, &u.obj)
	if err != nil {
		return err
	}
	had := map[msg.UUID]bool{}
	for _, h := range before {
		had[h.ID] = true
	}
	if err := sess.PutInObject(ctx, &u.obj, it); err != nil {
		return err
	}
	deadline := time.Now().Add(budget)
	for {
		now, err := sess.TaskInventory(ctx, &u.obj)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("reading the contents of %s: %v", u.name, err)
		}
		if c := ourCopy(now, had, it); c != nil {
			u.added = c
			s.t.drops = append(s.t.drops, u)
			return nil
		}
		if !time.Now().Before(deadline) {
			// The copy may still come, after the step has given up; the
			// end of the test looks once more (see undoDrops), so that a
			// slow region does not leave it in the product for good.
			s.t.late = append(s.t.late, lateDrop{u: u, item: it, had: had})
			return fmt.Errorf("%q did not show in the contents of %s within %s of the drop; the prim may not take it, or something took it out again", it.Name, u.name, budget)
		}
		if err := sess.Settle(ctx, s.r.cfg.objectEvery()); err != nil {
			return err
		}
	}
}

// undoDrops puts back, newest first, what the test's drop steps changed:
// a face set back to the texture it had, a copy removed from the prim's
// contents. It runs on a context of its own, since the test's may be what
// ended. Each is printed; the objects it could not put back are returned.
// Putting a texture back is itself a texture change, which a product may
// react to.
// Why: doc/slate-language.md#stimuli
func (t *testRun) undoDrops(ctx context.Context) []string {
	if len(t.drops) == 0 && len(t.late) == 0 {
		return nil
	}
	r := t.r
	dctx := context.WithoutCancel(ctx)
	var failed []string
	// A copy that came after its step gave up is looked for once, and
	// removed like any other.
	for _, l := range t.late {
		c, cancel := context.WithTimeout(dctx, dropUndoFor)
		now, err := r.sess.TaskInventory(c, &l.u.obj)
		cancel()
		if err != nil {
			r.printf("slate: remove failed: the late copy of %q from %s: %v", l.item.Name, l.u.name, err)
			failed = append(failed, l.u.name)
			continue
		}
		if cp := ourCopy(now, l.had, l.item); cp != nil {
			l.u.added = cp
			t.drops = append(t.drops, l.u)
		} else {
			r.printf("slate: the copy of %q never showed in %s; nothing removed", l.item.Name, l.u.name)
		}
	}
	t.late = nil
	for i := len(t.drops) - 1; i >= 0; i-- {
		u := t.drops[i]
		bad := false
		if u.faceSet && u.was != u.now {
			c, cancel := context.WithTimeout(dctx, dropUndoFor)
			err := r.sess.SetFace(c, &u.obj, u.face, func(f *sl.Face) { f.Texture = u.was })
			cancel()
			if err != nil {
				r.printf("slate: restore failed: %s face %d: %v", u.name, u.face, err)
				bad = true
			} else {
				r.printf("slate: restored %s face %d to texture %s", u.name, u.face, u.was)
			}
		}
		if u.added != nil {
			c, cancel := context.WithTimeout(dctx, dropUndoFor)
			err := t.removeAdded(c, u)
			cancel()
			if err != nil {
				r.printf("slate: remove failed: %q from %s: %v", u.added.Name, u.name, err)
				bad = true
			} else {
				r.printf("slate: removed %q from %s", u.added.Name, u.name)
			}
		}
		if bad {
			failed = append(failed, u.name)
		}
	}
	t.drops = nil
	return failed
}

// removeAdded removes the copy a drop put in, and reads the contents until
// it is gone: RemoveTaskInventory has no reply either.
func (t *testRun) removeAdded(ctx context.Context, u *dropped) error {
	sess := t.r.sess
	if err := sess.RemoveFromObject(ctx, &u.obj, u.added.ID); err != nil {
		return err
	}
	for {
		now, err := sess.TaskInventory(ctx, &u.obj)
		if err != nil {
			return err
		}
		still := false
		for _, h := range now {
			still = still || h.ID == u.added.ID
		}
		if !still {
			return nil
		}
		if err := sess.Settle(ctx, t.r.cfg.objectEvery()); err != nil {
			return fmt.Errorf("it is still in the contents: %w", err)
		}
	}
}
