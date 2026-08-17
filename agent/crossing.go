package agent

import (
	"net"

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
// **It arrives only for a client holding a child circuit**, and that was
// measured both ways.  A viewer keeps circuits to the simulators around
// it, answering the EnableSimulator each one is offered through, and a
// crossing is seamless precisely because the region over the border was
// already talking to it.  slgod connects to one simulator at a time by
// choice (c1e9d11), and while it did the border was a wall: an avatar
// walked at Pelmar Reach's west edge, stopped dead at x=0, and stayed
// there for twelve seconds; from the other side he was pinned at x=255
// for twenty-four.  No CrossedRegion, either time.  Take the offer up --
// one UDP socket, UseCircuitCode with this session's own three ids, and
// an answer to the handshake -- and the same walk crosses in two
// seconds, with the message arriving on the event queue exactly once.
// See doc/neighbours.md, which is the plan for holding those circuits
// properly; until it is built the handler below is reached only by a
// client that opened one by hand.
//
// **The shapes were inferred and then confirmed.**  Stage 7 read this
// message with the reader written for a measured TeleportFinish, on the
// grounds that the two describe the same thing, and marked every field
// as a guess.  A real body has since been captured and agrees with all
// of them -- see agniCrossedRegion in the test beside this file.  What
// the template settles rather than the wire is the block: the
// destination is in RegionData, where Info is the arrival position.
//
// A crossing this way is still a pause rather than the seamless thing a
// viewer gives you: moveTo dials the new simulator afresh even when a
// child circuit to it is already open, so the capabilities are fetched
// again and the region describes itself from nothing.  Promoting the
// child instead is stage 3 of doc/neighbours.md.

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
// Everything else is inference.  The fields inside RegionData are read
// with the reader written for the measured TeleportFinish -- handle and
// address as LLSD binary, port as an integer, seed as a string -- on the
// grounds that the two messages describe the same thing and the template
// declares their fields with the same types.  Nobody has seen the bytes.
// If the inference is wrong the failure is contained: destination
// refuses an address it cannot build out of four bytes and a port in
// range, so a shape nobody expected reads as a body with no destination
// in it, and a body with no destination in it is one to do nothing
// about.  What that costs is a crossing not followed, which is where
// this daemon stood before this file existed.
func crossingDestination(body any) (addr *net.UDPAddr, seed string, handle uint64) {
	return destination(eventBlock(body, "RegionData"))
}

// followCrossings acts on a CrossedRegion that arrives on the circuit.
//
// The template marks the message UDPBlackListed, which says it belongs
// on the event queue, and stage 6 made the point that a deprecation flag
// is a promise a grid need not keep.  Reading both roads costs one
// handler; reading one and guessing wrong costs an avatar that has
// walked into a region this session is not connected to.
//
// It is deliberately NOT Inline, and it is the only handler in this
// package that has a reason to say so.  A move waits for the new
// simulator's AgentMovementComplete, which is delivered by the handler
// registered for it -- inline, on the dispatch goroutine.  Run inline,
// this would be that goroutine, so the arrival it is waiting for could
// not be dispatched until it returned: the move would wait out its
// timeout and end the session, every time, for a crossing that was
// working perfectly.  That is not a reading of the dispatcher -- it was
// tried, and the circuit test below timed out on exactly that.
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

	// The session's context rather than anything belonging to the
	// goroutine that brought the message, which for the queue road is
	// about to be cancelled by this very move.  The error goes nowhere
	// because there is nobody to return it to: nothing asked for this,
	// and moveTo has already ended the session for the failures that
	// leave it pointed at a simulator the avatar has left.
	_ = a.moveTo(a.runCtx, addr, seed)
}
