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
// semaphore: at most the number given to WithConcurrency are in flight,
// and the dispatch loop blocks rather than spawning past that.  A stall
// therefore backs up into the receive channel and then into the kernel,
// which is honest for UDP -- bounded concurrency does not remove
// backpressure, it just stops the goroutine count being the thing that
// gives way first.
//
// Since handlers run concurrently, arrival order is not preserved.
// That is usually fine and sometimes not: TransferInfo carries the size
// that TransferPacket assembles against, and RegionHandshake and the
// teleport sequence are order sensitive.
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
	onDuplicate Handler
	tap         Handler
	relay       Handler
	gate        func(*Packet) bool

	dispatched atomic.Uint64
	inline     atomic.Uint64
	async      atomic.Uint64
	duplicates atomic.Uint64
	unhandled  atomic.Uint64
	errors     atomic.Uint64
	ackOnly    atomic.Uint64
	acksSeen   atomic.Uint64
	relayed    atomic.Uint64
	gated      atomic.Uint64

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
	AcksSeen   uint64 // acknowledgements of ours the peer sent back
	Relayed    uint64 // offered to the relay hook, duplicates already removed
	Gated      uint64 // refused by the gate before anything looked at them
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

// OnDuplicate is called, on the dispatch goroutine, for a packet dropped
// as a retransmission of one already handled: the packets the tap sees
// and the relay is not offered.  A record kept from the relay needs it
// to see the wire whole.
func OnDuplicate(fn Handler) DispatcherOption {
	return func(d *Dispatcher) { d.onDuplicate = fn }
}

// WithTap calls fn for every packet, before routing and before
// duplicate suppression, on the dispatch goroutine.  It is meant for
// capture and for noticing that anything at all has arrived; keep it
// quick, because it runs in the path of every packet.
func WithTap(fn Handler) DispatcherOption {
	return func(d *Dispatcher) { d.tap = fn }
}

// WithGate decides whether a packet is dispatched at all.  fn runs on
// the dispatch goroutine for every packet, before the tap, before
// acknowledgement bookkeeping and before routing; returning false drops
// the packet as though it had never arrived.
//
// Before the bookkeeping is the point of it.  A gate that ran later
// would still have acknowledged the packet and still have let its
// piggybacked acks confirm our own sends, which is a conversation with
// somebody the gate exists to refuse.  Dropping it here means the only
// trace is the counter.
//
// Nil, the default, dispatches everything: a circuit whose peer is
// settled by other means -- a session's own connection to a simulator,
// which reaches one address and receives from one -- has nothing to
// decide.
func WithGate(fn func(*Packet) bool) DispatcherOption {
	return func(d *Dispatcher) { d.gate = fn }
}

// WithRelay calls fn for every decoded message that is about to be
// routed, on the dispatch goroutine, AFTER duplicate suppression and
// before any handler runs.
//
// It exists because a relay wants something a tap cannot give it.  A tap
// sees retransmissions, since it runs ahead of the duplicate check --
// which is right for a capture, where seeing the wire as it really was
// is the whole point, and wrong for a relay, where forwarding the same
// message twice under two sequence numbers gives the far end no way to
// tell it was one message.  A chat line said once and shown twice is the
// visible form of that.
//
// It is offered decoded messages only.  A packet that would not decode
// is not something to pass on, and one carrying nothing but
// acknowledgements has nothing to pass on: reliability is terminated at
// each end of a relay rather than forwarded, so both sides number their
// own packets and neither ever sees the other's sequence numbers.  For
// the same reason a PacketAck the bookkeeping has already consumed is
// not offered either.
//
// Registering a handler is not required for the relay to see a message,
// and is not a reason to skip it: the messages a relay most needs to
// pass on are exactly the ones nothing here understands.
func WithRelay(fn Handler) DispatcherOption {
	return func(d *Dispatcher) { d.relay = fn }
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
		AcksSeen:   d.acksSeen.Load(),
		Relayed:    d.relayed.Load(),
		Gated:      d.gated.Load(),
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
	if d.gate != nil && !d.gate(p) {
		d.gated.Add(1)
		return
	}
	if d.tap != nil {
		d.tap(p)
	}

	// Acknowledgement bookkeeping comes before routing and happens for
	// every packet the gate lets through, including duplicates and ones
	// that failed to decode.
	// A duplicate arrived precisely because our previous
	// acknowledgement did not get through, so it needs another.
	consumed := false
	if d.sender != nil {
		// Acknowledgements reach us two ways.  Some ride on the
		// tail of another packet, and those are in p.Acks.  The
		// rest arrive as a PacketAck message in their own
		// datagram, which is how a simulator sends most of them
		// -- miss those and every reliable message we send
		// retransmits until it is abandoned.
		for _, a := range p.Acks {
			d.sender.ConfirmAck(a)
			d.acksSeen.Add(1)
		}
		if ack, ok := p.Message.(*PacketAck); ok {
			for i := range ack.Packets {
				d.sender.ConfirmAck(ack.Packets[i].ID)
				d.acksSeen.Add(1)
			}
			consumed = true
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

	if d.duplicate(&p.Header) {
		d.duplicates.Add(1)
		if d.onDuplicate != nil {
			d.onDuplicate(p)
		}
		return
	}

	// Past the duplicate check, so this message has not been seen
	// before -- which is the guarantee a relay needs and a tap cannot
	// offer.  Ahead of the handler lookup, so a message nothing here
	// registers for is still passed on.
	if d.relay != nil && !consumed {
		d.relayed.Add(1)
		d.relay(p)
	}

	d.mu.RLock()
	r := d.handlers[p.ID]
	d.mu.RUnlock()

	if r == nil {
		// A PacketAck the bookkeeping above has already acted on
		// is not unhandled, it is done with.
		if consumed {
			return
		}
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

// duplicate reports whether this packet is a retransmission of one
// already handled, the viewer's rule: only a packet flagged RESENT is
// one, and only under the number of a reliable packet handled recently.
// Only reliable packets are remembered, since only they are resent.  An
// unflagged packet under a remembered number is a new packet -- a peer
// whose numbering started again -- and is handled.
// Why: doc/duplicates.md
func (d *Dispatcher) duplicate(h *Header) bool {
	if d.seen == nil {
		return false
	}
	seq := h.Sequence
	if _, ok := d.seen[seq]; ok {
		if h.Flags&FlagResent != 0 {
			return true
		}
		// Not a resend: handled, and already remembered.
		return false
	}
	if !h.Reliable() {
		return false
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

// Forget discards every remembered sequence number, so that the next
// packet under any number counts as one that has not been seen.
//
// It exists for a circuit whose peer has been replaced.  A sequence
// number belongs to a conversation and not to a socket: a viewer numbers
// its own packets from 1, so the second one to attach to a session opens
// with the numbers the first one used.  Its first sending of each is not
// flagged RESENT and is handled anyway, but its retransmission of one
// would be taken for the first peer's and dropped.  Nothing else asks to
// forget, because for a peer that has not changed a RESENT packet under a
// remembered number really is a retransmission.
//
// Safe from the dispatch goroutine and nowhere else, which means from a
// gate, a tap, an inline handler or the relay hook.  d.seen and the ring are
// written by duplicate alone and carry no lock, and that is deliberate:
// they are touched for every packet that arrives, so a mutex there would
// be paid by every session to buy something one caller needs once in its
// life.  Called from a gate or a tap this runs between two dispatches, on the very
// goroutine that owns those fields, so the single-writer property is kept
// rather than defended.
func (d *Dispatcher) Forget() {
	if d.seen == nil {
		return
	}
	// The ring itself is left as it is.  A length of zero is what says
	// which of its slots hold anything, so refilling from the start
	// overwrites the stale numbers as it goes.
	clear(d.seen)
	d.ringPos, d.ringLen = 0, 0
}

func (d *Dispatcher) countUnhandled(id ID) {
	d.unmu.Lock()
	d.unhandledByID[id]++
	d.unmu.Unlock()
}
