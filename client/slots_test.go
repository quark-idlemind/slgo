package client

// Asking a daemon that is not there for places.
//
// The fake daemon in client_test.go answers nothing of its own accord
// here: each test reads the request off the stream and pushes back the
// answer it wants, in the order it wants, which is the point -- a real
// daemon answers a try at once and a wait when places come free.

import (
	"context"
	"errors"
	"testing"
	"time"

	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// grantFor is the daemon's answer to one request.
func grantFor(request uint64, grant, why string) *pb.ServerPacket {
	g := &pb.SlotsGranted{Request: request, Grant: grant, Why: why}
	if grant != "" {
		g.Held = []*pb.SlotHeld{{Agent: "quark", Slot: 3}}
		g.Expires = time.Now().Add(time.Minute).Unix()
	}
	return &pb.ServerPacket{Body: &pb.ServerPacket_Granted{Granted: g}}
}

type grantResult struct {
	g   *Grant
	err error
}

// TestTwoRequestsOnOneStreamEachGetTheirOwnAnswer: a wait is answered
// when places come free and a try at once, so the second request can be
// answered first.  Handing the first answer to the oldest request would
// give the waiter a refusal and the try somebody else's grant.
func TestTwoRequestsOnOneStreamEachGetTheirOwnAnswer(t *testing.T) {
	t.Parallel()
	d, conn := attachFake(t)
	ctx := context.Background()

	waiting := make(chan grantResult, 1)
	go func() {
		g, err := conn.Slots(ctx, 4, time.Minute, "")
		waiting <- grantResult{g, err}
	}()
	first := waitForSlots(t, d)

	trying := make(chan grantResult, 1)
	go func() {
		g, err := conn.TrySlots(ctx, 1, time.Minute, "")
		trying <- grantResult{g, err}
	}()
	second := waitForSlots(t, d)

	if first.Request == 0 || second.Request == 0 || first.Request == second.Request {
		t.Fatalf("the requests were numbered %d and %d", first.Request, second.Request)
	}

	d.relay <- grantFor(second.Request, "", "1 objects are not free")
	d.relay <- grantFor(first.Request, "g1", "")

	for _, c := range []struct {
		name  string
		got   chan grantResult
		grant string
	}{{"the try", trying, ""}, {"the wait", waiting, "g1"}} {
		select {
		case r := <-c.got:
			if r.err != nil {
				t.Errorf("%s: %v", c.name, r.err)
			} else if r.g.ID != c.grant {
				t.Errorf("%s was answered with grant %q (%s), want %q", c.name, r.g.ID, r.g.Why, c.grant)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("%s was never answered", c.name)
		}
	}
}

// TestAGrantForAWaitGivenUpOnIsGivenBackNotClean: the daemon is not told
// that a caller stopped waiting, and grants it anyway.  Kept, those
// places would be nobody's until the grant ran out.  Given back clean,
// they would lose the mark the last holder left on them.
func TestAGrantForAWaitGivenUpOnIsGivenBackNotClean(t *testing.T) {
	t.Parallel()
	d, conn := attachFake(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := conn.Slots(ctx, 4, time.Minute, "")
		done <- err
	}()
	ask := waitForSlots(t, d)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Slots = %v, want the cancellation", err)
	}

	d.relay <- grantFor(ask.Request, "g7", "")
	rel := waitForRelease(t, d)
	if rel.GetGrant() != "g7" {
		t.Errorf("gave back %q, want the late grant g7", rel.GetGrant())
	}
	if rel.GetClean() {
		t.Error("a grant nobody used was given back as clean")
	}
}

// TestABoundedWaitGivesUpOnADaemonThatWaitsOn: a daemon older than
// wait_seconds never answers while nothing is free, so this side keeps
// the same deadline.  The wait goes over in whole seconds, and the
// deadline is the one the daemon was told.  What the daemon grants after
// that is given back, not clean, as for any wait given up on.
func TestABoundedWaitGivesUpOnADaemonThatWaitsOn(t *testing.T) {
	t.Parallel()
	d, conn := attachFake(t)

	start := time.Now()
	done := make(chan error, 1)
	go func() {
		_, err := conn.SlotsWithin(context.Background(), 4, time.Minute, 300*time.Millisecond, "")
		done <- err
	}()
	ask := waitForSlots(t, d)
	if ask.GetWaitSeconds() != 1 {
		t.Errorf("a wait of 300ms went to the daemon as %d seconds, want 1", ask.GetWaitSeconds())
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrStillBusy) {
			t.Fatalf("SlotsWithin = %v, want ErrStillBusy", err)
		}
		if took := time.Since(start); took < time.Second {
			t.Errorf("gave up after %v, before the second the daemon was told", took)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a bounded wait on a daemon that never answers did not give up")
	}

	d.relay <- grantFor(ask.Request, "g8", "")
	rel := waitForRelease(t, d)
	if rel.GetGrant() != "g8" {
		t.Errorf("gave back %q, want the late grant g8", rel.GetGrant())
	}
	if rel.GetClean() {
		t.Error("a grant nobody used was given back as clean")
	}
}

// TestARefusalOnceTheWaitIsUpIsTheWaitRunningOut: the daemon says a
// bounded wait ran out no sooner than this side's own deadline, so the
// two can be there together.  Whichever is read first, the caller is
// told the same thing -- which a select that chose between them at
// random would not do.
func TestARefusalOnceTheWaitIsUpIsTheWaitRunningOut(t *testing.T) {
	t.Parallel()
	_, conn := attachFake(t)
	stream := conn.streamOrErr()

	for i := 0; i < 50; i++ {
		w := conn.grants.await(true)
		conn.grants.deliver(grantFor(w.id, "", "4 objects were still not free after 1s").GetGranted())
		_, err := conn.ask(context.Background(), stream, w, &pb.ClientPacket{Body: &pb.ClientPacket_Slots{
			Slots: &pb.Slots{Want: 4, WaitSeconds: 1, Request: w.id},
		}}, time.Nanosecond)
		if !errors.Is(err, ErrStillBusy) {
			t.Fatalf("the daemon's own answer that the wait ran out came back as %v, want ErrStillBusy", err)
		}
	}
}

// TestARefusalBeforeTheWaitIsUpSaysWhy: a daemon refuses a bounded wait
// early only for a reason of its own, such as more objects than it has,
// and that is said in its words rather than as the wait running out.
func TestARefusalBeforeTheWaitIsUpSaysWhy(t *testing.T) {
	t.Parallel()
	d, conn := attachFake(t)

	got := make(chan grantResult, 1)
	go func() {
		g, err := conn.SlotsWithin(context.Background(), 40, time.Minute, time.Minute, "")
		got <- grantResult{g, err}
	}()
	ask := waitForSlots(t, d)
	const why = "40 objects were asked for and this daemon's avatars have 12"
	d.relay <- grantFor(ask.Request, "", why)

	select {
	case r := <-got:
		if r.err != nil {
			t.Fatalf("SlotsWithin = %v, want the daemon's refusal", r.err)
		}
		if r.g.Held() || r.g.Why != why {
			t.Errorf("SlotsWithin = %+v, want nothing held and why %q", r.g, why)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the refusal never reached the caller")
	}
}

// TestALateAnswerToARenewalIsNotGivenBack: a renewal's answer names a
// grant its caller still holds and is using, so giving that back would
// hand the caller's objects to somebody else in the middle of a run.
func TestALateAnswerToARenewalIsNotGivenBack(t *testing.T) {
	t.Parallel()
	d, conn := attachFake(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := conn.RenewSlots(ctx, "g7", time.Minute)
		done <- err
	}()
	var renew *pb.RenewSlots
	for renew == nil {
		select {
		case p := <-d.sent:
			renew = p.GetRenewSlots()
		case <-time.After(5 * time.Second):
			t.Fatal("the daemon was never asked to renew")
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("RenewSlots = %v, want the cancellation", err)
	}

	d.relay <- grantFor(renew.Request, "g7", "")
	select {
	case p := <-d.sent:
		t.Errorf("a late renewal was answered with %v", p)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestAnOlderDaemonsAnswersGoFirstComeFirstServed: a daemon older than
// the numbers answers with none, and the answer goes to whoever has
// waited longest -- which is what every client did before there were
// numbers, and right while one request is out at a time.
func TestAnOlderDaemonsAnswersGoFirstComeFirstServed(t *testing.T) {
	t.Parallel()
	d, conn := attachFake(t)
	ctx := context.Background()

	first, second := make(chan grantResult, 1), make(chan grantResult, 1)
	go func() {
		g, err := conn.Slots(ctx, 1, time.Minute, "")
		first <- grantResult{g, err}
	}()
	waitForSlots(t, d)
	go func() {
		g, err := conn.Slots(ctx, 1, time.Minute, "")
		second <- grantResult{g, err}
	}()
	waitForSlots(t, d)

	d.relay <- grantFor(0, "gA", "")
	select {
	case r := <-first:
		if r.err != nil || r.g.ID != "gA" {
			t.Errorf("the first request got %+v, %v; want gA", r.g, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("an answer with no number did not reach the oldest request")
	}

	d.relay <- grantFor(0, "gB", "")
	select {
	case r := <-second:
		if r.err != nil || r.g.ID != "gB" {
			t.Errorf("the second request got %+v, %v; want gB", r.g, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the second answer with no number went nowhere")
	}
}

// TestAnAnswerThatArrivesAsTheWaitEndsIsGivenBack: the answer can land
// in the reply between the caller deciding to stop and it saying so,
// and then nothing else will ever read it.
func TestAnAnswerThatArrivesAsTheWaitEndsIsGivenBack(t *testing.T) {
	t.Parallel()
	var g granting

	w := g.await(true)
	g.deliver(grantFor(w.id, "g1", "").GetGranted())
	if late := g.giveUp(w); late != "g1" {
		t.Errorf("giving up after the answer came returned %q, want g1 to give back", late)
	}

	// A renewal's answer names a grant the caller still has, so the same
	// race leaves nothing to give back.
	r := g.await(false)
	g.deliver(grantFor(r.id, "g1", "").GetGranted())
	if late := g.giveUp(r); late != "" {
		t.Errorf("giving up on a renewal returned %q to give back", late)
	}

	// And one given up on first is given back when its answer comes.
	x := g.await(true)
	if late := g.giveUp(x); late != "" {
		t.Errorf("giving up before any answer returned %q", late)
	}
	if late := g.deliver(grantFor(x.id, "g2", "").GetGranted()); late != "g2" {
		t.Errorf("the late answer returned %q to give back, want g2", late)
	}
	// Once.
	if late := g.deliver(grantFor(x.id, "g2", "").GetGranted()); late != "" {
		t.Errorf("a second answer to the same request returned %q", late)
	}
}

// TestAsksCarryPerAgent: a connection that said how many places per
// avatar it wears sends it with every ask, the way a program with the
// auto pool's twenty-four does, and one that never said sends zero,
// which the daemon reads as the twelve of a client older than the field.
func TestAsksCarryPerAgent(t *testing.T) {
	t.Parallel()
	d, conn := attachFake(t)

	ask := func(try bool) *pb.Slots {
		go func() {
			if try {
				conn.TrySlots(context.Background(), 1, time.Minute, "")
			} else {
				conn.SlotsWithin(context.Background(), 1, time.Minute, time.Second, "")
			}
		}()
		s := waitForSlots(t, d)
		d.relay <- grantFor(s.Request, "", "not free")
		return s
	}

	if got := ask(true).GetPerAgent(); got != 0 {
		t.Errorf("a connection that never said sent per_agent %d, want 0", got)
	}
	conn.SetSlotsPerAgent(24)
	for _, try := range []bool{true, false} {
		if got := ask(try).GetPerAgent(); got != 24 {
			t.Errorf("per_agent = %d (try %v), want 24", got, try)
		}
	}
}

// waitForSlots reads the next request for places off the stream.
func waitForSlots(t *testing.T, d *fakeDaemon) *pb.Slots {
	t.Helper()
	for {
		select {
		case p := <-d.sent:
			if s := p.GetSlots(); s != nil {
				return s
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the daemon was never asked for places")
			return nil
		}
	}
}

// waitForRelease reads the next grant given back off the stream.
func waitForRelease(t *testing.T, d *fakeDaemon) *pb.ReleaseSlots {
	t.Helper()
	for {
		select {
		case p := <-d.sent:
			if r := p.GetReleaseSlots(); r != nil {
				return r
			}
		case <-time.After(5 * time.Second):
			t.Fatal("nothing was given back")
			return nil
		}
	}
}
