// Package server holds grid connections and relays them to attached
// clients.
//
// The division is that the server owns whatever breaks if a client
// restarts, and nothing else:
//
//   - the UDP circuit, its sequence numbers, retransmission queue,
//     acknowledgements, zero coding and duplicate window
//   - answering StartPingCheck and RegionHandshake, without which the
//     simulator drops the circuit
//   - the login exchange and the capability URLs it produces
//   - making capability requests on a client's behalf
//
// It does not decode message bodies, hold an inventory, understand
// chat, or know what a script is.  A message is relayed as its number
// and its undecoded bytes, so a client can handle a message type the
// server has never heard of, and can be restarted as often as you like
// without touching the grid session.  The one body it reads is a
// payment a client sends, which it checks against the profile's rules
// before it goes; see pay.go.
//
// With no client attached, inbound messages are acknowledged and
// dropped -- all but the offers that wait on a person, which are kept
// until somebody deals with them, because a client attaching later is
// otherwise never told they came.  See offers.go.
package server

import (
	"context"
	"errors"
	"fmt"
	"github.com/quark-idlemind/slgo/internal/auth"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/internal/pay"
	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// Server hosts any number of grid connections and serves clients.
type Server struct {
	// Embedding the generated stub means a service method added to
	// the proto compiles here as unimplemented rather than breaking
	// the build, which is the point of a base that does not change
	// often.
	pb.UnimplementedGridServer

	mu     sync.RWMutex
	agents map[string]*Hosted

	// ranked counts sessions as they come up, so that the default is
	// the one that has been hosted longest.  See Default.
	ranked uint64

	// profiles and loginFor are how the server learns what it could
	// start and how; see state.go.  Both nil means it knows only what
	// it was given.
	profiles Profiles
	loginFor LoginFor

	// seats is where a session's seat is remembered across a login;
	// see seat.go.  Nil remembers nothing, which is the old behaviour.
	seats Seats

	// payLedger is where each profile's record of what it has paid is
	// kept; see pay.go.  Nil keeps it in memory.
	payLedger PayLedger

	// starts covers starting agents on demand: what did not work, and
	// what is already being tried.
	starts agentState

	// slots is the shared objects, handed out a number at a time and
	// across avatars.  Started on first use; see slots.go.
	slots *slotPool

	// regions is one object store per region, shared by every avatar
	// hosted here.  What a region says about its objects is true for
	// all of them, so keeping a copy each would be three answers to
	// the same question -- see agent.Cache.
	regions *agent.Cache

	// ctx is the daemon's own lifetime.  A session started on request
	// outlives the request: cancelling the call that asked for it must
	// not take the avatar back out of the world.
	ctx context.Context

	// log and onStart are what the daemon does with a session it did
	// not start itself -- say so, and settle its group -- so that one
	// started on demand is set up exactly like one named at startup.
	log     func(string, ...any)
	onStart func(*Hosted)

	clients atomic.Int64

	// auth is nil when the server runs without authentication, which is
	// only reasonable bound to loopback.
	auth *auth.Server

	// throttle slows the answers to an address that keeps getting the
	// proof wrong.  Made with auth, and nil exactly when auth is.
	throttle *auth.Throttle

	// viewer is slgod's login endpoint for real viewers, which the
	// daemon owns and this package only reports on.  Nil -- the
	// ordinary case -- means none is served; see viewer.go.
	viewer Viewer
}

// SetAuth turns authentication on. Every method but Login is then
// refused on a connection that has not proved it knows the secret.
func (s *Server) SetAuth(a *auth.Server) {
	s.auth = a
	s.throttle = nil
	if a != nil {
		s.throttle = auth.NewThrottle()
	}
}

// Hosted is one grid connection and the clients watching it.
//
// The connection behind it is replaced when it has to be re-established,
// so it is reached through Agent() rather than being a field.  Clients
// keep their streams and their subscriptions across that; only the
// circuit underneath changes.  A Hosted that is finished with for good
// -- stopped deliberately, or no longer held by the server -- ends them
// instead; see ended.
type Hosted struct {
	Name string

	login agent.Login
	opts  agent.Options

	mu      sync.RWMutex
	agent   *agent.Agent
	clients map[*Client]bool

	// locks is exclusive use of named things, held for as long as the
	// client holding one keeps its stream.  See lock.go.
	locks *locks

	// group is the group to act as, reapplied after every reconnect.
	// See group.go.  Guarded by mu.
	group msg.UUID

	// homing cancels the loop that is trying to get this avatar home,
	// and is nil when nothing is trying.  homeAnswers is where the grid's
	// answer to its request is delivered, and is nil except while one
	// attempt is waiting for one.  Both guarded by mu.  See home.go.
	// homingID says which run of the loop homing belongs to, so that a
	// loop finishing clears its own cancel and not a later one's.
	homing      context.CancelFunc
	homingID    uint64
	homeAnswers chan homeAnswer

	// homeTried is what the grid has said about getting this avatar
	// home, kept across reconnects.  Guarded by mu.  See home.go.
	homeTried homeTried

	// viewerOn reports whether the daemon's viewer endpoint says a
	// viewer is on this session.  Nil is false.  See home.go.
	viewerOn func() bool

	// seats is where this avatar's seat is remembered, and seating
	// cancels the loop that restores and watches it.  Both nil when
	// nothing is remembering.  See seat.go.
	seats   Seats
	seating context.CancelFunc

	// sitRefusals counts the region's "not in the same region" answers
	// to a sit, which is how a seat that is gone is told from one that
	// is slow.  See seat.go.
	sitRefusals atomic.Uint64

	// rank is the order this session came up in, lowest first.  It is
	// what makes the default deterministic; see Server.Default.
	rank uint64

	// pay checks what this avatar's clients pay against its profile's
	// rules, and keeps what they have paid.  Guarded by mu, and made on
	// first use for a Hosted built by hand.  See pay.go.
	pay *pay.Gate

	// self is the avatar's id, known from the login, before the circuit
	// is: the grid can answer a payment as soon as it is up, when Agent
	// still says none.  Set when the Hosted is made and never changed.
	self msg.UUID

	// offers is what has been offered to this avatar and not yet dealt
	// with, kept whether or not anybody is attached.  It belongs to the
	// Hosted rather than to the agent under it, so a session that is
	// re-established keeps it.  See offers.go.
	offers *offerLog

	// Log is where anything worth a person's attention goes.  Nil is
	// silence, which is what a test wants.  StartAgent gives it what
	// SetLog or SetBase said, before the circuit is up: handlers read it
	// from then on, so it is not assigned afterwards.
	// Why: doc/money.md#a-log-that-is-there-before-the-first-reply
	Log func(format string, v ...any)

	// over is closed once, when this Hosted will carry no session
	// again, and removedWhy is the reason given by Remove, for a
	// session that Down has nothing to say about.  Both guarded by mu,
	// and over is made on first use; see ended and end.
	over       chan struct{}
	removedWhy string

	stopped  atomic.Bool
	attempts atomic.Uint64
	reconns  atomic.Uint64
	relayed  atomic.Uint64
	echoed   atomic.Uint64
	dropped  atomic.Uint64
}

// errNoSession is a request made of a connection that is not up.
var errNoSession = errors.New("server: no session")

// logf says something to whoever is running the daemon.  Silent when
// nobody is listening, which is what a test wants.
func (h *Hosted) logf(format string, v ...any) {
	if h.Log == nil {
		return
	}
	h.Log(h.Name+": "+format, v...)
}

// Agent is the current grid connection.  It changes when the session
// has to be re-established.
func (h *Hosted) Agent() *agent.Agent {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.agent
}

func (h *Hosted) setAgent(a *agent.Agent) {
	h.mu.Lock()
	h.agent = a
	h.mu.Unlock()
}

// New makes an empty server.
func New() *Server {
	return &Server{
		agents:  map[string]*Hosted{},
		regions: agent.NewCache(),
		ctx:     context.Background(),
	}
}

// SetLog says where a session started from now on logs, from the moment
// it exists.  A session's Log is read by handlers that run as soon as
// its circuit is up, so it is given at the start and never assigned
// afterwards.  SetBase sets the same thing; this is for a caller that
// starts sessions before it has the rest to give.
func (s *Server) SetLog(log func(string, ...any)) {
	s.mu.Lock()
	s.log = log
	s.mu.Unlock()
}

// logger is what a session started now is given to log with.
func (s *Server) logger() func(string, ...any) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.log
}

// SetBase gives the server the lifetime its sessions should have, and
// what to do with one it starts itself.
//
// Without this a session started on demand would take the lifetime of
// the request that asked for it, and hanging up would log the avatar
// out.
func (s *Server) SetBase(ctx context.Context, log func(string, ...any), onStart func(*Hosted)) {
	s.mu.Lock()
	s.ctx, s.log, s.onStart = ctx, log, onStart
	s.mu.Unlock()
}

// base is the lifetime a new session gets.
func (s *Server) base() context.Context {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.ctx == nil {
		return context.Background()
	}
	return s.ctx
}

// StartAgent logs in and brings up a circuit, then keeps it.  The name
// is how clients ask for it.
//
// Named for what it does rather than Host, which is the RPC's name: the
// wire contract owns that word.
func (s *Server) StartAgent(ctx context.Context, name string, login agent.Login, opts agent.Options) (*Hosted, error) {
	s.mu.Lock()
	if _, dup := s.agents[name]; dup {
		s.mu.Unlock()
		return nil, fmt.Errorf("server: %q is already hosted", name)
	}
	// Reserve the name so two Hosts cannot race on it.
	s.agents[name] = nil
	s.mu.Unlock()

	undo := func() {
		s.mu.Lock()
		if s.agents[name] == nil {
			delete(s.agents, name)
		}
		s.mu.Unlock()
	}

	acct, err := login.Do(ctx)
	if err != nil {
		undo()
		return nil, err
	}

	h := &Hosted{Name: name, login: login, clients: map[*Client]bool{}, seats: s.Seats(),
		offers: newOfferLog(time.Now()), viewerOn: func() bool { return s.viewerAttached(name) },
		pay: s.payGateFor(name, login.Pay), Log: s.logger(), self: acct.AgentID}

	// Keeping the undecoded body is what lets the relay pass on a
	// message it does not understand.
	opts.Recv = append(opts.Recv, msg.KeepBody())
	// The relay needs the tap, and so does anything the caller wanted
	// it for -- tracing, most of it.  Assigning here rather than
	// chaining would drop the caller's hook without saying so, which
	// is the sort of silence that gets diagnosed as "the trace does
	// not work".
	if caller := opts.Tap; caller != nil {
		opts.Tap = func(p *msg.Packet) {
			caller(p)
			h.relay(p)
		}
	} else {
		opts.Tap = func(p *msg.Packet) { h.relay(p) }
	}
	// Every avatar here shares one store per region.
	opts.Regions = s.regions
	// Chained for the same reason the tap is: a caller that wanted
	// the events too -- slgod, feeding a viewer's own queue -- would
	// otherwise have them taken away without a word.
	if caller := opts.OnEvent; caller != nil {
		opts.OnEvent = func(name string, body []byte) {
			caller(name, body)
			h.relayEvent(name, body)
		}
	} else {
		opts.OnEvent = func(name string, body []byte) { h.relayEvent(name, body) }
	}
	// The avatar being somewhere else is news for every client, and it
	// is the daemon that finds out: the grid announces a teleport or a
	// crossing to the session, not to whoever is attached to it.
	// Chained like the two above, for their reason.
	if caller := opts.OnRegionChange; caller != nil {
		opts.OnRegionChange = func(region string, handle uint64, teleport uint32) {
			caller(region, handle, teleport)
			h.noteRegion(region, handle, teleport)
		}
	} else {
		opts.OnRegionChange = func(region string, handle uint64, teleport uint32) { h.noteRegion(region, handle, teleport) }
	}
	// What this avatar sends, for the answers to offers the daemon is
	// keeping that nobody announced first.  Chained for the reason the
	// others are: slgod's trace wants it too.  See offers.go.
	if caller := opts.SendTap; caller != nil {
		opts.SendTap = func(p *msg.Packet) {
			caller(p)
			h.noteSent(p)
			h.noteStandSent(p)
		}
	} else {
		opts.SendTap = func(p *msg.Packet) { h.noteSent(p); h.noteStandSent(p) }
	}
	// What the grid says of a payment, for the log.  Chained for the
	// others' reason.  See pay.go.
	if caller := opts.OnMoney; caller != nil {
		opts.OnMoney = func(m *agent.Money) {
			caller(m)
			h.noteMoney(m)
		}
	} else {
		opts.OnMoney = h.noteMoney
	}
	h.opts = opts

	a, err := agent.Connect(ctx, acct, opts)
	if err != nil {
		undo()
		return nil, err
	}
	h.setAgent(a)

	s.mu.Lock()
	s.ranked++
	h.rank = s.ranked
	s.agents[name] = h
	sp := s.slots
	s.mu.Unlock()

	// A request already waiting for this avatar's places hears of them.
	if sp != nil {
		sp.sync()
	}

	go h.supervise(ctx)
	// A profile that asked to start at home may not have got there: the
	// login server puts the avatar somewhere else when the home region
	// is down, and says nothing about it afterwards.  See home.go.
	h.keepHome(ctx)
	// And a profile that was sitting on something when its last session
	// ended comes back standing, because nothing on the grid remembers
	// a seat.  See seat.go.
	h.keepSeat(ctx)
	return h, nil
}

// Default is the session a client gets when it names none: of those
// hosted, the one that has been hosted LONGEST.
//
// Stated as the property it gives rather than the procedure:
//
//	the default changes only when the default itself goes away.
//
// Adding an avatar never moves it, a client attaching never moves it,
// and a reconnect never moves it -- the rank belongs to the Hosted,
// which survives reconnection, and not to the agent underneath, which
// does not.  Nothing a person does casually can change which avatar a
// bare command drives, which is the entire point: a benchmark run
// against the wrong avatar is not an error, it is a plausible number.
//
// The rank is taken when a session comes UP rather than when it was
// asked for.  Stamping at request time looks tidier and quietly breaks
// the property: ask for a slow login first and a fast one second, and
// the fast one is the default until the slow one arrives and takes it
// away from a session that never went anywhere.  On arrival, a new
// session always has the highest rank and so always slots in behind.
//
// Startup logins are serial, so this is command-line order, which is
// what a person naming profiles in an order expects.
func (s *Server) Default() (*Hosted, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.defaultLocked()
}

func (s *Server) defaultLocked() (*Hosted, bool) {
	var best *Hosted
	for _, h := range s.agents {
		// A session that has been stopped is never the silent choice.
		// It is still listed, so that a person can ask why it is down,
		// but handing it to a client that named nothing would give
		// them a session that answers from what it remembers and
		// sends into nothing.
		if h == nil || h.Stopped() {
			continue
		}
		if best == nil || h.rank < best.rank {
			best = h
		}
	}
	return best, best != nil
}

// Stopped reports whether this session was ended deliberately -- logged
// out, or thrown off by the grid -- and so will not come back on its
// own.
func (h *Hosted) Stopped() bool { return h.stopped.Load() }

// Down says why a session cannot be used, or "" when it can.
func (h *Hosted) Down() string {
	if !h.Stopped() {
		return ""
	}
	if a := h.Agent(); a != nil {
		if reason, ok := a.Kicked(); ok {
			return "ended by the grid: " + reason
		}
		if err := a.Err(); err != nil {
			return err.Error()
		}
	}
	return "logged out"
}

// ended is closed when this Hosted is finished with for good: stopped
// deliberately, or removed from the server.  A reconnect does not close
// it, because a reconnect is this same Hosted carrying on.
//
// Made on first use rather than in a constructor, since a Hosted is a
// plain struct and is built as one in more than one place.
func (h *Hosted) ended() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.over == nil {
		h.over = make(chan struct{})
	}
	return h.over
}

// end closes ended, once; a second call changes nothing.  removed is
// the reason to give when Down has none, and is "" from the paths that
// stop the session, since stopping gives Down one.
func (h *Hosted) end(removed string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.over == nil {
		h.over = make(chan struct{})
	}
	select {
	case <-h.over:
		return
	default:
	}
	h.removedWhy = removed
	close(h.over)
}

// whyEnded is why a session that has ended cannot be used: Down's
// reason, or failing that the one it was removed with.
func (h *Hosted) whyEnded() string {
	if why := h.Down(); why != "" {
		return why
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.removedWhy
}

// ReconnectDelays are the waits before each attempt to re-establish a
// session, the last repeating.  They are generous on purpose: a login
// server will throttle a client that hammers it, and a failure here is
// usually something that takes a while to clear.
var ReconnectDelays = []time.Duration{
	5 * time.Second,
	15 * time.Second,
	30 * time.Second,
	time.Minute,
	2 * time.Minute,
}

// supervise re-establishes the session when it ends, unless it was
// stopped deliberately.
func (h *Hosted) supervise(ctx context.Context) {
	for {
		a := h.Agent()
		if a == nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-a.Done():
		}
		if ctx.Err() != nil || h.stopped.Load() {
			return
		}

		// Thrown off, rather than fallen off.  Somebody logged this
		// avatar in somewhere else, or an estate banned it, or an
		// administrator ejected it -- a decision was made, and logging
		// straight back in would overrule it.  Worse, it would keep
		// overruling it: the operator opening a viewer would be kicked
		// out of their own session every few seconds by their own
		// daemon, which would win.
		//
		// So the session stays down and waits to be asked for.  The
		// viewer always wins, which is the right rule.
		if err := a.Err(); !agent.Retryable(err) {
			h.stopped.Store(true)
			h.logf("%v -- staying logged out rather than taking the session back", err)
			h.notify(pb.AgentEvent_DISCONNECTED, errText(err)+
				" (not reconnecting; this session was ended deliberately)")
			// After the notice, so that a client is told why before
			// its stream ends.
			h.end("")
			return
		}

		h.notify(pb.AgentEvent_DISCONNECTED, errText(a.Err()))

		for attempt := 0; ; attempt++ {
			delay := ReconnectDelays[len(ReconnectDelays)-1]
			if attempt < len(ReconnectDelays) {
				delay = ReconnectDelays[attempt]
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			if h.stopped.Load() {
				return
			}

			h.attempts.Add(1)
			next, err := h.reconnect(ctx)
			if err != nil {
				h.notify(pb.AgentEvent_DISCONNECTED,
					fmt.Sprintf("reconnect attempt %d: %v", attempt+1, err))
				continue
			}
			h.setAgent(next)
			h.reconns.Add(1)
			// A fresh login has no active group, so whatever was
			// settled at startup has to be settled again.  See
			// group.go.
			h.restoreGroup(ctx, next)
			// And it is a fresh login with "start = home" in it, so
			// it may have landed in the wrong place for the same
			// reason the first one could have.  See home.go.
			h.keepHome(ctx)
			// A reconnect leaves the avatar standing exactly as a
			// first login does, so the seat is restored again too.
			h.keepSeat(ctx)
			// The identity is the same avatar but a new
			// session: a different session id, circuit code
			// and set of capability URLs.  Clients holding
			// any of those need to ask again.
			//
			// Where the avatar came back is filled in as a
			// teleport's is, because the fields mean where it is
			// now and a client should not have to know which of
			// the two kinds of region change it was reading.  A
			// fresh login lands wherever the grid thinks the
			// avatar is, which is not always where it was.
			h.notice(&pb.AgentEvent{
				Kind:         pb.AgentEvent_REGION_CHANGED,
				Detail:       "session re-established",
				Region:       next.RegionName(),
				RegionHandle: next.RegionHandle(),
			})
			break
		}
	}
}

func (h *Hosted) reconnect(ctx context.Context) (*agent.Agent, error) {
	acct, err := h.login.Do(ctx)
	if err != nil {
		return nil, err
	}
	return agent.Connect(ctx, acct, h.opts)
}

// notify tells every attached client something happened to the
// connection under them.
func (h *Hosted) notify(kind pb.AgentEvent_Kind, detail string) {
	h.notice(&pb.AgentEvent{Kind: kind, Detail: detail})
}

// noteRegion says the avatar is in a different region, with the name
// and handle of the one it is in now.
//
// This is a teleport or a border crossing arriving, and it is the same
// news the reconnect path sends: everything you were holding is stale.  A client that
// handles one handles the other, which is why it is the kind that
// already exists rather than a new one -- and why it goes out as a
// notice.  The message subscription stream carries what the grid said;
// this is what happened to the connection.
//
// It runs on the session's dispatch goroutine, so it does what notify
// does and no more: the sends to clients do not block.
func (h *Hosted) noteRegion(region string, handle uint64, teleportFlags uint32) {
	// A region that did not name itself in its handshake still moved
	// the avatar, and the detail is read by a person: "the avatar is
	// now in " with nothing after it is worse than saying less.
	detail := "the avatar is now in another region"
	if region != "" {
		detail = "the avatar is now in " + region
	}
	h.notice(&pb.AgentEvent{
		Kind:          pb.AgentEvent_REGION_CHANGED,
		Detail:        detail,
		Region:        region,
		RegionHandle:  handle,
		TeleportFlags: teleportFlags,
	})
}

func (h *Hosted) notice(ev *pb.AgentEvent) {
	p := &pb.ServerPacket{Body: &pb.ServerPacket_Notice{Notice: ev}}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		c.send(p)
	}
}

// Add hosts an already connected agent.
func (s *Server) Add(name string, a *agent.Agent) (*Hosted, error) {
	s.mu.Lock()
	if _, dup := s.agents[name]; dup {
		s.mu.Unlock()
		return nil, fmt.Errorf("server: %q is already hosted", name)
	}
	s.ranked++
	var self msg.UUID
	if a != nil && a.Account != nil {
		self = a.Account.AgentID
	}
	h := &Hosted{Name: name, agent: a, clients: map[*Client]bool{}, rank: s.ranked, seats: s.seats,
		offers: newOfferLog(time.Now()), viewerOn: func() bool { return s.viewerAttached(name) }, Log: s.log, self: self}
	s.agents[name] = h
	sp := s.slots
	s.mu.Unlock()

	// As StartAgent: a request waiting for its places hears of them.
	if sp != nil {
		sp.sync()
	}
	return h, nil
}

// Remove stops hosting a session and returns it.
//
// It does NOT log the avatar out -- that is the caller's to do, and to
// decide about, since a session with clients attached is somebody's work
// in progress.  What it does settle is the default: a removed session
// gives up its rank, so it comes back at the END of the queue if it is
// hosted again rather than reclaiming a default it used to hold.
//
// And it ends the stream of every client still attached, with the
// refusal an attach to a stopped session gets.  The name may be hosted
// again, by a new login, and a client left on the old stream would be
// held by something the server no longer holds while its calls by name
// reached the new one.
//
// Its places in the shared pool go with it; a name hosted again is
// given new ones once no grant holds the old.  See slots.go.
func (s *Server) Remove(name string) (*Hosted, bool) {
	s.mu.Lock()
	h, ok := s.agents[name]
	if !ok || h == nil {
		s.mu.Unlock()
		return nil, false
	}
	delete(s.agents, name)
	h.rank = 0
	sp := s.slots
	s.mu.Unlock()

	if sp != nil {
		sp.removeAgent(name)
	}
	h.end("no longer hosted here")
	return h, true
}

// Agent returns a hosted connection by name.
func (s *Server) Agent(name string) (*Hosted, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok := s.agents[name]
	return h, ok && h != nil
}

// Ranked lists the hosted sessions oldest first, which is the order a
// client should prefer them in, and a client that cannot be satisfied
// by one tries the next.  A stopped session -- logged out, or ended by
// the grid -- has given up its place and follows the rest, so the head
// of the list is the default whenever there is one.
func (s *Server) Ranked() []*Hosted {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Hosted, 0, len(s.agents))
	// Each read once: a session can stop while this sorts.
	place := make(map[*Hosted]uint64, len(s.agents))
	for _, h := range s.agents {
		if h != nil {
			out = append(out, h)
			if !h.Stopped() {
				place[h] = h.rank
			}
		}
	}
	// Place 0 means "gave up its place" -- a session stopped or
	// removed -- and must sort LAST rather than first, which is where a plain
	// numeric compare would put it.  It is still listed; it is simply
	// not at the head of a list whose head means "the default".
	sort.Slice(out, func(i, j int) bool {
		a, b := place[out[i]], place[out[j]]
		switch {
		case a == 0 && b == 0:
			return out[i].Name < out[j].Name
		case a == 0:
			return false
		case b == 0:
			return true
		}
		return a < b
	})
	return out
}

// Names lists the hosted connections.
func (s *Server) Names() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.agents))
	for k, v := range s.agents {
		if v != nil {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Close logs every hosted agent out and stops it, and ends the streams
// on each, as a logout does, with a notice first saying why.
func (s *Server) Close(ctx context.Context) {
	s.mu.Lock()
	hosted := make([]*Hosted, 0, len(s.agents))
	for _, h := range s.agents {
		if h != nil {
			hosted = append(hosted, h)
		}
	}
	s.agents = map[string]*Hosted{}
	sp := s.slots
	s.mu.Unlock()

	for _, h := range hosted {
		if sp != nil {
			sp.removeAgent(h.Name)
		}
		// Mark it stopped first, so the supervisor does not
		// treat a deliberate logout as a failure to recover
		// from.
		h.stopped.Store(true)
		h.standingAtStop()
		if a := h.Agent(); a != nil {
			_ = a.Logout(ctx, 10*time.Second)
		}
		h.notify(pb.AgentEvent_DISCONNECTED, "logged out: slgod is shutting down")
		h.end("slgod is shutting down")
	}
}

// Stats reports what the relay has done.
type Stats struct {
	Clients     int64
	Relayed     uint64 // messages handed to at least one client
	Dropped     uint64 // frames skipped because a client was not keeping up, counted as its stream ends
	Reconnects  uint64 // sessions re-established
	ReconnectAt uint64 // attempts made, successful or not
}

// Stats sums across the hosted connections.  The counters live on each
// Hosted rather than here, so a Hosted is complete on its own and does
// not need a pointer back to the server that happens to hold it.
func (s *Server) Stats() Stats {
	out := Stats{Clients: s.clients.Load()}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, h := range s.agents {
		if h != nil {
			out.Relayed += h.relayed.Load()
			out.Dropped += h.dropped.Load()
			out.Reconnects += h.reconns.Load()
			out.ReconnectAt += h.attempts.Load()
		}
	}
	return out
}

// attach adds a client, and hands back the offers still waiting when it
// wants them.
//
// The two happen under one lock, and that is the point of doing them
// together.  The relay records an offer before it takes this lock to
// choose who to send it to, so an offer recorded after the record is
// read here waits for the client to be added and is relayed to it, and
// one recorded before is in the record.  Nothing can fall between.  A
// client that read the record with a call of its own could not be
// promised that, whichever side of the attach it asked on.
func (h *Hosted) attach(c *Client) *pb.OfferRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[c] = true
	if !c.wants(imID) && !c.wants(dialogID) {
		return nil
	}
	if h.offers == nil {
		h.offers = newOfferLog(time.Now())
	}
	return h.offers.snapshotFor(c.wants)
}

func (h *Hosted) detach(c *Client) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()

	// Whatever it held is free now.  This is the whole of lock
	// revocation: the stream ending is the client going away, however
	// it went.
	h.lockSet().releaseAll(c)
}

// ClientCount is how many clients are watching.
func (h *Hosted) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// Serve accepts clients on ln until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	// Keepalive, which is what makes a lock safe to hold.
	//
	// A stream ending gives back the locks its client held, and a
	// client that exits or crashes ends its stream at once.  What does
	// not is a machine that is switched off or falls off the network:
	// the connection stays open as far as this end is concerned, and
	// the lock would be held by nobody until TCP gave up, which can be
	// hours.  Pinging an idle connection and dropping it when the ping
	// is not answered puts a bound on that of about half a minute.
	opts := []grpc.ServerOption{
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    20 * time.Second, // ping an idle connection
			Timeout: 10 * time.Second, // and give up if nothing comes back
		}),
		// Clients ping too, every twenty seconds whether or not a stream
		// is open, and gRPC's default policy would call a client that
		// pings more often than every five minutes, or without a stream,
		// abusive and disconnect it.
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	}
	// The receive limit is left at gRPC's own 4 MB, although a caller
	// that has proved nothing could be held to far less.  gRPC reads a
	// whole message before any interceptor or handler sees it, and the
	// limit is one number for the server, not for a method or for a
	// connection that has not authenticated -- and a Cap request
	// carrying an upload needs the room.  What an unauthenticated
	// caller sends is bounded instead by what is kept of it: nothing of
	// a Login but a challenge and a name of fixed size.
	if s.auth != nil {
		creds, err := auth.ServerTLS()
		if err != nil {
			return fmt.Errorf("server: cannot make a TLS certificate: %w", err)
		}
		unary, stream := AuthInterceptors(s.auth)
		opts = append(opts,
			grpc.Creds(creds),
			grpc.UnaryInterceptor(unary),
			grpc.StreamInterceptor(stream),
		)
	}
	// Per-connection state, which is what authentication hangs on and
	// where a connection records the address it came from.  Installed
	// whatever the authentication setting: a server running without it
	// still has clients worth telling apart, and where one is speaking
	// from is not a secret.
	opts = append(opts, grpc.StatsHandler(ConnTracker{}))
	g := grpc.NewServer(opts...)
	pb.RegisterGridServer(g, s)

	// The stopper waits for whichever comes first.  Waiting only on the
	// context was a deadlock: a listener that cannot be served on makes
	// Serve return at once, and this end then waited for a cancellation
	// that might never come -- so a daemon told to listen somewhere
	// impossible hung instead of saying so, and the error below could
	// not be reached at all.
	served := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			g.GracefulStop()
		case <-served:
			// Serve gave up on its own.  Stop is idempotent and there
			// is nothing left to be graceful towards, but the server
			// still holds what it was given.
			g.Stop()
		}
	}()

	err := g.Serve(ln)
	close(served)
	<-stopped
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// holding is the server's entry for name.  ok is whether the name is in
// the map at all -- hosted, stopped, or reserved by a login still under
// way, which has no Hosted yet and gives nil.
func (s *Server) holding(name string) (h *Hosted, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok = s.agents[name]
	return h, ok
}

// hosted is every avatar this daemon holds, by name.
func (s *Server) hosted() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.agents))
	for name, h := range s.agents {
		if h != nil {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
