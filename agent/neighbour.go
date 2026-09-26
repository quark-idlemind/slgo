package agent

import (
	"context"
	"fmt"
	"net"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

// Circuits to the regions around this one.
//
// A child agent is the same avatar, known to a simulator it is not
// standing in.  The circuit is opened with the same circuit code,
// session id and agent id as the root -- that is the whole of what
// UseCircuitCode carries -- and the one difference between a child and
// a root is that CompleteAgentMovement is never sent on it.  A child
// speaks for a place; the root speaks for the avatar.
//
// **A simulator will not hand an avatar over the border to a client
// that holds none of these**, and it was measured both ways.  With no
// child circuit an avatar walked at Pelmar Reach's west edge, stopped
// dead at x=0 and stayed there twelve seconds, and was pinned at x=255
// for twenty-four from the other side; with one open to Pelmar Mill the
// same walk was over the border in two seconds, and CrossedRegion
// arrived on the event queue exactly once, where crossing.go was
// already waiting for it.  See doc/history/neighbours.md, which is the
// plan this is stages one and two of.
//
// **Off unless asked for, and asked for per avatar.**  Neighbours cost
// a socket, a share of the bandwidth and the simulator's attention,
// multiplied by however many regions surround this one -- four for Pelmar
// Reach, up to eight elsewhere.  A session running a benchmark in one
// region wants none of that and one being driven by a person through a
// text viewer wants all of it, and a daemon holds both at once -- so
// this is a flag on the Agent rather than on the process, and it can be
// turned over while the session is up.  Options.Neighbours is only what
// it starts as; see SetNeighbours.  Off, no offer is read and no socket
// is opened, and the one handler that is registered either way returns
// on its first line.
//
// **Two handlers and no more.**  A child answers RegionHandshake and
// StartPingCheck and counts everything else.  Both are measured
// necessities rather than politeness: the handshake came back in about
// a second and came back TWICE, a retransmission with nothing else
// changed, so the answer has to be idempotent; and five StartPingCheck
// arrived in thirty seconds, so a child that did not answer them would
// be dropped like any other circuit.
//
// **What is deliberately not handled here.**  A neighbour describes
// itself fully and unprompted -- in thirty seconds one sent 59
// ObjectUpdate, 51 LayerData, 40 ObjectUpdateCached, 21
// CoarseLocationUpdate and 17 ImprovedTerseObjectUpdate -- and none of
// it is kept.  Terrain and objects from a neighbour are stage 4 of the
// plan, and wiring them to the Cache here would be wrong twice over:
// the store is keyed by the region uuid this avatar is IN, and local
// ids are each region's own numbering, so a neighbour's updates put
// through the root's store would describe this region with another
// one's objects.  Everything about the avatar -- AgentMovementComplete,
// KickUser, CrossedRegion -- must stay unhandled on a child for the
// other half of the same rule: a child that answered
// AgentMovementComplete would move this session's idea of where the
// avatar is to a region it is not in, and one that acted on KickUser
// would end the session on a neighbour's say-so.

// MaxNeighbours is how many child circuits a session will hold at once.
//
// Eight regions can surround one on the grid, which is the whole of a
// ring and so the whole of what this stage ever wants.  It is a cap
// rather than an expectation: the offers arrive from the simulator and
// nothing in a daemon should open sockets at a stranger's pace.
const MaxNeighbours = 8

// A Neighbour is one region beside this one and what this session has
// of it.
//
// Every field of it crosses to a client, which is what slsh's
// neighbours prints; no viewer sees a neighbour, and what a viewer may
// eventually be offered is a later stage of doc/history/neighbours.md.
type Neighbour struct {
	// Handle identifies the region on the grid.  msg.GridCoords turns
	// it into the square.
	Handle uint64

	// Addr is where the offer said the simulator was.
	Addr string

	// Name is what the region called itself in its handshake, or
	// empty for a circuit that has not been answered yet.
	Name string

	// Handshook says the simulator answered the circuit.  An address
	// that never does is an offer that came to nothing, which is
	// worth being able to see.
	Handshook bool

	// Heard is how many packets have arrived on the circuit,
	// counted and dropped.
	Heard uint64
}

// child is one circuit to a neighbouring simulator.
//
// It has its own socket, sender, receiver and dispatcher and shares
// none of the root's.  That is not tidiness: every circuit has its own
// sequence numbers, its own acknowledgements and its own duplicate
// ring, so a child hung off the root's would have two simulators
// numbering into one filter and each dropping the other's packets as
// retransmissions.
type child struct {
	handle uint64
	addr   *net.UDPAddr

	// The holder rather than the connection, for the reason Connect
	// uses one: msg.NewSender takes a PacketWriter and
	// msg.NewReceiver a PacketSource, and this is both, plus the
	// SetReadDeadline that lets a cancelled receiver stop waiting.
	sock *socket

	send *msg.Sender
	recv *msg.Receiver
	disp *msg.Dispatcher

	// cancel ends this circuit's three goroutines.  Its context is
	// the session's, so a session that ends takes its children with
	// it whether or not anyone drops them first.
	cancel context.CancelFunc

	// name is what the region called itself, under nameMu because
	// the dispatch goroutine writes it and Neighbours reads it.
	nameMu sync.Mutex
	name   string

	shook atomic.Bool
	heard atomic.Uint64
}

// NeighboursOn reports whether this session takes the offers up.
//
// Options.Neighbours is what it starts as and SetNeighbours is what
// changes it.  On with nothing held is an ordinary state and not a
// failure: a simulator offers a neighbour when the avatar is near one,
// and offered nothing at all to an avatar in a skybox in the middle of
// a region.
func (a *Agent) NeighboursOn() bool { return a.holdNeighbours.Load() }

// SetNeighbours turns the child circuits on or off for the rest of the
// session, or until it is called again.
//
// Turning them ON asks for nothing.  The simulator repeats an offer for
// as long as it goes untaken -- 57 times in a 200 second run naming four
// regions -- so a session that turns this on picks the next repeat up
// within seconds, and there is nothing here to send and no one to ask.
//
// Turning them OFF drops what is held rather than merely refusing what
// comes next.  A circuit left open would go on costing the socket and
// the share of the traffic this was turned off to stop paying, and the
// simulator would go on believing the avatar could be handed over the
// border at any moment.
//
// The flag is atomic and not under neighMu, which is what lets this be
// called from anywhere: dropNeighbours takes that lock, so a flag kept
// under it would be a caller holding the lock while waiting for it.
func (a *Agent) SetNeighbours(on bool) {
	if a.holdNeighbours.Swap(on) == on {
		return
	}
	if !on {
		a.dropNeighbours("neighbours were turned off")
	}
}

// Neighbours is the regions this session holds a circuit to, in grid
// handle order.
//
// Empty unless the session is holding them -- see NeighboursOn -- and
// empty again after a move: see dropNeighbours.
func (a *Agent) Neighbours() []Neighbour {
	a.neighMu.Lock()
	out := make([]Neighbour, 0, len(a.neighbours))
	for _, c := range a.neighbours {
		out = append(out, Neighbour{
			Handle:    c.handle,
			Addr:      c.addr.String(),
			Name:      c.regionName(),
			Handshook: c.shook.Load(),
			Heard:     c.heard.Load(),
		})
	}
	a.neighMu.Unlock()

	// In a definite order, because the map is not one and a listing
	// that shuffled itself between two readings would look like the
	// neighbours were changing.
	sort.Slice(out, func(i, j int) bool { return out[i].Handle < out[j].Handle })
	return out
}

// noteEnableSimulator takes up the offers an EnableSimulator on the
// event queue carries.
//
// The message is the simulator introducing a neighbour: a handle, an
// address and nothing else -- no capability, which is not what opening
// a circuit takes.  It repeats for as long as the offer goes untaken,
// 57 times in a 200 second run naming only four regions, and stops at
// one apiece once they are taken up.  So a repeat is a retry rather
// than a heartbeat, and openNeighbour refusing an offer it already
// holds is what makes it stop.
//
// It runs on the goroutine that polls the queue, like the two moves
// beside it, and unlike them it waits for nothing: the dial is a
// syscall and UseCircuitCode is queued rather than sent, so the poll is
// held up for microseconds.
func (a *Agent) noteEnableSimulator(body any) {
	if !a.NeighboursOn() {
		return
	}
	for _, row := range offeredSimulators(body) {
		// The handle arrives as LLSD binary and the port as a plain
		// integer, in the shapes stage 0's probe read successfully;
		// llsd.Int takes either.  The address is four binary bytes
		// in network order and is used as bytes, for the reason
		// teleport.go's destination gives at more length: read as a
		// number it would come out backwards, and only on a live
		// grid.
		a.openNeighbour(uint64(llsd.Int(row, "Handle")),
			neighbourAddr(llsd.Bytes(row, "IP"), llsd.Int(row, "Port")))
	}
}

// offeredSimulators is every SimulatorInfo block of an event body.
//
// eventBlock beside it takes the first row and no more, which is right
// for a destination -- an avatar is handed to one simulator -- and
// would be wrong here: the block is what names the neighbours, and
// there is nothing in the message to stop a simulator introducing two
// at once.  A bare map is taken as well, for eventBlock's reason.
func offeredSimulators(body any) []map[string]any {
	m := llsd.Map(body)
	if m == nil {
		return nil
	}
	switch v := m["SimulatorInfo"].(type) {
	case []any:
		rows := make([]map[string]any, 0, len(v))
		for _, e := range v {
			if r := llsd.Map(e); r != nil {
				rows = append(rows, r)
			}
		}
		return rows
	case map[string]any:
		return []map[string]any{v}
	}
	return nil
}

// neighbourAddr builds the address an offer names, or nil for one it
// could not.
//
// A neighbour that cannot be addressed is one to do nothing about.
// There is no falling back on anything: this message is the only place
// a neighbour's address is ever named, and what a bad one costs is a
// border that stays a wall, which is where this daemon stood before
// this file existed.
func neighbourAddr(ip []byte, port int64) *net.UDPAddr {
	if len(ip) != 4 || port <= 0 || port > 65535 {
		return nil
	}
	return &net.UDPAddr{IP: net.IPv4(ip[0], ip[1], ip[2], ip[3]), Port: int(port)}
}

// followNeighbours acts on an EnableSimulator that arrives on the
// circuit.
//
// The template marks the message UDPBlackListed, which says it belongs
// on the event queue, and that is where all 57 of stage 0's arrived.
// It is read here as well for the reason crossing.go reads CrossedRegion
// both ways: a deprecation flag is a promise a grid need not keep, and
// reading both roads costs one handler where reading one and guessing
// wrong costs the whole option on that grid.  The circuit road needs
// none of the LLSD guesswork above -- the generated type decodes IPADDR
// as four bytes in network order and IPPORT as a port -- so what is
// left to refuse is an address that names nowhere.
//
// **It is registered whether or not neighbours are on**, which reads
// like a mistake beside noteEnableSimulator asking the flag instead, so:
// this runs once, from register, and a registration cannot be withdrawn
// -- Dispatcher has no way to remove a handler, and a second MustHandle
// for one message panics.  A registration made when the flag went on
// would therefore have to be made once and never again, and would still
// have to check the flag for the case where it has since gone off.  That
// is this, without the bookkeeping.  What it costs when neighbours are
// off is a map entry and a returning function per EnableSimulator, where
// before it was a message counted as unhandled.
//
// It is not Inline, because a dial in the middle of the packet path is
// worth keeping off the dispatch goroutine; openNeighbour is serialized
// by its own lock, so two arriving at once cannot open the same
// neighbour twice.
func (a *Agent) followNeighbours() {
	a.Disp.MustHandle("EnableSimulator", func(p *msg.Packet) {
		if !a.NeighboursOn() {
			return
		}
		m, ok := p.Message.(*msg.EnableSimulator)
		if !ok {
			return
		}
		s := m.SimulatorInfo
		if s.IP == (msg.IPAddr{}) || s.Port == 0 {
			return
		}
		a.openNeighbour(s.Handle, &net.UDPAddr{
			IP:   net.IPv4(s.IP[0], s.IP[1], s.IP[2], s.IP[3]),
			Port: int(s.Port),
		})
	})
}

// openNeighbour holds a circuit to one neighbouring simulator.
//
// Everything is done under the one lock, the dial included.  The offers
// arrive on two goroutines -- the event queue's poll and the dispatcher
// -- and a check that let go before the socket was made would open two
// circuits to the same neighbour, each with its own sequence numbers,
// which is the one thing a simulator cannot be asked to make sense of.
// What that costs is a syscall's worth of a held mutex.
func (a *Agent) openNeighbour(handle uint64, addr *net.UDPAddr) {
	if handle == 0 || addr == nil {
		return
	}

	// The region the avatar is standing in is not a neighbour.  A
	// circuit to it would be a second one to the simulator already on
	// the other end of the root's, which is what crossTo refuses for
	// the same reason -- and here it is also how a stale offer reads
	// after a crossing into the region that made it.  Asked again
	// before the insert below.
	if handle == a.RegionHandle() {
		return
	}

	// A session that has ended opens nothing.  Close closes done,
	// cancels and then waits on the goroutine group, and an Add that
	// lands after the wait has begun panics; opening a child is the
	// second thing in this package to spawn after Connect returned,
	// and moveTo was the first.  Like moveTo this narrows the window
	// rather than closing it -- spawnChild asks again -- and what is
	// left is a circuit dialled into a session that is shutting down,
	// which is closed below the moment a spawn is refused.
	select {
	case <-a.done:
		return
	default:
	}

	a.neighMu.Lock()
	defer a.neighMu.Unlock()

	// Asked again here, under the lock, which is what makes turning
	// them off stick.  SetNeighbours puts the flag down and then takes
	// this lock to close what is held, so an offer already past the
	// check above either gets the lock first and is closed a moment
	// later, or gets it afterwards and finds the flag down.  Without
	// this, an offer in flight at the moment of the change would leave
	// one circuit open on a session that had just been told to hold
	// none.
	if !a.NeighboursOn() {
		return
	}

	// A neighbour already held is the offer being repeated, which is
	// what happens until it is taken up.  The address is not compared:
	// a region that moved to another host between two offers would go
	// on being talked to at the old one until the next move drops it,
	// and nothing has ever measured one moving.  Redialling on a
	// changed address is the wrong trade without that -- it would hand
	// anything that could forge an offer a way to make this session
	// drop a working circuit.
	if _, held := a.neighbours[handle]; held {
		return
	}
	if len(a.neighbours) >= MaxNeighbours {
		// Once per neighbour, not once per offer.  An offer that
		// goes untaken is repeated for as long as it is ignored --
		// 57 times in 200 seconds -- and this is exactly the case
		// where it will go on being ignored, so a line each would
		// be the log for the rest of the session.
		if !a.refused[handle] {
			if a.refused == nil {
				a.refused = map[uint64]bool{}
			}
			a.refused[handle] = true
			a.logf("neighbour %s at %s: not opened, %d circuits is the limit",
				gridSquare(handle), addr, MaxNeighbours)
		}
		return
	}

	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		a.logf("neighbour %s: dial %s: %v", gridSquare(handle), addr, err)
		return
	}

	ctx, cancel := context.WithCancel(a.runCtx)
	c := &child{handle: handle, addr: addr, sock: newSocket(conn), cancel: cancel}
	c.send = msg.NewSender(c.sock)
	c.recv = msg.NewReceiver(c.sock)
	c.disp = msg.NewDispatcher(
		msg.WithSender(c.send),
		// Everything the neighbour says, counted and dropped.  The
		// counter is the whole of what this stage does with a
		// region's description of itself; see the head of this file
		// for why none of it reaches the object store.
		msg.WithTap(func(*msg.Packet) { c.heard.Add(1) }),
	)

	// The handshake, answered every time it arrives rather than once:
	// it was measured arriving twice about a second apart, with
	// nothing else changed, so a child that replied once would be
	// answering a retransmission with silence.
	c.disp.MustHandle("RegionHandshake", func(p *msg.Packet) {
		m, ok := p.Message.(*msg.RegionHandshake)
		if !ok {
			return
		}
		name := trimNul(m.RegionInfo.SimName)
		c.setRegionName(name)

		reply := &msg.RegionHandshakeReply{}
		reply.AgentData.AgentID = a.Account.AgentID
		reply.AgentData.SessionID = a.Account.SessionID
		_ = c.send.SendReliable(ctx, reply)

		// Said once, for the same reason: the second handshake is
		// the first one again.
		if !c.shook.Swap(true) {
			a.logf("neighbour %s at %s: %s answered", gridSquare(handle), addr, quoted(name))
		}
	}, msg.Inline())

	// And the pings, which are not optional: five arrived in thirty
	// seconds, and a circuit that stops answering them is dropped.
	c.disp.MustHandle("StartPingCheck", func(p *msg.Packet) {
		m, ok := p.Message.(*msg.StartPingCheck)
		if !ok {
			return
		}
		reply := &msg.CompletePingCheck{}
		reply.PingID.PingID = m.PingID.PingID
		_ = c.send.Send(ctx, reply)
	}, msg.Inline())

	if !a.spawnChild(func() { _ = c.send.Run(ctx) }) ||
		!a.spawnChild(func() { _ = c.recv.Run(ctx) }) ||
		!a.spawnChild(func() { _ = c.disp.Run(ctx, c.recv.C()) }) {
		// The session ended between the check above and here.  The
		// socket is closed rather than left to a Close that has
		// already been through its list.
		c.close()
		return
	}

	// The three ids and nothing else, which is the whole of what a
	// circuit is opened with anywhere -- the same message the root
	// sends at login and at the far end of a teleport.  What is NOT
	// sent is CompleteAgentMovement: that is what would make this
	// circuit the one the avatar stands in, and sending it here would
	// move the avatar into the neighbour.
	if err := c.send.SendReliable(ctx, a.useCircuitCode()); err != nil {
		a.logf("neighbour %s at %s: UseCircuitCode: %v", gridSquare(handle), addr, err)
		c.close()
		return
	}

	// Asked again, under the lock: the avatar may have moved into this
	// region since the check at the top, and the move's dropNeighbours
	// may already have run, leaving nothing to close this circuit.
	// Taking a.mu under neighMu is safe because nothing takes neighMu
	// while holding a.mu.
	if handle == a.RegionHandle() {
		c.close()
		return
	}

	if a.neighbours == nil {
		a.neighbours = map[uint64]*child{}
	}
	a.neighbours[handle] = c
	a.logf("neighbour %s at %s: circuit open", gridSquare(handle), addr)
}

// spawnChild starts one of a child circuit's goroutines, reporting
// whether the session was still there to start it in.
//
// It is Agent.spawn without the failure: a child's sender returns an
// error the moment its socket is closed, which is how a circuit is
// dropped on purpose, and through spawn that would call fail and end
// the whole session over a neighbour nobody was talking to any more.
// Nothing a neighbour does is a reason to end this session -- the
// avatar is standing in the root's region, and a neighbour that went
// quiet costs a border crossing rather than a connection.
func (a *Agent) spawnChild(fn func()) bool {
	select {
	case <-a.done:
		return false
	default:
	}
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		fn()
	}()
	return true
}

// dropNeighbours closes every child circuit and says why.
//
// A move is the caller that matters.  After it the avatar is somewhere
// else and the regions around it are somebody else's neighbours: the
// circuits held are to the regions that surrounded the one it left, and
// one of them, on a crossing, is the region it has just moved INTO --
// which would otherwise sit in the map as a second circuit to the
// simulator the root is now talking to.  The new region makes its own
// offers within seconds of the avatar arriving, so what this costs is a
// few seconds with no neighbours rather than anything lasting.
//
// Promoting a child to root instead of dropping it -- crossing on the
// circuit that is already open, which is what would make a crossing
// seamless rather than merely possible -- is stage 3 of
// doc/history/neighbours.md and is deliberately not done here.
func (a *Agent) dropNeighbours(why string) {
	a.neighMu.Lock()
	held := a.neighbours
	a.neighbours, a.refused = nil, nil
	a.neighMu.Unlock()

	// In handle order, so that a log with several of them in reads the
	// same way twice.
	closing := make([]*child, 0, len(held))
	for _, c := range held {
		closing = append(closing, c)
	}
	sort.Slice(closing, func(i, j int) bool { return closing[i].handle < closing[j].handle })
	for _, c := range closing {
		c.close()
		a.logf("neighbour %s at %s: circuit closed, %s", gridSquare(c.handle), c.addr, why)
	}
}

// close ends a child's goroutines and gives its socket back.
//
// The cancel first, so that the receiver is on its way out before the
// read it is blocked in fails: the error is discarded either way, but
// the order is the one that makes the failure the ordinary end of a
// context rather than a socket pulled out from under it.
func (c *child) close() {
	if c.cancel != nil {
		c.cancel()
	}
	c.sock.Close()
}

func (c *child) setRegionName(name string) {
	c.nameMu.Lock()
	c.name = name
	c.nameMu.Unlock()
}

func (c *child) regionName() string {
	c.nameMu.Lock()
	defer c.nameMu.Unlock()
	return c.name
}

// gridSquare names a region the way a person reads one, since a handle
// is sixteen digits of nothing to look at.
func gridSquare(handle uint64) string {
	x, y := msg.GridCoords(handle)
	return fmt.Sprintf("(%d, %d)", x, y)
}

// quoted is a region's name for a log line, or a word for not having
// been told it yet.
func quoted(name string) string {
	if name == "" {
		return "an unnamed region"
	}
	return fmt.Sprintf("%q", name)
}
