package slate

// Items, wear, take off and attached.
//
// An item header is resolved at setup. wear binds its name when Wear
// returns, and the step's expectations may use it: the binding is made
// empty when the step is armed, so a say or a dialog from it is matched
// by what it holds when the line arrives. take off waits for the store
// to drop the root. attached is a reading of the store, which the
// watcher takes with the other readings.
// Why: doc/slate-runner.md#proposed-not-adopted

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// setupItems finds each item header: a top-level folder by name, then the
// one item of that name in it.
func (r *runner) setupItems(ctx context.Context) error {
	r.itemHdr = map[string]*sl.Item{}
	folders := map[string]msg.UUID{}
	for i := range r.s.Items {
		h := &r.s.Items[i]
		fail := func(err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &setupError{fmt.Sprintf("item %q in %q: %v", h.World, h.Folder, err)}
		}
		id, ok := folders[h.Folder]
		if !ok {
			var err error
			if id, err = r.sess.Folder(ctx, h.Folder); err != nil {
				return fail(err)
			}
			folders[h.Folder] = id
		}
		it, err := r.sess.FindItem(ctx, id, h.World)
		if err != nil {
			return fail(err)
		}
		r.itemHdr[h.Name.Text] = it
	}
	return nil
}

// wearStimulus is wear ITEM on "point" as NAME: a blocking Wear, and NAME
// bound to the worn root when it returns.
func (s *stepRun) wearStimulus(w *Wear) (*stimulus, error) {
	it := s.r.itemHdr[w.Item.Text]
	if it == nil {
		return nil, fmt.Errorf("%s is not an item", w.Item.Text)
	}
	point, err := sl.ParseAttachPoint(w.Point)
	if err != nil {
		return nil, pointError(w.Point, err)
	}
	name := w.As.Text
	// The name is made now and filled in when Wear returns, since the
	// step's expectations are made before the stimulus is sent.
	b := &binding{name: name, seen: &sl.Seen{}, owner: s.r.sess.Me(), item: it.ID, itemName: it.Name}
	s.t.as[name] = b
	drop := func() { delete(s.t.as, name) }
	return &stimulus{
		blocking: true,
		prepare: func(ctx context.Context) error {
			if a, worn := s.r.sess.WornFromItem(ctx, it.ID); worn {
				drop()
				return &stepSentence{fmt.Sprintf("slate: step %d: %q is already worn on %s; take it off first", s.n, it.Name, sl.AttachPointName(a.Point))}
			}
			return nil
		},
		send: func(ctx context.Context, budget time.Duration) (string, error) {
			a, err := s.r.sess.Wear(ctx, it, point|sl.AttachAdd, budget)
			if err != nil {
				drop()
				return "", err
			}
			all, err := s.r.sess.Backend().Objects(ctx, "", "")
			if err != nil && ctx.Err() != nil {
				return "", ctx.Err()
			}
			root := byID(all, a.Object.ID)
			if root == nil {
				root = &sl.Seen{Object: a.Object, AttachItem: a.Item, AttachPoint: a.Point}
			}
			b.seen = root
			b.members = []*sl.Seen{root}
			if members, err := linksetOf(all, root); err == nil {
				b.members = members
			}
			if err := s.t.watch.poll(ctx, true); err != nil {
				return "", err
			}
			return fmt.Sprintf("wore %q on %s as %s", it.Name, sl.AttachPointName(a.Point), name), nil
		},
	}, nil
}

// itemOf is the inventory item a binding is worn from: the one recorded
// at wear time, else the one its object says.
func (r *runner) itemOf(b *binding) (msg.UUID, string) {
	id, name := b.item, b.itemName
	if id.IsZero() {
		id = b.seen.AttachItem
	}
	if name == "" {
		for _, it := range r.itemHdr {
			if it.ID == id {
				name = it.Name
			}
		}
	}
	if name == "" {
		name = b.seen.Name
	}
	return id, name
}

// takeOffStimulus is take off NAME. It is done when the store no longer
// lists the root, read at the object cadence under the stimulus budget.
func (s *stepRun) takeOffStimulus(t *TakeOff) (*stimulus, error) {
	b, err := s.bound(t.Name)
	if err != nil {
		return nil, err
	}
	item, name := s.r.itemOf(b)
	if item.IsZero() {
		return nil, fmt.Errorf("%s is not worn", t.Name.Text)
	}
	return &stimulus{
		blocking: true,
		send: func(ctx context.Context, budget time.Duration) (string, error) {
			if err := s.r.sess.TakeOff(ctx, item); err != nil {
				return "", err
			}
			deadline := time.Now().Add(budget)
			for {
				found, err := s.r.sess.Backend().Objects(ctx, "", b.seen.ID.String())
				if err != nil {
					if ctx.Err() != nil {
						return "", ctx.Err()
					}
					return "", fmt.Errorf("reading %q: %v", name, err)
				}
				if len(found) == 0 {
					break
				}
				if !time.Now().Before(deadline) {
					return "", fmt.Errorf("%q is still in the store %s after the take off", name, budget)
				}
				if err := s.r.sess.Settle(ctx, s.r.cfg.objectEvery()); err != nil {
					return "", err
				}
			}
			if err := s.t.watch.poll(ctx, true); err != nil {
				return "", err
			}
			return fmt.Sprintf("took off %q", name), nil
		},
	}, nil
}

// attReading is one observation of where a binding is worn.
type attReading struct {
	at    time.Time
	point int  // the attachment point, when not off
	off   bool // the store no longer lists the root
}

func (a *attReading) text(name string) string {
	if a.off {
		return "attached " + name + " off"
	}
	return "attached " + name + " " + sl.AttachPointName(a.point)
}

// readAttached takes the reading of each name an attached expectation of
// the test names, and prints it when it differs from the last printed.
// A name that is not bound yet, or is still being worn, has none.
func (w *watcher) readAttached(all []*sl.Seen, at time.Time) {
	r := w.t.r
	for _, name := range w.att {
		b := r.lookup(name)
		if b == nil || b.seen.ID.IsZero() {
			continue
		}
		rd := &attReading{at: at, off: true}
		if o := byID(all, b.seen.ID); o != nil {
			rd.off, rd.point = false, o.AttachPoint
		}
		w.areads[name] = append(w.areads[name], rd)
		if line := rd.text(name); w.aprinted[name] != line {
			w.aprinted[name] = line
			ev := &event{kind: evReading, at: at, consumed: true, text: line}
			r.log.add(ev)
			r.printEvent(ev)
		}
	}
}

// attachedExp is an attached expectation: the name and what it holds.
type attachedExp struct {
	name  string
	off   bool
	point int
	final bool // a negative's window was judged
}

func (a *attachedExp) holds(r *attReading) bool {
	if a.off {
		return r.off
	}
	return !r.off && r.point == a.point
}

// attachedExpect makes attached NAME on "point" or off. It is judged
// against the readings as a state is: the one at the arm point holds, and
// so does any after it up to the limit.
func (s *stepRun) attachedExpect(x *expState) error {
	e := x.e.Attached
	ax := &attachedExp{name: e.Name.Text, off: e.Off}
	if !e.Off {
		p, err := sl.ParseAttachPoint(e.Point)
		if err != nil {
			return pointError(e.Point, err)
		}
		ax.point = p
	}
	x.match = never
	x.eval = func(ctx context.Context) error { return s.evalAttached(x, ax) }
	if !x.neg {
		x.noteFn = func() string {
			rs := s.t.watch.areads[ax.name]
			if len(rs) == 0 {
				return "; no reading was taken"
			}
			return "; last reading: " + rs[len(rs)-1].text(ax.name)
		}
	}
	return nil
}

func (s *stepRun) evalAttached(x *expState, ax *attachedExp) error {
	if x.neg && ax.final {
		return nil
	}
	rs := s.t.watch.areads[ax.name]
	var base *attReading
	var seq []*attReading
	for i := len(rs) - 1; i >= 0; i-- {
		if !rs[i].at.After(s.arm) {
			base = rs[i]
			break
		}
	}
	if base != nil {
		seq = append(seq, base)
	}
	for _, r := range rs {
		if r.at.After(s.arm) && !r.at.After(x.limit) {
			seq = append(seq, r)
		}
	}
	var hit *attReading
	for _, r := range seq {
		if ax.holds(r) {
			hit = r
			break
		}
	}
	now := time.Now()
	switch {
	case hit != nil:
		x.ev = &event{kind: evReading, at: hit.at, text: hit.text(ax.name)}
		if x.neg {
			x.forbidden = true
		} else {
			x.matched = true
		}
	case x.neg && !now.Before(x.limit):
		ax.final = true
		if len(seq) == 0 {
			x.note = "; no reading was taken, and a negative needs one"
			s.why = "a negative attached needs a real reading, and none was taken"
			s.fail(1, "no reading")
		}
	}
	return nil
}

// startOfPoint is what to say of a name that begins the names of
// points without being one: which they are, and that a name settles it.
// Slate has no number to give instead.
func startOfPoint(e *sl.AttachPointStartError) string {
	if e.Ambiguous() {
		return e.Error() + "; say which"
	}
	return e.Error()
}

// pointError is the error for a point ParseAttachPoint refused.
func pointError(name string, err error) error {
	var start *sl.AttachPointStartError
	if errors.As(err, &start) {
		return errors.New(startOfPoint(start))
	}
	return fmt.Errorf("%q is not an attachment point sl knows", name)
}
