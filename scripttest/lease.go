package scripttest

// The pool, the lease and the queue.
//
// A group is taken WHOLE.  Handing out objects one at a time would let
// two callers each hold some and wait for the rest, which is a deadlock
// where a queue is merely slow; that is the reasoning in
// internal/session/auto.go and in the contract, and a backend that did
// otherwise would let a caller pass its tests here and hang in world.
//
// # How a run knows the lease is still the caller's
//
// The contract says a Run's target comes "from a Granted on a lease held
// by this same caller", and there is nothing on a gRPC connection that
// says who a caller is -- over a pipe every caller has the same address.
// So the id is the proof: a fresh one is minted at every grant and
// forgotten when the lease ends.  An id from a lease that has been given
// back names nothing, which is exactly the mistake worth catching.  The
// ids count up, so a caller could guess the next one; that catches a
// mistake and would not stop a caller set on cheating, which is all a
// test backend needs.

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/quark-idlemind/slgo/proto/scriptv1"
)

type poolAgent struct {
	name   string
	groups []*poolGroup
}

type poolGroup struct {
	agent   *poolAgent
	n       int
	targets []*poolTarget

	held   bool
	heldBy string
}

type poolTarget struct {
	group *poolGroup
	name  string

	// id is what the caller holding this object was told to call it,
	// minted at grant time and cleared when the lease ends.
	id string

	beh Behaviour
}

// waiter is a caller queued for a group.
type waiter struct {
	agent string
	who   string
	n     int
	ch    chan *poolGroup // buffered; the grant is made under the lock
}

// take finds a free group with room for n objects, or says there is
// none.  agent empty takes the first free group anywhere, in the order
// the avatars were named -- which is the daemon's order live, and the
// point is that a caller minding which avatar it gets says so.
func (s *Server) take(agent, who string, n int) *poolGroup {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.takeLocked(agent, who, n)
}

func (s *Server) takeLocked(agent, who string, n int) *poolGroup {
	for _, a := range s.agents {
		if agent != "" && a.name != agent {
			continue
		}
		for _, g := range a.groups {
			if g.held || len(g.targets) < n {
				continue
			}
			g.held, g.heldBy = true, who
			s.mintLocked(g)
			return g
		}
	}
	return nil
}

// mintLocked gives every object in the group a fresh id.
func (s *Server) mintLocked(g *poolGroup) {
	for _, t := range g.targets {
		s.seq++
		t.id = fmt.Sprintf("%s/%d/%s#%d", g.agent.name, g.n, t.name, s.seq)
	}
}

// giveBack releases a group and hands it straight to the first waiter it
// suits.  The ids go with it: what the last caller was holding is now
// nothing, which is how a Run against a lease that has ended is caught
// rather than quietly served.
func (s *Server) giveBack(g *poolGroup) {
	if g == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, t := range g.targets {
		t.id = ""
	}
	g.held, g.heldBy = false, ""

	for i, w := range s.queue {
		if w.agent != "" && w.agent != g.agent.name {
			continue
		}
		if len(g.targets) < w.n {
			continue
		}
		s.queue = append(s.queue[:i], s.queue[i+1:]...)
		g.held, g.heldBy = true, w.who
		s.mintLocked(g)
		w.ch <- g // buffered, and nobody else can be sending on it
		return
	}
}

// enqueue puts a caller in the queue and says how many are in front.
func (s *Server) enqueue(agent, who string, n int) (*waiter, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := &waiter{agent: agent, who: who, n: n, ch: make(chan *poolGroup, 1)}
	ahead := len(s.queue)
	s.queue = append(s.queue, w)
	return w, ahead
}

// unqueue takes a caller back out.  It returns a group when the caller
// was granted one on the way out -- a race the caller cannot see and
// must not lose the group to.
func (s *Server) unqueue(w *waiter) *poolGroup {
	s.mu.Lock()
	for i, x := range s.queue {
		if x == w {
			s.queue = append(s.queue[:i], s.queue[i+1:]...)
			s.mu.Unlock()
			return nil
		}
	}
	s.mu.Unlock()
	select {
	case g := <-w.ch:
		return g
	default:
		return nil
	}
}

// find resolves a target id to the object it names, while the lease that
// minted it is still open.
func (s *Server) find(id string) *poolTarget {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.agents {
		for _, g := range a.groups {
			for _, t := range g.targets {
				if t.id != "" && t.id == id {
					return t
				}
			}
		}
	}
	return nil
}

func (s *Server) knows(agent string) bool {
	for _, a := range s.agents {
		if a.name == agent {
			return true
		}
	}
	return false
}

// Lease holds a group for as long as the caller is there.
//
// Returning ends the lease, and the only way to return is for the
// caller's stream to end -- by asking, by dying, or by being cut off.
// That is the whole design: there is no Release to forget to call and no
// lease timer to tune between "a long run loses its objects" and "a
// crash blocks the pool".
func (s *Server) Lease(req *scriptv1.LeaseRequest, stream grpc.ServerStreamingServer[scriptv1.LeaseEvent]) error {
	if err := s.notReady(); err != nil {
		return err
	}
	ctx := stream.Context()

	n := int(req.GetTargets())
	if n == 0 {
		n = 1
	}
	if n > s.opt.GroupSize {
		// An error rather than a wait, because no group will ever be big
		// enough and a caller waiting for one would wait forever.
		return status.Errorf(codes.InvalidArgument,
			"a lease takes at most %d objects at once; %d would need more than one group",
			s.opt.GroupSize, n)
	}
	agent := req.GetAgent()
	if agent != "" && !s.knows(agent) {
		return status.Errorf(codes.NotFound, "no avatar called %q", agent)
	}
	who := req.GetWho()

	g := s.take(agent, who, n)
	if g == nil {
		w, ahead := s.enqueue(agent, who, n)
		if err := stream.Send(&scriptv1.LeaseEvent{
			Event: &scriptv1.LeaseEvent_Queued{Queued: &scriptv1.Queued{Ahead: int32(ahead)}},
		}); err != nil {
			s.giveBack(s.unqueue(w))
			return err
		}

		var wait <-chan struct{}
		if d := s.seconds(req.GetWaitSeconds()); d > 0 {
			// A context rather than a timer so the wait can be selected
			// against alongside the caller going away, and so the timer
			// is stopped on either exit.
			bounded, cancel := context.WithTimeout(ctx, d)
			defer cancel()
			wait = bounded.Done()
		}

		select {
		case g = <-w.ch:
		case <-ctx.Done():
			s.giveBack(s.unqueue(w))
			return nil
		case <-wait:
			s.giveBack(s.unqueue(w))
			return status.Errorf(codes.DeadlineExceeded,
				"every group is busy and %ds is up", req.GetWaitSeconds())
		}
	}
	defer s.giveBack(g)

	granted := &scriptv1.Granted{Agent: g.agent.name, Group: int32(g.n)}
	for _, t := range g.targets[:n] {
		granted.Targets = append(granted.Targets, &scriptv1.Target{Id: t.id, Name: t.name})
	}
	if err := stream.Send(&scriptv1.LeaseEvent{
		Event: &scriptv1.LeaseEvent_Granted{Granted: granted},
	}); err != nil {
		return err
	}

	// Held until the caller goes away.  Not an error when it does: that
	// is how a lease ends.
	<-ctx.Done()
	return nil
}
