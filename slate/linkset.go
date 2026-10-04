package slate

// The linkset of a binding and the region position of a prim.
//
// Not sl's linkset: it climbs one parent, and an attachment's parent is
// the avatar, so it would swap a HUD root for the avatar and collect
// every attachment root as its children.
// Why: doc/slate-runner.md#linksets-and-region-positions

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

const (
	pcodePrim   = 9
	pcodeAvatar = 47

	// maxAncestors is the longest Parent chain followed for a position.
	maxAncestors = 8
)

// byLocal finds the object with a local id, or nil.
func byLocal(all []*sl.Seen, local uint32) *sl.Seen {
	for _, s := range all {
		if s.Local == local {
			return s
		}
	}
	return nil
}

// linksetOf walks from a bound prim to its root and returns the root and
// its prim children, root first. An attachment root stops the climb, and
// the avatar is never a member. A parent that is not in the store is an
// error.
func linksetOf(all []*sl.Seen, bound *sl.Seen) ([]*sl.Seen, error) {
	root := bound
	for steps := 0; root.Parent != 0; steps++ {
		parent := byLocal(all, root.Parent)
		if parent == nil || steps > len(all) {
			return nil, fmt.Errorf("the parent of %q is not in the store", bound.Name)
		}
		if parent.PCode != pcodePrim {
			break
		}
		root = parent
	}
	members := []*sl.Seen{root}
	for _, s := range all {
		if s.Parent == root.Local && s.PCode == pcodePrim && s.ID != root.ID {
			members = append(members, s)
		}
	}
	return members, nil
}

// regionPos is where a prim is in the region: the Position of the first
// ancestor with no parent, at most maxAncestors steps up, with no offset
// added. ok is false when the walk leaves the store.
func regionPos(all []*sl.Seen, s *sl.Seen) (pos msg.Vector3, ok bool) {
	for i := 0; i <= maxAncestors; i++ {
		if s.Parent == 0 {
			return s.Position, true
		}
		if s = byLocal(all, s.Parent); s == nil {
			return msg.Vector3{}, false
		}
	}
	return msg.Vector3{}, false
}

// setupLinksets finds the linkset of every header binding, whether or not
// it has a probe, because speakers, dialogs and gives match on it.
func (r *runner) setupLinksets(ctx context.Context) error {
	all, err := r.sess.Backend().Objects(ctx, "", "")
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &setupError{fmt.Sprintf("reading the objects of the region: %v", err)}
	}
	roots := map[uint32]string{} // linkset root -> the probed binding in it
	probed := map[string]bool{}
	for _, p := range r.s.Probes {
		probed[p.Name.Text] = true
	}
	for _, o := range r.s.Objects {
		b := r.bind[o.Name.Text]
		if b == nil {
			continue
		}
		members, err := linksetOf(all, b.seen)
		if err != nil {
			return &setupError{err.Error()}
		}
		b.members = members
		if probed[b.name] {
			root := members[0].Local
			if other, dup := roots[root]; dup {
				return &setupError{fmt.Sprintf("%s and %s are in one linkset and both have a probe; one probe covers the whole linkset", other, b.name)}
			}
			roots[root] = b.name
		}
	}
	return nil
}

// linkFault is why a link number does not resolve: a sentence without
// the step's prefix, which the step that asked says.
type linkFault struct{ text string }

func (e *linkFault) Error() string { return e.text }

// linkErr is err as a sentence of the step when it is a linkFault.
func (s *stepRun) linkErr(err error) error {
	var lf *linkFault
	if errors.As(err, &lf) {
		return s.sentence("%s", lf.text)
	}
	return err
}

// probed is whether a binding has a probe, which is where its link
// numbers come from; without one the store is the map.
func (r *runner) probed(b *binding) bool {
	return r.pr != nil && r.pr.links[b.name] != nil
}

// splitLinkName is the binding and link number a name made by linkName
// has, and false for any other name.
func splitLinkName(name string) (string, int64, bool) {
	i := strings.LastIndex(name, " link ")
	if i < 0 {
		return "", 0, false
	}
	n, err := strconv.ParseInt(name[i+len(" link "):], 10, 64)
	return name[:i], n, err == nil
}

// linkBinding is the binding of the prim at a link N of b, found now: by
// the probe's hello map when b has a probe, which is the script's own
// truth, and otherwise by the store's link numbers. A product can relink
// itself, so a store's answer is asked for at each use. all is the poll
// to climb in, or nil to read one. This is the one place a link number
// becomes a prim.
// Why: doc/slate-runner.md#linksets-and-region-positions
func (r *runner) linkBinding(ctx context.Context, all []*sl.Seen, b *binding, link int64) (*binding, error) {
	if r.probed(b) {
		pp, err := r.primAt(b, link)
		if err != nil {
			return nil, err
		}
		return r.bind[linkName(b.name, pp.link)], nil
	}
	var err error
	if all == nil {
		if all, err = r.sess.Backend().Objects(ctx, "", ""); err != nil {
			return nil, ctxOr(ctx, &linkFault{fmt.Sprintf("reading the objects of the region: %v", err)})
		}
	}
	cur := byID(all, b.seen.ID)
	if cur == nil {
		return nil, &linkFault{fmt.Sprintf("%q is not in the region", b.seen.Name)}
	}
	members, err := linksetOf(all, cur)
	if err != nil {
		return nil, &linkFault{err.Error()}
	}
	root := members[0]
	set, err := r.sess.Linkset(ctx, &root.Object)
	if errors.Is(err, sl.ErrLinkOrderUnknown) {
		return nil, &linkFault{fmt.Sprintf("the link order of %q is not known; a probe, or taking and rezzing it, gives it", root.Name)}
	}
	if err != nil {
		return nil, ctxOr(ctx, &linkFault{fmt.Sprintf("reading the links of %q: %v", root.Name, err)})
	}
	// A set of one is link 0, and the root of a larger one is link 1; so
	// is a lone prim with somebody sitting on it.
	i := int(link) - 1
	if len(set) == 1 && set[0].LinkNumber == 0 {
		i = int(link)
	}
	if link < 0 || link > int64(len(set)) || i < 0 || i >= len(set) {
		return nil, &linkFault{fmt.Sprintf("%q has no link %d; it has %s", root.Name, link, prims(len(set)))}
	}
	name := linkName(b.name, int32(link))
	lb := r.bind[name]
	if lb == nil {
		lb = &binding{name: name}
		r.bind[name] = lb
	}
	c := *set[i]
	lb.seen = &c
	return lb, nil
}

// ctxOr is the context's error when it has ended, else err.
func ctxOr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
