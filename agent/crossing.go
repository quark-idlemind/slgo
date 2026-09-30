package agent

import (
	"net"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// Following the avatar over a border.
//
// CrossedRegion is the same news as TeleportFinish and arrives for a
// different reason: the avatar walked, and nobody asked for anything at
// all.  It names the simulator the agent has been handed to and the seed
// to fetch that region's capabilities from, so what is owed is the same
// move, and this file is teleport.go's sibling down to the shapes it
// reads the fields in.
//
// **It arrives only for a client holding a child circuit** to the
// region over the border: without one the border is a wall, and the
// message never comes.  Options.Neighbours holds those circuits; see
// neighbour.go.
// Why: doc/history/neighbours.md#stage-0----take-one-offer-up-by-hand-done
//
// The destination is in RegionData, where Info is the arrival position;
// the template settles that, and the fields inside it are read as a
// TeleportFinish's are.  A captured body agrees with every field --
// agniCrossedRegion, in the test beside this file.
//
// A crossing this way is still a pause rather than the seamless thing a
// viewer gives you: moveTo dials the new simulator afresh even when a
// child circuit to it is already open, so the capabilities are fetched
// again and the region describes itself from nothing.  Promoting the
// child instead is stage 3 of doc/history/neighbours.md.

// noteCrossedRegion takes the session to the simulator a CrossedRegion
// on the event queue names.
//
// The move runs inline on the polling goroutine for the reasons set out
// on noteTeleportFinish, which apply here word for word: the region left
// behind stops being relayed to clients while the avatar arrives
// elsewhere, the poll being cancelled is the caller's own, and two moves
// cannot interleave.
func (a *Agent) noteCrossedRegion(body any) {
	a.crossTo(crossingDestination(body))
}

// crossingDestination reads where a CrossedRegion says the avatar has
// walked to.
//
// The block is RegionData, and that is the one thing here the message
// template settles: CrossedRegion carries AgentData, RegionData with the
// address, the handle and the seed in it, and an Info that is the
// arrival position and look-at.  A reader that went to Info because
// TeleportFinish's destination is there would find two vectors and no
// address at all.
//
// The fields inside RegionData are read with the reader written for the
// measured TeleportFinish -- handle and address as LLSD binary, port as
// an integer, seed as a string.  That was inferred first, on the grounds
// that the two messages describe the same thing and the template
// declares their fields with the same types, and a captured body has
// since agreed with every field: agniCrossedRegion, in the test beside
// this file.  A shape nobody expected still fails closed: destination
// refuses an address it cannot build out of four bytes and a port in
// range, so it reads as a body with no destination in it, and a body
// with no destination in it is one to do nothing about.  What that
// costs is a crossing not followed, which is where this daemon stood
// before this file existed.
func crossingDestination(body any) (addr *net.UDPAddr, seed string, handle uint64) {
	return destination(eventBlock(body, "RegionData"))
}

// followCrossings acts on a CrossedRegion that arrives on the circuit.
//
// The template marks the message UDPBlackListed, which says it belongs
// on the event queue, and stage 6 of doc/history/teleport.md made the
// point that a deprecation flag is a promise a grid need not keep.
// Reading both roads costs one handler; reading one and guessing wrong
// costs an avatar that has walked into a region this session is not
// connected to.
//
// It is deliberately NOT Inline, like EnableSimulator's in
// followNeighbours, and for a reason of its own.  A move waits for the
// new simulator's AgentMovementComplete, which is delivered by the
// handler registered for it -- inline, on the dispatch goroutine.  Run
// inline, this would be that goroutine, so the arrival it is waiting for
// could not be dispatched until it returned: the move would wait out its
// timeout and end the session, every time.  It was tried, and
// TestACrossedRegionOnTheCircuitIsFollowedTheSameWay timed out on
// exactly that.
// Why: doc/history/teleport.md#stage-7----walking-over-the-border-done-unverified
func (a *Agent) followCrossings() {
	a.Disp.MustHandle("CrossedRegion", func(p *msg.Packet) {
		m, ok := p.Message.(*msg.CrossedRegion)
		if !ok {
			return
		}
		r := m.RegionData
		// The circuit road needs none of the LLSD guesswork: the
		// generated types decode IPADDR as four bytes in network order
		// and IPPORT as a port, so what is left to refuse is an address
		// that names nowhere.  Dialled, a zero of either is this
		// machine or a port nothing listens on, and the move that
		// failed there would end the session.
		if r.SimIP == (msg.IPAddr{}) || r.SimPort == 0 {
			return
		}
		addr := &net.UDPAddr{
			IP:   net.IPv4(r.SimIP[0], r.SimIP[1], r.SimIP[2], r.SimIP[3]),
			Port: int(r.SimPort),
		}
		a.crossTo(addr, usableSeed(trimNulBytes(r.SeedCapability)), r.RegionHandle)
	})
}

// crossTo makes the move a crossing asks for, whichever road it came by.
//
// A nil address is a message this could not read, and there is nothing
// to fall back on: the message is the only place the destination is ever
// named.  The session stays where it is and the watchdog has it if the
// region really has stopped talking, which is what happened to a
// crossing before anything here read one.
func (a *Agent) crossTo(addr *net.UDPAddr, seed string, handle uint64) {
	if addr == nil {
		return
	}

	// A crossing into the region already occupied is not a move, for the
	// reason a teleport into it is not: acting on one dials a second
	// circuit to the simulator already on the other end of this one.
	// Here it is also the ordinary way the second road arrives -- a grid
	// that sent the message on the circuit as well as on the queue would
	// have the second one land after the first has finished moving, and
	// by then the handle it carries is this session's own.
	if handle != 0 && handle == a.RegionHandle() {
		return
	}

	// Walking over a border sends no TeleportStart, so any that is
	// kept belongs to a teleport that ended some other way, and is not
	// this arrival's cause.
	a.teleports.take(time.Now())

	// The session's context rather than anything belonging to the
	// goroutine that brought the message, which for the queue road is
	// about to be cancelled by this very move.  The error goes nowhere
	// because there is nobody to return it to: nothing asked for this,
	// and moveTo has already ended the session for the failures that
	// leave it pointed at a simulator the avatar has left.
	_ = a.moveTo(a.runCtx, addr, seed)
}
