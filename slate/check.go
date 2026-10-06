package slate

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// badDuration is the static error for a float written where a duration
// belongs. The wording is fixed: 2.5s gets the same sentence as 1.5s.
const badDuration = "write 1500ms, not 1.5s; a duration is whole digits and a unit, with no dot"

// originError is the static error for at 0 0 and for a drag endpoint 0 0.
// placeTouches treats a zero ST as not given and substitutes the middle.
const originError = "at 0 0 is the middle of the face; placeTouches treats a zero ST as not given (sl/touch.go)"

// notWorn is wornAt for a binding a rez made: in the world, not on anyone.
const notWorn = -1

// Check applies the static rules. It does not dial a region.
// A failure is a *Error at the offending token. The step rules run on each
// test's expanded steps: before each, the test, after each, with every do
// inlined. An error in a sequence is reported where it is written, with
// the test and the do calls in the message.
func Check(s *Script) error {
	if s == nil {
		return &Error{Msg: "no script"}
	}
	c := &checker{
		s:        s,
		declared: map[string]bool{},
		avatars:  map[string]Span{},
		bound:    map[string]Span{},
		names:    map[string]Span{},
		hidden:   map[string]Span{},
		wornAt:   map[string]int{},
		world:    map[string][]worldUse{},
		items:    map[string]Span{},
		gone:     map[string]Span{},
		probes:   map[string]Probe{},
		listens:  map[int32]struct{}{},
	}
	if err := c.headers(); err != nil {
		return err
	}
	c.hdr = c.bound
	if err := c.structure(); err != nil {
		return err
	}
	tests, err := s.Expand()
	if err != nil {
		return err
	}
	for _, et := range tests {
		if err := c.test(et); err != nil {
			return err
		}
	}
	return nil
}

type checker struct {
	s        *Script
	declared map[string]bool        // header bindings, whatever order the headers are in
	avatars  map[string]Span        // avatar headers: not objects, so never in bound
	bound    map[string]Span        // visible in the step being checked
	hdr      map[string]Span        // the header bindings, copied into bound per test
	wornAt   map[string]int         // the attachment point a binding is known to be worn on, or notWorn
	names    map[string]Span        // every name bound in this test, visible or not
	hidden   map[string]Span        // bound in the body: after each cannot see them
	capType  map[string]CaptureType // the type each capture of this test was bound with
	capAll   map[string]bool        // the captures that hold every face, bound by a face all
	curTest  *Test
	suffix   string                // names the test and do calls for a step from a sequence
	world    map[string][]worldUse // in-world names of the object headers, with their descriptions
	items    map[string]Span       // item headers: not bindings of the grid, only wear, rez and drop use them
	gone     map[string]Span       // names taken off in this test, and where
	phase    Phase                 // the part of the test the step being checked came from
	probes   map[string]Probe
	listens  map[int32]struct{}
}

// worldUse is one object header's claim on an in-world name. Key is the
// description as written, with the pattern marker; it is empty without one.
type worldUse struct {
	span Span
	has  bool
	key  string
}

func (c *checker) err(sp Span, format string, args ...any) error {
	return &Error{File: c.s.File, Line: sp.Line, Column: sp.Col, Msg: fmt.Sprintf(format, args...) + c.suffix}
}

func copySpans(m map[string]Span) map[string]Span {
	out := make(map[string]Span, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// structure checks what does not depend on a test's steps: how many
// before each and after each, the names of tests and sequences, and every
// do, including those in sequences no test calls.
func (c *checker) structure() error {
	s := c.s
	if len(s.Befores) > 1 {
		return c.err(s.Befores[1].Span, "before each appears more than once")
	}
	if len(s.Afters) > 1 {
		return c.err(s.Afters[1].Span, "after each appears more than once")
	}
	seqs := map[string]Span{}
	for _, q := range s.Sequences {
		if prev, ok := seqs[q.Name.Text]; ok {
			return c.err(q.Name.Span, "sequence %s is already defined at line %d", q.Name.Text, prev.Line)
		}
		seqs[q.Name.Text] = q.Name.Span
	}
	if len(s.Tests) == 0 {
		return c.err(Span{Line: 1, Col: 1}, "a file needs at least one test")
	}
	tests := map[string]Span{}
	for _, t := range s.Tests {
		if t.Implicit {
			continue
		}
		if t.Name == "" {
			return c.err(t.NameSpan, "a test name is empty")
		}
		if prev, ok := tests[t.Name]; ok {
			return c.err(t.NameSpan, "test %q is already defined at line %d", t.Name, prev.Line)
		}
		tests[t.Name] = t.NameSpan
	}
	for _, q := range s.Sequences {
		if err := s.inline(q.Steps, PhaseBody, q.Name.Text, nil, []string{q.Name.Text}, nil); err != nil {
			return err
		}
	}
	for _, b := range append(append([]Block{}, s.Befores...), s.Afters...) {
		if err := s.inline(b.Steps, PhaseBody, "", nil, nil, nil); err != nil {
			return err
		}
	}
	for _, t := range s.Tests {
		if err := s.inline(t.Steps, PhaseBody, "", nil, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

// test checks one test's expanded steps. A name bound in before each is
// visible to the body and after each; one bound in the body is not visible
// to after each; one a sequence binds is visible to what follows its do.
func (c *checker) test(et ExpandedTest) error {
	c.curTest = et.Test
	c.bound = copySpans(c.hdr)
	c.names = copySpans(c.hdr)
	for n, sp := range c.avatars {
		c.names[n] = sp // an as cannot bind a name an avatar has
	}
	c.hidden = map[string]Span{}
	c.gone = map[string]Span{}
	c.capType = map[string]CaptureType{}
	c.capAll = map[string]bool{}
	c.wornAt = map[string]int{}
	defer func() { c.suffix = "" }()
	var snap map[string]Span // what the body starts with
	phase := Phase(-1)
	for i, x := range et.Steps {
		c.phase = x.Phase
		if x.Phase != phase {
			switch x.Phase {
			case PhaseBody:
				snap = copySpans(c.bound)
			case PhaseAfter:
				if snap == nil {
					snap = copySpans(c.bound)
				}
				for n, sp := range c.bound {
					if _, ok := snap[n]; !ok {
						c.hidden[n] = sp
					}
				}
				c.bound = copySpans(snap)
			}
			phase = x.Phase
		}
		c.suffix = ""
		if len(x.Via) > 0 {
			c.suffix = fmt.Sprintf(" (in test %q, %s)", et.Test.Name, Via(x.Via))
		}
		if err := c.step(*x.Step); err != nil {
			return err
		}
		if g := guardOf(x.Step); g != nil && !followedByPositive(et.Steps, i) {
			return c.err(*g, "a guarded touch must be followed, in the same block, by a step with a positive expectation: the state the block drives towards, so that a misspelt label fails there and is not skipped every run")
		}
	}
	return nil
}

// guardOf is the if shown of a step's touch, or nil.
func guardOf(st *Step) *Span {
	if st.Stimulus == nil || st.Stimulus.Touch == nil {
		return nil
	}
	return st.Stimulus.Touch.Guard
}

// followedByPositive reports whether the step after steps[i] is of the same
// block and holds a positive expectation.
func followedByPositive(steps []ExpandedStep, i int) bool {
	if i+1 >= len(steps) || steps[i+1].Phase != steps[i].Phase {
		return false
	}
	for _, e := range steps[i+1].Step.Expect {
		if !e.Neg {
			return true
		}
	}
	return false
}

type placed struct {
	at  int
	run func() error
}

func (c *checker) headers() error {
	// A probe may be written before the object it names. The binding
	// still has to be a header, not a name a later step binds with as.
	for _, o := range c.s.Objects {
		c.declared[o.Name.Text] = true
	}
	var items []placed
	for i := range c.s.Timeouts {
		i := i
		items = append(items, placed{c.s.Timeouts[i].Span.Start, func() error { return c.timeoutAt(i) }})
	}
	for i := range c.s.Allows {
		i := i
		items = append(items, placed{c.s.Allows[i].Start, func() error { return c.allowAt(i) }})
	}
	for i := range c.s.Objects {
		i := i
		items = append(items, placed{c.s.Objects[i].Span.Start, func() error { return c.objectAt(i) }})
	}
	for i := range c.s.Avatars {
		i := i
		items = append(items, placed{c.s.Avatars[i].Span.Start, func() error { return c.avatarAt(i) }})
	}
	for i := range c.s.Items {
		i := i
		items = append(items, placed{c.s.Items[i].Span.Start, func() error { return c.itemAt(i) }})
	}
	for i := range c.s.Probes {
		i := i
		items = append(items, placed{c.s.Probes[i].Span.Start, func() error { return c.probeAt(i) }})
	}
	for i := range c.s.Listens {
		i := i
		items = append(items, placed{c.s.Listens[i].Span.Start, func() error { return c.listenAt(i) }})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].at < items[j].at })
	for _, it := range items {
		if err := it.run(); err != nil {
			return err
		}
	}
	return c.checkPermits()
}

func (c *checker) timeoutAt(i int) error {
	d := &c.s.Timeouts[i]
	if err := c.duration(d); err != nil {
		return err
	}
	if i > 0 {
		return c.err(d.Span, "timeout is set twice")
	}
	return nil
}

func (c *checker) allowAt(i int) error {
	if i > 0 {
		return c.err(c.s.Allows[i], "allow pay is set twice")
	}
	return nil
}

func (c *checker) objectAt(i int) error {
	o := c.s.Objects[i]
	if _, ok := c.bound[o.Name.Text]; ok {
		return c.err(o.Name.Span, "%s is already bound", o.Name.Text)
	}
	if _, ok := c.avatars[o.Name.Text]; ok {
		return c.err(o.Name.Span, "%s is already bound", o.Name.Text)
	}
	if _, ok := c.items[o.Name.Text]; ok {
		return c.err(o.Name.Span, "%s is already bound", o.Name.Text)
	}
	if o.World == "" {
		return c.err(o.WorldSpan, "in-world name is empty")
	}
	use := worldUse{span: o.WorldSpan}
	if o.HasDesc {
		if o.Desc.Capture != nil {
			return c.err(o.Desc.ValueSpan, "a description is read at setup, before any step; a capture cannot be used here")
		}
		if err := c.pattern(o.Desc); err != nil {
			return err
		}
		use.has, use.key = true, o.Desc.Value
		if o.Desc.Pattern {
			use.key = "matching " + use.key
		}
	}
	// Objects of one name are told apart by description, so a name may be
	// written again only when every header that has it has a description of
	// its own.
	for _, u := range c.world[o.World] {
		if !use.has || !u.has {
			return c.err(o.WorldSpan, "in-world name %q is already used; objects of one name need a description each", o.World)
		}
		if u.key == use.key {
			return c.err(o.WorldSpan, "in-world name %q is already used with that description", o.World)
		}
	}
	c.bound[o.Name.Text] = o.Name.Span
	c.world[o.World] = append(c.world[o.World], use)
	return nil
}

// avatarAt checks an avatar header: a name nothing else has.
// Why: doc/slate-language.md#a-second-avatar
func (c *checker) avatarAt(i int) error {
	a := c.s.Avatars[i]
	n := a.Name.Text
	_, obj := c.bound[n]
	_, item := c.items[n]
	_, twice := c.avatars[n]
	if obj || item || twice {
		return c.err(a.Name.Span, "%s is already bound", n)
	}
	c.avatars[n] = a.Name.Span
	return nil
}

// avatarRef checks a name that must be a second avatar's binding.
func (c *checker) avatarRef(id Ident) error {
	if _, ok := c.avatars[id.Text]; ok {
		return nil
	}
	_, obj := c.bound[id.Text]
	_, item := c.items[id.Text]
	switch {
	case obj:
		return c.err(id.Span, "%s is an object, not an avatar; as and to take an avatar header", id.Text)
	case item:
		return c.err(id.Span, "%s is an item, not an avatar", id.Text)
	}
	return c.err(id.Span, "%s is not an avatar; declare it with avatar %s and give it with --avatar", id.Text, id.Text)
}

func (c *checker) itemAt(i int) error {
	it := c.s.Items[i]
	if _, ok := c.bound[it.Name.Text]; ok {
		return c.err(it.Name.Span, "%s is already bound", it.Name.Text)
	}
	if _, ok := c.avatars[it.Name.Text]; ok {
		return c.err(it.Name.Span, "%s is already bound", it.Name.Text)
	}
	if _, ok := c.items[it.Name.Text]; ok {
		return c.err(it.Name.Span, "%s is already bound", it.Name.Text)
	}
	if strings.TrimSpace(it.World) == "" {
		return c.err(it.WorldSpan, "item name is empty")
	}
	if strings.TrimSpace(it.Folder) == "" {
		return c.err(it.FolderSpan, "folder name is empty")
	}
	c.items[it.Name.Text] = it.Name.Span
	return nil
}

// attachPoint checks the string of wear ... on and attached ... on: a point
// sl knows. A name it does not know is not guessed at.
func (c *checker) attachPoint(name string, sp Span) error {
	if _, ok := sl.AttachPointNamed(name); !ok {
		return c.err(sp, "%q is not an attachment point sl knows; write one by name, such as \"HUD Top Left\" or \"Chest\"", name)
	}
	return nil
}

func (c *checker) probeAt(i int) error {
	pr := c.s.Probes[i]
	if !c.declared[pr.Name.Text] {
		return c.err(pr.Name.Span, "%s is not an object", pr.Name.Text)
	}
	if _, ok := c.probes[pr.Name.Text]; ok {
		return c.err(pr.Name.Span, "%s already has a probe", pr.Name.Text)
	}
	c.probes[pr.Name.Text] = pr
	return nil
}

func (c *checker) listenAt(i int) error {
	ln := c.s.Listens[i]
	v, err := c.fit(ln.Channel, "listen")
	if err != nil {
		return err
	}
	if v == 0 {
		return c.err(ln.Channel.Span, "listen 0 is public chat, not a bridge channel")
	}
	if v == math.MaxInt32 {
		return c.err(ln.Channel.Span, "listen 2147483647 is debug chat, not a bridge channel")
	}
	if _, ok := c.listens[v]; ok {
		return c.err(ln.Channel.Span, "channel %d is already used", v)
	}
	if len(c.listens) >= maxListens {
		return c.err(ln.Channel.Span, "the bridge opens at most %d listens", maxListens)
	}
	c.listens[v] = struct{}{}
	return nil
}

func (c *checker) step(st Step) error {
	same := map[string]bool{}
	for _, e := range st.Expect {
		if e.Rez != nil && !e.Neg && e.Rez.As.Text != "" {
			same[e.Rez.As.Text] = true
		}
	}
	// A capture is bound under "$name" in the same maps as an object, so
	// it has the scope of an as name, and no object name can clash with it.
	caps, err := c.captureBinds(st)
	if err != nil {
		return err
	}
	for _, b := range caps {
		same["$"+b.name] = true
	}
	if st.Stimulus != nil {
		if err := c.stimulus(st.Stimulus, same); err != nil {
			return err
		}
	}
	if g := guardOf(&st); g != nil && len(st.Expect) > 0 {
		return c.err(st.Expect[0].Span, "a guarded touch has no expectations; put them in the next step")
	}
	if d := st.Stimulus; d != nil && d.Drag != nil && d.Drag.Screen == nil && d.Drag.Over != nil && !d.Drag.Over.Bad &&
		d.Drag.Press == nil && d.Drag.Dwell == nil {
		if budget := c.budget(st); d.Drag.Over.Value > budget {
			return c.err(d.Drag.Over.Span, "drag over %s is longer than the step's budget of %s, the longest within or the timeout", c.s.Source(d.Drag.Over.Span), budget)
		}
	}
	if d := st.Stimulus; d != nil && d.Drag != nil && d.Drag.Screen == nil && (d.Drag.Press != nil || d.Drag.Dwell != nil) {
		if total, what, ok := dragTotal(d.Drag); ok {
			if budget := c.budget(st); total > budget {
				return c.err(d.Span, "%s can take %s, which is longer than the step's budget of %s, the longest within or the timeout", what, total, budget)
			}
		}
	}
	if err := c.screenBudget(st); err != nil {
		return err
	}
	if w := st.Stimulus; w != nil && w.Wait != nil && !w.Wait.For.Bad {
		if budget := c.budget(st); w.Wait.For.Value > budget {
			return c.err(w.Wait.For.Span, "wait %s is longer than the step's budget of %s, the longest within or the timeout", c.s.Source(w.Wait.For.Span), budget)
		}
	}
	seenAs := map[string]Span{}
	for _, e := range st.Expect {
		if err := c.expect(e, same, seenAs); err != nil {
			return err
		}
	}
	for name := range same {
		if strings.HasPrefix(name, "$") {
			continue
		}
		c.bound[name] = seenAs[name]
		c.names[name] = seenAs[name]
		c.wornAt[name] = notWorn
	}
	for _, b := range caps {
		c.bound["$"+b.name] = b.span
		c.names["$"+b.name] = b.span
		c.capType[b.name] = b.typ
		c.capAll[b.name] = b.all
	}
	// A take off ends the name's use from the next step on: this step's own
	// expectations, such as attached NAME off, are what it is for.
	if st.Stimulus != nil && st.Stimulus.TakeOff != nil {
		n := st.Stimulus.TakeOff.Name
		c.gone[n.Text] = n.Span
	}
	return nil
}

// budget is how long a blocking stimulus in st may take: the longest
// within, else the script's timeout.
// Why: doc/slate-runner.md#rules
func (c *checker) budget(st Step) time.Duration {
	var b time.Duration
	for _, e := range st.Expect {
		if e.Within != nil && !e.Within.Bad && e.Within.Value > b {
			b = e.Within.Value
		}
	}
	if b == 0 {
		b = c.s.TimeoutDuration()
	}
	return b
}

func (c *checker) stimulus(s *Stimulus, same map[string]bool) error {
	if s.OtherAs != nil && s.Group != nil {
		return c.err(s.OtherAs.Span, "group names the avatar whose group is set (group NAME \"Group Name\"); an as does not follow it")
	}
	if s.OtherAs != nil {
		return c.err(s.OtherAs.Span, "%s stays the tester's: a second avatar only touches, drags a face, says, chooses and answers (a second avatar never pays)", stimulusWord(s))
	}
	switch {
	case s.Touch != nil:
		return c.touch(s.Touch, same)
	case s.Drag != nil:
		return c.drag(s.Drag, same)
	case s.Say != nil:
		return c.sayStim(s.Say, same)
	case s.Pay != nil:
		return c.pay(s.Pay, s.Span, same)
	case s.Sit != nil:
		return c.ref(s.Sit.Name, same)
	case s.Stand != nil:
		return nil
	case s.Wait != nil:
		return c.duration(&s.Wait.For)
	case s.Choose != nil:
		return c.choose(s.Choose, same)
	case s.Answer != nil:
		return c.answer(s.Answer, same)
	case s.Send != nil:
		return c.send(s.Send, same)
	case s.Wear != nil:
		return c.wear(s.Wear, same)
	case s.Rez != nil:
		return c.rezItem(s.Rez)
	case s.TakeOff != nil:
		return c.ref(s.TakeOff.Name, same)
	case s.Drop != nil:
		return c.drop(s.Drop, same)
	case s.Group != nil:
		return c.setGroup(s.Group)
	default:
		return c.err(s.Span, "stimulus has no body")
	}
}

// stimulusWord names a stimulus for an error.
func stimulusWord(s *Stimulus) string {
	switch {
	case s.Pay != nil:
		return "pay"
	case s.Sit != nil:
		return "sit"
	case s.Stand != nil:
		return "stand"
	case s.Wait != nil:
		return "wait"
	case s.Send != nil:
		return "send"
	case s.Wear != nil:
		return "wear"
	case s.Rez != nil:
		return "rez"
	case s.TakeOff != nil:
		return "take off"
	case s.Drop != nil:
		return "drop"
	case s.Drag != nil && s.Drag.Screen != nil:
		return "drag on screen"
	}
	return "this stimulus"
}

// wear checks wear ITEM on "point" as NAME. NAME is a new object binding,
// and it exists as soon as the stimulus returns, so this step's own
// expectations may use it: the one name a step binds that the step sees.
// Why: doc/slate-language.md#static-checks
func (c *checker) wear(w *Wear, same map[string]bool) error {
	if _, ok := c.items[w.Item.Text]; !ok {
		if _, obj := c.bound[w.Item.Text]; obj {
			return c.err(w.Item.Span, "%s is an object; wear takes an item (item NAME is \"item\" in \"folder\")", w.Item.Text)
		}
		return c.err(w.Item.Span, "%s is not an item", w.Item.Text)
	}
	if err := c.attachPoint(w.Point, w.PointSpan); err != nil {
		return err
	}
	as := w.As.Text
	if _, ok := c.avatars[as]; ok {
		return c.err(w.As.Span, "%s is a second avatar, and a second avatar never wears; as here names the worn object", as)
	}
	if _, ok := c.names[as]; ok {
		return c.err(w.As.Span, "%s is already bound", as)
	}
	if _, ok := c.items[as]; ok {
		return c.err(w.As.Span, "%s is already bound", as)
	}
	c.bound[as] = w.As.Span
	c.names[as] = w.As.Span
	if point, ok := sl.AttachPointNamed(w.Point); ok {
		c.wornAt[as] = point
	}
	return nil
}

// rezItem checks rez ITEM (at | by) X Y Z as NAME. ITEM is an item header,
// the numbers are exact, and NAME is a new object binding made as wear's
// is, so this step's own expectations may use it.
// Why: doc/slate-language.md#static-checks
func (c *checker) rezItem(r *RezItem) error {
	if _, ok := c.items[r.Item.Text]; !ok {
		if _, obj := c.bound[r.Item.Text]; obj {
			return c.err(r.Item.Span, "%s is an object; rez takes an item (item NAME is \"item\" in \"folder\")", r.Item.Text)
		}
		return c.err(r.Item.Span, "%s is not an item", r.Item.Text)
	}
	for _, n := range []Number{r.X, r.Y, r.Z} {
		if err := c.exact(n); err != nil {
			return err
		}
	}
	as := r.As.Text
	if _, ok := c.avatars[as]; ok {
		return c.err(r.As.Span, "%s is a second avatar, and a second avatar never rezzes; as here names the rezzed object", as)
	}
	if _, ok := c.names[as]; ok {
		return c.err(r.As.Span, "%s is already bound", as)
	}
	if _, ok := c.items[as]; ok {
		return c.err(r.As.Span, "%s is already bound", as)
	}
	c.bound[as] = r.As.Span
	c.names[as] = r.As.Span
	c.wornAt[as] = notWorn
	return nil
}

// drop checks drop ITEM into OBJ and drop ITEM onto OBJ face N. ITEM is an
// item header, OBJ an object binding. A drop is the tester's alone: the
// as of a second avatar is refused by stimulus.
// Why: doc/slate-language.md#static-checks
func (c *checker) drop(d *Drop, same map[string]bool) error {
	if _, ok := c.items[d.Item.Text]; !ok {
		if _, obj := c.bound[d.Item.Text]; obj {
			return c.err(d.Item.Span, "%s is an object; drop takes an item (item NAME is \"item\" in \"folder\")", d.Item.Text)
		}
		return c.err(d.Item.Span, "%s is not an item", d.Item.Text)
	}
	if err := c.ref(d.Name, same); err != nil {
		return err
	}
	if d.Link != nil {
		if err := c.linkRange(*d.Link); err != nil {
			return err
		}
	}
	if d.Onto {
		v, err := c.fit(d.Face, "face")
		if err != nil {
			return err
		}
		if v < 0 {
			return c.err(d.Face.Span, "face %d is below 0", v)
		}
	}
	return nil
}

// setGroup checks group NAME "Group Name" and group NAME none: NAME is a
// second avatar, never the tester, whose active group decides where it
// may build and which a test does not change.
// Why: doc/slate-language.md#static-checks
func (c *checker) setGroup(g *SetGroup) error {
	if g.Avatar.Text == "tester" {
		return c.err(g.Avatar.Span, "group stays off the tester: its active group decides where it may build, and a test does not change it; group takes a second avatar")
	}
	if err := c.avatarRef(g.Avatar); err != nil {
		return err
	}
	if !g.None && g.Group == "" {
		return c.err(g.GroupSpan, "a group is named by a non-empty string, or none")
	}
	return nil
}

func (c *checker) touch(t *Touch, same map[string]bool) error {
	if err := c.ref(t.Name, same); err != nil {
		return err
	}
	if t.AsAvatar != nil {
		if err := c.avatarRef(*t.AsAvatar); err != nil {
			return err
		}
	}
	if t.Guard != nil {
		if t.Button == nil {
			return c.err(*t.Guard, "if shown guards a touch of a button; this touch has no button")
		}
		if c.phase == PhaseBody {
			return c.err(*t.Guard, "if shown is legal only in before each and after each: a test is one path, and a guard that may skip its touch makes two")
		}
		if t.Button.Nth != nil {
			return c.err(t.Button.Nth.Span, "button N is not legal with if shown: the guard counts the buttons found, not one of them")
		}
	}
	if t.Showing != nil {
		return c.showing(t, same)
	}
	if t.Link != nil {
		if err := c.linkRange(*t.Link); err != nil {
			return err
		}
	}
	if t.Face != nil {
		if _, err := c.fit(*t.Face, "face"); err != nil {
			return err
		}
	}
	if t.At != nil {
		if err := c.origin(*t.At); err != nil {
			return err
		}
	}
	if t.Button != nil {
		return c.button(t.Button, same)
	}
	return nil
}

// showing checks touch OBJ showing: it searches the whole linkset for the
// face, so it names no link, face or button of its own.
// Why: doc/slate-language.md#stimuli
func (c *checker) showing(t *Touch, same map[string]bool) error {
	sh := t.Showing
	for _, o := range []struct {
		set  bool
		what string
	}{{t.Link != nil, "link"}, {t.Face != nil, "face"}, {t.Button != nil, "button"}, {t.Anywhere, "anywhere"}} {
		if o.set {
			return c.err(sh.Span, "showing cannot be combined with %s; it finds the prim and the face itself", o.what)
		}
	}
	if sh.Use != nil {
		if err := c.useCap(sh.Use, CapUUID, same); err != nil {
			return err
		}
	}
	if sh.At != nil {
		return c.origin(*sh.At)
	}
	return nil
}

// choose checks the button a choose names.
func (c *checker) choose(ch *Choose, same map[string]bool) error {
	if err := c.ref(ch.Name, same); err != nil {
		return err
	}
	if ch.AsAvatar != nil {
		if err := c.avatarRef(*ch.AsAvatar); err != nil {
			return err
		}
	}
	switch ch.Kind {
	case ChooseMatching:
		if _, err := regexp.Compile(ch.Label); err != nil {
			return c.err(ch.LabelSpan, "pattern %q: %s", ch.Label, err.Error())
		}
	case ChooseButton:
		return c.buttonIndex(*ch.Index)
	case ChooseCapture:
		return c.useCap(ch.Use, CapText, same)
	}
	return nil
}

// maxDialogButtons is the most buttons llDialog accepts: published, not
// measured.
// Why: doc/slate-language.md#expectations
const maxDialogButtons = 12

// buttonIndex checks a button N of a dialog: 1 to 12.
func (c *checker) buttonIndex(n Int) error {
	v, err := c.fit(n, "button")
	if err != nil {
		return err
	}
	if v < 1 || v > maxDialogButtons {
		return c.err(n.Span, "button %d is outside 1 to %d, the most buttons llDialog accepts", v, maxDialogButtons)
	}
	return nil
}

func (c *checker) button(b *Button, same map[string]bool) error {
	if b.Nth != nil {
		v, err := c.fit(*b.Nth, "button")
		if err != nil {
			return err
		}
		if v < 1 {
			return c.err(b.Nth.Span, "button %d is not a match; the first is 1", v)
		}
	}
	if len(b.Parts) == 0 {
		return c.err(Span{}, "a button needs a part")
	}
	for _, part := range b.Parts {
		switch part.Kind {
		case PartText:
			if part.Capture != nil {
				if err := c.useCap(part.Capture, CapText, same); err != nil {
					return err
				}
			} else if strings.TrimSpace(part.Text) == "" {
				return c.err(part.Span, "button text is empty")
			}
		case PartPattern:
			if _, err := regexp.Compile(part.Text); err != nil {
				return c.err(part.Span, "pattern %q: %s", part.Text, err.Error())
			}
		}
	}
	if b.Face != nil {
		if _, err := c.fit(*b.Face, "face"); err != nil {
			return err
		}
	}
	return nil
}

// screenBudget is the budget rule of a drag on the screen: its move, and
// the wait for the HUD to change when it settles, must fit the step.
// Why: doc/slate-language.md#static-checks
func (c *checker) screenBudget(st Step) error {
	d := st.Stimulus
	if d == nil || d.Drag == nil || d.Drag.Screen == nil {
		return nil
	}
	sd := d.Drag.Screen
	if d.Drag.Over == nil && d.Drag.Press == nil && d.Drag.Dwell == nil && !sd.Settle {
		return nil
	}
	total, what, ok := dragTotal(d.Drag)
	if !ok {
		return nil
	}
	if sd.Settle {
		total += sl.DefaultHUDChangeTimeout
		what += " and settle (up to " + sl.DefaultHUDChangeTimeout.String() + ")"
	}
	if budget := c.budget(st); total > budget {
		return c.err(sd.Span, "%s can take %s, which is longer than the step's budget of %s, the longest within or the timeout", what, total, budget)
	}
	return nil
}

// screenDrag checks drag OBJ on screen: the binding is worn on a HUD
// point when the script says how it was put on, and the numbers are
// finite.
// Why: doc/slate-language.md#static-checks
func (c *checker) screenDrag(d *Drag, sd *ScreenDrag) error {
	if at, ok := c.wornAt[d.Name.Text]; ok && !sl.IsHUDPoint(at) {
		if at == notWorn {
			return c.err(d.Name.Span, "%s is rezzed in the world; drag on screen needs an object worn on a HUD point", d.Name.Text)
		}
		return c.err(d.Name.Span, "%s is worn on %s; drag on screen needs an object worn on a HUD point", d.Name.Text, sl.AttachPointName(at))
	}
	if sd.Face != nil {
		if _, err := c.fit(*sd.Face, "face"); err != nil {
			return err
		}
		if sd.Link != nil {
			if err := c.linkRange(*sd.Link); err != nil {
				return err
			}
		}
		for _, n := range []Number{sd.At.S, sd.At.T} {
			if err := c.exact(n); err != nil {
				return err
			}
		}
	} else {
		for _, n := range []Number{sd.FromPixels.S, sd.FromPixels.T} {
			if err := c.exact(n); err != nil {
				return err
			}
		}
	}
	for _, n := range []Number{sd.To.S, sd.To.T} {
		if err := c.exact(n); err != nil {
			return err
		}
	}
	return nil
}

func (c *checker) drag(d *Drag, same map[string]bool) error {
	if err := c.ref(d.Name, same); err != nil {
		return err
	}
	if d.AsAvatar != nil {
		if err := c.avatarRef(*d.AsAvatar); err != nil {
			return err
		}
	}
	if d.Screen != nil {
		if err := c.screenDrag(d, d.Screen); err != nil {
			return err
		}
		return c.dragTimes(d)
	}
	if d.Link != nil {
		if err := c.linkRange(*d.Link); err != nil {
			return err
		}
	}
	if _, err := c.fit(d.Face, "face"); err != nil {
		return err
	}
	if err := c.origin(d.From); err != nil {
		return err
	}
	if err := c.origin(d.To); err != nil {
		return err
	}
	return c.dragTimes(d)
}

// dragTimes checks a drag's over, press and dwell.
func (c *checker) dragTimes(d *Drag) error {
	for _, t := range []*Duration{d.Over, d.Press, d.Dwell} {
		if err := c.duration(t); err != nil {
			return err
		}
	}
	return nil
}

func (c *checker) sayStim(s *Say, same map[string]bool) error {
	if s.As != nil {
		if err := c.speaker(*s.As, same); err != nil {
			return err
		}
	}
	ch, err := c.fit(s.Channel, "channel")
	if err != nil {
		return err
	}
	return c.sayLen(s.Text, s.TextSpan, ch)
}

func (c *checker) sayLen(text string, sp Span, ch int32) error {
	n := len(text)
	if ch < 0 {
		if n > sl.MaxDialogReply {
			return c.err(sp, "say is %d bytes; a negative channel carries at most %d", n, sl.MaxDialogReply)
		}
		return nil
	}
	if n > maxSay {
		return c.err(sp, "say is %d bytes; a channel carries at most %d", n, maxSay)
	}
	return nil
}

func (c *checker) pay(p *Pay, sp Span, same map[string]bool) error {
	if len(c.s.Allows) == 0 {
		return c.err(sp, "pay needs allow pay")
	}
	if err := c.ref(p.Name, same); err != nil {
		return err
	}
	if !p.Amount.OK || p.Amount.Value < int64(math.MinInt) || p.Amount.Value > int64(math.MaxInt) {
		return c.err(p.Amount.Span, "amount %s does not fit in an integer", p.Amount.Text)
	}
	if p.Amount.Value < 1 {
		return c.err(p.Amount.Span, "a payment is at least L$1")
	}
	if p.HasReason && len(p.Reason) > sl.MaxPayReason {
		return c.err(p.ReasonSpan, "the reason is %d bytes and a payment carries %d", len(p.Reason), sl.MaxPayReason)
	}
	return nil
}

func (c *checker) answer(a *Answer, same map[string]bool) error {
	if err := c.ref(a.Name, same); err != nil {
		return err
	}
	if a.AsAvatar != nil {
		if err := c.avatarRef(*a.AsAvatar); err != nil {
			return err
		}
	}
	if n := len(a.Text); n > sl.MaxDialogReply {
		return c.err(a.TextSpan, "answer is %d bytes; a text box carries at most %d", n, sl.MaxDialogReply)
	}
	return nil
}

func (c *checker) send(s *Send, same map[string]bool) error {
	if err := c.ref(s.Name, same); err != nil {
		return err
	}
	if err := c.needProbe(s.Name, s.Name.Span, "send on"); err != nil {
		return err
	}
	from, err := c.fit(s.From, "link")
	if err != nil {
		return err
	}
	target, err := c.linkTarget(s.To)
	if err != nil {
		return err
	}
	num, err := c.fit(s.Num, "num")
	if err != nil {
		return err
	}
	if s.TextCapture != nil {
		if err := c.useCap(s.TextCapture, CapText, same); err != nil {
			return err
		}
	} else if err := c.ascii(s.Text, s.TextSpan); err != nil {
		return err
	}
	if s.Key != nil && s.Key.Use != nil {
		if err := c.useCap(s.Key.Use, CapUUID, same); err != nil {
			return err
		}
	}
	key := nullKey
	if s.Key != nil && s.Key.ID != "" {
		key = s.Key.ID
	}
	if n := len(relayLine(from, target, num, key, s.Text)); n > maxSay {
		return c.err(s.TextSpan, "relayed command is %d bytes; the line to the bridge carries at most %d", n, maxSay)
	}
	return nil
}

func (c *checker) linkTarget(t LinkTarget) (int32, error) {
	if t.Word != "" {
		v, ok := LinkWords[t.Word]
		if !ok {
			return 0, c.err(t.Span, "%s is not a link target", t.Word)
		}
		return v, nil
	}
	return c.fit(t.Int, "link")
}

func (c *checker) expect(e Expect, same map[string]bool, seenAs map[string]Span) error {
	if err := c.duration(e.Within); err != nil {
		return err
	}
	if err := c.near(e); err != nil {
		return err
	}
	switch {
	case e.Say != nil:
		return c.sayExp(e.Say, same)
	case e.Dialog != nil:
		return c.dialog(e.Dialog, same)
	case e.TextBox != nil:
		if err := c.toRef(e.TextBox.To); err != nil {
			return err
		}
		return c.dialogLike(e.TextBox.Name, e.TextBox.Link, e.TextBox.Text, same)
	case e.Texture != nil:
		x := e.Texture
		if err := c.faceRef(x.Name, x.Link, x.Face, x.FaceAll, same); err != nil {
			return err
		}
		_, err := c.reading(e, x.State, x.Any, x.Use, CapUUID, same)
		return err
	case e.Offset != nil:
		return c.vec(e, e.Offset, same, true)
	case e.Repeats != nil:
		return c.vec(e, e.Repeats, same, false)
	case e.Rot != nil:
		return c.rot(e, e.Rot, same)
	case e.Click != nil:
		x := e.Click
		if err := c.ref(x.Name, same); err != nil {
			return err
		}
		if err := c.linkN(x.Name, x.Link); err != nil {
			return err
		}
		_, err := c.reading(e, x.State, x.Any, x.Use, CapClick, same)
		return err
	case e.FloatText != nil:
		x := e.FloatText
		if err := c.ref(x.Name, same); err != nil {
			return err
		}
		if err := c.linkN(x.Name, x.Link); err != nil {
			return err
		}
		lit, err := c.reading(e, x.State, x.Any, x.Value.Capture, CapText, same)
		if err != nil || !lit {
			return err
		}
		return c.pattern(x.Value)
	case e.Position != nil:
		return c.vec3(e, e.Position, "position", same)
	case e.Size != nil:
		return c.vec3(e, e.Size, "size", same)
	case e.Turn != nil:
		return c.vec3(e, e.Turn, "turn", same)
	case e.Fullbright != nil:
		x := e.Fullbright
		if err := c.faceRef(x.Name, x.Link, x.Face, x.FaceAll, same); err != nil {
			return err
		}
		_, err := c.reading(e, x.State, x.Any, x.Use, CapOnOff, same)
		return err
	case e.AlphaMode != nil:
		x := e.AlphaMode
		if x.FaceAll {
			return c.err(x.Face.Span, "alphamode reads one face's material at a time; there is no face all")
		}
		return c.faceRef(x.Name, x.Link, x.Face, false, same)
	case e.Material != nil:
		return c.material(e, e.Material, same)
	case e.Glow != nil:
		x := e.Glow
		return c.level(e, "glow", x.Name, x.Link, x.Face, x.FaceAll, x.State, x.Any, x.Use, same, x.Value)
	case e.Alpha != nil:
		x := e.Alpha
		return c.level(e, "alpha", x.Name, x.Link, x.Face, x.FaceAll, x.State, x.Any, x.Use, same, x.Value)
	case e.Colour != nil:
		x := e.Colour
		return c.level(e, "colour", x.Name, x.Link, x.Face, x.FaceAll, x.State, x.Any, x.Use, same, x.R, x.G, x.B)
	case e.Give != nil:
		if err := c.toRef(e.Give.To); err != nil {
			return err
		}
		if err := c.ref(e.Give.From, same); err != nil {
			return err
		}
		if err := c.textVal(e.Give.Item, same); err != nil {
			return err
		}
		for _, h := range e.Give.Holding {
			if err := c.textVal(h, same); err != nil {
				return err
			}
			if h.Pattern {
				gs, err := c.groups(h)
				if err != nil {
					return err
				}
				if len(gs) > 0 {
					return c.err(h.ValueSpan, "a holding pattern binds nothing: a named group in it has no capture to go to")
				}
			}
		}
		return nil
	case e.Rez != nil:
		return c.rez(e, seenAs, same)
	case e.Link != nil:
		return c.linkExp(e.Link, same)
	case e.Button != nil:
		return c.buttonExp(e.Button, same)
	case e.Attached != nil:
		if err := c.ref(e.Attached.Name, same); err != nil {
			return err
		}
		if e.Attached.Off {
			return nil
		}
		return c.attachPoint(e.Attached.Point, e.Attached.PointSpan)
	default:
		return c.err(e.Span, "expectation has no body")
	}
}

// nearWord is what an expectation that takes no tolerance is called in
// the sentence that refuses near.
func nearWord(e Expect) string {
	switch {
	case e.Say != nil:
		return "say"
	case e.Dialog != nil:
		return "dialog"
	case e.TextBox != nil:
		return "textbox"
	case e.Give != nil:
		return "give"
	case e.Rez != nil:
		return "rez"
	case e.Link != nil:
		return "link"
	case e.Attached != nil:
		return "attached"
	case e.Texture != nil:
		return "texture"
	case e.Click != nil:
		return "click"
	case e.FloatText != nil:
		return "text"
	case e.Fullbright != nil:
		return "fullbright"
	case e.AlphaMode != nil:
		return "alphamode"
	case e.Material != nil && e.Material.IsMap():
		return e.Material.Prop
	case e.Button != nil:
		return "button"
	}
	return ""
}

// near checks the tolerance of an expectation: only the numeric state
// readings take one, it is above 0 (a percentage up to 100), and it is
// not written beside `is any`, which asserts nothing to be near.
// Why: doc/slate-language.md#tolerances
func (c *checker) near(e Expect) error {
	if e.Near == nil {
		return nil
	}
	n := e.Near
	if w := nearWord(e); w != "" {
		return c.err(n.Span, "%s takes no near: near gives a number a margin, and only position, size, turn, offset, repeats, rotation, glow, colour, alpha, glossiness and environment are numbers a reading can be off by", w)
	}
	if e.Turn != nil && n.Percent {
		return c.err(n.Span, "a turn takes near in degrees, not percent: an angle has no size to be a share of")
	}
	if e.Rot != nil && n.Percent {
		return c.err(n.Span, "a rotation takes near in turns, not percent: an angle has no size to be a share of")
	}
	var any bool
	switch {
	case e.Position != nil:
		any = e.Position.Any
	case e.Size != nil:
		any = e.Size.Any
	case e.Turn != nil:
		any = e.Turn.Any
	case e.Offset != nil:
		any = e.Offset.Any
	case e.Repeats != nil:
		any = e.Repeats.Any
	case e.Rot != nil:
		any = e.Rot.Any
	case e.Glow != nil:
		any = e.Glow.Any
	case e.Colour != nil:
		any = e.Colour.Any
	case e.Alpha != nil:
		any = e.Alpha.Any
	case e.Material != nil:
		any = e.Material.Any
	}
	if any {
		return c.err(n.Span, "is any takes no near: it matches whatever the reading is")
	}
	if err := c.exact(n.Amount); err != nil {
		return err
	}
	if n.Amount.Value <= 0 {
		return c.err(n.Amount.Span, "near %g is not above 0; leave near out to compare as the reading is quantised", n.Amount.Value)
	}
	if n.Percent && n.Amount.Value > 100 {
		return c.err(n.Amount.Span, "near %g percent is above 100", n.Amount.Value)
	}
	return nil
}

// buttonExp checks expect button: the search is a touch's, except that a
// reading counts the buttons found, so button N has no meaning.
// Why: doc/slate-language.md#expectations
func (c *checker) buttonExp(x *ButtonExp, same map[string]bool) error {
	if err := c.ref(x.Name, same); err != nil {
		return err
	}
	if err := c.linkN(x.Name, x.Link); err != nil {
		return err
	}
	if x.Button.Nth != nil {
		return c.err(x.Button.Nth.Span, "button N is not legal in an expectation: a reading is the count of the buttons found, not one of them")
	}
	if err := c.button(x.Button, same); err != nil {
		return err
	}
	if hasValue(x.State) && x.Val == ButtonCount {
		v, err := c.fit(x.Count, "count")
		if err != nil {
			return err
		}
		if v < 0 {
			return c.err(x.Count.Span, "count %d is below 0", v)
		}
	}
	return nil
}

// level checks glow, alpha or colour: the face, the reading, and that a
// literal lies in 0 to 1.
func (c *checker) level(e Expect, what string, name Ident, link *Int, face Int, all bool, st State, any bool, use *Capture, same map[string]bool, lits ...Number) error {
	if err := c.faceRef(name, link, face, all, same); err != nil {
		return err
	}
	lit, err := c.reading(e, st, any, use, levelType(what), same)
	if err != nil || !lit {
		return err
	}
	for _, n := range lits {
		if err := c.exact(n); err != nil {
			return err
		}
		if n.Value < 0 || n.Value > 1 {
			return c.err(n.Span, "%s %g is outside 0 to 1", what, n.Value)
		}
	}
	return nil
}

// materialCapType is the type of the capture of a material field: a
// texture id for a map, a number for a level.
func materialCapType(prop string) CaptureType {
	if prop == "normalmap" || prop == "specularmap" {
		return CapUUID
	}
	return CapNumber
}

// material checks a material expectation: the face, the reading, and
// that a literal map is a uuid and a literal level a whole number from 0
// to 255, the range of PRIM_SPECULAR's glossiness and environment.
// Why: doc/slate-language.md#material-maps
func (c *checker) material(e Expect, x *MaterialExp, same map[string]bool) error {
	if err := c.faceRef(x.Name, x.Link, x.Face, x.FaceAll, same); err != nil {
		return err
	}
	lit, err := c.reading(e, x.State, x.Any, x.Use, materialCapType(x.Prop), same)
	if err != nil || !lit || x.IsMap() {
		return err
	}
	if err := c.exact(x.Value); err != nil {
		return err
	}
	if x.Value.Value != math.Trunc(x.Value.Value) {
		return c.err(x.Value.Span, "%s %g is not a whole number: it is a level from 0 to 255", x.Prop, x.Value.Value)
	}
	if x.Value.Value < 0 || x.Value.Value > 255 {
		return c.err(x.Value.Span, "%s %g is outside 0 to 255", x.Prop, x.Value.Value)
	}
	return nil
}

func levelType(what string) CaptureType {
	if what == "colour" {
		return CapTriple
	}
	return CapNumber
}

// reading checks the value of a state expectation that is not a literal:
// a capture of the right type, or any, which is legal only after is, only
// with as, and so only on a positive expectation. It reports whether the
// caller is to check a literal.
// Why: doc/slate-language.md#expectations
func (c *checker) reading(e Expect, st State, any bool, use *Capture, want CaptureType, same map[string]bool) (bool, error) {
	if !hasValue(st) {
		return false, nil
	}
	if any {
		if st.Kind != StateIs {
			return false, c.err(st.Span, "any is a reading of is, not of becomes")
		}
		if e.As == nil {
			return false, c.err(st.Span, "is any needs as $name: it asserts nothing, so it exists only to capture the reading")
		}
		return false, nil
	}
	if use != nil {
		return false, c.useCapAt(use, want, faceAll(e), same)
	}
	return true, nil
}

// textVal checks a text an expectation compares: a pattern compiles, a
// capture is bound and is text.
func (c *checker) textVal(t Text, same map[string]bool) error {
	if t.Capture != nil {
		return c.useCap(t.Capture, CapText, same)
	}
	return c.pattern(t)
}

// useCap checks a use of a capture: it is bound by an earlier step in
// scope, and holds what this place takes.
func (c *checker) useCap(cp *Capture, want CaptureType, same map[string]bool) error {
	return c.useCapAt(cp, want, false, same)
}

// useCapAt is useCap for a place that reads every face when all is set.
// A capture bound by a face all holds every face, so only a face all can
// use it, and a face all can use only such a capture.
// Why: doc/slate-language.md#expectations
func (c *checker) useCapAt(cp *Capture, want CaptureType, all bool, same map[string]bool) error {
	key := "$" + cp.Name
	if same[key] {
		if _, ok := c.bound[key]; !ok {
			return c.err(cp.Span, "%s is bound in this step; it can be used in a later step", key)
		}
	}
	at, ok := c.bound[key]
	if !ok {
		if _, hid := c.hidden[key]; hid {
			return c.err(cp.Span, "%s is bound in test %q, which after each cannot rely on: the test may have failed before binding it", key, c.curTest.Name)
		}
		return c.err(cp.Span, "%s is not bound; an earlier positive expectation binds it with as %s or a named group", key, key)
	}
	if got := c.capType[cp.Name]; got != want {
		return c.err(cp.Span, "capture type mismatch: %s holds %s (bound at line %d), and this place needs %s", key, got, at.Line, want)
	}
	switch held := c.capAll[cp.Name]; {
	case held && !all:
		return c.err(cp.Span, "%s holds every face (bound by a face all at line %d), and only a face all can use it", key, at.Line)
	case !held && all:
		return c.err(cp.Span, "%s holds one face (bound at line %d), and a face all needs every face", key, at.Line)
	}
	return nil
}

// faceAll reports whether a state expectation reads every face.
func faceAll(e Expect) bool {
	switch {
	case e.Texture != nil:
		return e.Texture.FaceAll
	case e.Offset != nil:
		return e.Offset.FaceAll
	case e.Repeats != nil:
		return e.Repeats.FaceAll
	case e.Rot != nil:
		return e.Rot.FaceAll
	case e.Fullbright != nil:
		return e.Fullbright.FaceAll
	case e.Glow != nil:
		return e.Glow.FaceAll
	case e.Colour != nil:
		return e.Colour.FaceAll
	case e.Alpha != nil:
		return e.Alpha.FaceAll
	case e.Material != nil:
		return e.Material.FaceAll
	}
	return false
}

// capBind is one capture an expectation binds.
type capBind struct {
	name string
	span Span
	typ  CaptureType
	all  bool // bound by a face all reading: it holds every face
}

// captureBinds lists what the step's positive expectations bind: as $name
// by the type of the reading, and each named group of a matching pattern
// as text. A name is bound once per expanded test, so one bound already,
// by another step or by an earlier call of a sequence, is an error here.
// Why: doc/slate-language.md#static-checks
func (c *checker) captureBinds(st Step) ([]capBind, error) {
	var out []capBind
	seen := map[string]Span{}
	add := func(name string, sp Span, typ CaptureType, all bool) error {
		if prev, ok := c.names["$"+name]; ok {
			return c.err(sp, "$%s is already bound at line %d; a capture is bound once per test", name, prev.Line)
		}
		if prev, ok := seen[name]; ok {
			return c.err(sp, "$%s is already bound at line %d; a capture is bound once per test", name, prev.Line)
		}
		seen[name] = sp
		out = append(out, capBind{name, sp, typ, all})
		return nil
	}
	for _, e := range st.Expect {
		var asTyp CaptureType
		if e.As != nil {
			typ, ok := asType(e)
			switch {
			case e.Neg:
				return nil, c.err(e.As.Span, "a negative expectation matches nothing to bind; as %s needs a positive one", e.As)
			case !ok && e.Rez != nil:
				return nil, c.err(e.As.Span, "a rez names the new object with as NAME before within; %s binds nothing here", e.As)
			case !ok:
				return nil, c.err(e.As.Span, "this expectation has no reading to bind; as %s is for say, dialog, textbox, give and the state expectations", e.As)
			}
			asTyp = typ
		}
		if !e.Neg {
			for _, t := range patternsOf(e) {
				names, err := c.groups(t)
				if err != nil {
					return nil, err
				}
				for _, n := range names {
					if err := add(n, t.ValueSpan, CapText, false); err != nil {
						return nil, err
					}
				}
			}
		}
		// The as is last in the source, so a clash with a group is its error.
		if e.As != nil {
			if err := add(e.As.Name, e.As.Span, asTyp, faceAll(e)); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// asType is the type as $name binds on an expectation, and whether it
// binds at all.
func asType(e Expect) (CaptureType, bool) {
	switch {
	case e.Say != nil, e.Dialog != nil, e.TextBox != nil, e.Give != nil:
		return CapText, true
	case e.Texture != nil:
		return CapUUID, true
	case e.Offset != nil, e.Repeats != nil:
		return CapPair, true
	case e.Rot != nil, e.Glow != nil, e.Alpha != nil:
		return CapNumber, true
	case e.Click != nil:
		return CapClick, true
	case e.Fullbright != nil:
		return CapOnOff, true
	case e.Colour != nil:
		return CapTriple, true
	case e.Position != nil, e.Size != nil, e.Turn != nil:
		return CapVector, true
	case e.FloatText != nil:
		return CapText, true
	case e.AlphaMode != nil:
		return CapText, true
	case e.Material != nil:
		return materialCapType(e.Material.Prop), true
	case e.Button != nil:
		return CapNumber, true
	}
	return 0, false
}

// patternsOf is the matching texts of an expectation: the patterns whose
// named groups bind.
func patternsOf(e Expect) []Text {
	var ts []Text
	switch {
	case e.Say != nil:
		ts = []Text{e.Say.Text}
	case e.Dialog != nil:
		if e.Dialog.HasText {
			ts = append(ts, e.Dialog.Text)
		}
		for _, b := range e.Dialog.Clauses {
			ts = append(ts, b.Text)
		}
	case e.TextBox != nil:
		ts = []Text{e.TextBox.Text}
	case e.Give != nil:
		ts = []Text{e.Give.Item}
	case e.Rez != nil:
		ts = []Text{e.Rez.Name}
		if e.Rez.HasDesc {
			ts = append(ts, e.Rez.Desc)
		}
	case e.Link != nil:
		ts = []Text{e.Link.Text}
	case e.FloatText != nil:
		ts = []Text{e.FloatText.Value}
	}
	var out []Text
	for _, t := range ts {
		if t.Pattern {
			out = append(out, t)
		}
	}
	return out
}

// groups is the named groups of a pattern, in order. A name has to be
// writable as a capture, and may not appear twice in one pattern.
func (c *checker) groups(t Text) ([]string, error) {
	re, err := regexp.Compile(t.Value)
	if err != nil {
		return nil, c.err(t.ValueSpan, "pattern %q: %s", t.Value, err.Error())
	}
	var out []string
	seen := map[string]bool{}
	for _, n := range re.SubexpNames() {
		if n == "" {
			continue
		}
		if !isCaptureName(n) {
			return nil, c.err(t.ValueSpan, "group name %q cannot be written as a capture; a name begins with a letter and has letters, digits and underscores", n)
		}
		if seen[n] {
			return nil, c.err(t.ValueSpan, "group name %q appears twice in pattern %q; each name binds $%s once", n, t.Value, n)
		}
		seen[n] = true
		out = append(out, n)
	}
	return out, nil
}

func isCaptureName(n string) bool {
	if n == "" || !isLetter(n[0]) {
		return false
	}
	for i := 1; i < len(n); i++ {
		if !isWordByte(n[i]) {
			return false
		}
	}
	return true
}

// dialog checks expect dialog: its message, its button clauses, count.
// Why: doc/slate-language.md#expectations
// toRef checks the to NAME of an expectation, when it has one.
func (c *checker) toRef(to *Ident) error {
	if to == nil {
		return nil
	}
	return c.avatarRef(*to)
}

func (c *checker) dialog(d *DialogExp, same map[string]bool) error {
	if err := c.ref(d.Name, same); err != nil {
		return err
	}
	if err := c.toRef(d.To); err != nil {
		return err
	}
	if err := c.linkN(d.Name, d.Link); err != nil {
		return err
	}
	if d.HasText {
		if err := c.textVal(d.Text, same); err != nil {
			return err
		}
	}
	pinned := map[int64]bool{}
	for _, b := range d.Clauses {
		if b.Nth != nil {
			if err := c.buttonIndex(*b.Nth); err != nil {
				return err
			}
			// Each clause needs a button of its own, so two clauses
			// pinned to one position can never both hold.
			if pinned[b.Nth.Value] {
				return c.err(b.Nth.Span, "two button clauses are pinned to button %d; each needs a button of its own", b.Nth.Value)
			}
			pinned[b.Nth.Value] = true
		}
		if err := c.textVal(b.Text, same); err != nil {
			return err
		}
	}
	if d.Ordered && len(d.Clauses) < 2 {
		return c.err(d.OrderedSpan, "ordered needs at least two button clauses to put in order; there are %d", len(d.Clauses))
	}
	if sd := d.Sorted; sd != nil && sd.Matching {
		re, err := regexp.Compile(sd.Pattern)
		if err != nil {
			return c.err(sd.PatternSpan, "pattern %q: %s", sd.Pattern, err.Error())
		}
		if n := re.NumSubexp(); n > 1 {
			return c.err(sd.PatternSpan, "sorted matching takes at most one capturing group, the text to compare; %q has %d", sd.Pattern, n)
		}
	}
	if d.Count == nil {
		return nil
	}
	n, err := c.fit(*d.Count, "count")
	if err != nil {
		return err
	}
	if n < 1 || n > maxDialogButtons {
		return c.err(d.Count.Span, "count %d is outside 1 to %d, the most buttons llDialog accepts", n, maxDialogButtons)
	}
	if len(d.Clauses) > int(n) {
		return c.err(d.Count.Span, "count %d: the %d button clauses each need a button of their own", n, len(d.Clauses))
	}
	// only claims every button for a clause, so with a count the clauses
	// are exactly that many.
	if d.Only && len(d.Clauses) < int(n) {
		return c.err(d.Count.Span, "only with count %d needs %d button clauses, one for each button; there are %d", n, n, len(d.Clauses))
	}
	return nil
}

func (c *checker) dialogLike(name Ident, link *Int, text Text, same map[string]bool) error {
	if err := c.ref(name, same); err != nil {
		return err
	}
	if err := c.linkN(name, link); err != nil {
		return err
	}
	return c.textVal(text, same)
}

func (c *checker) sayExp(s *SayExp, same map[string]bool) error {
	if err := c.speaker(s.From, same); err != nil {
		return err
	}
	if err := c.textVal(s.Text, same); err != nil {
		return err
	}
	if s.Channel.Kind != ChanNumber {
		return nil
	}
	v, err := c.fit(s.Channel.Int, "channel")
	if err != nil {
		return err
	}
	// 0 is public chat and 2147483647 is debug chat. Neither uses the bridge.
	if v == 0 || v == math.MaxInt32 {
		return nil
	}
	if _, ok := c.listens[v]; !ok {
		return c.err(s.Channel.Span, "channel %d needs a listen", v)
	}
	return nil
}

// faceRef checks OBJ link? face N, or face all.
func (c *checker) faceRef(name Ident, link *Int, face Int, all bool, same map[string]bool) error {
	if err := c.ref(name, same); err != nil {
		return err
	}
	if err := c.linkN(name, link); err != nil {
		return err
	}
	if all {
		return nil
	}
	_, err := c.fit(face, "face")
	return err
}

func (c *checker) vec(e Expect, v *VecExp, same map[string]bool, unit bool) error {
	if err := c.faceRef(v.Name, v.Link, v.Face, v.FaceAll, same); err != nil {
		return err
	}
	if lit, err := c.reading(e, v.State, v.Any, v.Use, CapPair, same); err != nil || !lit {
		return err
	}
	if unit {
		if err := c.unitInterval(v.S, "offset"); err != nil {
			return err
		}
		return c.unitInterval(v.T, "offset")
	}
	if err := c.exact(v.S); err != nil {
		return err
	}
	return c.exact(v.T)
}

// vec3 checks a position or size: the object, its link, the reading, and
// that the three numbers are exact and, for a size, above 0.
// Why: doc/slate-runner.md#position-and-size
func (c *checker) vec3(e Expect, v *VecExp3, what string, same map[string]bool) error {
	if err := c.ref(v.Name, same); err != nil {
		return err
	}
	if err := c.linkN(v.Name, v.Link); err != nil {
		return err
	}
	lit, err := c.reading(e, v.State, v.Any, v.Use, CapVector, same)
	if err != nil || !lit {
		return err
	}
	for _, n := range []Number{v.X, v.Y, v.Z} {
		if err := c.exact(n); err != nil {
			return err
		}
		if what == "size" && n.Value <= 0 {
			return c.err(n.Span, "size %g is not above 0", n.Value)
		}
	}
	return nil
}

func (c *checker) rot(e Expect, v *RotExp, same map[string]bool) error {
	if err := c.faceRef(v.Name, v.Link, v.Face, v.FaceAll, same); err != nil {
		return err
	}
	if lit, err := c.reading(e, v.State, v.Any, v.Use, CapNumber, same); err != nil || !lit {
		return err
	}
	return c.unitInterval(v.Turns, "rotation")
}

func (c *checker) rez(e Expect, seenAs map[string]Span, same map[string]bool) error {
	r := e.Rez
	if err := c.ref(r.From, same); err != nil {
		return err
	}
	if err := c.textVal(r.Name, same); err != nil {
		return err
	}
	if r.HasDesc {
		if err := c.textVal(r.Desc, same); err != nil {
			return err
		}
	}
	if e.Neg {
		if r.As.Text != "" {
			return c.err(r.As.Span, "a negative rez has no as")
		}
		return nil
	}
	if r.As.Text == "" {
		return c.err(e.Span, "a rez names the new object with as")
	}
	if _, ok := c.names[r.As.Text]; ok {
		return c.err(r.As.Span, "%s is already bound", r.As.Text)
	}
	if _, ok := seenAs[r.As.Text]; ok {
		return c.err(r.As.Span, "%s is already bound", r.As.Text)
	}
	seenAs[r.As.Text] = r.As.Span
	return nil
}

func (c *checker) linkExp(l *LinkExp, same map[string]bool) error {
	if err := c.ref(l.Name, same); err != nil {
		return err
	}
	if err := c.needProbe(l.Name, l.Name.Span, "expect link on"); err != nil {
		return err
	}
	if _, err := c.fit(l.From, "link"); err != nil {
		return err
	}
	if _, err := c.fit(l.Num, "num"); err != nil {
		return err
	}
	switch {
	case l.Text.Capture != nil:
		if err := c.useCap(l.Text.Capture, CapText, same); err != nil {
			return err
		}
	case l.Text.Pattern:
		if err := c.pattern(l.Text); err != nil {
			return err
		}
	default:
		if err := c.ascii(l.Text.Value, l.Text.ValueSpan); err != nil {
			return err
		}
	}
	if l.Key != nil && l.Key.Use != nil {
		if err := c.useCap(l.Key.Use, CapUUID, same); err != nil {
			return err
		}
	}
	if l.HeardBy != nil {
		if _, err := c.fit(*l.HeardBy, "link"); err != nil {
			return err
		}
	}
	return nil
}

func (c *checker) speaker(sp Speaker, same map[string]bool) error {
	switch sp.Kind {
	case SpeakOwner:
		return c.ref(sp.Name, same)
	case SpeakObject:
		if err := c.ref(sp.Name, same); err != nil {
			return err
		}
		return c.linkN(sp.Name, sp.Link)
	case SpeakSecond:
		return c.avatarRef(sp.Name)
	default:
		return nil
	}
}

func (c *checker) ref(id Ident, same map[string]bool) error {
	// A name this step binds with as is not visible yet. A name that is
	// already a header stays visible: the as is then a duplicate binding,
	// reported on the rez, and the reference means the header.
	if same[id.Text] {
		if _, ok := c.bound[id.Text]; !ok {
			return c.err(id.Span, "%s is bound in this step; it can be used in a later step", id.Text)
		}
	}
	if at, ok := c.gone[id.Text]; ok {
		return c.err(id.Span, "%s was taken off at line %d; it cannot be used again in this test", id.Text, at.Line)
	}
	if _, ok := c.items[id.Text]; ok {
		return c.err(id.Span, "%s is an item; only wear, rez and drop use an item", id.Text)
	}
	if _, ok := c.avatars[id.Text]; ok {
		return c.err(id.Span, "%s is an avatar, not an object; only as and to take an avatar", id.Text)
	}
	if _, ok := c.bound[id.Text]; !ok {
		if _, hid := c.hidden[id.Text]; hid {
			return c.err(id.Span, "%s is bound in test %q, which after each cannot rely on: the test may have failed before binding it", id.Text, c.curTest.Name)
		}
		return c.err(id.Span, "%s is not an object", id.Text)
	}
	return nil
}

func (c *checker) needProbe(name Ident, sp Span, what string) error {
	if _, ok := c.probes[name.Text]; ok {
		return nil
	}
	return c.err(sp, "%s %s needs a probe", what, name.Text)
}

func (c *checker) duration(d *Duration) error {
	if d == nil {
		return nil
	}
	if d.Bad {
		return c.err(d.Span, "%s", badDuration)
	}
	if d.Value < MinDuration || d.Value > MaxDuration {
		return c.err(d.Span, "%s is outside 100ms to 120s", c.s.Source(d.Span))
	}
	return nil
}

func (c *checker) origin(st ST) error {
	if st.S.Zero && st.T.Zero {
		return c.err(st.Span, "%s", originError)
	}
	if err := c.exact(st.S); err != nil {
		return err
	}
	return c.exact(st.T)
}

func (c *checker) exact(n Number) error {
	if !n.Exact {
		return c.err(n.Span, "number is not an exact float")
	}
	return nil
}

func (c *checker) unitInterval(n Number, what string) error {
	if err := c.exact(n); err != nil {
		return err
	}
	if n.Value < -1 || n.Value > 1 {
		return c.err(n.Span, "%s %g is outside -1 to 1", what, n.Value)
	}
	return nil
}

// ascii is the link-text rule for a literal: printable ASCII, tab, newline.
func (c *checker) ascii(s string, sp Span) error {
	if !linkText(s) {
		return c.err(sp, "link text must be bytes 0x20-0x7E, tab or newline")
	}
	return nil
}

// pattern compiles a matching text. A literal needs no check here.
func (c *checker) pattern(t Text) error {
	if !t.Pattern {
		return nil
	}
	if _, err := regexp.Compile(t.Value); err != nil {
		return c.err(t.ValueSpan, "pattern %q: %s", t.Value, err.Error())
	}
	return nil
}

// linkN checks a link N after a binding: N fits and is 0 or more. It needs
// no probe: without one the store numbers the links.
// Why: doc/slate-runner.md#linksets-and-region-positions
func (c *checker) linkN(name Ident, n *Int) error {
	if n == nil {
		return nil
	}
	v, err := c.fit(*n, "link")
	if err != nil {
		return err
	}
	if v < 0 {
		return c.err(n.Span, "link %d is below 0", v)
	}
	return nil
}

func (c *checker) linkRange(n Int) error {
	v, err := c.fit(n, "link")
	if err != nil {
		return err
	}
	if v < 0 {
		return c.err(n.Span, "link %d is below 0", v)
	}
	return nil
}

// hasValue is false for changes and original, which carry no literal.
func hasValue(st State) bool { return st.Kind != StateChanges && !st.Original }

// fit reports whether n is an LSL integer. Payments are the exception:
// an amount is a Go int, which may be wider than int32, so the lexer
// does not reject a long digit string on its own.
func (c *checker) fit(n Int, what string) (int32, error) {
	if !n.OK {
		return 0, c.err(n.Span, "%s %s does not fit in an integer", what, n.Text)
	}
	if n.Value < math.MinInt32 || n.Value > math.MaxInt32 {
		return 0, c.err(n.Span, "%s %d is outside -2147483648 to 2147483647", what, n.Value)
	}
	return int32(n.Value), nil
}

// relayLine is the line to the bridge for a send, as the codec builds
// it with the widest values: the prim key is 36 characters and the
// command channel is "-2147483648". It is not length-checked here, so
// the caller can say by how much it is over.
func relayLine(from, target, num int32, key, text string) string {
	k, err := msg.ParseUUID(key)
	if err != nil {
		k = msg.Zero // the length is the same; the parser reports a bad key
	}
	return relayCommand(nonceHex, msg.Zero, math.MinInt32, sendInner(nonceHex, from, target, num, k, text))
}

// dragTotal is how long a drag blocks for: its over (500 ms unless said),
// press and dwell, and those words for an error.  False when one of them
// is a duration that did not read, which has its own error.
func dragTotal(d *Drag) (time.Duration, string, bool) {
	total, what := defaultDragOver, "drag over "+defaultDragOver.String()
	if d.Over != nil {
		if d.Over.Bad {
			return 0, "", false
		}
		total, what = d.Over.Value, "drag over "+d.Over.Value.String()
	}
	for _, t := range []struct {
		word string
		d    *Duration
	}{{"press", d.Press}, {"dwell", d.Dwell}} {
		if t.d == nil {
			continue
		}
		if t.d.Bad {
			return 0, "", false
		}
		total += t.d.Value
		what += ", " + t.word + " " + t.d.Value.String()
	}
	return total, what, true
}
