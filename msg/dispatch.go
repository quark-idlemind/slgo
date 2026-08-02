package msg

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

// Handler is called with one decoded packet.
type Handler func(*Packet)

// Dispatcher reads a Receiver's channel, routes each packet to its
// handler, and does the acknowledgement bookkeeping in between.
//
// Handlers run in their own goroutine, throttled by a counting
// semaphore: at most Concurrency of them are in flight, and the
// dispatch loop blocks rather than spawning past that.  A stall
// therefore backs up into the receive channel and then into the kernel,
// which is honest for UDP -- bounded concurrency does not remove
// backpressure, it just stops the goroutine count being the thing that
// gives way first.
//
// Since handlers run concurrently, arrival order is not preserved.
// That is usually fine and sometimes not: TransferInfo carries the size
// that TransferPacket assembles against, and RegionHandshake, the
// teleport sequence and inventory descent are all order sensitive.
// Register those with Inline and they run on the dispatch goroutine, in
// order, at the cost of blocking it.
type Dispatcher struct {
	mu       sync.RWMutex
	handlers map[ID]*registration

	sem    chan struct{}
	sender *Sender

	seen    map[uint32]struct{}
	ring    []uint32
	ringPos int
	ringLen int

	onUnhandled Handler
	onError     Handler

	dispatched atomic.Uint64
	inline     atomic.Uint64
	async      atomic.Uint64
	duplicates atomic.Uint64
	unhandled  atomic.Uint64
	errors     atomic.Uint64
	ackOnly    atomic.Uint64

	unmu          sync.Mutex
	unhandledByID map[ID]uint64
}

type registration struct {
	fn     Handler
	inline bool
}

// DispatchStats counts what the dispatcher has done.
type DispatchStats struct {
	Dispatched uint64
	Inline     uint64
	Async      uint64
	Duplicates uint64 // retransmissions we had already handled
	Unhandled  uint64 // decoded, but nothing registered for it
	Errors     uint64 // packets the receiver could not decode
	AckOnly    uint64
}

// DispatcherOption configures a Dispatcher.
type DispatcherOption func(*Dispatcher)

// WithConcurrency caps how many handlers run at once.  Default 8.
func WithConcurrency(n int) DispatcherOption {
	return func(d *Dispatcher) {
		if n < 1 {
			n = 1
		}
		d.sem = make(chan struct{}, n)
	}
}

// WithSender wires the acknowledgement bookkeeping: every inbound
// acknowledgement is confirmed so it stops being retransmitted, and
// every reliable packet is queued for acknowledgement.
func WithSender(s *Sender) DispatcherOption {
	return func(d *Dispatcher) { d.sender = s }
}

// WithDedupe sets how many recent sequence numbers are remembered for
// duplicate suppression.  Default 4096; zero disables it.
func WithDedupe(n int) DispatcherOption {
	return func(d *Dispatcher) {
		if n <= 0 {
			d.seen, d.ring = nil, nil
			return
		}
		d.seen = make(map[uint32]struct{}, n)
		d.ring = make([]uint32, n)
		d.ringPos, d.ringLen = 0, 0
	}
}

// OnUnhandled is called for a decoded message with no registered
// handler.  DumpPacket makes a reasonable body for it.
func OnUnhandled(fn Handler) DispatcherOption {
	return func(d *Dispatcher) { d.onUnhandled = fn }
}

// OnError is called for a packet the receiver could not decode.
func OnError(fn Handler) DispatcherOption {
	return func(d *Dispatcher) { d.onError = fn }
}

// NewDispatcher prepares a Dispatcher.
func NewDispatcher(opts ...DispatcherOption) *Dispatcher {
	d := &Dispatcher{
		handlers:      make(map[ID]*registration),
		sem:           make(chan struct{}, 8),
		seen:          make(map[uint32]struct{}, 4096),
		ring:          make([]uint32, 4096),
		unhandledByID: make(map[ID]uint64),
	}
	for _, o := range opts {
		o(d)
	}
	return d
}

// HandlerOption configures one registration.
type HandlerOption func(*registration)

// Inline runs the handler on the dispatch goroutine instead of its own,
// which preserves arrival order at the cost of blocking dispatch while
// it runs.  Use it for handlers that are quick and order sensitive.
func Inline() HandlerOption {
	return func(r *registration) { r.inline = true }
}

// Handle registers a handler by message name, so a typo is an error at
// startup rather than a message that silently goes nowhere.
func (d *Dispatcher) Handle(name string, fn Handler, opts ...HandlerOption) error {
	info := LookupName(name)
	if info == nil {
		return fmt.Errorf("msg: no message named %q in the template", name)
	}
	r := &registration{fn: fn}
	for _, o := range opts {
		o(r)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, dup := d.handlers[info.ID]; dup {
		return fmt.Errorf("msg: %s already has a handler", name)
	}
	d.handlers[info.ID] = r
	return nil
}

// MustHandle is Handle for registrations made at startup, where a bad
// name is a programming error.
func (d *Dispatcher) MustHandle(name string, fn Handler, opts ...HandlerOption) {
	if err := d.Handle(name, fn, opts...); err != nil {
		panic(err)
	}
}

// Stats snapshots the counters.
func (d *Dispatcher) Stats() DispatchStats {
	return DispatchStats{
		Dispatched: d.dispatched.Load(),
		Inline:     d.inline.Load(),
		Async:      d.async.Load(),
		Duplicates: d.duplicates.Load(),
		Unhandled:  d.unhandled.Load(),
		Errors:     d.errors.Load(),
		AckOnly:    d.ackOnly.Load(),
	}
}

// Unhandled reports how many packets arrived for each message nobody
// registered for.  This is how a protocol change makes itself known:
// the C client only ever noticed CameraConstraint because unhandled
// messages got dumped.
func (d *Dispatcher) Unhandled() map[ID]uint64 {
	d.unmu.Lock()
	defer d.unmu.Unlock()
	out := make(map[ID]uint64, len(d.unhandledByID))
	for k, v := range d.unhandledByID {
		out[k] = v
	}
	return out
}

// Run consumes in until it closes or ctx is cancelled, then waits for
// every handler still running before returning.
func (d *Dispatcher) Run(ctx context.Context, in <-chan *Packet) error {
	if ctx == nil {
		ctx = context.Background()
	}
	defer d.drain()

	for {
		var p *Packet
		var ok bool
		select {
		case <-ctx.Done():
			return nil
		case p, ok = <-in:
			if !ok {
				return nil
			}
		}
		d.one(ctx, p)
	}
}

func (d *Dispatcher) one(ctx context.Context, p *Packet) {
	// Acknowledgement bookkeeping comes first and happens for every
	// packet, including duplicates and ones that failed to decode.
	// A duplicate arrived precisely because our previous
	// acknowledgement did not get through, so it needs another.
	if d.sender != nil {
		for _, a := range p.Acks {
			d.sender.ConfirmAck(a)
		}
		if p.Header.Reliable() {
			d.sender.QueueAck(p.Header.Sequence)
		}
	}

	switch {
	case p.Err != nil:
		d.errors.Add(1)
		if p.ID != 0 {
			d.countUnhandled(p.ID)
		}
		if d.onError != nil {
			d.onError(p)
		}
		return
	case p.Message == nil:
		d.ackOnly.Add(1)
		return
	}

	if d.duplicate(p.Header.Sequence) {
		d.duplicates.Add(1)
		return
	}

	d.mu.RLock()
	r := d.handlers[p.ID]
	d.mu.RUnlock()

	if r == nil {
		d.unhandled.Add(1)
		d.countUnhandled(p.ID)
		if d.onUnhandled != nil {
			d.onUnhandled(p)
		}
		return
	}

	d.dispatched.Add(1)
	if r.inline {
		d.inline.Add(1)
		r.fn(p)
		return
	}

	// Take a slot before spawning, so the number of handlers in
	// flight is bounded and the loop blocks instead of the heap
	// growing.
	select {
	case d.sem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	d.async.Add(1)
	go func() {
		defer func() { <-d.sem }()
		r.fn(p)
	}()
}

// drain waits for every in-flight handler by collecting the whole
// semaphore.
func (d *Dispatcher) drain() {
	for i := 0; i < cap(d.sem); i++ {
		d.sem <- struct{}{}
	}
	for i := 0; i < cap(d.sem); i++ {
		<-d.sem
	}
}

// duplicate reports whether this sequence number has been handled
// recently, remembering it either way.  Retransmissions are normal:
// anything reliable arrives again if our acknowledgement is lost.
func (d *Dispatcher) duplicate(seq uint32) bool {
	if d.seen == nil {
		return false
	}
	if _, ok := d.seen[seq]; ok {
		return true
	}
	// Once the ring has wrapped, the slot we are about to reuse
	// holds the oldest sequence number, which stops being
	// remembered.  Counting the fill rather than testing for a zero
	// slot keeps sequence number 0 from being a special case.
	if d.ringLen == len(d.ring) {
		delete(d.seen, d.ring[d.ringPos])
	} else {
		d.ringLen++
	}
	d.ring[d.ringPos] = seq
	d.ringPos = (d.ringPos + 1) % len(d.ring)
	d.seen[seq] = struct{}{}
	return false
}

func (d *Dispatcher) countUnhandled(id ID) {
	d.unmu.Lock()
	d.unhandledByID[id]++
	d.unmu.Unlock()
}
