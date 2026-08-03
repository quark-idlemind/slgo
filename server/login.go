package server

import (
	"context"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"

	"github.com/quark-idlemind/slgo/auth"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// Authentication for slgod, which holds a live Second Life session.
//
// Unauthenticated and bound to loopback it was merely unwise; reachable
// from another machine it would let anyone drive somebody else's avatar.
// The exchange is the same one slrund uses, from the same package, so
// there is one implementation and one secret.
//
// It belongs to the CONNECTION. TLS made the session and the handshake
// proved who is on the far end of it, so there is nothing to carry on
// later calls -- and nothing a caller could forge, since a connection is
// not something a request can claim to be on.

type connKey struct{}

type connState struct {
	mu     sync.Mutex
	client string
	authed bool
}

func (c *connState) mark(client string) {
	c.mu.Lock()
	c.client, c.authed = client, true
	c.mu.Unlock()
}

func (c *connState) ok() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.client, c.authed
}

func connFrom(ctx context.Context) (*connState, bool) {
	c, ok := ctx.Value(connKey{}).(*connState)
	return c, ok
}

// ConnTracker gives every connection somewhere to record what it has
// proved. A stats handler is the only hook in gRPC whose context every
// RPC on a connection descends from.
type ConnTracker struct{}

func (ConnTracker) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	return context.WithValue(ctx, connKey{}, &connState{})
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
	binding, err := auth.BindingFromContext(ctx)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition,
			"cannot bind authentication to this connection: %v", err)
	}

	if len(req.GetProof()) == 0 {
		challenge, err := s.auth.Begin(req.GetClient())
		if err != nil {
			return nil, status.Error(codes.Internal, "cannot start a login")
		}
		return &pb.LoginResponse{Challenge: challenge}, nil
	}

	proof, who, err := s.auth.Answer(req.GetChallenge(), req.GetProof(), binding)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, auth.ErrDenied.Error())
	}
	c, ok := connFrom(ctx)
	if !ok {
		return nil, status.Error(codes.Internal, "no connection state")
	}
	c.mark(who)
	return &pb.LoginResponse{Proof: proof}, nil
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
