package agent

import (
	"net"
	"net/url"

	"github.com/quark-idlemind/slgo/llsd"
)

// Following the avatar when the grid moves it.
//
// TeleportFinish is the old simulator saying it has let go.  It names
// the simulator this avatar has been handed to and the seed capability
// to fetch that region's capabilities from, and nothing in this package
// asked for it: a client sent the request, or somebody's lure was
// accepted.  So this is the session being told that it has already been
// moved, and moveTo is what it does about it.  An avatar that walked
// over a border is told the same thing by a different message, which is
// crossing.go, and the reading of the fields below is shared with it.
//
// Reading it is the whole of the work here: a session that does not is
// left on a circuit to a region the avatar has gone from, which looks
// healthy until that region lets go and the watchdog ends the session.
// Why: doc/history/teleport.md#what-exists-today
//
// One measured body, from Agni, 298ms after a request that took this
// avatar from Pelmar Reach to Sandbox Goguen, and on the event queue
// rather than on the circuit:
//
//	{"Info":[{"AgentID":"...","LocationID":"AAAAAw==",
//	  "RegionHandle":"AAPjAAAD5QA=","SimAccess":13,
//	  "SimIP":"ywBxCw==","SimPort":13032,"TeleportFlags":"AAAAEA==",
//	  "SeedCapability":"https://simhost-....agni.secondlife.io:12043/cap/..."}]}
//
// Almost every field there is a different shape from the one beside it,
// and each is commented where it is read.

// noteTeleportFinish takes the session to the simulator a TeleportFinish
// names.
//
// The move runs here, inline, on the goroutine polling the event queue,
// rather than on one spawned for it.  Three things follow from that and
// all three are wanted.  The old region's remaining events are not
// handed to clients while the avatar is arriving somewhere else, because
// this goroutine is the one that would hand them over.  The poll against
// the region being left is cancelled from inside its own deliver, which
// is the path stopEventQueue was written for -- it cancels and does not
// wait, precisely so that the caller may be the poll itself.  And two
// moves cannot interleave, since a second finish cannot be delivered
// until this one has returned.
//
// What it costs is that the relay stalls for the length of the move --
// the handshake at the new simulator, the capability fetch, and
// whatever the timeout allows if the new simulator says nothing.  A
// client attached to this session hears nothing from the queue for that
// long.  The alternative is worse: a move on its own goroutine would
// have the region left behind still being relayed to clients from a
// poll the move is in the middle of cancelling.
func (a *Agent) noteTeleportFinish(body any) {
	addr, seed, handle := teleportDestination(body)
	if addr == nil {
		return
	}

	// A finish naming the region this session is already in is not a
	// move.  Acting on one would dial a second circuit to the simulator
	// that is already on the other end of this one, which is a relog to
	// where we are standing rather than a teleport.  It is reachable:
	// the grid answers a lure to a spot in the region already occupied
	// with a UDP TeleportLocal, but nothing promises that every grid
	// will, and a handle that agrees with ours is the plainest evidence
	// there is that nobody has gone anywhere.
	if handle != 0 && handle == a.RegionHandle() {
		return
	}

	// The context is the session's own, not the poll's.  The poll's is
	// about to be cancelled by this very move -- stopEventQueue, step
	// five of moveTo -- so a move carrying it would cancel itself
	// halfway through arriving.
	//
	// The error is not returned anywhere, because there is nobody to
	// return it to: this is an event arriving, not a call.  moveTo has
	// already ended the session for the failures that leave it pointed
	// at a simulator the avatar has left, and the two it does not end
	// -- an address that will not dial, and a session that was closing
	// anyway -- leave the circuit where it was, which the watchdog ends
	// in about a minute.
	_ = a.moveTo(a.runCtx, addr, seed)
}

// teleportDestination reads where a TeleportFinish says the avatar has
// been sent: the address to dial, the seed to fetch capabilities from,
// and the handle of the region it is all about.
//
// A body this cannot make an address out of gives a nil one, which is a
// body to do nothing about.  There is nothing to fall back on -- the
// event is the only place the destination is ever named -- so a session
// that could not read one waits for the watchdog, which is what it did
// before any of this existed.
func teleportDestination(body any) (addr *net.UDPAddr, seed string, handle uint64) {
	return destination(eventBlock(body, "Info"))
}

// destination reads an address, a seed and a handle out of one block of
// an event body.
//
// Every shape it expects was measured in the TeleportFinish above.
// CrossedRegion's block is read by this same function: that was
// inferred from the TeleportFinish first, and a captured CrossedRegion
// has since agreed with it; crossingDestination says so where it asks
// for it.
func destination(info map[string]any) (addr *net.UDPAddr, seed string, handle uint64) {
	if info == nil {
		return nil, "", 0
	}

	// The handle is LLSD binary, eight bytes big endian: the measured
	// 00 03 e3 00 00 03 e5 00 is 1094014069892352, which msg.GridCoords
	// reads as grid square (995, 997) -- Sandbox Goguen.  llsd.Int
	// decodes binary big endian, which is what LLSD binary always is.
	// LocationID and TeleportFlags are binary too, in four bytes, and
	// are read by nothing here.
	handle = uint64(llsd.Int(info, "RegionHandle"))

	// SimIP is binary and is NOT a number.  The measured "ywBxCw==" is
	// the four bytes cb 00 71 0b, which is 203.0.113.11 in network
	// order; read as an integer and formatted it would come out
	// backwards or as a ten-digit number, and either would be a bug
	// that only shows on a live grid.  So the bytes are used as bytes,
	// and a value that is not four of them is refused rather than
	// padded into an address that would be three quarters right.
	ip := llsd.Bytes(info, "SimIP")
	if len(ip) != 4 {
		return nil, "", 0
	}

	// SimPort, on the other hand, is a plain integer, as is SimAccess,
	// in the middle of a block where everything else is binary.
	// llsd.Int takes either, so what this costs is knowing it.
	port := llsd.Int(info, "SimPort")
	if port <= 0 || port > 65535 {
		return nil, "", 0
	}

	// The seed is a string, and the one field used as it stands rather
	// than decoded.
	seed = usableSeed(llsd.String(info, "SeedCapability"))

	return &net.UDPAddr{IP: net.IPv4(ip[0], ip[1], ip[2], ip[3]), Port: int(port)}, seed, handle
}

// usableSeed is a seed capability this session could ask for a
// capability set, or empty for one it could not.
//
// Anything that is not an absolute http URL is dropped instead of passed
// on: moveTo would ask it for the capability set, fail, and end the
// session over a field this daemon merely could not read, where an empty
// seed is a shape it has a documented answer for -- drop the old set
// rather than keep URLs into the region being left, and move the circuit
// anyway, which is the half of a move that cannot be done later.
func usableSeed(seed string) string {
	if u, err := url.Parse(seed); err != nil || !u.IsAbs() ||
		(u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return seed
}

// eventBlock is the first row of a named block of an event body.
//
// A block arrives as an array here even where the message template
// declares a single one: TeleportFinish.Info is Single and still came
// back as Info[0] on every measurement.  So the array is the shape to
// expect, and a bare map is taken as well, because a grid that sent one
// would otherwise read as an event that never arrived at all.
// CrossedRegion's blocks are Single in the template too, and came back
// as arrays in the one body captured.
func eventBlock(body any, name string) map[string]any {
	m := llsd.Map(body)
	if m == nil {
		return nil
	}
	switch v := m[name].(type) {
	case []any:
		for _, e := range v {
			if b := llsd.Map(e); b != nil {
				return b
			}
		}
	case map[string]any:
		return v
	}
	return nil
}
