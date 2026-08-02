package agent

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"slgo/msg"
)

// An Agent is a live UDP circuit to one simulator.
//
// Connect performs the handshake the simulator expects -- UseCircuitCode
// to open the circuit, then CompleteAgentMovement to put the avatar in
// the region -- and answers RegionHandshake and StartPingCheck for the
// life of the session.  Everything else is yours: register handlers on
// Dispatcher before calling Connect, or with Handle afterwards.
type Agent struct {
	Account *Account

	Conn *net.UDPConn
	Recv *msg.Receiver
	Send *msg.Sender
	Disp *msg.Dispatcher

	// Caps are the capability URLs the simulator offered, and
	// Inventory is this agent'a folder tree.  Both belong to the
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

// Options configure an Agent.
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

	// Recv is passed through to the receiver.  A relay wants
	// msg.KeepBody so it can pass on a message it cannot decode.
	Recv []msg.ReceiverOption
}

// Connect opens the circuit and completes the handshake.
func Connect(ctx context.Context, acct *Account, opts Options) (*Agent, error) {
	if acct == nil {
		return nil, fmt.Errorf("agent: Connect needs an account")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 8
	}

	conn, err := net.DialUDP("udp", nil, acct.SimAddr())
	if err != nil {
		return nil, fmt.Errorf("agent: dial %s: %w", acct.SimAddr(), err)
	}

	a := &Agent{
		Account:   acct,
		Conn:      conn,
		HTTP:      opts.HTTP,
		Inventory: newInventory(acct.InventoryRoot),
		Caps:      Caps{},
		done:      make(chan struct{}),
		anyPacket: newSignal(),
		inRegion:  newSignal(),
		handshook: newSignal(),
		loggedOut: newSignal(),
	}

	a.Send = msg.NewSender(conn)
	a.Recv = msg.NewReceiver(conn, opts.Recv...)

	dopts := []msg.DispatcherOption{
		msg.WithSender(a.Send),
		msg.WithConcurrency(opts.Concurrency),
		msg.WithTap(func(p *msg.Packet) {
			a.anyPacket.fire()
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
	a.Disp = msg.NewDispatcher(dopts...)
	a.register()

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	a.cancel = cancel

	a.spawn(func() error { return a.Send.Run(runCtx) })
	a.spawn(func() error { return a.Recv.Run(runCtx) })
	a.spawn(func() error { return a.Disp.Run(runCtx, a.Recv.C()) })

	if err := a.handshake(ctx, opts.Timeout); err != nil {
		a.Close()
		return nil, err
	}

	// Capabilities are HTTP and have nothing to do with the
	// circuit, but almost everything above this layer needs them,
	// so they are fetched here rather than left for the caller to
	// remember.
	if !opts.SkipCaps && acct.SeedCapability != "" {
		caps, err := RequestCaps(ctx, acct.SeedCapability, opts.Caps, a.http())
		if err != nil {
			a.Close()
			return nil, err
		}
		a.Caps = caps
	}
	return a, nil
}

func (a *Agent) spawn(fn func() error) {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		if err := fn(); err != nil {
			a.fail(err)
		}
	}()
}

func (a *Agent) fail(err error) {
	a.errOnce.Do(func() { a.err = err })
	a.doneOnce.Do(func() { close(a.done) })
	if a.cancel != nil {
		a.cancel()
	}
}

// register installs the handlers the circuit itself needs.  All of them
// are Inline: they are trivial, and running them in order keeps the
// handshake deterministic.
func (a *Agent) register() {
	a.Disp.MustHandle("StartPingCheck", func(p *msg.Packet) {
		m := p.Message.(*msg.StartPingCheck)
		reply := &msg.CompletePingCheck{}
		reply.PingID.PingID = m.PingID.PingID
		_ = a.Send.Send(context.Background(), reply)
	}, msg.Inline())

	a.Disp.MustHandle("RegionHandshake", func(p *msg.Packet) {
		m := p.Message.(*msg.RegionHandshake)
		a.mu.Lock()
		a.regionName = trimNul(m.RegionInfo.SimName)
		a.regionFlags = m.RegionInfo.RegionFlags
		a.mu.Unlock()

		reply := &msg.RegionHandshakeReply{}
		reply.AgentData.AgentID = a.Account.AgentID
		reply.AgentData.SessionID = a.Account.SessionID
		_ = a.Send.SendReliable(context.Background(), reply)
		a.handshook.fire()
	}, msg.Inline())

	a.Disp.MustHandle("AgentMovementComplete", func(p *msg.Packet) {
		m := p.Message.(*msg.AgentMovementComplete)
		a.mu.Lock()
		a.position = m.Data.Position
		a.lookAt = m.Data.LookAt
		a.handle = m.Data.RegionHandle
		a.channel = trimNul(m.SimData.ChannelVersion)
		a.mu.Unlock()
		a.inRegion.fire()
	}, msg.Inline())

	a.Disp.MustHandle("LogoutReply", func(p *msg.Packet) {
		a.loggedOut.fire()
	}, msg.Inline())

	a.Disp.MustHandle("KickUser", func(p *msg.Packet) {
		m := p.Message.(*msg.KickUser)
		a.mu.Lock()
		a.kicked = trimNul(m.UserInfo.Reason)
		a.mu.Unlock()
		a.fail(fmt.Errorf("agent: kicked: %s", trimNul(m.UserInfo.Reason)))
	}, msg.Inline())
}

func (a *Agent) handshake(ctx context.Context, timeout time.Duration) error {
	// UseCircuitCode opens the circuit.  The simulator does not
	// answer it with anything in particular, so the circuit is up
	// once anything at all comes back.
	circuit := &msg.UseCircuitCode{}
	circuit.CircuitCode.Code = a.Account.CircuitCode
	circuit.CircuitCode.SessionID = a.Account.SessionID
	circuit.CircuitCode.ID = a.Account.AgentID
	if err := a.Send.SendReliable(ctx, circuit); err != nil {
		return fmt.Errorf("agent: UseCircuitCode: %w", err)
	}
	if err := a.await(ctx, a.anyPacket.wait(), timeout, "circuit to come up"); err != nil {
		return err
	}

	// CompleteAgentMovement puts the avatar in the region, and is
	// answered with AgentMovementComplete.
	move := &msg.CompleteAgentMovement{}
	move.AgentData.AgentID = a.Account.AgentID
	move.AgentData.SessionID = a.Account.SessionID
	move.AgentData.CircuitCode = a.Account.CircuitCode
	if err := a.Send.SendReliable(ctx, move); err != nil {
		return fmt.Errorf("agent: CompleteAgentMovement: %w", err)
	}
	return a.await(ctx, a.inRegion.wait(), timeout, "AgentMovementComplete")
}

func (a *Agent) await(ctx context.Context, ch <-chan struct{}, timeout time.Duration, what string) error {
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-ch:
		return nil
	case <-a.done:
		if a.err != nil {
			return a.err
		}
		return fmt.Errorf("agent: session ended waiting for %s", what)
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return fmt.Errorf("agent: timed out after %s waiting for %s", timeout, what)
	}
}

// Handle registers a handler, by message name, for the life of the
// session.
func (a *Agent) Handle(name string, fn msg.Handler, opts ...msg.HandlerOption) error {
	return a.Disp.Handle(name, fn, opts...)
}

// Done is closed when the session ends, however it ends.
func (a *Agent) Done() <-chan struct{} { return a.done }

// Err reports why the session ended, or nil for a clean shutdown.
func (a *Agent) Err() error { return a.err }

// RegionName is the simulator'a name, once RegionHandshake has arrived.
func (a *Agent) RegionName() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.regionName
}

// Position is where the avatar arrived.
func (a *Agent) Position() msg.Vector3 {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.position
}

// ChannelVersion is the simulator'a build string.
func (a *Agent) ChannelVersion() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.channel
}

// RegionHandle identifies the region on the grid.
func (a *Agent) RegionHandle() uint64 {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.handle
}

// WaitForRegionHandshake blocks until the simulator has introduced the
// region, which usually happens moments after Connect returns.
func (a *Agent) WaitForRegionHandshake(ctx context.Context, timeout time.Duration) error {
	return a.await(ctx, a.handshook.wait(), timeout, "RegionHandshake")
}

// Logout asks the simulator to end the session and waits for its reply
// before shutting down.  A simulator that never answers is not a reason
// to hang: the wait is bounded and Logout tears down either way.
func (a *Agent) Logout(ctx context.Context, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	out := &msg.LogoutRequest{}
	out.AgentData.AgentID = a.Account.AgentID
	out.AgentData.SessionID = a.Account.SessionID

	err := a.Send.SendReliable(ctx, out)
	if err == nil {
		err = a.await(ctx, a.loggedOut.wait(), timeout, "LogoutReply")
	}
	a.Close()
	return err
}

// Close stops the session'a goroutines and the socket without telling
// the simulator anything.
func (a *Agent) Close() {
	a.doneOnce.Do(func() { close(a.done) })
	if a.cancel != nil {
		a.cancel()
	}
	a.Conn.Close()
	a.wg.Wait()
}

// trimNul drops the terminator the protocol puts on its strings.
func trimNul(b []byte) string {
	if n := len(b); n > 0 && b[n-1] == 0 {
		b = b[:n-1]
	}
	return string(b)
}
