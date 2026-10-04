package slate

// rez ITEM at|by X Y Z as NAME: an inventory item rezzed in the region,
// bound to NAME, and deleted to the Trash when the test ends.
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
			return fmt.Sprintf("rezzed %q at <%g, %g, %g> as %s", it.Name, pos.X, pos.Y, pos.Z, name), nil
		},
	}, nil
}

// deleteRezzed deletes every object the test's rez steps made, to the
// Trash, on a context of its own since the test's may be what ended. Each
// is printed; the ones that could not be deleted are returned.
func (t *testRun) deleteRezzed(ctx context.Context) []string {
	if len(t.rezzed) == 0 {
		return nil
	}
	r := t.r
	dctx := context.WithoutCancel(ctx)
	var failed []string
	trash, terr := func() (msg.UUID, error) {
		c, cancel := context.WithTimeout(dctx, rezDeleteFor)
		defer cancel()
		return r.sess.TrashFolder(c)
	}()
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
