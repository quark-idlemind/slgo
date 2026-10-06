package slate

// One step: the state machine of doc/slate-runner.md#step-lifecycle.
//
// Armed -> Stimulus -> Matching -> (Hold) -> (Settle) -> Passed, or Failed
// from any of them. Hold and Settle belong to rez and click: a ready step
// stays open in Hold while a click describe is, and in Settle for one
// object poll after a positive rez. observe (expect.go) is the one hook
// for what is not a heard event.
// Why: doc/slate-runner.md#step-lifecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type stepState int

const (
	stArmed stepState = iota
	stStimulus
	stMatching
	stHold
	stSettle
	stPassed
	stFailed
)

// stimulus is a step's stimulus, made in Armed. prepare does the reads a
// stimulus needs before it can be sent (an owner, the tester's position),
// on the run's context and on no step clock. send puts the stimulus out;
// blocking says whether the time it takes is added to the step's deadline.
type stimulus struct {
	prepare  func(ctx context.Context) error
	send     func(ctx context.Context, budget time.Duration) (sent string, err error)
	blocking bool
	// armOnReturn is for a stimulus whose effect is time, which is `wait`:
	// the step's expectations are armed again, readings and all, when it
	// returns. Every other stimulus arms before it is sent.
	// Why: doc/slate-language.md#steps-and-timing
	armOnReturn bool
}

// expState is one expectation of the step.
type expState struct {
	e     *Expect
	neg   bool
	text  string        // the line as written, with its duration
	dur   time.Duration // its within, or the script's default
	limit time.Time     // a positive's deadline; the end of a negative's window

	match   func(ev *event) bool // does the event satisfy it
	onMatch func(ev *event)      // called once, when a positive consumes an event

	// eval judges what is not a heard event (a reading, an offer, a root)
	// on each turn of the step, and sets matched or forbidden itself.
	eval func(ctx context.Context) error
	// note is said after "unmatched" in the failure block, from noteFn when
	// the block is made.
	note   string
	noteFn func() string

	matched   bool
	forbidden bool
	ev        *event
}

type stepRun struct {
	t     *testRun
	r     *runner
	x     ExpandedStep
	st    *Step
	n     int
	first bool
	state stepState

	stim *stimulus
	exps []*expState

	arm, start   time.Time // the arm point, and where the deadline is measured from
	stimReturned time.Time
	blocked      time.Duration // time a blocking stimulus blocked, on its way to nil
	budget       time.Duration // the stimulus budget
	end          time.Time

	sent    string // what was sent, or why it was not
	payment string // the payment this step attempted, for the failure block
	reason  string // why the step failed, when no expectation says it
	exit    int

	sentTo *probePrim // the prim a send went to, for badScan (link.go)

	obs   stepObs // readings, gives, rez claims and click describes (expect.go)
	why   string  // a failure's own sentence, for the failure block
	quiet bool    // the failure printed its own line and has no block
}

func newStepRun(t *testRun, x ExpandedStep, n int, first bool) *stepRun {
	return &stepRun{t: t, r: t.r, x: x, st: x.Step, n: n, first: first}
}

func (s *stepRun) fail(exit int, format string, args ...any) {
	s.state = stFailed
	s.exit = exit
	s.reason = fmt.Sprintf(format, args...)
}

// run takes the step to Passed or Failed. The error is for a context or a
// session that ended.
func (s *stepRun) run(ctx context.Context) error {
	for s.state != stPassed && s.state != stFailed {
		var err error
		switch s.state {
		case stArmed:
			err = s.armed(ctx)
		case stStimulus:
			err = s.stimulate(ctx)
		case stMatching:
			err = s.matching(ctx)
		case stHold:
			err = s.hold(ctx)
		case stSettle:
			err = s.settle(ctx)
		}
		if err != nil {
			return err
		}
	}
	s.end = time.Now()
	if s.state == stPassed {
		s.t.next = later(s.arm, s.stimReturned, s.matchedAt())
		s.r.printf("slate: pass step %d", s.n)
	} else {
		s.t.next = s.end
	}
	return nil
}

// matchedAt is when the last event a positive expectation consumed was
// observed.
func (s *stepRun) matchedAt() time.Time {
	var at time.Time
	for _, e := range s.exps {
		if e.matched && !e.neg {
			at = later(at, e.ev.at)
		}
	}
	return at
}

// armed makes the stimulus and the expectations, which is where a step is
// refused before anything is sent, then takes the arm point.
func (s *stepRun) armed(ctx context.Context) error {
	if s.st.Stimulus != nil {
		stim, err := s.stimulusFor(s.st.Stimulus)
		if err != nil {
			s.sent = "not sent: " + err.Error()
			var ss *stepSentence
			if errors.As(err, &ss) {
				s.r.printf("%s", ss.text)
				s.sent = ss.text
			}
			s.fail(1, "%v", err)
			return nil
		}
		s.stim = stim
	}
	longest := time.Duration(0)
	for i := range s.st.Expect {
		e := &s.st.Expect[i]
		x := &expState{e: e, neg: e.Neg, dur: s.r.s.TimeoutDuration()}
		if e.Within != nil {
			x.dur = e.Within.Value
		}
		longest = max(longest, x.dur)
		x.text = s.expectText(e, x.dur)
		if err := s.expectFor(ctx, x); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// The run cannot go on in this environment: said once, as a
			// setup failure, exit 3, and the run stops, as for a click
			// byte that stays unknown.
			var ee *envError
			if errors.As(err, &ee) {
				s.r.printf("slate: setup: %s", ee.msg)
				s.quiet = true
				s.fail(3, "%s", ee.msg)
				return nil
			}
			s.sent = "not sent: " + err.Error()
			if s.st.Stimulus == nil {
				s.sent = err.Error()
			}
			s.fail(1, "%v", err)
			return nil
		}
		s.exps = append(s.exps, x)
	}
	s.budget = longest
	if s.budget == 0 {
		s.budget = s.r.s.TimeoutDuration()
	}
	if s.stim != nil && s.stim.prepare != nil {
		if err := s.stim.prepare(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.sent = "not sent: " + err.Error()
			var ss *stepSentence
			if errors.As(err, &ss) {
				// The sentence is a line of the transcript and the detail.
				s.r.printf("%s", ss.text)
				s.sent = ss.text
			}
			s.fail(1, "%v", err)
			return nil
		}
	}
	// The arming snapshot of doc/slate-runner.md#arming-snapshot: a poll of
	// the region, which is the baseline and the UUIDs in it, and the
	// inventory ids when the step expects a give. It is taken before the
	// drain, so what it waits for is stamped before the arm point.
	if err := s.snapshot(ctx); err != nil {
		return err
	}
	if s.state == stFailed {
		return nil
	}
	if err := s.r.drain(); err != nil {
		return err
	}
	now := time.Now()
	s.start = now
	if s.x.Phase == PhaseBody && s.t.ownStart.IsZero() {
		s.t.ownStart = now // where `original` is read: after before each
	}
	switch {
	case s.first:
		s.arm = s.t.start
	case s.st.Stimulus != nil:
		s.arm = now
	default:
		s.arm = s.t.next
	}
	if s.stim != nil {
		s.state = stStimulus
	} else {
		s.state = stMatching
	}
	return nil
}

// stimulate sends the stimulus. A blocking one is on the stimulus budget,
// and what it blocked is added to the deadline only if it returns nil.
func (s *stepRun) stimulate(ctx context.Context) error {
	t0 := time.Now()
	sent, err := s.stim.send(ctx, s.budget)
	s.stimReturned = time.Now()
	s.sent = sent
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.sent = err.Error()
		s.fail(1, "%v", err)
		return nil
	}
	if s.stim.blocking {
		s.blocked = s.stimReturned.Sub(t0)
	}
	if s.stim.armOnReturn {
		if err := s.rearm(ctx); err != nil {
			return err
		}
		if s.state == stFailed {
			return nil
		}
	}
	s.state = stMatching
	return nil
}

// rearm takes the arm point again, as the stimulus returns: a fresh
// reading of everything the step reads, then a drain, then the point, so
// that the baseline of `becomes` and `changes`, what `is` may count as
// already so, and what `is any` binds are the state at the end of the
// wait, and an event heard during it is not the step's.
// Why: doc/slate-language.md#steps-and-timing
func (s *stepRun) rearm(ctx context.Context) error {
	for _, g := range s.obs.gives {
		g.armHeld = 0
	}
	if err := s.snapshot(ctx); err != nil {
		return err
	}
	if s.state == stFailed {
		return nil
	}
	if err := s.r.drain(); err != nil {
		return err
	}
	s.arm = time.Now()
	s.stimReturned = later(s.stimReturned, s.arm)
	return nil
}

// matching offers the eligible events to the expectations until the step
// is ready, or an expectation fails it.
func (s *stepRun) matching(ctx context.Context) error {
	for _, e := range s.exps {
		e.limit = s.start.Add(s.blocked + e.dur)
	}
	for {
		if err := s.r.drain(); err != nil {
			return err
		}
		s.consume()
		if err := s.observe(ctx); err != nil {
			return err
		}
		if s.state == stFailed {
			return nil
		}
		now := time.Now()
		for _, e := range s.exps {
			if e.forbidden {
				s.fail(1, "forbidden")
				return nil
			}
		}
		for _, e := range s.exps {
			if !e.neg && !e.matched && !now.Before(e.limit) {
				s.fail(1, "unmatched")
				return nil
			}
		}
		wake, ready := s.next(now)
		if ready {
			s.state = s.afterMatching()
			return nil
		}
		if err := s.r.wait(ctx, wake); err != nil {
			return err
		}
	}
}

// next is the earliest time anything can change by the clock, and whether
// the step is ready: every positive matched and every negative window over.
func (s *stepRun) next(now time.Time) (wake time.Time, ready bool) {
	ready = true
	for _, e := range s.exps {
		if e.neg && now.Before(e.limit) || !e.neg && !e.matched {
			ready = false
			if wake.IsZero() || e.limit.Before(wake) {
				wake = e.limit
			}
		}
	}
	return wake, ready
}

// consume offers each eligible event, in the order observed, to the
// unmatched expectations in source order, and the first it satisfies takes
// it. An event is never offered to an expectation whose time has passed.
// A negative that an event satisfies is forbidden, and fails the step.
// Why: doc/slate-runner.md#consumption
func (s *stepRun) consume() {
	for _, ev := range s.r.log.events[s.t.mark:] {
		if !ev.eligible(s.arm) {
			continue
		}
		for _, e := range s.exps {
			if e.matched || e.forbidden || ev.at.After(e.limit) || !e.match(ev) {
				continue
			}
			e.ev = ev
			if e.neg {
				e.forbidden = true
				return
			}
			e.matched = true
			ev.consumed = true
			if e.onMatch != nil {
				e.onMatch(ev)
				if s.state == stFailed {
					return
				}
			}
			break
		}
	}
}

// afterMatching is the state a ready step goes to.
func (s *stepRun) afterMatching() stepState {
	switch {
	case s.needsHold():
		return stHold
	case s.needsSettle():
		return stSettle
	}
	return stPassed
}

// needsHold is true while a click describe is open.
func (s *stepRun) needsHold() bool { return len(s.obs.desc) > 0 }

// needsSettle is true for a step with a positive rez.
func (s *stepRun) needsSettle() bool { return s.hasPositiveRez() }

// hold keeps a ready step open until every click describe it started has
// stopped, with the byte known; an unknown byte at the end of one is exit
// 3. The object polls go on, so a second root still fails the step.
// Why: doc/slate-runner.md#rules
func (s *stepRun) hold(ctx context.Context) error {
	for s.needsHold() {
		if err := s.r.drain(); err != nil {
			return err
		}
		if err := s.observe(ctx); err != nil {
			return err
		}
		if s.state == stFailed {
			return nil
		}
		if !s.needsHold() {
			break
		}
		if err := s.r.wait(ctx, s.describeWake()); err != nil {
			return err
		}
	}
	s.state = stPassed
	if s.needsSettle() {
		s.state = stSettle
	}
	return nil
}

// settle waits one object poll for a second root to match a rez claim,
// then looks once more: a twin that shows in it fails the step.
// Why: doc/slate-runner.md#rules
func (s *stepRun) settle(ctx context.Context) error {
	if err := s.r.sess.Settle(ctx, s.r.cfg.settleFor()); err != nil {
		return err
	}
	if err := s.r.drain(); err != nil {
		return err
	}
	if err := s.t.watch.poll(ctx, true); err != nil {
		return err
	}
	if err := s.observe(ctx); err != nil {
		return err
	}
	if s.state != stFailed {
		s.state = stPassed
	}
	return nil
}

// expectText is the expectation as written, without the word expect, and
// with its duration when the file did not write one.
func (s *stepRun) expectText(e *Expect, d time.Duration) string {
	src := strings.Join(strings.Fields(s.r.s.Source(e.Span)), " ")
	src = strings.TrimPrefix(src, "expect ")
	if e.Within == nil {
		src += " within " + d.String()
	}
	return src
}

// expectFor fills in what matches an expectation, or says why it cannot
// be run. The kinds PR 6 and PR 7 add go here.
func (s *stepRun) expectFor(ctx context.Context, x *expState) error {
	e := x.e
	switch {
	case e.Say != nil:
		return s.sayExpect(ctx, x)
	case e.Dialog != nil:
		return s.dialogExpect(x, e.Dialog.Name, e.Dialog.To, e.Dialog.Link, e.Dialog.Text, e.Dialog)
	case e.TextBox != nil:
		return s.dialogExpect(x, e.TextBox.Name, e.TextBox.To, e.TextBox.Link, e.TextBox.Text, nil)
	case e.Texture != nil, e.Offset != nil, e.Repeats != nil, e.Rot != nil, e.Click != nil,
		e.Fullbright != nil, e.Glow != nil, e.Colour != nil, e.Alpha != nil, e.AlphaMode != nil,
		e.Position != nil, e.Size != nil, e.Turn != nil, e.FloatText != nil:
		return s.stateExpect(x)
	case e.Button != nil:
		return s.buttonExpect(x)
	case e.Give != nil:
		return s.giveExpect(x)
	case e.Rez != nil:
		return s.rezExpect(x)
	case e.Link != nil:
		return s.linkExpect(x)
	case e.Attached != nil:
		return s.attachedExpect(x)
	}
	return notYet("this expectation")
}

// stimulusFor makes a step's stimulus.
func (s *stepRun) stimulusFor(st *Stimulus) (*stimulus, error) {
	switch {
	case st.Say != nil:
		return s.sayStimulus(st.Say), nil
	case st.Choose != nil:
		return s.chooseStimulus(st.Choose), nil
	case st.Answer != nil:
		return s.answerStimulus(st.Answer), nil
	case st.Touch != nil:
		return s.touchStimulus(st.Touch)
	case st.Drag != nil:
		return s.dragStimulus(st.Drag)
	case st.Pay != nil:
		return s.payStimulus(st.Pay)
	case st.Sit != nil:
		return s.sitStimulus(st.Sit)
	case st.Stand != nil:
		return s.standStimulus(), nil
	case st.Wait != nil:
		return s.waitStimulus(st.Wait.For.Value), nil
	case st.Send != nil:
		return s.sendStimulus(st.Send)
	case st.Wear != nil:
		return s.wearStimulus(st.Wear)
	case st.Rez != nil:
		return s.rezStimulus(st.Rez)
	case st.TakeOff != nil:
		return s.takeOffStimulus(st.TakeOff)
	case st.Drop != nil:
		return s.dropStimulus(st.Drop)
	case st.Group != nil:
		return s.groupStimulus(st.Group)
	}
	return nil, notYet("this stimulus")
}

func notYet(what string) error { return fmt.Errorf("%s is not implemented yet", what) }
