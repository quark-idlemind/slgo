package scripttest

// Running a script: the compiler's verdict, what the object said, and
// how the run ended.
//
// The order is the contract's and is worth stating because it is what a
// caller is written against: Compiled exactly once and first, then any
// number of Lines, a Fault at most once, and Finished exactly once and
// last.  A script that would not compile is Compiled{ok:false} and then
// Finished, with nothing between -- there was nothing to say.
//
// # Where the sentinel is enforced
//
// Here, not in the caller.  That is the change the contract makes: under
// the old transport slbench held the DONE contract itself and every
// backend had to be trusted to stream forever.  A run ends when a line
// contains Done, when the timeout is up, when a fault ends it, or when
// the caller cancels -- and only the first of those sets
// Finished.sentinel.

import (
	"context"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/quark-idlemind/slgo/proto/scriptv1"
)

// Run runs one script and streams back what it said.
func (s *Server) Run(req *scriptv1.RunRequest, stream grpc.ServerStreamingServer[scriptv1.RunEvent]) error {
	if err := s.notReady(); err != nil {
		return err
	}
	if req.GetCompileOnly() && !s.caps.CompileOnly {
		// The contract's own words: where compile_only is not honoured,
		// asking for it is an error rather than a run.  Silently running
		// the script would be the worst answer -- the caller asked for
		// the object not to be disturbed.
		return status.Error(codes.Unimplemented, "this backend does not do compile_only")
	}
	ctx := stream.Context()

	// Where to run.  A named target is one this caller was granted and
	// still holds; empty is the one-off case, which takes a group,
	// runs, and gives it back -- so a caller with a single script need
	// not hold a lease stream at all.
	t := s.find(req.GetTarget())
	if req.GetTarget() == "" {
		g := s.take("", "run", 1)
		if g == nil {
			// Nothing free.  Queue on the caller's own context: a
			// one-off run has no wait_seconds of its own, and the caller
			// giving up is the bound.
			w, _ := s.enqueue("", "run", 1)
			select {
			case g = <-w.ch:
			case <-ctx.Done():
				s.giveBack(s.unqueue(w))
				return ctx.Err()
			}
		}
		defer s.giveBack(g)
		t = g.targets[0]
	}
	if t == nil {
		return status.Errorf(codes.FailedPrecondition,
			"no target %q: a target comes from a Granted on a lease that is still open",
			req.GetTarget())
	}

	// The concurrency bound.  Waiting rather than refusing: the number
	// is published in Capabilities, and a caller that respected it would
	// be punished for a race it cannot see.
	select {
	case s.slots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.slots }()

	return s.execute(ctx, stream, req, t)
}

// execute is the run itself, once there is somewhere to run it.
func (s *Server) execute(ctx context.Context, stream grpc.ServerStreamingServer[scriptv1.RunEvent], req *scriptv1.RunRequest, t *poolTarget) error {
	start := time.Now()
	name := req.GetName()
	if name == "" {
		name = "script"
	}

	s.mu.Lock()
	beh := t.beh
	mem := s.opt.Memory
	s.mu.Unlock()

	finish := func(sentinel bool) error {
		return stream.Send(&scriptv1.RunEvent{Event: &scriptv1.RunEvent_Finished{
			Finished: &scriptv1.Finished{
				Sentinel:      sentinel,
				ElapsedMillis: time.Since(start).Milliseconds(),
			},
		}})
	}

	cnt, pad, benchmark := Harness(req.GetSource())

	// The compiler's verdict, which is also where the model's size limit
	// lands.  It is on the RUN and not on a call of its own because that
	// is where the live one arrives too: the source goes up, Second Life
	// refuses it, and the caller gets a refusal having executed nothing.
	errs := beh.CompileErrors
	if len(errs) == 0 && benchmark {
		if why, refused := mem.refuses(cnt, pad); refused {
			errs = []string{why}
		}
	}
	if len(errs) > 0 {
		if err := stream.Send(&scriptv1.RunEvent{Event: &scriptv1.RunEvent_Compiled{
			Compiled: &scriptv1.Compiled{Ok: false, Errors: errs},
		}}); err != nil {
			return err
		}
		return finish(false)
	}
	if err := stream.Send(&scriptv1.RunEvent{Event: &scriptv1.RunEvent_Compiled{
		Compiled: &scriptv1.Compiled{Ok: true, Item: "item:" + t.name + "/" + name},
	}}); err != nil {
		return err
	}
	if req.GetCompileOnly() {
		// Nothing ran, so the sentinel was never said.  False rather
		// than true is the honest answer and the one a caller can act
		// on: it asked for a verdict, and Compiled is where the verdict
		// is.
		return finish(false)
	}

	// What the run has to say, worked out before the first line is sent
	// so that a fault and a transcript cannot disagree about the
	// reading.
	//
	// The noise hook gets it here and nowhere else: what it stands in for
	// is llGetUsedMemory answering wrongly, so the wrong number is what
	// the script reports -- live the script has only the one number and
	// cannot know it is wrong -- while the compiler's refusal above and
	// the collision below are unmoved, being the region's judgement about
	// the script rather than the script's report of itself.
	reading := mem.Reading(cnt, pad)
	if benchmark && s.opt.Noise != nil {
		reading = s.opt.Noise(cnt, pad, reading)
	}
	lines := beh.Lines
	if lines == nil {
		switch {
		case benchmark:
			done := req.GetDone()
			if beh.Silent {
				done = ""
			}
			lines = mem.transcript(cnt, pad, reading, done)
		default:
			lines = spoken(req.GetSource())
			if len(lines) == 0 && !beh.Silent && req.GetDone() != "" {
				// A script this backend cannot read still has to end,
				// and the sentinel is the only thing a caller is waiting
				// for.  Saying it is a kinder default than a timeout for
				// every script that is not a benchmark.
				lines = []string{req.GetDone()}
			}
		}
	}

	// A fault comes before the lines because that is when it happens
	// live: the script stops the moment it starts.  With ignore_fault
	// the run carries on, which is for an object where something else is
	// expected to keep talking.
	fault := beh.Fault
	oom := beh.OutOfMemory
	if fault == "" && benchmark && mem.collides(cnt, pad) {
		fault, oom = "Stack-Heap Collision", true
	}
	if fault != "" {
		if err := stream.Send(&scriptv1.RunEvent{Event: &scriptv1.RunEvent_Fault{
			Fault: &scriptv1.Fault{Script: name, Reason: fault, OutOfMemory: oom},
		}}); err != nil {
			return err
		}
		if !req.GetIgnoreFault() {
			return finish(false)
		}
	}

	timeout := s.seconds(req.GetTimeoutSeconds())
	if req.GetTimeoutSeconds() == 0 {
		timeout = s.seconds(s.opt.DefaultTimeout)
	}
	deadline := start.Add(timeout)

	for _, text := range lines {
		if beh.LineDelay > 0 {
			select {
			case <-time.After(beh.LineDelay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if !time.Now().Before(deadline) {
			// Out of time part way through.  The lines heard so far have
			// been sent, which is the contract: a script that said half
			// of what it meant to is a result worth seeing.
			return finish(false)
		}
		if err := stream.Send(&scriptv1.RunEvent{Event: &scriptv1.RunEvent_Line{
			Line: &scriptv1.Line{
				Text:        text,
				AtUnixNanos: time.Now().UnixNano(),
				From:        t.name,
				Source:      t.id,
			},
		}}); err != nil {
			return err
		}
		if d := req.GetDone(); d != "" && strings.Contains(text, d) {
			return finish(true)
		}
	}

	// Everything said and no sentinel: wait it out.  Reaching the
	// timeout is not an error -- Finished says the sentinel was never
	// seen and the caller decides what that means.
	select {
	case <-time.After(time.Until(deadline)):
	case <-ctx.Done():
		return ctx.Err()
	}
	return finish(false)
}
