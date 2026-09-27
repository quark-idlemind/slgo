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
// RegionHandshake -- and that, plus the pings, is the whole of what stage
// 0 of doc/history/neighbours.md found a real neighbour needs from us.
// What it must not be given is an event queue or a seed: a child has
// neither, and a test that handed it one would be exercising the region
// the avatar is in.
func aNeighbour(t *testing.T, name string, x, y uint32) (*fakeSim, uint64) {
	t.Helper()
	sim := newFakeSim(t)
	sim.regionNm = name
	go sim.run()
	t.Cleanup(sim.close)
	return sim, msg.RegionHandle(x, y)
}

// enableSimulator is the body a simulator offers a neighbour in, in the
// shapes stage 0 of doc/history/neighbours.md read: a SimulatorInfo block
// with the handle as LLSD binary, the address as four binary bytes in
// network order and the port as a plain integer.
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
// 2 of doc/history/neighbours.md in one assertion.  Nothing is called
// here -- the event is put on the queue the way a simulator puts one
// there, and the session takes it up on its own.
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
	// Somebody watching the daemon can see it happened, and so can a
	// client asking for the listing.
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
// numbers under one circuit code.  A repeat is an offer at the address
// already held, to a child still heard from; see
// TestAReOfferAtAnotherAddressReplacesTheChild for the other kind.
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

// TestAnOfferOvertakenByAMoveIntoItsRegionIsNotKept: the check on the
// handle is made before the lock, and the avatar can move into the
// offered region, and the move close the child to it, while the offer
// waits for the lock.  What was dialled is closed rather than listed.
func TestAnOfferOvertakenByAMoveIntoItsRegionIsNotKept(t *testing.T) {
	a, _, _, sim, handle := neighbourly(t)

	a.neighMu.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.openNeighbour(handle, sim.addr())
	}()
	// Long enough for the offer to be past the first check and waiting
	// on the lock; if it is not, the first check refuses it and the
	// test passes for the wrong reason rather than failing.
	time.Sleep(100 * time.Millisecond)
	a.mu.Lock()
	a.here.Handle = handle
	a.mu.Unlock()
	a.neighMu.Unlock()
	<-done

	if got := a.Neighbours(); len(got) != 0 {
		t.Errorf("Neighbours = %+v, want none: the offer names the region the avatar is in", got)
	}
}

// TestAnOfferOnTheCircuitIsTakenUpToo: the template marks EnableSimulator
// UDPBlackListed and all 57 of stage 0's in doc/history/neighbours.md
// arrived on the event queue, but a deprecation flag is a promise a grid
// need not keep -- crossing.go reads both roads for that reason and this
// follows it.
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

// ring is the eight regions around (x, y), each a simulator of its own.
func ring(t *testing.T, x, y uint32) map[uint64]*fakeSim {
	t.Helper()
	sims := map[uint64]*fakeSim{}
	for rx := x - 1; rx <= x+1; rx++ {
		for ry := y - 1; ry <= y+1; ry++ {
			if rx == x && ry == y {
				continue
			}
			sim, handle := aNeighbour(t, fmt.Sprintf("the region at %d, %d", rx, ry), rx, ry)
			sims[handle] = sim
		}
	}
	return sims
}

// TestNoMoreThanTheCapAreOpened: eight regions can surround one, and the
// offers come from the far end -- nothing in a daemon should open
// sockets at a stranger's pace.  Every child held here is beside the
// avatar's region, so there is none to make room with, and the offer
// past the cap is turned down: said out loud rather than dropped in
// silence, and said once however often it is offered again.
func TestNoMoreThanTheCapAreOpened(t *testing.T) {
	var said logLines
	a, from, _ := twoRegions(t, Options{
		Neighbours: true,
		OnEvent:    func(string, []byte) {},
		Log:        said.log,
	})

	_, here := from.sim.arrival()
	x, y := msg.GridCoords(here)
	for handle, sim := range ring(t, x, y) {
		from.eq.push("EnableSimulator", enableSimulator(handle, sim.addr()))
	}
	waitFor(t, "the circuits to open", func() bool { return len(a.Neighbours()) == MaxNeighbours })

	// One more than fits, twice over so that a line per offer would
	// show up as more than one.
	over, handle := aNeighbour(t, "one too many", x-8, y)
	from.eq.push("EnableSimulator", enableSimulator(handle, over.addr()))
	from.eq.push("EnableSimulator", enableSimulator(handle, over.addr()))
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
	if lines := said.saying("circuit closed"); len(lines) != 0 {
		t.Errorf("a neighbour was closed to make room: %v", lines)
	}
}

// TestTheCapClosesWhatIsNoLongerANeighbourFirst: children outlive a
// move, so after a teleport the ring around the region left can fill the
// cap while the new region's offers are still arriving.  An offer at the
// cap closes a child whose region does not touch the avatar's -- never
// one that does -- rather than being turned down.
//
// The avatar lands two squares west of the region left, so that the
// ring's west column is beside both regions and holds its lowest
// handles: a cap that closed in handle order alone would close one of
// those.
func TestTheCapClosesWhatIsNoLongerANeighbourFirst(t *testing.T) {
	var said logLines
	a, from, to := twoRegions(t, Options{
		Neighbours: true,
		OnEvent:    func(string, []byte) {},
		Log:        said.log,
	})
	_, left := from.sim.arrival()
	x, y := msg.GridCoords(left)
	at, _ := to.sim.arrival()
	to.sim.arrivalAt(at, msg.RegionHandle(x-2, y))

	held := ring(t, x, y)
	for handle, sim := range held {
		from.eq.push("EnableSimulator", enableSimulator(handle, sim.addr()))
	}
	waitFor(t, "the ring to be held", func() bool { return len(a.Neighbours()) == MaxNeighbours })

	if err := a.moveTo(context.Background(), to.sim.addr(), to.seed()); err != nil {
		t.Fatalf("moveTo: %v", err)
	}
	if got := a.Neighbours(); len(got) != MaxNeighbours {
		t.Fatalf("%d circuits held after the teleport, want the ring kept", len(got))
	}

	// The new region offers its own west neighbour.
	west, westAt := aNeighbour(t, "the new region's west", x-3, y)
	to.eq.push("EnableSimulator", enableSimulator(westAt, west.addr()))
	west.waitSeen(t, "UseCircuitCode", 5*time.Second)
	waitFor(t, "the offer to be held", func() bool {
		for _, n := range a.Neighbours() {
			if n.Handle == westAt {
				return true
			}
		}
		return false
	})

	got := a.Neighbours()
	if len(got) != MaxNeighbours {
		t.Errorf("%d circuits held, want the cap of %d", len(got), MaxNeighbours)
	}
	holding := map[uint64]bool{}
	for _, n := range got {
		holding[n.Handle] = true
	}
	closed := 0
	for handle := range held {
		// The ring's west column is the one beside the region
		// arrived in.
		rx, _ := msg.GridCoords(handle)
		switch {
		case holding[handle]:
		case rx == x-1:
			t.Errorf("%s, beside the region arrived in, was closed", gridSquare(handle))
		default:
			closed++
		}
	}
	if closed != 1 {
		t.Errorf("%d of the ring closed, want the one that made room", closed)
	}
	if lines := said.saying("to make room"); len(lines) != 1 {
		t.Errorf("making room was reported as %v", lines)
	}
	if lines := said.saying("is the limit"); len(lines) != 0 {
		t.Errorf("the offer was turned down: %v", lines)
	}
}

// childConn is the connection under the child held for a region.
func childConn(t *testing.T, a *Agent, handle uint64) *net.UDPConn {
	t.Helper()
	a.neighMu.Lock()
	defer a.neighMu.Unlock()
	c := a.neighbours[handle]
	if c == nil {
		t.Fatalf("no child held for %s", gridSquare(handle))
	}
	return c.sock.conn.Load()
}

// TestATeleportKeepsTheChildren: a child is kept until its simulator
// lets it go, as a viewer keeps one.  Measured on Agni, a region the
// avatar came back to within seconds of leaving offered none of its
// neighbours again, so a session that let them go on every move came
// back holding none.  The teleport here lands far from the region left,
// so the child is no longer beside the avatar, and is kept anyway.
func TestATeleportKeepsTheChildren(t *testing.T) {
	var said logLines
	a, from, to := twoRegions(t, Options{
		Neighbours: true,
		OnEvent:    func(string, []byte) {},
		Log:        said.log,
	})
	at, _ := to.sim.arrival()
	to.sim.arrivalAt(at, msg.RegionHandle(43552, 43728))
	sim, handle := aNeighbour(t, "Pelmar Mill", 43647, 43648)

	from.eq.push("EnableSimulator", enableSimulator(handle, sim.addr()))
	sim.waitSeen(t, "UseCircuitCode", 5*time.Second)
	waitFor(t, "the neighbour to be listed", func() bool { return len(a.Neighbours()) == 1 })
	conn := childConn(t, a, handle)

	if err := a.moveTo(context.Background(), to.sim.addr(), to.seed()); err != nil {
		t.Fatalf("moveTo: %v", err)
	}
	if got := a.Neighbours(); len(got) != 1 || got[0].Handle != handle {
		t.Fatalf("Neighbours = %+v after a teleport, want the child kept", got)
	}
	if lines := said.saying("circuit closed"); len(lines) != 0 {
		t.Errorf("the teleport closed a circuit: %v", lines)
	}
	if givenBack(conn) {
		t.Error("the kept child's socket was closed")
	}

	// And it is still a circuit: a ping after the move is answered.
	ping := &msg.StartPingCheck{}
	ping.PingID.PingID = 9
	sim.send(ping, 0)
	sim.waitSeen(t, "CompletePingCheck", 5*time.Second)
}

// TestACrossingClosesTheChildForTheRegionArrivedIn: the child to the
// region the avatar walks into would be a second circuit to the
// simulator the root now talks to, so the move closes that one, and
// only that one.  The child for it here is a simulator of its own, which
// is enough: the move goes by the handle.
func TestACrossingClosesTheChildForTheRegionArrivedIn(t *testing.T) {
	var said logLines
	a, from, to := twoRegions(t, Options{
		Neighbours: true,
		OnEvent:    func(string, []byte) {},
		Log:        said.log,
	})
	mill, millAt := aNeighbour(t, "Pelmar Mill", 43647, 43648)
	_, over := to.sim.arrival()
	x, y := msg.GridCoords(over)
	ahead, aheadAt := aNeighbour(t, to.sim.regionNm, x, y)

	from.eq.push("EnableSimulator", enableSimulator(millAt, mill.addr()))
	from.eq.push("EnableSimulator", enableSimulator(aheadAt, ahead.addr()))
	waitFor(t, "both neighbours to be held", func() bool { return len(a.Neighbours()) == 2 })
	conn := childConn(t, a, aheadAt)

	if err := a.moveTo(context.Background(), to.sim.addr(), to.seed()); err != nil {
		t.Fatalf("moveTo: %v", err)
	}
	if got := a.Neighbours(); len(got) != 1 || got[0].Handle != millAt {
		t.Errorf("Neighbours = %+v after the crossing, want only %s", got, gridSquare(millAt))
	}
	waitFor(t, "the closed child's socket to be given back", func() bool { return givenBack(conn) })
	lines := said.saying("circuit closed")
	if len(lines) != 1 || !strings.Contains(lines[0], gridSquare(aheadAt)) {
		t.Errorf("closing was reported as %v", lines)
	}
}

// TestADisableSimulatorClosesThatChild: DisableSimulator on a child's own
// circuit is its simulator letting it go.  The message has no body, so
// the circuit it came on is what says which child: the other is left
// alone.  And the region can be offered and held again afterwards.
func TestADisableSimulatorClosesThatChild(t *testing.T) {
	var said logLines
	a, from, _ := twoRegions(t, Options{
		Neighbours: true,
		OnEvent:    func(string, []byte) {},
		Log:        said.log,
	})
	mill, millAt := aNeighbour(t, "Pelmar Mill", 43647, 43648)
	gate, gateAt := aNeighbour(t, "Orrick Gate", 43648, 43647)

	from.eq.push("EnableSimulator", enableSimulator(millAt, mill.addr()))
	from.eq.push("EnableSimulator", enableSimulator(gateAt, gate.addr()))
	mill.waitSeen(t, "UseCircuitCode", 5*time.Second)
	waitFor(t, "both neighbours to be held", func() bool { return len(a.Neighbours()) == 2 })
	conn := childConn(t, a, millAt)

	mill.send(&msg.DisableSimulator{}, msg.FlagReliable)

	waitFor(t, "the disabled child to go", func() bool { return len(a.Neighbours()) == 1 })
	if got := a.Neighbours(); got[0].Handle != gateAt {
		t.Errorf("Neighbours = %+v, want only the one not disabled", got)
	}
	waitFor(t, "its socket to be given back", func() bool { return givenBack(conn) })
	lines := said.saying("circuit closed")
	if len(lines) != 1 || !strings.Contains(lines[0], gridSquare(millAt)) ||
		!strings.Contains(lines[0], "disabled") {
		t.Errorf("closing was reported as %v", lines)
	}
	select {
	case <-a.Done():
		t.Fatalf("a neighbour's DisableSimulator ended the session: %v", a.Err())
	default:
	}
	if lines := said.saying("sent DisableSimulator"); len(lines) != 0 {
		t.Errorf("a neighbour's DisableSimulator was taken for the root's: %v", lines)
	}

	from.eq.push("EnableSimulator", enableSimulator(millAt, mill.addr()))
	waitFor(t, "the region to be dialled again", func() bool { return dialled(mill) == 2 })
	waitFor(t, "the region to be held again", func() bool { return len(a.Neighbours()) == 2 })
}

// TestADisableSimulatorOnTheRootIsSaidAndEndsNothing: a viewer ends the
// session when the region the avatar is in disables its circuit, and
// none has been seen, so this session says so in one line and carries
// on.  The root circuit still carries messages, the session has not
// ended, and the child held is left alone.
func TestADisableSimulatorOnTheRootIsSaidAndEndsNothing(t *testing.T) {
	var said logLines
	a, from, _ := twoRegions(t, Options{
		Neighbours: true,
		OnEvent:    func(string, []byte) {},
		Log:        said.log,
	})
	mill, millAt := aNeighbour(t, "Pelmar Mill", 43647, 43648)
	from.eq.push("EnableSimulator", enableSimulator(millAt, mill.addr()))
	waitFor(t, "the neighbour to be held", func() bool { return len(a.Neighbours()) == 1 })
	root := a.sock.conn.Load()

	from.sim.send(&msg.DisableSimulator{}, msg.FlagReliable)

	waitFor(t, "the DisableSimulator to be said", func() bool {
		return len(said.saying("sent DisableSimulator")) > 0
	})
	lines := said.saying("sent DisableSimulator")
	if len(lines) != 1 {
		t.Fatalf("said %d times: %v", len(lines), lines)
	}
	for _, want := range []string{
		quoted(from.sim.regionNm),
		gridSquare(msg.RegionHandle(43648, 43648)),
		from.sim.addr().String(),
		"a viewer would end the session",
	} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("the line %q does not say %q", lines[0], want)
		}
	}

	// Still the circuit the avatar is in, and still carrying messages.
	if err := a.Send.Send(context.Background(), &msg.AgentPause{}); err != nil {
		t.Fatal(err)
	}
	from.sim.waitSeen(t, "AgentPause", 5*time.Second)
	select {
	case <-a.Done():
		t.Fatalf("the root's DisableSimulator ended the session: %v", a.Err())
	default:
	}
	if givenBack(root) || a.sock.conn.Load() != root {
		t.Error("the root circuit's socket was closed or replaced")
	}
	if got := a.Neighbours(); len(got) != 1 || got[0].Handle != millAt {
		t.Errorf("Neighbours = %+v, want the child left alone", got)
	}
	if closed := said.saying("circuit closed"); len(closed) != 0 {
		t.Errorf("the root's DisableSimulator closed a child: %v", closed)
	}
}

// TestAReOfferAtAnotherAddressReplacesTheChild: an offer of a region
// already held, naming another address, is the region's simulator
// somewhere else, and the viewer replaces its entry for it
// (LLWorld::addRegion).  The old circuit is closed and the new address
// dialled.
func TestAReOfferAtAnotherAddressReplacesTheChild(t *testing.T) {
	var said logLines
	a, from, _ := twoRegions(t, Options{
		Neighbours: true,
		OnEvent:    func(string, []byte) {},
		Log:        said.log,
	})
	was, handle := aNeighbour(t, "Pelmar Mill", 43647, 43648)
	now := newFakeSim(t)
	now.regionNm = "Pelmar Mill"
	go now.run()
	t.Cleanup(now.close)

	from.eq.push("EnableSimulator", enableSimulator(handle, was.addr()))
	was.waitSeen(t, "UseCircuitCode", 5*time.Second)
	waitFor(t, "the neighbour to be held", func() bool { return len(a.Neighbours()) == 1 })
	conn := childConn(t, a, handle)

	from.eq.push("EnableSimulator", enableSimulator(handle, now.addr()))
	now.waitSeen(t, "UseCircuitCode", 5*time.Second)
	waitFor(t, "the new address to be held", func() bool {
		got := a.Neighbours()
		return len(got) == 1 && got[0].Addr == now.addr().String()
	})
	waitFor(t, "the old circuit's socket to be given back", func() bool { return givenBack(conn) })
	if n := dialled(was); n != 1 {
		t.Errorf("the old address was dialled %d times, want once", n)
	}
	if lines := said.saying("offered again at"); len(lines) != 1 {
		t.Errorf("replacing was reported as %v", said.saying("circuit closed"))
	}
}

// TestAReOfferOfASilentChildReplacesIt: a child that has heard nothing
// for NeighbourTimeout is dead, and an offer of its region at the same
// address dials it afresh rather than being taken as a repeat, as the
// viewer replaces a region whose circuit died.  The child is made silent
// by hand, well inside the watchdog's first look, so it is the offer
// that finds it dead.
func TestAReOfferOfASilentChildReplacesIt(t *testing.T) {
	var said logLines
	a, from, _ := twoRegions(t, Options{
		Neighbours: true,
		OnEvent:    func(string, []byte) {},
		Log:        said.log,
	})
	sim, handle := aNeighbour(t, "Pelmar Mill", 43647, 43648)

	from.eq.push("EnableSimulator", enableSimulator(handle, sim.addr()))
	sim.waitSeen(t, "RegionHandshakeReply", 5*time.Second)
	waitFor(t, "the neighbour to be held", func() bool { return len(a.Neighbours()) == 1 })
	conn := childConn(t, a, handle)
	a.neighMu.Lock()
	a.neighbours[handle].lastHeard.Store(time.Now().Add(-2 * NeighbourTimeout).UnixNano())
	a.neighMu.Unlock()

	from.eq.push("EnableSimulator", enableSimulator(handle, sim.addr()))
	waitFor(t, "the region to be dialled again", func() bool { return dialled(sim) == 2 })
	waitFor(t, "the dead circuit's socket to be given back", func() bool { return givenBack(conn) })
	waitFor(t, "the fresh circuit to be held", func() bool {
		a.neighMu.Lock()
		defer a.neighMu.Unlock()
		c := a.neighbours[handle]
		return c != nil && c.sock.conn.Load() != conn
	})
	if lines := said.saying("nothing heard"); len(lines) != 1 {
		t.Errorf("replacing was reported as %v", said.saying("circuit closed"))
	}
}

// TestASilentChildIsDroppedAndOfferedAfresh: a simulator that went away
// without a DisableSimulator sends nothing more, and the viewer drops a
// circuit that has been silent for its circuit timeout.  The child here
// hears its handshake and then nothing, is closed by its watchdog, and
// the next offer of the region is dialled.
func TestASilentChildIsDroppedAndOfferedAfresh(t *testing.T) {
	var said logLines
	a, from, _ := twoRegions(t, Options{
		Neighbours:       true,
		OnEvent:          func(string, []byte) {},
		Log:              said.log,
		neighbourTimeout: 300 * time.Millisecond,
	})
	sim, handle := aNeighbour(t, "Pelmar Mill", 43647, 43648)

	from.eq.push("EnableSimulator", enableSimulator(handle, sim.addr()))
	sim.waitSeen(t, "UseCircuitCode", 5*time.Second)
	waitFor(t, "the neighbour to be held", func() bool { return len(a.Neighbours()) == 1 })
	conn := childConn(t, a, handle)

	waitFor(t, "the silent child to be dropped", func() bool { return len(a.Neighbours()) == 0 })
	waitFor(t, "its socket to be given back", func() bool { return givenBack(conn) })
	// Said after the socket is given back, so waited for too.
	waitFor(t, "the drop to be said", func() bool { return len(said.saying("nothing heard for")) > 0 })
	if lines := said.saying("nothing heard for"); len(lines) != 1 {
		t.Errorf("dropping was reported as %v", said.saying("circuit closed"))
	}
	select {
	case <-a.Done():
		t.Fatalf("a silent neighbour ended the session: %v", a.Err())
	default:
	}

	from.eq.push("EnableSimulator", enableSimulator(handle, sim.addr()))
	waitFor(t, "the region to be dialled again", func() bool { return dialled(sim) == 2 })
	waitFor(t, "the region to be held again", func() bool { return len(a.Neighbours()) == 1 })
}

// TestAdjacentIsAnEdgeOrACorner: the cap goes by it, so a region two
// squares off, or the region itself, is not a neighbour.
func TestAdjacentIsAnEdgeOrACorner(t *testing.T) {
	here := msg.RegionHandle(43648, 43648)
	for _, c := range []struct {
		x, y uint32
		want bool
	}{
		{43647, 43648, true},
		{43649, 43648, true},
		{43648, 43649, true},
		{43649, 43647, true},
		{43647, 43649, true},
		{43648, 43648, false},
		{43650, 43648, false},
		{43648, 43646, false},
		{43650, 43650, false},
	} {
		if got := adjacent(msg.RegionHandle(c.x, c.y), here); got != c.want {
			t.Errorf("adjacent(%d, %d) = %v, want %v", c.x, c.y, got, c.want)
		}
	}
}

// TestClosingTheSessionClosesTheChildrenAndLeavesNoGoroutineBehind: a
// child is four goroutines and a socket, so getting this wrong leaks
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
