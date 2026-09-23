package agent

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// fakeSim answers the handshake the way a simulator does, so the
// session can be exercised without the grid.
type fakeSim struct {
	conn *net.UDPConn

	mu       sync.Mutex
	seen     []string
	peer     *net.UDPAddr
	seq      uint32
	regionNm string
	regionID msg.UUID

	// What AgentMovementComplete says on arrival.  Two regions in one
	// test have to disagree about both, or nothing that reads them can
	// tell which region answered.
	position msg.Vector3
	handle   uint64

	// Set to skip a step, to test the timeouts.
	silent      bool
	noMovement  bool
	noHandshake bool
	kickInstead bool

	// lateHandshake delivers the region handshake BEHIND the arrival,
	// under the lower sequence number it was sent with, which is the
	// order a trace on Agni once caught them in: AgentMovementComplete
	// seq=2, and RegionHandshake seq=1 nearly a second after it.
	// heldHandshake is the packet waiting, already numbered.
	lateHandshake bool
	heldHandshake []byte

	// lateMovement delivers the arrival a while after it is asked for
	// rather than at once, which holds open the moment between the
	// handshake and the arrival that a real simulator leaves too short
	// to look into.
	lateMovement bool
}

// waitSeen blocks until the simulator has received a message, or the
// test gives up.  Nothing here is synchronous: Connect returns when
// AgentMovementComplete arrives, at which point RegionHandshakeReply
// has been queued but not necessarily put on the wire.
func (f *fakeSim) waitSeen(t *testing.T, name string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		for _, n := range f.got() {
			if n == name {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("simulator never saw %s; it saw %v", name, f.got())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func newFakeSim(t *testing.T) *fakeSim {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Skipf("no loopback UDP: %v", err)
	}
	return &fakeSim{
		conn:     conn,
		regionNm: "the test region",
		position: msg.Vector3{X: 188.4, Y: 202.8, Z: 26.3},
		handle:   0x0003_f000_0003_e800,
	}
}

func (f *fakeSim) addr() *net.UDPAddr { return f.conn.LocalAddr().(*net.UDPAddr) }

// arrivalAt sets what this simulator says when the avatar gets here, and
// arrival reads it back.  Both take the lock: the goroutine answering
// for this simulator is running from the moment it is built, so a test
// that assigned the fields would be writing them under a reader.
func (f *fakeSim) arrivalAt(at msg.Vector3, handle uint64) {
	f.mu.Lock()
	f.position, f.handle = at, handle
	f.mu.Unlock()
}

func (f *fakeSim) arrival() (msg.Vector3, uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.position, f.handle
}

func (f *fakeSim) close() { f.conn.Close() }

func (f *fakeSim) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

func (f *fakeSim) run() {
	buf := make([]byte, 8192)
	for {
		n, peer, err := f.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.peer = peer
		f.mu.Unlock()

		h, off, err := msg.DecodeHeader(buf[:n])
		if err != nil {
			continue
		}
		body := buf[off:n]
		if h.HasAcks() {
			body, _, err = msg.SplitAcks(body)
			if err != nil {
				continue
			}
		}
		if h.Zerocoded() {
			body, err = msg.ZeroExpand(nil, body)
			if err != nil {
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
		f.mu.Lock()
		f.seen = append(f.seen, name)
		f.mu.Unlock()

		f.mu.Lock()
		silent := f.silent
		f.mu.Unlock()
		if silent {
			// Not even an acknowledgement: an ack is an
			// inbound packet, and an inbound packet is what
			// tells the session its circuit is up.
			continue
		}
		if h.Reliable() {
			f.ack(h.Sequence)
		}
		f.react(name)
	}
}

func (f *fakeSim) ack(seq uint32) {
	f.send(&msg.PacketAck{Packets: []msg.PacketAck_Packets{{ID: seq}}}, 0)
}

func (f *fakeSim) react(name string) {
	switch name {
	case "UseCircuitCode":
		// The real simulator opens with the region handshake.
		f.mu.Lock()
		none, late := f.noHandshake, f.lateHandshake
		f.mu.Unlock()
		if none {
			return
		}
		rh := &msg.RegionHandshake{}
		rh.RegionInfo.SimName = []byte(f.regionNm + "\x00")
		rh.RegionInfo.RegionFlags = 0x1234
		rh.RegionInfo2.RegionID = f.regionID
		if late {
			held := f.packet(rh, msg.FlagReliable)
			f.mu.Lock()
			f.heldHandshake = held
			f.mu.Unlock()
			return
		}
		f.send(rh, msg.FlagReliable)

	case "CompleteAgentMovement":
		if f.noMovement {
			return
		}
		if f.kickInstead {
			k := &msg.KickUser{}
			k.UserInfo.Reason = []byte("go away\x00")
			f.send(k, msg.FlagReliable)
			return
		}
		f.mu.Lock()
		at, handle, slow := f.position, f.handle, f.lateMovement
		f.mu.Unlock()

		amc := &msg.AgentMovementComplete{}
		amc.Data.Position = at
		amc.Data.LookAt = msg.Vector3{X: 1}
		amc.Data.RegionHandle = handle
		amc.SimData.ChannelVersion = []byte("Second Life Server 2026-07-10\x00")
		if slow {
			time.AfterFunc(lateBy, func() { f.send(amc, msg.FlagReliable) })
			return
		}
		f.send(amc, msg.FlagReliable)

		f.mu.Lock()
		held := f.heldHandshake
		f.heldHandshake = nil
		f.mu.Unlock()
		if held != nil {
			// Long enough behind that anything reading the session
			// between the two is certain to be reading it then, and
			// on a timer so that the simulator goes on answering
			// meanwhile, as the real one did.
			time.AfterFunc(lateBy, func() { f.write(held) })
		}

	case "LogoutRequest":
		f.send(&msg.LogoutReply{}, msg.FlagReliable)
	}
}

// lateBy is how far behind a late message is delivered.
const lateBy = 300 * time.Millisecond

func (f *fakeSim) send(m msg.Message, flags uint8) {
	if out := f.packet(m, flags); out != nil {
		f.write(out)
	}
}

// packet numbers and encodes a message without sending it, so that one
// can be held back and delivered behind a later one.
func (f *fakeSim) packet(m msg.Message, flags uint8) []byte {
	f.mu.Lock()
	f.seq++
	seq := f.seq
	f.mu.Unlock()
	body, err := m.Encode()
	if err != nil {
		return nil
	}
	out := msg.AppendHeader(nil, &msg.Header{Flags: flags, Sequence: seq})
	out = msg.AppendID(out, msg.IDOf(m))
	return append(out, body...)
}

func (f *fakeSim) write(out []byte) {
	f.mu.Lock()
	peer := f.peer
	f.mu.Unlock()
	if peer == nil {
		return
	}
	f.conn.WriteToUDP(out, peer)
}

func testAccount(f *fakeSim) *Account {
	return &Account{
		AgentID:     msg.MustParseUUID("876e7e57-7e57-c0de-9eeb-1bd0e1ec6995"),
		SessionID:   msg.MustParseUUID("8d1b7e57-7e57-c0de-f4f4-19d29d124acf"),
		CircuitCode: 690139535,
		SimIP:       f.addr().IP,
		SimPort:     f.addr().Port,
		FirstName:   "Example",
		LastName:    "Resident",
	}
}

func TestSessionHandshake(t *testing.T) {
	sim := newFakeSim(t)
	defer sim.close()
	go sim.run()

	ctx := context.Background()
	s, err := Connect(ctx, testAccount(sim), Options{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.WaitForRegionHandshake(ctx, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if got := s.RegionName(); got != "the test region" {
		t.Errorf("region = %q, want the test region", got)
	}
	if p := s.Position(); p.X < 188 || p.X > 189 {
		t.Errorf("position = %+v", p)
	}
	if v := s.ChannelVersion(); !strings.HasPrefix(v, "Second Life Server") {
		t.Errorf("channel version = %q", v)
	}
	if s.RegionHandle() == 0 {
		t.Error("no region handle")
	}

	// The simulator should have seen the whole handshake, opening
	// with UseCircuitCode.
	sim.waitSeen(t, "CompleteAgentMovement", 5*time.Second)
	sim.waitSeen(t, "RegionHandshakeReply", 5*time.Second)
	if seen := sim.got(); seen[0] != "UseCircuitCode" {
		t.Errorf("handshake opened with %q, want UseCircuitCode: %v", seen[0], seen)
	}
}

func TestSessionAnswersPings(t *testing.T) {
	sim := newFakeSim(t)
	defer sim.close()
	go sim.run()

	s, err := Connect(context.Background(), testAccount(sim), Options{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ping := &msg.StartPingCheck{}
	ping.PingID.PingID = 42
	sim.send(ping, 0)

	sim.waitSeen(t, "CompletePingCheck", 5*time.Second)
}

func TestSessionLogout(t *testing.T) {
	sim := newFakeSim(t)
	defer sim.close()
	go sim.run()

	s, err := Connect(context.Background(), testAccount(sim), Options{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Logout(context.Background(), 5*time.Second); err != nil {
		t.Fatalf("logout: %v", err)
	}
	select {
	case <-s.Done():
	default:
		t.Error("session not marked done after logout")
	}
	var sawLogout bool
	for _, n := range sim.got() {
		if n == "LogoutRequest" {
			sawLogout = true
		}
	}
	if !sawLogout {
		t.Errorf("simulator never saw LogoutRequest: %v", sim.got())
	}
}

// TestSessionLogoutWithoutReply: a simulator that never answers must
// not hang the caller.
func TestSessionLogoutWithoutReply(t *testing.T) {
	sim := newFakeSim(t)
	defer sim.close()
	go sim.run()

	s, err := Connect(context.Background(), testAccount(sim), Options{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	// Stop the simulator answering anything further.
	sim.close()

	start := time.Now()
	err = s.Logout(context.Background(), 200*time.Millisecond)
	if err == nil {
		t.Error("expected a timeout")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("logout took %s, should have given up after the timeout", d)
	}
	select {
	case <-s.Done():
	default:
		t.Error("session should be shut down even when logout times out")
	}
}

func TestSessionCircuitTimeout(t *testing.T) {
	sim := newFakeSim(t)
	defer sim.close()
	sim.silent = true
	go sim.run()

	_, err := Connect(context.Background(), testAccount(sim), Options{Timeout: 200 * time.Millisecond})
	if err == nil {
		t.Fatal("expected a timeout waiting for the circuit")
	}
	if !strings.Contains(err.Error(), "circuit") {
		t.Errorf("err = %v, should say what it was waiting for", err)
	}
}

func TestSessionMovementTimeout(t *testing.T) {
	sim := newFakeSim(t)
	defer sim.close()
	sim.noMovement = true
	go sim.run()

	_, err := Connect(context.Background(), testAccount(sim), Options{Timeout: 300 * time.Millisecond})
	if err == nil {
		t.Fatal("expected a timeout waiting for AgentMovementComplete")
	}
	if !strings.Contains(err.Error(), "AgentMovementComplete") {
		t.Errorf("err = %v", err)
	}
}

func TestSessionKicked(t *testing.T) {
	sim := newFakeSim(t)
	defer sim.close()
	sim.kickInstead = true
	go sim.run()

	_, err := Connect(context.Background(), testAccount(sim), Options{Timeout: 3 * time.Second})
	if err == nil {
		t.Fatal("expected the kick to fail the connect")
	}
	if !strings.Contains(err.Error(), "kicked") || !strings.Contains(err.Error(), "go away") {
		t.Errorf("err = %v, should carry the reason", err)
	}
}

func TestSessionHandlerRegistration(t *testing.T) {
	sim := newFakeSim(t)
	defer sim.close()
	go sim.run()

	s, err := Connect(context.Background(), testAccount(sim), Options{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	got := make(chan string, 1)
	if err := s.Handle("ChatFromSimulator", func(p *msg.Packet) {
		m := p.Message.(*msg.ChatFromSimulator)
		got <- trimNul(m.ChatData.Message)
	}); err != nil {
		t.Fatal(err)
	}

	chat := &msg.ChatFromSimulator{}
	chat.ChatData.FromName = []byte("Someone\x00")
	chat.ChatData.Message = []byte("hello there\x00")
	sim.send(chat, 0)

	select {
	case s := <-got:
		if s != "hello there" {
			t.Errorf("chat = %q", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler never fired")
	}

	if err := s.Handle("NotAMessage", func(*msg.Packet) {}); err == nil {
		t.Error("expected an error for an unknown message name")
	}
}

// TestWatchdogEndsSilentSession: a circuit that has quietly died must
// say so.  Without this it looks exactly like an idle one and reports
// itself healthy forever, which is the worst failure mode for a
// connection meant to be left running.
func TestWatchdogEndsSilentSession(t *testing.T) {
	sim := newFakeSim(t)
	defer sim.close()
	go sim.run()

	a, err := Connect(context.Background(), testAccount(sim), Options{
		Timeout: 5 * time.Second,
		Idle:    300 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	// The simulator stops answering but keeps its socket, which is
	// what a lost circuit actually looks like.  Closing it instead
	// would draw an ICMP port unreachable and the read would fail
	// outright, which is a different failure and already handled.
	sim.mu.Lock()
	sim.silent = true
	sim.mu.Unlock()

	select {
	case <-a.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the watchdog did not notice a silent simulator")
	}
	if err := a.Err(); err == nil || !strings.Contains(err.Error(), "silent") {
		t.Errorf("Err = %v, want something about silence", err)
	}
}

// TestWatchdogToleratesTraffic: an active simulator must never trip it.
func TestWatchdogToleratesTraffic(t *testing.T) {
	sim := newFakeSim(t)
	defer sim.close()
	go sim.run()

	a, err := Connect(context.Background(), testAccount(sim), Options{
		Timeout: 5 * time.Second,
		Idle:    300 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
				ping := &msg.StartPingCheck{}
				sim.send(ping, 0)
			}
		}
	}()
	defer close(stop)

	select {
	case <-a.Done():
		t.Fatalf("the watchdog fired on a busy connection: %v", a.Err())
	case <-time.After(1200 * time.Millisecond):
	}
	if d := a.Idle(); d > 300*time.Millisecond {
		t.Errorf("Idle = %s while traffic was flowing", d)
	}
}

func TestWatchdogCanBeDisabled(t *testing.T) {
	sim := newFakeSim(t)
	defer sim.close()
	go sim.run()

	a, err := Connect(context.Background(), testAccount(sim), Options{
		Timeout: 5 * time.Second,
		Idle:    -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	sim.mu.Lock()
	sim.silent = true
	sim.mu.Unlock()

	select {
	case <-a.Done():
		t.Errorf("session ended with the watchdog disabled: %v", a.Err())
	case <-time.After(600 * time.Millisecond):
	}
}

// TestPresenceIsSent: the simulator streams no object data to a session
// that never sends AgentUpdate, so it must go out without a client
// asking.  Found by rezzing a prim on the live grid and watching the
// simulator never mention it.
func TestPresenceIsSent(t *testing.T) {
	sim := newFakeSim(t)
	defer sim.close()
	go sim.run()

	a, err := Connect(context.Background(), testAccount(sim), Options{
		Timeout:  5 * time.Second,
		Presence: 50 * time.Millisecond,

		DrawDistance: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	deadline := time.Now().Add(5 * time.Second)
	for {
		n := 0
		for _, name := range sim.got() {
			if name == "AgentUpdate" {
				n++
			}
		}
		if n >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("AgentUpdate was not sent; simulator saw %v", sim.got())
		}
		time.Sleep(5 * time.Millisecond)
	}

	if l := a.Look(); l.Far != 64 {
		t.Errorf("draw distance = %v, want 64", l.Far)
	}
	// The camera starts where the avatar arrived.
	if l := a.Look(); l.Center != a.Position() {
		t.Errorf("camera at %v, avatar at %v", l.Center, a.Position())
	}

	// A client can move it.
	a.SetLook(Look{Center: msg.Vector3{X: 9}, At: msg.Vector3{X: 1}, Far: 200})
	if l := a.Look(); l.Center.X != 9 || l.Far != 200 {
		t.Errorf("SetLook did not take: %+v", l)
	}
}

func TestPresenceCanBeDisabled(t *testing.T) {
	sim := newFakeSim(t)
	defer sim.close()
	go sim.run()

	a, err := Connect(context.Background(), testAccount(sim), Options{
		Timeout:  5 * time.Second,
		Presence: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	time.Sleep(300 * time.Millisecond)
	for _, name := range sim.got() {
		if name == "AgentUpdate" {
			t.Fatal("AgentUpdate was sent with presence disabled")
		}
	}
}

// TestErrIsSafeToReadAsTheSessionGoesDown: Err is read the moment Done
// fires -- server.Hosted.supervise does exactly that -- and a session
// that was closed from outside rather than having failed can still be
// setting it at that moment.
//
// Close closes done itself, without an error, and a goroutine still on
// its way down calls fail afterwards.  So the reader woken by done has
// no ordering edge to that write, and while err was a plain field the
// two happened at once.  Whether the race is reported depends on the
// interleaving, so this asks for it repeatedly.
func TestErrIsSafeToReadAsTheSessionGoesDown(t *testing.T) {
	for range 200 {
		a := &Agent{done: make(chan struct{})}

		var wg sync.WaitGroup
		wg.Add(2)
		// What Close does with done, without the rest of Close, which
		// wants a circuit underneath it.
		go func() { defer wg.Done(); a.doneOnce.Do(func() { close(a.done) }) }()
		go func() { defer wg.Done(); a.fail(errors.New("the circuit went away")) }()

		<-a.Done()
		_ = a.Err()
		wg.Wait()

		// Whichever way round they went, the reason survives: fail is
		// the only thing that has one.
		if a.Err() == nil {
			t.Fatal("the session ended with no reason at all")
		}
	}
}
