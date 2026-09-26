package server

// Handing out the shared objects, over the wire that hands them out.
//
// Every one of these is about two clients, because what a pool decides
// is invisible from one: a daemon that gives the same place to both
// looks, from either of them, exactly like one that works.  What that
// costs on a grid is two scripts in one object, which is not a failure
// but a pair of plausible answers.

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/client"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// TestSlotsAreExclusive: the whole of it.  Twelve places on one avatar,
// and what one client has the other cannot.
func TestSlotsAreExclusive(t *testing.T) {
	r := newRig(t, agent.Caps{})
	a, b := r.dial(t), r.dial(t)
	defer a.Close()
	defer b.Close()

	ctx := context.Background()
	first, err := a.Slots(ctx, SlotsPerAgent, time.Minute, "")
	if err != nil {
		t.Fatalf("Slots: %v", err)
	}
	if !first.Held() {
		t.Fatalf("a whole avatar's places would not go to one client: %s", first.Why)
	}
	if len(first.Places) != SlotsPerAgent {
		t.Errorf("got %d places, want %d", len(first.Places), SlotsPerAgent)
	}

	second, err := b.TrySlots(ctx, 1, time.Minute, "")
	if err != nil {
		t.Fatalf("TrySlots: %v", err)
	}
	if second.Held() {
		t.Errorf("a place was given to two clients at once: %v", second.Places)
	}
	if second.Why == "" {
		t.Error("a request that got nothing did not say why")
	}
}

// TestAllOfThemOrNoneOverTheWire: a client given four of the eight it
// asked for could only hold them while waiting for the rest, which is
// how two clients deadlock against each other.  So it is given none --
// and what it did not take is still there for somebody who can use it.
func TestAllOfThemOrNoneOverTheWire(t *testing.T) {
	r := newRig(t, agent.Caps{})
	a, b := r.dial(t), r.dial(t)
	defer a.Close()
	defer b.Close()

	ctx := context.Background()
	held, err := a.Slots(ctx, SlotsPerAgent-3, time.Minute, "")
	if err != nil || !held.Held() {
		t.Fatalf("Slots: %v %v", held, err)
	}

	short, err := b.TrySlots(ctx, 8, time.Minute, "")
	if err != nil {
		t.Fatal(err)
	}
	if short.Held() {
		t.Fatalf("eight places came out of the three that were left: %v", short.Places)
	}
	if len(short.Places) != 0 {
		t.Errorf("a request that was refused came back holding %d places", len(short.Places))
	}

	fits, err := b.TrySlots(ctx, 3, time.Minute, "")
	if err != nil {
		t.Fatal(err)
	}
	if !fits.Held() {
		t.Errorf("the three that were left went nowhere: %s", fits.Why)
	}
}

// TestSlotsWaitTheirTurn: waiting is the default, and the wait ends when
// the holder gives them back -- without the waiter holding anything
// meanwhile, which is what makes waiting safe.
func TestSlotsWaitTheirTurn(t *testing.T) {
	r := newRig(t, agent.Caps{})
	a, b := r.dial(t), r.dial(t)
	defer a.Close()
	defer b.Close()

	ctx := context.Background()
	held, err := a.Slots(ctx, SlotsPerAgent, time.Minute, "")
	if err != nil || !held.Held() {
		t.Fatalf("Slots: %v %v", held, err)
	}

	got := make(chan *client.Grant, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		g, err := b.Slots(ctx, 4, time.Minute, "")
		if err != nil {
			t.Errorf("the waiting client: %v", err)
		}
		got <- g
	}()

	select {
	case g := <-got:
		t.Fatalf("a client was given places somebody else holds: %v", g.Places)
	case <-time.After(200 * time.Millisecond):
	}

	if err := a.ReleaseSlots(held.ID, true); err != nil {
		t.Fatalf("ReleaseSlots: %v", err)
	}
	select {
	case g := <-got:
		if !g.Held() || len(g.Places) != 4 {
			t.Errorf("the wait ended with %v (%s)", g.Places, g.Why)
		}
	case <-time.After(10 * time.Second):
		t.Error("giving the places back did not end the wait")
	}
}

// TestAStreamThatEndsGivesItsPlacesBack: the lease is the stream, which
// is the reason any of this lives on the stream rather than in an RPC.
// A client that exited, crashed or was unplugged said nothing about it,
// and there is nothing to clean up by hand.
func TestAStreamThatEndsGivesItsPlacesBack(t *testing.T) {
	r := newRig(t, agent.Caps{})
	a, b := r.dial(t), r.dial(t)
	defer b.Close()

	ctx := context.Background()
	if g, err := a.Slots(ctx, SlotsPerAgent, time.Minute, ""); err != nil || !g.Held() {
		t.Fatalf("Slots: %v %v", g, err)
	}
	a.Close()

	deadline := time.Now().Add(10 * time.Second)
	for {
		g, err := b.TrySlots(ctx, SlotsPerAgent, time.Minute, "")
		if err != nil {
			t.Fatalf("TrySlots: %v", err)
		}
		if g.Held() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("a stream that ended did not give its places back: %s", g.Why)
		}
	}
}

// TestPlacesComeBackDirtyUnlessTheHolderSaysOtherwise: a script left
// running in an object is a line the next holder reads as its own, so
// the next holder has to be told.  Saying nothing -- which includes
// every stream that just ended -- means it was not left fit to use.
func TestPlacesComeBackDirtyUnlessTheHolderSaysOtherwise(t *testing.T) {
	r := newRig(t, agent.Caps{})
	a := r.dial(t)
	defer a.Close()

	ctx := context.Background()

	// The first time out they are dirty, because whatever is in them is
	// not this daemon's doing.
	first, err := a.Slots(ctx, 1, time.Minute, "")
	if err != nil || !first.Held() {
		t.Fatalf("Slots: %v %v", first, err)
	}
	if !first.Places[0].Dirty {
		t.Error("a place nobody has ever cleared came out clean")
	}

	// Given back as cleared, it comes out clean.
	if err := a.ReleaseSlots(first.ID, true); err != nil {
		t.Fatal(err)
	}
	second, err := a.Slots(ctx, 1, time.Minute, "")
	if err != nil || !second.Held() {
		t.Fatalf("Slots: %v %v", second, err)
	}
	if second.Places[0].Dirty {
		t.Error("a place given back as cleared came out dirty")
	}

	// Given back without saying, it is dirty again.
	if err := a.ReleaseSlots(second.ID, false); err != nil {
		t.Fatal(err)
	}
	third, err := a.Slots(ctx, 1, time.Minute, "")
	if err != nil || !third.Held() {
		t.Fatalf("Slots: %v %v", third, err)
	}
	if !third.Places[0].Dirty {
		t.Error("a place given back without a word came out clean")
	}
}

// TestAGrantIsOnlyItsOwnClientsToGiveBack: a client that could release
// somebody else's grant could take their objects out from under them,
// which is the same collision by another road.
func TestAGrantIsOnlyItsOwnClientsToGiveBack(t *testing.T) {
	r := newRig(t, agent.Caps{})
	a, b := r.dial(t), r.dial(t)
	defer a.Close()
	defer b.Close()

	ctx := context.Background()
	held, err := a.Slots(ctx, SlotsPerAgent, time.Minute, "")
	if err != nil || !held.Held() {
		t.Fatalf("Slots: %v %v", held, err)
	}

	// b tries to give back what a holds, and to put its clock back.
	if err := b.ReleaseSlots(held.ID, true); err != nil {
		t.Fatalf("ReleaseSlots: %v", err)
	}
	if g, err := b.RenewSlots(ctx, held.ID, time.Hour); err != nil {
		t.Fatalf("RenewSlots: %v", err)
	} else if g.Held() {
		t.Error("a client renewed somebody else's grant")
	}

	// Which changed nothing: they are still a's.
	got, err := b.TrySlots(ctx, 1, time.Minute, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Held() {
		t.Error("one client gave back another's places")
	}
}

// TestRenewingSaysTheNewDeadline: work that cannot say in advance how
// long it will take renews rather than guessing high, and guessing high
// is what makes a lease useless as a way of getting objects back.
func TestRenewingSaysTheNewDeadline(t *testing.T) {
	r := newRig(t, agent.Caps{})
	a := r.dial(t)
	defer a.Close()

	ctx := context.Background()
	held, err := a.Slots(ctx, 1, time.Minute, "")
	if err != nil || !held.Held() {
		t.Fatalf("Slots: %v %v", held, err)
	}

	got, err := a.RenewSlots(ctx, held.ID, time.Hour)
	if err != nil {
		t.Fatalf("RenewSlots: %v", err)
	}
	if !got.Held() {
		t.Fatalf("a grant that exists would not renew: %s", got.Why)
	}
	if !got.Expires.After(held.Expires) {
		t.Errorf("renewed to %v, which is no later than %v", got.Expires, held.Expires)
	}
	if got.ID != held.ID {
		t.Errorf("renewing changed the grant from %q to %q", held.ID, got.ID)
	}
}

// TestMoreThanTheDaemonHasIsRefusedRatherThanWaitedFor: waiting for
// objects that do not exist is waiting for ever, and a client that asked
// for more than there is deserves to be told rather than hung.
func TestMoreThanTheDaemonHasIsRefusedRatherThanWaitedFor(t *testing.T) {
	r := newRig(t, agent.Caps{})
	a := r.dial(t)
	defer a.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	g, err := a.Slots(ctx, SlotsPerAgent*10, time.Minute, "")
	if err != nil {
		t.Fatalf("Slots: %v", err)
	}
	if g.Held() {
		t.Errorf("the daemon granted %d places it does not have", len(g.Places))
	}
	if g.Why == "" {
		t.Error("asking for more than there is did not say why it could not be done")
	}
}

// TestAskingForNothing: a request for no places is a caller's mistake,
// and a grant of nothing is a grant that holds nothing and cannot be
// given back.
func TestAskingForNothingIsRefused(t *testing.T) {
	r := newRig(t, agent.Caps{})
	a := r.dial(t)
	defer a.Close()

	if _, err := a.Slots(context.Background(), 0, time.Minute, ""); err == nil {
		t.Error("a request for no places was sent rather than refused")
	}
}

// TestARenewalAndAWaitOnOneStreamEachGetTheirOwnAnswer: a renewal is
// answered at once and a wait when places come free, so on one stream
// the later request is answered first.  Matched by arrival, the waiter
// would be handed the renewed grant as new places and the renewal would
// wait for somebody else's.
func TestARenewalAndAWaitOnOneStreamEachGetTheirOwnAnswer(t *testing.T) {
	r := newRig(t, agent.Caps{})
	a, b := r.dial(t), r.dial(t)
	defer a.Close()
	defer b.Close()

	ctx := context.Background()
	mine, err := b.Slots(ctx, 1, time.Minute, "")
	if err != nil || !mine.Held() {
		t.Fatalf("Slots: %v %v", mine, err)
	}
	rest, err := a.Slots(ctx, SlotsPerAgent-1, time.Minute, "")
	if err != nil || !rest.Held() {
		t.Fatalf("Slots: %v %v", rest, err)
	}

	waiting := make(chan *client.Grant, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		g, err := b.Slots(ctx, 2, time.Minute, "")
		if err != nil {
			t.Errorf("the waiting request: %v", err)
		}
		waiting <- g
	}()
	time.Sleep(200 * time.Millisecond) // the wait is on the daemon

	renewCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	renewed, err := b.RenewSlots(renewCtx, mine.ID, time.Hour)
	if err != nil {
		t.Fatalf("RenewSlots: %v", err)
	}
	if renewed.ID != mine.ID || len(renewed.Places) != 1 {
		t.Errorf("the renewal was answered with %q holding %d places, want %q holding 1",
			renewed.ID, len(renewed.Places), mine.ID)
	}
	select {
	case g := <-waiting:
		t.Fatalf("the wait ended with %q while nothing was free", g.ID)
	default:
	}

	if err := a.ReleaseSlots(rest.ID, true); err != nil {
		t.Fatal(err)
	}
	select {
	case g := <-waiting:
		if !g.Held() || g.ID == mine.ID || len(g.Places) != 2 {
			t.Errorf("the wait ended with %q holding %d places", g.ID, len(g.Places))
		}
	case <-time.After(10 * time.Second):
		t.Error("the wait was never answered")
	}
}

// heldStream stands in for a client that reads only when the test does:
// each frame the daemon sends waits in Send until the test takes it off
// sent.
type heldStream struct {
	grpc.ServerStream
	ctx  context.Context
	in   chan *pb.ClientPacket
	sent chan *pb.ServerPacket
}

func (s *heldStream) Context() context.Context { return s.ctx }

func (s *heldStream) Recv() (*pb.ClientPacket, error) {
	select {
	case p := <-s.in:
		return p, nil
	case <-s.ctx.Done():
		return nil, io.EOF
	}
}

func (s *heldStream) Send(p *pb.ServerPacket) error {
	select {
	case s.sent <- p:
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

// holdStream attaches a heldStream to the rig's session, with room for
// depth frames from the test, and returns it with the daemon's record of
// the client and what Stream returns.
func holdStream(t *testing.T, r *rig, depth int) (*heldStream, *Client, chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st := &heldStream{
		ctx:  ctx,
		in:   make(chan *pb.ClientPacket, depth),
		sent: make(chan *pb.ServerPacket),
	}
	st.in <- &pb.ClientPacket{Body: &pb.ClientPacket_Attach{Attach: &pb.Attach{Agent: "example"}}}
	ended := make(chan error, 1)
	go func() { ended <- r.srv.Stream(st) }()

	select {
	case p := <-st.sent:
		if p.GetAttached() == nil {
			t.Fatalf("the stream opened with %v", p)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the attach was never answered")
	}

	h, err := r.srv.lookup("example")
	if err != nil {
		t.Fatal(err)
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		return st, c, ended
	}
	t.Fatal("the attached client is not on the session")
	return nil, nil, nil
}

// stall has the daemon's writer take one frame and wait in Send with it,
// which is what a client that has stopped reading looks like.
func stall(t *testing.T, c *Client) {
	t.Helper()
	c.send(&pb.ServerPacket{Body: &pb.ServerPacket_Notice{Notice: &pb.AgentEvent{}}})
	waitFor(t, 5*time.Second, "the writer to take a frame", func() bool { return len(c.out) == 0 })
}

// TestAnAnswerIsSentWhenTheRelayQueueIsFull: relayed traffic is dropped
// when a client falls behind, which costs nothing on the grid.  A grant
// or a lock dropped the same way would leave the client waiting for ever
// for what the daemon believes it gave.  Answers go ahead of the queue.
func TestAnAnswerIsSentWhenTheRelayQueueIsFull(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ask   *pb.ClientPacket
		wrong func(*pb.ServerPacket) string
	}{{
		name: "a grant",
		ask:  &pb.ClientPacket{Body: &pb.ClientPacket_Slots{Slots: &pb.Slots{Want: 1, Try: true, Request: 9}}},
		wrong: func(p *pb.ServerPacket) string {
			if g := p.GetGranted(); g.GetRequest() != 9 || g.GetGrant() == "" {
				return fmt.Sprintf("%v, want request 9 granted", p)
			}
			return ""
		},
	}, {
		name: "a lock",
		ask:  &pb.ClientPacket{Body: &pb.ClientPacket_Lock{Lock: &pb.Lock{Name: "the workbench", Try: true}}},
		wrong: func(p *pb.ServerPacket) string {
			if l := p.GetLocked(); l.GetName() != "the workbench" || !l.GetHeld() {
				return fmt.Sprintf("%v, want the workbench held", p)
			}
			return ""
		},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			r := newSession(t, agent.Caps{})
			st, c, _ := holdStream(t, r, 4)

			stall(t, c)
			relayed := &pb.ServerPacket{Body: &pb.ServerPacket_Notice{Notice: &pb.AgentEvent{}}}
			for i := 0; i < streamDepth; i++ {
				c.send(relayed)
			}
			c.send(relayed)
			if n := c.dropped.Load(); n != 1 {
				t.Fatalf("%d frames dropped filling the queue, want 1: it was not full", n)
			}

			st.in <- tc.ask
			waitFor(t, 5*time.Second, "the answer", func() bool { return len(c.ctl) > 0 || c.dropped.Load() > 1 })

			<-st.sent // the frame the writer was holding
			if why := tc.wrong(<-st.sent); why != "" {
				t.Errorf("the next frame after the one held was %s", why)
			}
			if n := c.dropped.Load(); n != 1 {
				t.Errorf("%d frames dropped, want only the relay that did not fit", n)
			}
		})
	}
}

// TestAClientThatWillNotReadItsAnswersHasItsStreamEnded: answers about
// objects are never dropped, so a client that lets more pile up than the
// daemon keeps for it loses its stream instead -- and with it what it
// holds, which is the one thing a dropped grant could not give back.
func TestAClientThatWillNotReadItsAnswersHasItsStreamEnded(t *testing.T) {
	r := newSession(t, agent.Caps{})
	st, c, ended := holdStream(t, r, controlDepth+2)

	stall(t, c)
	for i := 0; i <= controlDepth; i++ {
		st.in <- &pb.ClientPacket{Body: &pb.ClientPacket_Slots{
			Slots: &pb.Slots{Want: 1, Try: true, Request: uint64(i + 1)},
		}}
	}
	waitFor(t, 10*time.Second, "the answers to overflow", func() bool {
		select {
		case <-c.jammed:
			return true
		default:
			return false
		}
	})

	// Reading again lets the writer see it.
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-st.sent:
		case err := <-ended:
			if status.Code(err) != codes.ResourceExhausted {
				t.Fatalf("the stream ended with %v, want ResourceExhausted", err)
			}
			sp := r.srv.slotsOf()
			sp.mu.Lock()
			held := len(sp.grants)
			sp.mu.Unlock()
			if held != 0 {
				t.Errorf("%d grants outlived the stream that held them", held)
			}
			return
		case <-deadline:
			t.Fatal("a client with more answers waiting than the daemon keeps was left attached")
		}
	}
}

// TestAGrantSettledAsItsStreamEndsIsNotKept: a request can be settled
// just after the end of its stream gave back everything the client
// held.  Kept then, the grant would belong to nobody and hold its places
// until it ran out.  The order is made here by asking for a client whose
// stream has already ended, which is what the late request sees.
func TestAGrantSettledAsItsStreamEndsIsNotKept(t *testing.T) {
	r := newSession(t, agent.Caps{})
	sp := r.srv.slotsOf()
	ctx := context.Background()

	gone := &Client{ctl: make(chan *pb.ServerPacket, 4), jammed: make(chan struct{})}
	gone.closed.Store(true)
	sp.releaseAll(gone)
	sp.ask(ctx, gone, &pb.Slots{Want: SlotsPerAgent, Try: true, Request: 1})

	sp.mu.Lock()
	kept := len(sp.grants)
	sp.mu.Unlock()
	if kept != 0 {
		t.Errorf("%d grants were kept for a client whose stream had ended", kept)
	}

	next := &Client{ctl: make(chan *pb.ServerPacket, 4), jammed: make(chan struct{})}
	sp.ask(ctx, next, &pb.Slots{Want: SlotsPerAgent, Try: true, Request: 2})
	select {
	case p := <-next.ctl:
		if g := p.GetGranted(); g.GetGrant() == "" {
			t.Errorf("the places settled for a client that had gone were not free: %s", g.GetWhy())
		}
	default:
		t.Fatal("the next request was not answered")
	}
}
