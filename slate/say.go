package slate

// say: the stimulus, and the expectation on chat the avatar hears.
// Why: doc/slate-runner.md#stimuli, doc/slate-runner.md#say

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// testerName is the tester's displayed name, as the session was attached
// with it.
func (r *runner) testerName() string {
	if n := r.sess.Info().AvatarName; n != "" {
		return n
	}
	return r.sess.Name(r.sess.Me())
}

// ownerOf is who owns a binding's prim: what the store says, else a
// Properties read on the run's context (the 15 s of setup budgets). A
// group-deeded object's owner is the group, which is not the tester, so
// speaking as it fails and the avatar speaks as the tester instead.
// Why: doc/slate-runner.md#say
func (r *runner) ownerOf(ctx context.Context, b *binding) (msg.UUID, error) {
	if !b.owner.IsZero() {
		return b.owner, nil
	}
	if !b.seen.Owner.IsZero() {
		b.owner = b.seen.Owner
		return b.owner, nil
	}
	p, err := r.sess.Properties(ctx, &b.seen.Object, r.cfg.props)
	if err != nil {
		if ctx.Err() != nil {
			return msg.UUID{}, ctx.Err()
		}
		return msg.UUID{}, fmt.Errorf("the owner of %s is not known (properties of %q, %s: %v)", b.name, b.seen.Name, r.cfg.props, err)
	}
	if p.Owner.IsZero() {
		return msg.UUID{}, fmt.Errorf("the owner of %s is not known", b.name)
	}
	b.owner = p.Owner
	return b.owner, nil
}

// sayStimulus speaks as the tester. The speaker is settled in prepare so a
// refusal is made before anything is said.
func (s *stepRun) sayStimulus(sy *Say) *stimulus {
	who := "the tester"
	sess := s.r.sess
	if sy.As != nil && sy.As.Kind == SpeakSecond {
		sess = s.r.actor(&sy.As.Name)
	}
	return &stimulus{
		prepare: func(ctx context.Context) error {
			var err error
			who, err = s.sayAs(ctx, sy)
			if err != nil {
				return err
			}
			return s.sayRange(ctx, sy)
		},
		send: func(ctx context.Context, _ time.Duration) (string, error) {
			// Say and a negative-channel dialog reply return when the
			// message is sent: no stimulus budget.
			if err := sess.Say(ctx, sy.Text, int32(sy.Channel.Value)); err != nil {
				return "", err
			}
			return fmt.Sprintf("sent on channel %d as %s", sy.Channel.Value, who), nil
		},
	}
}

// sayAs checks the speaker of a say and says how to print it.
func (s *stepRun) sayAs(ctx context.Context, sy *Say) (string, error) {
	r := s.r
	if sy.As == nil {
		return "the tester", nil
	}
	switch sy.As.Kind {
	case SpeakTester:
		return "the tester", nil
	case SpeakOwner:
		b := r.lookup(sy.As.Name.Text)
		if b == nil {
			return "", fmt.Errorf("%s is not an object", sy.As.Name.Text)
		}
		owner, err := r.ownerOf(ctx, b)
		if err != nil {
			return "", err
		}
		if owner != r.sess.Me() {
			return "", fmt.Errorf("cannot speak as the owner of %s: the tester is not its owner", b.name)
		}
		return "the owner of " + b.name, nil
	case SpeakAvatar:
		if !strings.EqualFold(sy.As.Avatar, r.testerName()) {
			return "", fmt.Errorf("this runner drives only the tester")
		}
		return fmt.Sprintf("avatar %q", sy.As.Avatar), nil
	case SpeakSecond:
		return sy.As.Name.Text, nil
	}
	return "", fmt.Errorf("a say cannot be spoken as this")
}

// sayRadius is how far a say is heard, measured: 20 m.
// Why: doc/slate-runner.md#measurements
const sayRadius = 20.0

// stepSentence is a refusal that is printed as its own line, and is also
// the failing step's stimulus detail.
type stepSentence struct{ text string }

func (e *stepSentence) Error() string { return e.text }

// sayRange is the 20 m check: every binding the step names must be within
// the say radius of the tester, or the step fails before the say. It
// reads on the run's context, so it is on no step clock.
// Why: doc/slate-runner.md#say
func (s *stepRun) sayRange(ctx context.Context, sy *Say) error {
	bs := s.namedBindings(sy)
	if len(bs) == 0 {
		return nil
	}
	r := s.r
	tester, exact, err := r.testerPos(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("the tester position could not be read: %v", err)
	}
	if !exact {
		return &stepSentence{fmt.Sprintf("slate: step %d: tester position is not exact; the say was not sent", s.n)}
	}
	all, err := r.sess.Backend().Objects(ctx, "", "")
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("the objects of the region could not be read: %v", err)
	}
	for _, b := range bs {
		// The store's copy, which has moved since setup if the object has.
		cur := b.seen
		for _, o := range all {
			if o.ID == b.seen.ID {
				cur = o
				break
			}
		}
		pos, ok := regionPos(all, cur)
		if !ok {
			return &stepSentence{fmt.Sprintf("slate: step %d: the position of %q is not known; the say was not sent", s.n, b.seen.Name)}
		}
		dx, dy, dz := float64(pos.X-tester.X), float64(pos.Y-tester.Y), float64(pos.Z-tester.Z)
		if d := math.Sqrt(dx*dx + dy*dy + dz*dz); d > sayRadius {
			// Rounded up, so the figure printed is never one the rule allows.
			return &stepSentence{fmt.Sprintf("slate: step %d: %q is %.0f m from the tester; a say is ordinary chat, not as far as the region lookup can see (say radius 20 m: heard at 19.5 m and not at 20.5 m)", s.n, b.seen.Name, math.Ceil(d))}
		}
	}
	return nil
}

// namedBindings is the bindings a say step names, once each: the owner it
// speaks as, then those its expectations name.
func (s *stepRun) namedBindings(sy *Say) []*binding {
	var names []string
	if sy.As != nil && sy.As.Kind == SpeakOwner {
		names = append(names, sy.As.Name.Text)
	}
	for i := range s.st.Expect {
		e := &s.st.Expect[i]
		switch {
		case e.Say != nil && (e.Say.From.Kind == SpeakOwner || e.Say.From.Kind == SpeakObject):
			names = append(names, e.Say.From.Name.Text)
		case e.Dialog != nil:
			names = append(names, e.Dialog.Name.Text)
		case e.TextBox != nil:
			names = append(names, e.TextBox.Name.Text)
		}
	}
	var out []*binding
	seen := map[string]bool{}
	for _, n := range names {
		if b := s.r.lookup(n); b != nil && !seen[n] {
			seen[n] = true
			out = append(out, b)
		}
	}
	return out
}

// textMatcher is a literal, exact and case-sensitive, or a pattern, Go
// regexp and unanchored. Check compiled the pattern once; it is compiled
// again here because the parsed script keeps only the source.
// Why: doc/slate-runner.md#matching-text
func textMatcher(t Text) (textMatch, error) {
	if !t.Pattern {
		want := t.Value
		return textMatch{ok: func(got string) bool { return got == want }}, nil
	}
	re, err := regexp.Compile(t.Value)
	if err != nil {
		return textMatch{}, err
	}
	m := textMatch{ok: re.MatchString}
	for _, n := range re.SubexpNames() {
		if n != "" {
			m.re = re
			break
		}
	}
	return m, nil
}

// chatTypes is the line types a channel written on an expectation accepts.
func chatTypes(c ExpectChan) (func(uint8) bool, error) {
	switch c.Kind {
	case ChanPublic:
		return isPublic, nil
	case ChanOwner:
		return func(t uint8) bool { return t == sl.ChatOwner }, nil
	case ChanDebug:
		return func(t uint8) bool { return t == sl.ChatDebug }, nil
	case ChanDirect:
		return func(t uint8) bool { return t == sl.ChatDirect }, nil
	}
	switch c.Int.Value {
	case 0:
		return isPublic, nil
	case 2147483647:
		return func(t uint8) bool { return t == sl.ChatDebug }, nil
	}
	return nil, fmt.Errorf("channel %d is heard through the bridge, not as chat", c.Int.Value)
}

func isPublic(t uint8) bool {
	return t == sl.ChatWhisper || t == sl.ChatSay || t == sl.ChatShout
}

// sayExpect makes a say expectation. Region chat matches none, and a
// protocol line never does.
func (s *stepRun) sayExpect(ctx context.Context, x *expState) error {
	sx := x.e.Say
	if n, ok := listenChannel(sx.Channel); ok {
		return s.fwdExpect(ctx, x, n)
	}
	types, err := chatTypes(sx.Channel)
	if err != nil {
		return err
	}
	text, err := s.textMatch(sx.Text)
	if err != nil {
		return err
	}
	var why string
	from, err := s.speaker(ctx, sx.From, &why)
	if err != nil {
		return err
	}
	x.noteFn = s.linkNote(&why)
	x.match = func(ev *event) bool {
		if ev.kind != evChat {
			return false
		}
		l := ev.line
		if l.Type == sl.ChatRegion || s.r.isProtocol(l) {
			return false
		}
		return types(l.Type) && text.match(l.Text) && from(l)
	}
	if !x.neg {
		x.onMatch = func(ev *event) {
			v := capValue{typ: CapText, text: ev.line.Text}
			s.bindMatched(x, &v, groupSrc{text, ev.line.Text})
		}
	}
	return nil
}

// linkNote is an expectation's note after "unmatched": why the link of
// its speaker was not resolved, if it was not.
func (s *stepRun) linkNote(why *string) func() string {
	return func() string {
		if *why == "" {
			return ""
		}
		return fmt.Sprintf("; slate: step %d: %s", s.n, *why)
	}
}

// speaker is who a say expectation attributes the line to.
// A link N on an object that has no probe is resolved for each line, and
// why is what it said when it could not be.
func (s *stepRun) speaker(ctx context.Context, sp Speaker, why *string) (func(sl.Line) bool, error) {
	r := s.r
	switch sp.Kind {
	case SpeakTester:
		me := r.sess.Me()
		return func(l sl.Line) bool { return l.Source == me }, nil
	case SpeakOwner:
		b := r.lookup(sp.Name.Text)
		if b == nil {
			return nil, fmt.Errorf("%s is not an object", sp.Name.Text)
		}
		owner, err := r.ownerOf(ctx, b)
		if err != nil {
			return nil, err
		}
		return func(l sl.Line) bool { return l.Source == owner }, nil
	case SpeakAvatar:
		name := sp.Avatar
		return func(l sl.Line) bool { return strings.EqualFold(l.From, name) }, nil
	case SpeakSecond:
		sc := r.secondOf(sp.Name.Text)
		if sc == nil {
			return nil, fmt.Errorf("%s is not an avatar this run was given", sp.Name.Text)
		}
		id := sc.sess.Me()
		return func(l sl.Line) bool { return l.Source == id }, nil
	case SpeakObject:
		b := r.lookup(sp.Name.Text)
		if b == nil {
			return nil, fmt.Errorf("%s is not an object", sp.Name.Text)
		}
		if sp.Link != nil {
			if r.probed(b) {
				lb, err := r.linkBinding(ctx, nil, b, sp.Link.Value)
				if err != nil {
					return nil, err
				}
				return func(l sl.Line) bool { return l.Source == lb.seen.ID }, nil
			}
			return func(l sl.Line) bool {
				lb, err := r.linkBinding(r.ctx, nil, b, sp.Link.Value)
				if err != nil {
					*why = err.Error()
					return false
				}
				*why = ""
				return l.Source == lb.seen.ID
			}, nil
		}
		return func(l sl.Line) bool { return b.owns(l.Source) }, nil
	case SpeakAnyone:
		return func(sl.Line) bool { return true }, nil
	}
	return nil, notYet("this speaker")
}
