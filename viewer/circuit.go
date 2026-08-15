package viewer

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"sync/atomic"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
)

// PresenceLease is how far ahead the viewer's camera authority is
// renewed on each AgentUpdate it sends.
//
// A viewer sends these several times a second, so any value comfortably
// above that keeps the lease alive; what it really sets is how long a
// viewer that has stopped talking keeps the session mute.  Three seconds
// of no object updates after a crash is a shrug; a session that never
// speaks again is a dead avatar.
const PresenceLease = 3 * time.Second

// Backlog is how many simulator messages may be waiting to go to the
// viewer before the rest are dropped.
//
// There has to be a limit and it has to drop rather than block.  The
// hand-off happens on the grid session's dispatch goroutine, so a viewer
// that cannot keep up -- or a socket that has stopped draining -- would
// otherwise stall the session itself: no more object updates, no more
// chat, automate wedged, and all of it caused by a window somebody left
// open.  A viewer missing a few updates is a viewer that redraws
// something late.  A stalled session is the avatar gone.
const Backlog = 2048

// Circuit is the viewer's half of a handed-over session: a UDP socket on
// which slgod answers as though it were the simulator.
//
// It is deliberately not a proxy of a fresh login.  The session already
// exists, so most of what a viewer says at the start of one is about
// machinery that has been running for hours, and saying it again to the
// simulator would at best be ignored and at worst end the session.  What
// arrives here is therefore sorted rather than forwarded, and the
// sorting is the work.
type Circuit struct {
	conn *net.UDPConn

	// session is looked up rather than held, because a grid session
	// is replaced when it has to be re-established and a viewer
	// circuit outlives that.  Holding the pointer meant that after a
	// reconnect the circuit went on talking to a dead session: every
	// forward failed with "sender is not running" and the viewer sat
	// waiting for a region handshake that was being composed from a
	// session that had ended.
	session func() *agent.Agent

	send *msg.Sender
	recv *msg.Receiver
	disp *msg.Dispatcher

	census *Census
	trace  *Trace
	logf   func(string, ...any)

	// out carries what the simulator said, from the session's dispatch
	// goroutine to this circuit's own.  See Backlog.
	out     chan *msg.Packet
	dropped atomic.Uint64

	mu     sync.Mutex
	peer   *net.UDPAddr
	joined bool

	// pending holds the appearances kept from before this viewer
	// attached, until the viewer has been told the avatars they
	// describe exist.  See dressAvatars.
	pendingMu sync.Mutex
	pending   map[msg.UUID]*msg.AvatarAppearance

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// Listen opens a circuit for a viewer to find.
//
// The socket is opened before the login response is composed, because
// the port it lands on has to be named in that response -- a viewer is
// told where to send UDP once and never asks again.
func Listen(host string, session func() *agent.Agent, census *Census, trace *Trace, logf func(string, ...any)) (*Circuit, error) {
	if session == nil || session() == nil {
		return nil, fmt.Errorf("viewer: a circuit needs a session to hand over")
	}
	if logf == nil {
		logf = log.Printf
	}
	if census == nil {
		census = NewCensus()
	}
	addr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, "0"))
	if err != nil {
		return nil, fmt.Errorf("viewer: %w", err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("viewer: listening for a viewer: %w", err)
	}

	c := &Circuit{
		conn: conn, session: session, census: census, trace: trace, logf: logf,
		out: make(chan *msg.Packet, Backlog),
	}
	c.send = msg.NewSender(c, msg.WithSendTap(func(p *msg.Packet) {
		c.record(ToViewer, p, Forwarded)
	}))
	c.recv = msg.NewReceiver(conn, msg.KeepBody())
	c.disp = msg.NewDispatcher(
		msg.WithSender(c.send),
		// The peer is learned from the tap, not the relay, because it
		// has to be known for every packet including the ones the
		// relay never sees -- a bare acknowledgement is the first
		// thing some viewers send.
		msg.WithTap(func(p *msg.Packet) { c.notePeer(p.Addr) }),
		msg.WithRelay(c.fromViewer),
	)
	return c, nil
}

// Addr is where the viewer should send, which goes into the login
// response as sim_ip and sim_port.
func (c *Circuit) Addr() *net.UDPAddr { return c.conn.LocalAddr().(*net.UDPAddr) }

// Write satisfies msg.PacketWriter.  A simulator answers the circuit
// that called it and has nowhere else to send.
func (c *Circuit) Write(p []byte) (int, error) {
	c.mu.Lock()
	peer := c.peer
	c.mu.Unlock()
	if peer == nil {
		// Nothing has connected yet.  Reported as written so the
		// sender does not treat an idle circuit as a broken one.
		return len(p), nil
	}
	return c.conn.WriteToUDP(p, peer)
}

// Run serves the circuit until the context is cancelled.
func (c *Circuit) Run(ctx context.Context) {
	ctx, c.cancel = context.WithCancel(ctx)
	c.wg.Add(4)
	go func() { defer c.wg.Done(); c.recv.Run(ctx) }()
	go func() { defer c.wg.Done(); c.send.Run(ctx) }()
	go func() { defer c.wg.Done(); c.disp.Run(ctx, c.recv.C()) }()
	go func() { defer c.wg.Done(); c.pump(ctx) }()
	if a := c.session(); a != nil {
		c.logf("viewer: listening on %s for %s", c.Addr(), a.Account.Name())
	}
}

// Close ends the circuit and gives the camera back.
func (c *Circuit) Close() {
	if c.cancel != nil {
		c.cancel()
	}
	c.conn.Close()
	c.wg.Wait()
	// Whatever the viewer left the camera pointing at is not where
	// this session should go on looking.
	if a := c.session(); a != nil {
		a.ResumePresence()
	}
}

// Joined reports whether a viewer has completed the handshake.
func (c *Circuit) Joined() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.joined
}

// notePeer answers whoever is talking, which is what a simulator does.
//
// It follows the address rather than pinning the first one, because a
// viewer that is restarted comes back on a new port.  Pinning meant the
// circuit went on sending to the socket of a viewer that had quit, and
// the new one waited for a handshake that was being delivered to
// nobody.  The login endpoint decides who may attach; by here the
// question has been answered.
func (c *Circuit) notePeer(addr net.Addr) {
	ua, ok := addr.(*net.UDPAddr)
	if !ok {
		return
	}
	c.mu.Lock()
	changed := c.peer == nil || c.peer.String() != ua.String()
	if changed {
		if c.peer != nil {
			// A different viewer, so the last one's handshake
			// means nothing to it.
			c.joined = false
		}
		c.peer = ua
	}
	c.mu.Unlock()
	if changed {
		c.logf("viewer: a viewer appeared at %s", ua)
	}
}

func (c *Circuit) record(dir Direction, p *msg.Packet, what Disposition) {
	c.census.Record(MessageName(p), dir, p.At, what)
	c.trace.Write(dir, p, what)
}

// fromViewer sorts one message the viewer sent.
//
// Every path through here records what it decided, because a message
// classified wrongly does not announce itself: the viewer simply sits
// there, and the census showing the message under the wrong disposition
// is the only thing that says which one.
func (c *Circuit) fromViewer(p *msg.Packet) {
	name := MessageName(p)

	switch name {
	// ---- absorbed: about a circuit that is already open ----

	case "UseCircuitCode":
		// The claim on a circuit that has been open for hours.
		// Forwarding it would tell the simulator to reset the one
		// this session is using.
		c.checkCircuit(p)
		c.record(FromViewer, p, Absorbed)

	case "CompleteAgentMovement":
		// The request to be put in the region.  The avatar is
		// already there; what the viewer needs is the answer -- and
		// then everything the region said once, before it existed.
		c.record(FromViewer, p, Absorbed)
		c.describeRegion()
		c.sendMovementComplete()

	case "RegionHandshakeReply":
		// This session replied to the handshake when it arrived.
		c.record(FromViewer, p, Absorbed)

	case "LogoutRequest":
		// The one that matters most.  Forwarded, it would end the
		// grid session the moment somebody closed the viewer --
		// taking automate, every attached client and the avatar
		// with it.
		c.record(FromViewer, p, Absorbed)
		c.sendLogoutReply()
		c.logf("viewer: the viewer logged out; the session stays up")

	case "StartPingCheck", "CompletePingCheck":
		// Circuit machinery, per circuit.  This side answers the
		// viewer's and the session answers the simulator's.
		c.record(FromViewer, p, Absorbed)
		if name == "StartPingCheck" {
			c.answerPing(p)
		}

	// ---- forwarded, but read on the way past ----

	case "AgentUpdate":
		// The camera, which is not decoration: the simulator works
		// out what to stream from it.  While a viewer is attached it
		// is the viewer's, and this session stops sending its own.
		c.takeCamera(p)
		c.forward(p)

	default:
		c.forward(p)
	}
}

// forward passes a message to the simulator on the session's own
// circuit, where it is given that circuit's sequence number.
//
// It is a re-send rather than a relay of bytes, which is what keeps the
// two ack domains apart: neither side ever sees the other's numbering,
// and each retransmits on its own schedule.
func (c *Circuit) forward(p *msg.Packet) {
	if p.Message == nil {
		return
	}
	a := c.session()
	if a == nil {
		c.record(FromViewer, p, NoViewer)
		return
	}
	c.record(FromViewer, p, Forwarded)

	var err error
	if p.Header.Reliable() {
		err = a.Send.SendReliable(context.Background(), p.Message)
	} else {
		err = a.Send.Send(context.Background(), p.Message)
	}
	if err != nil {
		c.logf("viewer: forwarding %s: %v", MessageName(p), err)
	}
}

// checkCircuit says so when a viewer claims a circuit that is not this
// session's.  It is a loud log rather than a refusal: the login endpoint
// is what decides who may attach, and by here the answer is already yes.
func (c *Circuit) checkCircuit(p *msg.Packet) {
	m, ok := p.Message.(*msg.UseCircuitCode)
	if !ok {
		return
	}
	a := c.session()
	if a == nil {
		return
	}
	acct := a.Account
	if m.CircuitCode.Code != acct.CircuitCode || m.CircuitCode.SessionID != acct.SessionID {
		c.logf("viewer: a viewer claimed circuit %d session %s, but this session is %d/%s",
			m.CircuitCode.Code, m.CircuitCode.SessionID, acct.CircuitCode, acct.SessionID)
	}
}

// sendMovementComplete answers CompleteAgentMovement with where the
// avatar actually is.
//
// The real one arrived when this session joined the region, long before
// the viewer existed, and a viewer will not finish loading without it.
// Every field is one this session already holds.
func (c *Circuit) sendMovementComplete() {
	a := c.session()
	if a == nil {
		return
	}
	acct := a.Account
	m := &msg.AgentMovementComplete{}
	m.AgentData.AgentID = acct.AgentID
	m.AgentData.SessionID = acct.SessionID
	m.Data.Position = a.Position()
	m.Data.LookAt = a.Look().At
	m.Data.RegionHandle = a.RegionHandle()
	m.Data.Timestamp = uint32(time.Now().Unix())
	m.SimData.ChannelVersion = []byte(a.ChannelVersion() + "\x00")

	c.toViewer(m, msg.FlagReliable)

	c.mu.Lock()
	c.joined = true
	c.mu.Unlock()
	c.logf("viewer: told the viewer it is in %s at %v",
		a.RegionName(), m.Data.Position)
}

// describeRegion tells a joining viewer what the region said when this
// session arrived.
//
// A region introduces itself exactly once.  The handshake and the land
// arrived in the first seconds of a session that may have been up for
// hours, nothing will send them again, and a viewer will not begin
// rendering without them -- it sits on "Loading world" with no
// indication of what it is waiting for.
//
// Both are replayed rather than rebuilt.  The handshake is the message
// the simulator sent, kept whole: what this package decodes from it is
// twelve fields of thirty odd, and the ones it drops include every
// terrain texture id, so a viewer given a reconstruction would render
// ground with nothing on it.
func (c *Circuit) describeRegion() {
	a := c.session()
	if a == nil {
		return
	}
	if h := a.Handshake(); h != nil {
		c.toViewer(h, msg.FlagReliable)
	} else {
		c.logf("viewer: no region handshake to replay; the viewer will not finish loading")
	}

	for _, patch := range a.Terrain().Patches() {
		m := &msg.LayerData{}
		m.LayerID.Type = patch.Type
		m.LayerData.Data = patch.Data
		c.toViewer(m, msg.FlagReliable)
	}
	n, bytes, dropped := a.Terrain().Stats()
	c.logf("viewer: replayed the region handshake and %d land patches (%d bytes)", n, bytes)
	if dropped > 0 {
		c.logf("viewer: %d land patches were dropped for the size limit before this viewer attached", dropped)
	}

	// And the objects.  The region described each of them once, before
	// this viewer existed, so asking again is the only way it can be
	// told -- and asking is enough: the simulator answers with ordinary
	// updates, which reach the viewer the way everything else does and
	// carry every field the simulator sends rather than the dozen this
	// tree keeps.
	//
	// Without this a viewer sees only what changes while it watches,
	// which on a quiet parcel is almost nothing and reads as a relay
	// that has stopped working.
	if asked := a.Redescribe(); asked > 0 {
		c.logf("viewer: asked the simulator to describe %d objects again", asked)
	}

	// And how the avatars look, which the objects coming back will not
	// say.  These are held rather than sent: see dressAvatars for why
	// they cannot go out until their avatars have arrived.
	if held := a.Appearances().All(); len(held) > 0 {
		c.pendingMu.Lock()
		c.pending = held
		c.pendingMu.Unlock()
		c.logf("viewer: holding %d avatar appearances until the viewer knows the avatars", len(held))
	}
	if _, dropped := a.Appearances().Stats(); dropped > 0 {
		c.logf("viewer: %d avatar appearances were forgotten for the limit before this viewer attached", dropped)
	}

	// And whatever was said to the person while there was nothing to
	// show it on.  These are the messages a viewer exists to answer,
	// and until now they went into the daemon and stopped there: an
	// offered teleport arrived four minutes before the viewer did and
	// was never seen.
	//
	// Sent last, and after the region, because a viewer showing an
	// invitation before it has drawn anything is a dialogue over a grey
	// screen.
	if waiting := a.Offers().Take(); len(waiting) > 0 {
		for _, m := range waiting {
			c.toViewer(m, msg.FlagReliable)
		}
		c.logf("viewer: showed the viewer %d message(s) that arrived while nothing was attached", len(waiting))
	}
}

// pcodeAvatar is what the simulator calls an avatar in an object update.
const pcodeAvatar = 47

// dressAvatars sends a stored appearance once the viewer has been told
// that the avatar it describes exists.
//
// The order is the whole difficulty.  A viewer that receives an
// appearance for an avatar it has not heard of has nowhere to put it: it
// looks the avatar up by id, finds nothing, drops the message and says
// so in its log, and nothing ever asks again.  Meanwhile the avatars
// themselves only come back because Redescribe asked for them, seconds
// after this viewer joined and in whatever order the simulator answers.
//
// So rather than guess at a delay, each appearance waits for its own
// avatar and follows immediately behind it.
func (c *Circuit) dressAvatars(m msg.Message) {
	c.pendingMu.Lock()
	waiting := len(c.pending)
	c.pendingMu.Unlock()
	if waiting == 0 {
		return
	}

	switch u := m.(type) {
	case *msg.ObjectUpdate:
		for i := range u.ObjectData {
			if d := &u.ObjectData[i]; d.PCode == pcodeAvatar {
				c.dress(d.FullID)
			}
		}
	case *msg.ObjectUpdateCompressed:
		for i := range u.ObjectData {
			// A partly decoded object still says what it is and
			// which avatar it is, which is all this needs.
			d, _ := msg.DecodeCompressed(u.ObjectData[i].Data)
			if d != nil && d.PCode == pcodeAvatar {
				c.dress(d.FullID)
			}
		}
	}
}

// dress hands over one avatar's kept appearance, once.
func (c *Circuit) dress(id msg.UUID) {
	c.pendingMu.Lock()
	m := c.pending[id]
	delete(c.pending, id)
	c.pendingMu.Unlock()
	if m == nil {
		return
	}
	c.toViewer(m, msg.FlagReliable)
	c.logf("viewer: replayed how %s looks", id)
}

// sendLogoutReply lets the viewer quit cleanly.  Without it a viewer
// waits out its own timeout before closing, which reads as a hang.
func (c *Circuit) sendLogoutReply() {
	a := c.session()
	if a == nil {
		return
	}
	acct := a.Account
	m := &msg.LogoutReply{}
	m.AgentData.AgentID = acct.AgentID
	m.AgentData.SessionID = acct.SessionID
	c.toViewer(m, msg.FlagReliable)
}

func (c *Circuit) answerPing(p *msg.Packet) {
	in, ok := p.Message.(*msg.StartPingCheck)
	if !ok {
		return
	}
	out := &msg.CompletePingCheck{}
	out.PingID.PingID = in.PingID.PingID
	c.toViewer(out, 0)
}

// takeCamera reads the viewer's camera and stops this session sending
// its own.
//
// The pleasant consequence is that the object store then trims to what
// the person is actually looking at, rather than to a fixed view from
// wherever the avatar arrived.
func (c *Circuit) takeCamera(p *msg.Packet) {
	m, ok := p.Message.(*msg.AgentUpdate)
	if !ok {
		return
	}
	a := c.session()
	if a == nil {
		return
	}
	d := &m.AgentData
	a.SetLook(agent.Look{
		Center:       d.CameraCenter,
		At:           d.CameraAtAxis,
		Left:         d.CameraLeftAxis,
		Up:           d.CameraUpAxis,
		Far:          d.Far,
		ControlFlags: d.ControlFlags,
		State:        d.State,
	})
	a.DeferPresence(time.Now().Add(PresenceLease))
}

// toViewer sends a message slgod composed, as the simulator would.
func (c *Circuit) toViewer(m msg.Message, flags uint8) {
	var err error
	if flags&msg.FlagReliable != 0 {
		err = c.send.SendReliable(context.Background(), m)
	} else {
		err = c.send.Send(context.Background(), m)
	}
	if err != nil {
		c.logf("viewer: sending %s: %v", m.MsgInfo().Name, err)
	}
}

// FromSim offers a message the simulator sent, to be passed on to the
// viewer.
//
// It runs on the session's dispatch goroutine and must not block there,
// so it hands over and returns.  A full queue drops, and says so: see
// Backlog for why that is the right way round.
func (c *Circuit) FromSim(p *msg.Packet) {
	if p.Message == nil {
		return
	}
	switch MessageName(p) {
	case "StartPingCheck", "CompletePingCheck":
		// Per circuit.  This session answers the simulator's and
		// sends its own to the viewer; passing these on would have
		// the viewer answering pings meant for somebody else.
		c.record(FromSim, p, Absorbed)
		return
	case "KickUser":
		// The session's business, not the viewer's.  Whether a
		// viewer should be told its session is ending is a
		// question for when detaching cleanly exists.
		c.record(FromSim, p, Absorbed)
		return
	}

	select {
	case c.out <- p:
	default:
		c.dropped.Add(1)
		c.record(FromSim, p, Dropped)
	}
}

// Dropped is how many simulator messages were lost because the viewer
// was not keeping up.
func (c *Circuit) Dropped() uint64 { return c.dropped.Load() }

// pump moves what the simulator said onto the viewer's circuit, where it
// is given that circuit's own sequence number.
func (c *Circuit) pump(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case p := <-c.out:
			if !c.Joined() {
				// Before the handshake there is nowhere for
				// this to go: a viewer still asking to be let
				// in cannot make sense of the region yet.
				c.record(FromSim, p, NoViewer)
				continue
			}
			c.record(FromSim, p, Forwarded)
			var err error
			if p.Header.Reliable() {
				err = c.send.SendReliable(ctx, p.Message)
			} else {
				err = c.send.Send(ctx, p.Message)
			}
			if err != nil && ctx.Err() == nil {
				c.logf("viewer: passing on %s: %v", MessageName(p), err)
				continue
			}
			// The viewer now knows about whatever that described,
			// which for an avatar is the moment its appearance can
			// be delivered.
			c.dressAvatars(p.Message)
		}
	}
}
