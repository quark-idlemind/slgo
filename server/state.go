package server

// What can be done with an agent, which is not the same question as
// whether a circuit is up.
//
// # Why states rather than a boolean
//
// "Connected" answers one question and the daemon is asked several.
// Each of these is a different ACTION, which is what makes the
// distinctions worth keeping:
//
//	HOSTED      use it
//	CONNECTING  wait; something is already logging it in
//	CONFIGURED  start it, if you want it
//	STOPPED     leave it alone unless asked for by name
//	FAILED      starting it now will waste a login attempt
//
// Without them, "I have never heard of qi" and "qi is deliberately
// down" are the same answer, and the second is not an error at all --
// it is somebody using that avatar in a viewer.
//
// # Why the server does not read the config directory
//
// It needs to know which profiles exist, and how to log one in, and
// neither is its business to decide.  Both arrive as functions the
// daemon sets, so the policy -- where profiles live, what defaults they
// get, which machine identity they present -- stays in cmd/slgod where
// the rest of it already is.  A server with neither set behaves exactly
// as it did before: it knows only what it was given.

import (
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// Profiles is how the server learns which agents COULD be started.
// Nil means it knows of no others, which is the old behaviour.
type Profiles func() []string

// LoginFor supplies what is needed to bring one up.  Nil means none can
// be started on demand.
type LoginFor func(name string) (agent.Login, agent.Options, error)

// SetProfiles tells the server what else exists and how to start it.
func (s *Server) SetProfiles(list Profiles, login LoginFor) {
	s.mu.Lock()
	s.profiles, s.loginFor = list, login
	s.mu.Unlock()
}

// failure is a login that did not work, and when it is worth trying
// again.
//
// Remembered so that repeated asking does not turn into repeated
// logins.  A login server throttles a client that hammers it, and the
// throttle then presents as a different fault entirely -- so the
// backoff is not politeness, it is the difference between one clear
// error and an afternoon of confusing ones.
type failure struct {
	when   time.Time
	err    error
	tries  int
	before time.Time // not worth retrying until
}

// starting is a login in flight, so that several callers asking at once
// produce one login and all wait for it.
type starting struct {
	done chan struct{}
	h    *Hosted
	err  error
}

// state describes an agent for reporting.
func (h *Hosted) state() (pb.AgentInfo_State, string) {
	if h.Stopped() {
		return pb.AgentInfo_STOPPED, h.Down()
	}
	a := h.Agent()
	if a == nil {
		return pb.AgentInfo_CONNECTING, "logging in"
	}
	select {
	case <-a.Done():
		// Down but not stopped: the supervisor is getting it back.
		return pb.AgentInfo_CONNECTING, errText(a.Err())
	default:
		return pb.AgentInfo_HOSTED, ""
	}
}

// retryAfter is how long to wait before trying a failed login again.
// The same shape and the same reason as ReconnectDelays.
func retryAfter(tries int) time.Duration {
	if tries < 1 {
		tries = 1
	}
	if tries > len(ReconnectDelays) {
		tries = len(ReconnectDelays)
	}
	return ReconnectDelays[tries-1]
}

// agentState is the set of things the server knows about but is not
// holding: profiles that exist, and logins that failed.
type agentState struct {
	mu       sync.Mutex
	failures map[string]*failure
	inflight map[string]*starting
}
