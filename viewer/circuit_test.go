package viewer

import (
	"context"
	"encoding/binary"
	"math"
	"net"
	"strings"
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
			// A field regionFromHandshake throws away, so a test
			// can tell a replayed handshake from a rebuilt one.
			rh.RegionInfo.TerrainDetail0 = testTerrainTexture
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

var testTerrainTexture = msg.MustParseUUID("c4a67e57-7e57-c0de-f622-7fbb9d50e934")

// describeObjects sends full object updates, as a region does once when
// an avatar arrives and never again.
func (s *simStub) describeObjects(n int) {
	m := &msg.ObjectUpdate{}
	m.RegionData.RegionHandle = 0x0003_f000_0003_e800
	for i := 0; i < n; i++ {
		d := msg.ObjectUpdate_ObjectData{}
		d.ID = uint32(1000 + i)
		d.FullID = msg.MustParseUUID("00000000-0000-0000-0000-00000000000" + string(rune('1'+i)))
		d.PCode = 9
		// Placement lives at the front of ObjectData, and the store
		// drops anything outside the draw distance -- so these have
		// to be where the avatar is, not at the origin.
		d.ObjectData = make([]byte, 60)
		binary.LittleEndian.PutUint32(d.ObjectData[0:], math.Float32bits(128))
		binary.LittleEndian.PutUint32(d.ObjectData[4:], math.Float32bits(129))
		binary.LittleEndian.PutUint32(d.ObjectData[8:], math.Float32bits(2001))
		m.ObjectData = append(m.ObjectData, d)
	}
	s.send(m, msg.FlagReliable)
}

// sendLand puts one land patch on the wire, as a region does in the
// first seconds after an avatar arrives.
func (s *simStub) sendLand(body string) {
	m := &msg.LayerData{}
	m.LayerID.Type = 'L'
	m.LayerData.Data = []byte(body)
	s.send(m, msg.FlagReliable)
}

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
	c, err := Listen("127.0.0.1", func() *agent.Agent { return a }, census, nil, func(string, ...any) {})
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

// TestTheRegionIsDescribedToAJoiningViewer: a region introduces itself
// once, in the first seconds of a session that may have been up for
// hours.  A viewer that is not told sits on "Loading world" for ever
// with no indication of what it is waiting for.
func TestTheRegionIsDescribedToAJoiningViewer(t *testing.T) {
	sim, a, _, v, census := handedOver(t)
	sim.waitSeen(t, "CompleteAgentMovement", 5*time.Second)

	// The land arrives while the session is starting, long before any
	// viewer, which is why it is recorded rather than asked for.
	sim.sendLand("the ground")

	deadline := time.Now().Add(5 * time.Second)
	for {
		if n, _, _ := a.Terrain().Stats(); n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the session did not record the land")
		}
		time.Sleep(5 * time.Millisecond)
	}

	v.connect(testCircuitCode)
	v.waitSeen(t, "RegionHandshake", 5*time.Second)
	v.waitSeen(t, "LayerData", 5*time.Second)

	// The handshake is the simulator's own message, not one rebuilt
	// from the dozen fields this tree keeps: a reconstruction loses
	// every terrain texture id and renders ground with nothing on it.
	h := v.last(t, "RegionHandshake").(*msg.RegionHandshake)
	if got := trimNul(string(h.RegionInfo.SimName)); got != "Dovet" {
		t.Errorf("region name = %q", got)
	}
	if h.RegionInfo.TerrainDetail0 != testTerrainTexture {
		t.Errorf("terrain texture = %v, want the one the simulator sent", h.RegionInfo.TerrainDetail0)
	}

	land := v.last(t, "LayerData").(*msg.LayerData)
	if string(land.LayerData.Data) != "the ground" {
		t.Errorf("land = %q", land.LayerData.Data)
	}
	if !census.Seen("RegionHandshake", ToViewer) || !census.Seen("LayerData", ToViewer) {
		t.Errorf("the replay was not recorded:\n%s", census.Report())
	}
}

func trimNul(s string) string {
	if i := len(s) - 1; i >= 0 && s[i] == 0 {
		return s[:i]
	}
	return s
}

// TestWhatTheRegionSaysReachesTheViewer is the direction that was
// missing: without it a viewer is told only what slgod invents, so every
// answer to every question it asked stops at the daemon.
func TestWhatTheRegionSaysReachesTheViewer(t *testing.T) {
	sim, a, c, v, census := handedOver(t)
	sim.waitSeen(t, "CompleteAgentMovement", 5*time.Second)

	// The session's own relay hook, as slgod wires it.
	a.Disp.MustHandle("ChatFromSimulator", func(*msg.Packet) {}, msg.Inline())

	v.connect(testCircuitCode)
	v.waitSeen(t, "AgentMovementComplete", 5*time.Second)

	chat := &msg.ChatFromSimulator{}
	chat.ChatData.FromName = []byte("Somebody\x00")
	chat.ChatData.Message = []byte("hello from the region\x00")
	c.FromSim(&msg.Packet{
		Header:  msg.Header{Sequence: 4001, Flags: msg.FlagReliable},
		ID:      msg.IDOf(chat),
		Message: chat,
		At:      time.Now(),
	})

	v.waitSeen(t, "ChatFromSimulator", 5*time.Second)
	got := v.last(t, "ChatFromSimulator").(*msg.ChatFromSimulator)
	if string(got.ChatData.Message) != "hello from the region\x00" {
		t.Errorf("message = %q", got.ChatData.Message)
	}
	if !census.Seen("ChatFromSimulator", FromSim) {
		t.Errorf("not recorded:\n%s", census.Report())
	}
}

// TestPingsAreNotPassedOn: they are per circuit, and a viewer answering
// a ping meant for the session would be answering the wrong end.
func TestPingsAreNotPassedOn(t *testing.T) {
	sim, _, c, v, _ := handedOver(t)
	sim.waitSeen(t, "CompleteAgentMovement", 5*time.Second)
	v.connect(testCircuitCode)
	v.waitSeen(t, "AgentMovementComplete", 5*time.Second)

	ping := &msg.StartPingCheck{}
	ping.PingID.PingID = 9
	c.FromSim(&msg.Packet{ID: msg.IDOf(ping), Message: ping, At: time.Now()})

	time.Sleep(150 * time.Millisecond)
	for _, n := range v.got() {
		if n == "StartPingCheck" {
			t.Error("the simulator's ping was passed to the viewer")
		}
	}
}

// TestABehindViewerDropsRatherThanStallingTheSession: the hand-off runs
// on the grid session's dispatch goroutine, so a viewer that stops
// draining must cost updates rather than costing the avatar.
func TestABehindViewerDropsRatherThanStallingTheSession(t *testing.T) {
	sim, _, c, v, _ := handedOver(t)
	sim.waitSeen(t, "CompleteAgentMovement", 5*time.Second)
	v.connect(testCircuitCode)
	v.waitSeen(t, "AgentMovementComplete", 5*time.Second)

	// Far more than the backlog, offered as fast as the loop goes.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < Backlog*3; i++ {
			m := &msg.ChatFromSimulator{}
			m.ChatData.Message = []byte("flood\x00")
			c.FromSim(&msg.Packet{ID: msg.IDOf(m), Message: m, At: time.Now()})
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("offering messages to a viewer blocked the caller")
	}
	if c.Dropped() == 0 {
		t.Log("nothing was dropped; the viewer kept up, which is fine")
	}
}

// TestTheCircuitFollowsAReconnect: a grid session is replaced when it
// has to be re-established, and a viewer circuit outlives that.
//
// Holding the old pointer meant every forward failed with "sender is
// not running" and the region handshake was composed from a session
// that had ended -- which presents as a viewer stuck on "Waiting for
// region handshake", with nothing to say the session underneath had
// been swapped.
func TestTheCircuitFollowsAReconnect(t *testing.T) {
	sim := newSimStub(t)

	acct := &agent.Account{
		AgentID:     msg.MustParseUUID("876e7e57-7e57-c0de-9eeb-1bd0e1ec6995"),
		SessionID:   msg.MustParseUUID("8d1b7e57-7e57-c0de-f4f4-19d29d124acf"),
		CircuitCode: testCircuitCode,
		SimIP:       sim.addr().IP,
		SimPort:     sim.addr().Port,
		FirstName:   "Quark", LastName: "Idlemind",
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	current, err := agent.Connect(ctx, acct, agent.Options{
		Timeout: 10 * time.Second, SkipCaps: true, Idle: -1})
	if err != nil {
		t.Fatal(err)
	}
	get := func() *agent.Agent {
		mu.Lock()
		defer mu.Unlock()
		return current
	}

	census := NewCensus()
	c, err := Listen("127.0.0.1", get, census, nil, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	c.Run(ctx)
	defer c.Close()

	sim.waitSeen(t, "CompleteAgentMovement", 5*time.Second)

	// The session ends and is re-established, as the supervisor does.
	current.Close()
	replacement, err := agent.Connect(ctx, acct, agent.Options{
		Timeout: 10 * time.Second, SkipCaps: true, Idle: -1})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	current = replacement
	mu.Unlock()
	defer replacement.Close()

	v := newFakeViewer(t, c.Addr())
	defer v.close()
	go v.run()

	v.connect(testCircuitCode)
	// Answered at all means the circuit found the live session: the
	// dead one's sender would have refused.
	v.waitSeen(t, "AgentMovementComplete", 5*time.Second)

	chat := &msg.ChatFromViewer{}
	chat.ChatData.Message = []byte("through the new session\x00")
	v.send(chat, msg.FlagReliable)
	sim.waitSeen(t, "ChatFromViewer", 5*time.Second)
}

// TestAViewerThatComesBackOnANewPortIsAnswered: a restarted viewer has
// a new socket, and a circuit that pinned the first address would go on
// talking to one that had quit.
func TestAViewerThatComesBackOnANewPortIsAnswered(t *testing.T) {
	sim, _, c, first, _ := handedOver(t)
	sim.waitSeen(t, "CompleteAgentMovement", 5*time.Second)

	first.connect(testCircuitCode)
	first.waitSeen(t, "AgentMovementComplete", 5*time.Second)
	first.close()

	// A second viewer, on a socket of its own.
	second := newFakeViewer(t, c.Addr())
	defer second.close()
	go second.run()

	second.connect(testCircuitCode)
	second.waitSeen(t, "AgentMovementComplete", 5*time.Second)
}

// sentToViewer is how many times the circuit composed and sent a
// message of its own.
//
// It counts answers rather than arrivals, which is what these tests
// need: the send tap the census is wired to is not called for a
// retransmission (see WithSendTap), so a message that went out once and
// was resent to whoever happens to be listening now counts once.
func sentToViewer(c *Census, name string) uint64 {
	for _, row := range c.Counts() {
		if row.Name == name && row.Dir == ToViewer {
			return row.Packets
		}
	}
	return 0
}

// heardFromViewer is how many times the circuit acted on a message the
// viewer sent.
func heardFromViewer(c *Census, name string) uint64 {
	for _, row := range c.Counts() {
		if row.Name == name && row.Dir == FromViewer {
			return row.Packets
		}
	}
	return 0
}

// TestASecondViewerIsAnsweredRatherThanTakenForARetransmission is the
// bug that made a handover work exactly once per daemon.
//
// A viewer numbers its own packets from 1, so the second one to attach
// opens with UseCircuitCode at sequence 1 and CompleteAgentMovement at
// sequence 2 -- the numbers the first one used hours earlier, and still
// in the dispatcher's ring of recent sequence numbers.  Both were
// therefore thrown away as retransmissions before reaching the relay,
// which is the only thing that replays the region and answers the
// movement request.  The peer was still noticed, because the tap that
// notices it runs ahead of the duplicate check, so the daemon logged a
// viewer appearing and then said nothing more while the viewer sat at
// STATE_AGENT_WAIT looking at a grey world.
//
// TestAViewerThatComesBackOnANewPortIsAnswered was supposed to cover
// this and did not, for a reason worth writing down: it passed in 4.50
// seconds on every run of three, which is the retransmission timer and
// not an answer.  The first viewer's unacknowledged AgentMovementComplete
// was being resent to whoever the peer now was, and the second viewer
// heard it and the test was satisfied.  With this fix it passes in 0.01
// seconds, having actually been answered.  So the assertions here count
// what the circuit composed rather than what the socket heard: the
// census is fed by the send tap, which a retransmission never reaches.
func TestASecondViewerIsAnsweredRatherThanTakenForARetransmission(t *testing.T) {
	sim, _, c, first, census := handedOver(t)
	sim.waitSeen(t, "CompleteAgentMovement", 5*time.Second)

	first.connect(testCircuitCode)
	first.waitSeen(t, "AgentMovementComplete", 5*time.Second)
	first.close()

	// A viewer quit and launched again: a socket of its own, and a
	// conversation that starts over at sequence 1.
	second := newFakeViewer(t, c.Addr())
	defer second.close()
	go second.run()
	second.connect(testCircuitCode)

	deadline := time.Now().Add(5 * time.Second)
	for {
		if sentToViewer(census, "RegionHandshake") >= 2 && sentToViewer(census, "AgentMovementComplete") >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the second viewer got %d region handshakes and %d movement completes, want 2 of each; "+
				"the dispatcher suppressed %d duplicates\n%s",
				sentToViewer(census, "RegionHandshake"), sentToViewer(census, "AgentMovementComplete"),
				c.disp.Stats().Duplicates, census.Report())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !c.Joined() {
		t.Error("the circuit does not consider the second viewer joined")
	}
}

// TestTheLastViewersLogoutReplyIsNotDeliveredToTheNextOne is the other
// half of forgetting a viewer that has gone.
//
// A viewer quits by sending LogoutRequest and closing, so the reply it
// is owed is often never acknowledged and sits in the circuit's
// retransmission list.  Left there, it goes out again once the circuit
// has a new peer -- and a LogoutReply is not a stale nicety, it is the
// message that tells a viewer the session is over, arriving seconds
// after the new one finished loading.
func TestTheLastViewersLogoutReplyIsNotDeliveredToTheNextOne(t *testing.T) {
	sim, _, c, first, _ := handedOver(t)
	sim.waitSeen(t, "CompleteAgentMovement", 5*time.Second)

	first.connect(testCircuitCode)
	first.waitSeen(t, "AgentMovementComplete", 5*time.Second)

	first.send(&msg.LogoutRequest{}, msg.FlagReliable)
	first.waitSeen(t, "LogoutReply", 5*time.Second)
	// Closing without acknowledging it, which is what quitting is.
	first.close()

	second := newFakeViewer(t, c.Addr())
	defer second.close()
	go second.run()
	second.connect(testCircuitCode)
	second.waitSeen(t, "AgentMovementComplete", 5*time.Second)

	// Past the moment the retransmission would have carried it: the
	// sender waits one timeout of three seconds and then tries again
	// every second and a half.
	time.Sleep(5 * time.Second)
	for _, n := range second.got() {
		if n == "LogoutReply" {
			t.Fatalf("the new viewer was sent the last one's LogoutReply, which closes it; it heard %v", second.got())
		}
	}
}

// TestARetransmissionFromTheSameViewerIsStillSuppressed guards the other
// side of the same fix: forgetting a departed viewer's sequence numbers
// must not amount to turning duplicate suppression off.
//
// A viewer resends anything reliable whose acknowledgement went missing,
// which is ordinary and frequent.  Acted on twice, one
// CompleteAgentMovement becomes two region replays and two
// AgentMovementCompletes -- and, for everything that is forwarded rather
// than absorbed, one chat line said once and heard twice at the far end.
func TestARetransmissionFromTheSameViewerIsStillSuppressed(t *testing.T) {
	sim, _, _, v, census := handedOver(t)
	sim.waitSeen(t, "CompleteAgentMovement", 5*time.Second)

	v.connect(testCircuitCode)
	v.waitSeen(t, "AgentMovementComplete", 5*time.Second)

	// The same packet, from the same socket, under the number it went
	// out with.
	cam := &msg.CompleteAgentMovement{}
	cam.AgentData.CircuitCode = testCircuitCode
	v.sendSeq(cam, msg.FlagReliable, 2)

	// Long enough for a second answer to have been composed had the
	// duplicate been acted on.
	time.Sleep(250 * time.Millisecond)

	if n := heardFromViewer(census, "CompleteAgentMovement"); n != 1 {
		t.Errorf("CompleteAgentMovement was acted on %d times, want 1:\n%s", n, census.Report())
	}
	if n := sentToViewer(census, "AgentMovementComplete"); n != 1 {
		t.Errorf("the viewer was answered %d times, want 1:\n%s", n, census.Report())
	}
}

// TestAJoiningViewerIsSentTheObjects is the other half of describing a
// region: the handshake says where you are, and this says what is
// there.
//
// A region describes each object once, on arrival, and the session
// consumed those descriptions before the viewer existed.  Without
// asking again the viewer sees only what changes while it watches --
// almost nothing on a quiet parcel, and indistinguishable from a relay
// that has stopped working.
func TestAJoiningViewerIsSentTheObjects(t *testing.T) {
	sim, a, _, v, _ := handedOver(t)
	sim.waitSeen(t, "CompleteAgentMovement", 5*time.Second)

	// Objects the region described before any viewer turned up.
	sim.describeObjects(3)
	deadline := time.Now().Add(5 * time.Second)
	for a.Objects().Count() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("the session recorded %d objects, want 3", a.Objects().Count())
		}
		time.Sleep(5 * time.Millisecond)
	}

	v.connect(testCircuitCode)
	v.waitSeen(t, "AgentMovementComplete", 5*time.Second)

	// Asking is what the viewer's arrival has to produce.
	sim.waitSeen(t, "RequestMultipleObjects", 5*time.Second)
}

// TestWhatWasSaidBeforeTheViewerArrivedIsShown: an instant message --
// and with it every teleport offer, inventory offer and group
// invitation -- is said once and addressed to the person.  Arriving
// while no viewer was attached it went into the daemon and stopped
// there, which is how a teleport offer was lost.
func TestWhatWasSaidBeforeTheViewerArrivedIsShown(t *testing.T) {
	sim, a, _, v, _ := handedOver(t)
	sim.waitSeen(t, "CompleteAgentMovement", 5*time.Second)

	offer := &msg.ImprovedInstantMessage{}
	offer.MessageBlock.FromAgentName = []byte("Kerra Yule\x00")
	offer.MessageBlock.Message = []byte("come and see the house\x00")
	sim.send(offer, msg.FlagReliable)

	deadline := time.Now().Add(5 * time.Second)
	for {
		if held, _ := a.Offers().Stats(); held > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the session never kept the instant message")
		}
		time.Sleep(5 * time.Millisecond)
	}

	v.connect(testCircuitCode)
	v.waitSeen(t, "ImprovedInstantMessage", 5*time.Second)
	got := v.last(t, "ImprovedInstantMessage").(*msg.ImprovedInstantMessage)
	if s := string(got.MessageBlock.Message); s != "come and see the house\x00" {
		t.Errorf("message = %q", s)
	}

	// And taken, so a viewer attaching after this one is not handed an
	// invitation that has already been answered.
	if held, _ := a.Offers().Stats(); held != 0 {
		t.Errorf("%d offers were still held after the viewer was shown them", held)
	}
}

// describeAppearance says how one avatar looks, as a region does when it
// comes into view and never again.
func (s *simStub) describeAppearance(who msg.UUID, texture string) {
	m := &msg.AvatarAppearance{}
	m.Sender.ID = who
	m.ObjectData.TextureEntry = []byte(texture)
	s.send(m, msg.FlagReliable)
}

// TestAJoiningViewerIsToldHowAvatarsLook: an avatar's appearance is sent
// once, when it comes into view, and cannot be asked for again -- so a
// viewer attaching to a session that has been up for hours draws
// everybody as the default body, untextured and wearing nothing.
//
// It also has to arrive in the right order.  An appearance for an avatar
// the viewer has not heard of is dropped on the floor, so this checks
// that it is held back until the avatar itself has gone across.
func TestAJoiningViewerIsToldHowAvatarsLook(t *testing.T) {
	sim, a, c, v, _ := handedOver(t)
	sim.waitSeen(t, "CompleteAgentMovement", 5*time.Second)

	who := msg.MustParseUUID("6c457e57-7e57-c0de-8676-7ee670495387")
	sim.describeAppearance(who, "how she looks")
	deadline := time.Now().Add(5 * time.Second)
	for a.Appearances().Get(who) == nil {
		if time.Now().After(deadline) {
			t.Fatal("the session never recorded the avatar's appearance")
		}
		time.Sleep(5 * time.Millisecond)
	}

	v.connect(testCircuitCode)
	v.waitSeen(t, "AgentMovementComplete", 5*time.Second)

	// describeRegion has run by now, and must not have sent this yet:
	// the viewer does not know the avatar exists.
	for _, name := range v.got() {
		if name == "AvatarAppearance" {
			t.Fatal("the appearance went out before the avatar the viewer could attach it to")
		}
	}

	// The avatar comes back, as it does when the simulator answers
	// Redescribe.
	upd := &msg.ObjectUpdate{}
	upd.RegionData.RegionHandle = 0x0003_f000_0003_e800
	upd.ObjectData = []msg.ObjectUpdate_ObjectData{{
		ID:         2001,
		FullID:     who,
		PCode:      47,
		ObjectData: make([]byte, 60),
	}}
	c.FromSim(&msg.Packet{
		Header:  msg.Header{Sequence: 4101, Flags: msg.FlagReliable},
		ID:      msg.IDOf(upd),
		Message: upd,
		At:      time.Now(),
	})

	v.waitSeen(t, "AvatarAppearance", 5*time.Second)
	got := v.last(t, "AvatarAppearance").(*msg.AvatarAppearance)
	if got.Sender.ID != who {
		t.Errorf("appearance was for %s, want %s", got.Sender.ID, who)
	}
	if string(got.ObjectData.TextureEntry) != "how she looks" {
		t.Errorf("texture entry = %q", got.ObjectData.TextureEntry)
	}
}

// absorbedFromViewer is how many of these the circuit took and did not
// pass on.  It is the assertion an absorbing arm of fromViewer exists to
// make: a message that never reached the grid but was recorded as
// forwarded is a bug the census is the only witness to.
func absorbedFromViewer(c *Census, name string) uint64 {
	for _, row := range c.Counts() {
		if row.Name == name && row.Dir == FromViewer {
			return row.By[Absorbed]
		}
	}
	return 0
}

// thisRegion is the handle simStub puts in its AgentMovementComplete, so
// a test can ask for a teleport that stays here.  elsewhere is Sandbox
// Goguen's real handle, which is not it.
const (
	thisRegion = uint64(0x0003_f000_0003_e800)
	elsewhere  = uint64(1094014069892352)
)

// TestAViewerTeleportToAnotherRegionIsRefusedOutLoud: the daemon follows
// a teleport now, so a forwarded one does not end the session -- it
// moves the circuit and leaves the viewer drawing a region the avatar
// has left, with nothing anywhere reporting an error.  The three
// messages here are three different controls (a map click, a landmark,
// and accepting somebody's offer) and none of them may reach the grid.
func TestAViewerTeleportToAnotherRegionIsRefusedOutLoud(t *testing.T) {
	sim, _, _, v, census := handedOver(t)
	sim.waitSeen(t, "CompleteAgentMovement", 5*time.Second)

	v.connect(testCircuitCode)
	v.waitSeen(t, "AgentMovementComplete", 5*time.Second)

	tp := &msg.TeleportLocationRequest{}
	tp.Info.RegionHandle = elsewhere
	tp.Info.Position = msg.Vector3{X: 128, Y: 128, Z: 26}
	v.send(tp, msg.FlagReliable)

	lm := &msg.TeleportLandmarkRequest{}
	lm.Info.LandmarkID = msg.MustParseUUID("3b277e57-7e57-c0de-6c1a-995a5d4a9b60")
	v.send(lm, msg.FlagReliable)

	lure := &msg.TeleportLureRequest{}
	lure.Info.LureID = msg.MustParseUUID("96d97e57-7e57-c0de-6273-3461e4f8d11b")
	v.send(lure, msg.FlagReliable)

	// The person is told, because a control that does nothing and says
	// nothing looks exactly like a viewer that has frozen.
	v.waitSeen(t, "AgentAlertMessage", 5*time.Second)
	told := v.last(t, "AgentAlertMessage").(*msg.AgentAlertMessage)
	if !told.AlertData.Modal {
		t.Error("the refusal was not modal, so a viewer may show it as a tip that fades")
	}
	said := string(told.AlertData.Message)
	if !strings.Contains(said, "slsh tp") {
		t.Errorf("the refusal does not say what does work: %q", said)
	}

	// Absorbed means absorbed.
	time.Sleep(200 * time.Millisecond)
	for _, name := range []string{"TeleportLocationRequest", "TeleportLandmarkRequest", "TeleportLureRequest"} {
		sim.never(t, name)
		if n := absorbedFromViewer(census, name); n != 1 {
			t.Errorf("%s was absorbed %d times, want 1:\n%s", name, n, census.Report())
		}
	}
}

// TestATeleportInsideThisRegionIsStillForwarded: double-click to move
// and "teleport here" are ordinary things to do, they change nothing
// about the circuit, and the same simulator answers them.  Refusing
// them would break something that is not broken, so the handle in the
// request is compared with the session's own rather than assumed.
func TestATeleportInsideThisRegionIsStillForwarded(t *testing.T) {
	sim, a, _, v, census := handedOver(t)
	sim.waitSeen(t, "CompleteAgentMovement", 5*time.Second)

	v.connect(testCircuitCode)
	v.waitSeen(t, "AgentMovementComplete", 5*time.Second)

	// The session has to know where it is, or every teleport is
	// somewhere else by the safe-side rule.
	deadline := time.Now().Add(5 * time.Second)
	for a.RegionHandle() != thisRegion {
		if time.Now().After(deadline) {
			t.Fatalf("the session's handle is %#x, want the one the simulator gave", a.RegionHandle())
		}
		time.Sleep(5 * time.Millisecond)
	}

	tp := &msg.TeleportLocationRequest{}
	tp.Info.RegionHandle = thisRegion
	tp.Info.Position = msg.Vector3{X: 12, Y: 240, Z: 27}
	v.send(tp, msg.FlagReliable)

	sim.waitSeen(t, "TeleportLocationRequest", 5*time.Second)
	if n := absorbedFromViewer(census, "TeleportLocationRequest"); n != 0 {
		t.Errorf("a teleport within the region was absorbed %d times:\n%s", n, census.Report())
	}
	if n := sentToViewer(census, "AgentAlertMessage"); n != 0 {
		t.Error("the person was told a teleport was refused when it was not")
	}
}

// TestATeleportNobodyInTheViewerAskedForDoesNotTearDownItsWorld:
// TeleportStart is the simulator announcing a move another client asked
// for.  Handed over it puts the viewer in its teleport tunnel, and the
// message that would take it out again -- TeleportFinish -- is withheld
// on purpose, so it would sit there over something it neither asked for
// nor could stop.
func TestATeleportNobodyInTheViewerAskedForDoesNotTearDownItsWorld(t *testing.T) {
	sim, _, c, v, census := handedOver(t)
	sim.waitSeen(t, "CompleteAgentMovement", 5*time.Second)
	v.connect(testCircuitCode)
	v.waitSeen(t, "AgentMovementComplete", 5*time.Second)

	for _, m := range []msg.Message{
		&msg.TeleportStart{},
		&msg.TeleportProgress{},
		&msg.TeleportFinish{},
	} {
		c.FromSim(&msg.Packet{ID: msg.IDOf(m), Message: m, At: time.Now()})
	}

	// A chat line behind them, so that waiting for it proves the three
	// were considered and dropped rather than merely still in flight.
	chat := &msg.ChatFromSimulator{}
	chat.ChatData.Message = []byte("after the teleport\x00")
	c.FromSim(&msg.Packet{ID: msg.IDOf(chat), Message: chat, At: time.Now()})
	v.waitSeen(t, "ChatFromSimulator", 5*time.Second)

	for _, name := range []string{"TeleportStart", "TeleportProgress", "TeleportFinish"} {
		for _, seen := range v.got() {
			if seen == name {
				t.Errorf("%s reached the viewer; it heard %v", name, v.got())
			}
		}
	}
	if census.Total() == 0 {
		t.Fatal("nothing was recorded at all")
	}
}

// TestAViewerIsToldWhenTheAvatarIsTeleportedFromSomewhereElse: another
// client can move this session, and the viewer is no part of that
// conversation -- it goes on drawing a region the avatar has left while
// the new region's objects land on top under local ids that now mean
// something different.  Replaying the new region to it is "follow",
// which is not built, so what is owed is a plain sentence.
func TestAViewerIsToldWhenTheAvatarIsTeleportedFromSomewhereElse(t *testing.T) {
	sim, _, c, v, census := handedOver(t)
	sim.waitSeen(t, "CompleteAgentMovement", 5*time.Second)

	// Before anything attached there is nobody to tell, and a circuit
	// exists from the first login whether a viewer ever arrived or not.
	c.RegionChanged("Sandbox Goguen")
	if n := sentToViewer(census, "AgentAlertMessage"); n != 0 {
		t.Errorf("a viewer that never joined was told %d times", n)
	}

	v.connect(testCircuitCode)
	v.waitSeen(t, "AgentMovementComplete", 5*time.Second)

	c.RegionChanged("Sandbox Goguen")
	v.waitSeen(t, "AgentAlertMessage", 5*time.Second)
	told := v.last(t, "AgentAlertMessage").(*msg.AgentAlertMessage)
	said := string(told.AlertData.Message)
	if !strings.Contains(said, "Sandbox Goguen") {
		t.Errorf("the notice does not say where the avatar went: %q", said)
	}
	if !strings.Contains(said, "log out") {
		t.Errorf("the notice does not say what to do about it: %q", said)
	}
}

// TestANeighboursAddressOnTheCircuitDoesNotReachTheViewer: both of
// these are withheld from the event queue, which is where the template
// says they go, and both would hand a viewer a simulator to open its
// own circuit to if a grid ever sent them here instead.  Absorbing them
// costs nothing: a viewer that is not offered neighbours draws this
// region and no other either way.
func TestANeighboursAddressOnTheCircuitDoesNotReachTheViewer(t *testing.T) {
	sim, _, c, v, census := handedOver(t)
	sim.waitSeen(t, "CompleteAgentMovement", 5*time.Second)
	v.connect(testCircuitCode)
	v.waitSeen(t, "AgentMovementComplete", 5*time.Second)

	for _, m := range []msg.Message{
		&msg.EnableSimulator{},
		&msg.CrossedRegion{},
	} {
		c.FromSim(&msg.Packet{ID: msg.IDOf(m), Message: m, At: time.Now()})
	}

	// A chat line behind them, so that waiting for it proves the two
	// were considered and dropped rather than merely still in flight.
	chat := &msg.ChatFromSimulator{}
	chat.ChatData.Message = []byte("after the border\x00")
	c.FromSim(&msg.Packet{ID: msg.IDOf(chat), Message: chat, At: time.Now()})
	v.waitSeen(t, "ChatFromSimulator", 5*time.Second)

	for _, name := range []string{"EnableSimulator", "CrossedRegion"} {
		for _, seen := range v.got() {
			if seen == name {
				t.Errorf("%s reached the viewer; it heard %v", name, v.got())
			}
		}
	}
	if census.Total() == 0 {
		t.Fatal("nothing was recorded at all")
	}
}
