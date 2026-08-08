package agent

// A session with no circuit, so the handlers can be exercised without a
// simulator.
//
// Almost everything an Agent knows about the world it is in was put
// there by a handler reacting to one message: the object cache, the
// friend list, the region, the group memberships.  Reaching those
// through a real socket costs a handshake per test and leaves the
// arrival order to the network, which is the wrong price for asserting
// what one message did.
//
// So the packets are built here, handed to the dispatcher, and the
// dispatcher is run to exhaustion.  Every message goes out through the
// real encoder and comes back through the real decoder on the way in,
// so a handler sees what a simulator would have sent rather than the
// struct a test filled in -- the variable length blocks in particular,
// which are where a field that was never really on the wire would hide.

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// seq numbers the packets fed to a dispatcher.  It is package wide
// because the dispatcher suppresses duplicates by sequence number, and
// two tests that both started at one would be fine only by luck.
var seq atomic.Uint32

// sentPackets is the write half of a circuit that goes nowhere, keeping
// what was written so a test can read it back.
type sentPackets struct {
	mu sync.Mutex
	b  [][]byte
}

func (s *sentPackets) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b = append(s.b, append([]byte(nil), p...))
	return len(p), nil
}

func (s *sentPackets) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.b)
}

// messages decodes everything written, which is how a test checks what
// the session said rather than only that it said something.
func (s *sentPackets) messages(t *testing.T) []msg.Message {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []msg.Message
	for _, p := range s.b {
		_, off, err := msg.DecodeHeader(p)
		if err != nil {
			t.Fatalf("a datagram we wrote has no header: %v", err)
		}
		m, err := msg.DecodeBody(p[off:])
		if err != nil {
			t.Fatalf("a datagram we wrote would not decode: %v", err)
		}
		out = append(out, m)
	}
	return out
}

// waitFor blocks until n datagrams have been written.  Only the handlers
// that answer off the dispatch goroutine need it.
func (s *sentPackets) waitFor(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for s.count() < n {
		if time.Now().After(deadline) {
			t.Fatalf("only %d datagrams were sent, wanted %d", s.count(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

// offlineSession builds an Agent carrying everything Connect installs
// except the socket.
func offlineSession(t *testing.T) (*Agent, *sentPackets) {
	t.Helper()

	// A socket that goes nowhere, so Close has something real to close.
	// Nothing is written to it: the sender's write half is the recorder
	// below.
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Skipf("no loopback UDP: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	w := &sentPackets{}
	a := &Agent{
		Conn: conn,
		Account: &Account{
			AgentID:   msg.MustParseUUID("876e7e57-7e57-c0de-9eeb-1bd0e1ec6995"),
			SessionID: msg.MustParseUUID("8d1b7e57-7e57-c0de-f4f4-19d29d124acf"),
			FirstName: "Example",
			LastName:  "Resident",
		},
		Send:      msg.NewSender(w),
		Disp:      msg.NewDispatcher(),
		done:      make(chan struct{}),
		anyPacket: newSignal(),
		inRegion:  newSignal(),
		handshook: newSignal(),
		loggedOut: newSignal(),
	}
	a.register()

	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	go func() { _ = a.Send.Run(ctx) }()
	t.Cleanup(cancel)

	return a, w
}

// onTheWire encodes a message and decodes it again.
func onTheWire(t *testing.T, m msg.Message) msg.Message {
	t.Helper()
	body, err := m.Encode()
	if err != nil {
		t.Fatalf("encoding %s: %v", m.MsgInfo().Name, err)
	}
	out, err := msg.DecodeBody(append(msg.AppendID(nil, msg.IDOf(m)), body...))
	if err != nil {
		t.Fatalf("decoding %s: %v", m.MsgInfo().Name, err)
	}
	return out
}

// feed hands the messages to the dispatcher in order and returns once
// the last of them has been handled.
func feed(t *testing.T, a *Agent, ms ...msg.Message) {
	t.Helper()
	ch := make(chan *msg.Packet, len(ms))
	for _, m := range ms {
		ch <- &msg.Packet{
			At:      time.Now(),
			Header:  msg.Header{Sequence: seq.Add(1)},
			ID:      msg.IDOf(m),
			Message: onTheWire(t, m),
		}
	}
	close(ch)
	if err := a.Disp.Run(context.Background(), ch); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
}
