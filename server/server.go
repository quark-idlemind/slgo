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
// without touching the grid session.
//
// With no client attached, inbound messages are acknowledged and
// dropped.
package server

import (
	"context"
	"errors"
	"fmt"
	"github.com/quark-idlemind/slgo/auth"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	"github.com/quark-idlemind/slgo/agent"
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

	// failures and inflight cover starting agents on demand: what did
	// not work, and what is already being tried.
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

	// viewer is slgod's login endpoint for real viewers, which the
	// daemon owns and this package only reports on.  Nil -- the
	// ordinary case -- means none is served; see viewer.go.
	viewer Viewer
}

// SetAuth turns authentication on. Every method but Login is then
// refused on a connection that has not proved it knows the secret.
func (s *Server) SetAuth(a *auth.Server) { s.auth = a }

// Hosted is one grid connection and the clients watching it.
//
// The connection behind it is replaced when it has to be re-established,
// so it is reached through Agent() rather than being a field.  Clients
// keep their streams and their subscriptions across that; only the
// circuit underneath changes.
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

	// rank is the order this session came up in, lowest first.  It is
	// what makes the default deterministic; see Server.Default.
	rank uint64

	// Log is where anything worth a person's attention goes.  Nil is
	// silence, which is what a test wants; cmd/slgod sets it.
	Log func(format string, v ...any)

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

	h := &Hosted{Name: name, login: login, clients: map[*Client]bool{}}

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
	// is the daemon that finds out: the grid announces a teleport to
	// the session, not to whoever is attached to it.  Chained like the
	// two above, for their reason.
	if caller := opts.OnRegionChange; caller != nil {
		opts.OnRegionChange = func(region string, handle uint64) {
			caller(region, handle)
			h.noteRegion(region, handle)
		}
	} else {
		opts.OnRegionChange = func(region string, handle uint64) { h.noteRegion(region, handle) }
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
	s.mu.Unlock()

	go h.supervise(ctx)
	// A profile that asked to start at home may not have got there: the
	// login server puts the avatar somewhere else when the home region
	// is down, and says nothing about it afterwards.  See home.go.
	h.keepHome(ctx)
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
// This is the teleport arriving, and it is the same news the reconnect
// path sends: everything you were holding is stale.  A client that
// handles one handles the other, which is why it is the kind that
// already exists rather than a new one -- and why it goes out as a
// notice.  The message subscription stream carries what the grid said;
// this is what happened to the connection.
//
// It runs on the session's dispatch goroutine, so it does what notify
// does and no more: the sends to clients do not block.
func (h *Hosted) noteRegion(region string, handle uint64) {
	// A region that did not name itself in its handshake still moved
	// the avatar, and the detail is read by a person: "the avatar is
	// now in " with nothing after it is worse than saying less.
	detail := "the avatar is now in another region"
	if region != "" {
		detail = "the avatar is now in " + region
	}
	h.notice(&pb.AgentEvent{
		Kind:         pb.AgentEvent_REGION_CHANGED,
		Detail:       detail,
		Region:       region,
		RegionHandle: handle,
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
	defer s.mu.Unlock()
	if _, dup := s.agents[name]; dup {
		return nil, fmt.Errorf("server: %q is already hosted", name)
	}
	s.ranked++
	h := &Hosted{Name: name, agent: a, clients: map[*Client]bool{}, rank: s.ranked}
	s.agents[name] = h
	return h, nil
}

// Remove stops hosting a session and returns it.
//
// It does NOT log the avatar out -- that is the caller's to do, and to
// decide about, since a session with clients attached is somebody's work
// in progress.  What it does settle is the default: a removed session
// gives up its rank, so it comes back at the END of the queue if it is
// hosted again rather than reclaiming a default it used to hold.
func (s *Server) Remove(name string) (*Hosted, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.agents[name]
	if !ok || h == nil {
		return nil, false
	}
	delete(s.agents, name)
	h.rank = 0
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
// client should prefer them in: the first is the default, and a client
// that cannot be satisfied by one tries the next.
func (s *Server) Ranked() []*Hosted {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Hosted, 0, len(s.agents))
	for _, h := range s.agents {
		if h != nil {
			out = append(out, h)
		}
	}
	// Rank 0 means "gave up its place" -- a session that was stopped --
	// and must sort LAST rather than first, which is where a plain
	// numeric compare would put it.  It is still listed; it is simply
	// not at the head of a list whose head means "the default".
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].rank, out[j].rank
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

// Close logs every hosted agent out and stops it.
func (s *Server) Close(ctx context.Context) {
	s.mu.Lock()
	hosted := make([]*Hosted, 0, len(s.agents))
	for _, h := range s.agents {
		if h != nil {
			hosted = append(hosted, h)
		}
	}
	s.agents = map[string]*Hosted{}
	s.mu.Unlock()

	for _, h := range hosted {
		// Mark it stopped first, so the supervisor does not
		// treat a deliberate logout as a failure to recover
		// from.
		h.stopped.Store(true)
		if a := h.Agent(); a != nil {
			_ = a.Logout(ctx, 10*time.Second)
		}
	}
}

// Stats reports what the relay has done.
type Stats struct {
	Clients     int64
	Relayed     uint64 // messages handed to at least one client
	Dropped     uint64 // relays skipped because a client was not keeping up
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

func (h *Hosted) attach(c *Client) {
	h.mu.Lock()
	h.clients[c] = true
	h.mu.Unlock()
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
		// Clients ping too, and the default policy would call a client
		// that pings more often than every two hours abusive and
		// disconnect it.
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	}
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
			// Per-connection state, which is what authentication hangs on.
			grpc.StatsHandler(ConnTracker{}),
		)
	}
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
