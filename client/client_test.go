package client

// A daemon that is not there.
//
// Everything in client.go is one of two things: the handshake that
// proves a connection, and a translation of a gRPC call into a method
// somebody can call.  Neither can be reached without something at the
// far end, which is why this package was almost entirely untested --
// the only parts with tests were the two reassemblers, which are pure
// message handling and need no connection at all.
//
// So there is a real gRPC server here, in this process, on loopback.
// Dial takes dial options for precisely that, and passing them skips
// the shared-secret handshake; the handshake itself gets a second
// server, over TLS, with a secret in a home directory the test owns.
// What none of it can stand in for is a grid: the daemon here has no
// simulator behind it, so what is checked is the conversation with the
// daemon and not what a real session would have answered.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/internal/auth"
	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

var (
	testAgentID   = msg.MustParseUUID("3ac37e57-7e57-c0de-5607-527da8fa08de")
	testSessionID = msg.MustParseUUID("72427e57-7e57-c0de-39d2-e78c47465eb6")
	theOther      = msg.MustParseUUID("97c27e57-7e57-c0de-c041-be2c2f8cb586")
)

// fakeDaemon is a slgod with nothing behind it.
//
// It answers from fields, records what the client put on the stream,
// and relays what a test pushes down it.  That is all a client can tell
// about a daemon anyway: every answer a real one gives came from a
// simulator this one is not obliged to have.
type fakeDaemon struct {
	pb.UnimplementedGridServer

	info *pb.AgentInfo

	// attached is the name each Stream was opened with, sent is
	// everything the client put on it, and relay is what to push back.
	attached chan string
	sent     chan *pb.ClientPacket
	relay    chan *pb.ServerPacket

	agents   []*pb.AgentInfo
	status   *pb.StatusResponse
	presence *pb.PresenceResponse
	objects  []*pb.ObjectInfo
	region   *pb.RegionInfo
	friends  []*pb.Friend
	host     *pb.HostResponse
	logout   *pb.LogoutResponse
	capResp  *pb.CapResponse

	// capAnswers, when set, answers the capability requests in turn
	// before capResp does, and capsAsked and statusAsked count what
	// was asked; all three under askMu.
	askMu       sync.Mutex
	capAnswers  []*pb.CapResponse
	capsAsked   int
	statusAsked int

	// locked answers a lock request, since whoever asked for one is
	// waiting on the stream for it.  Answering nothing is a daemon too
	// old to know what a lock is, which is a wait rather than a refusal.
	locked func(*pb.Lock) *pb.Locked

	// slotted answers a request for places the same way.
	slotted func(*pb.Slots) *pb.SlotsGranted

	// auth is the server's half of the handshake, for a daemon that is
	// dialled rather than handed an open connection, and the three
	// fields after it are the ways that exchange goes wrong.
	auth           *auth.Server
	shortChallenge bool
	refuseProof    bool
	wrongProof     bool

	// loginMu guards the three that follow: loggedIn is the TLS
	// sessions that finished the handshake, which the interceptor in
	// authDaemon checks every other method against as slgod does,
	// logins counts them, and denyLogin refuses the proof from now on.
	loginMu   sync.Mutex
	loggedIn  map[string]bool
	logins    int
	denyLogin bool

	// attachFail refuses the attach outright -- a daemon not holding
	// the session that was asked for -- and answerAttachWith replaces
	// the Attached frame with something else, which is a daemon
	// answering a question it was not asked.
	attachFail       error
	answerAttachWith *pb.ServerPacket

	// hangUp ends the stream as soon as it is attached, which is a
	// daemon going away under a client that is watching it, and
	// hangUpOnLock does it once a lock has been asked for, which is one
	// going away under a client waiting its turn.
	hangUp       bool
	hangUpOnLock bool

	// fail, when set, is what every unary call answers with, which is
	// how the error half of each translation is reached.
	fail error
}

func newFakeDaemon() *fakeDaemon {
	return &fakeDaemon{
		info: &pb.AgentInfo{
			Name: "quark", AgentId: testAgentID.String(), SessionId: testSessionID.String(),
			AvatarName: "Quark Idlemind", Region: "Test Region",
			Caps: []string{"SimulatorFeatures", "ViewerAsset"},
		},
		attached: make(chan string, 4),
		sent:     make(chan *pb.ClientPacket, 64),
		relay:    make(chan *pb.ServerPacket, 16),
	}
}

// serve puts a daemon on loopback and answers with its address.
func (d *fakeDaemon) serve(t *testing.T, opts ...grpc.ServerOption) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening on loopback: %v", err)
	}
	srv := grpc.NewServer(opts...)
	pb.RegisterGridServer(srv, d)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

// dialFake connects to one without attaching, for the tests that are
// about the attach itself or about a call that does not need a stream.
func dialFake(t *testing.T) (*fakeDaemon, *Conn) {
	t.Helper()
	d := newFakeDaemon()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := Dial(ctx, d.serve(t), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return d, conn
}

// attachFake is dialFake with the stream already open, which is what
// most of these need: the stream is where messages, events, notices and
// locks all arrive.
func attachFake(t *testing.T) (*fakeDaemon, *Conn) {
	t.Helper()
	d, conn := dialFake(t)
	if _, err := conn.Attach(context.Background(), "quark", "*"); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	return d, conn
}

// authDaemon starts one a client may DIAL rather than one handed an
// open connection, with a secret in a home directory of the test's own.
//
// The difference is the handshake: Dial with no options does the real
// thing, TLS and a challenge each way over a shared secret read from
// disk, and that is a good half of client.go.  It writes to $HOME, so
// nothing using it may run in parallel with anything else that does.
func authDaemon(t *testing.T) (*fakeDaemon, string) {
	t.Helper()
	const secret = "a shared secret for a test"
	writeSecret(t, secret)

	a, err := auth.New(secret)
	if err != nil {
		t.Fatal(err)
	}
	d := newFakeDaemon()
	d.auth = a

	creds, err := auth.ServerTLS()
	if err != nil {
		t.Fatal(err)
	}
	return d, d.serve(t, grpc.Creds(creds), grpc.UnaryInterceptor(d.requireLogin))
}

// requireLogin refuses every method but Login on a TLS session that has
// not finished the handshake, as server.AuthInterceptors does.
func (d *fakeDaemon) requireLogin(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
	if info.FullMethod != pb.Grid_Login_FullMethodName {
		b, err := auth.BindingFromContext(ctx)
		d.loginMu.Lock()
		ok := err == nil && d.loggedIn[string(b)]
		d.loginMu.Unlock()
		if !ok {
			return nil, status.Error(codes.Unauthenticated,
				"not authenticated: call Login on this connection first")
		}
	}
	return h(ctx, req)
}

// loginCount is how many handshakes the daemon has finished.
func (d *fakeDaemon) loginCount() int {
	d.loginMu.Lock()
	defer d.loginMu.Unlock()
	return d.logins
}

// writeSecret puts a shared secret where LoadSecret will find it, in a
// home directory belonging to the test.
func writeSecret(t *testing.T, secret string) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "slrun")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
}

func (d *fakeDaemon) Stream(s grpc.BidiStreamingServer[pb.ClientPacket, pb.ServerPacket]) error {
	first, err := s.Recv()
	if err != nil {
		return err
	}
	att := first.GetAttach()
	if att == nil {
		return errors.New("the first packet was not an attach")
	}
	if d.attachFail != nil {
		return d.attachFail
	}
	d.attached <- att.Agent

	answer := d.answerAttachWith
	if answer == nil {
		answer = &pb.ServerPacket{Body: &pb.ServerPacket_Attached{
			Attached: &pb.Attached{Agent: d.info},
		}}
	}
	if err := s.Send(answer); err != nil {
		return err
	}
	if d.hangUp {
		return nil
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			p, err := s.Recv()
			if err != nil {
				return
			}
			select {
			case d.sent <- p:
			default:
			}
			if l := p.GetLock(); l != nil {
				if d.hangUpOnLock {
					return
				}
				if d.locked != nil {
					s.Send(&pb.ServerPacket{Body: &pb.ServerPacket_Locked{Locked: d.locked(l)}})
				}
			}
			if sl := p.GetSlots(); sl != nil && d.slotted != nil {
				s.Send(&pb.ServerPacket{Body: &pb.ServerPacket_Granted{Granted: d.slotted(sl)}})
			}
		}
	}()

	for {
		select {
		case p := <-d.relay:
			if err := s.Send(p); err != nil {
				return err
			}
		case <-done:
			return nil
		case <-s.Context().Done():
			return s.Context().Err()
		}
	}
}

// Login is the daemon's half of the handshake, which is the server's
// own code with the connection bookkeeping left out and each way of
// getting it wrong available on a field.
func (d *fakeDaemon) Login(ctx context.Context, req *pb.LoginRequest) (*pb.LoginResponse, error) {
	binding, err := auth.BindingFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if len(req.GetProof()) == 0 {
		if d.fail != nil {
			return nil, d.fail
		}
		challenge, err := d.auth.Begin(auth.UnpackName([4]uint64{
			req.GetClient_0(), req.GetClient_1(), req.GetClient_2(), req.GetClient_3(),
		}))
		if err != nil {
			return nil, err
		}
		if d.shortChallenge {
			challenge = challenge[:len(challenge)-1]
		}
		return &pb.LoginResponse{Challenge: challenge}, nil
	}
	d.loginMu.Lock()
	deny := d.denyLogin
	d.loginMu.Unlock()
	if d.refuseProof || deny {
		return nil, errors.New("that is not the secret")
	}
	proof, _, err := d.auth.Answer(req.GetChallenge(), req.GetProof(), binding)
	if err != nil {
		return nil, err
	}
	d.loginMu.Lock()
	if d.loggedIn == nil {
		d.loggedIn = map[string]bool{}
	}
	d.loggedIn[string(binding)] = true
	d.logins++
	d.loginMu.Unlock()
	if d.wrongProof {
		proof = append([]byte(nil), proof...)
		proof[0]++
	}
	return &pb.LoginResponse{Proof: proof}, nil
}

func (d *fakeDaemon) ListAgents(context.Context, *pb.ListAgentsRequest) (*pb.ListAgentsResponse, error) {
	if d.fail != nil {
		return nil, d.fail
	}
	return &pb.ListAgentsResponse{Agents: d.agents}, nil
}

func (d *fakeDaemon) Status(context.Context, *pb.StatusRequest) (*pb.StatusResponse, error) {
	d.askMu.Lock()
	d.statusAsked++
	d.askMu.Unlock()
	if d.fail != nil {
		return nil, d.fail
	}
	return d.status, nil
}

func (d *fakeDaemon) Host(_ context.Context, r *pb.HostRequest) (*pb.HostResponse, error) {
	if d.fail != nil {
		return nil, d.fail
	}
	out := d.host
	if out == nil {
		out = &pb.HostResponse{Agent: &pb.AgentInfo{Name: r.Agent}, Already: !r.Force}
	}
	return out, nil
}

func (d *fakeDaemon) Logout(_ context.Context, r *pb.LogoutRequest) (*pb.LogoutResponse, error) {
	if d.fail != nil {
		return d.logout, d.fail
	}
	out := d.logout
	if out == nil {
		out = &pb.LogoutResponse{}
		if !r.Force {
			out.Clients = []string{"a benchmark mid-run"}
		}
	}
	return out, nil
}

func (d *fakeDaemon) Presence(_ context.Context, r *pb.PresenceRequest) (*pb.PresenceResponse, error) {
	if d.fail != nil {
		return nil, d.fail
	}
	out := d.presence
	if out != nil && r.DrawDistance > 0 {
		out.DrawDistance = r.DrawDistance
	}
	return out, nil
}

func (d *fakeDaemon) Objects(_ context.Context, r *pb.ObjectsRequest) (*pb.ObjectsResponse, error) {
	if d.fail != nil {
		return nil, d.fail
	}
	return &pb.ObjectsResponse{Objects: d.objects}, nil
}

func (d *fakeDaemon) Region(context.Context, *pb.RegionRequest) (*pb.RegionInfo, error) {
	if d.fail != nil {
		return nil, d.fail
	}
	return d.region, nil
}

func (d *fakeDaemon) Flush(context.Context, *pb.FlushRequest) (*pb.FlushResponse, error) {
	if d.fail != nil {
		return nil, d.fail
	}
	return &pb.FlushResponse{Forgotten: int32(len(d.objects))}, nil
}

func (d *fakeDaemon) Friends(context.Context, *pb.FriendsRequest) (*pb.FriendsResponse, error) {
	if d.fail != nil {
		return nil, d.fail
	}
	return &pb.FriendsResponse{Friends: d.friends}, nil
}

func (d *fakeDaemon) NoteFriend(_ context.Context, r *pb.NoteFriendRequest) (*pb.NoteFriendResponse, error) {
	if d.fail != nil {
		return nil, d.fail
	}
	select {
	case d.sent <- &pb.ClientPacket{}:
	default:
	}
	return &pb.NoteFriendResponse{}, nil
}

func (d *fakeDaemon) Cap(context.Context, *pb.CapRequest) (*pb.CapResponse, error) {
	if d.fail != nil {
		return nil, d.fail
	}
	d.askMu.Lock()
	defer d.askMu.Unlock()
	d.capsAsked++
	if len(d.capAnswers) > 0 {
		r := d.capAnswers[0]
		d.capAnswers = d.capAnswers[1:]
		return r, nil
	}
	return d.capResp, nil
}

// asked is how many capability requests and status requests arrived.
func (d *fakeDaemon) asked() (caps, status int) {
	d.askMu.Lock()
	defer d.askMu.Unlock()
	return d.capsAsked, d.statusAsked
}

func (d *fakeDaemon) Send(context.Context, *pb.SendRequest) (*pb.SendResponse, error) {
	if d.fail != nil {
		return nil, d.fail
	}
	return &pb.SendResponse{}, nil
}

// ------------------------------------------------------------ messages

// TestAMessageThisBuildHasNeverHeardOfIsStillDelivered: the server
// relays what it does not understand to a client subscribed to "*", so
// that a client can handle a message added to the protocol after the
// server was built.  Decoding one is nil and NOT an error, and the bytes
// are there to look at.
func TestAMessageThisBuildHasNeverHeardOfIsStillDelivered(t *testing.T) {
	t.Parallel()
	unknown := msg.MakeID(msg.FreqLow, 65530)
	if info := msg.Lookup(unknown); info != nil {
		t.Fatalf("%d is %s in this build, so it is not the case being tested", unknown, info.Name)
	}

	m := &Message{ID: unknown, Name: "SomethingNewer", Body: []byte("bytes to look at")}
	v, err := m.Decode()
	if v != nil || err != nil {
		t.Errorf("Decode = %v, %v, want nothing and no complaint", v, err)
	}
	if string(m.Body) != "bytes to look at" {
		t.Errorf("the body was lost: %q", m.Body)
	}
}

// TestAMessageThatWillNotDecodeSaysSo: the number is known and the
// bytes are wrong, which is a different thing from a number this build
// does not have -- one is the protocol moving on, the other is a bug or
// a corrupted relay, and reporting both as nil would hide the second.
func TestAMessageThatWillNotDecodeSaysSo(t *testing.T) {
	t.Parallel()
	chat := &msg.ChatFromSimulator{}
	m := &Message{ID: msg.IDOf(chat), Name: "ChatFromSimulator", Body: []byte{1}}
	if v, err := m.Decode(); err == nil {
		t.Errorf("Decode = %v, %v, want a complaint about the bytes", v, err)
	}
}

// TestReliableIsReadOffTheFlagsTheServerRelayed: the server has already
// acknowledged it, so this is only a client asking what the simulator
// wanted -- but a client that resends what it thinks was unreliable
// would be duplicating what the server already handled.
func TestReliableIsReadOffTheFlagsTheServerRelayed(t *testing.T) {
	t.Parallel()
	if (&Message{Flags: msg.FlagReliable}).Reliable() != true {
		t.Error("a reliable message says it is not")
	}
	if (&Message{}).Reliable() != false {
		t.Error("a message with no flags says it is reliable")
	}
}

// TestAnEventBodyIsLLSDAndNotTheMessageEncoding: events come off the
// grid's event queue, which is a different encoding entirely -- which
// is why they are not on Messages, and why decoding one that is not
// LLSD has to be reported rather than guessed at.
func TestAnEventBodyIsLLSDAndNotTheMessageEncoding(t *testing.T) {
	t.Parallel()
	e := &Event{Name: "TeleportFinish", Body: []byte(
		`<?xml version="1.0" ?><llsd><map><key>region</key><string>Test Region</string></map></llsd>`)}
	got, err := e.Decode()
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got["region"] != "Test Region" {
		t.Errorf("the event decoded to %v", got)
	}

	if _, err := (&Event{Body: []byte("not llsd at all")}).Decode(); err == nil {
		t.Error("a body that is not LLSD decoded without complaint")
	}
}

// ----------------------------------------------------------- the dial

// TestDialWithOptionsSkipsTheHandshake: the options are what a test
// brings its own server with, and a connection made that way must not
// go looking for a shared secret on disk -- there is none, and the
// server on the other end is not asking for one.
func TestDialWithOptionsSkipsTheHandshake(t *testing.T) {
	t.Parallel()
	_, conn := dialFake(t)
	if conn.Err() != nil {
		t.Errorf("Err = %v on a fresh connection", conn.Err())
	}
	select {
	case <-conn.Done():
		t.Error("a fresh connection says it has ended")
	default:
	}
	if err := conn.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	select {
	case <-conn.Done():
	case <-time.After(5 * time.Second):
		t.Error("a closed connection never said it had ended")
	}
	if conn.Err() != nil {
		t.Errorf("Err = %v after an ordinary Close", conn.Err())
	}
}

// TestDialRefusesAnAddressItCannotEvenParse: the address usually comes
// from a config file or a command line, and one that is not an address
// at all has to be refused here rather than turned into a connection
// that fails later for a reason that does not mention it.
func TestDialRefusesAnAddressItCannotEvenParse(t *testing.T) {
	t.Parallel()
	if _, err := Dial(context.Background(), "unix://\x00"); err == nil {
		t.Error("Dial accepted an address containing a control character")
	}
}

// TestTheHandshakeProvesBothEnds: a client that proves itself to a
// server it has not checked has proved nothing -- the certificate is
// self-signed and means nothing, so what stands in for it is the
// server's own half of the exchange, tied to this TLS session.
//
// It cannot run in parallel: the secret has to be in $HOME for
// LoadSecret to find it, which is process-wide.
func TestTheHandshakeProvesBothEnds(t *testing.T) {
	d, addr := authDaemon(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, err := Dial(ctx, addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	// And it is a working connection afterwards, rather than merely one
	// that got through the handshake.
	d.agents = []*pb.AgentInfo{{Name: "quark"}}
	got, err := conn.ListAgents(ctx)
	if err != nil || len(got) != 1 {
		t.Errorf("ListAgents = %v, %v", got, err)
	}
}

// ---------------------------------------- a connection that comes back

// idleDial dials the real handshake with a channel that goes idle after
// a moment, and waits until it has, which closes its transport.  It
// returns how long that took.
func idleDial(t *testing.T, ctx context.Context, addr string) (*Conn, time.Duration) {
	t.Helper()
	conn, err := dial(ctx, addr, true, grpc.WithIdleTimeout(300*time.Millisecond))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	start := time.Now()
	for s := conn.cc.GetState(); s != connectivity.Idle; s = conn.cc.GetState() {
		if !conn.cc.WaitForStateChange(ctx, s) {
			t.Fatalf("the channel never went idle: still %v", s)
		}
	}
	return conn, time.Since(start)
}

// TestACallAfterTheChannelWentIdleLogsInAgain: the channel's transport
// is closed when it goes idle and the next call opens another, which
// slgod has not seen log in.  Without the client's own re-login that
// call is refused for ever.
// Why: doc/client.md#a-connection-that-comes-back
func TestACallAfterTheChannelWentIdleLogsInAgain(t *testing.T) {
	d, addr := authDaemon(t)
	d.agents = []*pb.AgentInfo{{Name: "quark"}}
	// The idle wait is 300 ms; 60 s is far past anything a handshake
	// and a call take, so it only ends a hang.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// A TLS session that never logged in is refused, which is what the
	// idle channel's next transport is.
	creds, _ := auth.ClientTLS()
	raw, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := pb.NewGridClient(raw).ListAgents(ctx, &pb.ListAgentsRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("a transport that never logged in got %v, want Unauthenticated", err)
	}

	conn, took := idleDial(t, ctx, addr)
	t.Logf("the channel went idle after %v", took)
	if got, err := conn.ListAgents(ctx); err != nil || len(got) != 1 {
		t.Fatalf("ListAgents after idle = %v, %v", got, err)
	}
	if n := d.loginCount(); n != 2 {
		t.Errorf("the daemon saw %d logins, want the first and one more", n)
	}
}

// TestCallsRefusedTogetherCauseOneLogin: every call that was waiting on
// the lost transport is refused at once, and each logging in again
// would be a handshake apiece.
func TestCallsRefusedTogetherCauseOneLogin(t *testing.T) {
	d, addr := authDaemon(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn, _ := idleDial(t, ctx, addr)

	const callers = 8
	start := make(chan struct{})
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() {
			<-start
			_, err := conn.ListAgents(ctx)
			errs <- err
		}()
	}
	close(start)
	for i := 0; i < callers; i++ {
		if err := <-errs; err != nil {
			t.Errorf("a caller got %v", err)
		}
	}
	if n := d.loginCount(); n != 2 {
		t.Errorf("the daemon saw %d logins for %d callers, want the first and one more", n, callers)
	}
}

// TestALoginThatFailsAgainStillReadsAsUnauthenticated: callers and
// slbotd tell a lost login by the code, so logging in again failing must
// not change it.
func TestALoginThatFailsAgainStillReadsAsUnauthenticated(t *testing.T) {
	d, addr := authDaemon(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn, _ := idleDial(t, ctx, addr)

	d.loginMu.Lock()
	d.denyLogin = true
	d.loginMu.Unlock()
	_, err := conn.ListAgents(ctx)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("ListAgents = %v (%v), want Unauthenticated", err, status.Code(err))
	}
	if !strings.Contains(err.Error(), "logging in again") {
		t.Errorf("ListAgents = %v, want it to say the second login failed", err)
	}
}

// TestADaemonThatCannotProveItselfIsNotTheDaemon: this is the half that
// is easy to leave out, and leaving it out is what lets anything
// listening on the port drive somebody's avatar.  A wrong answer is a
// refusal, not a warning.
func TestADaemonThatCannotProveItselfIsNotTheDaemon(t *testing.T) {
	d, addr := authDaemon(t)
	d.wrongProof = true

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, err := Dial(ctx, addr)
	if err == nil {
		conn.Close()
		t.Fatal("Dial accepted a daemon that could not prove it knows the secret")
	}
	if !strings.Contains(err.Error(), "shared secret") {
		t.Errorf("Dial = %v, want it to say what was not proved", err)
	}
}

// TestTheHandshakeSaysWhichStepFailed: there are four ways for it to go
// wrong and they need different things done about them -- no secret
// file, a daemon that will not talk, a daemon that offers nonsense, and
// a daemon that refuses the proof.  One message for all four would send
// every reader to the wrong place.
func TestTheHandshakeSaysWhichStepFailed(t *testing.T) {
	t.Run("no secret on disk", func(t *testing.T) {
		// A home directory with nothing in it, which is every machine
		// that has not been set up yet.
		t.Setenv("HOME", t.TempDir())
		_, err := Dial(context.Background(), "127.0.0.1:1")
		if err == nil || !strings.Contains(err.Error(), "share this file") {
			t.Errorf("Dial = %v, want it to say the file is shared with slgod", err)
		}
	})

	t.Run("a daemon that will not begin", func(t *testing.T) {
		d, addr := authDaemon(t)
		d.fail = errors.New("no")
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := Dial(ctx, addr); err == nil || !strings.Contains(err.Error(), "login:") {
			t.Errorf("Dial = %v, want the first call named", err)
		}
	})

	t.Run("a challenge of the wrong size", func(t *testing.T) {
		d, addr := authDaemon(t)
		d.shortChallenge = true
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, err := Dial(ctx, addr)
		if err == nil || !strings.Contains(err.Error(), "byte challenge") {
			t.Errorf("Dial = %v, want the size it was offered", err)
		}
	})

	t.Run("a proof the daemon will not take", func(t *testing.T) {
		d, addr := authDaemon(t)
		d.refuseProof = true
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, err := Dial(ctx, addr)
		if err == nil || !strings.Contains(err.Error(), "login refused") {
			t.Errorf("Dial = %v, want the refusal named as one", err)
		}
	})
}

// ---------------------------------------------------------- the attach

// TestAttachingSaysWhichSessionItGot: an empty name is passed through
// rather than resolved here, because the daemon picks -- the session it
// has held longest that has not stopped -- and the Attached frame says
// which.
func TestAttachingSaysWhichSessionItGot(t *testing.T) {
	t.Parallel()
	d, conn := dialFake(t)

	info, err := conn.Attach(context.Background(), "", "ChatFromSimulator")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if got := <-d.attached; got != "" {
		t.Errorf("the daemon was asked for %q, want the choice left to it", got)
	}
	if info.Name != "quark" || conn.Info().Name != "quark" {
		t.Errorf("attached to %+v", info)
	}
	// The capabilities are kept as a set, because HasCap is asked far
	// more often than the list is read.
	if !conn.HasCap("ViewerAsset") || conn.HasCap("LSLSyntax") {
		t.Errorf("HasCap disagrees with %v", conn.Caps())
	}
	if len(conn.Caps()) != 2 {
		t.Errorf("Caps = %v", conn.Caps())
	}
}

// TestEveryCallNamesTheSessionTheAttachGot: an attach with no name takes
// the daemon's default, and every call afterwards names the session the
// Attached frame said it got.  Asking again by the empty name reached
// whichever session was the default by then -- another avatar entirely,
// once the first had been logged out -- while the stream stayed where it
// was.
func TestEveryCallNamesTheSessionTheAttachGot(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		what   string
		ask    string // the name attached with
		answer string // the name the Attached frame gives
		want   string // the name every call must carry
	}{
		{"an empty name takes what the daemon resolved", "", "quark", "quark"},
		// A daemon that names nothing leaves nothing better than what
		// was asked for.
		{"a frame with no name keeps the one asked for", "quark", "", "quark"},
	} {
		t.Run(tc.what, func(t *testing.T) {
			var mu sync.Mutex
			var asked []string
			record := grpc.UnaryInterceptor(func(ctx context.Context, req any,
				info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
				if r, ok := req.(interface{ GetAgent() string }); ok {
					mu.Lock()
					asked = append(asked, fmt.Sprintf("%s asked for %q", info.FullMethod, r.GetAgent()))
					mu.Unlock()
				}
				return next(ctx, req)
			})
			d := newFakeDaemon()
			d.info.Name = tc.answer
			d.status = &pb.StatusResponse{Agent: d.info}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			conn, err := Dial(ctx, d.serve(t, record), grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				t.Fatalf("Dial: %v", err)
			}
			t.Cleanup(func() { conn.Close() })
			if _, err := conn.Attach(ctx, tc.ask); err != nil {
				t.Fatalf("Attach: %v", err)
			}

			// Whether each is answered does not matter here, and most
			// are not: this daemon implements few of them.  What each
			// ASKED for is the point.
			conn.Status(ctx)
			conn.ViewerCredential(ctx)
			conn.Presence(ctx, 0)
			conn.Attachments(ctx, "")
			conn.Objects(ctx, "", "")
			conn.Friends(ctx)
			conn.NoteFriend(ctx, theOther, true)
			conn.Region(ctx)
			conn.Land(ctx)
			conn.Ground(ctx, 1, 2, 3, 4)
			conn.Neighbours(ctx, nil)
			conn.Control(ctx, 0)
			conn.Flush(ctx)
			conn.DoCap(ctx, agent.CapRequest{Cap: "SimulatorFeatures"})
			conn.Handled(ctx, "an offer", "", false)
			conn.Face(ctx, nil, 0)
			conn.Halt(ctx)
			conn.Posture(ctx)

			mu.Lock()
			defer mu.Unlock()
			if len(asked) < 18 {
				t.Errorf("%d calls reached the daemon, want 18: %v", len(asked), asked)
			}
			for _, a := range asked {
				if !strings.HasSuffix(a, fmt.Sprintf("asked for %q", tc.want)) {
					t.Errorf("%s, want %q", a, tc.want)
				}
			}
		})
	}
}

// TestAnAttachTheDaemonRefusedIsReported: the refusal arrives as the
// first Recv rather than as a failure of the Send, because the stream
// opens locally and the daemon only learns what was asked for when the
// attach reaches it.
func TestAnAttachTheDaemonRefusedIsReported(t *testing.T) {
	t.Parallel()
	d, conn := dialFake(t)
	d.attachFail = errors.New("no such agent")
	if _, err := conn.Attach(context.Background(), "somebody else"); err == nil {
		t.Error("Attach succeeded against a daemon that refused it")
	}
}

// TestAnAttachAnsweredWithSomethingElseIsNotAnAttach: the first frame
// is the whole of what a client learns about who it is talking to, so
// taking anything that arrives would leave a connection that believes
// it is attached to nobody.
func TestAnAttachAnsweredWithSomethingElseIsNotAnAttach(t *testing.T) {
	t.Parallel()
	d, conn := dialFake(t)
	d.answerAttachWith = &pb.ServerPacket{Body: &pb.ServerPacket_Notice{
		Notice: &pb.AgentEvent{Kind: pb.AgentEvent_REGION_CHANGED},
	}}
	_, err := conn.Attach(context.Background(), "quark")
	if err == nil || !strings.Contains(err.Error(), "answered attach with") {
		t.Errorf("Attach = %v, want it to say what arrived instead", err)
	}
}

// TestAnAttachThatNeverLeftIsNotWaitedFor: the stream opens locally and
// says nothing about the far end, so the attach frame is the first
// thing that can be refused -- and one refused on the way out must not
// leave the caller waiting on a Recv for an answer to a question that
// was never asked.
//
// The refusal here is a send limit small enough that the frame will not
// fit, which is the one way to fail a Send without a race: everything
// else that breaks a stream breaks the Stream call first.
func TestAnAttachThatNeverLeftIsNotWaitedFor(t *testing.T) {
	t.Parallel()
	d := newFakeDaemon()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := Dial(ctx, d.serve(t),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallSendMsgSize(1)))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	if _, err := conn.Attach(ctx, "quark", "ChatFromSimulator"); err == nil {
		t.Error("Attach reported success for a frame that never went out")
	}
}

// TestAttachingOnAConnectionThatIsClosingCannotOpenAStream: Close is
// what a program does on its way out, and a call racing it must fail
// rather than block on a stream that will never be answered.
func TestAttachingOnAConnectionThatIsClosingCannotOpenAStream(t *testing.T) {
	t.Parallel()
	_, conn := dialFake(t)
	conn.Close()
	if _, err := conn.Attach(context.Background(), "quark"); err == nil {
		t.Error("Attach opened a stream on a closed connection")
	}
}

// TestTheStreamEndingIsRememberedAsWhy: everything reading this
// connection finds out the same way -- the channels close -- and the
// reason has to be kept somewhere, because by then there is nothing
// left to ask.
func TestTheStreamEndingIsRememberedAsWhy(t *testing.T) {
	t.Parallel()
	d, conn := dialFake(t)
	d.hangUp = true
	if _, err := conn.Attach(context.Background(), "quark"); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	select {
	case <-conn.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the receive loop never noticed the stream end")
	}
	if conn.Err() == nil {
		t.Error("Err says nothing about a stream that ended under it")
	}
	// The channels are closed rather than merely idle, so a range over
	// Messages finishes instead of hanging.
	if _, ok := <-conn.Messages(); ok {
		t.Error("Messages was still open after the stream ended")
	}
	if _, ok := <-conn.Events(); ok {
		t.Error("Events was still open after the stream ended")
	}
	if _, ok := <-conn.Notices(); ok {
		t.Error("Notices was still open after the stream ended")
	}
}

// TestEverythingTheDaemonRelaysArrivesOnItsOwnChannel: all six kinds
// come down one stream and they are not interchangeable -- a message is
// the binary encoding, an event is LLSD, a notice is about the
// connection rather than about the grid, a handled offer is one some
// client has dealt with, and the answers to a lock and to a request for
// places belong to whoever is waiting on them.
func TestEverythingTheDaemonRelaysArrivesOnItsOwnChannel(t *testing.T) {
	t.Parallel()
	d, conn := attachFake(t)
	ctx := context.Background()

	// A lock and a request for places, each waiting for its answer.
	locked := make(chan error, 1)
	go func() { locked <- conn.Lock(ctx, "the workbench") }()
	lock := waitForLock(t, d)
	granted := make(chan grantResult, 1)
	go func() {
		g, err := conn.Slots(ctx, 1, time.Minute, "")
		granted <- grantResult{g, err}
	}()
	slots := waitForSlots(t, d)

	at := time.Now().UnixMicro()
	message := func(seq uint32) *pb.ServerPacket {
		say := &msg.ChatFromSimulator{}
		body, err := say.Encode()
		if err != nil {
			t.Fatal(err)
		}
		return &pb.ServerPacket{Body: &pb.ServerPacket_Message{Message: &pb.InboundMessage{
			Id: uint32(msg.IDOf(say)), Name: "ChatFromSimulator", Body: body,
			Sequence: seq, Flags: msg.FlagReliable, ReceivedAt: at,
		}}}
	}
	d.relay <- message(42)
	d.relay <- &pb.ServerPacket{Body: &pb.ServerPacket_Event{Event: &pb.InboundEvent{
		Message: "TeleportFinish", Body: []byte("some llsd"), ReceivedAt: at,
	}}}
	d.relay <- &pb.ServerPacket{Body: &pb.ServerPacket_Notice{Notice: &pb.AgentEvent{
		Kind: pb.AgentEvent_DISCONNECTED, Detail: "the circuit went away",
	}}}
	d.relay <- &pb.ServerPacket{Body: &pb.ServerPacket_Handled{Handled: &pb.OfferHandled{
		Offer: "offer 7", How: "accepted", By: "slsh",
	}}}
	d.relay <- &pb.ServerPacket{Body: &pb.ServerPacket_Locked{Locked: &pb.Locked{
		Name: lock.Name, Held: true, Request: lock.Request,
	}}}
	d.relay <- grantFor(slots.Request, "g1", "")
	// A second message last.  The stream is read in order, so once it is
	// out everything before it has been put wherever it went.
	d.relay <- message(43)

	for _, seq := range []uint32{42, 43} {
		select {
		case m := <-conn.Messages():
			if m.Name != "ChatFromSimulator" || m.Sequence != seq || !m.Reliable() {
				t.Errorf("the relayed message came out as %+v, want sequence %d", m, seq)
			}
			if m.At.UnixMicro() != at {
				t.Errorf("the arrival time came out as %v", m.At)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("message %d never came back on Messages", seq)
		}
	}

	select {
	case e := <-conn.Events():
		if e.Name != "TeleportFinish" || string(e.Body) != "some llsd" {
			t.Errorf("the relayed event came out as %+v", e)
		}
		if e.At.UnixMicro() != at {
			t.Errorf("the arrival time came out as %v", e.At)
		}
	default:
		t.Error("nothing came back on Events")
	}

	select {
	case n := <-conn.Notices():
		if n.Kind != pb.AgentEvent_DISCONNECTED || n.Detail != "the circuit went away" {
			t.Errorf("the relayed notice came out as %+v", n)
		}
	default:
		t.Error("nothing came back on Notices")
	}

	select {
	case h := <-conn.HandledOffers():
		if h.Offer != "offer 7" || h.How != "accepted" || h.By != "slsh" {
			t.Errorf("the handled offer came out as %+v", h)
		}
	default:
		t.Error("nothing came back on HandledOffers")
	}

	select {
	case err := <-locked:
		if err != nil {
			t.Errorf("Lock = %v, want it held", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("the lock's answer never reached the Lock waiting for it")
	}
	select {
	case r := <-granted:
		if r.err != nil || r.g.ID != "g1" {
			t.Errorf("Slots = %+v, %v, want grant g1", r.g, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Error("the grant never reached the Slots waiting for it")
	}

	// And nothing arrived anywhere else as well.
	select {
	case m := <-conn.Messages():
		t.Errorf("Messages had something else on it: %+v", m)
	case e := <-conn.Events():
		t.Errorf("Events had something else on it: %+v", e)
	case n := <-conn.Notices():
		t.Errorf("Notices had something else on it: %+v", n)
	case h := <-conn.HandledOffers():
		t.Errorf("HandledOffers had something else on it: %+v", h)
	case c := <-conn.RegionChanges():
		t.Errorf("RegionChanges had something on it: %+v", c)
	default:
	}
}

// TestARelayWithNoArrivalTimeIsNotTheEpoch: the field is optional on
// the wire, and taking zero at face value would date every such message
// to 1970 -- which is worse than having no time at all, because it
// looks like an answer.
func TestARelayWithNoArrivalTimeIsNotTheEpoch(t *testing.T) {
	t.Parallel()
	d, conn := attachFake(t)
	d.relay <- &pb.ServerPacket{Body: &pb.ServerPacket_Message{
		Message: &pb.InboundMessage{Id: 1, Name: "Older"},
	}}
	d.relay <- &pb.ServerPacket{Body: &pb.ServerPacket_Event{
		Event: &pb.InboundEvent{Message: "AlsoOlder"},
	}}
	select {
	case m := <-conn.Messages():
		if !m.At.IsZero() {
			t.Errorf("a message with no time is dated %v", m.At)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing came back on Messages")
	}
	select {
	case e := <-conn.Events():
		if !e.At.IsZero() {
			t.Errorf("an event with no time is dated %v", e.At)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing came back on Events")
	}
}

// ------------------------------------------------------ subscriptions

// TestSubscribingReplacesAddsAndRemovesSeparately: three calls onto one
// message, and the field each fills is the whole difference between
// them -- a Watch that arrived as a Set would throw away everything the
// client was already listening for.
func TestSubscribingReplacesAddsAndRemovesSeparately(t *testing.T) {
	t.Parallel()
	d, conn := attachFake(t)

	// Naming nothing is the call that clears the subscription; what
	// reaches the daemon is the subject of the test below.
	if err := conn.Subscribe(); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	s := waitForSubscribe(t, d)
	if len(s.Set) != 0 || len(s.Add) != 0 || len(s.Remove) != 0 {
		t.Errorf("clearing the subscription sent %+v", s)
	}

	if err := conn.Subscribe("ChatFromSimulator"); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if s = waitForSubscribe(t, d); len(s.Set) != 1 || s.Set[0] != "ChatFromSimulator" {
		t.Errorf("Set = %v", s.Set)
	}

	if err := conn.Watch("ObjectUpdate"); err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if s = waitForSubscribe(t, d); len(s.Add) != 1 || s.Set != nil {
		t.Errorf("Watch sent %+v, want an addition and nothing else", s)
	}

	if err := conn.Unwatch("ObjectUpdate"); err != nil {
		t.Fatalf("Unwatch: %v", err)
	}
	if s = waitForSubscribe(t, d); len(s.Remove) != 1 || s.Set != nil {
		t.Errorf("Unwatch sent %+v, want a removal and nothing else", s)
	}
}

// TestClearingASubscriptionSurvivesTheWire: a client that names nothing
// must stop receiving what it was receiving.
//
// This used to be sent as an empty Set and nothing else, on the theory
// that a Set of nothing stays distinguishable from no Set at all.  It
// does not: proto3 writes an empty repeated field as no field, so what
// arrived was nil either way, and the daemon -- which wipes the
// subscription only when Set is non-nil -- quietly changed nothing, on
// a link already being flooded.  Replace is the bit an empty list
// cannot erase.
func TestClearingASubscriptionSurvivesTheWire(t *testing.T) {
	t.Parallel()
	d, conn := attachFake(t)

	if err := conn.Subscribe(); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if s := waitForSubscribe(t, d); !s.GetReplace() {
		t.Errorf("clearing the subscription reached the daemon as %+v, "+
			"which it cannot tell from being left alone", s)
	}
}

// waitForSubscribe reads the next subscription change off the stream.
func waitForSubscribe(t *testing.T, d *fakeDaemon) *pb.Subscribe {
	t.Helper()
	for {
		select {
		case p := <-d.sent:
			if s := p.GetSubscribe(); s != nil {
				return s
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the daemon was never told about the subscription")
			return nil
		}
	}
}

// TestSubscribingBeforeAttachingHasNoStreamToSayItOn: the subscription
// belongs to the stream, so there is nothing to remember it on and
// pretending otherwise would silently drop it.
func TestSubscribingBeforeAttachingHasNoStreamToSayItOn(t *testing.T) {
	t.Parallel()
	_, conn := dialFake(t)
	if err := conn.Subscribe("ChatFromSimulator"); err == nil {
		t.Error("Subscribe was accepted before anything was attached")
	}
	if err := conn.Watch("ObjectUpdate"); err == nil {
		t.Error("Watch was accepted before anything was attached")
	}
	if err := conn.Unwatch("ObjectUpdate"); err == nil {
		t.Error("Unwatch was accepted before anything was attached")
	}
}

// --------------------------------------------------------------- send

// TestSendGoesOnTheStreamOnceThereIsOne: a message put on the stream
// keeps its order with everything else this client is doing, which the
// unary call cannot promise -- and the unary call exists for the client
// that has not attached and only wants to say one thing.
func TestSendGoesOnTheStreamOnceThereIsOne(t *testing.T) {
	t.Parallel()
	d, conn := dialFake(t)

	// No stream yet: this goes as its own call, and the daemon answers
	// it rather than the stream reader picking it up.
	say := &msg.ChatFromViewer{}
	say.ChatData.Message = append([]byte("hello"), 0)
	if err := conn.Send(context.Background(), say, true); err != nil {
		t.Fatalf("Send with no stream: %v", err)
	}

	if _, err := conn.Attach(context.Background(), "quark"); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := conn.Send(context.Background(), say, true); err != nil {
		t.Fatalf("Send: %v", err)
	}
	for {
		select {
		case p := <-d.sent:
			out := p.GetMessage()
			if out == nil {
				continue
			}
			if msg.ID(out.Id) != msg.IDOf(say) || !out.Reliable {
				t.Errorf("the daemon was sent %+v", out)
			}
			return
		case <-time.After(5 * time.Second):
			t.Fatal("nothing reached the daemon on the stream")
		}
	}
}

// TestAMessageThatWillNotEncodeNeverReachesTheWire: Send builds the
// bytes here rather than on the server, which is what lets the server
// relay something it has never heard of -- and it means a message the
// caller filled in wrongly has to be caught here or not at all.
func TestAMessageThatWillNotEncodeNeverReachesTheWire(t *testing.T) {
	t.Parallel()
	_, conn := dialFake(t)

	// A Variable block carries its count in one byte, so 256 of
	// anything cannot be said at all.
	m := &msg.ObjectName{}
	m.ObjectData = make([]msg.ObjectName_ObjectData, 256)
	if err := conn.Send(context.Background(), m, true); err == nil {
		t.Error("a message that cannot be encoded was sent anyway")
	}
}

// TestSendOnAStreamThatHasGoneReportsIt: the stream is where a client's
// sends keep their order, so losing it is not something to carry on
// past -- the message did not go, and the caller is the only one who
// can decide what to do about that.
func TestSendOnAStreamThatHasGoneReportsIt(t *testing.T) {
	t.Parallel()
	_, conn := attachFake(t)
	conn.Close()

	err := conn.SendRaw(context.Background(), msg.ID(1), []byte("hello"), true)
	if err == nil {
		t.Error("SendRaw reported success on a stream that had been closed")
	}
}

// TestSendWithoutAStreamReportsWhatTheDaemonSaid: the unary path is the
// only one that can be refused synchronously, since a stream Send is
// buffered and says nothing about what happened to it.
func TestSendWithoutAStreamReportsWhatTheDaemonSaid(t *testing.T) {
	t.Parallel()
	d, conn := dialFake(t)
	d.fail = errors.New("no such agent")
	if err := conn.SendRaw(context.Background(), msg.ID(1), nil, false); err == nil {
		t.Error("SendRaw reported success from a daemon that refused")
	}
}

// ------------------------------------------------------ what it holds

// TestEveryDaemonCallAnswersOrSaysWhyNot: each of these is a one-line
// translation, and the failing half of each matters more than the
// working half -- a call that swallowed the error would answer with a
// zero value that reads as "nothing there" rather than "did not ask".
func TestEveryDaemonCallAnswersOrSaysWhyNot(t *testing.T) {
	t.Parallel()
	d, conn := attachFake(t)
	ctx := context.Background()

	d.agents = []*pb.AgentInfo{{Name: "quark"}, {Name: "somebody else"}}
	d.status = &pb.StatusResponse{Agent: &pb.AgentInfo{Name: "quark"}, Clients: 3}
	d.presence = &pb.PresenceResponse{Region: "Test Region"}
	d.objects = []*pb.ObjectInfo{{Id: theOther.String()}}
	d.region = &pb.RegionInfo{Name: "Test Region", Known: true}
	d.friends = []*pb.Friend{{Id: theOther.String(), Online: true}}
	d.capResp = &pb.CapResponse{Status: 200, Body: []byte("some llsd")}

	if got, err := conn.ListAgents(ctx); err != nil || len(got) != 2 {
		t.Errorf("ListAgents = %v, %v", got, err)
	}
	if got, err := conn.Status(ctx); err != nil || got.Clients != 3 {
		t.Errorf("Status = %v, %v", got, err)
	}
	if got, err := conn.Presence(ctx, 128); err != nil || got.DrawDistance != 128 {
		t.Errorf("Presence = %v, %v", got, err)
	}
	if got, err := conn.Objects(ctx, "", ""); err != nil || len(got.Objects) != 1 {
		t.Errorf("Objects = %v, %v", got, err)
	}
	if got, err := conn.Region(ctx); err != nil || got.Name != "Test Region" {
		t.Errorf("Region = %v, %v", got, err)
	}
	if got, err := conn.Flush(ctx); err != nil || got != 1 {
		t.Errorf("Flush = %v, %v", got, err)
	}
	if got, err := conn.Friends(ctx); err != nil || len(got) != 1 {
		t.Errorf("Friends = %v, %v", got, err)
	}
	if err := conn.NoteFriend(ctx, theOther, true); err != nil {
		t.Errorf("NoteFriend: %v", err)
	}
	if got, err := conn.Host(ctx, "quark", false); err != nil || !got.Already {
		t.Errorf("Host = %v, %v", got, err)
	}
	if got, err := conn.Logout(ctx, "quark", true); err != nil || len(got.Clients) != 0 {
		t.Errorf("Logout = %v, %v", got, err)
	}
	if got, err := conn.DoCap(ctx, agent.CapRequest{Cap: "SimulatorFeatures"}); err != nil ||
		got.Status != 200 || string(got.Body) != "some llsd" {
		t.Errorf("DoCap = %+v, %v", got, err)
	}

	d.fail = errors.New("the daemon is going down")
	if _, err := conn.ListAgents(ctx); err == nil {
		t.Error("ListAgents answered from a daemon that refused")
	}
	if _, err := conn.Status(ctx); err == nil {
		t.Error("Status answered from a daemon that refused")
	}
	if _, err := conn.Presence(ctx, 0); err == nil {
		t.Error("Presence answered from a daemon that refused")
	}
	if _, err := conn.Objects(ctx, "", ""); err == nil {
		t.Error("Objects answered from a daemon that refused")
	}
	if _, err := conn.Region(ctx); err == nil {
		t.Error("Region answered from a daemon that refused")
	}
	if _, err := conn.Flush(ctx); err == nil {
		t.Error("Flush reported on a daemon that refused")
	}
	if _, err := conn.Friends(ctx); err == nil {
		t.Error("Friends answered from a daemon that refused")
	}
	if err := conn.NoteFriend(ctx, theOther, true); err == nil {
		t.Error("NoteFriend recorded a friendship with a daemon that refused")
	}
	if _, err := conn.Host(ctx, "quark", false); err == nil {
		t.Error("Host reported on a daemon that refused")
	}
	if _, err := conn.DoCap(ctx, agent.CapRequest{Cap: "x"}); err == nil {
		t.Error("DoCap answered from a daemon that refused")
	}
	// Logout is the one that answers with a response AND an error
	// together, because the refusal names who is using the session.
	if _, err := conn.Logout(ctx, "quark", false); err == nil {
		t.Error("Logout reported on a daemon that refused")
	}
}

// TestHangingUpDoesNotCloseTheRelayUnderItsSender: Close must never
// close a channel recvLoop is still sending on.
//
// It used to.  Close closed messages, events and notices itself while
// recvLoop sat in the select that sends to them, which is a data race
// and, when the timing fell the other way, a panic on send to a closed
// channel -- on the one path every client takes to hang up.  recvLoop
// closes them on its way out now, and Close waits for it.
//
// The flood is what makes it reproducible rather than occasional: the
// thing being raced is a sender parked in that select, so there has to
// be one.  The loop is for the same reason -- a single hang-up finds it
// perhaps one time in ten under -race, and twenty do not all miss.
func TestHangingUpDoesNotCloseTheRelayUnderItsSender(t *testing.T) {
	say := &msg.ChatFromSimulator{}
	body, err := say.Encode()
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 20; i++ {
		d, conn := attachFake(t)

		stop, flooded := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(flooded)
			for {
				select {
				case d.relay <- &pb.ServerPacket{Body: &pb.ServerPacket_Message{
					Message: &pb.InboundMessage{
						Id: uint32(msg.IDOf(say)), Name: "ChatFromSimulator", Body: body,
					},
				}}:
				case <-stop:
					return
				}
			}
		}()

		// Hang up while it is actually arriving, not before it starts.
		select {
		case <-conn.Messages():
		case <-time.After(5 * time.Second):
			t.Fatal("the daemon relayed nothing to hang up in the middle of")
		}
		conn.Close()

		close(stop)
		<-flooded

		// And the promise Close keeps: whoever is ranging over the relay
		// is given an end to range to, before Close returns.
		for range conn.Messages() {
		}
	}
}

// TestARegionChangeArrivesOnBothChannels: it is a notice like any
// other, and it is also the one a session cannot afford to miss.  Two
// things want it -- something showing a person what happened, and the
// bookkeeping that drops everything keyed on the region left behind --
// and a notice stream has one reader, so sifting it out of that one
// would make the second steal from the first.
func TestARegionChangeArrivesOnBothChannels(t *testing.T) {
	t.Parallel()
	d, conn := attachFake(t)

	// Sandbox Goguen's handle on Agni.
	const goguen = uint64(1094014069892352)
	d.relay <- &pb.ServerPacket{Body: &pb.ServerPacket_Notice{Notice: &pb.AgentEvent{
		Kind:         pb.AgentEvent_REGION_CHANGED,
		Detail:       "the avatar is now in Sandbox Goguen",
		Region:       "Sandbox Goguen",
		RegionHandle: goguen,
	}}}

	select {
	case c := <-conn.RegionChanges():
		if c.Region != "Sandbox Goguen" || c.Handle != goguen {
			t.Errorf("the region change came out as %+v", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing came back on RegionChanges")
	}

	select {
	case n := <-conn.Notices():
		if n.Kind != pb.AgentEvent_REGION_CHANGED || n.Region != "Sandbox Goguen" {
			t.Errorf("the notice came out as %+v", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a region change was taken off Notices by the channel beside it")
	}
}

// TestOnlyARegionChangeIsOne: the notice stream carries disconnections
// too, and a session that took one of those for a move would throw away
// everything it holds every time the circuit blinked.
func TestOnlyARegionChangeIsOne(t *testing.T) {
	t.Parallel()
	d, conn := attachFake(t)

	d.relay <- &pb.ServerPacket{Body: &pb.ServerPacket_Notice{Notice: &pb.AgentEvent{
		Kind: pb.AgentEvent_DISCONNECTED, Detail: "the circuit went away",
	}}}
	select {
	case <-conn.Notices():
	case <-time.After(5 * time.Second):
		t.Fatal("nothing came back on Notices")
	}
	select {
	case c := <-conn.RegionChanges():
		t.Errorf("a disconnection came out as a region change: %+v", c)
	default:
	}
}

// TestARegionChangeSaysWhatMovedTheAvatarInAWord: the first cause the
// flags carry, in a fixed order, and nothing for what has none.
func TestARegionChangeSaysWhatMovedTheAvatarInAWord(t *testing.T) {
	for _, c := range []struct {
		flags uint32
		want  string
	}{
		{0, ""},
		{agent.TeleportViaHome, "home"},
		{agent.TeleportViaLure, "lure"},
		{agent.TeleportViaLandmark, "landmark"},
		{agent.TeleportViaLocation, "location"},
		{agent.TeleportViaGodlikeLure, "god"},
		{agent.TeleportGodlike, "god"},
		{agent.TeleportForceRedirect, "forced"},
		// Ejected from land and sent home: the measured combination,
		// 34848, says where it went before it says it was forced.
		{agent.TeleportForceRedirect | agent.TeleportDisableCancel | agent.TeleportViaHome, "home"},
		{agent.TeleportViaLandmark | agent.TeleportSetLastToTarget, "landmark"},
		// A cancel button's flag, a telehub, a login and a region by its
		// id are not causes this names.
		{agent.TeleportDisableCancel, ""},
		{agent.TeleportViaTelehub, ""},
		{agent.TeleportViaLogin, ""},
		{agent.TeleportViaRegionID, ""},
	} {
		if got := (&RegionChange{TeleportFlags: c.flags}).Cause(); got != c.want {
			t.Errorf("flags %#x: cause %q, want %q", c.flags, got, c.want)
		}
	}
}
