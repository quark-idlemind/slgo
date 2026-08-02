package client

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"slgo/msg"
)

// A Session is a live UDP circuit to one simulator.
//
// Connect performs the handshake the simulator expects -- UseCircuitCode
// to open the circuit, then CompleteAgentMovement to put the avatar in
// the region -- and answers RegionHandshake and StartPingCheck for the
// life of the session.  Everything else is yours: register handlers on
// Dispatcher before calling Connect, or with Handle afterwards.
type Session struct {
	Account *Account

	Conn *net.UDPConn
	Recv *msg.Receiver
	Send *msg.Sender
	Disp *msg.Dispatcher

	// Caps are the capability URLs the simulator offered, and
	// Inventory is this agent's folder tree.  Both belong to the
	// session: nothing here is package level, so one process can
	// hold as many sessions as it likes.
	Caps      Caps
	Inventory *Inventory

	// HTTP is used for capability and inventory requests.  A nil
	// client gets a default with a sixty second timeout.
	HTTP *http.Client

	cancel context.CancelFunc
	wg     sync.WaitGroup

	done     chan struct{}
	doneOnce sync.Once
	errOnce  sync.Once
	err      error

	// Signals for the handshake, each closed once.
	anyPacket signal
	inRegion  signal
	handshook signal
	loggedOut signal

	mu          sync.RWMutex
	regionName  string
	regionFlags uint32
	position    msg.Vector3
	lookAt      msg.Vector3
	handle      uint64
	channel     string
	kicked      string
}

// signal is a channel closed at most once.
type signal struct {
	ch   chan struct{}
	once sync.Once
}

func newSignal() signal { return signal{ch: make(chan struct{})} }

func (s *signal) fire()                 { s.once.Do(func() { close(s.ch) }) }
func (s *signal) wait() <-chan struct{} { return s.ch }

// Options configure a Session.
type Options struct {
	// Timeout bounds each step of the handshake.  Default 30s.
	Timeout time.Duration

	// Concurrency caps how many handlers run at once.  Default 8.
	Concurrency int

	// Tap, if set, sees every packet.  msg.DumpPacket makes a
	// reasonable capture.
	Tap msg.Handler

	// OnUnhandled sees decoded messages nothing is registered for.
	// Worth setting: this is how a protocol change announces
	// itself.
	OnUnhandled msg.Handler

	// OnError sees packets that would not decode.
	OnError msg.Handler

	// Caps names the capabilities to ask the seed capability for.
	// Empty means DefaultCaps; SkipCaps skips the request.
	Caps     []string
	SkipCaps bool

	// HTTP is used for capability and inventory requests.
	HTTP *http.Client
}

// Connect opens the circuit and completes the handshake.
func Connect(ctx context.Context, a *Account, opts Options) (*Session, error) {
	if a == nil {
		return nil, fmt.Errorf("client: Connect needs an account")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 8
	}

	conn, err := net.DialUDP("udp", nil, a.SimAddr())
	if err != nil {
		return nil, fmt.Errorf("client: dial %s: %w", a.SimAddr(), err)
	}

	s := &Session{
		Account:   a,
		Conn:      conn,
		HTTP:      opts.HTTP,
		Inventory: newInventory(a.InventoryRoot),
		Caps:      Caps{},
		done:      make(chan struct{}),
		anyPacket: newSignal(),
		inRegion:  newSignal(),
		handshook: newSignal(),
		loggedOut: newSignal(),
	}

	s.Send = msg.NewSender(conn)
	s.Recv = msg.NewReceiver(conn)

	dopts := []msg.DispatcherOption{
		msg.WithSender(s.Send),
		msg.WithConcurrency(opts.Concurrency),
		msg.WithTap(func(p *msg.Packet) {
			s.anyPacket.fire()
			if opts.Tap != nil {
				opts.Tap(p)
			}
		}),
	}
	if opts.OnUnhandled != nil {
		dopts = append(dopts, msg.OnUnhandled(opts.OnUnhandled))
	}
	if opts.OnError != nil {
		dopts = append(dopts, msg.OnError(opts.OnError))
	}
	s.Disp = msg.NewDispatcher(dopts...)
	s.register()

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.cancel = cancel

	s.spawn(func() error { return s.Send.Run(runCtx) })
	s.spawn(func() error { return s.Recv.Run(runCtx) })
	s.spawn(func() error { return s.Disp.Run(runCtx, s.Recv.C()) })

	if err := s.handshake(ctx, opts.Timeout); err != nil {
		s.Close()
		return nil, err
	}

	// Capabilities are HTTP and have nothing to do with the
	// circuit, but almost everything above this layer needs them,
	// so they are fetched here rather than left for the caller to
	// remember.
	if !opts.SkipCaps && a.SeedCapability != "" {
		caps, err := RequestCaps(ctx, a.SeedCapability, opts.Caps, s.http())
		if err != nil {
			s.Close()
			return nil, err
		}
		s.Caps = caps
	}
	return s, nil
}

func (s *Session) spawn(fn func() error) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := fn(); err != nil {
			s.fail(err)
		}
	}()
}

func (s *Session) fail(err error) {
	s.errOnce.Do(func() { s.err = err })
	s.doneOnce.Do(func() { close(s.done) })
	if s.cancel != nil {
		s.cancel()
	}
}

// register installs the handlers the circuit itself needs.  All of them
// are Inline: they are trivial, and running them in order keeps the
// handshake deterministic.
func (s *Session) register() {
	s.Disp.MustHandle("StartPingCheck", func(p *msg.Packet) {
		m := p.Message.(*msg.StartPingCheck)
		reply := &msg.CompletePingCheck{}
		reply.PingID.PingID = m.PingID.PingID
		_ = s.Send.Send(context.Background(), reply)
	}, msg.Inline())

	s.Disp.MustHandle("RegionHandshake", func(p *msg.Packet) {
		m := p.Message.(*msg.RegionHandshake)
		s.mu.Lock()
		s.regionName = trimNul(m.RegionInfo.SimName)
		s.regionFlags = m.RegionInfo.RegionFlags
		s.mu.Unlock()

		reply := &msg.RegionHandshakeReply{}
		reply.AgentData.AgentID = s.Account.AgentID
		reply.AgentData.SessionID = s.Account.SessionID
		_ = s.Send.SendReliable(context.Background(), reply)
		s.handshook.fire()
	}, msg.Inline())

	s.Disp.MustHandle("AgentMovementComplete", func(p *msg.Packet) {
		m := p.Message.(*msg.AgentMovementComplete)
		s.mu.Lock()
		s.position = m.Data.Position
		s.lookAt = m.Data.LookAt
		s.handle = m.Data.RegionHandle
		s.channel = trimNul(m.SimData.ChannelVersion)
		s.mu.Unlock()
		s.inRegion.fire()
	}, msg.Inline())

	s.Disp.MustHandle("LogoutReply", func(p *msg.Packet) {
		s.loggedOut.fire()
	}, msg.Inline())

	s.Disp.MustHandle("KickUser", func(p *msg.Packet) {
		m := p.Message.(*msg.KickUser)
		s.mu.Lock()
		s.kicked = trimNul(m.UserInfo.Reason)
		s.mu.Unlock()
		s.fail(fmt.Errorf("client: kicked: %s", trimNul(m.UserInfo.Reason)))
	}, msg.Inline())
}

func (s *Session) handshake(ctx context.Context, timeout time.Duration) error {
	// UseCircuitCode opens the circuit.  The simulator does not
	// answer it with anything in particular, so the circuit is up
	// once anything at all comes back.
	circuit := &msg.UseCircuitCode{}
	circuit.CircuitCode.Code = s.Account.CircuitCode
	circuit.CircuitCode.SessionID = s.Account.SessionID
	circuit.CircuitCode.ID = s.Account.AgentID
	if err := s.Send.SendReliable(ctx, circuit); err != nil {
		return fmt.Errorf("client: UseCircuitCode: %w", err)
	}
	if err := s.await(ctx, s.anyPacket.wait(), timeout, "circuit to come up"); err != nil {
		return err
	}

	// CompleteAgentMovement puts the avatar in the region, and is
	// answered with AgentMovementComplete.
	move := &msg.CompleteAgentMovement{}
	move.AgentData.AgentID = s.Account.AgentID
	move.AgentData.SessionID = s.Account.SessionID
	move.AgentData.CircuitCode = s.Account.CircuitCode
	if err := s.Send.SendReliable(ctx, move); err != nil {
		return fmt.Errorf("client: CompleteAgentMovement: %w", err)
	}
	return s.await(ctx, s.inRegion.wait(), timeout, "AgentMovementComplete")
}

func (s *Session) await(ctx context.Context, ch <-chan struct{}, timeout time.Duration, what string) error {
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-ch:
		return nil
	case <-s.done:
		if s.err != nil {
			return s.err
		}
		return fmt.Errorf("client: session ended waiting for %s", what)
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return fmt.Errorf("client: timed out after %s waiting for %s", timeout, what)
	}
}

// Handle registers a handler, by message name, for the life of the
// session.
func (s *Session) Handle(name string, fn msg.Handler, opts ...msg.HandlerOption) error {
	return s.Disp.Handle(name, fn, opts...)
}

// Done is closed when the session ends, however it ends.
func (s *Session) Done() <-chan struct{} { return s.done }

// Err reports why the session ended, or nil for a clean shutdown.
func (s *Session) Err() error { return s.err }

// RegionName is the simulator's name, once RegionHandshake has arrived.
func (s *Session) RegionName() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.regionName
}

// Position is where the avatar arrived.
func (s *Session) Position() msg.Vector3 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.position
}

// ChannelVersion is the simulator's build string.
func (s *Session) ChannelVersion() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.channel
}

// RegionHandle identifies the region on the grid.
func (s *Session) RegionHandle() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.handle
}

// WaitForRegionHandshake blocks until the simulator has introduced the
// region, which usually happens moments after Connect returns.
func (s *Session) WaitForRegionHandshake(ctx context.Context, timeout time.Duration) error {
	return s.await(ctx, s.handshook.wait(), timeout, "RegionHandshake")
}

// Logout asks the simulator to end the session and waits for its reply
// before shutting down.  A simulator that never answers is not a reason
// to hang: the wait is bounded and Logout tears down either way.
func (s *Session) Logout(ctx context.Context, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	out := &msg.LogoutRequest{}
	out.AgentData.AgentID = s.Account.AgentID
	out.AgentData.SessionID = s.Account.SessionID

	err := s.Send.SendReliable(ctx, out)
	if err == nil {
		err = s.await(ctx, s.loggedOut.wait(), timeout, "LogoutReply")
	}
	s.Close()
	return err
}

// Close stops the session's goroutines and the socket without telling
// the simulator anything.
func (s *Session) Close() {
	s.doneOnce.Do(func() { close(s.done) })
	if s.cancel != nil {
		s.cancel()
	}
	s.Conn.Close()
	s.wg.Wait()
}

// trimNul drops the terminator the protocol puts on its strings.
func trimNul(b []byte) string {
	if n := len(b); n > 0 && b[n-1] == 0 {
		b = b[:n-1]
	}
	return string(b)
}
