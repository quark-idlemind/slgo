package server

// The viewer login endpoint: where it is, whether anybody is on it, the
// one-off password that lets somebody in, and what the session has to
// hear of a viewer that is on it.
//
// # Why the server answers for something it does not own
//
// slgod can serve an XMLRPC login endpoint that hands a running session
// to a real viewer (cmd/slgod/viewer.go).  The server knows nothing
// about it: where it is bound, which profiles may be handed over, and
// what a viewer password is are all decisions taken in cmd/slgod, and
// making the server hold them would mean this package importing the
// viewer package to relay one address and one bool.
//
// So it arrives as an interface the daemon sets, exactly as Profiles
// and LoginFor do in state.go, and for the same reason: the policy
// stays where the rest of it already is.  A server with none set
// behaves as it always did.
//
// # Why nil is an answer and not a failure
//
// -viewer is not the default and the daemon running here does not use
// it, so "there is no viewer endpoint" is the ordinary state rather
// than a fault.  It has to be tellable from "there is one and it is
// idle", because the two have different answers: the second is "point a
// viewer at this address", the first is "restart the daemon with the
// flag".  Reporting an empty address for both would leave a person
// staring at a blank field with no idea which they were in.
//
// # Why minting is a call of its own
//
// Where the endpoint is costs nothing to read and rides along with the
// rest of the status.  Minting a credential does not: each call makes a
// secret and invalidates the one before, so it has to be something a
// client asks for on purpose and never something it does by looking.

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// Viewer is slgod's login endpoint for real viewers, as much of it as
// the server has to answer questions about.  Nil means this daemon
// serves no viewer logins.
type Viewer interface {
	// LoginURI is the address to add to a viewer's grid list, or
	// empty if the endpoint is not up.
	LoginURI() string

	// Attached reports whether a viewer has taken this profile's
	// session.
	Attached(profile string) bool

	// Mint makes a password good for ONE login as this profile, and
	// says how long it is good for.  The plaintext is the caller's to
	// pass on and nobody's to keep.
	//
	// The error is meant to be shown to a person: minting fails for
	// reasons they can do something about.
	Mint(profile string) (password string, life time.Duration, err error)
}

// SetViewer tells the server where viewer logins are served, which is
// something only the daemon that bound the listener knows.
func (s *Server) SetViewer(v Viewer) {
	s.mu.Lock()
	s.viewer = v
	s.mu.Unlock()
}

// noViewerEndpoint is the one wording for a daemon that serves no
// viewer logins, and it says what to do rather than what is missing.
//
// cmd/slsh says the same thing in its own words when it reads an empty
// address out of a status -- see the viewer command there -- because a
// client that has the answer already should not have to ask for an
// error to find out how to act on it.
const noViewerEndpoint = "this daemon serves no viewer logins; " +
	"restart slgod with -viewer ADDR (-viewer 127.0.0.1:9000, say) and a real viewer can take a session over"

// viewerFor is what a status says about the viewer endpoint.
//
// Never nil, so that a client reads a field with an empty address
// rather than having to know that an absent message means the same
// thing.
func (s *Server) viewerFor(profile string) *pb.ViewerEndpoint {
	s.mu.RLock()
	v := s.viewer
	s.mu.RUnlock()
	if v == nil {
		return &pb.ViewerEndpoint{}
	}
	return &pb.ViewerEndpoint{LoginUri: v.LoginURI(), Attached: v.Attached(profile)}
}

// ViewerCredential mints a password a viewer may log in with once.
//
// The plaintext crosses the wire to the client that asked and is not
// logged, here or by the daemon: it goes on to a viewer's argv, which
// is as far as it should ever be readable.
func (s *Server) ViewerCredential(ctx context.Context, req *pb.ViewerCredentialRequest) (*pb.ViewerCredentialResponse, error) {
	h, err := s.lookup(req.Agent)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	v := s.viewer
	s.mu.RUnlock()
	if v == nil {
		return nil, status.Error(codes.FailedPrecondition, noViewerEndpoint)
	}

	// The avatar's name, because that is what a viewer's login box
	// wants; the profile name is this daemon's filing and means
	// nothing to a viewer.
	a := h.Agent()
	if a == nil || a.Account == nil {
		return nil, status.Errorf(codes.FailedPrecondition,
			"%s is not logged in, so there is no session to hand to a viewer", h.Name)
	}

	pass, life, err := v.Mint(h.Name)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	return &pb.ViewerCredentialResponse{
		LoginUri:      v.LoginURI(),
		First:         a.Account.FirstName,
		Last:          a.Account.LastName,
		Password:      pass,
		ExpirySeconds: int32(life / time.Second),
	}, nil
}

// ViewerAttached tells the session that a viewer has been handed it.
// A person at a viewer has the wheel, so the daemon stops trying to get
// the avatar home, as it does when a client teleports.  See home.go.
func (h *Hosted) ViewerAttached() {
	h.stopHoming("a viewer was handed this session")
}

// ViewerSent tells the session of a message a viewer on it has sent,
// as the viewer circuit passes it on to the simulator.
//
// A viewer's messages go down its own circuit straight to the session
// and not through sendMessage, so this is the only way the daemon hears
// of a teleport or a new home asked for at a viewer.  See home.go.
func (h *Hosted) ViewerSent(id msg.ID) {
	h.noteRequest(id, "a viewer")
}
