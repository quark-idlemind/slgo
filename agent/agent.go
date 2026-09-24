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
	// client gets a default with a sixty second timeout.  Either way
	// it is used through a copy that will not follow a redirect off
	// the simulator's hosts: see Agent.http.
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

	// parcels is the land the avatar is standing on and the layout of
	// the region round it, both of which arrive unasked.
	parcels parcels

	// appearance is how the avatars nearby look, kept for the same
	// reason.
	appearance Appearances

	// anims is what this avatar is animating, which is half of what
	// posture is; the other half is the parent in the object cache.
	// See posture.go.
	anims animations

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

	// arrived, when a move has installed one, is fired when the
	// avatar has arrived in the new region: when both its
	// RegionHandshake and its AgentMovementComplete have come, in
	// whichever order they were delivered.  See arrival.
	//
	// The four signals above are closed once and stay closed, which is
	// what Connect and WaitForRegionHandshake want: they ask whether
	// something has happened, and anyone asking later is answered at
	// once.  A move asks the other question -- whether it has happened
	// AGAIN -- and re-arming inRegion to answer it would take the
	// first answer away from everyone already holding it.  A signal
	// the move installs and removes is smaller and leaves Connect's
	// handshake exactly as it was.
	arrived atomic.Pointer[signal]

	// moveMu serializes moves; see moveTo.
	moveMu sync.Mutex

	// walker is the walk under way, if there is one, and what every
	// AgentUpdate holds down and which way it faces.  See walk.go; the
	// move above is to another simulator, and is not this.
	walker walker

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

	// holdNeighbours is whether this session takes up the offers of
	// the regions around it.  Options.Neighbours starts it and
	// SetNeighbours turns it over; it is atomic and deliberately not
	// under neighMu, which dropNeighbours takes.
	holdNeighbours atomic.Bool

	// neighbours are the circuits held to the regions around this
	// one, by grid handle, and refused are the offers turned down for
	// being past MaxNeighbours -- kept only so that one is logged
	// once rather than every time it is offered again.  Both are nil
	// while neighbours are off; see neighbour.go.
	neighMu    sync.Mutex
	neighbours map[uint64]*child
	refused    map[uint64]bool

	mu          sync.RWMutex
	friends     map[msg.UUID]*Friend
	look        Look
	regionName  string
	activeGroup msg.UUID
	groups      []Group
	regionFlags uint32

	// maturity is the preference the grid granted the last time this
	// session asked for one, and empty until it has.  See Maturity.
	maturity string

	// attached is the region whose store is held now, under mu.
	attached msg.UUID

	region regionState

	position msg.Vector3
	lookAt   msg.Vector3
	handle   uint64
	channel  string
	kicked   string

	// entering is the region a move is arriving in, from the moment
	// its circuit is opened until the avatar is there, under mu; nil
	// at every other time.  See arrival.
	entering *arrival
}

// arrival is a region being arrived in, put together out of the two
// messages that say so.
//
// The region's name comes in RegionHandshake and its handle and the
// avatar's place in it in AgentMovementComplete, and the session is not
// in the new region until it has both.  The simulator sends them in
// that order, and UDP does not deliver them in it: a trace on Agni
// caught the handshake, seq=1, arriving 985ms behind the movement,
// seq=2.  A session that took each as it came spent that second at the
// new position, with the new handle, under the old region's name -- so
// a teleport's read-back named the region it had left, and so did the
// notice that the region had changed.  In the ordinary order it was the
// other way about, the new name around the old position, for as long as
// the movement took to follow.
//
// So during a move neither is taken on its own.  Each is held here
// until the other comes, and then the name, the handle and the position
// become the session's together, under the one lock anything reading
// them takes.
type arrival struct {
	// named says the handshake has come, and name is what it called
	// the region.
	named bool
	name  string

	// moved is the movement, when it came first.
	moved *msg.AgentMovementComplete
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

	// Neighbours holds a circuit to each region around this one, so
	// that the avatar can walk over a border: a simulator will not
	// hand it over to a client that holds none.  See neighbour.go,
	// which is where the whole of it lives, and
	// doc/history/neighbours.md for what it measured.
	//
	// This is what the session STARTS as and not the whole truth:
	// SetNeighbours turns them over while the session is up, which is
	// what a person driving one avatar of several wants.  Ask
	// NeighboursOn rather than reading this back.
	//
	// Off by default.  Off, no offer is read and no socket is opened,
	// so a session that does not ask for this behaves as it did
	// before any of it existed.  It costs a socket and a share of the
	// traffic per neighbour -- four regions surround Pelmar Reach and
	// eight can surround one anywhere -- which a daemon acting only
	// where its avatar stands should not be made to pay.
	Neighbours bool

	// Log is where this session says the few things worth the
	// attention of whoever is running the daemon.  Nil is silence,
	// which is what a test wants; cmd/slgod passes log.Printf with
	// the profile's name on the front.
	//
	// It is not a trace and not an error channel: what belongs here
	// is what nothing else would ever say.  Today that is the child
	// circuits opening and closing, which a client can now list --
	// see Neighbours -- but only as they stand: a circuit that opened
	// and closed between two of a client's questions was never there
	// as far as the listing is concerned, and this is where it went.
	Log func(format string, v ...any)

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

// sendTap is the hook every message this session puts on the wire goes
// through, wrapping whatever the caller asked for.
//
// It is the one place every path out reaches: a direct caller, a client
// of the daemon sending raw bytes, and a viewer bridged onto this
// circuit all end up here.  What it is for is Objects.sent -- a change
// to an appearance makes what this session knows wrong, and nothing
// arrives afterwards to say so.
//
// The caller's tap is still called, after.  It is a trace, and a trace
// that stopped working because something else wanted the hook would be
// a bad trade.
func (a *Agent) sendTap(user msg.Handler) msg.SenderOption {
	return msg.WithSendTap(func(p *msg.Packet) {
		a.Objects().sent(p)
		if user != nil {
			user(p)
		}
	})
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
	// The option is the starting value of a flag rather than a
	// settled fact, so it is stored where the flag is read from.
	a.holdNeighbours.Store(opts.Neighbours)

	a.seedFriends(acct.Buddies)

	// Both are given the holder rather than the connection, which is
	// what lets a move change the connection without changing them.
	a.Send = msg.NewSender(a.sock, a.sendTap(opts.SendTap))
	// Dropping rather than blocking, and said out loud when it happens.
	//
	// What is on the other end of the receiver's channel is the dispatch
	// goroutine, which runs every handler for a packet before it takes
	// the next; anything slow in one of them stops the socket being read.
	// Blocking there does not save the packet -- the kernel discards it
	// instead, once its own buffer fills -- it only makes the loss
	// invisible, and stalls this session's acknowledgements behind the
	// same slow handler, so the simulator resends what it already sent.
	//
	// The caller's own options come after these, so anything it feels
	// strongly about it can still say.
	recv := append([]msg.ReceiverOption{
		msg.OnDrop(func(p *msg.Packet) {
			a.logf("dropped %s: the session is not keeping up with the region",
				p.ID)
		}),
		// Said as it climbs rather than counted for later.  A backlog
		// that is growing is something happening now, and how fast it
		// grows and what it grows during is what says why.
		msg.OnPeak(func(depth, size int) {
			a.logf("backlog %d of %d packets", depth, size)
		}),
	}, opts.Recv...)
	a.Recv = msg.NewReceiver(a.sock, recv...)

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

// logf says something to whoever is running the daemon, and nothing at
// all when nobody is listening.
func (a *Agent) logf(format string, v ...any) {
	if a.opts.Log == nil {
		return
	}
	a.opts.Log(format, v...)
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
	a.trackPosture()
	a.keepOffers()
	a.followCrossings()
	a.followNeighbours()

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

	// The region's parcel layout, which arrives with the terrain and
	// for the same reason is kept: four packets on arrival and none
	// after, whatever asks later.
	a.Disp.MustHandle("ParcelOverlay", func(p *msg.Packet) {
		if m, ok := p.Message.(*msg.ParcelOverlay); ok {
			a.parcels.noteOverlay(m)
		}
	}, msg.Inline())

	a.Disp.MustHandle("RegionHandshake", func(p *msg.Packet) {
		m := p.Message.(*msg.RegionHandshake)
		name := trimNul(m.RegionInfo.SimName)
		var moved *msg.AgentMovementComplete
		a.mu.Lock()
		if e := a.entering; e != nil {
			// A move's: the name waits for the movement, or the
			// movement that was waiting for it is taken below.
			e.named, e.name = true, name
			moved, e.moved = e.moved, nil
		} else {
			a.regionName = name
		}
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

		// The movement came first and has been waiting for this.  It is
		// taken after the store has been entered, so that what the
		// arrival tells anything above is about the region whose
		// objects are now the ones held.
		if moved != nil {
			a.arrive(moved)
		}
	}, msg.Inline())

	a.Disp.MustHandle("AgentMovementComplete", func(p *msg.Packet) {
		a.arrive(p.Message.(*msg.AgentMovementComplete))
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
		if a.entering != nil {
			// The new region's, and the avatar is not yet there as
			// far as this session says: the handle and the name are
			// still the region left's, and a position from here would
			// be the other region's place under their name.  The
			// arrival carries a position of its own, and an exact one.
			a.mu.Unlock()
			return
		}
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

// arrive takes an AgentMovementComplete: the avatar is in a region, at
// a position, and the session says so from now on.
//
// During a move it is taken only once the new region's handshake has
// come too, and until then it is held; see arrival for why, and the
// RegionHandshake handler for the other half, which calls this again
// with the movement it held.  Outside a move -- the first arrival of a
// session, or a second one in the region the avatar is already in --
// the name is whatever the handshake last recorded, which is this
// circuit's region's or, at login, nothing yet: never another region's.
func (a *Agent) arrive(m *msg.AgentMovementComplete) {
	a.mu.Lock()
	if e := a.entering; e != nil {
		if !e.named {
			e.moved = m
			a.mu.Unlock()
			return
		}
		a.regionName = e.name
		a.entering = nil
	}
	// The handle this session held until now, which is what says
	// whether the avatar has arrived somewhere it was not, and the name
	// of the region it has arrived in; both are read under the lock
	// that writes the place, so that the three cannot be of two regions.
	was, name := a.handle, a.regionName
	a.position = m.Data.Position
	a.lookAt = m.Data.LookAt
	a.handle = m.Data.RegionHandle
	a.channel = trimNul(m.SimData.ChannelVersion)
	a.mu.Unlock()
	a.setCenter(m.Data.Position)
	a.inRegion.fire()
	// A move is waiting for this one rather than for the first one
	// ever, which inRegion has already answered.
	if s := a.arrived.Load(); s != nil {
		s.fire()
	}
	a.regionChanged(was, m.Data.RegionHandle, name)
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
	if err := a.Send.SendReliable(ctx, a.useCircuitCode()); err != nil {
		return fmt.Errorf("agent: UseCircuitCode: %w", err)
	}
	return nil
}

// useCircuitCode is the message that opens a circuit, wherever it is
// being opened.
//
// Its whole content is this session's three ids, which is why the same
// one serves the region the avatar is in and every neighbour of it: see
// openNeighbour, the other caller, and note that what makes a circuit
// the root is CompleteAgentMovement rather than anything here.
func (a *Agent) useCircuitCode() *msg.UseCircuitCode {
	circuit := &msg.UseCircuitCode{}
	circuit.CircuitCode.Code = a.Account.CircuitCode
	circuit.CircuitCode.SessionID = a.Account.SessionID
	circuit.CircuitCode.ID = a.Account.AgentID
	return circuit
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

// Position is where the avatar is.
//
// Ordinarily that is what the simulator last said outright --
// AgentMovementComplete when the avatar arrived, and
// CoarseLocationUpdate as it moves, which is whole metres and arrives
// every few seconds.
//
// A seated avatar is the exception, and it is worth the special case.
// Sitting on something MOVES the avatar, up to about ten metres, and the
// coarse update saying where it ended up can be seconds behind: measured
// on Agni, a sit that carried the avatar three metres still read as the
// old position for about ten seconds afterwards.  Meanwhile the exact
// answer is already in hand, because the update that seats an avatar
// carries its position as an offset from the seat -- so a seated
// position is composed out of the seat's own placement rather than
// waited for.
//
// It falls back to the coarse answer whenever the seat cannot be
// resolved, which is the ordinary case for a seat nothing has described:
// stale by a few metres beats a confident zero.
func (a *Agent) Position() msg.Vector3 {
	a.mu.RLock()
	at := a.position
	a.mu.RUnlock()
	return a.seated(at)
}

// Here is where the avatar is and which region that is, read together.
//
// Three calls to Position, RegionHandle and RegionName are three
// moments, and an arrival that lands between them makes an answer out
// of two regions: the position of one under the name of the other.
// Anything that reports where the avatar is as one line should ask
// here, where the three are read under the one lock the arrival writes
// them under.
func (a *Agent) Here() (at msg.Vector3, handle uint64, region string) {
	a.mu.RLock()
	at, handle, region = a.position, a.handle, a.regionName
	a.mu.RUnlock()
	return a.seated(at), handle, region
}

// seated is Position's special case for an avatar sitting on something:
// the place composed out of the seat's, or at when that cannot be done.
func (a *Agent) seated(at msg.Vector3) msg.Vector3 {
	// An agent with no account is one nothing has logged in, which
	// happens in tests and in the moments before a login answers.  It
	// has no avatar in the store to be seated, and asking for one by a
	// zero id would find whatever else has never been described.
	if a.Account == nil {
		return at
	}
	store := a.Objects()
	own, ok := store.Get(a.Account.AgentID)
	if !ok || own.Parent == 0 {
		return at
	}
	if seated, _, ok := store.worldPlacement(own.Local); ok {
		return seated
	}
	return at
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
	// The children's goroutines are in the group below and the cancel
	// above has already told them to stop; what this adds is their
	// sockets, which nothing else would ever close.
	a.dropNeighbours("the session ended")
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
