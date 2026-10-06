package slate

// Run drives a checked script against a session.
//
// Setup runs once, then each selected test runs its expanded steps one at
// a time on this goroutine (step.go is the state machine, arm.go the event
// log it reads). Nothing here dials: the caller holds the session.
// Why: doc/slate-runner.md#tests-and-the-run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// Options is what the process decides rather than the file.
type Options struct {
	Pay bool           // the --pay flag
	Run *regexp.Regexp // the -run flag; nil runs every test
	Out io.Writer      // when set, each transcript line is written to it as it happens

	// Screen is the world view a drag on the screen is given in. A zero
	// Width or Height is DefaultScreenWidth by DefaultScreenHeight, and a
	// zero Zoom is 1.
	Screen sl.HUDView

	// Avatars is the session of each second avatar the file declares, by
	// its binding. The caller dialled them and closes them; Run needs
	// exactly the declared names, each a different avatar from the tester
	// and from the others.
	// Why: doc/slate-runner.md#second-avatars
	Avatars map[string]*sl.Session
}

// second is a second avatar the run drives: its binding, its session, and
// the instant messages it is sent. Its chat is not subscribed: the tester
// hears what it says, as it hears anyone, and one line is not logged twice.
type second struct {
	name string
	sess *sl.Session
	ims  <-chan *sl.IM

	// The active group it had when the run began, read at setup for an
	// avatar a group step names (group.go), and whether a step changed it.
	group     msg.UUID
	groupRead bool
	changed   bool
}

// setupSeconds matches the file's avatar headers to the sessions given,
// and refuses a run that is not given exactly those, or that is given one
// avatar twice. No message names a profile: the caller has not told Run
// one, and a pasted error must not carry it.
func (r *runner) setupSeconds() error {
	declared := map[string]bool{}
	for _, a := range r.s.Avatars {
		declared[a.Name.Text] = true
		if r.opt.Avatars[a.Name.Text] == nil {
			return &setupError{fmt.Sprintf("%s is declared but no --avatar %s=... was given", a.Name.Text, a.Name.Text)}
		}
	}
	var names []string
	for n := range r.opt.Avatars {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if !declared[n] {
			return &setupError{fmt.Sprintf("an --avatar was given for %s, which the file does not declare", n)}
		}
	}
	ids := map[msg.UUID]string{r.sess.Me(): ""}
	for _, a := range r.s.Avatars {
		n := a.Name.Text
		sess := r.opt.Avatars[n]
		if prev, dup := ids[sess.Me()]; dup || sess == r.sess {
			if prev == "" {
				return &setupError{fmt.Sprintf("the profile given for %s is the tester's own", n)}
			}
			return &setupError{fmt.Sprintf("the profiles given for %s and %s are the same avatar", prev, n)}
		}
		ids[sess.Me()] = n
		r.seconds = append(r.seconds, &second{name: n, sess: sess, ims: sess.IMs(r.cfg.chatDepth)})
	}
	return nil
}

// actor is the session a stimulus is sent on: the tester's, or the second
// avatar named by as. Check has made sure the name is declared.
func (r *runner) actor(as *Ident) *sl.Session {
	if as != nil {
		if sc := r.secondOf(as.Text); sc != nil {
			return sc.sess
		}
	}
	return r.sess
}

// secondOf is the second avatar bound to name, or nil.
func (r *runner) secondOf(name string) *second {
	for _, sc := range r.seconds {
		if sc.name == name {
			return sc
		}
	}
	return nil
}

// TestResult is one test that ran.
type TestResult struct {
	Name   string
	Passed bool
	Exit   int
}

// Result is the whole run. Tests holds a test only if it ran; Exit is 3 if
// setup failed, else 1 if any test failed, else 0.
type Result struct {
	Exit       int
	Tests      []TestResult
	Transcript string
}

// Setup budgets and the runner's own pacing. Tests shorten them.
// Why: doc/slate-runner.md#timeouts-and-setup-budgets
type runCfg struct {
	lookup    time.Duration // ObjectsNamed, per header binding
	props     time.Duration // Properties, for an owner
	chatDepth int           // the chat and IM subscriptions
	poll      time.Duration // how often the session's dialogs are looked at

	// Zero is the production value, in expect.go.
	objects    time.Duration // the object and face polls, 250 ms
	redescribe time.Duration // between requests for one prim, 1 s
	click      time.Duration // a click describe, 30 s
	settle     time.Duration // the rez settle, 250 ms
	inv        time.Duration // between inventory fetches for a give, 1 s
	find       finder        // the button finder; nil is imgfind.Find

	// The bring-up (bridge.go); zero is the production value there.
	ready time.Duration // the bridge's ready line, 30 s
	hello time.Duration // every probe's hello, 30 s
	wear  time.Duration // Session.Wear, 40 s
	draw  io.Reader     // the nonce and channels; nil is crypto/rand
	ops   gridOps       // the grid operations of the bring-up; nil is the session
}

func defaultCfg() runCfg {
	return runCfg{lookup: 30 * time.Second, props: 15 * time.Second, chatDepth: 256, poll: 25 * time.Millisecond}
}

// Run runs the tests of a script that passed Check. A failed step or test
// is in the Result, not an error. The error is for a session that died, a
// context that ended, or a setup failure the transcript also describes.
func Run(ctx context.Context, sess *sl.Session, s *Script, opt Options) (*Result, error) {
	return run(ctx, sess, s, opt, defaultCfg())
}

// binding is a script name for a prim in the region.
type binding struct {
	name  string
	seen  *sl.Seen
	owner msg.UUID // read lazily, see ownerOf

	members []*sl.Seen // the linkset, root first; set at setup by setupLinksets

	item     msg.UUID // the inventory item a wear bound this from (wear.go)
	itemName string
}

// prims is every prim that answers to the binding: its linkset, so a
// speaker, a dialog and a give match any prim of it. A binding made
// without a walk is the bound prim only.
// Why: doc/slate-runner.md#linksets-and-region-positions
func (b *binding) prims() []*sl.Seen {
	if len(b.members) > 0 {
		return b.members
	}
	return []*sl.Seen{b.seen}
}

func (b *binding) owns(id msg.UUID) bool {
	for _, p := range b.prims() {
		if p.ID == id {
			return true
		}
	}
	return false
}

func (b *binding) named(name string) bool {
	return b.nameIs(func(n string) bool { return n == name })
}

// nameIs says whether a prim of the binding has a name that the function
// is accepts. An object's name from a message is compared through it and
// never read out, since only Sender.Label may print one.
// Why: doc/im-senders.md#labelling-a-sender
func (b *binding) nameIs(is func(string) bool) bool {
	for _, p := range b.prims() {
		if is(p.Name) {
			return true
		}
	}
	return false
}

// runner is the state of one run.
type runner struct {
	sess *sl.Session
	s    *Script
	opt  Options
	cfg  runCfg

	out   liveOut
	log   *eventLog
	chat  <-chan sl.Line
	ims   <-chan *sl.IM
	tick  *time.Ticker
	began time.Time

	ctx  context.Context // the run's, for what drain does on its own
	seat string          // where a sit left the avatar, "" when standing

	bind    map[string]*binding // header bindings, for the file
	itemHdr map[string]*sl.Item // item headers, found at setup (wear.go)
	cur     *testRun            // the test being run, for its as bindings
	seconds []*second           // the second avatars, in the order the file declares them

	ops gridOps   // what the bring-up and cleanup ask of the grid
	pr  *probeRun // the bridge and probes; nil when the file has none
}

// lookup finds a name: bound with as in this test, else a header.
func (r *runner) lookup(name string) *binding {
	if r.cur != nil {
		if b := r.cur.as[name]; b != nil {
			return b
		}
	}
	return r.bind[name]
}

func (r *runner) ended() error {
	if err := r.sess.Err(); err != nil {
		return fmt.Errorf("slate: the session ended: %w", err)
	}
	return errors.New("slate: the session ended")
}

// envError is a step that cannot be taken in this environment -- the
// daemon or its session lacks what the step needs -- rather than a product
// that failed it: exit 3, and the run stops.
type envError struct{ msg string }

func (e *envError) Error() string { return e.msg }

// setupError is a setup failure with its sentence.
type setupError struct{ msg string }

func (e *setupError) Error() string { return e.msg }

func run(ctx context.Context, sess *sl.Session, s *Script, opt Options, cfg runCfg) (*Result, error) {
	res := &Result{}
	if sess == nil || s == nil {
		res.Exit = 2
		return res, errors.New("slate: Run needs a session and a script")
	}
	tests, err := s.Expand()
	if err != nil {
		res.Exit = 2
		return res, err
	}
	r := &runner{
		ctx:  ctx,
		sess: sess, s: s, opt: opt, cfg: cfg,
		log:  newEventLog(),
		bind: map[string]*binding{},
		ops:  cfg.ops,
	}
	if r.ops == nil {
		r.ops = sess
	}
	r.out.live = opt.Out
	defer func() { res.Transcript = r.out.String() }()

	r.began = time.Now()
	r.chat = sess.Chat(sl.ChatFilter{}, cfg.chatDepth)
	r.ims = sess.IMs(cfg.chatDepth)
	r.tick = time.NewTicker(cfg.poll)
	defer func() {
		r.tick.Stop()
		sess.StopChat(r.chat)
		sess.StopIMs(r.ims)
		for _, sc := range r.seconds {
			sc.sess.StopIMs(sc.ims)
		}
	}()
	if err := r.setupSeconds(); err != nil {
		res.Exit = 3
		r.printf("slate: setup: %v", err)
		return res, err
	}

	defer r.cleanup(ctx)
	if err := r.setup(ctx); err != nil {
		res.Exit = 3
		var se *setupError
		if errors.As(err, &se) {
			r.printf("slate: setup: %s", se.msg)
		} else {
			r.printf("slate: setup: %v", err)
		}
		return res, err
	}

	var selected []ExpandedTest
	for _, et := range tests {
		if opt.Run == nil || opt.Run.MatchString(et.Test.Name) {
			selected = append(selected, et)
		}
	}
	failed := 0
	for _, et := range selected {
		tr, stop, err := r.runTest(ctx, et)
		res.Tests = append(res.Tests, tr)
		if !tr.Passed {
			failed++
		}
		if err != nil {
			r.printf("slate: run stopped: %v", err)
			res.Exit = 1
			return res, err
		}
		if stop {
			break
		}
	}
	// Clean up before the verdict, so the run's last line is its result.
	r.cleanup(ctx)
	res.Exit = 0
	if failed > 0 {
		res.Exit = 1
		// A click byte that stayed unknown outranks a failed test.
		for _, tr := range res.Tests {
			if tr.Exit == 3 {
				res.Exit = 3
			}
		}
		r.printf("slate: failed %d of %d tests", failed, len(res.Tests))
	} else {
		r.printf("slate: passed %d tests", len(res.Tests))
	}
	return res, nil
}

// setupStep is one part of setup. Each is run in order and the first
// failure ends the run with exit 3.
type setupStep struct {
	name string
	do   func(ctx context.Context) error
}

// setupSteps is the setup, in order.
func (r *runner) setupSteps() []setupStep {
	return []setupStep{
		{"objects", r.setupObjects},
		{"items", r.setupItems},
		{"groups", r.setupGroups},
		{"linksets", r.setupLinksets},
		{"probe", r.setupProbe},
		{"click", r.setupClick},
	}
}

func (r *runner) setup(ctx context.Context) error {
	for _, st := range r.setupSteps() {
		if err := st.do(ctx); err != nil {
			return err
		}
	}
	return nil
}

// setupObjects binds each header name to the one prim of that name.
func (r *runner) setupObjects(ctx context.Context) error {
	claimed := map[msg.UUID]string{} // prims bound through a description, by header name
	for i := range r.s.Objects {
		o := &r.s.Objects[i]
		found, err := r.sess.ObjectsNamed(ctx, o.World, r.cfg.lookup)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &setupError{fmt.Sprintf("lookup of %q (%s): %v", o.World, r.cfg.lookup, err)}
		}
		if o.HasDesc && len(found) > 0 {
			if found, err = r.withDescription(ctx, o, found); err != nil {
				return err
			}
			if prev, ok := claimed[found[0].ID]; ok {
				return &setupError{fmt.Sprintf("%q with description %q is the same object as %s (%s): one object cannot be bound twice", o.World, o.Desc.Value, prev, found[0].ID)}
			}
			claimed[found[0].ID] = o.Name.Text
		}
		switch len(found) {
		case 0:
			return &setupError{fmt.Sprintf("%q is not in the region, or is beyond the draw distance (looked up for %s)", o.World, r.cfg.lookup)}
		case 1:
			r.bind[o.Name.Text] = &binding{name: o.Name.Text, seen: found[0]}
		default:
			return &setupError{fmt.Sprintf("%q names %d objects", o.World, len(found))}
		}
	}
	return nil
}

// withDescription keeps the matches of a header whose description is the
// header's, which is exactly one or an error.
func (r *runner) withDescription(ctx context.Context, o *Object, found []*sl.Seen) ([]*sl.Seen, error) {
	m, err := textMatcher(o.Desc)
	if err != nil {
		return nil, &setupError{fmt.Sprintf("description of %q: %v", o.World, err)}
	}
	var kept []*sl.Seen
	for _, f := range found {
		p, err := r.sess.Properties(ctx, &f.Object, r.cfg.props)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, &setupError{fmt.Sprintf("properties of %q (%s): %v", o.World, r.cfg.props, err)}
		}
		if m.match(p.Description) {
			kept = append(kept, f)
		}
	}
	switch len(kept) {
	case 1:
		return kept, nil
	case 0:
		return nil, &setupError{fmt.Sprintf("%q with description %q matches none of %d objects of that name", o.World, o.Desc.Value, len(found))}
	}
	var list []string
	for _, k := range kept {
		list = append(list, k.ID.String())
	}
	return nil, &setupError{fmt.Sprintf("%q with description %q matches %d objects (%s)", o.World, o.Desc.Value, len(kept), strings.Join(list, ", "))}
}

// testRun is one test being run.
type testRun struct {
	r     *runner
	et    ExpandedTest
	start time.Time
	mark  int
	as    map[string]*binding // bound with as; gone with the test
	holds map[string]*hold    // dialogs held for an object and who they came to (holdKey)

	caps     map[string]*capValue // captured values; gone with the test
	capNames []string             // in the order they were bound

	watch     *watcher        // the readings, the roots (expect.go)
	ownStart  time.Time       // when the test's own steps began, which is where `original` is read
	exit3     bool            // a step failed with exit 3
	step      int             // the number of the step being run, 0 before the first
	uncounted map[string]bool // bindings whose prim face count was not known, noted once

	next        time.Time // the arm point of a step that follows the last one
	failed      bool
	afterFailed bool
	dropped     string
	rezzed      []*rezzed  // what rez steps made, deleted at the end (rezitem.go)
	drops       []*dropped // what drop steps changed, put back at the end (drop.go)
	late        []lateDrop // drops whose copy had not shown when the step gave up (drop.go)
	blocks      []*failBlock
}

// runTest runs one test. stop is true when nothing after it may run.
// The error is for a context or session that ended.
func (r *runner) runTest(ctx context.Context, et ExpandedTest) (TestResult, bool, error) {
	t := &testRun{r: r, et: et, as: map[string]*binding{}, holds: map[string]*hold{}, caps: map[string]*capValue{}}
	tr := TestResult{Name: et.Test.Name}
	r.cur = t
	defer func() { r.cur = nil }()
	if err := r.drain(); err != nil {
		return tr, true, err
	}
	r.printf("slate: test %q", et.Test.Name)
	// The first reading is stamped before the start: it is the baseline of
	// the first step.
	t.watch = newWatcher(t)
	if err := t.watch.start(ctx); err != nil {
		return tr, true, err
	}
	t.start = time.Now()
	t.next = t.start
	t.mark = r.log.mark()

	stop := false
	for i, x := range et.Steps {
		if t.failed && x.Phase != PhaseAfter || t.afterFailed {
			continue
		}
		s := newStepRun(t, x, i+1, i == 0)
		if err := s.run(ctx); err != nil {
			t.finish(ctx)
			return TestResult{Name: tr.Name, Exit: 1}, true, err
		}
		if s.state == stFailed {
			t.failed = true
			t.afterFailed = x.Phase == PhaseAfter
			if !s.quiet {
				t.blocks = append(t.blocks, s.block())
			}
			if s.exit == 3 {
				stop, t.exit3 = true, true
			}
		}
		// A dropped subscription means events were lost: the next step, and
		// the next test, could not be trusted.
		if msg := r.dropped(); msg != "" {
			t.failed, t.dropped, stop = true, msg, true
		}
		if stop {
			break
		}
	}
	t.finish(ctx)
	tr.Passed = !t.failed
	if t.failed {
		tr.Exit = 1
		if t.exit3 {
			tr.Exit = 3
		}
	}
	return tr, stop, nil
}

// dropped says whether either subscription lost lines.
func (r *runner) dropped() string {
	if n := r.sess.ChatDropped(r.chat); n > 0 {
		return fmt.Sprintf("the chat subscription dropped %d lines", n)
	}
	if n := r.sess.IMsDropped(r.ims); n > 0 {
		return fmt.Sprintf("the instant message subscription dropped %d lines", n)
	}
	for _, sc := range r.seconds {
		if n := sc.sess.IMsDropped(sc.ims); n > 0 {
			return fmt.Sprintf("the instant message subscription of %s dropped %d lines", sc.name, n)
		}
	}
	return ""
}

// finish ends a test: offers and dialogs nobody answered are declined and
// ignored (leave.go), an unconsumed hold is forgotten and reported, the
// failure blocks are printed with it, what its drop steps changed is put
// back and what its rez steps made is deleted (either that fails fails
// the test), and the as bindings go with t.
// Why: doc/slate-runner.md#dialog-hold
func (t *testRun) finish(ctx context.Context) {
	t.r.denyPermissions()
	// Before dropHolds, which forgets the held dialogs without telling slgod.
	t.leaveAsFound(ctx)
	left := t.dropHolds()
	for _, b := range t.blocks {
		b.unanswered = left
		t.r.out.WriteString(b.render())
	}
	if t.dropped != "" {
		t.r.printf("slate: %s", t.dropped)
	}
	// What a drop changed in an object goes back before the objects a rez
	// made are deleted.
	var could []string
	if undone := t.undoDrops(ctx); len(undone) > 0 {
		could = append(could, "put back "+strings.Join(undone, ", "))
	}
	if undeleted := t.deleteRezzed(ctx); len(undeleted) > 0 {
		could = append(could, "delete "+strings.Join(undeleted, ", "))
	}
	if len(could) > 0 {
		wasPassing := !t.failed
		t.failed = true
		if wasPassing {
			t.r.printf("slate: fail test %q: could not %s", t.et.Test.Name, strings.Join(could, "; could not "))
		}
	}
	if !t.failed {
		t.r.printf("slate: pass test %q", t.et.Test.Name)
	}
}
