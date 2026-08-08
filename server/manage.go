package server

// Starting a session that is not running, and stopping one that is.
//
// # Why these are their own methods
//
// Both are deliberate acts by a person.  Logging an avatar in is
// visible on the grid -- an arrival, a presence, a notice to anyone
// watching for it -- and it should follow from somebody asking, not
// from a typo in a stale config file being retried by a program that
// attaches on a timer.  So attaching never starts anything; Host does,
// and it has to be told which.
//
// # Why a stopped session stays stopped
//
// The reason to stop one is usually that a person wants that avatar
// somewhere else -- in a viewer, most likely.  A daemon that started it
// again because something asked vaguely would be taking the avatar back
// off them, which is the fight this whole arrangement exists to avoid.
// So STOPPED is sticky and only a request that names the agent, with
// force, overrules it.

import (
	"context"
	"sort"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// Host brings up a session that is not running.
//
// Safe to repeat: an agent already hosted is reported as such rather
// than logged in twice, which would kick the session it already has.
func (s *Server) Host(ctx context.Context, req *pb.HostRequest) (*pb.HostResponse, error) {
	name := req.GetAgent()
	if name == "" {
		return nil, status.Error(codes.InvalidArgument,
			"which agent to start has to be named; there is no sensible default for starting one")
	}

	if h, ok := s.Agent(name); ok && !h.Stopped() {
		return &pb.HostResponse{Agent: h.info(), Already: true}, nil
	}

	// Something stopped it on purpose, most likely a person now using
	// that avatar elsewhere.  Taking it back needs saying so.
	if h, ok := s.Agent(name); ok && h.Stopped() && !req.GetForce() {
		return nil, status.Errorf(codes.FailedPrecondition,
			"%s was stopped deliberately (%s); use force to start it anyway, "+
				"but check nobody is using that avatar first", name, h.Down())
	}

	h, err := s.startOnDemand(ctx, name)
	if err != nil {
		return nil, err
	}
	return &pb.HostResponse{Agent: h.info()}, nil
}

// startOnDemand logs an agent in, once, however many callers ask.
func (s *Server) startOnDemand(ctx context.Context, name string) (*Hosted, error) {
	s.mu.RLock()
	loginFor := s.loginFor
	s.mu.RUnlock()
	if loginFor == nil {
		return nil, status.Error(codes.Unimplemented,
			"this server was not given a way to start agents; name them on its command line")
	}

	// Single flight.  Two clients asking at once must produce one
	// login: two would race to the same account and the grid would
	// resolve it by kicking one of them, which looks exactly like the
	// fault this daemon is meant to avoid.
	s.starts.mu.Lock()
	if s.starts.inflight == nil {
		s.starts.inflight = map[string]*starting{}
	}
	if in, ok := s.starts.inflight[name]; ok {
		s.starts.mu.Unlock()
		select {
		case <-in.done:
			return in.h, in.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	// Do not hammer a login server that has already refused.  The
	// throttle it applies would present as a different fault
	// altogether, so the wait is what keeps the error honest.
	if f := s.starts.failures[name]; f != nil && time.Now().Before(f.before) {
		wait := time.Until(f.before).Round(time.Second)
		s.starts.mu.Unlock()
		return nil, status.Errorf(codes.Unavailable,
			"%s failed to log in (%v); not trying again for %v", name, f.err, wait)
	}

	in := &starting{done: make(chan struct{})}
	s.starts.inflight[name] = in
	s.starts.mu.Unlock()

	h, err := s.login(ctx, name, loginFor)

	s.starts.mu.Lock()
	delete(s.starts.inflight, name)
	if err != nil {
		if s.starts.failures == nil {
			s.starts.failures = map[string]*failure{}
		}
		f := s.starts.failures[name]
		if f == nil {
			f = &failure{}
			s.starts.failures[name] = f
		}
		f.tries++
		f.when, f.err = time.Now(), err
		f.before = f.when.Add(retryAfter(f.tries))
	} else {
		delete(s.starts.failures, name)
	}
	in.h, in.err = h, err
	close(in.done)
	s.starts.mu.Unlock()

	return h, err
}

// login does the work, replacing a stopped session of the same name.
func (s *Server) login(ctx context.Context, name string, loginFor LoginFor) (*Hosted, error) {
	l, opts, err := loginFor(name)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%s: %v", name, err)
	}

	// A stopped session still holds the name.  Take it out first, so
	// that starting one is not refused by the corpse of the last.
	if old, ok := s.Agent(name); ok && old.Stopped() {
		s.Remove(name)
	}

	// The daemon's own context, not the caller's: the session outlives
	// the request that asked for it, and cancelling a request must not
	// take the avatar back out of the world.
	h, err := s.StartAgent(s.base(), name, l, opts)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "%s: %v", name, err)
	}
	s.mu.RLock()
	log, onStart := s.log, s.onStart
	s.mu.RUnlock()
	h.Log = log
	if onStart != nil {
		// The same settling a startup session gets -- the active
		// group, above all, without which the avatar cannot build and
		// the simulator blames the land.
		onStart(h)
	}
	return h, nil
}

// Logout puts a session down and keeps it down.
func (s *Server) Logout(ctx context.Context, req *pb.LogoutRequest) (*pb.LogoutResponse, error) {
	name := req.GetAgent()
	if name == "" {
		return nil, status.Error(codes.InvalidArgument,
			"which agent to log out has to be named")
	}
	h, ok := s.Agent(name)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "no agent named %q", name)
	}
	if h.Stopped() {
		return &pb.LogoutResponse{}, nil // already down; asking twice is not an error
	}

	// Somebody is using it.  A benchmark mid-run has a script installed
	// and a reading half taken, and throwing that away should be a
	// decision rather than a side effect.
	if who := h.clientNames(); len(who) > 0 && !req.GetForce() {
		st := status.Newf(codes.FailedPrecondition,
			"%s is in use by %s; use force to log out anyway",
			name, strings.Join(who, ", "))
		// The names travel in the status details as well as the
		// sentence.  A unary call carries a response or an error and
		// never both, so returning them on a LogoutResponse alongside
		// this put them somewhere the client could not read: it gets a
		// nil response with the error, every time.
		if with, err := st.WithDetails(&pb.LogoutResponse{Clients: who}); err == nil {
			st = with
		}
		return nil, st.Err()
	}

	// Stopped BEFORE logging out, so the supervisor reads a deliberate
	// logout as deliberate rather than as a session to recover.
	h.stopped.Store(true)
	h.logf("logging out on request")
	if a := h.Agent(); a != nil {
		out, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = a.Logout(out, 10*time.Second)
	}
	h.notify(pb.AgentEvent_DISCONNECTED, "logged out on request; it will not come back until asked for by name")

	// It keeps its place in the map so that it can be reported as
	// STOPPED rather than looking like a name nobody has heard of, but
	// gives up its rank: coming back means coming back at the end of
	// the queue, not reclaiming a default it used to hold.
	s.mu.Lock()
	h.rank = 0
	s.mu.Unlock()

	return &pb.LogoutResponse{}, nil
}

// clientNames is who is attached, for an error a person can act on.
//
// The names are the ones clients authenticated under, so the answer is
// "autobench and slsh" rather than a count -- which is the difference
// between knowing what you are about to interrupt and not.
func (h *Hosted) clientNames() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]string, 0, len(h.clients))
	for c := range h.clients {
		name := c.name
		if name == "" {
			name = "an unnamed client"
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
