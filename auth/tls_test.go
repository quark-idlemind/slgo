package auth

// The channel binding cannot be checked without a real handshake.
//
// Exported keying material comes out of a completed TLS 1.3 session and
// from nothing else, so there is no way to stand in for it: a test that
// invented a binding would be checking the arithmetic in auth.go, which
// auth_test.go already does, and would say nothing about whether the
// two ends ever arrive at the SAME material.  That is the property the
// whole file exists for -- an attacker terminating TLS on both sides
// has two sessions, and the proof computed on one must not verify on
// the other -- and it is only visible with a server on one end and a
// client on the other.
//
// So there is a gRPC server on loopback here, with the credentials this
// package hands out, echoing back what it exported.  Without it nothing
// in tls.go runs at all, and the failure that matters most would be
// invisible: a binding that quietly came back empty still verifies at
// both ends, because both ends hash the same nothing.

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/peer"

	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// notTLSInfo is what a connection that was never encrypted reports, so
// that the refusal can be reached without standing up a second server
// to be plaintext on.
type notTLSInfo struct{}

func (notTLSInfo) AuthType() string { return "insecure" }

// echoBinding is a server that answers with the keying material it
// exported, which is the only way for a test to see the server's half.
type echoBinding struct {
	pb.UnimplementedGridServer
}

func (echoBinding) Login(ctx context.Context, _ *pb.LoginRequest) (*pb.LoginResponse, error) {
	b, err := BindingFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return &pb.LoginResponse{Challenge: b}, nil
}

// serveTLS puts an echoing server on loopback with the credentials
// ServerTLS makes, and answers with its address.
func serveTLS(t *testing.T) string {
	t.Helper()
	creds, err := ServerTLS()
	if err != nil {
		t.Fatalf("making server credentials: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening on loopback: %v", err)
	}
	g := grpc.NewServer(grpc.Creds(creds))
	pb.RegisterGridServer(g, echoBinding{})
	go g.Serve(ln)
	t.Cleanup(g.Stop)
	return ln.Addr().String()
}

// TestBothEndsExportTheSameChannelBinding is the property everything
// else rests on: the client's proof is computed over what IT exported,
// and the server checks it against what IT exported, so if those two
// values ever differed no honest client could authenticate at all.
func TestBothEndsExportTheSameChannelBinding(t *testing.T) {
	t.Parallel()

	addr := serveTLS(t)
	creds, binding := ClientTLS()

	// A clone taken before the handshake, because gRPC takes one per
	// connection: it must see the same binding afterwards or a caller
	// would compute its proof over nothing at all.
	clone := creds.Clone().(*bindingCreds)

	cc, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := pb.NewGridClient(cc).Login(ctx, &pb.LoginRequest{Client: "test"})
	if err != nil {
		t.Fatalf("the call did not complete: %v", err)
	}

	mine, err := binding()
	if err != nil {
		t.Fatalf("the client captured no binding: %v", err)
	}
	if len(mine) != BindingLength {
		t.Errorf("the binding is %d bytes, want %d", len(mine), BindingLength)
	}
	if !bytes.Equal(mine, resp.GetChallenge()) {
		t.Errorf("the two ends exported different material:\n client %x\n server %x",
			mine, resp.GetChallenge())
	}
	if got, err := clone.Binding(); err != nil || !bytes.Equal(got, mine) {
		t.Errorf("a clone reports %x (%v), want the same binding as the original", got, err)
	}
}

// A binding of the wrong length is refused rather than returned,
// because an empty one is worse than useless: both ends would hash the
// same nothing and the handshake would succeed with no binding at all.
func TestBindingRefusesToAnswerBeforeAnyHandshake(t *testing.T) {
	t.Parallel()

	_, binding := ClientTLS()
	b, err := binding()
	if err == nil {
		t.Fatalf("Binding answered %x before any connection was made", b)
	}
	if !strings.Contains(err.Error(), "refusing") {
		t.Errorf("the refusal does not say it is refusing: %v", err)
	}
}

// The server's half fails the same way rather than silently binding to
// nothing, and it has two ways to be asked on a connection that cannot
// be bound to.
func TestBindingFromContextNeedsATLSPeer(t *testing.T) {
	t.Parallel()

	if _, err := BindingFromContext(context.Background()); err == nil {
		t.Error("a context with no peer produced a binding")
	}

	notTLS := peer.NewContext(context.Background(), &peer.Peer{AuthInfo: notTLSInfo{}})
	if _, err := BindingFromContext(notTLS); err == nil {
		t.Error("a connection that is not TLS produced a binding")
	} else if !strings.Contains(err.Error(), "not TLS") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

// A handshake that never completes must leave the slot empty rather
// than half filled, so that Binding refuses instead of answering with
// whatever was there.
func TestAFailedHandshakeCapturesNothing(t *testing.T) {
	t.Parallel()

	creds, binding := ClientTLS()
	here, there := net.Pipe()
	defer here.Close()
	defer there.Close()

	// Nobody is speaking TLS on the other end, so the handshake runs
	// out of time.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, _, err := creds.ClientHandshake(ctx, "test", here); err == nil {
		t.Fatal("a handshake against a pipe nobody answered succeeded")
	}
	if b, err := binding(); err == nil {
		t.Errorf("a failed handshake left a binding behind: %x", b)
	}
}
