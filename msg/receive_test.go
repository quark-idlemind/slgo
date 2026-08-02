package msg

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

// fakeConn serves a fixed list of datagrams and then blocks until its
// read deadline is set in the past, which is how Run is cancelled.
type fakeConn struct {
	mu       sync.Mutex
	pkts     [][]byte
	i        int
	deadline chan struct{}
	once     sync.Once
	err      error // returned instead of blocking, when set
}

func newFakeConn(pkts ...[]byte) *fakeConn {
	return &fakeConn{pkts: pkts, deadline: make(chan struct{})}
}

func (c *fakeConn) ReadFrom(p []byte) (int, net.Addr, error) {
	c.mu.Lock()
	if c.i < len(c.pkts) {
		n := copy(p, c.pkts[c.i])
		c.i++
		c.mu.Unlock()
		return n, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 13010}, nil
	}
	err := c.err
	c.mu.Unlock()

	if err != nil {
		return 0, nil, err
	}
	<-c.deadline
	return 0, nil, &net.OpError{Op: "read", Err: errTimeout{}}
}

func (c *fakeConn) SetReadDeadline(t time.Time) error {
	c.once.Do(func() { close(c.deadline) })
	return nil
}

type errTimeout struct{}

func (errTimeout) Error() string { return "i/o timeout" }
func (errTimeout) Timeout() bool { return true }

// packet assembles a datagram the way a simulator would.
func packet(t *testing.T, flags uint8, seq uint32, m Message, acks ...uint32) []byte {
	t.Helper()
	body, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	payload := AppendID(nil, m.MsgInfo().ID)
	payload = append(payload, body...)

	if len(acks) > 0 {
		flags |= FlagAck
	}
	out := AppendHeader(nil, &Header{Flags: flags, Sequence: seq})
	if flags&FlagZerocoded != 0 {
		out = ZeroCollapse(out, payload)
	} else {
		out = append(out, payload...)
	}
	// Acks ride on the tail, after any zero coding.  SplitAcks reads
	// them back from the end, so they come out reversed.
	for _, a := range acks {
		out = append(out, byte(a>>24), byte(a>>16), byte(a>>8), byte(a))
	}
	if len(acks) > 0 {
		out = append(out, byte(len(acks)))
	}
	return out
}

// collect runs a receiver over a fake connection and returns everything
// it produced.
func collect(t *testing.T, conn *fakeConn, opts ...ReceiverOption) ([]*Packet, Stats) {
	t.Helper()
	r := NewReceiver(conn, opts...)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	var got []*Packet
	timeout := time.After(5 * time.Second)
	want := len(conn.pkts)
	for len(got) < want {
		select {
		case p, ok := <-r.C():
			if !ok {
				t.Fatal("channel closed early")
			}
			got = append(got, p)
		case <-timeout:
			t.Fatalf("timed out with %d of %d packets", len(got), want)
		}
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run returned %v", err)
	}
	if _, ok := <-r.C(); ok {
		t.Error("channel should be closed after Run returns")
	}
	return got, r.Stats()
}

func TestReceivePlain(t *testing.T) {
	m := &CompletePingCheck{}
	m.PingID.PingID = 7

	got, st := collect(t, newFakeConn(packet(t, FlagReliable, 99, m)))
	if len(got) != 1 {
		t.Fatalf("got %d packets", len(got))
	}
	p := got[0]
	if p.Err != nil {
		t.Fatalf("Err = %v", p.Err)
	}
	ping, ok := p.Message.(*CompletePingCheck)
	if !ok {
		t.Fatalf("Message is %T", p.Message)
	}
	if ping.PingID.PingID != 7 {
		t.Errorf("PingID = %d", ping.PingID.PingID)
	}
	if p.Header.Sequence != 99 || !p.Header.Reliable() {
		t.Errorf("header = %+v", p.Header)
	}
	if p.Addr == nil || p.At.IsZero() {
		t.Error("Addr and At should be set")
	}
	if st.Packets != 1 || st.Failed != 0 || st.Unknown != 0 {
		t.Errorf("stats = %+v", st)
	}
}

func TestReceiveZerocoded(t *testing.T) {
	m := &UseCircuitCode{}
	m.CircuitCode.Code = 0x12345678
	m.CircuitCode.SessionID = MustParseUUID("8d1b7e57-7e57-c0de-f4f4-19d29d124acf")
	// ID left zero, so the body has a long run for the coder to eat.

	raw := packet(t, FlagZerocoded, 1, m)
	got, _ := collect(t, newFakeConn(raw))

	ucc, ok := got[0].Message.(*UseCircuitCode)
	if !ok {
		t.Fatalf("Message is %T (err %v)", got[0].Message, got[0].Err)
	}
	if ucc.CircuitCode.Code != 0x12345678 || ucc.CircuitCode.SessionID != m.CircuitCode.SessionID {
		t.Errorf("have %+v", ucc.CircuitCode)
	}
	if !ucc.CircuitCode.ID.IsZero() {
		t.Errorf("ID should be zero, got %v", ucc.CircuitCode.ID)
	}
}

func TestReceiveWithAcks(t *testing.T) {
	m := &CompletePingCheck{}
	m.PingID.PingID = 3

	got, _ := collect(t, newFakeConn(packet(t, 0, 5, m, 11, 22, 33)))
	p := got[0]
	if p.Err != nil {
		t.Fatalf("Err = %v", p.Err)
	}
	if p.Message == nil {
		t.Fatal("message should still decode alongside acks")
	}
	if len(p.Acks) != 3 {
		t.Fatalf("acks = %v", p.Acks)
	}
	// SplitAcks walks back from the tail, matching s.c.
	want := []uint32{33, 22, 11}
	for i, w := range want {
		if p.Acks[i] != w {
			t.Errorf("acks = %v, want %v", p.Acks, want)
			break
		}
	}
}

// TestReceiveAckOnly is a packet that carries no message at all, which
// the C client drops after queueing the acks.  Here it is delivered so
// the acks are not lost.
func TestReceiveAckOnly(t *testing.T) {
	out := AppendHeader(nil, &Header{Flags: FlagAck, Sequence: 8})
	out = append(out, 0x00, 0x00, 0x00, 0x2a) // ack 42
	out = append(out, 0x01)

	got, st := collect(t, newFakeConn(out))
	p := got[0]
	if p.Message != nil || p.Err != nil {
		t.Fatalf("expected an ack-only packet, got message=%v err=%v", p.Message, p.Err)
	}
	if len(p.Acks) != 1 || p.Acks[0] != 42 {
		t.Errorf("acks = %v", p.Acks)
	}
	if st.Failed != 0 {
		t.Errorf("ack-only packet counted as a failure: %+v", st)
	}
}

func TestReceiveUnknownMessage(t *testing.T) {
	// High 253 is not in the template.
	out := AppendHeader(nil, &Header{Sequence: 1})
	out = append(out, 253, 0xde, 0xad)

	got, st := collect(t, newFakeConn(out))
	p := got[0]
	if !errors.Is(p.Err, ErrUnknownMessage) {
		t.Fatalf("Err = %v, want ErrUnknownMessage", p.Err)
	}
	if string(p.Body) != "\xde\xad" {
		t.Errorf("Body = %x, want dead", p.Body)
	}
	if st.Unknown != 1 {
		t.Errorf("stats = %+v", st)
	}
}

func TestReceiveBadBodyKeepsAcks(t *testing.T) {
	// UseCircuitCode truncated to two bytes, with an ack attached.
	out := AppendHeader(nil, &Header{Flags: FlagAck, Sequence: 2})
	out = AppendID(out, MakeID(FreqLow, 3))
	out = append(out, 0x01, 0x02)
	out = append(out, 0x00, 0x00, 0x00, 0x07, 0x01) // ack 7

	got, st := collect(t, newFakeConn(out))
	p := got[0]
	if p.Err == nil {
		t.Fatal("expected a decode error")
	}
	if len(p.Acks) != 1 || p.Acks[0] != 7 {
		t.Errorf("acks lost on a bad body: %v", p.Acks)
	}
	if st.Failed != 1 {
		t.Errorf("stats = %+v", st)
	}
}

// TestReceiveRuntSkipped: a datagram too small to hold a header is
// counted and dropped, and does not stop the loop.
func TestReceiveRuntSkipped(t *testing.T) {
	m := &CompletePingCheck{}
	m.PingID.PingID = 1

	conn := newFakeConn([]byte{1, 2, 3}, packet(t, 0, 1, m))
	r := NewReceiver(conn)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	select {
	case p := <-r.C():
		if p.Message == nil {
			t.Fatalf("expected the good packet, got err %v", p.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the runt stopped the loop")
	}
	cancel()
	<-done
	if st := r.Stats(); st.Runts != 1 || st.Packets != 2 {
		t.Errorf("stats = %+v", st)
	}
}

// TestReceiveNoAliasing is the one that matters for buffer reuse: a
// message delivered on the channel must not be disturbed by the next
// datagram read into the same buffer.
func TestReceiveNoAliasing(t *testing.T) {
	first := &ChatFromViewer{}
	first.ChatData.Message = []byte("first message")
	second := &ChatFromViewer{}
	second.ChatData.Message = []byte("SECOND!!!!!!!")

	got, _ := collect(t, newFakeConn(
		packet(t, 0, 1, first),
		packet(t, FlagZerocoded, 2, second),
	))
	if len(got) != 2 {
		t.Fatalf("got %d packets", len(got))
	}
	a, ok := got[0].Message.(*ChatFromViewer)
	if !ok {
		t.Fatalf("first is %T (%v)", got[0].Message, got[0].Err)
	}
	b, ok := got[1].Message.(*ChatFromViewer)
	if !ok {
		t.Fatalf("second is %T (%v)", got[1].Message, got[1].Err)
	}
	if string(a.ChatData.Message) != "first message" {
		t.Errorf("first message was clobbered: %q", a.ChatData.Message)
	}
	if string(b.ChatData.Message) != "SECOND!!!!!!!" {
		t.Errorf("second message = %q", b.ChatData.Message)
	}
}

func TestReceiveCancelClosesChannel(t *testing.T) {
	conn := newFakeConn()
	r := NewReceiver(conn)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("cancelled Run returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop on cancellation")
	}
	if _, ok := <-r.C(); ok {
		t.Error("channel not closed")
	}
}

func TestReceiveReadError(t *testing.T) {
	conn := newFakeConn()
	conn.err = errors.New("connection closed")
	r := NewReceiver(conn)

	err := r.Run(context.Background())
	if err == nil || err.Error() != "connection closed" {
		t.Errorf("Run returned %v, want the read error", err)
	}
	if _, ok := <-r.C(); ok {
		t.Error("channel not closed after a read error")
	}
}

func TestReceiveDropWhenFull(t *testing.T) {
	m := &CompletePingCheck{}
	m.PingID.PingID = 1

	var pkts [][]byte
	for i := 0; i < 20; i++ {
		pkts = append(pkts, packet(t, 0, uint32(i), m))
	}
	conn := newFakeConn(pkts...)
	r := NewReceiver(conn, WithBuffer(1), DropWhenFull())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	// Let it run without draining, so the channel fills.
	deadline := time.After(5 * time.Second)
	for r.Stats().Packets < 20 {
		select {
		case <-deadline:
			t.Fatalf("only read %d packets", r.Stats().Packets)
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	<-done

	st := r.Stats()
	if st.Dropped == 0 {
		t.Errorf("nothing dropped with a full channel: %+v", st)
	}
	if st.Packets != 20 {
		t.Errorf("read %d packets, want 20", st.Packets)
	}
}

// TestReceiveOverUDP runs the whole thing over a real socket.
func TestReceiveOverUDP(t *testing.T) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback UDP: %v", err)
	}
	defer conn.Close()

	r := NewReceiver(conn)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	m := &CompletePingCheck{}
	m.PingID.PingID = 21
	out := packet(t, FlagReliable, 4242, m)

	send, err := net.Dial("udp", conn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer send.Close()
	if _, err := send.Write(out); err != nil {
		t.Fatal(err)
	}

	select {
	case p := <-r.C():
		ping, ok := p.Message.(*CompletePingCheck)
		if !ok {
			t.Fatalf("got %T (%v)", p.Message, p.Err)
		}
		if ping.PingID.PingID != 21 || p.Header.Sequence != 4242 {
			t.Errorf("ping %d seq %d", ping.PingID.PingID, p.Header.Sequence)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing arrived")
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run returned %v", err)
	}
}
