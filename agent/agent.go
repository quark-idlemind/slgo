package agent

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// An Agent is a live UDP circuit to one simulator at a time.
//
// At a time, because the circuit can be moved: a teleport takes the same
// session to another simulator, and the sender, the receiver and the
// dispatcher keep their identity across it so that everything holding
// one of them goes on working.  See moveTo.
//
// Connect performs the handshake the simulator expects -- UseCircuitCode
// to open the circuit, then CompleteAgentMovement to put the avatar in
// the region -- and answers RegionHandshake and StartPingCheck for the
// life of the session.  Everything else is yours: register handlers on
// Dispatcher before calling Connect, or with Handle afterwards.
type Agent struct {
	Account *Account

	Recv *msg.Receiver
	Send *msg.Sender
	Disp *msg.Dispatcher

	// sock is the connection those three were built over.  It was an
	// exported *net.UDPConn until the circuit had to be able to move:
	// a teleport dials another simulator and stores it here, and the
	// sender, the receiver and everyone holding them go on as they
	// were.  Nothing outside this package ever used the field.
	sock *socket

	// Inventory is this agent's folder tree.  It belongs to the
	// session: nothing here is package level, so one process can hold
	// as many sessions as it likes.
	Inventory *Inventory

	// caps are the capability URLs the region offered, behind a
	// pointer because a move replaces the whole set at once while
	// requests are being made through it.  Read them with Caps.
	caps atomic.Pointer[Caps]

	// seed is the capability the set above was fetched from, kept for
	// the same reason and behind the same kind of pointer.  Read it
	// with Seed.
	seed atomic.Pointer[string]

	// HTTP is used for capability and inventory requests.  A nil
	// client gets a default with a sixty second timeout.
	HTTP *http.Client

	// opts is how this session was asked for.  A move re-reads
	// Timeout, Caps, SkipCaps and OnEvent from it: the new region has
	// to be handshaken, asked for capabilities and polled on the same
	// terms as the one before it.
	opts Options

	// runCtx is the session's own lifetime, cancelled by cancel.  A
	// move spawns against it, so that what it starts ends when the
	// session does rather than when the move returns.
	runCtx context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	done     chan struct{}
	doneOnce sync.Once
	errOnce  sync.Once

	// err is why the session ended, and is atomic because done can be
	// closed without it.  Close closes done itself, and a goroutine
	// still on its way down can call fail afterwards -- so a reader
	// woken by done has no ordering edge to the write, and a plain
	// field is read and written at once.  errOnce makes it one store
	// ever, which is what atomic.Value needs.
	err atomic.Value

	// terrain is the land, kept because it cannot be asked for twice.
	terrain Terrain

	// appearance is how the avatars nearby look, kept for the same
	// reason.
	appearance Appearances

	// offers is what was said to the person while no viewer was there
	// to show it.
	offers Offers

	// asked remembers which local ids were recently asked about, so
	// that something moving on the edge of the draw distance is not
	// asked for several times a second.  See askAgain.
	askedMu sync.Mutex
	asked   map[uint32]time.Time

	// presenceHeldUntil is when this session may speak for the camera
	// again.  See DeferPresence.
	presenceHeldUntil time.Time

	// Signals for the handshake, each closed once.
	anyPacket signal
	inRegion  signal
	handshook signal
	loggedOut signal

	// arrived, when a move has installed one, is fired by the next
	// AgentMovementComplete to arrive.
	//
	// The four signals above are closed once and stay closed, which is
	// what Connect and WaitForRegionHandshake want: they ask whether
	// something has happened, and anyone asking later is answered at
	// once.  A move asks the other question -- whether it has happened
	// AGAIN -- and re-arming inRegion to answer it would take the
	// first answer away from everyone already holding it.  A signal
	// the move installs and removes is smaller and leaves Connect's
	// handshake exactly as it was.
	//
	// One is enough.  A simulator introduces the region before it
	// answers the movement request and both handlers are Inline, so a
	// move that has seen AgentMovementComplete has been through
	// RegionHandshake already: a second handshook would have nothing
	// left to wait for.
	arrived atomic.Pointer[signal]

	// moveMu serializes moves; see moveTo.
	moveMu sync.Mutex

	// forgetSeen asks the dispatch goroutine to forget the sequence
	// numbers it has seen.  msg.Dispatcher.Forget is safe on that
	// goroutine and nowhere else, and the tap is that goroutine.
	forgetSeen atomic.Bool

	eq eventQueue

	urlMu sync.Mutex
	urls  map[string]bool

	// lastPacket is when anything last arrived from the simulator,
	// as unix nanoseconds.  The watchdog reads it; the tap writes
	// it.
	lastPacket atomic.Int64

	// objects is what the region has said about itself.  It lives here
	// because a region describes itself once, when the avatar arrives,
	// and a client that attaches later is never told.
	//
	// It is a pointer that changes: crossing into another region swaps
	// in that region's store, which may be one other agents are already
	// keeping.  Read it through Objects, never directly, or a read
	// racing a crossing gets the wrong region's objects.
	objects atomic.Pointer[Objects]

	// regions hands out those stores.  A nil cache means this agent
	// keeps its own, which is what a single direct login wants.
	regions *Cache

	mu          sync.RWMutex
	friends     map[msg.UUID]*Friend
	look        Look
	regionName  string
	activeGroup msg.UUID
	groups      []Group
	regionFlags uint32

	// attached is the region whose store is held now, under mu.
	attached msg.UUID

	region regionState

	position msg.Vector3
	lookAt   msg.Vector3
	handle   uint64
	channel  string
	kicked   string
}

// signal is a channel closed at most once.
type signal struct {
	ch   chan struct{}
	once sync.Once
}

func newSignal() signal { return signal{ch: make(chan struct{})} }

func (s *signal) fire()                 { s.once.Do(func() { close(s.ch) }) }
func (s *signal) wait() <-chan struct{} { return s.ch }

// Options configure an Agent.
type Options struct {
	// Timeout bounds each step of the handshake.  Default 30s.
	Timeout time.Duration

	// Concurrency caps how many handlers run at once.  Default 8.
	Concurrency int

	// Tap, if set, sees every packet.  msg.DumpPacket makes a
	// reasonable capture.
	Tap msg.Handler

	// SendTap, if set, sees every message this session puts on the
	// wire, with the header it went out under.  Tap and SendTap
	// together are a whole trace; Tap alone records only what
	// arrived, which cannot answer whether something was ever sent.
	SendTap msg.Handler

	// Relay, if set, sees every decoded message once, after
	// duplicate suppression and before any handler.
	//
	// This is what something passing messages on wants, and Tap is
	// not: a tap runs ahead of the duplicate check, so a
	// retransmission reaches it twice and would be passed on as two
	// messages the far end cannot tell apart.  Keep it quick and do
	// not block in it -- it runs on the dispatch goroutine, so a
	// slow one stops this session reading anything at all.
	Relay msg.Handler

	// Regions, if set, is where this agent gets its object store: one
	// per region, shared with the other agents there.  Nil gives the
	// agent a store of its own, which is what one login on its own
	// wants.  A server hosting several avatars should pass the same
	// cache to all of them.
	Regions *Cache

	// OnUnhandled sees decoded messages nothing is registered for.
	// Worth setting: this is how a protocol change announces
	// itself.
	OnUnhandled msg.Handler

	// OnError sees packets that would not decode.
	OnError msg.Handler

	// Caps names the capabilities to ask the seed capability for.
	// Empty means DefaultCaps; SkipCaps skips the request.
	Caps     []string
	SkipCaps bool

	// HTTP is used for capability and inventory requests.
	HTTP *http.Client

	// Recv is passed through to the receiver.  A relay wants
	// msg.KeepBody so it can pass on a message it cannot decode.
	Recv []msg.ReceiverOption

	// OnEvent receives what arrives on the event queue.  Nil turns
	// the poll off, which is the right thing for a one-shot client
	// that does not care.
	OnEvent EventHandler

	// OnRegionChange is told that the avatar is in a different
	// region from the one it was in, with that region's name and
	// handle.  See regionChanged for when it fires and, as
	// importantly, when it does not.
	//
	// It is what lets something above this package throw away what
	// belongs to the region left behind, which is most of what a
	// client holds: local ids are the region's own numbering and an
	// object cache describes somewhere else.  This package does not
	// know what a client is and does not learn it here -- the
	// callback is the whole of what it says.
	//
	// It runs on the dispatch goroutine, like Relay, so keep it
	// quick and do not block in it: a slow one stops this session
	// reading anything at all.
	OnRegionChange RegionChangeHandler

	// Presence is how often AgentUpdate is sent.  Default one
	// second; a negative value stops it, which also stops the
	// simulator streaming any object data.
	Presence time.Duration

	// DrawDistance is the Far value in those updates.  It decides
	// how much the simulator sends, so it is worth setting low for
	// a client that does not care about objects.
	DrawDistance float32

	// Idle ends the session when nothing has arrived from the
	// simulator for this long.  Default 60s; a negative value
	// disables it.
	//
	// Something like this is not optional for a connection meant to
	// be left running.  A circuit that has quietly died looks
	// exactly like an idle one, and without a deadline the session
	// reports itself healthy forever.  The C client has the same
	// check at s.c:618, on a fifteen second ping deadline.
	Idle time.Duration
}

// Connect opens the circuit and completes the handshake.
func Connect(ctx context.Context, acct *Account, opts Options) (*Agent, error) {
	if acct == nil {
		return nil, fmt.Errorf("agent: Connect needs an account")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 8
	}

	conn, err := net.DialUDP("udp", nil, acct.SimAddr())
	if err != nil {
		return nil, fmt.Errorf("agent: dial %s: %w", acct.SimAddr(), err)
	}

	a := &Agent{
		Account:   acct,
		sock:      newSocket(conn),
		HTTP:      opts.HTTP,
		Inventory: newInventory(acct.InventoryRoot),
		opts:      opts,
		regions:   opts.Regions,
		done:      make(chan struct{}),
		anyPacket: newSignal(),
		inRegion:  newSignal(),
		handshook: newSignal(),
		loggedOut: newSignal(),
	}
	a.SetCaps(Caps{})
	a.setSeed(acct.SeedCapability)

	a.seedFriends(acct.Buddies)

	var sendOpts []msg.SenderOption
	if opts.SendTap != nil {
		sendOpts = append(sendOpts, msg.WithSendTap(opts.SendTap))
	}
	// Both are given the holder rather than the connection, which is
	// what lets a move change the connection without changing them.
	a.Send = msg.NewSender(a.sock, sendOpts...)
	a.Recv = msg.NewReceiver(a.sock, opts.Recv...)

	dopts := []msg.DispatcherOption{
		msg.WithSender(a.Send),
		msg.WithConcurrency(opts.Concurrency),
		msg.WithTap(func(p *msg.Packet) {
			// A move asks here because this runs on the dispatch
			// goroutine, ahead of duplicate suppression: whatever
			// packet carries this out, the new simulator's own
			// packets are all judged against an empty ring.
			if a.forgetSeen.CompareAndSwap(true, false) {
				a.Disp.Forget()
			}
			a.lastPacket.Store(time.Now().UnixNano())
			a.anyPacket.fire()
			if opts.Tap != nil {
				opts.Tap(p)
			}
		}),
	}
	if opts.Relay != nil {
		dopts = append(dopts, msg.WithRelay(opts.Relay))
	}
	if opts.OnUnhandled != nil {
		dopts = append(dopts, msg.OnUnhandled(opts.OnUnhandled))
	}
	if opts.OnError != nil {
		dopts = append(dopts, msg.OnError(opts.OnError))
	}
	a.Disp = msg.NewDispatcher(dopts...)
	a.register()

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	a.runCtx, a.cancel = runCtx, cancel

	a.lastPacket.Store(time.Now().UnixNano())
	if opts.Idle == 0 {
		opts.Idle = 60 * time.Second
	}
	if opts.Idle > 0 {
		a.spawn(func() error { a.watchdog(runCtx, opts.Idle); return nil })
	}

	a.spawn(func() error { return a.Send.Run(runCtx) })
	a.spawn(func() error { return a.Recv.Run(runCtx) })
	a.spawn(func() error { return a.Disp.Run(runCtx, a.Recv.C()) })

	if err := a.handshake(ctx, opts.Timeout); err != nil {
		a.Close()
		return nil, err
	}

	// The avatar has arrived, so there is a position to look from.
	a.SetLook(defaultLook(a.Position()))
	if opts.DrawDistance > 0 {
		l := a.Look()
		l.Far = opts.DrawDistance
		a.SetLook(l)
	}
	if opts.Presence == 0 {
		opts.Presence = time.Second
	}
	if opts.Presence > 0 {
		a.spawn(func() error { a.sendPresence(runCtx, opts.Presence); return nil })
		a.spawn(func() error { a.trimObjects(runCtx, TrimInterval); return nil })
	}

	// Capabilities are HTTP and have nothing to do with the
	// circuit, but almost everything above this layer needs them,
	// so they are fetched here rather than left for the caller to
	// remember.
	if !opts.SkipCaps && acct.SeedCapability != "" {
		caps, err := RequestCaps(ctx, acct.SeedCapability, opts.Caps, a.http())
		if err != nil {
			a.Close()
			return nil, err
		}
		a.SetCaps(caps)
	}

	// The queue needs the capability, so it starts after the
	// capabilities have been fetched rather than with the circuit.
	if opts.OnEvent != nil {
		a.startEventQueue(runCtx, opts.OnEvent)
	}
	return a, nil
}

// LastPacket is when anything last arrived from the simulator.
func (a *Agent) LastPacket() time.Time {
	return time.Unix(0, a.lastPacket.Load())
}

// Idle is how long the simulator has been silent.
func (a *Agent) Idle() time.Duration { return time.Since(a.LastPacket()) }

// watchdog ends the session when the simulator stops talking.
//
// A live simulator is never quiet for long: it pings every few seconds
// and sends time and location updates besides.  Silence means the
// circuit is gone, and saying so is the whole point -- a connection
// that has died without anyone noticing is worse than one that
// reported the failure.
func (a *Agent) watchdog(ctx context.Context, idle time.Duration) {
	tick := idle / 4
	if tick < time.Second {
		tick = time.Second
	}
	t := time.NewTicker(tick)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-a.done:
			return
		case <-t.C:
			if since := a.Idle(); since > idle {
				a.fail(fmt.Errorf(
					"agent: simulator silent for %s", since.Round(time.Second)))
				return
			}
		}
	}
}

func (a *Agent) spawn(fn func() error) {
	// Nothing is started for a session that is over.  Close waits on
	// this group, and an Add that lands after the wait has begun
	// panics; a move, which spawns the new region's poll long after
	// Connect returned, is the only caller that can be racing a Close
	// at all.  It narrows the window rather than closing it -- see
	// moveTo, which refuses outright -- and what is left is a goroutine
	// spawned into a session that is shutting down, which returns at
	// once because everything it waits on is already cancelled.
	select {
	case <-a.done:
		return
	default:
	}
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		if err := fn(); err != nil {
			a.fail(err)
		}
	}()
}

func (a *Agent) fail(err error) {
	if err != nil {
		a.errOnce.Do(func() { a.err.Store(err) })
	}
	a.doneOnce.Do(func() { close(a.done); a.leaveRegion() })
	if a.cancel != nil {
		a.cancel()
	}
}

// register installs the handlers the circuit itself needs.  All of them
// are Inline: they are trivial, and running them in order keeps the
// handshake deterministic.
func (a *Agent) register() {
	// A store of its own until the handshake says which region this
	// is.  Objects can be described before that arrives, and they are
	// this region's whatever it turns out to be called.
	a.objects.Store(newObjects())
	a.trackObjects()
	a.keepOffers()
	a.followCrossings()

	// AgentDataUpdate carries the active group, which decides whether a
	// parcel lets this avatar build. It is sent at login and when the
	// group changes, so nothing that attaches later can learn it.
	a.Disp.MustHandle("AgentDataUpdate", func(p *msg.Packet) {
		m := p.Message.(*msg.AgentDataUpdate)
		a.mu.Lock()
		a.activeGroup = m.AgentData.ActiveGroupID
		a.mu.Unlock()
	}, msg.Inline())

	// AgentGroupDataUpdate carries every group the avatar has joined.
	//
	// This, and not the login response, is where the membership list
	// comes from: login answers only what its options ask for, and
	// "groups" is not among the ones this server honours -- so asking
	// there gets a missing field, which reads exactly like belonging to
	// none. The simulator volunteers the real list moments after the
	// handshake and again whenever it changes.
	a.Disp.MustHandle("AgentGroupDataUpdate", func(p *msg.Packet) {
		m := p.Message.(*msg.AgentGroupDataUpdate)
		gs := make([]Group, 0, len(m.GroupData))
		for _, g := range m.GroupData {
			gs = append(gs, Group{
				ID:     g.GroupID,
				Name:   trimNulBytes(g.GroupName),
				Powers: g.GroupPowers,
			})
		}
		a.mu.Lock()
		a.groups = gs
		a.mu.Unlock()
	}, msg.Inline())

	a.Disp.MustHandle("StartPingCheck", func(p *msg.Packet) {
		m := p.Message.(*msg.StartPingCheck)
		reply := &msg.CompletePingCheck{}
		reply.PingID.PingID = m.PingID.PingID
		_ = a.Send.Send(context.Background(), reply)
	}, msg.Inline())

	// Land, kept from the first packet.  This is registered whether or
	// not anything will ever want it, because by the time something
	// does it is far too late: a region sends its heightmap in the
	// first seconds after the avatar arrives and will not send it
	// again for the asking.  The cost of always keeping it is about
	// forty kilobytes.
	a.Disp.MustHandle("LayerData", func(p *msg.Packet) {
		if m, ok := p.Message.(*msg.LayerData); ok {
			a.terrain.note(m)
		}
	}, msg.Inline())

	// And how everybody looks, kept for the same reason and at about
	// the same cost: a few hundred bytes an avatar, for as long as the
	// session can see them.
	a.Disp.MustHandle("AvatarAppearance", func(p *msg.Packet) {
		if m, ok := p.Message.(*msg.AvatarAppearance); ok {
			a.appearance.note(m)
		}
	}, msg.Inline())

	a.Disp.MustHandle("RegionHandshake", func(p *msg.Packet) {
		m := p.Message.(*msg.RegionHandshake)
		a.mu.Lock()
		a.regionName = trimNul(m.RegionInfo.SimName)
		a.regionFlags = m.RegionInfo.RegionFlags
		a.mu.Unlock()

		r := regionFromHandshake(m)
		a.setHandshake(m)
		a.setRegion(r)
		// Whichever region this is, its objects are kept apart from
		// the last one's.  What tells anything ABOVE this package
		// that the region changed is fired from
		// AgentMovementComplete rather than here, because this
		// message does not carry the handle; see regionChanged.
		a.enterRegion(r.ID)

		reply := &msg.RegionHandshakeReply{}
		reply.AgentData.AgentID = a.Account.AgentID
		reply.AgentData.SessionID = a.Account.SessionID
		_ = a.Send.SendReliable(context.Background(), reply)
		a.handshook.fire()
	}, msg.Inline())

	a.Disp.MustHandle("AgentMovementComplete", func(p *msg.Packet) {
		m := p.Message.(*msg.AgentMovementComplete)
		a.mu.Lock()
		// The handle this session held until now, which is what
		// says whether the avatar has arrived somewhere it was not,
		// and the name the handshake just recorded, which is the
		// new region's; both are read under the one lock so that
		// the pair cannot be half of each region.
		was, name := a.handle, a.regionName
		a.position = m.Data.Position
		a.lookAt = m.Data.LookAt
		a.handle = m.Data.RegionHandle
		a.channel = trimNul(m.SimData.ChannelVersion)
		a.mu.Unlock()
		a.setCenter(m.Data.Position)
		a.inRegion.fire()
		// A move is waiting for this one rather than for the first
		// one ever, which inRegion has already answered.
		if s := a.arrived.Load(); s != nil {
			s.fire()
		}
		a.regionChanged(was, m.Data.RegionHandle, name)
	}, msg.Inline())

	// Keep the camera on the avatar.  AgentUpdate is what puts a
	// session in the simulator's interest list, and the interest list
	// is worked out from the camera rather than from where the avatar
	// actually is.  Leave the camera behind and the simulator stops
	// describing everything around the avatar -- including the
	// avatar's own attachments -- while cheerfully reporting that the
	// teleport succeeded.
	a.Disp.MustHandle("TeleportLocal", func(p *msg.Packet) {
		m := p.Message.(*msg.TeleportLocal)
		a.mu.Lock()
		a.position = m.Info.Position
		a.lookAt = m.Info.LookAt
		a.mu.Unlock()
		a.setCenter(m.Info.Position)
	}, msg.Inline())

	// coarseTooHigh is the height byte at its ceiling.  A coarse
	// location counts four metres to the step, so 255 is 1020 and is
	// also everything above it.
	const coarseTooHigh = 255

	// CoarseLocationUpdate is the only thing that keeps arriving as an
	// avatar walks, so it is what stops the camera drifting away from
	// one that moved without teleporting.  It is coarse -- whole
	// metres, and four of them vertically -- which is ample for
	// deciding what is nearby.
	a.Disp.MustHandle("CoarseLocationUpdate", func(p *msg.Packet) {
		m := p.Message.(*msg.CoarseLocationUpdate)
		i := int(m.Index.You)
		if i < 0 || i >= len(m.Location) {
			return
		}
		l := m.Location[i]

		// The height is one byte of four metre steps, so it stops at
		// 1020 and says nothing at all about an avatar above that: 255
		// means "higher than this can say", not "at 1020".  Taking it
		// literally puts the camera a kilometre below an avatar on a
		// skybox, and everything the region then describes is judged
		// against a place the avatar is not -- the objects around it,
		// and the other avatars standing beside it, arrive already out
		// of range and are dropped.  Nothing describes them twice, so
		// the session never recovers.
		//
		// Measured: three avatars at about 2001m were all reported at
		// exactly 1020, and none of them could see any of the others,
		// nor its own avatar.
		//
		// So a saturated height is no height.  The last one from a
		// message that carries it in full -- AgentMovementComplete, or
		// a teleport -- is kept instead, which is where the avatar was
		// when something last said properly.
		a.mu.Lock()
		at := msg.Vector3{X: float32(l.X), Y: float32(l.Y), Z: float32(l.Z) * 4}
		if l.Z == coarseTooHigh {
			at.Z = a.position.Z
		}
		a.position = at
		a.mu.Unlock()
		a.setCenter(at)
	}, msg.Inline())

	// Who is logged in arrives as one burst a few seconds after the
	// handshake and then only as people come and go, so a session that
	// does not keep it can never be told again.  See friends.go.
	a.Disp.MustHandle("OnlineNotification", func(p *msg.Packet) {
		m := p.Message.(*msg.OnlineNotification)
		ids := make([]msg.UUID, 0, len(m.AgentBlock))
		for _, b := range m.AgentBlock {
			ids = append(ids, b.AgentID)
		}
		a.setOnline(ids, true)
	}, msg.Inline())

	a.Disp.MustHandle("OfflineNotification", func(p *msg.Packet) {
		m := p.Message.(*msg.OfflineNotification)
		ids := make([]msg.UUID, 0, len(m.AgentBlock))
		for _, b := range m.AgentBlock {
			ids = append(ids, b.AgentID)
		}
		a.setOnline(ids, false)
	}, msg.Inline())

	a.Disp.MustHandle("LogoutReply", func(p *msg.Packet) {
		a.loggedOut.fire()
	}, msg.Inline())

	a.Disp.MustHandle("KickUser", func(p *msg.Packet) {
		m := p.Message.(*msg.KickUser)
		reason := trimNul(m.UserInfo.Reason)
		a.mu.Lock()
		a.kicked = reason
		a.mu.Unlock()
		a.fail(&Kicked{Reason: reason})
	}, msg.Inline())
}

func (a *Agent) handshake(ctx context.Context, timeout time.Duration) error {
	if err := a.sendUseCircuitCode(ctx); err != nil {
		return err
	}
	if err := a.await(ctx, a.anyPacket.wait(), timeout, "circuit to come up"); err != nil {
		return err
	}
	if err := a.sendCompleteAgentMovement(ctx); err != nil {
		return err
	}
	return a.await(ctx, a.inRegion.wait(), timeout, "AgentMovementComplete")
}

// sendUseCircuitCode opens the circuit.  The simulator does not answer
// it with anything in particular, so the circuit is up once anything at
// all comes back.
//
// The same circuit code opens the circuit at every simulator this
// session ever talks to, which is why a teleport is not a relog: see
// moveTo, the other caller.
func (a *Agent) sendUseCircuitCode(ctx context.Context) error {
	circuit := &msg.UseCircuitCode{}
	circuit.CircuitCode.Code = a.Account.CircuitCode
	circuit.CircuitCode.SessionID = a.Account.SessionID
	circuit.CircuitCode.ID = a.Account.AgentID
	if err := a.Send.SendReliable(ctx, circuit); err != nil {
		return fmt.Errorf("agent: UseCircuitCode: %w", err)
	}
	return nil
}

// sendCompleteAgentMovement puts the avatar in the region, and is
// answered with AgentMovementComplete.
func (a *Agent) sendCompleteAgentMovement(ctx context.Context) error {
	move := &msg.CompleteAgentMovement{}
	move.AgentData.AgentID = a.Account.AgentID
	move.AgentData.SessionID = a.Account.SessionID
	move.AgentData.CircuitCode = a.Account.CircuitCode
	if err := a.Send.SendReliable(ctx, move); err != nil {
		return fmt.Errorf("agent: CompleteAgentMovement: %w", err)
	}
	return nil
}

func (a *Agent) await(ctx context.Context, ch <-chan struct{}, timeout time.Duration, what string) error {
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-ch:
		return nil
	case <-a.done:
		if err := a.Err(); err != nil {
			return err
		}
		return fmt.Errorf("agent: session ended waiting for %s", what)
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return fmt.Errorf("agent: timed out after %s waiting for %s", timeout, what)
	}
}

// Handle registers a handler, by message name, for the life of the
// session.
func (a *Agent) Handle(name string, fn msg.Handler, opts ...msg.HandlerOption) error {
	return a.Disp.Handle(name, fn, opts...)
}

// Done is closed when the session ends, however it ends.
func (a *Agent) Done() <-chan struct{} { return a.done }

// Err reports why the session ended, or nil for a clean shutdown.
//
// Safe to read at any time, including the moment Done fires: a session
// closed from outside rather than failed can still be setting this as
// the caller reads it.
func (a *Agent) Err() error {
	if v := a.err.Load(); v != nil {
		return v.(error)
	}
	return nil
}

// RegionName is the simulator'a name, once RegionHandshake has arrived.
// Group is one of the avatar's memberships.
type Group struct {
	ID     msg.UUID
	Name   string
	Powers uint64
}

// Groups is every group the avatar has joined.
//
// Empty until the simulator sends the list, which is shortly after the
// handshake -- so an empty answer immediately after login means "not
// told yet" rather than "none", and callers that must know should wait
// with WaitGroups.
func (a *Agent) Groups() []Group {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Group(nil), a.groups...)
}

// WaitGroups waits for the membership list to arrive.
//
// Belonging to no groups is indistinguishable from not having been told
// yet, so this returns whatever it has when the time runs out rather
// than failing: no groups is a perfectly ordinary state, and refusing
// to proceed on account of it would be wrong.
func (a *Agent) WaitGroups(ctx context.Context, timeout time.Duration) []Group {
	deadline := time.Now().Add(timeout)
	for {
		if gs := a.Groups(); len(gs) > 0 {
			return gs
		}
		if time.Now().After(deadline) {
			return nil
		}
		t := time.NewTimer(100 * time.Millisecond)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return a.Groups()
		}
	}
}

// ActiveGroup is the group the avatar is acting as, or zero for none.
func (a *Agent) ActiveGroup() msg.UUID {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.activeGroup
}

func (a *Agent) RegionName() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.regionName
}

// Position is where the avatar arrived.
func (a *Agent) Position() msg.Vector3 {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.position
}

// ChannelVersion is the simulator'a build string.
func (a *Agent) ChannelVersion() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.channel
}

// RegionHandle identifies the region on the grid.
func (a *Agent) RegionHandle() uint64 {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.handle
}

// WaitForRegionHandshake blocks until the simulator has introduced the
// region, which usually happens moments after Connect returns.
func (a *Agent) WaitForRegionHandshake(ctx context.Context, timeout time.Duration) error {
	return a.await(ctx, a.handshook.wait(), timeout, "RegionHandshake")
}

// Logout asks the simulator to end the session and waits for its reply
// before shutting down.  A simulator that never answers is not a reason
// to hang: the wait is bounded and Logout tears down either way.
func (a *Agent) Logout(ctx context.Context, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	out := &msg.LogoutRequest{}
	out.AgentData.AgentID = a.Account.AgentID
	out.AgentData.SessionID = a.Account.SessionID

	err := a.Send.SendReliable(ctx, out)
	if err == nil {
		err = a.await(ctx, a.loggedOut.wait(), timeout, "LogoutReply")
	}
	a.Close()
	return err
}

// Close stops the session'a goroutines and the socket without telling
// the simulator anything.
func (a *Agent) Close() {
	a.doneOnce.Do(func() { close(a.done); a.leaveRegion() })
	if a.cancel != nil {
		a.cancel()
	}
	a.sock.Close()
	a.wg.Wait()
}

// trimNul drops the terminator the protocol puts on its strings.
func trimNul(b []byte) string {
	if n := len(b); n > 0 && b[n-1] == 0 {
		b = b[:n-1]
	}
	return string(b)
}

// trimNulBytes makes a Go string of a wire string, which is nul ended.
func trimNulBytes(b []byte) string {
	if n := len(b); n > 0 && b[n-1] == 0 {
		b = b[:n-1]
	}
	return string(b)
}
