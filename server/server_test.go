package server

import (
	"context"
	"fmt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
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

// newRig brings a session up and serves it, without authentication.
func newRig(t *testing.T, caps agent.Caps) *rig {
	t.Helper()
	r := newSession(t, caps)
	r.serve(t)
	return r
}

// newSession is newRig without the listener, for the tests that have to
// settle something on the server -- whether it authenticates, above all
// -- before anything may be listening.  Serve reads those fields, so
// setting one under a server already serving is a race whatever it
// says.
func newSession(t *testing.T, caps agent.Caps) *rig {
	t.Helper()
	return newSessionWith(t, caps, 0)
}

// newSessionWith is newSession with an idle timeout, for the tests
// about a session that ends on its own.  There is a real difference:
// a session that FAILS records why and then says it has ended, while
// one that is closed from outside says it has ended straight away, so
// only the first can have the reason read off it by anything watching.
func newSessionWith(t *testing.T, caps agent.Caps, idle time.Duration) *rig {
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
		Idle:     idle,
		Recv:     []msg.ReceiverOption{msg.KeepBody()},
		Tap:      func(p *msg.Packet) { h.relay(p) },
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if caps != nil {
		a.Caps = caps
	}
	h.setAgent(a)
	srv.mu.Lock()
	srv.agents["example"] = h
	srv.mu.Unlock()

	t.Cleanup(func() {
		a.Close()
		sim.close()
	})
	return &rig{sim: sim, srv: srv}
}

// serve puts the session's server on loopback and takes it down again
// when the test ends.
func (r *rig) serve(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.srv.Serve(ctx, ln) }()

	r.ln, r.stop, r.done = ln, cancel, done
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func (r *rig) dial(t *testing.T, subscribe ...string) *client.Conn {
	t.Helper()
	c, err := client.Dial(context.Background(), r.ln.Addr().String(), plaintext())
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
	c, err := client.Dial(context.Background(), r.ln.Addr().String(), plaintext())
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
		if h.Agent().Send.Stats().AcksQueued > 0 {
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

	before := h.Agent().Recv.Stats().Packets
	region := h.Agent().RegionName()

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
		case <-h.Agent().Done():
			t.Fatalf("the agent ended when client %d disconnected: %v", i, h.Agent().Err())
		default:
		}
	}

	if h.Agent().RegionName() != region {
		t.Error("the agent lost its region across client restarts")
	}
	if h.Agent().Recv.Stats().Packets <= before {
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

// TestReconnect is the other half of leaving a server running: when the
// session dies the server must get it back, and clients must keep their
// streams and their subscriptions across it.
func TestReconnect(t *testing.T) {
	sim := newSim(t)
	defer sim.close()

	// A login server that always answers, pointing at the sim.
	var logins atomic.Int64
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logins.Add(1)
		fmt.Fprintf(w, `<?xml version="1.0"?><methodResponse><params><param><value><struct>
		  <member><name>login</name><value><string>true</string></value></member>
		  <member><name>agent_id</name><value><string>876e7e57-7e57-c0de-9eeb-1bd0e1ec6995</string></value></member>
		  <member><name>session_id</name><value><string>8d1b7e57-7e57-c0de-f4f4-19d29d124acf</string></value></member>
		  <member><name>secure_session_id</name><value><string>95507e57-7e57-c0de-d169-d9847afe641e</string></value></member>
		  <member><name>circuit_code</name><value><int>%d</int></value></member>
		  <member><name>sim_ip</name><value><string>%s</string></value></member>
		  <member><name>sim_port</name><value><int>%d</int></value></member>
		  <member><name>first_name</name><value><string>"Example"</string></value></member>
		  <member><name>last_name</name><value><string>Resident</string></value></member>
		</struct></value></param></params></methodResponse>`,
			4000+logins.Load(), sim.addr().IP, sim.addr().Port)
	}))
	defer hs.Close()

	// Retry fast, so the test does not take a minute.
	saved := ReconnectDelays
	ReconnectDelays = []time.Duration{20 * time.Millisecond}
	defer func() { ReconnectDelays = saved }()

	srv := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h, err := srv.StartAgent(ctx, "example",
		agent.Login{First: "Example", Last: "Resident", Password: "x", URL: hs.URL},
		agent.Options{Timeout: 10 * time.Second, SkipCaps: true, Idle: -1})
	if err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); srv.Serve(ctx, ln) }()
	defer func() { cancel(); <-done }()

	c, err := client.Dial(context.Background(), ln.Addr().String(), plaintext())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Attach(context.Background(), "example", "ChatFromSimulator"); err != nil {
		t.Fatal(err)
	}

	first := h.Agent()

	// The session dies the way a lost circuit does.
	first.Close()

	// The client should hear about it...
	select {
	case ev := <-c.Notices():
		if ev.Kind != pb.AgentEvent_DISCONNECTED {
			t.Errorf("first event was %v", ev.Kind)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the client was not told the session ended")
	}

	// ... and the server should get it back.
	deadline := time.Now().Add(10 * time.Second)
	for h.Agent() == first || h.Agent() == nil {
		if time.Now().After(deadline) {
			t.Fatal("the session was not re-established")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if logins.Load() < 2 {
		t.Errorf("%d logins, expected a second", logins.Load())
	}
	if st := srv.Stats(); st.Reconnects != 1 {
		t.Errorf("stats = %+v", st)
	}

	// The client kept its stream and its subscription: a message on
	// the new circuit still reaches it.
	deadline = time.Now().Add(5 * time.Second)
	for {
		chat := &msg.ChatFromSimulator{}
		chat.ChatData.Message = []byte("after the reconnect\x00")
		sim.send(chat, 0)

		select {
		case m, ok := <-c.Messages():
			if !ok {
				t.Fatalf("stream ended: %v", c.Err())
			}
			if m.Name == "ChatFromSimulator" {
				return
			}
		case <-time.After(200 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatal("the client's subscription did not survive the reconnect")
		}
	}
}

// TestNoReconnectAfterLogout: a deliberate shutdown must not be treated
// as a failure to recover from.
func TestNoReconnectAfterLogout(t *testing.T) {
	sim := newSim(t)
	defer sim.close()

	var logins atomic.Int64
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logins.Add(1)
		fmt.Fprintf(w, `<?xml version="1.0"?><methodResponse><params><param><value><struct>
		  <member><name>login</name><value><string>true</string></value></member>
		  <member><name>agent_id</name><value><string>876e7e57-7e57-c0de-9eeb-1bd0e1ec6995</string></value></member>
		  <member><name>session_id</name><value><string>8d1b7e57-7e57-c0de-f4f4-19d29d124acf</string></value></member>
		  <member><name>secure_session_id</name><value><string>95507e57-7e57-c0de-d169-d9847afe641e</string></value></member>
		  <member><name>circuit_code</name><value><int>77</int></value></member>
		  <member><name>sim_ip</name><value><string>%s</string></value></member>
		  <member><name>sim_port</name><value><int>%d</int></value></member>
		  <member><name>first_name</name><value><string>Example</string></value></member>
		  <member><name>last_name</name><value><string>Resident</string></value></member>
		</struct></value></param></params></methodResponse>`, sim.addr().IP, sim.addr().Port)
	}))
	defer hs.Close()

	saved := ReconnectDelays
	ReconnectDelays = []time.Duration{20 * time.Millisecond}
	defer func() { ReconnectDelays = saved }()

	srv := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := srv.StartAgent(ctx, "example",
		agent.Login{First: "Example", Last: "Resident", Password: "x", URL: hs.URL},
		agent.Options{Timeout: 10 * time.Second, SkipCaps: true, Idle: -1}); err != nil {
		t.Fatal(err)
	}

	srv.Close(context.Background())
	time.Sleep(300 * time.Millisecond)

	if n := logins.Load(); n != 1 {
		t.Errorf("%d logins after a deliberate shutdown, want 1", n)
	}
}

// ------------------------------------------------- holding sessions

// TestHostingTheSameNameTwiceIsRefused: a second login to the same
// account makes the grid kick the first, so a name that is taken has to
// be refused rather than raced for.
func TestHostingTheSameNameTwiceIsRefused(t *testing.T) {
	t.Parallel()

	srv := New()
	if _, err := srv.Add("example", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Add("example", nil); err == nil {
		t.Error("the same name was hosted twice")
	}
	if _, err := srv.StartAgent(context.Background(), "example",
		agent.Login{}, agent.Options{}); err == nil {
		t.Error("a login was attempted for a name already hosted")
	}

	// And removing something that was never there is not an error to
	// report, it is nothing to do.
	if _, ok := srv.Remove("qi"); ok {
		t.Error("removing a name nobody hosts reported success")
	}
}

// TestASessionWithNoLifetimeGetsOne: a server nobody told about the
// daemon's lifetime still has to give a new session one, or a login
// started on request would belong to nothing at all.
func TestASessionWithNoLifetimeGetsOne(t *testing.T) {
	t.Parallel()

	if (&Server{}).base() == nil {
		t.Error("a server with no base context answered with nothing")
	}
}

// TestALoginThatCannotReachTheCircuitIsNotHosted: the account came back
// and the simulator did not, which must leave the name free rather than
// reserved by a session that never existed.
func TestALoginThatCannotReachTheCircuitIsNotHosted(t *testing.T) {
	t.Parallel()

	// A login server that answers, pointing at a port nothing is on.
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0"?><methodResponse><params><param><value><struct>
		  <member><name>login</name><value><string>true</string></value></member>
		  <member><name>agent_id</name><value><string>876e7e57-7e57-c0de-9eeb-1bd0e1ec6995</string></value></member>
		  <member><name>session_id</name><value><string>8d1b7e57-7e57-c0de-f4f4-19d29d124acf</string></value></member>
		  <member><name>secure_session_id</name><value><string>95507e57-7e57-c0de-d169-d9847afe641e</string></value></member>
		  <member><name>circuit_code</name><value><int>4242</int></value></member>
		  <member><name>sim_ip</name><value><string>127.0.0.1</string></value></member>
		  <member><name>sim_port</name><value><int>1</int></value></member>
		  <member><name>first_name</name><value><string>Example</string></value></member>
		  <member><name>last_name</name><value><string>Resident</string></value></member>
		</struct></value></param></params></methodResponse>`)
	}))
	defer hs.Close()

	srv := New()
	_, err := srv.StartAgent(context.Background(), "example",
		agent.Login{First: "Example", Last: "Resident", Password: "x", URL: hs.URL},
		agent.Options{Timeout: 300 * time.Millisecond, SkipCaps: true, Idle: -1})
	if err == nil {
		t.Fatal("a session with no simulator behind it was hosted")
	}
	if _, ok := srv.Agent("example"); ok {
		t.Error("the name is still reserved by a session that never came up")
	}
	if names := srv.Names(); len(names) != 0 {
		t.Errorf("Names() = %v after a failed login", names)
	}
}

// TestStoppedSessionsSortLast: the head of Ranked means "the default",
// so a session that gave up its rank must not sort to the front of a
// list whose front has that meaning.
func TestStoppedSessionsSortLast(t *testing.T) {
	t.Parallel()

	srv := New()
	ctx := context.Background()
	for _, name := range []string{"example", "qi", "helper", "spare"} {
		if _, err := srv.Add(name, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := srv.Names(); len(got) != 4 || got[0] != "example" {
		t.Errorf("Names() = %v, want all four sorted", got)
	}

	// Two of them give up their place, which is what logging out does.
	for _, name := range []string{"example", "qi"} {
		if _, err := srv.Logout(ctx, &pb.LogoutRequest{Agent: name}); err != nil {
			t.Fatal(err)
		}
	}

	var order []string
	for _, h := range srv.Ranked() {
		order = append(order, h.Name)
	}
	// The two still running keep the order they came up in; the two
	// that stopped follow in name order, having no rank to sort by.
	want := []string{"helper", "spare", "example", "qi"}
	for i := range want {
		if i >= len(order) || order[i] != want[i] {
			t.Fatalf("Ranked() = %v, want %v", order, want)
		}
	}
}

// TestStartAgentWiresTheRelayIntoTheSession: the session is told to
// hand everything to the Hosted, which is what makes a client able to
// see a message -- or an event -- the server itself knows nothing
// about.
func TestStartAgentWiresTheRelayIntoTheSession(t *testing.T) {
	sim := newSim(t)
	defer sim.close()
	var logins atomic.Int64
	hs := loginServer(t, sim, &logins, nil)

	srv := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, err := srv.StartAgent(ctx, "example",
		agent.Login{First: "Example", Last: "Resident", Password: "x", URL: hs.URL},
		agent.Options{Timeout: 10 * time.Second, SkipCaps: true, Idle: -1})
	if err != nil {
		t.Fatal(err)
	}
	if h.opts.Tap == nil || h.opts.OnEvent == nil {
		t.Fatal("the session was not told where to send what it receives")
	}
	// Both go to this Hosted, which is what a reconnect keeps and the
	// agent underneath does not.
	h.opts.OnEvent("TeleportFinish", []byte("<llsd><map/></llsd>"))
	h.opts.Tap(&msg.Packet{})
}

// TestStartAgentKeepsTheCallersTap: the relay needs the tap and so does
// anything the caller wanted it for -- slgod's packet trace, in
// particular.  Overwriting it instead of chaining would drop the
// caller's hook in silence, which presents as "the trace is empty" and
// sends the search to entirely the wrong place.
func TestStartAgentKeepsTheCallersTap(t *testing.T) {
	sim := newSim(t)
	defer sim.close()
	var logins atomic.Int64
	hs := loginServer(t, sim, &logins, nil)

	var mine atomic.Int64
	srv := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, err := srv.StartAgent(ctx, "example",
		agent.Login{First: "Example", Last: "Resident", Password: "x", URL: hs.URL},
		agent.Options{
			Timeout: 10 * time.Second, SkipCaps: true, Idle: -1,
			Tap: func(*msg.Packet) { mine.Add(1) },
		})
	if err != nil {
		t.Fatal(err)
	}
	if h.opts.Tap == nil {
		t.Fatal("no tap at all")
	}
	// The session is live and has already pushed its handshake through
	// the tap, so the count is whatever it is; what matters is that
	// one more packet reaches the caller's hook and not only the
	// relay's.
	before := mine.Load()
	h.opts.Tap(&msg.Packet{})
	if got := mine.Load() - before; got != 1 {
		t.Errorf("the caller's tap saw %d of the packet, want 1", got)
	}
	if before == 0 {
		t.Error("the caller's tap saw none of the session's own traffic")
	}
}

// TestSupervisingASessionThatIsNotThere: nothing to watch is not a
// fault, it is a Hosted whose login never produced one.
func TestSupervisingASessionThatIsNotThere(t *testing.T) {
	t.Parallel()

	done := make(chan struct{})
	go func() { defer close(done); (&Hosted{}).supervise(context.Background()) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("supervising a session with no agent did not return")
	}
}

// TestAFailedReconnectIsSaidAndTriedAgain: the login server being down
// is the ordinary case -- it is usually why the session went -- so an
// attempt that fails must be reported to whoever is attached and
// followed by another, not treated as the end of the session.
func TestAFailedReconnectIsSaidAndTriedAgain(t *testing.T) {
	sim := newSim(t)
	defer sim.close()

	var logins atomic.Int64
	var refuse atomic.Bool
	hs := loginServer(t, sim, &logins, &refuse)

	saved := ReconnectDelays
	ReconnectDelays = []time.Duration{20 * time.Millisecond}
	defer func() { ReconnectDelays = saved }()

	srv := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, err := srv.StartAgent(ctx, "example",
		agent.Login{First: "Example", Last: "Resident", Password: "x", URL: hs.URL},
		agent.Options{Timeout: 10 * time.Second, SkipCaps: true, Idle: -1})
	if err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serving := make(chan struct{})
	go func() { defer close(serving); srv.Serve(ctx, ln) }()

	c, err := client.Dial(context.Background(), ln.Addr().String(), plaintext())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Attach(context.Background(), "example"); err != nil {
		t.Fatal(err)
	}

	// The login server refuses from here on, and the session dies.
	refuse.Store(true)
	first := h.Agent()
	first.Close()

	// The client is told the attempts are failing, and told which
	// attempt it was -- one notice with no number would look like the
	// session had gone for good.
	deadline := time.After(10 * time.Second)
	sawAttempt := false
	for !sawAttempt {
		select {
		case ev := <-c.Notices():
			if strings.Contains(ev.GetDetail(), "reconnect attempt") {
				sawAttempt = true
			}
		case <-deadline:
			t.Fatal("no failed reconnect was reported to the client")
		}
	}
	if h.attempts.Load() == 0 {
		t.Error("a failed attempt was not counted")
	}

	// And when the login server comes back, so does the session, which
	// the client is told about as well: the avatar is the same one and
	// the session is not, so anything holding a session id or a
	// capability URL has to ask again.
	refuse.Store(false)
	deadline = time.After(20 * time.Second)
	back := false
	for !back {
		select {
		case ev := <-c.Notices():
			back = strings.Contains(ev.GetDetail(), "session re-established")
		case <-deadline:
			t.Fatal("the client was not told the session came back")
		}
	}
	if a := h.Agent(); a == nil || a == first {
		t.Error("the session was reported as re-established and is not")
	}
	if st := srv.Stats(); st.ReconnectAt <= st.Reconnects {
		t.Errorf("stats = %+v; attempts should exceed the successes", st)
	}

	// Hung up on in this order on purpose: the daemon goes second
	// because a graceful stop waits for the streams its clients still
	// hold.
	c.Close()
	cancel()
	<-serving
}

// TestAShutdownDuringTheReconnectWaitIsHonoured: the waits between
// attempts are minutes long, so the daemon exiting -- or the session
// being told to stay down -- has to be noticed during one rather than
// after it.
func TestAShutdownDuringTheReconnectWaitIsHonoured(t *testing.T) {
	for _, tc := range []struct {
		name  string
		delay time.Duration
		// stop ends the wait, either by taking the daemon down or by
		// marking the session as one that should stay down.
		stop func(h *Hosted, cancel context.CancelFunc)
	}{
		{
			// Noticed at once: the daemon exiting must not wait out a
			// two minute backoff before the process can end.
			name:  "the daemon is going down",
			delay: 30 * time.Second,
			stop:  func(_ *Hosted, cancel context.CancelFunc) { cancel() },
		},
		{
			// Noticed when the wait ends, which is enough: nothing has
			// been done to the grid in the meantime.
			name:  "the session was told to stay down",
			delay: 300 * time.Millisecond,
			stop:  func(h *Hosted, _ context.CancelFunc) { h.stopped.Store(true) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The supervisor is run here rather than by StartAgent, so
			// that the test can WAIT for it to have finished.  Without
			// that there is no moment at which putting the delays back
			// is safe: they are a package variable and the supervisor
			// reads them every time round.
			//
			// The session is left to die of a simulator that says
			// nothing rather than closed from outside, because that is
			// the ending a supervisor can read the reason off.
			r := newSessionWith(t, agent.Caps{}, 200*time.Millisecond)
			h, _ := r.srv.Agent("example")

			saved := ReconnectDelays
			ReconnectDelays = []time.Duration{tc.delay}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			supervising := make(chan struct{})
			first := h.Agent()
			go func() { defer close(supervising); h.supervise(ctx) }()

			select {
			case <-first.Done():
			case <-time.After(10 * time.Second):
				t.Fatal("the session outlived a simulator that said nothing")
			}
			// Long enough to be inside the wait, short enough to be
			// well before it ends.
			time.Sleep(100 * time.Millisecond)
			tc.stop(h, cancel)

			select {
			case <-supervising:
			case <-time.After(10 * time.Second):
				t.Fatal("the supervisor sat out its wait to the end")
			}
			ReconnectDelays = saved

			if n := h.attempts.Load(); n != 0 {
				t.Errorf("%d reconnect attempt(s) were made after the wait should have been abandoned", n)
			}
			if a := h.Agent(); a != first {
				t.Error("the session was re-established anyway")
			}
		})
	}
}

// TestASessionThatEndedSaysWhyItCannotBeUsed: a client handed a session
// that is down would talk to something answering out of what it last
// heard and sending into nothing, so the refusal has to carry the
// reason -- and the reason is usually only in the error the circuit
// died of.
func TestASessionThatEndedSaysWhyItCannotBeUsed(t *testing.T) {
	r := newRig(t, agent.Caps{})
	h, _ := r.srv.Agent("example")

	if why := h.Down(); why != "" {
		t.Errorf("a running session says it is down: %q", why)
	}

	// Stopped first, so the supervisor reads this as deliberate, which
	// is what Close on a hosted session means.
	h.stopped.Store(true)
	h.Agent().Close()
	if why := h.Down(); why == "" {
		t.Error("a stopped session gives no reason at all")
	}

	// One that was stopped without any session under it at all still
	// answers, because "logged out" is the reason then.
	empty := &Hosted{Name: "example"}
	empty.stopped.Store(true)
	if why := empty.Down(); why != "logged out" {
		t.Errorf("Down() = %q for a session with no agent, want logged out", why)
	}

	// And a session that fell over for a reason keeps the reason: a
	// simulator that went quiet is a different problem from a logout,
	// and the difference is only in the error.
	var logins atomic.Int64
	hs := loginServer(t, r.sim, &logins, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	quiet, err := New().StartAgent(ctx, "example",
		agent.Login{First: "Example", Last: "Resident", Password: "x", URL: hs.URL},
		// The simulator answers the handshake and then says nothing,
		// which is what a circuit going away looks like from here.
		agent.Options{Timeout: 10 * time.Second, SkipCaps: true, Idle: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	quiet.stopped.Store(true)
	// Waited for rather than polled: the session sets its error and
	// then closes this channel, so receiving from it is what makes the
	// error safe to read from another goroutine.
	select {
	case <-quiet.Agent().Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the session outlived a simulator that said nothing")
	}
	if why := quiet.Down(); !strings.Contains(why, "silent") {
		t.Errorf("Down() = %q, want the error the circuit died of", why)
	}
}

// TestServeReportsAListenerThatFails: a listener that cannot be
// accepted on must come back as an error, promptly.
//
// It used not to.  Serve waited for the goroutine that stops the gRPC
// server before looking at the error, and that goroutine waited for the
// context -- so Serve blocked for ever on a listener that had already
// given up, and cmd/slgod, which calls it in a goroutine meaning to log
// the failure, never got the chance.
func TestServeReportsAListenerThatFails(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	got := make(chan error, 1)
	go func() { got <- New().Serve(ctx, ln) }()
	select {
	case err := <-got:
		if err == nil {
			t.Error("a listener that cannot accept was reported as a clean stop")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve never returned from a listener that cannot accept")
	}
}

// plaintext dials without TLS or authentication.
//
// Dial authenticates by default, which is right for a client reaching a
// slgod that might be on another machine and wrong for a test that
// brought the server up in this process a microsecond ago. Passing any
// dial option is the documented way to say so.
func plaintext() grpc.DialOption {
	return grpc.WithTransportCredentials(insecure.NewCredentials())
}
