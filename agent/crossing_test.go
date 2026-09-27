package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// TestACrossedRegionOnTheQueueTakesTheSessionToTheSimulatorItNames:
// stage 7 of doc/history/teleport.md in one assertion.  The event is put
// on the queue the way a simulator puts one there and nothing else is
// called, so what moves the session is the session noticing.
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
// end the session, or take the poll goroutine down with it.
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

// agniCrossedRegion is a CrossedRegion as it came off the event queue on
// Agni on 2026-08-16, when this avatar walked west out of Pelmar Reach and
// into Pelmar Mill.
//
// Kept byte for byte.  Everything stage 7 of doc/history/teleport.md
// inferred from the measured TeleportFinish turned out to be right, and
// this is what says so: the destination is in RegionData and not in
// Info, the handle is eight binary bytes big endian, the address is four
// in network order, the port is a plain integer and the seed is a
// string.  Info is the
// arrival position -- 254.8, 128.0, 21.6, which is a stride the far side
// of a border at x=0.
//
// What is not the grid's: the two uuids, the seed's host and capability
// id, the address and the square the handle names are invented.  The
// rest is the measured value, and every shape is the measured shape.
const agniCrossedRegion = `<llsd><map>` +
	`<key>AgentData</key><array><map>` +
	`<key>AgentID</key><string>45d57e57-7e57-c0de-d221-6ffd8a188ce4</string>` +
	`<key>SessionID</key><string>c2ea7e57-7e57-c0de-aef9-4e04641652b3</string>` +
	`</map></array>` +
	`<key>Info</key><array><map>` +
	`<key>LookAt</key><array><real>-1</real><real>0</real><real>0</real></array>` +
	`<key>Position</key><array><real>254.76400756835938</real>` +
	`<real>128.0030059814453</real><real>21.62459945678711</real></array>` +
	`</map></array>` +
	`<key>RegionData</key><array><map>` +
	`<key>SimPort</key><integer>13009</integer>` +
	`<key>RegionHandle</key><binary>AKp/AACqgAA=</binary>` +
	`<key>SeedCapability</key>` +
	`<string>https://simhost-0aaaaaaaaaaaaaaa2.agni.secondlife.io:12043/cap/` +
	`d8ad7e57-7e57-c0de-c3e4-64514ffb79e0</string>` +
	`<key>SimIP</key><binary>ywBxCg==</binary>` +
	`</map></array></map></llsd>`

// TestTheCrossingIsReadFromTheBytesTheGridSent: the decoding, against
// the body that was captured rather than one built here.
//
// This is the test stage 7 of doc/history/teleport.md could not write,
// and writing it retired the stage's largest caveat.  Every shape in it was carried over from a
// TeleportFinish on the strength of the two messages describing the same
// thing, and a real CrossedRegion agrees in every field.
func TestTheCrossingIsReadFromTheBytesTheGridSent(t *testing.T) {
	addr, seed, handle := crossingDestination(decodeLLSD(t, agniCrossedRegion))
	if addr == nil {
		t.Fatal("the measured body was read as one with no destination in it")
	}
	if got := addr.String(); got != "203.0.113.10:13009" {
		t.Errorf("address = %s, want 203.0.113.10:13009", got)
	}
	// The same address EnableSimulator had been offering for this
	// neighbour all along, which is what makes a child circuit the thing
	// a crossing walks into rather than a second dial.
	if handle != 47990384028712960 {
		t.Errorf("handle = %d, want 47990384028712960", handle)
	}
	if x, y := msg.GridCoords(handle); x != 43647 || y != 43648 {
		t.Errorf("the handle is grid square (%d, %d), want (43647, 43648)", x, y)
	}
	if !strings.HasSuffix(seed, "d8ad7e57-7e57-c0de-c3e4-64514ffb79e0") {
		t.Errorf("seed = %q", seed)
	}
}

// TestACrossingIsReadFromRegionDataAndNotInfo: CrossedRegion carries
// both blocks and they mean opposite things -- RegionData is where the
// avatar has been sent and Info is where it will be standing when it
// gets there.  The message template is the authority for that, and the
// body captured on Agni agrees, so it is worth a test of its own: a
// reader that followed TeleportFinish's habit and went to Info
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
// Built rather than captured, because a test needs the fake region's
// own address in it.  The shapes are the measured ones -- see
// agniCrossedRegion above, which is a real body and agrees with every
// one of them.
func crossedRegionTo(r *fakeRegion) map[string]any {
	addr := r.sim.addr()
	at, handle := r.sim.arrival()
	return map[string]any{
		"AgentData": []any{map[string]any{
			"AgentID":   "45d57e57-7e57-c0de-d221-6ffd8a188ce4",
			"SessionID": "6d2b7e57-7e57-c0de-ba4e-212a750ee6e7",
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
