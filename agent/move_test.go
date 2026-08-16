package agent

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

// fakeRegion is a simulator and the HTTP half that goes with it: a seed
// capability naming this region's own event queue, and the queue.
//
// Two of these are what a move is tested against.  Everything that has
// to change when the avatar arrives somewhere else -- the address, the
// sequence numbers, the capability URLs, the queue -- is different
// between them, so a move that half worked shows up as one of them still
// pointing at the region left behind.
type fakeRegion struct {
	sim   *fakeSim
	eq    *eqServer
	http  *httptest.Server
	polls atomic.Int64
}

func newRegion(t *testing.T, name string, id msg.UUID) *fakeRegion {
	t.Helper()

	r := &fakeRegion{sim: newFakeSim(t), eq: &eqServer{}}
	r.sim.regionNm = name
	r.sim.regionID = id

	queue := r.eq.handler()
	mux := http.NewServeMux()
	mux.HandleFunc("/seed", func(w http.ResponseWriter, _ *http.Request) {
		body, err := llsd.Encode(map[string]any{
			EventQueueCap:       r.http.URL + "/event",
			"SimulatorFeatures": r.http.URL + "/features",
		})
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/llsd+xml")
		w.Write(body)
	})
	mux.HandleFunc("/event", func(w http.ResponseWriter, req *http.Request) {
		r.polls.Add(1)
		// A simulator holds a poll open until it has something to say.
		// eqServer answers an empty one at once, and a poll loop
		// spinning at that rate is enough on its own to move a
		// goroutine count around.
		time.Sleep(20 * time.Millisecond)
		queue.ServeHTTP(w, req)
	})
	r.http = httptest.NewServer(mux)

	go r.sim.run()
	t.Cleanup(func() {
		r.http.Close()
		r.sim.close()
	})
	return r
}

func (r *fakeRegion) seed() string { return r.http.URL + "/seed" }

func (r *fakeRegion) saw(name string) bool { return r.count(name) > 0 }

func (r *fakeRegion) count(name string) int {
	n := 0
	for _, seen := range r.sim.got() {
		if seen == name {
			n++
		}
	}
	return n
}

// twoRegions stands a session up in the first of two regions.
func twoRegions(t *testing.T, opts Options) (*Agent, *fakeRegion, *fakeRegion) {
	t.Helper()

	from := newRegion(t, "the region left", msg.MustParseUUID("12b57e57-7e57-c0de-efe3-b327af5dfe62"))
	to := newRegion(t, "the region arrived at", msg.MustParseUUID("1c117e57-7e57-c0de-da64-e42aeda52a0a"))

	acct := testAccount(from.sim)
	acct.SeedCapability = from.seed()
	if opts.Timeout == 0 {
		opts.Timeout = 5 * time.Second
	}
	a, err := Connect(context.Background(), acct, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a, from, to
}

// TestAMoveTakesTheSessionToTheOtherSimulator: the whole of stage 2 in
// one assertion -- the session handshakes at the new address and answers
// with the new region afterwards.
func TestAMoveTakesTheSessionToTheOtherSimulator(t *testing.T) {
	a, from, to := twoRegions(t, Options{})

	if err := a.moveTo(context.Background(), to.sim.addr(), to.seed()); err != nil {
		t.Fatalf("moveTo: %v", err)
	}

	if !to.saw("UseCircuitCode") || !to.saw("CompleteAgentMovement") {
		t.Errorf("the new simulator saw %v", to.sim.got())
	}
	if got := a.RegionName(); got != to.sim.regionNm {
		t.Errorf("region = %q, want %q", got, to.sim.regionNm)
	}
	if got := a.RegionHandle(); got == 0 {
		t.Error("no region handle after the move")
	}
	// The capabilities are the new region's, fetched from the seed the
	// move was given.
	if u, _ := a.Caps().Get(EventQueueCap); !strings.HasPrefix(u, to.http.URL) {
		t.Errorf("%s = %q, want one of %s", EventQueueCap, u, to.http.URL)
	}
	// The circuit was opened once at each simulator: the same circuit
	// code, which is what makes this a teleport rather than a relog,
	// but not a second claim on the circuit already open.
	if n := from.count("UseCircuitCode"); n != 1 {
		t.Errorf("the region left saw UseCircuitCode %d times", n)
	}
	select {
	case <-a.Done():
		t.Fatalf("the session ended: %v", a.Err())
	default:
	}
}

// TestTheThingsHoldingTheCircuitSurviveAMove: six places outside this
// package hold a.Send, a.Recv or a.Disp and none of them is told a
// teleport happened, so a move must change the socket underneath them
// rather than replace them.
func TestTheThingsHoldingTheCircuitSurviveAMove(t *testing.T) {
	a, from, to := twoRegions(t, Options{SkipCaps: true})
	send, recv, disp := a.Send, a.Recv, a.Disp

	if err := a.moveTo(context.Background(), to.sim.addr(), to.seed()); err != nil {
		t.Fatalf("moveTo: %v", err)
	}
	if a.Send != send || a.Recv != recv || a.Disp != disp {
		t.Fatal("a move replaced the sender, receiver or dispatcher; " +
			"everything holding one of them is now writing nowhere")
	}

	// And what goes into the one they are all holding comes out at the
	// new simulator.
	if err := send.Send(context.Background(), &msg.AgentPause{}); err != nil {
		t.Fatal(err)
	}
	to.sim.waitSeen(t, "AgentPause", 5*time.Second)
	if from.saw("AgentPause") {
		t.Error("the message went to the region the avatar left")
	}
}

// TestTheNewSimulatorsSequenceNumbersAreNotTakenForRetransmissions: the
// new simulator numbers its packets from one, and the dispatcher's ring
// still holds the old simulator's one.  Without Dispatcher.Forget its
// handshake is dropped as a retransmission and the move never completes
// at all; this then goes on to check a message after the handshake,
// under a number the old region certainly used.
func TestTheNewSimulatorsSequenceNumbersAreNotTakenForRetransmissions(t *testing.T) {
	a, from, to := twoRegions(t, Options{SkipCaps: true})

	var stats atomic.Int64
	if err := a.Handle("SimStats", func(*msg.Packet) { stats.Add(1) }); err != nil {
		t.Fatal(err)
	}

	// Fill the ring with the old region's numbering, well past
	// anything the new simulator will reach during its handshake.
	const filled = 30
	for i := 0; i < filled; i++ {
		from.sim.send(&msg.SimStats{}, 0)
	}
	waitFor(t, "the old region's packets to arrive", func() bool {
		return stats.Load() >= filled
	})

	if err := a.moveTo(context.Background(), to.sim.addr(), to.seed()); err != nil {
		t.Fatalf("moveTo: %v", err)
	}

	was := stats.Load()
	to.sim.send(&msg.SimStats{}, 0)
	waitFor(t, "the new region's packet to be delivered", func() bool {
		return stats.Load() > was
	})
}

// TestAReliableMessageToTheOldSimulatorIsNotResentToTheNewOne: a
// reliable message in flight when the avatar leaves will never be
// acknowledged, and the retransmission would arrive at a simulator it
// was not addressed to and was never true of.
func TestAReliableMessageToTheOldSimulatorIsNotResentToTheNewOne(t *testing.T) {
	t.Parallel()

	a, from, to := twoRegions(t, Options{SkipCaps: true})

	// Let the handshake settle before the old simulator goes quiet, so
	// that what is left unacknowledged is only what is sent below.
	from.sim.waitSeen(t, "RegionHandshakeReply", 5*time.Second)
	time.Sleep(200 * time.Millisecond)

	from.sim.mu.Lock()
	from.sim.silent = true
	from.sim.mu.Unlock()

	if err := a.Send.SendReliable(context.Background(), &msg.AgentPause{}); err != nil {
		t.Fatal(err)
	}
	from.sim.waitSeen(t, "AgentPause", 5*time.Second)

	if err := a.moveTo(context.Background(), to.sim.addr(), to.seed()); err != nil {
		t.Fatalf("moveTo: %v", err)
	}

	// Long enough for the sender's first retransmission, which is one
	// timeout after the message went out.
	time.Sleep(4500 * time.Millisecond)
	if to.saw("AgentPause") {
		t.Errorf("a message meant for the region left was resent into the new one: %v",
			to.sim.got())
	}
}

// TestASimulatorThatNeverCompletesTheMovementEndsTheSession: there is
// nothing to go back to.  The origin hands the agent off before it says
// where to, so a circuit restored to it would be a circuit to a region
// the avatar is not in -- which reads as a healthy session and is not
// one.  Ending it says which move failed, and lets whatever hosts the
// session log in again rather than wait out the watchdog.
func TestASimulatorThatNeverCompletesTheMovementEndsTheSession(t *testing.T) {
	t.Parallel()

	a, _, to := twoRegions(t, Options{SkipCaps: true, Timeout: 500 * time.Millisecond})
	to.sim.mu.Lock()
	to.sim.noMovement = true
	to.sim.mu.Unlock()

	err := a.moveTo(context.Background(), to.sim.addr(), to.seed())
	if err == nil {
		t.Fatal("a simulator that never answered was taken for a successful move")
	}
	for _, want := range []string{"move to", to.sim.addr().String(), "AgentMovementComplete"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err, want)
		}
	}

	select {
	case <-a.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the session outlived the move that failed")
	}
	if got := a.Err(); got == nil || !strings.Contains(got.Error(), "move to") {
		t.Errorf("Err = %v, want the move that ended it", got)
	}
}

// TestADialThatFailsLeavesTheSessionWhereItWas: the dial is first
// because it is the one failure that changes nothing.
func TestADialThatFailsLeavesTheSessionWhereItWas(t *testing.T) {
	a, from, _ := twoRegions(t, Options{SkipCaps: true})

	// An address with an IP that is neither four bytes nor sixteen.
	err := a.moveTo(context.Background(), &net.UDPAddr{IP: net.IP{1, 2, 3}, Port: 1}, "")
	if err == nil {
		t.Fatal("moveTo accepted an address it cannot have dialled")
	}
	select {
	case <-a.Done():
		t.Fatalf("a failed dial ended the session: %v", a.Err())
	default:
	}

	if err := a.Send.Send(context.Background(), &msg.AgentPause{}); err != nil {
		t.Fatal(err)
	}
	from.sim.waitSeen(t, "AgentPause", 5*time.Second)
}

// TestASessionThatHasEndedIsNotMoved: Close closes done, cancels,
// closes the socket and then waits on the session's goroutines.  A move
// is the first thing here that spawns after Connect returned, so a move
// racing a Close would add the new region's poll to that group during
// the wait -- which panics -- and would dial a connection nothing would
// ever close.  A teleport for a session that is over is not something to
// half-perform, so it is refused.
func TestASessionThatHasEndedIsNotMoved(t *testing.T) {
	a, _, to := twoRegions(t, Options{OnEvent: func(string, []byte) {}})
	a.Close()

	err := a.moveTo(context.Background(), to.sim.addr(), to.seed())
	if err == nil {
		t.Fatal("a session that had ended was moved anyway")
	}
	if !strings.Contains(err.Error(), "the session has ended") {
		t.Errorf("error %q does not say the session was over", err)
	}
	if to.saw("UseCircuitCode") {
		t.Error("the new simulator was talked to on behalf of a session that had ended")
	}
	// Nothing was spawned into the group Close had finished waiting on.
	if n := to.polls.Load(); n != 0 {
		t.Errorf("the new region's queue was polled %d times after Close", n)
	}
}

// TestAMovePollsTheNewRegionsQueueAndClosesTheOld: the queue is one long
// poll keeping an acknowledgement sequence, and both halves of it are
// per region.
func TestAMovePollsTheNewRegionsQueueAndClosesTheOld(t *testing.T) {
	events := make(chan string, 8)
	a, from, to := twoRegions(t, Options{
		OnEvent: func(name string, _ []byte) {
			select {
			case events <- name:
			default:
			}
		},
	})

	// An event, so that the poll has an id to acknowledge -- which is
	// what the done that closes the queue carries.
	from.eq.push("ParcelProperties", map[string]any{"x": int64(1)})
	select {
	case <-events:
	case <-time.After(5 * time.Second):
		t.Fatal("the queue in the first region was never polled")
	}

	if err := a.moveTo(context.Background(), to.sim.addr(), to.seed()); err != nil {
		t.Fatalf("moveTo: %v", err)
	}

	to.eq.push("ParcelProperties", map[string]any{"x": int64(2)})
	select {
	case <-events:
	case <-time.After(5 * time.Second):
		t.Fatal("the queue in the new region is not being polled")
	}

	waitFor(t, "the old region's queue to be told we are finished", func() bool {
		from.eq.mu.Lock()
		defer from.eq.mu.Unlock()
		return from.eq.dones > 0
	})

	// And nothing is still polling the region the avatar left.
	settled := from.polls.Load()
	time.Sleep(300 * time.Millisecond)
	if now := from.polls.Load(); now != settled {
		t.Errorf("the old region was polled %d more times after the move", now-settled)
	}
}

// TestAMoveLeavesNoPollBehind: the poll is a goroutine per region, so
// getting this wrong leaks one per teleport rather than failing.
func TestAMoveLeavesNoPollBehind(t *testing.T) {
	a, from, to := twoRegions(t, Options{OnEvent: func(string, []byte) {}})

	a.eq.mu.Lock()
	stopped := a.eq.stopped
	a.eq.mu.Unlock()
	if stopped == nil {
		t.Fatal("no poll was running to leave behind")
	}
	waitFor(t, "the first region to be polled", func() bool { return from.polls.Load() > 0 })

	before := runtime.NumGoroutine()
	if err := a.moveTo(context.Background(), to.sim.addr(), to.seed()); err != nil {
		t.Fatalf("moveTo: %v", err)
	}

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the poll against the region left behind is still running")
	}
	waitFor(t, "the new region to be polled", func() bool { return to.polls.Load() > 0 })

	// One poll before and one after, whatever the run of things around
	// them: a move that started a second poll without stopping the
	// first would show up here as a goroutine that never came back.
	waitFor(t, "the goroutine count to come back to where it was", func() bool {
		return runtime.NumGoroutine() <= before
	})
}

// TestAMoveEntersTheNewRegionsObjectStore: nothing new was written for
// this.  enterRegion swaps the store on every RegionHandshake and the
// new simulator sends one, so a move should get it for free -- and this
// is the first time that path has ever run.
func TestAMoveEntersTheNewRegionsObjectStore(t *testing.T) {
	cache := NewCache()
	a, _, to := twoRegions(t, Options{SkipCaps: true, Regions: cache})

	was := a.Objects()
	if n := cache.Regions(); n != 1 {
		t.Fatalf("%d regions held before the move, want one", n)
	}

	if err := a.moveTo(context.Background(), to.sim.addr(), to.seed()); err != nil {
		t.Fatalf("moveTo: %v", err)
	}

	if a.Objects() == was {
		t.Error("the new region kept the old region's objects")
	}
	if n := cache.Regions(); n != 1 {
		t.Errorf("%d regions held after the move, want one: "+
			"the store of the region left was not given back", n)
	}
}

// waitFor polls a condition rather than sleeping for one, because
// everything here is a network round trip.
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
