package slate

// rez ITEM at|by X Y Z as NAME: an inventory item rezzed in the region,
// bound to NAME, and deleted to the Trash when the test ends; and delete NAME,
// which takes a rez step's or a claimed object to the Trash on the step's own.
// Why: doc/slate-language.md#stimuli

import (
	"context"
	"fmt"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// rezDeleteFor bounds the delete of one object a rez step made.
const rezDeleteFor = 30 * time.Second

// rezzed is an object a rez step made, to be deleted at the test's end.
type rezzed struct {
	name string // the script binding
	item string // the inventory item's name
	obj  *sl.Object
}

// rezStimulus is rez ITEM at|by X Y Z as NAME: a blocking
// Session.RezFromInventory in the tester's active group, and NAME bound to
// the root it returns. The position of a by is read in prepare, on no step
// clock, as a say reads the tester's.
func (s *stepRun) rezStimulus(x *RezItem) (*stimulus, error) {
	it := s.r.itemHdr[x.Item.Text]
	if it == nil {
		return nil, fmt.Errorf("%s is not an item", x.Item.Text)
	}
	name := x.As.Text
	// Made now and filled in when the rez returns, as wear's is, since the
	// step's expectations are made before the stimulus is sent.
	b := &binding{name: name, seen: &sl.Seen{}, owner: s.r.sess.Me(), item: it.ID, itemName: it.Name}
	s.t.as[name] = b
	drop := func() { delete(s.t.as, name) }
	var (
		pos   = msg.Vector3{X: float32(x.X.Value), Y: float32(x.Y.Value), Z: float32(x.Z.Value)}
		group msg.UUID
	)
	return &stimulus{
		blocking: true,
		prepare: func(ctx context.Context) error {
			if x.By {
				tester, exact, err := s.r.testerPos(ctx)
				if err != nil {
					drop()
					if ctx.Err() != nil {
						return ctx.Err()
					}
					return fmt.Errorf("the tester position could not be read: %v", err)
				}
				if !exact {
					drop()
					return &stepSentence{fmt.Sprintf("slate: step %d: tester position is not exact; the rez was not sent", s.n)}
				}
				pos = msg.Vector3{X: tester.X + pos.X, Y: tester.Y + pos.Y, Z: tester.Z + pos.Z}
			}
			g, err := s.r.sess.ActiveGroup(ctx)
			if err != nil {
				drop()
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return fmt.Errorf("the active group could not be read: %v", err)
			}
			group = g
			return nil
		},
		send: func(ctx context.Context, budget time.Duration) (string, error) {
			made, err := s.r.sess.RezFromInventory(ctx, it, pos, group, budget)
			if made != nil {
				// Even beside an error: whatever stands in the region is
				// deleted at the end of the test.
				s.t.rezzed = append(s.t.rezzed, &rezzed{name: name, item: it.Name, obj: made})
			}
			if err != nil {
				drop()
				return "", err
			}
			all, err := s.r.sess.Backend().Objects(ctx, "", "")
			if err != nil && ctx.Err() != nil {
				return "", ctx.Err()
			}
			root := byID(all, made.ID)
			if root == nil {
				root = &sl.Seen{Object: *made, Owner: s.r.sess.Me()}
			}
			b.seen = root
			b.members = []*sl.Seen{root}
			if members, err := linksetOf(all, root); err == nil {
				b.members = members
			}
			if err := s.t.watch.poll(ctx, true); err != nil {
				return "", err
			}
			// A link N in a later step reads the store's order: nothing
			// is dropped into a rezzed or worn object, and the
			// transcript says so once.
			s.r.noteStoreOrder(b)
			return fmt.Sprintf("rezzed %q at <%g, %g, %g> as %s", it.Name, pos.X, pos.Y, pos.Z, name), nil
		},
	}, nil
}

// trashFolder is the tester's Trash, on a context of its own since the
// caller's may be what ended the test.
func (r *runner) trashFolder(ctx context.Context) (msg.UUID, error) {
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), rezDeleteFor)
	defer cancel()
	return r.sess.TrashFolder(c)
}

// deleteRezzed deletes every object the test's rez steps made, to the
// Trash, on a context of its own since the test's may be what ended. Each
// is printed; the ones that could not be deleted are returned. An object a
// delete step already took is not in the list.
func (t *testRun) deleteRezzed(ctx context.Context) []string {
	if len(t.rezzed) == 0 {
		return nil
	}
	r := t.r
	dctx := context.WithoutCancel(ctx)
	var failed []string
	trash, terr := r.trashFolder(ctx)
	for _, z := range t.rezzed {
		err := terr
		if err == nil {
			c, cancel := context.WithTimeout(dctx, rezDeleteFor)
			err = r.sess.Delete(c, z.obj, trash)
			cancel()
		}
		if err != nil {
			r.printf("slate: delete failed: %s (%q): %v", z.name, z.item, err)
			failed = append(failed, z.name)
			continue
		}
		r.printf("slate: deleted %s (%q) to the Trash", z.name, z.item)
	}
	t.rezzed = nil
	return failed
}

// forgetRezzed takes an object out of what the end of the test deletes, for
// one a delete step already dealt with.
func (t *testRun) forgetRezzed(id msg.UUID) {
	for i, z := range t.rezzed {
		if z.obj.ID == id {
			t.rezzed = append(t.rezzed[:i], t.rezzed[i+1:]...)
			return
		}
	}
}

// deleteStimulus is delete NAME: the object a rez expectation or a rez step
// bound, to the tester's Trash, done when the store no longer lists it, read
// at the object cadence under the stimulus budget. Only the tester's own is
// deleted; one that is not fails the step before anything is sent. One
// already gone is a line and not a failure, and so is a name the test that
// has just run never bound, in after each.
// Why: doc/slate-language.md#stimuli
func (s *stepRun) deleteStimulus(d *Delete) (*stimulus, error) {
	name := d.Name.Text
	b := s.r.lookup(name)
	if b == nil || b.seen.ID.IsZero() {
		if s.x.Phase != PhaseAfter {
			return nil, fmt.Errorf("%s is not an object", name)
		}
		line := fmt.Sprintf("%s was not bound in this test; nothing deleted", name)
		return &stimulus{send: func(context.Context, time.Duration) (string, error) {
			s.r.printf("slate: %s", line)
			return line, nil
		}}, nil
	}
	id, label := b.seen.ID, b.seen.Name
	listed := func(ctx context.Context) (bool, error) {
		found, err := s.r.sess.Backend().Objects(ctx, "", id.String())
		if err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			return false, fmt.Errorf("reading %q: %v", label, err)
		}
		return len(found) > 0, nil
	}
	gone := false
	return &stimulus{
		blocking: true,
		prepare: func(ctx context.Context) error {
			here, err := listed(ctx)
			if err != nil {
				return err
			}
			if !here {
				gone = true
				return nil
			}
			owner, err := s.r.ownerOf(ctx, b)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return &stepSentence{fmt.Sprintf("slate: step %d: %s (%q) was not deleted: %v", s.n, name, label, err)}
			}
			if owner != s.r.sess.Me() {
				return &stepSentence{fmt.Sprintf("slate: step %d: %s (%q) is not the tester's; it was not deleted", s.n, name, label)}
			}
			return nil
		},
		send: func(ctx context.Context, budget time.Duration) (string, error) {
			if gone {
				s.t.forgetRezzed(id)
				line := fmt.Sprintf("%s (%q) is already gone; nothing deleted", name, label)
				s.r.printf("slate: %s", line)
				return line, nil
			}
			dctx, cancel := context.WithTimeout(ctx, budget)
			defer cancel()
			still := fmt.Errorf("%q is still in the store %s after the delete", label, budget)
			trash, err := s.r.trashFolder(ctx)
			if err == nil {
				err = s.r.sess.Delete(dctx, &b.seen.Object, trash)
			}
			if err != nil {
				if ctx.Err() != nil || dctx.Err() == nil {
					return "", err
				}
				return "", still
			}
			for {
				here, err := listed(dctx)
				if err != nil {
					if ctx.Err() != nil {
						return "", ctx.Err()
					}
					return "", still
				}
				if !here {
					break
				}
				if err := s.r.sess.Settle(dctx, s.r.cfg.objectEvery()); err != nil {
					if ctx.Err() != nil {
						return "", ctx.Err()
					}
					return "", still
				}
			}
			s.t.forgetRezzed(id)
			if err := s.t.watch.poll(ctx, true); err != nil {
				return "", err
			}
			s.r.printf("slate: deleted %s (%q) to the Trash", name, label)
			return fmt.Sprintf("deleted %q to the Trash", label), nil
		},
	}, nil
}
