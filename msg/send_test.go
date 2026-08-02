package msg

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

// capture collects everything written to it.
type capture struct {
	mu   sync.Mutex
	pkts [][]byte
	err  error
	ch   chan struct{}
}

func newCapture() *capture { return &capture{ch: make(chan struct{}, 256)} }

func (c *capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return 0, err
	}
	c.pkts = append(c.pkts, append([]byte(nil), p...))
	c.mu.Unlock()
	select {
	case c.ch <- struct{}{}:
	default:
	}
	return len(p), nil
}

func (c *capture) all() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]byte(nil), c.pkts...)
}

func (c *capture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pkts)
}

// waitFor spins until cond holds or the test times out.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func runSender(t *testing.T, c *capture, opts ...SenderOption) (*Sender, func()) {
	t.Helper()
	s := NewSender(c, opts...)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	return s, func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("Run returned %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not stop")
		}
	}
}

func TestSendUnreliable(t *testing.T) {
	c := newCapture()
	s, stop := runSender(t, c)
	defer stop()

	m := &CompletePingCheck{}
	m.PingID.PingID = 5
	if err := s.Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the packet", func() bool { return c.count() == 1 })

	p := c.all()[0]
	h, n, err := DecodeHeader(p)
	if err != nil {
		t.Fatal(err)
	}
	if h.Sequence != 1 {
		t.Errorf("sequence = %d, want 1", h.Sequence)
	}
	if h.Reliable() || h.HasAcks() {
		t.Errorf("flags = %#x", h.Flags)
	}
	got, err := DecodeBody(p[n:])
	if err != nil {
		t.Fatal(err)
	}
	if got.(*CompletePingCheck).PingID.PingID != 5 {
		t.Errorf("body did not round trip")
	}
}

func TestSendSequenceIncrements(t *testing.T) {
	c := newCapture()
	s, stop := runSender(t, c)
	defer stop()

	for i := 0; i < 3; i++ {
		if err := s.Send(context.Background(), &CompletePingCheck{}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "three packets", func() bool { return c.count() == 3 })
	for i, p := range c.all() {
		h, _, _ := DecodeHeader(p)
		if h.Sequence != uint32(i+1) {
			t.Errorf("packet %d has sequence %d", i, h.Sequence)
		}
	}
}

// TestAcksRideAlong is the point of the two channel design: an
// acknowledgement waiting when a message goes out travels on its tail
// rather than in a datagram of its own.
func TestAcksRideAlong(t *testing.T) {
	c := newCapture()
	s, stop := runSender(t, c, WithAckDelay(time.Hour)) // never flush alone
	defer stop()

	s.QueueAck(11)
	s.QueueAck(22)
	waitFor(t, "acks to be queued", func() bool { return s.Stats().AcksQueued == 2 })

	if err := s.Send(context.Background(), &CompletePingCheck{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the packet", func() bool { return c.count() == 1 })

	p := c.all()[0]
	h, n, err := DecodeHeader(p)
	if err != nil {
		t.Fatal(err)
	}
	if !h.HasAcks() {
		t.Fatalf("ack flag not set: %#x", h.Flags)
	}
	body, acks, err := SplitAcks(p[n:])
	if err != nil {
		t.Fatal(err)
	}
	if len(acks) != 2 {
		t.Fatalf("acks = %v", acks)
	}
	if _, err := DecodeBody(body); err != nil {
		t.Errorf("message did not survive the ack tail: %v", err)
	}
	if st := s.Stats(); st.AcksCarried != 2 || st.AcksAlone != 0 {
		t.Errorf("stats = %+v", st)
	}
}

// TestAcksFlushAlone: with nothing else going out, waiting
// acknowledgements are sent as a batched PacketAck rather than waiting
// forever.
func TestAcksFlushAlone(t *testing.T) {
	c := newCapture()
	s, stop := runSender(t, c, WithAckDelay(10*time.Millisecond))
	defer stop()

	for i := uint32(1); i <= 4; i++ {
		s.QueueAck(i)
	}
	waitFor(t, "the ack packet", func() bool { return c.count() == 1 })

	p := c.all()[0]
	_, n, err := DecodeHeader(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeBody(p[n:])
	if err != nil {
		t.Fatal(err)
	}
	ack, ok := got.(*PacketAck)
	if !ok {
		t.Fatalf("sent %T, want *PacketAck", got)
	}
	// All four batched into one datagram, unlike the C client's one
	// message per acknowledgement.
	if len(ack.Packets) != 4 {
		t.Fatalf("PacketAck carries %d, want 4", len(ack.Packets))
	}
	if st := s.Stats(); st.AckPackets != 1 || st.AcksAlone != 4 {
		t.Errorf("stats = %+v", st)
	}
}

func TestAcksBatchOverLimit(t *testing.T) {
	c := newCapture()
	s, stop := runSender(t, c, WithAckDelay(10*time.Millisecond))
	defer stop()

	const n = 600
	for i := uint32(0); i < n; i++ {
		s.QueueAck(i)
	}
	waitFor(t, "every ack to go out", func() bool { return s.Stats().AcksAlone >= n })

	total := 0
	for _, p := range c.all() {
		_, k, err := DecodeHeader(p)
		if err != nil {
			t.Fatal(err)
		}
		m, err := DecodeBody(p[k:])
		if err != nil {
			t.Fatal(err)
		}
		ack, ok := m.(*PacketAck)
		if !ok {
			t.Fatalf("got %T", m)
		}
		if len(ack.Packets) > maxAcksPerPacket {
			t.Errorf("%d acks in one message", len(ack.Packets))
		}
		total += len(ack.Packets)
	}
	if total != n {
		t.Errorf("%d acks arrived, want %d", total, n)
	}
}

func TestReliableRetransmits(t *testing.T) {
	c := newCapture()
	s, stop := runSender(t, c, WithRetransmit(20*time.Millisecond, 5))
	defer stop()

	if err := s.SendReliable(context.Background(), &CompletePingCheck{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a retransmission", func() bool { return s.Stats().Resent >= 1 })

	pkts := c.all()
	if len(pkts) < 2 {
		t.Fatalf("only %d packets", len(pkts))
	}
	h0, _, _ := DecodeHeader(pkts[0])
	if !h0.Reliable() || h0.Flags&FlagResent != 0 {
		t.Errorf("first transmission flags = %#x, should be reliable and not resent", h0.Flags)
	}
	h1, _, _ := DecodeHeader(pkts[1])
	if h1.Flags&FlagResent == 0 {
		t.Errorf("retransmission flags = %#x, want the resent bit", h1.Flags)
	}
	if h1.Sequence != h0.Sequence {
		t.Errorf("retransmission changed the sequence number")
	}
}

func TestReliableStopsOnConfirm(t *testing.T) {
	c := newCapture()
	s, stop := runSender(t, c, WithRetransmit(30*time.Millisecond, 5))
	defer stop()

	if err := s.SendReliable(context.Background(), &CompletePingCheck{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the first send", func() bool { return c.count() == 1 })

	h, _, _ := DecodeHeader(c.all()[0])
	s.ConfirmAck(h.Sequence)

	time.Sleep(150 * time.Millisecond)
	if st := s.Stats(); st.Resent != 0 {
		t.Errorf("resent %d times after confirmation", st.Resent)
	}
	if c.count() != 1 {
		t.Errorf("%d packets after confirmation, want 1", c.count())
	}
}

func TestReliableGivesUp(t *testing.T) {
	c := newCapture()
	s, stop := runSender(t, c, WithRetransmit(10*time.Millisecond, 3))
	defer stop()

	if err := s.SendReliable(context.Background(), &CompletePingCheck{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the sender to give up", func() bool { return s.Stats().Abandoned == 1 })

	before := c.count()
	time.Sleep(100 * time.Millisecond)
	if c.count() != before {
		t.Errorf("still retransmitting after giving up")
	}
}

func TestSendWriteError(t *testing.T) {
	c := newCapture()
	c.err = errors.New("network is down")
	s := NewSender(c)
	done := make(chan error, 1)
	go func() { done <- s.Run(context.Background()) }()

	_ = s.Send(context.Background(), &CompletePingCheck{})
	select {
	case err := <-done:
		if err == nil {
			t.Error("Run should return the write error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	// Send must not block once Run has stopped.
	if err := s.Send(context.Background(), &CompletePingCheck{}); !errors.Is(err, ErrSenderClosed) {
		t.Errorf("Send after close returned %v, want ErrSenderClosed", err)
	}
}

func TestQueueAckNeverBlocks(t *testing.T) {
	// No Run goroutine at all, so nothing drains the channel.
	s := NewSender(newCapture())
	done := make(chan struct{})
	go func() {
		for i := 0; i < 10000; i++ {
			s.QueueAck(uint32(i))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("QueueAck blocked")
	}
}

// TestSendReceiveLoopback runs a sender into a receiver over real UDP.
func TestSendReceiveLoopback(t *testing.T) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback UDP: %v", err)
	}
	defer conn.Close()

	r := NewReceiver(conn)
	rctx, rcancel := context.WithCancel(context.Background())
	rdone := make(chan error, 1)
	go func() { rdone <- r.Run(rctx) }()

	out, err := net.Dial("udp", conn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()

	sender := NewSender(out, WithAckDelay(10*time.Millisecond))
	sctx, scancel := context.WithCancel(context.Background())
	sdone := make(chan error, 1)
	go func() { sdone <- sender.Run(sctx) }()

	m := &ChatFromViewer{}
	m.ChatData.Message = []byte("over the wire")
	sender.QueueAck(77)
	if err := sender.SendReliable(context.Background(), m); err != nil {
		t.Fatal(err)
	}

	select {
	case p := <-r.C():
		chat, ok := p.Message.(*ChatFromViewer)
		if !ok {
			t.Fatalf("got %T (%v)", p.Message, p.Err)
		}
		if string(chat.ChatData.Message) != "over the wire" {
			t.Errorf("message = %q", chat.ChatData.Message)
		}
		if !p.Header.Reliable() {
			t.Error("reliable flag lost")
		}
		if len(p.Acks) != 1 || p.Acks[0] != 77 {
			t.Errorf("acks = %v, want [77]", p.Acks)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing arrived")
	}

	scancel()
	<-sdone
	rcancel()
	<-rdone
}
