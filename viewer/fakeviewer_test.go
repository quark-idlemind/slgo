package viewer

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// fakeViewer speaks the viewer half of a circuit, so the recording can
// be exercised over a real socket without a real viewer.
//
// It is the mirror of agent.fakeSim: that one answers a handshake the
// way a simulator does, this one opens one the way a viewer does.  The
// difference that matters is which end starts talking -- a viewer sends
// UseCircuitCode into the dark and waits, so anything standing in for a
// simulator has to be listening before the viewer says anything.
type fakeViewer struct {
	conn *net.UDPConn
	to   *net.UDPAddr

	mu   sync.Mutex
	seen []string
	msgs []msg.Message
	seq  uint32
}

func newFakeViewer(t *testing.T, to *net.UDPAddr) *fakeViewer {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Skipf("no loopback UDP: %v", err)
	}
	return &fakeViewer{conn: conn, to: to}
}

func (v *fakeViewer) close() { v.conn.Close() }

func (v *fakeViewer) got() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.seen...)
}

// run reads whatever the far end sends and records what it was.
func (v *fakeViewer) run() {
	buf := make([]byte, 8192)
	for {
		n, _, err := v.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		h, off, err := msg.DecodeHeader(buf[:n])
		if err != nil {
			continue
		}
		body := buf[off:n]
		if h.HasAcks() {
			if body, _, err = msg.SplitAcks(body); err != nil {
				continue
			}
		}
		if h.Zerocoded() {
			if body, err = msg.ZeroExpand(nil, body); err != nil {
				continue
			}
		}
		if len(body) == 0 {
			continue // nothing but acknowledgements
		}
		m, err := msg.DecodeBody(body)
		if err != nil {
			continue
		}
		v.mu.Lock()
		v.seen = append(v.seen, m.MsgInfo().Name)
		v.msgs = append(v.msgs, m)
		v.mu.Unlock()
	}
}

// send writes one message, choosing its own sequence number.  A viewer
// numbers from 1 and knows nothing of the other circuit's numbering,
// which is the whole point of terminating reliability at each end.
func (v *fakeViewer) send(m msg.Message, flags uint8) {
	v.mu.Lock()
	v.seq++
	seq := v.seq
	v.mu.Unlock()
	v.sendSeq(m, flags, seq)
}

// sendSeq writes a message under a chosen sequence number, so a test can
// retransmit one deliberately.
func (v *fakeViewer) sendSeq(m msg.Message, flags uint8, seq uint32) {
	body, err := m.Encode()
	if err != nil {
		return
	}
	out := msg.AppendHeader(nil, &msg.Header{Flags: flags, Sequence: seq})
	out = msg.AppendID(out, msg.IDOf(m))
	out = append(out, body...)
	v.conn.WriteToUDP(out, v.to)
}

// connect is what a viewer does on arrival: claim the circuit, then ask
// to be put in the region.
//
// All three fields of the CircuitCode block are filled in, as a real
// viewer fills them from its login response.  They used to be a code
// and two zero uuids, which was enough while the circuit answered
// whoever spoke and checkCircuit only grumbled at a mismatch; now the
// session id is what admits the sender, so a fake viewer that sent
// less than a real one would be testing a door it had walked around.
func (v *fakeViewer) connect(code uint32) {
	uc := &msg.UseCircuitCode{}
	uc.CircuitCode.Code = code
	uc.CircuitCode.SessionID = testSessionID
	uc.CircuitCode.ID = testAgentID
	v.send(uc, msg.FlagReliable)

	cam := &msg.CompleteAgentMovement{}
	cam.AgentData.CircuitCode = code
	v.send(cam, msg.FlagReliable)
}

func (v *fakeViewer) waitSeen(t *testing.T, name string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		for _, n := range v.got() {
			if n == name {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the viewer was never told %s; it heard %v", name, v.got())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// endpoint stands in for slgod's viewer-facing side: a socket, the
// ordinary receive and dispatch machinery, and the recording wired to
// the relay hook.
//
// There is no handover logic here on purpose.  Stage 1 of
// doc/history/viewer-frontend.md was about proving the record is
// trustworthy before anything depended on it, so this endpoint forwards
// nothing and decides nothing -- it only writes down what arrived.
type endpoint struct {
	conn   *net.UDPConn
	census *Census
	trace  *Trace
	disp   *msg.Dispatcher
	send   *msg.Sender

	mu   sync.Mutex
	peer *net.UDPAddr
}

// Write satisfies msg.PacketWriter by sending to whichever address was
// heard from first.  A simulator answers the circuit that called it;
// there is nowhere else to send.
func (e *endpoint) Write(p []byte) (int, error) {
	e.mu.Lock()
	peer := e.peer
	e.mu.Unlock()
	if peer == nil {
		return len(p), nil // nobody has spoken yet; drop it
	}
	return e.conn.WriteToUDP(p, peer)
}

func (e *endpoint) note(addr net.Addr) {
	ua, ok := addr.(*net.UDPAddr)
	if !ok {
		return
	}
	e.mu.Lock()
	if e.peer == nil {
		e.peer = ua
	}
	e.mu.Unlock()
}

func newEndpoint(t *testing.T, census *Census, trace *Trace) (*endpoint, func()) {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Skipf("no loopback UDP: %v", err)
	}

	e := &endpoint{conn: conn, census: census, trace: trace}
	e.send = msg.NewSender(e)
	e.disp = msg.NewDispatcher(
		msg.WithSender(e.send),
		// The peer address is learned from the tap rather than the
		// relay, because it has to be known for every packet
		// including the ones the relay never sees.
		msg.WithTap(func(p *msg.Packet) { e.note(p.Addr) }),
		msg.WithRelay(func(p *msg.Packet) {
			name := MessageName(p)
			census.Record(name, FromViewer, p.At, NoViewer)
			trace.Write(FromViewer, p, NoViewer)
		}),
	)

	// KeepBody is what a relay wants: a message this build cannot
	// decode is still a message worth passing on.
	recv := msg.NewReceiver(conn, msg.KeepBody())

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); recv.Run(ctx) }()
	go func() { defer wg.Done(); e.send.Run(ctx) }()
	go func() { defer wg.Done(); e.disp.Run(ctx, recv.C()) }()

	return e, func() {
		cancel()
		conn.Close()
		wg.Wait()
	}
}

func (e *endpoint) addr() *net.UDPAddr { return e.conn.LocalAddr().(*net.UDPAddr) }

// waitRecorded blocks until the census has seen a message go one way.
func waitRecorded(t *testing.T, c *Census, name string, dir Direction, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if c.Seen(name, dir) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s %s was never recorded; the census holds:\n%s", name, dir, c.Report())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestFakeViewerHandshakeIsRecorded drives the viewer half of a
// handshake over a real socket and checks the record shows it.  This is
// the verification of stage 1 of doc/history/viewer-frontend.md: nothing
// is relayed and nothing renders, but what crossed is written down.
func TestFakeViewerHandshakeIsRecorded(t *testing.T) {
	census := NewCensus()
	trace := NewTrace(nil, nil, false) // exercised separately below

	e, stop := newEndpoint(t, census, trace)
	defer stop()

	v := newFakeViewer(t, e.addr())
	defer v.close()
	go v.run()

	const circuit = 690139535
	v.connect(circuit)

	waitRecorded(t, census, "UseCircuitCode", FromViewer, 5*time.Second)
	waitRecorded(t, census, "CompleteAgentMovement", FromViewer, 5*time.Second)

	// The direction has to be right, not merely present: a message
	// recorded the wrong way is the failure this stage exists to
	// catch, and it would pass a test that only asked "was it seen".
	if census.Seen("UseCircuitCode", ToViewer) {
		t.Error("UseCircuitCode recorded as going TO the viewer")
	}
	if census.Total() < 2 {
		t.Errorf("census total = %d:\n%s", census.Total(), census.Report())
	}
}

// TestRelayIgnoresAViewerRetransmission is the WithRelay fix proved on a
// real circuit rather than on hand-built packets.
//
// A viewer retransmits anything reliable that goes unacknowledged, and
// under the old tap the relay would have seen both copies and forwarded
// two messages the far end could not tell apart.  Here the same
// sequence number is sent three times and must be recorded once.
func TestRelayIgnoresAViewerRetransmission(t *testing.T) {
	census := NewCensus()
	e, stop := newEndpoint(t, census, nil)
	defer stop()

	v := newFakeViewer(t, e.addr())
	defer v.close()
	go v.run()

	uc := &msg.UseCircuitCode{}
	uc.CircuitCode.Code = 42
	// The same packet, thrice: sent, then resent twice, flagged RESENT
	// as a viewer flags a retransmission.
	v.sendSeq(uc, msg.FlagReliable, 1)
	for i := 0; i < 2; i++ {
		v.sendSeq(uc, msg.FlagReliable|msg.FlagResent, 1)
	}

	waitRecorded(t, census, "UseCircuitCode", FromViewer, 5*time.Second)
	// Give the other two every chance to be wrongly recorded.
	time.Sleep(200 * time.Millisecond)

	var got uint64
	for _, row := range census.Counts() {
		if row.Name == "UseCircuitCode" && row.Dir == FromViewer {
			got = row.Packets
		}
	}
	if got != 1 {
		t.Errorf("a retransmitted message was recorded %d times, want 1:\n%s",
			got, census.Report())
	}
	if st := e.disp.Stats(); st.Duplicates != 2 {
		t.Errorf("dispatcher stats = %+v, want 2 duplicates suppressed", st)
	}
}

// TestEndpointAcknowledgesTheViewer: the endpoint must acknowledge what
// the viewer sends reliably, or the viewer retransmits until it gives
// up and the session looks dead from the far side.
func TestEndpointAcknowledgesTheViewer(t *testing.T) {
	census := NewCensus()
	e, stop := newEndpoint(t, census, nil)
	defer stop()

	v := newFakeViewer(t, e.addr())
	defer v.close()
	go v.run()

	v.connect(7)
	waitRecorded(t, census, "CompleteAgentMovement", FromViewer, 5*time.Second)

	// PacketAck is how the acknowledgements come back when there is
	// no other traffic to carry them.
	v.waitSeen(t, "PacketAck", 5*time.Second)
}

// TestTraceRecordsALiveCircuit checks the trace against real traffic
// rather than synthetic packets, since the fields it prints come from
// the receiver and not from the test.
func TestTraceRecordsALiveCircuit(t *testing.T) {
	var buf syncBuffer
	census := NewCensus()
	trace := NewTrace(&buf, nil, false)

	e, stop := newEndpoint(t, census, trace)
	defer stop()

	v := newFakeViewer(t, e.addr())
	defer v.close()
	go v.run()

	v.connect(99)
	waitRecorded(t, census, "CompleteAgentMovement", FromViewer, 5*time.Second)

	out := buf.String()
	for _, want := range []string{"viewer>slgod", "UseCircuitCode", "CompleteAgentMovement", "seq="} {
		if !strings.Contains(out, want) {
			t.Errorf("trace is missing %q:\n%s", want, out)
		}
	}
}

// syncBuffer is a bytes.Buffer that can be read while the circuit is
// still writing to it.
type syncBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b = append(s.b, p...)
	return len(p), nil
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.b)
}

// last returns the most recent message of a given name, so a test can
// look inside what the viewer was told rather than only counting it.
func (v *fakeViewer) last(t *testing.T, name string) msg.Message {
	t.Helper()
	v.mu.Lock()
	defer v.mu.Unlock()
	for i := len(v.msgs) - 1; i >= 0; i-- {
		if v.msgs[i].MsgInfo().Name == name {
			return v.msgs[i]
		}
	}
	t.Fatalf("the viewer was never sent %s; it heard %v", name, v.seen)
	return nil
}
