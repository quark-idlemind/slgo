package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/quark-idlemind/slgo/internal/auth"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
	"github.com/quark-idlemind/slgo/sl"
)

// Keeping an avatar attended to is a small state machine with two rules
// worth being exact about: a session that drops is asked for again, and
// a session somebody stopped on purpose is not.  Getting the second one
// wrong means a daemon fighting a person for their own avatar, which is
// the fault the deliberate logout exists to prevent.

// fakeSlgod stands in for host and attach, two of the daemon's calls to
// slgod.
type fakeSlgod struct {
	mu sync.Mutex

	hosted  []string // every name asked for, in order
	forced  []bool   // whether each ask said force
	hostErr error
	deliber bool

	attached int
	attachFn func() (*sl.Session, error)
}

func withFakeSlgod(t *testing.T) (*daemon, *bot, *fakeSlgod) {
	t.Helper()
	d, b, _ := newTestDaemon(t)
	b.setSession(nil)
	b.setState(stateStarting, "")

	f := &fakeSlgod{}
	d.host = func(ctx context.Context, name string, force bool) (bool, error) {
		f.mu.Lock()
		f.hosted = append(f.hosted, name)
		f.forced = append(f.forced, force)
		err, deliberate := f.hostErr, f.deliber
		f.mu.Unlock()
		return deliberate, err
	}
	d.attach = func(ctx context.Context, name string) (*sl.Session, error) {
		f.mu.Lock()
		f.attached++
		fn := f.attachFn
		f.mu.Unlock()
		if fn != nil {
			return fn()
		}
		return nil, fmt.Errorf("nothing to attach to")
	}
	return d, b, f
}

func (f *fakeSlgod) asks() ([]string, []bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.hosted...), append([]bool(nil), f.forced...)
}

// A session that came up and then ended is asked for again: that is
// what an attendant is for.
func TestASessionThatEndsIsAskedForAgain(t *testing.T) {
	d, b, f := withFakeSlgod(t)
	f.attachFn = func() (*sl.Session, error) {
		g := newFakeGrid()
		s, err := sl.New(g)
		if err != nil {
			return nil, err
		}
		// It ends the moment it is served, which is the shape of a
		// session dropping.
		go func() {
			time.Sleep(10 * time.Millisecond)
			g.Close()
		}()
		return s, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); b.run(ctx) }()

	deadline := time.Now().Add(15 * time.Second)
	for {
		names, _ := f.asks()
		if len(names) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("asked for the session %d times, wanted it asked for again", len(names))
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	_ = d
}

// A deliberate logout stops the asking.  Without this the attendant
// would go on asking for ever, and would undo the one thing the logout
// was for.
func TestADeliberateLogoutStopsTheAsking(t *testing.T) {
	_, b, f := withFakeSlgod(t)
	f.deliber = true
	f.hostErr = status.Error(codes.FailedPrecondition,
		"example was stopped deliberately (logged out on request)")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); b.run(ctx) }()

	waitState(t, b, stateHeldBack)

	// And it stays stopped: nothing asks again on its own.
	asked, _ := f.asks()
	time.Sleep(500 * time.Millisecond)
	again, _ := f.asks()
	if len(again) != len(asked) {
		t.Errorf("asked %d times and then %d; it should have stopped asking", len(asked), len(again))
	}

	// Until somebody says to force it, which is what ":host --force"
	// does.  The force travels with the next ask and is used once.
	f.mu.Lock()
	f.deliber, f.hostErr = false, nil
	f.mu.Unlock()
	b.Wake(true)

	deadline := time.Now().Add(10 * time.Second)
	for {
		_, forced := f.asks()
		if len(forced) > len(asked) {
			if !forced[len(asked)] {
				t.Error("the ask after a forced wake did not say force")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a forced wake did not make it ask again")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
}

// The force is spent on one attempt.  A flag that stayed set would turn
// every later retry into one that overrules a person, which is exactly
// what it was added not to do.
func TestForceIsUsedOnce(t *testing.T) {
	_, b, _ := withFakeSlgod(t)
	b.Wake(true)
	if !b.takeForce() {
		t.Fatal("the force was not carried")
	}
	if b.takeForce() {
		t.Error("the force survived being used")
	}
}

// An avatar with no session answers a command by saying so and saying
// why, since "nothing happened" is the least useful thing a daemon can
// send back.
func TestAnAttendantSaysWhyItHasNoSession(t *testing.T) {
	_, b, _ := withFakeSlgod(t)
	b.setState(stateHeldBack, "stopped deliberately")
	_, err := b.Need()
	if err == nil {
		t.Fatal("a bot with no session handed one over")
	}
	if got := err.Error(); !strings.Contains(got, "example") ||
		!strings.Contains(got, "stopped deliberately") {
		t.Errorf("err = %q", got)
	}
}

func waitState(t *testing.T, b *bot, want state) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if got, _ := b.State(); got == want {
			return
		}
		if time.Now().After(deadline) {
			got, detail := b.State()
			t.Fatalf("state is %q (%s), waited for %q", got, detail, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// An avatar stopped on purpose and then started again by a person
// should be picked up without anybody having to tell this daemon so.
//
// The logout half of that already worked -- the attendant stops asking
// when slgod says the session was stopped deliberately.  The login half
// did not: it waited to be told, and nothing ever told it.  Measured on
// a live daemon: a forced logout and then a login left slgod holding the
// avatar and slbotd detached from it indefinitely.
func TestAnAvatarStartedAgainIsPickedUp(t *testing.T) {
	_, b, f := withFakeSlgod(t)
	f.deliber = true
	f.hostErr = status.Error(codes.FailedPrecondition,
		"example was stopped deliberately (logged out on request)")

	// slgod says it is stopped, until it does not.
	var stopped atomic.Bool
	stopped.Store(true)
	b.d.host = func(ctx context.Context, name string, force bool) (bool, error) {
		f.mu.Lock()
		f.hosted = append(f.hosted, name)
		f.forced = append(f.forced, force)
		f.mu.Unlock()
		if stopped.Load() {
			return true, status.Error(codes.FailedPrecondition, "stopped deliberately")
		}
		return false, nil
	}
	b.d.agents = func(ctx context.Context) ([]*pb.AgentInfo, error) {
		st := pb.AgentInfo_HOSTED
		if stopped.Load() {
			st = pb.AgentInfo_STOPPED
		}
		return []*pb.AgentInfo{{Name: "example", State: st}}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); b.run(ctx) }()

	waitState(t, b, stateHeldBack)
	asked, _ := f.asks()

	// It is not arguing with whoever stopped it.
	time.Sleep(3 * HeldRecheck / 2)
	again, _ := f.asks()
	if len(again) > len(asked) {
		t.Errorf("asked to host %d more times while it was stopped on purpose",
			len(again)-len(asked))
	}

	// And when a person starts it again, it notices on its own.
	stopped.Store(false)
	deadline := time.Now().Add(15 * time.Second)
	for {
		if n, _ := f.asks(); len(n) > len(again) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("it never noticed the avatar had been started again")
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
}

// loginSlgod is a slgod over TLS that does the handshake and then
// answers every Host with err, which is all hostThroughSlgod needs of it.
type loginSlgod struct {
	pb.UnimplementedGridServer
	a   *auth.Server
	err error
}

func (s *loginSlgod) Login(ctx context.Context, req *pb.LoginRequest) (*pb.LoginResponse, error) {
	if len(req.GetProof()) == 0 {
		c, err := s.a.Begin("test")
		return &pb.LoginResponse{Challenge: c}, err
	}
	bind, err := auth.BindingFromContext(ctx)
	if err != nil {
		return nil, err
	}
	proof, _, err := s.a.Answer(req.GetChallenge(), req.GetProof(), bind)
	return &pb.LoginResponse{Proof: proof}, err
}

func (s *loginSlgod) Host(context.Context, *pb.HostRequest) (*pb.HostResponse, error) {
	return nil, s.err
}

// A control connection slgod refuses as not logged in is dropped, like
// one it cannot be reached over, so the next host dials and logs in
// afresh; a refusal that is slgod answering leaves it be.
// Why: doc/client.md#a-connection-that-comes-back
func TestAControlConnectionRefusedAsNotLoggedInIsDropped(t *testing.T) {
	// The handshake reads its secret from $HOME, so this cannot be parallel.
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "slrun")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret"), []byte("a secret for a test"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	a, err := auth.New("a secret for a test")
	if err != nil {
		t.Fatal(err)
	}
	creds, err := auth.ServerTLS()
	if err != nil {
		t.Fatal(err)
	}
	slgod := &loginSlgod{a: a}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.Creds(creds))
	pb.RegisterGridServer(srv, slgod)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	d, _, _ := newTestDaemon(t)
	d.addr = lis.Addr().String()
	t.Cleanup(d.closeControl)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	slgod.err = status.Error(codes.FailedPrecondition, "logged out on purpose")
	if deliberate, _ := d.hostThroughSlgod(ctx, "example", false); !deliberate {
		t.Fatal("a deliberate refusal was not reported as one")
	}
	if d.ctl == nil {
		t.Fatal("slgod answering dropped the control connection")
	}

	slgod.err = status.Error(codes.Unauthenticated, "not authenticated: call Login on this connection first")
	if _, err := d.hostThroughSlgod(ctx, "example", false); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("hostThroughSlgod = %v, want the refusal passed on", err)
	}
	if d.ctl != nil {
		t.Error("the control connection was kept after slgod refused it as not logged in")
	}
}
