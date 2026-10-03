package agent

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
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
	mux.HandleFunc("/seed", func(w http.ResponseWriter, _ *http.Request) { r.answerSeed(w) })
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

// answerSeed is this region's capabilities, as its seed gives them.
func (r *fakeRegion) answerSeed(w http.ResponseWriter) {
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
}

// heldSeed is a seed for r that answers only once released, so that a
// move can be caught between saying the avatar has arrived and having
// the new region's capabilities.
func (r *fakeRegion) heldSeed(t *testing.T) (seed string, release func()) {
	t.Helper()
	hold := make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(hold) }) }
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-hold
		r.answerSeed(w)
	}))
	// Cleanups run last first: the request held here is let go before
	// Close waits for it.
	t.Cleanup(s.Close)
	t.Cleanup(release)
	return s.URL + "/seed", release
}

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

	// Two neighbouring grid squares, and an avatar standing at a
	// different spot in each.  Every one of those is something a move
	// has to carry across, and two regions that agreed about them would
	// hide a move that carried none of them.
	from.sim.arrivalAt(msg.Vector3{X: 188.4, Y: 202.8, Z: 26.3}, msg.RegionHandle(43648, 43648))
	to.sim.arrivalAt(msg.Vector3{X: 12.5, Y: 240.25, Z: 2001}, msg.RegionHandle(43649, 43648))

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

// TestAMoveTakesTheSessionToTheOtherSimulator: the whole of stage 2 of
// doc/history/teleport.md in one assertion -- the session handshakes at
// the new address and answers with the new region afterwards.
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

// TestTheThingsHoldingTheCircuitSurviveAMove: the server, sl.Direct and
// the viewer front end hold a.Send, a.Recv or a.Disp and none of them is
// told a teleport happened, so a move must change the socket underneath
// them rather than replace them.
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
	if err := a.Handle("AttachedSound", func(*msg.Packet) { stats.Add(1) }); err != nil {
		t.Fatal(err)
	}

	// Fill the ring with the old region's numbering, well past
	// anything the new simulator will reach during its handshake.
	const filled = 30
	for i := 0; i < filled; i++ {
		from.sim.send(&msg.AttachedSound{}, 0)
	}
	waitFor(t, "the old region's packets to arrive", func() bool {
		return stats.Load() >= filled
	})

	if err := a.moveTo(context.Background(), to.sim.addr(), to.seed()); err != nil {
		t.Fatalf("moveTo: %v", err)
	}

	was := stats.Load()
	to.sim.send(&msg.AttachedSound{}, 0)
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

// TestAMoveThatFailsClosesTheSocketItDialled: a failed move ends the
// session, and slgod replaces a session that ended without closing it.
// The move closes the connection it replaced itself; the one it dialled
// is closed by the session ending.
func TestAMoveThatFailsClosesTheSocketItDialled(t *testing.T) {
	t.Parallel()

	a, _, to := twoRegions(t, Options{SkipCaps: true, Timeout: 500 * time.Millisecond})
	to.sim.mu.Lock()
	to.sim.noMovement = true
	to.sim.mu.Unlock()

	before := a.sock.conn.Load()
	if err := a.moveTo(context.Background(), to.sim.addr(), to.seed()); err == nil {
		t.Fatal("a simulator that never answered was taken for a successful move")
	}
	fresh := a.sock.conn.Load()
	if fresh == before {
		t.Fatal("the move never put its connection under the session")
	}
	waitFor(t, "the connection the move dialled to be closed", func() bool { return givenBack(fresh) })
	waitFor(t, "the connection it replaced to be closed", func() bool { return givenBack(before) })
}

// TestAClosedSocketTakesNoOtherConnection: a session can end between a
// move asking whether it has and the move's swap.  A connection put
// under a socket that was closed would be one nothing ever closes, so it
// is refused and left to the caller, and a second Close is no error.
func TestAClosedSocketTakesNoOtherConnection(t *testing.T) {
	loopback := func() *net.UDPConn {
		c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Skipf("no loopback UDP: %v", err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	first, second := loopback(), loopback()

	s := newSocket(first)
	if err := s.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if old, ok := s.swap(second); ok || old != nil {
		t.Errorf("swap on a closed socket = %v, %v; want nil, false", old, ok)
	}
	if s.conn.Load() != first {
		t.Error("a closed socket took the connection it was offered")
	}
	if givenBack(second) {
		t.Error("the refused connection was closed; that is the caller's to do")
	}
	if err := s.Close(); err != nil {
		t.Errorf("a second Close = %v", err)
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

// TestTheLookIsRefreshedAfterAMove: the camera is what the simulator
// works its interest list out from, so a session that arrived somewhere
// else with its camera left behind is described nothing that is near it
// -- including its own attachments -- while reporting that the teleport
// worked.
//
// Nothing in moveTo does this and nothing needs to.  The
// AgentMovementComplete handler calls setCenter with the position the
// new simulator gave, and setCenter keeps the draw distance the session
// is using rather than resetting it to the one it was asked for at
// login.  That is worth a test rather than a reading, because it is two
// files away from the move and would be silently lost by a change to
// either.
func TestTheLookIsRefreshedAfterAMove(t *testing.T) {
	a, _, to := twoRegions(t, Options{SkipCaps: true})

	l := a.Look()
	l.Far = 96
	a.SetLook(l)

	if err := a.moveTo(context.Background(), to.sim.addr(), to.seed()); err != nil {
		t.Fatalf("moveTo: %v", err)
	}

	got := a.Look()
	if at, _ := to.sim.arrival(); got.Center != at {
		t.Errorf("the camera is at %v, want the arrival position %v", got.Center, at)
	}
	if got.Far != 96 {
		t.Errorf("draw distance = %v, want the 96 this session was using", got.Far)
	}
	if got.At == (msg.Vector3{}) || got.Up == (msg.Vector3{}) || got.Left == (msg.Vector3{}) {
		t.Errorf("the view after the move is not a direction: %+v", got)
	}
}

// TestTheFirstArrivalOfASessionIsNotARegionChange: the notice means
// "everything you were holding is stale", and a session that has just
// connected was holding nothing.  Firing on the first arrival would have
// every client throw away a cache it had not filled yet, and would make
// a reconnect say it twice -- once for the fresh session's own arrival
// and once from whatever hosts it.
func TestTheFirstArrivalOfASessionIsNotARegionChange(t *testing.T) {
	told := make(chan string, 4)
	a, _, _ := twoRegions(t, Options{SkipCaps: true,
		OnRegionChange: func(name string, _ uint64, _ uint32) { told <- name }})

	// Connect does not return until AgentMovementComplete has been
	// handled, so anything this was going to say has been said.
	select {
	case name := <-told:
		t.Errorf("arriving in %q for the first time was reported as a region change", name)
	default:
	}
	if a.RegionName() == "" {
		t.Error("the session never got a handshake, so it proves nothing")
	}
}

// TestAMoveSaysWhichRegionTheAvatarIsInNow is the assertion the rest of
// stage 4 of doc/history/teleport.md rests on, and the one that catches
// the trap in it.  The two
// halves of the answer come from different messages -- the name from
// RegionHandshake and the handle from the AgentMovementComplete that
// follows it -- so a notice fired at the obvious moment carries the new
// region's name beside the handle of the region the avatar has left, and
// looks entirely reasonable while doing it.
func TestAMoveSaysWhichRegionTheAvatarIsInNow(t *testing.T) {
	type where struct {
		name   string
		handle uint64
	}
	told := make(chan where, 4)
	a, from, to := twoRegions(t, Options{SkipCaps: true,
		OnRegionChange: func(name string, handle uint64, _ uint32) { told <- where{name, handle} }})

	if err := a.moveTo(context.Background(), to.sim.addr(), to.seed()); err != nil {
		t.Fatalf("moveTo: %v", err)
	}

	var got where
	select {
	case got = <-told:
	case <-time.After(5 * time.Second):
		t.Fatal("a move said nothing about the region changing")
	}

	_, arrived := to.sim.arrival()
	if got.name != to.sim.regionNm || got.handle != arrived {
		_, left := from.sim.arrival()
		t.Errorf("told %q handle %d, want %q handle %d; the region left is %q handle %d",
			got.name, got.handle, to.sim.regionNm, arrived, from.sim.regionNm, left)
	}

	// Exactly one.  A second would have a client throw away the object
	// cache it had just been handed by the region it arrived in.
	select {
	case again := <-told:
		t.Errorf("one move was reported twice; the second was %+v", again)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestAHandshakeArrivingBehindTheMovementStillNamesTheNewRegion: the
// two messages that say the avatar has arrived are sent in order and
// need not be delivered in it.  A trace on Agni caught the handshake a
// second behind the movement, and a session that took the movement as
// the arrival spent that second in the new region, at the new position,
// calling itself by the name of the region it had left -- which is what
// a teleport's read-back printed, and what the notice of the change
// said.  The name and the place have to change together.
func TestAHandshakeArrivingBehindTheMovementStillNamesTheNewRegion(t *testing.T) {
	type where struct {
		name   string
		handle uint64
	}
	told := make(chan where, 4)
	a, from, to := twoRegions(t, Options{SkipCaps: true,
		OnRegionChange: func(name string, handle uint64, _ uint32) { told <- where{name, handle} }})
	to.sim.mu.Lock()
	to.sim.lateHandshake = true
	to.sim.mu.Unlock()

	if err := a.moveTo(context.Background(), to.sim.addr(), to.seed()); err != nil {
		t.Fatalf("moveTo: %v", err)
	}

	// Read at once: the move has returned, which is the moment a
	// teleport's caller reads where it has got to.
	at, handle, name := a.Here()
	wantAt, wantHandle := to.sim.arrival()
	if name != to.sim.regionNm || handle != wantHandle || at != wantAt {
		t.Errorf("after the move the session says %q handle %d at %v, want %q handle %d at %v; "+
			"the region left is %q", name, handle, at, to.sim.regionNm, wantHandle, wantAt,
			from.sim.regionNm)
	}

	select {
	case got := <-told:
		if got.name != to.sim.regionNm || got.handle != wantHandle {
			t.Errorf("the change was told as %q handle %d, want %q handle %d",
				got.name, got.handle, to.sim.regionNm, wantHandle)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a move said nothing about the region changing")
	}
}

// TestTheNewRegionsNameIsNotTakenBeforeTheAvatarIsThere: the other half
// of keeping the name and the place together.  In the ordinary order the
// handshake comes first, and a session that took the new name from it
// at once would answer, until the movement arrived, with the name of a
// region it had not reached around a position in the one it had not yet
// left.
func TestTheNewRegionsNameIsNotTakenBeforeTheAvatarIsThere(t *testing.T) {
	a, from, to := twoRegions(t, Options{SkipCaps: true})
	to.sim.mu.Lock()
	to.sim.lateMovement = true
	to.sim.mu.Unlock()

	moved := make(chan error, 1)
	go func() { moved <- a.moveTo(context.Background(), to.sim.addr(), to.seed()) }()

	// The reply is sent once the handshake has been handled, and the
	// movement is a timer's length behind it.
	to.sim.waitSeen(t, "RegionHandshakeReply", 5*time.Second)
	at, handle, name := a.Here()
	wasAt, wasHandle := from.sim.arrival()
	if name != from.sim.regionNm || handle != wasHandle || at != wasAt {
		t.Errorf("between the handshake and the movement the session says %q handle %d at %v, "+
			"want the region it has not yet left: %q handle %d at %v",
			name, handle, at, from.sim.regionNm, wasHandle, wasAt)
	}

	if err := <-moved; err != nil {
		t.Fatalf("moveTo: %v", err)
	}
	if got := a.RegionName(); got != to.sim.regionNm {
		t.Errorf("region = %q after the move, want %q", got, to.sim.regionNm)
	}
}

// TestWhatTheRegionLeftHadQueuedHidesNothingOfTheNewOnes: packets from
// the region left can still be waiting for the dispatcher when the
// socket moves.  A move that forgot the sequence numbers on the first of
// those, rather than on the new simulator's first packet, put their
// numbers back, and the new simulator's packets under the same numbers
// were dropped as duplicates.  It was seen as
// TestTheNewRegionsNameIsNotTakenBeforeTheAvatarIsThere failing
// twice in 1000 runs, both times with the new region's
// AgentMovementComplete dropped as a duplicate under number 5.  Here the
// dispatcher is held so that eight of them are queued, numbered from one
// as the new simulator numbers its own.
func TestWhatTheRegionLeftHadQueuedHidesNothingOfTheNewOnes(t *testing.T) {
	var holding atomic.Bool
	held := make(chan struct{})
	release := make(chan struct{})
	a, from, to := twoRegions(t, Options{SkipCaps: true, Tap: func(*msg.Packet) {
		if holding.CompareAndSwap(true, false) {
			close(held)
			<-release
		}
	}})
	var once sync.Once
	letGo := func() { once.Do(func() { close(release) }) }
	t.Cleanup(letGo)

	const queued = 8
	read := a.Recv.Stats().Packets
	holding.Store(true)
	from.sim.mu.Lock()
	from.sim.seq = 0
	from.sim.mu.Unlock()
	for range queued {
		from.sim.send(&msg.AgentPause{}, 0)
	}
	select {
	case <-held:
	case <-time.After(5 * time.Second):
		t.Fatal("the dispatcher was never held")
	}
	waitFor(t, "the region left's packets to be read", func() bool {
		return a.Recv.Stats().Packets >= read+queued
	})

	moved := make(chan error, 1)
	go func() { moved <- a.moveTo(context.Background(), to.sim.addr(), to.seed()) }()
	// The socket has moved before anything goes out on it.
	to.sim.waitSeen(t, "UseCircuitCode", 5*time.Second)
	letGo()

	if err := <-moved; err != nil {
		t.Fatalf("moveTo: %v", err)
	}
	if got := a.RegionName(); got != to.sim.regionNm {
		t.Errorf("region = %q after the move, want %q", got, to.sim.regionNm)
	}
}

// TestTheNewRegionsCoarseLocationWaitsForTheArrival: the new simulator
// starts saying where the avatar is as soon as the circuit is open, and
// it may say so before the arrival has been put together.  Taken then,
// the new region's coarse position would sit under the old region's
// handle and name, which is the fault the arrival exists to prevent
// arriving by another door.
func TestTheNewRegionsCoarseLocationWaitsForTheArrival(t *testing.T) {
	t.Parallel()

	a, _ := offlineSession(t)
	handshake := func(name string) *msg.RegionHandshake {
		m := &msg.RegionHandshake{}
		m.RegionInfo.SimName = []byte(name + "\x00")
		return m
	}
	movement := func(at msg.Vector3, handle uint64) *msg.AgentMovementComplete {
		m := &msg.AgentMovementComplete{}
		m.Data.Position = at
		m.Data.LookAt = msg.Vector3{X: 1}
		m.Data.RegionHandle = handle
		return m
	}
	leftAt, left := msg.Vector3{X: 188, Y: 202, Z: 26}, msg.RegionHandle(43648, 43648)
	feed(t, a, handshake("the region left"), movement(leftAt, left))

	// What moveTo marks before the socket changes.
	a.mu.Lock()
	a.entering = &arrival{}
	a.mu.Unlock()

	coarse := &msg.CoarseLocationUpdate{}
	coarse.Location = []msg.CoarseLocationUpdate_Location{{X: 12, Y: 240, Z: 7}}
	feed(t, a, coarse)
	if at, handle, name := a.Here(); at != leftAt || handle != left || name != "the region left" {
		t.Errorf("before the arrival the session says %q handle %d at %v, want %q handle %d at %v",
			name, handle, at, "the region left", left, leftAt)
	}

	arrivedAt, arrived := msg.Vector3{X: 12.5, Y: 240.25, Z: 30.5}, msg.RegionHandle(43649, 43648)
	feed(t, a, movement(arrivedAt, arrived), handshake("the region arrived at"))
	if at, handle, name := a.Here(); at != arrivedAt || handle != arrived || name != "the region arrived at" {
		t.Errorf("after the arrival the session says %q handle %d at %v, want %q handle %d at %v",
			name, handle, at, "the region arrived at", arrived, arrivedAt)
	}

	// And once it has arrived, the coarse updates are its own again.
	feed(t, a, coarse)
	if want := (msg.Vector3{X: 12, Y: 240, Z: 28}); a.Position() != want {
		t.Errorf("after the arrival a coarse update left the avatar at %v, want %v",
			a.Position(), want)
	}
}

// TestTheRegionRecordIsNotTakenBeforeTheAvatarIsThere: Region is the
// whole of what the handshake said, with the handle the movement gave,
// and it changes when the name and the place do.  Taken from the
// handshake at once, it answered until the movement came with the new
// region's id and name around the old region's handle.
func TestTheRegionRecordIsNotTakenBeforeTheAvatarIsThere(t *testing.T) {
	a, from, to := twoRegions(t, Options{SkipCaps: true})
	to.sim.mu.Lock()
	to.sim.lateMovement = true
	to.sim.mu.Unlock()

	moved := make(chan error, 1)
	go func() { moved <- a.moveTo(context.Background(), to.sim.addr(), to.seed()) }()

	to.sim.waitSeen(t, "RegionHandshakeReply", 5*time.Second)
	_, left := from.sim.arrival()
	wantRegion(t, a, "between the handshake and the movement", from.sim.regionID, from.sim.regionNm, left)

	if err := <-moved; err != nil {
		t.Fatalf("moveTo: %v", err)
	}
	_, arrived := to.sim.arrival()
	wantRegion(t, a, "after the move", to.sim.regionID, to.sim.regionNm, arrived)
}

// TestTheRegionRecordWaitsForAHandshakeBehindTheMovement: the other
// order, which a trace on Agni caught.  The held movement's handle is not
// the region's until the handshake it waits for has come.
func TestTheRegionRecordWaitsForAHandshakeBehindTheMovement(t *testing.T) {
	a, from, to := twoRegions(t, Options{SkipCaps: true})
	to.sim.mu.Lock()
	to.sim.lateHandshake = true
	to.sim.mu.Unlock()

	moved := make(chan error, 1)
	go func() { moved <- a.moveTo(context.Background(), to.sim.addr(), to.seed()) }()

	// The handshake is a timer's length behind the movement.
	waitFor(t, "the movement to be held for the handshake", func() bool {
		a.mu.RLock()
		defer a.mu.RUnlock()
		return a.entering != nil && a.entering.moved != nil
	})
	_, left := from.sim.arrival()
	wantRegion(t, a, "between the movement and the handshake", from.sim.regionID, from.sim.regionNm, left)

	if err := <-moved; err != nil {
		t.Fatalf("moveTo: %v", err)
	}
	_, arrived := to.sim.arrival()
	wantRegion(t, a, "after the move", to.sim.regionID, to.sim.regionNm, arrived)
}

// TestALoginsHandshakeKeepsTheHandleItFinds: outside a move the
// handshake is taken as it comes, with whatever handle is held, which on
// the circuit's own region can only be that region's or none.  None is
// what the ordinary order gives, and the movement fills it in; in the
// other order the movement's handle has to survive the handshake.
func TestALoginsHandshakeKeepsTheHandleItFinds(t *testing.T) {
	t.Parallel()

	const name = "the test region"
	handle := msg.RegionHandle(43648, 43648)
	movement := &msg.AgentMovementComplete{}
	movement.Data.RegionHandle = handle

	t.Run("handshake first", func(t *testing.T) {
		a, _ := offlineSession(t)
		feed(t, a, handshakeFor(aRegion, name))
		wantRegion(t, a, "before the movement", aRegion, name, 0)
		feed(t, a, movement)
		wantRegion(t, a, "after the movement", aRegion, name, handle)
	})
	t.Run("movement first", func(t *testing.T) {
		a, _ := offlineSession(t)
		feed(t, a, movement, handshakeFor(aRegion, name))
		wantRegion(t, a, "after the handshake", aRegion, name, handle)
		if got := a.RegionHandle(); got != handle {
			t.Errorf("RegionHandle = %d after the handshake, want %d", got, handle)
		}
	})
}

// wantRegion checks that Region answers with one region throughout: its
// id and name, and the handle it is expected to carry.
func wantRegion(t *testing.T, a *Agent, when string, id msg.UUID, name string, handle uint64) {
	t.Helper()
	r, known := a.Region()
	if !known || r.ID != id || r.Name != name || r.Handle != handle {
		t.Errorf("%s Region is %q %s handle %d (known %v), want %q %s handle %d",
			when, r.Name, r.ID, r.Handle, known, name, id, handle)
	}
}

// TestAMoveWhoseRegionNeverIntroducesItselfEndsTheSession: an arrival
// with no handshake is a session in a region it has no name for, holding
// the objects of the one it left, and never having answered the
// handshake that would have the new one describe anything.  That is not
// a session to keep, and it is ended as one whose movement never came is
// ended.
func TestAMoveWhoseRegionNeverIntroducesItselfEndsTheSession(t *testing.T) {
	t.Parallel()

	a, _, to := twoRegions(t, Options{SkipCaps: true, Timeout: 500 * time.Millisecond})
	to.sim.mu.Lock()
	to.sim.noHandshake = true
	to.sim.mu.Unlock()

	err := a.moveTo(context.Background(), to.sim.addr(), to.seed())
	if err == nil {
		t.Fatal("a region that never sent its handshake was taken for a successful move")
	}
	if !strings.Contains(err.Error(), "RegionHandshake") {
		t.Errorf("error %q does not say what never came", err)
	}
	select {
	case <-a.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the session outlived the move that failed")
	}
}

// TestArrivingAgainInTheRegionWeAreInIsNotAChange: an
// AgentMovementComplete naming the handle this session already has says
// the avatar is where it was.  There is nothing stale to drop, and the
// notice costs a client everything it holds.
func TestArrivingAgainInTheRegionWeAreInIsNotAChange(t *testing.T) {
	told := make(chan string, 4)
	a, from, _ := twoRegions(t, Options{SkipCaps: true,
		OnRegionChange: func(name string, _ uint64, _ uint32) { told <- name }})

	// The same region and the same handle, at a different spot, which is
	// what makes the arrival visible from here without asking the
	// dispatcher anything.
	_, handle := from.sim.arrival()
	at := msg.Vector3{X: 33, Y: 44, Z: 55}
	amc := &msg.AgentMovementComplete{}
	amc.Data.Position = at
	amc.Data.LookAt = msg.Vector3{X: 1}
	amc.Data.RegionHandle = handle
	from.sim.send(amc, msg.FlagReliable)

	waitFor(t, "the second arrival to be handled", func() bool { return a.Position() == at })
	select {
	case name := <-told:
		t.Errorf("arriving again in %q was reported as a change of region", name)
	default:
	}
}

// TestTheSeedFollowsTheAvatar: Account.SeedCapability names the region
// this session LOGGED IN to and goes on naming it for the rest of the
// session, so anything above this package that wants the current
// region's capabilities -- slgod handing a viewer its seed -- has to be
// able to ask for them.  moveTo is given the new seed, uses it and used
// to drop it, which left the answer nowhere.
func TestTheSeedFollowsTheAvatar(t *testing.T) {
	a, from, to := twoRegions(t, Options{})

	if got := a.Seed(); got != from.seed() {
		t.Fatalf("seed = %q before the move, want the login region's %q", got, from.seed())
	}
	if err := a.moveTo(context.Background(), to.sim.addr(), to.seed()); err != nil {
		t.Fatalf("moveTo: %v", err)
	}
	if got := a.Seed(); got != to.seed() {
		t.Errorf("seed = %q after the move, want the region arrived in, %q", got, to.seed())
	}
	// And the account is untouched, which is the whole reason this had
	// to be asked for somewhere else: it is the login region's answer
	// and stays right about the question it was asked.
	if a.Account.SeedCapability != from.seed() {
		t.Errorf("the account's seed = %q; it names the region logged in to",
			a.Account.SeedCapability)
	}
}

// TestAMoveWithNoSeedLeavesNoneBehind: an unusable seed drops the
// capability set rather than keeping URLs into the region left behind,
// and the seed itself has to go the same way.  Kept, it would hand a
// viewer the login region's capabilities under the name of the one the
// avatar is in.
func TestAMoveWithNoSeedLeavesNoneBehind(t *testing.T) {
	a, _, to := twoRegions(t, Options{})

	if err := a.moveTo(context.Background(), to.sim.addr(), ""); err != nil {
		t.Fatalf("moveTo: %v", err)
	}
	if got := a.Seed(); got != "" {
		t.Errorf("seed = %q after a move that carried none", got)
	}
}

// TestWaitCapsIsTheNewRegionsSetAndNotTheOldOne: a move says the avatar
// has arrived before it has asked the new region for its capabilities,
// so anything reacting to that by reading Caps gets the region left's
// set.  WaitCaps is what it reads instead: it waits for as long as its
// context lets it, and then answers with the region arrived in.
func TestWaitCapsIsTheNewRegionsSetAndNotTheOldOne(t *testing.T) {
	type heard struct {
		name string
		caps Caps
	}
	var self atomic.Pointer[Agent]
	told := make(chan heard, 4)
	a, from, to := twoRegions(t, Options{
		OnRegionChange: func(name string, _ uint64, _ uint32) { told <- heard{name, self.Load().Caps()} }})
	self.Store(a)

	if caps, err := a.WaitCaps(context.Background()); err != nil {
		t.Fatalf("WaitCaps with no move under way: %v", err)
	} else if u, _ := caps.Get(EventQueueCap); !strings.HasPrefix(u, from.http.URL) {
		t.Errorf("with no move under way WaitCaps = %s %q, want the login region's", EventQueueCap, u)
	}

	seed, release := to.heldSeed(t)
	moved := make(chan error, 1)
	go func() { moved <- a.moveTo(context.Background(), to.sim.addr(), seed) }()

	var got heard
	select {
	case got = <-told:
	case err := <-moved:
		t.Fatalf("the move ended without saying the region changed: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("a move said nothing about the region changing")
	}
	// Why this exists: at the notice the set is still the old one.
	if u, _ := got.caps.Get(EventQueueCap); !strings.HasPrefix(u, from.http.URL) {
		t.Errorf("at the notice %s = %q; the new region had not been asked yet, "+
			"so it should still be the region left's", EventQueueCap, u)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if caps, err := a.WaitCaps(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("WaitCaps while the new region was being asked = %v, %v; "+
			"want it to wait until its context ended", caps.Names(), err)
	}

	time.AfterFunc(100*time.Millisecond, release)
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	caps, err := a.WaitCaps(ctx)
	if err != nil {
		t.Fatalf("WaitCaps: %v", err)
	}
	if u, _ := caps.Get(EventQueueCap); !strings.HasPrefix(u, to.http.URL) {
		t.Errorf("WaitCaps = %s %q, want one of %s, the region arrived in", EventQueueCap, u, to.http.URL)
	}
	if got.name != to.sim.regionNm || a.RegionName() != to.sim.regionNm {
		t.Errorf("told %q and the session says %q, want %q", got.name, a.RegionName(), to.sim.regionNm)
	}
	if err := <-moved; err != nil {
		t.Fatalf("moveTo: %v", err)
	}
}

// TestWaitCapsFailsWhenTheMoveEndedTheSession: a move whose new region
// will not give its capabilities ends the session, and the set left
// behind is the region left's.  Handing that back would be the stale
// answer WaitCaps is there to prevent.
func TestWaitCapsFailsWhenTheMoveEndedTheSession(t *testing.T) {
	a, _, to := twoRegions(t, Options{})
	refused := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	t.Cleanup(refused.Close)

	if err := a.moveTo(context.Background(), to.sim.addr(), refused.URL+"/seed"); err == nil {
		t.Fatal("a move whose seed was refused succeeded")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if caps, err := a.WaitCaps(ctx); err == nil {
		t.Errorf("WaitCaps after the move ended the session = %v", caps.Names())
	}
}
