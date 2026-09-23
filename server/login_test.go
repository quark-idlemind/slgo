package server

// Authentication, end to end, because half of it cannot be seen from
// either side alone.
//
// The handshake is mutual and bound to the TLS session, so a test that
// called Login with values it made up would prove nothing: the binding
// only exists once a real TLS 1.3 session has been established, and the
// whole point of the exercise is that a proof made on one connection is
// worthless on another.  So there is a real server here, with real
// credentials, dialled by the real client -- which is also the only way
// the interceptors are reached at all, since they refuse on the
// connection rather than on the request.
//
// It writes a secret into a $HOME of its own, which is why nothing in
// this file may run in parallel with anything else that does.

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/auth"
	"github.com/quark-idlemind/slgo/client"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

const testSecret = "a shared secret for a test"

// secretInATestHome puts the shared secret where both ends will look
// for it, in a home directory belonging to the test rather than to
// whoever is running it -- the developer's own slgod credentials are on
// this disk and must not be touched.
func secretInATestHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "slrun")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret"), []byte(testSecret), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
}

// authRig is newRig with authentication turned on, served the way the
// daemon serves it: TLS, the interceptors, and per-connection state.
// Anything in setup is done to the server before it starts serving.
func authRig(t *testing.T, setup ...func(*Server)) *rig {
	t.Helper()
	secretInATestHome(t)

	a, err := auth.New(testSecret)
	if err != nil {
		t.Fatal(err)
	}

	// Settled before anything is listening: Serve decides then whether
	// to put TLS and the interceptors in front of the service, and a
	// server told afterwards would have been serving without them.
	r := newSession(t, agent.Caps{})
	r.srv.SetAuth(a)
	for _, f := range setup {
		f(r.srv)
	}
	r.serve(t)
	return r
}

// TestAnAuthenticatedClientIsKnownByName is what the handshake is for
// beyond keeping strangers out: the connection remembers who proved
// itself, so the daemon can say "slgo is using it" rather than "1
// client".
func TestAnAuthenticatedClientIsKnownByName(t *testing.T) {
	r := authRig(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// No dial options at all, which is what makes Dial do the real
	// thing: TLS, a challenge each way, and the secret read from disk.
	c, err := client.Dial(ctx, r.ln.Addr().String())
	if err != nil {
		t.Fatalf("dialling an authenticating daemon: %v", err)
	}
	defer c.Close()

	if _, err := c.Attach(ctx, "example"); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if _, err := c.Status(ctx); err != nil {
		t.Fatalf("a call on an authenticated connection was refused: %v", err)
	}

	h, _ := r.srv.Agent("example")
	waitFor(t, 5*time.Second, "the client to be attached", func() bool {
		return h.ClientCount() == 1
	})
	// The program, which copy of it, and where it is speaking from:
	// "slsh[1234]@198.51.100.7".  Under test the program is the test
	// binary, so the name is matched loosely and the shape exactly.
	who := h.clientNames()
	if len(who) != 1 || !strings.Contains(who[0], "[") || !strings.Contains(who[0], "@127.0.0.1") {
		t.Errorf("attached clients are %v, want name[pid]@address", who)
	}
	mine := who[0]

	// And that name is what a refusal to log the session out names, so
	// that a person knows what they are about to interrupt.  It comes
	// back twice over: in the sentence, and as a field somebody can act
	// on without reading English.
	//
	// The field is the part that took work.  Returning it on a
	// LogoutResponse beside the status did not reach here at all -- a
	// unary call carries a message or a status -- so it travels in the
	// status details, which is where the two can go together.
	got, err := c.Logout(ctx, "example", false)
	if status.Code(err) != codes.FailedPrecondition || !strings.Contains(errText(err), mine) {
		t.Errorf("logout refusal = %v; want a FailedPrecondition naming %s", err, mine)
	}
	if who := got.GetClients(); len(who) != 1 || who[0] != mine {
		t.Errorf("the refusal came back naming %v, want %s", who, mine)
	}
}

// TestAnUnnamedClientIsStillReported: a server without authentication
// has nothing to call its clients, and the refusal has to say something
// -- a count would leave the operator no wiser about what they are
// interrupting.
func TestAnUnnamedClientIsStillReported(t *testing.T) {
	r := newRig(t, agent.Caps{})
	c := r.dial(t)
	defer c.Close()

	h, _ := r.srv.Agent("example")
	waitFor(t, 5*time.Second, "the client to be attached", func() bool {
		return h.ClientCount() == 1
	})
	// No name to be had, but the address still is: the connection
	// tracker runs whether or not authentication does, because where a
	// client is speaking from is not a secret.
	if who := h.clientNames(); len(who) != 1 || !strings.HasPrefix(who[0], "an unnamed client") {
		t.Errorf("attached clients are %v on an unauthenticated server", who)
	}
}

// TestNothingButLoginRunsBeforeTheHandshake is the whole of the
// enforcement: it hangs on the connection, so a caller that skips the
// handshake is refused whatever it asks for and however it asks.
func TestNothingButLoginRunsBeforeTheHandshake(t *testing.T) {
	r := authRig(t)

	creds, _ := auth.ClientTLS()
	cc, err := grpc.NewClient(r.ln.Addr().String(), grpc.WithTransportCredentials(creds))
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	grid := pb.NewGridClient(cc)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := grid.Status(ctx, &pb.StatusRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Errorf("a unary call before the handshake: %v; want Unauthenticated", err)
	}

	s, err := grid.Stream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(&pb.ClientPacket{Body: &pb.ClientPacket_Attach{
		Attach: &pb.Attach{Agent: "example"},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Recv(); status.Code(err) != codes.Unauthenticated {
		t.Errorf("a stream before the handshake: %v; want Unauthenticated", err)
	}

	// A proof that is not one is refused with the same error as every
	// other failure, on purpose: which half went wrong is a fact an
	// attacker would like and a legitimate client does not need.
	begun, err := grid.Login(ctx, &pb.LoginRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(begun.GetChallenge()) != auth.ChallengeSize {
		t.Fatalf("the server offered a %d byte challenge", len(begun.GetChallenge()))
	}
	_, err = grid.Login(ctx, &pb.LoginRequest{
		Challenge: make([]byte, auth.ChallengeSize),
		Proof:     []byte("this is not the secret"),
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("a wrong proof: %v; want Unauthenticated", err)
	}
}

// TestLoginRefusesWhatItCannotBindTo: without a TLS session there is no
// keying material, and authenticating over a channel nothing is bound
// to is the exact trap the binding exists to close -- so it refuses
// rather than proceeding with an empty one.
func TestLoginRefusesWhatItCannotBindTo(t *testing.T) {
	t.Parallel()

	a, err := auth.New(testSecret)
	if err != nil {
		t.Fatal(err)
	}
	s := New()
	s.SetAuth(a)

	// A context with no peer at all, which is what any caller reaching
	// this method other than over TLS would have.
	_, err = s.Login(context.Background(), &pb.LoginRequest{})
	if status.Code(err) != codes.FailedPrecondition ||
		!strings.Contains(errText(err), "cannot bind") {
		t.Errorf("login without TLS = %v; want FailedPrecondition saying it cannot bind", err)
	}

	// And a server that was never given a secret says so rather than
	// letting the caller think it authenticated.
	_, err = New().Login(context.Background(), &pb.LoginRequest{})
	if status.Code(err) != codes.FailedPrecondition ||
		!strings.Contains(errText(err), "without authentication") {
		t.Errorf("login on an unauthenticated server = %v", err)
	}
}

// TestAProvedConnectionMustHaveSomewhereToRecordIt: the connection is
// what authentication belongs to, so a server serving TLS without the
// per-connection state has nowhere to put the answer -- and must say so
// rather than accept a handshake it cannot remember.
func TestAProvedConnectionMustHaveSomewhereToRecordIt(t *testing.T) {
	secretInATestHome(t)

	a, err := auth.New(testSecret)
	if err != nil {
		t.Fatal(err)
	}
	s := New()
	s.SetAuth(a)

	creds, err := auth.ServerTLS()
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately without the stats handler Serve installs, which is
	// the only hook whose context every RPC on a connection descends
	// from.
	g := grpc.NewServer(grpc.Creds(creds))
	pb.RegisterGridServer(g, s)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go g.Serve(ln)
	defer g.Stop()

	clientCreds, binding := auth.ClientTLS()
	cc, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(clientCreds))
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	grid := pb.NewGridClient(cc)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	begun, err := grid.Login(ctx, &pb.LoginRequest{})
	if err != nil {
		t.Fatal(err)
	}
	bind, err := binding()
	if err != nil {
		t.Fatal(err)
	}
	_, err = grid.Login(ctx, &pb.LoginRequest{
		Challenge: make([]byte, auth.ChallengeSize),
		Proof:     auth.ClientProof(testSecret, begun.GetChallenge(), bind),
	})
	if status.Code(err) != codes.Internal {
		t.Errorf("a proved handshake with nowhere to record it = %v; want Internal", err)
	}
}

// sleeps replaces the throttle's sleep with one that records how long
// it would have been and returns at once, so that a test of a delay
// neither takes the delay nor has to guess at it from a stopwatch.
type sleeps struct {
	mu sync.Mutex
	d  []time.Duration
}

func (sl *sleeps) install(s *Server) {
	s.throttle.Sleep = func(_ context.Context, d time.Duration) error {
		sl.mu.Lock()
		sl.d = append(sl.d, d)
		sl.mu.Unlock()
		return nil
	}
}

func (sl *sleeps) take() []time.Duration {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	out := sl.d
	sl.d = nil
	return out
}

// guess is one attempt at the handshake without the secret, on a
// connection of its own, answered with this proof.  It returns the
// error the second call came back with.
func guess(t *testing.T, addr string, proof []byte) error {
	t.Helper()
	creds, _ := auth.ClientTLS()
	cc, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	grid := pb.NewGridClient(cc)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := grid.Login(ctx, &pb.LoginRequest{}); err != nil {
		t.Fatalf("beginning a login: %v", err)
	}
	_, err = grid.Login(ctx, &pb.LoginRequest{
		Challenge: make([]byte, auth.ChallengeSize),
		Proof:     proof,
	})
	return err
}

// TestWrongProofsFromOneAddressAreSlowedAfterThree: a wrong answer cost
// a guesser one round trip and nothing else.  Now the fourth from one
// address waits, and each one after it longer -- counted by address, so
// that hanging up and connecting again, which every guess here does,
// does not start the count again.  The right answer from that address
// waits too, or the speed of the answer would give it away, and then
// the address is forgiven.
func TestWrongProofsFromOneAddressAreSlowedAfterThree(t *testing.T) {
	var slept sleeps
	r := authRig(t, slept.install)
	addr := r.ln.Addr().String()

	wrong := make([]byte, auth.ProofSize)
	for i := 0; i < auth.FreeFailures+2; i++ {
		if err := guess(t, addr, wrong); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("guess %d = %v, want Unauthenticated", i+1, err)
		}
	}
	if got := slept.take(); len(got) != 2 || got[0] >= got[1] {
		t.Errorf("five wrong guesses, each on a new connection, waited %v; want the last two, the second longer", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := client.Dial(ctx, addr)
	if err != nil {
		t.Fatalf("a client with the secret, after the guessing: %v", err)
	}
	c.Close()
	if got := slept.take(); len(got) != 1 {
		t.Errorf("the right answer after the guessing waited %v; want it slowed like a wrong one", got)
	}

	c, err = client.Dial(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if got := slept.take(); len(got) != 0 {
		t.Errorf("an address that has proved itself still waits: %v", got)
	}
}

// TestAProofOfTheWrongLengthIsNotAGuess: a proof has one length that
// could ever verify, so anything else is refused before any work is
// done with it -- including counting it, since it was never a guess at
// anything.
func TestAProofOfTheWrongLengthIsNotAGuess(t *testing.T) {
	var slept sleeps
	r := authRig(t, slept.install)
	addr := r.ln.Addr().String()

	for _, proof := range [][]byte{
		make([]byte, auth.ProofSize+1), make([]byte, auth.ProofSize-1),
		make([]byte, 4<<10), []byte("this is not the secret"),
	} {
		if err := guess(t, addr, proof); status.Code(err) != codes.Unauthenticated {
			t.Errorf("a %d byte proof = %v, want Unauthenticated", len(proof), err)
		}
	}
	if err := guess(t, addr, make([]byte, auth.ProofSize)); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("a wrong proof = %v, want Unauthenticated", err)
	}
	if got := slept.take(); len(got) != 0 {
		t.Errorf("after four malformed proofs and one wrong one, waited %v; the malformed were counted", got)
	}
}

// TestAClientFromBeforeTheNameMovedIsToldItIsTooOld: the name was a
// string, field 1, and is four fixed words now.  A client built before
// that is refused whatever it does, and being refused in the words of
// a wrong secret would send somebody to check a secret that is fine.
func TestAClientFromBeforeTheNameMovedIsToldItIsTooOld(t *testing.T) {
	a, err := auth.New(testSecret)
	if err != nil {
		t.Fatal(err)
	}
	s := New()
	s.SetAuth(a)

	// What an old client put on the wire: its name as field 1, and
	// its pid, which has not moved.
	var wire []byte
	wire = protowire.AppendTag(wire, 1, protowire.BytesType)
	wire = protowire.AppendString(wire, "slsh")
	wire = protowire.AppendTag(wire, 4, protowire.VarintType)
	wire = protowire.AppendVarint(wire, 1234)
	var req pb.LoginRequest
	if err := proto.Unmarshal(wire, &req); err != nil {
		t.Fatal(err)
	}

	_, err = s.Login(context.Background(), &req)
	if status.Code(err) != codes.FailedPrecondition || !strings.Contains(errText(err), "older than slgod") {
		t.Errorf("an old client's login = %v; want FailedPrecondition saying it is too old", err)
	}

	// And a new client that happens to send no name is not mistaken
	// for one: it gets as far as the binding, which this call has none
	// of.
	_, err = s.Login(context.Background(), &pb.LoginRequest{Pid: 1234})
	if strings.Contains(errText(err), "older") {
		t.Errorf("a new client with no name was told it is too old: %v", err)
	}
}
