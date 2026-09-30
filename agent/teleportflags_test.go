package agent

import (
	"context"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

type changed struct {
	name   string
	handle uint64
	flags  uint32
}

// watchingRegions is an offline session that says what each region
// change it announces was told.
func watchingRegions(t *testing.T) (*Agent, chan changed) {
	t.Helper()
	a, _ := offlineSession(t)
	told := make(chan changed, 8)
	a.opts.OnRegionChange = func(name string, handle uint64, flags uint32) {
		told <- changed{name, handle, flags}
	}
	return a, told
}

func teleportStart(flags uint32) *msg.TeleportStart {
	m := &msg.TeleportStart{}
	m.Info.TeleportFlags = flags
	return m
}

// movementTo is the AgentMovementComplete of arriving in a region.
func movementTo(handle uint64) *msg.AgentMovementComplete {
	m := &msg.AgentMovementComplete{}
	m.Data.RegionHandle = handle
	m.Data.Position = msg.Vector3{X: 128, Y: 128, Z: 30}
	return m
}

func nothingTold(t *testing.T, told chan changed, when string) {
	t.Helper()
	select {
	case c := <-told:
		t.Errorf("%s: told %+v", when, c)
	default:
	}
}

var (
	westHandle = msg.RegionHandle(0xAA80, 0xAA80)
	eastHandle = msg.RegionHandle(0xAA81, 0xAA80)
	farHandle  = msg.RegionHandle(0xAA81, 0xAA81)
)

// TestATeleportStartsFlagsAttachToTheNextArrivalAndAreSpent: the flags
// ride on the region change of the arrival the start began, and the
// arrival after it, which no start began, has none.
func TestATeleportStartsFlagsAttachToTheNextArrivalAndAreSpent(t *testing.T) {
	t.Parallel()
	a, told := watchingRegions(t)
	feed(t, a, movementTo(westHandle))
	nothingTold(t, told, "the first arrival")

	feed(t, a, teleportStart(TeleportViaHome|TeleportForceRedirect), movementTo(eastHandle))
	if got := <-told; got.handle != eastHandle || got.flags != TeleportViaHome|TeleportForceRedirect {
		t.Errorf("told %+v, want the flags of the start", got)
	}

	feed(t, a, movementTo(farHandle))
	if got := <-told; got.handle != farHandle || got.flags != 0 {
		t.Errorf("an arrival with no start of its own was told %+v", got)
	}
}

// TestALandmarksTwoStartsAreOneCause: the simulator sends two
// TeleportStarts for a landmark teleport, and the arrival gets the one
// cause and spends it.
func TestALandmarksTwoStartsAreOneCause(t *testing.T) {
	t.Parallel()
	a, told := watchingRegions(t)
	feed(t, a, movementTo(westHandle))

	feed(t, a, teleportStart(TeleportViaLandmark), teleportStart(TeleportViaLandmark), movementTo(eastHandle))
	if got := <-told; got.flags != TeleportViaLandmark {
		t.Errorf("told flags %#x, want the landmark's", got.flags)
	}
	feed(t, a, movementTo(farHandle))
	if got := <-told; got.flags != 0 {
		t.Errorf("the second start outlived the arrival: told flags %#x", got.flags)
	}
}

// TestATeleportStartFromLoginIsNotTheNextTeleportsCause: a
// TeleportStart is sent at login, and the first arrival spends it
// rather than leaving it for the next move.  (One that came after the
// first arrival is not covered: see doc/avatar-state.md#teleportstart.)
func TestATeleportStartFromLoginIsNotTheNextTeleportsCause(t *testing.T) {
	t.Parallel()
	a, told := watchingRegions(t)

	// Before the first arrival: spent by it.
	feed(t, a, teleportStart(TeleportViaLogin), movementTo(westHandle))
	feed(t, a, movementTo(eastHandle))
	if got := <-told; got.flags != 0 {
		t.Errorf("the login's start was given to a later arrival: flags %#x", got.flags)
	}
}

// TestAStaleTeleportStartIsNotAttached: one that came a long while ago
// was for a teleport that did not arrive.
func TestAStaleTeleportStartIsNotAttached(t *testing.T) {
	t.Parallel()
	a, told := watchingRegions(t)
	feed(t, a, movementTo(westHandle))

	a.teleports.note(TeleportViaLure, time.Now().Add(-TeleportCauseKept-time.Second))
	feed(t, a, movementTo(eastHandle))
	if got := <-told; got.flags != 0 {
		t.Errorf("a start from over a minute before was attached: flags %#x", got.flags)
	}

	a.teleports.note(TeleportViaLure, time.Now().Add(-TeleportCauseKept+5*time.Second))
	feed(t, a, movementTo(farHandle))
	if got := <-told; got.flags != TeleportViaLure {
		t.Errorf("a start from just under a minute before was dropped: flags %#x", got.flags)
	}
}

// TestALocalTeleportSpendsItsStart: a TeleportStart and then a
// TeleportLocal, twenty microseconds apart as measured, is a teleport
// that arrived nowhere else, and its cause is not the next border
// crossing's.
func TestALocalTeleportSpendsItsStart(t *testing.T) {
	t.Parallel()
	a, told := watchingRegions(t)
	feed(t, a, movementTo(westHandle))

	local := &msg.TeleportLocal{}
	local.Info.Position = msg.Vector3{X: 10, Y: 10, Z: 30}
	feed(t, a, teleportStart(TeleportViaLocation), local)
	nothingTold(t, told, "a teleport within the region")

	feed(t, a, movementTo(eastHandle))
	if got := <-told; got.flags != 0 {
		t.Errorf("a local teleport's start was given to the next arrival: flags %#x", got.flags)
	}
}

// TestACrossingIsNotGivenAStartItDidNotHave: a border crossed sends no
// TeleportStart, so what is kept is left from a teleport that ended
// some other way, and the crossing says no cause.
func TestACrossingIsNotGivenAStartItDidNotHave(t *testing.T) {
	told := make(chan changed, 4)
	a, from, to := twoRegions(t, Options{SkipCaps: true,
		OnRegionChange: func(name string, handle uint64, flags uint32) { told <- changed{name, handle, flags} }})

	from.sim.send(teleportStart(TeleportViaLure), 0)
	waitStartKept(t, a)
	_, handle := to.sim.arrival()
	a.crossTo(to.sim.addr(), to.seed(), handle)

	select {
	case got := <-told:
		if got.flags != 0 {
			t.Errorf("a crossing was told flags %#x", got.flags)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a crossing said nothing")
	}
}

// waitStartKept waits for a TeleportStart sent over the circuit to have
// been read.
func waitStartKept(t *testing.T, a *Agent) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		a.teleports.mu.Lock()
		set := a.teleports.set
		a.teleports.mu.Unlock()
		if set {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the TeleportStart was never kept")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestARealMoveCarriesTheStartsFlags is the whole road: a simulator
// says TeleportStart, the session is moved to another, and the change
// it announces says why.
func TestARealMoveCarriesTheStartsFlags(t *testing.T) {
	told := make(chan changed, 4)
	a, from, to := twoRegions(t, Options{SkipCaps: true,
		OnRegionChange: func(name string, handle uint64, flags uint32) { told <- changed{name, handle, flags} }})

	from.sim.send(teleportStart(TeleportViaLocation), 0)
	waitStartKept(t, a)

	if err := a.moveTo(context.Background(), to.sim.addr(), to.seed()); err != nil {
		t.Fatalf("moveTo: %v", err)
	}
	select {
	case got := <-told:
		if got.flags != TeleportViaLocation {
			t.Errorf("the move was told flags %#x, want %#x", got.flags, TeleportViaLocation)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a move said nothing")
	}
}
