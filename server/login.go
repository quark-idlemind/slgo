package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/quark-idlemind/slgo/auth"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// Authentication for slgod, which holds a live Second Life session.
//
// Unauthenticated and bound to loopback it was merely unwise; reachable
// from another machine it would let anyone drive somebody else's avatar.
// The exchange is package auth's, which the client package speaks too,
// so there is one implementation and one secret.
//
// It belongs to the CONNECTION. TLS made the session and the handshake
// proved who is on the far end of it, so there is nothing to carry on
// later calls -- and nothing a caller could forge, since a connection is
// not something a request can claim to be on.

type connKey struct{}

type connState struct {
	mu     sync.Mutex
	client string
	pid    int32
	authed bool

	// remote is where this connection came from, as the SERVER sees
	// it.  Never anything the client said: the whole value of printing
	// an address in "who is holding this" is that it tells you where
	// to go and look, and one the far end chose would tell you where
	// it wanted you to look.
	remote string
}

func (c *connState) mark(client string, pid int32) {
	c.mu.Lock()
	c.client, c.pid, c.authed = client, pid, true
	c.mu.Unlock()
}

func (c *connState) ok() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return describeClient(c.client, c.pid, c.remote), c.authed
}

// describeClient is how a client is named in a message to a person:
//
//	slsh[1234]@198.51.100.7
//
// Each piece is left out when it is not known, so an old client that
// sends no pid is "slsh@..." and one on a server with no connection
// tracking is just "slsh".  A client with no name at all is described
// rather than named, since "" in the middle of a sentence reads as a
// bug in the sentence.
func describeClient(name string, pid int32, remote string) string {
	if name == "" {
		// No name at all, which is a server running without
		// authentication: nothing ever said what it was.  "@" reads as
		// an address attached to a name, so with no name it is spelt
		// out in words instead.
		if remote == "" {
			return "an unnamed client"
		}
		return "an unnamed client at " + remote
	}
	if pid > 0 {
		name = fmt.Sprintf("%s[%d]", name, pid)
	}
	if remote != "" {
		name += "@" + remote
	}
	return name
}

// hostOf is an address without its port.  The port a client dialled
// FROM is ephemeral and tells nobody anything.
func hostOf(a net.Addr) string {
	if a == nil {
		return ""
	}
	if host, _, err := net.SplitHostPort(a.String()); err == nil {
		return host
	}
	return a.String()
}

func connFrom(ctx context.Context) (*connState, bool) {
	c, ok := ctx.Value(connKey{}).(*connState)
	return c, ok
}

// ConnTracker gives every connection somewhere to record what it has
// proved. A stats handler is the only hook in gRPC whose context every
// RPC on a connection descends from.
type ConnTracker struct{}

func (ConnTracker) TagConn(ctx context.Context, info *stats.ConnTagInfo) context.Context {
	var remote string
	if info != nil {
		remote = hostOf(info.RemoteAddr)
	}
	return context.WithValue(ctx, connKey{}, &connState{remote: remote})
}
func (ConnTracker) HandleConn(context.Context, stats.ConnStats)                     {}
func (ConnTracker) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context { return ctx }
func (ConnTracker) HandleRPC(context.Context, stats.RPCStats)                       {}

const loginMethod = "/slgo.v1.Grid/Login"

// Login runs the handshake. See auth for what it is and why.
func (s *Server) Login(ctx context.Context, req *pb.LoginRequest) (*pb.LoginResponse, error) {
	if s.auth == nil {
		return nil, status.Error(codes.FailedPrecondition, "this server runs without authentication")
	}
	if sentOldName(req) {
		return nil, status.Error(codes.FailedPrecondition, tooOld)
	}
	binding, err := auth.BindingFromContext(ctx)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition,
			"cannot bind authentication to this connection: %v", err)
	}

	if len(req.GetProof()) == 0 {
		challenge, err := s.auth.Begin(loginName(req))
		if err != nil {
			return nil, status.Error(codes.Internal, "cannot start a login")
		}
		return &pb.LoginResponse{Challenge: challenge}, nil
	}

	// Each has exactly one length that could ever verify, so anything
	// else is refused before any work is done with it -- and before
	// the throttle, since it is not a guess at anything.
	if len(req.GetChallenge()) != auth.ChallengeSize || len(req.GetProof()) != auth.ProofSize {
		return nil, status.Error(codes.Unauthenticated, auth.ErrDenied.Error())
	}

	// The address this connection came from, which is what failures
	// are counted by.  A server without connection tracking counts
	// every caller as one, which is stricter rather than looser; and it
	// is refused below anyway, for having nowhere to record a success.
	c, tracked := connFrom(ctx)
	var remote string
	if tracked {
		remote = c.remote
	}
	done, err := s.throttle.Admit(ctx, remote)
	if errors.Is(err, auth.ErrBusy) {
		return nil, status.Error(codes.ResourceExhausted, err.Error())
	}
	if err != nil {
		return nil, status.FromContextError(err).Err()
	}

	proof, who, err := s.auth.Answer(req.GetChallenge(), req.GetProof(), binding)
	done(err == nil)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, auth.ErrDenied.Error())
	}
	if !tracked {
		return nil, status.Error(codes.Internal, "no connection state")
	}
	c.mark(who, req.GetPid())
	return &pb.LoginResponse{Proof: proof}, nil
}

// loginName is the name a client gave, from the four words it is
// carried in.  See auth.PackName.
func loginName(req *pb.LoginRequest) string {
	return auth.UnpackName([4]uint64{
		req.GetClient_0(), req.GetClient_1(), req.GetClient_2(), req.GetClient_3(),
	})
}

// tooOld is what a client built before the name moved is told.
const tooOld = "this client is older than slgod: it sends its name in a form slgod no longer reads. " +
	"Rebuild and reinstall it with the same tree as the daemon"

// sentOldName reports a request from a client built when the name was
// a string, field 1.
//
// That field is reserved now rather than deleted, and a field this end
// does not know is not thrown away by the decoder but kept, as it came,
// among the message's unknown fields.  So an old client is recognised
// by what it sent rather than guessed at from what it did not: an empty
// name would be the other way to tell, and would say the same thing
// about a new client that had merely sent none.  Every old client sent
// one, since every one of them set Client.
//
// Telling it so is the point.  Refused with the same words as a wrong
// secret, somebody would go and check the secret, which is not what is wrong.
func sentOldName(req *pb.LoginRequest) bool {
	b := req.ProtoReflect().GetUnknown()
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return false
		}
		if num == 1 {
			return true
		}
		b = b[n:]
		n = protowire.ConsumeFieldValue(num, typ, b)
		if n < 0 {
			return false
		}
		b = b[n:]
	}
	return false
}

// AuthInterceptors refuse every method but Login on a connection that
// has not authenticated.
func AuthInterceptors(a *auth.Server) (grpc.UnaryServerInterceptor, grpc.StreamServerInterceptor) {
	check := func(ctx context.Context, method string) error {
		if a == nil || method == loginMethod {
			return nil
		}
		c, ok := connFrom(ctx)
		if ok {
			_, ok = c.ok()
		}
		if !ok {
			return status.Error(codes.Unauthenticated,
				"not authenticated: call Login on this connection first")
		}
		return nil
	}
	unary := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		if err := check(ctx, info.FullMethod); err != nil {
			return nil, err
		}
		return h(ctx, req)
	}
	stream := func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler) error {
		if err := check(ss.Context(), info.FullMethod); err != nil {
			return err
		}
		return h(srv, ss)
	}
	return unary, stream
}
