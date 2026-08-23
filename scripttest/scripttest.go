// Package scripttest is a Runner backend that runs no LSL.
//
// proto/script.proto is the seam between a program that wants LSL run --
// slrun, slbench -- and whatever runs it: the eLSL simulator, a
// viewer driven from outside, or a client holding a real grid session.
// Everything on the caller's side of that seam is about LSL and about
// what a script said, and none of it needs a grid to be exercised.  This
// is what stands in for the grid while that is done.
//
// It answers the contract in full and offline: a pool of objects with
// leases and a queue, a compiler that has opinions, and a script that
// says something.  What it says is worked out from the source, by the
// same staircase slbench's --test model uses (see Memory), so a
// benchmark run against this backend has to come out with the numbers
// the model says -- and a search that reads the staircase wrongly fails
// here rather than after twenty minutes of grid time.
//
// # What it is not
//
// It is not a simulator.  It does not parse LSL, it has no notion of
// what a statement costs, and every number it produces is the model's
// invention.  What can be tested through it is what a CALLER does with
// what it is told -- which readings it takes, which failures it backs
// off from, whether it gives its objects back -- and never what Second
// Life would have said.  Capabilities.Grid is false and stays false for
// that reason: a test that would do something it would not do in public
// checks that flag.
//
// # Why it is a real server
//
// Everything here would be shorter as a Go value implementing
// RunnerClient directly, and it would test less.  Over a connection --
// Pipe gives one, in process and with no network -- the messages are
// marshalled, the streams are real streams, and a caller that goes away
// mid-lease goes away the way a caller really does: the server's context
// is cancelled and it has to notice.  That last one is the whole reason
// the lease is a stream, and an in-process fake cannot fail it.
//
// # And why there is an in-process client anyway
//
// Direct is that shorter thing, and the argument above is why it is an
// addition rather than a replacement.  It is for the caller that runs
// scripts by the hundred thousand and is measuring something else -- the
// offline model behind slbench --test, and the sweeps in its tests,
// which cost 100 microseconds a run over the pipe and about 5 through
// Direct, nearly all of the difference being goroutine hand-off for the
// seven messages a run streams.  What it cannot do is what Pipe is for:
// a caller cannot DIE, having no connection to lose, and a run arrives
// when it has finished rather than as it goes, so a caller cannot cancel
// one in reaction to what it has heard.  Those tests stay on Pipe.
// Everything else is checked against both clients, so a divergence
// between them fails the build rather than waiting to be noticed.
//
// # Who it is for
//
// Our own tests, and anybody writing a backend: the tests in this
// package are what the contract means in practice, and a new backend
// that disagrees with them disagrees with slbench.
package scripttest

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/quark-idlemind/slgo/proto/scriptv1"
)

// Options is what the backend is made of.  The zero value is a working
// backend: one avatar, one group of four objects, and a model with no
// code in it.
type Options struct {
	// Backend and Version are what Health reports.  They are for logs
	// and no caller should change what it does based on them.
	Backend string
	Version string

	// Agents names the avatars whose objects the pool holds.  Empty is
	// one avatar called "test", which is what a caller that does not
	// care about avatars gets.
	Agents []string

	// Groups is how many groups each avatar has and GroupSize how many
	// objects are in one.  The defaults are one group of four, four
	// being what slbench takes at once: one object to measure in and
	// three to take readings in.
	Groups    int
	GroupSize int

	// Memory is the model.
	Memory Memory

	// NotReady, when set, makes Health report ready=false with this as
	// the reason, and every Lease and Run fail.  It stands in for a
	// backend that is up but has nothing to run scripts in yet -- a
	// viewer not started, a session still logging in.
	NotReady string

	// MaxConcurrentRuns bounds how many runs happen at once, and is what
	// Capabilities reports.  A run past the bound WAITS rather than
	// failing: the number was published, and a backend that refused
	// would make its own capability a trap.  Zero means as many as there
	// are objects.
	MaxConcurrentRuns int

	// NoCompileOnly and NoPersistence turn off capabilities, so that a
	// caller can be tested against a backend that lacks them.  Asking
	// for compile_only where it is off is an error, as the contract
	// says; without persistence an object forgets what a script left in
	// it and every run starts from nothing.
	NoCompileOnly bool
	NoPersistence bool

	// Second is what one second of wait_seconds and timeout_seconds
	// means here.
	//
	// The contract counts both in whole seconds, which is right for a
	// grid and useless for a test: the shortest timeout expressible is a
	// second, and a test of the timeout path would then take one.  With
	// Second set to a millisecond the same request takes a millisecond
	// and tests exactly the same code.  Zero is a real second, so a
	// backend served to somebody else behaves as the contract says.
	Second time.Duration

	// DefaultTimeout is how many seconds a run gets when the request
	// names none.  Zero is 30.  There has to be one: a script that never
	// says the sentinel would otherwise run until the caller gave up,
	// and a test that hangs says less than one that fails.
	DefaultTimeout int64

	// Noise, when set, is asked what a benchmark run's reading should be
	// and may answer something other than the truth.
	//
	// It is here because llGetUsedMemory really does this.  On 2026-08-03
	// the one-copy reference script at pad 602 read 6436 bytes where it
	// reads 5924 every other time it has been asked -- one whole block
	// high, once, and never again in the 45 asks since.  A padding search
	// is a chain of comparisons between readings, and a reading one block
	// high looks exactly like the memory having grown, which is the event
	// the search exists to find.  slbench's crossing confirmation is
	// there to survive that, and there is no other way to write a test
	// for it: the fault cannot be provoked to order.
	//
	// It is called once per RUN and not once per reading, so a hook that
	// lies the first time it sees a pad and tells the truth afterwards
	// reproduces the live event exactly -- a single bad reading, which
	// the caller's own cache then serves back for the rest of the search.
	//
	// This package deliberately left it out at first as speculative, and
	// it is in now for one reason: without it the contract cannot express
	// the one instrument fault this repository has actually seen, so the
	// code written to survive that fault could not be reached from the
	// caller's side of the seam at all.
	//
	// What it does NOT touch is the compiler's refusal or the
	// out-of-memory collision.  Those are the region's judgements about
	// the script, made from the script; this is llGetUsedMemory
	// misreporting the size of one that ran either way.  A hook that
	// moved the limits as well would make a search's back-off depend on
	// a number that is meant to be noise.
	Noise func(cnt, pad, reading int) int
}

// Behaviour is a way to make one object misbehave.
//
// Per object rather than per server, because that is how the interesting
// tests are written: a probe in a spare object is made to fail while the
// measured one carries on, which is the failure a caller is most likely
// to get wrong.
type Behaviour struct {
	// CompileErrors refuses the script, saying these.  The run is then
	// Compiled{ok:false} and Finished, with no lines between.
	CompileErrors []string

	// Fault is a run-time error raised the moment the script starts, and
	// OutOfMemory says it is the kind a benchmark searching for a size
	// limit treats as the answer rather than as a failure.
	Fault       string
	OutOfMemory bool

	// Silent runs the script but never says the sentinel, which is the
	// timeout a caller has to report rather than hang on.
	Silent bool

	// LineDelay is how long to leave between lines, for a caller that
	// prints them as they arrive and claims not to buffer.
	LineDelay time.Duration

	// Lines replaces what the script would have said.  It is for a
	// caller being tested on what it does with output rather than on the
	// measurement: name the lines and they are said in order.
	Lines []string
}

// Server implements scriptv1.RunnerServer.
type Server struct {
	scriptv1.UnimplementedRunnerServer

	opt  Options
	caps *scriptv1.Capabilities

	// slots bounds concurrent runs.  A buffered channel rather than a
	// semaphore type so that waiting for one can be selected against the
	// caller going away.
	slots chan struct{}

	mu     sync.Mutex
	agents []*poolAgent
	queue  []*waiter
	seq    int // mints target ids, so no two grants share one

	grpc  *grpc.Server
	conns []*grpc.ClientConn

	// direct is what Stop has to cut off on the in-process client.  A
	// Direct lease handler runs on a goroutine of its own, waiting to be
	// told the caller has gone, and there is no connection whose closing
	// would ever tell it.  Keyed by seq, which already mints ids nothing
	// else uses.
	direct map[int]context.CancelFunc
}

// New builds a backend.  Nothing is served until Pipe or Listen.
func New(o Options) *Server {
	if o.Backend == "" {
		o.Backend = "scripttest"
	}
	if len(o.Agents) == 0 {
		o.Agents = []string{"test"}
	}
	if o.Groups == 0 {
		o.Groups = 1
	}
	if o.GroupSize == 0 {
		o.GroupSize = 4
	}
	if o.Second == 0 {
		o.Second = time.Second
	}
	if o.DefaultTimeout == 0 {
		o.DefaultTimeout = 30
	}
	s := &Server{opt: o}
	for _, name := range o.Agents {
		a := &poolAgent{name: name}
		for g := 0; g < o.Groups; g++ {
			grp := &poolGroup{agent: a, n: g}
			for i := 0; i < o.GroupSize; i++ {
				grp.targets = append(grp.targets, &poolTarget{
					group: grp,
					name:  autoName(g*o.GroupSize + i),
				})
			}
			a.groups = append(a.groups, grp)
		}
		s.agents = append(s.agents, a)
	}

	runs := o.MaxConcurrentRuns
	if runs <= 0 {
		runs = len(o.Agents) * o.Groups * o.GroupSize
	}
	s.slots = make(chan struct{}, runs)
	s.caps = &scriptv1.Capabilities{
		MaxConcurrentRuns: int32(runs),
		CompileOnly:       !o.NoCompileOnly,
		Faults:            true,
		OutOfMemory:       true,
		PersistentTargets: !o.NoPersistence,
		Agents:            true,
		// Never true.  It is the flag a destructive test checks before
		// doing something it would not do in public, and this backend is
		// the one place that answer is knowable for certain.
		Grid: false,
	}

	s.grpc = grpc.NewServer()
	scriptv1.RegisterRunnerServer(s.grpc, s)
	return s
}

// autoName is what the nth object of an avatar's pool is called.  The
// first keeps the bare name, matching what the objects are really called
// in world -- an account that has only ever run one at a time has one
// object, called "auto".
//
// Written out here rather than taken from internal/session, which is
// where the live naming lives: this package is importable by a backend
// outside this repository, and a name in a test transcript is not worth
// a dependency on how the pool is really built.
func autoName(n int) string {
	if n == 0 {
		return "auto"
	}
	return fmt.Sprintf("auto %d", n+1)
}

// Register adds the backend to somebody else's gRPC server, for a
// process that already has one.
func (s *Server) Register(r grpc.ServiceRegistrar) { scriptv1.RegisterRunnerServer(r, s) }

// bufSize is the pipe's buffer.  Big enough that a transcript is never
// the thing that blocks, small enough to notice a caller that has
// stopped reading.
const bufSize = 1 << 20

// Pipe serves the backend over an in-process pipe and returns a
// connection to it.
//
// No network and no port: the pipe is a pair of buffers.  What it does
// keep is everything above the socket -- the messages are marshalled,
// the streams are HTTP/2 streams, and cancelling a call cancels the
// server's context -- which is the difference between testing a caller
// and testing a caller's idea of a backend.
//
// The connection is closed by Stop.
func (s *Server) Pipe() (*grpc.ClientConn, error) {
	lis := bufconn.Listen(bufSize)
	go s.grpc.Serve(lis)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.conns = append(s.conns, conn)
	s.mu.Unlock()
	return conn, nil
}

// Listen serves the backend on a real address, for a person who wants to
// point a program at it -- "127.0.0.1:0" and read back the port.  Tests
// in this repository use Pipe; this is for driving a caller that takes
// an address on its command line.
func (s *Server) Listen(addr string) (net.Addr, error) {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	go s.grpc.Serve(lis)
	return lis.Addr(), nil
}

// Stop shuts the server down and closes what Pipe handed out.  Streams
// in flight are cut off rather than waited for, which is what a caller
// that leaked a lease should see.
//
// A Direct lease is cut off the only way it can be, by cancelling the
// context its handler is waiting on.  A Direct RUN is not: it happens on
// the caller's own goroutine and has returned before there is anything
// to stop.
func (s *Server) Stop() {
	s.mu.Lock()
	conns := s.conns
	s.conns = nil
	direct := s.direct
	s.direct = nil
	s.mu.Unlock()
	// Outside the lock: cancelling wakes a lease handler, and the first
	// thing it does is give its group back, which takes the lock.
	for _, cancel := range direct {
		cancel()
	}
	for _, c := range conns {
		c.Close()
	}
	s.grpc.Stop()
}

// SetBehaviour makes one object misbehave.  which names it by the name a
// person would call it -- "auto 2" -- or by the opaque id from a
// Granted; empty means every object in the pool.
//
// Both spellings are accepted because both are what a test has to hand:
// before a lease there are no ids, and after one the id is what the
// caller is holding.
func (s *Server) SetBehaviour(which string, b Behaviour) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.agents {
		for _, g := range a.groups {
			for _, t := range g.targets {
				if which == "" || which == t.name || (t.id != "" && which == t.id) {
					t.beh = b
				}
			}
		}
	}
}

// SetReady says whether the backend can run anything, and why not.  It
// is a method as well as an option so that a test can bring a backend up
// after a caller has already found it down, which is the sequence a
// caller that waits is supposed to survive.
func (s *Server) SetReady(ready bool, why string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ready {
		s.opt.NotReady = ""
		return
	}
	if why == "" {
		why = "not ready"
	}
	s.opt.NotReady = why
}

// Memory is the model the backend is answering from, for a test that
// wants to say what a reading should have been.
func (s *Server) Memory() Memory { return s.opt.Memory }

func (s *Server) Health(ctx context.Context, req *scriptv1.HealthRequest) (*scriptv1.HealthResponse, error) {
	s.mu.Lock()
	why := s.opt.NotReady
	s.mu.Unlock()
	return &scriptv1.HealthResponse{
		Backend:      s.opt.Backend,
		Version:      s.opt.Version,
		Capabilities: s.caps,
		Ready:        why == "",
		Why:          why,
	}, nil
}

func (s *Server) Pool(ctx context.Context, req *scriptv1.PoolRequest) (*scriptv1.PoolResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := &scriptv1.PoolResponse{Waiting: int32(len(s.queue))}
	for _, a := range s.agents {
		pa := &scriptv1.PoolAgent{Agent: a.name}
		for _, g := range a.groups {
			pa.Groups = append(pa.Groups, &scriptv1.PoolGroup{
				Group:  int32(g.n),
				Size:   int32(len(g.targets)),
				HeldBy: g.heldBy,
			})
		}
		out.Agents = append(out.Agents, pa)
	}
	return out, nil
}

// notReady is the error every call that would need to run something
// returns while the backend is down.  Unavailable rather than an
// invented code because that is what a caller retries on, and waiting is
// what the contract says a caller may reasonably do.
func (s *Server) notReady() error {
	s.mu.Lock()
	why := s.opt.NotReady
	s.mu.Unlock()
	if why == "" {
		return nil
	}
	return status.Error(codes.Unavailable, why)
}

// seconds converts one of the contract's second counts into a duration,
// through Options.Second.
func (s *Server) seconds(n int64) time.Duration { return time.Duration(n) * s.opt.Second }
