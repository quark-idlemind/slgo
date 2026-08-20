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
	// A first viewer arriving is not a replacement.  There is no
	// previous conversation behind it and so nothing to forget.
	replaced := changed && c.peer != nil
	if changed {
		if replaced {
			// A different viewer, so the last one's handshake
			// means nothing to it.
			c.joined = false
		}
		c.peer = ua
	}
	c.mu.Unlock()
	if replaced {
		c.forgetTheLastViewer()
	}
	if changed {
		c.logf("viewer: a viewer appeared at %s", ua)
	}
}

// forgetTheLastViewer drops what belonged to the viewer that has gone,
// and nothing else.
//
// The sequence numbers are the whole reason this exists.  A viewer
// numbers its own packets from 1, so a second one opens with
// UseCircuitCode at 1 and CompleteAgentMovement at 2 -- numbers the
// first viewer used, and still in the dispatcher's ring of the last
// 4096.  Both were dropped as retransmissions before ever reaching
// fromViewer, which is the only thing that replays the region and
// answers the movement request, while the peer was noticed anyway
// because the tap that notices it runs ahead of the duplicate check.
// The daemon logged a viewer appearing and then said nothing, and the
// viewer sat at STATE_AGENT_WAIT with a grey world until slgod was
// restarted.
//
// Measured on Agni, from two daemon traces, which is why it looked
// intermittent rather than certain.  Where re-attaching failed the first
// viewer had sent 326 traced packets, so 1 and 2 were still in the ring.
// Where it succeeded the first viewer had sent 3777 traced packets and
// about fifty minutes of acknowledgements and pings the trace does not
// record, which is enough for the ring to have wrapped past them.
//
// Both halves run here because both are the departed viewer's.  Anything
// still awaiting acknowledgement was addressed to a socket that has
// closed, and the LogoutReply case is the one that bites: a viewer that
// quits sends LogoutRequest, and the reply it never acknowledged would
// be retransmitted to its replacement and log that one out on arrival.
//
// What is not touched is as deliberate.  This is the same circuit and
// the same grid session: the session's own circuit to the simulator, its
// sequence numbers, and everything it has learned about the region
// belong to the daemon rather than to whoever is looking at it, and the
// simulator messages already queued for the viewer are the region's
// current state, which the new viewer wants as much as the old one did.
// The appearances in c.pending are the same: describeRegion refills them
// from the session, and anything left over describes an avatar standing
// in this region either way.  The receiver has nothing of its own to
// forget -- it carries no state at all from one datagram to the next.
//
// Called from notePeer, which runs on the dispatch goroutine as the
// circuit's tap.  That is what makes Dispatcher.Forget safe: it writes
// fields no lock protects, on the one goroutine that owns them.
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

	// ---- absorbed: a teleport out of this region ----
	//
	// A viewer's teleport is refused rather than followed, and what
	// forwarding one would now do is not what doc/viewer-frontend.md
	// said until this stage.  That sentence -- the agent goes to a
	// simulator slgod is not connected to and the session ends -- was
	// written when nothing in the daemon read TeleportFinish, and the
	// daemon follows a teleport now.  So the session does not end, and
	// what happens instead is harder to see and worse to be in: the
	// request is granted, slgod moves the circuit to the new simulator,
	// and the viewer is told none of it, because TeleportFinish is
	// withheld from its event queue.  It goes on drawing a region the
	// avatar has left, pushing a camera around it that the new
	// simulator is deciding what to stream from, and taking object
	// updates whose local ids are the new region's numbering laid over
	// the old region's.  Nothing anywhere reports an error.  A session
	// that ends at least says so.
	//
	// Following properly is a second circuit on a second port and a
	// rewritten TeleportFinish -- doc/teleport.md's other option, which
	// is deliberately not built.  So these are absorbed the way
	// UseCircuitCode and LogoutRequest are, and the person is told:
	// a control that does nothing and says nothing is indistinguishable
	// from a viewer that has stopped working.
	//
	// StartLure is deliberately not among them and goes on being
	// forwarded.  Offering somebody else a teleport to where this
	// avatar is standing moves this avatar nowhere, and it is a thing a
	// viewer does far better than a shell does.

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
// slsh waiting -- and the viewer is no part of that conversation.  In
// an ordinary session the viewer is the client that asked, so the
// protocol has nothing that says "you have been moved" to one that did
// not.
//
// What a viewer does about that was measured on 2026-08-16 with
// Firestorm attached, and it is not what this said when it was written.
// It does not go on drawing the region left behind: within a second of
// the move it put up "You have been logged out of slgod.  You were sent
// to an invalid region." and sent a LogoutRequest.  That request is
// absorbed like any other (see fromViewer), so the grid session stayed
// up and the viewer dropped off it -- which is the outcome refusing was
// for, arrived at by the viewer's own judgement rather than by this
// telling it anything.
//
// The alert is still worth sending and is delivered before that
// happens: it names the region and says what to do, where the viewer's
// own message says only that something was invalid.  A person reading
// the two together knows what became of their avatar.
//
// Telling is all this does.  Replaying the new region to a viewer that
// believes it is in the old one is "follow", which is deferred, so the
// person gets the one thing that is true and can be acted on.  A viewer
// that never joined is not told: there is nobody at the other end, and
// a circuit exists from the first login whether anything attached or
// not.
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

// tell says something to the person, in the one place a viewer will
// always draw it.
//
// AgentAlertMessage with Modal set, rather than a line of chat.  What
// there is to say here is always about a control that did nothing or a
// window that is now a lie, and the person's next move depends on
// having read it.  Nearby chat is the wrong place for that: the window
// can be closed, collapsed or scrolled past, its toasts can be turned
// off in the preferences, and a line in it reads as something somebody
// in the region said.  A modal alert is the simulator addressing this
// avatar by id, and a viewer draws it in front of the world with a
// button on it.
//
// Sent from whichever goroutine noticed, including the grid session's
// dispatch goroutine by way of RegionChanged.  That is one message onto
// the sender's buffered channel, served by a goroutine that does
// nothing but write UDP, which is as close to not blocking there as
// this side gets.
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

	case "TeleportStart", "TeleportProgress":
		// The messages here about something the viewer did not ask
		// for.  Its own teleports out of this region are refused in
		// fromViewer, so a start on this circuit is always another
		// client's -- slsh tp, or a lure accepted somewhere else.
		//
		// Handed over, TeleportStart puts a viewer in the teleport
		// tunnel: the world torn down, a progress bar, and no way
		// out except TeleportFinish, TeleportLocal or
		// TeleportFailed.  TeleportFinish is withheld from the
		// event queue (see caps.go) precisely because it is the
		// dangerous one, so a viewer sent the start would sit in the
		// tunnel over a teleport it did not ask for and could not
		// have stopped.  TeleportProgress is the same message with a
		// caption on it and goes the same way.
		//
		// What absorbing them costs is the progress bar of a
		// WITHIN-region teleport, which is the viewer's own and is
		// forwarded -- and that cost was measured on Agni rather
		// than guessed at.  A local teleport IS announced with a
		// start: the simulator sent TeleportStart and TeleportLocal
		// twenty microseconds apart, in the same burst.  So the
		// viewer is put in the tunnel and taken straight out of it
		// again by the message it is really waiting for, and
		// absorbing the start costs it those twenty microseconds of
		// progress bar.
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
		// from the event queue with the reasoning in caps.go; this arm
		// is the other road, on the same terms as TeleportFinish above.
		//
		// CrossedRegion is the more speculative of the two, and
		// deliberately so: nobody has seen one on this grid, so this
		// costs nothing until a grid sends one and closes a hole the
		// moment one does.  EnableSimulator is the opposite -- it
		// arrives constantly, and on Agni it arrives on the queue, so
		// what this arm covers is a grid that puts it where the
		// template says it no longer goes.
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
