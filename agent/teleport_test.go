package agent

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

// agniTeleportFinish is a TeleportFinish as it came off the event queue
// on Agni on 2026-08-16, teleporting this avatar from Pelmar Reach to
// Sandbox Goguen.
//
// Kept byte for byte, base64 and all, because that is the only way the
// decoding is checked against a measurement rather than against the
// encoder in this repository.  Two of the fields would decode to
// something plausible and wrong if they were read as the shape the
// message template declares: RegionHandle is eight binary bytes rather
// than a U64, and SimIP is four binary bytes in network order rather
// than a number of any kind.
//
// What is not the grid's: the AgentID is a made-up uuid, and the seed's
// host and capability id are invented.  Everything that is read here is
// the measured value.
const agniTeleportFinish = `<llsd><map><key>Info</key><array><map>` +
	`<key>AgentID</key><uuid>45d57e57-7e57-c0de-d221-6ffd8a188ce4</uuid>` +
	`<key>LocationID</key><binary>AAAAAw==</binary>` +
	`<key>RegionHandle</key><binary>AAPjAAAD5QA=</binary>` +
	`<key>SeedCapability</key>` +
	`<string>https://simhost-0aaaaaaaaaaaaaaa2.agni.secondlife.io:12043/cap/` +
	`fd277e57-7e57-c0de-d6ee-dd800a9b6922</string>` +
	`<key>SimAccess</key><integer>13</integer>` +
	`<key>SimIP</key><binary>ywBxCw==</binary>` +
	`<key>SimPort</key><integer>13032</integer>` +
	`<key>TeleportFlags</key><binary>AAAAEA==</binary>` +
	`</map></array></map></llsd>`

// TestTheDestinationIsReadFromTheBytesTheGridSent: the whole of the
// decoding, against the body that was captured rather than one built
// here.
//
// The address is what has teeth.  203.0.113.11 is the four bytes
// cb 00 71 0b in the order they arrived; read as a big endian integer
// and formatted it would be 3405803787, and read as a little endian one
// it would be the same four bytes backwards -- somebody else's address
// entirely, dialled forever.  Neither mistake can show up anywhere but on a live grid,
// which is why the fixture is a measurement.
func TestTheDestinationIsReadFromTheBytesTheGridSent(t *testing.T) {
	addr, seed, handle := teleportDestination(decodeLLSD(t, agniTeleportFinish))
	if addr == nil {
		t.Fatal("the measured body was read as one with no destination in it")
	}
	if got := addr.String(); got != "203.0.113.11:13032" {
		t.Errorf("address = %s, want 203.0.113.11:13032", got)
	}
	if handle != 1094014069892352 {
		t.Errorf("handle = %d, want 1094014069892352", handle)
	}
	// Which is Sandbox Goguen, and saying so here is what makes the
	// number above readable.
	if x, y := msg.GridCoords(handle); x != 995 || y != 997 {
		t.Errorf("the handle is grid square (%d, %d), want (995, 997)", x, y)
	}
	if !strings.HasSuffix(seed, "fd277e57-7e57-c0de-d6ee-dd800a9b6922") {
		t.Errorf("seed = %q", seed)
	}
}

// TestATeleportFinishOnTheQueueTakesTheSessionToTheSimulatorItNames:
// stage 3's own half, end to end.  The event is put on the queue the way
// a simulator puts one there, and nothing else is called: the session
// notices on its own, which is the thing that did not happen before.
func TestATeleportFinishOnTheQueueTakesTheSessionToTheSimulatorItNames(t *testing.T) {
	a, from, to := twoRegions(t, Options{OnEvent: func(string, []byte) {}})

	from.eq.push("TeleportFinish", teleportFinishTo(to))

	// The capabilities rather than the region's name, because the name
	// comes from the handshake and the handshake is the middle of a
	// move: the capability set is replaced after it, from the seed the
	// event carried, which is the only place the new region's URLs are
	// ever named.
	waitFor(t, "the session to arrive in the region the event named", func() bool {
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
		t.Errorf("handle = %d after the move, want %d", a.RegionHandle(), want)
	}
	select {
	case <-a.Done():
		t.Fatalf("the session ended: %v", a.Err())
	default:
	}
}

// TestATeleportFinishForTheRegionAlreadyOccupiedMovesNothing: acting on
// one would dial a second circuit to the simulator that is already on
// the other end of this one.
func TestATeleportFinishForTheRegionAlreadyOccupiedMovesNothing(t *testing.T) {
	a, from, to := twoRegions(t, Options{OnEvent: func(string, []byte) {}})

	// Somewhere else's address and seed under this region's handle, so
	// that a move refused on the strength of the handle is the only
	// reason nothing happens.
	_, here := from.sim.arrival()
	body := teleportFinishTo(to)
	body["Info"].([]any)[0].(map[string]any)["RegionHandle"] = handleBytes(here)
	from.eq.push("TeleportFinish", body)

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

// TestATeleportFinishNobodyCanReadIsNotActedOn: the event is the only
// place the destination is ever named, so a body this cannot read leaves
// nothing to do but let the watchdog have it.  What it must not do is
// dial somewhere on half a body, or take the session down.
func TestATeleportFinishNobodyCanReadIsNotActedOn(t *testing.T) {
	a, from, to := twoRegions(t, Options{OnEvent: func(string, []byte) {}})

	// A three byte SimIP is the one worth naming: an address built from
	// three of the four bytes would be somewhere, and dialled it would
	// look like a simulator that never answered.
	short := teleportFinishTo(to)
	short["Info"].([]any)[0].(map[string]any)["SimIP"] = []byte{127, 0, 0}

	noPort := teleportFinishTo(to)
	noPort["Info"].([]any)[0].(map[string]any)["SimPort"] = int64(0)

	for _, body := range []map[string]any{
		{"Info": []any{}},
		{"Info": map[string]any{"nothing": "at all"}},
		{"AlertInfo": []any{}},
		short,
		noPort,
	} {
		from.eq.push("TeleportFinish", body)
	}

	// The session is still here, still in the region it started in, and
	// the queue still delivers -- which is what says the poll goroutine
	// came back rather than dying inside one of those.
	from.eq.push("TeleportFinish", teleportFinishTo(to))
	waitFor(t, "a readable event after the unreadable ones", func() bool {
		return a.RegionName() == to.sim.regionNm
	})
	if n := to.count("UseCircuitCode"); n != 1 {
		t.Errorf("the new simulator was dialled %d times, want once", n)
	}
	select {
	case <-a.Done():
		t.Fatalf("an unreadable event ended the session: %v", a.Err())
	default:
	}
}

// TestASeedThatIsNotAUrlStillMovesTheCircuit: the circuit is the half of
// a teleport that cannot be done afterwards, and the avatar has already
// been handed over by the time this arrives.  So a seed that cannot be
// asked for anything is dropped and the move goes ahead without
// capabilities, rather than the move failing and taking the session with
// it over a field this daemon could not read.
func TestASeedThatIsNotAUrlStillMovesTheCircuit(t *testing.T) {
	a, from, to := twoRegions(t, Options{OnEvent: func(string, []byte) {}})

	body := teleportFinishTo(to)
	body["Info"].([]any)[0].(map[string]any)["SeedCapability"] = "not a url at all"
	from.eq.push("TeleportFinish", body)

	// No capabilities rather than the region left behind's: a URL into a
	// region the avatar is not in is worse than not having one.  That
	// swap is the last thing a move does, so waiting for it is waiting
	// for the whole of one.
	waitFor(t, "the capabilities of the region left to be dropped", func() bool {
		_, ok := a.Caps().Get(EventQueueCap)
		return !ok
	})
	if got := a.RegionName(); got != to.sim.regionNm {
		t.Errorf("region = %q, want %q", got, to.sim.regionNm)
	}
	select {
	case <-a.Done():
		t.Fatalf("the session ended: %v", a.Err())
	default:
	}
}

// teleportFinishTo builds the body a simulator sends when it hands this
// avatar to another one, in the shapes measured on Agni: Info as an
// array of one map, the handle and the address as binary, the port as a
// plain integer.
func teleportFinishTo(r *fakeRegion) map[string]any {
	addr := r.sim.addr()
	_, handle := r.sim.arrival()
	return map[string]any{
		"Info": []any{map[string]any{
			"RegionHandle":   handleBytes(handle),
			"SimIP":          []byte(addr.IP.To4()),
			"SimPort":        int64(addr.Port),
			"SeedCapability": r.seed(),
			"SimAccess":      int64(13),
			"LocationID":     []byte{0, 0, 0, 3},
			"TeleportFlags":  []byte{0, 0, 0, 0x10},
		}},
	}
}

func handleBytes(h uint64) []byte {
	return binary.BigEndian.AppendUint64(nil, h)
}

func decodeLLSD(t *testing.T, s string) any {
	t.Helper()
	v, err := llsd.Decode(bytes.NewReader([]byte(s)))
	if err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}
	return v
}
