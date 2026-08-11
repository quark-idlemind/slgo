package scripttest

// Direct: the same backend, with no connection under it.
//
// scriptv1.RunnerClient is an interface and so is
// grpc.ServerStreamingClient, so a caller can be handed something that
// answers the contract without a transport in the middle.  What that
// buys is the per-run cost, and the per-run cost is the whole reason
// this file exists: measured by BenchmarkARunReachedEachWay, one run of
// the offline model costs about 100us over Pipe and about 4us here.
// Almost all of the difference is goroutine hand-off -- a run streams
// seven messages and each one is a wake-up on the other side -- and a
// profile of autobench's tests before this change spent 73% of the run
// in pthread_cond_signal, pthread_cond_wait and findRunnable.  That is
// worth paying where a run is the thing being tested and not worth
// paying 440,000 times where the runs are how a sweep gets to its
// arithmetic.
//
// # What it does not do, and why Pipe stays
//
// It cannot fail the tests Pipe exists for, which is the argument in
// this package's own doc comment and is unchanged.  Specifically:
//
//   - There is no connection, so a caller that DIES cannot be
//     expressed.  Closing a connection out from under a live lease is
//     what a crashed benchmark looks like, and it stays on Pipe.
//   - A run is delivered when it has finished, so a caller cannot
//     cancel PART WAY THROUGH one in reaction to what it has heard.  A
//     test that did that here would pass by running the script to the
//     end and then noticing, which is a pass for the wrong reason.
//
// Everything else the two clients agree on, and the tests say so rather
// than take it on trust: lease_test.go and run_test.go run against both,
// so a change that made them disagree fails the build.
//
// # Whose messages these are
//
// The caller is handed the server's own messages rather than copies of
// them, because there is nothing to marshal.  A caller that scribbled on
// one would be scribbling on the server, which over a connection it
// could not do.  Nothing in this repository does, and the one message
// that would matter -- Capabilities, which the server keeps and hands
// out again -- is copied below for that reason.

import (
	"context"
	"io"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/quark-idlemind/slgo/proto/scriptv1"
)

// Direct returns a client that reaches this backend in process.
//
// It is for the caller that runs thousands of scripts and is measuring
// something else -- autobench's offline model and the sweeps in its
// tests.  A caller testing the CONTRACT wants Pipe; see above for the
// two things this cannot be made to do.
func (s *Server) Direct() scriptv1.RunnerClient { return direct{s} }

type direct struct{ s *Server }

var _ scriptv1.RunnerClient = direct{}

func (d direct) Health(ctx context.Context, in *scriptv1.HealthRequest, _ ...grpc.CallOption) (*scriptv1.HealthResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	h, err := d.s.Health(ctx, in)
	if err != nil {
		return nil, asStatus(err)
	}
	// Copied, because Capabilities is the one message the server keeps
	// and hands out again: over a connection every caller gets its own,
	// and a caller that edited this one would be editing what the next
	// caller is told.
	return proto.Clone(h).(*scriptv1.HealthResponse), nil
}

func (d direct) Pool(ctx context.Context, in *scriptv1.PoolRequest, _ ...grpc.CallOption) (*scriptv1.PoolResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	p, err := d.s.Pool(ctx, in)
	return p, asStatus(err)
}

// Lease runs the handler on a goroutine, because it does not return.
//
// The lease is held for exactly as long as the stream is open, so the
// Granted has to reach the caller while the handler is still sitting in
// the call waiting to be told to let go.  That is one goroutine per
// LEASE, which a benchmark takes one of; it is the one per RUN EVENT
// that this file exists to avoid.
func (d direct) Lease(ctx context.Context, in *scriptv1.LeaseRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[scriptv1.LeaseEvent], error) {
	// A context of our own so that Stop can cut the handler off.  With
	// no connection to close there is nothing else that would ever tell
	// it, and a lease nobody ended would keep its goroutine and its
	// group for the life of the process.
	lctx, cancel := context.WithCancel(ctx)
	st := &streamed[scriptv1.LeaseEvent]{
		noWire: noWire{lctx},
		ch:     make(chan *scriptv1.LeaseEvent),
		done:   make(chan struct{}),
	}
	id := d.s.watchDirect(cancel)
	go func() {
		// The order matters and cost a debugging session.  Cancelling
		// before closing done would race the caller: a Recv sitting in
		// its select would see the cancellation and report Canceled,
		// losing the InvalidArgument or DeadlineExceeded the handler
		// returned -- which is precisely the answer the caller asked
		// for.  Closing done first means the verdict is there to be
		// found before anything says the call is over.
		defer cancel()
		defer d.s.forgetDirect(id)
		defer close(st.done)
		st.err = asStatus(d.s.Lease(in, st))
	}()
	return st, nil
}

// Run runs the whole script and hands the events back afterwards.
//
// The alternative -- a goroutine and a channel, as Lease has -- is most
// of what the 100us a run over the pipe costs, and nothing in this
// repository needs a run's events while the run is still going: the one
// caller that prints lines as they arrive is automate, whose test for
// that (TestALineThroughABackendIsPrintedBeforeTheRunHasEnded) drives it
// through --backend over a real listener, which is the transport a
// person typing --backend actually gets.  autobench reads a run's whole
// transcript and then does arithmetic on it.
//
// So the events are the same events in the same order, and only their
// TIMING differs -- which is exactly what the second bullet at the top
// of this file gives up.
func (d direct) Run(ctx context.Context, in *scriptv1.RunRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[scriptv1.RunEvent], error) {
	st := &collected[scriptv1.RunEvent]{noWire: noWire{ctx}}
	st.err = asStatus(d.s.Run(in, st))
	// Never an error from the call itself, which is what a caller over a
	// connection sees: the handler's verdict arrives at Recv, after
	// whatever it managed to say first.
	return st, nil
}

// watchDirect registers something for Stop to cancel, and forgetDirect
// takes it back out when the call ends of its own accord.
func (s *Server) watchDirect(cancel context.CancelFunc) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	id := s.seq
	if s.direct == nil {
		s.direct = map[int]context.CancelFunc{}
	}
	s.direct[id] = cancel
	return id
}

func (s *Server) forgetDirect(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.direct, id)
}

// asStatus turns what a handler returned into what a caller over a
// connection would have been given.
//
// The same two steps grpc-go's own server takes: a handler that returned
// a status error is believed, and anything else becomes one -- which is
// how a bare context.Canceled from a handler reaches the caller as
// codes.Canceled rather than as itself.  Without this the two clients
// would disagree about every error code, and the tests below compare
// them.
func asStatus(err error) error {
	if err == nil {
		return nil
	}
	if st, ok := status.FromError(err); ok {
		return st.Err()
	}
	return status.FromContextError(err).Err()
}

// errNoWire is what the raw stream methods answer.  gRPC's own
// documentation says a caller must not touch them -- Recv and Send are
// the stream -- and the generated code does not, so a call here is a
// caller reaching past the contract rather than a case to support.
var errNoWire = status.Error(codes.Unimplemented,
	"this backend is reached in process; there is no stream to read or write messages on")

// noWire answers the parts of a gRPC stream that have no meaning without
// a connection.  Header and trailer metadata is the whole of it: this
// contract sends none, and a caller asking for it over a pipe gets
// nothing either.
type noWire struct{ ctx context.Context }

func (s noWire) Context() context.Context   { return s.ctx }
func (noWire) Header() (metadata.MD, error) { return nil, nil }
func (noWire) Trailer() metadata.MD         { return nil }
func (noWire) CloseSend() error             { return nil }
func (noWire) SetHeader(metadata.MD) error  { return nil }
func (noWire) SendHeader(metadata.MD) error { return nil }
func (noWire) SetTrailer(metadata.MD)       {}
func (noWire) SendMsg(any) error            { return errNoWire }
func (noWire) RecvMsg(any) error            { return errNoWire }

// collected is a finished call's events, handed back one at a time.  It
// is both ends of the stream at once, which it can be because the
// handler has returned by the time the caller reads.
type collected[T any] struct {
	noWire
	ev  []*T
	err error
}

func (c *collected[T]) Send(m *T) error {
	// Checked even though nothing here blocks, because a handler is
	// entitled to learn from Send that the caller has gone -- over a
	// connection that is what a Send into a cancelled stream says, and a
	// handler written against it would otherwise carry on talking to
	// nobody.
	if err := c.ctx.Err(); err != nil {
		return status.FromContextError(err).Err()
	}
	c.ev = append(c.ev, m)
	return nil
}

func (c *collected[T]) Recv() (*T, error) {
	// A caller that has gone away is told so rather than served the rest
	// of what it asked for.  Over a connection this is a race -- the
	// transport selects between the cancellation and the bytes already
	// buffered -- so either answer would be faithful; this is the one
	// that cannot let a cancelled caller act on data it should not have
	// had.
	if err := c.ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if len(c.ev) > 0 {
		m := c.ev[0]
		c.ev = c.ev[1:]
		return m, nil
	}
	if c.err != nil {
		return nil, c.err
	}
	return nil, io.EOF
}

// streamed carries a handler's events to a caller as the handler sends
// them, for a call whose handler does not return until the caller has
// gone.
//
// The channel is unbuffered on purpose: it makes done, once closed, mean
// that nothing is left to read.  A Send that had not been taken would
// still be blocked in Send, and the handler could not have returned.
type streamed[T any] struct {
	noWire
	ch   chan *T
	done chan struct{}

	// err is what the handler returned.  Written before done is closed
	// and read only after, which is the whole of the synchronisation it
	// needs.
	err error
}

func (s *streamed[T]) Send(m *T) error {
	select {
	case s.ch <- m:
		return nil
	case <-s.ctx.Done():
		return status.FromContextError(s.ctx.Err()).Err()
	}
}

func (s *streamed[T]) Recv() (*T, error) {
	// A handler that has already returned is answered from what it
	// returned, without consulting the context.  Both can be ready at
	// once -- the call ending is what cancels the context -- and a
	// select between them would then report the ending rather than the
	// verdict, at random.
	select {
	case <-s.done:
		return nil, s.ended()
	default:
	}
	select {
	case m := <-s.ch:
		return m, nil
	case <-s.done:
		return nil, s.ended()
	case <-s.ctx.Done():
		return nil, status.FromContextError(s.ctx.Err()).Err()
	}
}

// ended is what a call that has returned tells its caller.  Nothing can
// be left unread: the channel is unbuffered, so a Send nobody took would
// still have the handler in it.
func (s *streamed[T]) ended() error {
	if s.err != nil {
		return s.err
	}
	return io.EOF
}
