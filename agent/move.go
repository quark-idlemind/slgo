package agent

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"time"
)

// Moving a live session to another simulator.
//
// A teleport keeps the grid session and changes the simulator under it:
// the avatar, the session id and the circuit code are the same on both
// sides of the move, which is what makes it a teleport rather than a
// relog.  The socket, the sequence numbers, the capabilities and the
// event queue belong to the region and none of them survive.
//
// What must survive is everything outside this package that reached
// through the Agent to talk to the simulator.  Six places hold a.Send,
// a.Recv or a.Disp -- server/grpc.go, server/group.go, sl/direct.go and
// viewer/circuit.go -- and none of them is told a move happened, so the
// objects keep their identity and the connection underneath them moves.

// socket is the connection the sender and the receiver were built over,
// with the one currently in use behind an atomic pointer.
//
// msg.NewSender takes a PacketWriter and msg.NewReceiver a PacketSource,
// both of them single-method interfaces that this satisfies, so moving
// the circuit is a store rather than a new Sender that none of the
// holders above would be holding.
type socket struct {
	conn atomic.Pointer[net.UDPConn]
}

func newSocket(c *net.UDPConn) *socket {
	s := &socket{}
	s.conn.Store(c)
	return s
}

// swap puts a new connection under the sender and the receiver and
// returns the one it replaced, still open.
//
// The receiver is almost certainly blocked in ReadFrom on the old
// connection at this moment, and a store on its own would leave it there
// until the region it has left said something.  A deadline in the past
// makes that read return immediately; closing would too, but the old
// connection stays open until the move has succeeded.
func (s *socket) swap(c *net.UDPConn) *net.UDPConn {
	old := s.conn.Swap(c)
	if old != nil {
		_ = old.SetReadDeadline(time.Now())
	}
	return old
}

func (s *socket) Write(p []byte) (int, error) {
	return s.conn.Load().Write(p)
}

// ReadFrom reads from whichever connection is current, and reads again
// if that stopped being true while it was blocked.
//
// Both outcomes of a swap are dealt with here.  The error is the one the
// deadline swap set to wake this read, and is not a failure to report --
// returning it would stop the receiver, and with it the session.  A
// datagram is one the region the avatar has left sent before it was told
// to stop, which is worse than an error: delivered now it would go
// through duplicate suppression under that region's sequence numbers,
// which is exactly the state the move has just cleared.
func (s *socket) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		c := s.conn.Load()
		n, addr, err := c.ReadFrom(p)
		if s.conn.Load() != c {
			continue
		}
		return n, addr, err
	}
}

// SetReadDeadline is how msg.Receiver unblocks a read when its context
// is cancelled.  It is a method on the interface it type-asserts for, so
// without this here a cancelled session would sit in ReadFrom until the
// simulator said something.
func (s *socket) SetReadDeadline(t time.Time) error {
	return s.conn.Load().SetReadDeadline(t)
}

func (s *socket) local() net.Addr { return s.conn.Load().LocalAddr() }

// Close closes the connection in use.  A connection a move replaced is
// the mover's to close, not this.
func (s *socket) Close() error {
	if s == nil {
		return nil
	}
	if c := s.conn.Load(); c != nil {
		return c.Close()
	}
	return nil
}

// moveTo takes this session to another simulator.
//
// It is all of a teleport except the teleport.  The address and the seed
// capability arrive in TeleportFinish, or in CrossedRegion for an avatar
// that walked over a border, and which of those it was is no business of
// this method: it is handed where to go, and what it does is see that a
// session pointed at one simulator ends up pointed at another with
// nothing above it noticing.
//
// The order is not free:
//
//  1. Dial first.  A dial that fails is the one failure that leaves the
//     session exactly where it was.
//  2. Swap the socket, so that the sender writes to the new simulator
//     and the receiver reads it.
//  3. Forget.  The dispatcher's remembered sequence numbers go because
//     the new simulator numbers from one and its handshake would be
//     dropped as a retransmission of the old one's -- the bug abaa2af
//     fixed one level up, for a viewer re-attaching.  The sender's
//     unacknowledged packets go because they were composed for a
//     simulator that will never acknowledge them, and retransmitting
//     them into the new region is worse than losing them.
//  4. Handshake, which is what says the avatar is really there.
//  5. Stop the event queue BEFORE the capabilities are replaced.  The
//     poll holds one region's URL for its whole life, and its last act
//     is to close that queue; letting the set change under it first is
//     how the region left's acknowledgement gets posted to the region
//     arrived in.
//  6. Start the poll again against the new region, if there was one.
//  7. Close the old connection last, because until the handshake has
//     been answered it is the only address that has ever talked to us.
//
// A move that fails after step 2 ends the session.  There is nothing to
// go back to: the origin hands the agent off before it says where to, so
// a circuit restored to it is a circuit to a region the avatar is not in
// -- which is the state stage 0 measured, where everything looked
// healthy for fifty seconds while the avatar was somewhere else.  Ending
// it says so at once and lets the reconnect above run in seconds rather
// than after the watchdog's minute, and a reconnect is a fresh login,
// which lands the avatar wherever the grid thinks it is.
//
// The region's own facts need nothing here.  The new simulator sends a
// RegionHandshake like any other, and the handler for it already swaps
// the object store, drops the terrain and the appearances and records
// the new name and flags.
func (a *Agent) moveTo(ctx context.Context, addr *net.UDPAddr, seed string) error {
	if addr == nil {
		return fmt.Errorf("agent: moveTo needs an address")
	}
	if a.sock == nil {
		return fmt.Errorf("agent: moveTo on a session with no circuit")
	}

	// One move at a time.  Two interleaved would swap the socket under
	// one another's handshake and each take away the arrival signal the
	// other installed -- and there is nothing far-fetched about it,
	// since a border crossing and a teleport are both moves and neither
	// asks the other first.
	a.moveMu.Lock()
	defer a.moveMu.Unlock()

	// A session that has ended is not moved.  Close closes done,
	// cancels, closes the socket and then waits on the goroutines, and
	// a move is the first thing in this package that spawns after
	// Connect has returned: the new region's poll added to that group
	// during the wait is the documented way to panic a WaitGroup.  The
	// dial has the same shape and a smaller cost -- a connection dialled
	// after Close has closed the socket is one nothing will ever close.
	//
	// This refuses a move for a session already over; it does not
	// prevent a Close arriving in the middle of one, which cannot be
	// prevented and does not need to be.  What that costs is a
	// handshake against a socket that closes under it, which fails and
	// says so, and a poll that starts and stops again -- both of them
	// what closing a session means.
	select {
	case <-a.done:
		if err := a.Err(); err != nil {
			return fmt.Errorf("agent: move to %s: the session has ended: %w", addr, err)
		}
		return fmt.Errorf("agent: move to %s: the session has ended", addr)
	default:
	}

	timeout := a.opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return fmt.Errorf("agent: dial %s: %w", addr, err)
	}

	// Both of these are in place before the socket moves, so that they
	// are ready for the new simulator's first packet rather than racing
	// it.
	a.forgetSeen.Store(true)
	arrived := newSignal()
	a.arrived.Store(&arrived)
	defer a.arrived.Store(nil)

	old := a.sock.swap(conn)
	// Last, whichever way this ends.  While the handshake is out the
	// old address is the only one that has ever answered us, and a
	// failed move that had already closed it would leave the session
	// with no socket at all rather than with a bad one.
	defer old.Close()

	// Forget rides the sender's message channel, so it is served in
	// order with the sends around it: asked for here it cannot reach
	// Run after the handshake it is meant to precede.
	if err := a.Send.Forget(ctx); err != nil {
		return a.moveFailed(addr, err)
	}
	if err := a.sendUseCircuitCode(ctx); err != nil {
		return a.moveFailed(addr, err)
	}
	// The circuit is not waited for.  Connect waits because a login
	// that reaches a dead simulator should say so before it sends
	// anything else; here the two messages go out together, as a viewer
	// sends them on arrival, and the answer to the second is the answer
	// to both.
	if err := a.sendCompleteAgentMovement(ctx); err != nil {
		return a.moveFailed(addr, err)
	}
	if err := a.await(ctx, arrived.wait(), timeout, "AgentMovementComplete"); err != nil {
		return a.moveFailed(addr, err)
	}

	a.stopEventQueue()

	// Every URL this session holds addresses the simulator it has left,
	// including the ones a capability reply offered along the way.
	a.forgetURLs()

	// The seed is recorded whatever is done with it below, including
	// when it is empty and when capabilities were never wanted.  It is
	// the answer to "where would this region's capabilities come from",
	// which something above may need -- slgod hands it to a viewer --
	// and the login response's answer to that stopped being true the
	// moment the socket moved.
	a.setSeed(seed)
	switch {
	case a.opts.SkipCaps:
		// Asked not to have any, at login and still.
	case seed == "":
		// Nothing to ask.  The old set is dropped rather than kept:
		// a request through one of those URLs would reach the wrong
		// simulator, which is worse than failing to find the
		// capability at all.
		a.SetCaps(Caps{})
	default:
		caps, err := RequestCaps(ctx, seed, a.opts.Caps, a.http())
		if err != nil {
			return a.moveFailed(addr, err)
		}
		a.SetCaps(caps)
	}

	// Only if there was one: the poll is spawned in Connect only when
	// something is listening, and a move that started one anyway would
	// poll a queue nobody reads for the rest of the session.
	if a.opts.OnEvent != nil {
		a.startEventQueue(a.runCtx, a.opts.OnEvent)
	}
	return nil
}

// moveFailed ends the session and says which move ended it.
//
// The name of the failure matters as much as the failure: a session that
// stopped because a simulator it was handed to never answered reads,
// without this, as the same "simulator silent" the watchdog reports for
// any lost circuit -- which is what stage 0 saw and could not tell apart
// from a teleport.
func (a *Agent) moveFailed(addr *net.UDPAddr, err error) error {
	e := fmt.Errorf("agent: move to %s: %w", addr, err)
	a.fail(e)
	return e
}

// forgetURLs drops the addresses a capability reply offered, which are
// one-shot uploaders and the like in the region being left.
func (a *Agent) forgetURLs() {
	a.urlMu.Lock()
	a.urls = nil
	a.urlMu.Unlock()
}
