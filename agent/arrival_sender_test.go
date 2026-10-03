package agent

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// feedFrom dispatches messages as if sent from addr.
func feedFrom(t *testing.T, a *Agent, addr *net.UDPAddr, ms ...msg.Message) {
	t.Helper()
	ch := make(chan *msg.Packet, len(ms))
	for _, m := range ms {
		ch <- &msg.Packet{
			At:      time.Now(),
			Addr:    addr,
			Header:  msg.Header{Sequence: seq.Add(1), Flags: msg.FlagReliable},
			ID:      msg.IDOf(m),
			Message: onTheWire(t, m),
		}
	}
	close(ch)
	if err := a.Disp.Run(context.Background(), ch); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
}

// TestOnlyTheDestinationArrives: while a move is arriving at one
// simulator, a RegionHandshake or AgentMovementComplete from another --
// the region being left, a packet of its dispatched late -- is not the
// arrival.  The viewer applies a handshake to the region at the sender's
// address.  Before this, the region left's late handshake completed a
// move to a region that never sent one.
// Why: doc/history/teleport.md#only-the-destination-arrives
func TestOnlyTheDestinationArrives(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	left := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 13001}
	dest := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 13002}

	e := &arrival{from: dest}
	a.mu.Lock()
	a.entering = e
	a.mu.Unlock()

	rh := &msg.RegionHandshake{}
	rh.RegionInfo.SimName = []byte("Testville\x00")
	amc := &msg.AgentMovementComplete{}
	amc.Data.RegionHandle = msg.RegionHandle(43520, 43520)

	feedFrom(t, a, left, rh, amc)
	a.mu.RLock()
	named, moved, still := e.named, e.moved, a.entering
	a.mu.RUnlock()
	if named || moved != nil || still != e {
		t.Fatalf("a handshake and movement from the region left were taken for the arrival: named %v, moved %v", named, moved != nil)
	}

	feedFrom(t, a, dest, rh, amc)
	a.mu.RLock()
	still = a.entering
	a.mu.RUnlock()
	if still != nil {
		t.Error("the destination's own handshake and movement did not complete the arrival")
	}
}

// TestOutsideAMoveAHandshakeIsTheCircuitsOwn: with no move under way the
// check does nothing; a handshake names this circuit's region as before.
func TestOutsideAMoveAHandshakeIsTheCircuitsOwn(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	rh := &msg.RegionHandshake{}
	rh.RegionInfo.SimName = []byte("Testville\x00")
	feedFrom(t, a, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 13003}, rh)
	if got := a.RegionName(); got != "Testville" {
		t.Errorf("region = %q, want the handshake's", got)
	}
}

// TestAnArrivalOutsideTheMoveDoesNotCompleteIt: a move installs its
// arrival signal a moment before it records where it is entering, and a
// movement from the region left that lands between the two is an
// ordinary arrival there -- it must not be taken for the move's.  The
// soak that found this had it complete a move to a region that never
// answered.
// Why: doc/history/teleport.md#only-the-destination-arrives
func TestAnArrivalOutsideTheMoveDoesNotCompleteIt(t *testing.T) {
	t.Parallel()
	a, _ := offlineSession(t)
	dest := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 13004}
	left := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 13005}
	arrived := newSignal()
	a.arrived.Store(&arrived)

	amc := &msg.AgentMovementComplete{}
	amc.Data.RegionHandle = msg.RegionHandle(43520, 43520)
	feedFrom(t, a, left, amc)
	select {
	case <-arrived.wait():
		t.Fatal("an arrival with no move entering anywhere completed the move")
	default:
	}

	a.mu.Lock()
	a.entering = &arrival{from: dest}
	a.mu.Unlock()
	rh := &msg.RegionHandshake{}
	rh.RegionInfo.SimName = []byte("Testville\x00")
	feedFrom(t, a, dest, rh, amc)
	select {
	case <-arrived.wait():
	default:
		t.Error("the destination's handshake and movement did not complete the move")
	}
}
