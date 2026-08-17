package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// TestACrossedRegionOnTheQueueTakesTheSessionToTheSimulatorItNames: the
// stage in one assertion.  The event is put on the queue the way a
// simulator puts one there and nothing else is called, so what moves the
// session is the session noticing.
func TestACrossedRegionOnTheQueueTakesTheSessionToTheSimulatorItNames(t *testing.T) {
	a, from, to := twoRegions(t, Options{OnEvent: func(string, []byte) {}})

	from.eq.push("CrossedRegion", crossedRegionTo(to))

	// The capabilities rather than the name, for the reason the teleport
	// test gives: the name arrives in the middle of a move and the
	// capability set is replaced at the end of one, from the seed the
	// event carried.
	waitFor(t, "the session to arrive in the region the crossing named", func() bool {
		u, _ := a.Caps().Get(EventQueueCap)
		return strings.HasPrefix(u, to.http.URL)
	})
	if !to.saw("UseCircuitCode") || !to.saw("CompleteAgentMovement") {
		t.Errorf("the new simulator saw %v", to.sim.got())
	}
	if got := a.RegionName(); got != to.sim.regionNm {
		t.Errorf("region = %q, want %q", got, to.sim.regionNm)
	}
	if _, want := to.sim.arrival(); a.RegionHandle() != want {
		t.Errorf("handle = %d after the crossing, want %d", a.RegionHandle(), want)
	}
	select {
	case <-a.Done():
		t.Fatalf("the session ended: %v", a.Err())
	default:
	}
}

// TestACrossedRegionOnTheCircuitIsFollowedTheSameWay: the template says
// this message belongs on the event queue, and a deprecation flag is a
// promise a grid need not keep.  It is also the road that would deadlock
// if the handler ran on the dispatch goroutine, because the move waits
// for an AgentMovementComplete that goroutine has to deliver -- so this
// test failing with a session that ended by timeout is that mistake.
func TestACrossedRegionOnTheCircuitIsFollowedTheSameWay(t *testing.T) {
	a, from, to := twoRegions(t, Options{SkipCaps: true})

	from.sim.send(crossedRegionPacket(to), msg.FlagReliable)

	waitFor(t, "the session to arrive in the region the crossing named", func() bool {
		return a.RegionName() == to.sim.regionNm
	})
	if !to.saw("UseCircuitCode") || !to.saw("CompleteAgentMovement") {
		t.Errorf("the new simulator saw %v", to.sim.got())
	}
	select {
	case <-a.Done():
		t.Fatalf("the session ended: %v", a.Err())
	default:
	}
}

// TestACrossedRegionForTheRegionAlreadyOccupiedMovesNothing: acting on
// one dials a second circuit to the simulator already on the other end
// of this one.  It is also how the second road arrives on a grid that
// sends the message twice: by then the handle it carries is our own.
func TestACrossedRegionForTheRegionAlreadyOccupiedMovesNothing(t *testing.T) {
	a, from, to := twoRegions(t, Options{OnEvent: func(string, []byte) {}})

	// Somewhere else's address and seed under this region's handle, so
	// that the handle is the only reason nothing happens.
	_, here := from.sim.arrival()
	body := crossedRegionTo(to)
	body["RegionData"].([]any)[0].(map[string]any)["RegionHandle"] = handleBytes(here)
	from.eq.push("CrossedRegion", body)

	// Long enough for a move to have been begun and given up on: the
	// dial is immediate and UseCircuitCode goes out before anything is
	// waited for.
	time.Sleep(500 * time.Millisecond)
	if to.saw("UseCircuitCode") {
		t.Errorf("a second circuit was opened for a region we are in: %v", to.sim.got())
	}
	if got := a.RegionName(); got != from.sim.regionNm {
		t.Errorf("region = %q, want %q", got, from.sim.regionNm)
	}
}

// TestACrossedRegionNobodyCanReadIsNotActedOn: the message is the only
// place the destination is named, so a body this cannot read leaves
// nothing to do.  What it must not do is dial somewhere on half a body,
// end the session, or take the poll goroutine down with it -- and this
// one carries more weight than its teleport twin, because the shapes
// being read here are an inference and a wrong inference arrives looking
// exactly like these.
func TestACrossedRegionNobodyCanReadIsNotActedOn(t *testing.T) {
	a, from, to := twoRegions(t, Options{OnEvent: func(string, []byte) {}})

	// A three byte SimIP is the one worth naming: an address built from
	// three of the four bytes would be somewhere, and dialled it would
	// look like a simulator that never answered.
	short := crossedRegionTo(to)
	short["RegionData"].([]any)[0].(map[string]any)["SimIP"] = []byte{127, 0, 0}

	noPort := crossedRegionTo(to)
	noPort["RegionData"].([]any)[0].(map[string]any)["SimPort"] = int64(0)

	// And the destination where a TeleportFinish keeps it, which is what
	// a reader that went to the wrong block would happily act on.
	wrongBlock := map[string]any{
		"Info": crossedRegionTo(to)["RegionData"],
	}

	for _, body := range []map[string]any{
		{"RegionData": []any{}},
		{"RegionData": map[string]any{"nothing": "at all"}},
		{"AgentData": []any{}},
		short,
		noPort,
		wrongBlock,
	} {
		from.eq.push("CrossedRegion", body)
	}

	// The session is still here, still in the region it started in, and
	// the queue still delivers -- which is what says the poll goroutine
	// came back rather than dying inside one of those.
	from.eq.push("CrossedRegion", crossedRegionTo(to))
	waitFor(t, "a readable crossing after the unreadable ones", func() bool {
		return a.RegionName() == to.sim.regionNm
	})
	if n := to.count("UseCircuitCode"); n != 1 {
		t.Errorf("the new simulator was dialled %d times, want once", n)
	}
	select {
	case <-a.Done():
		t.Fatalf("an unreadable crossing ended the session: %v", a.Err())
	default:
	}
}

// TestACrossingIsReadFromRegionDataAndNotInfo: CrossedRegion carries
// both blocks and they mean opposite things -- RegionData is where the
// avatar has been sent and Info is where it will be standing when it
// gets there.  The message template is the authority for that and it is
// the one thing here that was not inferred, so it is worth a test of its
// own: a reader that followed TeleportFinish's habit and went to Info
// would find two vectors, no address, and nothing to say about it.
func TestACrossingIsReadFromRegionDataAndNotInfo(t *testing.T) {
	region := newRegion(t, "over the border", msg.MustParseUUID("35517e57-7e57-c0de-220b-d980670cf2d2"))
	region.sim.arrivalAt(msg.Vector3{X: 1, Y: 2, Z: 3}, msg.RegionHandle(43649, 43648))
	body := crossedRegionTo(region)

	addr, seed, handle := crossingDestination(body)
	if addr == nil {
		t.Fatal("a crossing with a RegionData block in it was read as having no destination")
	}
	if got, want := addr.String(), region.sim.addr().String(); got != want {
		t.Errorf("address = %s, want %s", got, want)
	}
	if _, want := region.sim.arrival(); handle != want {
		t.Errorf("handle = %d, want %d", handle, want)
	}
	if seed != region.seed() {
		t.Errorf("seed = %q, want %q", seed, region.seed())
	}

	// The same body with only the Info block left, which is what a
	// crossing whose RegionData this daemon could not read amounts to.
	if addr, _, _ := crossingDestination(map[string]any{"Info": body["Info"]}); addr != nil {
		t.Errorf("the arrival position was read as a destination: %s", addr)
	}
}

// crossedRegionTo builds the body a simulator would put on the queue
// when this avatar walks into another region's simulator.
//
// Inferred rather than measured: nobody has seen a CrossedRegion on this
// grid.  The block names and the fields in them are the message
// template's, and the LLSD shapes -- the handle and the address as
// binary, the port as an integer, the seed as a string -- are the ones
// stage 0 measured in a TeleportFinish.  If a real one differs, this
// fixture is where the difference belongs.
func crossedRegionTo(r *fakeRegion) map[string]any {
	addr := r.sim.addr()
	at, handle := r.sim.arrival()
	return map[string]any{
		"AgentData": []any{map[string]any{
			"AgentID":   "45d57e57-7e57-c0de-05ab-3469e908f363",
			"SessionID": "6d2b7e57-7e57-c0de-88a6-12d509e5ef7b",
		}},
		"RegionData": []any{map[string]any{
			"RegionHandle":   handleBytes(handle),
			"SimIP":          []byte(addr.IP.To4()),
			"SimPort":        int64(addr.Port),
			"SeedCapability": r.seed(),
		}},
		// Where the avatar comes out on the other side, which is what
		// makes this block the trap it is: it is a destination of a
		// sort, and it is not the one to dial.
		"Info": []any{map[string]any{
			"Position": []any{float64(at.X), float64(at.Y), float64(at.Z)},
			"LookAt":   []any{float64(1), float64(0), float64(0)},
		}},
	}
}

// crossedRegionPacket is the same crossing as the message a simulator
// would send on the circuit, where the generated types do the decoding
// and none of the LLSD inference applies.
func crossedRegionPacket(r *fakeRegion) *msg.CrossedRegion {
	addr := r.sim.addr()
	at, handle := r.sim.arrival()
	m := &msg.CrossedRegion{}
	copy(m.RegionData.SimIP[:], addr.IP.To4())
	m.RegionData.SimPort = msg.IPPort(addr.Port)
	m.RegionData.RegionHandle = handle
	m.RegionData.SeedCapability = []byte(r.seed() + "\x00")
	m.Info.Position = at
	m.Info.LookAt = msg.Vector3{X: 1}
	return m
}
