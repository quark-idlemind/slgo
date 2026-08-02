package msg

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"
)

// MaxDatagram is the largest packet we will build.  Acknowledgements
// are only attached to an outgoing packet while it stays under this.
const MaxDatagram = 1200

// maxAcksPerPacket is the limit of the one byte count, both on the tail
// of a piggybacked packet and in a PacketAck block.
const maxAcksPerPacket = 255

// ErrSenderClosed is returned by Send once Run has stopped.
var ErrSenderClosed = errors.New("msg: sender is not running")

// A PacketWriter is the write half of a connected socket.  A
// *net.UDPConn from net.Dial satisfies it.
type PacketWriter interface {
	Write(p []byte) (int, error)
}

// SendStats counts what the sender has done.
type SendStats struct {
	Sent        uint64 // datagrams written
	Bytes       uint64
	Reliable    uint64 // reliable messages queued
	Resent      uint64 // retransmissions
	Abandoned   uint64 // reliable messages given up on
	AcksQueued  uint64 // acknowledgements asked for
	AcksCarried uint64 // acknowledgements that rode on another packet
	AcksAlone   uint64 // acknowledgements sent as a PacketAck
	AckPackets  uint64 // PacketAck datagrams sent
}

// Sender owns the socket's write end.  Everything outbound is
// serialized through its goroutine, which is also the only thing that
// assigns sequence numbers, so no locking is needed for either.
//
// Acknowledgements travel on their own channel so they never queue
// behind a large message.  When a message goes out, whatever
// acknowledgements have accumulated ride along on its tail; if none
// goes out before AckDelay expires they are flushed as a batched
// PacketAck.  That is the whole reason for the split: acks want to be
// cheap and prompt, and messages want to be ordered.
type Sender struct {
	conn PacketWriter

	out      chan *outbound
	acks     chan uint32
	confirms chan uint32
	done     chan struct{}

	ackDelay time.Duration
	rto      time.Duration
	maxTries int

	// Owned by Run.
	seq     uint32
	pending []uint32
	unacked map[uint32]*inflight
	buf     []byte

	sent        atomic.Uint64
	bytes       atomic.Uint64
	reliable    atomic.Uint64
	resent      atomic.Uint64
	abandoned   atomic.Uint64
	acksQueued  atomic.Uint64
	acksCarried atomic.Uint64
	acksAlone   atomic.Uint64
	ackPackets  atomic.Uint64
}

type outbound struct {
	m        Message
	reliable bool
}

type inflight struct {
	seq   uint32
	data  []byte
	tries int
	next  time.Time
}

// SenderOption configures a Sender.
type SenderOption func(*Sender)

// WithAckDelay sets how long acknowledgements wait for a message to
// ride out on before being flushed as a PacketAck.  Default 100ms.
func WithAckDelay(d time.Duration) SenderOption {
	return func(s *Sender) { s.ackDelay = d }
}

// WithRetransmit sets the initial retransmission timeout and how many
// attempts a reliable message gets before it is abandoned.  Default 3s
// and 5.  The C client retried forever, which is why its acktime kept
// growing; giving up and saying so is more useful.
func WithRetransmit(rto time.Duration, tries int) SenderOption {
	return func(s *Sender) { s.rto, s.maxTries = rto, tries }
}

// WithSendBuffer sets the outbound channel capacity.  Default 128.
func WithSendBuffer(n int) SenderOption {
	return func(s *Sender) { s.out = make(chan *outbound, n) }
}

// NewSender prepares a Sender.  Nothing is written until Run is called.
func NewSender(conn PacketWriter, opts ...SenderOption) *Sender {
	s := &Sender{
		conn:     conn,
		out:      make(chan *outbound, 128),
		acks:     make(chan uint32, 1024),
		confirms: make(chan uint32, 1024),
		done:     make(chan struct{}),
		ackDelay: 100 * time.Millisecond,
		rto:      3 * time.Second,
		maxTries: 5,
		unacked:  make(map[uint32]*inflight),
		buf:      make([]byte, 0, MaxDatagram),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Stats snapshots the counters.  Safe from any goroutine.
func (s *Sender) Stats() SendStats {
	return SendStats{
		Sent:        s.sent.Load(),
		Bytes:       s.bytes.Load(),
		Reliable:    s.reliable.Load(),
		Resent:      s.resent.Load(),
		Abandoned:   s.abandoned.Load(),
		AcksQueued:  s.acksQueued.Load(),
		AcksCarried: s.acksCarried.Load(),
		AcksAlone:   s.acksAlone.Load(),
		AckPackets:  s.ackPackets.Load(),
	}
}

// Send queues a message for unreliable delivery.
func (s *Sender) Send(ctx context.Context, m Message) error {
	return s.queue(ctx, &outbound{m: m})
}

// SendReliable queues a message and retransmits it until the peer
// acknowledges it or the attempts run out.
func (s *Sender) SendReliable(ctx context.Context, m Message) error {
	return s.queue(ctx, &outbound{m: m, reliable: true})
}

// queue hands a message to Run.  Returning nil means queued, not sent:
// anything still in the channel when Run stops is discarded.
func (s *Sender) queue(ctx context.Context, ob *outbound) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// Test for a stopped sender before offering the message.  The
	// channel is buffered, so a plain three way select would
	// sometimes take the send case against a closed done channel
	// and swallow the message without a word.
	select {
	case <-s.done:
		return ErrSenderClosed
	default:
	}
	select {
	case s.out <- ob:
		return nil
	case <-s.done:
		return ErrSenderClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// QueueAck asks for an inbound packet to be acknowledged.  It never
// blocks: a dropped acknowledgement costs one retransmission from the
// peer, which is a far better trade than stalling the receiver.
func (s *Sender) QueueAck(seq uint32) {
	select {
	case s.acks <- seq:
		s.acksQueued.Add(1)
	default:
	}
}

// ConfirmAck reports that the peer acknowledged one of our packets, so
// it can stop being retransmitted.  Feed it everything in Packet.Acks.
func (s *Sender) ConfirmAck(seq uint32) {
	select {
	case s.confirms <- seq:
	default:
	}
}

// Run writes until ctx is cancelled or the connection fails.  Call it
// as a goroutine, as with Receiver.Run.
func (s *Sender) Run(ctx context.Context) error {
	defer close(s.done)

	if ctx == nil {
		ctx = context.Background()
	}

	// Disarmed until an acknowledgement is waiting.
	flush := time.NewTimer(time.Hour)
	if !flush.Stop() {
		<-flush.C
	}
	defer flush.Stop()
	armed := false

	retry := time.NewTicker(s.rto / 2)
	defer retry.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil

		case ob := <-s.out:
			if err := s.transmit(ob); err != nil {
				return err
			}
			// Whatever was waiting went with it.
			if armed && len(s.pending) == 0 {
				if !flush.Stop() {
					select {
					case <-flush.C:
					default:
					}
				}
				armed = false
			}

		case a := <-s.acks:
			s.pending = append(s.pending, a)
			s.drainAcks()
			if len(s.pending) >= maxAcksPerPacket {
				if err := s.flushAcks(); err != nil {
					return err
				}
				continue
			}
			if !armed {
				flush.Reset(s.ackDelay)
				armed = true
			}

		case <-flush.C:
			armed = false
			if err := s.flushAcks(); err != nil {
				return err
			}

		case c := <-s.confirms:
			delete(s.unacked, c)

		case <-retry.C:
			if err := s.retransmit(); err != nil {
				return err
			}
		}
	}
}

// drainAcks takes everything already waiting on the ack channel without
// blocking, so one wakeup can collect a burst.
func (s *Sender) drainAcks() {
	for len(s.pending) < maxAcksPerPacket {
		select {
		case a := <-s.acks:
			s.pending = append(s.pending, a)
		default:
			return
		}
	}
}

// transmit builds and writes one message, attaching as many waiting
// acknowledgements as will fit.
func (s *Sender) transmit(ob *outbound) error {
	body, err := ob.m.Encode()
	if err != nil {
		// A message we cannot encode is a bug in the caller, not
		// a reason to take the connection down.
		return nil
	}

	s.drainAcks()

	s.seq++
	h := Header{Sequence: s.seq}
	if ob.reliable {
		h.Flags |= FlagReliable
	}

	// How many acknowledgements fit under the datagram limit.
	room := (MaxDatagram - HeaderSize - 4 - len(body) - 1) / 4
	if room < 0 {
		room = 0
	}
	n := len(s.pending)
	if n > room {
		n = room
	}
	if n > maxAcksPerPacket {
		n = maxAcksPerPacket
	}
	if n > 0 {
		h.Flags |= FlagAck
	}

	s.buf = AppendHeader(s.buf[:0], &h)
	s.buf = AppendID(s.buf, ob.m.MsgInfo().ID)
	s.buf = append(s.buf, body...)
	for i := 0; i < n; i++ {
		a := s.pending[i]
		s.buf = append(s.buf, byte(a>>24), byte(a>>16), byte(a>>8), byte(a))
	}
	if n > 0 {
		s.buf = append(s.buf, byte(n))
		s.acksCarried.Add(uint64(n))
	}
	s.pending = s.pending[:copy(s.pending, s.pending[n:])]

	if err := s.write(s.buf); err != nil {
		return err
	}

	if ob.reliable {
		s.reliable.Add(1)
		// Keep a copy: s.buf is reused on the next send.
		cp := append([]byte(nil), s.buf...)
		s.unacked[s.seq] = &inflight{
			seq:   s.seq,
			data:  cp,
			tries: 1,
			next:  time.Now().Add(s.rto),
		}
	}
	return nil
}

// flushAcks sends whatever is waiting as batched PacketAck messages.
// The C client sent one datagram per acknowledgement; PacketAck's block
// is Variable, so 255 fit in one.
func (s *Sender) flushAcks() error {
	for len(s.pending) > 0 {
		n := len(s.pending)
		if n > maxAcksPerPacket {
			n = maxAcksPerPacket
		}
		m := &PacketAck{Packets: make([]PacketAck_Packets, n)}
		for i := 0; i < n; i++ {
			m.Packets[i].ID = s.pending[i]
		}
		body, err := m.Encode()
		if err != nil {
			return err
		}

		s.seq++
		h := Header{Sequence: s.seq}
		s.buf = AppendHeader(s.buf[:0], &h)
		s.buf = AppendID(s.buf, m.MsgInfo().ID)
		s.buf = append(s.buf, body...)
		if err := s.write(s.buf); err != nil {
			return err
		}
		s.acksAlone.Add(uint64(n))
		s.ackPackets.Add(1)
		s.pending = s.pending[:copy(s.pending, s.pending[n:])]
	}
	return nil
}

// retransmit resends anything still unacknowledged, backing off each
// time, and abandons a packet once the attempts run out.
func (s *Sender) retransmit() error {
	now := time.Now()
	for seq, f := range s.unacked {
		if now.Before(f.next) {
			continue
		}
		if f.tries >= s.maxTries {
			delete(s.unacked, seq)
			s.abandoned.Add(1)
			continue
		}
		// The first transmission is not a resend; every one
		// after it is.
		f.data[0] |= FlagResent
		if err := s.write(f.data); err != nil {
			return err
		}
		f.tries++
		f.next = now.Add(s.rto * time.Duration(f.tries))
		s.resent.Add(1)
	}
	return nil
}

func (s *Sender) write(b []byte) error {
	n, err := s.conn.Write(b)
	if err != nil {
		return fmt.Errorf("msg: send: %w", err)
	}
	s.sent.Add(1)
	s.bytes.Add(uint64(n))
	return nil
}
