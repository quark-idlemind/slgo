package msg

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
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

// fail makes every write from now on report an error, which is how the
// paths that only a failure part way through a session can reach are
// arranged: a socket that stops working after the first packet.
func (c *capture) fail(err error) {
	c.mu.Lock()
	c.err = err
	c.mu.Unlock()
}

// gatedWriter holds the sender inside a write until it is let go, which
// is the only way to have anything else happen while Run is busy.
type gatedWriter struct {
	entered chan struct{}
	release chan struct{}
	err     error // what the write reports once it is let go
	writes  atomic.Int64
}

func newGatedWriter(err error) *gatedWriter {
	return &gatedWriter{entered: make(chan struct{}, 8), release: make(chan struct{}), err: err}
}

func (g *gatedWriter) Write(p []byte) (int, error) {
	g.writes.Add(1)
	select {
	case g.entered <- struct{}{}:
	default:
	}
	<-g.release
	if g.err != nil {
		return 0, g.err
	}
	return len(p), nil
}

// unencodable is a message that cannot be encoded.  Building one is a
// bug in the caller, and the sender's answer is to drop it rather than
// to take a working connection down over it.
type unencodable struct{}

var unencodableInfo = Info{Name: "Unencodable"}

func (*unencodable) MsgInfo() *Info          { return &unencodableInfo }
func (*unencodable) Encode() ([]byte, error) { return nil, errors.New("this message cannot be built") }
func (*unencodable) Decode([]byte) error     { return nil }

func TestWithSendBuffer(t *testing.T) {
	s := NewSender(newCapture(), WithSendBuffer(4))
	if cap(s.out) != 4 {
		t.Errorf("WithSendBuffer(4) gave a channel of %d", cap(s.out))
	}
}

// TestSendWithoutAContext: a caller with nothing to cancel by passes
// nil, and panicking on it would take the session down.
func TestSendWithoutAContext(t *testing.T) {
	c := newCapture()
	s, stop := runSender(t, c)
	defer stop()

	//lint:ignore SA1012 the nil is the point of the test
	if err := s.Send(nil, &CompletePingCheck{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the packet", func() bool { return c.count() == 1 })
}

// TestSendGivesUpWhenTheCallerDoes: with the queue full and nothing
// draining it, a caller that has been cancelled must not be left
// holding a message forever.
func TestSendGivesUpWhenTheCallerDoes(t *testing.T) {
	// No Run goroutine, so nothing ever takes the message.
	s := NewSender(newCapture(), WithSendBuffer(0))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Send(ctx, &CompletePingCheck{}); !errors.Is(err, context.Canceled) {
		t.Errorf("Send returned %v, want the context's error", err)
	}
}

// TestSendUnblocksWhenTheSenderStops is the other half of that: a Send
// already waiting on a full queue when Run gives up has to be told, not
// left there.  The plain three way select in queue is not enough on its
// own, which is why there is a check before it.
func TestSendUnblocksWhenTheSenderStops(t *testing.T) {
	t.Parallel()

	g := newGatedWriter(errors.New("network is down"))
	s := NewSender(g, WithSendBuffer(1))
	runDone := make(chan error, 1)
	go func() { runDone <- s.Run(context.Background()) }()

	// The first message reaches the writer and stays there.
	if err := s.Send(context.Background(), &CompletePingCheck{}); err != nil {
		t.Fatal(err)
	}
	<-g.entered

	// The second fills the queue and the third has to wait for it.
	if err := s.Send(context.Background(), &CompletePingCheck{}); err != nil {
		t.Fatal(err)
	}
	blocked := make(chan error, 1)
	go func() { blocked <- s.Send(context.Background(), &CompletePingCheck{}) }()
	time.Sleep(20 * time.Millisecond)

	// Letting the write go turns it into a failure, which stops Run
	// with the third message still queued behind it.
	close(g.release)

	select {
	case err := <-blocked:
		if !errors.Is(err, ErrSenderClosed) {
			t.Errorf("the waiting Send returned %v, want ErrSenderClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the waiting Send was never told the sender had stopped")
	}
	<-runDone
}

// TestUnencodableMessageIsDropped: the connection is fine and the next
// message must still go out.
func TestUnencodableMessageIsDropped(t *testing.T) {
	c := newCapture()
	s, stop := runSender(t, c)
	defer stop()

	if err := s.Send(context.Background(), &unencodable{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), &CompletePingCheck{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the good message", func() bool { return c.count() == 1 })

	h, _, err := DecodeHeader(c.all()[0])
	if err != nil {
		t.Fatal(err)
	}
	// The dropped message did not consume a sequence number either.
	if h.Sequence != 1 {
		t.Errorf("sequence = %d, want 1", h.Sequence)
	}
}

// TestAcksOnlyRideWhatTheyFitIn.  A datagram over the path's limit gets
// fragmented or dropped, so acknowledgements are attached only while
// there is room under it, and a message that is already at or over the
// limit carries none rather than being made worse.  The message itself
// is never split: what the caller asked to send goes as it stands.
func TestAcksOnlyRideWhatTheyFitIn(t *testing.T) {
	// Chosen so that exactly two acknowledgements fit: the body is
	// 1180 bytes and the room for them is what is left under
	// MaxDatagram after the header, the message number and the count.
	fits := chatOfBodySize(t, 1180)
	if room := (MaxDatagram - HeaderSize - 4 - 1180 - 1) / 4; room != 2 {
		t.Fatalf("the arithmetic moved: room for %d acks, expected 2", room)
	}
	// And one with no room whatever.
	full := chatOfBodySize(t, 1300)

	cases := []struct {
		what string
		m    Message
		want int
	}{
		{"a message with room for two", fits, 2},
		{"a message with no room at all", full, 0},
	}
	for _, c := range cases {
		cap := newCapture()
		s, stop := runSender(t, cap, WithAckDelay(time.Hour)) // never flush alone

		for i := uint32(1); i <= 5; i++ {
			s.QueueAck(i)
		}
		if err := s.Send(context.Background(), c.m); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the packet", func() bool { return cap.count() == 1 })

		p := cap.all()[0]
		h, n, err := DecodeHeader(p)
		if err != nil {
			t.Fatal(err)
		}
		got := 0
		if h.HasAcks() {
			_, acks, err := SplitAcks(p[n:])
			if err != nil {
				t.Fatal(err)
			}
			got = len(acks)
		}
		if got != c.want {
			t.Errorf("%s carried %d acks, want %d", c.what, got, c.want)
		}
		stop()
	}
}

// chatOfBodySize builds a ChatFromViewer whose encoded body is exactly
// n bytes: two UUIDs of AgentData, a two byte length prefix, the text,
// and the type and channel that follow it.
func chatOfBodySize(t *testing.T, n int) *ChatFromViewer {
	t.Helper()
	const overhead = 16*2 + 2 + 1 + 4
	m := &ChatFromViewer{}
	m.ChatData.Message = bytes.Repeat([]byte("x"), n-overhead)
	b, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != n {
		t.Fatalf("the body came to %d bytes, wanted %d", len(b), n)
	}
	return m
}

// TestTheFlushTimerIsDisarmedByAMessage: acknowledgements that rode out
// on a message are gone, and a flush left armed behind them would send
// a PacketAck with nothing in it.
func TestTheFlushTimerIsDisarmedByAMessage(t *testing.T) {
	t.Parallel()

	g := newGatedWriter(nil)
	s := NewSender(g, WithAckDelay(20*time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	// The acknowledgement arms the flush.
	s.QueueAck(11)
	time.Sleep(5 * time.Millisecond)

	// The message takes it with it, and stays in the write for long
	// enough that the flush comes due while it is in there.
	if err := s.Send(context.Background(), &CompletePingCheck{}); err != nil {
		t.Fatal(err)
	}
	<-g.entered
	time.Sleep(40 * time.Millisecond)
	close(g.release)

	// Nothing further should go out: the acknowledgement is spent.
	time.Sleep(50 * time.Millisecond)
	if n := g.writes.Load(); n != 1 {
		t.Errorf("%d datagrams went out, want 1 -- the flush fired on an empty queue", n)
	}
	if st := s.Stats(); st.AcksCarried != 1 || st.AckPackets != 0 {
		t.Errorf("stats = %+v", st)
	}
	cancel()
	<-done
}

// TestSendStopsWhenAcksCannotBeFlushed, by both routes into flushAcks:
// the timer coming due, and the queue reaching the limit of the one
// byte count that describes it.
func TestSendStopsWhenAcksCannotBeFlushed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		what  string
		acks  int
		delay time.Duration
	}{
		{"the timer came due", 1, 10 * time.Millisecond},
		{"the queue filled", maxAcksPerPacket + 20, time.Hour},
	}
	for _, c := range cases {
		cap := newCapture()
		cap.fail(errors.New("network is down"))
		s := NewSender(cap, WithAckDelay(c.delay))
		for i := 0; i < c.acks; i++ {
			s.QueueAck(uint32(i))
		}

		done := make(chan error, 1)
		go func() { done <- s.Run(context.Background()) }()
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("%s: Run should report the write error", c.what)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: Run did not return", c.what)
		}
	}
}

// TestSendStopsWhenARetransmissionCannotGoOut: the socket worked when
// the message was first sent and stopped working before it was
// acknowledged, which is what a network going away looks like.
func TestSendStopsWhenARetransmissionCannotGoOut(t *testing.T) {
	t.Parallel()

	c := newCapture()
	s := NewSender(c, WithRetransmit(20*time.Millisecond, 5))
	done := make(chan error, 1)
	go func() { done <- s.Run(context.Background()) }()

	if err := s.SendReliable(context.Background(), &CompletePingCheck{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the first send", func() bool { return c.count() == 1 })
	c.fail(errors.New("network is down"))

	select {
	case err := <-done:
		if err == nil {
			t.Error("Run should report the failed retransmission")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
}

// TestSendWithoutARunGoroutineDoesNotBlockForever guards the check that
// comes before the queue: with Run already stopped the message has
// nowhere to go, and the buffered channel would otherwise accept it and
// say nothing.
func TestSendAfterRunStopped(t *testing.T) {
	c := newCapture()
	s := NewSender(c)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	cancel()
	<-done

	if err := s.Send(context.Background(), &CompletePingCheck{}); !errors.Is(err, ErrSenderClosed) {
		t.Errorf("Send returned %v, want ErrSenderClosed", err)
	}
	if c.count() != 0 {
		t.Errorf("%d datagrams went out after Run stopped", c.count())
	}
}

// TestSenderRunWithoutAContext: nil is what a caller with nothing to
// cancel by passes, and panicking on it would take the session down.
func TestSenderRunWithoutAContext(t *testing.T) {
	c := newCapture()
	c.fail(errors.New("network is down"))
	s := NewSender(c)

	done := make(chan error, 1)
	//lint:ignore SA1012 the nil is the point of the test
	go func() { done <- s.Run(nil) }()

	_ = s.Send(context.Background(), &CompletePingCheck{})
	select {
	case err := <-done:
		if err == nil {
			t.Error("Run should still report the write error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
}

// TestSendTapReportsWhatWentOut is the outbound half of a trace.  The
// question a relay gets asked is whether it ever sent the thing, and a
// record of what arrived cannot answer that.
func TestSendTapReportsWhatWentOut(t *testing.T) {
	c := newCapture()
	var mu sync.Mutex
	var seen []uint32
	s, stop := runSender(t, c, WithSendTap(func(p *Packet) {
		mu.Lock()
		defer mu.Unlock()
		if p.Message == nil {
			t.Error("the send tap was given a packet with no message")
		}
		seen = append(seen, p.Header.Sequence)
	}))
	defer stop()

	for i := 0; i < 3; i++ {
		if err := s.Send(context.Background(), &CompletePingCheck{}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "three sends", func() bool { return c.count() == 3 })

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 3 {
		t.Fatalf("the tap saw %v, want three sends", seen)
	}
	// The sequence numbers are the ones the wire carried, which is the
	// point: a trace has to be matchable against a capture.
	for i := 1; i < len(seen); i++ {
		if seen[i] <= seen[i-1] {
			t.Errorf("sequence numbers not increasing: %v", seen)
		}
	}
}

// TestSendTapIgnoresRetransmissions: a resend carries the original's
// sequence number and bytes, so reporting it as a fresh send would show
// one message as several.
func TestSendTapIgnoresRetransmissions(t *testing.T) {
	c := newCapture()
	var tapped atomic.Int64
	s, stop := runSender(t, c,
		WithRetransmit(30*time.Millisecond, 5),
		WithSendTap(func(*Packet) { tapped.Add(1) }))
	defer stop()

	// Reliable and never acknowledged, so it retransmits.
	if err := s.SendReliable(context.Background(), &CompletePingCheck{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the retransmissions", func() bool { return c.count() >= 3 })

	if got := tapped.Load(); got != 1 {
		t.Errorf("the tap saw %d sends of one retransmitted message, want 1", got)
	}
}

// TestForgetDropsWhatWasMeantForAPeerThatHasGone: a circuit whose peer
// has been replaced is still holding the tail of the last conversation,
// and retransmitting it delivers that tail into the start of the next
// one.  The LogoutReply a viewer quit without acknowledging is the case
// that bites: arriving at its replacement, it logs that one straight out
// again.
func TestForgetDropsWhatWasMeantForAPeerThatHasGone(t *testing.T) {
	c := newCapture()
	s, stop := runSender(t, c, WithRetransmit(100*time.Millisecond, 20))
	defer stop()

	if err := s.SendReliable(context.Background(), &CompletePingCheck{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the first send", func() bool { return c.count() == 1 })

	if err := s.Forget(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Several retransmission rounds' worth of doing nothing.
	time.Sleep(500 * time.Millisecond)
	if st := s.Stats(); st.Resent != 0 {
		t.Errorf("resent %d times to a peer that has gone", st.Resent)
	}
	if c.count() != 1 {
		t.Errorf("%d packets went out, want the one from before the peer changed", c.count())
	}
}

// TestForgetIsOrderedAgainstTheSendsAroundIt is why forgetting travels
// on the message channel rather than one of its own.
//
// Run selects between its channels at random, so a forget asked for
// before the replacement's handshake was composed could be served after
// it -- and would then drop from the retransmission list the one message
// the new peer cannot start without.
func TestForgetIsOrderedAgainstTheSendsAroundIt(t *testing.T) {
	c := newCapture()
	s, stop := runSender(t, c, WithRetransmit(30*time.Millisecond, 20))
	defer stop()

	if err := s.Forget(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.SendReliable(context.Background(), &CompletePingCheck{}); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "the new peer's message to be retransmitted", func() bool {
		return s.Stats().Resent >= 1
	})
}
