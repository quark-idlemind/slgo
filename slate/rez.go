package slate

// expect rez: the new roots the region shows after the arm point, matched
// to the step's claims by what the region can say about them.
// Why: doc/slate-runner.md#rez

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// rezRadius is how near a new root has to be to the prim it is from. It is
// the historical llRezObject limit, used as a match radius and not
// measured.
// Why: doc/slate-runner.md#rez
const rezRadius = 10.0

// rezRoot is a prim the region showed after the test began: a root, a
// prim, and not there at the arm point of the step looking at it.
type rezRoot struct {
	ev   *event // observed when the poll first saw it; its text is the transcript line
	seen *sl.Seen
	info *rezInfo
	err  string    // the last Properties failure
	next time.Time // not asked again before this: Properties is retried each poll
}

// rezInfo is what only Properties says.
type rezInfo struct {
	name, desc string
	owner      msg.UUID
}

func (r *rezRoot) name() string {
	if r.info != nil {
		return r.info.name
	}
	return r.seen.Name
}

func (r *rezRoot) owner() msg.UUID {
	if !r.seen.Owner.IsZero() {
		return r.seen.Owner
	}
	if r.info != nil {
		return r.info.owner
	}
	return msg.UUID{}
}

func (r *rezRoot) show() string {
	p := r.seen.Position
	return fmt.Sprintf("%s %q at %.1f %.1f %.1f", r.seen.ID, r.name(), p.X, p.Y, p.Z)
}

// rezClaim is one rez expectation of a step.
type rezClaim struct {
	x      *expState
	c      *RezExp
	from   *binding
	name   func(string) bool
	nameM  textMatch // name and desc with the groups they bind
	descM  textMatch
	desc   func(string) bool // nil when the claim writes none
	root   *rezRoot          // the root it was assigned
	nearby string            // why the latest root was not near, for the report
}

// rezStep is a step's claims and what it has judged.
type rezStep struct {
	claims []*rezClaim // every claim, source order, negatives included
	judged map[*rezRoot]bool
}

// rezExpect makes a rez expectation. The claim is judged against roots as
// they are seen, by rezScan.
func (s *stepRun) rezExpect(x *expState) error {
	c := x.e.Rez
	b := s.r.lookup(c.From.Text)
	if b == nil {
		return fmt.Errorf("%s is not an object", c.From.Text)
	}
	name, err := s.textMatch(c.Name)
	if err != nil {
		return err
	}
	cl := &rezClaim{x: x, c: c, from: b, name: name.match, nameM: name}
	if c.HasDesc {
		d, err := s.textMatch(c.Desc)
		if err != nil {
			return err
		}
		cl.desc, cl.descM = d.match, d
	}
	if s.obs.rez == nil {
		s.obs.rez = &rezStep{judged: map[*rezRoot]bool{}}
	}
	s.obs.rez.claims = append(s.obs.rez.claims, cl)
	x.match = never
	if !x.neg {
		x.noteFn = func() string { return s.rejections(cl) }
	}
	return nil
}

// hasPositiveRez says whether the step claims a rez, which is what makes
// it settle.
func (s *stepRun) hasPositiveRez() bool {
	for _, x := range s.exps {
		if x.e.Rez != nil && !x.neg {
			return true
		}
	}
	return false
}

// callCtx is the context of a call that can block: the time left to the
// step's deadline while it is matching, never a call's own default. After
// Matching nothing is left to expire, and the call has the cap.
func (s *stepRun) callCtx(ctx context.Context, limit time.Duration) (context.Context, context.CancelFunc) {
	d := limit
	if s.state == stMatching {
		var end time.Time
		for _, e := range s.exps {
			end = later(end, e.limit)
		}
		d = min(d, max(time.Until(end), time.Millisecond))
	}
	return context.WithTimeout(ctx, d)
}

// position says why a claim's `from` does not put the root near, or "": the
// region position of some prim of the linkset, by the walk of
// linkset.go, within the radius.
func (s *stepRun) position(c *rezClaim, root *rezRoot) string {
	all := s.t.watch.all
	best, known := math.MaxFloat64, false
	for _, m := range c.from.prims() {
		cur := byID(all, m.ID)
		if cur == nil {
			cur = m
		}
		pos, ok := regionPos(all, cur)
		if !ok {
			continue
		}
		known = true
		p := root.seen.Position
		d := math.Sqrt(sq(float64(p.X-pos.X)) + sq(float64(p.Y-pos.Y)) + sq(float64(p.Z-pos.Z)))
		best = math.Min(best, d)
	}
	switch {
	case !known:
		return "position unknown"
	case best > rezRadius:
		return fmt.Sprintf("distance %.1f m from %s", best, c.c.From.Text)
	}
	return ""
}

func sq(f float64) float64 { return f * f }

// fromOwner is who owns the prim a claim is from, when that can be read.
func (s *stepRun) fromOwner(ctx context.Context, b *binding) (msg.UUID, bool, error) {
	if id, ok := s.obs.owners[b.name]; ok {
		return id, true, nil
	}
	if s.obs.noOwner[b.name] {
		return msg.UUID{}, false, nil
	}
	callCtx, cancel := s.callCtx(ctx, s.r.cfg.props)
	defer cancel()
	id, err := s.r.ownerOf(callCtx, b)
	if err != nil {
		if ctx.Err() != nil {
			return id, false, ctx.Err()
		}
		if s.obs.noOwner == nil {
			s.obs.noOwner = map[string]bool{}
		}
		s.obs.noOwner[b.name] = true
		return msg.UUID{}, false, nil
	}
	if s.obs.owners == nil {
		s.obs.owners = map[string]msg.UUID{}
	}
	s.obs.owners[b.name] = id
	return id, true, nil
}

// ownerReason says why the owners differ, when both are known and
// non-zero, or "".
func (s *stepRun) ownerReason(ctx context.Context, c *rezClaim, root *rezRoot) (string, error) {
	have := root.owner()
	if have.IsZero() {
		return "", nil
	}
	want, ok, err := s.fromOwner(ctx, c.from)
	if err != nil || !ok || want.IsZero() || want == have {
		return "", err
	}
	return fmt.Sprintf("owner %s is not the owner of %s", have, c.c.From.Text), nil
}

// rezInfoFor reads what Properties says of a root: its name, and its
// description when a claim wants one. A failure is not a failed claim: the
// root is looked at again on the next poll, until the deadline.
func (s *stepRun) rezInfoFor(ctx context.Context, root *rezRoot, needDesc, needOwner bool) bool {
	if root.info != nil || (root.seen.Name != "" && !needDesc && !needOwner) {
		return true
	}
	if time.Now().Before(root.next) {
		return false
	}
	callCtx, cancel := s.callCtx(ctx, s.r.cfg.props)
	defer cancel()
	d := s.r.cfg.props
	if dl, ok := callCtx.Deadline(); ok {
		d = time.Until(dl)
	}
	p, err := s.r.sess.Properties(callCtx, &root.seen.Object, d)
	if err != nil {
		if ctx.Err() == nil && root.seen.Name != "" && !needDesc {
			// Only the owner was wanted, and an owner that is not known is
			// not a mismatch.
			return true
		}
		root.err = err.Error()
		root.next = time.Now().Add(s.r.cfg.objectEvery())
		return false
	}
	root.err = ""
	root.info = &rezInfo{name: p.Name, desc: p.Description, owner: p.Owner}
	if root.info.name == "" {
		root.info.name = root.seen.Name
	}
	return true
}

// textReason says why the name or description of a claim does not match,
// or "". A description that has not been read is not a mismatch.
func (c *rezClaim) textReason(root *rezRoot) string {
	if !c.name(root.name()) {
		return fmt.Sprintf("name %q", root.name())
	}
	if c.desc != nil && root.info != nil && !c.desc(root.info.desc) {
		return fmt.Sprintf("description %q", root.info.desc)
	}
	return ""
}

// rezScan judges each root the step has not judged. It is where a rez is
// matched, where a second root fails the step, and where a name is bound.
func (s *stepRun) rezScan(ctx context.Context) error {
	rz := s.obs.rez
	for _, root := range s.t.watch.roots {
		if root.ev.consumed || root.ev.at.Before(s.arm) || rz.judged[root] {
			continue
		}
		if err := s.judgeRoot(ctx, rz, root); err != nil || s.state == stFailed {
			return err
		}
	}
	// A negative's window is not extended and a positive claim's deadline is
	// the matching loop's: nothing else here ends a step.
	return nil
}

// printRoot prints a root's line once its name is known.
func (s *stepRun) printRoot(root *rezRoot) {
	if root.ev.text != "" || root.name() == "" {
		return
	}
	p := root.seen.Position
	root.ev.text = fmt.Sprintf("rez %s name %q at %.1f %.1f %.1f", root.seen.ID, root.name(), p.X, p.Y, p.Z)
	s.r.log.add(root.ev)
	s.r.printEvent(root.ev)
}

// judgeRoot decides one root against the claims. A root that is not near
// any claim's prim is not the step's concern. A near one needs its name,
// and its description if a claim wants one; it then goes to the earliest
// unmatched claim it satisfies. A root that satisfies only claims already
// taken is a second match and fails the step, and one that is near and
// satisfies none fails it as well. A negative claim it satisfies is a
// forbidden event.
// Why: doc/slate-runner.md#rez
func (s *stepRun) judgeRoot(ctx context.Context, rz *rezStep, root *rezRoot) error {
	var near []*rezClaim
	for _, c := range rz.claims {
		if c.nearby = s.position(c, root); c.nearby == "" {
			near = append(near, c)
		}
	}
	if len(near) == 0 {
		rz.judged[root] = true
		s.printRoot(root)
		return nil
	}
	needDesc := false
	for _, c := range near {
		needDesc = needDesc || c.desc != nil
	}
	needOwner := false
	if root.seen.Owner.IsZero() {
		for _, c := range near {
			if o, ok, err := s.fromOwner(ctx, c.from); err != nil {
				return err
			} else if ok && !o.IsZero() {
				needOwner = true
			}
		}
	}
	if !s.rezInfoFor(ctx, root, needDesc, needOwner) {
		return nil
	}
	s.printRoot(root)
	var from, full []*rezClaim
	for _, c := range near {
		why, err := s.ownerReason(ctx, c, root)
		if err != nil {
			return err
		}
		if c.nearby = why; why != "" {
			continue
		}
		from = append(from, c)
		if c.nearby = c.textReason(root); c.nearby == "" {
			full = append(full, c)
		}
	}
	rz.judged[root] = true
	positives := false
	for _, c := range from {
		positives = positives || !c.x.neg
	}
	forbidden := false
	for _, c := range full {
		if c.x.neg && !c.x.forbidden {
			c.x.forbidden, c.x.ev, forbidden = true, root.ev, true
		}
	}
	if forbidden || !positives {
		return nil
	}
	var mine *rezClaim
	var first *rezClaim
	for _, c := range full {
		if c.x.neg {
			continue
		}
		if first == nil {
			first = c
		}
		if c.root == nil {
			mine = c
			break
		}
	}
	switch {
	case mine != nil:
		return s.assign(ctx, mine, root)
	case first != nil:
		s.why = fmt.Sprintf("a second root matches the rez claim %s: %s and %s", first.x.text, first.root.show(), root.show())
		s.fail(1, "second root")
	default:
		var why []string
		for _, c := range from {
			if !c.x.neg {
				why = append(why, c.nearby)
			}
		}
		s.why = fmt.Sprintf("a new root %s is from %s and matches no rez claim (%s)", root.show(), from[0].c.From.Text, strings.Join(why, "; "))
		s.fail(1, "no claim")
	}
	return nil
}

// assign gives a root to a claim, and binds the name it is written with.
// The name is usable from the next step: a step's expectations are made
// when it begins. Its linkset is the root and its children as the store
// shows them, read again each time they are polled.
func (s *stepRun) assign(ctx context.Context, c *rezClaim, root *rezRoot) error {
	c.root = root
	root.ev.consumed = true
	c.x.matched, c.x.ev = true, root.ev
	srcs := []groupSrc{{c.nameM, root.name()}}
	if c.desc != nil && root.info != nil {
		srcs = append(srcs, groupSrc{c.descM, root.info.desc})
	}
	if s.bindMatched(c.x, nil, srcs...); s.state == stFailed {
		return nil
	}
	if c.c.As.Text == "" {
		return nil
	}
	seen := *root.seen
	// The region's updates carry no name; Properties said the one the
	// claim matched, and the transcript names the object by it.
	if seen.Name == "" {
		seen.Name = root.name()
	}
	b := &binding{name: c.c.As.Text, seen: &seen, owner: root.owner()}
	if members, err := linksetOf(s.t.watch.all, &seen); err == nil {
		b.members = members
	}
	s.t.as[b.name] = b
	if err := s.t.watch.poll(ctx, true); err != nil {
		return err
	}
	s.startDescribe(ctx, b)
	return nil
}

// rejections is the report of a claim nothing matched: every new root the
// step saw and why that claim did not take it.
func (s *stepRun) rejections(c *rezClaim) string {
	var seen []string
	for _, root := range s.t.watch.roots {
		if root.ev.at.Before(s.arm) {
			continue
		}
		switch {
		case c.root == root:
			continue
		case root.info == nil && root.seen.Name == "" && root.err != "":
			seen = append(seen, fmt.Sprintf("%s (properties: %s)", root.show(), root.err))
			continue
		}
		reason := s.position(c, root)
		if reason == "" {
			if o, ok, _ := s.fromOwnerKnown(c.from); ok && !o.IsZero() && !root.owner().IsZero() && o != root.owner() {
				reason = fmt.Sprintf("owner %s is not the owner of %s", root.owner(), c.c.From.Text)
			} else if c.desc != nil && root.info == nil && root.err != "" {
				reason = "properties: " + root.err
			} else {
				reason = c.textReason(root)
			}
		}
		if reason == "" && !s.obs.rez.judged[root] {
			reason = "not judged: its properties were not read"
		} else if reason == "" {
			reason = "matches, and was taken by another claim"
		}
		seen = append(seen, fmt.Sprintf("%s (%s)", root.show(), reason))
	}
	if len(seen) == 0 {
		return "; no new root was seen"
	}
	return "; new roots rejected: " + strings.Join(seen, ", ")
}

// fromOwnerKnown is fromOwner without a read: what has been learned.
func (s *stepRun) fromOwnerKnown(b *binding) (msg.UUID, bool, error) {
	if id, ok := s.obs.owners[b.name]; ok {
		return id, true, nil
	}
	if !b.seen.Owner.IsZero() {
		return b.seen.Owner, true, nil
	}
	return msg.UUID{}, false, nil
}
