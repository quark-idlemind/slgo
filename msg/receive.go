package msg

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"time"
)

// MaxPacketSize is the largest datagram we will read.  It matches
// NET_BUFFER_SIZE in the C client.
const MaxPacketSize = 0x2000

// ErrUnknownMessage is reported for a message number that is not in the
// template.  Callers who expect to meet messages this build does not
// know about can filter on it with errors.Is.
var ErrUnknownMessage = errors.New("msg: unknown message number")

// A PacketSource is the read half of a net.PacketConn.  *net.UDPConn
// satisfies it, and so does anything else that can hand over datagrams.
type PacketSource interface {
	ReadFrom(p []byte) (n int, addr net.Addr, err error)
}

// Packet is one datagram, taken apart.
//
// Exactly one of three things is true of a delivered Packet:
//
//	Message != nil                 it decoded
//	Err != nil                     it did not, and Body holds the bytes
//	Message == nil && Err == nil    it carried only acknowledgements
//
// Acks is filled in regardless, so a packet that failed to decode still
// releases whatever it was acknowledging.
type Packet struct {
	Addr    net.Addr
	At      time.Time
	Header  Header
	ID      ID
	Message Message
	Acks    []uint32
	Body    []byte
	Err     error
}

// Stats counts what the receiver has seen.
type Stats struct {
	Packets uint64 // datagrams read
	Bytes   uint64 // bytes read
	Runts   uint64 // too short to hold a header
	Unknown uint64 // message number not in the template
	Failed  uint64 // header, ack, zero coding or body decode failures
	Dropped uint64 // discarded because the channel was full
}

// Receiver reads datagrams from a PacketSource, takes them apart and
// puts them on a channel.
//
// It does not acknowledge anything and does not track sequence numbers:
// Header.Reliable and Header.Sequence are handed to the caller so the
// session layer above can do that.
type Receiver struct {
	conn PacketSource
	ch   chan *Packet
	drop bool
	keep bool

	// Owned by the Run goroutine.
	buf  []byte
	zbuf []byte

	packets atomic.Uint64
	bytes   atomic.Uint64
	runts   atomic.Uint64
	unknown atomic.Uint64
	failed  atomic.Uint64
	dropped atomic.Uint64
}

// ReceiverOption configures a Receiver.
type ReceiverOption func(*Receiver)

// WithBuffer sets the channel capacity.  The default is 256.
func WithBuffer(n int) ReceiverOption {
	return func(r *Receiver) {
		if n < 0 {
			n = 0
		}
		r.ch = make(chan *Packet, n)
	}
}

// KeepBody makes every packet carry its undecoded body, not just the
// ones that failed.  A relay needs the original bytes so it can pass on
// a message without understanding it; nothing else does, so it is off
// by default and costs one copy per packet when on.
func KeepBody() ReceiverOption {
	return func(r *Receiver) { r.keep = true }
}

// DropWhenFull discards packets instead of blocking when the channel is
// full, counting them in Stats.Dropped.
//
// The default is to block, which pushes back on the network and lets
// the kernel drop datagrams instead -- honest for UDP, but it also
// stalls the caller's acknowledgements behind a slow consumer.  Choose
// deliberately.
func DropWhenFull() ReceiverOption {
	return func(r *Receiver) { r.drop = true }
}

// NewReceiver prepares a Receiver.  Nothing is read until Run is
// called.
func NewReceiver(conn PacketSource, opts ...ReceiverOption) *Receiver {
	r := &Receiver{
		conn: conn,
		ch:   make(chan *Packet, 256),
		buf:  make([]byte, MaxPacketSize),
		zbuf: make([]byte, 0, MaxPacketSize),
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// C is the channel of decoded packets.  It is closed when Run returns.
func (r *Receiver) C() <-chan *Packet { return r.ch }

// Stats snapshots the counters.  Safe to call from any goroutine.
func (r *Receiver) Stats() Stats {
	return Stats{
		Packets: r.packets.Load(),
		Bytes:   r.bytes.Load(),
		Runts:   r.runts.Load(),
		Unknown: r.unknown.Load(),
		Failed:  r.failed.Load(),
		Dropped: r.dropped.Load(),
	}
}

// Run reads until ctx is cancelled or the connection fails, then closes
// the channel.  Call it as a goroutine:
//
//	recv := NewReceiver(conn)
//	go func() { err = recv.Run(ctx) }()
//	for p := range recv.C() { ... }
//
// Cancellation unblocks a read in progress if the connection supports
// SetReadDeadline, which *net.UDPConn does.  Otherwise close the
// connection to stop it.  A cancelled Run returns nil.
func (r *Receiver) Run(ctx context.Context) error {
	defer close(r.ch)

	if ctx == nil {
		ctx = context.Background()
	}
	if d, ok := r.conn.(interface {
		SetReadDeadline(time.Time) error
	}); ok {
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			select {
			case <-ctx.Done():
				// A deadline in the past makes the
				// blocked read return immediately.
				_ = d.SetReadDeadline(time.Now())
			case <-stop:
			}
		}()
	}

	for {
		n, addr, err := r.conn.ReadFrom(r.buf)
		at := time.Now()
		if err != nil {
			// A read that failed because we were asked to
			// stop is not a failure.
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		if n <= 0 {
			continue
		}

		r.packets.Add(1)
		r.bytes.Add(uint64(n))

		p := r.parse(r.buf[:n], addr, at)
		if p == nil {
			continue
		}

		if r.drop {
			select {
			case r.ch <- p:
			default:
				r.dropped.Add(1)
			}
			continue
		}
		select {
		case r.ch <- p:
		case <-ctx.Done():
			return nil
		}
	}
}

// parse takes one datagram apart.  It returns nil for a packet too
// malformed to say anything useful about.
//
// Nothing in the returned Packet aliases b: the decoder copies Variable
// fields, and the header's extra bytes and any raw body are copied
// here.  That is what lets Run reuse one read buffer forever.
func (r *Receiver) parse(b []byte, addr net.Addr, at time.Time) *Packet {
	p := &Packet{Addr: addr, At: at}

	h, n, err := DecodeHeader(b)
	if err != nil {
		r.runts.Add(1)
		return nil
	}
	if len(h.Extra) > 0 {
		h.Extra = append([]byte(nil), h.Extra...)
	}
	p.Header = h
	body := b[n:]

	// Acknowledgements ride on the tail and are never zero coded,
	// so they come off first.
	if h.HasAcks() {
		rest, acks, err := SplitAcks(body)
		if err != nil {
			r.failed.Add(1)
			p.Err = err
			return p
		}
		body, p.Acks = rest, acks
	}

	if h.Zerocoded() {
		expanded, err := ZeroExpand(r.zbuf[:0], body)
		if err != nil {
			r.failed.Add(1)
			p.Err = err
			return p
		}
		// Keep the grown buffer for next time.
		r.zbuf = expanded
		body = expanded
	}

	// A packet can legitimately carry nothing but acknowledgements.
	if len(body) == 0 {
		return p
	}

	id, k, err := DecodeID(body)
	if err != nil {
		r.failed.Add(1)
		p.Err = err
		return p
	}
	p.ID = id
	body = body[k:]

	if r.keep {
		p.Body = append([]byte(nil), body...)
	}

	m := New(id)
	if m == nil {
		r.unknown.Add(1)
		p.Err = fmt.Errorf("%w %v", ErrUnknownMessage, id)
		if p.Body == nil {
			p.Body = append([]byte(nil), body...)
		}
		return p
	}
	if err := m.Decode(body); err != nil {
		r.failed.Add(1)
		p.Err = err
		if p.Body == nil {
			p.Body = append([]byte(nil), body...)
		}
		return p
	}
	p.Message = m
	return p
}
