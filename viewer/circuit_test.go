package viewer

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
)

// simStub is the simulator end of the session slgod is holding, so that
// a test can see what the relay forwarded to the grid.
type simStub struct {
	conn *net.UDPConn

	mu   sync.Mutex
	seen []string
	peer *net.UDPAddr
	seq  uint32
}

func newSimStub(t *testing.T) *simStub {
	t.Helper()
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Skipf("no loopback UDP: %v", err)
	}
	s := &simStub{conn: conn}
	go s.run()
	t.Cleanup(func() { conn.Close() })
	return s
}

func (s *simStub) addr() *net.UDPAddr { return s.conn.LocalAddr().(*net.UDPAddr) }

func (s *simStub) got() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

func (s *simStub) run() {
	buf := make([]byte, 8192)
	for {
		n, peer, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		s.mu.Lock()
		s.peer = peer
		s.mu.Unlock()

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
			continue
		}
		m, err := msg.DecodeBody(body)
		if err != nil {
			continue
		}
		name := m.MsgInfo().Name
		s.mu.Lock()
		s.seen = append(s.seen, name)
		s.mu.Unlock()

		if h.Reliable() {
			s.send(&msg.PacketAck{Packets: []msg.PacketAck_Packets{{ID: h.Sequence}}}, 0)
		}
		switch name {
		case "UseCircuitCode":
			rh := &msg.RegionHandshake{}
			rh.RegionInfo.SimName = []byte("Dovet\x00")
			s.send(rh, msg.FlagReliable)
		case "CompleteAgentMovement":
			amc := &msg.AgentMovementComplete{}
			amc.Data.Position = msg.Vector3{X: 128, Y: 129, Z: 2001}
			amc.Data.LookAt = msg.Vector3{X: 1}
			amc.Data.RegionHandle = 0x0003_f000_0003_e800
			amc.SimData.ChannelVersion = []byte("Second Life Server test\x00")
			s.send(amc, msg.FlagReliable)
		}
	}
}

func (s *simStub) send(m msg.Message, flags uint8) {
	s.mu.Lock()
	peer := s.peer
	s.seq++
	seq := s.seq
	s.mu.Unlock()
	if peer == nil {
		return
	}
	body, err := m.Encode()
	if err != nil {
		return
	}
	out := msg.AppendHeader(nil, &msg.Header{Flags: flags, Sequence: seq})
	out = msg.AppendID(out, msg.IDOf(m))
	out = append(out, body...)
	s.conn.WriteToUDP(out, peer)
}

func (s *simStub) waitSeen(t *testing.T, name string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		for _, n := range s.got() {
			if n == name {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the simulator was never sent %s; it saw %v", name, s.got())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (s *simStub) never(t *testing.T, name string) {
	t.Helper()
	for _, n := range s.got() {
		if n == name {
			t.Fatalf("%s reached the simulator; it saw %v", name, s.got())
		}
	}
}

const testCircuitCode = 690139535

// handedOver stands up a session against simStub and a viewer circuit
// in front of it, which is the arrangement slgod has when a viewer
// attaches.
func handedOver(t *testing.T) (*simStub, *agent.Agent, *Circuit, *fakeViewer, *Census) {
	t.Helper()
	sim := newSimStub(t)

	acct := &agent.Account{
		AgentID:     msg.MustParseUUID("876e7e57-7e57-c0de-9eeb-1bd0e1ec6995"),
		SessionID:   msg.MustParseUUID("8d1b7e57-7e57-c0de-f4f4-19d29d124acf"),
		CircuitCode: testCircuitCode,
		SimIP:       sim.addr().IP,
		SimPort:     sim.addr().Port,
		FirstName:   "Taren",
		LastName:    "Holt",
	}

	ctx, cancel := context.WithCancel(context.Background())
	a, err := agent.Connect(ctx, acct, agent.Options{
		Timeout: 10 * time.Second, SkipCaps: true, Idle: -1,
	})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close(); cancel() })

	census := NewCensus()
	c, err := Listen("127.0.0.1", a, census, nil, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	c.Run(ctx)
	t.Cleanup(c.Close)

	v := newFakeViewer(t, c.Addr())
	go v.run()
	t.Cleanup(v.close)

	return sim, a, c, v, census
}

// TestViewerJoinsAnExistingSession is stage 2 in one test: the viewer
// opens a circuit, asks to be put in the region, and is told it is
// there -- without a single one of those messages reaching the grid.
func TestViewerJoinsAnExistingSession(t *testing.T) {
	sim, _, c, v, census := handedOver(t)

	// Whatever the session itself said on connecting is not what is
	// under test here.
	sim.waitSeen(t, "CompleteAgentMovement", 5*time.Second)
	before := len(sim.got())

	v.connect(testCircuitCode)
	v.waitSeen(t, "AgentMovementComplete", 5*time.Second)

	if !c.Joined() {
		t.Error("the circuit does not consider the viewer joined")
	}
	// Absorbed means absorbed: the grid heard nothing new.
	time.Sleep(100 * time.Millisecond)
	for _, n := range sim.got()[before:] {
		if n == "UseCircuitCode" || n == "CompleteAgentMovement" {
			t.Errorf("%s reached the simulator a second time", n)
		}
	}
	for _, name := range []string{"UseCircuitCode", "CompleteAgentMovement"} {
		if !census.Seen(name, FromViewer) {
			t.Errorf("%s was not recorded", name)
		}
	}
	if !census.Seen("AgentMovementComplete", ToViewer) {
		t.Errorf("the synthesised reply was not recorded:\n%s", census.Report())
	}
}

// TestMovementCompleteCarriesTheRealPosition: the answer is synthesised,
// so its contents come from the session rather than from the message
// that prompted it.  A viewer told the wrong place puts the avatar
// somewhere it is not and renders the wrong part of the region.
func TestMovementCompleteCarriesTheRealPosition(t *testing.T) {
	sim, _, _, v, _ := handedOver(t)
	sim.waitSeen(t, "CompleteAgentMovement", 5*time.Second)

	v.connect(testCircuitCode)
	v.waitSeen(t, "AgentMovementComplete", 5*time.Second)

	m := v.last(t, "AgentMovementComplete").(*msg.AgentMovementComplete)
	if m.Data.Position != (msg.Vector3{X: 128, Y: 129, Z: 2001}) {
		t.Errorf("position = %v, want where the simulator put the avatar", m.Data.Position)
	}
	if m.Data.RegionHandle != 0x0003_f000_0003_e800 {
		t.Errorf("region handle = %#x", m.Data.RegionHandle)
	}
	if m.AgentData.AgentID != msg.MustParseUUID("876e7e57-7e57-c0de-9eeb-1bd0e1ec6995") {
		t.Errorf("agent id = %v, want the session's own", m.AgentData.AgentID)
	}
}

// TestLogoutFromTheViewerLeavesTheSessionUp is the one that would hurt
// most to get wrong: closing a viewer must not log the avatar out from
// under automate and every attached client.
func TestLogoutFromTheViewerLeavesTheSessionUp(t *testing.T) {
	sim, _, _, v, _ := handedOver(t)
	sim.waitSeen(t, "CompleteAgentMovement", 5*time.Second)

	v.connect(testCircuitCode)
	v.waitSeen(t, "AgentMovementComplete", 5*time.Second)

	v.send(&msg.LogoutRequest{}, msg.FlagReliable)
	v.waitSeen(t, "LogoutReply", 5*time.Second)

	time.Sleep(150 * time.Millisecond)
	sim.never(t, "LogoutRequest")
}

// TestOrdinaryMessagesReachTheGrid: absorbing the handshake must not
// turn into absorbing everything.
func TestOrdinaryMessagesReachTheGrid(t *testing.T) {
	sim, _, _, v, census := handedOver(t)
	sim.waitSeen(t, "CompleteAgentMovement", 5*time.Second)

	v.connect(testCircuitCode)
	v.waitSeen(t, "AgentMovementComplete", 5*time.Second)

	chat := &msg.ChatFromViewer{}
	chat.ChatData.Message = []byte("hello from the viewer\x00")
	v.send(chat, msg.FlagReliable)

	sim.waitSeen(t, "ChatFromViewer", 5*time.Second)
	if !census.Seen("ChatFromViewer", FromViewer) {
		t.Error("the forwarded message was not recorded")
	}
}

// TestViewerTakesTheCamera: while a viewer is attached it is
// authoritative for the camera, and the session stops sending its own.
// Two things arguing over the camera is not a cosmetic problem -- the
// simulator decides what to stream from it.
func TestViewerTakesTheCamera(t *testing.T) {
	sim, a, _, v, _ := handedOver(t)
	sim.waitSeen(t, "CompleteAgentMovement", 5*time.Second)

	v.connect(testCircuitCode)
	v.waitSeen(t, "AgentMovementComplete", 5*time.Second)

	up := &msg.AgentUpdate{}
	up.AgentData.CameraCenter = msg.Vector3{X: 10, Y: 20, Z: 30}
	up.AgentData.CameraAtAxis = msg.Vector3{Y: 1}
	up.AgentData.Far = 96
	v.send(up, 0)

	deadline := time.Now().Add(5 * time.Second)
	for {
		if l := a.Look(); l.Center == (msg.Vector3{X: 10, Y: 20, Z: 30}) && l.Far == 96 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the session never took the viewer's camera: %+v", a.Look())
		}
		time.Sleep(5 * time.Millisecond)
	}
}
