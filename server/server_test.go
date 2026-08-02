package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"slgo/agent"
	"slgo/client"
	"slgo/msg"
)

// ---------------------------------------------------------------- a sim

// fakeSim answers the handshake and can be told to send anything.
type fakeSim struct {
	conn *net.UDPConn

	mu   sync.Mutex
	seen []string
	peer *net.UDPAddr
	seq  uint32
}

func newSim(t *testing.T) *fakeSim {
	t.Helper()
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	c, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Skipf("no loopback UDP: %v", err)
	}
	s := &fakeSim{conn: c}
	go s.run()
	return s
}

func (f *fakeSim) addr() *net.UDPAddr { return f.conn.LocalAddr().(*net.UDPAddr) }
func (f *fakeSim) close()             { f.conn.Close() }

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
		id, k, err := msg.DecodeID(body)
		if err != nil {
			continue
		}
		name := id.String()
		f.mu.Lock()
		f.seen = append(f.seen, name)
		f.mu.Unlock()

		if h.Reliable() {
			f.sendRaw(msg.IDOf(&msg.PacketAck{}), ackBody(h.Sequence), 0)
		}
		_ = k

		switch name {
		case "UseCircuitCode":
			rh := &msg.RegionHandshake{}
			rh.RegionInfo.SimName = []byte("Testville\x00")
			f.send(rh, msg.FlagReliable)
		case "CompleteAgentMovement":
			amc := &msg.AgentMovementComplete{}
			amc.Data.Position = msg.Vector3{X: 1, Y: 2, Z: 3}
			amc.SimData.ChannelVersion = []byte("Fake Server\x00")
			f.send(amc, msg.FlagReliable)
		case "LogoutRequest":
			f.send(&msg.LogoutReply{}, msg.FlagReliable)
		}
	}
}

func ackBody(seq uint32) []byte {
	m := &msg.PacketAck{Packets: []msg.PacketAck_Packets{{ID: seq}}}
	b, _ := m.Encode()
	return b
}

func (f *fakeSim) send(m msg.Message, flags uint8) {
	b, err := m.Encode()
	if err != nil {
		return
	}
	f.sendRaw(msg.IDOf(m), b, flags)
}

// sendRaw puts a message number and body on the wire, so a test can
// send something the template does not describe.
func (f *fakeSim) sendRaw(id msg.ID, body []byte, flags uint8) {
	f.mu.Lock()
	peer := f.peer
	f.seq++
	seq := f.seq
	f.mu.Unlock()
	if peer == nil {
		return
	}
	out := msg.AppendHeader(nil, &msg.Header{Flags: flags, Sequence: seq})
	out = msg.AppendID(out, id)
	out = append(out, body...)
	f.conn.WriteToUDP(out, peer)
}

// ------------------------------------------------------------ the rig

type rig struct {
	sim  *fakeSim
	srv  *Server
	ln   net.Listener
	stop context.CancelFunc
	done chan struct{}
}

func newRig(t *testing.T, caps agent.Caps) *rig {
	t.Helper()
	sim := newSim(t)

	acct := &agent.Account{
		AgentID:       msg.MustParseUUID("876e7e57-7e57-c0de-9eeb-1bd0e1ec6995"),
		SessionID:     msg.MustParseUUID("8d1b7e57-7e57-c0de-f4f4-19d29d124acf"),
		CircuitCode:   4242,
		SimIP:         sim.addr().IP,
		SimPort:       sim.addr().Port,
		FirstName:     "Example",
		LastName:      "Resident",
		InventoryRoot: msg.MustParseUUID("b7cd7e57-7e57-c0de-2c0a-000000000000"),
	}

	srv := New()
	h := &Hosted{Name: "example", clients: map[*Client]bool{}}
	a, err := agent.Connect(context.Background(), acct, agent.Options{
		Timeout:  10 * time.Second,
		SkipCaps: true,
		Recv:     []msg.ReceiverOption{msg.KeepBody()},
		Tap:      func(p *msg.Packet) { h.relay(p) },
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if caps != nil {
		a.Caps = caps
	}
	h.Agent = a
	srv.mu.Lock()
	srv.agents["example"] = h
	srv.mu.Unlock()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); srv.Serve(ctx, ln) }()

	r := &rig{sim: sim, srv: srv, ln: ln, stop: cancel, done: done}
	t.Cleanup(func() {
		cancel()
		<-done
		a.Close()
		sim.close()
	})
	return r
}

func (r *rig) dial(t *testing.T, subscribe ...string) *client.Conn {
	t.Helper()
	c, err := client.Dial(context.Background(), r.ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Attach(context.Background(), "example", subscribe...); err != nil {
		t.Fatalf("attach: %v", err)
	}
	return c
}

func waitMsg(t *testing.T, c *client.Conn, name string, d time.Duration) *client.Message {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case m, ok := <-c.Messages():
			if !ok {
				t.Fatal("connection closed while waiting for " + name)
			}
			if m.Name == name || name == "" {
				return m
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", name)
		}
	}
}

// ------------------------------------------------------------- tests

func TestAttachAndWelcome(t *testing.T) {
	r := newRig(t, nil)
	c := r.dial(t)
	defer c.Close()

	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Agent.AvatarName != "Example Resident" {
		t.Errorf("name = %q", st.Agent.AvatarName)
	}
	if st.Agent.Region != "Testville" {
		t.Errorf("region = %q", st.Agent.Region)
	}
	if st.Clients != 1 {
		t.Errorf("clients = %d", st.Clients)
	}
	if !st.Agent.Connected {
		t.Error("agent reported as not connected")
	}
}

func TestAttachUnknownAgent(t *testing.T) {
	r := newRig(t, nil)
	c, err := client.Dial(context.Background(), r.ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Attach(context.Background(), "nobody"); err == nil {
		t.Error("expected an error attaching to an agent that is not hosted")
	}
}

func TestAgentsList(t *testing.T) {
	r := newRig(t, nil)
	c := r.dial(t)
	defer c.Close()
	agents, err := c.ListAgents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].Name != "example" {
		t.Errorf("agents = %v", agents)
	}
}

// TestSubscriptionGates: nothing is relayed until it is asked for.
func TestSubscriptionGates(t *testing.T) {
	r := newRig(t, nil)
	c := r.dial(t)
	defer c.Close()

	chat := &msg.ChatFromSimulator{}
	chat.ChatData.Message = []byte("ignored\x00")
	r.sim.send(chat, 0)

	select {
	case m, ok := <-c.Messages():
		if !ok {
			t.Fatalf("stream ended: %v", c.Err())
		}
		t.Fatalf("relayed %s without a subscription", m.Name)
	case <-time.After(200 * time.Millisecond):
	}

	if err := c.Subscribe("ChatFromSimulator"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	chat.ChatData.Message = []byte("wanted\x00")
	r.sim.send(chat, 0)

	m := waitMsg(t, c, "ChatFromSimulator", 5*time.Second)
	got, err := m.Decode()
	if err != nil {
		t.Fatal(err)
	}
	body := got.(*msg.ChatFromSimulator).ChatData.Message
	if strings.TrimRight(string(body), "\x00") != "wanted" {
		t.Errorf("message = %q", body)
	}
}

// TestRelayUnknownMessage is the property the whole design exists for:
// the server passes on a message number it has never heard of, so a
// client can learn new messages without the grid session restarting.
func TestRelayUnknownMessage(t *testing.T) {
	r := newRig(t, nil)
	c := r.dial(t)
	defer c.Close()

	if err := c.Subscribe("*"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	// High 251 is not in the template.
	unknown := msg.MakeID(msg.FreqHigh, 251)
	if msg.Lookup(unknown) != nil {
		t.Skip("High 251 is in the template now; pick another")
	}
	r.sim.sendRaw(unknown, []byte("hello from the future"), 0)

	deadline := time.After(5 * time.Second)
	for {
		select {
		case m := <-c.Messages():
			if m.ID != unknown {
				continue
			}
			if string(m.Body) != "hello from the future" {
				t.Errorf("body = %q", m.Body)
			}
			// It does not decode, and that is fine: the
			// bytes are there for a client that knows how.
			v, err := m.Decode()
			if v != nil || err != nil {
				t.Errorf("Decode = %v, %v; want nil, nil", v, err)
			}
			return
		case <-deadline:
			t.Fatal("an unknown message was not relayed")
		}
	}
}

// TestClientSendsMessage: the client composes, the server frames.
func TestClientSendsMessage(t *testing.T) {
	r := newRig(t, nil)
	c := r.dial(t)
	defer c.Close()

	chat := &msg.ChatFromViewer{}
	chat.ChatData.Message = []byte("hello world\x00")
	if err := c.Send(context.Background(), chat, true); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, n := range r.sim.got() {
			if n == "ChatFromViewer" {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("simulator never saw it: %v", r.sim.got())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestNoClientStillAcks is the stated requirement: with nobody
// attached, inbound messages are acknowledged and dropped.
func TestNoClientStillAcks(t *testing.T) {
	r := newRig(t, nil)

	// Nothing attached at all.
	if n := r.srv.Stats().Clients; n != 0 {
		t.Fatalf("%d clients", n)
	}

	chat := &msg.ChatFromSimulator{}
	chat.ChatData.Message = []byte("into the void\x00")
	r.sim.send(chat, msg.FlagReliable)

	h, _ := r.srv.Agent("example")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if h.Agent.Send.Stats().AcksQueued > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a reliable message went unacknowledged with no client attached")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestClientRestartLeavesAgentUp is the reason for the split.
func TestClientRestartLeavesAgentUp(t *testing.T) {
	r := newRig(t, nil)
	h, _ := r.srv.Agent("example")

	before := h.Agent.Recv.Stats().Packets
	region := h.Agent.RegionName()

	for i := 0; i < 5; i++ {
		c := r.dial(t)
		if err := c.Subscribe("ChatFromSimulator"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)

		chat := &msg.ChatFromSimulator{}
		chat.ChatData.Message = []byte(fmt.Sprintf("round %d\x00", i))
		r.sim.send(chat, 0)
		waitMsg(t, c, "ChatFromSimulator", 5*time.Second)
		c.Close()

		// The client is gone; the agent must not be.
		select {
		case <-h.Agent.Done():
			t.Fatalf("the agent ended when client %d disconnected: %v", i, h.Agent.Err())
		default:
		}
	}

	if h.Agent.RegionName() != region {
		t.Error("the agent lost its region across client restarts")
	}
	if h.Agent.Recv.Stats().Packets <= before {
		t.Error("the agent stopped receiving")
	}
	deadline := time.Now().Add(2 * time.Second)
	for h.ClientCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if h.ClientCount() != 0 {
		t.Errorf("%d clients still attached after all disconnected", h.ClientCount())
	}
}

// TestTwoClientsOneAgent: both see what they asked for, and only that.
func TestTwoClientsOneAgent(t *testing.T) {
	r := newRig(t, nil)
	a := r.dial(t)
	defer a.Close()
	b := r.dial(t)
	defer b.Close()

	if err := a.Subscribe("ChatFromSimulator"); err != nil {
		t.Fatal(err)
	}
	if err := b.Subscribe("AgentDataUpdate"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	chat := &msg.ChatFromSimulator{}
	chat.ChatData.Message = []byte("for a only\x00")
	r.sim.send(chat, 0)

	waitMsg(t, a, "ChatFromSimulator", 5*time.Second)
	select {
	case m, ok := <-b.Messages():
		if !ok {
			t.Fatalf("b's stream ended: %v", b.Err())
		}
		t.Errorf("b received %s, which it did not subscribe to", m.Name)
	case <-time.After(200 * time.Millisecond):
	}

	h, _ := r.srv.Agent("example")
	if h.ClientCount() != 2 {
		t.Errorf("%d clients attached", h.ClientCount())
	}
}

// TestCapProxy: the server makes the request, knowing nothing about it.
func TestCapProxy(t *testing.T) {
	var gotPath, gotMethod, gotBody string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotPath, gotMethod = req.URL.Path, req.Method
		b := make([]byte, req.ContentLength)
		req.Body.Read(b)
		gotBody = string(b)
		w.WriteHeader(201)
		fmt.Fprint(w, "the response")
	}))
	defer hs.Close()

	r := newRig(t, agent.Caps{"SomethingNew": hs.URL + "/base"})
	c := r.dial(t)
	defer c.Close()

	if !c.HasCap("SomethingNew") {
		t.Fatalf("capability not advertised: %v", c.Caps())
	}
	resp, err := c.DoCap(context.Background(), agent.CapRequest{
		Cap: "SomethingNew", Method: "POST", Path: "/thing",
		Type: "text/plain", Body: []byte("a request"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 201 || string(resp.Body) != "the response" {
		t.Errorf("status %d body %q", resp.Status, resp.Body)
	}
	if gotMethod != "POST" || gotPath != "/base/thing" || gotBody != "a request" {
		t.Errorf("server sent %s %s %q", gotMethod, gotPath, gotBody)
	}
}

func TestCapProxyUnknownCapability(t *testing.T) {
	r := newRig(t, agent.Caps{})
	c := r.dial(t)
	defer c.Close()
	if _, err := c.DoCap(context.Background(), agent.CapRequest{Cap: "Nope"}); err == nil {
		t.Error("expected an error for a capability the agent does not have")
	}
}

// TestInventoryOverTheLink is the point of making the server a gateway.
//
// agent.FetchInventory is given a client.Conn instead of an Agent.  The
// identical code walks the tree, the server makes the HTTPS requests
// without knowing what any of them mean, and the inventory ends up in
// the client.  Nothing about inventory exists in package server.
func TestInventoryOverTheLink(t *testing.T) {
	root := "b7cd7e57-7e57-c0de-2c0a-000000000000"
	kid := "b7cd7e57-7e57-c0de-2c0a-000000000001"
	item := "b7cd7e57-7e57-c0de-2c0a-000000000002"

	var requests int
	var mu sync.Mutex
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()

		id := root
		if strings.Contains(req.URL.Path, kid) {
			id = kid
		}
		fmt.Fprintf(w, `<llsd><map>
		  <key>category_id</key><string>%s</string>
		  <key>parent_id</key><string>%s</string>
		  <key>name</key><string>%s</string>
		  <key>version</key><integer>1</integer>
		  <key>_embedded</key><map>
		    <key>categories</key><map>%s</map>
		    <key>items</key><map>%s</map>
		    <key>links</key><map/>
		  </map></map></llsd>`,
			id, root, map[string]string{root: "My Inventory", kid: "Objects"}[id],
			ifElse(id == root, fmt.Sprintf(`<key>%s</key><map>
			    <key>category_id</key><string>%s</string>
			    <key>parent_id</key><string>%s</string>
			    <key>name</key><string>Objects</string></map>`, kid, kid, root), ""),
			ifElse(id == kid, fmt.Sprintf(`<key>%s</key><map>
			    <key>item_id</key><string>%s</string>
			    <key>parent_id</key><string>%s</string>
			    <key>name</key><string>a notecard</string>
			    <key>type</key><integer>7</integer></map>`, item, item, kid), ""))
	}))
	defer hs.Close()

	r := newRig(t, agent.Caps{agent.InventoryCap: hs.URL + "/cap"})
	c := r.dial(t)
	defer c.Close()

	inv := agent.NewInventory(msg.MustParseUUID(root))
	if err := agent.FetchInventory(context.Background(), c, inv, agent.FetchOptions{}); err != nil {
		t.Fatal(err)
	}

	folders, items := inv.Counts()
	if folders != 2 || items != 1 {
		t.Fatalf("counts = %d folders, %d items; want 2 and 1", folders, items)
	}
	if got := inv.Path(msg.MustParseUUID(kid)); got != "My Inventory/Objects" {
		t.Errorf("path = %q", got)
	}
	its := inv.Contents(msg.MustParseUUID(kid))
	if len(its) != 1 || its[0].Name != "a notecard" {
		t.Errorf("contents = %v", its)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 2 {
		t.Errorf("%d capability requests, want one per folder", requests)
	}
}

func ifElse(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
