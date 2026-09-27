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
	"github.com/quark-idlemind/slgo/internal/redact"
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
// chat, slrun wedged, and all of it caused by a window somebody left
// open.  A viewer missing a few updates is a viewer that redraws
// something late.  A stalled session is the avatar gone.
const Backlog = 2048

// SilenceTimeout is how long a joined viewer may send nothing before it
// is taken as gone, as though it had logged out.
//
// It is the viewer's own circuit timeout (newview/llstartup.cpp:916),
// the figure agent.NeighbourTimeout uses too.  A viewer that is there
// is never that quiet: it sends an AgentUpdate several times a second.
const SilenceTimeout = 100 * time.Second

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
	// refused counts datagrams dropped by admit, from an address
	// that never opened this circuit.
	refused atomic.Uint64

	conn *net.UDPConn

	// session is looked up rather than held, because a grid session
	// is replaced when it has to be re-established and a viewer
	// circuit outlives that.
	// Why: doc/handover.md#the-session-is-looked-up-not-held
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

	// heard is when the admitted viewer last sent anything, as unix
	// nanoseconds, and joins is told of each join.  See watch.
	heard atomic.Int64
	joins chan struct{}

	// silence stands in for SilenceTimeout when set, so that a test
	// need not wait a hundred seconds for a viewer to be found gone.
	silence time.Duration

	// forwarded is told of each message passed on to the simulator.
	// Guarded by mu.  See OnForward.
	forwarded func(msg.Message)

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
		out: make(chan *msg.Packet, Backlog), joins: make(chan struct{}, 1),
	}
	c.send = msg.NewSender(c, msg.WithSendTap(func(p *msg.Packet) {
		c.record(ToViewer, p, Forwarded)
	}))
	c.recv = msg.NewReceiver(conn, msg.KeepBody())
	c.disp = msg.NewDispatcher(
		msg.WithSender(c.send),
		// The peer is settled by the gate, not by the relay or a tap,
		// because it has to be decided for every packet including the
		// ones the relay never sees -- a bare acknowledgement is the
		// first thing some viewers send -- and because a packet from
		// an address this circuit has not admitted must not be
		// acknowledged either.  See admit.
		msg.WithGate(c.admit),
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
	c.wg.Add(5)
	go func() { defer c.wg.Done(); c.recv.Run(ctx) }()
	go func() { defer c.wg.Done(); c.send.Run(ctx) }()
	go func() { defer c.wg.Done(); c.disp.Run(ctx, c.recv.C()) }()
	go func() { defer c.wg.Done(); c.pump(ctx) }()
	go func() { defer c.wg.Done(); c.watch(ctx) }()
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

// Joined reports whether a viewer has completed the handshake and not
// left since.  One that goes without logging out -- a crash, a lost
// connection -- is taken as gone once it has sent nothing for
// SilenceTimeout.
func (c *Circuit) Joined() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.joined
}

// OnForward sets what is told of each message the viewer sends that this
// circuit passes on to the simulator, just before it goes.  Nil tells
// nothing.
//
// What a viewer sends reaches the session down this circuit and through
// nothing else of the daemon's, so a teleport or a new home asked for at
// a viewer is seen here or not at all.  What is absorbed here is not
// told: the grid never hears it.
//
// It is called on the circuit's dispatch goroutine and must not block.
func (c *Circuit) OnForward(f func(msg.Message)) {
	c.mu.Lock()
	c.forwarded = f
	c.mu.Unlock()
}

// admit decides whether a datagram is this circuit's viewer talking.
//
// One from the admitted address is.  A new address is adopted only by
// opening with a UseCircuitCode carrying this session's own circuit code
// and session id, which is what a viewer sends first; anything else from
// it, a packet that would not decode included, is dropped unread and
// counted.  The address is followed rather than pinned because a
// restarted viewer comes back on a new port.  The ids prove this is the
// viewer the login response went to and are not a secret: what keeps a
// stranger out is never having been given that response.
// Why: doc/handover.md#who-may-talk-on-the-circuit
func (c *Circuit) admit(p *msg.Packet) bool {
	ua, ok := p.Addr.(*net.UDPAddr)
	if !ok {
		return false
	}
	c.mu.Lock()
	known := c.peer != nil && c.peer.String() == ua.String()
	c.mu.Unlock()
	if known {
		c.heard.Store(time.Now().UnixNano())
		return true
	}

	// A stranger.  Only the message that opens a circuit gets any
	// further, and only with the right ids in it.
	m, ok := p.Message.(*msg.UseCircuitCode)
	if !ok || !c.ownCircuit(m) {
		c.refuse(ua)
		return false
	}
	c.notePeer(ua)
	c.heard.Store(time.Now().UnixNano())
	return true
}

// ownCircuit reports whether a UseCircuitCode names this session.
func (c *Circuit) ownCircuit(m *msg.UseCircuitCode) bool {
	a := c.session()
	if a == nil || a.Account == nil {
		return false
	}
	return m.CircuitCode.Code == a.Account.CircuitCode &&
		m.CircuitCode.SessionID == a.Account.SessionID
}

// refuse counts a datagram from an address that has not opened a
// circuit, and logs the first and then every thousandth.
//
// Rarely, because a sender that floods the port would otherwise fill the
// log with a line per datagram, a second way to be harmed by it; and
// with nothing of this session's own ids, because a refusal that quoted
// the circuit code it wanted would help the sender more than the
// operator.
func (c *Circuit) refuse(ua *net.UDPAddr) {
	n := c.refused.Add(1)
	if n == 1 || n%1000 == 0 {
		c.logf("viewer: refused %d datagram(s) from an address that has not opened this circuit, latest %s", n, ua)
	}
}

// Refused is how many datagrams were dropped for coming from an
// address that never opened the circuit.
func (c *Circuit) Refused() uint64 { return c.refused.Load() }

// notePeer adopts an address that has just proved itself.
func (c *Circuit) notePeer(ua *net.UDPAddr) {
	c.mu.Lock()
	// A first viewer arriving is not a replacement.  There is no
	// previous conversation behind it and so nothing to forget.
	replaced := c.peer != nil
	if replaced {
		// A different viewer, so the last one's handshake
		// means nothing to it.
		c.joined = false
	}
	c.peer = ua
	c.mu.Unlock()
	if replaced {
		c.forgetTheLastViewer()
	}
	c.logf("viewer: a viewer appeared at %s", ua)
}

// forgetTheLastViewer drops what belonged to the viewer that has gone,
// and nothing else.
//
// A viewer numbers its packets from 1, so the dispatcher forgets the
// sequence numbers it has seen, or a new viewer's UseCircuitCode and
// CompleteAgentMovement would be dropped as the last one's
// retransmissions.  The sender forgets what it was retransmitting, or a
// LogoutReply the last viewer never acknowledged would log the new one
// out.  The session and its own circuit are the daemon's, and what is
// queued for the viewer and c.pending describe the region, so they are
// kept.
//
// Called from notePeer, on the dispatch goroutine, which is the one
// goroutine Dispatcher.Forget is safe on.
// Why: doc/handover.md#a-second-viewer-on-the-same-circuit
func (c *Circuit) forgetTheLastViewer() {
	c.disp.Forget()
	// A sender that has stopped has nothing left in flight, so there is
	// nothing to report about being told too late.
	_ = c.send.Forget(context.Background())
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
		// already there; what the viewer needs is everything the
		// region said once, before it existed, and then the answer.
		c.record(FromViewer, p, Absorbed)
		c.describeRegion()
		c.sendMovementComplete()

	case "RegionHandshakeReply":
		// This session replied to the handshake when it arrived.
		c.record(FromViewer, p, Absorbed)

	case "LogoutRequest":
		// The one that matters most.  Forwarded, it would end the
		// grid session the moment somebody closed the viewer --
		// taking slrun, every attached client and the avatar
		// with it.
		c.record(FromViewer, p, Absorbed)
		c.sendLogoutReply()
		c.leave("the viewer logged out")

	case "StartPingCheck", "CompletePingCheck":
		// Circuit machinery, per circuit.  This side answers the
		// viewer's and the session answers the simulator's.
		c.record(FromViewer, p, Absorbed)
		if name == "StartPingCheck" {
			c.answerPing(p)
		}

	// ---- absorbed: a teleport out of this region ----
	//
	// Forwarded, one would move the session and the viewer would be
	// told none of it -- TeleportFinish is withheld from its event
	// queue -- so it would go on drawing a region the avatar has left.
	// Following it there is not built, so these are absorbed and the
	// person is told why (refuseTeleport).  StartLure is not among
	// them and is forwarded: offering somebody a teleport to where
	// this avatar stands moves this avatar nowhere.
	// Why: doc/handover.md#a-teleport-asked-for-at-the-viewer

	case "TeleportLocationRequest":
		// The map, a SLurl, and "teleport here" off the double-click
		// menu -- and the last of those is the one that must not be
		// refused.  A teleport within this region changes nothing
		// about the circuit: the same simulator answers it, with a
		// TeleportLocal that goes back to the viewer like any other
		// message, and refusing it would break something that is not
		// broken.  The Info block carries the destination's handle and
		// the session knows its own, so the two are told apart by
		// asking rather than by guessing.
		if c.withinThisRegion(p.Message) {
			c.forward(p)
			break
		}
		c.record(FromViewer, p, Absorbed)
		c.refuseTeleport()

	case "TeleportRequest":
		// The same thing addressed by region id rather than by handle.
		// Compared the same way and for the same reason: a spot in the
		// region we are standing in is not a move.
		if c.withinThisRegion(p.Message) {
			c.forward(p)
			break
		}
		c.record(FromViewer, p, Absorbed)
		c.refuseTeleport()

	case "TeleportLandmarkRequest":
		// A landmark, and -- with a null landmark id -- "teleport
		// home".  Neither says where it goes: the id is resolved by
		// the grid, so there is nothing here to compare against this
		// region and nothing to forward safely.  Home is somewhere
		// else by construction.
		c.record(FromViewer, p, Absorbed)
		c.refuseTeleport()

	case "TeleportLureRequest":
		// Accepting somebody's offer, which is the same move with
		// somebody else's finger on it.  slsh accepts lures and
		// follows them (sl.Session.AcceptLure); a viewer cannot,
		// for the reason above.
		c.record(FromViewer, p, Absorbed)
		c.refuseTeleport()

	case "TeleportCancel":
		// Absorbed without a word.  There is nothing of the viewer's
		// to cancel -- its requests never left this daemon -- and
		// forwarded it would cancel a teleport ANOTHER client asked
		// for, which is the one thing it must not do.  No alert,
		// because a cancel that cancels nothing is not news.
		c.record(FromViewer, p, Absorbed)

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

	// Before the send, so that whoever is told has heard of the request
	// before any answer to it can come back.
	c.mu.Lock()
	told := c.forwarded
	c.mu.Unlock()
	if told != nil {
		told(p.Message)
	}

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
// session's.  Only the admitted viewer's address gets here with a wrong
// claim -- admit refuses one from anywhere else -- so it is a loud log
// rather than a refusal: the login endpoint is what decides who may
// attach, and by here the answer is already yes.
//
// It says which of the two did not match, never what either should have
// been: with the agent id, which the log names everywhere, they are all
// UseCircuitCode needs to open a circuit as this avatar, and any sender
// can provoke a mismatch, so writing them out would copy the credentials
// into the log.  The claimed session id is cut to its first characters,
// enough to tell two claims apart; package redact's switch puts every
// value back for a debugging run.
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
	codeWrong := m.CircuitCode.Code != acct.CircuitCode
	sessionWrong := m.CircuitCode.SessionID != acct.SessionID
	if !codeWrong && !sessionWrong {
		return
	}
	if redact.Full() {
		c.logf("viewer: a viewer claimed circuit %d session %s, but this session is %d/%s",
			m.CircuitCode.Code, m.CircuitCode.SessionID, acct.CircuitCode, acct.SessionID)
		return
	}
	var wrong string
	switch {
	case codeWrong && sessionWrong:
		wrong = "the circuit code and the session id"
	case codeWrong:
		wrong = "the circuit code"
	default:
		wrong = "the session id"
	}
	c.logf("viewer: a viewer claimed session %s, and %s did not match this session's",
		redact.ID(m.CircuitCode.SessionID), wrong)
}

// withinThisRegion reports whether a teleport request names the region
// the avatar is already in.
//
// Anything else is treated as somewhere else, including a session that
// does not yet know its own handle or region id.  That is the safe way
// round: the only request worth forwarding is one positively known to
// stay here, and "I do not know where I am" is not that.
func (c *Circuit) withinThisRegion(m msg.Message) bool {
	a := c.session()
	if a == nil {
		return false
	}
	switch t := m.(type) {
	case *msg.TeleportLocationRequest:
		here := a.RegionHandle()
		return here != 0 && t.Info.RegionHandle == here
	case *msg.TeleportRequest:
		here, known := a.Region()
		return known && !here.ID.IsZero() && t.Info.RegionID == here.ID
	}
	return false
}

// refuseTeleport says why the teleport the person just asked for did
// not happen, and what to do instead.
//
// The wording is the only explanation there will be, so it says all
// three things: that nothing was sent, why, and the way that does work.
// "slsh tp" is that way -- it moves the session, and the daemon follows
// it -- and the viewer has to be attached again afterwards because
// replaying a new region to a viewer that believes it is in the old one
// is the part that is not built.
func (c *Circuit) refuseTeleport() {
	c.tell("slgod is holding this session, so the teleport was not sent: " +
		"a viewer cannot follow the avatar to another simulator yet. " +
		"Move with \"slsh tp\", then log this viewer out and in again.")
	c.logf("viewer: a teleport out of this region was refused; the session stays where it is")
}

// RegionChanged tells an attached viewer that the avatar is somewhere
// else now.
//
// Another client moves this session -- slsh tp, or a lure accepted from
// slsh waiting -- and in an ordinary session the viewer is the client
// that asked, so the protocol has nothing that says "you have been
// moved" to one that did not.  Firestorm, measured, decides within a
// second that it was sent to an invalid region and sends a
// LogoutRequest, which fromViewer absorbs; this alert arrives first,
// and names the region and what to do.  Replaying the new region to the
// viewer is "follow", which is not built.  A viewer that never joined
// is not told: a circuit exists from the first login whether anything
// attached or not.
// Why: doc/history/teleport.md#stage-6----a-viewer-attached-while-it-happens-done
func (c *Circuit) RegionChanged(name string) {
	if !c.Joined() {
		return
	}
	where := name
	if where == "" {
		where = "another region"
	}
	c.tell("The avatar has been teleported to " + where + ". " +
		"This viewer is still drawing the region it left; " +
		"log out and in again to follow it.")
	c.logf("viewer: the avatar moved to %s under an attached viewer; it was told to attach again", where)
}

// tell says something to the person, as an AgentAlertMessage with
// Modal set, which a viewer always draws.
//
// Not a line of chat: what is said here is about a control that did
// nothing or a window that is now a lie, and the person's next move
// depends on having read it.  Nearby chat can be closed, collapsed or
// scrolled past, its toasts turned off in the preferences, and a line in
// it reads as something somebody in the region said; a modal alert is
// the simulator addressing this avatar by id, in front of the world with
// a button on it.
//
// It may run on the grid session's dispatch goroutine, by way of
// RegionChanged.  It puts one message on the sender's buffered channel,
// served by a goroutine that does nothing but write UDP, which is as
// close to not blocking there as this side gets.
func (c *Circuit) tell(text string) {
	a := c.session()
	if a == nil {
		return
	}
	m := &msg.AgentAlertMessage{}
	m.AgentData.AgentID = a.Account.AgentID
	m.AlertData.Modal = true
	m.AlertData.Message = []byte(text + "\x00")
	c.toViewer(m, msg.FlagReliable)
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
	select {
	case c.joins <- struct{}{}:
	default:
	}
	c.logf("viewer: told the viewer it is in %s at %v",
		a.RegionName(), m.Data.Position)
}

// leave is what a viewer leaving does, whether it logged out or went
// silent: it is not attached any more, and what the region says has
// nobody to go to until the next one joins, which sets joined again.
func (c *Circuit) leave(how string) {
	c.mu.Lock()
	c.joined = false
	c.mu.Unlock()
	c.logf("viewer: %s; the session stays up", how)
}

// watch takes a joined viewer that has sent nothing for SilenceTimeout
// as gone, as a LogoutRequest would.  It watches from each join until
// that silence, on the watchdog the session keeps on its own circuits.
// Why: doc/handover.md#a-viewer-that-goes-silent
func (c *Circuit) watch(ctx context.Context) {
	idle := c.silence
	if idle <= 0 {
		idle = SilenceTimeout
	}
	heardAt := func() time.Time { return time.Unix(0, c.heard.Load()) }
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.joins:
		}
		agent.WatchSilence(ctx, idle, heardAt, func(since time.Duration) {
			if c.Joined() {
				c.leave(fmt.Sprintf("nothing heard from the viewer for %s, so it is taken as gone",
					since.Round(time.Second)))
			}
		})
	}
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
// the simulator sent, kept whole: what package agent decodes from it
// (regionFromHandshake) is fourteen fields of thirty odd, and the ones it
// drops include every terrain texture id, so a viewer given a
// reconstruction would render ground with nothing on it.
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
	// show it on: the messages a viewer exists to answer, which the
	// session keeps for it (agent.Offers).  Sent last here, after the
	// handshake and the land -- though still ahead of the
	// AgentMovementComplete that fromViewer sends once this returns --
	// because a viewer showing an invitation before it has drawn
	// anything is a dialogue over a grey screen.
	// Why: doc/handover.md#what-was-said-while-nobody-was-attached
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
// so in its log (process_avatar_appearance, llviewermessage.cpp:5410),
// and nothing ever asks again.  Meanwhile the avatars
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

	case "TeleportStart", "TeleportProgress":
		// Handed over, TeleportStart puts a viewer in the teleport
		// tunnel: the world torn down, a progress bar, and no way
		// out except TeleportFinish, TeleportLocal or
		// TeleportFailed.  TeleportFinish is withheld
		// (withheldEvents), so a viewer sent the start of a move
		// another client asked for -- slsh tp, or a lure accepted
		// somewhere else -- would sit in the tunnel over a teleport
		// it could not have stopped.  Its own teleports out of the
		// region never start: fromViewer refuses them.
		// TeleportProgress is the same message with a caption on it.
		//
		// The cost is the progress bar of the viewer's own teleport
		// within the region, which is forwarded, and it is small:
		// the simulator sends the TeleportLocal that ends it in the
		// same burst as the start.
		// Why: doc/history/teleport.md#stage-6----a-viewer-attached-while-it-happens-done
		c.record(FromSim, p, Absorbed)
		return

	case "TeleportFinish":
		// Belt and braces, and cheap.  The template marks this
		// UDPBlackListed, so on Agni it arrives on the event queue
		// and is withheld there; a grid that sent it on the circuit
		// instead would hand a viewer the address, the seed and the
		// invitation to open a connection to the real simulator with
		// this session's own ids.  That failure is bad enough to be
		// worth closing from both roads.
		c.record(FromSim, p, Absorbed)
		return

	case "CrossedRegion", "EnableSimulator":
		// The same address, offered for walking rather than for
		// teleporting.  Both are UDPBlackListed and both are withheld
		// from the event queue, with the reasoning on withheldEvents;
		// this arm is the other road, on the same terms as
		// TeleportFinish above.
		//
		// On Agni both arrive on the queue -- CrossedRegion only to
		// a session holding a child circuit to the region over the
		// border, EnableSimulator over and over for as long as its
		// offer goes untaken -- so what this arm covers is a grid
		// that puts them where the template says they no longer go.
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
