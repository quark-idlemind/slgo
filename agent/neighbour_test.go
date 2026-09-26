package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

// aNeighbour is a simulator standing in for a region beside the one the
// avatar is in.
//
// It is the ordinary fakeSim, which answers UseCircuitCode with a
// RegionHandshake -- and that, plus the pings, is the whole of what
// stage 0 found a real neighbour needs from us.  What it must not be
// given is an event queue or a seed: a child has neither, and a test
// that handed it one would be exercising the region the avatar is in.
func aNeighbour(t *testing.T, name string, x, y uint32) (*fakeSim, uint64) {
	t.Helper()
	sim := newFakeSim(t)
	sim.regionNm = name
	go sim.run()
	t.Cleanup(sim.close)
	return sim, msg.RegionHandle(x, y)
}

// enableSimulator is the body a simulator offers a neighbour in, in the
// shapes stage 0 read: a SimulatorInfo block with the handle as LLSD
// binary, the address as four binary bytes in network order and the
// port as a plain integer.
//
// Built rather than captured.  Stage 0's probe read every field of the
// 57 offers it saw this way and dialled what came out of them, so the
// shapes are measured; no body was kept byte for byte, which is why
// this is not the fixture agniTeleportFinish is.
func enableSimulator(handle uint64, addr *net.UDPAddr) map[string]any {
	return map[string]any{
		"SimulatorInfo": []any{map[string]any{
			"Handle": handleBytes(handle),
			"IP":     []byte(addr.IP.To4()),
			"Port":   int64(addr.Port),
		}},
	}
}

// logLines collects what a session said, so that a test can ask whether
// anybody watching would have known this was working.
type logLines struct {
	mu    sync.Mutex
	lines []string
}

func (l *logLines) log(format string, v ...any) {
	l.mu.Lock()
	l.lines = append(l.lines, fmt.Sprintf(format, v...))
	l.mu.Unlock()
}

func (l *logLines) saying(what string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, s := range l.lines {
		if strings.Contains(s, what) {
			out = append(out, s)
		}
	}
	return out
}

// neighbourly is a session that takes offers up, in the first of two
// regions, with a neighbour beside it that has not been offered yet.
func neighbourly(t *testing.T) (*Agent, *fakeRegion, *fakeRegion, *fakeSim, uint64) {
	t.Helper()
	a, from, to := twoRegions(t, Options{
		Neighbours: true,
		OnEvent:    func(string, []byte) {},
	})
	sim, handle := aNeighbour(t, "Pelmar Mill", 43647, 43648)
	return a, from, to, sim, handle
}

// TestWithNeighboursOffAnOfferOpensNothingAndSendsNothing: off by
// default, and off has to cost the grid nothing.  So the offer is made
// both ways it can arrive and the neighbour hears nothing, no circuit is
// recorded, and the goroutine count comes back to where it started.
//
// It used to assert one thing more -- that EnableSimulator reached the
// dispatcher's unhandled pile, which said no handler had been
// registered.  That went when the flag became something a session can
// turn over: a handler cannot be withdrawn once registered, so the one
// in followNeighbours is registered either way and returns on its first
// line when the flag is down.  What the test was really protecting is
// all still here and is the half anybody cares about: with neighbours
// off, no socket is opened and nothing is said to a neighbour.
func TestWithNeighboursOffAnOfferOpensNothingAndSendsNothing(t *testing.T) {
	a, from, _ := twoRegions(t, Options{OnEvent: func(string, []byte) {}})
	sim, handle := aNeighbour(t, "Pelmar Mill", 43647, 43648)

	// After the session has settled, so that the login's own
	// goroutines are not counted as this offer's.
	waitFor(t, "the first region to be polled", func() bool { return from.polls.Load() > 0 })
	before := runtime.NumGoroutine()

	from.eq.push("EnableSimulator", enableSimulator(handle, sim.addr()))
	offer := &msg.EnableSimulator{}
	offer.SimulatorInfo.Handle = handle
	offer.SimulatorInfo.IP = msg.IPAddr{127, 0, 0, 1}
	offer.SimulatorInfo.Port = msg.IPPort(sim.addr().Port)
	from.sim.send(offer, 0)

	// Long enough for a circuit to have been opened: the dial is
	// immediate and UseCircuitCode is queued before anything is waited
	// for.
	time.Sleep(500 * time.Millisecond)

	if got := sim.got(); len(got) != 0 {
		t.Errorf("the neighbour was talked to with the option off: %v", got)
	}
	if got := a.Neighbours(); len(got) != 0 {
		t.Errorf("Neighbours = %+v with the option off", got)
	}
	if a.NeighboursOn() {
		t.Error("NeighboursOn is true for a session that did not ask for them")
	}
	waitFor(t, "the goroutine count to come back to where it was", func() bool {
		return runtime.NumGoroutine() <= before
	})
}

// TestTheOptionIsWhatTheFlagStartsAtAndNotWhatItStaysAt: Connect takes
// the option as an opening position.  Everything after that is
// SetNeighbours, so a session that was started one way can be asked for
// the other without being logged in again.
func TestTheOptionIsWhatTheFlagStartsAtAndNotWhatItStaysAt(t *testing.T) {
	a, _, _ := twoRegions(t, Options{OnEvent: func(string, []byte) {}})
	if a.NeighboursOn() {
		t.Error("a session started without the option is holding neighbours")
	}
	a.SetNeighbours(true)
	if !a.NeighboursOn() {
		t.Error("SetNeighbours(true) did not take")
	}
	a.SetNeighbours(false)
	if a.NeighboursOn() {
		t.Error("SetNeighbours(false) did not take")
	}

	on, _, _ := twoRegions(t, Options{Neighbours: true, OnEvent: func(string, []byte) {}})
	if !on.NeighboursOn() {
		t.Error("a session started with the option is not holding neighbours")
	}
}

// TestNeighboursTurnedOnMidSessionTakeUpTheNextOffer: the whole reason
// this is worth being a runtime flag.  Nothing is sent to ask for an
// offer and there is nothing to ask -- the simulator repeats one for as
// long as it goes untaken, 57 times in 200 seconds -- so a session that
// turns them on has a circuit within seconds of the next repeat.
//
// The offer here is pushed twice for that reason: the first one arrives
// while the flag is still down and is dropped, exactly as the ones a
// session slept through are, and the second is the repeat that finds it
// up.
func TestNeighboursTurnedOnMidSessionTakeUpTheNextOffer(t *testing.T) {
	a, from, _ := twoRegions(t, Options{OnEvent: func(string, []byte) {}})
	sim, handle := aNeighbour(t, "Pelmar Mill", 43647, 43648)

	from.eq.push("EnableSimulator", enableSimulator(handle, sim.addr()))
	time.Sleep(300 * time.Millisecond)
	if got := sim.got(); len(got) != 0 {
		t.Fatalf("the neighbour was talked to before it was turned on: %v", got)
	}

	a.SetNeighbours(true)
	from.eq.push("EnableSimulator", enableSimulator(handle, sim.addr()))

	sim.waitSeen(t, "UseCircuitCode", 5*time.Second)
	waitFor(t, "the neighbour to be listed", func() bool { return len(a.Neighbours()) == 1 })
	waitFor(t, "the neighbour's handshake", func() bool { return a.Neighbours()[0].Handshook })
	if name := a.Neighbours()[0].Name; name != "Pelmar Mill" {
		t.Errorf("name = %q, want the one the handshake carried", name)
	}
}

// TestNeighboursTurnedOffDropWhatIsHeldAndTakeNoMore: turning them off
// has to stop the cost, and the cost is the circuits that are open
// rather than the offers that have not arrived yet.  A session that only
// stopped listening would go on holding the sockets and the traffic it
// was told to stop paying for, and the simulator would go on believing
// the avatar could be handed over the border.
func TestNeighboursTurnedOffDropWhatIsHeldAndTakeNoMore(t *testing.T) {
	var said logLines
	a, from, _ := twoRegions(t, Options{
		Neighbours: true,
		OnEvent:    func(string, []byte) {},
		Log:        said.log,
	})
	sim, handle := aNeighbour(t, "Pelmar Mill", 43647, 43648)

	from.eq.push("EnableSimulator", enableSimulator(handle, sim.addr()))
	sim.waitSeen(t, "UseCircuitCode", 5*time.Second)
	waitFor(t, "the neighbour to be listed", func() bool { return len(a.Neighbours()) == 1 })

	a.SetNeighbours(false)

	if got := a.Neighbours(); len(got) != 0 {
		t.Errorf("Neighbours = %+v after being turned off", got)
	}
	if lines := said.saying("circuit closed"); len(lines) != 1 {
		t.Errorf("closing was reported %d times: %v", len(lines), lines)
	}

	// And the offers that follow, which go on arriving whatever this
	// session thinks: the simulator starts repeating one the moment it
	// goes untaken again.
	before := len(sim.got())
	for range 3 {
		from.eq.push("EnableSimulator", enableSimulator(handle, sim.addr()))
	}
	time.Sleep(500 * time.Millisecond)
	if got := sim.got(); len(got) != before {
		t.Errorf("the neighbour was talked to after being turned off: %v", got[before:])
	}
	if got := a.Neighbours(); len(got) != 0 {
		t.Errorf("Neighbours = %+v after an offer arrived with them off", got)
	}
}

// TestNeighboursTurnedOffAndOnAgainOpenAFreshCircuit: off is not a state
// the session cannot come back from, and coming back must not depend on
// anything that was kept from before -- the circuit was closed and the
// map emptied, so what opens here is a new one from a new offer.
func TestNeighboursTurnedOffAndOnAgainOpenAFreshCircuit(t *testing.T) {
	a, from, _, sim, handle := neighbourly(t)

	from.eq.push("EnableSimulator", enableSimulator(handle, sim.addr()))
	sim.waitSeen(t, "UseCircuitCode", 5*time.Second)
	waitFor(t, "the neighbour to be listed", func() bool { return len(a.Neighbours()) == 1 })

	a.SetNeighbours(false)
	if got := a.Neighbours(); len(got) != 0 {
		t.Fatalf("Neighbours = %+v after being turned off", got)
	}

	a.SetNeighbours(true)
	from.eq.push("EnableSimulator", enableSimulator(handle, sim.addr()))
	waitFor(t, "the circuit to open again", func() bool { return len(a.Neighbours()) == 1 })

	// And then for the dial itself, which is a separate moment.  The
	// neighbour is listed when the circuit is created and the datagram
	// that opens it goes out afterwards, so counting what the simulator
	// has seen the instant the list changes is a race -- one this test
	// won for years and then started losing when unrelated work shifted
	// the timing by a few microseconds.
	waitFor(t, "the neighbour to be dialled a second time", func() bool {
		return dialled(sim) >= 2
	})
	if n := dialled(sim); n != 2 {
		t.Errorf("the neighbour was dialled %d times, want one circuit each side of the off: %v",
			n, sim.got())
	}
}

// dialled is how many circuits have been opened to a simulator, which is
// one UseCircuitCode each.
func dialled(sim *fakeSim) int {
	n := 0
	for _, name := range sim.got() {
		if name == "UseCircuitCode" {
			n++
		}
	}
	return n
}

// TestAnOfferTakenUpOpensACircuitToTheAddressItNames: the whole of stage
// 2 in one assertion.  Nothing is called here -- the event is put on the
// queue the way a simulator puts one there, and the session takes it up
// on its own.
func TestAnOfferTakenUpOpensACircuitToTheAddressItNames(t *testing.T) {
	var said logLines
	a, from, to := twoRegions(t, Options{
		Neighbours: true,
		OnEvent:    func(string, []byte) {},
		Log:        said.log,
	})
	sim, handle := aNeighbour(t, "Pelmar Mill", 43647, 43648)

	from.eq.push("EnableSimulator", enableSimulator(handle, sim.addr()))

	sim.waitSeen(t, "UseCircuitCode", 5*time.Second)
	waitFor(t, "the neighbour to be listed", func() bool { return len(a.Neighbours()) == 1 })

	got := a.Neighbours()[0]
	if got.Handle != handle {
		t.Errorf("handle = %d, want %d", got.Handle, handle)
	}
	if got.Addr != sim.addr().String() {
		t.Errorf("address = %s, want the one the offer named, %s", got.Addr, sim.addr())
	}
	waitFor(t, "the neighbour's handshake", func() bool { return a.Neighbours()[0].Handshook })
	if name := a.Neighbours()[0].Name; name != "Pelmar Mill" {
		t.Errorf("name = %q, want the one the handshake carried", name)
	}
	waitFor(t, "the neighbour's packets to be counted", func() bool {
		return a.Neighbours()[0].Heard > 0
	})

	// Nothing that belongs to the avatar is said to a neighbour.  This
	// is the line between a child and a root, and it is one message.
	if sim.got()[0] != "UseCircuitCode" {
		t.Errorf("the circuit opened with %q: %v", sim.got()[0], sim.got())
	}
	for _, name := range sim.got() {
		if name == "CompleteAgentMovement" {
			t.Fatalf("the avatar was moved into the neighbour: %v", sim.got())
		}
	}
	// And the region the avatar is in is untouched by any of it.
	if a.RegionName() != from.sim.regionNm {
		t.Errorf("the session is in %q, want %q", a.RegionName(), from.sim.regionNm)
	}
	if to.saw("UseCircuitCode") {
		t.Error("the other region was dialled by an offer that never named it")
	}
	// Somebody watching the daemon can see it happened, which until a
	// later stage gives clients a listing is the only way they can.
	if lines := said.saying("circuit open"); len(lines) != 1 {
		t.Errorf("the daemon said %v about opening a circuit", said.saying(""))
	}
}

// TestAChildAnswersTheHandshakeAndAnswersItWhenItComesAgain: measured on
// Agni, RegionHandshake came back twice about a second apart with
// nothing else changed.  A child that treated the second as already
// done would be answering a retransmission with silence, and the
// simulator would go on asking.
func TestAChildAnswersTheHandshakeAndAnswersItWhenItComesAgain(t *testing.T) {
	a, from, _, sim, handle := neighbourly(t)

	from.eq.push("EnableSimulator", enableSimulator(handle, sim.addr()))
	sim.waitSeen(t, "RegionHandshakeReply", 5*time.Second)
	waitFor(t, "the handshake to be recorded", func() bool {
		return len(a.Neighbours()) == 1 && a.Neighbours()[0].Handshook
	})

	// The same handshake again, under a fresh sequence number, which is
	// how a retransmission that the duplicate ring has not already
	// swallowed reaches a handler.
	rh := &msg.RegionHandshake{}
	rh.RegionInfo.SimName = []byte("Pelmar Mill\x00")
	sim.send(rh, msg.FlagReliable)

	waitFor(t, "the second handshake to be answered", func() bool {
		n := 0
		for _, name := range sim.got() {
			if name == "RegionHandshakeReply" {
				n++
			}
		}
		return n >= 2
	})
	if got := a.Neighbours(); len(got) != 1 || !got[0].Handshook {
		t.Errorf("Neighbours = %+v after a second handshake", got)
	}
}

// TestAChildAnswersThePings: five StartPingCheck arrived on a child in
// thirty seconds.  A circuit that does not answer them is dropped, and a
// dropped circuit is a border that is a wall again.
func TestAChildAnswersThePings(t *testing.T) {
	_, from, _, sim, handle := neighbourly(t)

	from.eq.push("EnableSimulator", enableSimulator(handle, sim.addr()))
	sim.waitSeen(t, "UseCircuitCode", 5*time.Second)

	ping := &msg.StartPingCheck{}
	ping.PingID.PingID = 7
	sim.send(ping, 0)

	sim.waitSeen(t, "CompletePingCheck", 5*time.Second)
}

// TestASecondOfferForANeighbourAlreadyHeldOpensNothingNew: the simulator
// repeats an offer for as long as it goes untaken -- 57 times in 200
// seconds, naming four regions -- so the repeats arrive whatever we do,
// and a second circuit to a neighbour would give it two sets of sequence
// numbers under one circuit code.
func TestASecondOfferForANeighbourAlreadyHeldOpensNothingNew(t *testing.T) {
	a, from, _, sim, handle := neighbourly(t)

	from.eq.push("EnableSimulator", enableSimulator(handle, sim.addr()))
	sim.waitSeen(t, "UseCircuitCode", 5*time.Second)

	for range 3 {
		from.eq.push("EnableSimulator", enableSimulator(handle, sim.addr()))
	}
	time.Sleep(500 * time.Millisecond)

	n := 0
	for _, name := range sim.got() {
		if name == "UseCircuitCode" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the neighbour was dialled %d times, want once: %v", n, sim.got())
	}
	if got := a.Neighbours(); len(got) != 1 {
		t.Errorf("Neighbours = %+v, want the one", got)
	}
}

// TestAnOfferForTheRegionTheAvatarIsInIsNotTakenUp: a handle that agrees
// with this session's own is not a neighbour, and a circuit to it would
// be a second one to the simulator already on the other end of the
// root's.  It is also how a stale offer reads after the avatar has
// crossed into the region that made it.
func TestAnOfferForTheRegionTheAvatarIsInIsNotTakenUp(t *testing.T) {
	a, from, _, sim, _ := neighbourly(t)

	_, here := from.sim.arrival()
	from.eq.push("EnableSimulator", enableSimulator(here, sim.addr()))

	time.Sleep(500 * time.Millisecond)
	if got := sim.got(); len(got) != 0 {
		t.Errorf("a circuit was opened for the region the avatar is in: %v", got)
	}
	if got := a.Neighbours(); len(got) != 0 {
		t.Errorf("Neighbours = %+v", got)
	}
}

// TestAnOfferOnTheCircuitIsTakenUpToo: the template marks
// EnableSimulator UDPBlackListed and all 57 of stage 0's arrived on the
// event queue, but a deprecation flag is a promise a grid need not keep
// -- crossing.go reads both roads for that reason and this follows it.
func TestAnOfferOnTheCircuitIsTakenUpToo(t *testing.T) {
	a, from, _, sim, handle := neighbourly(t)

	offer := &msg.EnableSimulator{}
	offer.SimulatorInfo.Handle = handle
	offer.SimulatorInfo.IP = msg.IPAddr{127, 0, 0, 1}
	offer.SimulatorInfo.Port = msg.IPPort(sim.addr().Port)
	from.sim.send(offer, 0)

	sim.waitSeen(t, "UseCircuitCode", 5*time.Second)
	waitFor(t, "the neighbour to be listed", func() bool { return len(a.Neighbours()) == 1 })
}

// TestAnOfferWithNoUsableAddressIsNotDialled: the message is the only
// place a neighbour's address is ever named, so one this cannot read
// leaves nothing to do.  What it must not do is dial three quarters of
// an address, which would look exactly like a simulator that never
// answered.
func TestAnOfferWithNoUsableAddressIsNotDialled(t *testing.T) {
	a, from, _, sim, handle := neighbourly(t)

	short := enableSimulator(handle, sim.addr())
	short["SimulatorInfo"].([]any)[0].(map[string]any)["IP"] = []byte{127, 0, 0}

	noPort := enableSimulator(handle, sim.addr())
	noPort["SimulatorInfo"].([]any)[0].(map[string]any)["Port"] = int64(0)

	for _, body := range []map[string]any{
		{"SimulatorInfo": []any{}},
		{"SimulatorInfo": map[string]any{"nothing": "at all"}},
		{"AlertInfo": []any{}},
		short,
		noPort,
	} {
		from.eq.push("EnableSimulator", body)
	}

	// A readable one after them, which is what says the poll goroutine
	// came back from every one of the others.
	from.eq.push("EnableSimulator", enableSimulator(handle, sim.addr()))
	sim.waitSeen(t, "UseCircuitCode", 5*time.Second)
	if got := a.Neighbours(); len(got) != 1 {
		t.Errorf("Neighbours = %+v, want only the readable offer's", got)
	}
	select {
	case <-a.Done():
		t.Fatalf("an unreadable offer ended the session: %v", a.Err())
	default:
	}
}

// TestNoMoreThanTheCapAreOpened: eight regions can surround one, and the
// offers come from the far end -- nothing in a daemon should open
// sockets at a stranger's pace.  The one turned down is said out loud
// rather than dropped in silence, and said once however often it is
// offered again.
func TestNoMoreThanTheCapAreOpened(t *testing.T) {
	var said logLines
	a, from, _ := twoRegions(t, Options{
		Neighbours: true,
		OnEvent:    func(string, []byte) {},
		Log:        said.log,
	})

	// One more than fits, each a different grid square, and the last
	// one twice over so that a line per offer would show up as more
	// than one.
	var over *fakeSim
	for i := range MaxNeighbours + 1 {
		sim, handle := aNeighbour(t, fmt.Sprintf("neighbour %d", i), uint32(43634+i), 43648)
		from.eq.push("EnableSimulator", enableSimulator(handle, sim.addr()))
		if i == MaxNeighbours {
			over = sim
			from.eq.push("EnableSimulator", enableSimulator(handle, sim.addr()))
		}
	}

	waitFor(t, "the circuits to open", func() bool { return len(a.Neighbours()) == MaxNeighbours })
	time.Sleep(300 * time.Millisecond)

	if got := a.Neighbours(); len(got) != MaxNeighbours {
		t.Errorf("%d circuits open, want the cap of %d", len(got), MaxNeighbours)
	}
	if got := over.got(); len(got) != 0 {
		t.Errorf("the neighbour past the cap was talked to anyway: %v", got)
	}
	if lines := said.saying("is the limit"); len(lines) != 1 {
		t.Errorf("the cap was reported %d times, want once: %v", len(lines), lines)
	}
}

// TestAMoveDropsTheChildren: after a move the avatar is somewhere else
// and the circuits held are to the regions that surrounded the one it
// left.  On a crossing one of them is the region it has just arrived in,
// which would sit in the map as a second circuit to the simulator the
// root is now talking to.
func TestAMoveDropsTheChildren(t *testing.T) {
	var said logLines
	a, from, to := twoRegions(t, Options{
		Neighbours: true,
		OnEvent:    func(string, []byte) {},
		Log:        said.log,
	})
	sim, handle := aNeighbour(t, "Pelmar Mill", 43647, 43648)

	from.eq.push("EnableSimulator", enableSimulator(handle, sim.addr()))
	sim.waitSeen(t, "UseCircuitCode", 5*time.Second)
	waitFor(t, "the neighbour to be listed", func() bool { return len(a.Neighbours()) == 1 })

	if err := a.moveTo(context.Background(), to.sim.addr(), to.seed()); err != nil {
		t.Fatalf("moveTo: %v", err)
	}
	if got := a.Neighbours(); len(got) != 0 {
		t.Errorf("Neighbours = %+v after a move; they belong to the region left", got)
	}
	if lines := said.saying("circuit closed"); len(lines) != 1 {
		t.Errorf("closing was reported %d times: %v", len(lines), lines)
	}
}

// TestClosingTheSessionClosesTheChildrenAndLeavesNoGoroutineBehind: a
// child is three goroutines and a socket, so getting this wrong leaks
// them per neighbour rather than failing.  Close closes done, cancels
// and then waits on the group they are in -- which is also why opening
// one has to be as careful about a session that is ending as moveTo is.
func TestClosingTheSessionClosesTheChildrenAndLeavesNoGoroutineBehind(t *testing.T) {
	a, from, _ := twoRegions(t, Options{Neighbours: true, OnEvent: func(string, []byte) {}})

	waitFor(t, "the first region to be polled", func() bool { return from.polls.Load() > 0 })
	before := runtime.NumGoroutine()

	for i := range 3 {
		sim, handle := aNeighbour(t, fmt.Sprintf("neighbour %d", i), uint32(43644+i), 43648)
		from.eq.push("EnableSimulator", enableSimulator(handle, sim.addr()))
	}
	waitFor(t, "the circuits to open", func() bool { return len(a.Neighbours()) == 3 })

	a.Close()

	if got := a.Neighbours(); len(got) != 0 {
		t.Errorf("Neighbours = %+v after the session closed", got)
	}
	waitFor(t, "the goroutine count to come back to where it was", func() bool {
		return runtime.NumGoroutine() <= before
	})
}

// TestAKickGivesBackEverySocketWithoutAClose: slgod keeps a kicked
// session so that it can say why it ended, and nothing calls Close on
// it.  So the session's own end closes its sockets, the root circuit's
// and each neighbour's, and a Close made later still returns as one
// does.
func TestAKickGivesBackEverySocketWithoutAClose(t *testing.T) {
	a, from, _, sim, handle := neighbourly(t)
	from.eq.push("EnableSimulator", enableSimulator(handle, sim.addr()))
	waitFor(t, "the neighbour to be held", func() bool { return len(a.Neighbours()) == 1 })

	root := a.sock.conn.Load()
	a.neighMu.Lock()
	child := a.neighbours[handle].sock.conn.Load()
	a.neighMu.Unlock()

	kick := &msg.KickUser{}
	kick.UserInfo.Reason = []byte("ended by the test\x00")
	from.sim.send(kick, msg.FlagReliable)

	select {
	case <-a.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the kick did not end the session")
	}
	var k *Kicked
	if !errors.As(a.Err(), &k) {
		t.Fatalf("Err = %v, want the kick", a.Err())
	}

	waitFor(t, "the root circuit's socket to be closed", func() bool { return givenBack(root) })
	waitFor(t, "the neighbour's socket to be closed", func() bool { return givenBack(child) })
	if got := a.Neighbours(); len(got) != 0 {
		t.Errorf("Neighbours = %+v after the session was kicked", got)
	}

	closed := make(chan struct{})
	go func() { a.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close after a kick did not return")
	}
}

// TestAnOfferArrivingAsTheSessionEndsIsRefused: opening a child spawns
// into the group Close waits on, and an Add that lands after the wait
// has begun panics.  A session that is over opens nothing, which is what
// moveTo does about the same trap.
func TestAnOfferArrivingAsTheSessionEndsIsRefused(t *testing.T) {
	a, _, _, sim, handle := neighbourly(t)
	a.Close()

	a.openNeighbour(handle, sim.addr())

	if got := a.Neighbours(); len(got) != 0 {
		t.Errorf("Neighbours = %+v for a session that had ended", got)
	}
	if got := sim.got(); len(got) != 0 {
		t.Errorf("a neighbour was dialled on behalf of a session that had ended: %v", got)
	}
}

// TestTheOfferIsReadInTheShapesTheGridSends: three fields, three
// different shapes, and two of them would decode to something plausible
// and wrong if they were read as the message template declares them --
// the handle is binary rather than a U64, and the address is four binary
// bytes in network order rather than a number of any kind.
func TestTheOfferIsReadInTheShapesTheGridSends(t *testing.T) {
	rows := offeredSimulators(map[string]any{
		"SimulatorInfo": []any{map[string]any{
			"Handle": handleBytes(msg.RegionHandle(995, 997)),
			"IP":     []byte{203, 0, 113, 11},
			"Port":   int64(13032),
		}},
	})
	if len(rows) != 1 {
		t.Fatalf("%d blocks read, want one", len(rows))
	}
	if got := uint64(llsd.Int(rows[0], "Handle")); got != msg.RegionHandle(995, 997) {
		t.Errorf("handle = %d, want %d", got, msg.RegionHandle(995, 997))
	}
	addr := neighbourAddr(rows[0]["IP"].([]byte), 13032)
	if addr == nil || addr.String() != "203.0.113.11:13032" {
		t.Errorf("address = %v, want 203.0.113.11:13032", addr)
	}

	// A block that arrived bare rather than in an array, which
	// eventBlock takes for the same reason.
	if got := offeredSimulators(map[string]any{
		"SimulatorInfo": map[string]any{"Handle": int64(1)},
	}); len(got) != 1 {
		t.Errorf("%d blocks read from a bare map, want one", len(got))
	}

	// And the addresses that are not ones.  Three of four bytes is the
	// one worth naming: dialled, it would be somewhere, and would look
	// like a simulator that never answered.
	for _, bad := range []struct {
		ip   []byte
		port int64
	}{
		{[]byte{127, 0, 0}, 13032},
		{nil, 13032},
		{[]byte{127, 0, 0, 1}, 0},
		{[]byte{127, 0, 0, 1}, 70000},
	} {
		if got := neighbourAddr(bad.ip, bad.port); got != nil {
			t.Errorf("neighbourAddr(%v, %d) = %v, want nothing to dial", bad.ip, bad.port, got)
		}
	}
}
