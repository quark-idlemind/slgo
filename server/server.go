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
	"fmt"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"

	"slgo/agent"
	"slgo/msg"
	pb "slgo/proto/slgov1"
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

	clients atomic.Int64
}

// Hosted is one grid connection and the clients watching it.
type Hosted struct {
	Name  string
	Agent *agent.Agent

	mu      sync.RWMutex
	clients map[*Client]bool

	relayed atomic.Uint64
	dropped atomic.Uint64
}

// New makes an empty server.
func New() *Server {
	return &Server{agents: map[string]*Hosted{}}
}

// Host logs in and brings up a circuit, then keeps it.  The name is how
// clients ask for it.
func (s *Server) Host(ctx context.Context, name string, login agent.Login, opts agent.Options) (*Hosted, error) {
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

	h := &Hosted{Name: name, clients: map[*Client]bool{}}

	// Keeping the undecoded body is what lets the relay pass on a
	// message it does not understand.
	opts.Recv = append(opts.Recv, msg.KeepBody())
	opts.Tap = func(p *msg.Packet) { h.relay(p) }

	a, err := agent.Connect(ctx, acct, opts)
	if err != nil {
		undo()
		return nil, err
	}
	h.Agent = a

	s.mu.Lock()
	s.agents[name] = h
	s.mu.Unlock()
	return h, nil
}

// Add hosts an already connected agent.
func (s *Server) Add(name string, a *agent.Agent) (*Hosted, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.agents[name]; dup {
		return nil, fmt.Errorf("server: %q is already hosted", name)
	}
	h := &Hosted{Name: name, Agent: a, clients: map[*Client]bool{}}
	s.agents[name] = h
	return h, nil
}

// Agent returns a hosted connection by name.
func (s *Server) Agent(name string) (*Hosted, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok := s.agents[name]
	return h, ok && h != nil
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
		_ = h.Agent.Logout(ctx, 10*time.Second)
	}
}

// Stats reports what the relay has done.
type Stats struct {
	Clients int64
	Relayed uint64 // messages handed to at least one client
	Dropped uint64 // relays skipped because a client was not keeping up
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
}

// ClientCount is how many clients are watching.
func (h *Hosted) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// Serve accepts clients on ln until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	g := grpc.NewServer()
	pb.RegisterGridServer(g, s)

	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		g.GracefulStop()
	}()

	err := g.Serve(ln)
	<-done
	if ctx.Err() != nil {
		return nil
	}
	return err
}
